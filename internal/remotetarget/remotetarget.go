// Package remotetarget keeps the remote targets: Linux servers ABCP reaches over
// SSH (Repo C design note BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md, BF-14.1;
// docs/plans/remote-targets.md).
//
// For each target ABCP makes an Ed25519 key pair; only the public key leaves
// ABCP, for the server's owner to add to the deploy login's authorized_keys.
// The first connection only reads the server's host key and stops before
// signing in; once the owner confirms that fingerprint it is pinned, and a
// server presenting any other key is refused before anything runs. A preflight
// runs one fixed, read-only script and reports what it found. Nothing here
// changes a server, and nothing from a request ever becomes part of a command.
package remotetarget

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/strictjson"
)

// MaxTargets bounds the targets one ABCP keeps: 200 since BF-14 was offered to everyone (two servers of their own per
// person while invite-only, and ours).
const MaxTargets = 200

// DefaultUser is the deploy login a target uses unless it names another.
const DefaultUser = "autobuild"

const (
	stateNew       = "new"
	statePending   = "fingerprint_pending"
	stateConfirmed = "confirmed"
	recordSchema   = 1
	maxOutput      = 64 << 10
	dialTimeout    = 10 * time.Second
	runTimeout     = 30 * time.Second
)

var (
	keyPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	userPattern    = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	hostPattern    = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62}\.)*[A-Za-z0-9-]{1,63}$`)
	errHostKeySeen = errors.New("host key read")
	errKeyChanged  = errors.New("host key changed")
)

// PreflightScript is the only thing a preflight runs on a server, through
// `sh -s`. It reads; it changes nothing.
const PreflightScript = `set +e
[ -S "/run/user/$(id -u)/docker.sock" ] && export DOCKER_HOST="unix:///run/user/$(id -u)/docker.sock"
echo "uid=$(id -u)"
echo "arch=$(uname -m)"
if [ -r /etc/os-release ]; then . /etc/os-release; echo "os_id=$ID"; echo "os_version=$VERSION_ID"; echo "os_name=$PRETTY_NAME"; fi
echo "cpus=$(nproc 2>/dev/null)"
echo "mem_kb=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo 2>/dev/null)"
echo "disk_free_kb=$(df -Pk / 2>/dev/null | awk 'NR==2 {print $4}')"
if command -v docker >/dev/null 2>&1; then
  echo "docker=$(docker version --format '{{.Server.Version}}' 2>/dev/null)"
  echo "docker_security=$(docker info --format '{{join .SecurityOptions ","}}' 2>/dev/null)"
  echo "docker_runtimes=$(docker info --format '{{range $name, $value := .Runtimes}}{{$name}} {{end}}' 2>/dev/null)"
  echo "frontdoor=$(docker ps -q --filter name=^autobuild-frontdoor$ --filter status=running 2>/dev/null | head -n 1)"
fi
if command -v runsc >/dev/null 2>&1; then echo "runsc=$(runsc --version 2>/dev/null | head -n 1)"; fi
echo "listening=$(ss -Htln 2>/dev/null | awk '{print $4}' | tr '\n' ' ')"
echo "ntp=$(timedatectl show -p NTPSynchronized --value 2>/dev/null)"
if command -v curl >/dev/null 2>&1; then echo "registry=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 https://ghcr.io/v2/ 2>/dev/null)"; fi
`

// record is one target as stored in <dir>/<key>.json (mode 0600). The private
// key is beside it in <dir>/<key>.key (mode 0600).
type record struct {
	SchemaVersion int                           `json:"schema_version"`
	Key           string                        `json:"key"`
	Address       string                        `json:"address"`
	Port          int                           `json:"port"`
	User          string                        `json:"user"`
	PublicKey     string                        `json:"public_key"`
	State         string                        `json:"state"`
	HostKey       string                        `json:"host_key,omitempty"`
	Fingerprint   string                        `json:"fingerprint,omitempty"`
	CreatedAt     string                        `json:"created_at"`
	Preflight     *serviceapi.TargetPreflightV1 `json:"preflight,omitempty"`
	// Sites ABCP put on the target, and the releases whose files are there (BF-14.2a).
	Sites    []siteRecord `json:"sites,omitempty"`
	Releases []string     `json:"releases,omitempty"`
	// Node apps' databases, kept after an app is removed (BF-14.2b).
	Databases []databaseRecord `json:"databases,omitempty"`
	// The last check of the target against this record (BF-14.5a).
	Drift *serviceapi.DriftV1 `json:"drift,omitempty"`
	// Each backup is also copied off the target, through a one-object upload address (offsite.go, BF-14.4d d1).
	BackupsOffServer bool `json:"backups_off_server,omitempty"`
	// Where a bought server is, which picks its legal area's bucket (areas.go, R2); empty for one bought before R2.
	Provider string `json:"provider,omitempty"`
	Region   string `json:"region,omitempty"`
}

// Manager keeps the targets in one private folder.
type Manager struct {
	dir     string
	now     func() time.Time
	mu      sync.Mutex
	resolve func(context.Context, string) ([]net.IP, error)
	allow   func(net.IP) bool
	dial    func(context.Context, string, string) (net.Conn, error)

	// Deployments (deploy.go, releases.go).
	releaseMu  sync.Mutex
	busy       map[string]bool
	wg         sync.WaitGroup
	remote     func(context.Context, record) (remote, string)
	fetch      func(context.Context, net.IP, string, string) (string, error)
	checkEvery time.Duration
	checkFor   time.Duration
	healthWait int

	// Backups copied off the server (offsite.go): the instance metadata address and, for tests, an S3 address in
	// place of the bucket's own.
	metadataURL    string
	s3Endpoint     string
	bucketEndpoint func(bucket, region string) string
	// uploadsFile is the hosted backups' settings (bucket and key), whose key signs the upload addresses of the targets
	// whose backups are copied off them; empty, none is copied.
	uploadsFile string
	// areasFile is the server backups' settings (areas.go): each legal area's bucket and each region's legal area.
	areasFile string

	// relayHost is where sites' front-door blocks send Autobuild's paths
	// (BF-08a); empty, they send nothing on.
	relayHost string

	// pageFiles are the page files of each pages operation not yet run
	// (pages.go), by operation id: held in memory alone, never recorded.
	pageFiles map[string][]serviceapi.PageFile
}

// New keeps targets in dir, a folder open to the controller's user alone.
func New(dir string, clock func() time.Time) (*Manager, error) {
	if err := privateDirectory(dir); err != nil {
		return nil, fmt.Errorf("targets folder %s: %w", dir, err)
	}
	if clock == nil {
		clock = time.Now
	}
	dialer := &net.Dialer{Timeout: dialTimeout}
	m := &Manager{
		dir: dir, now: clock,
		resolve: func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		},
		allow: PublicAddress,
		dial:  dialer.DialContext,
		busy:  map[string]bool{}, fetch: httpsFetch, checkEvery: defaultCheckEvery, checkFor: checkWindow, healthWait: defaultHealthWait,
		metadataURL: "http://169.254.169.254",
	}
	m.interrupted()
	return m, nil
}

var refusedNetworks = func() []*net.IPNet {
	var nets []*net.IPNet
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "64:ff9b::/96", "100::/64", "2001:db8::/32"} {
		_, n, _ := net.ParseCIDR(cidr)
		nets = append(nets, n)
	}
	return nets
}()

// PublicAddress is true for an address on the public internet: never
// loopback, private, link-local, shared (CGNAT), documentation, multicast or
// unspecified ranges, so a target can never point inside our own network.
func PublicAddress(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, n := range refusedNetworks {
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

// addresses resolves a target's address and refuses it unless every address is public.
func (m *Manager) addresses(ctx context.Context, address string) ([]net.IP, error) {
	var ips []net.IP
	if ip := net.ParseIP(address); ip != nil {
		ips = []net.IP{ip}
	} else {
		resolved, err := m.resolve(ctx, address)
		if err != nil || len(resolved) == 0 {
			return nil, serviceapi.ErrTargetAddressRefused
		}
		ips = resolved
	}
	for _, ip := range ips {
		if !m.allow(ip) {
			return nil, serviceapi.ErrTargetAddressRefused
		}
	}
	return ips, nil
}

func validAddress(address string) bool {
	if len(address) == 0 || len(address) > 253 {
		return false
	}
	if ip := net.ParseIP(address); ip != nil {
		return true
	}
	return hostPattern.MatchString(address) && strings.Contains(address, ".")
}

// Targets lists every target, oldest first.
func (m *Manager) Targets(context.Context) (serviceapi.TargetsV1, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	records, err := m.all()
	if err != nil {
		return serviceapi.TargetsV1{}, err
	}
	list := serviceapi.TargetsV1{SchemaVersion: serviceapi.TargetsSchemaV1, Targets: []serviceapi.TargetV1{}}
	for _, r := range records {
		list.Targets = append(list.Targets, r.view())
	}
	return list, nil
}

// Target answers one target.
func (m *Manager) Target(_ context.Context, key string) (serviceapi.TargetV1, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read(key)
	if err != nil {
		return serviceapi.TargetV1{}, err
	}
	return r.view(), nil
}

// CreateTarget registers a target and makes its key pair.
func (m *Manager) CreateTarget(ctx context.Context, key string, create serviceapi.TargetCreateV1) (serviceapi.TargetV1, error) {
	if !keyPattern.MatchString(key) || key == offsiteKey {
		return serviceapi.TargetV1{}, serviceapi.ErrTargetInvalid
	}
	address := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(create.Address), "."))
	port, user := create.Port, create.User
	if port == 0 {
		port = 22
	}
	if user == "" {
		user = DefaultUser
	}
	if !validAddress(address) || port < 1 || port > 65535 || !userPattern.MatchString(user) || user == "root" {
		return serviceapi.TargetV1{}, serviceapi.ErrTargetInvalid
	}
	if err := m.checkBoughtArea(create); err != nil {
		return serviceapi.TargetV1{}, err
	}
	if _, err := m.addresses(ctx, address); err != nil {
		return serviceapi.TargetV1{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	records, err := m.all()
	if err != nil {
		return serviceapi.TargetV1{}, err
	}
	for _, r := range records {
		if r.Key == key {
			return serviceapi.TargetV1{}, serviceapi.ErrTargetExists
		}
	}
	if len(records) >= MaxTargets {
		return serviceapi.TargetV1{}, serviceapi.ErrTargetLimit
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return serviceapi.TargetV1{}, serviceapi.ErrTargetUnavailable
	}
	comment := "autobuild-" + key
	block, err := ssh.MarshalPrivateKey(private, comment)
	if err != nil {
		return serviceapi.TargetV1{}, serviceapi.ErrTargetUnavailable
	}
	sshPublic, err := ssh.NewPublicKey(public)
	if err != nil {
		return serviceapi.TargetV1{}, serviceapi.ErrTargetUnavailable
	}
	r := record{
		SchemaVersion: recordSchema, Key: key, Address: address, Port: port, User: user,
		PublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPublic))) + " " + comment,
		State:     stateNew, CreatedAt: m.now().UTC().Format(time.RFC3339), BackupsOffServer: create.BackupsOffServer,
		Provider: create.Provider, Region: create.Region,
	}
	if err := writePrivate(filepath.Join(m.dir, key+".key"), pem.EncodeToMemory(block)); err != nil {
		return serviceapi.TargetV1{}, serviceapi.ErrTargetUnavailable
	}
	if err := m.write(r); err != nil {
		_ = os.Remove(filepath.Join(m.dir, key+".key"))
		return serviceapi.TargetV1{}, err
	}
	return r.view(), nil
}

// ConfirmTarget pins the host key whose fingerprint the owner checked.
func (m *Manager) ConfirmTarget(_ context.Context, key string, confirm serviceapi.TargetConfirmV1) (serviceapi.TargetV1, error) {
	fingerprint := strings.TrimSpace(confirm.Fingerprint)
	if !strings.HasPrefix(fingerprint, "SHA256:") || len(fingerprint) > 80 {
		return serviceapi.TargetV1{}, serviceapi.ErrTargetInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read(key)
	if err != nil {
		return serviceapi.TargetV1{}, err
	}
	switch {
	case r.State == stateNew:
		// Nothing has been seen yet: a preflight shows the fingerprint first.
		return serviceapi.TargetV1{}, serviceapi.ErrTargetInvalid
	case r.Fingerprint != fingerprint:
		return serviceapi.TargetV1{}, serviceapi.ErrTargetFingerprintMismatch
	case r.State == stateConfirmed:
		return r.view(), nil
	}
	r.State = stateConfirmed
	if err := m.write(r); err != nil {
		return serviceapi.TargetV1{}, err
	}
	return r.view(), nil
}

// DeleteTarget forgets a target and deletes its private key.
func (m *Manager) DeleteTarget(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.read(key); err != nil {
		return err
	}
	keyErr := os.Remove(filepath.Join(m.dir, key+".key"))
	if err := os.Remove(filepath.Join(m.dir, key+".json")); err != nil {
		return serviceapi.ErrTargetUnavailable
	}
	if keyErr != nil && !errors.Is(keyErr, os.ErrNotExist) {
		return serviceapi.ErrTargetUnavailable
	}
	return nil
}

// PreflightTarget connects to a target. Before its host key is confirmed it
// only reads that key; afterwards it runs PreflightScript and records what it
// found. The answer is the target with its latest preflight.
func (m *Manager) PreflightTarget(ctx context.Context, key string) (serviceapi.TargetV1, error) {
	m.mu.Lock()
	r, err := m.read(key)
	m.mu.Unlock()
	if err != nil {
		return serviceapi.TargetV1{}, err
	}
	preflight := &serviceapi.TargetPreflightV1{}
	var seen ssh.PublicKey
	if r.State != stateConfirmed {
		seen, preflight.Outcome = m.readHostKey(ctx, r)
	} else {
		preflight.Outcome, preflight.Facts, preflight.Checks = m.check(ctx, r)
		preflight.Ready = preflight.Outcome == "checked" && ready(preflight.Checks)
	}
	preflight.CheckedAt = m.now().UTC().Format(time.RFC3339)

	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.read(key)
	if err != nil {
		return serviceapi.TargetV1{}, err
	}
	if current.State != r.State || current.HostKey != r.HostKey {
		// Confirmed or changed meanwhile: keep what is there now.
		return current.view(), nil
	}
	if seen != nil {
		current.State = statePending
		current.HostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(seen)))
		current.Fingerprint = ssh.FingerprintSHA256(seen)
	}
	current.Preflight = preflight
	if err := m.write(current); err != nil {
		return serviceapi.TargetV1{}, err
	}
	return current.view(), nil
}

// connect dials a public address of the target and starts SSH, for one check.
func (m *Manager) connect(ctx context.Context, r record, config *ssh.ClientConfig) (*ssh.Client, string) {
	return m.connectFor(ctx, r, config, runTimeout)
}

// connectFor dials a public address of the target and starts SSH; the
// connection ends after deadline whatever happens on it.
func (m *Manager) connectFor(ctx context.Context, r record, config *ssh.ClientConfig, deadline time.Duration) (*ssh.Client, string) {
	ips, err := m.addresses(ctx, r.Address)
	if err != nil {
		return nil, "refused_address"
	}
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	target := net.JoinHostPort(ips[0].String(), strconv.Itoa(r.Port))
	conn, err := m.dial(dialCtx, "tcp", target)
	if err != nil {
		if errors.Is(dialCtx.Err(), context.DeadlineExceeded) {
			return nil, "timeout"
		}
		return nil, "unreachable"
	}
	_ = conn.SetDeadline(time.Now().Add(deadline))
	config.Timeout = dialTimeout
	client, chans, reqs, err := ssh.NewClientConn(conn, target, config)
	if err != nil {
		_ = conn.Close()
		switch {
		case errors.Is(err, errHostKeySeen):
			return nil, "host_key_seen"
		case errors.Is(err, errKeyChanged):
			return nil, "host_key_changed"
		case strings.Contains(err.Error(), "unable to authenticate"):
			return nil, "auth_refused"
		case isTimeout(err):
			return nil, "timeout"
		default:
			return nil, "failed"
		}
	}
	return ssh.NewClient(client, chans, reqs), ""
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// readHostKey reads the server's host key and stops before signing in.
func (m *Manager) readHostKey(ctx context.Context, r record) (ssh.PublicKey, string) {
	var seen ssh.PublicKey
	config := &ssh.ClientConfig{
		User: r.User,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			seen = key
			return errHostKeySeen
		},
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256},
	}
	client, outcome := m.connect(ctx, r, config)
	if client != nil {
		_ = client.Close()
	}
	if seen == nil {
		if outcome == "" || outcome == "host_key_seen" {
			outcome = "failed"
		}
		return nil, outcome
	}
	return seen, "host_key_seen"
}

// check signs in with the target's key, the pinned host key only, and runs PreflightScript.
func (m *Manager) check(ctx context.Context, r record) (string, *serviceapi.TargetFactsV1, []serviceapi.TargetCheckV1) {
	client, outcome := m.pinnedClient(ctx, r, runTimeout)
	if client == nil {
		return outcome, nil, nil
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return "failed", nil, nil
	}
	defer session.Close()
	var out limitedBuffer
	session.Stdin = strings.NewReader(PreflightScript)
	session.Stdout = &out
	done := make(chan error, 1)
	go func() { done <- session.Run("sh -s") }()
	select {
	case err = <-done:
	case <-ctx.Done():
		return "timeout", nil, nil
	case <-time.After(runTimeout):
		return "timeout", nil, nil
	}
	if err != nil && out.Len() == 0 {
		return "failed", nil, nil
	}
	facts, checks := Evaluate(parse(out.String()))
	return "checked", facts, checks
}

// hostKeyAlgorithms offers only the pinned key's algorithms; an RSA key is
// offered with SHA-2 signatures, as current OpenSSH servers require.
func hostKeyAlgorithms(key ssh.PublicKey) []string {
	if key.Type() == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	}
	return []string{key.Type()}
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := maxOutput - b.Len(); room < len(p) {
		if room > 0 {
			b.Buffer.Write(p[:room])
		}
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

func parse(output string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && key != "" && len(key) <= 32 {
			values[key] = strings.TrimSpace(value)
		}
	}
	return values
}

// supportedSystems are the systems whose setup lines Repo C shows and tried on a fresh server (its design note
// BF14_4_YOUR_OWN_CLOUD_ACCOUNT.md §16), by os-release's ID and VERSION_ID: Ubuntu 24.04 and 26.04, Debian 12 and 13.
// Nothing else ABCP runs on a server depends on the system: the apps, their databases and the front door run in containers.
var supportedSystems = map[string]bool{"ubuntu 24.04": true, "ubuntu 26.04": true, "debian 12": true, "debian 13": true}

// Evaluate turns what PreflightScript printed into facts and checks.
func Evaluate(v map[string]string) (*serviceapi.TargetFactsV1, []serviceapi.TargetCheckV1) {
	number := func(key string) int {
		n, _ := strconv.Atoi(v[key])
		return n
	}
	facts := &serviceapi.TargetFactsV1{
		OS: limit(v["os_name"]), OSID: limit(v["os_id"]), OSVersion: limit(v["os_version"]), Arch: limit(v["arch"]),
		CPUs: number("cpus"), MemoryMB: number("mem_kb") / 1024, DiskFreeMB: number("disk_free_kb") / 1024,
		Rootless: strings.Contains(v["docker_security"], "name=rootless"),
		GVisor:   v["runsc"] != "" || strings.Contains(" "+v["docker_runtimes"]+" ", " runsc "),
	}
	if version := limit(v["docker"]); version != "" {
		facts.ContainerRuntime = "docker " + version
	}
	listening := func(port string) bool {
		for _, address := range strings.Fields(v["listening"]) {
			if strings.HasSuffix(address, ":"+port) {
				return true
			}
		}
		return false
	}
	// A port is free when nothing listens on it, or when ABCP's own front door
	// does (a target that already has sites): checking such a target again must
	// not make it look unready for its next app.
	ours := v["frontdoor"] != ""
	portFree := func(port string) (bool, string) {
		switch {
		case !listening(port):
			return true, "yes"
		case ours:
			return true, "front door"
		default:
			return false, "no"
		}
	}
	free80, held80 := portFree("80")
	free443, held443 := portFree("443")
	yesNo := func(ok bool) string {
		if ok {
			return "yes"
		}
		return "no"
	}
	registry := v["registry"]
	checks := []serviceapi.TargetCheckV1{
		{Name: "os_supported", Required: true, OK: supportedSystems[facts.OSID+" "+facts.OSVersion], Value: facts.OS},
		{Name: "arch", Required: true, OK: facts.Arch == "x86_64", Value: facts.Arch},
		{Name: "memory", Required: true, OK: facts.MemoryMB >= 1800, Value: fmt.Sprintf("%d MB", facts.MemoryMB)},
		{Name: "disk", Required: true, OK: facts.DiskFreeMB >= 10240, Value: fmt.Sprintf("%d MB free", facts.DiskFreeMB)},
		{Name: "not_root", Required: true, OK: v["uid"] != "" && v["uid"] != "0", Value: yesNo(v["uid"] != "0")},
		{Name: "container_runtime", Required: true, OK: facts.ContainerRuntime != "", Value: facts.ContainerRuntime},
		{Name: "rootless", Required: true, OK: facts.Rootless, Value: yesNo(facts.Rootless)},
		// gVisor is required on servers shared by different customers (the
		// design note §7); the product decides that per target.
		{Name: "gvisor", Required: false, OK: facts.GVisor, Value: yesNo(facts.GVisor)},
		{Name: "port_80_free", Required: true, OK: free80, Value: held80},
		{Name: "port_443_free", Required: true, OK: free443, Value: held443},
		{Name: "clock_synced", Required: true, OK: v["ntp"] == "yes", Value: yesNo(v["ntp"] == "yes")},
		{Name: "registry_reachable", Required: true, OK: registry == "200" || registry == "401", Value: yesNo(registry == "200" || registry == "401")},
	}
	return facts, checks
}

func limit(value string) string {
	if len(value) > 120 {
		return value[:120]
	}
	return value
}

func ready(checks []serviceapi.TargetCheckV1) bool {
	for _, c := range checks {
		if c.Required && !c.OK {
			return false
		}
	}
	return len(checks) > 0
}

func (r record) view() serviceapi.TargetV1 {
	return serviceapi.TargetV1{
		Key: r.Key, Address: r.Address, Port: r.Port, User: r.User, PublicKey: r.PublicKey, State: r.State,
		HostKeyFingerprint: r.Fingerprint, CreatedAt: r.CreatedAt, Preflight: r.Preflight, BackupsOffServer: r.BackupsOffServer,
		Provider: r.Provider, Region: r.Region,
	}
}

func (m *Manager) read(key string) (record, error) {
	if !keyPattern.MatchString(key) {
		return record{}, serviceapi.ErrTargetInvalid
	}
	if key == offsiteKey {
		return record{}, serviceapi.ErrTargetUnknown
	}
	data, err := os.ReadFile(filepath.Join(m.dir, key+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return record{}, serviceapi.ErrTargetUnknown
	}
	if err != nil {
		return record{}, serviceapi.ErrTargetUnavailable
	}
	var r record
	if strictjson.Decode(data, &r) != nil || r.Key != key || r.SchemaVersion != recordSchema {
		return record{}, serviceapi.ErrTargetUnavailable
	}
	return r, nil
}

func (m *Manager) all() ([]record, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil, serviceapi.ErrTargetUnavailable
	}
	var records []record
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !strings.HasSuffix(name, ".json") || name == offsiteKey+".json" {
			continue
		}
		r, err := m.read(strings.TrimSuffix(name, ".json"))
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].CreatedAt != records[j].CreatedAt {
			return records[i].CreatedAt < records[j].CreatedAt
		}
		return records[i].Key < records[j].Key
	})
	return records, nil
}

func (m *Manager) write(r record) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return serviceapi.ErrTargetUnavailable
	}
	if err := writePrivate(filepath.Join(m.dir, r.Key+".json"), append(data, '\n')); err != nil {
		return serviceapi.ErrTargetUnavailable
	}
	return nil
}

// writePrivate writes a file open to the controller's user alone, atomically.
func writePrivate(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".target-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

var (
	_ serviceapi.TargetController     = (*Manager)(nil)
	_ serviceapi.DeploymentController = (*Manager)(nil)
)

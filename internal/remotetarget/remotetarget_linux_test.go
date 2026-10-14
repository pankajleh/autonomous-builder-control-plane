//go:build linux

package remotetarget

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

const goodOutput = `uid=1001
arch=x86_64
os_id=ubuntu
os_version=24.04
os_name=Ubuntu 24.04.1 LTS
cpus=2
mem_kb=3926000
disk_free_kb=45000000
docker=27.3.1
docker_security=name=seccomp,profile=builtin,name=rootless,name=cgroupns
docker_runtimes=io.containerd.runc.v2 runc runsc
runsc=runsc version release-20241001.0
listening=127.0.0.53%lo:53 0.0.0.0:22 [::]:22
ntp=yes
registry=401
`

// fakeServer is an SSH server on loopback that accepts one client key and
// answers `sh -s` with canned output. It records every command it was asked
// to run and the script it received.
type fakeServer struct {
	t        *testing.T
	listener net.Listener
	hostKey  ssh.Signer
	mu       sync.Mutex
	allowed  ssh.PublicKey
	output   string
	commands []string
	scripts  []string
	logins   int
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeServer{t: t, listener: listener, hostKey: signer, output: goodOutput}
	go s.serve()
	t.Cleanup(func() { listener.Close() })
	return s
}

func (s *fakeServer) port() int { return s.listener.Addr().(*net.TCPAddr).Port }

func (s *fakeServer) newHostKey() {
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(private)
	s.mu.Lock()
	s.hostKey = signer
	s.mu.Unlock()
}

func (s *fakeServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeServer) handle(conn net.Conn) {
	defer conn.Close()
	s.mu.Lock()
	config := &ssh.ServerConfig{
		PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.allowed != nil && meta.User() == "autobuild" && string(key.Marshal()) == string(s.allowed.Marshal()) {
				s.logins++
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("refused")
		},
	}
	config.AddHostKey(s.hostKey)
	s.mu.Unlock()
	_, chans, reqs, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		if ch.ChannelType() != "session" {
			_ = ch.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		channel, requests, err := ch.Accept()
		if err != nil {
			return
		}
		for req := range requests {
			if req.Type != "exec" {
				_ = req.Reply(false, nil)
				continue
			}
			var payload struct{ Command string }
			_ = ssh.Unmarshal(req.Payload, &payload)
			_ = req.Reply(true, nil)
			script, _ := io.ReadAll(channel)
			s.mu.Lock()
			s.commands = append(s.commands, payload.Command)
			s.scripts = append(s.scripts, string(script))
			out := s.output
			s.mu.Unlock()
			_, _ = channel.Write([]byte(out))
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			_ = channel.Close()
		}
	}
}

func testManager(t *testing.T) *Manager {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "targets")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m, err := New(dir, func() time.Time { return time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	m.allow = func(ip net.IP) bool { return ip.IsLoopback() }
	return m
}

func publicKeyOf(t *testing.T, line string) ssh.PublicKey {
	t.Helper()
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestATargetIsEnrolledConfirmedAndCheckedWithoutChangingTheServer(t *testing.T) {
	ctx := context.Background()
	server := newFakeServer(t)
	m := testManager(t)

	target, err := m.CreateTarget(ctx, "host-1", serviceapi.TargetCreateV1{Address: "127.0.0.1", Port: server.port()})
	if err != nil {
		t.Fatal(err)
	}
	if target.State != "new" || target.User != "autobuild" || !strings.HasPrefix(target.PublicKey, "ssh-ed25519 ") || !strings.HasSuffix(target.PublicKey, " autobuild-host-1") {
		t.Fatalf("created = %+v", target)
	}
	for _, name := range []string{"host-1.json", "host-1.key"} {
		info, err := os.Stat(filepath.Join(m.dir, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v %v", name, info, err)
		}
	}
	stored, _ := os.ReadFile(filepath.Join(m.dir, "host-1.json"))
	if strings.Contains(string(stored), "PRIVATE KEY") {
		t.Fatal("the private key must stay in its own file")
	}
	server.mu.Lock()
	server.allowed = publicKeyOf(t, target.PublicKey)
	server.mu.Unlock()

	// First contact reads the host key and signs in to nothing.
	seen, err := m.PreflightTarget(ctx, "host-1")
	if err != nil {
		t.Fatal(err)
	}
	want := ssh.FingerprintSHA256(server.hostKey.PublicKey())
	if seen.State != "fingerprint_pending" || seen.HostKeyFingerprint != want || seen.Preflight.Outcome != "host_key_seen" || seen.Preflight.Ready {
		t.Fatalf("seen = %+v %+v", seen, seen.Preflight)
	}
	if server.logins != 0 || len(server.commands) != 0 {
		t.Fatalf("first contact signed in (%d) or ran %v", server.logins, server.commands)
	}

	// A fingerprint that is not the one presented is refused; the right one pins it.
	if _, err := m.ConfirmTarget(ctx, "host-1", serviceapi.TargetConfirmV1{Fingerprint: "SHA256:wrong"}); !errors.Is(err, serviceapi.ErrTargetFingerprintMismatch) {
		t.Fatalf("wrong fingerprint = %v", err)
	}
	confirmed, err := m.ConfirmTarget(ctx, "host-1", serviceapi.TargetConfirmV1{Fingerprint: want})
	if err != nil || confirmed.State != "confirmed" {
		t.Fatalf("confirm = %+v %v", confirmed, err)
	}

	checked, err := m.PreflightTarget(ctx, "host-1")
	if err != nil {
		t.Fatal(err)
	}
	p := checked.Preflight
	if p.Outcome != "checked" || !p.Ready || p.Facts.Arch != "x86_64" || p.Facts.MemoryMB != 3833 || !p.Facts.Rootless || !p.Facts.GVisor || p.Facts.ContainerRuntime != "docker 27.3.1" {
		t.Fatalf("preflight = %+v facts %+v checks %+v", p, p.Facts, p.Checks)
	}
	if len(server.commands) != 1 || server.commands[0] != "sh -s" || server.scripts[0] != PreflightScript {
		t.Fatalf("ran %q with %q", server.commands, server.scripts)
	}

	list, err := m.Targets(ctx)
	if err != nil || len(list.Targets) != 1 || list.Targets[0].Preflight == nil || list.SchemaVersion != "TargetsV1" {
		t.Fatalf("list = %+v %v", list, err)
	}

	if err := m.DeleteTarget(ctx, "host-1"); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(m.dir); len(entries) != 0 {
		t.Fatalf("left behind: %v", entries)
	}
	if _, err := m.Target(ctx, "host-1"); !errors.Is(err, serviceapi.ErrTargetUnknown) {
		t.Fatalf("after delete = %v", err)
	}
}

func TestAServerWithAnotherHostKeyIsRefusedBeforeAnythingRuns(t *testing.T) {
	ctx := context.Background()
	server := newFakeServer(t)
	m := testManager(t)
	target, _ := m.CreateTarget(ctx, "host-1", serviceapi.TargetCreateV1{Address: "127.0.0.1", Port: server.port()})
	server.allowed = publicKeyOf(t, target.PublicKey)
	seen, _ := m.PreflightTarget(ctx, "host-1")
	if _, err := m.ConfirmTarget(ctx, "host-1", serviceapi.TargetConfirmV1{Fingerprint: seen.HostKeyFingerprint}); err != nil {
		t.Fatal(err)
	}
	server.newHostKey()
	changed, err := m.PreflightTarget(ctx, "host-1")
	if err != nil || changed.Preflight.Outcome != "host_key_changed" || changed.Preflight.Ready || changed.State != "confirmed" || changed.HostKeyFingerprint != seen.HostKeyFingerprint {
		t.Fatalf("changed host = %+v %+v %v", changed, changed.Preflight, err)
	}
	if server.logins != 0 || len(server.commands) != 0 {
		t.Fatalf("signed in (%d) or ran %v on a swapped server", server.logins, server.commands)
	}
}

func TestRefusedLoginsUnreachableServersAndFailedChecksAreReportedNotHidden(t *testing.T) {
	ctx := context.Background()
	server := newFakeServer(t)
	m := testManager(t)
	_, _ = m.CreateTarget(ctx, "host-1", serviceapi.TargetCreateV1{Address: "127.0.0.1", Port: server.port()})
	seen, _ := m.PreflightTarget(ctx, "host-1")
	_, _ = m.ConfirmTarget(ctx, "host-1", serviceapi.TargetConfirmV1{Fingerprint: seen.HostKeyFingerprint})
	// The public key was never put on the server.
	refused, err := m.PreflightTarget(ctx, "host-1")
	if err != nil || refused.Preflight.Outcome != "auth_refused" || refused.Preflight.Ready {
		t.Fatalf("refused = %+v %v", refused.Preflight, err)
	}

	// A server without Docker, as root, with port 80 taken: checked, not ready.
	target, _ := m.Target(ctx, "host-1")
	server.allowed = publicKeyOf(t, target.PublicKey)
	server.output = "uid=0\narch=aarch64\nos_id=debian\nos_version=12\nmem_kb=1000000\ndisk_free_kb=1000\nlistening=0.0.0.0:80\nntp=no\n"
	poor, err := m.PreflightTarget(ctx, "host-1")
	if err != nil || poor.Preflight.Outcome != "checked" || poor.Preflight.Ready {
		t.Fatalf("poor host = %+v %v", poor.Preflight, err)
	}
	failed := map[string]bool{}
	for _, c := range poor.Preflight.Checks {
		if !c.OK {
			failed[c.Name] = true
		}
	}
	for _, name := range []string{"os_supported", "arch", "memory", "disk", "not_root", "container_runtime", "rootless", "gvisor", "port_80_free", "clock_synced", "registry_reachable"} {
		if !failed[name] {
			t.Errorf("%s should fail: %+v", name, poor.Preflight.Checks)
		}
	}
	if failed["port_443_free"] {
		t.Error("port 443 is free")
	}

	// Nobody listening.
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	port := closed.Addr().(*net.TCPAddr).Port
	closed.Close()
	_, _ = m.CreateTarget(ctx, "host-2", serviceapi.TargetCreateV1{Address: "127.0.0.1", Port: port})
	gone, err := m.PreflightTarget(ctx, "host-2")
	if err != nil || gone.Preflight.Outcome != "unreachable" || gone.State != "new" {
		t.Fatalf("unreachable = %+v %v", gone.Preflight, err)
	}
}

func TestTargetsPointOnlyAtPublicAddressesAndRefuseBadRequests(t *testing.T) {
	ctx := context.Background()
	m := testManager(t)
	m.allow = PublicAddress
	m.resolve = func(_ context.Context, host string) ([]net.IP, error) {
		switch host {
		case "inside.example.com":
			return []net.IP{net.ParseIP("203.0.114.9"), net.ParseIP("10.1.2.3")}, nil
		case "server.example.com":
			return []net.IP{net.ParseIP("13.232.1.2")}, nil
		}
		return nil, errors.New("no such host")
	}
	for _, address := range []string{"127.0.0.1", "10.0.0.5", "172.16.3.4", "192.168.1.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "::1", "fd00::1", "fe80::1", "inside.example.com", "missing.example.com"} {
		if _, err := m.CreateTarget(ctx, "t-"+strconv.Itoa(len(address)), serviceapi.TargetCreateV1{Address: address}); !errors.Is(err, serviceapi.ErrTargetAddressRefused) {
			t.Errorf("%s = %v", address, err)
		}
	}
	for _, bad := range []struct {
		key    string
		create serviceapi.TargetCreateV1
	}{
		{"Host_1", serviceapi.TargetCreateV1{Address: "13.232.1.2"}},
		{"host-1", serviceapi.TargetCreateV1{Address: "13.232.1.2", User: "root"}},
		{"host-1", serviceapi.TargetCreateV1{Address: "13.232.1.2", User: "a b"}},
		{"host-1", serviceapi.TargetCreateV1{Address: "13.232.1.2", Port: 70000}},
		{"host-1", serviceapi.TargetCreateV1{Address: "server.example.com; rm -rf /"}},
		{"host-1", serviceapi.TargetCreateV1{Address: "localhost"}},
		{"host-1", serviceapi.TargetCreateV1{Address: ""}},
	} {
		if _, err := m.CreateTarget(ctx, bad.key, bad.create); !errors.Is(err, serviceapi.ErrTargetInvalid) {
			t.Errorf("%+v = %v", bad, err)
		}
	}
	if _, err := m.CreateTarget(ctx, "host-1", serviceapi.TargetCreateV1{Address: "Server.Example.com."}); err != nil {
		t.Fatalf("public host name = %v", err)
	}
	if _, err := m.CreateTarget(ctx, "host-1", serviceapi.TargetCreateV1{Address: "13.232.1.2"}); !errors.Is(err, serviceapi.ErrTargetExists) {
		t.Fatalf("duplicate = %v", err)
	}
	if _, err := m.ConfirmTarget(ctx, "host-1", serviceapi.TargetConfirmV1{Fingerprint: "SHA256:abc"}); !errors.Is(err, serviceapi.ErrTargetInvalid) {
		t.Fatalf("confirm before any host key = %v", err)
	}
	for i := 2; i <= MaxTargets; i++ {
		if _, err := m.CreateTarget(ctx, "host-"+strconv.Itoa(i), serviceapi.TargetCreateV1{Address: "13.232.1.2"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.CreateTarget(ctx, "one-more", serviceapi.TargetCreateV1{Address: "13.232.1.2"}); !errors.Is(err, serviceapi.ErrTargetLimit) {
		t.Fatalf("limit = %v", err)
	}
	// A name that resolved to a public address once and points inside later is refused at connection time.
	m.resolve = func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("10.0.0.7")}, nil }
	moved, err := m.PreflightTarget(ctx, "host-1")
	if err != nil || moved.Preflight.Outcome != "refused_address" {
		t.Fatalf("moved inside = %+v %v", moved.Preflight, err)
	}
}

func TestTheFolderMustBePrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "open")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir, nil); err == nil {
		t.Fatal("an open folder was accepted")
	}
}

// A target that already has sites listens on 80 and 443 through ABCP's own
// front door: checking it again keeps it ready, while anything else on those
// ports still fails the check.
func TestEvaluatePortsHeldByTheFrontDoor(t *testing.T) {
	held := strings.Replace(goodOutput, "listening=127.0.0.53%lo:53 0.0.0.0:22 [::]:22", "listening=127.0.0.53%lo:53 0.0.0.0:22 0.0.0.0:80 0.0.0.0:443", 1)
	ports := func(output string) map[string]serviceapi.TargetCheckV1 {
		_, checks := Evaluate(parse(output))
		found := map[string]serviceapi.TargetCheckV1{}
		for _, c := range checks {
			found[c.Name] = c
		}
		return found
	}
	for name, c := range ports(held + "frontdoor=0123456789ab\n") {
		if (name == "port_80_free" || name == "port_443_free") && (!c.OK || c.Value != "front door") {
			t.Errorf("%s held by the front door = %+v", name, c)
		}
	}
	for name, c := range ports(held + "frontdoor=\n") {
		if (name == "port_80_free" || name == "port_443_free") && (c.OK || c.Value != "no") {
			t.Errorf("%s held by something else = %+v", name, c)
		}
	}
	for name, c := range ports(goodOutput + "frontdoor=0123456789ab\n") {
		if (name == "port_80_free" || name == "port_443_free") && (!c.OK || c.Value != "yes") {
			t.Errorf("%s free = %+v", name, c)
		}
	}
}

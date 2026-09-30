package preview

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

type dockerCommand func(context.Context, ...string) (string, error)
type presentation struct {
	server                *http.Server
	transport             *http.Transport
	url, endpoint, handle string
	appCookies            bool
	// volume is the hosted data volume mounted at /data in the data service, or "" for a preview.
	volume string
	closed atomic.Bool
}

// gatewayCookie reports a cookie name the preview gateway keeps for itself; such cookies never reach or come from an app.
func gatewayCookie(name string) bool {
	return strings.HasPrefix(name, "__Host-preview") || strings.HasPrefix(name, "__Host-unlock")
}

// appCookieHeader is a Cookie header without the gateway's own cookies, or "" when nothing is left.
func appCookieHeader(values []string) string {
	kept := []string{}
	for _, value := range values {
		for _, part := range strings.Split(value, ";") {
			part = strings.TrimSpace(part)
			name, _, ok := strings.Cut(part, "=")
			if ok && name != "" && !gatewayCookie(strings.TrimSpace(name)) {
				kept = append(kept, part)
			}
		}
	}
	return strings.Join(kept, "; ")
}

type dockerRuntime struct {
	mu              sync.Mutex
	root, namespace string
	run             dockerCommand
	approved        map[string]bool
	groups          map[string]*presentation
	// stream runs one Docker command with the given standard input and output, for hosted backups and restores.
	stream func(ctx context.Context, stdin io.Reader, stdout io.Writer, args ...string) error
}

// hostedSleepSeconds keeps a hosted instance's containers alive until they are stopped: ten years.
const hostedSleepSeconds = 10 * 365 * 24 * 3600

// dockerEnvironment ignores ambient Docker contexts, endpoints, registry credentials and proxy settings.
func dockerEnvironment(root string) ([]string, []string) {
	return []string{"PATH=/usr/bin:/bin", "LANG=C", "HOME=" + root}, []string{"--host=unix:///var/run/docker.sock", "--config=" + filepath.Join(root, "docker-config")}
}

func newDocker(root string) *dockerRuntime {
	d := &dockerRuntime{root: root, namespace: digest([]byte(root)), approved: map[string]bool{}, groups: map[string]*presentation{}}
	d.run = func(ctx context.Context, args ...string) (string, error) {
		// Ignore ambient Docker contexts, endpoints, registry credentials and proxy
		// settings. The socket is controller-only; never a candidate mount.
		env, global := dockerEnvironment(root)
		return execute(ctx, "/usr/bin/docker", env, append(global, args...)...)
	}
	d.stream = func(ctx context.Context, stdin io.Reader, stdout io.Writer, args ...string) error {
		env, global := dockerEnvironment(root)
		return stream(ctx, "/usr/bin/docker", env, stdin, stdout, append(global, args...)...)
	}
	return d
}
func (d *dockerRuntime) Available(p PreviewProfileV1) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return p.Validate() == nil && d.approved[p.Digest()]
}
func (d *dockerRuntime) label() string { return "abcp.preview.owner=" + d.namespace }
func (d *dockerRuntime) network(id string) string {
	return "abcp-preview-" + d.namespace[:16] + "-" + id[:24]
}
func (d *dockerRuntime) container(id string, i int) string {
	return d.network(id) + "-" + strconv.Itoa(i)
}

// volume names a hosting key's data volume. Only a key digest reaches it, never the key.
func (d *dockerRuntime) volume(keyDigest string) string {
	return "abcp-hosted-" + d.namespace[:16] + "-" + keyDigest[:24]
}
func cleanEnv(s ServiceProfileV1) []string {
	out := []string{"-i", "PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/scratch", "TMPDIR=/scratch"}
	return append(out, environmentValues(s.Environment)...)
}
func scratchOptions(p PreviewProfileV1, s ServiceProfileV1) string {
	uid, gid, _ := strings.Cut(s.User, ":")
	return "rw,nosuid,nodev,noexec,size=" + strconv.FormatInt(p.TmpfsBytes, 10) + ",mode=700,uid=" + uid + ",gid=" + gid
}
func (d *dockerRuntime) createArgs(id, path string, p PreviewProfileV1, s ServiceProfileV1, i int, volume string) []string {
	args := []string{"create", "--pull=never", "--name", d.container(id, i), "--label", d.label(), "--label", "abcp.preview.id=" + id, "--network", d.network(id), "--network-alias", s.Name, "--user", s.User, "--cap-drop=ALL", "--security-opt=no-new-privileges", "--read-only", "--cpu-period=100000", "--cpu-quota=" + strconv.Itoa(p.CPUQuota), "--memory=" + strconv.FormatInt(p.MemoryBytes, 10), "--memory-swap=" + strconv.FormatInt(p.MemoryBytes, 10), "--pids-limit=" + strconv.Itoa(p.PidsLimit), "--ulimit=nofile=1024:1024", "--restart=no", "--ipc=none", "--log-driver=none", "--no-healthcheck", "--workdir=/scratch", "--tmpfs", "/scratch:" + scratchOptions(p, s), "--entrypoint=/usr/bin/env"}
	if s.MountSource {
		args = append(args, "--mount", "type=bind,src="+path+",dst=/source,readonly,bind-recursive=readonly,bind-propagation=rprivate")
	}
	if s.DataVolume {
		args = append(args, "--mount", "type=volume,src="+volume+",dst="+DataPath)
	}
	args = append(args, s.Image)
	args = append(args, cleanEnv(s)...)
	sleep := p.TTLSeconds
	if p.Hosted {
		sleep = hostedSleepSeconds
	}
	return append(args, "/bin/sleep", strconv.Itoa(sleep))
}
func (d *dockerRuntime) imageSafe(ctx context.Context, s ServiceProfileV1) bool {
	raw, err := d.run(ctx, "image", "inspect", s.Image)
	if err != nil {
		return false
	}
	var images []struct {
		RepoDigests []string
		Config      struct {
			Env     []string
			Volumes map[string]json.RawMessage
		}
	}
	if json.Unmarshal([]byte(raw), &images) != nil || len(images) != 1 || len(images[0].Config.Volumes) != 0 {
		return false
	}
	found := false
	for _, ref := range images[0].RepoDigests {
		if ref == s.Image {
			found = true
		}
	}
	if !found {
		return false
	}
	for _, value := range images[0].Config.Env {
		key, v, ok := strings.Cut(value, "=")
		if !ok {
			return false
		}
		switch key {
		case "PATH":
			if v != "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" && v != "/usr/local/bin:/usr/bin:/bin" && v != "/usr/bin:/bin" {
				return false
			}
		case "HOME":
			if v != "/root" && v != "/home/node" && v != "/" {
				return false
			}
		default:
			if !validEnvironment(map[string]string{key: v}) {
				return false
			}
		}
	}
	return true
}
func (d *dockerRuntime) internalNetwork(ctx context.Context, id string) bool {
	raw, err := d.run(ctx, "network", "inspect", d.network(id))
	if err != nil {
		return false
	}
	var values []struct {
		Internal   bool
		EnableIPv6 bool
		Driver     string
		Labels     map[string]string
	}
	return json.Unmarshal([]byte(raw), &values) == nil && len(values) == 1 && values[0].Internal && !values[0].EnableIPv6 && values[0].Driver == "bridge" && values[0].Labels["abcp.preview.owner"] == d.namespace
}

// endpoint refuses host network, bridge attachment, published ports and any
// second network. Only the configured presented service IP/port is returned.
func (d *dockerRuntime) endpoint(ctx context.Context, id string, p PreviewProfileV1, s ServiceProfileV1, i int, volume string) (string, error) {
	raw, err := d.run(ctx, "inspect", d.container(id, i))
	if err != nil {
		return "", ErrUnavailable
	}
	var values []struct {
		State  struct{ Running bool }
		Config struct {
			User   string
			Labels map[string]string
		}
		HostConfig struct {
			Privileged, ReadonlyRootfs                         bool
			NetworkMode, IpcMode                               string
			CapDrop, SecurityOpt, Binds                        []string
			CpuPeriod, CpuQuota, Memory, MemorySwap, PidsLimit int64
			Tmpfs                                              map[string]string
			PortBindings                                       map[string]json.RawMessage
		}
		NetworkSettings struct {
			Networks map[string]struct{ IPAddress string }
			Ports    map[string]json.RawMessage
		}
		Mounts []struct {
			Type, Name, Source, Destination string
			RW                              bool
		}
	}
	if json.Unmarshal([]byte(raw), &values) != nil || len(values) != 1 {
		return "", ErrUnavailable
	}
	v := values[0]
	h := v.HostConfig
	if h.CpuPeriod != 100000 || h.IpcMode != "none" || len(h.Tmpfs) != 1 || h.Tmpfs["/scratch"] != scratchOptions(p, s) {
		return "", ErrUnavailable
	}
	if !v.State.Running || v.Config.User != s.User || v.Config.Labels["abcp.preview.owner"] != d.namespace || v.Config.Labels["abcp.preview.id"] != id || h.Privileged || !h.ReadonlyRootfs || h.NetworkMode != d.network(id) || len(h.CapDrop) != 1 || h.CapDrop[0] != "ALL" || len(h.SecurityOpt) != 1 || (h.SecurityOpt[0] != "no-new-privileges" && h.SecurityOpt[0] != "no-new-privileges=true") || len(h.Binds) != 0 || h.CpuQuota != int64(p.CPUQuota) || h.Memory != p.MemoryBytes || h.MemorySwap != p.MemoryBytes || h.PidsLimit != int64(p.PidsLimit) || len(h.PortBindings) != 0 || len(v.NetworkSettings.Networks) != 1 {
		return "", ErrUnavailable
	}
	for _, binding := range v.NetworkSettings.Ports {
		if string(binding) != "null" && string(binding) != "[]" {
			return "", ErrUnavailable
		}
	}
	sourceFound, volumeFound := false, false
	for _, m := range v.Mounts {
		if m.Type == "tmpfs" && m.Destination == "/scratch" {
			continue
		}
		// The one writable mount besides /scratch: this hosting key's own data volume, at /data, in the data service.
		if m.Type == "volume" && m.Destination == DataPath && m.RW && s.DataVolume && volume != "" && m.Name == volume && !volumeFound {
			volumeFound = true
			continue
		}
		if m.Type != "bind" || m.Destination != "/source" || m.RW || !s.MountSource || m.Source != filepath.Join(d.root, "sources", id) || sourceFound {
			return "", ErrUnavailable
		}
		sourceFound = true
	}
	if s.MountSource != sourceFound || s.DataVolume != volumeFound {
		return "", ErrUnavailable
	}
	network, ok := v.NetworkSettings.Networks[d.network(id)]
	ip := net.ParseIP(network.IPAddress)
	if !ok || ip == nil || ip.To4() == nil || !ip.IsPrivate() || ip.IsLoopback() {
		return "", ErrUnavailable
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(s.Port)), nil
}
func (d *dockerRuntime) startGroup(ctx context.Context, id, path string, p PreviewProfileV1, probe bool, volume string) (*presentation, error) {
	if !sha256Pattern.MatchString(id) || p.Validate() != nil || (p.dataService() >= 0) != (volume != "") {
		return nil, ErrUnavailable
	}
	if path != filepath.Join(d.root, "sources", id) || strings.ContainsAny(path, ",\x00\r\n") {
		return nil, ErrUnavailable
	}
	for _, s := range p.Services {
		if !d.imageSafe(ctx, s) {
			return nil, ErrUnavailable
		}
	}
	if _, err := d.run(ctx, "network", "create", "--internal", "--driver=bridge", "--label", d.label(), "--label", "abcp.preview.id="+id, d.network(id)); err != nil {
		return nil, err
	}
	if !d.internalNetwork(ctx, id) {
		return nil, ErrUnavailable
	}
	if volume != "" {
		if err := d.ensureVolume(ctx, volume); err != nil {
			return nil, err
		}
	}
	g := &presentation{handle: jsonDigest([]string{d.namespace, id, "route"}), appCookies: p.AppCookies, volume: volume}
	for i, s := range p.Services {
		if _, err := d.run(ctx, d.createArgs(id, path, p, s, i, volume)...); err != nil {
			return nil, err
		}
		name := d.container(id, i)
		if _, err := d.run(ctx, "start", name); err != nil {
			return nil, err
		}
		if len(s.PrepareArgv) > 0 && !probe {
			args := append([]string{"exec", name, "/usr/bin/env"}, cleanEnv(s)...)
			args = append(args, s.PrepareArgv...)
			if _, err := d.run(ctx, args...); err != nil {
				return nil, err
			}
		}
		start := s.StartArgv
		if probe {
			start = []string{"/bin/busybox", "httpd", "-f", "-p", strconv.Itoa(s.Port), "-h", "/scratch"}
		}
		args := append([]string{"exec", "--detach", name, "/usr/bin/env"}, cleanEnv(s)...)
		args = append(args, start...)
		if _, err := d.run(ctx, args...); err != nil {
			return nil, err
		}
		endpoint, err := d.endpoint(ctx, id, p, s, i, volume)
		if err != nil {
			return nil, err
		}
		if s.Presented {
			g.endpoint = endpoint
		}
	}
	if g.endpoint == "" {
		return nil, ErrUnavailable
	}
	if err := g.present(); err != nil {
		return nil, err
	}
	return g, nil
}
func (g *presentation) present() error {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return ErrUnavailable
	}
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.Equal(net.ParseIP("127.0.0.1")) {
		listener.Close()
		return ErrUnavailable
	}
	g.url = "http://" + listener.Addr().String()
	// The dialer ignores all request-derived destinations, proxy environment,
	// redirects, and DNS. It can reach this one presented endpoint only.
	g.transport = &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxConnsPerHost: 8, ResponseHeaderTimeout: 5 * time.Second, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp4", g.endpoint)
	}}
	target := &url.URL{Scheme: "http", Host: g.endpoint}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = g.transport
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "preview unavailable", http.StatusServiceUnavailable)
	}
	original := proxy.Director
	proxy.Director = func(r *http.Request) {
		original(r)
		r.Host = g.endpoint
		r.Header.Del("Authorization")
		r.Header.Del("Proxy-Authorization")
		cookies := r.Header.Values("Cookie")
		r.Header.Del("Cookie")
		if g.appCookies {
			if kept := appCookieHeader(cookies); kept != "" {
				r.Header.Set("Cookie", kept)
			}
		}
		r.Header.Del("Forwarded")
		r.Header.Del("X-Forwarded-Host")
		r.Header.Del("X-Forwarded-For")
	}
	proxy.ModifyResponse = func(r *http.Response) error {
		set := r.Header.Values("Set-Cookie")
		r.Header.Del("Set-Cookie")
		if g.appCookies {
			for _, value := range set {
				name, _, ok := strings.Cut(value, "=")
				if ok && strings.TrimSpace(name) != "" && !gatewayCookie(strings.TrimSpace(name)) {
					r.Header.Add("Set-Cookie", value)
				}
			}
		}
		return nil
	}
	slots := make(chan struct{}, 8)
	g.server = &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 10 * time.Second, MaxHeaderBytes: 16 << 10, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect || r.Header.Get("Upgrade") != "" {
			http.Error(w, "unsupported request", 400)
			return
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			http.Error(w, "preview busy", 503)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		proxy.ServeHTTP(w, r)
	})}
	go func() {
		defer g.closed.Store(true)
		_ = g.server.Serve(listener)
	}()
	return nil
}
func (d *dockerRuntime) Start(ctx context.Context, id, path string, p PreviewProfileV1) (string, error) {
	if p.Hosted {
		return "", ErrUnavailable
	}
	return d.start(ctx, id, path, p, "")
}

// StartHosted starts a hosted instance with the hosting key's data volume (B9.2.1).
func (d *dockerRuntime) StartHosted(ctx context.Context, id, path string, p PreviewProfileV1, keyDigest string) (string, error) {
	if !p.Hosted || !sha256Pattern.MatchString(keyDigest) {
		return "", ErrUnavailable
	}
	volume := ""
	if p.dataService() >= 0 {
		volume = d.volume(keyDigest)
	}
	return d.start(ctx, id, path, p, volume)
}
func (d *dockerRuntime) start(ctx context.Context, id, path string, p PreviewProfileV1, volume string) (string, error) {
	if !sha256Pattern.MatchString(id) || path != filepath.Join(d.root, "sources", id) || strings.ContainsAny(path, ",\x00\r\n") {
		return "", ErrUnavailable
	}
	if !d.Available(p) {
		return "", ErrUnavailable
	}
	g, err := d.startGroup(ctx, id, path, p, false, volume)
	if err != nil {
		_ = d.Stop(context.Background(), id)
		return "", err
	}
	d.mu.Lock()
	d.groups[id] = g
	d.mu.Unlock()
	return g.handle, nil
}
func requestHealth(ctx context.Context, address, path string, allow404 bool) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address+path, nil)
	if err != nil {
		return false
	}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return response.StatusCode >= 200 && response.StatusCode < 300 || allow404 && response.StatusCode == 404
}
func (d *dockerRuntime) Healthy(ctx context.Context, id string, p PreviewProfileV1) (bool, error) {
	d.mu.Lock()
	g := d.groups[id]
	d.mu.Unlock()
	unsafe := func() (bool, error) {
		// Stop and TTL cancellation interrupt these same inspection commands.
		// An interrupted observation does not disprove the profile's isolation.
		if err := ctx.Err(); err != nil {
			return false, err
		}
		d.mu.Lock()
		delete(d.approved, p.Digest())
		d.mu.Unlock()
		return false, ErrIntegrity
	}
	if g == nil || !d.internalNetwork(ctx, id) {
		return unsafe()
	}
	for i, s := range p.Services {
		endpoint, err := d.endpoint(ctx, id, p, s, i, g.volume)
		if err != nil || s.Presented && endpoint != g.endpoint {
			return unsafe()
		}
	}
	return requestHealth(ctx, g.url, p.HealthPath, false), nil
}

// ResolveRoute exposes only the listener owned by this runtime for the exact
// preview/handle pair, never a container endpoint or a reconstructed URL.
func (d *dockerRuntime) ResolveRoute(ctx context.Context, id, handle string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	g := d.groups[id]
	if ctx.Err() != nil || !sha256Pattern.MatchString(id) || !sha256Pattern.MatchString(handle) || g == nil || g.handle != handle || g.server == nil || g.transport == nil || g.closed.Load() || serviceapi.ValidatePreviewTargetURL(g.url) != nil {
		return "", ErrUnavailable
	}
	return g.url, nil
}

func (d *dockerRuntime) Stop(ctx context.Context, id string) error {
	if !sha256Pattern.MatchString(id) {
		return ErrIntegrity
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	d.mu.Lock()
	g := d.groups[id]
	delete(d.groups, id)
	d.mu.Unlock()
	if g != nil {
		g.closed.Store(true)
		_ = g.server.Close()
		g.transport.CloseIdleConnections()
	}
	return d.removeObjects(ctx, "abcp.preview.id="+id)
}
func (d *dockerRuntime) removeObjects(ctx context.Context, extra string) error {
	filters := []string{"--filter", "label=" + d.label()}
	if extra != "" {
		filters = append(filters, "--filter", "label="+extra)
	}
	ids, err := d.run(ctx, append([]string{"ps", "-aq", "--no-trunc"}, filters...)...)
	if err != nil {
		return ErrUnavailable
	}
	list := strings.Fields(ids)
	if len(list) > 64 {
		return ErrIntegrity
	}
	for _, id := range list {
		if !sha256Pattern.MatchString(id) {
			return ErrIntegrity
		}
		if _, err = d.run(ctx, "rm", "--force", "--volumes", id); err != nil {
			return ErrUnavailable
		}
	}
	networks, err := d.run(ctx, append([]string{"network", "ls", "-q", "--no-trunc"}, filters...)...)
	if err != nil {
		return ErrUnavailable
	}
	list = strings.Fields(networks)
	if len(list) > 16 {
		return ErrIntegrity
	}
	for _, id := range list {
		if !sha256Pattern.MatchString(id) {
			return ErrIntegrity
		}
		if _, err = d.run(ctx, "network", "rm", id); err != nil {
			return ErrUnavailable
		}
	}
	return nil
}
func (d *dockerRuntime) Reconcile(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.groups) != 0 {
		return ErrIntegrity
	}
	return d.removeObjects(ctx, "")
}

const probeSourceName = ".abcp-preview-source-probe"
const probeSourceContents = "governed preview source probe"

// prove is intentionally conservative: a local pinned image must also contain
// the fixed offline probe tools. Missing tools, unusable container IPs, egress,
// or an unreachable explicit loopback listener all leave capability false.
func (d *dockerRuntime) prove(ctx context.Context, p PreviewProfileV1) (proved bool) {
	if !d.hostSupported(ctx) {
		return false
	}
	id := jsonDigest([]string{d.namespace, p.Digest(), "isolation-probe"})
	checkout := Checkout{Root: filepath.Join(d.root, "sources")}
	path := filepath.Join(checkout.Root, id)
	sourceCreated := false
	// A hosted profile's probe mounts a throwaway data volume of its own, removed afterwards.
	volume := ""
	if p.dataService() >= 0 {
		volume = d.volume(jsonDigest([]string{d.namespace, p.Digest(), "isolation-probe-volume"}))
	}
	defer func() {
		err := d.Stop(context.Background(), id)
		if err == nil && sourceCreated {
			err = checkout.Remove(id)
		}
		if err == nil && volume != "" {
			err = d.removeVolume(context.Background(), volume)
		}
		proved = proved && err == nil
		d.mu.Lock()
		defer d.mu.Unlock()
		if proved {
			d.approved[p.Digest()] = true
		} else {
			delete(d.approved, p.Digest())
		}
	}()
	for _, s := range p.Services {
		if s.MountSource {
			root, err := privateDirectory(checkout.Root, true)
			if err != nil {
				return false
			}
			root.Close()
			// Exercise the same mount as candidate execution, with readable offline
			// input under this namespace rather than any governed worktree.
			if os.Mkdir(path, 0700) != nil {
				return false
			}
			sourceCreated = true
			if os.WriteFile(filepath.Join(path, probeSourceName), []byte(probeSourceContents), 0600) != nil || readableSource(path) != nil {
				return false
			}
			break
		}
	}
	g, err := d.startGroup(ctx, id, path, p, true, volume)
	if err != nil {
		return false
	}
	d.mu.Lock()
	d.groups[id] = g
	d.mu.Unlock()
	for i, s := range p.Services {
		// A functioning route reader and wget are prerequisites; a missing probe
		// executable cannot masquerade as evidence of blocked external egress.
		args := []string{"exec", d.container(id, i), "/usr/bin/env", "-i", "/bin/busybox"}
		if s.MountSource {
			// Docker exec inherits the configured container user. Prove that the
			// real bind mount exposes readable bytes to that user before approval.
			data, err := d.run(ctx, append(args, "cat", "/source/"+probeSourceName)...)
			if err != nil || data != probeSourceContents {
				return false
			}
		}
		if _, err = d.run(ctx, append(args, "wget", "--help")...); err != nil {
			return false
		}
		routes, e := d.run(ctx, append(args, "cat", "/proc/net/route")...)
		if e != nil || !strings.HasPrefix(routes, "Iface") {
			return false
		}
		lines := strings.Split(routes, "\n")
		for _, line := range lines[1:] {
			fields := strings.Fields(line)
			if len(fields) > 1 && fields[1] == "00000000" {
				return false
			}
		}
		// A successful external HTTP request disproves isolation.
		if _, err = d.run(ctx, append(args, "wget", "-T", "2", "-O", "/dev/null", "http://1.1.1.1/")...); err == nil {
			return false
		}
		if s.DataVolume {
			// The service's own user must be able to write its data volume, as the image's /data prepares it.
			if _, err = d.run(ctx, append(args, "touch", DataPath+"/.abcp-probe")...); err != nil {
				return false
			}
		}
	}
	reachable := false
	for i := 0; i < 20 && ctx.Err() == nil; i++ {
		if requestHealth(ctx, g.url, "/", true) {
			reachable = true
			break
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
		timer.Stop()
	}
	if !reachable {
		return false
	}
	return true
}
func (d *dockerRuntime) proveProfiles(ctx context.Context, profiles map[string]PreviewProfileV1) {
	for _, p := range profiles {
		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		d.prove(probeCtx, p)
		cancel()
	}
}

// String never includes private Docker IDs, endpoints or filesystem paths.
func (d *dockerRuntime) String() string { return fmt.Sprint("governed preview runtime") }

func (d *dockerRuntime) hostSupported(ctx context.Context) bool {
	raw, err := d.run(ctx, "info", "--format", "{{json .}}")
	var info struct {
		OSType                                         string
		MemoryLimit, SwapLimit, CpuCfsQuota, PidsLimit bool
		SecurityOptions                                []string
	}
	if err != nil || json.Unmarshal([]byte(raw), &info) != nil || info.OSType != "linux" || !info.MemoryLimit || !info.SwapLimit || !info.CpuCfsQuota || !info.PidsLimit {
		return false
	}
	for _, opt := range info.SecurityOptions {
		if strings.HasPrefix(opt, "name=seccomp,") {
			return true
		}
	}
	return false
}

// ensureVolume creates a hosting key's data volume, or finds the one this controller made before. A volume of that name
// with another owner, another driver or any driver options (a bind to a host path, for example) is refused.
func (d *dockerRuntime) ensureVolume(ctx context.Context, volume string) error {
	if _, err := d.run(ctx, "volume", "create", "--driver", "local", "--label", d.label(), "--label", "abcp.hosted.volume="+volume, volume); err != nil {
		return ErrUnavailable
	}
	return d.ownVolume(ctx, volume)
}
func (d *dockerRuntime) ownVolume(ctx context.Context, volume string) error {
	raw, err := d.run(ctx, "volume", "inspect", volume)
	if err != nil {
		return ErrUnavailable
	}
	var values []struct {
		Name, Driver string
		Labels       map[string]string
		Options      map[string]string
	}
	if json.Unmarshal([]byte(raw), &values) != nil || len(values) != 1 {
		return ErrUnavailable
	}
	v := values[0]
	if v.Name != volume || v.Driver != "local" || len(v.Options) != 0 || v.Labels["abcp.preview.owner"] != d.namespace || v.Labels["abcp.hosted.volume"] != volume {
		return ErrIntegrity
	}
	return nil
}
func (d *dockerRuntime) removeVolume(ctx context.Context, volume string) error {
	raw, err := d.run(ctx, "volume", "ls", "-q", "--filter", "name=^"+volume+"$")
	if err != nil {
		return ErrUnavailable
	}
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	if err = d.ownVolume(ctx, volume); err != nil {
		return err
	}
	if _, err = d.run(ctx, "volume", "rm", volume); err != nil {
		return ErrUnavailable
	}
	return nil
}

// RemoveVolume removes a hosting key's data volume. Docker refuses while a container still uses it.
func (d *dockerRuntime) RemoveVolume(ctx context.Context, keyDigest string) error {
	if !sha256Pattern.MatchString(keyDigest) {
		return ErrIntegrity
	}
	return d.removeVolume(ctx, d.volume(keyDigest))
}

// dataExec checks the running hosted instance exactly as a health read does, then returns the docker exec arguments
// that run argv in its data service.
func (d *dockerRuntime) dataExec(ctx context.Context, id string, p PreviewProfileV1, argv []string, stdin bool) ([]string, error) {
	i := p.dataService()
	if !p.Hosted || i < 0 || len(argv) == 0 {
		return nil, ErrUnavailable
	}
	d.mu.Lock()
	g := d.groups[id]
	d.mu.Unlock()
	if g == nil || g.volume == "" || !d.internalNetwork(ctx, id) {
		return nil, ErrUnavailable
	}
	if _, err := d.endpoint(ctx, id, p, p.Services[i], i, g.volume); err != nil {
		return nil, ErrUnavailable
	}
	args := []string{"exec"}
	if stdin {
		args = append(args, "-i")
	}
	args = append(args, d.container(id, i), "/usr/bin/env")
	args = append(args, cleanEnv(p.Services[i])...)
	return append(args, argv...), nil
}

// Dump writes a backup of a hosted instance's data to w with the profile's backup command.
func (d *dockerRuntime) Dump(ctx context.Context, id string, p PreviewProfileV1, w io.Writer) error {
	i := p.dataService()
	if i < 0 {
		return ErrUnavailable
	}
	args, err := d.dataExec(ctx, id, p, p.Services[i].BackupArgv, false)
	if err != nil {
		return err
	}
	return d.stream(ctx, nil, w, args...)
}

// Load restores a backup read from r into a hosted instance with the profile's restore command.
func (d *dockerRuntime) Load(ctx context.Context, id string, p PreviewProfileV1, r io.Reader) error {
	i := p.dataService()
	if i < 0 {
		return ErrUnavailable
	}
	args, err := d.dataExec(ctx, id, p, p.Services[i].RestoreArgv, true)
	if err != nil {
		return err
	}
	return d.stream(ctx, r, io.Discard, args...)
}

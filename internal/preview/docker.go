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
	"time"
)

type dockerCommand func(context.Context, ...string) (string, error)
type presentation struct {
	server                *http.Server
	transport             *http.Transport
	url, endpoint, handle string
}
type dockerRuntime struct {
	mu              sync.Mutex
	root, namespace string
	run             dockerCommand
	approved        map[string]bool
	groups          map[string]*presentation
}

func newDocker(root string) *dockerRuntime {
	d := &dockerRuntime{root: root, namespace: digest([]byte(root)), approved: map[string]bool{}, groups: map[string]*presentation{}}
	d.run = func(ctx context.Context, args ...string) (string, error) {
		// Ignore ambient Docker contexts, endpoints, registry credentials and proxy
		// settings. The socket is controller-only; never a candidate mount.
		return execute(ctx, "/usr/bin/docker", []string{"PATH=/usr/bin:/bin", "LANG=C", "HOME=" + root}, append([]string{"--host=unix:///var/run/docker.sock", "--config=" + filepath.Join(root, "docker-config")}, args...)...)
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
func cleanEnv(s ServiceProfileV1) []string {
	out := []string{"-i", "PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/scratch", "TMPDIR=/scratch"}
	return append(out, environmentValues(s.Environment)...)
}
func scratchOptions(p PreviewProfileV1, s ServiceProfileV1) string {
	uid, gid, _ := strings.Cut(s.User, ":")
	return "rw,nosuid,nodev,noexec,size=" + strconv.FormatInt(p.TmpfsBytes, 10) + ",mode=700,uid=" + uid + ",gid=" + gid
}
func (d *dockerRuntime) createArgs(id, path string, p PreviewProfileV1, s ServiceProfileV1, i int) []string {
	args := []string{"create", "--pull=never", "--name", d.container(id, i), "--label", d.label(), "--label", "abcp.preview.id=" + id, "--network", d.network(id), "--network-alias", s.Name, "--user", s.User, "--cap-drop=ALL", "--security-opt=no-new-privileges", "--read-only", "--cpu-period=100000", "--cpu-quota=" + strconv.Itoa(p.CPUQuota), "--memory=" + strconv.FormatInt(p.MemoryBytes, 10), "--memory-swap=" + strconv.FormatInt(p.MemoryBytes, 10), "--pids-limit=" + strconv.Itoa(p.PidsLimit), "--ulimit=nofile=1024:1024", "--restart=no", "--ipc=none", "--log-driver=none", "--no-healthcheck", "--workdir=/scratch", "--tmpfs", "/scratch:" + scratchOptions(p, s), "--entrypoint=/usr/bin/env"}
	if s.MountSource {
		args = append(args, "--mount", "type=bind,src="+path+",dst=/source,readonly,bind-recursive=readonly")
	}
	args = append(args, s.Image)
	args = append(args, cleanEnv(s)...)
	return append(args, "/bin/sleep", strconv.Itoa(p.TTLSeconds))
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
func (d *dockerRuntime) endpoint(ctx context.Context, id string, p PreviewProfileV1, s ServiceProfileV1, i int) (string, error) {
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
			Type, Source, Destination string
			RW                        bool
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
	sourceFound := false
	for _, m := range v.Mounts {
		if m.Type == "tmpfs" && m.Destination == "/scratch" {
			continue
		}
		if m.Type != "bind" || m.Destination != "/source" || m.RW || !s.MountSource || m.Source != filepath.Join(d.root, "sources", id) || sourceFound {
			return "", ErrUnavailable
		}
		sourceFound = true
	}
	if s.MountSource != sourceFound {
		return "", ErrUnavailable
	}
	network, ok := v.NetworkSettings.Networks[d.network(id)]
	ip := net.ParseIP(network.IPAddress)
	if !ok || ip == nil || ip.To4() == nil || !ip.IsPrivate() || ip.IsLoopback() {
		return "", ErrUnavailable
	}
	return net.JoinHostPort(ip.String(), strconv.Itoa(s.Port)), nil
}
func (d *dockerRuntime) startGroup(ctx context.Context, id, path string, p PreviewProfileV1, probe bool) (*presentation, error) {
	if !sha256Pattern.MatchString(id) || p.Validate() != nil {
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
	g := &presentation{handle: jsonDigest([]string{d.namespace, id, "route"})}
	for i, s := range p.Services {
		if _, err := d.run(ctx, d.createArgs(id, path, p, s, i)...); err != nil {
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
		endpoint, err := d.endpoint(ctx, id, p, s, i)
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
		r.Header.Del("Cookie")
		r.Header.Del("Forwarded")
		r.Header.Del("X-Forwarded-Host")
		r.Header.Del("X-Forwarded-For")
	}
	proxy.ModifyResponse = func(r *http.Response) error { r.Header.Del("Set-Cookie"); return nil }
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
	go func() { _ = g.server.Serve(listener) }()
	return nil
}
func (d *dockerRuntime) Start(ctx context.Context, id, path string, p PreviewProfileV1) (string, error) {
	if !sha256Pattern.MatchString(id) || path != filepath.Join(d.root, "sources", id) || strings.ContainsAny(path, ",\x00\r\n") {
		return "", ErrUnavailable
	}
	if !d.Available(p) {
		return "", ErrUnavailable
	}
	g, err := d.startGroup(ctx, id, path, p, false)
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
		endpoint, err := d.endpoint(ctx, id, p, s, i)
		if err != nil || s.Presented && endpoint != g.endpoint {
			return unsafe()
		}
	}
	return requestHealth(ctx, g.url, p.HealthPath, false), nil
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
	defer func() {
		err := d.Stop(context.Background(), id)
		if err == nil && sourceCreated {
			err = checkout.Remove(id)
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
	g, err := d.startGroup(ctx, id, path, p, true)
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

package preview

import (
	"context"
	"encoding/json"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Stack sandboxes (docs/plans/stack-jobs.md; Repo C design note IMPROVE_EXISTING_PRODUCTS §13): a customer's code,
// installed, built and tested in one container per part, on the preview runtime's walls: gVisor, a private network of
// the preview range that reaches only the egress proxy, which lets through only the stack's package registries, no
// capabilities, a read-only root and limits. Nothing here is a preview: no presentation, no record in the preview store.

// StackSpec is what one part runs in, from the operator's stack profile; nothing in it comes from a caller.
type StackSpec struct {
	Image       string
	CPUQuota    int
	MemoryBytes int64
	PidsLimit   int
	TmpfsBytes  int64
	Lifetime    time.Duration
	Egress      []string
	// Hosts are the names of the job's own private registries (Repo C IMPROVE_EXISTING_PRODUCTS §15.9), from its request
	// and checked there: reached on 443 at public addresses only, as the registry rules' names are.
	Hosts []string
	// OSVData is the operator's vulnerability data folder (Repo C SECURITY_AND_COMPLIANCE §11.1), bound read-only at /osv
	// for a scan job's parts only; "" binds nothing.
	OSVData string
}

// Valid reports a spec ABCP may run: a pinned image, bounded limits, and registries only.
func (s StackSpec) Valid() bool {
	if !imagePattern.MatchString(s.Image) || s.CPUQuota < 10000 || s.CPUQuota > 400000 || s.MemoryBytes < 256<<20 || s.MemoryBytes > 16<<30 ||
		s.PidsLimit < 64 || s.PidsLimit > 4096 || s.TmpfsBytes < 16<<20 || s.TmpfsBytes > 2<<30 || s.Lifetime < time.Minute || s.Lifetime > 2*time.Hour ||
		len(s.Egress) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, rule := range s.Egress {
		if seen[rule] || !RegistryRule(rule) {
			return false
		}
		seen[rule] = true
	}
	if len(s.Hosts) > 3 {
		return false
	}
	for _, host := range s.Hosts {
		if _, err := netip.ParseAddr(host); err == nil || seen[host] || !stackHost.MatchString(host) {
			return false
		}
		seen[host] = true
	}
	return true
}

// stackHost is a lower-case DNS name with a dot in it.
var stackHost = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// StackSession is one part's container, its network and its egress proxy, until Close.
type StackSession struct {
	d       *dockerRuntime
	id      string
	gateway netip.Addr
	egress  *egressProxy
	once    sync.Once
	closed  atomic.Bool
}

// stackPath is a folder a stack container may bind: absolute, clean, and nothing Docker's mount syntax would read as more.
var stackPath = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

// stackUser is this controller's own uid:gid, which the work and cache folders belong to; never root.
func stackUser() (string, bool) {
	uid, gid := os.Getuid(), os.Getgid()
	if uid <= 0 || gid <= 0 {
		return "", false
	}
	return strconv.Itoa(uid) + ":" + strconv.Itoa(gid), true
}

// OpenStack starts the container for one part of a stack job: id is the part's own digest, work and cache the folders
// it may write (bound at /work and /cache). The network, the proxy and the container are removed by Close, or by the
// next start's cleanup if this process ends first.
func (s *Service) OpenStack(ctx context.Context, id string, spec StackSpec, work, cache string) (*StackSession, error) {
	if !s.Available() {
		return nil, ErrUnavailable
	}
	d, ok := s.runtime.(*dockerRuntime)
	if !ok {
		return nil, ErrUnavailable
	}
	return d.openStack(ctx, id, spec, work, cache)
}

func (d *dockerRuntime) openStack(ctx context.Context, id string, spec StackSpec, work, cache string) (_ *StackSession, err error) {
	user, ok := stackUser()
	if !sha256Pattern.MatchString(id) || !spec.Valid() || !ok || !stackPath.MatchString(work) || !stackPath.MatchString(cache) ||
		filepath.Clean(work) != work || filepath.Clean(cache) != cache || strings.Contains(work, "..") || strings.Contains(cache, "..") ||
		(spec.OSVData != "" && !stackBindable(spec.OSVData)) {
		return nil, ErrUnavailable
	}
	if !d.runtimeAvailable(ctx, RuntimeGVisor) || !d.stackImageSafe(ctx, spec.Image) {
		return nil, ErrUnavailable
	}
	created := false
	for attempt := 0; attempt < 64 && !created && ctx.Err() == nil; attempt++ {
		_, err := d.run(ctx, "network", "create", "--internal", "--driver=bridge", "--subnet", previewSubnet(id, attempt).String(), "--label", d.label(), "--label", "abcp.stack.id="+id, d.network(id))
		created = err == nil
	}
	if !created {
		return nil, ErrUnavailable
	}
	x := &StackSession{d: d, id: id}
	defer func() {
		if err != nil {
			x.Close()
		}
	}()
	gateway, ok := d.networkGateway(ctx, id)
	if !ok {
		return nil, ErrUnavailable
	}
	x.gateway = gateway
	listener, err := d.listen("tcp4", netip.AddrPortFrom(gateway, EgressPort).String())
	if err != nil {
		return nil, ErrUnavailable
	}
	x.egress = newEgressProxy(listener, spec.Egress)
	x.egress.hosts = append([]string(nil), spec.Hosts...)
	x.egress.start()
	if _, err := d.run(ctx, d.stackCreateArgs(id, spec, user, work, cache)...); err != nil {
		return nil, ErrUnavailable
	}
	if _, err := d.run(ctx, "start", d.container(id, 0)); err != nil {
		return nil, ErrUnavailable
	}
	return x, nil
}

// stackCreateArgs is the part's `docker create`. The container only sleeps; each step is a `docker exec` with an
// environment made from nothing.
func (d *dockerRuntime) stackCreateArgs(id string, spec StackSpec, user, work, cache string) []string {
	args := []string{"create", "--pull=never", "--name", d.container(id, 0), "--label", d.label(), "--label", "abcp.stack.id=" + id,
		"--network", d.network(id), "--user", user, "--cap-drop=ALL", "--security-opt=no-new-privileges", "--read-only",
		"--cpu-period=100000", "--cpu-quota=" + strconv.Itoa(spec.CPUQuota), "--memory=" + strconv.FormatInt(spec.MemoryBytes, 10),
		"--memory-swap=" + strconv.FormatInt(spec.MemoryBytes, 10), "--pids-limit=" + strconv.Itoa(spec.PidsLimit), "--ulimit=nofile=4096:4096",
		"--restart=no", "--ipc=none", "--log-driver=none", "--no-healthcheck", "--workdir=/work",
		// exec: the code already runs from /work, and native builds run what they compile in /tmp (Docker's default is noexec).
		"--tmpfs", "/tmp:rw,exec,nosuid,nodev,size=" + strconv.FormatInt(spec.TmpfsBytes, 10) + ",mode=1777",
		"--mount", "type=bind,src=" + work + ",dst=/work,bind-propagation=rprivate",
		"--mount", "type=bind,src=" + cache + ",dst=/cache,bind-propagation=rprivate"}
	if spec.OSVData != "" {
		args = append(args, "--mount", "type=bind,src="+spec.OSVData+",dst=/osv,readonly,bind-propagation=rprivate")
	}
	return append(args, "--runtime="+RuntimeGVisor, "--entrypoint=/usr/bin/env", spec.Image,
		"-i", "PATH=/usr/bin:/bin", "/bin/sleep", strconv.Itoa(int(spec.Lifetime/time.Second)))
}

// stackBindable is a folder a stack container may bind beside /work and /cache: absolute, clean, nothing Docker's mount
// syntax would read as more, and a real folder, not a link.
func stackBindable(path string) bool {
	if !stackPath.MatchString(path) || filepath.Clean(path) != path || strings.Contains(path, "..") {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

// stackImageSafe checks the image is the pinned one, present locally, declaring no volumes. Its own environment does not
// matter: every step's environment is made from nothing.
func (d *dockerRuntime) stackImageSafe(ctx context.Context, image string) bool {
	raw, err := d.run(ctx, "image", "inspect", image)
	if err != nil {
		return false
	}
	var images []struct {
		RepoDigests []string
		Config      struct {
			Volumes map[string]json.RawMessage
		}
	}
	return json.Unmarshal([]byte(raw), &images) == nil && len(images) == 1 && len(images[0].Config.Volumes) == 0 && slices.Contains(images[0].RepoDigests, image)
}

// Proxy is the address of the part's egress proxy, for the tools' proxy settings.
func (x *StackSession) Proxy() netip.AddrPort { return netip.AddrPortFrom(x.gateway, EgressPort) }

// stackEnv is a name=value a step's environment may carry.
var stackEnv = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}=[^\x00\r\n]*$`)

// stackDir is a working folder under /work.
var stackDir = regexp.MustCompile(`^/work(?:/[A-Za-z0-9._-]+){0,8}$`)

// Exec runs one step in the part's container, in dir (under /work), with exactly env, writing what it prints to out, and
// answers its exit code. Ending ctx ends the step.
func (x *StackSession) Exec(ctx context.Context, dir string, env, argv []string, out io.Writer) (int, error) {
	if x.closed.Load() || x.d.attach == nil || !stackDir.MatchString(dir) || strings.Contains(dir, "..") || len(argv) == 0 || len(argv) > 64 {
		return 0, ErrUnavailable
	}
	args := []string{"exec", "--workdir", dir, x.d.container(x.id, 0), "/usr/bin/env", "-i"}
	for _, e := range env {
		if len(e) > 1100 || !stackEnv.MatchString(e) {
			return 0, ErrUnavailable
		}
		args = append(args, e)
	}
	for _, a := range argv {
		if strings.ContainsRune(a, 0) || len(a) > 4096 {
			return 0, ErrUnavailable
		}
	}
	return x.d.attach(ctx, out, append(args, argv...)...)
}

// Close ends the part: the proxy, the container and its network.
func (x *StackSession) Close() error {
	var err error
	x.once.Do(func() {
		x.closed.Store(true)
		if x.egress != nil {
			_ = x.egress.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err = x.d.removeObjects(ctx, "abcp.stack.id="+x.id)
	})
	return err
}

// EgressCounts is what the part's proxy did.
func (x *StackSession) EgressCounts() EgressCounts {
	if x.egress == nil {
		return EgressCounts{}
	}
	return x.egress.Counts()
}

// ProveStack is the stack profile's isolation probe, run once before its jobs (docs/plans/stack-jobs.md, "The probe"):
// in a part made from spec with work and cache as its folders, the image's /bin/busybox must find no default route,
// reach neither 1.1.1.1 nor the host, have the proxy refuse example.com and open the profile's first registry.
func (s *Service) ProveStack(ctx context.Context, id string, spec StackSpec, work, cache string) bool {
	x, err := s.OpenStack(ctx, id, spec, work, cache)
	if err != nil {
		return false
	}
	defer x.Close()
	return x.prove(ctx, spec)
}

func (x *StackSession) prove(ctx context.Context, spec StackSpec) bool {
	d := x.d
	listener, err := d.listen("tcp4", netip.AddrPortFrom(x.gateway, 0).String())
	if err != nil {
		return false
	}
	defer listener.Close()
	var reached atomic.Int32
	go func() {
		for {
			c, err := listener.Accept()
			if err != nil {
				return
			}
			reached.Add(1)
			c.Close()
		}
	}()
	box := []string{"exec", d.container(x.id, 0), "/usr/bin/env", "-i", "/bin/busybox"}
	if _, err := d.run(ctx, append(box, "wget", "--help")...); err != nil {
		return false
	}
	routes, err := d.run(ctx, append(box, "cat", "/proc/net/route")...)
	if err != nil || !strings.HasPrefix(routes, "Iface") {
		return false
	}
	for _, line := range strings.Split(routes, "\n")[1:] {
		if fields := strings.Fields(line); len(fields) > 1 && fields[1] == "00000000" {
			return false
		}
	}
	if _, err := d.run(ctx, append(box, "wget", "-T", "2", "-O", "/dev/null", "http://1.1.1.1/")...); err == nil {
		return false
	}
	if _, err := d.run(ctx, append(box, "wget", "-T", "2", "-O", "/dev/null", "http://"+listener.Addr().String()+"/")...); err == nil || reached.Load() != 0 {
		return false
	}
	connect := func(host string) string {
		script := "/bin/busybox printf 'CONNECT " + host + ":443 HTTP/1.1\\r\\nHost: " + host + ":443\\r\\n\\r\\n' | /bin/busybox nc -w 5 " + x.gateway.String() + " " + strconv.Itoa(EgressPort) + "; true"
		// A tunnel that opens stays open until nc's own timeout, which may end nc with an error: the shell answers 0.
		answer, _ := d.run(ctx, append(box, "sh", "-c", script)...)
		return answer
	}
	if !strings.HasPrefix(connect("example.com"), "HTTP/1.1 403 ") {
		return false
	}
	return strings.HasPrefix(connect(RegistryHosts(spec.Egress[0])[0]), "HTTP/1.1 200 ")
}

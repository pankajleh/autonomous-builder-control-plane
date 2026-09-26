//go:build linux

package preview

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

type dockerFixture struct {
	d          *dockerRuntime
	p          PreviewProfileV1
	id         string
	commands   [][]string
	containers map[string]string
	network    bool
	reject     func(context.Context, []string) error
	external   func()
}

func newDockerFixture(t *testing.T, p PreviewProfileV1) *dockerFixture {
	t.Helper()
	f := &dockerFixture{d: newDocker(t.TempDir()), p: p, id: strings.Repeat("c", 64), containers: map[string]string{}}
	f.d.run = f.run
	return f
}
func (f *dockerFixture) run(ctx context.Context, args ...string) (string, error) {
	f.commands = append(f.commands, append([]string(nil), args...))
	if f.reject != nil {
		if err := f.reject(ctx, args); err != nil {
			return "", err
		}
	}
	encoded := func(v any) (string, error) { b, e := json.Marshal(v); return string(b), e }
	switch args[0] {
	case "info":
		return `{"OSType":"linux","MemoryLimit":true,"SwapLimit":true,"CpuCfsQuota":true,"PidsLimit":true,"SecurityOptions":["name=seccomp,profile=builtin"]}`, nil
	case "image":
		return encoded([]any{map[string]any{"RepoDigests": []string{args[2]}, "Config": map[string]any{}}})
	case "network":
		switch args[1] {
		case "create":
			f.network = true
		case "inspect":
			return encoded([]any{map[string]any{"Internal": true, "Driver": "bridge", "Labels": map[string]string{"abcp.preview.owner": f.d.namespace}}})
		case "ls":
			if f.network {
				return strings.Repeat("f", 64), nil
			}
		case "rm":
			f.network = false
		}
	case "create":
		for i, arg := range args {
			if arg == "--name" {
				f.containers[args[i+1]] = strings.Repeat(strconv.Itoa(len(f.containers)+1), 64)
			}
		}
	case "inspect":
		i, err := strconv.Atoi(args[1][len(args[1])-1:])
		if err != nil {
			return "", err
		}
		s := f.p.Services[i]
		v := dockerInspection(f.d, f.id, f.p, s)
		if !s.MountSource {
			v["Mounts"] = []any{}
		}
		v["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)[f.d.network(f.id)] = map[string]string{"IPAddress": "10.88.0." + strconv.Itoa(i+2)}
		return encoded([]any{v})
	case "ps":
		ids := []string{}
		for _, id := range f.containers {
			ids = append(ids, id)
		}
		return strings.Join(ids, "\n"), nil
	case "rm":
		for name, id := range f.containers {
			if id == args[len(args)-1] {
				delete(f.containers, name)
			}
		}
	case "exec":
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "/proc/net/route") {
			return "Iface Destination Gateway Flags\neth0 0000580A 00000000 0001", nil
		}
		if strings.Contains(joined, "http://1.1.1.1/") {
			if f.external != nil {
				f.external()
			}
			return "", ErrUnavailable
		}
	}
	return "", nil
}
func connectPresentation(t *testing.T, g *presentation, address string) {
	t.Helper()
	target, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture replaces the network wire, while exercising the actual proxy
	// listener, health request and production isolation inspection.
	g.transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", target.Host)
	}
}
func TestDockerStartHealthIsolationAndCleanup(t *testing.T) {
	p := testProfile()
	p.Services[0].PrepareArgv = []string{"/usr/bin/node", "/source/prepare.js"}
	sibling := p.Services[0]
	sibling.Name = "db"
	sibling.Presented = false
	sibling.MountSource = false
	sibling.PrepareArgv = nil
	p.Services = append(p.Services, sibling)
	f := newDockerFixture(t, p)
	f.d.approved[p.Digest()] = true
	route, err := f.d.Start(context.Background(), f.id, filepath.Join(f.d.root, "sources", f.id), p)
	if err != nil || !sha256Pattern.MatchString(route) {
		t.Fatal("start", route, err)
	}
	t.Cleanup(func() {
		if err := f.d.Stop(context.Background(), f.id); err != nil {
			t.Error(err)
		}
	})
	for i, s := range p.Services {
		expected := append([]string{"exec", "--detach", f.d.container(f.id, i), "/usr/bin/env"}, cleanEnv(s)...)
		expected = append(expected, s.StartArgv...)
		found := false
		for _, args := range f.commands {
			if reflect.DeepEqual(args, expected) {
				found = true
			}
		}
		if !found {
			t.Fatal("candidate argv not executed", expected)
		}
	}
	prepare := append([]string{"exec", f.d.container(f.id, 0), "/usr/bin/env"}, cleanEnv(p.Services[0])...)
	prepare = append(prepare, p.Services[0].PrepareArgv...)
	prepared, mounted := false, false
	for _, args := range f.commands {
		prepared = prepared || reflect.DeepEqual(args, prepare)
		mounted = mounted || strings.Contains(strings.Join(args, " "), "dst=/source,readonly,bind-recursive=readonly")
	}
	if !prepared || !mounted {
		t.Fatal("prepare/source mount missing")
	}
	g := f.d.groups[f.id]
	if g.endpoint != "10.88.0.2:8080" {
		t.Fatal("sibling presented", g.endpoint)
	}
	var healthy atomic.Bool
	healthy.Store(true)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(503)
		}
	}))
	defer target.Close()
	connectPresentation(t, g, target.URL)
	if ok, err := f.d.Healthy(context.Background(), f.id, p); !ok || err != nil {
		t.Fatal("healthy", ok, err)
	}
	healthy.Store(false)
	if ok, err := f.d.Healthy(context.Background(), f.id, p); ok || err != nil || !f.d.Available(p) {
		t.Fatal("application health revoked isolation", ok, err)
	}
	f.reject = func(_ context.Context, args []string) error {
		if args[0] == "inspect" && args[1] == f.d.container(f.id, 1) {
			return ErrIntegrity
		}
		return nil
	}
	if ok, err := f.d.Healthy(context.Background(), f.id, p); ok || !errors.Is(err, ErrIntegrity) || f.d.Available(p) {
		t.Fatal("sibling isolation drift accepted", ok, err)
	}
	if err := f.d.Stop(context.Background(), f.id); err != nil {
		t.Fatal(err)
	}
	if len(f.containers) != 0 || f.network || len(f.d.groups) != 0 {
		t.Fatal("runtime leaked")
	}
}
func TestDockerStartupFailuresRemovePartiallyCreatedObjects(t *testing.T) {
	for _, stage := range []string{"start", "prepare", "launch", "inspect"} {
		t.Run(stage, func(t *testing.T) {
			p := testProfile()
			p.Services[0].PrepareArgv = []string{"/usr/bin/node", "/source/prepare.js"}
			f := newDockerFixture(t, p)
			f.d.approved[p.Digest()] = true
			f.reject = func(_ context.Context, args []string) error {
				if args[0] == stage || stage == "prepare" && args[0] == "exec" && args[1] != "--detach" || stage == "launch" && args[0] == "exec" && args[1] == "--detach" {
					return ErrUnavailable
				}
				return nil
			}
			if _, err := f.d.Start(context.Background(), f.id, filepath.Join(f.d.root, "sources", f.id), p); err == nil {
				t.Fatal("failed stage accepted")
			}
			if len(f.containers) != 0 || f.network || len(f.d.groups) != 0 {
				t.Fatal("partial startup leaked")
			}
		})
	}
}
func TestDockerCanceledHealthDoesNotRevokeProfile(t *testing.T) {
	for _, stage := range []string{"network", "inspect"} {
		t.Run(stage, func(t *testing.T) {
			f := newDockerFixture(t, testProfile())
			f.d.approved[f.p.Digest()] = true
			f.d.groups[f.id] = &presentation{endpoint: "10.88.0.2:8080"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered := make(chan struct{})
			f.reject = func(ctx context.Context, args []string) error {
				if args[0] == stage {
					close(entered)
					<-ctx.Done()
					return ErrUnavailable
				}
				return nil
			}
			done := make(chan error, 1)
			go func() { _, err := f.d.Healthy(ctx, f.id, f.p); done <- err }()
			<-entered
			cancel()
			if err := <-done; !errors.Is(err, context.Canceled) || !f.d.Available(f.p) {
				t.Fatal("normal cancellation revoked profile", err)
			}
		})
	}
}
func TestDockerProbeExercisesSourceMountAndCleansIt(t *testing.T) {
	for _, rejectMount := range []bool{false, true} {
		t.Run(strconv.FormatBool(rejectMount), func(t *testing.T) {
			f := newDockerFixture(t, testProfile())
			f.id = jsonDigest([]string{f.d.namespace, f.p.Digest(), "isolation-probe"})
			path := filepath.Join(f.d.root, "sources", f.id)
			sawMount := false
			f.reject = func(_ context.Context, args []string) error {
				if args[0] == "create" {
					sawMount = strings.Contains(strings.Join(args, " "), "src="+path+",dst=/source,readonly,bind-recursive=readonly")
					if _, err := os.Stat(path); err != nil {
						t.Error("probe source absent", err)
					}
					if rejectMount && sawMount {
						return ErrUnavailable
					}
				}
				return nil
			}
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
			defer target.Close()
			f.external = func() { connectPresentation(t, f.d.groups[f.id], target.URL) }
			proved := f.d.prove(context.Background(), f.p)
			if !sawMount || proved == rejectMount || f.d.Available(f.p) != proved {
				t.Fatal("incorrect mount proof", sawMount, proved)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatal("probe source remains", err)
			}
			if len(f.containers) != 0 || f.network || len(f.d.groups) != 0 {
				t.Fatal("probe runtime leaked")
			}
		})
	}
}

func TestDockerProbeCleanupFailureCannotApproveProfile(t *testing.T) {
	f := newDockerFixture(t, testProfile())
	f.id = jsonDigest([]string{f.d.namespace, f.p.Digest(), "isolation-probe"})
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer target.Close()
	f.external = func() { connectPresentation(t, f.d.groups[f.id], target.URL) }
	f.reject = func(_ context.Context, args []string) error {
		if args[0] == "rm" {
			return ErrUnavailable
		}
		return nil
	}
	if f.d.prove(context.Background(), f.p) || f.d.Available(f.p) {
		t.Fatal("failed probe cleanup advertised capability")
	}
	checkout := Checkout{Root: filepath.Join(f.d.root, "sources")}
	if _, err := os.Stat(filepath.Join(checkout.Root, f.id)); err != nil {
		t.Fatal("source removed while probe containers remain", err)
	}
	f.reject = nil
	if err := f.d.Stop(context.Background(), f.id); err != nil {
		t.Fatal(err)
	}
	if err := checkout.Remove(f.id); err != nil {
		t.Fatal(err)
	}
}

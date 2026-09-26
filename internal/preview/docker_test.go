package preview

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func dockerInspection(d *dockerRuntime, id string, p PreviewProfileV1, s ServiceProfileV1) map[string]any {
	return map[string]any{
		"State":           map[string]any{"Running": true},
		"Config":          map[string]any{"User": s.User, "Labels": map[string]string{"abcp.preview.owner": d.namespace, "abcp.preview.id": id}},
		"HostConfig":      map[string]any{"CpuPeriod": 100000, "IpcMode": "none", "Tmpfs": map[string]string{"/scratch": scratchOptions(p, s)}, "ReadonlyRootfs": true, "NetworkMode": d.network(id), "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges"}, "CpuQuota": p.CPUQuota, "Memory": p.MemoryBytes, "MemorySwap": p.MemoryBytes, "PidsLimit": p.PidsLimit},
		"NetworkSettings": map[string]any{"Networks": map[string]any{d.network(id): map[string]string{"IPAddress": "10.88.0.2"}}, "Ports": map[string]any{"8080/tcp": nil}},
		"Mounts":          []map[string]any{{"Type": "bind", "Destination": "/source", "Source": filepath.Join(d.root, "sources", id), "RW": false}},
	}
}
func TestDockerStructuredIsolationAndInjectionBoundary(t *testing.T) {
	d := newDocker("/private/previews")
	p := testProfile()
	id := strings.Repeat("c", 64)
	args := d.createArgs(id, filepath.Join(d.root, "sources", id), p, p.Services[0], 0)
	joined := strings.Join(args, " ")
	for _, required := range []string{"--pull=never", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--read-only", "--cpu-quota=10000", "--memory=67108864", "--memory-swap=67108864", "--pids-limit=32", "--restart=no", "--log-driver=none", "--ipc=none", "--entrypoint=/usr/bin/env", "1000:1000", "dst=/source,readonly,bind-recursive=readonly", "noexec,size=8388608", "/bin/sleep 60"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing %s", required)
		}
	}
	for _, forbidden := range []string{"--privileged", "--publish", "--network=host", "docker.sock", "--env-file"} {
		if strings.Contains(joined, forbidden) {
			t.Fatal("unsafe Docker argv", forbidden)
		}
	}
	if d.Available(p) {
		t.Fatal("unproved host advertised capability")
	}
	if _, err := d.Start(context.Background(), id, "/host/secret", p); err == nil {
		t.Fatal("unproved start accepted")
	}
	d.approved[p.Digest()] = true
	called := false
	d.run = func(context.Context, ...string) (string, error) { called = true; return "", nil }
	if _, err := d.Start(context.Background(), id, "/host/secret", p); err == nil || called {
		t.Fatal("host path accepted")
	}
}
func TestDockerRejectsIsolationDrift(t *testing.T) {
	cases := map[string]func(map[string]any){
		"privileged":    func(v map[string]any) { v["HostConfig"].(map[string]any)["Privileged"] = true },
		"writable-root": func(v map[string]any) { v["HostConfig"].(map[string]any)["ReadonlyRootfs"] = false },
		"host-network":  func(v map[string]any) { v["HostConfig"].(map[string]any)["NetworkMode"] = "host" },
		"second-network": func(v map[string]any) {
			v["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)["bridge"] = map[string]string{"IPAddress": "172.17.0.2"}
		},
		"root":             func(v map[string]any) { v["Config"].(map[string]any)["User"] = "0:0" },
		"missing-cap-drop": func(v map[string]any) { v["HostConfig"].(map[string]any)["CapDrop"] = []string{} },
		"unbounded-memory": func(v map[string]any) { v["HostConfig"].(map[string]any)["Memory"] = 0 },
		"host-bind":        func(v map[string]any) { v["HostConfig"].(map[string]any)["Binds"] = []string{"/:/host"} },
		"published-port": func(v map[string]any) {
			v["NetworkSettings"].(map[string]any)["Ports"].(map[string]any)["8080/tcp"] = []string{"0.0.0.0:8080"}
		},
		"writable-source": func(v map[string]any) { v["Mounts"].([]map[string]any)[0]["RW"] = true },
		"socket-mount":    func(v map[string]any) { v["Mounts"].([]map[string]any)[0]["Source"] = "/var/run/docker.sock" },
		"no-ip":           func(v map[string]any) { v["NetworkSettings"].(map[string]any)["Networks"] = map[string]any{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := newDocker("/private/previews")
			p := testProfile()
			id := strings.Repeat("c", 64)
			v := dockerInspection(d, id, p, p.Services[0])
			good, _ := json.Marshal([]any{v})
			d.run = func(context.Context, ...string) (string, error) { return string(good), nil }
			if _, err := d.endpoint(context.Background(), id, p, p.Services[0], 0); err != nil {
				t.Fatal("baseline isolation rejected", err)
			}
			mutate(v)
			raw, _ := json.Marshal([]any{v})
			d.run = func(context.Context, ...string) (string, error) { return string(raw), nil }
			if _, err := d.endpoint(context.Background(), id, p, p.Services[0], 0); err == nil {
				t.Fatal("isolation drift accepted")
			}
		})
	}
}
func TestDockerRejectsImageSecretsAndAnonymousVolumes(t *testing.T) {
	p := testProfile()
	for _, scenario := range []string{"token", "preload", "volume", "wrong-digest", "valid"} {
		t.Run(scenario, func(t *testing.T) {
			d := newDocker("/private/previews")
			config := map[string]any{"Env": []string{"PATH=/usr/bin:/bin"}}
			digests := []string{p.Services[0].Image}
			switch scenario {
			case "token":
				config["Env"] = []string{"AWS_SECRET_ACCESS_KEY=secret"}
			case "preload":
				config["Env"] = []string{"LD_PRELOAD=/bad.so"}
			case "volume":
				config["Volumes"] = map[string]any{"/data": map[string]any{}}
			case "wrong-digest":
				digests = []string{"node:latest"}
			}
			raw, _ := json.Marshal([]any{map[string]any{"RepoDigests": digests, "Config": config}})
			d.run = func(context.Context, ...string) (string, error) { return string(raw), nil }
			if d.imageSafe(context.Background(), p.Services[0]) != (scenario == "valid") {
				t.Fatal("incorrect image acceptance")
			}
		})
	}
}
func TestControllerProxyBindsLoopbackAndOnlyDialsPresentedService(t *testing.T) {
	var mu sync.Mutex
	var receivedHost, auth, cookie, path string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		receivedHost = r.Host
		auth = r.Header.Get("Authorization")
		cookie = r.Header.Get("Cookie")
		path = r.URL.Path
		mu.Unlock()
		w.Header().Set("Set-Cookie", "private=value")
		w.Write([]byte("candidate"))
	}))
	defer target.Close()
	parsed, _ := url.Parse(target.URL)
	g := &presentation{endpoint: parsed.Host}
	if err := g.present(); err != nil {
		t.Fatal(err)
	}
	defer g.server.Close()
	if !strings.HasPrefix(g.url, "http://127.0.0.1:") {
		t.Fatal("proxy is not explicit loopback", g.url)
	}
	req, _ := http.NewRequest(http.MethodGet, g.url+"/test", nil)
	req.Host = "attacker.example"
	req.Header.Set("Authorization", "Bearer controller-secret")
	req.Header.Set("Cookie", "session=secret")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "candidate" || response.Header.Get("Set-Cookie") != "" {
		t.Fatal("bad proxy response")
	}
	mu.Lock()
	defer mu.Unlock()
	if receivedHost != g.endpoint || auth != "" || cookie != "" || path != "/test" {
		t.Fatal("proxy forwarded authority or destination", receivedHost, path)
	}
}
func TestDockerOrphanCleanupUsesBothOwnershipFilters(t *testing.T) {
	d := newDocker("/private/previews")
	id := strings.Repeat("c", 64)
	container := strings.Repeat("a", 64)
	network := strings.Repeat("b", 64)
	var commands [][]string
	d.run = func(_ context.Context, args ...string) (string, error) {
		commands = append(commands, args)
		if args[0] == "ps" {
			return container, nil
		}
		if args[0] == "network" && args[1] == "ls" {
			return network, nil
		}
		return "", nil
	}
	if err := d.Stop(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 4 {
		t.Fatal(commands)
	}
	for _, index := range []int{0, 2} {
		joined := strings.Join(commands[index], " ")
		if !strings.Contains(joined, "label="+d.label()) || !strings.Contains(joined, "label=abcp.preview.id="+id) {
			t.Fatal("cleanup escaped namespace", joined)
		}
	}
	if strings.Join(commands[1], " ") != "rm --force --volumes "+container || strings.Join(commands[3], " ") != "network rm "+network {
		t.Fatal("cleanup did not remove objects")
	}
}
func TestIsolationProbeFailsClosedWhenHostPresentationUnavailable(t *testing.T) {
	d := newDocker("/private/previews")
	p := testProfile()
	d.run = func(_ context.Context, args ...string) (string, error) {
		if args[0] == "ps" || args[0] == "network" && args[1] == "ls" {
			return "", nil
		}
		return "", ErrUnavailable
	}
	if d.prove(context.Background(), p) || d.Available(p) {
		t.Fatal("unsupported host advertised preview")
	}
}

func TestHostInternalNetworkWithoutReachableLoopbackRemainsUnavailable(t *testing.T) {
	d := newDocker("/private/previews")
	p := testProfile()
	p.Services[0].MountSource = false
	id := jsonDigest([]string{d.namespace, p.Digest(), "isolation-probe"})
	egressAttempted := false
	d.run = func(_ context.Context, args ...string) (string, error) {
		switch args[0] {
		case "info":
			return `{"OSType":"linux","MemoryLimit":true,"SwapLimit":true,"CpuCfsQuota":true,"PidsLimit":true,"SecurityOptions":["name=seccomp,profile=builtin"]}`, nil
		case "image":
			b, _ := json.Marshal([]any{map[string]any{"RepoDigests": []string{p.Services[0].Image}, "Config": map[string]any{}}})
			return string(b), nil
		case "network":
			if args[1] == "inspect" {
				b, _ := json.Marshal([]any{map[string]any{"Internal": true, "Driver": "bridge", "Labels": map[string]string{"abcp.preview.owner": d.namespace}}})
				return string(b), nil
			}
		case "inspect":
			v := dockerInspection(d, id, p, p.Services[0])
			v["Mounts"] = []any{}
			b, _ := json.Marshal([]any{v})
			return string(b), nil
		case "exec":
			joined := strings.Join(args, " ")
			if strings.Contains(joined, "/proc/net/route") {
				return "Iface Destination Gateway Flags\neth0 0000580A 00000000 0001", nil
			}
			if strings.Contains(joined, "http://1.1.1.1/") {
				egressAttempted = true
				return "", ErrUnavailable
			}
		}
		return "", nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if d.prove(ctx, p) || d.Available(p) || !egressAttempted {
		t.Fatal("blocked egress alone enabled preview")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.groups) != 0 {
		t.Fatal("probe route was not removed")
	}
}

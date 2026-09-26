package preview

import (
	"encoding/json"
	"strings"
	"testing"
)

func testProfile() PreviewProfileV1 {
	return PreviewProfileV1{SchemaVersion: 1, ProfileID: "web-v1", RepositoryIdentityDigest: strings.Repeat("a", 64), Services: []ServiceProfileV1{{Name: "web", Image: "local/web@sha256:" + strings.Repeat("b", 64), User: "1000:1000", StartArgv: []string{"/usr/bin/node", "/source/server.js"}, Environment: map[string]string{"NODE_ENV": "development"}, MountSource: true, Port: 8080, Presented: true}}, HealthPath: "/health", HealthTimeoutSeconds: 1, TTLSeconds: 60, CPUQuota: 10000, MemoryBytes: 64 << 20, PidsLimit: 32, TmpfsBytes: 8 << 20, NetworkPolicy: "INTERNAL_ONLY"}
}
func cloneProfile(p PreviewProfileV1) PreviewProfileV1 {
	b, _ := json.Marshal(p)
	var out PreviewProfileV1
	_ = json.Unmarshal(b, &out)
	return out
}
func TestProfileRejectsUnboundedAndInjectedExecution(t *testing.T) {
	cases := map[string]func(*PreviewProfileV1){
		"mutable-image":      func(p *PreviewProfileV1) { p.Services[0].Image = "node:latest" },
		"shell":              func(p *PreviewProfileV1) { p.Services[0].StartArgv = []string{"/bin/sh", "-c", "id"} },
		"shell-text":         func(p *PreviewProfileV1) { p.Services[0].StartArgv = []string{"/usr/bin/node", "-e", "process.exit()"} },
		"shell-substitution": func(p *PreviewProfileV1) { p.Services[0].StartArgv[1] = "$(cat /secret)" },
		"busybox-shell":      func(p *PreviewProfileV1) { p.Services[0].StartArgv = []string{"/bin/busybox", "sh"} },
		"socket":             func(p *PreviewProfileV1) { p.Services[0].StartArgv[1] = "/var/run/docker.sock" },
		"root":               func(p *PreviewProfileV1) { p.Services[0].User = "0:0" },
		"cpu":                func(p *PreviewProfileV1) { p.CPUQuota = 0 }, "memory": func(p *PreviewProfileV1) { p.MemoryBytes = 0 }, "pids": func(p *PreviewProfileV1) { p.PidsLimit = 0 }, "scratch": func(p *PreviewProfileV1) { p.TmpfsBytes = 0 }, "ttl": func(p *PreviewProfileV1) { p.TTLSeconds = 3601 }, "health": func(p *PreviewProfileV1) { p.HealthTimeoutSeconds = 0 },
		"host-network":     func(p *PreviewProfileV1) { p.NetworkPolicy = "HOST" },
		"secret-env":       func(p *PreviewProfileV1) { p.Services[0].Environment["API_TOKEN"] = "secret" },
		"secret-value":     func(p *PreviewProfileV1) { p.Services[0].Environment["NODE_ENV"] = "password=abc" },
		"preload":          func(p *PreviewProfileV1) { p.Services[0].Environment["LD_PRELOAD"] = "/source/code.so" },
		"no-presented":     func(p *PreviewProfileV1) { p.Services[0].Presented = false },
		"double-presented": func(p *PreviewProfileV1) { s := p.Services[0]; s.Name = "db"; p.Services = append(p.Services, s) },
		"too-many": func(p *PreviewProfileV1) {
			for i := 0; i < 4; i++ {
				p.Services = append(p.Services, p.Services[0])
			}
		},
		"route-injection": func(p *PreviewProfileV1) { p.HealthPath = "//outside/" },
		"repo":            func(p *PreviewProfileV1) { p.RepositoryIdentityDigest = "anything" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			p := testProfile()
			change(&p)
			if p.Validate() == nil {
				t.Fatal("unsafe profile accepted")
			}
		})
	}
	p := testProfile()
	if p.Validate() != nil {
		t.Fatal("valid profile rejected")
	}
	copy := cloneProfile(p)
	if copy.Digest() != p.Digest() {
		t.Fatal("unstable canonical profile digest")
	}
	copy.TTLSeconds++
	if copy.Digest() == p.Digest() {
		t.Fatal("policy edit did not change digest")
	}
}

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

// A profile opts in to app cookies with app_cookies; without it the JSON, and so the digest, is what it was before the
// field existed, and a file naming it loads.
func TestAppCookiesIsOptInAndLeavesOtherProfilesDigestsAlone(t *testing.T) {
	p := testProfile()
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), "app_cookies") {
		t.Fatal("a profile without app cookies must not mention them", string(b))
	}
	before := p.Digest()
	p.AppCookies = true
	if p.Validate() != nil || p.Digest() == before {
		t.Fatal("app_cookies is a valid, distinct profile")
	}
	var decoded PreviewProfileV1
	if err := json.Unmarshal([]byte(strings.Replace(string(b), `"network_policy":"INTERNAL_ONLY"`, `"network_policy":"INTERNAL_ONLY","app_cookies":true`, 1)), &decoded); err != nil || !decoded.AppCookies {
		t.Fatal("app_cookies decodes", err)
	}
}

// A hosted app runs its own timetable (Repo C design note BF08_NO_PAGE_OPEN §19): only a hosted profile may say so, only
// as "run", and an image never brings it.
func TestOnlyAHostedProfileRunsTheAppsTimetable(t *testing.T) {
	hosted := testProfile()
	hosted.Hosted = true
	before := hosted.Digest()
	hosted.Services[0].Environment["AUTOBUILD_JOBS"] = "run"
	if hosted.Validate() != nil {
		t.Fatal("a hosted profile may run the app's timetable")
	}
	if hosted.Digest() == before {
		t.Fatal("the timetable switch is part of the profile's digest")
	}
	if got := strings.Join(cleanEnv(hosted.Services[0]), " "); !strings.Contains(got, "AUTOBUILD_JOBS=run") {
		t.Fatal("the switch reaches the service's environment", got)
	}
	preview := testProfile()
	preview.Services[0].Environment["AUTOBUILD_JOBS"] = "run"
	if preview.Validate() == nil {
		t.Fatal("a preview ran the app's timetable")
	}
	for _, value := range []string{"", "yes", "RUN", "run ", "1"} {
		bad := testProfile()
		bad.Hosted = true
		bad.Services[0].Environment["AUTOBUILD_JOBS"] = value
		if bad.Validate() == nil {
			t.Fatalf("AUTOBUILD_JOBS=%q accepted", value)
		}
	}
}

// A preview may hand the app a sample first-administrator token (Repo C design note TRYING_APPS.md); a hosted profile may
// not, and the value must look like a sample token, never anything that could carry a secret, a URL or an argument.
func TestOnlyAPreviewTakesASampleSetupToken(t *testing.T) {
	p := testProfile()
	p.Services[0].Environment["BOOTSTRAP_TOKEN"] = "try-autobuild"
	if p.Validate() != nil {
		t.Fatal("a preview's sample setup token rejected")
	}
	before := testProfile().Digest()
	if p.Digest() == before {
		t.Fatal("the token is part of the profile's digest")
	}
	hosted := testProfile()
	hosted.Hosted = true
	if hosted.Validate() != nil {
		t.Fatal("the hosted test profile is valid without a token")
	}
	hosted.Services[0].Environment["BOOTSTRAP_TOKEN"] = "try-autobuild"
	if hosted.Validate() == nil {
		t.Fatal("a hosted profile took a setup token")
	}
	for _, value := range []string{"", "short", "Try-Autobuild", "-leading-dash", "with space", "a=b&c=d", "https://x.example", strings.Repeat("a", 41)} {
		bad := testProfile()
		bad.Services[0].Environment["BOOTSTRAP_TOKEN"] = value
		if bad.Validate() == nil {
			t.Fatalf("setup token %q accepted", value)
		}
	}
	if !validEnvironment(map[string]string{"BOOTSTRAP_TOKEN": strings.Repeat("a", 40)}, false) || validEnvironment(map[string]string{"BOOTSTRAP_TOKEN": "try-autobuild"}, true) {
		t.Fatal("40 characters is the longest token; an image's own environment never takes one")
	}
	if got := strings.Join(cleanEnv(p.Services[0]), " "); got != "-i PATH=/usr/local/bin:/usr/bin:/bin HOME=/scratch TMPDIR=/scratch BOOTSTRAP_TOKEN=try-autobuild NODE_ENV=development" {
		t.Fatal("the token reaches the service's environment", got)
	}
}

func TestKeptDataProfilesHaveOneVolumeAndAreNotHosted(t *testing.T) {
	kept := testProfile()
	kept.ProfileID, kept.KeepData = "kept-v1", true
	kept.Services = append(kept.Services, ServiceProfileV1{Name: "db", Image: "local/db@sha256:" + strings.Repeat("f", 64), User: "999:999", StartArgv: []string{"/usr/local/bin/autobuild-postgres", "hosted"}, Environment: map[string]string{}, Port: 5432, DataVolume: true})
	if err := kept.Validate(); err != nil {
		t.Fatal("a kept-data profile", err)
	}
	cases := map[string]func(*PreviewProfileV1){
		"volume-without-keep-data": func(p *PreviewProfileV1) { p.KeepData = false },
		"keep-data-without-volume": func(p *PreviewProfileV1) { p.Services[1].DataVolume = false },
		"keep-data-hosted":         func(p *PreviewProfileV1) { p.Hosted = true },
		"backup-not-hosted": func(p *PreviewProfileV1) {
			p.Services[1].BackupArgv = []string{"/usr/bin/pg_dump"}
			p.Services[1].RestoreArgv = []string{"/usr/bin/pg_restore"}
		},
	}
	for name, change := range cases {
		p := cloneProfile(kept)
		change(&p)
		if p.Validate() == nil {
			t.Fatal(name)
		}
	}
	// Profiles without it keep their digests: the field is omitted when false.
	if strings.Contains(string(mustJSON(t, testProfile())), "keep_data") {
		t.Fatal("keep_data written for a profile without it")
	}
}
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

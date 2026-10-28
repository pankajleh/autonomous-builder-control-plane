package stackjob

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

const (
	npmToken  = "npm_registry_token_0123456789abcdef"
	mvnToken  = "maven-registry-token-0123456789"
	pyToken   = "python.registry.token+0123456789/="
	npmHost   = "acme-123456789012.d.codeartifact.ap-south-1.amazonaws.com"
	mavenHost = "maven.acme.example"
)

func testRegistries() []serviceapi.RegistryV1 {
	return []serviceapi.RegistryV1{
		{Kind: "npm", Use: "own", Scope: "@acme", URL: "https://" + npmHost + "/npm/acme", Token: npmToken},
		{Kind: "maven", Use: "all", URL: "https://" + mavenHost + "/repository/maven-public/", Username: "builder", Token: mvnToken},
		{Kind: "python", Use: "own", URL: "https://pypi.acme.example/simple", Username: "aws", Token: pyToken},
	}
}

func TestTheToolsSettingsNameEachRegistryWithItsTokenForItsOwnAddressOnly(t *testing.T) {
	rs := testRegistries()
	if got := npmrc(rs); got != "@acme:registry=https://"+npmHost+"/npm/acme/\n//"+npmHost+"/npm/acme/:_authToken="+npmToken+"\n" {
		t.Errorf("npmrc = %q", got)
	}
	all := []serviceapi.RegistryV1{{Kind: "npm", Use: "all", URL: "https://npm.acme.example/", Token: npmToken}}
	if got := npmrc(all); got != "registry=https://npm.acme.example/\n//npm.acme.example/:_authToken="+npmToken+"\n" {
		t.Errorf("npmrc all = %q", got)
	}
	if got := yarnrc(rs); !strings.Contains(got, "npmScopes:\n  \"acme\":\n    npmRegistryServer: \"https://"+npmHost+"/npm/acme\"\n    npmAuthToken: \""+npmToken+"\"") ||
		!strings.Contains(got, "npmRegistries:\n  \"//"+npmHost+"/npm/acme\":\n    npmAuthToken: \""+npmToken+"\"") || strings.Contains(got, "npmRegistryServer: \"https://"+npmHost+"\"\n") {
		t.Errorf("yarnrc = %q", got)
	}
	if got := yarnrc(all); !strings.HasPrefix(got, "npmRegistryServer: \"https://npm.acme.example\"\n") {
		t.Errorf("yarnrc all = %q", got)
	}
	encoded := strings.ReplaceAll(pyToken, "/", "%2F") // as Go writes a password in an address: "+" and "=" stay
	if got := pipConf(rs); got != "[global]\nextra-index-url =\n    https://aws:"+encoded+"@pypi.acme.example/simple\n" {
		t.Errorf("pip.conf = %q", got)
	}
	if got := uvToml(rs); got != "[[index]]\nurl = \"https://aws:"+encoded+"@pypi.acme.example/simple\"\n\n" {
		t.Errorf("uv.toml = %q", got)
	}
	pyAll := []serviceapi.RegistryV1{{Kind: "python", Use: "all", URL: "https://pypi.acme.example/simple/", Username: "aws", Token: "abcdefgh"}}
	if got := pipConf(pyAll); got != "[global]\nindex-url = https://aws:abcdefgh@pypi.acme.example/simple/\n" || !strings.Contains(uvToml(pyAll), "default = true") {
		t.Errorf("pip.conf all = %q, uv = %q", got, uvToml(pyAll))
	}
	proxy := netip.MustParseAddrPort("10.213.0.1:3129")
	settings := mavenSettings(proxy, rs)
	for _, want := range []string{
		"<settings><servers><server><id>abcp-registry-2</id><username>builder</username><password>" + mvnToken + "</password></server></servers>",
		"<mirrors><mirror><id>abcp-registry-2</id><mirrorOf>*</mirrorOf><url>https://" + mavenHost + "/repository/maven-public/</url></mirror></mirrors><proxies>",
		"<host>10.213.0.1</host><port>3129</port>",
	} {
		if !strings.Contains(settings, want) {
			t.Errorf("settings.xml lacks %q: %s", want, settings)
		}
	}
	if strings.Contains(settings, "<profiles>") || strings.Contains(mavenSettings(proxy, nil), "<server>") {
		t.Errorf("settings.xml = %s", settings)
	}
	own := []serviceapi.RegistryV1{{Kind: "maven", Use: "own", URL: "https://" + mavenHost + "/releases", Username: "builder", Token: mvnToken}}
	if got := mavenSettings(proxy, own); !strings.Contains(got, "<profiles><profile><id>abcp-registries</id><repositories><repository><id>abcp-registry-1</id><url>https://"+mavenHost+"/releases</url></repository></repositories><pluginRepositories><pluginRepository>") ||
		!strings.Contains(got, "<activeProfiles><activeProfile>abcp-registries</activeProfile></activeProfiles></settings>") || strings.Contains(got, "<mirror>") {
		t.Errorf("settings.xml own = %s", got)
	}
	gradle := gradleInit(rs)
	if !strings.Contains(gradle, "handler.maven { url = uri('https://"+mavenHost+"/repository/maven-public/'); credentials { username = 'builder'; password = '"+mvnToken+"' } }") ||
		!strings.Contains(gradle, "FAIL_ON_PROJECT_REPOS") || gradleInit(all) != "" {
		t.Errorf("gradle = %s", gradle)
	}
	if hosts := registryHosts(rs); strings.Join(hosts, ",") != npmHost+","+mavenHost+",pypi.acme.example" {
		t.Errorf("hosts = %v", hosts)
	}
}

func TestEveryFormOfATokenIsMasked(t *testing.T) {
	secrets := registrySecrets(testRegistries())
	basic := base64.StdEncoding.EncodeToString([]byte("aws:" + pyToken))
	text := "npm http fetch GET 200 //x/:_authToken=" + npmToken + "\nAuthorization: Basic " + basic + "\nLooking in indexes: https://aws:" +
		strings.ReplaceAll(pyToken, "/", "%2F") + "@pypi.acme.example/simple\n" + mvnToken + "\nindex https://aws:" +
		strings.NewReplacer("+", "%2B", "/", "%2F", "=", "%3D").Replace(pyToken) + "@pypi.acme.example\n"
	got := maskRegistries(text, secrets)
	for _, token := range []string{npmToken, mvnToken, pyToken, basic, "0123456789"} {
		if strings.Contains(got, token) {
			t.Errorf("%q left in %q", token, got)
		}
	}
	if strings.Count(got, RegistryMask) != 5 || maskRegistries("nothing here", nil) != "nothing here" {
		t.Errorf("masked = %q", got)
	}
}

func TestRegistrySettingsGoWithTheJobAndNeverFollowALink(t *testing.T) {
	cache, outside := t.TempDir(), t.TempDir()
	forget, err := writeRegistrySettings(cache, testRegistries())
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"home/.npmrc", "home/.yarnrc.yml", "home/.config/pip/pip.conf", "home/.config/uv/uv.toml", "home/.gradle/init.d/abcp-registries.gradle"} {
		if !realFile(cache, strings.Split(rel, "/")...) {
			t.Errorf("%s not written", rel)
		}
	}
	// The job's code replaced a folder with a link to somewhere else, which holds a file of the same name.
	write(t, outside, map[string]string{"pip/pip.conf": "not ABCP's"})
	if err := os.RemoveAll(filepath.Join(cache, "home", ".config")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cache, "home", ".config")); err != nil {
		t.Fatal(err)
	}
	write(t, cache, map[string]string{"home/.m2/settings.xml": "<settings/>"})
	forget()
	if data, err := os.ReadFile(filepath.Join(outside, "pip", "pip.conf")); err != nil || string(data) != "not ABCP's" {
		t.Fatalf("the link was followed: %q %v", data, err)
	}
	filepath.WalkDir(cache, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			t.Errorf("left %s", path)
		}
		return nil
	})
	if forget, err := writeRegistrySettings(cache, nil); err != nil || len(registryFiles(nil)) != 0 {
		t.Fatalf("nothing to write: %v", err)
	} else {
		forget()
	}
}

func TestAJobsRegistriesReachItsSandboxButNeverItsRecord(t *testing.T) {
	box := &fakeSandbox{proveOK: true}
	cache := ""
	seen := map[string]string{}
	box.exec = func(_ context.Context, work, dir string, argv []string, out io.Writer) (int, error) {
		data, _ := os.ReadFile(filepath.Join(cache, "home", ".npmrc"))
		seen[strings.Join(argv, " ")] = string(data)
		io.WriteString(out, "npm verb token "+npmToken+"\n")
		return 0, nil
	}
	f := newFixture(t, box)
	cache = filepath.Join(f.root, "caches", key)
	write(t, filepath.Join(f.sources, "proj-r"), map[string]string{"package.json": `{"name":"x","scripts":{"build":"true","test":"true"}}`, "package-lock.json": "{}"})
	request := serviceapi.StackJobCreateV1{RequestID: "req-r", Source: "proj-r", CacheKey: key, Registries: testRegistries()}
	queued, err := f.m.StartStackJob(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	job := f.wait(t, queued.ID)
	if job.State != Passed || len(seen) == 0 {
		t.Fatalf("job = %+v, seen %d", job, len(seen))
	}
	for command, npmrc := range seen {
		if !strings.Contains(npmrc, "//"+npmHost+"/npm/acme/:_authToken="+npmToken) {
			t.Errorf("%s ran without the registry: %q", command, npmrc)
		}
	}
	if len(box.specs) != 1 || strings.Join(box.specs[0].Hosts, ",") != npmHost+","+mavenHost+",pypi.acme.example" {
		t.Fatalf("specs = %+v", box.specs)
	}
	for _, s := range job.Parts[0].Steps {
		if strings.Contains(s.OutputTail, npmToken) || !strings.Contains(s.OutputTail, RegistryMask) {
			t.Errorf("step output %q", s.OutputTail)
		}
	}
	// Nothing ABCP keeps holds a token: not the record, not the cache.
	filepath.WalkDir(f.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, _ := os.ReadFile(path)
		for _, token := range []string{npmToken, mvnToken, pyToken} {
			if strings.Contains(string(data), token) {
				t.Errorf("%s holds a token", path)
			}
		}
		return nil
	})
	f.m.mu.Lock()
	left := len(f.m.registries)
	f.m.mu.Unlock()
	if left != 0 {
		t.Errorf("registries kept after the job: %d", left)
	}
	// The same request with another token is the same job; another address under it is a conflict.
	again := request
	again.Registries = testRegistries()
	again.Registries[0].Token = "npm_another_token_0123456789"
	if replay, err := f.m.StartStackJob(context.Background(), again); err != nil || replay.ID != job.ID {
		t.Fatalf("replay = %v %v", replay.ID, err)
	}
	again.Registries[0].URL = "https://" + npmHost + "/npm/other"
	if _, err := f.m.StartStackJob(context.Background(), again); !errors.Is(err, serviceapi.ErrStackJobConflict) {
		t.Fatalf("conflict = %v", err)
	}
	// A request with registries ABCP refuses is refused whole.
	bad := serviceapi.StackJobCreateV1{RequestID: "req-bad", Source: "proj-r", CacheKey: key, Registries: []serviceapi.RegistryV1{{Kind: "npm", Use: "all", URL: "https://10.0.0.1/", Token: npmToken}}}
	if _, err := f.m.StartStackJob(context.Background(), bad); !errors.Is(err, serviceapi.ErrStackJobNotAccepted) {
		t.Fatalf("bad = %v", err)
	}
}

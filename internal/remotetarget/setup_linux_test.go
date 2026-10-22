//go:build linux

package remotetarget

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// setupFile is what the server keeps beside the app's database.env.
func (f *deployFixture) setupFile(site string) (string, os.FileMode) {
	path := filepath.Join(f.remote.home, "autobuild/apps", site, "setup.env")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0
	}
	info, _ := os.Stat(path)
	return string(data), info.Mode().Perm()
}

func TestANodeAppStartsWithItsOwnSetupTokenWhichNoCommandLineHoldsAndWhichLastsWithItsData(t *testing.T) {
	f := newDeployFixture(t)
	if _, op := f.deploy(f.putNode("one"), "crm", testHost, "req-crm-first"); op.State != opSucceeded {
		t.Fatalf("deploy = %+v", op)
	}
	setup, err := f.m.SiteSetup(context.Background(), f.key, "crm")
	if err != nil || setup.SchemaVersion != serviceapi.SiteSetupSchemaV1 || setup.Target != f.key || setup.Site != "crm" || setup.Host != testHost ||
		!regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(setup.Token) {
		t.Fatalf("setup = %+v %v", setup, err)
	}
	token := setup.Token
	if env, mode := f.setupFile("crm"); env != "BOOTSTRAP_TOKEN="+token+"\n" || mode != 0o600 {
		t.Fatalf("setup.env = %q %v", env, mode)
	}
	docker := f.remote.docker()
	if !strings.Contains(docker, "--env-file "+f.remote.home+"/autobuild/apps/crm/setup.env") {
		t.Fatalf("the app was not given its setup token:\n%s", docker)
	}
	// The token reaches the server on the script's input: no command line and no script holds it.
	if strings.Contains(docker, token) {
		t.Fatal("the token is on a command line")
	}
	for _, script := range f.remote.scripts {
		if strings.Contains(script, token) {
			t.Fatal("a script holds the token")
		}
	}
	// The sites list never shows it.
	if sites, _ := f.m.Sites(context.Background(), f.key); strings.Contains(sites.Sites[0].Release+sites.Sites[0].Operation, token) {
		t.Fatal("the sites list shows the token")
	}

	// An update, a stop and a start keep the same token.
	if _, op := f.deploy(f.putNode("two"), "crm", testHost, "req-crm-second"); op.State != opSucceeded {
		t.Fatalf("update = %+v", op)
	}
	f.finished(f.m.StopSite(context.Background(), f.key, "crm", serviceapi.OperationRequestV1{RequestID: "req-stop-001"}))
	f.finished(f.m.StartSite(context.Background(), f.key, "crm", serviceapi.OperationRequestV1{RequestID: "req-start-001"}))
	if again, _ := f.m.SiteSetup(context.Background(), f.key, "crm"); again.Token != token {
		t.Fatalf("the token changed: %q", again.Token)
	}
	if env, _ := f.setupFile("crm"); env != "BOOTSTRAP_TOKEN="+token+"\n" {
		t.Fatalf("setup.env after a start = %q", env)
	}

	// Taken off, then its data removed: the token goes with the data, and the app put back gets a new one.
	f.finished(f.m.RemoveSite(context.Background(), f.key, "crm", serviceapi.SiteRemoveV1{RequestID: "req-remove-01"}))
	if _, err := f.m.SiteSetup(context.Background(), f.key, "crm"); !errors.Is(err, serviceapi.ErrSiteUnknown) {
		t.Fatalf("a site taken off = %v", err)
	}
	f.finished(f.m.RemoveData(context.Background(), f.key, "crm", serviceapi.ConfirmedRequestV1{RequestID: "req-data-0001", Confirm: "app_crm"}))
	if env, _ := f.setupFile("crm"); env != "" {
		t.Fatalf("setup.env after the data was removed = %q", env)
	}
	if _, op := f.deploy(f.putNode("three"), "crm", testHost, "req-crm-third"); op.State != opSucceeded {
		t.Fatalf("deploy again = %+v", op)
	}
	if fresh, _ := f.m.SiteSetup(context.Background(), f.key, "crm"); fresh.Token == token || fresh.Token == "" {
		t.Fatalf("a fresh database kept the old token: %q", fresh.Token)
	}
}

func TestOnlyANodeAppHasASetupTokenAndOneStartedBeforeTokensGetsItsOnNextStart(t *testing.T) {
	f := newDeployFixture(t)
	f.deploy(f.put(map[string]string{"index.html": "blog"}), "blog", "blog.example.com", "req-blog-0001")
	for _, c := range []struct {
		site string
		want error
	}{
		{"blog", serviceapi.ErrTargetInvalid},
		{"shop", serviceapi.ErrSiteUnknown},
		{"Shop", serviceapi.ErrTargetInvalid},
	} {
		if _, err := f.m.SiteSetup(context.Background(), f.key, c.site); !errors.Is(err, c.want) {
			t.Fatalf("%s = %v", c.site, err)
		}
	}
	if _, err := f.m.SiteSetup(context.Background(), "srv-000000000000", "crm"); !errors.Is(err, serviceapi.ErrTargetUnknown) {
		t.Fatalf("an unknown target = %v", err)
	}

	// An app started before ABCP kept setup tokens has none until it starts again.
	f.deploy(f.putNode("one"), "crm", testHost, "req-crm-first")
	r, _ := f.m.read(f.key)
	d, _ := findDatabase(r, "crm")
	d.SetupToken = ""
	setDatabase(&r, d)
	if err := f.m.write(r); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.SiteSetup(context.Background(), f.key, "crm"); !errors.Is(err, serviceapi.ErrSetupPending) {
		t.Fatalf("an app without a token = %v", err)
	}
	f.finished(f.m.StopSite(context.Background(), f.key, "crm", serviceapi.OperationRequestV1{RequestID: "req-stop-001"}))
	f.finished(f.m.StartSite(context.Background(), f.key, "crm", serviceapi.OperationRequestV1{RequestID: "req-start-001"}))
	setup, err := f.m.SiteSetup(context.Background(), f.key, "crm")
	if env, _ := f.setupFile("crm"); err != nil || env != "BOOTSTRAP_TOKEN="+setup.Token+"\n" {
		t.Fatalf("after a start = %+v %v %q", setup, err, env)
	}
}

func TestTheStartScriptRefusesAnythingButASetupToken(t *testing.T) {
	f := newDeployFixture(t)
	f.deploy(f.putNode("one"), "crm", testHost, "req-crm-first")
	script := fill(startAppScript, "N", "crm", "RUNTIME", "", "RELAY", "")
	for _, input := range []string{"", "\n", strings.Repeat("a", 31) + "\n", strings.Repeat("A", 32) + "\n", "$(id)" + strings.Repeat("a", 27) + "\n"} {
		if _, err := run(context.Background(), f.remote, script, strings.NewReader(input)); err == nil {
			t.Errorf("%q started the app", input)
		}
	}
}

// An app compares a write's Origin with its own address, which it sees as plain
// http behind the front door (the template library's kit/server.js does this).
func appTakesWrite(origin, host string) bool {
	return origin == "" || origin == "http://"+host
}

func TestTheFrontDoorHandsANodeAppItsOwnPagesOriginAndNoOtherSitesAsItsOwn(t *testing.T) {
	f := newDeployFixture(t)
	f.deploy(f.putNode("one"), "crm", testHost, "req-crm-first")
	f.deploy(f.put(map[string]string{"index.html": "blog"}), "blog", "blog.example.com", "req-blog-0001")
	caddy := f.caddy()
	// Only the app's block changes the Origin, and only its scheme, with Caddy's
	// header_up search and replacement (a regular expression and its replacement).
	block := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(testHost) + ` \{\n\treverse_proxy autobuild-app-crm:8080 \{\n\t\theader_up Origin (\S+) (\S+)\n\t\}\n`).FindStringSubmatch(caddy)
	if block == nil || strings.Count(caddy, "header_up") != 1 {
		t.Fatalf("Caddyfile = %s", caddy)
	}
	rewrite := func(origin string) string {
		if origin == "" {
			return "" // Caddy changes only a header the request has
		}
		return regexp.MustCompile(block[1]).ReplaceAllString(origin, block[2])
	}
	for _, c := range []struct {
		origin string
		takes  bool
	}{
		{"https://" + testHost, true},
		{"", true}, // no Origin: the app's Sec-Fetch-Site check still applies
		{"https://evil.example", false},
		{"https://" + testHost + ".evil.example", false},
		{"https://evil.example/https://" + testHost, false},
		{"null", false},
		{"https://blog.example.com", false},
	} {
		if got := appTakesWrite(rewrite(c.origin), testHost); got != c.takes {
			t.Errorf("Origin %q reaches the app as %q: takes the write = %v", c.origin, rewrite(c.origin), got)
		}
	}
	// Before this, the app's own page was refused.
	if appTakesWrite("https://"+testHost, testHost) {
		t.Fatal("the app's own page needs no change")
	}
}

package remotetarget

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// TestFrontDoorSendsAutobuildsPathsOnOnlyForSitesWithACredential: BF-08a's
// block comes before a site's own content, strips the headers it sets, and
// leaves the visitor's cookies and credentials behind; a paused site and a
// site without a credential keep their blocks as before, and so does every
// site when ABCP has no relay host.
func TestFrontDoorSendsAutobuildsPathsOnOnlyForSitesWithACredential(t *testing.T) {
	credential := strings.Repeat("c", 43)
	r := record{Key: "srv-0123456789ab", Sites: []siteRecord{
		{Name: "cafe", Host: "cafe.deployflix.app", Kind: kindNode, Release: "r1", State: siteLive, Relay: credential},
		{Name: "menu", Host: "menu.deployflix.app", Release: "r2", State: siteLive, Relay: strings.Repeat("d", 43)},
		{Name: "paused", Host: "paused.deployflix.app", Kind: kindNode, Release: "r3", State: siteStopped, Relay: strings.Repeat("e", 43)},
		{Name: "plain", Host: "plain.deployflix.app", Kind: kindNode, Release: "r4", State: siteLive},
	}}
	got := caddyfileFor(r, "", "", "siterelays.deployflix.app")
	block := func(host string) string {
		start := strings.Index(got, "\n"+host+" {\n")
		if start < 0 {
			t.Fatalf("no block for %s in\n%s", host, got)
		}
		end := strings.Index(got[start+1:], "\n}\n")
		return got[start : start+1+end]
	}
	cafe := block("cafe.deployflix.app")
	for _, want := range []string{
		"\trequest_header -X-Autobuild-Site\n\trequest_header -X-Autobuild-Host\n\trequest_header -X-Autobuild-Client\n",
		"\thandle /__autobuild/* {\n\t\treverse_proxy https://siterelays.deployflix.app {\n\t\t\theader_up Host siterelays.deployflix.app\n",
		"\t\t\theader_up X-Autobuild-Site " + credential + "\n\t\t\theader_up X-Autobuild-Host {host}\n\t\t\theader_up X-Autobuild-Client {remote_host}\n",
		"\t\t\theader_up -Cookie\n\t\t\theader_up -Authorization\n",
		"\thandle {\n\t\treverse_proxy autobuild-app-cafe:8080 {\n\t\t\theader_up Origin " + originFrom + " " + originTo + "\n\t\t}\n\t}\n",
	} {
		if !strings.Contains(cafe, want) {
			t.Fatalf("cafe block lacks %q:\n%s", want, cafe)
		}
	}
	if menu := block("menu.deployflix.app"); !strings.Contains(menu, "\thandle {\n\t\troot * /srv/sites/menu\n\t\tfile_server\n\t}\n") ||
		!strings.Contains(menu, "handle /__autobuild/*") {
		t.Fatalf("a website's block relays too:\n%s", menu)
	}
	if paused := block("paused.deployflix.app"); strings.Contains(paused, "__autobuild") || !strings.Contains(paused, "respond \"This app is paused.\" 503") {
		t.Fatalf("a paused site answers 503 for everything:\n%s", paused)
	}
	if plain := block("plain.deployflix.app"); strings.Contains(plain, "__autobuild") {
		t.Fatalf("a site without a credential relays:\n%s", plain)
	}
	if off := caddyfileFor(r, "", "", ""); strings.Contains(off, "__autobuild") || strings.Contains(off, credential) {
		t.Fatalf("without a relay host nothing relays:\n%s", off)
	}
	r.Sites[0].Relay = "short"
	if odd := caddyfileFor(r, "", "", "siterelays.deployflix.app"); strings.Contains(odd, "short") {
		t.Fatal("a malformed credential is never written")
	}
}

func TestRelayHostAndCredentialsAreChecked(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"not a host", "13.232.1.2", "Siterelays.deployflix.app", "siterelays.deployflix.app\nx"} {
		if m.SetRelayHost(bad) == nil {
			t.Fatalf("relay host %q accepted", bad)
		}
	}
	if err := m.SetRelayHost("siterelays.deployflix.app"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ site, credential string }{{"cafe", "short"}, {"Cafe", strings.Repeat("c", 43)}, {"cafe", strings.Repeat("c", 42) + "!"}} {
		if _, err := m.SetSiteRelay(context.Background(), "srv-0123456789ab", c.site, serviceapi.SiteRelayV1{Credential: c.credential}); err != serviceapi.ErrTargetInvalid {
			t.Fatalf("%q %q = %v", c.site, c.credential, err)
		}
	}
	if _, err := m.SetSiteRelay(context.Background(), "srv-0123456789ab", "cafe", serviceapi.SiteRelayV1{Credential: strings.Repeat("c", 43)}); err == nil {
		t.Fatal("an unknown target took a credential")
	}
}

// TestTheInsideListenerPassesOnlyAnAppServersStatusReads (BF-08b2): a running
// Node app with a relay credential gets an entry on the inside listener, known
// by the matcher insideScript writes from its network's range; a website, a
// paused app and an app without a credential get none, and nothing listens
// inside without a relay host. The public blocks drop the server's mark.
func TestTheInsideListenerPassesOnlyAnAppServersStatusReadsAndAIAnswers(t *testing.T) {
	credential := strings.Repeat("c", 43)
	r := record{Key: "srv-0123456789ab", Sites: []siteRecord{
		{Name: "cafe", Host: "cafe.example.com", Kind: kindNode, Release: "r1", State: siteLive, Relay: credential},
		{Name: "menu", Host: "menu.deployflix.app", Release: "r2", State: siteLive, Relay: strings.Repeat("d", 43)},
		{Name: "paused", Host: "paused.deployflix.app", Kind: kindNode, Release: "r3", State: siteStopped, Relay: strings.Repeat("e", 43)},
		{Name: "plain", Host: "plain.deployflix.app", Kind: kindNode, Release: "r4", State: siteLive},
	}}
	got := caddyfileFor(r, "", "", "siterelays.deployflix.app")
	start := strings.Index(got, "\nhttp://:8089 {\n")
	if start < 0 {
		t.Fatalf("no inside listener in\n%s", got)
	}
	inside := got[start:]
	for _, want := range []string{
		"\trequest_header -X-Autobuild-Caller\n\trequest_header -X-Autobuild-Relay-Token\n",
		"\timport inside/cafe.caddy\n\thandle @from_cafe {\n\t\treverse_proxy https://siterelays.deployflix.app {\n",
		"\thandle @ai_cafe {\n\t\trequest_body {\n\t\t\tmax_size 64KB\n\t\t}\n\t\treverse_proxy https://siterelays.deployflix.app {\n",
		"\t\t\theader_up X-Autobuild-Site " + credential + "\n\t\t\theader_up X-Autobuild-Host cafe.example.com\n",
		"\t\t\theader_up X-Autobuild-Caller server\n\t\t\theader_up -Cookie\n\t\t\theader_up -Authorization\n",
		"\trespond 404\n}\n",
	} {
		if !strings.Contains(inside, want) {
			t.Fatalf("the inside listener lacks %q:\n%s", want, inside)
		}
	}
	for _, other := range []string{"menu", "paused", "plain"} {
		if strings.Contains(inside, "inside/"+other+".caddy") {
			t.Fatalf("%s is on the inside listener:\n%s", other, inside)
		}
	}
	if !strings.Contains(got, "\ncafe.example.com {\n\trequest_header -X-Autobuild-Site\n\trequest_header -X-Autobuild-Host\n\trequest_header -X-Autobuild-Client\n\trequest_header -X-Autobuild-Caller\n") {
		t.Fatalf("the public block keeps a visitor's X-Autobuild-Caller:\n%s", got)
	}
	if off := caddyfileFor(r, "", "", ""); strings.Contains(off, ":8089") {
		t.Fatalf("an inside listener without a relay host:\n%s", off)
	}
	r.Sites[0].Relay = ""
	if none := caddyfileFor(r, "", "", "siterelays.deployflix.app"); strings.Contains(none, ":8089") {
		t.Fatalf("an inside listener with no app to pass on:\n%s", none)
	}
}

// TestTheInsideMatchersComeFromEachNetworksRange runs insideScript with a
// docker that knows one network: its site's matcher takes that range and only
// a connection's status read, and a site whose network is not found gets a
// range nothing comes from. With ABCP_TEST_CADDY naming a caddy binary, the
// Caddyfile and its matchers are adapted by Caddy itself.
func TestTheInsideMatchersComeFromEachNetworksRange(t *testing.T) {
	home, bin := t.TempDir(), t.TempDir()
	fake := "#!/bin/sh\ncase \"$*\" in *autobuild-net-cafe*) echo '172.21.0.0/16 ' ;; *) exit 1 ;; esac\n"
	if err := os.WriteFile(bin+"/docker", []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", "set -eu\n"+fill(insideScript, "NETS", "cafe plain"))
	cmd.Env = []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	read := func(site string) string {
		b, err := os.ReadFile(home + "/autobuild/frontdoor/config/inside/" + site + ".caddy")
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if cafe := read("cafe"); cafe != "@from_cafe {\n\tremote_ip 172.21.0.0/16\n\tmethod GET\n\tpath_regexp ^/__autobuild/connect/[a-z0-9-]+/[a-z0-9-]+/[A-Za-z0-9_-]+$\n}\n"+
		"@ai_cafe {\n\tremote_ip 172.21.0.0/16\n\tmethod POST\n\tpath /__autobuild/ai\n}\n" {
		t.Fatalf("cafe's matcher:\n%s", cafe)
	}
	if plain := read("plain"); !strings.Contains(plain, "\tremote_ip 255.255.255.255/32\n") {
		t.Fatalf("a site without a network matches someone:\n%s", plain)
	}
	caddy := os.Getenv("ABCP_TEST_CADDY")
	if caddy == "" {
		return
	}
	r := record{Key: "srv-0123456789ab", Sites: []siteRecord{
		{Name: "cafe", Host: "cafe.example.com", Kind: kindNode, Release: "r1", State: siteLive, Relay: strings.Repeat("c", 43)},
		{Name: "plain", Host: "plain.deployflix.app", Kind: kindNode, Release: "r4", State: siteLive},
	}}
	config := home + "/autobuild/frontdoor/config/Caddyfile"
	if err := os.WriteFile(config, []byte(caddyfileFor(r, "", "", "siterelays.deployflix.app")), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(caddy, "adapt", "--config", config, "--adapter", "caddyfile").CombinedOutput(); err != nil {
		t.Fatalf("caddy adapt: %v: %s", err, out)
	}
}

// TestANodeAppIsToldTheInsideListenerOnlyWhileRelaysAreOn (BF-08b2).
func TestANodeAppIsToldTheInsideListenerOnlyWhileRelaysAreOn(t *testing.T) {
	on := fill(startAppScript, "N", "crm", "RUNTIME", "", "RELAY", "-e AUTOBUILD_RELAY="+insideRelay+" ")
	if !strings.Contains(on, " -e AUTOBUILD_RELAY=http://autobuild-frontdoor:8089 --label autobuild.site=crm ") {
		t.Fatalf("the app is not told where to ask:\n%s", on)
	}
	if off := fill(startAppScript, "N", "crm", "RUNTIME", "", "RELAY", ""); strings.Contains(off, "AUTOBUILD_RELAY") || strings.Contains(off, "@RELAY@") ||
		!strings.Contains(off, "NODE_OPTIONS=--max-old-space-size=160 \\\n  --label autobuild.site=crm ") {
		t.Fatalf("without relays the start is as before:\n%s", off)
	}
}

package remotetarget

import (
	"context"
	"os"
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

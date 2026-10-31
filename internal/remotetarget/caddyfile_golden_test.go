package remotetarget

import (
	"os"
	"strings"
	"testing"
)

// goldenNodeAndStatic is a target with Node apps and static sites in every
// state the front door writes differently, with and without relay
// credentials; testdata/caddyfile-node-static.golden is what ABCP wrote for it
// before WordPress websites (W5b), case by case.
func goldenNodeAndStatic() record {
	return record{Key: "srv-0123456789ab", Sites: []siteRecord{
		{Name: "cafe", Host: "cafe.example.com", Kind: kindNode, Release: "r1", State: siteLive, Relay: strings.Repeat("c", 43)},
		{Name: "crm", Host: "crm.example.com", Kind: kindNode, Release: "r2", State: siteLive},
		{Name: "gone", Host: "gone.example.com", Kind: kindNode, Release: "r6", State: siteRemoving, Relay: strings.Repeat("g", 43)},
		{Name: "menu", Host: "menu.deployflix.app", Release: "r3", State: siteLive, Relay: strings.Repeat("d", 43)},
		{Name: "paused", Host: "paused.deployflix.app", Kind: kindNode, Release: "r4", State: siteStopped, Relay: strings.Repeat("e", 43)},
		{Name: "shop", Host: "shop.example.com", Release: "r5", State: siteFailed},
		{Name: "tasks", Host: "tasks.example.com", Kind: kindNode, Release: "r7", State: siteFailed, Relay: strings.Repeat("t", 43)},
	}}
}

type goldenCase struct{ name, site, host, relay string }

func goldenCases() []goldenCase {
	return []goldenCase{
		{"relays on", "", "", "siterelays.deployflix.app"},
		{"relays off", "", "", ""},
		{"a new static site", "blog", "blog.example.com", "siterelays.deployflix.app"},
		{"a site taken off", "cafe", "", "siterelays.deployflix.app"},
		{"a node app's host changed", "crm", "www.crm.example.com", ""},
	}
}

// goldenCaddyfiles reads the golden file, case by case.
func goldenCaddyfiles(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile("testdata/caddyfile-node-static.golden")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, part := range strings.Split(string(data), "=== ")[1:] {
		name, body, _ := strings.Cut(part, "\n")
		out[name] = body
	}
	return out
}

// The front door's Caddyfile for Node apps and static sites is byte for byte
// the golden file's, in every state and case: written before WordPress
// websites (W5b), it shows their blocks and inside entries stay as they were.
func TestNodeAndStaticCaddyfilesAreUnchangedByteForByte(t *testing.T) {
	golden := goldenCaddyfiles(t)
	r := goldenNodeAndStatic()
	if len(golden) != len(goldenCases()) {
		t.Fatalf("golden cases: %d", len(golden))
	}
	for _, c := range goldenCases() {
		if got := caddyfileFor(r, c.site, c.host, c.relay); got != golden[c.name] {
			t.Errorf("%s: the Caddyfile changed:\n%s\nwas:\n%s", c.name, got, golden[c.name])
		}
	}
}

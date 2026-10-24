package remotetarget

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// A Node app's recent errors are read from its own container when asked: its
// state and restarts, and only the lines of its output that read as errors,
// with secrets, email addresses and numbers replaced; an app whose container
// is gone answers "none" and no lines, a static site and an unconfirmed target
// are refused, and nothing is changed on the server (BF-07c).
func TestAnAppsRecentErrorsAreReadFromItsOwnOutputWithSecretsReplaced(t *testing.T) {
	f := newDeployFixture(t)
	f.deploy(f.putNode("one"), "crm", testHost, "req-crm-first")
	ctx := context.Background()
	read, err := f.m.SiteErrors(ctx, f.key, "crm")
	if err != nil || read.SchemaVersion != serviceapi.SiteErrorsSchemaV1 || read.Target != f.key || read.Site != "crm" || read.ReadAt == "" ||
		!read.Running || read.Restarts != 0 || read.ExitCode != nil || len(read.Lines) != 0 || read.Truncated {
		t.Fatalf("a quiet app = %+v %v", read, err)
	}
	docker := f.remote.docker()
	if !strings.Contains(docker, "logs --since 24h --tail 2000 autobuild-app-crm") {
		t.Fatalf("the app's output was not read:\n%s", docker)
	}
	if !strings.Contains(docker, "--log-driver json-file --log-opt max-size=1m --log-opt max-file=2") {
		t.Fatalf("the app was started without bounded output:\n%s", docker)
	}

	s := filepath.Join(f.remote.home, ".docker")
	if err := os.WriteFile(filepath.Join(s, "c-autobuild-app-crm.state"), []byte("restarting 3 1 false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := strings.Join([]string{
		"listening on 8080",
		"GET /health 200 2ms",
		"Error: connect ECONNREFUSED postgres://app_crm:0f1e2d3c4b5a69788796a5b4c3d2e1f0aa@autobuild-postgres:5432/app_crm",
		"    at TCPConnectWrap.afterConnect [as oncomplete] (node:net:1555:16)",
		"Unhandled rejection for asha@example.com: token=0123456789abcdef",
		"booking saved for 9876543210",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(s, "c-autobuild-app-crm.log"), []byte(output), 0o600); err != nil {
		t.Fatal(err)
	}
	scripts := len(f.remote.scripts)
	read, err = f.m.SiteErrors(ctx, f.key, "crm")
	if err != nil || read.Running || read.Restarts != 3 || read.ExitCode == nil || *read.ExitCode != 1 || read.OutOfMemory {
		t.Fatalf("a restarting app = %+v %v", read, err)
	}
	want := []string{
		"Error: connect ECONNREFUSED postgres://[hidden]@autobuild-postgres:5432/app_crm",
		"    at TCPConnectWrap.afterConnect [as oncomplete] (node:net:1555:16)",
		"Unhandled rejection for [email]: token=[hidden]",
	}
	if strings.Join(read.Lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("lines = %q", read.Lines)
	}
	if len(f.remote.scripts) != scripts+1 {
		t.Fatalf("reading ran %d scripts", len(f.remote.scripts)-scripts)
	}
	for _, word := range []string{" rm ", " run ", " kill ", " start ", " stop ", " restart "} {
		if strings.Contains(f.remote.scripts[len(f.remote.scripts)-1], "docker"+word) {
			t.Fatalf("reading changed something with %q", word)
		}
	}

	// An app whose container is gone answers that, with no lines.
	if err := os.Remove(filepath.Join(s, "c-autobuild-app-crm")); err != nil {
		t.Fatal(err)
	}
	read, err = f.m.SiteErrors(ctx, f.key, "crm")
	if err != nil || read.Running || read.Restarts != 0 || read.ExitCode != nil || len(read.Lines) != 0 {
		t.Fatalf("no container = %+v %v", read, err)
	}

	// Only a Node app on a confirmed target is read.
	f.deploy(f.put(map[string]string{"index.html": "<h1>Shop</h1>"}), "shop", "shop.deployflix.app", "req-shop")
	for _, site := range []string{"shop", "nosuch", "Bad Name"} {
		if _, err := f.m.SiteErrors(ctx, f.key, site); !errors.Is(err, serviceapi.ErrSiteUnknown) {
			t.Fatalf("site %q = %v", site, err)
		}
	}
	if _, err := f.m.SiteErrors(ctx, "srv-000000000000", "crm"); err == nil {
		t.Fatal("an unknown target was read")
	}
	r, _ := f.m.read(f.key)
	r.State = statePending
	if err := f.m.write(r); err != nil {
		t.Fatal(err)
	}
	scripts = len(f.remote.scripts)
	if _, err := f.m.SiteErrors(ctx, f.key, "crm"); !errors.Is(err, serviceapi.ErrTargetNotReady) || len(f.remote.scripts) != scripts {
		t.Fatalf("an unconfirmed target = %v", err)
	}
}

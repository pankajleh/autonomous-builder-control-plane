//go:build linux

package remotetarget

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

func (f *deployFixture) isolation(site, other, request string, crash bool) serviceapi.OperationV1 {
	f.t.Helper()
	return f.finished(f.m.CheckIsolation(context.Background(), f.key, serviceapi.IsolationRequestV1{RequestID: request, Site: site, Other: other, Crash: crash}))
}

// twoNodeApps puts crm and shop on the target, both live.
func (f *deployFixture) twoNodeApps() {
	f.t.Helper()
	f.m.resolve = func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("13.232.1.2")}, nil }
	f.deploy(f.putNode("one"), "crm", testHost, "req-crm-first")
	f.deploy(f.putNode("two"), "shop", "shop.13-232-1-2.sslip.io", "req-shop-first")
	if f.siteState("crm") != siteLive || f.siteState("shop") != siteLive {
		f.t.Fatalf("crm %s, shop %s", f.siteState("crm"), f.siteState("shop"))
	}
}

func stepNames(op serviceapi.OperationV1) string {
	var names []string
	for _, s := range op.Steps {
		names = append(names, s.Name)
	}
	return strings.Join(names, ",")
}

// BF-14 proof P1b: two apps share the server's Postgres, each login is refused
// on the other's database, neither container reaches the other, and one app's
// crash leaves the other up.
func TestTwoAppsOnOneServerAreKeptApartAndOnesCrashLeavesTheOtherUp(t *testing.T) {
	f := newDeployFixture(t)
	f.twoNodeApps()

	op := f.isolation("crm", "shop", "req-apart-001", false)
	if op.State != opSucceeded || op.Kind != "isolation" || op.Site != "crm" || stepNames(op) != "own_login,other_login,login_back,network" {
		t.Fatalf("isolation = %+v", op)
	}
	for name, want := range map[string]string{
		"own_login":   "app_crm connects to its own database",
		"other_login": "app_crm on app_shop: refused by the server's access rules; app_crm may not connect to app_shop",
		"login_back":  "app_shop on app_crm: refused by the server's access rules; app_shop may not connect to app_crm",
		"network":     "crm and shop cannot reach each other, by name or by address",
	} {
		if got := stepOf(op, name, "").Detail; got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	// The logins' passwords stay on the server: no script and no docker command line carries one.
	for _, site := range []string{"crm", "shop"} {
		pw, err := os.ReadFile(filepath.Join(f.remote.home, ".docker", "role-app_"+site))
		if err != nil || len(strings.TrimSpace(string(pw))) != 48 {
			t.Fatalf("password of %s = %q %v", site, pw, err)
		}
		if strings.Contains(strings.Join(f.remote.scripts, "\n"), strings.TrimSpace(string(pw))) || strings.Contains(f.remote.docker(), strings.TrimSpace(string(pw))) {
			t.Fatalf("the password of app_%s left the server's files", site)
		}
	}
	if again, _ := f.m.CheckIsolation(context.Background(), f.key, serviceapi.IsolationRequestV1{RequestID: "req-apart-001", Site: "crm", Other: "shop"}); again.OperationID != op.OperationID {
		t.Fatal("the same request is one check")
	}
	if _, err := f.m.CheckIsolation(context.Background(), f.key, serviceapi.IsolationRequestV1{RequestID: "req-apart-001", Site: "crm", Other: "shop", Crash: true}); !errors.Is(err, serviceapi.ErrTargetInvalid) {
		t.Fatalf("the same request asking something else = %v", err)
	}

	// The crash: crm is killed, shop still answers, crm is started again.
	op = f.isolation("crm", "shop", "req-apart-002", true)
	if op.State != opSucceeded || stepNames(op) != "own_login,other_login,login_back,network,crash,other_answers,start_again" ||
		stepOf(op, "crash", "").Detail != "killed; it is down" || stepOf(op, "other_answers", "").Detail != "shop answers /health while crm is down" ||
		stepOf(op, "start_again", "").Detail != "started again; answers" {
		t.Fatalf("crash = %+v", op)
	}
	if !strings.Contains(f.remote.docker(), "kill --signal KILL autobuild-app-crm") || f.state("c-autobuild-app-crm.down") || f.siteState("crm") != siteLive {
		t.Fatalf("crm after the crash: down %v, state %s", f.state("c-autobuild-app-crm.down"), f.siteState("crm"))
	}
	f.flag("app.restarts", true)
	if op = f.isolation("crm", "shop", "req-apart-003", true); op.State != opSucceeded || stepOf(op, "crash", "").Detail != "killed; it came back by itself" {
		t.Fatalf("an app that restarts by itself = %+v", op)
	}
	f.flag("app.restarts", false)
}

func TestAnIsolationCheckFailsWhenAppsAreNotApartAndAlwaysStartsTheCrashedAppAgain(t *testing.T) {
	f := newDeployFixture(t)
	f.twoNodeApps()

	f.flag("hba.open", true)
	op := f.isolation("crm", "shop", "req-apart-101", false)
	if op.State != opFailed || stepOf(op, "other_login", "").Detail != "app_crm on app_shop: connects; may connect: false" || stepOf(op, "network", "").State != "pending" {
		t.Fatalf("a login that reaches the other database = %+v", op)
	}
	f.flag("hba.open", false)
	f.flag("connect.open", true)
	if op = f.isolation("crm", "shop", "req-apart-102", false); op.State != opFailed ||
		stepOf(op, "other_login", "").Detail != "app_crm on app_shop: refused by the server's access rules; may connect: true" {
		t.Fatalf("a role allowed to connect = %+v", op)
	}
	f.flag("connect.open", false)

	// shop's container joined crm's network: crm reaches it.
	shared := filepath.Join(f.remote.home, ".docker", "nets", "autobuild-app-shop", "autobuild-net-crm")
	if err := os.WriteFile(shared, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if op = f.isolation("crm", "shop", "req-apart-103", false); op.State != opFailed || stepOf(op, "network", "").Detail != "crm reaches shop (autobuild-app-shop)" {
		t.Fatalf("a shared network = %+v", op)
	}
	_ = os.Remove(shared)

	// The other app does not answer while crm is down: the check fails, and crm is still started again.
	if err := os.WriteFile(filepath.Join(f.remote.home, ".docker", "c-autobuild-app-shop.unhealthy"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	op = f.isolation("crm", "shop", "req-apart-104", true)
	if op.State != opFailed || stepOf(op, "other_answers", "").State != "failed" || stepOf(op, "start_again", "").Detail != "started again; answers" ||
		f.state("c-autobuild-app-crm.down") {
		t.Fatalf("crash with the other down = %+v", op)
	}
}

func TestAnIsolationCheckIsRefusedForAnythingButTwoLiveNodeApps(t *testing.T) {
	f := newDeployFixture(t)
	f.twoNodeApps()
	f.deploy(f.put(map[string]string{"index.html": "<h1>Site</h1>"}), "site", "site.13-232-1-2.sslip.io", "req-site-first")
	for _, c := range []struct {
		request serviceapi.IsolationRequestV1
		want    error
	}{
		{serviceapi.IsolationRequestV1{RequestID: "req-apart-201", Site: "crm", Other: "crm"}, serviceapi.ErrTargetInvalid},
		{serviceapi.IsolationRequestV1{RequestID: "bad", Site: "crm", Other: "shop"}, serviceapi.ErrTargetInvalid},
		{serviceapi.IsolationRequestV1{RequestID: "req-apart-202", Site: "Crm", Other: "shop"}, serviceapi.ErrTargetInvalid},
		{serviceapi.IsolationRequestV1{RequestID: "req-apart-203", Site: "crm", Other: "nope"}, serviceapi.ErrSiteUnknown},
		{serviceapi.IsolationRequestV1{RequestID: "req-apart-204", Site: "site", Other: "shop"}, serviceapi.ErrSiteUnknown},
	} {
		if _, err := f.m.CheckIsolation(context.Background(), f.key, c.request); !errors.Is(err, c.want) {
			t.Fatalf("%+v = %v, want %v", c.request, err, c.want)
		}
	}
	f.finished(f.m.StopSite(context.Background(), f.key, "shop", serviceapi.OperationRequestV1{RequestID: "req-stop-201"}))
	if _, err := f.m.CheckIsolation(context.Background(), f.key, serviceapi.IsolationRequestV1{RequestID: "req-apart-205", Site: "crm", Other: "shop"}); !errors.Is(err, serviceapi.ErrSiteStopped) {
		t.Fatalf("a stopped app = %v", err)
	}
}

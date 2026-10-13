//go:build linux

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

func (f *deployFixture) caddy() string {
	data, _ := os.ReadFile(filepath.Join(f.remote.home, "autobuild/frontdoor/config/Caddyfile"))
	return string(data)
}

func (f *deployFixture) siteState(name string) string {
	sites, _ := f.m.Sites(context.Background(), f.key)
	for _, s := range sites.Sites {
		if s.Name == name {
			return s.State
		}
	}
	return "absent"
}

func (f *deployFixture) drift(request string) serviceapi.DriftV1 {
	f.t.Helper()
	op := f.finished(f.m.CheckDrift(context.Background(), f.key, serviceapi.OperationRequestV1{RequestID: request}))
	if op.State != opSucceeded || op.Kind != "drift" || op.Steps[0].Name != "read_server" {
		f.t.Fatalf("drift = %+v", op)
	}
	report, err := f.m.Drift(context.Background(), f.key)
	if err != nil || !report.Checked {
		f.t.Fatalf("report = %+v %v", report, err)
	}
	return report
}

func kinds(report serviceapi.DriftV1) string {
	var out []string
	for _, finding := range report.Findings {
		out = append(out, finding.Kind+":"+finding.Site)
	}
	return strings.Join(out, ",")
}

func TestANodeAppStopsAndStartsWithoutLosingAnything(t *testing.T) {
	f := newDeployFixture(t)
	f.deploy(f.putNode("one"), "crm", testHost, "req-crm-first")
	f.rows("app_crm", `{"public.members":3}`)
	taken := f.backup("crm", "req-backup-1")

	op := f.finished(f.m.StopSite(context.Background(), f.key, "crm", serviceapi.OperationRequestV1{RequestID: "req-stop-001"}))
	var names []string
	for _, s := range op.Steps {
		names = append(names, s.Name)
	}
	if op.State != opSucceeded || op.Kind != "stop" || strings.Join(names, ",") != "stop_app,write_site,reload_frontdoor" {
		t.Fatalf("stop = %+v", op)
	}
	if f.siteState("crm") != siteStopped || f.state("c-autobuild-app-crm") || !f.state("net-autobuild-net-crm") || f.held("app_crm") != `{"public.members":3}` {
		t.Fatalf("stopped: state %s", f.siteState("crm"))
	}
	if !strings.Contains(f.caddy(), testHost+" {\n\trespond \"This app is paused.\" 503\n") {
		t.Fatalf("Caddyfile = %s", f.caddy())
	}
	if again, _ := f.m.StopSite(context.Background(), f.key, "crm", serviceapi.OperationRequestV1{RequestID: "req-stop-001"}); again.OperationID != op.OperationID {
		t.Fatal("the same request is one stop")
	}
	if _, err := f.m.StopSite(context.Background(), f.key, "crm", serviceapi.OperationRequestV1{RequestID: "req-stop-002"}); !errors.Is(err, serviceapi.ErrSiteStopped) {
		t.Fatalf("stopping a stopped app = %v", err)
	}
	if _, err := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Restore: &serviceapi.PlanRestoreV1{Site: "crm", Backup: taken.BackupID}}); !errors.Is(err, serviceapi.ErrSiteStopped) {
		t.Fatalf("restoring a stopped app = %v", err)
	}
	// A stopped app is left out of the nightly backups; the night still checks the server.
	f.m.NightlyPass(context.Background(), nil)
	night, _ := f.m.operationByRequest(f.key, "nightly-2026-10-14")
	if len(night.Operation.Steps) != 1 || night.Operation.Steps[0].Name != "read_server" || night.Operation.State != opSucceeded {
		t.Fatalf("night = %+v", night.Operation)
	}

	op = f.finished(f.m.StartSite(context.Background(), f.key, "crm", serviceapi.OperationRequestV1{RequestID: "req-start-001"}))
	if op.State != opSucceeded || op.Kind != "start" || f.siteState("crm") != siteLive || !f.state("c-autobuild-app-crm") ||
		!strings.Contains(f.caddy(), "reverse_proxy autobuild-app-crm:8080") {
		t.Fatalf("start = %+v; state %s", op, f.siteState("crm"))
	}
	if _, err := f.m.StartSite(context.Background(), f.key, "crm", serviceapi.OperationRequestV1{RequestID: "req-start-002"}); !errors.Is(err, serviceapi.ErrTargetInvalid) {
		t.Fatalf("starting a live app = %v", err)
	}

	// An app that never answers when started is stopped again.
	f.finished(f.m.StopSite(context.Background(), f.key, "crm", serviceapi.OperationRequestV1{RequestID: "req-stop-003"}))
	f.flag("app.fails-once", true)
	op = f.finished(f.m.StartSite(context.Background(), f.key, "crm", serviceapi.OperationRequestV1{RequestID: "req-start-003"}))
	if op.State != opFailed || stepOf(op, "wait_health", "").Detail != "the app did not answer /health; the app is stopped again" ||
		f.siteState("crm") != siteStopped || f.state("c-autobuild-app-crm") || !strings.Contains(f.caddy(), "This app is paused.") {
		t.Fatalf("failed start = %+v; state %s", op, f.siteState("crm"))
	}

	shop := f.put(map[string]string{"index.html": "<h1>Shop</h1>"})
	f.deploy(shop, "shop", "shop.13-232-1-2.sslip.io", "req-shop-first")
	if _, err := f.m.StopSite(context.Background(), f.key, "shop", serviceapi.OperationRequestV1{RequestID: "req-stop-004"}); !errors.Is(err, serviceapi.ErrSiteUnknown) {
		t.Fatalf("stopping a static site = %v", err)
	}
	// A new version puts a stopped app back up.
	_, op = f.deploy(f.putNode("two"), "crm", testHost, "req-crm-second")
	if op.State != opSucceeded || f.siteState("crm") != siteLive || !strings.Contains(f.caddy(), "reverse_proxy autobuild-app-crm:8080") {
		t.Fatalf("update of a stopped app = %+v", op)
	}
}

func TestAnAppsDataIsRemovedOnlyOnceItsSiteIsOffAndItsNameRepeated(t *testing.T) {
	f := newDeployFixture(t)
	f.deploy(f.putNode("one"), "crm", testHost, "req-crm-first")
	f.backup("crm", "req-backup-1")
	list, _ := f.m.Databases(context.Background(), f.key)
	if len(list.Databases) != 1 || list.Databases[0].Database != "app_crm" || !list.Databases[0].OnServer || list.Databases[0].Backups != 1 {
		t.Fatalf("databases = %+v", list)
	}
	remove := func(request, confirm string) (serviceapi.OperationV1, error) {
		return f.m.RemoveData(context.Background(), f.key, "crm", serviceapi.ConfirmedRequestV1{RequestID: request, Confirm: confirm})
	}
	if _, err := remove("req-data-0001", "app_crm"); !errors.Is(err, serviceapi.ErrDataInUse) {
		t.Fatalf("data of an app on the server = %v", err)
	}
	f.finished(f.m.RemoveSite(context.Background(), f.key, "crm", serviceapi.SiteRemoveV1{RequestID: "req-remove-01"}))
	if list, _ = f.m.Databases(context.Background(), f.key); list.Databases[0].OnServer {
		t.Fatal("the app is off the server")
	}
	if _, err := remove("req-data-0002", "app_shop"); !errors.Is(err, serviceapi.ErrTargetInvalid) {
		t.Fatalf("another name = %v", err)
	}
	op := f.finished(remove("req-data-0003", "app_crm"))
	var names []string
	for _, s := range op.Steps {
		names = append(names, s.Name)
	}
	if op.State != opSucceeded || op.Kind != "remove_data" || strings.Join(names, ",") != "start_database,drop_database,remove_backups" ||
		stepOf(op, "remove_backups", "").Detail != "1 backups removed\n" {
		t.Fatalf("remove data = %+v", op)
	}
	home := f.remote.home
	if f.held("app_crm") != "absent" || f.state("role-app_crm") || exists(filepath.Join(home, "autobuild/apps/crm/database.env")) || exists(filepath.Join(home, "autobuild/backups/crm")) {
		t.Fatal("the data is still there")
	}
	if list, _ = f.m.Databases(context.Background(), f.key); len(list.Databases) != 0 {
		t.Fatalf("databases = %+v", list)
	}
	if _, err := remove("req-data-0004", "app_crm"); !errors.Is(err, serviceapi.ErrSiteUnknown) {
		t.Fatalf("data already removed = %v", err)
	}
	// Put back under the same name, the app gets a new, empty database and login.
	_, op = f.deploy(f.putNode("two"), "crm", testHost, "req-crm-again")
	if op.State != opSucceeded || stepOf(op, "make_database", "").Detail != "made\n" || !f.state("role-app_crm") {
		t.Fatalf("again = %+v", op)
	}
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

func TestATargetIsCleanedUpOnlyWhenNothingIsLeftAndItsKeyRepeated(t *testing.T) {
	f := newDeployFixture(t)
	f.deploy(f.putNode("one"), "crm", testHost, "req-crm-first")
	clean := func(request, confirm string) (serviceapi.OperationV1, error) {
		return f.m.CleanTarget(context.Background(), f.key, serviceapi.ConfirmedRequestV1{RequestID: request, Confirm: confirm})
	}
	if _, err := clean("req-clean-001", f.key); !errors.Is(err, serviceapi.ErrTargetInUse) {
		t.Fatalf("with a site = %v", err)
	}
	f.finished(f.m.RemoveSite(context.Background(), f.key, "crm", serviceapi.SiteRemoveV1{RequestID: "req-remove-01"}))
	if _, err := clean("req-clean-002", f.key); !errors.Is(err, serviceapi.ErrTargetInUse) {
		t.Fatalf("with an app's data = %v", err)
	}
	f.finished(f.m.RemoveData(context.Background(), f.key, "crm", serviceapi.ConfirmedRequestV1{RequestID: "req-data-0001", Confirm: "app_crm"}))
	if _, err := clean("req-clean-003", "srv-000000000000"); !errors.Is(err, serviceapi.ErrTargetInvalid) {
		t.Fatalf("another key = %v", err)
	}
	before := len(f.remote.docker())
	op := f.finished(clean("req-clean-004", f.key))
	var names []string
	for _, s := range op.Steps {
		names = append(names, s.Name)
	}
	if op.State != opSucceeded || op.Kind != "clean" ||
		strings.Join(names, ",") != "stop_frontdoor,stop_database,remove_volume,remove_networks,remove_images,remove_files" {
		t.Fatalf("clean = %+v", op)
	}
	docker := f.remote.docker()[before:]
	for _, want := range []string{"rm -f autobuild-postgres", "volume rm autobuild-pgdata", "network rm autobuild-db", "image rm " + FrontDoorImage,
		"image rm " + NodeRuntimeImage, "image rm " + NodePostgresImage} {
		if !strings.Contains(docker, want) {
			t.Fatalf("docker did not get %q:\n%s", want, docker)
		}
	}
	if exists(filepath.Join(f.remote.home, "autobuild")) || f.state("c-autobuild-postgres") || f.state("vol-autobuild-pgdata") || f.state("net-autobuild-db") {
		t.Fatal("something is left")
	}
	r, _ := f.m.read(f.key)
	if len(r.Releases) != 0 || len(r.Sites) != 0 || len(r.Databases) != 0 {
		t.Fatalf("record = %+v", r)
	}
	// Nothing is left to check against.
	if report := f.drift("req-drift-001"); !report.OK {
		t.Fatalf("after clean up = %+v", report)
	}
}

func TestCheckingATargetFindsWhatDiffersAndReconcilesASiteLeftUnknown(t *testing.T) {
	f := newDeployFixture(t)
	if report, _ := f.m.Drift(context.Background(), f.key); report.Checked || report.Findings == nil {
		t.Fatalf("before any check = %+v", report)
	}
	f.deploy(f.putNode("one"), "crm", testHost, "req-crm-first")
	shop := f.put(map[string]string{"index.html": "<h1>Shop</h1>"})
	f.deploy(shop, "shop", "shop.13-232-1-2.sslip.io", "req-shop-first")
	f.backup("crm", "req-backup-1")
	if report := f.drift("req-drift-001"); !report.OK || len(report.Reconciled) != 0 {
		t.Fatalf("as deployed = %s", kinds(report))
	}

	state := filepath.Join(f.remote.home, ".docker")
	home := f.remote.home
	_ = os.Remove(filepath.Join(state, "c-autobuild-app-crm"))
	_ = os.WriteFile(filepath.Join(state, "c-autobuild-leftover"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(state, "db-check_app_crm"), nil, 0o644)
	_ = os.WriteFile(filepath.Join(state, "db-app_ghost"), nil, 0o644)
	_ = os.MkdirAll(filepath.Join(home, "autobuild/releases", strings.Repeat("e", 64)), 0o755)
	_ = os.WriteFile(filepath.Join(home, "autobuild/frontdoor/config/Caddyfile"), []byte("edited by hand\n"), 0o644)
	list, _ := f.m.Backups(context.Background(), f.key, "crm")
	_ = os.Remove(filepath.Join(home, "autobuild/backups/crm", list.Backups[0].BackupID+".dump"))
	_ = os.Remove(filepath.Join(home, "autobuild/sites/shop"))
	report := f.drift("req-drift-002")
	want := "link_missing:shop,container_missing:crm,container_unknown:,release_unknown:,database_unknown:,copy_left:,backup_missing:crm,frontdoor_config:"
	if report.OK || kinds(report) != want {
		t.Fatalf("drift = %s", kinds(report))
	}
	if !f.state("c-autobuild-leftover") || !f.state("db-check_app_crm") || strings.Contains(f.remote.docker(), "autobuild-leftover") {
		t.Fatal("a check repaired something")
	}

	// A site an interrupted operation left unknown is reconciled when the server is unambiguous.
	_ = os.Symlink("../releases/"+shop, filepath.Join(home, "autobuild/sites/shop"))
	r, _ := f.m.read(f.key)
	for i := range r.Sites {
		r.Sites[i].State = siteUnknown
	}
	_ = f.m.write(r)
	report = f.drift("req-drift-003")
	if strings.Join(report.Reconciled, ",") != "shop" || f.siteState("shop") != siteLive || f.siteState("crm") != siteUnknown ||
		!strings.HasPrefix(kinds(report), "site_unknown:crm,") {
		t.Fatalf("reconciled %v, findings %s", report.Reconciled, kinds(report))
	}
}

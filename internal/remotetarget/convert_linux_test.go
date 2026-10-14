//go:build linux

package remotetarget

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

const testConversion = "ALTER TABLE members RENAME TO people;\nDELETE FROM autobuild_migrations;\n"

// convertFixture is a Node app online with three rows, and another app's
// release to replace it.
func convertFixture(t *testing.T) (*deployFixture, string, string) {
	t.Helper()
	f := newDeployFixture(t)
	first := f.putNode("one")
	if _, op := f.deploy(first, "crm", testHost, "req-crm-first"); op.State != opSucceeded {
		t.Fatalf("first = %+v", op)
	}
	if err := os.WriteFile(filepath.Join(f.remote.home, ".docker/db-app_crm"), []byte(`{"public.members":3}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f, first, f.putNode("another app")
}

func (f *deployFixture) convert(digest, sql, request string) (serviceapi.PlanV1, serviceapi.OperationV1) {
	f.t.Helper()
	plan, err := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Release: digest, Site: serviceapi.PlanSiteV1{Name: "crm", Host: testHost}, Conversion: sql})
	if err != nil {
		f.t.Fatalf("plan: %v", err)
	}
	op, err := f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: plan.PlanID, PlanHash: plan.Hash, RequestID: request})
	if err != nil {
		f.t.Fatalf("apply: %v", err)
	}
	f.m.Wait()
	done, err := f.m.Operation(context.Background(), f.key, op.OperationID)
	if err != nil {
		f.t.Fatal(err)
	}
	return plan, done
}

func (f *deployFixture) file(name string) string {
	data, _ := os.ReadFile(filepath.Join(f.remote.home, name))
	return string(data)
}

func TestAnAppReplacedByAnotherHasItsDataConvertedOnceAsItsOwnLoginAfterItStopsAndIsBackedUp(t *testing.T) {
	f, first, second := convertFixture(t)
	before := len(f.remote.docker())
	plan, op := f.convert(second, testConversion, "req-crm-convert")
	want := "send_files,get_images,start_database,make_database,stop_app,back_up,convert,switch_site,start_app,wait_health,write_site,reload_frontdoor,check"
	if strings.Join(plan.Steps, ",") != want || plan.Replaces != first || plan.Conversion == nil ||
		plan.Conversion.Digest != conversionDigest(testConversion) || plan.Conversion.Bytes != len(testConversion) || plan.Hash != planHash(plan) {
		t.Fatalf("plan = %+v", plan)
	}
	if op.State != opSucceeded || stepOf(op, "convert", "").Detail != "converted; 1 tables, 2 rows" {
		t.Fatalf("operation = %+v", op)
	}
	// Stopped before the backup, so nothing written after it is lost; converted after it, before the new release starts.
	docker := f.remote.docker()
	ran := docker[before:]
	stop, dump := strings.Index(ran, "rm -f autobuild-app-crm"), strings.Index(ran, "-d app_crm -qAtX")
	conv, start := strings.Index(ran, "PGPASSFILE"), strings.Index(ran, "run -d --name autobuild-app-crm")
	if stop < 0 || !(stop < dump && dump < conv && conv < start) {
		t.Fatalf("order: stop %d backup %d convert %d start %d\n%s", stop, dump, conv, start, ran)
	}
	backups, _ := f.m.Backups(context.Background(), f.key, "crm")
	if len(backups.Backups) != 1 || backups.Backups[0].Reason != reasonBeforeUpdate || *backups.Backups[0].Rows != 3 || backups.Backups[0].Release != first {
		t.Fatalf("backups = %+v", backups)
	}
	if got := f.file(".docker/convert.sql"); got != testConversion {
		t.Fatalf("the conversion ran as %q", got)
	}
	args := f.file(".docker/convert.args")
	for _, want := range []string{"PGPASSFILE=/scratch/.convert-app_crm.pgpass", "/usr/lib/postgresql/17/bin/psql -h 127.0.0.1 -U app_crm -d app_crm -X -q -v ON_ERROR_STOP=1 --single-transaction -f -"} {
		if !strings.Contains(args, want) {
			t.Fatalf("the conversion ran with %q", args)
		}
	}
	// The app's own login, its password from database.env through a file removed after; no command line holds it.
	env := f.file("autobuild/apps/crm/database.env")
	password := regexp.MustCompile(`app_crm:([0-9a-f]{48})@`).FindStringSubmatch(env)[1]
	if f.file(".docker/convert.pgpass") != "127.0.0.1:5432:app_crm:app_crm:"+password+"\n" {
		t.Fatalf("pgpass = %q", f.file(".docker/convert.pgpass"))
	}
	if _, err := os.Stat(filepath.Join(f.remote.home, ".docker/scratch/.convert-app_crm.pgpass")); !os.IsNotExist(err) {
		t.Fatal("the password file was left behind")
	}
	if strings.Contains(docker, password) || strings.Contains(args, password) {
		t.Fatal("the password is on a command line")
	}
	for _, script := range f.remote.scripts {
		if strings.Contains(script, password) || strings.Contains(script, "RENAME TO people") || regexp.MustCompile(`@[A-Z]+@`).MatchString(script) {
			t.Fatalf("a script = %s", script)
		}
	}
	if link, _ := os.Readlink(filepath.Join(f.remote.home, "autobuild/sites/crm")); link != "../releases/"+second || !f.state("c-autobuild-app-crm") {
		t.Fatalf("link = %q", link)
	}
	if f.file(".docker/db-app_crm") != `{"public.people":2}`+"\n" {
		t.Fatalf("data = %q", f.file(".docker/db-app_crm"))
	}
	sites, _ := f.m.Sites(context.Background(), f.key)
	if sites.Sites[0].Release != second || sites.Sites[0].State != siteLive {
		t.Fatalf("site = %+v", sites.Sites[0])
	}
}

func TestAConversionThatFailsChangesNothingAndStartsTheAppAgain(t *testing.T) {
	f, first, second := convertFixture(t)
	if err := os.WriteFile(filepath.Join(f.remote.home, "convert.fails"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, op := f.convert(second, testConversion, "req-crm-convert")
	convert := stepOf(op, "convert", "")
	if op.State != opFailed || convert.State != "failed" ||
		convert.Detail != `the conversion failed: psql:<stdin>:3: ERROR:  column "cut_date" does not exist; nothing changed and the app is started again` ||
		stepOf(op, "switch_site", "").State != "pending" {
		t.Fatalf("operation = %+v", op)
	}
	if link, _ := os.Readlink(filepath.Join(f.remote.home, "autobuild/sites/crm")); link != "../releases/"+first || !f.state("c-autobuild-app-crm") {
		t.Fatalf("the app that ran is not running again: link %q", link)
	}
	if f.file(".docker/db-app_crm") != `{"public.members":3}`+"\n" {
		t.Fatalf("data = %q", f.file(".docker/db-app_crm"))
	}
	if _, err := os.Stat(filepath.Join(f.remote.home, ".docker/scratch/.convert-app_crm.pgpass")); !os.IsNotExist(err) {
		t.Fatal("the password file was left behind")
	}
	sites, _ := f.m.Sites(context.Background(), f.key)
	if sites.Sites[0].Release != first {
		t.Fatalf("site = %+v", sites.Sites[0])
	}
}

func TestAConvertedAppThatNeverAnswersGetsItsDataAndReleaseBack(t *testing.T) {
	f, first, second := convertFixture(t)
	if err := os.WriteFile(filepath.Join(f.remote.home, "app.fails-once"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, op := f.convert(second, testConversion, "req-crm-convert")
	health := stepOf(op, "wait_health", "")
	if op.State != opFailed || health.Detail != "the app did not answer /health; the data is put back as it was before; switched back to the release it replaced" {
		t.Fatalf("operation = %+v", op)
	}
	if f.file(".docker/db-app_crm") != `{"public.members":3}`+"\n" || f.state("db-previous_app_crm") || f.state("db-restore_app_crm") {
		t.Fatalf("data = %q", f.file(".docker/db-app_crm"))
	}
	if link, _ := os.Readlink(filepath.Join(f.remote.home, "autobuild/sites/crm")); link != "../releases/"+first || !f.state("c-autobuild-app-crm") || f.state("c-autobuild-app-crm.unhealthy") {
		t.Fatalf("the release it replaced is not running again: link %q", link)
	}
	sites, _ := f.m.Sites(context.Background(), f.key)
	if sites.Sites[0].Release != first || sites.Sites[0].State != siteFailed {
		t.Fatalf("site = %+v", sites.Sites[0])
	}
}

func TestAConversionIsOnlyForARunningNodeAppReplacedAndOnlyTheSQLApproved(t *testing.T) {
	f, first, second := convertFixture(t)
	plan := func(digest, site, sql string) error {
		_, err := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Release: digest, Site: serviceapi.PlanSiteV1{Name: site, Host: site + ".13-232-1-2.sslip.io"}, Conversion: sql})
		return err
	}
	for name, sql := range map[string]string{
		"a psql command": "SELECT 1;\n\\! id\n",
		"a backslash":    "SELECT E'a\\nb';",
		"a NUL":          "SELECT 1;\x00",
		"nothing":        "  \n",
		"too long":       strings.Repeat("-", serviceapi.MaxConversionBytes+1),
		"not UTF-8":      "SELECT '\xff';",
	} {
		if err := plan(second, "crm", sql); !errors.Is(err, serviceapi.ErrTargetInvalid) {
			t.Errorf("%s = %v", name, err)
		}
	}
	// A first version has no data to convert, a static site none at all.
	if err := plan(second, "blog", testConversion); !errors.Is(err, serviceapi.ErrTargetInvalid) {
		t.Fatalf("a first version = %v", err)
	}
	if _, err := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Release: first, Site: serviceapi.PlanSiteV1{Name: "crm", Host: testHost}, Conversion: testConversion}); !errors.Is(err, serviceapi.ErrTargetInvalid) {
		t.Fatalf("the release it runs = %v", err)
	}

	// The plan's hash covers the SQL: another SQL in its record is never run.
	p, err := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Release: second, Site: serviceapi.PlanSiteV1{Name: "crm", Host: testHost}, Conversion: testConversion})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.m.dir, "plans", p.PlanID+".json")
	var pr planRecord
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &pr); err != nil || pr.Conversion != testConversion {
		t.Fatalf("plan record = %+v %v", pr, err)
	}
	pr.Conversion = "DROP TABLE members;"
	data, _ = json.Marshal(pr)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: p.PlanID, PlanHash: p.Hash, RequestID: "req-crm-tampered"}); !errors.Is(err, serviceapi.ErrPlanStale) {
		t.Fatalf("another SQL = %v", err)
	}

	// A stopped app is started first.
	if _, err := f.m.StopSite(context.Background(), f.key, "crm", serviceapi.OperationRequestV1{RequestID: "req-crm-stop"}); err != nil {
		t.Fatal(err)
	}
	f.m.Wait()
	if err := plan(second, "crm", testConversion); !errors.Is(err, serviceapi.ErrSiteStopped) {
		t.Fatalf("a stopped app = %v", err)
	}
}

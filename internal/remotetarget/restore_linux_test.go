//go:build linux

package remotetarget

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// rows sets what a database holds, as the fake Postgres counts it.
func (f *deployFixture) rows(db, counts string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.remote.home, ".docker", "db-"+db), []byte(counts+"\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *deployFixture) held(db string) string {
	data, err := os.ReadFile(filepath.Join(f.remote.home, ".docker", "db-"+db))
	if err != nil {
		return "absent"
	}
	return strings.TrimSpace(string(data))
}

func (f *deployFixture) flag(name string, on bool) {
	f.t.Helper()
	path := filepath.Join(f.remote.home, name)
	if !on {
		_ = os.Remove(path)
		return
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *deployFixture) backup(site, request string) serviceapi.BackupV1 {
	f.t.Helper()
	op, err := f.m.BackUp(context.Background(), f.key, site, serviceapi.BackupCreateV1{RequestID: request})
	if err != nil {
		f.t.Fatal(err)
	}
	f.m.Wait()
	if op, _ = f.m.Operation(context.Background(), f.key, op.OperationID); op.State != opSucceeded {
		f.t.Fatalf("backup = %+v", op)
	}
	list, _ := f.m.Backups(context.Background(), f.key, site)
	return list.Backups[len(list.Backups)-1]
}

func (f *deployFixture) finished(op serviceapi.OperationV1, err error) serviceapi.OperationV1 {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
	f.m.Wait()
	done, err := f.m.Operation(context.Background(), f.key, op.OperationID)
	if err != nil {
		f.t.Fatal(err)
	}
	return done
}

func stepOf(op serviceapi.OperationV1, name, site string) serviceapi.OperationStepV1 {
	for _, s := range op.Steps {
		if s.Name == name && s.Site == site {
			return s
		}
	}
	return serviceapi.OperationStepV1{}
}

func TestABackupCountsItsRowsAndACheckRestoresItIntoADatabaseOfItsOwn(t *testing.T) {
	f := newDeployFixture(t)
	f.deploy(f.putNode("one"), "crm", testHost, "req-crm-first")
	f.rows("app_crm", `{"public.members":3,"public.notes":2}`)
	taken := f.backup("crm", "req-backup-1")
	if taken.Tables == nil || *taken.Tables != 2 || taken.Rows == nil || *taken.Rows != 5 || taken.Check != nil {
		t.Fatalf("backup = %+v", taken)
	}
	// BF-06a: the listing names each table with its rows, in name order.
	if len(taken.Counts) != 2 || taken.Counts[0] != (serviceapi.TableRowsV1{Table: "public.members", Rows: 3}) ||
		taken.Counts[1] != (serviceapi.TableRowsV1{Table: "public.notes", Rows: 2}) || serviceapi.ValidateTableRows(taken.Counts) != nil {
		t.Fatalf("counts = %+v", taken.Counts)
	}
	f.rows("app_crm", `{"public.members":9}`)

	check := f.finished(f.m.CheckBackup(context.Background(), f.key, "crm", taken.BackupID, serviceapi.BackupCreateV1{RequestID: "req-check-1"}))
	if check.State != opSucceeded || check.Kind != "check_backup" || len(check.Steps) != 1 ||
		check.Steps[0].Name != "check_restore" || check.Steps[0].Detail != "2 tables, 5 rows, as when it was taken" {
		t.Fatalf("check = %+v", check)
	}
	if again, _ := f.m.CheckBackup(context.Background(), f.key, "crm", taken.BackupID, serviceapi.BackupCreateV1{RequestID: "req-check-1"}); again.OperationID != check.OperationID {
		t.Fatal("the same request is one check")
	}
	// The app's own database is untouched and the copy is gone.
	if f.held("app_crm") != `{"public.members":9}` || f.held("check_app_crm") != "absent" {
		t.Fatalf("databases: app %s, copy %s", f.held("app_crm"), f.held("check_app_crm"))
	}
	if !strings.Contains(f.remote.docker(), "pg_restore -h /scratch -U postgres --role=app_crm --no-owner --no-privileges --single-transaction --exit-on-error -d check_app_crm") ||
		!strings.Contains(f.remote.docker(), "CREATE DATABASE check_app_crm OWNER app_crm") || !strings.Contains(f.remote.docker(), "REVOKE CONNECT, TEMPORARY ON DATABASE check_app_crm FROM PUBLIC") {
		t.Fatalf("docker:\n%s", f.remote.docker())
	}
	list, _ := f.m.Backups(context.Background(), f.key, "crm")
	if c := list.Backups[0].Check; c == nil || !c.OK || c.Detail != "2 tables, 5 rows, as when it was taken" {
		t.Fatalf("check on the backup = %+v", c)
	}

	// A restore that loses rows fails its check, and says which.
	f.flag("restore.loses-rows", true)
	check = f.finished(f.m.CheckBackup(context.Background(), f.key, "crm", taken.BackupID, serviceapi.BackupCreateV1{RequestID: "req-check-2"}))
	want := "the rows differ: public.members: 3 taken, 0 restored; public.notes: 2 taken, not restored"
	if check.State != opFailed || check.Steps[0].Detail != want || f.held("check_app_crm") != "absent" {
		t.Fatalf("check = %+v", check)
	}
	list, _ = f.m.Backups(context.Background(), f.key, "crm")
	if c := list.Backups[0].Check; c == nil || c.OK || c.Detail != want {
		t.Fatalf("check on the backup = %+v", c)
	}

	for name, call := range map[string]func() error{
		"an unknown backup": func() error {
			_, err := f.m.CheckBackup(context.Background(), f.key, "crm", "bk-20261014T010000Z-ffff", serviceapi.BackupCreateV1{RequestID: "req-check-3"})
			return err
		},
		"an app with no database": func() error {
			_, err := f.m.CheckBackup(context.Background(), f.key, "blog", taken.BackupID, serviceapi.BackupCreateV1{RequestID: "req-check-4"})
			return err
		},
		"a backup id that is not one": func() error {
			_, err := f.m.CheckBackup(context.Background(), f.key, "crm", "bk-1", serviceapi.BackupCreateV1{RequestID: "req-check-5"})
			return err
		},
	} {
		if err := call(); err == nil || errors.Is(err, serviceapi.ErrTargetBusy) {
			t.Errorf("%s = %v", name, err)
		}
	}
}

func TestARestoreIsAnApprovedPlanThatChecksTheCopyBeforeSwappingItIn(t *testing.T) {
	f := newDeployFixture(t)
	release := f.putNode("one")
	f.deploy(release, "crm", testHost, "req-crm-first")
	f.rows("app_crm", `{"public.members":3}`)
	taken := f.backup("crm", "req-backup-1")
	f.rows("app_crm", `{"public.members":5,"public.notes":1}`)

	plan, err := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Restore: &serviceapi.PlanRestoreV1{Site: "crm", Backup: taken.BackupID}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Kind != planRestore || plan.Release.Digest != release || plan.Replaces != "" || plan.Database != "app_crm" || plan.Sandbox != sandboxStandard ||
		plan.Backup == nil || plan.Backup.BackupID != taken.BackupID || plan.Site.Host != testHost || plan.Hash != planHash(plan) ||
		strings.Join(plan.Steps, ",") != "back_up,restore_copy,stop_app,switch_database,start_app,wait_health,check,clean_up" {
		t.Fatalf("plan = %+v", plan)
	}
	if f.held("app_crm") != `{"public.members":5,"public.notes":1}` {
		t.Fatal("a plan changed the database")
	}
	op := f.finished(f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: plan.PlanID, PlanHash: plan.Hash, RequestID: "req-restore-1"}))
	if op.State != opSucceeded || op.Kind != "restore" || stepOf(op, "restore_copy", "").Detail != "1 tables, 3 rows, as when it was taken" {
		t.Fatalf("restore = %+v", op)
	}
	if f.held("app_crm") != `{"public.members":3}` || f.held("restore_app_crm") != "absent" || f.held("previous_app_crm") != "absent" {
		t.Fatalf("databases: app %s, copy %s, previous %s", f.held("app_crm"), f.held("restore_app_crm"), f.held("previous_app_crm"))
	}
	list, _ := f.m.Backups(context.Background(), f.key, "crm")
	if len(list.Backups) != 2 || list.Backups[1].Reason != reasonBeforeRestore || *list.Backups[1].Rows != 6 || list.Backups[1].Release != release ||
		list.Backups[0].Check == nil || !list.Backups[0].Check.OK {
		t.Fatalf("backups = %+v", list.Backups)
	}
	psqlIn, _ := os.ReadFile(filepath.Join(f.remote.home, ".docker/psql.stdin"))
	if !strings.Contains(string(psqlIn), "BEGIN;\nALTER DATABASE app_crm RENAME TO previous_app_crm;\nALTER DATABASE restore_app_crm RENAME TO app_crm;\nCOMMIT;") {
		t.Fatalf("the swap is not one transaction: %s", psqlIn)
	}
	if !f.state("c-autobuild-app-crm") {
		t.Fatal("the app is not running")
	}
	sites, _ := f.m.Sites(context.Background(), f.key)
	if sites.Sites[0].State != siteLive || sites.Sites[0].Release != release {
		t.Fatalf("site = %+v", sites.Sites[0])
	}
	if _, err := f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: plan.PlanID, PlanHash: plan.Hash, RequestID: "req-restore-2"}); !errors.Is(err, serviceapi.ErrPlanStale) {
		t.Fatalf("a plan applies once: %v", err)
	}

	// A restored app that never answers gets its database back as it was.
	f.rows("app_crm", `{"public.members":7}`)
	before := list.Backups[1]
	plan, err = f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Restore: &serviceapi.PlanRestoreV1{Site: "crm", Backup: before.BackupID}})
	if err != nil {
		t.Fatal(err)
	}
	f.flag("app.fails-once", true)
	op = f.finished(f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: plan.PlanID, PlanHash: plan.Hash, RequestID: "req-restore-3"}))
	health := stepOf(op, "wait_health", "")
	if op.State != opFailed || health.Detail != "the app did not answer /health; the database is put back as it was and the app started again" ||
		stepOf(op, "check", "").State != "pending" {
		t.Fatalf("restore = %+v", op)
	}
	if f.held("app_crm") != `{"public.members":7}` || f.held("restore_app_crm") != "absent" || f.held("previous_app_crm") != "absent" {
		t.Fatalf("databases: app %s, copy %s, previous %s", f.held("app_crm"), f.held("restore_app_crm"), f.held("previous_app_crm"))
	}
	if !f.state("c-autobuild-app-crm") || f.state("c-autobuild-app-crm.unhealthy") {
		t.Fatal("the app is not running again")
	}

	// A copy that does not hold the backup's rows is never swapped in.
	f.flag("restore.loses-rows", true)
	plan, _ = f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Restore: &serviceapi.PlanRestoreV1{Site: "crm", Backup: taken.BackupID}})
	op = f.finished(f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: plan.PlanID, PlanHash: plan.Hash, RequestID: "req-restore-4"}))
	if op.State != opFailed || !strings.HasPrefix(stepOf(op, "restore_copy", "").Detail, "the rows differ") || stepOf(op, "stop_app", "").State != "pending" {
		t.Fatalf("restore = %+v", op)
	}
	if f.held("app_crm") != `{"public.members":7}` || f.held("restore_app_crm") != "absent" || !f.state("c-autobuild-app-crm") {
		t.Fatalf("databases: app %s, copy %s", f.held("app_crm"), f.held("restore_app_crm"))
	}
	f.flag("restore.loses-rows", false)

	// A plan made before the app changed no longer applies.
	plan, _ = f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Restore: &serviceapi.PlanRestoreV1{Site: "crm", Backup: taken.BackupID}})
	f.deploy(f.putNode("two"), "crm", testHost, "req-crm-second")
	if _, err := f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: plan.PlanID, PlanHash: plan.Hash, RequestID: "req-restore-5"}); !errors.Is(err, serviceapi.ErrPlanStale) {
		t.Fatalf("a stale restore = %v", err)
	}

	shop := f.put(map[string]string{"index.html": "<h1>Shop</h1>"})
	f.deploy(shop, "shop", "shop.13-232-1-2.sslip.io", "req-shop-first")
	for name, create := range map[string]serviceapi.PlanCreateV1{
		"a release as well":  {Release: shop, Restore: &serviceapi.PlanRestoreV1{Site: "crm", Backup: taken.BackupID}},
		"a site as well":     {Site: serviceapi.PlanSiteV1{Name: "crm"}, Restore: &serviceapi.PlanRestoreV1{Site: "crm", Backup: taken.BackupID}},
		"a static site":      {Restore: &serviceapi.PlanRestoreV1{Site: "shop", Backup: taken.BackupID}},
		"an unknown backup":  {Restore: &serviceapi.PlanRestoreV1{Site: "crm", Backup: "bk-20261014T010000Z-ffff"}},
		"a malformed backup": {Restore: &serviceapi.PlanRestoreV1{Site: "crm", Backup: "../x"}},
	} {
		if _, err := f.m.CreatePlan(context.Background(), f.key, create); err == nil {
			t.Errorf("%s was planned", name)
		}
	}
	if _, err := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Restore: &serviceapi.PlanRestoreV1{Site: "crm", Backup: "bk-20261014T010000Z-ffff"}}); !errors.Is(err, serviceapi.ErrBackupUnknown) {
		t.Fatalf("an unknown backup = %v", err)
	}
}

func TestEveryNightEachNodeAppIsBackedUpAndItsBackupChecked(t *testing.T) {
	f := newDeployFixture(t)
	f.deploy(f.putNode("one"), "crm", testHost, "req-crm-first")
	f.deploy(f.putNode("blog"), "blog", "blog.13-232-1-2.sslip.io", "req-blog-first")
	f.deploy(f.put(map[string]string{"index.html": "<h1>Shop</h1>"}), "shop", "shop.13-232-1-2.sslip.io", "req-shop-first")
	f.rows("app_crm", `{"public.members":4}`)
	f.rows("app_blog", `{"public.posts":2,"public.comments":8}`)

	if busy := f.m.NightlyPass(context.Background(), nil); len(busy) != 0 {
		t.Fatalf("busy = %v", busy)
	}
	rec, ok := f.m.operationByRequest(f.key, "nightly-2026-10-14")
	if !ok {
		t.Fatal("no nightly operation")
	}
	op := rec.Operation
	var names []string
	for _, s := range op.Steps {
		names = append(names, s.Name+":"+s.Site+":"+s.State)
	}
	if op.State != opSucceeded || op.Kind != "nightly" || op.Site != "" ||
		strings.Join(names, ",") != "back_up:blog:done,check_restore:blog:done,back_up:crm:done,check_restore:crm:done,read_server::done" ||
		stepOf(op, "check_restore", "blog").Detail != "2 tables, 10 rows, as when it was taken" {
		t.Fatalf("nightly = %+v", op)
	}
	for _, site := range []string{"crm", "blog"} {
		list, _ := f.m.Backups(context.Background(), f.key, site)
		if len(list.Backups) != 1 || list.Backups[0].Reason != reasonNightly || list.Backups[0].Check == nil || !list.Backups[0].Check.OK {
			t.Fatalf("%s backups = %+v", site, list.Backups)
		}
	}
	// A night runs once.
	scripts := len(f.remote.scripts)
	f.m.NightlyPass(context.Background(), nil)
	if len(f.remote.scripts) != scripts {
		t.Fatal("the night ran twice")
	}
	// A busy target is answered for its one more try.
	f.now = f.now.Add(24 * time.Hour)
	f.m.mu.Lock()
	f.m.busy[f.key] = true
	f.m.mu.Unlock()
	if busy := f.m.NightlyPass(context.Background(), nil); strings.Join(busy, ",") != f.key {
		t.Fatalf("busy = %v", busy)
	}
	f.m.mu.Lock()
	delete(f.m.busy, f.key)
	f.m.mu.Unlock()
	// A target that does not answer fails its night and is not tried in a loop.
	f.m.remote = func(context.Context, record) (remote, string) { return nil, "unreachable" }
	if busy := f.m.NightlyPass(context.Background(), []string{f.key}); len(busy) != 0 {
		t.Fatalf("busy = %v", busy)
	}
	rec, _ = f.m.operationByRequest(f.key, "nightly-2026-10-15")
	if rec.Operation.State != opFailed || rec.Operation.Steps[0].Detail != "unreachable" {
		t.Fatalf("unreachable night = %+v", rec.Operation)
	}
	f.m.NightlyPass(context.Background(), nil)
	if count := strings.Count(f.remote.docker(), "pg_restore"); count != 2 {
		t.Fatalf("restores = %d", count)
	}
}

func TestTheNightIsAtHalfPastTwoInIndia(t *testing.T) {
	for _, c := range []struct{ now, next string }{
		{"2026-10-14T01:00:00Z", "2026-10-14T21:00:00Z"},
		{"2026-10-14T20:59:59Z", "2026-10-14T21:00:00Z"},
		{"2026-10-14T21:00:00Z", "2026-10-15T21:00:00Z"},
		{"2026-10-14T22:00:00Z", "2026-10-15T21:00:00Z"},
	} {
		now, _ := time.Parse(time.RFC3339, c.now)
		if got := nextNightly(now).UTC().Format(time.RFC3339); got != c.next {
			t.Errorf("after %s: %s, want %s", c.now, got, c.next)
		}
	}
}

func TestCountsAreComparedTableByTable(t *testing.T) {
	ok, detail := compareCounts(map[string]int64{"a": 1, "b": 2}, map[string]int64{"b": 2, "a": 1})
	if !ok || detail != "2 tables, 3 rows, as when it was taken" {
		t.Fatalf("%v %s", ok, detail)
	}
	ok, detail = compareCounts(map[string]int64{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5}, map[string]int64{"x": 1})
	if ok || detail != "the rows differ: a: 1 taken, not restored; b: 2 taken, not restored; c: 3 taken, not restored; 3 more" {
		t.Fatalf("%v %s", ok, detail)
	}
	if _, err := parseCounts(`{"a":-1}`); err == nil {
		t.Fatal("a negative count")
	}
	if _, err := parseCounts(`[1]`); err == nil {
		t.Fatal("not an object")
	}
	if counts, err := parseCounts(` {} `); err != nil || len(counts) != 0 {
		t.Fatalf("no tables = %v %v", counts, err)
	}
}

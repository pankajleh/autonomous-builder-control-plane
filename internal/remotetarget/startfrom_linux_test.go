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

// startFromFixture is a Node app updated once: its first release's data,
// three rows, backed up just before the update, and today's data, six rows,
// written by the second release.
func startFromFixture(t *testing.T) (f *deployFixture, first, second string, before serviceapi.BackupV1) {
	t.Helper()
	f = newDeployFixture(t)
	first = f.putNode("one")
	f.deploy(first, "crm", testHost, "req-crm-first")
	f.rows("app_crm", `{"public.members":3}`)
	second = f.putNode("two")
	if _, op := f.deploy(second, "crm", testHost, "req-crm-second"); op.State != opSucceeded {
		t.Fatalf("second = %+v", op)
	}
	list, _ := f.m.Backups(context.Background(), f.key, "crm")
	if len(list.Backups) != 1 || list.Backups[0].Release != first || list.Backups[0].Reason != reasonBeforeUpdate {
		t.Fatalf("backups = %+v", list.Backups)
	}
	f.rows("app_crm", `{"public.members":5,"public.notes":1}`)
	return f, first, second, list.Backups[0]
}

func (f *deployFixture) startFrom(digest, backup, request string) (serviceapi.PlanV1, serviceapi.OperationV1) {
	f.t.Helper()
	plan, err := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Release: digest, Site: serviceapi.PlanSiteV1{Name: "crm", Host: testHost}, StartFrom: backup})
	if err != nil {
		f.t.Fatalf("plan: %v", err)
	}
	return plan, f.finished(f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: plan.PlanID, PlanHash: plan.Hash, RequestID: request}))
}

func TestAnUpdateStartsItsReleaseOnABackupTakenWhileThatReleaseRan(t *testing.T) {
	f, first, second, before := startFromFixture(t)
	ran := len(f.remote.docker())
	plan, op := f.startFrom(first, before.BackupID, "req-crm-back")
	want := "send_files,get_images,start_database,make_database,stop_app,back_up,restore_data,switch_site,start_app,wait_health,write_site,reload_frontdoor,check,clean_up"
	if strings.Join(plan.Steps, ",") != want || plan.Kind != "" || plan.Replaces != second || plan.Conversion != nil ||
		plan.StartFrom == nil || plan.StartFrom.BackupID != before.BackupID || plan.Hash != planHash(plan) {
		t.Fatalf("plan = %+v", plan)
	}
	if op.State != opSucceeded || op.Kind != "apply" ||
		stepOf(op, "restore_data", "").Detail != "1 tables, 3 rows, as when it was taken; switched; the database it replaced is kept until the app answers" {
		t.Fatalf("operation = %+v", op)
	}
	// Stopped before today's data is backed up, restored after it, before the release starts.
	docker := f.remote.docker()[ran:]
	stop, dump := strings.Index(docker, "rm -f autobuild-app-crm"), strings.Index(docker, "-d app_crm -qAtX")
	restore, start := strings.Index(docker, "pg_restore"), strings.Index(docker, "run -d --name autobuild-app-crm")
	if stop < 0 || !(stop < dump && dump < restore && restore < start) {
		t.Fatalf("order: stop %d backup %d restore %d start %d\n%s", stop, dump, restore, start, docker)
	}
	if f.held("app_crm") != `{"public.members":3}` || f.held("restore_app_crm") != "absent" || f.held("previous_app_crm") != "absent" {
		t.Fatalf("databases: app %s, copy %s, previous %s", f.held("app_crm"), f.held("restore_app_crm"), f.held("previous_app_crm"))
	}
	// Today's data is kept as a backup taken under the release it ran.
	list, _ := f.m.Backups(context.Background(), f.key, "crm")
	if len(list.Backups) != 2 || list.Backups[1].Reason != reasonBeforeUpdate || *list.Backups[1].Rows != 6 || list.Backups[1].Release != second ||
		list.Backups[0].Check == nil || !list.Backups[0].Check.OK {
		t.Fatalf("backups = %+v", list.Backups)
	}
	if link, _ := os.Readlink(filepath.Join(f.remote.home, "autobuild/sites/crm")); link != "../releases/"+first || !f.state("c-autobuild-app-crm") {
		t.Fatalf("link = %q", link)
	}
	sites, _ := f.m.Sites(context.Background(), f.key)
	if sites.Sites[0].Release != first || sites.Sites[0].State != siteLive {
		t.Fatalf("site = %+v", sites.Sites[0])
	}

	// And forward again, on the backup of the data the second release had.
	_, op = f.startFrom(second, list.Backups[1].BackupID, "req-crm-forward")
	if op.State != opSucceeded || f.held("app_crm") != `{"public.members":5,"public.notes":1}` || f.held("previous_app_crm") != "absent" {
		t.Fatalf("forward = %+v, data %s", op, f.held("app_crm"))
	}
}

func TestAnUpdateOnABackupThatFailsPutsTodaysDataAndReleaseBack(t *testing.T) {
	f, first, second, before := startFromFixture(t)
	today := `{"public.members":5,"public.notes":1}`
	running := func(name string) {
		t.Helper()
		if f.held("app_crm") != today || f.held("restore_app_crm") != "absent" || f.held("previous_app_crm") != "absent" {
			t.Fatalf("%s: databases: app %s, copy %s, previous %s", name, f.held("app_crm"), f.held("restore_app_crm"), f.held("previous_app_crm"))
		}
		if link, _ := os.Readlink(filepath.Join(f.remote.home, "autobuild/sites/crm")); link != "../releases/"+second ||
			!f.state("c-autobuild-app-crm") || f.state("c-autobuild-app-crm.unhealthy") {
			t.Fatalf("%s: the release it replaced is not running again: link %q", name, link)
		}
		sites, _ := f.m.Sites(context.Background(), f.key)
		if sites.Sites[0].Release != second || sites.Sites[0].State != siteFailed {
			t.Fatalf("%s: site = %+v", name, sites.Sites[0])
		}
	}

	// A copy that does not hold the backup's rows is never swapped in.
	f.flag("restore.loses-rows", true)
	_, op := f.startFrom(first, before.BackupID, "req-crm-back-1")
	restore := stepOf(op, "restore_data", "")
	if op.State != opFailed || restore.Detail != "the rows differ: public.members: 3 taken, 0 restored; nothing changed and the app is started again" ||
		stepOf(op, "switch_site", "").State != "pending" {
		t.Fatalf("operation = %+v", op)
	}
	running("a copy that loses rows")
	f.flag("restore.loses-rows", false)

	// A release that never answers gets today's data and the release it replaced back.
	f.flag("app.fails-once", true)
	_, op = f.startFrom(first, before.BackupID, "req-crm-back-2")
	health := stepOf(op, "wait_health", "")
	if op.State != opFailed || health.Detail != "the app did not answer /health; the data it replaced is put back; switched back to the release it replaced" ||
		stepOf(op, "clean_up", "").State != "pending" {
		t.Fatalf("operation = %+v", op)
	}
	running("a release that never answers")
}

func TestAnUpdateStartsOnlyOnACountedBackupOfThatReleaseOfARunningApp(t *testing.T) {
	f, first, second, before := startFromFixture(t)
	plan := func(digest, site, backup, sql string) error {
		host := testHost
		if site != "crm" {
			host = site + ".13-232-1-2.sslip.io"
		}
		_, err := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Release: digest, Site: serviceapi.PlanSiteV1{Name: site, Host: host}, StartFrom: backup, Conversion: sql})
		return err
	}
	now := f.backup("crm", "req-backup-now")
	for name, c := range map[string]struct {
		digest, site, backup, sql string
		want                      error
	}{
		"a backup of another release": {first, "crm", now.BackupID, "", serviceapi.ErrTargetInvalid},
		"the release it runs":         {second, "crm", now.BackupID, "", serviceapi.ErrTargetInvalid},
		"an unknown backup":           {first, "crm", "bk-20261014T010000Z-ffff", "", serviceapi.ErrBackupUnknown},
		"a malformed backup":          {first, "crm", "../x", "", serviceapi.ErrTargetInvalid},
		"a conversion as well":        {first, "crm", before.BackupID, testConversion, serviceapi.ErrTargetInvalid},
		"a first version":             {first, "blog", before.BackupID, "", serviceapi.ErrTargetInvalid},
	} {
		if err := plan(c.digest, c.site, c.backup, c.sql); !errors.Is(err, c.want) {
			t.Errorf("%s = %v", name, err)
		}
	}

	// A plan made while the app ran no longer applies once it is stopped, and a stopped app is started first.
	p, err := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Release: first, Site: serviceapi.PlanSiteV1{Name: "crm", Host: testHost}, StartFrom: before.BackupID})
	if err != nil {
		t.Fatal(err)
	}
	if op := f.finished(f.m.StopSite(context.Background(), f.key, "crm", serviceapi.OperationRequestV1{RequestID: "req-crm-stop"})); op.State != opSucceeded {
		t.Fatalf("stop = %+v", op)
	}
	if _, err := f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: p.PlanID, PlanHash: p.Hash, RequestID: "req-crm-stale"}); !errors.Is(err, serviceapi.ErrPlanStale) {
		t.Fatalf("a stopped app = %v", err)
	}
	if err := plan(first, "crm", before.BackupID, ""); !errors.Is(err, serviceapi.ErrSiteStopped) {
		t.Fatalf("plan on a stopped app = %v", err)
	}
	if f.held("app_crm") != `{"public.members":5,"public.notes":1}` {
		t.Fatal("a refusal changed the data")
	}
}

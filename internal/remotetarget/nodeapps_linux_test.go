//go:build linux

package remotetarget

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// nodeFiles is a Node format 1 app, changed by version.
func nodeFiles(version string) map[string]string {
	return map[string]string{
		"package.json":                 `{"type":"module"}`,
		"server.js":                    "// " + version,
		"public/index.html":            "<h1>" + version + "</h1>",
		"migrations/0001_members.sql":  "CREATE TABLE members (id serial PRIMARY KEY);",
		"migrations/0002_add_note.sql": "ALTER TABLE members ADD COLUMN note text;",
	}
}

func asNode(m *manifest, _ *tar.Writer) { m.Kind = kindNode }

func (f *deployFixture) putNode(version string) string {
	f.t.Helper()
	data, digest := release(f.t, nodeFiles(version), asNode)
	if _, created, err := f.m.PutRelease(context.Background(), digest, bytes.NewReader(data)); err != nil || !created {
		f.t.Fatalf("put node = %v %v", created, err)
	}
	return digest
}

func (f *deployFixture) state(name string) bool {
	_, err := os.Stat(filepath.Join(f.remote.home, ".docker", name))
	return err == nil
}

func TestANodeReleaseFollowsNodeFormatOne(t *testing.T) {
	f := newDeployFixture(t)
	data, digest := release(t, nodeFiles("one"), asNode)
	got, created, err := f.m.PutRelease(context.Background(), digest, bytes.NewReader(data))
	if err != nil || !created || got.Kind != kindNode || got.Files != 5 {
		t.Fatalf("put = %+v %v %v", got, created, err)
	}
	without := func(name string) map[string]string { files := nodeFiles("one"); delete(files, name); return files }
	with := func(name, body string) map[string]string { files := nodeFiles("one"); files[name] = body; return files }
	for name, files := range map[string]map[string]string{
		"no server.js":          without("server.js"),
		"no package.json":       without("package.json"),
		"no public/index.html":  without("public/index.html"),
		"installed packages":    with("node_modules/fastify/index.js", "x"),
		"nested packages":       with("lib/node_modules/x.js", "x"),
		"a migration misnamed":  with("migrations/3_Add.sql", "x"),
		"a migration not SQL":   with("migrations/0003_add.js", "x"),
		"a static site as node": {"index.html": "x"},
	} {
		data, digest := release(t, files, asNode)
		if _, _, err := f.m.PutRelease(context.Background(), digest, bytes.NewReader(data)); !errors.Is(err, serviceapi.ErrReleaseInvalid) {
			t.Errorf("%s = %v", name, err)
		}
	}
}

func TestANodeAppGetsTheSharedDatabaseItsOwnLoginAndNetworkAndABackupBeforeEachUpdate(t *testing.T) {
	f := newDeployFixture(t)
	first := f.putNode("one")
	plan, op := f.deploy(first, "crm", testHost, "req-crm-first")
	want := "send_files,get_images,start_database,make_database,switch_site,start_app,wait_health,write_site,start_frontdoor,check"
	if strings.Join(plan.Steps, ",") != want || plan.Database != "app_crm" || plan.Sandbox != sandboxStandard ||
		strings.Join(plan.Images, " ") != NodeRuntimeImage+" "+NodePostgresImage || plan.Hash != planHash(plan) {
		t.Fatalf("plan = %+v", plan)
	}
	if op.State != opSucceeded {
		t.Fatalf("operation = %+v", op)
	}
	home := f.remote.home
	docker := f.remote.docker()
	for _, want := range []string{
		"pull -q " + NodeRuntimeImage, "pull -q " + NodePostgresImage,
		"volume create autobuild-pgdata", "network create --internal autobuild-db",
		"run -d --name autobuild-postgres --restart unless-stopped --network autobuild-db --user 999:999 --read-only --cap-drop ALL",
		"--mount type=volume,src=autobuild-pgdata,dst=/data", NodePostgresImage + " -i PATH=/usr/local/bin:/usr/bin:/bin HOME=/scratch TMPDIR=/scratch /usr/local/bin/autobuild-postgres server",
		"network create --internal --label autobuild.site=crm autobuild-net-crm",
		"network connect --alias autobuild-postgres autobuild-net-crm autobuild-postgres",
		"run -d --name autobuild-app-crm --restart unless-stopped --network autobuild-net-crm --network-alias app",
		"--user 65534:65534 --read-only --cap-drop ALL --security-opt no-new-privileges --memory 256m --memory-swap 256m",
		"--env-file " + home + "/autobuild/apps/crm/database.env", "--entrypoint /usr/local/bin/node " + NodeRuntimeImage + " /opt/autobuild/start.mjs",
		"network connect autobuild-net-crm autobuild-frontdoor",
	} {
		if !strings.Contains(docker, want) {
			t.Fatalf("docker did not get %q:\n%s", want, docker)
		}
	}
	if strings.Contains(docker, "--runtime=runsc") || strings.Contains(docker, "-p 5432") {
		t.Fatalf("a standard server runs no gVisor and publishes no database:\n%s", docker)
	}
	env, err := os.ReadFile(filepath.Join(home, "autobuild/apps/crm/database.env"))
	match := regexp.MustCompile(`^DATABASE_URL=postgres://app_crm:([0-9a-f]{48})@autobuild-postgres:5432/app_crm\n$`).FindStringSubmatch(string(env))
	if err != nil || match == nil {
		t.Fatalf("database.env = %q %v", env, err)
	}
	if info, _ := os.Stat(filepath.Join(home, "autobuild/apps/crm/database.env")); info.Mode().Perm() != 0o600 {
		t.Fatalf("database.env mode = %v", info.Mode())
	}
	password := match[1]
	psqlIn, _ := os.ReadFile(filepath.Join(home, ".docker/psql.stdin"))
	if !strings.Contains(string(psqlIn), "CREATE ROLE app_crm LOGIN PASSWORD '"+password+"'") {
		t.Fatalf("the login was not made through psql's input: %s", psqlIn)
	}
	// The password is made on the server: no command line and no script ABCP sent holds it.
	if strings.Contains(docker, password) {
		t.Fatal("the password is on a command line")
	}
	for _, script := range f.remote.scripts {
		if strings.Contains(script, password) {
			t.Fatal("ABCP sent the password")
		}
	}
	caddy, _ := os.ReadFile(filepath.Join(home, "autobuild/frontdoor/config/Caddyfile"))
	if !strings.Contains(string(caddy), testHost+" {\n\treverse_proxy autobuild-app-crm:8080 {\n\t\theader_up Origin ^https:// http://\n\t}\n") {
		t.Fatalf("Caddyfile = %s", caddy)
	}
	sites, _ := f.m.Sites(context.Background(), f.key)
	if len(sites.Sites) != 1 || sites.Sites[0].Kind != kindNode || sites.Sites[0].Database != "app_crm" || sites.Sites[0].Sandbox != sandboxStandard ||
		sites.Sites[0].State != siteLive || sites.Sites[0].LastCheck == nil || !sites.Sites[0].LastCheck.OK {
		t.Fatalf("sites = %+v", sites)
	}
	if backups, err := f.m.Backups(context.Background(), f.key, "crm"); err != nil || backups.Database != "app_crm" || len(backups.Backups) != 0 {
		t.Fatalf("no backup before the first version: %+v %v", backups, err)
	}

	second := f.putNode("two")
	plan, op = f.deploy(second, "crm", testHost, "req-crm-second")
	want = "send_files,get_images,start_database,make_database,back_up,switch_site,start_app,wait_health,write_site,reload_frontdoor,check"
	if strings.Join(plan.Steps, ",") != want || plan.Replaces != first || op.State != opSucceeded {
		t.Fatalf("second plan %+v op %+v", plan, op)
	}
	for _, s := range op.Steps {
		if (s.Name == "get_images" && s.Detail != "already there\n") || (s.Name == "start_database" && s.Detail != "running\n") || (s.Name == "make_database" && s.Detail != "kept\n") {
			t.Fatalf("step %+v", s)
		}
	}
	if again, _ := os.ReadFile(filepath.Join(home, "autobuild/apps/crm/database.env")); string(again) != string(env) {
		t.Fatal("an update changed the app's password")
	}
	backups, _ := f.m.Backups(context.Background(), f.key, "crm")
	if len(backups.Backups) != 1 || backups.Backups[0].Reason != reasonBeforeUpdate || backups.Backups[0].Release != first || backups.Backups[0].Bytes == 0 ||
		!backupIDPattern.MatchString(backups.Backups[0].BackupID) || backups.Backups[0].Tables == nil || *backups.Backups[0].Tables != 0 ||
		backups.Backups[0].Rows == nil || *backups.Backups[0].Rows != 0 || backups.Backups[0].Check != nil {
		t.Fatalf("backups = %+v", backups)
	}
	dump, err := os.ReadFile(filepath.Join(home, "autobuild/backups/crm", backups.Backups[0].BackupID+".dump"))
	if err != nil || !strings.Contains(string(dump), "PGDMP fake dump of app_crm") {
		t.Fatalf("dump = %q %v", dump, err)
	}
	if link, _ := os.Readlink(filepath.Join(home, "autobuild/sites/crm")); link != "../releases/"+second {
		t.Fatalf("link = %q", link)
	}

	// A static site beside it: the front door serves both, and joins only the app's network.
	shop := f.put(map[string]string{"index.html": "<h1>Shop</h1>"})
	if _, op := f.deploy(shop, "shop", "shop.13-232-1-2.sslip.io", "req-shop-first"); op.State != opFailed || op.Steps[len(op.Steps)-1].Detail != "dns" {
		t.Fatalf("shop = %+v", op)
	}
	caddy, _ = os.ReadFile(filepath.Join(home, "autobuild/frontdoor/config/Caddyfile"))
	if !strings.Contains(string(caddy), "shop.13-232-1-2.sslip.io {\n\troot * /srv/sites/shop\n\tfile_server") || !strings.Contains(string(caddy), "reverse_proxy autobuild-app-crm:8080") {
		t.Fatalf("Caddyfile = %s", caddy)
	}
	if strings.Contains(f.remote.docker(), "autobuild-net-shop") {
		t.Fatal("a static site has no network of its own")
	}
	// A site keeps its kind.
	if _, err := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Release: shop, Site: serviceapi.PlanSiteV1{Name: "crm", Host: testHost}}); !errors.Is(err, serviceapi.ErrTargetInvalid) {
		t.Fatalf("a static release for a Node site = %v", err)
	}
	for _, script := range f.remote.scripts {
		if regexp.MustCompile(`@[A-Z]+@`).MatchString(script) || strings.Contains(script, "sudo") {
			t.Fatalf("a script = %s", script)
		}
	}
}

func TestANewVersionThatNeverAnswersIsSwitchedBackWithoutTouchingData(t *testing.T) {
	f := newDeployFixture(t)
	first := f.putNode("one")
	if _, op := f.deploy(first, "crm", testHost, "req-crm-first"); op.State != opSucceeded {
		t.Fatalf("first = %+v", op)
	}
	second := f.putNode("two")
	if err := os.WriteFile(filepath.Join(f.remote.home, "app.fails-once"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, op := f.deploy(second, "crm", testHost, "req-crm-second")
	var health serviceapi.OperationStepV1
	for _, s := range op.Steps {
		if s.Name == "wait_health" {
			health = s
		}
	}
	if op.State != opFailed || health.State != "failed" || health.Detail != "the app did not answer /health; switched back to the release it replaced" {
		t.Fatalf("operation = %+v", op)
	}
	if link, _ := os.Readlink(filepath.Join(f.remote.home, "autobuild/sites/crm")); link != "../releases/"+first {
		t.Fatalf("link = %q", link)
	}
	if !f.state("c-autobuild-app-crm") || f.state("c-autobuild-app-crm.unhealthy") {
		t.Fatal("the release it replaced is not running again")
	}
	sites, _ := f.m.Sites(context.Background(), f.key)
	if sites.Sites[0].Release != first || sites.Sites[0].State != siteFailed {
		t.Fatalf("site = %+v", sites.Sites[0])
	}
	if psqlIn, _ := os.ReadFile(filepath.Join(f.remote.home, ".docker/psql.stdin")); strings.Count(string(psqlIn), "ROLE") != 1 || strings.Contains(f.remote.docker(), "pg_restore") {
		t.Fatalf("data was touched: %s", psqlIn)
	}
	if backups, _ := f.m.Backups(context.Background(), f.key, "crm"); len(backups.Backups) != 1 {
		t.Fatalf("backups = %+v", backups)
	}
	// The next update starts from the release that is running.
	plan, err := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Release: second, Site: serviceapi.PlanSiteV1{Name: "crm", Host: testHost}})
	if err != nil || plan.Replaces != first || plan.Steps[0] != "reuse_files" {
		t.Fatalf("plan = %+v %v", plan, err)
	}

	// A first version that never answers is stopped.
	other := f.putNode("other")
	if err := os.WriteFile(filepath.Join(f.remote.home, "app.fails-once"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, op = f.deploy(other, "blog", "blog.13-232-1-2.sslip.io", "req-blog-first")
	for _, s := range op.Steps {
		if s.Name == "wait_health" {
			health = s
		}
	}
	if op.State != opFailed || health.Detail != "the app did not answer /health; the new app is stopped" || f.state("c-autobuild-app-blog") {
		t.Fatalf("blog = %+v", op)
	}
}

func TestRemovingANodeAppKeepsItsDatabaseAndBackups(t *testing.T) {
	f := newDeployFixture(t)
	first := f.putNode("one")
	f.deploy(first, "crm", testHost, "req-crm-first")
	op, err := f.m.BackUp(context.Background(), f.key, "crm", serviceapi.BackupCreateV1{RequestID: "req-backup-1"})
	if err != nil {
		t.Fatal(err)
	}
	f.m.Wait()
	if again, _ := f.m.BackUp(context.Background(), f.key, "crm", serviceapi.BackupCreateV1{RequestID: "req-backup-1"}); again.OperationID != op.OperationID {
		t.Fatal("the same request is one backup")
	}
	if op, _ = f.m.Operation(context.Background(), f.key, op.OperationID); op.State != opSucceeded || op.Kind != "backup" {
		t.Fatalf("backup = %+v", op)
	}
	before := len(f.remote.docker())
	op, err = f.m.RemoveSite(context.Background(), f.key, "crm", serviceapi.SiteRemoveV1{RequestID: "req-remove-1"})
	if err != nil {
		t.Fatal(err)
	}
	f.m.Wait()
	op, _ = f.m.Operation(context.Background(), f.key, op.OperationID)
	names := []string{}
	for _, s := range op.Steps {
		names = append(names, s.Name)
	}
	if op.State != opSucceeded || strings.Join(names, ",") != "stop_app,unlink_site,write_site,reload_frontdoor,remove_files" {
		t.Fatalf("remove = %+v", op)
	}
	docker := f.remote.docker()[before:]
	for _, want := range []string{"rm -f autobuild-app-crm", "network disconnect -f autobuild-net-crm autobuild-postgres", "network rm autobuild-net-crm", "rm -f autobuild-frontdoor"} {
		if !strings.Contains(docker, want) {
			t.Fatalf("docker did not get %q:\n%s", want, docker)
		}
	}
	if strings.Contains(docker, "DROP DATABASE") || strings.Contains(docker, "rm -f autobuild-postgres") || strings.Contains(docker, "volume rm") || !f.state("c-autobuild-postgres") {
		t.Fatalf("the database was touched:\n%s", docker)
	}
	backups, err := f.m.Backups(context.Background(), f.key, "crm")
	if err != nil || len(backups.Backups) != 1 || backups.Backups[0].Reason != reasonManual {
		t.Fatalf("backups after removing = %+v %v", backups, err)
	}
	if _, err := os.Stat(filepath.Join(f.remote.home, "autobuild/backups/crm", backups.Backups[0].BackupID+".dump")); err != nil {
		t.Fatal("the backup file is gone")
	}
	// Put back under the same name, it gets its database and login again.
	_, op = f.deploy(first, "crm", testHost, "req-crm-again")
	if op.State != opSucceeded || op.Steps[3].Name != "make_database" || op.Steps[3].Detail != "kept\n" {
		t.Fatalf("again = %+v", op)
	}
	if _, err := f.m.Backups(context.Background(), f.key, "blog"); !errors.Is(err, serviceapi.ErrSiteUnknown) {
		t.Fatalf("an app with no database = %v", err)
	}
	if _, err := f.m.BackUp(context.Background(), f.key, "blog", serviceapi.BackupCreateV1{RequestID: "req-backup-2"}); !errors.Is(err, serviceapi.ErrSiteUnknown) {
		t.Fatalf("a backup of no database = %v", err)
	}
	if _, err := f.m.BackUp(context.Background(), f.key, "crm", serviceapi.BackupCreateV1{RequestID: "x"}); !errors.Is(err, serviceapi.ErrTargetInvalid) {
		t.Fatalf("a short request id = %v", err)
	}
}

func TestANodeAppRunsUnderGVisorWhenTheServerHasIt(t *testing.T) {
	f := newDeployFixture(t)
	r, _ := f.m.read(f.key)
	r.Preflight = &serviceapi.TargetPreflightV1{Outcome: "checked", Ready: true, Facts: &serviceapi.TargetFactsV1{GVisor: true}}
	_ = f.m.write(r)
	plan, op := f.deploy(f.putNode("one"), "crm", testHost, "req-crm-first")
	if plan.Sandbox != sandboxGVisor || op.State != opSucceeded || !strings.Contains(f.remote.docker(), "--network-alias app --runtime=runsc --user 65534:65534") {
		t.Fatalf("plan %+v op %+v:\n%s", plan, op, f.remote.docker())
	}
	if sites, _ := f.m.Sites(context.Background(), f.key); sites.Sites[0].Sandbox != sandboxGVisor {
		t.Fatalf("sites = %+v", sites)
	}
}

func TestBackupsAreKeptSevenDaysAndAtMostFourteenAndTheNewestAlways(t *testing.T) {
	now := time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)
	backup := func(age time.Duration, n int) backupRecord {
		return backupRecord{BackupV1: serviceapi.BackupV1{BackupID: fmt.Sprintf("bk-%s-%04x", now.Add(-age).Format("20060102T150405Z"), n),
			TakenAt: now.Add(-age).Format(time.RFC3339)}}
	}
	var list []backupRecord
	for i := 16; i > 0; i-- {
		list = append(list, backup(time.Duration(i)*time.Hour, i))
	}
	kept, drop := pruneBackupList(list, now, "")
	if len(kept) != 14 || len(drop) != 2 || drop[0] != list[0].BackupID || kept[13].BackupID != list[15].BackupID {
		t.Fatalf("kept %d, dropped %v", len(kept), drop)
	}
	// The backup a restore puts back is kept while it runs.
	kept, drop = pruneBackupList(list, now, list[0].BackupID)
	if len(kept) != 14 || kept[0].BackupID != list[0].BackupID || strings.Join(drop, " ") != list[1].BackupID+" "+list[2].BackupID {
		t.Fatalf("kept %d, dropped %v", len(kept), drop)
	}
	old := []backupRecord{backup(10*24*time.Hour, 1), backup(9*24*time.Hour, 2)}
	kept, drop = pruneBackupList(old, now, "")
	if len(kept) != 1 || kept[0].BackupID != old[1].BackupID || len(drop) != 1 {
		t.Fatalf("the newest stays: kept %+v dropped %v", kept, drop)
	}
	if kept, _ = pruneBackupList(old, now, old[0].BackupID); len(kept) != 2 {
		t.Fatalf("an old backup being restored stays: %+v", kept)
	}
}

// The app's heap stays under its 256 MB whichever sandbox runs it: gVisor shows
// the app the server's memory, not the container's limit.
func TestStartAppCapsTheHeap(t *testing.T) {
	if !strings.Contains(startAppScript, "--memory 256m") || !strings.Contains(startAppScript, "-e NODE_OPTIONS=--max-old-space-size=160 ") {
		t.Fatal("the app's start has no heap cap under its memory limit")
	}
}

// An app on a server runs all the time, so it runs its own timetable, with or
// without relays (Repo C design note BF08_NO_PAGE_OPEN §19).
func TestStartAppRunsTheAppsTimetable(t *testing.T) {
	for _, relay := range []string{"", "-e AUTOBUILD_RELAY=" + insideRelay + " "} {
		if script := fill(startAppScript, "N", "crm", "RUNTIME", "", "RELAY", relay); strings.Count(script, " -e AUTOBUILD_JOBS=run ") != 1 {
			t.Fatalf("the app's timetable does not run on its own (relay %q):\n%s", relay, script)
		}
	}
}

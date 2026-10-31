//go:build linux

package remotetarget

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

const testAdmin = "owner@example.com"

// wpFiles is a WordPress website in Repo C's format, changed by version.
func wpFiles(version string) map[string]string {
	return map[string]string{
		"README.md":            "# Northwind Studio\n",
		"site/site.json":       `{"format":"autobuild-wp-site/1","name":"Northwind Studio"}`,
		"site/pages.json":      `{"pages":[{"slug":"home","title":"Home","description":"Design studio","front":true}]}`,
		"site/pages/home.html": "<!-- wp:paragraph --><p>" + version + "</p><!-- /wp:paragraph -->",
		"site/media/logo.png":  "png",
		"theme/style.css":      "/* Theme Name: Autobuild website */",
		"theme/theme.json":     `{"version":3}`,
		"theme/functions.php":  "<?php // Twenty Twenty-Five's own",
	}
}

func asWP(m *manifest, _ *tar.Writer) { m.Kind = kindWP }

func (f *deployFixture) putWP(version string) string {
	f.t.Helper()
	data, digest := release(f.t, wpFiles(version), asWP)
	if _, created, err := f.m.PutRelease(context.Background(), digest, bytes.NewReader(data)); err != nil || !created {
		f.t.Fatalf("put wp = %v %v", created, err)
	}
	return digest
}

func (f *deployFixture) deployWP(digest, site, host, email, request string) (serviceapi.PlanV1, serviceapi.OperationV1) {
	f.t.Helper()
	plan, err := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Release: digest, Site: serviceapi.PlanSiteV1{Name: site, Host: host},
		AdminEmail: email})
	if err != nil {
		f.t.Fatalf("plan: %v", err)
	}
	return plan, f.finished(f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: plan.PlanID, PlanHash: plan.Hash, RequestID: request}))
}

// volume is the folder standing for a WordPress website's volume.
func (f *deployFixture) volume(site string) string {
	return filepath.Join(f.remote.home, ".docker", "wpvol-autobuild-wp-"+site)
}

func (f *deployFixture) upload(site, name, body string) {
	f.t.Helper()
	path := filepath.Join(f.volume(site), "uploads", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *deployFixture) media(site string) string {
	var out []string
	root := filepath.Join(f.volume(site), "uploads")
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			body, _ := os.ReadFile(path)
			out = append(out, strings.TrimPrefix(path, root+"/")+"="+string(body))
		}
		return nil
	})
	return strings.Join(out, " ")
}

func TestAWordPressReleaseFollowsTheWordPressWebsiteFormat(t *testing.T) {
	f := newDeployFixture(t)
	data, digest := release(t, wpFiles("one"), asWP)
	got, created, err := f.m.PutRelease(context.Background(), digest, bytes.NewReader(data))
	if err != nil || !created || got.Kind != kindWP || got.Files != 8 {
		t.Fatalf("put = %+v %v %v", got, created, err)
	}
	without := func(name string) map[string]string { files := wpFiles("one"); delete(files, name); return files }
	with := func(name, body string) map[string]string { files := wpFiles("one"); files[name] = body; return files }
	for name, files := range map[string]map[string]string{
		"no site/site.json":         without("site/site.json"),
		"no site/pages.json":        without("site/pages.json"),
		"no theme/style.css":        without("theme/style.css"),
		"no README.md":              without("README.md"),
		"PHP in site/":              with("site/pages/x.php", "<?php"),
		"PHP at the top":            with("wp-config.php", "<?php"),
		"PHP in capitals":           with("site/media/shell.PHP", "<?php"),
		"another PHP extension":     with("plugin/x.phtml", "<?php"),
		"a PHP archive":             with("site/media/x.phar", "x"),
		"a static site as a wp one": {"index.html": "x"},
		"a Node app as a wp one":    nodeFiles("one"),
	} {
		data, digest := release(t, files, asWP)
		if _, _, err := f.m.PutRelease(context.Background(), digest, bytes.NewReader(data)); !errors.Is(err, serviceapi.ErrReleaseInvalid) {
			t.Errorf("%s = %v", name, err)
		}
	}
	// A WordPress website is not another kind's release.
	data, digest = release(t, wpFiles("one"), asNode)
	if _, _, err := f.m.PutRelease(context.Background(), digest, bytes.NewReader(data)); !errors.Is(err, serviceapi.ErrReleaseInvalid) {
		t.Fatalf("a wp site as a Node app = %v", err)
	}
}

func TestAWordPressWebsiteGetsTheSharedMariaDBItsOwnLoginNetworkAndVolume(t *testing.T) {
	f := newDeployFixture(t)
	first := f.putWP("one")
	plan, op := f.deployWP(first, "blog", testHost, " Owner@Example.com ", "req-blog-first")
	want := "send_files,get_images,start_database,make_database,switch_site,start_app,wait_health,write_site,start_frontdoor,check"
	if strings.Join(plan.Steps, ",") != want || plan.Database != "wp_blog" || plan.Sandbox != sandboxStandard || plan.AdminEmail != testAdmin ||
		strings.Join(plan.Images, " ") != WordPressServerImage+" "+WordPressMariaDBImage || plan.Hash != planHash(plan) || plan.Release.Kind != kindWP {
		t.Fatalf("plan = %+v", plan)
	}
	if op.State != opSucceeded {
		t.Fatalf("operation = %+v", op)
	}
	home := f.remote.home
	docker := f.remote.docker()
	for _, want := range []string{
		"pull -q " + WordPressServerImage, "pull -q " + WordPressMariaDBImage,
		"volume create autobuild-mariadb-data", "network create --internal autobuild-db",
		"run -d --name autobuild-mariadb --restart unless-stopped --network autobuild-db --user 999:999 --read-only --cap-drop ALL",
		"--mount type=volume,src=autobuild-mariadb-data,dst=/data --entrypoint /usr/local/bin/autobuild-mariadb " + WordPressMariaDBImage + " server",
		"network create --internal --label autobuild.site=blog autobuild-net-blog",
		"network connect --alias autobuild-mariadb autobuild-net-blog autobuild-mariadb",
		"exec -i autobuild-mariadb autobuild-mariadb create-site wp_blog",
		"volume create autobuild-wp-blog",
		"run -d --name autobuild-app-blog --restart unless-stopped --network autobuild-net-blog --network-alias app",
		"--user 33:33 --read-only --cap-drop ALL --security-opt no-new-privileges --memory 512m --memory-swap 512m",
		"--tmpfs /scratch:rw,nosuid,nodev,noexec,size=134217728,mode=700,uid=33,gid=33",
		"--mount type=volume,src=autobuild-wp-blog,dst=/data",
		"--env-file " + home + "/autobuild/apps/blog/database.env --env-file " + home + "/autobuild/apps/blog/site.env",
		"--entrypoint /usr/local/bin/autobuild-wp-server " + WordPressServerImage,
		"exec autobuild-app-blog /bin/busybox wget -T 2 -q -O /dev/null http://127.0.0.1:8080/health",
		"network connect autobuild-net-blog autobuild-frontdoor",
	} {
		if !strings.Contains(docker, want) {
			t.Fatalf("docker did not get %q:\n%s", want, docker)
		}
	}
	if strings.Contains(docker, "autobuild-postgres") || strings.Contains(docker, "--runtime=runsc") || strings.Contains(docker, "-p 3306") {
		t.Fatalf("a WordPress website needs no Postgres, runs no gVisor on a standard server and publishes no database:\n%s", docker)
	}
	// The password is made on the server: in database.env and on MariaDB's input, nowhere else.
	env, err := os.ReadFile(filepath.Join(home, "autobuild/apps/blog/database.env"))
	match := regexp.MustCompile(`^DB_HOST=autobuild-mariadb:3306\nDB_NAME=wp_blog\nDB_USER=wp_blog\nDB_PASSWORD=([0-9a-f]{48})\n$`).FindStringSubmatch(string(env))
	if err != nil || match == nil {
		t.Fatalf("database.env = %q %v", env, err)
	}
	password := match[1]
	if stdin, _ := os.ReadFile(filepath.Join(home, ".docker/mariadb.stdin")); string(stdin) != password+"\n" || !f.state("role-wp_blog") {
		t.Fatalf("the login was not made through create-site's input: %q", stdin)
	}
	if strings.Contains(docker, password) {
		t.Fatal("the password is on a command line")
	}
	// The site's settings: its address and its administrator's, 0600, written on the server.
	siteEnv, _ := os.ReadFile(filepath.Join(home, "autobuild/apps/blog/site.env"))
	if string(siteEnv) != "SITE_URL=https://"+testHost+"\nADMIN_EMAIL="+testAdmin+"\n" {
		t.Fatalf("site.env = %q", siteEnv)
	}
	for _, name := range []string{"database.env", "site.env"} {
		if info, _ := os.Stat(filepath.Join(home, "autobuild/apps/blog", name)); info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v", name, info.Mode())
		}
	}
	if started, _ := os.ReadFile(filepath.Join(home, ".docker/c-autobuild-app-blog.env")); string(started) != string(env)+string(siteEnv) {
		t.Fatalf("the site started with %q", started)
	}
	for _, script := range f.remote.scripts {
		if strings.Contains(script, password) || strings.Contains(script, testAdmin) {
			t.Fatal("ABCP sent the password or the administrator's address in a script")
		}
		if regexp.MustCompile(`@[A-Z]+@`).MatchString(script) || strings.Contains(script, "sudo") {
			t.Fatalf("a script = %s", script)
		}
	}
	// The release's manifest is kept at /source, checked as every other file.
	rec, _ := f.m.readRelease(first)
	manifestBody, err := os.ReadFile(filepath.Join(home, "autobuild/releases", first, manifestName))
	sum := sha256.Sum256(manifestBody)
	if err != nil || rec.Manifest == nil || hex.EncodeToString(sum[:]) != rec.Manifest.SHA256 || !strings.Contains(string(manifestBody), `"kind":"wp"`) {
		t.Fatalf("the manifest at /source = %q %v (%+v)", manifestBody, err, rec.Manifest)
	}
	caddy, _ := os.ReadFile(filepath.Join(home, "autobuild/frontdoor/config/Caddyfile"))
	if !strings.Contains(string(caddy), "\n"+testHost+" {\n\treverse_proxy autobuild-app-blog:8080\n\tencode zstd gzip\n") {
		t.Fatalf("Caddyfile = %s", caddy)
	}
	sites, _ := f.m.Sites(context.Background(), f.key)
	if len(sites.Sites) != 1 || sites.Sites[0].Kind != kindWP || sites.Sites[0].Database != "wp_blog" || sites.Sites[0].Sandbox != sandboxStandard ||
		sites.Sites[0].State != siteLive || sites.Sites[0].LastCheck == nil || !sites.Sites[0].LastCheck.OK {
		t.Fatalf("sites = %+v", sites)
	}
	if backups, err := f.m.Backups(context.Background(), f.key, "blog"); err != nil || backups.Database != "wp_blog" || len(backups.Backups) != 0 {
		t.Fatalf("no backup before the first version: %+v %v", backups, err)
	}

	// An update: a backup of both parts first, the same password and administrator.
	f.upload("blog", "2026/10/logo.png", "logo")
	second := f.putWP("two")
	plan, op = f.deployWP(second, "blog", testHost, "someone.else@example.com", "req-blog-second")
	want = "send_files,get_images,start_database,make_database,back_up,switch_site,start_app,wait_health,write_site,reload_frontdoor,check"
	if strings.Join(plan.Steps, ",") != want || plan.Replaces != first || op.State != opSucceeded || plan.AdminEmail != testAdmin {
		t.Fatalf("second plan %+v op %+v", plan, op)
	}
	if s := stepOf(op, "make_database", ""); s.Detail != "kept\n" {
		t.Fatalf("make_database = %+v", s)
	}
	if again, _ := os.ReadFile(filepath.Join(home, "autobuild/apps/blog/database.env")); string(again) != string(env) {
		t.Fatal("an update changed the site's password")
	}
	if again, _ := os.ReadFile(filepath.Join(home, "autobuild/apps/blog/site.env")); string(again) != string(siteEnv) {
		t.Fatalf("an update changed the site's administrator: %q", again)
	}
	backups, _ := f.m.Backups(context.Background(), f.key, "blog")
	if len(backups.Backups) != 1 {
		t.Fatalf("backups = %+v", backups)
	}
	b := backups.Backups[0]
	if b.Reason != reasonBeforeUpdate || b.Release != first || b.Bytes == 0 || b.UploadsBytes == nil || *b.UploadsBytes == 0 ||
		b.Tables == nil || *b.Tables != 3 || b.Rows == nil || *b.Rows != 148 || len(b.Counts) != 3 {
		t.Fatalf("backup = %+v", b)
	}
	if s := stepOf(op, "back_up", ""); !strings.HasSuffix(s.Detail, "3 tables, 148 rows; uploads "+strconv.FormatInt(*b.UploadsBytes, 10)+" bytes") {
		t.Fatalf("back_up = %+v", s)
	}
	dump, _ := os.ReadFile(filepath.Join(home, "autobuild/backups/blog", b.BackupID+".dump"))
	listing, err := exec.Command("tar", "-tf", filepath.Join(home, "autobuild/backups/blog", b.BackupID+".uploads.tar")).Output()
	if !strings.HasPrefix(string(dump), "MARIADB fake dump of wp_blog\n") || err != nil || !strings.Contains(string(listing), "uploads/2026/10/logo.png") {
		t.Fatalf("dump %q, uploads %q %v", dump, listing, err)
	}
	if f.held("check_wp_blog") != "absent" {
		t.Fatal("the copy the rows were counted in is left")
	}

	// A site keeps its kind, and an address is asked for a WordPress website alone.
	node := f.putNode("one")
	shop := f.put(map[string]string{"index.html": "<h1>Shop</h1>"})
	for name, create := range map[string]serviceapi.PlanCreateV1{
		"a Node release for a WordPress website":   {Release: node, Site: serviceapi.PlanSiteV1{Name: "blog", Host: testHost}},
		"a static release for a WordPress website": {Release: shop, Site: serviceapi.PlanSiteV1{Name: "blog", Host: testHost}},
		"a website without an address":             {Release: second, Site: serviceapi.PlanSiteV1{Name: "news", Host: "news.example.com"}},
		"a website with a bad address":             {Release: second, Site: serviceapi.PlanSiteV1{Name: "news", Host: "news.example.com"}, AdminEmail: "owner@localhost"},
		"a website with a shell in its address":    {Release: second, Site: serviceapi.PlanSiteV1{Name: "news", Host: "news.example.com"}, AdminEmail: "$(id)@example.com"},
		"a static site with an address":            {Release: shop, Site: serviceapi.PlanSiteV1{Name: "shop", Host: "shop.example.com"}, AdminEmail: testAdmin},
		"a Node app with an address":               {Release: node, Site: serviceapi.PlanSiteV1{Name: "crm", Host: "crm.example.com"}, AdminEmail: testAdmin},
		"a restore with an address":                {Restore: &serviceapi.PlanRestoreV1{Site: "blog", Backup: b.BackupID}, AdminEmail: testAdmin},
	} {
		if _, err := f.m.CreatePlan(context.Background(), f.key, create); !errors.Is(err, serviceapi.ErrTargetInvalid) {
			t.Errorf("%s = %v", name, err)
		}
	}
}

func TestAWordPressWebsiteThatNeverAnswersIsSwitchedBackWithoutTouchingData(t *testing.T) {
	f := newDeployFixture(t)
	first := f.putWP("one")
	if _, op := f.deployWP(first, "blog", testHost, testAdmin, "req-blog-first"); op.State != opSucceeded {
		t.Fatalf("first = %+v", op)
	}
	f.upload("blog", "logo.png", "one")
	f.flag("app.fails-once", true)
	_, op := f.deployWP(f.putWP("two"), "blog", testHost, testAdmin, "req-blog-second")
	health := stepOf(op, "wait_health", "")
	if op.State != opFailed || health.Detail != "the app did not answer /health; switched back to the release it replaced" {
		t.Fatalf("operation = %+v", op)
	}
	if link, _ := os.Readlink(filepath.Join(f.remote.home, "autobuild/sites/blog")); link != "../releases/"+first {
		t.Fatalf("link = %q", link)
	}
	if !f.state("c-autobuild-app-blog") || f.state("c-autobuild-app-blog.unhealthy") {
		t.Fatal("the release it replaced is not running again")
	}
	if f.media("blog") != "logo.png=one" || f.held("wp_blog") != `{"wp_options":135,"wp_posts":12,"wp_users":1}` || strings.Contains(f.remote.docker(), "files restore") {
		t.Fatalf("data was touched: %s, %s", f.media("blog"), f.held("wp_blog"))
	}
	if sites, _ := f.m.Sites(context.Background(), f.key); sites.Sites[0].Release != first || sites.Sites[0].State != siteFailed {
		t.Fatalf("site = %+v", sites.Sites[0])
	}
}

// W5b (Repo C WORDPRESS_WEBSITES.md §13.3): the administrator's password is
// read from the site once and passes through ABCP to the answer alone; a
// reset link is sent to the administrator's address, which reaches the
// server on a script's input.
func TestTheAdministratorsPasswordIsReadOnceAndNeverKept(t *testing.T) {
	f := newDeployFixture(t)
	f.deployWP(f.putWP("one"), "blog", testHost, testAdmin, "req-blog-first")
	got, err := f.m.SiteAdminPassword(context.Background(), f.key, "blog")
	if err != nil || !regexp.MustCompile(`^[0-9a-f]{48}$`).MatchString(got.Password) || got.Taken || got.User != testAdmin || got.Email != testAdmin ||
		got.Host != testHost || got.Site != "blog" || got.Target != f.key || got.SchemaVersion != serviceapi.SiteAdminPasswordSchemaV1 {
		t.Fatalf("password = %+v %v", got, err)
	}
	again, err := f.m.SiteAdminPassword(context.Background(), f.key, "blog")
	if err != nil || !again.Taken || again.Password != "" || again.User != testAdmin {
		t.Fatalf("a second read = %+v %v", again, err)
	}
	// Nothing ABCP keeps or ran holds it.
	_ = filepath.Walk(f.m.dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			if body, _ := os.ReadFile(path); bytes.Contains(body, []byte(got.Password)) {
				t.Fatalf("%s holds the password", path)
			}
		}
		return nil
	})
	for _, script := range f.remote.scripts {
		if strings.Contains(script, got.Password) {
			t.Fatal("a script holds the password")
		}
	}
	if strings.Contains(f.remote.docker(), got.Password) || !strings.Contains(f.remote.docker(), "exec autobuild-app-blog admin-password take") {
		t.Fatalf("docker:\n%s", f.remote.docker())
	}

	sent, err := f.m.SiteResetLink(context.Background(), f.key, "blog")
	if err != nil || !sent.Sent || sent.Email != testAdmin || sent.SchemaVersion != serviceapi.SiteResetLinkSchemaV1 {
		t.Fatalf("reset link = %+v %v", sent, err)
	}
	if asked, _ := os.ReadFile(filepath.Join(f.remote.home, ".docker/reset.log")); string(asked) != testAdmin+"\n" {
		t.Fatalf("the reset link went to %q", asked)
	}
	for _, script := range f.remote.scripts {
		if strings.Contains(script, testAdmin) {
			t.Fatal("a script holds the administrator's address")
		}
	}
	f.flag("reset.fails", true)
	if _, err := f.m.SiteResetLink(context.Background(), f.key, "blog"); !errors.Is(err, serviceapi.ErrNotSent) {
		t.Fatalf("a link not sent = %v", err)
	}

	f.deploy(f.put(map[string]string{"index.html": "x"}), "shop", "shop.13-232-1-2.sslip.io", "req-shop-first")
	f.deploy(f.putNode("one"), "crm", "crm.13-232-1-2.sslip.io", "req-crm-first")
	for _, c := range []struct {
		key, site string
		want      error
	}{
		{f.key, "shop", serviceapi.ErrTargetInvalid},
		{f.key, "crm", serviceapi.ErrTargetInvalid},
		{f.key, "news", serviceapi.ErrSiteUnknown},
		{f.key, "Blog", serviceapi.ErrTargetInvalid},
		{"srv-000000000000", "blog", serviceapi.ErrTargetUnknown},
	} {
		if _, err := f.m.SiteAdminPassword(context.Background(), c.key, c.site); !errors.Is(err, c.want) {
			t.Errorf("password %s/%s = %v, want %v", c.key, c.site, err, c.want)
		}
		if _, err := f.m.SiteResetLink(context.Background(), c.key, c.site); !errors.Is(err, c.want) {
			t.Errorf("reset link %s/%s = %v, want %v", c.key, c.site, err, c.want)
		}
	}
	f.finished(f.m.StopSite(context.Background(), f.key, "blog", serviceapi.OperationRequestV1{RequestID: "req-stop-blog"}))
	if _, err := f.m.SiteAdminPassword(context.Background(), f.key, "blog"); !errors.Is(err, serviceapi.ErrSiteStopped) {
		t.Fatalf("a stopped site = %v", err)
	}
	if _, err := f.m.SiteSetup(context.Background(), f.key, "blog"); !errors.Is(err, serviceapi.ErrTargetInvalid) {
		t.Fatalf("a website has no setup token = %v", err)
	}
}

// W5b: a WordPress website's backup is its database's dump and its uploads,
// its rows counted in a copy restored from the dump; a check restores both;
// a restore puts both back, swapping the tables in one step, and puts back
// what the site had when it never answers.
func TestAWordPressBackupHoldsItsDatabaseAndUploadsAndARestorePutsBothBack(t *testing.T) {
	f := newDeployFixture(t)
	first := f.putWP("one")
	f.deployWP(first, "blog", testHost, testAdmin, "req-blog-first")
	f.upload("blog", "2026/10/logo.png", "logo one")
	taken := f.backup("blog", "req-backup-1")
	if taken.Tables == nil || *taken.Tables != 3 || taken.Rows == nil || *taken.Rows != 148 || taken.UploadsBytes == nil || taken.Check != nil ||
		len(taken.Counts) != 3 || taken.Counts[0] != (serviceapi.TableRowsV1{Table: "wp_options", Rows: 135}) {
		t.Fatalf("backup = %+v", taken)
	}
	f.rows("wp_blog", `{"wp_options":140,"wp_posts":20,"wp_users":1}`)
	f.upload("blog", "2026/10/logo.png", "logo two")
	f.upload("blog", "extra.png", "extra")

	check := f.finished(f.m.CheckBackup(context.Background(), f.key, "blog", taken.BackupID, serviceapi.BackupCreateV1{RequestID: "req-check-1"}))
	if check.State != opSucceeded || check.Steps[0].Name != "check_restore" || check.Steps[0].Detail != "3 tables, 148 rows, as when it was taken" {
		t.Fatalf("check = %+v", check)
	}
	if f.held("check_wp_blog") != "absent" || f.held("wp_blog") != `{"wp_options":140,"wp_posts":20,"wp_users":1}` {
		t.Fatalf("databases: site %s, copy %s", f.held("wp_blog"), f.held("check_wp_blog"))
	}
	f.flag("restore.loses-rows", true)
	check = f.finished(f.m.CheckBackup(context.Background(), f.key, "blog", taken.BackupID, serviceapi.BackupCreateV1{RequestID: "req-check-2"}))
	if want := "the rows differ: wp_options: 135 taken, not restored; wp_posts: 12 taken, 0 restored; wp_users: 1 taken, not restored"; check.State != opFailed ||
		check.Steps[0].Detail != want || f.held("check_wp_blog") != "absent" {
		t.Fatalf("check = %+v", check)
	}
	f.flag("restore.loses-rows", false)

	plan, err := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Restore: &serviceapi.PlanRestoreV1{Site: "blog", Backup: taken.BackupID}})
	if err != nil || plan.Kind != planRestore || strings.Join(plan.Steps, ",") != "back_up,restore_copy,stop_app,switch_database,restore_files,start_app,wait_health,check,clean_up" ||
		plan.Database != "wp_blog" || strings.Join(plan.Images, " ") != WordPressServerImage+" "+WordPressMariaDBImage || plan.Backup.BackupID != taken.BackupID {
		t.Fatalf("restore plan = %+v %v", plan, err)
	}
	op := f.finished(f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: plan.PlanID, PlanHash: plan.Hash, RequestID: "req-restore-1"}))
	if op.State != opSucceeded || op.Kind != "restore" {
		t.Fatalf("restore = %+v", op)
	}
	if f.held("wp_blog") != `{"wp_options":135,"wp_posts":12,"wp_users":1}` || f.media("blog") != "2026/10/logo.png=logo one" {
		t.Fatalf("restored: %s, uploads %s", f.held("wp_blog"), f.media("blog"))
	}
	if f.held("previous_wp_blog") != "absent" || f.held("restore_wp_blog") != "absent" {
		t.Fatal("the restore left a copy")
	}
	sql, _ := os.ReadFile(filepath.Join(f.remote.home, ".docker/mariadb.sql"))
	if !strings.Contains(string(sql), "SELECT IF(SUM(o = 2) > 0, CONCAT('RENAME TABLE ', GROUP_CONCAT(m ORDER BY o, t SEPARATOR ', ')), NULL) INTO @swap") {
		t.Fatalf("the tables were not swapped in one RENAME TABLE:\n%s", sql)
	}
	list, _ := f.m.Backups(context.Background(), f.key, "blog")
	if len(list.Backups) != 2 || list.Backups[1].Reason != reasonBeforeRestore || list.Backups[0].Check == nil || !list.Backups[0].Check.OK {
		t.Fatalf("backups = %+v", list)
	}

	// A restored site that never answers gets its tables and uploads back as they were.
	f.rows("wp_blog", `{"wp_options":150,"wp_posts":30,"wp_users":2}`)
	f.upload("blog", "2026/10/logo.png", "logo three")
	plan, err = f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Restore: &serviceapi.PlanRestoreV1{Site: "blog", Backup: taken.BackupID}})
	if err != nil {
		t.Fatal(err)
	}
	f.flag("app.fails-once", true)
	op = f.finished(f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: plan.PlanID, PlanHash: plan.Hash, RequestID: "req-restore-2"}))
	if health := stepOf(op, "wait_health", ""); op.State != opFailed ||
		health.Detail != "the app did not answer /health; the database and uploads are put back as they were and the site started again" {
		t.Fatalf("restore = %+v", op)
	}
	if f.held("wp_blog") != `{"wp_options":150,"wp_posts":30,"wp_users":2}` || f.media("blog") != "2026/10/logo.png=logo three" ||
		f.held("previous_wp_blog") != "absent" || f.held("restore_wp_blog") != "absent" || !f.state("c-autobuild-app-blog") || f.state("c-autobuild-app-blog.unhealthy") {
		t.Fatalf("after: %s, uploads %s", f.held("wp_blog"), f.media("blog"))
	}
}

// W5b: every night each WordPress website is backed up and checked as a Node
// app is, each backup's two files are copied off the server, and a backup
// downloads as one tar of both.
func TestWordPressBackupsRunNightlyAreCopiedOffTheServerAndDownloadAsOneTar(t *testing.T) {
	f := newDeployFixture(t)
	f.deployWP(f.putWP("one"), "blog", testHost, testAdmin, "req-blog-first")
	f.upload("blog", "logo.png", "logo")
	f.m.NightlyPass(context.Background(), nil)
	night, ok := f.m.operationByRequest(f.key, "nightly-"+f.now.In(india).Format("2006-01-02"))
	if !ok || night.Operation.State != opSucceeded || stepOf(night.Operation, "back_up", "blog").State != "done" ||
		stepOf(night.Operation, "check_restore", "blog").Detail != "3 tables, 148 rows, as when it was taken" ||
		stepOf(night.Operation, "read_server", "").Detail != "as recorded" {
		t.Fatalf("night = %+v", night.Operation)
	}
	list, _ := f.m.Backups(context.Background(), f.key, "blog")
	b := list.Backups[0]
	if b.Reason != reasonNightly || b.Check == nil || !b.Check.OK {
		t.Fatalf("nightly backup = %+v", b)
	}

	// Downloaded: one tar of the dump and the uploads, each as it is on the server.
	dir := filepath.Join(f.remote.home, "autobuild/backups/blog")
	dump, _ := os.ReadFile(filepath.Join(dir, b.BackupID+".dump"))
	uploads, _ := os.ReadFile(filepath.Join(dir, b.BackupID+".uploads.tar"))
	body, size, err := f.m.OpenBackup(context.Background(), f.key, "blog", b.BackupID)
	if err != nil || size != websiteBackupSize(int64(len(dump)), int64(len(uploads))) {
		t.Fatalf("open = %d %v", size, err)
	}
	got, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || int64(len(got)) != size {
		t.Fatalf("read %d bytes %v, announced %d", len(got), err, size)
	}
	tr := tar.NewReader(bytes.NewReader(got))
	for _, want := range []struct {
		name string
		body []byte
	}{{websiteDumpName, dump}, {websiteUploadsName, uploads}} {
		h, err := tr.Next()
		if err != nil || h.Name != want.name || h.Mode != 0o600 {
			t.Fatalf("entry %+v %v", h, err)
		}
		if read, _ := io.ReadAll(tr); !bytes.Equal(read, want.body) {
			t.Fatalf("%s differs", want.name)
		}
	}
	if _, err := tr.Next(); err != io.EOF {
		t.Fatalf("more in the tar: %v", err)
	}
	if _, err := f.m.OpenSpreadsheets(context.Background(), f.key, "blog", b.BackupID); !errors.Is(err, serviceapi.ErrTargetInvalid) {
		t.Fatalf("spreadsheets of a website = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, b.BackupID+".uploads.tar"), append(uploads, 'x'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.m.OpenBackup(context.Background(), f.key, "blog", b.BackupID); !errors.Is(err, serviceapi.ErrBackupUnknown) {
		t.Fatalf("an uploads file of another size = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, b.BackupID+".uploads.tar"), uploads, 0o600); err != nil {
		t.Fatal(err)
	}

	// Off the server: both files, with the server's own role.
	aws := f.offsite(t)
	taken := f.backup("blog", "req-backup-1")
	object := "/" + f.key + "/blog/" + taken.BackupID
	for _, ext := range []string{".dump", ".uploads.tar"} {
		local, _ := os.ReadFile(filepath.Join(dir, taken.BackupID+ext))
		if len(local) == 0 || !bytes.Equal(aws.objects[object+ext], local) {
			t.Fatalf("%s copied %d bytes, has %d", ext, len(aws.objects[object+ext]), len(local))
		}
	}
	if got := f.backupOf("blog", taken.BackupID).Offsite; got == nil || !got.OK ||
		got.Detail != "s3://autobuild-bf14-backups-123456789012"+object+".dump, s3://autobuild-bf14-backups-123456789012"+object+".uploads.tar" {
		t.Fatalf("offsite = %+v", got)
	}
}

// W5b: a server bought for a person copies both of a website's files to
// addresses that may put each one.
func TestAWordPressBackupIsUploadedOffABoughtServer(t *testing.T) {
	f := newDeployFixture(t)
	f.deployWP(f.putWP("one"), "blog", testHost, testAdmin, "req-blog-first")
	aws, _ := f.uploads(t)
	taken := f.backup("blog", "req-backup-1")
	object := "/hosted/servers/" + f.key + "/blog/" + taken.BackupID
	for _, ext := range []string{".dump", ".uploads.tar"} {
		local, _ := os.ReadFile(filepath.Join(f.remote.home, "autobuild/backups/blog", taken.BackupID+ext))
		if len(local) == 0 || !bytes.Equal(aws.objects[object+ext], local) {
			t.Fatalf("%s uploaded %d bytes, has %d", ext, len(aws.objects[object+ext]), len(local))
		}
	}
	if got := f.backupOf("blog", taken.BackupID).Offsite; got == nil || !got.OK || !strings.HasSuffix(got.Detail, object+".uploads.tar") {
		t.Fatalf("offsite = %+v", got)
	}
}

// W5b: a WordPress website is stopped and started as a Node app is; taken
// off, its database, uploads and backups stay; its data removed, its
// database, login, env files, volume and backups go; the MariaDB started
// again joins the websites' networks alone; a drift check knows it all; and
// cleaning the target removes the MariaDB, its volume and the images.
func TestAWordPressWebsiteIsStoppedStartedTakenOffAndItsDataRemoved(t *testing.T) {
	f := newDeployFixture(t)
	release := f.putWP("one")
	f.deployWP(release, "blog", testHost, testAdmin, "req-blog-first")
	f.deploy(f.putNode("one"), "crm", "crm.13-232-1-2.sslip.io", "req-crm-first")
	f.upload("blog", "logo.png", "logo")

	stop := f.finished(f.m.StopSite(context.Background(), f.key, "blog", serviceapi.OperationRequestV1{RequestID: "req-stop-blog"}))
	caddy, _ := os.ReadFile(filepath.Join(f.remote.home, "autobuild/frontdoor/config/Caddyfile"))
	if stop.State != opSucceeded || f.state("c-autobuild-app-blog") || !strings.Contains(string(caddy), testHost+" {\n\trespond \"This website is paused.\" 503\n") ||
		f.media("blog") != "logo.png=logo" || f.held("wp_blog") == "absent" {
		t.Fatalf("stop = %+v\n%s", stop, caddy)
	}
	start := f.finished(f.m.StartSite(context.Background(), f.key, "blog", serviceapi.OperationRequestV1{RequestID: "req-start-blog"}))
	caddy, _ = os.ReadFile(filepath.Join(f.remote.home, "autobuild/frontdoor/config/Caddyfile"))
	if start.State != opSucceeded || !f.state("c-autobuild-app-blog") || !strings.Contains(string(caddy), testHost+" {\n\treverse_proxy autobuild-app-blog:8080\n") {
		t.Fatalf("start = %+v\n%s", start, caddy)
	}

	// The MariaDB, started again, joins the websites' networks and no Node app's.
	if err := os.Remove(filepath.Join(f.remote.home, ".docker/c-autobuild-mariadb")); err != nil {
		t.Fatal(err)
	}
	before := len(f.remote.docker())
	_, op := f.deployWP(release, "blog", testHost, testAdmin, "req-blog-again")
	docker := f.remote.docker()[before:]
	if op.State != opSucceeded || stepOf(op, "start_database", "").Detail != "started\n" ||
		!strings.Contains(docker, "network connect --alias autobuild-mariadb autobuild-net-blog autobuild-mariadb") || strings.Contains(docker, "autobuild-net-crm autobuild-mariadb") {
		t.Fatalf("again = %+v\n%s", op, docker)
	}
	drift := f.finished(f.m.CheckDrift(context.Background(), f.key, serviceapi.OperationRequestV1{RequestID: "req-drift-01"}))
	if report, _ := f.m.Drift(context.Background(), f.key); drift.State != opSucceeded || !report.OK {
		t.Fatalf("drift = %+v", report)
	}
	f.backup("blog", "req-backup-1")

	remove := f.finished(f.m.RemoveSite(context.Background(), f.key, "blog", serviceapi.SiteRemoveV1{RequestID: "req-remove-blog"}))
	names := []string{}
	for _, s := range remove.Steps {
		names = append(names, s.Name)
	}
	docker = f.remote.docker()
	if remove.State != opSucceeded || strings.Join(names, ",") != "stop_app,unlink_site,write_site,reload_frontdoor,remove_files" ||
		!strings.Contains(docker, "network disconnect -f autobuild-net-blog autobuild-mariadb") || !strings.Contains(docker, "network rm autobuild-net-blog") {
		t.Fatalf("remove = %+v", remove)
	}
	if !f.state("vol-autobuild-wp-blog") || f.held("wp_blog") == "absent" || !f.state("c-autobuild-mariadb") || f.media("blog") != "logo.png=logo" {
		t.Fatal("taking a website off touched its data")
	}
	if backups, err := f.m.Backups(context.Background(), f.key, "blog"); err != nil || len(backups.Backups) != 1 {
		t.Fatalf("backups after removing = %+v %v", backups, err)
	}
	// Its data is a website's: a Node app may not take its name until it goes.
	if _, err := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Release: f.putNode("two"), Site: serviceapi.PlanSiteV1{Name: "blog", Host: testHost}}); !errors.Is(err, serviceapi.ErrTargetInvalid) {
		t.Fatalf("a Node app on a website's data = %v", err)
	}
	if _, err := f.m.RemoveData(context.Background(), f.key, "blog", serviceapi.ConfirmedRequestV1{RequestID: "req-data-blog", Confirm: "app_blog"}); !errors.Is(err, serviceapi.ErrTargetInvalid) {
		t.Fatalf("data removed under another name = %v", err)
	}
	data := f.finished(f.m.RemoveData(context.Background(), f.key, "blog", serviceapi.ConfirmedRequestV1{RequestID: "req-data-blog", Confirm: "wp_blog"}))
	names = []string{}
	for _, s := range data.Steps {
		names = append(names, s.Name)
	}
	if data.State != opSucceeded || strings.Join(names, ",") != "start_database,drop_database,remove_volume,remove_backups" ||
		stepOf(data, "remove_volume", "").Detail != "removed\n" || stepOf(data, "remove_backups", "").Detail != "1 backups removed\n" {
		t.Fatalf("remove data = %+v", data)
	}
	if f.held("wp_blog") != "absent" || f.state("role-wp_blog") || f.state("vol-autobuild-wp-blog") {
		t.Fatal("the database, its login or its volume stays")
	}
	if _, err := os.Stat(filepath.Join(f.remote.home, "autobuild/apps/blog")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the env files stay")
	}
	if databases, _ := f.m.Databases(context.Background(), f.key); len(databases.Databases) != 1 || databases.Databases[0].Database != "app_crm" {
		t.Fatalf("databases = %+v", databases)
	}
	// Put back, it is a new website with a new administrator.
	_, op = f.deployWP(release, "blog", testHost, "new.owner@example.com", "req-blog-back")
	if op.State != opSucceeded || stepOf(op, "make_database", "").Detail != "made\n" {
		t.Fatalf("back = %+v", op)
	}
	if got, err := f.m.SiteAdminPassword(context.Background(), f.key, "blog"); err != nil || got.Password == "" || got.Email != "new.owner@example.com" {
		t.Fatalf("a new administrator = %+v %v", got, err)
	}
	// With nothing left, the target is cleaned of the MariaDB too.
	for _, site := range []string{"blog", "crm"} {
		f.finished(f.m.RemoveSite(context.Background(), f.key, site, serviceapi.SiteRemoveV1{RequestID: "req-off-" + site}))
		db := "wp_blog"
		if site == "crm" {
			db = "app_crm"
		}
		f.finished(f.m.RemoveData(context.Background(), f.key, site, serviceapi.ConfirmedRequestV1{RequestID: "req-gone-" + site, Confirm: db}))
	}
	clean := f.finished(f.m.CleanTarget(context.Background(), f.key, serviceapi.ConfirmedRequestV1{RequestID: "req-clean-01", Confirm: f.key}))
	docker = f.remote.docker()
	for _, want := range []string{"rm -f autobuild-mariadb", "volume rm autobuild-mariadb-data", "image rm " + WordPressServerImage, "image rm " + WordPressMariaDBImage} {
		if !strings.Contains(docker, want) {
			t.Fatalf("clean %+v: docker did not get %q", clean, want)
		}
	}
	if clean.State != opSucceeded || f.state("c-autobuild-mariadb") || f.state("vol-autobuild-mariadb-data") {
		t.Fatalf("clean = %+v", clean)
	}
}

// W5b: a drift check finds a website's volume gone, its copy left and its
// database missing, and reconciles a website left unknown only while its
// container runs.
func TestADriftCheckKnowsAWordPressWebsitesVolumeDatabaseAndCopies(t *testing.T) {
	f := newDeployFixture(t)
	f.deployWP(f.putWP("one"), "blog", testHost, testAdmin, "req-blog-first")
	if err := os.Remove(filepath.Join(f.remote.home, ".docker/vol-autobuild-wp-blog")); err != nil {
		t.Fatal(err)
	}
	f.rows("check_wp_blog", "{}")
	f.finished(f.m.CheckDrift(context.Background(), f.key, serviceapi.OperationRequestV1{RequestID: "req-drift-01"}))
	report, _ := f.m.Drift(context.Background(), f.key)
	var found []string
	for _, finding := range report.Findings {
		found = append(found, finding.Kind+":"+finding.Site+":"+finding.Detail)
	}
	if strings.Join(found, ",") != "volume_missing:blog:autobuild-wp-blog,copy_left::check_wp_blog" {
		t.Fatalf("findings = %v", found)
	}
	r, _ := f.m.read(f.key)
	s, _ := findSite(r, "blog")
	s.State = siteUnknown
	f.m.setSite(&r, s)
	_ = f.m.write(r)
	_ = os.Remove(filepath.Join(f.remote.home, ".docker/c-autobuild-app-blog"))
	f.finished(f.m.CheckDrift(context.Background(), f.key, serviceapi.OperationRequestV1{RequestID: "req-drift-02"}))
	if report, _ = f.m.Drift(context.Background(), f.key); len(report.Reconciled) != 0 {
		t.Fatalf("a website without its container reconciled: %+v", report)
	}
	if err := os.WriteFile(filepath.Join(f.remote.home, ".docker/c-autobuild-app-blog"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f.finished(f.m.CheckDrift(context.Background(), f.key, serviceapi.OperationRequestV1{RequestID: "req-drift-03"}))
	if report, _ = f.m.Drift(context.Background(), f.key); strings.Join(report.Reconciled, ",") != "blog" {
		t.Fatalf("a running website left unknown is not reconciled: %+v", report)
	}
}

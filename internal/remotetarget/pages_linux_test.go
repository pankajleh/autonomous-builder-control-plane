//go:build linux

package remotetarget

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// pageFiles is a page set as Repo C sends it: each name and its JSON.
func pageFiles(files ...string) []serviceapi.PageFileV1 {
	var out []serviceapi.PageFileV1
	for i := 0; i+1 < len(files); i += 2 {
		out = append(out, serviceapi.PageFileV1{Name: files[i], ContentBase64: base64.StdEncoding.EncodeToString([]byte(files[i+1]))})
	}
	return out
}

const (
	privacyPage = `{"format":"autobuild-page-set/1","slug":"privacy-policy","content":"PRIVACY-WORDS"}`
	termsPage   = `{"format":"autobuild-page-set/1","slug":"terms","content":"TERMS-WORDS"}`
	contactPage = `{"format":"autobuild-page-set/1","slug":"contact-us","content":"CONTACT-WORDS"}`
)

func (f *deployFixture) setPages(site, request string, files []serviceapi.PageFileV1) serviceapi.OperationV1 {
	f.t.Helper()
	return f.finished(f.m.SetSitePages(context.Background(), f.key, site, serviceapi.SitePagesRequestV1{RequestID: request, Files: files}))
}

func (f *deployFixture) dockerState(name string) string {
	return filepath.Join(f.remote.home, ".docker", name)
}

func pageOf(op serviceapi.OperationV1, file string) serviceapi.SitePageV1 {
	for _, p := range op.Pages {
		if p.File == file {
			return p
		}
	}
	return serviceapi.SitePageV1{}
}

// SP1b (Repo C STANDARD_PAGES.md §14): a WordPress website's page files go on with a backup first, each set with the
// kit's command in the site's container; a page edited in WordPress is kept and a file the kit refuses is refused, and
// neither fails the operation. The files never reach a script, a record or a step, and leave the container after.
func TestAWordPressWebsitesPagesGoOnAfterABackupAndEachSaysWhatItCameTo(t *testing.T) {
	f := newDeployFixture(t)
	f.deployWP(f.putWP("one"), "blog", testHost, testAdmin, "req-blog-first")
	before, _ := f.m.Backups(context.Background(), f.key, "blog")
	if err := os.WriteFile(f.dockerState("page-contact-us.edited"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.dockerState("page-terms.refused"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	files := pageFiles("contact-us.json", contactPage, "privacy-policy.json", privacyPage, "terms.json", termsPage)
	logged := len(f.remote.docker())
	op := f.setPages("blog", "req-pages-0001", files)
	if op.State != opSucceeded || op.Kind != "pages" || op.Site != "blog" {
		t.Fatalf("operation = %+v", op)
	}
	var names []string
	for _, s := range op.Steps {
		names = append(names, s.Name+":"+s.State)
	}
	if strings.Join(names, ",") != "check_app:done,back_up:done,send_pages:done,set_pages:done,clean_up:done" ||
		stepOf(op, "set_pages", "").Detail != "1 set, 1 kept, 1 refused" || stepOf(op, "check_app", "").Detail != "kit 1.2.0" {
		t.Fatalf("steps = %+v", op.Steps)
	}
	if p := pageOf(op, "privacy-policy.json"); p.Outcome != "created" || p.Footer != "added" || p.Why != "" || p.Detail != "" {
		t.Fatalf("privacy = %+v", p)
	}
	if p := pageOf(op, "contact-us.json"); p.Outcome != "kept" || p.Footer != "none" || p.Why != "edited in WordPress since it was last imported or set" {
		t.Fatalf("contact = %+v", p)
	}
	if p := pageOf(op, "terms.json"); p.Outcome != "refused" || p.Footer != "" || p.Detail != "the content holds a block or markup a standard page never has" {
		t.Fatalf("terms = %+v", p)
	}
	// Backed up first, as before an update.
	after, _ := f.m.Backups(context.Background(), f.key, "blog")
	if len(after.Backups) != len(before.Backups)+1 || after.Backups[len(after.Backups)-1].Reason != reasonBeforeUpdate {
		t.Fatalf("backups = %+v", after.Backups)
	}
	docker := f.remote.docker()[logged:]
	for _, want := range []string{
		"exec autobuild-app-blog php /usr/local/bin/wp --path=/opt/wordpress plugin get autobuild-site --field=version",
		"exec autobuild-app-blog /bin/busybox mkdir -m 700 /scratch/autobuild-pages",
		"exec -i autobuild-app-blog /bin/busybox tee /scratch/autobuild-pages/privacy-policy.json",
		"exec autobuild-app-blog php /usr/local/bin/wp --path=/opt/wordpress autobuild-site page set privacy-policy /scratch/autobuild-pages/privacy-policy.json --format=json",
		"exec autobuild-app-blog php /usr/local/bin/wp --path=/opt/wordpress autobuild-site page set contact-us /scratch/autobuild-pages/contact-us.json --format=json",
	} {
		if !strings.Contains(docker, want) {
			t.Fatalf("missing %q in:\n%s", want, docker)
		}
	}
	// The kit's backup ran before any file went in.
	if strings.Index(docker, "autobuild-mariadb dump wp_blog") > strings.Index(docker, "tee /scratch/autobuild-pages/") {
		t.Fatalf("files went in before the backup:\n%s", docker)
	}
	if kept, _ := os.ReadFile(f.dockerState("page-autobuild-app-blog-privacy-policy")); string(kept) != privacyPage {
		t.Fatalf("the page set took %q", kept)
	}
	if _, err := os.Stat(f.dockerState("pages-autobuild-app-blog")); !os.IsNotExist(err) {
		t.Fatal("the files stayed in the container", err)
	}
	for _, words := range []string{"PRIVACY-WORDS", "CONTACT-WORDS", "TERMS-WORDS"} {
		if strings.Contains(strings.Join(f.remote.scripts, "\n"), words) || strings.Contains(docker, words) {
			t.Fatalf("%s reached a script or an argument", words)
		}
		record, _ := os.ReadFile(filepath.Join(f.m.dir, "operations", op.OperationID+".json"))
		if strings.Contains(string(record), words) || !strings.Contains(string(record), serviceapi.PageFilesDigest(mustDecode(t, files))) {
			t.Fatalf("the operation's record: %s", record)
		}
	}
	if len(f.m.pageFiles) != 0 {
		t.Fatal("the files stayed in memory")
	}
	// The same request answers the same operation; the same request with other files is refused.
	if again, err := f.m.SetSitePages(context.Background(), f.key, "blog", serviceapi.SitePagesRequestV1{RequestID: "req-pages-0001", Files: files}); err != nil ||
		again.OperationID != op.OperationID {
		t.Fatalf("again = %+v %v", again, err)
	}
	if _, err := f.m.SetSitePages(context.Background(), f.key, "blog", serviceapi.SitePagesRequestV1{RequestID: "req-pages-0001", Files: files[:1]}); !errors.Is(err, serviceapi.ErrTargetInvalid) {
		t.Fatalf("other files = %v", err)
	}
	// Set again as they are, the pages are unchanged and their footer links already there.
	_ = os.Remove(f.dockerState("page-terms.refused"))
	op = f.setPages("blog", "req-pages-0002", files)
	if p := pageOf(op, "privacy-policy.json"); op.State != opSucceeded || p.Outcome != "unchanged" || p.Footer != "there" {
		t.Fatalf("again = %+v", op)
	}
	if p := pageOf(op, "terms.json"); p.Outcome != "created" {
		t.Fatalf("terms = %+v", p)
	}
}

func mustDecode(t *testing.T, files []serviceapi.PageFileV1) []serviceapi.PageFile {
	t.Helper()
	out, err := serviceapi.DecodePageFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A site whose server image carries a kit older than page set is left as it is: the operation fails at check_app,
// before any backup, saying the live site needs updating first.
func TestPagesWaitForASiteWhoseKitIsTooOld(t *testing.T) {
	f := newDeployFixture(t)
	f.deployWP(f.putWP("one"), "blog", testHost, testAdmin, "req-blog-first")
	before, _ := f.m.Backups(context.Background(), f.key, "blog")
	for _, version := range []string{"1.1.9", "garbage\n<b>"} {
		if err := os.WriteFile(f.dockerState("c-autobuild-app-blog.kit"), []byte(version), 0o644); err != nil {
			t.Fatal(err)
		}
		logged := len(f.remote.docker())
		op := f.setPages("blog", "req-pages-old-"+strings.Repeat("x", len(version)), pageFiles("terms.json", termsPage))
		want := "the site's kit is 1.1.9, older than 1.2.0: update the live site first"
		if version != "1.1.9" {
			want = "the site's kit is unknown, older than 1.2.0: update the live site first"
		}
		if check := stepOf(op, "check_app", ""); op.State != opFailed || check.State != "failed" || check.Detail != want || stepOf(op, "back_up", "").State != "pending" || len(op.Pages) != 0 {
			t.Fatalf("operation = %+v", op)
		}
		if strings.Contains(f.remote.docker()[logged:], "page set") || strings.Contains(f.remote.docker()[logged:], "dump wp_blog") {
			t.Fatal("an old site was touched")
		}
	}
	if after, _ := f.m.Backups(context.Background(), f.key, "blog"); len(after.Backups) != len(before.Backups) {
		t.Fatal("an old site was backed up")
	}
	if len(f.m.pageFiles) != 0 {
		t.Fatal("the files stayed in memory")
	}
}

// Only a WordPress website ABCP serves, running, takes page files, and only a valid request.
func TestPagesAreRefusedForAnythingButARunningWordPressWebsite(t *testing.T) {
	f := newDeployFixture(t)
	f.deployWP(f.putWP("one"), "blog", testHost, testAdmin, "req-blog-first")
	f.deploy(f.putNode("one"), "shop", "shop.example.com", "req-shop-first")
	files := pageFiles("terms.json", termsPage)
	ask := func(site, request string, files []serviceapi.PageFileV1) error {
		_, err := f.m.SetSitePages(context.Background(), f.key, site, serviceapi.SitePagesRequestV1{RequestID: request, Files: files})
		return err
	}
	for name, c := range map[string]struct {
		site, request string
		files         []serviceapi.PageFileV1
		want          error
	}{
		"a Node app":       {"shop", "req-pages-node", files, serviceapi.ErrTargetInvalid},
		"no such site":     {"nowhere", "req-pages-none", files, serviceapi.ErrSiteUnknown},
		"a bad site":       {"Blog", "req-pages-bad1", files, serviceapi.ErrTargetInvalid},
		"a bad request id": {"blog", "short", files, serviceapi.ErrTargetInvalid},
		"no files":         {"blog", "req-pages-none", nil, serviceapi.ErrTargetInvalid},
		"a bad file":       {"blog", "req-pages-file", pageFiles("Terms.json", termsPage), serviceapi.ErrTargetInvalid},
	} {
		if err := ask(c.site, c.request, c.files); !errors.Is(err, c.want) {
			t.Errorf("%s = %v", name, err)
		}
	}
	f.finished(f.m.StopSite(context.Background(), f.key, "blog", serviceapi.OperationRequestV1{RequestID: "req-stop-blog"}))
	if err := ask("blog", "req-pages-stop", files); !errors.Is(err, serviceapi.ErrSiteStopped) {
		t.Fatalf("a stopped site = %v", err)
	}
	f.finished(f.m.StartSite(context.Background(), f.key, "blog", serviceapi.OperationRequestV1{RequestID: "req-start-blog"}))
	f.remote.hold = make(chan struct{})
	busy, err := f.m.SetSitePages(context.Background(), f.key, "blog", serviceapi.SitePagesRequestV1{RequestID: "req-pages-busy", Files: files})
	if err != nil {
		t.Fatal(err)
	}
	if err := ask("blog", "req-pages-more", files); !errors.Is(err, serviceapi.ErrTargetBusy) {
		t.Fatalf("while another runs = %v", err)
	}
	close(f.remote.hold)
	op := f.finished(busy, nil)
	f.remote.hold = nil
	if op.State != opSucceeded || pageOf(op, "terms.json").Outcome != "created" {
		t.Fatalf("operation = %+v", op)
	}
}

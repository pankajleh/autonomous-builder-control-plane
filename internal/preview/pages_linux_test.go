//go:build linux

package preview

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// Page files (Repo C STANDARD_PAGES.md §14): a WordPress website's standard pages, read-only at /pages in its preview's
// pages service from its start.

// pagesProfile is a WordPress website's preview, as Repo C's wp-v1 is: the site's own server with the source and the
// pages, and its database beside it.
func pagesProfile() PreviewProfileV1 {
	p := testProfile()
	p.ProfileID = "wp-v1"
	p.Services[0].Name, p.Services[0].MountPages = "app", true
	db := testProfile().Services[0]
	db.Name, db.Presented, db.MountSource = "db", false, false
	p.Services = append(p.Services, db)
	return p
}

func pageFilesOf(t *testing.T, files ...string) ([]serviceapi.PageFileV1, []serviceapi.PageFile) {
	t.Helper()
	var wire []serviceapi.PageFileV1
	for i := 0; i+1 < len(files); i += 2 {
		wire = append(wire, serviceapi.PageFileV1{Name: files[i], ContentBase64: base64.StdEncoding.EncodeToString([]byte(files[i+1]))})
	}
	decoded, err := serviceapi.DecodePageFiles(wire)
	if err != nil {
		t.Fatal(err)
	}
	return wire, decoded
}

// StartPages, as the docker runtime has it: what it was started with, and the files there at that moment.
func (r *memoryRuntime) StartPages(_ context.Context, id, path string, p PreviewProfileV1, keyDigest, pages string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = append(r.started, id)
	entries, _ := os.ReadDir(pages)
	seen := []string{}
	for _, e := range entries {
		data, _ := os.ReadFile(filepath.Join(pages, e.Name()))
		seen = append(seen, e.Name()+"="+string(data))
	}
	r.pagesSeen = append(r.pagesSeen, pagesStart{id: id, pages: pages, key: keyDigest, files: seen})
	return strings.Repeat("d", 64), r.startErr
}

type pagesStart struct {
	id, pages, key string
	files          []string
}

func TestPageFilesAreAPreviewsOwnReadableFolderRemovedWithIt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pages")
	f := PageFiles{Root: root}
	id := strings.Repeat("a", 64)
	_, files := pageFilesOf(t, "privacy-policy.json", `{"a":1}`, "terms.json", `{"b":2}`)
	path, err := f.Write(id, files)
	if err != nil || path != filepath.Join(root, id) {
		t.Fatal(path, err)
	}
	if info, _ := os.Stat(root); info.Mode().Perm() != 0700 {
		t.Fatal("the root is not private", info.Mode())
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0755 {
		t.Fatal("the folder is not readable for the service's user", info.Mode())
	}
	for _, name := range []string{"privacy-policy.json", "terms.json"} {
		info, err := os.Lstat(filepath.Join(path, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0644 {
			t.Fatal(name, info, err)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(path, "terms.json")); string(data) != `{"b":2}` {
		t.Fatal("bytes changed", string(data))
	}
	if _, err := f.Write(id, files); err == nil {
		t.Fatal("a preview's folder was written twice")
	}
	for name, bad := range map[string]struct {
		id    string
		files []serviceapi.PageFile
	}{
		"a bad id":    {"../" + id, files},
		"no files":    {strings.Repeat("b", 64), nil},
		"a path":      {strings.Repeat("c", 64), []serviceapi.PageFile{{Name: "../x.json", Content: []byte("{}")}}},
		"hidden":      {strings.Repeat("d", 64), []serviceapi.PageFile{{Name: ".x.json", Content: []byte("{}")}}},
		"a same name": {strings.Repeat("e", 64), []serviceapi.PageFile{{Name: "x.json", Content: []byte("{}")}, {Name: "x.json", Content: []byte("{}")}}},
	} {
		if _, err := f.Write(bad.id, bad.files); err == nil {
			t.Errorf("%s accepted", name)
		}
		if _, err := os.Lstat(filepath.Join(root, bad.id)); bad.id != "../"+id && !os.IsNotExist(err) {
			t.Errorf("%s left a folder", name)
		}
	}
	if err := f.Remove(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("not removed")
	}
	if _, err := f.Write(id, files); err != nil {
		t.Fatal(err)
	}
	if err := f.Reconcile(); err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatal("a restart left page files", entries)
	}
}

func TestAPagesServiceIsOneServiceOfAPreviewProfileAndKeepsOldDigests(t *testing.T) {
	plain := testProfile()
	raw, _ := json.Marshal(plain)
	if strings.Contains(string(raw), "mount_pages") {
		t.Fatal("a profile without pages changed its JSON and so its digest")
	}
	p := pagesProfile()
	if p.Validate() != nil || p.pagesService() != 0 {
		t.Fatal("a WordPress website's profile is refused")
	}
	two := pagesProfile()
	two.Services[1].MountPages = true
	if two.Validate() == nil {
		t.Fatal("two pages services accepted")
	}
	hosted := pagesProfile()
	hosted.Hosted = true
	if hosted.Validate() == nil {
		t.Fatal("a hosted profile took pages")
	}
	without := cloneProfile(p)
	without.Services[0].MountPages = false
	if without.Digest() == p.Digest() {
		t.Fatal("the pages service does not change the profile's digest")
	}
}

// The mount is the source's, at /pages, in the pages service alone: recursively read-only under Docker's default
// runtime and the plain read-only bind under gVisor.
func TestPagesMountIsReadOnlyInThePagesServiceAloneUnderBothRuntimes(t *testing.T) {
	d := newDocker("/private/previews")
	id := strings.Repeat("c", 64)
	source, pages := filepath.Join(d.root, "sources", id), filepath.Join(d.root, "pages", id)
	mounts := func(args []string) []string {
		var out []string
		for i, a := range args {
			if a == "--mount" {
				out = append(out, args[i+1])
			}
		}
		return out
	}
	for _, gvisor := range []bool{false, true} {
		p := pagesProfile()
		want := "type=bind,src=" + pages + ",dst=/pages,readonly,bind-recursive=readonly,bind-propagation=rprivate"
		if gvisor {
			p.Runtime = RuntimeGVisor
			want = "type=bind,src=" + pages + ",dst=/pages,readonly,bind-propagation=rprivate"
		}
		app := mounts(d.createArgsWith(id, source, p, p.Services[0], 0, "", pages))
		if len(app) != 2 || app[0] != sourceMount(p, source) || app[1] != want {
			t.Fatalf("gvisor=%t app mounts = %v", gvisor, app)
		}
		if db := mounts(d.createArgsWith(id, source, p, p.Services[1], 1, "", pages)); len(db) != 0 {
			t.Fatalf("the database got %v", db)
		}
		if none := mounts(d.createArgsWith(id, source, p, p.Services[0], 0, "", "")); len(none) != 1 || none[0] != sourceMount(p, source) {
			t.Fatalf("a preview without page files mounted %v", none)
		}
		if old := mounts(d.createArgs(id, source, p, p.Services[0], 0, "")); len(old) != 1 {
			t.Fatalf("createArgs mounted %v", old)
		}
	}
}

// Inspection still refuses anything unexpected, and accepts exactly the read-only /pages bind of the preview's own
// folder, in its pages service, when it was started with page files.
func TestInspectionAcceptsExactlyThePreviewsReadOnlyPagesMount(t *testing.T) {
	id := strings.Repeat("c", 64)
	inspect := func(d *dockerRuntime, p PreviewProfileV1, i int, mutate func([]map[string]any) []map[string]any, pages string) error {
		v := dockerInspection(d, id, p, p.Services[i])
		mounts := []map[string]any{}
		if p.Services[i].MountSource {
			mounts = v["Mounts"].([]map[string]any)
		}
		v["Mounts"] = mutate(mounts)
		raw, _ := json.Marshal([]any{v})
		d.run = func(context.Context, ...string) (string, error) { return string(raw), nil }
		_, err := d.endpointWith(context.Background(), id, p, p.Services[i], i, "", pages)
		return err
	}
	d := newDocker("/private/previews")
	p := pagesProfile()
	path := filepath.Join(d.root, "pages", id)
	bind := func(over map[string]any) func([]map[string]any) []map[string]any {
		return func(m []map[string]any) []map[string]any {
			b := map[string]any{"Type": "bind", "Destination": "/pages", "Source": path, "RW": false}
			for k, v := range over {
				b[k] = v
			}
			return append(m, b)
		}
	}
	same := func(m []map[string]any) []map[string]any { return m }
	if err := inspect(d, p, 0, bind(nil), path); err != nil {
		t.Fatal("the pages bind was refused", err)
	}
	if err := inspect(d, p, 0, same, ""); err != nil {
		t.Fatal("a preview without pages was refused", err)
	}
	for name, c := range map[string]struct {
		service int
		mutate  func([]map[string]any) []map[string]any
		pages   string
	}{
		"writable":                 {0, bind(map[string]any{"RW": true}), path},
		"another folder":           {0, bind(map[string]any{"Source": "/home/devagent/autobuild-vendor/standard-pages"}), path},
		"another preview's folder": {0, bind(map[string]any{"Source": filepath.Join(d.root, "pages", strings.Repeat("e", 64))}), path},
		"elsewhere":                {0, bind(map[string]any{"Destination": "/pages/x"}), path},
		"a volume":                 {0, bind(map[string]any{"Type": "volume"}), path},
		"twice":                    {0, func(m []map[string]any) []map[string]any { return bind(nil)(bind(nil)(m)) }, path},
		"missing":                  {0, same, path},
		"not started with pages":   {0, bind(nil), ""},
		"in the database":          {1, bind(nil), path},
		"a folder not its own":     {0, bind(map[string]any{"Source": "/tmp/pages"}), "/tmp/pages"},
	} {
		if err := inspect(d, p, c.service, c.mutate, c.pages); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// A profile without a pages service takes no /pages, whatever it is told.
	plain := testProfile()
	if err := inspect(d, plain, 0, bind(nil), path); err == nil {
		t.Fatal("a profile without pages accepted a /pages bind")
	}
}

// A preview started with page files has them read-only in its pages service and passes its isolation inspection on
// every health read, under Docker's default runtime and gVisor alike; StartPages refuses anything else.
func TestAPreviewWithPagesStartsWithThemAndPassesItsInspection(t *testing.T) {
	for _, gvisor := range []bool{false, true} {
		p := pagesProfile()
		if gvisor {
			p.Runtime = RuntimeGVisor
		}
		f := newDockerFixture(t, p)
		f.d.approved[p.Digest()] = true
		_, files := pageFilesOf(t, "terms.json", `{"t":1}`)
		pages, err := PageFiles{Root: filepath.Join(f.d.root, "pages")}.Write(f.id, files)
		if err != nil {
			t.Fatal(err)
		}
		source := filepath.Join(f.d.root, "sources", f.id)
		for name, start := range map[string]func() (string, error){
			"no pages service": func() (string, error) {
				return f.d.StartPages(context.Background(), f.id, source, testProfile(), "", pages)
			},
			"another folder": func() (string, error) { return f.d.StartPages(context.Background(), f.id, source, p, "", "/tmp/pages") },
			"no folder":      func() (string, error) { return f.d.StartPages(context.Background(), f.id, source, p, "", "") },
			"a kept key": func() (string, error) {
				return f.d.StartPages(context.Background(), f.id, source, p, strings.Repeat("a", 64), pages)
			},
		} {
			if _, err := start(); err == nil {
				t.Fatalf("gvisor=%t %s started", gvisor, name)
			}
		}
		f.commands, f.created = nil, map[string][]string{}
		route, err := f.d.StartPages(context.Background(), f.id, source, p, "", pages)
		if err != nil || !sha256Pattern.MatchString(route) {
			t.Fatal("start", route, err)
		}
		app, db := strings.Join(f.created[f.d.container(f.id, 0)], " "), strings.Join(f.created[f.d.container(f.id, 1)], " ")
		if !strings.Contains(app, "--mount "+pagesMount(p, pages)) || strings.Contains(db, "/pages") {
			t.Fatalf("gvisor=%t\napp: %s\ndb: %s", gvisor, app, db)
		}
		if ok, err := f.d.Healthy(context.Background(), f.id, p); err != nil {
			t.Fatal("a preview with pages failed its inspection", ok, err)
		}
		if err := f.d.Stop(context.Background(), f.id); err != nil {
			t.Fatal(err)
		}
	}
}

// The probe proves the /pages mount as it proves /source: a page file readable through it by the pages service's
// own user, and nothing of it left after.
func TestTheProbeProvesThePagesMountThroughThePagesService(t *testing.T) {
	p := pagesProfile()
	f := newDockerFixture(t, p)
	f.id = jsonDigest([]string{f.d.namespace, p.Digest(), "isolation-probe"})
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
	defer target.Close()
	f.external = func() { connectPresentation(t, f.d.groups[f.id], target.URL) }
	if !f.d.prove(context.Background(), p) {
		t.Fatal("a pages profile was not proved")
	}
	read := 0
	for _, args := range f.commands {
		if args[0] == "exec" && slices.Contains(args, PagesPath+"/"+probePagesName) {
			read++
			if args[1] != f.d.container(f.id, 0) {
				t.Fatal("the page file was read in", args[1])
			}
		}
	}
	if read != 1 {
		t.Fatal("the probe read its page file", read, "times")
	}
	if !strings.Contains(strings.Join(f.created[f.d.container(f.id, 0)], " "), "dst=/pages,") {
		t.Fatal("the probe did not mount /pages")
	}
	if _, err := os.Lstat(filepath.Join(f.d.root, "pages", f.id)); !os.IsNotExist(err) {
		t.Fatal("the probe left its page file", err)
	}
	// A pages folder the service's user cannot read fails the probe.
	g := newDockerFixture(t, p)
	g.id = f.id
	g.external = f.external
	g.reject = func(_ context.Context, args []string) error {
		if args[0] == "exec" && slices.Contains(args, PagesPath+"/"+probePagesName) {
			return os.Chmod(filepath.Join(g.d.root, "pages", g.id), 0700)
		}
		return nil
	}
	if g.d.prove(context.Background(), p) {
		t.Fatal("an unreadable pages mount was proved")
	}
}

// The service: page files reach the runtime in the preview's own folder at its start, the receipt binds them without
// keeping them, and they go with the preview.
func TestAPreviewCreatedWithPageFilesStartsWithThemAndTheyGoWithIt(t *testing.T) {
	rt := &memoryRuntime{available: true, healthy: true}
	root := filepath.Join(t.TempDir(), "previews")
	s, _ := serviceAt(t, root, rt, time.Now)
	p := pagesProfile()
	p.RepositoryIdentityDigest = testProfile().RepositoryIdentityDigest
	s.profiles[p.ProfileID] = p
	c := createCommand("create-pages")
	wire, files := pageFilesOf(t, "privacy-policy.json", `{"p":1}`, "terms.json", `{"t":2}`)
	c.Pages = wire
	// A profile without a pages service takes none.
	if _, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", c); !errors.Is(err, serviceapi.ErrPreviewPagesRefused) {
		t.Fatal("pages for web-v1", err)
	}
	if len(s.store.records) != 0 {
		t.Fatal("a refused create was recorded")
	}
	c.ProfileID = p.ProfileID
	v, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", c)
	if err != nil || v.PagesDigest != serviceapi.PageFilesDigest(files) {
		t.Fatal(v, err)
	}
	awaitPreview(t, s, v.PreviewID, "READY", "HEALTHY")
	rt.mu.Lock()
	seen := rt.pagesSeen
	rt.mu.Unlock()
	if len(seen) != 1 || seen[0].id != v.PreviewID || seen[0].pages != filepath.Join(root, "pages", v.PreviewID) || seen[0].key != "" ||
		strings.Join(seen[0].files, " ") != `privacy-policy.json={"p":1} terms.json={"t":2}` {
		t.Fatalf("started with %+v", seen)
	}
	if again, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", c); err != nil || again != v {
		t.Fatal("replay", again, err)
	}
	other := c
	other.Pages, _ = pageFilesOf(t, "terms.json", `{"t":3}`)
	if _, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", other); !errors.Is(err, serviceapi.ErrRequestIDConflict) {
		t.Fatal("other pages under the same request", err)
	}
	stop := serviceapi.PreviewStopRequestV1{SchemaVersion: 1, RequestID: "stop-pages", ExpectedRunID: "run-1", ExpectedPreviewID: v.PreviewID, DelegatedActor: c.DelegatedActor}
	if _, err := s.StopPreview(context.Background(), principal(), testAuthorityDigest, "run-1", v.PreviewID, stop); err != nil {
		t.Fatal(err)
	}
	awaitCleanup(t, s)
	if _, err := os.Lstat(filepath.Join(root, "pages", v.PreviewID)); !os.IsNotExist(err) {
		t.Fatal("the page files stayed", err)
	}
	// The journal keeps the digest, never the files, and a restart accepts the receipt.
	journal, _ := os.ReadFile(filepath.Join(root, "history.jsonl"))
	if strings.Contains(string(journal), wire[0].ContentBase64) || !strings.Contains(string(journal), v.PagesDigest) {
		t.Fatal("the journal", string(journal))
	}
	s.Close()
	again, _ := serviceAt(t, root, rt, time.Now)
	again.profiles[p.ProfileID] = p
	if again.unavailable || again.store.broken {
		t.Fatal("a restart refused a receipt with page files")
	}
	if replay, err := again.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", c); err != nil || replay.PreviewID != v.PreviewID {
		t.Fatal("replay after a restart", replay, err)
	}
}

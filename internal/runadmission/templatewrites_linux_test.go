//go:build linux

package runadmission

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

const (
	salonConfig = "{\n  \"cal.slotMinutes\": 30,\n  \"site.brand\": { \"name\": \"Booking\" }\n}\n"
	salonPreset = "{\n  \"key\": \"salon\",\n  \"brand\": { \"name\": \"Salon\" }\n}\n"
)

// salonLibrary makes a template library whose dist/booking_lite is a salon's app and returns its bare mirror and the
// template a request names.
func salonLibrary(t *testing.T, root string) (string, serviceapi.RunTemplateV1) {
	t.Helper()
	return bookingLibrary(t, root, map[string]string{
		"app.json":           "{\"defaultPreset\": \"spa\"}\n",
		"app.config.json":    salonConfig,
		"presets/salon.json": salonPreset,
	})
}

// bookingLibrary makes a template library whose dist/booking_lite holds files.
func bookingLibrary(t *testing.T, root string, files map[string]string) (string, serviceapi.RunTemplateV1) {
	t.Helper()
	library := filepath.Join(root, "salon-library")
	git(t, "", "init", "-q", "-b", "main", library)
	git(t, library, "config", "user.email", "library@example.test")
	git(t, library, "config", "user.name", "Library Test")
	for path, contents := range files {
		file := filepath.Join(library, "dist", "booking_lite", filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, library, "add", "-A")
	git(t, library, "commit", "-q", "-m", "booking_lite 1.0.0")
	mirror := filepath.Join(root, "salon-library.git")
	git(t, "", "clone", "-q", "--bare", library, mirror)
	commit := git(t, library, "rev-parse", "HEAD")
	return mirror, serviceapi.RunTemplateV1{TemplateID: "booking_lite", Version: "1.0.0", CommitSHA: commit, TreeSHA: git(t, library, "rev-parse", commit+":dist/booking_lite")}
}

// salonEdit is Repo C's writes for a salon: the preset whole, defaultPreset and a setting.
func salonEdit(t *testing.T, mirror string, template serviceapi.RunTemplateV1) *serviceapi.RunExactEditV1 {
	t.Helper()
	blob := func(path string) string {
		return git(t, "", "--git-dir="+mirror, "rev-parse", template.TreeSHA+":"+path)
	}
	return &serviceapi.RunExactEditV1{Replacements: []serviceapi.RunReplacementV1{
		{Path: "presets/salon.json", BaseBlob: blob("presets/salon.json"), From: `{"key":"salon","brand":{"name":"Salon"}}`, To: `{"key":"salon","brand":{"name":"Studio Nine"}}`},
		{Path: "app.json", BaseBlob: blob("app.json"), Pointer: "/defaultPreset", From: `"spa"`, To: `"salon"`},
		{Path: "app.config.json", BaseBlob: blob("app.config.json"), Pointer: "/cal.slotMinutes", From: `30`, To: `45`},
	}}
}

func treeSnapshot(t *testing.T, root string) string {
	t.Helper()
	var out strings.Builder
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		fmt.Fprintf(&out, "%s %s %d %d", path, info.Mode(), info.Size(), info.ModTime().UnixNano())
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			out.WriteString(" " + hex.EncodeToString(sum[:]))
		}
		out.WriteString("\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func (f *admissionFixture) startCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.starts)
}

func TestATemplatesCopyWithExactWritesIsCheckedBeforeItsReceiptAndBound(t *testing.T) {
	var mirror string
	var template serviceapi.RunTemplateV1
	fixture := newEditedAdmissionFixture(t, true, func(root string, profiles *ProfileFileV1) {
		mirror, template = salonLibrary(t, root)
		profiles.Profiles[0].TemplateMirrorPath = mirror
	})
	mirrorBefore := treeSnapshot(t, mirror)
	settingsOnly := func(requestID string, change func(*serviceapi.RunAdmissionRequestV1)) serviceapi.RunAdmissionRequestV1 {
		request := fixture.request
		request.RequestID = requestID
		named := template
		request.Template = &named
		request.ExactEdit = salonEdit(t, mirror, template)
		if change != nil {
			change(&request)
		}
		return request
	}
	receipt := func(requestID string) string {
		return filepath.Join(fixture.service, "admissions", receiptKey(fixture.principal.PrincipalID, requestID)+".json")
	}
	setting := func(r *serviceapi.RunAdmissionRequestV1, pointer, from, to string) {
		base := r.ExactEdit.Replacements[2].BaseBlob
		r.ExactEdit.Replacements = append(r.ExactEdit.Replacements, serviceapi.RunReplacementV1{Path: "app.config.json", BaseBlob: base, Pointer: pointer, From: from, To: to})
	}

	for _, test := range []struct {
		name   string
		change func(*serviceapi.RunAdmissionRequestV1)
		want   error
	}{
		{"a wrong blob", func(r *serviceapi.RunAdmissionRequestV1) {
			r.ExactEdit.Replacements[1].BaseBlob = r.ExactEdit.Replacements[2].BaseBlob
		}, serviceapi.ErrExactEditNotAccepted},
		{"a value that is not the one there", func(r *serviceapi.RunAdmissionRequestV1) { r.ExactEdit.Replacements[2].From = `25` }, serviceapi.ErrExactEditNotAccepted},
		{"a whole document that is not the file", func(r *serviceapi.RunAdmissionRequestV1) { r.ExactEdit.Replacements[0].From = `{"key":"salon"}` }, serviceapi.ErrExactEditNotAccepted},
		{"a file the template does not have", func(r *serviceapi.RunAdmissionRequestV1) { r.ExactEdit.Replacements[1].Path = "presets/spa.json" }, serviceapi.ErrExactEditNotAccepted},
		{"nesting pointers", func(r *serviceapi.RunAdmissionRequestV1) {
			setting(r, "/site.brand", `{"name":"Booking"}`, `{"name":"Nine"}`)
			setting(r, "/site.brand/name", `"Booking"`, `"Nine"`)
		}, serviceapi.ErrExactEditNotAccepted},
		{"the whole document beside another value", func(r *serviceapi.RunAdmissionRequestV1) {
			r.ExactEdit.Replacements = append(r.ExactEdit.Replacements, serviceapi.RunReplacementV1{Path: "presets/salon.json",
				BaseBlob: r.ExactEdit.Replacements[0].BaseBlob, Pointer: "/brand/name", From: `"Salon"`, To: `"Nine"`})
		}, serviceapi.ErrExactEditNotAccepted},
		{"more than 40", func(r *serviceapi.RunAdmissionRequestV1) {
			for i := 0; len(r.ExactEdit.Replacements) <= authority.MaxTemplateExactEditReplacements; i++ {
				setting(r, fmt.Sprintf("/k%d", i), `1`, `2`)
			}
		}, serviceapi.ErrExactEditNotAccepted},
		{"a value over 64 KB", func(r *serviceapi.RunAdmissionRequestV1) {
			r.ExactEdit.Replacements[2].To = `"` + strings.Repeat("x", authority.MaxTemplateExactEditValueBytes) + `"`
		}, serviceapi.ErrExactEditNotAccepted},
		{"a template the mirror does not hold", func(r *serviceapi.RunAdmissionRequestV1) { r.Template.TreeSHA = strings.Repeat("e", 40) }, serviceapi.ErrTemplateNotAccepted},
		{"a source commit beside the template", func(r *serviceapi.RunAdmissionRequestV1) {
			r.ExactEdit.SourceSHA = git(t, fixture.repository, "rev-parse", "HEAD")
		}, serviceapi.ErrAdmissionUnavailable},
	} {
		requestID := "request-" + strings.ReplaceAll(test.name, " ", "-")
		if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, settingsOnly(requestID, test.change)); !errors.Is(err, test.want) {
			t.Fatalf("%s: admission = %v, want %v", test.name, err, test.want)
		}
		if _, err := os.Stat(receipt(requestID)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: a refused template's writes left a receipt: %v", test.name, err)
		}
		if _, err := os.Stat(filepath.Join(fixture.input, DeriveRunID(fixture.principal.PrincipalID, requestID))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: a refused template's writes left the run's input: %v", test.name, err)
		}
	}
	if fixture.startCount() != 0 {
		t.Fatal("a refused template's writes started a run")
	}

	// Repo C sends a refused settings-only request the builder's way under the same request ID (§10.6): it is admitted.
	builder := fixture.request
	builder.RequestID = "request-a-wrong-blob"
	named := template
	builder.Template = &named
	builder.TaskMarkdown = "# A salon\n\nCopy the template, then set its settings.\n"
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, builder); err != nil {
		t.Fatalf("the builder's way after a refusal, under the same request ID: %v", err)
	}
	if fixture.startCount() != 1 {
		t.Fatal("the builder's way did not start")
	}

	request := settingsOnly("request-salon", nil)
	response, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(fixture.input, response.RunID, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest authority.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.TemplateCopy == nil || manifest.TemplateCopy.TreeSHA != template.TreeSHA || manifest.TemplateCopy.MirrorPath != mirror ||
		manifest.ExactEdit == nil || manifest.ExactEdit.SourceSHA != "" || len(manifest.ExactEdit.Replacements) != 3 ||
		manifest.ExactEdit.Replacements[0] != (authority.ExactEditReplacement{Path: "presets/salon.json", BaseBlob: request.ExactEdit.Replacements[0].BaseBlob,
			From: request.ExactEdit.Replacements[0].From, To: request.ExactEdit.Replacements[0].To}) ||
		manifest.Worktree.Branch != "abcp/"+response.RunID || !strings.Contains(string(data), `"exact_edit":{"replacements":[`) {
		t.Fatalf("the run's manifest does not bind the template and its writes: %s", data)
	}
	if _, err := authority.New(manifest); err != nil && strings.Contains(err.Error(), "exact edit") {
		t.Fatalf("the run's authority refuses its writes: %v", err)
	}
	if fixture.startCount() != 2 {
		t.Fatal("the template's writes did not start their run")
	}
	registerFixtureRun(t, fixture, response.RunID)
	if replayed, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request); err != nil || replayed != response {
		t.Fatalf("the same request replays: %+v %v", replayed, err)
	}
	changed := settingsOnly("request-salon", func(r *serviceapi.RunAdmissionRequestV1) { r.ExactEdit.Replacements[2].To = `60` })
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, changed); !errors.Is(err, serviceapi.ErrRequestIDConflict) {
		t.Fatalf("the same request id with other writes is another request: %v", err)
	}
	if fixture.startCount() != 2 {
		t.Fatal("a replay or a conflict started a run")
	}
	if after := treeSnapshot(t, mirror); after != mirrorBefore {
		t.Fatalf("admission wrote in the mirror:\n%s\n---\n%s", mirrorBefore, after)
	}
}

func TestAProfileWithoutAMirrorRefusesATemplatesCopyWithExactWrites(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	mirror, template := salonLibrary(t, t.TempDir())
	request := fixture.request
	request.Template = &template
	request.ExactEdit = salonEdit(t, mirror, template)
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request); !errors.Is(err, serviceapi.ErrTemplateNotAccepted) {
		t.Fatalf("template's writes without a mirror = %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.service, "admissions", receiptKey(fixture.principal.PrincipalID, request.RequestID)+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused template's writes left a receipt: %v", err)
	}
	if fixture.startCount() != 0 {
		t.Fatal("a refused template's writes started a run")
	}
}

// Writes at their bounds make the run's manifest larger than any other (each value a JSON string there, where Go writes
// "<" in six bytes): it is admitted, and read back, as any run's.
func TestATemplatesWritesAtTheirBoundsFitTheRunsManifest(t *testing.T) {
	half := (authority.MaxTemplateExactEditBytes / 4) - 8
	angles, ampersands := `"`+strings.Repeat("<", half)+`"`, `"`+strings.Repeat("&", half)+`"`
	var mirror string
	var template serviceapi.RunTemplateV1
	fixture := newEditedAdmissionFixture(t, true, func(root string, profiles *ProfileFileV1) {
		mirror, template = bookingLibrary(t, root, map[string]string{"content.json": `{"a": ` + angles + `, "b": ` + angles + "}\n"})
		profiles.Profiles[0].TemplateMirrorPath = mirror
	})
	blob := git(t, "", "--git-dir="+mirror, "rev-parse", template.TreeSHA+":content.json")
	request := fixture.request
	request.Template = &template
	request.ExactEdit = &serviceapi.RunExactEditV1{Replacements: []serviceapi.RunReplacementV1{
		{Path: "content.json", BaseBlob: blob, Pointer: "/a", From: angles, To: ampersands},
		{Path: "content.json", BaseBlob: blob, Pointer: "/b", From: angles, To: ampersands},
	}}
	response, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request)
	if err != nil {
		t.Fatalf("writes at their bounds were refused: %v", err)
	}
	info, err := os.Stat(filepath.Join(fixture.input, response.RunID, "manifest.json"))
	if err != nil || info.Size() <= MaxManifestTemplateBytes || info.Size() > MaxRunManifestBytes {
		t.Fatalf("the run's manifest is %v bytes, %v; want above the profile's bound and within the run's", info, err)
	}
	registerFixtureRun(t, fixture, response.RunID)
	if replayed, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request); err != nil || replayed != response {
		t.Fatalf("the same request replays: %+v %v", replayed, err)
	}
}

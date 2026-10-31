package serviceapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
)

// Page files (Repo C STANDARD_PAGES.md §14) are opaque named files: ABCP checks their names, sizes, canonical base64
// and that each is a JSON object, and nothing of what they say.
func TestPageFilesAreNamedBoundedJSONObjectsInOneCanonicalSpelling(t *testing.T) {
	good := []PageFileV1{pageFile("contact-us.json", `{"format":"anything","n":1}`), pageFile("privacy-policy.json", " {\n}\n"), pageFile("terms.json", "{}")}
	files, err := DecodePageFiles(good)
	if err != nil || len(files) != 3 || files[1].Name != "privacy-policy.json" || string(files[1].Content) != " {\n}\n" {
		t.Fatalf("files = %+v %v", files, err)
	}
	if files, err := DecodePageFiles(nil); err != nil || len(files) != 0 || PageFilesDigest(files) != "" {
		t.Fatal("no files is an empty set", files, err)
	}
	// The digest is one line per file in name order: its name, a space and its bytes' SHA-256.
	var lines string
	for _, f := range files {
		sum := sha256.Sum256(f.Content)
		lines += f.Name + " " + hex.EncodeToString(sum[:]) + "\n"
	}
	sum := sha256.Sum256([]byte(lines))
	if PageFilesDigest(files) != hex.EncodeToString(sum[:]) {
		t.Fatal("digest", PageFilesDigest(files))
	}
	edited := []PageFile{files[0], files[1], {Name: "terms.json", Content: []byte(`{"x":1}`)}}
	if PageFilesDigest(edited) == PageFilesDigest(files) {
		t.Fatal("a changed file keeps the digest")
	}
	at := func(i int, f PageFileV1) []PageFileV1 { out := append([]PageFileV1{}, good...); out[i] = f; return out }
	big := `{"x":"` + strings.Repeat("a", MaxPageFileBytes-7) + `"}`
	var many, heavy []PageFileV1
	for i := 0; i <= MaxPageFiles; i++ {
		many = append(many, pageFile(string(rune('a'+i))+".json", "{}"))
	}
	for i := 0; i < 5; i++ {
		heavy = append(heavy, pageFile(string(rune('a'+i))+".json", `{"x":"`+strings.Repeat("a", MaxPageFileBytes-8)+`"}`))
	}
	for name, set := range map[string][]PageFileV1{
		"a path":            at(0, pageFile("../contact.json", "{}")),
		"a folder":          at(0, pageFile("a/contact.json", "{}")),
		"capitals":          at(0, pageFile("Contact.json", "{}")),
		"a leading digit":   at(0, pageFile("1contact.json", "{}")),
		"another extension": at(0, pageFile("contact.php", "{}")),
		"a hidden file":     at(0, pageFile(".json", "{}")),
		"a long name":       at(0, pageFile("a"+strings.Repeat("b", 60)+".json", "{}")),
		"twice":             {good[0], good[0]},
		"out of order":      {good[2], good[0]},
		"an array":          at(0, pageFile("contact-us.json", "[]")),
		"a string":          at(0, pageFile("contact-us.json", `"{}"`)),
		"not JSON":          at(0, pageFile("contact-us.json", "{")),
		"two values":        at(0, pageFile("contact-us.json", "{} {}")),
		"empty":             at(0, pageFile("contact-us.json", "")),
		"not UTF-8":         at(0, pageFile("contact-us.json", "{\"a\":\"\xff\"}")),
		"line breaks":       at(0, PageFileV1{Name: "contact-us.json", ContentBase64: "e30\n="}),
		"unpadded":          at(0, PageFileV1{Name: "contact-us.json", ContentBase64: "e30"}),
		"url base64":        at(0, PageFileV1{Name: "contact-us.json", ContentBase64: base64.URLEncoding.EncodeToString([]byte(`{"a":"??>"}`))}),
		"a file too big":    at(0, pageFile("contact-us.json", big)),
		"too many":          many,
		"too much in all":   heavy,
	} {
		if _, err := DecodePageFiles(set); !errors.Is(err, ErrPageFilesInvalid) {
			t.Errorf("%s = %v", name, err)
		}
	}
	if _, err := DecodePageFiles(many[:MaxPageFiles]); err != nil {
		t.Fatal("the most files", err)
	}
	if _, err := DecodePageFiles(heavy[:4]); err != nil {
		t.Fatal("the most bytes", err)
	}
}

// A preview's create may carry page files, past the 8 KiB of every other preview command and within their own bound;
// without them it keeps its 8 KiB, and a stop never takes them.
func TestAPreviewCreateMayCarryPageFilesWithinTheirOwnBound(t *testing.T) {
	s := newTestServer(t, &testCatalog{runs: []runtimecatalog.RunRegistrationV1{{RunID: "run-1"}}}, time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC))
	grantPreviewControl(s)
	backend := &testPreviewController{available: true, result: previewDTO()}
	s.reserved.Preview = backend
	post := func(path string, body []byte) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, authenticatedRequest(http.MethodPost, path, bytes.NewReader(body)))
		return w
	}
	c := PreviewRequestV1{SchemaVersion: 1, RequestID: "create-1", ExpectedRunID: "run-1", CheckpointActivityID: strings.Repeat("b", 64), ProfileID: "wp-v1",
		DelegatedActor: DelegatedActorV1{SubjectID: "alice", SubjectType: PrincipalUser}}
	for _, name := range []string{"a.json", "b.json", "c.json", "d.json"} {
		c.Pages = append(c.Pages, pageFile(name, `{"x":"`+strings.Repeat("a", MaxPageFileBytes-8)+`"}`))
	}
	data, _ := json.Marshal(c)
	if len(data) <= MaxRequestBodyBytes {
		t.Fatal("the largest set is past an ordinary body", len(data))
	}
	if w := post("/v1/runs/run-1/previews", data); w.Code != 202 || len(backend.created.Pages) != 4 || backend.created.Pages[3].Name != "d.json" {
		t.Fatalf("create with pages = %d %s", w.Code, w.Body.String())
	}
	calls := backend.calls
	plain := c
	plain.Pages = nil
	padded, _ := json.Marshal(plain)
	padded = append(padded[:len(padded)-1], []byte(strings.Repeat(" ", 8192)+"}")...)
	for name, body := range map[string][]byte{
		"a create without pages past 8 KiB": padded,
		"a page file that is no object":     []byte(strings.Replace(string(data), `"name":"a.json","content_base64":"`, `"name":"a.json","content_base64":"WzFd`, 1)),
		"pages for another run":             []byte(strings.Replace(string(data), `"expected_run_id":"run-1"`, `"expected_run_id":"run-2"`, 1)),
	} {
		if w := post("/v1/runs/run-1/previews", body); w.Code != 400 {
			t.Errorf("%s = %d %s", name, w.Code, w.Body.String())
		}
	}
	stop, _ := json.Marshal(map[string]any{"schema_version": 1, "request_id": "stop-1", "expected_run_id": "run-1", "expected_preview_id": backend.result.PreviewID,
		"delegated_actor": c.DelegatedActor, "pages": c.Pages[:1]})
	if w := post("/v1/runs/run-1/previews/"+backend.result.PreviewID+"/stop", stop); w.Code != 400 {
		t.Fatalf("a stop with pages = %d", w.Code)
	}
	if backend.calls != calls {
		t.Fatal("refused commands reached the controller")
	}
	// A preview created with page files names them; a name that is not a digest is refused as a leak.
	backend.result.PagesDigest = strings.Repeat("9", 64)
	if w := post("/v1/runs/run-1/previews", data); w.Code != 202 || !strings.Contains(w.Body.String(), `"pages_digest":"`+strings.Repeat("9", 64)+`"`) {
		t.Fatalf("pages digest = %d %s", w.Code, w.Body.String())
	}
	backend.result.PagesDigest = "/private/pages"
	if w := post("/v1/runs/run-1/previews", data); w.Code != 500 || strings.Contains(w.Body.String(), "/private/") {
		t.Fatalf("a bad pages digest = %d %s", w.Code, w.Body.String())
	}
	backend.result.PagesDigest = ""
	backend.err = ErrPreviewPagesRefused
	if w := post("/v1/runs/run-1/previews", data); w.Code != 400 || !strings.Contains(w.Body.String(), `"code":"invalid_request"`) {
		t.Fatalf("a profile that takes no pages = %d %s", w.Code, w.Body.String())
	}
}

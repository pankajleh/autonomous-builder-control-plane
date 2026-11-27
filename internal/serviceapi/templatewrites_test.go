package serviceapi

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// repoCSettingsOnlyJSON is a settings-only request as Repo C's AbcpRequestFactory writes it (TEMPLATE_LIBRARY_ROUND_2
// §10.6): its fields in Repo C's order, the template, no executor, no read paths, and an exact edit with no source_sha
// whose JSON values carry empty find and replace; the preset is written whole, at the pointer "".
func repoCSettingsOnlyJSON(replacements string) string {
	return `{"schema_version":1,"request_id":"repo-c-6f1d7a52-3c1b-4d8e-9a0f-2b3c4d5e6f70","profile_id":"local-p02",` +
		`"product_authorization_id":"6f1d7a52-3c1b-4d8e-9a0f-2b3c4d5e6f70","product_task_id":"f4dc7767-1a2b-4c3d-8e9f-0a1b2c3d4e5f",` +
		`"product_version_id":"8088966a-1b2c-4d3e-9f0a-1b2c3d4e5f60","product_manifest_sha256":"` + strings.Repeat("a", 64) + `",` +
		`"repository_base_sha":"` + strings.Repeat("b", 40) + `","task_markdown":"# A salon\n\nStudio Nine.\n",` +
		`"delegated_actor":{"subject_id":"operator-1","subject_type":"operator"},` +
		`"template":{"template_id":"booking_lite","version":"1.0.0","commit_sha":"` + strings.Repeat("1", 40) + `","tree_sha":"` + strings.Repeat("2", 40) + `"},` +
		`"exact_edit":{"replacements":[` + replacements + `]}}`
}

const repoCSalonWrites = `{"path":"presets/salon.json","base_blob":"` + "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" + `","find":"","replace":"","pointer":"","from":"{\"key\":\"salon\",\"default\":true}","to":"{\"key\":\"salon\",\"default\":true,\"brand\":{\"name\":\"Studio Nine\"}}"},` +
	`{"path":"app.json","base_blob":"cccccccccccccccccccccccccccccccccccccccc","find":"","replace":"","pointer":"/defaultPreset","from":"\"spa\"","to":"\"salon\""},` +
	`{"path":"app.config.json","base_blob":"dddddddddddddddddddddddddddddddddddddddd","find":"","replace":"","pointer":"/cal.slotMinutes","from":"30","to":"45"},` +
	`{"path":"app.config.json","base_blob":"dddddddddddddddddddddddddddddddddddddddd","find":"","replace":"","pointer":"/cal.hours","from":"{\"mon\":[\"09:00\",\"17:00\"]}","to":"{\"mon\":[\"10:00\",\"18:00\"]}"}`

func TestRunAdmissionTakesRepoCsSettingsOnlyRequest(t *testing.T) {
	controller := &testRunAdmissionController{response: RunAdmissionResponseV1{RunID: "admission-run-1", RunURL: "/v1/runs/admission-run-1"}}
	server := newTestServer(t, &testCatalog{}, time.Now().UTC())
	server.reserved.RunAdmission = controller
	grantRunAdmission(server)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, authenticatedRequest(http.MethodPost, "/v1/runs", bytes.NewReader([]byte(repoCSettingsOnlyJSON(repoCSalonWrites)))))
	if response.Code != http.StatusAccepted {
		t.Fatalf("Repo C's settings-only request = %d %q", response.Code, response.Body.String())
	}
	edit := controller.request.ExactEdit
	if controller.request.Template == nil || controller.request.Executor != nil || edit == nil || edit.SourceSHA != "" || len(edit.Replacements) != 4 ||
		edit.Replacements[0].Pointer != "" || edit.Replacements[0].From != `{"key":"salon","default":true}` || edit.Replacements[2].Pointer != "/cal.slotMinutes" {
		t.Fatalf("the template's writes reach admission: %+v %+v", controller.request.Template, edit)
	}

	// Admission's two refusals are the 422s Repo C sends the same submission the builder's way on.
	for _, test := range []struct {
		err  error
		code string
	}{{ErrExactEditNotAccepted, "exact_edit_not_accepted"}, {ErrTemplateNotAccepted, "template_not_accepted"}} {
		controller.err = test.err
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, authenticatedRequest(http.MethodPost, "/v1/runs", bytes.NewReader([]byte(repoCSettingsOnlyJSON(repoCSalonWrites)))))
		if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
			t.Fatalf("%v = %d %q", test.err, response.Code, response.Body.String())
		}
	}

	// Its bounds are admission's, against the template's files (422); only a request of another form is 400.
	controller.err = nil
	var many []string
	for i := 0; i < 41; i++ {
		many = append(many, fmt.Sprintf(`{"path":"app.config.json","base_blob":"dddddddddddddddddddddddddddddddddddddddd","find":"","replace":"","pointer":"/k%d","from":"1","to":"2"}`, i))
	}
	nesting := repoCSalonWrites + `,{"path":"app.config.json","base_blob":"dddddddddddddddddddddddddddddddddddddddd","find":"","replace":"","pointer":"/cal.hours/mon","from":"1","to":"2"}`
	for name, body := range map[string]string{"more than 40": repoCSettingsOnlyJSON(strings.Join(many, ",")), "nesting pointers": repoCSettingsOnlyJSON(nesting)} {
		calls := controller.calls
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, authenticatedRequest(http.MethodPost, "/v1/runs", bytes.NewReader([]byte(body))))
		if response.Code != http.StatusAccepted || controller.calls != calls+1 {
			t.Fatalf("%s did not reach admission: %d %q", name, response.Code, response.Body.String())
		}
	}
	withSource := strings.Replace(repoCSettingsOnlyJSON(repoCSalonWrites), `"exact_edit":{`, `"exact_edit":{"source_sha":"`+strings.Repeat("d", 40)+`",`, 1)
	withExecutor := strings.Replace(repoCSettingsOnlyJSON(repoCSalonWrites), `"exact_edit":{`, `"executor":{"effort":"high"},"exact_edit":{`, 1)
	noTemplate := strings.Replace(repoCSettingsOnlyJSON(repoCSalonWrites), `"template":{"template_id":"booking_lite","version":"1.0.0","commit_sha":"`+strings.Repeat("1", 40)+`","tree_sha":"`+strings.Repeat("2", 40)+`"},`, "", 1)
	migration := repoCSettingsOnlyJSON(strings.Replace(repoCSalonWrites, `"path":"app.json"`, `"path":"db/migrations/app.json"`, 1))
	for name, body := range map[string]string{"a source commit beside the template": withSource, "an executor": withExecutor, "no template": noTemplate, "a migration": migration} {
		calls := controller.calls
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, authenticatedRequest(http.MethodPost, "/v1/runs", bytes.NewReader([]byte(body))))
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"invalid_request"`) || controller.calls != calls {
			t.Fatalf("%s = %d %q", name, response.Code, response.Body.String())
		}
	}
}

func TestATemplatesCopyWithExactWritesIsValidatedByItsForm(t *testing.T) {
	valid := RunAdmissionRequestV1{
		SchemaVersion: 1, RequestID: "request-1", ProfileID: "default",
		ProductAuthorizationID: "authorization-1", ProductTaskID: "task-1", ProductVersionID: "version-1",
		ProductManifestSHA256: strings.Repeat("a", 64), RepositoryBaseSHA: strings.Repeat("b", 40),
		TaskMarkdown: "# A salon\n", DelegatedActor: DelegatedActorV1{SubjectID: "user-1", SubjectType: PrincipalUser},
		Template: &RunTemplateV1{TemplateID: "booking_lite", Version: "1.0.0", CommitSHA: strings.Repeat("c", 40), TreeSHA: strings.Repeat("d", 40)},
	}
	whole := RunReplacementV1{Path: "presets/salon.json", BaseBlob: strings.Repeat("e", 40), From: `{"key":"salon"}`, To: `{"key":"salon","n":1}`}
	setting := RunReplacementV1{Path: "app.config.json", BaseBlob: strings.Repeat("f", 40), Pointer: "/cal.slotMinutes", From: `30`, To: `45`}
	words := RunReplacementV1{Path: "public/index.html", BaseBlob: strings.Repeat("1", 40), Find: "Book now", Replace: "Book a visit"}
	valid.ExactEdit = &RunExactEditV1{Replacements: []RunReplacementV1{whole, setting, words}}
	if err := ValidateRunAdmissionRequestV1(valid); err != nil {
		t.Fatalf("a template's copy with exact writes is refused: %v", err)
	}
	// Bounds and how values sit in a file are not the form: admission refuses them with 422.
	bounded := valid
	large := setting
	large.To = `"` + strings.Repeat("x", 70<<10) + `"`
	bounded.ExactEdit = &RunExactEditV1{Replacements: []RunReplacementV1{large, setting, whole, whole}}
	if err := ValidateRunAdmissionRequestV1(bounded); err != nil {
		t.Fatalf("the form refused what admission bounds: %v", err)
	}
	for name, change := range map[string]func(*RunAdmissionRequestV1){
		"no template":         func(r *RunAdmissionRequestV1) { r.Template = nil },
		"an executor":         func(r *RunAdmissionRequestV1) { r.Executor = &RunExecutorV1{Effort: "high"} },
		"a source and a copy": func(r *RunAdmissionRequestV1) { r.ExactEdit.SourceSHA = strings.Repeat("9", 40) },
		"nothing":             func(r *RunAdmissionRequestV1) { r.ExactEdit.Replacements = nil },
		"a migration":         func(r *RunAdmissionRequestV1) { r.ExactEdit.Replacements[1].Path = "migrations/app.config.json" },
		"git":                 func(r *RunAdmissionRequestV1) { r.ExactEdit.Replacements[1].Path = ".git/config.json" },
		"parent":              func(r *RunAdmissionRequestV1) { r.ExactEdit.Replacements[1].Path = "../app.config.json" },
		"backslash":           func(r *RunAdmissionRequestV1) { r.ExactEdit.Replacements[1].Path = "a\\app.config.json" },
		"a control character": func(r *RunAdmissionRequestV1) { r.ExactEdit.Replacements[1].Path = "app\x01.json" },
		"a short blob":        func(r *RunAdmissionRequestV1) { r.ExactEdit.Replacements[1].BaseBlob = "abc" },
		"not a JSON file":     func(r *RunAdmissionRequestV1) { r.ExactEdit.Replacements[1].Path = "app.config.yaml" },
		"no leading slash":    func(r *RunAdmissionRequestV1) { r.ExactEdit.Replacements[1].Pointer = "cal.slotMinutes" },
		"a bad escape":        func(r *RunAdmissionRequestV1) { r.ExactEdit.Replacements[1].Pointer = "/cal~2" },
		"a value not JSON":    func(r *RunAdmissionRequestV1) { r.ExactEdit.Replacements[1].To = "forty" },
		"the same value":      func(r *RunAdmissionRequestV1) { r.ExactEdit.Replacements[1].To = `30` },
		"words and a value":   func(r *RunAdmissionRequestV1) { r.ExactEdit.Replacements[1].Find = "30" },
		"no value after":      func(r *RunAdmissionRequestV1) { r.ExactEdit.Replacements[0].To = "" },
		"the same words":      func(r *RunAdmissionRequestV1) { r.ExactEdit.Replacements[2].Replace = "Book now" },
		"words with NUL":      func(r *RunAdmissionRequestV1) { r.ExactEdit.Replacements[2].Replace = "Book\x00" },
	} {
		invalid := valid
		edit := RunExactEditV1{Replacements: append([]RunReplacementV1(nil), valid.ExactEdit.Replacements...)}
		invalid.ExactEdit = &edit
		change(&invalid)
		if err := ValidateRunAdmissionRequestV1(invalid); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A quick edit never takes the whole document.
	quick := valid
	quick.Template = nil
	quick.ExactEdit = &RunExactEditV1{SourceSHA: strings.Repeat("9", 40), Replacements: []RunReplacementV1{whole}}
	if err := ValidateRunAdmissionRequestV1(quick); err == nil {
		t.Error("a quick edit took the whole document")
	}
}

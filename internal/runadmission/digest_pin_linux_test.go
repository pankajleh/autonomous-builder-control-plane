//go:build linux

package runadmission

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// pinnedRequest is a product request as Repo C sends one, with nothing but its own fields.
func pinnedRequest() serviceapi.RunAdmissionRequestV1 {
	return serviceapi.RunAdmissionRequestV1{
		SchemaVersion: 1, RequestID: "repo-c-6f1d7a52-3c1b-4d8e-9a0f-2b3c4d5e6f70", ProfileID: "local-p02",
		ProductAuthorizationID: "6f1d7a52-3c1b-4d8e-9a0f-2b3c4d5e6f70", ProductTaskID: "f4dc7767-1a2b-4c3d-8e9f-0a1b2c3d4e5f",
		ProductVersionID: "8088966a-1b2c-4d3e-9f0a-1b2c3d4e5f60", ProductManifestSHA256: strings.Repeat("a", 64),
		RepositoryBaseSHA: strings.Repeat("b", 40), TaskMarkdown: "# Build\n\nA cake shop.\n",
		DelegatedActor: serviceapi.DelegatedActorV1{SubjectID: "operator-1", SubjectType: serviceapi.PrincipalOperator},
	}
}

// Every request without a template's copy with exact writes keeps its JSON and its digest byte for byte (Repo C
// TEMPLATE_LIBRARY_ROUND_2 §10.5, "Unchanged"): a fresh build's, a quick edit's (words and a JSON value) and a template
// build's, as main 6ae5f89 made them. The receipt names that digest, so each keeps its receipt too.
func TestRequestDigestsArePinned(t *testing.T) {
	fresh := pinnedRequest()

	quick := pinnedRequest()
	quick.ExactEdit = &serviceapi.RunExactEditV1{SourceSHA: strings.Repeat("d", 40), Replacements: []serviceapi.RunReplacementV1{
		{Path: "index.html", BaseBlob: strings.Repeat("c", 40), Find: "Book now", Replace: "Reserve a table"},
		{Path: "content.json", BaseBlob: strings.Repeat("e", 40), Pointer: "/pages/home/title", From: `"Cakes"`, To: `"Cakes & tarts"`},
	}}

	template := pinnedRequest()
	template.Template = &serviceapi.RunTemplateV1{TemplateID: "booking_lite", Version: "1.2.3", CommitSHA: strings.Repeat("1", 40), TreeSHA: strings.Repeat("2", 40)}
	template.Executor = &serviceapi.RunExecutorV1{Model: "claude-sonnet-4-6", Effort: "high"}
	template.ReadPaths = []string{"/vendor/templates/booking_lite"}

	for _, test := range []struct {
		name    string
		request serviceapi.RunAdmissionRequestV1
		json    string
		digest  string
	}{
		{"a fresh build", fresh,
			`{"schema_version":1,"request_id":"repo-c-6f1d7a52-3c1b-4d8e-9a0f-2b3c4d5e6f70","profile_id":"local-p02","product_authorization_id":"6f1d7a52-3c1b-4d8e-9a0f-2b3c4d5e6f70","product_task_id":"f4dc7767-1a2b-4c3d-8e9f-0a1b2c3d4e5f","product_version_id":"8088966a-1b2c-4d3e-9f0a-1b2c3d4e5f60","product_manifest_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repository_base_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","task_markdown":"# Build\n\nA cake shop.\n","delegated_actor":{"subject_id":"operator-1","subject_type":"operator"}}`,
			"9a079acd9f7aa495bc6cd33b8f9c3595916980eef7e4b485700a8293b585b74c"},
		{"a quick edit", quick,
			`{"schema_version":1,"request_id":"repo-c-6f1d7a52-3c1b-4d8e-9a0f-2b3c4d5e6f70","profile_id":"local-p02","product_authorization_id":"6f1d7a52-3c1b-4d8e-9a0f-2b3c4d5e6f70","product_task_id":"f4dc7767-1a2b-4c3d-8e9f-0a1b2c3d4e5f","product_version_id":"8088966a-1b2c-4d3e-9f0a-1b2c3d4e5f60","product_manifest_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repository_base_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","task_markdown":"# Build\n\nA cake shop.\n","delegated_actor":{"subject_id":"operator-1","subject_type":"operator"},"exact_edit":{"source_sha":"dddddddddddddddddddddddddddddddddddddddd","replacements":[{"path":"index.html","base_blob":"cccccccccccccccccccccccccccccccccccccccc","find":"Book now","replace":"Reserve a table"},{"path":"content.json","base_blob":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","find":"","replace":"","pointer":"/pages/home/title","from":"\"Cakes\"","to":"\"Cakes \u0026 tarts\""}]}}`,
			"8c54e7dca3a5ad89d793ecf72b4a2f8c81035d569df2eb5926d2bd5dff43b042"},
		{"a template build", template,
			`{"schema_version":1,"request_id":"repo-c-6f1d7a52-3c1b-4d8e-9a0f-2b3c4d5e6f70","profile_id":"local-p02","product_authorization_id":"6f1d7a52-3c1b-4d8e-9a0f-2b3c4d5e6f70","product_task_id":"f4dc7767-1a2b-4c3d-8e9f-0a1b2c3d4e5f","product_version_id":"8088966a-1b2c-4d3e-9f0a-1b2c3d4e5f60","product_manifest_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repository_base_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","task_markdown":"# Build\n\nA cake shop.\n","delegated_actor":{"subject_id":"operator-1","subject_type":"operator"},"template":{"template_id":"booking_lite","version":"1.2.3","commit_sha":"1111111111111111111111111111111111111111","tree_sha":"2222222222222222222222222222222222222222"},"executor":{"model":"claude-sonnet-4-6","effort":"high"},"read_paths":["/vendor/templates/booking_lite"]}`,
			"1a1833cfd98fe363a5d8e4468dfa76370230383fc833663db57533b39c8ba793"},
	} {
		if err := serviceapi.ValidateRunAdmissionRequestV1(test.request); err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		data, err := json.Marshal(test.request)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != test.json {
			t.Errorf("%s: the request's JSON changed:\n%s", test.name, data)
		}
		if got := RequestDigest(test.request); got != test.digest {
			t.Errorf("%s: the request's digest changed: %s", test.name, got)
		}
	}
}

// A quick edit binds its run's manifest as before: the manifest's exact_edit, which the run's authority digest covers.
func TestAQuickEditsManifestEditIsPinned(t *testing.T) {
	quick := serviceapi.RunExactEditV1{SourceSHA: strings.Repeat("d", 40), Replacements: []serviceapi.RunReplacementV1{
		{Path: "index.html", BaseBlob: strings.Repeat("c", 40), Find: "Book now", Replace: "Reserve a table"},
		{Path: "content.json", BaseBlob: strings.Repeat("e", 40), Pointer: "/pages/home/title", From: `"Cakes"`, To: `"Cakes & tarts"`},
	}}
	data, err := json.Marshal(exactEditManifest(quick))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"source_sha":"dddddddddddddddddddddddddddddddddddddddd","replacements":[{"path":"index.html","base_blob":"cccccccccccccccccccccccccccccccccccccccc","find":"Book now","replace":"Reserve a table"},{"path":"content.json","base_blob":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","find":"","replace":"","pointer":"/pages/home/title","from":"\"Cakes\"","to":"\"Cakes \u0026 tarts\""}]}`; string(data) != want {
		t.Fatalf("a quick edit's manifest edit changed:\n%s", data)
	}
}

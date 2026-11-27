package run

import (
	"encoding/json"
	"strings"
	"testing"
)

// A quick edit's evidence keeps its shape (Repo C TEMPLATE_LIBRARY_ROUND_2 §10.5, "Unchanged"): exact-edit.json as main
// 6ae5f89 wrote it.
func TestAQuickEditsEvidenceIsPinned(t *testing.T) {
	data, err := json.Marshal(ExactEditResultV1{
		Kind: "ExactEditResultV1", SourceSHA: strings.Repeat("d", 40), CopySHA: strings.Repeat("1", 40), HeadSHA: strings.Repeat("2", 40),
		Branch: "abcp/admission-1", Worktree: "/product/.ralphex/worktrees/abcp/admission-1",
		Files: []ExactEditFileResultV1{{Path: "index.html", BaseBlob: strings.Repeat("c", 40), NewBlob: strings.Repeat("e", 40)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"kind":"ExactEditResultV1","source_sha":"dddddddddddddddddddddddddddddddddddddddd","copy_sha":"1111111111111111111111111111111111111111","head_sha":"2222222222222222222222222222222222222222","branch":"abcp/admission-1","worktree":"/product/.ralphex/worktrees/abcp/admission-1","files":[{"path":"index.html","base_blob":"cccccccccccccccccccccccccccccccccccccccc","new_blob":"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"}]}`; string(data) != want {
		t.Fatalf("a quick edit's evidence changed:\n%s", data)
	}
}

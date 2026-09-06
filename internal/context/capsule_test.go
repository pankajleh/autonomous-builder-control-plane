package context

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildIsDeterministicAndCompact(t *testing.T) {
	repository, head := capsuleRepository(t)
	spec := fixtureSpec(head)
	spec.Sources = []string{"docs/second.md", "docs/source.md"}
	first, firstJSON, err := Build(repository, spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Sources = []string{"docs/source.md", "docs/second.md"}
	second, secondJSON, err := Build(repository, spec)
	if err != nil {
		t.Fatal(err)
	}
	if first.CapsuleSHA256 != second.CapsuleSHA256 || !bytes.Equal(firstJSON, secondJSON) {
		t.Fatal("equivalent specs did not produce identical canonical capsules")
	}
	if first.Sources[0].Path != "docs/second.md" || first.Sources[1].Path != "docs/source.md" {
		t.Fatalf("sources are not canonically sorted: %+v", first.Sources)
	}
	if bytes.Contains(firstJSON, []byte("source document body that must not be copied")) {
		t.Fatal("capsule copied source contents instead of retaining a hash reference")
	}
	parsed, err := Parse(firstJSON)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(repository, parsed); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRejectsChangedSource(t *testing.T) {
	repository, head := capsuleRepository(t)
	_, data, err := Build(repository, fixtureSpec(head))
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	writeContextFile(t, filepath.Join(repository, "docs", "source.md"), []byte("changed"))
	if _, err := Verify(repository, capsule); err == nil || !strings.Contains(err.Error(), "SHA256 mismatch") {
		t.Fatalf("expected changed-source rejection, got %v", err)
	}
}

func TestBuildRejectsWrongBaseSHA(t *testing.T) {
	repository, head := capsuleRepository(t)
	spec := fixtureSpec(head)
	spec.BaseSHA = strings.Repeat("0", 40)
	if _, _, err := Build(repository, spec); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected base-SHA rejection, got %v", err)
	}
}

func TestBuildRejectsWrongRepositoryIdentity(t *testing.T) {
	repository, head := capsuleRepository(t)
	spec := fixtureSpec(head)
	spec.Repository = "different/project"
	if _, _, err := Build(repository, spec); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected repository-identity rejection, got %v", err)
	}
}

func TestBuildRejectsTraversalAndSymlinks(t *testing.T) {
	repository, head := capsuleRepository(t)
	spec := fixtureSpec(head)
	spec.Sources = []string{"../outside.md"}
	if _, _, err := Build(repository, spec); err == nil || !strings.Contains(err.Error(), "canonical") && !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected traversal rejection, got %v", err)
	}

	outside := filepath.Join(t.TempDir(), "outside.md")
	writeContextFile(t, outside, []byte("outside"))
	link := filepath.Join(repository, "docs", "linked.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	spec.Sources = []string{"docs/linked.md"}
	if _, _, err := Build(repository, spec); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestBuildRejectsDuplicateSource(t *testing.T) {
	repository, head := capsuleRepository(t)
	spec := fixtureSpec(head)
	spec.Sources = []string{"docs/source.md", "docs/source.md"}
	if _, _, err := Build(repository, spec); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate-source rejection, got %v", err)
	}
}

func TestParseRejectsOversizeOrNonCanonicalCapsule(t *testing.T) {
	if _, err := Parse(make([]byte, MaxCapsuleBytes+1)); err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("expected oversize rejection, got %v", err)
	}
	repository, head := capsuleRepository(t)
	_, data, err := Build(repository, fixtureSpec(head))
	if err != nil {
		t.Fatal(err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, data, "", "  "); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(pretty.Bytes()); err == nil || !strings.Contains(err.Error(), "not canonical") {
		t.Fatalf("expected non-canonical rejection, got %v", err)
	}
}

func TestBuildRejectsOversizeCapsule(t *testing.T) {
	repository, head := capsuleRepository(t)
	spec := fixtureSpec(head)
	large := strings.Repeat("x", MaxStringBytes)
	spec.Invariants = make([]string, MaxListItems)
	spec.NonGoals = make([]string, MaxListItems)
	for index := 0; index < MaxListItems; index++ {
		spec.Invariants[index] = large
		spec.NonGoals[index] = large
	}
	if _, _, err := Build(repository, spec); err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("expected built-capsule size rejection, got %v", err)
	}
}

func TestVerifyRejectsTamperedCapsuleHash(t *testing.T) {
	repository, head := capsuleRepository(t)
	capsule, _, err := Build(repository, fixtureSpec(head))
	if err != nil {
		t.Fatal(err)
	}
	capsule.Task = "tampered"
	if _, err := Verify(repository, capsule); err == nil || !strings.Contains(err.Error(), "capsule SHA256 mismatch") {
		t.Fatalf("expected capsule-hash rejection, got %v", err)
	}
}

func capsuleRepository(t *testing.T) (string, string) {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repository")
	gitContextCommand(t, "", "init", "-b", "main", repository)
	gitContextCommand(t, repository, "config", "user.email", "capsule@example.test")
	gitContextCommand(t, repository, "config", "user.name", "Capsule Test")
	gitContextCommand(t, repository, "remote", "add", "origin", "https://example.test/example/project.git")
	if err := os.MkdirAll(filepath.Join(repository, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeContextFile(t, filepath.Join(repository, "docs", "source.md"), []byte("source document body that must not be copied"))
	writeContextFile(t, filepath.Join(repository, "docs", "second.md"), []byte("second source"))
	gitContextCommand(t, repository, "add", "docs")
	gitContextCommand(t, repository, "commit", "-m", "source documents")
	return repository, gitContextCommand(t, repository, "rev-parse", "HEAD")
}

func fixtureSpec(head string) Spec {
	return Spec{
		PolicyVersion: PolicyVersion,
		Project:       "Autonomous Builder Control Plane",
		Plan:          "EP-004 plan",
		RoadmapPhase:  "Phase 3",
		ExecutionPack: "EP-004",
		Task:          "Task 1",
		Repository:    "example/project",
		BaseSHA:       head,
		Invariants:    []string{"Fail closed on authority drift."},
		NonGoals:      []string{"No semantic retrieval."},
		PredecessorOutcomes: []Outcome{{
			Task: "EP-003", Summary: "Added recovery control.", CommitSHA: head,
		}},
		Sources: []string{"docs/source.md"},
	}
}

func writeContextFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitContextCommand(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

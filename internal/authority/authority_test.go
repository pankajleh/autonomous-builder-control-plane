package authority

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
)

func TestNewRejectsMissingRequiredFields(t *testing.T) {
	valid := fixtureManifest(t)
	tests := []struct {
		name  string
		field string
		clear func(*Manifest)
	}{
		{name: "run ID", field: "run_id", clear: func(m *Manifest) { m.RunID = "" }},
		{name: "repository path", field: "repository.path", clear: func(m *Manifest) { m.Repository.Path = "" }},
		{name: "start SHA", field: "repository.start_sha", clear: func(m *Manifest) { m.Repository.StartSHA = "" }},
		{name: "plan path", field: "plan.path", clear: func(m *Manifest) { m.Plan.Path = "" }},
		{name: "plan hash", field: "plan.sha256", clear: func(m *Manifest) { m.Plan.SHA256 = "" }},
		{name: "binary path", field: "ralphex.binary_path", clear: func(m *Manifest) { m.Ralphex.BinaryPath = "" }},
		{name: "binary hash", field: "ralphex.binary_sha256", clear: func(m *Manifest) { m.Ralphex.BinarySHA256 = "" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := cloneManifest(valid)
			test.clear(&manifest)
			_, err := New(manifest)
			if err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("expected error naming %s, got %v", test.field, err)
			}
		})
	}
}

func TestNewRejectsEmptyAcceptanceArgv(t *testing.T) {
	manifest := fixtureManifest(t)
	manifest.Acceptance[0].Argv = nil
	_, err := New(manifest)
	if err == nil || !strings.Contains(err.Error(), "argv") {
		t.Fatalf("expected empty argv error, got %v", err)
	}
}

func TestNewRejectsPlanOutsideRepository(t *testing.T) {
	manifest := fixtureManifest(t)
	outside := filepath.Join(t.TempDir(), "outside.md")
	writeFile(t, outside, []byte("outside"), 0o600)
	manifest.Plan.Path = outside
	manifest.Plan.SHA256 = fileHash(t, outside)

	_, err := New(manifest)
	if err == nil || !strings.Contains(err.Error(), "outside governed repository") {
		t.Fatalf("expected path-boundary error, got %v", err)
	}
}

func TestNewRejectsPlanSymlinkOutsideRepository(t *testing.T) {
	manifest := fixtureManifest(t)
	outside := filepath.Join(t.TempDir(), "outside.md")
	writeFile(t, outside, []byte("outside"), 0o600)
	link := filepath.Join(manifest.Repository.Path, "linked-plan.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	manifest.Plan.Path = "linked-plan.md"
	manifest.Plan.SHA256 = fileHash(t, outside)

	_, err := New(manifest)
	if err == nil || !strings.Contains(err.Error(), "outside governed repository") {
		t.Fatalf("expected symlink path-boundary error, got %v", err)
	}
}

func TestNewSupportsRalphexModes(t *testing.T) {
	for _, mode := range []ralphex.Mode{ralphex.ModeFull, ralphex.ModeTasksOnly, ralphex.ModeReview} {
		t.Run(string(mode), func(t *testing.T) {
			manifest := fixtureManifest(t)
			manifest.Ralphex.Mode = mode
			if mode == ralphex.ModeReview {
				manifest.Worktree = WorktreePolicy{}
			}
			authority, err := New(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if got := authority.Ralphex().Mode; got != mode {
				t.Fatalf("mode mismatch: got %q, want %q", got, mode)
			}
		})
	}
}

func TestNewRejectsReviewWorktree(t *testing.T) {
	manifest := fixtureManifest(t)
	manifest.Ralphex.Mode = ralphex.ModeReview
	_, err := New(manifest)
	if err == nil || !strings.Contains(err.Error(), "review mode") {
		t.Fatalf("expected review worktree rejection, got %v", err)
	}
}

func TestNewRejectsUnsupportedMode(t *testing.T) {
	manifest := fixtureManifest(t)
	manifest.Ralphex.Mode = "turbo"
	_, err := New(manifest)
	if err == nil || !strings.Contains(err.Error(), "unsupported ralphex mode") {
		t.Fatalf("expected unsupported mode error, got %v", err)
	}
}

func TestNewRequiresExplicitBranchForWorktree(t *testing.T) {
	manifest := fixtureManifest(t)
	manifest.Worktree.Branch = ""
	_, err := New(manifest)
	if err == nil || !strings.Contains(err.Error(), "worktree.branch") {
		t.Fatalf("expected missing worktree branch error, got %v", err)
	}
}

func TestNewCanonicalizesPathsAndProducesStableHash(t *testing.T) {
	manifest := fixtureManifest(t)
	repositoryLink := filepath.Join(t.TempDir(), "repository-link")
	if err := os.Symlink(manifest.Repository.Path, repositoryLink); err != nil {
		t.Fatal(err)
	}
	firstInput := cloneManifest(manifest)
	firstInput.Repository.Path = repositoryLink
	firstInput.Plan.Path = filepath.Join("docs", "..", "plan.md")
	firstInput.Repository.Remotes = map[string]string{
		"upstream": "https://example.test/upstream.git",
		"origin":   "https://example.test/origin.git",
	}

	secondInput := cloneManifest(manifest)
	secondInput.Repository.Remotes = map[string]string{
		"origin":   "https://example.test/origin.git",
		"upstream": "https://example.test/upstream.git",
	}

	first, err := New(firstInput)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(secondInput)
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256() != second.SHA256() {
		t.Fatalf("same semantic manifest hashed differently:\n%s\n%s", first.SHA256(), second.SHA256())
	}
	if string(first.CanonicalJSON()) != string(second.CanonicalJSON()) {
		t.Fatalf("canonical serialization differs:\n%s\n%s", first.CanonicalJSON(), second.CanonicalJSON())
	}
	if got := first.Repository().Path; got != manifest.Repository.Path {
		t.Fatalf("repository path was not canonicalized: got %q", got)
	}
	if got := first.Plan().Path; got != filepath.Join(manifest.Repository.Path, "plan.md") {
		t.Fatalf("plan path was not canonicalized: got %q", got)
	}
}

func TestAuthorityDoesNotExposeMutableState(t *testing.T) {
	manifest := fixtureManifest(t)
	authority, err := New(manifest)
	if err != nil {
		t.Fatal(err)
	}
	wantHash := authority.SHA256()
	wantJSON := string(authority.CanonicalJSON())

	manifest.Repository.Remotes["origin"] = "changed"
	manifest.Acceptance[0].Argv[0] = "changed"
	copyManifest := authority.Manifest()
	copyManifest.Repository.Remotes["origin"] = "changed-again"
	copyManifest.Acceptance[0].Argv[0] = "changed-again"
	bytes := authority.CanonicalJSON()
	bytes[0] = '['

	if authority.SHA256() != wantHash || string(authority.CanonicalJSON()) != wantJSON {
		t.Fatal("validated authority changed through mutable input or accessor")
	}
	if got := authority.Acceptance()[0].Argv[0]; got != "go" {
		t.Fatalf("acceptance argv mutated: got %q", got)
	}
}

func TestNewRejectsHashMismatch(t *testing.T) {
	manifest := fixtureManifest(t)
	manifest.Plan.SHA256 = strings.Repeat("0", 64)
	_, err := New(manifest)
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("expected hash mismatch error, got %v", err)
	}
}

func fixtureManifest(t *testing.T) Manifest {
	t.Helper()
	repository := t.TempDir()
	planPath := filepath.Join(repository, "plan.md")
	binaryPath := filepath.Join(t.TempDir(), "ralphex")
	writeFile(t, planPath, []byte("# governed plan\n"), 0o600)
	writeFile(t, binaryPath, []byte("#!/bin/sh\nexit 0\n"), 0o700)
	return Manifest{
		RunID: "run-123",
		Repository: RepositoryManifest{
			Path:          repository,
			Identity:      "example/project",
			Remotes:       map[string]string{"origin": "https://example.test/origin.git"},
			DefaultBranch: "main",
			StartSHA:      "0123456789abcdef",
		},
		Plan: PlanManifest{
			Path:   "plan.md",
			SHA256: fileHash(t, planPath),
		},
		Ralphex: RalphexManifest{
			BinaryPath:   binaryPath,
			BinarySHA256: fileHash(t, binaryPath),
			SourceSHA:    "abcdef0123456789",
			Mode:         ralphex.ModeFull,
		},
		Executor: ExecutorPolicy{
			Executor:     "codex",
			TaskModel:    "gpt-test",
			TaskEffort:   "high",
			ReviewModel:  "gpt-review",
			ReviewEffort: "medium",
		},
		Worktree: WorktreePolicy{Enabled: true, Branch: "governed-plan"},
		Acceptance: []AcceptanceCommand{{
			Name:     "unit tests",
			Class:    "unit",
			Required: true,
			Argv:     []string{"go", "test", "./..."},
		}},
		PolicyVersion: "branch-v1",
	}
}

func writeFile(t *testing.T, path string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
}

func fileHash(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

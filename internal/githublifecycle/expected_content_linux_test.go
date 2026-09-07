//go:build linux

package githublifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
)

func TestExpectedMergeContentDerivesExactTreeFromVerifiedPhase3Evidence(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "init", "-q")
	runGit(t, repository, "config", "user.name", "ABCP Test")
	runGit(t, repository, "config", "user.email", "abcp@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "content.txt"), []byte("accepted content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "add", "content.txt")
	runGit(t, repository, "commit", "-q", "-m", "accepted")
	headText := runGit(t, repository, "rev-parse", "HEAD")
	treeText := runGit(t, repository, "rev-parse", "HEAD^{tree}")
	head, err := NewGitSHA(strings.TrimSpace(headText))
	if err != nil {
		t.Fatal(err)
	}
	wantTree, err := NewGitSHA(strings.TrimSpace(treeText))
	if err != nil {
		t.Fatal(err)
	}
	base := head

	evidenceRoot := filepath.Join(t.TempDir(), "evidence")
	store, err := evidence.NewStore(evidenceRoot, "run-phase3")
	if err != nil {
		t.Fatal(err)
	}
	decision, _ := json.Marshal(map[string]any{
		"state": "READY_FOR_MERGE",
		"combined": map[string]any{"target": map[string]any{
			"head_sha": head.String(), "integration": map[string]any{"integrated_head_sha": head.String(), "baseline_sha": base.String()},
		}},
	})
	ref, err := store.WriteBytes("ready.json", phase3DecisionKind, decision)
	if err != nil {
		t.Fatal(err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}

	derived, err := DeriveExpectedMergeContent(context.Background(), repository, store.Root(), head, base, ref, gitPath)
	if err != nil {
		t.Fatal(err)
	}
	if derived.Policy() != ExpectedMergeContentPolicy || derived.SourceIntegratedHeadSHA() != head ||
		derived.ExpectedResultTreeSHA() != wantTree || derived.SourceIntegrationEvidence() != ref || derived.SHA256() == "" {
		t.Fatalf("unexpected derived content: %#v", derived)
	}
	copyJSON := derived.CanonicalJSON()
	copyJSON[0] = '!'
	if derived.CanonicalJSON()[0] == '!' {
		t.Fatal("expected-content canonical bytes were aliased")
	}

	forged := ref
	forged.SHA256 = strings.Repeat("0", sha256.Size*2)
	if _, err := DeriveExpectedMergeContent(context.Background(), repository, store.Root(), head, base, forged, gitPath); err == nil {
		t.Fatal("forged Phase 3 evidence digest was accepted")
	}
	missing := ref
	missing.URI = filepath.Join(store.RunDir(), "missing.json")
	if _, err := DeriveExpectedMergeContent(context.Background(), repository, store.Root(), head, base, missing, gitPath); err == nil {
		t.Fatal("missing Phase 3 evidence was accepted")
	}
	wrongDecision, _ := json.Marshal(map[string]any{
		"state": "READY_FOR_MERGE",
		"combined": map[string]any{"target": map[string]any{
			"head_sha": wantTree.String(), "integration": map[string]any{"integrated_head_sha": wantTree.String(), "baseline_sha": base.String()},
		}},
	})
	wrongRef, err := store.WriteBytes("wrong-head.json", phase3DecisionKind, wrongDecision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DeriveExpectedMergeContent(context.Background(), repository, store.Root(), head, base, wrongRef, gitPath); err == nil {
		t.Fatal("Phase 3 evidence for another integrated head was accepted")
	}
}

func TestExpectedMergeContentIgnoresReplacementRefs(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository, "init", "-q")
	runGit(t, repository, "config", "user.name", "ABCP Test")
	runGit(t, repository, "config", "user.email", "abcp@example.invalid")
	writeCommit := func(content, message string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repository, "content.txt"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, repository, "add", "content.txt")
		runGit(t, repository, "commit", "-q", "-m", message)
		return strings.TrimSpace(runGit(t, repository, "rev-parse", "HEAD"))
	}
	acceptedText := writeCommit("accepted\n", "accepted")
	acceptedTree := strings.TrimSpace(runGit(t, repository, "rev-parse", "HEAD^{tree}"))
	replacementText := writeCommit("replacement\n", "replacement")
	runGit(t, repository, "replace", acceptedText, replacementText)

	head, _ := NewGitSHA(acceptedText)
	base := head
	evidenceRoot := filepath.Join(t.TempDir(), "evidence")
	store, err := evidence.NewStore(evidenceRoot, "run-phase3")
	if err != nil {
		t.Fatal(err)
	}
	decision := []byte(`{"state":"READY_FOR_MERGE","combined":{"target":{"head_sha":"` + head.String() + `","integration":{"integrated_head_sha":"` + head.String() + `","baseline_sha":"` + base.String() + `"}}}}`)
	ref, err := store.WriteBytes("ready.json", phase3DecisionKind, decision)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := DeriveExpectedMergeContent(context.Background(), repository, store.Root(), head, base, ref, "git")
	if err != nil {
		t.Fatal(err)
	}
	if derived.ExpectedResultTreeSHA().String() != acceptedTree {
		t.Fatalf("replacement ref influenced tree: got %s want %s", derived.ExpectedResultTreeSHA(), acceptedTree)
	}
}

func runGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

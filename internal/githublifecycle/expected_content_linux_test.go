//go:build linux

package githublifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"maps"
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

func TestExpectedMergeContentDoesNotLazyFetchMissingPromisorObject(t *testing.T) {
	fixtureRoot := t.TempDir()
	source := filepath.Join(fixtureRoot, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "init", "-q")
	runGit(t, source, "config", "user.name", "ABCP Test")
	runGit(t, source, "config", "user.email", "abcp@example.invalid")
	if err := os.WriteFile(filepath.Join(source, "content.txt"), []byte("promisor content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "content.txt")
	runGit(t, source, "commit", "-q", "-m", "promisor fixture")
	headText := strings.TrimSpace(runGit(t, source, "rev-parse", "HEAD"))
	treeText := strings.TrimSpace(runGit(t, source, "rev-parse", "HEAD^{tree}"))

	origin := filepath.Join(fixtureRoot, "origin.git")
	runGit(t, fixtureRoot, "clone", "-q", "--bare", source, origin)
	runGit(t, origin, "config", "uploadpack.allowFilter", "true")
	partial := filepath.Join(fixtureRoot, "partial")
	runGit(t, fixtureRoot, "clone", "-q", "--filter=tree:0", "--no-checkout", "file://"+origin, partial)
	runGit(t, partial, "cat-file", "-e", headText+"^{commit}")
	assertGitObjectMissingLocally(t, partial, treeText)

	head, err := NewGitSHA(headText)
	if err != nil {
		t.Fatal(err)
	}
	evidenceRoot := filepath.Join(fixtureRoot, "evidence")
	store, err := evidence.NewStore(evidenceRoot, "run-phase3")
	if err != nil {
		t.Fatal(err)
	}
	decision := []byte(`{"state":"READY_FOR_MERGE","combined":{"target":{"head_sha":"` + head.String() + `","integration":{"integrated_head_sha":"` + head.String() + `","baseline_sha":"` + head.String() + `"}}}}`)
	ref, err := store.WriteBytes("ready.json", phase3DecisionKind, decision)
	if err != nil {
		t.Fatal(err)
	}

	before := gitObjectInventory(t, partial)
	if _, err := DeriveExpectedMergeContent(context.Background(), partial, store.Root(), head, head, ref, "git"); err == nil {
		t.Fatal("derivation fetched a missing promisor object")
	} else if !strings.Contains(err.Error(), "derive Phase 3 integrated tree") {
		t.Fatalf("unexpected missing-object error: %v", err)
	}
	after := gitObjectInventory(t, partial)
	if !maps.Equal(before, after) {
		t.Fatalf("governed derivation materialized objects or packs: before %#v after %#v", before, after)
	}

	// An ordinary Git lookup can fetch the same object, proving the promisor
	// remote was available when the governed lookup failed closed.
	runGit(t, partial, "cat-file", "-e", treeText+"^{tree}")
	if maps.Equal(after, gitObjectInventory(t, partial)) {
		t.Fatal("ordinary Git did not materialize the missing promisor object")
	}
}

func assertGitObjectMissingLocally(t *testing.T, repository, object string) {
	t.Helper()
	command := exec.Command("git", "cat-file", "-e", object+"^{tree}")
	command.Dir = repository
	command.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1", "LC_ALL=C")
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("partial-clone fixture unexpectedly contains tree %s", object)
	} else if len(output) == 0 {
		t.Fatalf("missing-object check returned no diagnostic: %v", err)
	}
}

func gitObjectInventory(t *testing.T, repository string) map[string][sha256.Size]byte {
	t.Helper()
	root := filepath.Join(repository, ".git", "objects")
	inventory := make(map[string][sha256.Size]byte)
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		inventory[relative] = sha256.Sum256(content)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return inventory
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

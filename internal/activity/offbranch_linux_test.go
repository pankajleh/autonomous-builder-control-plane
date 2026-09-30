//go:build linux

package activity

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
)

// offBranchService binds the run's worktree with one recorded checkpoint, as a
// run that has been implementing for a while.
func offBranchService(t *testing.T) (*bindingFixture, *Service, string, *time.Time, *bytes.Buffer) {
	t.Helper()
	f := newBindingFixture(t)
	now := testTime.Add(time.Hour)
	s, registration := worktreeService(t, f, &now, domain.StateImplementing)
	var diagnostics bytes.Buffer
	s.diagnostics = &diagnostics
	commitFile(t, f.worktree, "first")
	if refreshMarkers(t, s, f.run, registration) != 0 {
		t.Fatal("binding the worktree was marked")
	}
	if shas, err := s.recordedCheckpoints(f.run, registration); err != nil || len(shas) != 1 {
		t.Fatalf("fixture checkpoint = %v, %v", shas, err)
	}
	return f, s, registration, &now, &diagnostics
}

func commitFile(t *testing.T, worktree, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(worktree, "source"), []byte(content+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command(t, worktree, "add", "source")
	command(t, worktree, "commit", "-m", content)
}

func checkpointCount(t *testing.T, s *Service, run, registration string) int {
	t.Helper()
	shas, err := s.recordedCheckpoints(run, registration)
	if err != nil {
		t.Fatal(err)
	}
	return len(shas)
}

func TestWorktreeBrieflyOffBranchRecordsNoMarker(t *testing.T) {
	f, s, registration, now, diagnostics := offBranchService(t)
	command(t, f.worktree, "checkout", "--detach")
	// A commit made off the branch is never observed as a checkpoint.
	commitFile(t, f.worktree, "detached")
	for i := 0; i < 5; i++ {
		if refreshMarkers(t, s, f.run, registration) != 0 {
			t.Fatal("a worktree off its branch inside the window was marked")
		}
		*now = now.Add(30 * time.Second)
	}
	if checkpointCount(t, s, f.run, registration) != 1 {
		t.Fatal("a checkpoint was observed off the branch")
	}
	command(t, f.worktree, "checkout", "abcp/"+f.run)
	commitFile(t, f.worktree, "second")
	if refreshMarkers(t, s, f.run, registration) != 0 {
		t.Fatal("a worktree back on its branch with its history kept was marked")
	}
	if checkpointCount(t, s, f.run, registration) != 2 {
		t.Fatal("the checkpoint after returning was not observed")
	}
	lines := diagnostics.String()
	if strings.Count(lines, "what=worktree-off-branch") != 1 || strings.Count(lines, "what=worktree-back-on-branch") != 1 || strings.Contains(lines, "marker=") {
		t.Fatalf("diagnostics = %q", lines)
	}
}

func TestWorktreeHistoryRewrittenOffBranchRecordsOneMarker(t *testing.T) {
	f, s, registration, now, diagnostics := offBranchService(t)
	command(t, f.worktree, "checkout", "--detach")
	if refreshMarkers(t, s, f.run, registration) != 0 {
		t.Fatal("first observation off the branch was marked")
	}
	// Rebuild the branch from its base, dropping the recorded checkpoint.
	command(t, f.worktree, "reset", "--hard", f.scope.Base)
	commitFile(t, f.worktree, "rewritten")
	command(t, f.worktree, "checkout", "-B", "abcp/"+f.run)
	*now = now.Add(2 * time.Minute)
	if refreshMarkers(t, s, f.run, registration) != 1 {
		t.Fatal("a rewritten history was not marked once on return")
	}
	events := unknownEvents(t, s, f.run, registration)
	if events[0].Title != "Implementation history rewritten" {
		t.Fatalf("marker title = %q", events[0].Title)
	}
	if !strings.Contains(diagnostics.String(), "marker=history-rewritten step=history-rewritten class=integrity") {
		t.Fatalf("diagnostics = %q", diagnostics.String())
	}
	// The check runs once per excursion; later reads add no marker.
	*now = now.Add(time.Minute)
	if refreshMarkers(t, s, f.run, registration) != 1 {
		t.Fatal("a later read repeated the history marker")
	}
}

func TestWorktreeOffBranchPastTheWindowIsMarked(t *testing.T) {
	f, s, registration, now, _ := offBranchService(t)
	command(t, f.worktree, "checkout", "--detach")
	if refreshMarkers(t, s, f.run, registration) != 0 {
		t.Fatal("first observation off the branch was marked")
	}
	*now = now.Add(worktreeOffBranchWindow - time.Second)
	if refreshMarkers(t, s, f.run, registration) != 0 {
		t.Fatal("a worktree off its branch just inside the window was marked")
	}
	*now = now.Add(time.Minute + time.Second)
	if refreshMarkers(t, s, f.run, registration) != 1 {
		t.Fatal("a worktree off its branch for 11 minutes was not marked")
	}
}

func TestWorktreeOffBranchWithItsRefDeletedKeepsTheMissingRule(t *testing.T) {
	f, s, registration, now, _ := offBranchService(t)
	command(t, f.worktree, "checkout", "--detach")
	command(t, f.repo, "branch", "-D", "abcp/"+f.run)
	if refreshMarkers(t, s, f.run, registration) != 0 {
		t.Fatal("first observation of a deleted branch was marked")
	}
	*now = now.Add(worktreeTransitionWindow)
	if refreshMarkers(t, s, f.run, registration) != 1 {
		t.Fatal("a deleted branch was not marked after the 30-second window")
	}
}

func TestWorktreeRemovedAfterBindingKeepsTheMissingRule(t *testing.T) {
	f, s, registration, now, _ := offBranchService(t)
	command(t, f.repo, "worktree", "remove", "--force", f.worktree)
	if refreshMarkers(t, s, f.run, registration) != 0 {
		t.Fatal("first observation of a removed worktree was marked")
	}
	*now = now.Add(worktreeTransitionWindow)
	if refreshMarkers(t, s, f.run, registration) != 1 {
		t.Fatal("a removed worktree was not marked after the 30-second window")
	}
}

func TestWorktreeOnAnotherBranchIsOffBranch(t *testing.T) {
	f, s, registration, now, _ := offBranchService(t)
	command(t, f.worktree, "checkout", "-b", "look-around")
	for i := 0; i < 2; i++ {
		if refreshMarkers(t, s, f.run, registration) != 0 {
			t.Fatal("a worktree on another branch inside the window was marked")
		}
		*now = now.Add(5 * time.Minute)
	}
	command(t, f.worktree, "checkout", "abcp/"+f.run)
	if refreshMarkers(t, s, f.run, registration) != 0 {
		t.Fatal("a worktree back from another branch with its history kept was marked")
	}
}

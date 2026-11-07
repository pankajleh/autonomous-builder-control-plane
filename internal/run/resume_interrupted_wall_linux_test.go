//go:build linux

package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
)

// resumedWalledRalphex is freshWalledRalphex's Ralphex inside the wall for an interruption: its first start commits its
// work in progress, leaves a file uncommitted, says it is ready in the run's repository and runs until it is stopped;
// its second commits a candidate.
const resumedWalledRalphex = `#!/bin/sh
branch=
plan=
while [ "$#" -gt 0 ]; do
	case "$1" in
		--branch) branch=$2; shift 2 ;;
		--*) shift ;;
		*) plan=$1; shift ;;
	esac
done
[ -n "$branch" ] || exit 20
[ -r "$plan" ] || exit 21
root=$(git rev-parse --show-toplevel) || exit 24
rel=${plan#"$root"/}
worktree="$root/.ralphex/worktrees/$branch"
if [ -e "$worktree" ]; then
	echo "error: create worktree: worktree already exists at $worktree, another instance may be running" >&2
	exit 1
fi
mkdir -p "$root/.ralphex/worktrees"
grep -qx '.ralphex/' "$root/.git/info/exclude" 2>/dev/null || echo '.ralphex/' >> "$root/.git/info/exclude"
if git rev-parse --verify -q "refs/heads/$branch" > /dev/null; then
	git worktree add -q "$worktree" "$branch" || exit 26
else
	git worktree add -q -b "$branch" "$worktree" HEAD || exit 26
fi
mkdir -p "$worktree/$(dirname "$rel")" || exit 27
cp "$plan" "$worktree/$rel" || exit 28
cd "$worktree" || exit 29
git add "$rel" || exit 31
git commit -qm 'add execution plan' || exit 32
n=$(cat "$root/.ralphex/starts" 2>/dev/null || echo 0); n=$((n+1)); echo "$n" > "$root/.ralphex/starts"
if [ "$n" = 1 ]; then
	printf 'first\n' > attempt-1.txt
	git add attempt-1.txt && git commit -qm 'attempt 1: work in progress' || exit 33
	printf 'not committed\n' > uncommitted.txt
	printf ready > "$root/.ralphex/ready-1"
	while :; do sleep 1; done
fi
printf 'candidate\n' > candidate.txt
git add candidate.txt && git commit -qm 'attempt 2: candidate' || exit 34
`

// R6's resume of a walled run: a SIGTERM while its builder runs inside the wall stops it at RECOVERY_REQUIRED; the
// resume finds the run's folder, removes the interrupted attempt's worktree inside the wall, Ralphex adds it again from
// the branch and commits the resumed attempt's own plan copy, and the branch comes back into the product repository with
// both attempts' commits and is accepted.
func TestAResumedWalledRunAfterASigtermRemovesItsWorktreeInsideItsWallAndIsAccepted(t *testing.T) {
	bwrap := requireBwrap(t)
	fixture, wall, _, _ := walledFixture(t, bwrap, func(wallDirs, string) string { return resumedWalledRalphex })
	manifest := fixture.authority.Manifest()
	manifest.Ralphex.SourceSHA, manifest.Ralphex.FreshAttempts, manifest.Ralphex.Timeout = ralphex.TaskPromptSourceSHA, 1, "1m"
	fixture.authority = fixture.admit(t, manifest)
	repository := filepath.Join(wall.RunFolder(manifest.RunID), "repo")
	first := fixture.runner(t)
	if err := first.SetBuildWall(wall); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	done := make(chan runResponse, 1)
	go func() {
		result, err := first.Run(ctx)
		done <- runResponse{result, err}
	}()
	waitForRunFile(t, filepath.Join(repository, ".ralphex", "ready-1"))
	cancel(sigterm())
	got := <-done
	if got.err != nil || got.result.State != domain.StateRecoveryRequired {
		t.Fatalf("interrupted walled run = %s %q, %v", got.result.State, got.result.FailureReason, got.err)
	}
	worktree := filepath.Join(repository, ".ralphex", "worktrees", "abcp", "run-fixture")
	if _, err := os.Stat(filepath.Join(worktree, "uncommitted.txt")); err != nil {
		t.Fatalf("the interrupted attempt's worktree: %v", err)
	}

	resumed := fixture.runner(t)
	if err := resumed.SetBuildWall(wall); err != nil {
		t.Fatal(err)
	}
	result, err := resumed.RunResumeInterrupted(context.Background())
	if err != nil || !result.Accepted() {
		stderr, _ := os.ReadFile(result.Ralphex.StderrRef.URI)
		t.Fatalf("resumed walled run = %s %q, %v\nstderr:\n%s", result.State, result.FailureReason, err, stderr)
	}
	events := readEvents(t, fixture.ledgerPath)
	restart := transitionTo(t, events, domain.StateRecoveryRequired, domain.StateExecutionStarting)
	if restart.Payload["attempt"] != float64(2) || restart.Payload["resume_after"] != "INTERRUPTED" || restart.Payload["retained_worktree_removed"] != true {
		t.Fatalf("the resumed attempt's start = %v", restart.Payload)
	}
	product := fixture.authority.Repository().Path
	log := runGit(t, product, "log", "--format=%s", "abcp/run-fixture")
	if !strings.Contains(log, "attempt 1: work in progress") || !strings.Contains(log, "attempt 2: candidate") {
		t.Fatalf("the product repository's branch:\n%s", log)
	}
	if _, err := os.Stat(filepath.Join(worktree, "uncommitted.txt")); !os.IsNotExist(err) {
		t.Fatalf("the interrupted attempt's uncommitted file outlived its worktree: %v", err)
	}
	sum := sha256.Sum256([]byte(manifest.RunID))
	resumedPlan := ralphex.ExecutionPlanHandoffPrefixV1 + hex.EncodeToString(sum[:]) + "-fresh-2/plan.md"
	if _, err := os.Stat(filepath.Join(worktree, resumedPlan)); err != nil {
		t.Fatalf("the resumed attempt's plan copy in its worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.evidence, manifest.RunID, "fresh-attempt-worktree-1-stdout-fresh-2.log")); err != nil {
		t.Fatalf("the worktree's removal inside the wall left no evidence: %v", err)
	}
}

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

// freshWalledRalphex keeps the pinned Ralphex's rules inside the wall: it refuses to start while the branch's worktree
// is there, adds it from the branch when the branch exists, copies the plan into it and commits it (refusing, as
// Ralphex's "git commit" does, when there is nothing to commit), and keeps the worktree. Its first start fails as the
// Thali change did; its second commits a candidate.
const freshWalledRalphex = `#!/bin/sh
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
	printf '[26-11-07 10:00:00] validation started: unit\n'
	printf '[26-11-07 10:00:05] deterministic validation failed; returning evidence to model for one repair turn: validation unit exit 1: not ok 2 - totals\n'
	printf '[26-11-07 10:01:00] task failed, retrying...\n'
	echo 'error: runner: task phase: task execution failed after retry (FAILED signal received)' >&2
	exit 1
fi
printf 'candidate\n' > candidate.txt
git add candidate.txt && git commit -qm 'attempt 2: candidate' || exit 34
`

// A walled run's fresh attempt: the first attempt's worktree is removed inside the wall, Ralphex adds it again from the
// branch and commits the attempt's own plan copy, and the branch comes back into the product repository with both
// attempts' commits and is accepted.
func TestAWalledRunGetsAFreshAttemptAfterItsWorktreeIsRemovedInsideItsWall(t *testing.T) {
	bwrap := requireBwrap(t)
	fixture, wall, _, _ := walledFixture(t, bwrap, func(wallDirs, string) string { return freshWalledRalphex })
	manifest := fixture.authority.Manifest()
	manifest.Ralphex.SourceSHA, manifest.Ralphex.FreshAttempts = ralphex.TaskPromptSourceSHA, 1
	fixture.authority = fixture.admit(t, manifest)
	runner := fixture.runner(t)
	if err := runner.SetBuildWall(wall); err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background())
	if err != nil || !result.Accepted() {
		stderr, _ := os.ReadFile(result.Ralphex.StderrRef.URI)
		t.Fatalf("walled fresh attempt = %s %q, %v\nstderr:\n%s", result.State, result.FailureReason, err, stderr)
	}
	retrying := 0
	for _, event := range readEvents(t, fixture.ledgerPath) {
		if event.StateTo == domain.StateRetrying {
			retrying++
		}
	}
	if retrying != 1 {
		t.Fatalf("RETRYING %d times", retrying)
	}
	product := fixture.authority.Repository().Path
	log := runGit(t, product, "log", "--format=%s", "abcp/run-fixture")
	if !strings.Contains(log, "attempt 1: work in progress") || !strings.Contains(log, "attempt 2: candidate") {
		t.Fatalf("the product repository's branch:\n%s", log)
	}
	sum := sha256.Sum256([]byte(manifest.RunID))
	fresh := ralphex.ExecutionPlanHandoffPrefixV1 + hex.EncodeToString(sum[:]) + "-fresh-2/plan.md"
	if _, err := os.Stat(filepath.Join(wall.RunFolder(manifest.RunID), "repo", ".ralphex", "worktrees", "abcp", "run-fixture", fresh)); err != nil {
		t.Fatalf("the fresh attempt's plan copy in its worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.evidence, manifest.RunID, "fresh-attempt-worktree-1-stdout-fresh-2.log")); err != nil {
		t.Fatalf("the worktree's removal inside the wall left no evidence: %v", err)
	}
}

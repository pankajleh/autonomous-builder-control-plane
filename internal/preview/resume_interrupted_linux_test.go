//go:build linux

package preview

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/activity"
)

// After a resume (Repo B docs/plans/build-recovery.md §6.2) the run's activity holds the interrupted attempt's
// checkpoint, the provider gap a restarted progress numbering leaves, and the resumed attempt's checkpoint on the same
// branch, its worktree removed (its uncommitted work with it) and added again in between: both checkpoints resolve, so
// the accepted version opens with Try it, and so does the interrupted attempt's.
func TestAResumedRunsCheckpointsBothResolve(t *testing.T) {
	f := newBindingFixture(t)
	first := f.events.page.Events[0]
	os.WriteFile(filepath.Join(f.worktree, "uncommitted"), []byte("lost with the worktree\n"), 0600)
	command(t, f.repo, "worktree", "remove", "--force", f.worktree)
	command(t, f.repo, "worktree", "add", f.worktree, "abcp/"+f.run)
	os.WriteFile(filepath.Join(f.worktree, "resumed"), []byte("the resumed attempt\n"), 0600)
	command(t, f.worktree, "add", "resumed")
	command(t, f.worktree, "commit", "-m", "resumed attempt")
	sha := command(t, f.worktree, "rev-parse", "HEAD")
	gap := activity.Event{SchemaVersion: "ActivityEventV1", RunID: f.run, Ordinal: 2, ActivityID: strings.Repeat("e", 64), AuthorityLevel: "PROVIDER_DETAIL",
		Category: "WARNING", Status: "UNKNOWN", Title: activity.ProviderGapTitle, SourceKind: "RALPHEX_PROGRESS"}
	second := activity.Event{SchemaVersion: "ActivityEventV1", Ordinal: 3, ActivityID: jsonDigest([]string{f.run, "checkpoint", sha}), RunID: f.run, AuthorityLevel: "ABCP_STATE",
		Category: "CHECKPOINT", Status: "AVAILABLE", SourceKind: "ABCP_CHECKPOINT_OBSERVER", SourceDigest: jsonDigest([]string{f.run, sha}), CheckpointSHA: sha, CheckpointClean: true}
	f.events.page.Events = []activity.Event{first, gap, second}
	for _, checkpoint := range []activity.Event{second, first} {
		source, err := f.resolver().Resolve(context.Background(), f.run, checkpoint.ActivityID)
		if err != nil || source.SHA != checkpoint.CheckpointSHA {
			t.Fatalf("checkpoint %s: %+v, %v", checkpoint.CheckpointSHA, source, err)
		}
	}
}

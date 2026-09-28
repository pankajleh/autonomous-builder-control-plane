//go:build linux

package preview

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/eviction"
)

// evict reproduces a controller eviction: pin the checkpoint, seal the record,
// then remove the governed worktree.
func (f *bindingFixture) evict(t *testing.T, change func(*eviction.RecordV1)) (*eviction.Store, string) {
	t.Helper()
	sha := f.events.page.Events[0].CheckpointSHA
	ref := eviction.CheckpointRef(f.run, sha)
	command(t, f.repo, "update-ref", ref, sha)
	store, err := eviction.OpenStore(f.root)
	if err != nil {
		t.Fatal(err)
	}
	record := eviction.RecordV1{Kind: "WorktreeEvictionV1", SchemaVersion: 1, RunID: f.run, AuthorityDigest: f.binding.AuthorityDigest,
		Repository: f.repo, Branch: f.manifest.Worktree.Branch, Base: f.manifest.Repository.StartSHA, Worktree: f.worktree,
		Reason: eviction.ReasonIdle, EvictedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Checkpoints: []eviction.PinnedCheckpoint{{SHA: sha, Ref: ref}}}
	if change != nil {
		change(&record)
	}
	if err = store.Write(record); err != nil {
		t.Fatal(err)
	}
	command(t, f.repo, "worktree", "remove", "--force", f.worktree)
	return store, ref
}

func TestEvictedRunPreviewsThroughPinnedCheckpointWithoutBranch(t *testing.T) {
	f := newBindingFixture(t)
	store, ref := f.evict(t, nil)
	resolver := f.resolver()
	checkpoint := f.events.page.Events[0]
	if _, err := resolver.Resolve(context.Background(), f.run, checkpoint.ActivityID); err == nil {
		t.Fatal("evicted run resolved without consulting its eviction record")
	}
	resolver.Evictions = store
	// Even the run's branch may be deleted: the pinned ref keeps the checkpoint.
	command(t, f.repo, "branch", "-D", f.manifest.Worktree.Branch)
	command(t, f.repo, "gc", "--quiet", "--prune=now")
	source, err := resolver.Resolve(context.Background(), f.run, checkpoint.ActivityID)
	if err != nil || source.FetchRef != ref || source.SHA != checkpoint.CheckpointSHA {
		t.Fatalf("evicted source = %+v, %v", source, err)
	}
	checkout := Checkout{filepath.Join(t.TempDir(), "sources")}
	path, err := checkout.Materialize(context.Background(), strings.Repeat("e", 64), source)
	if err != nil {
		t.Fatal(err)
	}
	if command(t, path, "rev-parse", "HEAD") != source.SHA {
		t.Fatal("not the pinned checkpoint")
	}
	if data, _ := os.ReadFile(filepath.Join(path, "source")); string(data) != "candidate\n" {
		t.Fatal("wrong checkpoint content", string(data))
	}
	if _, err = git(context.Background(), path, "symbolic-ref", "--quiet", "HEAD"); err == nil {
		t.Fatal("checkout not detached")
	}
	if err := filepath.WalkDir(filepath.Join(path, ".git"), func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(name)
		if err == nil && bytes.Contains(data, []byte(f.repo)) {
			t.Errorf("source metadata retains controller path: %s", name)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	foreign := source
	foreign.FetchRef = "refs/heads/main"
	if _, err = checkout.Materialize(context.Background(), strings.Repeat("f", 64), foreign); err == nil {
		t.Fatal("a fetch ref outside the run's pinned checkpoints was used")
	}
}

func TestEvictedRunRejectsUnsealedOrMismatchedPins(t *testing.T) {
	cases := map[string]struct {
		record func(*eviction.RecordV1)
		after  func(*testing.T, *bindingFixture, string)
	}{
		"record-base": {record: func(r *eviction.RecordV1) { r.Base = strings.Repeat("0", 40) }},
		"record-authority": {record: func(r *eviction.RecordV1) {
			r.AuthorityDigest = strings.Repeat("f", 64)
		}},
		"unpinned-checkpoint": {record: func(r *eviction.RecordV1) { r.Checkpoints = []eviction.PinnedCheckpoint{} }},
		"moved-ref": {after: func(t *testing.T, f *bindingFixture, ref string) {
			command(t, f.repo, "update-ref", ref, f.manifest.Repository.StartSHA)
		}},
		"deleted-ref": {after: func(t *testing.T, f *bindingFixture, ref string) {
			command(t, f.repo, "update-ref", "-d", ref)
		}},
		"unsafe-record": {after: func(t *testing.T, f *bindingFixture, _ string) {
			os.Chmod(filepath.Join(f.root, "evictions", f.run+".json"), 0o644)
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newBindingFixture(t)
			store, ref := f.evict(t, tc.record)
			if tc.after != nil {
				tc.after(t, f, ref)
			}
			resolver := f.resolver()
			resolver.Evictions = store
			if source, err := resolver.Resolve(context.Background(), f.run, f.events.page.Events[0].ActivityID); err == nil {
				t.Fatalf("unsafe evicted source accepted: %+v", source)
			}
		})
	}
}

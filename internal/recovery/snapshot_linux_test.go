//go:build linux

package recovery

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

func TestSnapshotPreservesDirtyStateAndProgress(t *testing.T) {
	fixture := newWorktreeFixture(t)
	ownership := deadOwnership(t, fixture)
	writeTestRecoveryFile(t, filepath.Join(fixture.worktree, "README.md"), []byte("fixture\ndirty recovery work\n"))
	progressRoot := filepath.Join(t.TempDir(), "progress")
	if err := os.Mkdir(progressRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	progressPath := filepath.Join(progressRoot, "attempt.log")
	progressContents := []byte("task 2 was in progress\n")
	writeTestRecoveryFile(t, progressPath, progressContents)

	store, err := evidence.NewStore(t.TempDir(), ownership.Attempt.RunID)
	if err != nil {
		t.Fatal(err)
	}
	snapshotter, err := NewSnapshotter(store)
	if err != nil {
		t.Fatal(err)
	}
	capturedAt := time.Date(2026, 9, 6, 12, 30, 0, 0, time.UTC)
	snapshotter.now = func() time.Time { return capturedAt }

	published, err := snapshotter.Capture(context.Background(), SnapshotRequest{
		SnapshotID:    "attempt-1-terminal",
		Ownership:     ownership,
		ProgressRoot:  progressRoot,
		ProgressPaths: []string{progressPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata := published.Metadata
	if metadata.Attempt != ownership.Attempt || metadata.Repository != fixture.repository || metadata.Worktree != fixture.worktree || metadata.Branch != fixture.branch {
		t.Fatalf("snapshot identity = %#v", metadata)
	}
	if metadata.HeadSHA != strings.TrimSpace(gitRecoveryOutput(t, fixture.worktree, "rev-parse", "HEAD")) {
		t.Fatalf("snapshot HEAD = %q", metadata.HeadSHA)
	}
	if !metadata.Dirty || metadata.OwnerProof.Classification != ClassificationStaleOwnerDead {
		t.Fatalf("snapshot dirty/proof = (%t, %#v)", metadata.Dirty, metadata.OwnerProof)
	}
	if metadata.CapturedAt != capturedAt {
		t.Fatalf("captured at = %s, want %s", metadata.CapturedAt, capturedAt)
	}

	status := readVerifiedRecoveryArtifact(t, metadata.Status.Ref)
	if !bytes.Contains(status, []byte("README.md")) {
		t.Fatalf("status artifact does not identify dirty file: %q", status)
	}
	diff := readVerifiedRecoveryArtifact(t, metadata.Diff.Ref)
	if !bytes.Contains(diff, []byte("+dirty recovery work")) {
		t.Fatalf("diff artifact does not preserve dirty work: %q", diff)
	}
	progress := readVerifiedRecoveryArtifact(t, metadata.Progress.Ref)
	reader := tar.NewReader(bytes.NewReader(progress))
	header, err := reader.Next()
	if err != nil {
		t.Fatal(err)
	}
	archived, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if header.Name != "attempt.log" || !bytes.Equal(archived, progressContents) {
		t.Fatalf("progress archive = (%q, %q)", header.Name, archived)
	}
	if len(metadata.ProgressFiles) != 1 || metadata.ProgressFiles[0].Path != "attempt.log" || metadata.ProgressFiles[0].Size != int64(len(progressContents)) {
		t.Fatalf("progress metadata = %#v", metadata.ProgressFiles)
	}

	metadataBytes := readVerifiedRecoveryArtifact(t, published.MetadataRef)
	var stored SnapshotMetadata
	if err := json.Unmarshal(metadataBytes, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.SnapshotID != metadata.SnapshotID || stored.Status.Ref != metadata.Status.Ref || stored.Diff.Ref != metadata.Diff.Ref || stored.Progress.Ref != metadata.Progress.Ref {
		t.Fatalf("stored metadata does not bind artifact references: %#v", stored)
	}
}

func TestSnapshotCleanStatePublishesEmptyStatusAndDiff(t *testing.T) {
	fixture := newWorktreeFixture(t)
	ownership := deadOwnership(t, fixture)
	store, err := evidence.NewStore(t.TempDir(), ownership.Attempt.RunID)
	if err != nil {
		t.Fatal(err)
	}
	snapshotter, err := NewSnapshotter(store)
	if err != nil {
		t.Fatal(err)
	}

	published, err := snapshotter.Capture(context.Background(), SnapshotRequest{
		SnapshotID: "clean-state",
		Ownership:  ownership,
	})
	if err != nil {
		t.Fatal(err)
	}
	if published.Metadata.Dirty {
		t.Fatal("clean worktree recorded as dirty")
	}
	if status := readVerifiedRecoveryArtifact(t, published.Metadata.Status.Ref); len(status) != 0 {
		t.Fatalf("clean status = %q", status)
	}
	if diff := readVerifiedRecoveryArtifact(t, published.Metadata.Diff.Ref); len(diff) != 0 {
		t.Fatalf("clean diff = %q", diff)
	}
	if progress := readVerifiedRecoveryArtifact(t, published.Metadata.Progress.Ref); len(progress) != 0 {
		t.Fatalf("absent progress = %q", progress)
	}
	for name, artifact := range map[string]ArtifactMetadata{
		"status":   published.Metadata.Status,
		"diff":     published.Metadata.Diff,
		"progress": published.Metadata.Progress,
	} {
		if artifact.SourceBytes != 0 || artifact.CapturedBytes != 0 || artifact.Truncated {
			t.Fatalf("clean %s metadata = %#v", name, artifact)
		}
	}
}

func TestSnapshotArtifactsAreHashAddressedAndImmutable(t *testing.T) {
	fixture := newWorktreeFixture(t)
	ownership := deadOwnership(t, fixture)
	writeTestRecoveryFile(t, filepath.Join(fixture.worktree, "README.md"), []byte("immutable snapshot\n"))
	store, err := evidence.NewStore(t.TempDir(), ownership.Attempt.RunID)
	if err != nil {
		t.Fatal(err)
	}
	snapshotter, err := NewSnapshotter(store)
	if err != nil {
		t.Fatal(err)
	}
	request := SnapshotRequest{SnapshotID: "immutable", Ownership: ownership}
	first, err := snapshotter.Capture(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	refs := []ledger.EvidenceRef{first.Metadata.Status.Ref, first.Metadata.Diff.Ref, first.Metadata.Progress.Ref, first.MetadataRef}
	before := make(map[string][]byte, len(refs))
	for _, ref := range refs {
		before[ref.URI] = readVerifiedRecoveryArtifact(t, ref)
	}

	writeTestRecoveryFile(t, filepath.Join(fixture.worktree, "README.md"), []byte("replacement state\n"))
	if _, err := snapshotter.Capture(context.Background(), request); !errors.Is(err, evidence.ErrArtifactExists) {
		t.Fatalf("second snapshot error = %v, want immutable artifact collision", err)
	}
	for _, ref := range refs {
		after, err := os.ReadFile(ref.URI)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(after, before[ref.URI]) {
			t.Fatalf("immutable artifact %q changed", ref.URI)
		}
	}
}

func TestSnapshotBoundsArtifactsAndRecordsTruncation(t *testing.T) {
	fixture := newWorktreeFixture(t)
	ownership := deadOwnership(t, fixture)
	writeTestRecoveryFile(t, filepath.Join(fixture.worktree, "README.md"), bytes.Repeat([]byte("large dirty line\n"), 128))
	for index := 0; index < 12; index++ {
		writeTestRecoveryFile(t, filepath.Join(fixture.worktree, strings.Repeat("x", 20)+string(rune('a'+index))+".txt"), []byte("untracked\n"))
	}
	progressRoot := filepath.Join(t.TempDir(), "progress")
	if err := os.Mkdir(progressRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	progressPath := filepath.Join(progressRoot, "large.log")
	writeTestRecoveryFile(t, progressPath, bytes.Repeat([]byte("progress data\n"), 128))
	store, err := evidence.NewStore(t.TempDir(), ownership.Attempt.RunID)
	if err != nil {
		t.Fatal(err)
	}
	snapshotter, err := NewSnapshotter(store)
	if err != nil {
		t.Fatal(err)
	}
	limits := SnapshotLimits{StatusBytes: 32, DiffBytes: 64, ProgressBytes: 96}

	published, err := snapshotter.Capture(context.Background(), SnapshotRequest{
		SnapshotID:    "bounded",
		Ownership:     ownership,
		ProgressRoot:  progressRoot,
		ProgressPaths: []string{progressPath},
		Limits:        limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, check := range map[string]struct {
		metadata ArtifactMetadata
		limit    int64
	}{
		"status":   {published.Metadata.Status, limits.StatusBytes},
		"diff":     {published.Metadata.Diff, limits.DiffBytes},
		"progress": {published.Metadata.Progress, limits.ProgressBytes},
	} {
		if !check.metadata.Truncated || check.metadata.SourceBytes <= check.limit || check.metadata.CapturedBytes != check.limit {
			t.Fatalf("%s truncation metadata = %#v, limit %d", name, check.metadata, check.limit)
		}
		artifact := readVerifiedRecoveryArtifact(t, check.metadata.Ref)
		if int64(len(artifact)) != check.limit {
			t.Fatalf("%s artifact size = %d, want %d", name, len(artifact), check.limit)
		}
	}
}

func TestSnapshotPublicationFailurePreventsProtectedAction(t *testing.T) {
	fixture := newWorktreeFixture(t)
	ownership := deadOwnership(t, fixture)
	store, err := evidence.NewStore(t.TempDir(), ownership.Attempt.RunID)
	if err != nil {
		t.Fatal(err)
	}
	snapshotter, err := NewSnapshotter(failingRecoveryWriter{delegate: store, failKind: "recovery-snapshot-metadata"})
	if err != nil {
		t.Fatal(err)
	}
	actionCalled := false

	_, err = snapshotter.CaptureBeforeAction(context.Background(), SnapshotRequest{
		SnapshotID: "publication-failure",
		Ownership:  ownership,
	}, func(PublishedSnapshot) error {
		actionCalled = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "injected publication failure") {
		t.Fatalf("CaptureBeforeAction error = %v", err)
	}
	if actionCalled {
		t.Fatal("protected cleanup/restart action ran without published snapshot metadata")
	}
}

func TestSnapshotRefusesLiveOwnerBeforePublishing(t *testing.T) {
	fixture := newWorktreeFixture(t)
	ownership := fixture.ownership(t, os.Getpid())
	store, err := evidence.NewStore(t.TempDir(), ownership.Attempt.RunID)
	if err != nil {
		t.Fatal(err)
	}
	snapshotter, err := NewSnapshotter(store)
	if err != nil {
		t.Fatal(err)
	}
	actionCalled := false

	_, err = snapshotter.CaptureBeforeAction(context.Background(), SnapshotRequest{
		SnapshotID: "live-owner",
		Ownership:  ownership,
	}, func(PublishedSnapshot) error {
		actionCalled = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "positive owner-dead proof") {
		t.Fatalf("live-owner snapshot error = %v", err)
	}
	if actionCalled {
		t.Fatal("protected action ran while the governed owner was live")
	}
	entries, err := os.ReadDir(store.RunDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("live-owner refusal published %d artifacts", len(entries))
	}
}

type failingRecoveryWriter struct {
	delegate ArtifactWriter
	failKind string
}

func (writer failingRecoveryWriter) WriteBytes(name, kind string, data []byte) (ledger.EvidenceRef, error) {
	if kind == writer.failKind {
		return ledger.EvidenceRef{}, errors.New("injected publication failure")
	}
	return writer.delegate.WriteBytes(name, kind, data)
}

func deadOwnership(t *testing.T, fixture worktreeFixture) Ownership {
	t.Helper()
	process := startRecoveryHelper(t)
	ownership := fixture.ownership(t, process.Process.Pid)
	if err := process.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err == nil {
		t.Fatal("killed recovery owner exited successfully")
	}
	return ownership
}

func writeTestRecoveryFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitRecoveryOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := captureCommand(t, directory, arguments...)
	return string(command)
}

func captureCommand(t *testing.T, directory string, arguments ...string) []byte {
	t.Helper()
	capture, err := captureGitOutput(context.Background(), directory, 1<<20, arguments...)
	if err != nil {
		t.Fatal(err)
	}
	return capture.data
}

func readVerifiedRecoveryArtifact(t *testing.T, ref ledger.EvidenceRef) []byte {
	t.Helper()
	data, err := os.ReadFile(ref.URI)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if got := hex.EncodeToString(digest[:]); got != ref.SHA256 {
		t.Fatalf("artifact %q SHA256 = %s, want %s", ref.URI, got, ref.SHA256)
	}
	return data
}

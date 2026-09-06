package recovery

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

const (
	defaultStatusLimit   int64 = 1 << 20
	defaultDiffLimit     int64 = 8 << 20
	defaultProgressLimit int64 = 2 << 20
	defaultMetadataLimit int64 = 1 << 20
	maximumArtifactLimit int64 = 64 << 20
)

// ArtifactWriter publishes immutable, content-addressed evidence. The
// evidence.Store implementation provides atomic create-if-absent semantics.
type ArtifactWriter interface {
	WriteBytes(name, kind string, data []byte) (ledger.EvidenceRef, error)
}

// SnapshotLimits bounds every recovery artifact held in memory or published.
// A zero value selects the conservative default for that artifact.
type SnapshotLimits struct {
	StatusBytes   int64 `json:"status_bytes"`
	DiffBytes     int64 `json:"diff_bytes"`
	ProgressBytes int64 `json:"progress_bytes"`
	MetadataBytes int64 `json:"metadata_bytes"`
}

// SnapshotRequest identifies one immutable snapshot. ProgressRoot is the
// controller-authorized Ralphex progress directory; every ProgressPath must
// be a regular file contained beneath it.
type SnapshotRequest struct {
	SnapshotID    string
	Ownership     Ownership
	ProgressRoot  string
	ProgressPaths []string
	Limits        SnapshotLimits
}

// ArtifactMetadata records both the bytes available at the source and the
// bounded bytes actually published. Truncated is always explicit.
type ArtifactMetadata struct {
	Ref           ledger.EvidenceRef `json:"ref"`
	SourceBytes   int64              `json:"source_bytes"`
	CapturedBytes int64              `json:"captured_bytes"`
	Truncated     bool               `json:"truncated"`
}

// ProgressFileMetadata preserves the identity and filesystem metadata of one
// Ralphex progress file represented in the bounded progress archive.
type ProgressFileMetadata struct {
	Path          string    `json:"path"`
	Size          int64     `json:"size"`
	Mode          uint32    `json:"mode"`
	ModifiedAt    time.Time `json:"modified_at"`
	ArchiveOffset int64     `json:"archive_offset"`
}

// SnapshotMetadata is published last. Its existence proves that every
// referenced artifact was durably published before a cleanup/restart action.
type SnapshotMetadata struct {
	SchemaVersion int                    `json:"schema_version"`
	SnapshotID    string                 `json:"snapshot_id"`
	CapturedAt    time.Time              `json:"captured_at"`
	Attempt       AttemptIdentity        `json:"attempt"`
	Repository    string                 `json:"repository"`
	Worktree      string                 `json:"worktree"`
	Branch        string                 `json:"branch"`
	HeadSHA       string                 `json:"head_sha"`
	Dirty         bool                   `json:"dirty"`
	OwnerProof    Inspection             `json:"owner_proof"`
	Status        ArtifactMetadata       `json:"status"`
	Diff          ArtifactMetadata       `json:"diff"`
	Progress      ArtifactMetadata       `json:"progress"`
	ProgressFiles []ProgressFileMetadata `json:"progress_files,omitempty"`
}

// PublishedSnapshot is the proof token passed to a protected action only
// after all snapshot artifacts, including the final metadata, are immutable.
type PublishedSnapshot struct {
	Metadata    SnapshotMetadata
	MetadataRef ledger.EvidenceRef
}

// Snapshotter captures and publishes pre-cleanup recovery evidence.
type Snapshotter struct {
	artifacts ArtifactWriter
	now       func() time.Time
}

// NewSnapshotter constructs a snapshot publisher.
func NewSnapshotter(artifacts ArtifactWriter) (*Snapshotter, error) {
	if artifacts == nil {
		return nil, errors.New("recovery snapshot artifact writer is required")
	}
	return &Snapshotter{artifacts: artifacts, now: time.Now}, nil
}

// Capture publishes a bounded snapshot after independently proving that the
// governed owner is dead. The metadata artifact is published last.
func (s *Snapshotter) Capture(ctx context.Context, request SnapshotRequest) (PublishedSnapshot, error) {
	if s == nil || s.artifacts == nil {
		return PublishedSnapshot{}, errors.New("recovery snapshotter is not configured")
	}
	if ctx == nil {
		return PublishedSnapshot{}, errors.New("context is required")
	}
	if err := validateSnapshotID(request.SnapshotID); err != nil {
		return PublishedSnapshot{}, err
	}
	limits, err := normalizeSnapshotLimits(request.Limits)
	if err != nil {
		return PublishedSnapshot{}, err
	}

	inspection := Inspect(ctx, request.Ownership)
	if inspection.Classification != ClassificationStaleOwnerDead ||
		(inspection.OwnerProof != OwnerProofProcessAbsent && inspection.OwnerProof != OwnerProofIdentityMismatch && inspection.OwnerProof != OwnerProofProcessExited) {
		return PublishedSnapshot{}, fmt.Errorf("recovery snapshot requires positive owner-dead proof: classification=%s proof=%s", inspection.Classification, inspection.OwnerProof)
	}

	headBefore, err := captureGitOutput(ctx, inspection.Ownership.Worktree.Path, 4096, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return PublishedSnapshot{}, fmt.Errorf("capture recovery HEAD: %w", err)
	}
	if headBefore.truncated {
		return PublishedSnapshot{}, errors.New("capture recovery HEAD: output exceeded safety bound")
	}
	headSHA := strings.TrimSpace(string(headBefore.data))
	if !validGitObjectID(headSHA) {
		return PublishedSnapshot{}, fmt.Errorf("capture recovery HEAD: invalid Git object ID %q", headSHA)
	}

	status, err := captureGitOutput(ctx, inspection.Ownership.Worktree.Path, limits.StatusBytes,
		"status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return PublishedSnapshot{}, fmt.Errorf("capture recovery status: %w", err)
	}
	diff, err := captureGitOutput(ctx, inspection.Ownership.Worktree.Path, limits.DiffBytes,
		"diff", "--binary", "--no-ext-diff", "--no-textconv", "HEAD", "--")
	if err != nil {
		return PublishedSnapshot{}, fmt.Errorf("capture recovery diff: %w", err)
	}
	progress, progressFiles, err := captureProgress(request.ProgressRoot, request.ProgressPaths, limits.ProgressBytes)
	if err != nil {
		return PublishedSnapshot{}, fmt.Errorf("capture Ralphex progress: %w", err)
	}

	headAfter, err := captureGitOutput(ctx, inspection.Ownership.Worktree.Path, 4096, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return PublishedSnapshot{}, fmt.Errorf("verify recovery HEAD: %w", err)
	}
	if headAfter.truncated || strings.TrimSpace(string(headAfter.data)) != headSHA {
		return PublishedSnapshot{}, errors.New("governed worktree HEAD changed while the recovery snapshot was captured")
	}

	prefix := "recovery-" + request.SnapshotID
	statusRef, err := s.artifacts.WriteBytes(prefix+"-status.txt", "recovery-git-status", status.data)
	if err != nil {
		return PublishedSnapshot{}, fmt.Errorf("publish recovery status: %w", err)
	}
	if err := validatePublishedRef(statusRef, "recovery-git-status"); err != nil {
		return PublishedSnapshot{}, fmt.Errorf("publish recovery status: %w", err)
	}
	diffRef, err := s.artifacts.WriteBytes(prefix+"-diff.patch", "recovery-git-diff", diff.data)
	if err != nil {
		return PublishedSnapshot{}, fmt.Errorf("publish recovery diff: %w", err)
	}
	if err := validatePublishedRef(diffRef, "recovery-git-diff"); err != nil {
		return PublishedSnapshot{}, fmt.Errorf("publish recovery diff: %w", err)
	}
	progressRef, err := s.artifacts.WriteBytes(prefix+"-progress.tar", "recovery-ralphex-progress", progress.data)
	if err != nil {
		return PublishedSnapshot{}, fmt.Errorf("publish Ralphex progress: %w", err)
	}
	if err := validatePublishedRef(progressRef, "recovery-ralphex-progress"); err != nil {
		return PublishedSnapshot{}, fmt.Errorf("publish Ralphex progress: %w", err)
	}

	metadata := SnapshotMetadata{
		SchemaVersion: 1,
		SnapshotID:    request.SnapshotID,
		CapturedAt:    s.now().UTC(),
		Attempt:       inspection.Ownership.Attempt,
		Repository:    inspection.Ownership.Worktree.RepositoryPath,
		Worktree:      inspection.Ownership.Worktree.Path,
		Branch:        inspection.Ownership.Worktree.Branch,
		HeadSHA:       headSHA,
		Dirty:         status.total > 0,
		OwnerProof:    inspection,
		Status:        artifactMetadata(statusRef, status),
		Diff:          artifactMetadata(diffRef, diff),
		Progress:      artifactMetadata(progressRef, progress),
		ProgressFiles: progressFiles,
	}
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		return PublishedSnapshot{}, fmt.Errorf("marshal recovery snapshot metadata: %w", err)
	}
	if int64(len(metadataBytes)) > limits.MetadataBytes {
		return PublishedSnapshot{}, fmt.Errorf("recovery snapshot metadata is %d bytes, exceeds %d-byte bound", len(metadataBytes), limits.MetadataBytes)
	}
	metadataRef, err := s.artifacts.WriteBytes(prefix+"-metadata.json", "recovery-snapshot-metadata", metadataBytes)
	if err != nil {
		return PublishedSnapshot{}, fmt.Errorf("publish recovery snapshot metadata: %w", err)
	}
	if err := validatePublishedRef(metadataRef, "recovery-snapshot-metadata"); err != nil {
		return PublishedSnapshot{}, fmt.Errorf("publish recovery snapshot metadata: %w", err)
	}
	return PublishedSnapshot{Metadata: metadata, MetadataRef: metadataRef}, nil
}

// CaptureBeforeAction invokes action only after Capture returns a complete,
// immutable snapshot. Cleanup and restart orchestration must enter through
// this gate rather than acting on partially published evidence.
func (s *Snapshotter) CaptureBeforeAction(ctx context.Context, request SnapshotRequest, action func(PublishedSnapshot) error) (PublishedSnapshot, error) {
	if action == nil {
		return PublishedSnapshot{}, errors.New("protected recovery action is required")
	}
	snapshot, err := s.Capture(ctx, request)
	if err != nil {
		return PublishedSnapshot{}, err
	}
	if err := action(snapshot); err != nil {
		return snapshot, fmt.Errorf("protected recovery action: %w", err)
	}
	return snapshot, nil
}

type boundedCapture struct {
	data      []byte
	total     int64
	truncated bool
	limit     int64
}

func (capture *boundedCapture) Write(data []byte) (int, error) {
	capture.total += int64(len(data))
	remaining := capture.limit - int64(len(capture.data))
	if remaining > 0 {
		keep := int64(len(data))
		if keep > remaining {
			keep = remaining
		}
		capture.data = append(capture.data, data[:int(keep)]...)
	}
	capture.truncated = capture.total > capture.limit
	return len(data), nil
}

func captureGitOutput(ctx context.Context, worktree string, limit int64, arguments ...string) (boundedCapture, error) {
	capture := boundedCapture{limit: limit}
	stderr := boundedCapture{limit: 4096}
	command := exec.CommandContext(ctx, "git", append([]string{"-C", worktree}, arguments...)...)
	command.Env = gitexec.Environment()
	command.Stdout = &capture
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return boundedCapture{}, fmt.Errorf("git %s failed: %w: %s", arguments[0], err, strings.TrimSpace(string(stderr.data)))
	}
	return capture, nil
}

func captureProgress(root string, paths []string, limit int64) (boundedCapture, []ProgressFileMetadata, error) {
	capture := boundedCapture{limit: limit}
	if len(paths) == 0 {
		return capture, nil, nil
	}
	canonicalRoot, err := validateProgressRoot(root)
	if err != nil {
		return boundedCapture{}, nil, err
	}
	archive := tar.NewWriter(&capture)
	files := make([]ProgressFileMetadata, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		canonicalPath, relative, info, err := validateProgressPath(canonicalRoot, path)
		if err != nil {
			return boundedCapture{}, nil, err
		}
		if _, duplicate := seen[canonicalPath]; duplicate {
			return boundedCapture{}, nil, fmt.Errorf("duplicate progress path %q", path)
		}
		seen[canonicalPath] = struct{}{}
		header := &tar.Header{
			Name:    filepath.ToSlash(relative),
			Mode:    int64(info.Mode().Perm()),
			Size:    info.Size(),
			ModTime: info.ModTime().UTC(),
			Format:  tar.FormatPAX,
		}
		offset := capture.total
		if err := archive.WriteHeader(header); err != nil {
			return boundedCapture{}, nil, fmt.Errorf("archive progress file %q: %w", relative, err)
		}
		file, err := os.Open(canonicalPath)
		if err != nil {
			return boundedCapture{}, nil, fmt.Errorf("open progress file %q: %w", relative, err)
		}
		copied, copyErr := io.Copy(archive, file)
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil {
			return boundedCapture{}, nil, fmt.Errorf("archive progress file %q: %w", relative, errors.Join(copyErr, closeErr))
		}
		if copied != info.Size() {
			return boundedCapture{}, nil, fmt.Errorf("progress file %q changed while snapshotting", relative)
		}
		files = append(files, ProgressFileMetadata{
			Path:          filepath.ToSlash(relative),
			Size:          info.Size(),
			Mode:          uint32(info.Mode().Perm()),
			ModifiedAt:    info.ModTime().UTC(),
			ArchiveOffset: offset,
		})
	}
	if err := archive.Close(); err != nil {
		return boundedCapture{}, nil, fmt.Errorf("finalize progress archive: %w", err)
	}
	return capture, files, nil
}

func validateProgressRoot(root string) (string, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", errors.New("progress root must be an absolute clean path")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("inspect progress root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("progress root must be a real directory, not a symlink")
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return "", errors.New("progress root must not traverse symlinks")
	}
	return root, nil
}

func validateProgressPath(root, path string) (string, string, os.FileInfo, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", "", nil, errors.New("progress paths must be absolute clean paths")
	}
	contained, err := pathContained(root, path)
	if err != nil || !contained || path == root {
		return "", "", nil, errors.New("progress path escapes the authorized progress root")
	}
	present, err := rejectSymlinkComponents(root, path)
	if err != nil {
		return "", "", nil, err
	}
	if !present {
		return "", "", nil, fmt.Errorf("progress path %q does not exist", path)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", "", nil, fmt.Errorf("inspect progress path: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", nil, errors.New("progress path must be a regular file, not a symlink")
	}
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return "", "", nil, fmt.Errorf("resolve progress path: %w", err)
	}
	return path, relative, info, nil
}

func normalizeSnapshotLimits(limits SnapshotLimits) (SnapshotLimits, error) {
	defaults := []struct {
		name         string
		value        *int64
		defaultValue int64
	}{
		{"status", &limits.StatusBytes, defaultStatusLimit},
		{"diff", &limits.DiffBytes, defaultDiffLimit},
		{"progress", &limits.ProgressBytes, defaultProgressLimit},
		{"metadata", &limits.MetadataBytes, defaultMetadataLimit},
	}
	for _, item := range defaults {
		if *item.value == 0 {
			*item.value = item.defaultValue
		}
		if *item.value < 0 || *item.value > maximumArtifactLimit {
			return SnapshotLimits{}, fmt.Errorf("%s snapshot limit must be between 1 and %d bytes", item.name, maximumArtifactLimit)
		}
	}
	return limits, nil
}

func artifactMetadata(ref ledger.EvidenceRef, capture boundedCapture) ArtifactMetadata {
	return ArtifactMetadata{
		Ref:           ref,
		SourceBytes:   capture.total,
		CapturedBytes: int64(len(capture.data)),
		Truncated:     capture.truncated,
	}
}

func validateSnapshotID(id string) error {
	if id == "" || id != strings.TrimSpace(id) || len(id) > 128 {
		return errors.New("snapshot ID must be 1-128 characters without surrounding whitespace")
	}
	for _, character := range id {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._-", character) {
			continue
		}
		return errors.New("snapshot ID may contain only letters, digits, dot, underscore, and hyphen")
	}
	return nil
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !bytes.ContainsRune([]byte("0123456789abcdefABCDEF"), character) {
			return false
		}
	}
	return true
}

func validatePublishedRef(ref ledger.EvidenceRef, expectedKind string) error {
	if strings.TrimSpace(ref.URI) == "" || ref.Kind != expectedKind || len(ref.SHA256) != 64 || !validGitObjectID(ref.SHA256) {
		return errors.New("artifact writer returned an incomplete immutable evidence reference")
	}
	return nil
}

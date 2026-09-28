package eviction

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/strictjson"
)

const maxRecordBytes = 256 << 10

var (
	ErrIntegrity = errors.New("worktree eviction record integrity failure")
	gitSHA       = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

// PinnedCheckpoint is one clean checkpoint kept reachable after eviction.
type PinnedCheckpoint struct {
	SHA string `json:"sha"`
	Ref string `json:"ref"`
}

// RecordV1 seals a controller eviction. It is written before the worktree is
// removed, so an interrupted eviction is completed, never forgotten.
type RecordV1 struct {
	Kind            string             `json:"kind"`
	SchemaVersion   int                `json:"schema_version"`
	RunID           string             `json:"run_id"`
	AuthorityDigest string             `json:"authority_digest"`
	Repository      string             `json:"repository"`
	Branch          string             `json:"branch"`
	Base            string             `json:"base"`
	Worktree        string             `json:"worktree"`
	Reason          string             `json:"reason"`
	EvictedAt       string             `json:"evicted_at"`
	WorktreeBytes   int64              `json:"worktree_bytes"`
	Checkpoints     []PinnedCheckpoint `json:"checkpoints"`
}

// CheckpointRef names the ref that keeps a run's checkpoint reachable.
func CheckpointRef(run, sha string) string { return "refs/abcp/checkpoints/" + run + "/" + sha }

// Pinned reports whether the record pins sha under its canonical ref.
func (r RecordV1) Pinned(sha string) bool {
	for _, c := range r.Checkpoints {
		if c.SHA == sha && c.Ref == CheckpointRef(r.RunID, sha) {
			return true
		}
	}
	return false
}

func (r RecordV1) validate() error {
	if r.Kind != "WorktreeEvictionV1" || r.SchemaVersion != 1 || !runtimecatalog.ValidIdentifier(r.RunID) || len(r.AuthorityDigest) != 64 ||
		!filepath.IsAbs(r.Repository) || filepath.Clean(r.Repository) != r.Repository || r.Branch != "abcp/"+r.RunID || !gitSHA.MatchString(r.Base) ||
		!filepath.IsAbs(r.Worktree) || filepath.Clean(r.Worktree) != r.Worktree || r.WorktreeBytes < 0 || !validReason(r.Reason) {
		return ErrIntegrity
	}
	if _, err := time.Parse(time.RFC3339Nano, r.EvictedAt); err != nil {
		return ErrIntegrity
	}
	for _, c := range r.Checkpoints {
		if !gitSHA.MatchString(c.SHA) || c.Ref != CheckpointRef(r.RunID, c.SHA) {
			return ErrIntegrity
		}
	}
	return nil
}

// Store keeps one record per evicted run under <service-root>/evictions.
type Store struct{ dir string }

func OpenStore(serviceRoot string) (*Store, error) {
	dir := filepath.Join(serviceRoot, "evictions")
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create eviction store: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, ErrIntegrity
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		return nil, ErrIntegrity
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(run string) (string, error) {
	if s == nil || !runtimecatalog.ValidIdentifier(run) {
		return "", ErrIntegrity
	}
	return filepath.Join(s.dir, run+".json"), nil
}

// Read returns the run's eviction record, or ok=false when it was never evicted.
func (s *Store) Read(run string) (RecordV1, bool, error) {
	path, err := s.path(run)
	if err != nil {
		return RecordV1{}, false, err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return RecordV1{}, false, nil
	}
	if err != nil {
		return RecordV1{}, false, ErrIntegrity
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > maxRecordBytes {
		return RecordV1{}, false, ErrIntegrity
	}
	data, err := io.ReadAll(io.LimitReader(file, maxRecordBytes+1))
	var record RecordV1
	if err != nil || len(data) > maxRecordBytes || strictjson.Decode(data, &record) != nil || record.RunID != run || record.validate() != nil {
		return RecordV1{}, false, ErrIntegrity
	}
	return record, true, nil
}

// Fact returns the reason and time of the run's sealed eviction. An unreadable
// or invalid record is not an eviction, so its absent worktree stays a fault.
func (s *Store) Fact(run string) (reason string, at time.Time, ok bool) {
	record, found, err := s.Read(run)
	if err != nil || !found {
		return "", time.Time{}, false
	}
	at, _ = time.Parse(time.RFC3339Nano, record.EvictedAt)
	return record.Reason, at, true
}

// Write durably replaces the run's record (temp file, fsync, rename, dir fsync).
func (s *Store) Write(record RecordV1) error {
	if err := record.validate(); err != nil {
		return err
	}
	path, err := s.path(record.RunID)
	if err != nil {
		return err
	}
	data, err := marshal(record)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".record-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

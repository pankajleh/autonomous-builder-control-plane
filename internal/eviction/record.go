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
	dir, err := openRecordDirectory(serviceRoot, "evictions")
	if err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// openRecordDirectory makes, or opens, <service-root>/<name>: a real folder of this user, private to it.
func openRecordDirectory(serviceRoot, name string) (string, error) {
	dir := filepath.Join(serviceRoot, name)
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create %s store: %w", name, err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return "", ErrIntegrity
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		return "", ErrIntegrity
	}
	return dir, nil
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
	data, ok, err := readRecordFile(path, maxRecordBytes)
	if err != nil || !ok {
		return RecordV1{}, false, err
	}
	var record RecordV1
	if strictjson.Decode(data, &record) != nil || record.RunID != run || record.validate() != nil {
		return RecordV1{}, false, ErrIntegrity
	}
	return record, true, nil
}

// readRecordFile reads a private record (a regular file of at most limit bytes, readable by this user alone, never
// through a link), or ok=false when there is none.
func readRecordFile(path string, limit int64) ([]byte, bool, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, ErrIntegrity
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > limit {
		return nil, false, ErrIntegrity
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, false, ErrIntegrity
	}
	return data, true, nil
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
	return writeRecordFile(s.dir, path, data)
}

// Remove removes the run's eviction record, because the run is forgotten: nothing is left for it to explain.
func (s *Store) Remove(run string) error {
	path, err := s.path(run)
	if err != nil {
		return err
	}
	return removeRecordFile(s.dir, path)
}

// writeRecordFile durably replaces path in dir with data (temp file, fsync, rename, dir fsync).
func writeRecordFile(dir, path string, data []byte) error {
	tmp, err := os.CreateTemp(dir, ".record-*")
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
	return syncDirectory(dir)
}

// removeRecordFile removes path in dir durably; an absent one is removed already.
func removeRecordFile(dir, path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(dir)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

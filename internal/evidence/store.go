// Package evidence stores immutable, content-addressed references to run artifacts.
package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

// ErrArtifactExists is returned when an artifact name has already been
// published. Evidence is immutable, so existing artifacts are never replaced.
var ErrArtifactExists = errors.New("evidence artifact already exists")

// Store writes immutable artifacts into one run-specific directory beneath an
// explicit caller-supplied evidence root.
type Store struct {
	root     string
	runID    string
	runDir   string
	link     func(string, string) error
	remove   func(string) error
	syncFile func(*os.File) error
	syncDir  func(string) error
}

// NewStore creates or opens the evidence directory for runID. root is always
// explicit: the store never derives an evidence location from a repository.
func NewStore(root, runID string) (*Store, error) {
	if root == "" {
		return nil, errors.New("evidence root is required")
	}
	if err := validateComponent("run ID", runID); err != nil {
		return nil, err
	}

	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve evidence root: %w", err)
	}
	if err := os.MkdirAll(absoluteRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create evidence root: %w", err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(absoluteRoot)
	if err != nil {
		return nil, fmt.Errorf("canonicalize evidence root: %w", err)
	}

	runDir := filepath.Join(canonicalRoot, runID)
	if err := os.Mkdir(runDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create run evidence directory: %w", err)
	}
	runInfo, err := os.Lstat(runDir)
	if err != nil {
		return nil, fmt.Errorf("inspect run evidence directory: %w", err)
	}
	if !runInfo.IsDir() || runInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("run evidence path must be a directory, not a symlink")
	}
	canonicalRunDir, err := filepath.EvalSymlinks(runDir)
	if err != nil {
		return nil, fmt.Errorf("canonicalize run evidence directory: %w", err)
	}
	if !within(canonicalRoot, canonicalRunDir) {
		return nil, errors.New("run evidence directory escapes evidence root")
	}

	return &Store{
		root: canonicalRoot, runID: runID, runDir: canonicalRunDir,
		link: os.Link, remove: os.Remove,
		syncFile: func(file *os.File) error { return file.Sync() },
		syncDir:  syncDirectory,
	}, nil
}

// Root returns the canonical caller-supplied evidence root.
func (s *Store) Root() string {
	return s.root
}

// RunID returns the run identity associated with the store.
func (s *Store) RunID() string {
	return s.runID
}

// RunDir returns the canonical run-specific evidence directory.
func (s *Store) RunDir() string {
	return s.runDir
}

// WriteBytes atomically publishes data as an immutable artifact and returns a
// reference whose SHA256 covers the exact supplied bytes.
func (s *Store) WriteBytes(name, kind string, data []byte) (ledger.EvidenceRef, error) {
	if err := validateComponent("artifact name", name); err != nil {
		return ledger.EvidenceRef{}, err
	}
	if kind == "" {
		return ledger.EvidenceRef{}, errors.New("evidence kind is required")
	}

	destination := filepath.Join(s.runDir, name)
	if !within(s.runDir, destination) {
		return ledger.EvidenceRef{}, errors.New("artifact path escapes run evidence directory")
	}
	if _, err := os.Lstat(destination); err == nil {
		if err := s.stabilizeExisting(destination); err != nil {
			return ledger.EvidenceRef{}, fmt.Errorf("stabilize existing evidence artifact: %w", err)
		}
		return ledger.EvidenceRef{}, fmt.Errorf("%w: %s", ErrArtifactExists, name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ledger.EvidenceRef{}, fmt.Errorf("inspect evidence artifact: %w", err)
	}

	temporary, err := os.CreateTemp(s.runDir, ".evidence-*")
	if err != nil {
		return ledger.EvidenceRef{}, fmt.Errorf("create temporary evidence artifact: %w", err)
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = s.remove(temporaryName)
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return ledger.EvidenceRef{}, fmt.Errorf("set evidence artifact permissions: %w", err)
	}
	if err := writeFull(temporary, data); err != nil {
		temporary.Close()
		return ledger.EvidenceRef{}, fmt.Errorf("write evidence artifact: %w", err)
	}
	if err := s.syncFile(temporary); err != nil {
		temporary.Close()
		return ledger.EvidenceRef{}, fmt.Errorf("sync evidence artifact: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return ledger.EvidenceRef{}, fmt.Errorf("close evidence artifact: %w", err)
	}

	// A hard link publishes the completed temporary file atomically while also
	// providing create-if-absent semantics. Unlike Rename, it cannot overwrite
	// an artifact another writer published concurrently.
	if err := s.link(temporaryName, destination); err != nil {
		if errors.Is(err, os.ErrExist) {
			if stabilizeErr := s.stabilizeExisting(destination); stabilizeErr != nil {
				return ledger.EvidenceRef{}, fmt.Errorf("stabilize concurrently published evidence artifact: %w", stabilizeErr)
			}
			return ledger.EvidenceRef{}, fmt.Errorf("%w: %s", ErrArtifactExists, name)
		}
		return ledger.EvidenceRef{}, fmt.Errorf("publish evidence artifact: %w", err)
	}
	if err := s.remove(temporaryName); err != nil {
		// Preserve the temporary name on failure. Its second link keeps every
		// reader fail-closed until a later, explicit cleanup succeeds.
		removeTemporary = false
		return ledger.EvidenceRef{}, fmt.Errorf("remove temporary evidence link: %w", err)
	}
	removeTemporary = false
	if _, err := os.Lstat(temporaryName); !errors.Is(err, os.ErrNotExist) {
		return ledger.EvidenceRef{}, errors.New("temporary evidence link still exists after removal")
	}
	if err := s.stabilizeExisting(destination); err != nil {
		return ledger.EvidenceRef{}, fmt.Errorf("stabilize published evidence artifact: %w", err)
	}

	digest := sha256.Sum256(data)
	return ledger.EvidenceRef{
		URI:    destination,
		SHA256: hex.EncodeToString(digest[:]),
		Kind:   kind,
	}, nil
}

func (s *Store) stabilizeExisting(path string) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		file, err := os.OpenFile(path, os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		verifyErr := verifyEvidenceSingleLink(file, path)
		if errors.Is(verifyErr, errEvidenceMultipleLinks) && time.Now().Before(deadline) {
			_ = file.Close()
			time.Sleep(time.Millisecond)
			continue
		}
		if verifyErr != nil {
			_ = file.Close()
			return verifyErr
		}
		if err := s.syncFile(file); err != nil {
			_ = file.Close()
			return fmt.Errorf("sync final evidence artifact: %w", err)
		}
		if err := verifyEvidenceSingleLink(file, path); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		if err := s.syncDir(s.runDir); err != nil {
			return fmt.Errorf("sync run evidence directory: %w", err)
		}
		confirmed, err := os.OpenFile(path, os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		err = verifyEvidenceSingleLink(confirmed, path)
		closeErr := confirmed.Close()
		return errors.Join(err, closeErr)
	}
}

func writeFull(file *os.File, data []byte) error {
	for len(data) > 0 {
		n, err := file.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

// WriteJSON marshals value as JSON and stores those exact bytes immutably.
func (s *Store) WriteJSON(name, kind string, value any) (ledger.EvidenceRef, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return ledger.EvidenceRef{}, fmt.Errorf("marshal evidence JSON: %w", err)
	}
	return s.WriteBytes(name, kind, data)
}

func validateComponent(label, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", label)
	}
	if value == "." || value == ".." || filepath.IsAbs(value) || filepath.Base(value) != value || strings.ContainsAny(value, `/\\`) {
		return fmt.Errorf("unsafe %s %q", label, value)
	}
	return nil
}

func within(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

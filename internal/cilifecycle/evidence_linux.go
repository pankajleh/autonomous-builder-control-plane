//go:build linux

package cilifecycle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

type artifactBoundary struct {
	store      ArtifactStore
	rootPath   string
	runPath    string
	boundRunID string
	rootDir    *os.File
	runDirFile *os.File
	rootID     ciFileID
	runDirID   ciFileID
}

func newArtifactBoundary(store ArtifactStore) (*artifactBoundary, error) {
	if store == nil || !validText(store.RunID(), MaxTextBytes) || !filepath.IsAbs(store.Root()) ||
		filepath.Clean(store.Root()) != store.Root() || !filepath.IsAbs(store.RunDir()) || filepath.Clean(store.RunDir()) != store.RunDir() ||
		store.RunDir() != filepath.Join(store.Root(), store.RunID()) {
		return nil, errors.New("evidence store paths or run identity are invalid")
	}
	for _, path := range []string{store.Root(), store.RunDir()} {
		canonical, err := filepath.EvalSymlinks(path)
		if err != nil || canonical != path {
			return nil, errors.New("evidence directories must pre-exist without symlink components")
		}
	}
	rootDir, rootID, err := openCIPath(store.Root(), syscall.O_RDONLY|syscall.O_DIRECTORY, true, 0o700)
	if err != nil {
		return nil, err
	}
	runDir, runID, err := openCIPath(store.RunDir(), syscall.O_RDONLY|syscall.O_DIRECTORY, true, 0o700)
	if err != nil {
		_ = rootDir.Close()
		return nil, err
	}
	return &artifactBoundary{
		store: store, rootPath: store.Root(), runPath: store.RunDir(), boundRunID: store.RunID(),
		rootDir: rootDir, runDirFile: runDir, rootID: rootID, runDirID: runID,
	}, nil
}

func (b *artifactBoundary) close() error {
	if b == nil {
		return nil
	}
	var runErr, rootErr error
	if b.runDirFile != nil {
		runErr = b.runDirFile.Close()
		b.runDirFile = nil
	}
	if b.rootDir != nil {
		rootErr = b.rootDir.Close()
		b.rootDir = nil
	}
	return errors.Join(runErr, rootErr)
}

func (b *artifactBoundary) runID() string  { return b.boundRunID }
func (b *artifactBoundary) runDir() string { return b.runPath }

func (b *artifactBoundary) verify() error {
	if b == nil || b.rootDir == nil || b.runDirFile == nil {
		return errors.New("CI evidence boundary is closed")
	}
	root, rootID, err := openCIPath(b.rootPath, syscall.O_RDONLY|syscall.O_DIRECTORY, true, 0o700)
	if err != nil {
		return err
	}
	_ = root.Close()
	run, runID, err := openCIPath(b.runPath, syscall.O_RDONLY|syscall.O_DIRECTORY, true, 0o700)
	if err != nil {
		return err
	}
	_ = run.Close()
	if rootID != b.rootID || runID != b.runDirID {
		return errors.New("CI evidence directory identity changed")
	}
	return nil
}

func (b *artifactBoundary) readVerified(name string, ref ledger.EvidenceRef, maximum int) ([]byte, error) {
	if err := validBundleArtifactName(name); err != nil {
		return nil, err
	}
	wantPath := filepath.Join(b.runPath, name)
	if ref.URI != wantPath || ref.Kind != EvidenceBundleKindV1 || !validDigest(ref.SHA256) {
		return nil, errors.New("evidence reference conflicts with deterministic attempt path")
	}
	if err := b.verify(); err != nil {
		return nil, err
	}
	data, err := evidence.ReadVerifiedLocal(b.rootPath, ref, int64(maximum))
	if err != nil {
		return nil, err
	}
	if err := b.verify(); err != nil {
		return nil, err
	}
	return data, nil
}

func (b *artifactBoundary) readExisting(name, kind string, maximum int) ([]byte, ledger.EvidenceRef, bool, error) {
	if err := validBundleArtifactName(name); err != nil || kind != EvidenceBundleKindV1 || maximum < 1 || maximum > MaxBundleBytes {
		return nil, ledger.EvidenceRef{}, false, errors.New("invalid deterministic evidence read")
	}
	if err := b.verify(); err != nil {
		return nil, ledger.EvidenceRef{}, false, err
	}
	file, id, err := openCIAt(b.runDirFile, name, syscall.O_RDONLY|syscall.O_NONBLOCK, false, 0o600)
	if errors.Is(err, syscall.ENOENT) {
		return nil, ledger.EvidenceRef{}, false, nil
	}
	if err != nil {
		return nil, ledger.EvidenceRef{}, false, err
	}
	defer file.Close()
	before, err := artifactFileSnapshot(file)
	if err != nil || before.size < 1 || before.size > int64(maximum) {
		return nil, ledger.EvidenceRef{}, false, errors.New("deterministic evidence has an invalid size")
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil || len(data) > maximum {
		return nil, ledger.EvidenceRef{}, false, errors.New("deterministic evidence exceeds its read bound")
	}
	after, err := artifactFileSnapshot(file)
	if err != nil || before != after || int64(len(data)) != before.size {
		return nil, ledger.EvidenceRef{}, false, errors.New("deterministic evidence changed while being read")
	}
	resolved, resolvedID, err := openCIAt(b.runDirFile, name, syscall.O_RDONLY|syscall.O_NONBLOCK, false, 0o600)
	if err != nil {
		return nil, ledger.EvidenceRef{}, false, err
	}
	_ = resolved.Close()
	if resolvedID != id {
		return nil, ledger.EvidenceRef{}, false, errors.New("deterministic evidence path was replaced")
	}
	digest := sha256.Sum256(data)
	ref := ledger.EvidenceRef{URI: filepath.Join(b.runPath, name), SHA256: hex.EncodeToString(digest[:]), Kind: kind}
	verified, err := b.readVerified(name, ref, maximum)
	if err != nil || !bytes.Equal(data, verified) {
		return nil, ledger.EvidenceRef{}, false, fmt.Errorf("verify deterministic evidence: %w", err)
	}
	return verified, ref, true, nil
}

type artifactSnapshot struct {
	device, inode uint64
	size          int64
	mode          uint32
	mtimeSec      int64
	mtimeNsec     int64
	ctimeSec      int64
	ctimeNsec     int64
}

func artifactFileSnapshot(file *os.File) (artifactSnapshot, error) {
	var stat syscall.Stat_t
	if file == nil || syscall.Fstat(int(file.Fd()), &stat) != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFREG ||
		os.FileMode(stat.Mode).Perm() != 0o600 || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 {
		return artifactSnapshot{}, errors.New("unsafe deterministic evidence object")
	}
	return artifactSnapshot{
		device: uint64(stat.Dev), inode: stat.Ino, size: stat.Size, mode: stat.Mode,
		mtimeSec: stat.Mtim.Sec, mtimeNsec: stat.Mtim.Nsec, ctimeSec: stat.Ctim.Sec, ctimeNsec: stat.Ctim.Nsec,
	}, nil
}

func validBundleArtifactName(name string) error {
	if len(name) != len("ci-")+64+len(".json") || filepath.Base(name) != name ||
		name[:3] != "ci-" || name[len(name)-5:] != ".json" || !validDigest(name[3:len(name)-5]) {
		return errors.New("invalid deterministic CI bundle name")
	}
	return nil
}

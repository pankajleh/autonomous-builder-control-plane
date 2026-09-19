//go:build linux

package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

func TestReadVerifiedLocalRejectsUnsafeArtifactsAndIntegrityChanges(t *testing.T) {
	root := t.TempDir()
	data := []byte("verified evidence")
	path := filepath.Join(root, "artifact.txt")
	writeEvidenceFixture(t, path, data)
	ref := evidenceFixtureRef(path, "test", data)

	got, err := ReadVerifiedLocal(root, ref, int64(len(data)))
	if err != nil || string(got) != string(data) {
		t.Fatalf("verified read = %q, %v", got, err)
	}

	t.Run("outside root", func(t *testing.T) {
		outside := filepath.Join(t.TempDir(), "outside.txt")
		writeEvidenceFixture(t, outside, data)
		if got, err := ReadVerifiedLocal(root, evidenceFixtureRef(outside, "test", data), 1024); err == nil || got != nil {
			t.Fatalf("outside-root read = %q, %v", got, err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		link := filepath.Join(root, "artifact-link")
		if err := os.Symlink(path, link); err != nil {
			t.Fatal(err)
		}
		if got, err := ReadVerifiedLocal(root, evidenceFixtureRef(link, "test", data), 1024); err == nil || got != nil {
			t.Fatalf("symlink read = %q, %v", got, err)
		}
	})

	t.Run("hard link before open", func(t *testing.T) {
		linked := filepath.Join(root, "hard-linked.txt")
		writeEvidenceFixture(t, linked, data)
		if err := os.Link(linked, linked+".other"); err != nil {
			t.Fatal(err)
		}
		if got, err := ReadVerifiedLocal(root, evidenceFixtureRef(linked, "test", data), 1024); err == nil || got != nil {
			t.Fatalf("hard-link read = %q, %v", got, err)
		}
	})

	t.Run("special file", func(t *testing.T) {
		fifo := filepath.Join(root, "artifact.fifo")
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := ReadVerifiedLocal(root, evidenceFixtureRef(fifo, "test", nil), 1024); err == nil || got != nil {
			t.Fatalf("special-file read = %q, %v", got, err)
		}
	})

	t.Run("digest mismatch", func(t *testing.T) {
		bad := ref
		bad.SHA256 = strings.Repeat("0", 64)
		if got, err := ReadVerifiedLocal(root, bad, 1024); err == nil || got != nil {
			t.Fatalf("digest-mismatch read = %q, %v", got, err)
		}
	})

	t.Run("size ceiling", func(t *testing.T) {
		if got, err := ReadVerifiedLocal(root, ref, int64(len(data)-1)); err == nil || got != nil {
			t.Fatalf("oversized read = %q, %v", got, err)
		}
	})
}

func TestReadBoundedLocalRequiresOneLinkAtEveryDescriptorCheck(t *testing.T) {
	data := []byte("descriptor identity")

	t.Run("after read", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "artifact.txt")
		writeEvidenceFixture(t, path, data)
		got, err := readBoundedLocalAtStages(root, path, 1024, func() error {
			return os.Link(path, path+".after-read")
		}, nil)
		if err == nil || got != nil || !strings.Contains(err.Error(), "exactly one hard link") {
			t.Fatalf("after-read link race = %q, %v", got, err)
		}
	})

	t.Run("reopened path", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "artifact.txt")
		writeEvidenceFixture(t, path, data)
		got, err := readBoundedLocalAtStages(root, path, 1024, nil, func() error {
			return os.Link(path, path+".before-reopen")
		})
		if err == nil || got != nil || !strings.Contains(err.Error(), "exactly one hard link") {
			t.Fatalf("reopen link race = %q, %v", got, err)
		}
	})
}

func TestReadBoundedLocalRejectsPathReplacementRace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "artifact.txt")
	data := []byte("same bytes, different inode")
	writeEvidenceFixture(t, path, data)

	got, err := readBoundedLocalAtStages(root, path, 1024, nil, func() error {
		if err := os.Rename(path, path+".original"); err != nil {
			return err
		}
		return os.WriteFile(path, data, 0o600)
	})
	if err == nil || got != nil || !strings.Contains(err.Error(), "path was replaced") {
		t.Fatalf("replacement race = %q, %v", got, err)
	}
}

func evidenceFixtureRef(path, kind string, data []byte) ledger.EvidenceRef {
	digest := sha256.Sum256(data)
	return ledger.EvidenceRef{URI: path, SHA256: hex.EncodeToString(digest[:]), Kind: kind}
}

func writeEvidenceFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

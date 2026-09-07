//go:build linux

package integrationworkspace

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

func TestReadExistingEvidenceRejectsSpecialSymlinkAndOversizedFilesBeforeRead(t *testing.T) {
	root := t.TempDir()
	digest := func(data []byte) string {
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:])
	}

	t.Run("fifo", func(t *testing.T) {
		path := filepath.Join(root, "capture-fifo")
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := readExistingEvidence(ledger.EvidenceRef{URI: path, SHA256: digest(nil), Kind: captureEvidenceKind}, captureEvidenceKind, root)
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("FIFO error = %v", err)
		}
	})

	t.Run("device", func(t *testing.T) {
		_, err := readExistingEvidence(ledger.EvidenceRef{URI: "/dev/null", SHA256: digest(nil), Kind: captureEvidenceKind}, captureEvidenceKind, "/dev")
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("device error = %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		target := filepath.Join(root, "capture-target")
		if err := os.WriteFile(target, []byte("capture"), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "capture-link")
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		_, err := readExistingEvidence(ledger.EvidenceRef{URI: path, SHA256: digest([]byte("capture")), Kind: captureEvidenceKind}, captureEvidenceKind, root)
		if err == nil || !strings.Contains(err.Error(), "open evidence artifact") {
			t.Fatalf("symlink error = %v", err)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		path := filepath.Join(root, "capture-large")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(maximumExistingEvidenceBytes + 1); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		_, err = readExistingEvidence(ledger.EvidenceRef{URI: path, SHA256: digest(nil), Kind: captureEvidenceKind}, captureEvidenceKind, root)
		if err == nil || !strings.Contains(err.Error(), "maximum") {
			t.Fatalf("oversized error = %v", err)
		}
	})
}

//go:build linux

package integrationgate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

func TestGateRejectsReviewEvidenceOutsideRootAndUnsafeFileTypes(t *testing.T) {
	tests := []struct {
		name string
		make func(*testing.T, gateFixture) ledger.EvidenceRef
		want string
	}{
		{name: "outside-root", make: func(t *testing.T, fixture gateFixture) ledger.EvidenceRef {
			path := filepath.Join(t.TempDir(), "outside")
			return writeRawEvidence(t, path, []byte("outside"), "review")
		}, want: "outside the controller-owned evidence root"},
		{name: "fifo", make: func(t *testing.T, fixture gateFixture) ledger.EvidenceRef {
			path := filepath.Join(filepath.Dir(fixture.request.Reviews[0].Evidence[0].URI), "review-fifo")
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
			return ledger.EvidenceRef{URI: path, SHA256: emptyDigest(), Kind: "review"}
		}, want: "regular file"},
		{name: "device", make: func(t *testing.T, fixture gateFixture) ledger.EvidenceRef {
			return ledger.EvidenceRef{URI: "/dev/null", SHA256: emptyDigest(), Kind: "review"}
		}, want: "outside the controller-owned evidence root"},
		{name: "symlink", make: func(t *testing.T, fixture gateFixture) ledger.EvidenceRef {
			target := fixture.request.Reviews[1].Evidence[0]
			path := filepath.Join(filepath.Dir(target.URI), "review-link")
			if err := os.Symlink(target.URI, path); err != nil {
				t.Fatal(err)
			}
			return ledger.EvidenceRef{URI: path, SHA256: target.SHA256, Kind: "review"}
		}, want: "open evidence artifact"},
		{name: "oversized", make: func(t *testing.T, fixture gateFixture) ledger.EvidenceRef {
			path := filepath.Join(filepath.Dir(fixture.request.Reviews[0].Evidence[0].URI), "review-oversized")
			file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(maxEvidenceArtifactBytes + 1); err != nil {
				file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			return ledger.EvidenceRef{URI: path, SHA256: emptyDigest(), Kind: "review"}
		}, want: "maximum"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGateFixture(t, false, "pass")
			fixture.request.Reviews[0].Evidence[0] = test.make(t, fixture)
			_, err := fixture.gate.Run(context.Background(), fixture.request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func writeRawEvidence(t *testing.T, path string, data []byte, kind string) ledger.EvidenceRef {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return ledger.EvidenceRef{URI: path, SHA256: hex.EncodeToString(sum[:]), Kind: kind}
}

func emptyDigest() string {
	sum := sha256.Sum256(nil)
	return hex.EncodeToString(sum[:])
}

//go:build linux

package combinedacceptance

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
)

func TestEvaluatorRejectsFIFOIntegrationEvidenceWithoutBlocking(t *testing.T) {
	fixture := newFixture(t, []authority.AcceptanceCommand{{
		Name: "combined unit", Class: "unit", Required: true, Timeout: "5s", Argv: helperArgv("pass"),
	}})
	target := cloneTarget(fixture.target)
	fifo := filepath.Join(t.TempDir(), "integration-evidence.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	target.Integration.Evidence[0].URI = fifo
	target.Integration.Evidence[0].SHA256 = strings.Repeat("0", sha256.Size*2)

	started := time.Now()
	assertIntegrationEvidenceRejectedBeforeExecution(t, fixture, target, "regular file")
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("FIFO rejection took %s; evidence validation may have blocked", elapsed)
	}
}

func TestEvaluatorRejectsDeviceIntegrationEvidenceWithoutReading(t *testing.T) {
	const device = "/dev/null"
	info, err := os.Lstat(device)
	if err != nil || info.Mode()&os.ModeDevice == 0 {
		t.Skip("safe character device is unavailable")
	}
	fixture := newFixture(t, []authority.AcceptanceCommand{{
		Name: "combined unit", Class: "unit", Required: true, Timeout: "5s", Argv: helperArgv("pass"),
	}})
	target := cloneTarget(fixture.target)
	target.Integration.Evidence[0].URI = device
	target.Integration.Evidence[0].SHA256 = strings.Repeat("0", sha256.Size*2)
	assertIntegrationEvidenceRejectedBeforeExecution(t, fixture, target, "regular file")
}

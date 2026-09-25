package runadmission

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// ResumeRun re-launches the exact admitted run with --resume and the same
// durable manifest/ledger/evidence paths from the admission binding.
func TestResumeRunRelaunchesWithResumeFlag(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	defer func() {
		fixture.closeLocks()
		fixture.controller.Close()
	}()

	runID := DeriveRunID(fixture.principal.PrincipalID, fixture.request.RequestID)
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); err != nil {
		t.Fatalf("admit run: %v", err)
	}

	fixture.mu.Lock()
	firstLaunches := len(fixture.starts)
	// Simulate the first run subprocess exiting and releasing its launch lock,
	// so the resume can acquire it.
	for _, lock := range fixture.locks {
		_ = lock.Close()
	}
	fixture.locks = nil
	fixture.mu.Unlock()
	if firstLaunches != 1 {
		t.Fatalf("first admission launches = %d, want 1", firstLaunches)
	}

	if err := fixture.controller.ResumeRun(context.Background(), runID); err != nil {
		t.Fatalf("resume run: %v", err)
	}

	fixture.mu.Lock()
	starts := append([][]string(nil), fixture.starts...)
	fixture.mu.Unlock()
	if len(starts) != 2 {
		t.Fatalf("launches after resume = %d, want 2", len(starts))
	}
	resume := starts[1]
	if len(resume) < 3 || resume[0] != fixture.controller.executable {
		t.Fatalf("resume argv[0] = %q", resume[0])
	}
	// The resume argv must include "run" followed by "--resume" and the durable
	// binding paths.
	joined := strings.Join(resume, " ")
	if !strings.Contains(joined, "run") || !strings.Contains(joined, "--resume") {
		t.Fatalf("resume argv missing run/--resume: %v", resume)
	}
	binding, err := fixture.controller.bindingForRunID(runID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--manifest", binding.ManifestPath,
		"--ledger", binding.CanonicalLedgerPath,
		"--evidence-root", binding.EvidenceRoot,
	} {
		if !containsArg(resume, want) {
			t.Fatalf("resume argv missing %q: %v", want, resume)
		}
	}
}

// ResumeRun fails closed when the run id has no durable binding.
func TestResumeRunUnknownRunFailsClosed(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	defer func() {
		fixture.closeLocks()
		fixture.controller.Close()
	}()
	if err := fixture.controller.ResumeRun(context.Background(), "no-such-run"); !errors.Is(err, serviceapi.ErrActionStatusNotFound) {
		t.Fatalf("resume unknown run = %v", err)
	}
}

func containsArg(argv []string, want string) bool {
	for _, arg := range argv {
		if arg == want {
			return true
		}
	}
	return false
}

var _ = os.Getenv
var _ = filepath.Join

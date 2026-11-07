package runadmission

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// R6 (docs/plans/build-recovery.md §6.2): while the workflow database a profile's runs use cannot be reached, admission
// answers 503 (admission_unavailable) before anything says the run was launched, and the same request admits it once
// the database is back (58cbcfac, 2026-10-17: a 202 for a run that never ran).
func TestAdmissionAnswers503WhileTheWorkflowDatabaseCannotBeReached(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	down := errors.New("workflow authority storage is unavailable")
	probed := ""
	fixture.controller.workflowProbe = func(_ context.Context, path string) error {
		probed = path
		return down
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); !errors.Is(err, serviceapi.ErrAdmissionUnavailable) {
			t.Fatalf("admission while the database is down = %v", err)
		}
	}
	if len(fixture.starts) != 0 || probed == "" {
		t.Fatalf("launched %d runs while the database was down (probed %q)", len(fixture.starts), probed)
	}
	fixture.controller.workflowProbe = func(context.Context, string) error { return nil }
	response, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request)
	if err != nil || response.RunID != DeriveRunID(fixture.principal.PrincipalID, fixture.request.RequestID) || len(fixture.starts) != 1 {
		t.Fatalf("admission once the database is back = %+v, %v, %d launches", response, err, len(fixture.starts))
	}
}

// R6: a launched run that exits before it registers (as abcp run does when it cannot open its workflow database) is
// refused as passing, its launch intent cleared, and the same request launches it again; a run that registers is
// admitted as soon as it does, without the whole wait.
func TestAdmissionRefusesARunThatExitsBeforeItRegistersAndLaunchesItAgain(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	fixture.controller.launchWait = 5 * time.Second
	runID := DeriveRunID(fixture.principal.PrincipalID, fixture.request.RequestID)
	launches := 0
	fixture.controller.start = func(executable string, args []string, lock *os.File) error {
		launches++
		duplicate, err := syscall.Dup(int(lock.Fd()))
		if err != nil {
			return err
		}
		held := os.NewFile(uintptr(duplicate), "test-child-launch-lock")
		if launches == 1 {
			// The first run exits at once, before it registers.
			go func() { time.Sleep(100 * time.Millisecond); held.Close() }()
			return nil
		}
		// The second registers and keeps running.
		fixture.mu.Lock()
		fixture.locks = append(fixture.locks, held)
		fixture.mu.Unlock()
		registerFixtureRun(t, fixture, runID)
		return nil
	}
	started := time.Now()
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); !errors.Is(err, serviceapi.ErrAdmissionUnavailable) {
		t.Fatalf("admission of a run that exited before registering = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("the refusal waited %s, not until the run exited", elapsed)
	}
	started = time.Now()
	response, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request)
	if err != nil || response.RunID != runID || launches != 2 {
		t.Fatalf("the same request again = %+v, %v after %d launches", response, err, launches)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("a registered run waited %s", elapsed)
	}
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); err != nil || launches != 2 {
		t.Fatalf("a registered run's replay = %v after %d launches", err, launches)
	}
}

// R6: a run still starting when the wait ends is admitted as before (it may register a moment later).
func TestAdmissionAdmitsARunStillStartingWhenTheWaitEnds(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	fixture.controller.launchWait = 200 * time.Millisecond
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); err != nil || len(fixture.starts) != 1 {
		t.Fatalf("admission of a run still starting = %v, %d launches", err, len(fixture.starts))
	}
}

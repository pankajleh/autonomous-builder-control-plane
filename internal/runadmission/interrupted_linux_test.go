package runadmission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
	runctl "github.com/pankajleh/autonomous-builder-control-plane/internal/run"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
)

// fakeInterruptedRuns answers the resume pass from fixed ledgers and owners.
type fakeInterruptedRuns struct {
	mu    sync.Mutex
	runs  map[string]InterruptedRun
	reads int
}

func (f *fakeInterruptedRuns) set(runID string, run InterruptedRun) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runs == nil {
		f.runs = map[string]InterruptedRun{}
	}
	f.runs[runID] = run
}

func (f *fakeInterruptedRuns) ListRuns(after string, limit int) ([]runtimecatalog.RunRegistrationV1, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.runs))
	for id := range f.runs {
		if id > after {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	more := len(ids) > limit
	if more {
		ids = ids[:limit]
	}
	page := make([]runtimecatalog.RunRegistrationV1, 0, len(ids))
	for _, id := range ids {
		page = append(page, runtimecatalog.RunRegistrationV1{RunID: id})
	}
	return page, more, nil
}

func (f *fakeInterruptedRuns) InterruptedRun(_ context.Context, runID string) (InterruptedRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	run, ok := f.runs[runID]
	if !ok {
		return InterruptedRun{}, os.ErrNotExist
	}
	return run, nil
}

// interruptedAt is what the ledger of a run interrupted in state at at says: built from its events, as abcp serve
// reads them.
func interruptedAt(state domain.State, at time.Time, extra ...ledger.Event) runctl.Interruption {
	events := []ledger.Event{{EventType: string(domain.StateRunCreated), Timestamp: at.Add(-3 * time.Minute)}}
	from := domain.StateRunCreated
	for _, to := range []domain.State{domain.StateAuthorityValidated, domain.StateExecutionStarting, domain.StateImplementing} {
		events = append(events, ledger.Event{EventType: "STATE_TRANSITION", StateFrom: from, StateTo: to, Timestamp: at.Add(-time.Minute)})
		from = to
	}
	switch state {
	case domain.StateRecoveryRequired:
		events = append(events, ledger.Event{EventType: "STATE_TRANSITION", StateFrom: from, StateTo: state, Timestamp: at, Payload: map[string]any{"recovery": runctl.RecoveryResume}})
	case domain.StateFailed, domain.StateCancelled:
		events = append(events, ledger.Event{EventType: "STATE_TRANSITION", StateFrom: from, StateTo: state, Timestamp: at})
	}
	return runctl.InterruptionOf(append(events, extra...))
}

// newResumeFixture is an admission fixture whose product template allows a fresh attempt (and so a resume).
func newResumeFixture(t *testing.T, freshAttempts int) *admissionFixture {
	t.Helper()
	return newEditedAdmissionFixture(t, true, func(_ string, profiles *ProfileFileV1) {
		path := profiles.Profiles[0].ManifestTemplatePath
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var template authority.Manifest
		if err := json.Unmarshal(data, &template); err != nil {
			t.Fatal(err)
		}
		template.Ralphex.SourceSHA, template.Ralphex.FreshAttempts = ralphex.TaskPromptSourceSHA, freshAttempts
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		writeProtectedJSON(t, path, template)
	})
}

// admitAndExit admits request n's run, registers it, and lets its launched run exit (its launch lock comes free).
func admitAndExit(t *testing.T, fixture *admissionFixture, n int) string {
	t.Helper()
	request := fixture.request
	request.RequestID = fmt.Sprintf("request-%d", n)
	response, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request)
	if err != nil {
		t.Fatalf("admit %s: %v", request.RequestID, err)
	}
	registerFixtureRun(t, fixture, response.RunID)
	fixture.closeLocks()
	return response.RunID
}

func (f *admissionFixture) launches() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.starts...)
}

// R6's resume: a registered run interrupted while it implemented, whose owner is gone, is launched again once as abcp
// run --resume-interrupted from its admission binding (its launch lock handed over as admission's), the record written
// first; no later pass, nor a pass of the next abcp serve, launches it again.
func TestTheResumePassLaunchesAnInterruptedRunOnce(t *testing.T) {
	fixture := newResumeFixture(t, 1)
	now := fixture.controller.clock()
	runID := admitAndExit(t, fixture, 1)
	runs := &fakeInterruptedRuns{}
	runs.set(runID, InterruptedRun{Interruption: interruptedAt(domain.StateRecoveryRequired, now.Add(-time.Minute))})
	var log bytes.Buffer
	if launched := fixture.controller.Resumer(runs, &log).Pass(context.Background()); launched != 1 {
		t.Fatalf("launched %d, log:\n%s", launched, log.String())
	}
	starts := fixture.launches()
	if len(starts) != 2 {
		t.Fatalf("launches = %d, want the admission's and the resume", len(starts))
	}
	binding, err := fixture.controller.bindingForRunID(runID)
	if err != nil {
		t.Fatal(err)
	}
	resume := starts[1]
	if resume[0] != fixture.controller.executable || resume[1] != "run" || resume[2] != "--resume-interrupted" {
		t.Fatalf("the resume's argv = %v", resume)
	}
	for _, want := range []string{binding.ManifestPath, binding.CanonicalLedgerPath, binding.EvidenceRoot, binding.WorkflowAuthorityConfigPath, fixture.service, "--build-wall-file"} {
		if !containsArg(resume, want) {
			t.Fatalf("the resume's argv lacks %q: %v", want, resume)
		}
	}
	if containsArg(resume, "--resume") {
		t.Fatalf("the resume is not a human decision's: %v", resume)
	}
	data, err := os.ReadFile(filepath.Join(fixture.service, "admissions", "resumes", runID+".json"))
	var record InterruptedResumeV1
	if err != nil || json.Unmarshal(data, &record) != nil || record.Kind != InterruptedResumeKind || record.RunID != runID ||
		record.State != string(domain.StateRecoveryRequired) || record.Attempts != 1 {
		t.Fatalf("the resume record = %s, %v", data, err)
	}
	if !strings.Contains(log.String(), "abcp resume launched run="+runID+" state=RECOVERY_REQUIRED attempt=2") {
		t.Fatalf("log:\n%s", log.String())
	}
	fixture.closeLocks()
	if launched := fixture.controller.Resumer(runs, &log).Pass(context.Background()); launched != 0 || len(fixture.launches()) != 2 {
		t.Fatalf("the next abcp serve's pass launched %d", launched)
	}
}

// A run is not resumed while its owner lives or a launch of it holds its lock, nor ever when it ended, was resumed
// before, was asked to stop, stopped over a day ago, or its profile allows no fresh attempt.
func TestTheResumePassLeavesEveryOtherRun(t *testing.T) {
	fixture := newResumeFixture(t, 1)
	now := fixture.controller.clock()
	recent := now.Add(-time.Minute)
	resumed := ledger.Event{EventType: "STATE_TRANSITION", StateFrom: domain.StateRecoveryRequired, StateTo: domain.StateExecutionStarting, Timestamp: recent}
	implementingAgain := ledger.Event{EventType: "STATE_TRANSITION", StateFrom: domain.StateExecutionStarting, StateTo: domain.StateImplementing, Timestamp: recent}
	stop := ledger.Event{EventType: "API_CANCEL_REQUESTED", Timestamp: recent}
	cases := []struct {
		name    string
		run     InterruptedRun
		settled bool
	}{
		{"its owner lives", InterruptedRun{Interruption: interruptedAt(domain.StateImplementing, recent), OwnerLive: true}, false},
		{"it failed", InterruptedRun{Interruption: interruptedAt(domain.StateFailed, recent)}, true},
		{"it was cancelled", InterruptedRun{Interruption: interruptedAt(domain.StateCancelled, recent)}, true},
		{"it was resumed before", InterruptedRun{Interruption: interruptedAt(domain.StateRecoveryRequired, recent, resumed, implementingAgain)}, true},
		{"a stop was asked for", InterruptedRun{Interruption: interruptedAt(domain.StateImplementing, recent, stop)}, true},
		{"it stopped over a day ago", InterruptedRun{Interruption: interruptedAt(domain.StateImplementing, now.Add(-ResumeWithin-time.Minute))}, true},
	}
	runs := &fakeInterruptedRuns{}
	ids := map[string]string{}
	for index, c := range cases {
		ids[c.name] = admitAndExit(t, fixture, index+1)
		runs.set(ids[c.name], c.run)
	}
	held := admitAndExit(t, fixture, len(cases)+1)
	runs.set(held, InterruptedRun{Interruption: interruptedAt(domain.StateImplementing, recent)})
	lock, acquired, err := acquireLaunchLock(fixture.controller.launchesFD, held)
	if err != nil || !acquired {
		t.Fatal("hold the launch lock", err)
	}
	defer lock.Close()
	var log bytes.Buffer
	resumer := fixture.controller.Resumer(runs, &log)
	if launched := resumer.Pass(context.Background()); launched != 0 || len(fixture.launches()) != len(cases)+1 {
		t.Fatalf("launched %d, log:\n%s", launched, log.String())
	}
	for _, c := range cases {
		if resumer.settled[ids[c.name]] != c.settled {
			t.Fatalf("%s: settled %t, want %t", c.name, resumer.settled[ids[c.name]], c.settled)
		}
	}
	if !strings.Contains(log.String(), "abcp resume skipped run="+ids["it stopped over a day ago"]+" state=IMPLEMENTING reason=interrupted_over_24h0m0s_ago") {
		t.Fatalf("log:\n%s", log.String())
	}
	// A settled run is never read again.
	runs.mu.Lock()
	runs.reads = 0
	runs.mu.Unlock()
	resumer.Pass(context.Background())
	if runs.reads != 2 {
		t.Fatalf("the next pass read %d runs, want the two that may still be resumed", runs.reads)
	}

	t.Run("a profile without a fresh attempt", func(t *testing.T) {
		plain := newResumeFixture(t, 0)
		runID := admitAndExit(t, plain, 1)
		runs := &fakeInterruptedRuns{}
		runs.set(runID, InterruptedRun{Interruption: interruptedAt(domain.StateImplementing, plain.controller.clock().Add(-time.Minute))})
		resumer := plain.controller.Resumer(runs, nil)
		if launched := resumer.Pass(context.Background()); launched != 0 || len(plain.launches()) != 1 || !resumer.settled[runID] {
			t.Fatalf("launched %d", launched)
		}
	})
}

// While the wall cannot start or the workflow database cannot be reached, a pass launches nothing and records nothing;
// the next pass, once they are back, launches the resume.
func TestTheResumePassWaitsForTheWallAndTheWorkflowDatabase(t *testing.T) {
	fixture := newResumeFixture(t, 1)
	runID := admitAndExit(t, fixture, 1)
	runs := &fakeInterruptedRuns{}
	runs.set(runID, InterruptedRun{Interruption: interruptedAt(domain.StateImplementing, fixture.controller.clock().Add(-time.Minute))})
	var log bytes.Buffer
	resumer := fixture.controller.Resumer(runs, &log)
	record := filepath.Join(fixture.service, "admissions", "resumes", runID+".json")

	passWall(fixture.controller, errors.New("bwrap: setting up uid map: Permission denied"))
	if launched := resumer.Pass(context.Background()); launched != 0 || len(fixture.launches()) != 1 {
		t.Fatalf("launched %d without a wall", launched)
	}
	passWall(fixture.controller, nil)
	fixture.controller.workflowProbe = func(context.Context, string) error { return errors.New("workflow authority storage is unavailable") }
	if launched := resumer.Pass(context.Background()); launched != 0 || len(fixture.launches()) != 1 {
		t.Fatalf("launched %d without the workflow database", launched)
	}
	if _, err := os.Stat(record); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a waiting pass wrote a record: %v", err)
	}
	for _, want := range []string{"cause=build_wall_unavailable", "cause=workflow_database_unavailable"} {
		if !strings.Contains(log.String(), "abcp resume waits run="+runID+" "+want) {
			t.Fatalf("log lacks %s:\n%s", want, log.String())
		}
	}
	fixture.controller.workflowProbe = func(context.Context, string) error { return nil }
	if launched := resumer.Pass(context.Background()); launched != 1 || len(fixture.launches()) != 2 {
		t.Fatalf("launched %d once both were back", launched)
	}
}

// At most ResumesPerPass a pass; the rest the next pass. A launch that could not start leaves no record, so a later pass
// launches it.
func TestTheResumePassLaunchesAtMostFourAndRetriesALaunchThatDidNotStart(t *testing.T) {
	fixture := newResumeFixture(t, 1)
	runs := &fakeInterruptedRuns{}
	for n := 1; n <= ResumesPerPass+1; n++ {
		runID := admitAndExit(t, fixture, n)
		runs.set(runID, InterruptedRun{Interruption: interruptedAt(domain.StateImplementing, fixture.controller.clock().Add(-time.Minute))})
	}
	resumer := fixture.controller.Resumer(runs, nil)
	if launched := resumer.Pass(context.Background()); launched != ResumesPerPass {
		t.Fatalf("the first pass launched %d", launched)
	}
	fixture.closeLocks()
	start := fixture.controller.start
	fixture.controller.start = func(string, []string, *os.File) error { return errors.New("exec: no such file") }
	if launched := resumer.Pass(context.Background()); launched != 0 {
		t.Fatalf("a launch that did not start counted: %d", launched)
	}
	fixture.controller.start = start
	if launched := resumer.Pass(context.Background()); launched != 1 {
		t.Fatalf("the pass after a launch that did not start launched %d", launched)
	}
	if launched := resumer.Pass(context.Background()); launched != 0 {
		t.Fatalf("a later pass launched %d", launched)
	}
	if got := len(fixture.launches()); got != 2*(ResumesPerPass+1) {
		t.Fatalf("launches = %d, want each run's admission and one resume each", got)
	}
}

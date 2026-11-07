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

// queueFixture is an admission fixture whose profile queues (R7), whose clock the test moves, and whose launches the
// test answers: register makes each launched run register at once (holding its launch lock, as abcp run does).
type queueFixture struct {
	*admissionFixture
	now      time.Time
	down     error
	launches int
	register bool
	runID    string
}

func newQueueFixture(t *testing.T, queue bool) *queueFixture {
	t.Helper()
	fixture := &queueFixture{admissionFixture: newEditedAdmissionFixture(t, true, func(_ string, profiles *ProfileFileV1) {
		profiles.Profiles[0].QueueAdmissions = queue
	}), now: time.Date(2026, 11, 7, 18, 0, 0, 0, time.UTC), register: true}
	fixture.runID = DeriveRunID(fixture.principal.PrincipalID, fixture.request.RequestID)
	fixture.controller.clock = func() time.Time { return fixture.now }
	fixture.controller.workflowProbe = func(context.Context, string) error { return fixture.down }
	fixture.controller.start = func(_ string, _ []string, lock *os.File) error {
		fixture.launches++
		duplicate, err := syscall.Dup(int(lock.Fd()))
		if err != nil {
			return err
		}
		held := os.NewFile(uintptr(duplicate), "test-child-launch-lock")
		if !fixture.register {
			go func() { time.Sleep(100 * time.Millisecond); held.Close() }()
			return nil
		}
		fixture.mu.Lock()
		fixture.locks = append(fixture.locks, held)
		fixture.mu.Unlock()
		registerFixtureRun(t, fixture.admissionFixture, fixture.runID)
		return nil
	}
	return fixture
}

func (f *queueFixture) admit(t *testing.T) {
	t.Helper()
	response, err := f.controller.AdmitRun(context.Background(), f.principal, f.request)
	if err != nil || response.RunID != f.runID {
		t.Fatalf("admission = %+v, %v", response, err)
	}
}

func (f *queueFixture) queued(t *testing.T) AdmissionQueueEntryV1 {
	t.Helper()
	entry, found, err := f.controller.readQueueEntry(f.runID)
	if err != nil || !found {
		t.Fatalf("no queue entry: %v", err)
	}
	return entry
}

// R7: while the workflow database is down, a queuing profile answers 202 and queues the admission (nothing launched, the
// run answering run_queued); a replay is answered alike; the queue tries again every 30 s, backing off, and launches the
// run once the database is back, the entry going once the run registers.
func TestAQueuedAdmissionWaitsForTheDatabaseAndStartsWhenItIsBack(t *testing.T) {
	f := newQueueFixture(t, true)
	f.down = errors.New("workflow authority storage is unavailable")
	f.admit(t)
	f.admit(t)
	if f.launches != 0 {
		t.Fatalf("%d launches while the database was down", f.launches)
	}
	entry := f.queued(t)
	if entry.State != QueueStateQueued || entry.Cause != QueueCauseWorkflowDatabase || entry.QueuedSince != "2026-11-07T18:00:00Z" ||
		entry.NextTryAt != "2026-11-07T18:00:30Z" || entry.Attempts != 0 {
		t.Fatalf("entry = %+v", entry)
	}
	if queued, ok := f.controller.QueuedRun(f.runID); !ok || queued.Expired || queued.Cause != QueueCauseWorkflowDatabase || queued.QueuedSince != entry.QueuedSince {
		t.Fatalf("QueuedRun = %+v %v", queued, ok)
	}
	if f.controller.QueuePass(context.Background()) != 0 || f.queued(t).Attempts != 0 {
		t.Fatal("a pass before the entry was due tried it")
	}
	f.now = f.now.Add(QueueRetryEvery)
	f.controller.QueuePass(context.Background())
	if entry := f.queued(t); entry.Attempts != 1 || entry.NextTryAt != "2026-11-07T18:01:00Z" || f.launches != 0 {
		t.Fatalf("after a try with the database still down: %+v, %d launches", entry, f.launches)
	}
	f.now = f.now.Add(QueueRetryEvery)
	f.controller.QueuePass(context.Background())
	if entry := f.queued(t); entry.Attempts != 2 || entry.NextTryAt != "2026-11-07T18:02:00Z" {
		t.Fatalf("the back-off did not double: %+v", entry)
	}
	f.down = nil
	f.now = f.now.Add(2 * QueueRetryEvery)
	if registered := f.controller.QueuePass(context.Background()); registered != 1 || f.launches != 1 {
		t.Fatalf("once the database is back: %d registered, %d launches", registered, f.launches)
	}
	if _, found, _ := f.controller.readQueueEntry(f.runID); found {
		t.Fatal("the entry stayed after its run registered")
	}
	if _, ok := f.controller.QueuedRun(f.runID); ok {
		t.Fatal("a registered run still reads as queued")
	}
	f.admit(t)
	if f.launches != 1 {
		t.Fatalf("a registered run's replay launched it again: %d", f.launches)
	}
}

// R7: a queued admission that waited 30 minutes expires and answers admission_expired for 7 days; a replay of its
// request queues it again (or launches it, its cause cleared).
func TestAQueuedAdmissionExpiresAndAReplayQueuesItAgain(t *testing.T) {
	f := newQueueFixture(t, true)
	f.down = errors.New("down")
	f.admit(t)
	f.now = f.now.Add(QueueWait)
	f.controller.QueuePass(context.Background())
	if entry := f.queued(t); entry.State != QueueStateExpired || entry.ExpiredAt != "2026-11-07T18:30:00Z" {
		t.Fatalf("after 30 minutes: %+v", entry)
	}
	if queued, ok := f.controller.QueuedRun(f.runID); !ok || !queued.Expired {
		t.Fatalf("QueuedRun = %+v %v", queued, ok)
	}
	f.admit(t)
	if entry := f.queued(t); entry.State != QueueStateQueued || entry.QueuedSince != "2026-11-07T18:30:00Z" || f.launches != 0 {
		t.Fatalf("the replay after expiry: %+v, %d launches", entry, f.launches)
	}
	f.now = f.now.Add(QueueWait)
	f.controller.QueuePass(context.Background())
	f.now = f.now.Add(QueueKeepExpired)
	f.controller.QueuePass(context.Background())
	if _, found, _ := f.controller.readQueueEntry(f.runID); found {
		t.Fatal("an expired entry outlived its 7 days")
	}
	if _, ok := f.controller.QueuedRun(f.runID); ok {
		t.Fatal("a removed entry still reads as queued")
	}
}

// R7: a run that exits before it registers, and a wall that cannot start, queue the admission too; the queue launches
// it once the cause clears.
func TestARunThatExitsBeforeRegisteringAndAWallThatCannotStartAreQueued(t *testing.T) {
	f := newQueueFixture(t, true)
	f.controller.launchWait = 5 * time.Second
	f.register = false
	f.admit(t)
	if entry := f.queued(t); entry.Cause != QueueCauseExitedBeforeRegistering || f.launches != 1 {
		t.Fatalf("entry = %+v after %d launches", entry, f.launches)
	}
	if attempted, err := f.controller.readAdmissionLaunchIntent(mustBinding(t, f)); err != nil || attempted {
		t.Fatalf("the exited run's launch intent was kept: %v %v", attempted, err)
	}
	passWall(f.controller, errors.New("bubblewrap cannot start"))
	f.now = f.now.Add(QueueRetryEvery)
	f.controller.QueuePass(context.Background())
	if entry := f.queued(t); entry.Cause != QueueCauseBuildWall || entry.Attempts != 1 || f.launches != 1 {
		t.Fatalf("a try while the wall cannot start: %+v, %d launches", entry, f.launches)
	}
	passWall(f.controller, nil)
	f.register = true
	f.now = f.now.Add(QueueRetryEvery)
	if registered := f.controller.QueuePass(context.Background()); registered != 1 || f.launches != 2 {
		t.Fatalf("once the wall is back: %d registered, %d launches", registered, f.launches)
	}

	wall := newQueueFixture(t, true)
	passWall(wall.controller, errors.New("bubblewrap cannot start"))
	wall.admit(t)
	if entry := wall.queued(t); entry.Cause != QueueCauseBuildWall || wall.launches != 0 {
		t.Fatalf("a wall that cannot start: %+v, %d launches", entry, wall.launches)
	}
}

// R7 is the profile's choice: without queue_admissions, a passing cause is refused as before (503), nothing queued,
// and the setting is not part of the profile's binding digest.
func TestWithoutTheProfilesQueueAPassingCauseIsRefusedAsBefore(t *testing.T) {
	f := newQueueFixture(t, false)
	f.down = errors.New("down")
	if _, err := f.controller.AdmitRun(context.Background(), f.principal, f.request); !errors.Is(err, serviceapi.ErrAdmissionUnavailable) {
		t.Fatalf("admission = %v", err)
	}
	if _, found, _ := f.controller.readQueueEntry(f.runID); found {
		t.Fatal("a profile without the queue queued")
	}
	passWall(f.controller, errors.New("bubblewrap cannot start"))
	if _, err := f.controller.AdmitRun(context.Background(), f.principal, f.request); !errors.Is(err, serviceapi.ErrBuildWallUnavailable) {
		t.Fatalf("admission without a wall = %v", err)
	}
	for _, profile := range f.controller.profiles {
		configuration := profile.configuration
		configuration.QueueAdmissions = true
		queuing, err := loadProfile(configuration)
		if err != nil || queuing.profileBindingSHA256 != profile.profileBindingSHA256 {
			t.Fatalf("the queue setting changed the profile's binding digest: %v", err)
		}
	}
}

func mustBinding(t *testing.T, f *queueFixture) AdmissionBindingV1 {
	t.Helper()
	entry := f.queued(t)
	_, binding, err := f.controller.queuedBinding(entry)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

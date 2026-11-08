package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/actioncontrol"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/readmodel"
	runctl "github.com/pankajleh/autonomous-builder-control-plane/internal/run"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runadmission"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
)

// R6's resume (docs/plans/build-recovery.md §6.2): abcp run's signals, the takeover of an interrupted run's catalog
// ownership by abcp run --resume-interrupted, and what abcp serve's resume pass reads of a registered run.

// runSignals is abcp run's context: SIGINT or SIGTERM cancels it with runctl.NewSignalCause (SIGTERM lets a run that may
// be resumed stop at RECOVERY_REQUIRED), and both stay caught until stop, as signal.NotifyContext's were.
func runSignals(parent context.Context) (context.Context, func()) {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	ctx, stop := cancelOnSignal(parent, signals)
	return ctx, func() {
		signal.Stop(signals)
		stop()
	}
}

// cancelOnSignal cancels its context with the first signal's cause.
func cancelOnSignal(parent context.Context, signals <-chan os.Signal) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	done := make(chan struct{})
	go func() {
		select {
		case sig := <-signals:
			cancel(runctl.NewSignalCause(sig))
		case <-done:
		}
	}()
	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			close(done)
			cancel(context.Canceled)
		})
	}
}

// takeOverInterruptedOwner retires the owner lease the interrupted run's last abcp run left, so this one installs the
// next generation (registerRuntimeOwner) with its own cancel watcher and action journal: only once that owner's process
// is gone, and never when a stop is still waiting for that owner in the action journal (a run its owner stopped is not
// resumed). A lease retired already (abcp run closed it after a SIGTERM) needs nothing.
func takeOverInterruptedOwner(ctx context.Context, serviceRoot, runID string) error {
	catalog, err := runtimecatalog.Open(serviceRoot)
	if err != nil {
		return errors.New("open service runtime catalog safely")
	}
	defer catalog.Close()
	lease, found, err := readOwnerLease(catalog, runID)
	if err != nil {
		return errors.New("read the interrupted run's owner")
	}
	if !found || lease.State == runtimecatalog.OwnerLeaseRetired {
		return nil
	}
	if runtimecatalog.VerifyLiveOwner(lease) == nil {
		return fmt.Errorf("%w: the run's owner is still running", runctl.ErrNotResumable)
	}
	journal, err := actioncontrol.Open(serviceRoot)
	if err != nil {
		return errors.New("open service action journal safely")
	}
	defer journal.Close()
	pending, err := journal.OperationsForLease(ctx, runID, lease.LeaseID, nil)
	if err != nil {
		return errors.New("read the interrupted run's actions")
	}
	if len(pending) > 0 {
		return fmt.Errorf("%w: a stop was asked of it", runctl.ErrNotResumable)
	}
	// The stale generation is closed and retired under the journal's lock (journal, then catalog, as its own watcher
	// would have), so no action binds to it meanwhile; none can bind after it either, its process being gone.
	err = journal.BindSequence(ctx, runID, func(sequence uint64) error {
		guard, err := catalog.AcquireOwnerLeaseGuard(runID)
		if err != nil {
			return err
		}
		current, err := guard.Lease()
		if err == nil && current.LeaseID != lease.LeaseID {
			err = errors.New("the interrupted run's owner changed")
		}
		if err == nil && current.State == runtimecatalog.OwnerLeaseActive {
			_, err = guard.MarkClosing(lease.LeaseID, sequence)
		}
		if err == nil {
			_, err = guard.Retire(lease.LeaseID)
		}
		return errors.Join(err, guard.Close())
	})
	if err != nil {
		return errors.New("retire the interrupted run's owner")
	}
	return nil
}

func readOwnerLease(catalog *runtimecatalog.Catalog, runID string) (runtimecatalog.ActiveOwnerLeaseV1, bool, error) {
	guard, err := catalog.AcquireOwnerLeaseGuard(runID)
	if err != nil {
		return runtimecatalog.ActiveOwnerLeaseV1{}, false, err
	}
	lease, leaseErr := guard.Lease()
	if closeErr := guard.Close(); closeErr != nil {
		return runtimecatalog.ActiveOwnerLeaseV1{}, false, closeErr
	}
	if errors.Is(leaseErr, os.ErrNotExist) {
		return runtimecatalog.ActiveOwnerLeaseV1{}, false, nil
	}
	return lease, leaseErr == nil, leaseErr
}

// interruptedRuns answers abcp serve's resume pass from the runtime catalog, the authoritative read model and the run's
// owner lease.
type interruptedRuns struct {
	catalog *runtimecatalog.Catalog
	states  interface {
		Snapshot(context.Context, string) (readmodel.Snapshot, error)
	}
}

func (s interruptedRuns) ListRuns(after string, limit int) ([]runtimecatalog.RunRegistrationV1, bool, error) {
	return s.catalog.ListRuns(after, limit)
}

func (s interruptedRuns) InterruptedRun(ctx context.Context, runID string) (runadmission.InterruptedRun, error) {
	snapshot, err := s.states.Snapshot(ctx, runID)
	if err != nil {
		return runadmission.InterruptedRun{}, err
	}
	facts := runadmission.InterruptedRun{Interruption: runctl.InterruptionOf(snapshot.Events)}
	if !facts.Interruption.Resumable() {
		return facts, nil
	}
	lease, found, err := readOwnerLease(s.catalog, runID)
	if err != nil {
		return runadmission.InterruptedRun{}, err
	}
	facts.OwnerLive = found && runtimecatalog.VerifyLiveOwner(lease) == nil
	return facts, nil
}

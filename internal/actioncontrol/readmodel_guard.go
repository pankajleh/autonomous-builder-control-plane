package actioncontrol

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/readmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

const (
	GlobalLedgerSlots   = readmodel.MaxOpenLedgerObjects
	GlobalSnapshotSlots = readmodel.MaxConcurrentSnapshots
)

// guardedReadModelBase is the frozen Track-B service surface wrapped by the
// Track-D cross-process coordinator. The base service retains its in-process
// limits; the wrapper adds one service-root-wide limit shared by all OS
// processes.
type guardedReadModelBase interface {
	Snapshot(context.Context, string) (readmodel.Snapshot, error)
	ReadRunProjection(context.Context, string) (json.RawMessage, error)
	ReadEventProjection(context.Context, string, serviceapi.PageRequestV1) (json.RawMessage, error)
	WithExistingWritableLedger(context.Context, string, func(*ledger.JSONLLedger) error) error
}

// GuardedReadModel applies the service-root-wide ledger and snapshot ceilings
// to every frozen read seam. Writable action paths use the combined operation
// so capacity for their in-lease revalidation is reserved atomically.
type GuardedReadModel struct {
	base  guardedReadModelBase
	guard *resourceGuard
}

// RunnerSnapshotCoordinator gives a service-root-owned runner access to the
// same physical snapshot slots as service reads and owner watchers. The
// operation scope may contain more than one sequential ledger Snapshot call;
// callers use that only when one transition decision requires both state
// reconstruction and cancellation proof under the same authority.
type RunnerSnapshotCoordinator struct {
	guard *resourceGuard
}

func NewRunnerSnapshotCoordinator(serviceRoot string) (*RunnerSnapshotCoordinator, error) {
	guard, err := openResourceGuard(serviceRoot)
	if err != nil {
		return nil, err
	}
	return &RunnerSnapshotCoordinator{guard: guard}, nil
}

// WithSnapshot reserves exactly one global snapshot slot before invoking
// operation and releases it on every return path. It deliberately reserves no
// ledger-object slot: the runner's authoritative writable ledger is already
// open for its full process lifetime and is not a service read object.
func (c *RunnerSnapshotCoordinator) WithSnapshot(ctx context.Context, operation func() error) (err error) {
	if c == nil || c.guard == nil || ctx == nil || operation == nil {
		return ErrIntegrity
	}
	lease, err := c.guard.acquire(ctx, 0, 1)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lease.Close()) }()
	return operation()
}

func (c *RunnerSnapshotCoordinator) Close() error {
	if c == nil || c.guard == nil {
		return nil
	}
	return c.guard.Close()
}

func NewGuardedReadModel(serviceRoot string, base guardedReadModelBase) (*GuardedReadModel, error) {
	if base == nil {
		return nil, errors.New("read-model service is required")
	}
	guard, err := openResourceGuard(serviceRoot)
	if err != nil {
		return nil, err
	}
	return &GuardedReadModel{base: base, guard: guard}, nil
}

func (m *GuardedReadModel) Close() error {
	if m == nil || m.guard == nil {
		return nil
	}
	return m.guard.Close()
}

func (m *GuardedReadModel) Snapshot(ctx context.Context, runID string) (snapshot readmodel.Snapshot, err error) {
	lease, err := m.acquire(ctx, 1, 1)
	if err != nil {
		return readmodel.Snapshot{}, err
	}
	defer func() {
		if closeErr := lease.Close(); closeErr != nil {
			snapshot = readmodel.Snapshot{}
			err = errors.Join(serviceapi.ErrProjectionIntegrity, err, closeErr)
		}
	}()
	return m.base.Snapshot(ctx, runID)
}

func (m *GuardedReadModel) ReadRunProjection(ctx context.Context, runID string) (projection json.RawMessage, err error) {
	lease, err := m.acquire(ctx, 1, 1)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := lease.Close(); closeErr != nil {
			projection = nil
			err = errors.Join(serviceapi.ErrProjectionIntegrity, err, closeErr)
		}
	}()
	return m.base.ReadRunProjection(ctx, runID)
}

func (m *GuardedReadModel) ReadEventProjection(ctx context.Context, runID string, request serviceapi.PageRequestV1) (projection json.RawMessage, err error) {
	lease, err := m.acquire(ctx, 1, 1)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := lease.Close(); closeErr != nil {
			projection = nil
			err = errors.Join(serviceapi.ErrProjectionIntegrity, err, closeErr)
		}
	}()
	return m.base.ReadEventProjection(ctx, runID, request)
}

// WithExistingWritableLedgerAndSnapshot atomically reserves the writable
// ledger object, the nested read-only ledger object, and one snapshot slot.
// snapshot must be called from inside operation after any transition lease is
// acquired; it cannot escape the operation or be invoked more than once.
func (m *GuardedReadModel) WithExistingWritableLedgerAndSnapshot(ctx context.Context, runID string, operation func(*ledger.JSONLLedger, func() (readmodel.Snapshot, error)) error) (err error) {
	if operation == nil {
		return serviceapi.ErrProjectionIntegrity
	}
	lease, err := m.acquire(ctx, 2, 1)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := lease.Close(); closeErr != nil {
			err = errors.Join(serviceapi.ErrProjectionIntegrity, err, closeErr)
		}
	}()
	return m.base.WithExistingWritableLedger(ctx, runID, func(writer *ledger.JSONLLedger) error {
		var snapshotMu sync.Mutex
		called := false
		active := true
		defer func() {
			snapshotMu.Lock()
			active = false
			snapshotMu.Unlock()
		}()
		snapshot := func() (readmodel.Snapshot, error) {
			snapshotMu.Lock()
			defer snapshotMu.Unlock()
			if !active || called {
				return readmodel.Snapshot{}, serviceapi.ErrProjectionIntegrity
			}
			called = true
			return m.base.Snapshot(ctx, runID)
		}
		return operation(writer, snapshot)
	})
}

func (m *GuardedReadModel) acquire(ctx context.Context, ledgers, snapshots int) (*resourceLease, error) {
	if m == nil || m.guard == nil || m.base == nil || ctx == nil {
		return nil, serviceapi.ErrProjectionIntegrity
	}
	lease, err := m.guard.acquire(ctx, ledgers, snapshots)
	if err == nil {
		return lease, nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, errors.Join(serviceapi.ErrAuthoritativeReadBusy, err)
	}
	return nil, serviceapi.ErrProjectionIntegrity
}

var (
	_ serviceapi.RunProjectionReader   = (*GuardedReadModel)(nil)
	_ serviceapi.EventProjectionReader = (*GuardedReadModel)(nil)
)

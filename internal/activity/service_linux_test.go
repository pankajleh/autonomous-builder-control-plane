//go:build linux

package activity

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/readmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

type fakeSnapshots struct {
	mu     sync.Mutex
	events []ledger.Event
	err    error
}

func (s *fakeSnapshots) Snapshot(_ context.Context, run string) (readmodel.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	events := append([]ledger.Event(nil), s.events...)
	for i := range events {
		events[i].RunID = run
	}
	return readmodel.Snapshot{Events: events}, s.err
}
func testService(t *testing.T) (*Service, *fakeSnapshots) {
	t.Helper()
	root := t.TempDir()
	catalog := &fakeCatalog{runs: map[string]runtimecatalog.RunRegistrationV1{"run": {RunID: "run"}, "other": {RunID: "other"}}}
	signer, err := serviceapi.NewCursorSigner("activity-key", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	facts := &fakeSnapshots{}
	s, err := New(context.Background(), root, catalog, facts, signer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, facts
}

func TestActivityServiceCursorAndResumeBoundaries(t *testing.T) {
	s, facts := testService(t)
	facts.events = []ledger.Event{{EventID: "pending", StateTo: domain.StateBranchAcceptancePending, Timestamp: testTime}, {EventID: "accepted", StateTo: domain.StateBranchAccepted, Timestamp: testTime}}
	data, err := s.ReadActivity(context.Background(), "run", serviceapi.PageRequestV1{PageSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	var first Page
	json.Unmarshal(data, &first)
	if len(first.Events) != 1 || first.NextCursor == "" || first.Events[0].Status != "PROGRESS" {
		t.Fatalf("page: %s", data)
	}
	data, err = s.ReadActivity(context.Background(), "run", serviceapi.PageRequestV1{PageSize: 1, Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	var next Page
	json.Unmarshal(data, &next)
	if len(next.Events) != 1 || next.Events[0].Status != "ACCEPTED" {
		t.Fatal("ledger acceptance absent", string(data))
	}
	for _, c := range []string{first.NextCursor + "x", strings.Repeat("a", 5000)} {
		if _, err = s.ReadActivity(context.Background(), "run", serviceapi.PageRequestV1{PageSize: 1, Cursor: c}); err == nil {
			t.Fatal("tampered cursor accepted")
		}
	}
	if _, err = s.ReadActivity(context.Background(), "other", serviceapi.PageRequestV1{PageSize: 1, Cursor: first.NextCursor}); err == nil {
		t.Fatal("cross-run cursor accepted")
	}
	last := strconv.FormatUint(first.Events[0].Ordinal, 10)
	subscription, err := s.OpenActivityStream(context.Background(), "run", last)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	frame, err := subscription.Next(context.Background())
	if err != nil || frame.Ordinal != next.Events[0].Ordinal {
		t.Fatal("resume skipped durable detail", err)
	}
	for _, bad := range []string{"0", "-1", "01", "x", "999999999", " 1", strings.Repeat("9", 513)} {
		if _, err = s.OpenActivityStream(context.Background(), "run", bad); err == nil {
			t.Fatal("invalid resume", bad)
		}
	}
	if _, err = s.OpenActivityStream(context.Background(), "other", last); err == nil {
		t.Fatal("run mismatch resume accepted")
	}
	// Binding failure contributes UNKNOWN while authoritative ledger facts remain.
	data, err = s.ReadActivity(context.Background(), "run", serviceapi.PageRequestV1{PageSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	var all Page
	json.Unmarshal(data, &all)
	if len(all.Events) != 3 || all.Events[2].Status != "UNKNOWN" {
		t.Fatal("unbound run missing unknown", string(data))
	}
	expiredAt := time.Now().Add(-time.Hour)
	expired, _ := s.cursors.Sign(serviceapi.CursorKindLedger, "activity-v1:run", activityCursor{Run: "run", Generation: "gen", After: 1}, expiredAt, expiredAt.Add(time.Minute))
	if _, err = s.ReadActivity(context.Background(), "run", serviceapi.PageRequestV1{PageSize: 1, Cursor: expired}); !errors.Is(err, serviceapi.ErrInvalidCursor) {
		t.Fatal("expired cursor", err)
	}
	reg, _ := s.registration("run")
	_, gen, _, _ := s.store.read("run", reg, 0, 1)
	token, _ := s.cursors.Sign(serviceapi.CursorKindLedger, "activity-v1:run", activityCursor{Run: "run", Generation: gen + "wrong", After: first.Events[0].Ordinal}, time.Now(), time.Now().Add(time.Minute))
	if _, err = s.ReadActivity(context.Background(), "run", serviceapi.PageRequestV1{PageSize: 1, Cursor: token}); !errors.Is(err, serviceapi.ErrProjectionLineageChanged) {
		t.Fatal("generation cursor", err)
	}
}

func TestActivityExtensionFailureDoesNotMutateFacts(t *testing.T) {
	s, facts := testService(t)
	facts.events = []ledger.Event{{EventID: "accepted", StateTo: domain.StateBranchAccepted, Timestamp: testTime}}
	if _, err := s.ReadActivity(context.Background(), "run", serviceapi.PageRequestV1{PageSize: 100}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(s.store.path("run"), []byte("corrupt\n"), 0600)
	if _, err := s.ReadActivity(context.Background(), "run", serviceapi.PageRequestV1{PageSize: 100}); !errors.Is(err, serviceapi.ErrActivityUnavailable) {
		t.Fatal("corruption not closed", err)
	}
	snapshot, err := facts.Snapshot(context.Background(), "run")
	if err != nil || snapshot.Events[0].StateTo != domain.StateBranchAccepted {
		t.Fatal("activity affected source facts")
	}
	// Unsafe/missing activity storage must not prevent construction of the rest
	// of the service. The extension returns a bounded retryable error instead.
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "activity"), []byte("not directory"), 0600)
	disabled, err := New(context.Background(), root, s.resolver.Catalog, facts, s.cursors)
	if err != nil {
		t.Fatal(err)
	}
	defer disabled.Close()
	if _, err = disabled.ReadActivity(context.Background(), "run", serviceapi.PageRequestV1{PageSize: 100}); !errors.Is(err, serviceapi.ErrActivityUnavailable) {
		t.Fatal("unsafe store accepted", err)
	}
}

func TestSubscriptionStopsOnServiceCancellationAndCollectorFailure(t *testing.T) {
	s, _ := testService(t)
	stream, err := s.OpenActivityStream(context.Background(), "run", "")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if _, err = stream.Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.faults["run"] = ErrIntegrity
	s.mu.Unlock()
	if _, err = stream.Next(context.Background()); !errors.Is(err, serviceapi.ErrActivityUnavailable) {
		t.Fatal("failed collector left stream alive", err)
	}
	s.mu.Lock()
	delete(s.faults, "run")
	s.mu.Unlock()
	s.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = stream.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("service cancellation did not end stream", err)
	}
}

type snapshotFunc func(context.Context, string) (readmodel.Snapshot, error)

func (f snapshotFunc) Snapshot(ctx context.Context, run string) (readmodel.Snapshot, error) {
	return f(ctx, run)
}

func TestCollectorRecoversFromTransientSnapshotErrors(t *testing.T) {
	for _, transient := range []error{serviceapi.ErrAuthoritativeReadBusy, runtimecatalog.ErrBusy, context.DeadlineExceeded, context.Canceled} {
		t.Run(transient.Error(), func(t *testing.T) {
			s, _ := testService(t)
			var calls atomic.Int32
			failed := make(chan struct{})
			s.snapshots = snapshotFunc(func(_ context.Context, run string) (readmodel.Snapshot, error) {
				switch calls.Add(1) {
				case 1:
					return readmodel.Snapshot{}, nil // initial stream admission
				case 2:
					close(failed)
					return readmodel.Snapshot{}, transient
				default:
					return readmodel.Snapshot{Events: []ledger.Event{{RunID: run, EventID: "accepted", StateTo: domain.StateBranchAccepted, Timestamp: testTime}}}, nil
				}
			})
			stream, err := s.OpenActivityStream(context.Background(), "run", "")
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			select {
			case <-failed:
			case <-time.After(time.Second):
				t.Fatal("collector did not encounter transient error")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			for {
				frame, err := stream.Next(ctx)
				if err != nil {
					t.Fatal("transient error disabled the live stream", err)
				}
				var event Event
				if err := json.Unmarshal(frame.Data, &event); err != nil {
					t.Fatal(err)
				}
				if event.Status == "ACCEPTED" {
					break
				}
			}
			if _, err := s.ReadActivity(ctx, "run", serviceapi.PageRequestV1{PageSize: 100}); err != nil {
				t.Fatal("recovered collector left a permanent fault", err)
			}
		})
	}
}

func TestCollectorIntegrityFailureStillClosesStream(t *testing.T) {
	s, _ := testService(t)
	var calls atomic.Int32
	s.snapshots = snapshotFunc(func(context.Context, string) (readmodel.Snapshot, error) {
		if calls.Add(1) == 1 {
			return readmodel.Snapshot{}, nil
		}
		return readmodel.Snapshot{}, ErrIntegrity
	})
	stream, err := s.OpenActivityStream(context.Background(), "run", "")
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		_, err := stream.Next(ctx)
		if errors.Is(err, serviceapi.ErrActivityUnavailable) {
			break
		}
		if err != nil {
			t.Fatal("integrity failure did not close stream", err)
		}
	}
}

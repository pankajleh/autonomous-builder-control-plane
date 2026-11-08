package runadmission

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// R7, a queue at admission (Repo C BUILD_RELIABILITY.md §5.6; docs/plans/build-recovery.md §6.3). For a profile with
// queue_admissions, an admission whose request is valid and whose binding is frozen, but whose launch meets a passing
// cause, is answered 202 (the same body as today) and queued instead of refused: a durable entry in admissions/queued,
// written before the 202. abcp serve retries the queue every QueueRetryEvery, oldest first, at most QueueLaunchesPerPass
// launches a pass: the wall and the workflow database checked again, then the launch as admission's (R6's wait
// included). Registered, the entry goes; the same cause, it stays with a back-off; queued QueueWait, it expires and is
// kept QueueKeepExpired. While queued, GET /v1/runs/{run} and its routes answer 404 run_queued; once expired, 404
// admission_expired (serviceapi.AdmissionQueue).

const (
	// QueueEntryKind is the kind of a queue entry.
	QueueEntryKind = "AdmissionQueueEntryV1"
	// QueueStateQueued and QueueStateExpired are an entry's states.
	QueueStateQueued  = "QUEUED"
	QueueStateExpired = "EXPIRED"

	// The passing causes a queued admission waits on (the details' cause).
	QueueCauseBuildWall               = "build_wall_unavailable"
	QueueCauseWorkflowDatabase        = "workflow_database_unavailable"
	QueueCauseExitedBeforeRegistering = "run_exited_before_registering"
	QueueCauseLaunchFailed            = "launch_failed"

	// QueueRetryEvery is how often abcp serve retries the queue; a failed try waits QueueRetryEvery, doubling up to
	// QueueMaxBackoff.
	QueueRetryEvery = 30 * time.Second
	QueueMaxBackoff = 5 * time.Minute
	// QueueWait is how long an admission waits in the queue before it expires.
	QueueWait = 30 * time.Minute
	// QueueKeepExpired is how long an expired entry is kept, so its run answers admission_expired.
	QueueKeepExpired = 7 * 24 * time.Hour
	// QueueLaunchesPerPass bounds the launches one pass tries.
	QueueLaunchesPerPass = 4
	// MaxQueueEntryBytes bounds an entry.
	MaxQueueEntryBytes = 4 << 10
	// maxQueueEntries bounds the entries one pass reads.
	maxQueueEntries = 10000
)

// AdmissionQueueEntryV1 is one queued admission: its run, the receipt and binding it was admitted with, the cause it
// waits on, since when, how often its launch was tried, when next, and whether it expired.
type AdmissionQueueEntryV1 struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	ReceiptName   string `json:"receipt_name"`
	Cause         string `json:"cause"`
	QueuedSince   string `json:"queued_since"`
	Attempts      int    `json:"attempts"`
	NextTryAt     string `json:"next_try_at"`
	State         string `json:"state"`
	ExpiredAt     string `json:"expired_at,omitempty"`
}

func (c *Controller) queues(profileID string) bool {
	profile, ok := c.profiles[profileID]
	return ok && profile.configuration.QueueAdmissions && c.queuedFD > 0
}

func queueEntryName(runID string) string { return runID + ".json" }

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func validQueueEntry(entry AdmissionQueueEntryV1, runID string) bool {
	if entry.Kind != QueueEntryKind || entry.SchemaVersion != 1 || entry.RunID != runID || runtimecatalog.ValidateIdentifier(runID) != nil ||
		!strings.HasSuffix(entry.ReceiptName, ".json") || strings.ContainsAny(entry.ReceiptName, "/\x00") || entry.Attempts < 0 {
		return false
	}
	switch entry.Cause {
	case QueueCauseBuildWall, QueueCauseWorkflowDatabase, QueueCauseExitedBeforeRegistering, QueueCauseLaunchFailed:
	default:
		return false
	}
	for _, value := range []string{entry.QueuedSince, entry.NextTryAt} {
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			return false
		}
	}
	switch entry.State {
	case QueueStateQueued:
		return entry.ExpiredAt == ""
	case QueueStateExpired:
		_, err := time.Parse(time.RFC3339Nano, entry.ExpiredAt)
		return err == nil
	}
	return false
}

func (c *Controller) readQueueEntry(runID string) (AdmissionQueueEntryV1, bool, error) {
	if c.queuedFD <= 0 {
		return AdmissionQueueEntryV1{}, false, nil
	}
	data, found, err := readRegularAt(c.queuedFD, queueEntryName(runID), MaxQueueEntryBytes)
	if err != nil {
		return AdmissionQueueEntryV1{}, false, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	if !found {
		return AdmissionQueueEntryV1{}, false, nil
	}
	var entry AdmissionQueueEntryV1
	if strictJSON(data, &entry) != nil || !validQueueEntry(entry, runID) {
		return AdmissionQueueEntryV1{}, false, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	if canonical, _ := json.Marshal(entry); !bytes.Equal(data, canonical) {
		return AdmissionQueueEntryV1{}, false, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	return entry, true, nil
}

func (c *Controller) writeQueueEntry(entry AdmissionQueueEntryV1) error {
	data, err := json.Marshal(entry)
	if err != nil || len(data) > MaxQueueEntryBytes || !validQueueEntry(entry, entry.RunID) {
		return serviceapi.ErrUnsafeAdmissionMaterialization
	}
	if err := writeNewAtomicAt(c.queuedFD, queueEntryName(entry.RunID), data); err != nil {
		return serviceapi.ErrAdmissionUnavailable
	}
	return nil
}

func (c *Controller) removeQueueEntry(runID string) error {
	if err := syscall.Unlinkat(c.queuedFD, queueEntryName(runID)); err != nil && !errors.Is(err, syscall.ENOENT) {
		return err
	}
	return syscall.Fsync(c.queuedFD)
}

// queueAdmission queues a frozen admission whose launch met a passing cause, and answers it as admitted.
func (c *Controller) queueAdmission(response serviceapi.RunAdmissionResponseV1, receipt AdmissionReceiptV1, binding AdmissionBindingV1, cause string) (serviceapi.RunAdmissionResponseV1, error) {
	now := c.clock()
	entry := AdmissionQueueEntryV1{
		Kind: QueueEntryKind, SchemaVersion: 1, RunID: binding.RunID,
		ReceiptName: receiptKeyFor(receipt.AdmissionKind, receipt.Principal.PrincipalID, receipt.RequestID) + ".json",
		Cause:       cause, QueuedSince: formatTime(now), NextTryAt: formatTime(now.Add(QueueRetryEvery)), State: QueueStateQueued,
	}
	if err := c.writeQueueEntry(entry); err != nil {
		return serviceapi.RunAdmissionResponseV1{}, err
	}
	return response, nil
}

// QueuedRun answers serviceapi's question for a run the catalog does not know: queued (and since when, on what), or
// expired. An expired entry past QueueKeepExpired, or an unreadable one, is not there.
func (c *Controller) QueuedRun(runID string) (serviceapi.QueuedRunV1, bool) {
	if c == nil || runtimecatalog.ValidateIdentifier(runID) != nil {
		return serviceapi.QueuedRunV1{}, false
	}
	entry, found, err := c.readQueueEntry(runID)
	if err != nil || !found {
		return serviceapi.QueuedRunV1{}, false
	}
	if entry.State == QueueStateExpired {
		if expired, _ := time.Parse(time.RFC3339Nano, entry.ExpiredAt); c.clock().Sub(expired) >= QueueKeepExpired {
			return serviceapi.QueuedRunV1{}, false
		}
	}
	return serviceapi.QueuedRunV1{Expired: entry.State == QueueStateExpired, QueuedSince: entry.QueuedSince, Cause: entry.Cause}, true
}

// RunQueue retries the queue every QueueRetryEvery until ctx ends.
func (c *Controller) RunQueue(ctx context.Context) {
	if c == nil || c.queuedFD <= 0 {
		return
	}
	ticker := time.NewTicker(QueueRetryEvery)
	defer ticker.Stop()
	for {
		c.QueuePass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// QueuePass is one pass over the queue: expired entries past their keep removed, queued ones past QueueWait expired,
// and at most QueueLaunchesPerPass due ones, oldest first, launched once their cause clears. It reports how many runs
// it saw registered (their entries removed).
func (c *Controller) QueuePass(ctx context.Context) int {
	if c == nil || c.queuedFD <= 0 {
		return 0
	}
	names, err := readDirAt(c.queuedFD)
	if err != nil {
		return 0
	}
	now := c.clock()
	var due []AdmissionQueueEntryV1
	for index, name := range names {
		if index >= maxQueueEntries || !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".") {
			continue
		}
		runID := strings.TrimSuffix(name, ".json")
		entry, found, err := c.readQueueEntry(runID)
		if err != nil || !found {
			continue
		}
		since, _ := time.Parse(time.RFC3339Nano, entry.QueuedSince)
		switch {
		case entry.State == QueueStateExpired:
			if expired, _ := time.Parse(time.RFC3339Nano, entry.ExpiredAt); now.Sub(expired) >= QueueKeepExpired {
				_ = c.removeQueueEntry(runID)
			}
		case now.Sub(since) >= QueueWait:
			if registered, _ := c.queuedRunRegistered(entry); registered {
				_ = c.removeQueueEntry(runID)
				continue
			}
			entry.State, entry.ExpiredAt = QueueStateExpired, formatTime(now)
			_ = c.writeQueueEntry(entry)
		default:
			if next, _ := time.Parse(time.RFC3339Nano, entry.NextTryAt); !now.Before(next) {
				due = append(due, entry)
			}
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].QueuedSince < due[j].QueuedSince })
	registered, tried := 0, 0
	for _, entry := range due {
		if tried >= QueueLaunchesPerPass || ctx.Err() != nil {
			break
		}
		done, launched := c.tryQueued(ctx, entry)
		if launched {
			tried++
		}
		if done {
			registered++
		}
	}
	return registered
}

// queuedRunRegistered reads the queued admission's receipt and binding again and asks the catalog for its run.
func (c *Controller) queuedRunRegistered(entry AdmissionQueueEntryV1) (bool, error) {
	_, binding, err := c.queuedBinding(entry)
	if err != nil {
		return false, err
	}
	return c.runRegistered(entry.RunID, binding.runBinding())
}

func (c *Controller) queuedBinding(entry AdmissionQueueEntryV1) (AdmissionReceiptV1, AdmissionBindingV1, error) {
	data, found, err := readRegularAt(c.admissionsFD, entry.ReceiptName, MaxReceiptBytes)
	if err != nil || !found {
		return AdmissionReceiptV1{}, AdmissionBindingV1{}, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	var stored AdmissionReceiptV1
	if strictJSON(data, &stored) != nil || stored.RunID != entry.RunID {
		return AdmissionReceiptV1{}, AdmissionBindingV1{}, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	receipt, found, err := c.readAdmissionReceipt(stored)
	if err != nil || !found {
		return AdmissionReceiptV1{}, AdmissionBindingV1{}, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	binding, found, err := c.readAdmissionBinding(receipt)
	if err != nil || !found || binding.RunID != entry.RunID {
		return AdmissionReceiptV1{}, AdmissionBindingV1{}, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	return receipt, binding, nil
}

// tryQueued tries one due entry: done when its run is registered (the entry removed); launched when a launch was
// started. Any other outcome leaves the entry with its cause and a back-off.
func (c *Controller) tryQueued(ctx context.Context, entry AdmissionQueueEntryV1) (done, launched bool) {
	_, binding, err := c.queuedBinding(entry)
	if err != nil {
		return false, false
	}
	if registered, err := c.runRegistered(entry.RunID, binding.runBinding()); err != nil {
		return false, false
	} else if registered {
		return c.removeQueueEntry(entry.RunID) == nil, false
	}
	lock, acquired, err := acquireLaunchLock(c.launchesFD, entry.RunID)
	if err != nil || !acquired {
		// A run of it is starting (its launch holds the lock): the next pass sees it registered.
		return false, false
	}
	retry := func(cause string) (bool, bool) {
		lock.Close()
		entry.Attempts++
		entry.Cause = cause
		backoff := QueueRetryEvery << min(entry.Attempts-1, 4)
		entry.NextTryAt = formatTime(c.clock().Add(min(backoff, QueueMaxBackoff)))
		_ = c.writeQueueEntry(entry)
		return false, launched
	}
	// A launch intent left by an earlier try whose run died before registering: the lock is free, so it is gone.
	if attempted, err := c.readAdmissionLaunchIntent(binding); err != nil {
		lock.Close()
		return false, false
	} else if attempted {
		if err := c.clearAdmissionLaunchIntent(binding); err != nil {
			lock.Close()
			return false, false
		}
	}
	if err := c.requireWall(ctx); err != nil {
		return retry(QueueCauseBuildWall)
	}
	if c.workflowProbe != nil {
		if err := c.workflowProbe(ctx, binding.WorkflowAuthorityConfigPath); err != nil {
			return retry(QueueCauseWorkflowDatabase)
		}
	}
	launched = true
	cause, err := c.launch(ctx, binding, lock)
	if err != nil {
		if cause == "" {
			cause = entry.Cause
		}
		entry.Attempts++
		entry.Cause = cause
		backoff := QueueRetryEvery << min(entry.Attempts-1, 4)
		entry.NextTryAt = formatTime(c.clock().Add(min(backoff, QueueMaxBackoff)))
		_ = c.writeQueueEntry(entry)
		return false, true
	}
	if registered, _ := c.runRegistered(entry.RunID, binding.runBinding()); registered {
		return c.removeQueueEntry(entry.RunID) == nil, true
	}
	// Still starting when R6's wait ended: the next pass sees it registered (or its lock free again).
	entry.Attempts++
	entry.NextTryAt = formatTime(c.clock().Add(QueueRetryEvery))
	_ = c.writeQueueEntry(entry)
	return false, true
}

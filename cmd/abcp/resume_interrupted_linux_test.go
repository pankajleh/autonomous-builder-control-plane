//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/actioncontrol"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/readmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/recovery"
	runctl "github.com/pankajleh/autonomous-builder-control-plane/internal/run"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// R6: SIGTERM ends abcp run's context with the cause a resumable interruption is told by; SIGINT with another.
func TestRunSignalsEndTheRunWithTheSignalsCause(t *testing.T) {
	for _, test := range []struct {
		signal os.Signal
		want   string
	}{{syscall.SIGTERM, "abcp run received SIGTERM"}, {os.Interrupt, "abcp run received interrupt"}} {
		signals := make(chan os.Signal, 1)
		ctx, stop := cancelOnSignal(context.Background(), signals)
		signals <- test.signal
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("the signal did not end the run's context")
		}
		if cause := context.Cause(ctx); cause == nil || cause.Error() != test.want {
			t.Fatalf("%v: cause = %v", test.signal, cause)
		}
		stop()
		stop()
	}
	signals := make(chan os.Signal, 1)
	ctx, stop := cancelOnSignal(context.Background(), signals)
	stop()
	if ctx.Err() == nil || strings.Contains(context.Cause(ctx).Error(), "SIGTERM") {
		t.Fatalf("a stopped run's context = %v", context.Cause(ctx))
	}
}

// abcp run --resume-interrupted is never a human decision's resume too, and takes catalog ownership over, so it needs
// the service root.
func TestRunResumeInterruptedNeedsTheServiceRootAndNoOtherResume(t *testing.T) {
	var stdout, stderr bytes.Buffer
	base := []string{"run", "--manifest", "manifest.json", "--ledger", "events.jsonl", "--evidence-root", "evidence"}
	if code := runCLI(append(append([]string{}, base...), "--resume", "--resume-interrupted", "--service-root", "/service"), &stdout, &stderr); code != 2 {
		t.Fatalf("both resumes = %d %s", code, stderr.String())
	}
	stderr.Reset()
	if code := runCLI(append(append([]string{}, base...), "--resume-interrupted"), &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "--service-root is required") {
		t.Fatalf("a resume without the service root = %d %s", code, stderr.String())
	}
}

// resumeServiceFixture is a registered run (as abcp run registers one) in a service root of its own, its ledger at
// IMPLEMENTING.
type resumeServiceFixture struct {
	serviceRoot string
	governed    authority.Authority
	events      *ledger.JSONLLedger
	evidence    string
}

func newResumeServiceFixture(t *testing.T) resumeServiceFixture {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repository")
	gitCommand(t, "", "init", "-b", "main", repository)
	gitCommand(t, repository, "config", "user.email", "resume@example.test")
	gitCommand(t, repository, "config", "user.name", "Resume Test")
	remoteURL := "https://example.test/example/resume.git"
	gitCommand(t, repository, "remote", "add", "origin", remoteURL)
	planPath := filepath.Join(repository, "plan.md")
	writeCLIFile(t, planPath, []byte("# plan\n"), 0o600)
	gitCommand(t, repository, "add", "plan.md")
	gitCommand(t, repository, "commit", "-m", "plan")
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	acceptancePath := filepath.Join(t.TempDir(), "acceptance-validator")
	writeCLIFile(t, acceptancePath, []byte("#!/bin/sh\nexit 0\n"), 0o700)
	governed, err := authority.New(authority.Manifest{
		RunID:         "interrupted-run",
		Repository:    authority.RepositoryManifest{Path: repository, Identity: "example/resume", Remotes: map[string]string{"origin": remoteURL}, DefaultBranch: "main", StartSHA: gitCommand(t, repository, "rev-parse", "HEAD")},
		Plan:          authority.PlanManifest{Path: planPath, SHA256: cliFileHash(t, planPath)},
		Ralphex:       authority.RalphexManifest{BinaryPath: truePath, BinarySHA256: cliFileHash(t, truePath), Mode: ralphex.ModeFull, Timeout: "5s", WaitOnLimit: "0s"},
		Acceptance:    []authority.AcceptanceCommand{{Required: true, Timeout: "5s", Argv: []string{acceptancePath}}},
		PolicyVersion: "resume-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	events, err := ledger.NewJSONLLedger(filepath.Join(root, "ledger", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { events.Close() })
	evidence := filepath.Join(root, "evidence", governed.RunID())
	if err := os.MkdirAll(evidence, 0o700); err != nil {
		t.Fatal(err)
	}
	f := resumeServiceFixture{serviceRoot: filepath.Join(root, "service"), governed: governed, events: events, evidence: evidence}
	at := time.Now().UTC().Add(-time.Minute)
	created, _ := ledger.NewEvent(governed.RunID(), string(domain.StateRunCreated), "control-plane", "governed-runner")
	created.Timestamp, created.Payload = at, map[string]any{"state": domain.StateRunCreated}
	if err := events.Append(created); err != nil {
		t.Fatal(err)
	}
	from := domain.StateRunCreated
	for _, to := range []domain.State{domain.StateAuthorityValidated, domain.StateExecutionStarting, domain.StateImplementing} {
		event, _ := ledger.NewEvent(governed.RunID(), "STATE_TRANSITION", "control-plane", "governed-runner")
		event.Timestamp, event.StateFrom, event.StateTo = at, from, to
		if err := events.Append(event); err != nil {
			t.Fatal(err)
		}
		from = to
	}
	return f
}

// ownerOf installs the run's owner lease for a process of its own and answers how to end that process (its owner
// killed outright).
func (f resumeServiceFixture) ownerOf(t *testing.T) (runtimecatalog.ActiveOwnerLeaseV1, func()) {
	t.Helper()
	catalog, owner, err := registerRuntimeOwner(f.serviceRoot, f.governed, f.events, f.evidence)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	if err := retireRuntimeOwner(catalog, owner); err != nil {
		t.Fatal(err)
	}
	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	identity, err := recovery.CaptureProcessIdentity(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := catalog.InstallOwnerLease(f.governed.RunID(), runctl.DerivedTransitionProvenance(f.governed).AttemptID, identity)
	if err != nil {
		t.Fatal(err)
	}
	killed := false
	kill := func() {
		if !killed {
			killed = true
			child.Process.Kill()
			child.Wait()
		}
	}
	t.Cleanup(kill)
	return lease, kill
}

func (f resumeServiceFixture) lease(t *testing.T) runtimecatalog.ActiveOwnerLeaseV1 {
	t.Helper()
	catalog, err := runtimecatalog.Open(f.serviceRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	lease, found, err := readOwnerLease(catalog, f.governed.RunID())
	if err != nil || !found {
		t.Fatalf("owner lease: %v %v", found, err)
	}
	return lease
}

// R6: abcp run --resume-interrupted takes the run's ownership over only from an owner whose process is gone: it retires
// that generation, and abcp run then installs the next one with its own watcher. A live owner is refused, and so is a
// dead one a stop is still waiting for (a run its owner stopped is never resumed).
func TestAResumeTakesOverOnlyAnOwnerThatIsGone(t *testing.T) {
	f := newResumeServiceFixture(t)
	lease, kill := f.ownerOf(t)
	if err := takeOverInterruptedOwner(context.Background(), f.serviceRoot, f.governed.RunID()); !errors.Is(err, runctl.ErrNotResumable) || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("a takeover from a live owner = %v", err)
	}
	if current := f.lease(t); current.LeaseID != lease.LeaseID || current.State != runtimecatalog.OwnerLeaseActive {
		t.Fatalf("the live owner's lease = %+v", current)
	}
	kill()
	if err := takeOverInterruptedOwner(context.Background(), f.serviceRoot, f.governed.RunID()); err != nil {
		t.Fatalf("a takeover from a dead owner = %v", err)
	}
	if current := f.lease(t); current.LeaseID != lease.LeaseID || current.State != runtimecatalog.OwnerLeaseRetired || current.DrainThroughJournalSequence == nil {
		t.Fatalf("the dead owner's lease = %+v", current)
	}
	catalog, next, err := registerRuntimeOwner(f.serviceRoot, f.governed, f.events, f.evidence)
	if err != nil {
		t.Fatalf("the resumed owner's registration = %v", err)
	}
	defer catalog.Close()
	if next.LeaseGeneration != lease.LeaseGeneration+1 || runtimecatalog.VerifyLiveOwner(next) != nil {
		t.Fatalf("the resumed owner = %+v", next)
	}
	// A retired lease (abcp run closed it after a SIGTERM) needs nothing.
	if err := retireRuntimeOwner(catalog, next); err != nil {
		t.Fatal(err)
	}
	if err := takeOverInterruptedOwner(context.Background(), f.serviceRoot, f.governed.RunID()); err != nil {
		t.Fatalf("a takeover from a retired owner = %v", err)
	}

	t.Run("a stop is waiting for the dead owner", func(t *testing.T) {
		f := newResumeServiceFixture(t)
		lease, kill := f.ownerOf(t)
		journal, err := actioncontrol.Open(f.serviceRoot)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte("stop"))
		input := actioncontrol.ReceiptInput{PrincipalID: "service-principal", PrincipalType: "service", RequestID: "stop-1", RequestSHA256: hex.EncodeToString(sum[:]),
			Action: "cancel", RunID: f.governed.RunID(), AttemptID: runctl.DerivedTransitionProvenance(f.governed).AttemptID, ExpectedState: "IMPLEMENTING",
			ExpectedRevision: hex.EncodeToString(sum[:]), Payload: json.RawMessage(`{}`), StateTransitionID: "transition-event-1"}
		if _, _, err := journal.CreateReceipt(context.Background(), input, func(uint64) (actioncontrol.AdmissionBinding, error) {
			return actioncontrol.AdmissionBinding{OwnerLeaseID: lease.LeaseID}, nil
		}); err != nil {
			t.Fatal(err)
		}
		journal.Close()
		kill()
		if err := takeOverInterruptedOwner(context.Background(), f.serviceRoot, f.governed.RunID()); !errors.Is(err, runctl.ErrNotResumable) || !strings.Contains(err.Error(), "a stop was asked") {
			t.Fatalf("a takeover with a stop waiting = %v", err)
		}
		if current := f.lease(t); current.State != runtimecatalog.OwnerLeaseActive {
			t.Fatalf("the stopped run's lease = %+v", current)
		}
	})
}

// abcp serve's resume pass reads a registered run from its ledger and its owner lease: resumable once its owner is gone.
func TestTheResumePassReadsARunsLedgerAndOwner(t *testing.T) {
	f := newResumeServiceFixture(t)
	_, kill := f.ownerOf(t)
	catalog, err := runtimecatalog.Open(f.serviceRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	signer, err := serviceapi.NewCursorSigner("resume-test", []byte(strings.Repeat("k", 32)))
	if err != nil {
		t.Fatal(err)
	}
	states, err := readmodel.New(catalog, signer)
	if err != nil {
		t.Fatal(err)
	}
	runs := interruptedRuns{catalog: catalog, states: states}
	page, _, err := runs.ListRuns("", 10)
	if err != nil || len(page) != 1 || page[0].RunID != f.governed.RunID() {
		t.Fatalf("runs = %+v, %v", page, err)
	}
	facts, err := runs.InterruptedRun(context.Background(), f.governed.RunID())
	if err != nil || facts.Interruption.State != domain.StateImplementing || !facts.Interruption.Resumable() || !facts.OwnerLive {
		t.Fatalf("a run whose owner lives = %+v, %v", facts, err)
	}
	kill()
	facts, err = runs.InterruptedRun(context.Background(), f.governed.RunID())
	if err != nil || !facts.Interruption.Resumable() || facts.OwnerLive || facts.Interruption.Attempts != 1 {
		t.Fatalf("a run whose owner is gone = %+v, %v", facts, err)
	}
}

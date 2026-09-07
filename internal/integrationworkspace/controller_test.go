package integrationworkspace

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
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/scheduler"
)

func TestIntegrateCleanCandidatesInGovernedOrderWithoutMutatingSource(t *testing.T) {
	repository := newRepository(t)
	baseline := commitFile(t, repository, "base.txt", "base\n", "baseline")
	headA := branchCommit(t, repository, baseline, "candidate-a", "a.txt", "candidate a\n")
	headB := branchCommit(t, repository, baseline, "candidate-b", "b.txt", "candidate b\n")
	git(t, repository, "checkout", "--quiet", "main")

	candidateA := acceptedCandidate(t, repository, "candidate-a", baseline, headA, "run-a", time.Date(2026, 9, 6, 1, 0, 0, 0, time.UTC))
	candidateB := acceptedCandidate(t, repository, "candidate-b", baseline, headB, "run-b", time.Date(2026, 9, 6, 2, 0, 0, 0, time.UTC))
	report := riskReport(t, []scheduler.AcceptedCandidate{candidateB, candidateA})

	beforeHead := git(t, repository, "rev-parse", "HEAD")
	beforeStatus := git(t, repository, "status", "--porcelain=v1", "--untracked-files=all")
	beforeA := git(t, repository, "rev-parse", "refs/heads/candidate-a")
	beforeB := git(t, repository, "rev-parse", "refs/heads/candidate-b")
	temporaryRoot := t.TempDir()
	store := evidenceStore(t)
	controller := newTestController(t, temporaryRoot, store)

	result, err := controller.Integrate(context.Background(), Request{
		BaselineSHA: baseline, RiskReport: report, EvidencePrefix: "clean",
	})
	if err != nil {
		t.Fatalf("Integrate() error = %v, result = %s", err, result.CanonicalJSON())
	}
	if result.Status() != StatusClean {
		t.Fatalf("Status() = %q, want %q", result.Status(), StatusClean)
	}
	steps := result.Steps()
	if len(steps) != 2 || steps[0].Candidate.Key() != candidateA.Key() || steps[1].Candidate.Key() != candidateB.Key() {
		t.Fatalf("steps are not in governed order: %#v", steps)
	}
	for index, step := range steps {
		if step.Outcome != StepMerged || step.BeforeSHA == "" || step.AfterSHA == "" || step.CommandStart >= step.CommandEnd || step.CommandEnd > len(result.Commands()) {
			t.Fatalf("step %d = %#v, want completed merge", index, step)
		}
	}
	if got := git(t, repository, "rev-parse", "HEAD"); got != beforeHead {
		t.Fatalf("source HEAD changed from %s to %s", beforeHead, got)
	}
	if got := git(t, repository, "status", "--porcelain=v1", "--untracked-files=all"); got != beforeStatus {
		t.Fatalf("source status changed from %q to %q", beforeStatus, got)
	}
	if got := git(t, repository, "rev-parse", "refs/heads/candidate-a"); got != beforeA {
		t.Fatalf("candidate-a changed from %s to %s", beforeA, got)
	}
	if got := git(t, repository, "rev-parse", "refs/heads/candidate-b"); got != beforeB {
		t.Fatalf("candidate-b changed from %s to %s", beforeB, got)
	}
	assertEvidence(t, result.CaptureRef(), captureEvidenceKind)
	assertEvidence(t, result.CleanupRef(), cleanupEvidenceKind)
	if !result.Cleanup().WorkspaceRemoved {
		t.Fatal("workspace was not reported removed")
	}
	entries, err := os.ReadDir(temporaryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary root contains leftovers: %#v", entries)
	}

	// Accessors must not expose mutable result storage.
	commands := result.Commands()
	commands[0].Argv[0] = "changed"
	commands[0].Stdout = append(commands[0].Stdout, 'x')
	if result.Commands()[0].Argv[0] != "git" || bytes.Equal(commands[0].Stdout, result.Commands()[0].Stdout) {
		t.Fatal("command evidence was mutable through an accessor")
	}
}

func TestIntegrateCommitIdentityIsIndependentOfWallClockSecond(t *testing.T) {
	repository := newRepository(t)
	baseline := commitFile(t, repository, "base.txt", "base\n", "baseline")
	headA := branchCommit(t, repository, baseline, "candidate-a", "a.txt", "candidate a\n")
	headB := branchCommit(t, repository, baseline, "candidate-b", "b.txt", "candidate b\n")
	git(t, repository, "checkout", "--quiet", "main")

	candidateA := acceptedCandidate(t, repository, "candidate-a", baseline, headA, "run-a", time.Date(2026, 9, 6, 1, 0, 0, 0, time.UTC))
	candidateB := acceptedCandidate(t, repository, "candidate-b", baseline, headB, "run-b", time.Date(2026, 9, 6, 2, 0, 0, 0, time.UTC))
	report := riskReport(t, []scheduler.AcceptedCandidate{candidateB, candidateA})
	controller := newTestController(t, t.TempDir(), evidenceStore(t))

	first, err := controller.Integrate(context.Background(), Request{
		BaselineSHA: baseline, RiskReport: report, EvidencePrefix: "first-second",
	})
	if err != nil {
		t.Fatalf("first Integrate() error = %v", err)
	}
	firstCompletedAt := time.Now()
	for time.Now().Unix() == firstCompletedAt.Unix() {
		time.Sleep(time.Millisecond)
	}
	second, err := controller.Integrate(context.Background(), Request{
		BaselineSHA: baseline, RiskReport: report, EvidencePrefix: "later-second",
	})
	if err != nil {
		t.Fatalf("second Integrate() error = %v", err)
	}

	firstSteps := first.Steps()
	secondSteps := second.Steps()
	if len(firstSteps) != 2 || len(secondSteps) != 2 {
		t.Fatalf("integration step counts = (%d, %d), want (2, 2)", len(firstSteps), len(secondSteps))
	}
	for index := range firstSteps {
		if firstSteps[index].BeforeSHA != secondSteps[index].BeforeSHA || firstSteps[index].AfterSHA != secondSteps[index].AfterSHA {
			t.Fatalf("step %d commit identity changed across wall-clock seconds: (%s, %s) != (%s, %s)", index, firstSteps[index].BeforeSHA, firstSteps[index].AfterSHA, secondSteps[index].BeforeSHA, secondSteps[index].AfterSHA)
		}
	}
	if first.Status() != second.Status() || first.RiskReportSHA256() != second.RiskReportSHA256() {
		t.Fatalf("governed result identity changed: status (%q, %q), risk report (%q, %q)", first.Status(), second.Status(), first.RiskReportSHA256(), second.RiskReportSHA256())
	}
}

func TestUseMaterializedReproducesBoundResultAndAlwaysCleans(t *testing.T) {
	repository := newRepository(t)
	baseline := commitFile(t, repository, "base.txt", "base\n", "baseline")
	head := branchCommit(t, repository, baseline, "candidate", "candidate.txt", "candidate\n")
	git(t, repository, "checkout", "--quiet", "main")
	candidate := acceptedCandidate(t, repository, "candidate", baseline, head, "run", time.Now().UTC())
	report := riskReport(t, []scheduler.AcceptedCandidate{candidate})
	temporaryRoot := t.TempDir()
	controller := newTestController(t, temporaryRoot, evidenceStore(t))
	request := Request{BaselineSHA: baseline, RiskReport: report, EvidencePrefix: "bound"}
	result, err := controller.Integrate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}

	var materializedPath string
	materialization, err := controller.UseMaterialized(context.Background(), request, result, "replay", func(target MaterializedTarget) error {
		materializedPath = target.RepositoryPath
		if target.HeadSHA != result.Steps()[0].AfterSHA || target.Branch != "integration" || len(target.Evidence) != 3 {
			t.Fatalf("materialized target = %#v", target)
		}
		if got := git(t, target.RepositoryPath, "rev-parse", "HEAD"); got != target.HeadSHA {
			t.Fatalf("materialized HEAD = %s", got)
		}
		return errors.New("callback failure")
	})
	if err == nil || !strings.Contains(err.Error(), "callback failure") {
		t.Fatalf("UseMaterialized error = %v", err)
	}
	if _, err := os.Lstat(materializedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("materialized path was not removed: %v", err)
	}
	assertEvidence(t, materialization.CaptureRef, materializeEvidenceKind)
	assertEvidence(t, materialization.CleanupRef, materializeCleanupEvidenceKind)
}

func TestUseMaterializedDoesNotExposeUnverifiedPublishedCapture(t *testing.T) {
	for _, mode := range []string{"mismatched", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			repository := newRepository(t)
			baseline := commitFile(t, repository, "base.txt", "base\n", "baseline")
			head := branchCommit(t, repository, baseline, "candidate", "candidate.txt", "candidate\n")
			git(t, repository, "checkout", "--quiet", "main")
			candidate := acceptedCandidate(t, repository, "candidate", baseline, head, "run", time.Now().UTC())
			report := riskReport(t, []scheduler.AcceptedCandidate{candidate})
			store := evidenceStore(t)
			writer := &corruptingEvidenceWriter{ArtifactWriter: store, suffix: "-materialization.json", mode: mode}
			controller := newTestController(t, t.TempDir(), writer)
			request := Request{BaselineSHA: baseline, RiskReport: report, EvidencePrefix: "bound"}
			result, err := controller.Integrate(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			called := false
			materialization, err := controller.UseMaterialized(context.Background(), request, result, "adversarial", func(MaterializedTarget) error {
				called = true
				return nil
			})
			if err == nil || called {
				t.Fatalf("unverified materialization result = %#v, called = %v, err = %v", materialization, called, err)
			}
			if materialization.CaptureRef.URI != "" {
				t.Fatalf("unverified capture escaped in result: %#v", materialization.CaptureRef)
			}
			assertEvidence(t, materialization.CleanupRef, materializeCleanupEvidenceKind)
			cleanupBytes, readErr := os.ReadFile(materialization.CleanupRef.URI)
			if readErr != nil {
				t.Fatal(readErr)
			}
			var cleanup struct {
				CaptureRef ledger.EvidenceRef `json:"capture_ref"`
			}
			if err := json.Unmarshal(cleanupBytes, &cleanup); err != nil {
				t.Fatal(err)
			}
			if cleanup.CaptureRef.URI != "" {
				t.Fatalf("cleanup evidence exposed rejected capture ref: %s", cleanupBytes)
			}
		})
	}
}

func TestUseMaterializedRejectsForgedResultAndFailsClosedOnCleanupFailure(t *testing.T) {
	repository := newRepository(t)
	baseline := commitFile(t, repository, "base.txt", "base\n", "baseline")
	head := branchCommit(t, repository, baseline, "candidate", "candidate.txt", "candidate\n")
	git(t, repository, "checkout", "--quiet", "main")
	candidate := acceptedCandidate(t, repository, "candidate", baseline, head, "run", time.Now().UTC())
	report := riskReport(t, []scheduler.AcceptedCandidate{candidate})
	temporaryRoot := t.TempDir()
	controller := newTestController(t, temporaryRoot, evidenceStore(t))
	request := Request{BaselineSHA: baseline, RiskReport: report, EvidencePrefix: "bound"}
	result, err := controller.Integrate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}

	forged := result
	forged.record.Steps[0].AfterSHA = strings.Repeat("0", 40)
	if _, err := controller.UseMaterialized(context.Background(), request, forged, "forged", func(MaterializedTarget) error { return nil }); err == nil || !strings.Contains(err.Error(), "does not match bound Track B result") {
		t.Fatalf("forged result error = %v", err)
	}

	controller.removeAll = func(path string) error {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		return errors.New("reported cleanup failure")
	}
	materialization, err := controller.UseMaterialized(context.Background(), request, result, "cleanup-failure", func(MaterializedTarget) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "reported cleanup failure") || materialization.CleanupRef.URI != "" {
		t.Fatalf("cleanup failure result = %#v, err = %v", materialization, err)
	}
	if entries, readErr := os.ReadDir(temporaryRoot); readErr != nil || len(entries) != 0 {
		t.Fatalf("cleanup failure left target: %v, %v", entries, readErr)
	}
}

func TestIntegrateCanonicalResultAndCaptureAreReproducibleAcrossWorkspaces(t *testing.T) {
	repository := newRepository(t)
	baseline := commitFile(t, repository, "base.txt", "base\n", "baseline")
	headA := branchCommit(t, repository, baseline, "candidate-a", "a.txt", "candidate a\n")
	headB := branchCommit(t, repository, baseline, "candidate-b", "b.txt", "candidate b\n")
	git(t, repository, "checkout", "--quiet", "main")

	candidateA := acceptedCandidate(t, repository, "candidate-a", baseline, headA, "run-a", time.Date(2026, 9, 6, 1, 0, 0, 0, time.UTC))
	candidateB := acceptedCandidate(t, repository, "candidate-b", baseline, headB, "run-b", time.Date(2026, 9, 6, 2, 0, 0, 0, time.UTC))
	report := riskReport(t, []scheduler.AcceptedCandidate{candidateB, candidateA})
	firstRoot, secondRoot := t.TempDir(), t.TempDir()
	firstStore, secondStore := evidenceStore(t), evidenceStore(t)

	first, err := newTestController(t, firstRoot, firstStore).Integrate(context.Background(), Request{
		BaselineSHA: baseline, RiskReport: report, EvidencePrefix: "first-run",
	})
	if err != nil {
		t.Fatalf("first Integrate() error = %v", err)
	}
	second, err := newTestController(t, secondRoot, secondStore).Integrate(context.Background(), Request{
		BaselineSHA: baseline, RiskReport: report, EvidencePrefix: "second-run",
	})
	if err != nil {
		t.Fatalf("second Integrate() error = %v", err)
	}

	if first.SHA256() != second.SHA256() || !bytes.Equal(first.CanonicalJSON(), second.CanonicalJSON()) {
		t.Fatalf("canonical result changed across disposable workspaces:\n%s\n%s", first.CanonicalJSON(), second.CanonicalJSON())
	}
	if first.CaptureRef().SHA256 != second.CaptureRef().SHA256 {
		t.Fatalf("capture digest changed across disposable workspaces: %q != %q", first.CaptureRef().SHA256, second.CaptureRef().SHA256)
	}
	firstCapture, readErr := os.ReadFile(first.CaptureRef().URI)
	if readErr != nil {
		t.Fatal(readErr)
	}
	secondCapture, readErr := os.ReadFile(second.CaptureRef().URI)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(firstCapture, secondCapture) {
		t.Fatalf("pre-cleanup capture changed across disposable workspaces:\n%s\n%s", firstCapture, secondCapture)
	}
	for _, deterministic := range [][]byte{first.CanonicalJSON(), firstCapture} {
		for _, operationalPath := range []string{firstRoot, secondRoot, firstStore.Root(), secondStore.Root()} {
			if bytes.Contains(deterministic, []byte(operationalPath)) {
				t.Fatalf("deterministic evidence contains operational path %q: %s", operationalPath, deterministic)
			}
		}
	}

	assertCleanupReceipt(t, first.CleanupRef(), firstRoot)
	assertCleanupReceipt(t, second.CleanupRef(), secondRoot)
	if first.CleanupRef().SHA256 == second.CleanupRef().SHA256 {
		t.Fatal("operational cleanup receipts unexpectedly have identical digests")
	}
}

func TestIntegrateCapturesDeterministicTextualConflictAndPreservesEvidence(t *testing.T) {
	repository := newRepository(t)
	baseline := commitFile(t, repository, "conflict.txt", "base\n", "baseline")
	headA := branchCommit(t, repository, baseline, "candidate-a", "conflict.txt", "left\n")
	headB := branchCommit(t, repository, baseline, "candidate-b", "conflict.txt", "right\n")
	git(t, repository, "checkout", "--quiet", "main")
	candidateA := acceptedCandidate(t, repository, "candidate-a", baseline, headA, "run-a", time.Date(2026, 9, 6, 1, 0, 0, 0, time.UTC))
	candidateB := acceptedCandidate(t, repository, "candidate-b", baseline, headB, "run-b", time.Date(2026, 9, 6, 2, 0, 0, 0, time.UTC))
	report := riskReport(t, []scheduler.AcceptedCandidate{candidateB, candidateA})
	temporaryRoot := t.TempDir()
	controller := newTestController(t, temporaryRoot, evidenceStore(t))

	result, err := controller.Integrate(context.Background(), Request{
		BaselineSHA: baseline, RiskReport: report, EvidencePrefix: "conflict",
	})
	if err != nil {
		t.Fatalf("Integrate() conflict error = %v", err)
	}
	if result.Status() != StatusConflict {
		t.Fatalf("Status() = %q, want %q", result.Status(), StatusConflict)
	}
	steps := result.Steps()
	if len(steps) != 2 || steps[1].Outcome != StepConflict || len(steps[1].ConflictPaths) != 1 || steps[1].ConflictPaths[0] != "conflict.txt" {
		t.Fatalf("conflict steps = %#v", steps)
	}
	paths := steps[1].ConflictPaths
	paths[0] = "changed"
	if result.Steps()[1].ConflictPaths[0] != "conflict.txt" {
		t.Fatal("conflict paths were mutable through an accessor")
	}
	assertEvidence(t, result.CaptureRef(), captureEvidenceKind)
	assertEvidence(t, result.CleanupRef(), cleanupEvidenceKind)
	if _, err := os.Stat(result.CaptureRef().URI); err != nil {
		t.Fatalf("capture did not survive cleanup: %v", err)
	}
	if entries, err := os.ReadDir(temporaryRoot); err != nil || len(entries) != 0 {
		t.Fatalf("disposable workspace was not cleaned: entries=%v err=%v", entries, err)
	}
}

func TestIntegrateRejectsMissingAcceptedCommit(t *testing.T) {
	repository := newRepository(t)
	baseline := commitFile(t, repository, "base.txt", "base\n", "baseline")
	head := branchCommit(t, repository, baseline, "candidate", "candidate.txt", "candidate\n")
	git(t, repository, "checkout", "--quiet", "main")
	candidate := acceptedCandidate(t, repository, "candidate", baseline, head, "run", time.Now().UTC())
	report := riskReport(t, []scheduler.AcceptedCandidate{candidate})
	git(t, repository, "branch", "-D", "candidate")
	git(t, repository, "reflog", "expire", "--expire=now", "--all")
	git(t, repository, "prune", "--expire=now")
	if command := exec.Command("git", "-C", repository, "cat-file", "-e", head+"^{commit}"); command.Run() == nil {
		t.Fatal("candidate object remained reachable after prune")
	}
	controller := newTestController(t, t.TempDir(), evidenceStore(t))

	result, err := controller.Integrate(context.Background(), Request{
		BaselineSHA: baseline, RiskReport: report, EvidencePrefix: "missing",
	})
	if err == nil || result.Status() != StatusUnavailable || !strings.Contains(result.Failure(), "missing or non-commit") {
		t.Fatalf("Integrate() = status %q failure %q err %v", result.Status(), result.Failure(), err)
	}
	assertEvidence(t, result.CaptureRef(), captureEvidenceKind)
}

func TestIntegrateFailsClosedOnMalformedGitOutputAndTruncation(t *testing.T) {
	repository := newRepository(t)
	baseline := commitFile(t, repository, "base.txt", "base\n", "baseline")
	head := branchCommit(t, repository, baseline, "candidate", "candidate.txt", "candidate\n")
	git(t, repository, "checkout", "--quiet", "main")
	candidate := acceptedCandidate(t, repository, "candidate", baseline, head, "run", time.Now().UTC())
	report := riskReport(t, []scheduler.AcceptedCandidate{candidate})

	t.Run("malformed", func(t *testing.T) {
		controller := newTestController(t, t.TempDir(), evidenceStore(t))
		controller.runner = staticRunner{result: gitResult{
			Stdout: []byte("ambiguous\n"), ExitCode: 0, StdoutLimitBytes: 100, StderrLimitBytes: 100,
		}}
		result, err := controller.Integrate(context.Background(), Request{BaselineSHA: baseline, RiskReport: report, EvidencePrefix: "malformed"})
		if err == nil || result.Status() != StatusUnavailable || !strings.Contains(result.Failure(), "ambiguous object verification output") {
			t.Fatalf("malformed output result = %s, err = %v", result.CanonicalJSON(), err)
		}
	})

	t.Run("truncated", func(t *testing.T) {
		store := evidenceStore(t)
		controller, err := NewController(Config{TemporaryRoot: t.TempDir(), Artifacts: store, StdoutLimitBytes: 1, StderrLimitBytes: 1024})
		if err != nil {
			t.Fatal(err)
		}
		result, err := controller.Integrate(context.Background(), Request{BaselineSHA: baseline, RiskReport: report, EvidencePrefix: "truncated"})
		if err == nil || result.Status() != StatusUnavailable || !strings.Contains(result.Failure(), "truncated") {
			t.Fatalf("truncated output result = %s, err = %v", result.CanonicalJSON(), err)
		}
		commands := result.Commands()
		if len(commands) == 0 || !commands[0].StdoutTruncated {
			t.Fatalf("truncation not captured in command evidence: %#v", commands)
		}
	})
}

func TestCleanupIsScopedAndFailsClosedWhenRemovalIsUncertain(t *testing.T) {
	repository := newRepository(t)
	baseline := commitFile(t, repository, "base.txt", "base\n", "baseline")
	head := branchCommit(t, repository, baseline, "candidate", "candidate.txt", "candidate\n")
	git(t, repository, "checkout", "--quiet", "main")
	candidate := acceptedCandidate(t, repository, "candidate", baseline, head, "run", time.Now().UTC())
	report := riskReport(t, []scheduler.AcceptedCandidate{candidate})
	temporaryRoot := t.TempDir()
	sentinel := filepath.Join(temporaryRoot, "controller-sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	controller := newTestController(t, temporaryRoot, evidenceStore(t))
	var cleanupTarget string
	controller.removeAll = func(target string) error {
		cleanupTarget = target
		return nil // Simulate an uncertain no-op cleanup.
	}

	result, err := controller.Integrate(context.Background(), Request{
		BaselineSHA: baseline, RiskReport: report, EvidencePrefix: "uncertain-cleanup",
	})
	if err == nil || result.Status() != StatusUnavailable || !strings.Contains(result.Failure(), "still exists") {
		t.Fatalf("uncertain cleanup result = %s, err = %v", result.CanonicalJSON(), err)
	}
	if filepath.Dir(cleanupTarget) != temporaryRoot || filepath.Base(cleanupTarget) == filepath.Base(sentinel) {
		t.Fatalf("cleanup target %q was not one direct disposable child", cleanupTarget)
	}
	if data, readErr := os.ReadFile(sentinel); readErr != nil || string(data) != "keep" {
		t.Fatalf("cleanup touched sibling sentinel: data=%q err=%v", data, readErr)
	}
	assertEvidence(t, result.CaptureRef(), captureEvidenceKind)
}

func TestGateLifecycleCleansWhenCapturePublicationFails(t *testing.T) {
	repository := newRepository(t)
	baseline := commitFile(t, repository, "base.txt", "base\n", "baseline")
	head := branchCommit(t, repository, baseline, "candidate", "candidate.txt", "candidate\n")
	git(t, repository, "checkout", "--quiet", "main")
	candidate := acceptedCandidate(t, repository, "candidate", baseline, head, "run", time.Now().UTC())
	report := riskReport(t, []scheduler.AcceptedCandidate{candidate})
	temporaryRoot := t.TempDir()
	writer := &selectiveFailureWriter{ArtifactWriter: evidenceStore(t), suffix: "-capture.json"}
	controller, err := NewController(Config{TemporaryRoot: temporaryRoot, Artifacts: writer, CleanupOnEvidenceFailure: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := controller.Integrate(context.Background(), Request{BaselineSHA: baseline, RiskReport: report, EvidencePrefix: "failed-capture"})
	if err == nil || result.Status() != StatusUnavailable {
		t.Fatalf("capture failure result = %s, err = %v", result.Status(), err)
	}
	if entries, readErr := os.ReadDir(temporaryRoot); readErr != nil || len(entries) != 0 {
		t.Fatalf("capture failure left workspace: %v, %v", entries, readErr)
	}
}

type selectiveFailureWriter struct {
	ArtifactWriter
	suffix string
}

type corruptingEvidenceWriter struct {
	ArtifactWriter
	suffix string
	mode   string
}

func (w *corruptingEvidenceWriter) WriteBytes(name, kind string, data []byte) (ledger.EvidenceRef, error) {
	ref, err := w.ArtifactWriter.WriteBytes(name, kind, data)
	if err != nil || !strings.HasSuffix(name, w.suffix) {
		return ref, err
	}
	switch w.mode {
	case "mismatched":
		ref.SHA256 = strings.Repeat("0", 64)
	case "oversized":
		oversized := make([]byte, maximumExistingEvidenceBytes+1)
		if err := os.WriteFile(ref.URI, oversized, 0o600); err != nil {
			return ledger.EvidenceRef{}, err
		}
		digest := sha256.Sum256(oversized)
		ref.SHA256 = hex.EncodeToString(digest[:])
	}
	return ref, nil
}

func (w *selectiveFailureWriter) WriteBytes(name, kind string, data []byte) (ledger.EvidenceRef, error) {
	if strings.HasSuffix(name, w.suffix) {
		return ledger.EvidenceRef{}, errors.New("injected publication failure")
	}
	return w.ArtifactWriter.WriteBytes(name, kind, data)
}

func TestControllerRejectsSymlinkTemporaryRoot(t *testing.T) {
	realRoot := t.TempDir()
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Fatal(err)
	}
	_, err := NewController(Config{TemporaryRoot: link, Artifacts: evidenceStore(t)})
	if err == nil || !strings.Contains(err.Error(), "non-symlink") {
		t.Fatalf("NewController() error = %v, want symlink rejection", err)
	}
}

func TestIntegrateRejectsWorkspaceRootInsideCandidateRepository(t *testing.T) {
	repository := newRepository(t)
	baseline := commitFile(t, repository, "base.txt", "base\n", "baseline")
	head := branchCommit(t, repository, baseline, "candidate", "candidate.txt", "candidate\n")
	git(t, repository, "checkout", "--quiet", "main")
	candidate := acceptedCandidate(t, repository, "candidate", baseline, head, "run", time.Now().UTC())
	report := riskReport(t, []scheduler.AcceptedCandidate{candidate})
	temporaryRoot := filepath.Join(repository, "controller-temp")
	if err := os.Mkdir(temporaryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	controller := newTestController(t, temporaryRoot, evidenceStore(t))

	result, err := controller.Integrate(context.Background(), Request{
		BaselineSHA: baseline, RiskReport: report, EvidencePrefix: "overlap",
	})
	if err == nil || result.Status() != StatusUnavailable || !strings.Contains(result.Failure(), "must be disjoint") {
		t.Fatalf("overlapping root result = %s, err = %v", result.CanonicalJSON(), err)
	}
	entries, readErr := os.ReadDir(temporaryRoot)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("overlapping temporary root was used: entries=%v err=%v", entries, readErr)
	}
}

type staticRunner struct {
	result gitResult
	err    error
}

func (runner staticRunner) Run(_ context.Context, _ string, arguments ...string) (gitResult, error) {
	result := runner.result
	result.Argv = append([]string{"git"}, arguments...)
	return result, runner.err
}

func newTestController(t *testing.T, temporaryRoot string, artifacts ArtifactWriter) *Controller {
	t.Helper()
	controller, err := NewController(Config{TemporaryRoot: temporaryRoot, Artifacts: artifacts})
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func evidenceStore(t *testing.T) *evidence.Store {
	t.Helper()
	store, err := evidence.NewStore(t.TempDir(), "integration-test")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func assertEvidence(t *testing.T, ref ledger.EvidenceRef, kind string) {
	t.Helper()
	if ref.Kind != kind || ref.URI == "" {
		t.Fatalf("evidence ref = %#v, want kind %q", ref, kind)
	}
	data, err := os.ReadFile(ref.URI)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	if ref.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("evidence digest %q does not match bytes", ref.SHA256)
	}
}

func assertCleanupReceipt(t *testing.T, ref ledger.EvidenceRef, temporaryRoot string) {
	t.Helper()
	assertEvidence(t, ref, cleanupEvidenceKind)
	data, err := os.ReadFile(ref.URI)
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		Cleanup struct {
			TemporaryRoot    string `json:"temporary_root"`
			WorkspacePath    string `json:"workspace_path"`
			WorkspaceID      string `json:"workspace_id"`
			WorkspaceRemoved bool   `json:"workspace_removed"`
		} `json:"cleanup"`
	}
	if err := json.Unmarshal(data, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Cleanup.TemporaryRoot != temporaryRoot || filepath.Dir(receipt.Cleanup.WorkspacePath) != temporaryRoot ||
		filepath.Base(receipt.Cleanup.WorkspacePath) != receipt.Cleanup.WorkspaceID || !receipt.Cleanup.WorkspaceRemoved {
		t.Fatalf("cleanup receipt does not preserve exact operational identity: %#v", receipt.Cleanup)
	}
}

func newRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	git(t, repository, "init", "--quiet", "--initial-branch=main")
	git(t, repository, "config", "user.name", "Test User")
	git(t, repository, "config", "user.email", "test@example.invalid")
	return repository
}

func commitFile(t *testing.T, repository, name, content, message string) string {
	t.Helper()
	filename := filepath.Join(repository, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, repository, "add", "--", name)
	git(t, repository, "commit", "--quiet", "-m", message)
	return git(t, repository, "rev-parse", "HEAD")
}

func branchCommit(t *testing.T, repository, baseline, branch, name, content string) string {
	t.Helper()
	git(t, repository, "checkout", "--quiet", "-b", branch, baseline)
	return commitFile(t, repository, name, content, branch)
}

func acceptedCandidate(t *testing.T, repository, branch, start, head, run string, acceptedAt time.Time) scheduler.AcceptedCandidate {
	t.Helper()
	digest := sha256.Sum256([]byte(run))
	candidate, err := scheduler.NewAcceptedCandidate(scheduler.CandidateInput{
		ProjectID: "project", PlanID: "plan-" + run, RunID: run, AttemptID: "attempt",
		Repository: repository, Branch: branch, StartSHA: start, HeadSHA: head,
		AcceptanceEvidence: []ledger.EvidenceRef{{URI: "/evidence/" + run, SHA256: hex.EncodeToString(digest[:]), Kind: "acceptance"}},
		AcceptedAt:         acceptedAt, AcceptancePolicyIdentity: "policy-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func riskReport(t *testing.T, candidates []scheduler.AcceptedCandidate) scheduler.RiskReport {
	t.Helper()
	analyzer, err := scheduler.NewAnalyzer(scheduler.RiskPolicy{PolicyIdentity: "risk-v1"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := analyzer.Analyze(context.Background(), candidates)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func git(t *testing.T, repository string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repository}, arguments...)...)
	command.Env = append(os.Environ(), "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

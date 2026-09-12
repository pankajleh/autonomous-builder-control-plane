//go:build linux

package mergelifecycle

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

func TestCorrectionC01TerminalCoreRecovery(t *testing.T) {
	t.Run("MERGED", func(t *testing.T) {
		f := newControllerFixture(t)
		provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationApplied}
		controller := f.controller(t, provider)
		defer controller.Close()
		controller.afterTerminalCore = func(core TerminalCoreV1) error {
			if core.Destination != domain.StateMerged {
				t.Fatalf("selected %s, want MERGED", core.Destination)
			}
			return errors.New("crash after durable terminal core")
		}
		if _, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID}); err == nil {
			t.Fatal("terminal-core crash unexpectedly returned success")
		}
		calls := provider.totalCalls()
		if err := controller.Close(); err != nil {
			t.Fatal(err)
		}
		recovery := f.controller(t, provider)
		defer recovery.Close()
		result, err := recovery.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
		if err != nil || result.State != domain.StateMerged || provider.totalCalls() != calls {
			t.Fatalf("MERGED recovery = %+v, %v, provider calls %d -> %d", result, err, calls, provider.totalCalls())
		}
		assertOneTerminalEvent(t, f.ledger.Path(), domain.StateMerged, CodeMergeAppliedAccepted)
	})

	t.Run("FAILED", func(t *testing.T) {
		f := newControllerFixture(t)
		provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationApplied, invalidPreparation: true}
		controller := f.controller(t, provider)
		defer controller.Close()
		controller.afterTerminalCore = func(core TerminalCoreV1) error {
			if core.Destination != domain.StateFailed || core.ReasonCode != CodeCommitPreparationFailed {
				t.Fatalf("unexpected failure selection: %+v", core)
			}
			return errors.New("crash after durable terminal core")
		}
		if _, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID}); err == nil {
			t.Fatal("terminal-core crash unexpectedly returned success")
		}
		calls := provider.totalCalls()
		if err := controller.Close(); err != nil {
			t.Fatal(err)
		}
		recovery := f.controller(t, provider)
		defer recovery.Close()
		result, err := recovery.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
		if result.State != domain.StateFailed || result.ReasonCode != CodeCommitPreparationFailed || provider.totalCalls() != calls {
			t.Fatalf("FAILED recovery = %+v, %v, provider calls %d -> %d", result, err, calls, provider.totalCalls())
		}
		assertOneTerminalEvent(t, f.ledger.Path(), domain.StateFailed, CodeCommitPreparationFailed)
	})

	t.Run("pre-admission CANCELLED", func(t *testing.T) {
		f := newControllerFixture(t)
		provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationApplied}
		controller := controllerWithCancellation(t, f, provider, cancellationGrant())
		defer controller.Close()
		controller.afterTerminalCore = func(core TerminalCoreV1) error {
			if core.Destination != domain.StateCancelled || core.ReasonCode != CodeCancelledBeforeSubmission {
				t.Fatalf("unexpected cancellation selection: %+v", core)
			}
			return errors.New("crash after durable terminal core")
		}
		if _, err := controller.Cancel(context.Background(), CancelRequest{RunID: f.runID, SourceRequestID: "c01-cancel"}); err == nil {
			t.Fatal("terminal-core crash unexpectedly returned success")
		}
		if err := controller.Close(); err != nil {
			t.Fatal(err)
		}
		recovery := controllerWithCancellation(t, f, provider, cancellationGrant())
		defer recovery.Close()
		result, err := recovery.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
		if err != nil || result.State != domain.StateCancelled || provider.totalCalls() != 0 {
			t.Fatalf("CANCELLED recovery = %+v, %v, calls=%d", result, err, provider.totalCalls())
		}
		assertOneTerminalEvent(t, f.ledger.Path(), domain.StateCancelled, CodeCancelledBeforeSubmission)
	})

	t.Run("CANCELLED event before final terminal", func(t *testing.T) {
		f := newControllerFixture(t)
		provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationApplied}
		controller := controllerWithCancellation(t, f, provider, cancellationGrant())
		controller.afterTerminalEvent = func(core TerminalCoreV1) error {
			if core.Destination != domain.StateCancelled {
				t.Fatalf("selected %s, want CANCELLED", core.Destination)
			}
			return errors.New("crash after exact terminal event")
		}
		if _, err := controller.Cancel(context.Background(), CancelRequest{RunID: f.runID, SourceRequestID: "event-cancel"}); err == nil {
			t.Fatal("terminal-event crash unexpectedly returned success")
		}
		if err := controller.Close(); err != nil {
			t.Fatal(err)
		}
		recovery := controllerWithCancellation(t, f, provider, cancellationGrant())
		defer recovery.Close()
		result, err := recovery.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
		if err != nil || result.State != domain.StateCancelled || provider.totalCalls() != 0 {
			t.Fatalf("CANCELLED event recovery = %+v, %v, calls=%d", result, err, provider.totalCalls())
		}
		assertOneTerminalEvent(t, f.ledger.Path(), domain.StateCancelled, CodeCancelledBeforeSubmission)
	})
}

func TestClosureCritical01UnresolvedSubmissionAuthorityFailure(t *testing.T) {
	tests := []struct {
		name        string
		disposition githublifecycle.ReconciliationDisposition
		crash       bool
	}{
		{name: "UNKNOWN result then restart", disposition: githublifecycle.ReconciliationUnknown},
		{name: "post-submit crash then restart", disposition: githublifecycle.ReconciliationApplied, crash: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newControllerFixture(t)
			provider := &fakeProvider{t: t, disposition: test.disposition, reconcileDisposition: githublifecycle.ReconciliationApplied}
			controller := f.controller(t, provider)
			if test.crash {
				controller.afterTargetOutcome = func(TargetOutcome) error { return errors.New("crash after durable target outcome") }
			}
			first, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
			if err == nil || !first.Unresolved || first.State != domain.StateReadyForMerge {
				t.Fatalf("initial unresolved submission = %+v, %v", first, err)
			}
			if provider.submitCalls != 1 {
				t.Fatalf("target submissions = %d, want 1", provider.submitCalls)
			}
			barrier, active, err := f.ledger.ActiveTransitionBarrier(f.runID)
			if err != nil || !active {
				t.Fatalf("post-submit barrier: active=%v err=%v", active, err)
			}
			assembled := assembleFixture(t, f)
			if barrier.AttemptID != deterministicWriteID(assembled) {
				t.Fatalf("barrier attempt = %q, want %q", barrier.AttemptID, deterministicWriteID(assembled))
			}
			attempt, err := controller.store.openAttempt(attemptKey(assembled, barrier.AttemptID))
			if err != nil {
				t.Fatal(err)
			}
			if _, found, readErr := attempt.read("target-submission.json"); readErr != nil || !found {
				t.Fatalf("durable target-submission boundary: found=%v err=%v", found, readErr)
			}
			if err := attempt.close(); err != nil {
				t.Fatal(err)
			}
			if err := controller.Close(); err != nil {
				t.Fatal(err)
			}

			policyPath := f.governed.Policy.SourceConfiguration.URI
			policyBytes, err := os.ReadFile(policyPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(policyPath); err != nil {
				t.Fatal(err)
			}
			recovery := f.controller(t, provider)
			blocked, err := recovery.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
			var lifecycleErr *Error
			if !errors.As(err, &lifecycleErr) || lifecycleErr.Code != CodeTargetUnknown || blocked.State != domain.StateReadyForMerge ||
				blocked.ReasonCode != CodeTargetUnknown || !blocked.Unresolved {
				t.Fatalf("unreadable-authority recovery = %+v, %v", blocked, err)
			}
			if provider.submitCalls != 1 || provider.reconcileCalls != 0 {
				t.Fatalf("unreadable authority touched provider: submit=%d reconcile=%d", provider.submitCalls, provider.reconcileCalls)
			}
			if _, _, _, found, findErr := recovery.store.findTerminalCore(f.runID); findErr != nil || found {
				t.Fatalf("unresolved submission selected a terminal core: found=%v err=%v", found, findErr)
			}
			assertNoTerminalEvent(t, f.ledger.Path())
			if err := recovery.Close(); err != nil {
				t.Fatal(err)
			}
			cancellation := controllerWithCancellation(t, f, provider, cancellationGrant())
			alternate, err := cancellation.Cancel(context.Background(), CancelRequest{RunID: f.runID, SourceRequestID: "unreadable-authority-cancel"})
			lifecycleErr = nil
			if !errors.As(err, &lifecycleErr) || lifecycleErr.Code != CodeTargetUnknown || alternate.State != domain.StateReadyForMerge ||
				alternate.ReasonCode != CodeTargetUnknown || !alternate.Unresolved {
				t.Fatalf("alternate cancellation recovery = %+v, %v", alternate, err)
			}
			if _, _, _, found, findErr := cancellation.store.findTerminalCore(f.runID); findErr != nil || found {
				t.Fatalf("alternate path selected a terminal core: found=%v err=%v", found, findErr)
			}
			if err := cancellation.Close(); err != nil {
				t.Fatal(err)
			}

			if err := os.WriteFile(policyPath, policyBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			settlement := f.controller(t, provider)
			defer settlement.Close()
			settled, err := settlement.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
			if err != nil || settled.State != domain.StateMerged {
				t.Fatalf("reconciliation after authority repair = %+v, %v", settled, err)
			}
			if provider.submitCalls != 1 || provider.reconcileCalls != 1 {
				t.Fatalf("settlement retried submission: submit=%d reconcile=%d", provider.submitCalls, provider.reconcileCalls)
			}
			assertOneTerminalEvent(t, f.ledger.Path(), domain.StateMerged, CodeMergeAppliedAccepted)
		})
	}

	t.Run("pre-authority settlement holds repository base lease", func(t *testing.T) {
		f := newControllerFixture(t)
		if err := os.Remove(f.governed.Policy.SourceConfiguration.URI); err != nil {
			t.Fatal(err)
		}
		controller := f.controller(t, &fakeProvider{t: t})
		defer controller.Close()
		selected := make(chan struct{})
		release := make(chan struct{})
		controller.afterTerminalCore = func(TerminalCoreV1) error {
			close(selected)
			<-release
			return nil
		}
		type executeOutcome struct {
			result Result
			err    error
		}
		executed := make(chan executeOutcome, 1)
		go func() {
			result, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
			executed <- executeOutcome{result: result, err: err}
		}()
		select {
		case <-selected:
		case <-time.After(2 * time.Second):
			t.Fatal("pre-authority terminal core was not selected")
		}

		competingStore, err := newDurableStore(f.stateRoot, productionLimits())
		if err != nil {
			t.Fatal(err)
		}
		defer competingStore.close()
		acquired := make(chan *repositoryBaseLease, 1)
		acquireErrors := make(chan error, 1)
		go func() {
			lease, acquireErr := competingStore.acquireRepositoryBase(governedRepositoryBaseLockKey(f.governed))
			if acquireErr != nil {
				acquireErrors <- acquireErr
				return
			}
			acquired <- lease
		}()
		select {
		case lease := <-acquired:
			_ = lease.close()
			t.Fatal("pre-authority settlement released repository/base serialization before terminal selection")
		case acquireErr := <-acquireErrors:
			t.Fatal(acquireErr)
		case <-time.After(75 * time.Millisecond):
		}
		close(release)
		outcome := <-executed
		if outcome.err == nil || outcome.result.State != domain.StateFailed || outcome.result.ReasonCode != CodeInvalidAuthority {
			t.Fatalf("pre-authority settlement = %+v, %v", outcome.result, outcome.err)
		}
		select {
		case lease := <-acquired:
			defer lease.close()
		case acquireErr := <-acquireErrors:
			t.Fatal(acquireErr)
		case <-time.After(2 * time.Second):
			t.Fatal("repository/base lease did not release after pre-authority settlement")
		}
	})
}

func TestCorrectionM01AuthorityEvidenceBytes(t *testing.T) {
	makeAuthority := func(t *testing.T) (GovernedAuthority, ledger.EvidenceRef, ledger.EvidenceRef, ledger.EvidenceRef) {
		t.Helper()
		root := t.TempDir()
		write := func(name, kind, value string) ledger.EvidenceRef {
			path := filepath.Join(root, name)
			data := []byte(value)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			return ledger.EvidenceRef{URI: path, SHA256: digest(data), Kind: kind}
		}
		repository := write("repository.json", "repository-binding", `{"repository":"octo-org/control-plane"}`)
		policy := write("policy.json", "merge-policy", `{"policy":"merge-v1"}`)
		ready := write("ready.json", "serial-integration-gate-decision", `{"state":"READY_FOR_MERGE"}`)
		governed := GovernedAuthority{EvidenceRoot: root, EvidenceClosure: []ledger.EvidenceRef{repository, policy, ready},
			RepositoryBinding: githublifecycle.RepositoryBindingV1Input{ConfigurationEvidence: repository},
			Policy:            PolicyDefinition{SourceConfiguration: policy}}
		return governed, repository, policy, ready
	}

	t.Run("valid exact bytes", func(t *testing.T) {
		governed, _, _, ready := makeAuthority(t)
		if err := verifyAuthorityEvidence(governed, []ledger.EvidenceRef{ready}, ready); err != nil {
			t.Fatal(err)
		}
	})

	for _, name := range []string{"missing", "digest mismatch", "symlink", "hard link", "non-regular", "over limit", "wrong repository artifact", "wrong policy artifact", "wrong READY artifact", "omitted closure member"} {
		t.Run(name, func(t *testing.T) {
			governed, repository, policy, ready := makeAuthority(t)
			switch name {
			case "missing":
				if err := os.Remove(policy.URI); err != nil {
					t.Fatal(err)
				}
			case "digest mismatch":
				if err := os.WriteFile(ready.URI, []byte("changed"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(governed.EvidenceRoot, "target")
				if err := os.WriteFile(target, []byte("target"), 0o600); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(governed.EvidenceRoot, "link")
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				policy.URI, policy.SHA256 = link, digest([]byte("target"))
				governed.Policy.SourceConfiguration, governed.EvidenceClosure[1] = policy, policy
			case "hard link":
				link := filepath.Join(governed.EvidenceRoot, "hard-link")
				if err := os.Link(policy.URI, link); err != nil {
					t.Fatal(err)
				}
				policy.URI = link
				governed.Policy.SourceConfiguration, governed.EvidenceClosure[1] = policy, policy
			case "non-regular":
				directory := filepath.Join(governed.EvidenceRoot, "directory")
				if err := os.Mkdir(directory, 0o700); err != nil {
					t.Fatal(err)
				}
				policy.URI = directory
				governed.Policy.SourceConfiguration, governed.EvidenceClosure[1] = policy, policy
			case "over limit":
				oversized := filepath.Join(governed.EvidenceRoot, "oversized")
				file, err := os.OpenFile(oversized, os.O_CREATE|os.O_RDWR, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Truncate((16 << 20) + 1); err != nil {
					t.Fatal(err)
				}
				_ = file.Close()
				policy.URI = oversized
				governed.Policy.SourceConfiguration, governed.EvidenceClosure[1] = policy, policy
			case "wrong repository artifact":
				governed.RepositoryBinding.ConfigurationEvidence = policy
				governed.EvidenceClosure = []ledger.EvidenceRef{policy, ready}
			case "wrong policy artifact":
				governed.Policy.SourceConfiguration = repository
				governed.EvidenceClosure = []ledger.EvidenceRef{repository, ready}
			case "wrong READY artifact":
				ready = policy
			case "omitted closure member":
				governed.EvidenceClosure = []ledger.EvidenceRef{repository, ready}
			}
			if err := verifyAuthorityEvidence(governed, []ledger.EvidenceRef{ready}, ready); err == nil {
				t.Fatal("invalid authority evidence was accepted")
			}
		})
	}

	// The integrated controller check must fail before any provider operation.
	f := newControllerFixture(t)
	f.governed.EvidenceClosure = f.governed.EvidenceClosure[:2]
	provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationApplied}
	controller := f.controller(t, provider)
	defer controller.Close()
	if _, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID}); err == nil || provider.totalCalls() != 0 {
		t.Fatalf("omitted closure reached provider: err=%v calls=%d", err, provider.totalCalls())
	}
}

func TestCorrectionM02ExactCommitPreparationProof(t *testing.T) {
	f := newControllerFixture(t)
	assembled := assembleFixture(t, f)
	recipe, err := githublifecycle.NewMergeCommitRecipeV1(deterministicWriteID(assembled), assembled.authority, githublifecycle.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	valid := exactCommitPreparation(recipe, "exact-object", "a")
	if err := valid.validate(recipe); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*CommitPreparation)
	}{
		{"wrong tree", func(p *CommitPreparation) { p.Observation.ResultTree = strings.Repeat("0", 40) }},
		{"parent order", func(p *CommitPreparation) {
			p.Observation.Parents[0], p.Observation.Parents[1] = p.Observation.Parents[1], p.Observation.Parents[0]
		}},
		{"one parent", func(p *CommitPreparation) { p.Observation.Parents = p.Observation.Parents[:1] }},
		{"message trailer", func(p *CommitPreparation) { p.Observation.Message += "\nchanged" }},
		{"author", func(p *CommitPreparation) { p.Observation.Author.Name = "Other" }},
		{"committer", func(p *CommitPreparation) { p.Observation.Committer.Email = "other@example.test" }},
		{"timestamp", func(p *CommitPreparation) { p.Observation.AuthorUnix++ }},
		{"object bytes", func(p *CommitPreparation) { p.Observation.ObjectBytes = append(p.Observation.ObjectBytes, '!') }},
		{"OID", func(p *CommitPreparation) { p.Observation.ResultSHA = strings.Repeat("0", 40) }},
		{"malformed observation", func(p *CommitPreparation) { p.Observation.Schema = "merge-commit-preparation-observation-v2" }},
		{"absent observation", func(p *CommitPreparation) { p.Observation = CommitPreparationObservationV1{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			candidate.Observation.Parents = append([]string(nil), valid.Observation.Parents...)
			candidate.Observation.ObjectBytes = append([]byte(nil), valid.Observation.ObjectBytes...)
			test.mutate(&candidate)
			if err := candidate.validate(recipe); err == nil {
				t.Fatal("mismatched preparation proof was accepted")
			}
		})
	}

	provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationApplied, prepareError: true}
	controller := f.controller(t, provider)
	defer controller.Close()
	result, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
	if err != nil || result.State != domain.StateMerged || provider.prepareCalls != 1 || provider.prepareReconcileCalls != 1 {
		t.Fatalf("ambiguous preparation recovery = %+v, %v, create=%d reconcile=%d", result, err, provider.prepareCalls, provider.prepareReconcileCalls)
	}
}

func TestCorrectionM03CanonicalTerminalReasons(t *testing.T) {
	base := newControllerFixture(t)
	baseLedger, err := os.ReadFile(base.ledger.Path())
	if err != nil {
		t.Fatal(err)
	}
	reasons := []string{CodeInvalidAuthority, CodeStaleReadyAuthority, CodePullRequestIneligible, CodeAuthorizationFailed,
		CodePaginationInvalid, CodePreSubmitUnavailable, CodeUnsupportedAtomicCAS, CodeCommitPreparationFailed,
		CodeTargetNotSubmitted, CodeTargetNotAppliedBase, CodeTargetNotAppliedHead, CodeTargetNotAppliedRefs,
		CodeLocalPublicationFailed, CodeResourceLimitExhausted, CodeLocalCleanupFailed,
		CodeLocalStorageIntegrityFailure, CodePostMergeAcceptanceFailed}
	for _, reason := range reasons {
		if classified := classifyAuthorizationFailure(typedTerminalFailure(reason, errors.New("typed boundary"))); classified != reason {
			t.Fatalf("typed failure %s classified as %s", reason, classified)
		}
		t.Run(reason, func(t *testing.T) {
			ledgerPath := filepath.Join(t.TempDir(), "events.jsonl")
			if err := os.WriteFile(ledgerPath, baseLedger, 0o600); err != nil {
				t.Fatal(err)
			}
			value, err := ledger.NewJSONLLedger(ledgerPath)
			if err != nil {
				t.Fatal(err)
			}
			f := base
			f.ledger, f.stateRoot = value, t.TempDir()
			if err := os.Chmod(f.stateRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			provider := &fakeProvider{t: t}
			controller := f.controller(t, provider)
			defer controller.Close()
			lease, err := value.AcquireRunTransition(f.runID)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Close()
			assembled := assembleFixture(t, f)
			repositoryLease, err := controller.store.acquireRepositoryBase(repositoryBaseLockKey(assembled))
			if err != nil {
				t.Fatal(err)
			}
			defer repositoryLease.close()
			writeID := deterministicWriteID(assembled)
			attempt, err := controller.store.openAttempt(attemptKey(assembled, writeID))
			if err != nil {
				t.Fatal(err)
			}
			defer attempt.close()
			result, err := controller.terminalize(lease, assembled, attempt, terminalSelection{destination: domain.StateFailed, reason: reason, writeID: writeID})
			if err != nil || result.State != domain.StateFailed || result.ReasonCode != reason || provider.totalCalls() != 0 {
				t.Fatalf("terminal reason %s = %+v, %v, calls=%d", reason, result, err, provider.totalCalls())
			}
			assertOneTerminalEvent(t, ledgerPath, domain.StateFailed, reason)
		})
	}
	if legalTerminalReason(domain.StateFailed, "VALIDATION_UNAVAILABLE") || legalTerminalReason(domain.StateFailed, "RECOVERY_REQUIRED") {
		t.Fatal("non-canonical reason was accepted")
	}
}

func TestCorrectionM04CancellationRecoveryBinding(t *testing.T) {
	t.Run("indexed before attempt marker", func(t *testing.T) {
		f := newControllerFixture(t)
		provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationApplied}
		controller := controllerWithCancellation(t, f, provider, cancellationGrant())
		defer controller.Close()
		controller.afterCancellationIndexed = func() error { return errors.New("crash before attempt pending marker") }
		if _, err := controller.Cancel(context.Background(), CancelRequest{RunID: f.runID, SourceRequestID: "indexed-cancel"}); err == nil {
			t.Fatal("injected cancellation crash unexpectedly succeeded")
		}
		controller.afterCancellationIndexed = nil
		result, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
		if err != nil || result.State != domain.StateCancelled || provider.totalCalls() != 0 {
			t.Fatalf("indexed cancellation recovery = %+v, %v, calls=%d", result, err, provider.totalCalls())
		}
	})

	t.Run("fresh controller policy mismatch", func(t *testing.T) {
		f := newControllerFixture(t)
		provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationApplied}
		grant := cancellationGrant()
		controller := controllerWithCancellation(t, f, provider, grant)
		controller.afterCancellationIndexed = func() error { return errors.New("crash before selection") }
		if _, err := controller.Cancel(context.Background(), CancelRequest{RunID: f.runID, SourceRequestID: "forged-cancel"}); err == nil {
			t.Fatal("injected cancellation crash unexpectedly succeeded")
		}
		_ = controller.Close()
		grant.Requester.Identity.NodeID = "U_forged"
		recovery := controllerWithCancellation(t, f, provider, grant)
		defer recovery.Close()
		if _, err := recovery.Execute(context.Background(), ExecuteRequest{RunID: f.runID}); err == nil || provider.totalCalls() != 0 {
			t.Fatalf("forged recovered cancellation was accepted: %v calls=%d", err, provider.totalCalls())
		}
	})

	t.Run("all independently reconstructed bindings", func(t *testing.T) {
		f := newControllerFixture(t)
		provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationApplied}
		controller := controllerWithCancellation(t, f, provider, cancellationGrant())
		defer controller.Close()
		controller.afterCancellationIndexed = func() error { return errors.New("crash before selection") }
		if _, err := controller.Cancel(context.Background(), CancelRequest{RunID: f.runID, SourceRequestID: "binding-cancel"}); err == nil {
			t.Fatal("injected cancellation crash unexpectedly succeeded")
		}
		assembled := assembleFixture(t, f)
		writeID := deterministicWriteID(assembled)
		attempt, err := controller.store.openAttempt(attemptKey(assembled, writeID))
		if err != nil {
			t.Fatal(err)
		}
		defer attempt.close()
		durable, expected, found, err := controller.indexedCancellation(context.Background(), assembled, attempt)
		if err != nil || !found {
			t.Fatalf("exact indexed cancellation was not independently reconstructed: found=%v err=%v", found, err)
		}
		if err := githublifecycle.ValidateCancellationAuthorityV1(durable.Authority(), expected, githublifecycle.DefaultLimits()); err != nil {
			t.Fatalf("exact reconstructed expectation failed: %v", err)
		}
		tests := []struct {
			name   string
			mutate func(*githublifecycle.CancellationAuthorityExpectationV1)
		}{
			{"READY", func(v *githublifecycle.CancellationAuthorityExpectationV1) {
				v.ReadyBinding = githublifecycle.ReadyAuthorityBindingV1{}
			}},
			{"policy", func(v *githublifecycle.CancellationAuthorityExpectationV1) {
				v.CancellationPolicySHA256 = strings.Repeat("0", 64)
			}},
			{"requester", func(v *githublifecycle.CancellationAuthorityExpectationV1) { v.Requester.Identity.NodeID = "U_wrong" }},
			{"attempt and write", func(v *githublifecycle.CancellationAuthorityExpectationV1) {
				v.AdmissionSHA256 = strings.Repeat("0", 64)
			}},
			{"seal", func(v *githublifecycle.CancellationAuthorityExpectationV1) { v.SealSHA256 = strings.Repeat("0", 64) }},
			{"commitment", func(v *githublifecycle.CancellationAuthorityExpectationV1) {
				v.CommitmentSHA256 = strings.Repeat("0", 64)
			}},
			{"boundary", func(v *githublifecycle.CancellationAuthorityExpectationV1) {
				v.Boundary = githublifecycle.CancellationAdmittedPreTargetSubmission
			}},
			{"replay source request", func(v *githublifecycle.CancellationAuthorityExpectationV1) { v.SourceRequestID = "other-request" }},
			{"replay source kind", func(v *githublifecycle.CancellationAuthorityExpectationV1) { v.SourceKind = "other-source" }},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				candidate := expected
				test.mutate(&candidate)
				if err := githublifecycle.ValidateCancellationAuthorityV1(durable.Authority(), candidate, githublifecycle.DefaultLimits()); err == nil {
					t.Fatal("forged recovery binding was accepted")
				}
			})
		}
	})

	t.Run("exact NOT_APPLIED may cancel", func(t *testing.T) {
		f := newControllerFixture(t)
		provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationNotApplied}
		controller := controllerWithCancellation(t, f, provider, cancellationGrant())
		defer controller.Close()
		controller.afterTargetOutcome = func(outcome TargetOutcome) error {
			if outcome.Disposition != githublifecycle.ReconciliationNotApplied {
				t.Fatalf("outcome = %s, want NOT_APPLIED", outcome.Disposition)
			}
			return errors.New("crash after authenticated NOT_APPLIED")
		}
		if _, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID}); err == nil {
			t.Fatal("injected NOT_APPLIED crash unexpectedly succeeded")
		}
		calls := provider.totalCalls()
		controller.afterTargetOutcome = nil
		result, err := controller.Cancel(context.Background(), CancelRequest{RunID: f.runID, SourceRequestID: "not-applied-cancel"})
		if err != nil || result.State != domain.StateCancelled || result.ReasonCode != CodeCancelledAfterNotApplied || provider.totalCalls() != calls {
			t.Fatalf("NOT_APPLIED cancellation = %+v, %v, calls %d -> %d", result, err, calls, provider.totalCalls())
		}
	})

	t.Run("APPLIED wins", func(t *testing.T) {
		f := newControllerFixture(t)
		provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationUnknown, reconcileDisposition: githublifecycle.ReconciliationApplied}
		controller := controllerWithCancellation(t, f, provider, cancellationGrant())
		defer controller.Close()
		if result, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID}); err == nil || !result.Unresolved {
			t.Fatalf("initial UNKNOWN = %+v, %v", result, err)
		}
		if result, err := controller.Cancel(context.Background(), CancelRequest{RunID: f.runID, SourceRequestID: "applied-wins"}); err != nil || !result.Unresolved {
			t.Fatalf("pending cancellation = %+v, %v", result, err)
		}
		result, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
		if err != nil || result.State != domain.StateMerged {
			t.Fatalf("APPLIED precedence = %+v, %v", result, err)
		}
	})
}

func TestCorrectionM05CumulativeProviderBudgets(t *testing.T) {
	callRoot := t.TempDir()
	_ = os.Chmod(callRoot, 0o700)
	callLimits := productionLimits()
	callLimits.providerCalls, callLimits.preSubmitCalls = 1, 1
	callStore, err := newDurableStore(callRoot, callLimits)
	if err != nil {
		t.Fatal(err)
	}
	callAttempt, err := callStore.openAttempt(strings.Repeat("4", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := callAttempt.reserveCounter("pre-submit"); err != nil {
		t.Fatalf("exact provider-call limit failed: %v", err)
	}
	if _, err := callAttempt.accountProvider(ProviderAccountingV1{}); err != nil {
		t.Fatal(err)
	}
	if _, err := callAttempt.reserveCounter("pre-submit"); err == nil {
		t.Fatal("provider call limit+1 was accepted")
	}
	_ = callAttempt.close()
	_ = callStore.close()
	callStore, err = newDurableStore(callRoot, callLimits)
	if err != nil {
		t.Fatal(err)
	}
	defer callStore.close()
	callAttempt, err = callStore.openAttempt(strings.Repeat("4", 64))
	if err != nil {
		t.Fatal(err)
	}
	defer callAttempt.close()
	if counters, err := callAttempt.currentCounters(); err != nil || counters.ProviderAccountingPending {
		t.Fatalf("completed provider accounting changed across restart: %+v, %v", counters, err)
	}

	pendingRoot := t.TempDir()
	_ = os.Chmod(pendingRoot, 0o700)
	pendingStore, err := newDurableStore(pendingRoot, productionLimits())
	if err != nil {
		t.Fatal(err)
	}
	pendingAttemptID := strings.Repeat("3", 64)
	pendingAttempt, err := pendingStore.openAttempt(pendingAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pendingAttempt.reserveCounter("pre-submit"); err != nil {
		t.Fatal(err)
	}
	_ = pendingAttempt.close()
	_ = pendingStore.close()
	pendingStore, err = newDurableStore(pendingRoot, productionLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer pendingStore.close()
	pendingAttempt, err = pendingStore.openAttempt(pendingAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	defer pendingAttempt.close()
	if counters, err := pendingAttempt.currentCounters(); err != nil || !counters.ProviderAccountingPending {
		t.Fatalf("restart lost pending provider reservation: %+v, %v", counters, err)
	}
	if _, err := pendingAttempt.reserveCounter("pre-submit"); err == nil {
		t.Fatal("restart allowed a new call before pending provider accounting was resolved")
	}

	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := newDurableStore(root, productionLimits())
	if err != nil {
		t.Fatal(err)
	}
	attemptID := strings.Repeat("5", 64)
	attempt, err := store.openAttempt(attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attempt.reserveCounter("pre-submit"); err != nil {
		t.Fatal(err)
	}
	exact := ProviderAccountingV1{RequestBytes: MaxCumulativeRequestBytes, HeaderBytes: MaxCumulativeHeaderBytes,
		CompressedResponseBytes: MaxCumulativeCompressedBytes, DecompressedResponseBytes: MaxCumulativeDecompressedBytes,
		ActiveNanos: int64(MaxCumulativeProviderCallTime), InvocationNanos: int64(ControllerInvocationTimeout)}
	if _, err := attempt.accountProvider(exact); err != nil {
		t.Fatalf("exact cumulative limits failed: %v", err)
	}
	_ = attempt.close()
	_ = store.close()
	store, err = newDurableStore(root, productionLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	attempt, err = store.openAttempt(attemptID)
	if err != nil {
		t.Fatal(err)
	}
	defer attempt.close()
	counters, err := attempt.currentCounters()
	if err != nil || counters.CumulativeRequestBytes != MaxCumulativeRequestBytes || counters.CumulativeHeaderBytes != MaxCumulativeHeaderBytes ||
		counters.CumulativeCompressedBytes != MaxCumulativeCompressedBytes || counters.CumulativeDecompressedBytes != MaxCumulativeDecompressedBytes ||
		counters.CumulativeCallNanos != int64(MaxCumulativeProviderCallTime) || counters.LastInvocationNanos != int64(ControllerInvocationTimeout) ||
		counters.ProviderAccountingPending {
		t.Fatalf("restart lost provider counters: %+v, %v", counters, err)
	}
	if _, err := attempt.accountProvider(ProviderAccountingV1{RequestBytes: 1}); err == nil {
		t.Fatal("limit+1 provider bytes were accepted")
	}
	if _, err := attempt.reserveCounter("pre-submit"); err == nil {
		t.Fatal("a provider call was reserved after a cumulative byte/time limit was reached")
	}

	for _, test := range []struct {
		name   string
		metric ProviderAccountingV1
	}{
		{"request", ProviderAccountingV1{RequestBytes: MaxCumulativeRequestBytes + 1}},
		{"headers", ProviderAccountingV1{HeaderBytes: MaxCumulativeHeaderBytes + 1}},
		{"compressed", ProviderAccountingV1{CompressedResponseBytes: MaxCumulativeCompressedBytes + 1}},
		{"decompressed", ProviderAccountingV1{DecompressedResponseBytes: MaxCumulativeDecompressedBytes + 1}},
		{"active time", ProviderAccountingV1{ActiveNanos: int64(MaxCumulativeProviderCallTime) + 1}},
		{"invocation time", ProviderAccountingV1{InvocationNanos: int64(ControllerInvocationTimeout) + 1}},
	} {
		t.Run(test.name+" limit+1", func(t *testing.T) {
			testRoot := t.TempDir()
			_ = os.Chmod(testRoot, 0o700)
			testStore, err := newDurableStore(testRoot, productionLimits())
			if err != nil {
				t.Fatal(err)
			}
			defer testStore.close()
			testAttempt, err := testStore.openAttempt(strings.Repeat("6", 64))
			if err != nil {
				t.Fatal(err)
			}
			defer testAttempt.close()
			if _, err := testAttempt.reserveCounter("pre-submit"); err != nil {
				t.Fatal(err)
			}
			if _, err := testAttempt.accountProvider(test.metric); err == nil {
				t.Fatal("limit+1 accounting was accepted")
			}
		})
	}

	intervalRoot := t.TempDir()
	_ = os.Chmod(intervalRoot, 0o700)
	intervalStore, err := newDurableStore(intervalRoot, productionLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer intervalStore.close()
	intervalAttempt, err := intervalStore.openAttempt(strings.Repeat("7", 64))
	if err != nil {
		t.Fatal(err)
	}
	defer intervalAttempt.close()
	now := time.Unix(1700000000, 0).UnixNano()
	if _, err := intervalAttempt.reserveReconciliation(now); err != nil {
		t.Fatal(err)
	}
	if _, err := intervalAttempt.reserveReconciliation(now + int64(MinimumReconciliationInterval) - 1); err == nil {
		t.Fatal("early reconciliation was accepted")
	}
	if _, err := intervalAttempt.reserveReconciliation(now + int64(MinimumReconciliationInterval)); err != nil {
		t.Fatalf("exact reconciliation interval failed: %v", err)
	}
}

func TestCorrectionM06StorageReservationsCleanupLimits(t *testing.T) {
	limits := productionLimits()
	limits.publishedFiles = 1
	limits.publishedBytes = 2
	limits.liveFiles = limits.publishedFiles + limits.temporaryFiles
	limits.liveBytes = limits.publishedBytes + limits.temporaryBytes
	root := t.TempDir()
	_ = os.Chmod(root, 0o700)
	store, err := newDurableStore(root, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	first, err := store.openAttempt(strings.Repeat("8", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := first.publish("admission.json", []byte(`{}`)); err != nil {
		t.Fatalf("exact storage limits failed: %v", err)
	}
	defer first.close()
	if _, _, err := first.publish("commit-prepare-marker.json", []byte(`{}`)); err == nil || !strings.Contains(err.Error(), "storage reservation capacity exhausted") {
		t.Fatalf("per-attempt file limit+1 did not reach capacity logic: %v", err)
	}

	byteLimits := productionLimits()
	byteLimits.publishedFiles, byteLimits.publishedBytes = 2, 2
	byteLimits.liveFiles = byteLimits.publishedFiles + byteLimits.temporaryFiles
	byteLimits.liveBytes = byteLimits.publishedBytes + byteLimits.temporaryBytes
	byteRoot := t.TempDir()
	_ = os.Chmod(byteRoot, 0o700)
	byteStore, err := newDurableStore(byteRoot, byteLimits)
	if err != nil {
		t.Fatal(err)
	}
	defer byteStore.close()
	byteAttempt, err := byteStore.openAttempt(strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	defer byteAttempt.close()
	if _, _, err := byteAttempt.publish("admission.json", []byte(`{}`)); err != nil {
		t.Fatalf("exact per-attempt byte limit failed: %v", err)
	}
	if _, _, err := byteAttempt.publish("commit-prepare-marker.json", []byte(`0`)); err == nil || !strings.Contains(err.Error(), "storage reservation capacity exhausted") {
		t.Fatalf("per-attempt byte limit+1 did not reach capacity logic: %v", err)
	}

	concurrentLimits := productionLimits()
	concurrentLimits.publishedFilesTotal, concurrentLimits.publishedBytesTotal = 1, 2
	concurrentRoot := t.TempDir()
	_ = os.Chmod(concurrentRoot, 0o700)
	stores := make([]*durableStore, 2)
	attempts := make([]*attemptStore, 2)
	for index := range stores {
		stores[index], err = newDurableStore(concurrentRoot, concurrentLimits)
		if err != nil {
			t.Fatal(err)
		}
		defer stores[index].close()
		attempts[index], err = stores[index].openAttempt(strings.Repeat(string(rune('e'+index)), 64))
		if err != nil {
			t.Fatal(err)
		}
		defer attempts[index].close()
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, attempt := range attempts {
		go func(value *attemptStore) {
			<-start
			_, _, publishErr := value.publish("admission.json", []byte(`{}`))
			results <- publishErr
		}(attempt)
	}
	close(start)
	successes := 0
	for index := 0; index < 2; index++ {
		if <-results == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent global reservation winners = %d, want 1", successes)
	}

	recoveryRoot := t.TempDir()
	_ = os.Chmod(recoveryRoot, 0o700)
	recoveryStore, err := newDurableStore(recoveryRoot, productionLimits())
	if err != nil {
		t.Fatal(err)
	}
	recoveryID := strings.Repeat("a", 64)
	recoveryAttempt, err := recoveryStore.openAttempt(recoveryID)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"reserved":true}`)
	if err := flock(recoveryStore.lock, false); err != nil {
		t.Fatal(err)
	}
	err = recoveryStore.reserveStorageLocked(recoveryAttempt, "admission.json", data)
	_ = funlock(recoveryStore.lock)
	if err != nil {
		t.Fatal(err)
	}
	_ = recoveryAttempt.close()
	_ = recoveryStore.close()
	recoveryStore, err = newDurableStore(recoveryRoot, productionLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer recoveryStore.close()
	recoveryAttempt, err = recoveryStore.openAttempt(recoveryID)
	if err != nil {
		t.Fatal(err)
	}
	defer recoveryAttempt.close()
	if _, _, err := recoveryAttempt.publish("admission.json", data); err != nil {
		t.Fatalf("restart did not preserve reservation: %v", err)
	}

	controller := &Controller{store: recoveryStore, limits: productionLimits()}
	coreSHA := strings.Repeat("b", 64)
	var last LocalCleanupIncidentV1
	for index := 1; index <= MaxCleanupRecoveryAttempts; index++ {
		last, err = controller.recordCleanupIncident(recoveryAttempt, coreSHA, "event", terminalSelection{writeID: "write"}, errors.New("cleanup failed"), 0)
		if err != nil || last.Sequence != index {
			t.Fatalf("cleanup incident %d = %+v, %v", index, last, err)
		}
	}
	if last.ErrorClass != CodeLocalCleanupExhausted {
		t.Fatalf("32nd incident class = %q", last.ErrorClass)
	}
	var removes atomic.Int32
	recoveryStore.remove = func(string) error { removes.Add(1); return nil }
	if err := controller.cleanupTerminalAttempt(recoveryAttempt, coreSHA); err == nil || removes.Load() != 0 {
		t.Fatalf("33rd automatic cleanup ran: err=%v removes=%d", err, removes.Load())
	}
}

func TestCorrectionM08RepositoryBaseLock(t *testing.T) {
	root := t.TempDir()
	_ = os.Chmod(root, 0o700)
	firstStore, err := newDurableStore(root, productionLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer firstStore.close()
	secondStore, err := newDurableStore(root, productionLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.close()
	key := digest([]byte("repo-id\x00refs/heads/main"))
	first, err := firstStore.acquireRepositoryBase(key)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan *repositoryBaseLease, 1)
	errorsChannel := make(chan error, 1)
	go func() {
		lease, err := secondStore.acquireRepositoryBase(key)
		if err != nil {
			errorsChannel <- err
			return
		}
		acquired <- lease
	}()
	select {
	case lease := <-acquired:
		_ = lease.close()
		t.Fatal("same repository/base acquired two mutation rights")
	case err := <-errorsChannel:
		t.Fatal(err)
	case <-time.After(75 * time.Millisecond):
	}
	if err := first.close(); err != nil {
		t.Fatal(err)
	}
	select {
	case lease := <-acquired:
		defer lease.close()
	case err := <-errorsChannel:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("same repository/base did not serialize after release")
	}
	different := digest([]byte("repo-id\x00refs/heads/release"))
	differentLease, err := firstStore.acquireRepositoryBase(different)
	if err != nil {
		t.Fatalf("different base did not proceed independently: %v", err)
	}
	_ = differentLease.close()
	if key == different || key == digest([]byte("other-repo\x00refs/heads/main")) {
		t.Fatal("repository/base lock identities aliased")
	}

	// Fixed lock order: run transition -> repository/base -> attempt -> store.
	ledgerPath := filepath.Join(t.TempDir(), "events.jsonl")
	value, err := ledger.NewJSONLLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	runLease, err := value.AcquireRunTransition("lock-order-run")
	if err != nil {
		t.Fatal(err)
	}
	defer runLease.Close()
	repositoryLease, err := firstStore.acquireRepositoryBase(digest([]byte("lock-order-resource")))
	if err != nil {
		t.Fatal(err)
	}
	defer repositoryLease.close()
	attempt, err := firstStore.openAttempt(strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	defer attempt.close()
	if _, _, err := attempt.publish("admission.json", []byte(`{"lock":"ordered"}`)); err != nil {
		t.Fatalf("fixed lock order deadlocked or failed: %v", err)
	}
}

func controllerWithCancellation(t *testing.T, f controllerFixture, provider Provider, grant CancellationGrant) *Controller {
	t.Helper()
	source, err := NewStaticAuthoritySource(f.governed)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := New(Config{StateRoot: f.stateRoot, Ledger: f.ledger, AuthoritySource: source,
		CancellationSource: staticCancellationSource{grant: grant}, Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func assembleFixture(t *testing.T, f controllerFixture) assembledAuthority {
	t.Helper()
	data, identity, err := f.ledger.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	assembled, err := assembleAuthority(f.governed, data, identity, githublifecycle.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return assembled
}

func assertOneTerminalEvent(t *testing.T, path string, destination domain.State, reason string) {
	t.Helper()
	count := 0
	for _, event := range readLifecycleEvents(t, path) {
		if event.StateFrom == domain.StateReadyForMerge {
			count++
			if event.StateTo != destination || event.Payload["reason_code"] != reason {
				t.Fatalf("terminal event = %+v, want %s/%s", event, destination, reason)
			}
		}
	}
	if count != 1 {
		t.Fatalf("terminal event count = %d, want 1", count)
	}
}

func assertNoTerminalEvent(t *testing.T, path string) {
	t.Helper()
	for _, event := range readLifecycleEvents(t, path) {
		if event.StateFrom == domain.StateReadyForMerge {
			t.Fatalf("unexpected terminal event: %+v", event)
		}
	}
}

var _ = bytes.Equal

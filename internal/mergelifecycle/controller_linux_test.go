//go:build linux

package mergelifecycle

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
)

func TestControllerAppliedPersistsMergedBeforeCleanup(t *testing.T) {
	f := newControllerFixture(t)
	provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationApplied}
	controller := f.controller(t, provider)
	defer controller.Close()

	result, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != domain.StateMerged || result.ReasonCode != CodeMergeAppliedAccepted || result.TerminalSHA256 == "" {
		t.Fatalf("unexpected merge result: %+v", result)
	}
	if provider.submitCalls != 1 || provider.prepareCalls != 1 || provider.reconcileCalls != 0 || provider.postMergeCalls != 1 {
		t.Fatalf("provider call counts = prepare %d submit %d reconcile %d post %d", provider.prepareCalls, provider.submitCalls, provider.reconcileCalls, provider.postMergeCalls)
	}
	events := readLifecycleEvents(t, f.ledger.Path())
	terminal := events[len(events)-1]
	if terminal.StateFrom != domain.StateReadyForMerge || terminal.StateTo != domain.StateMerged || terminal.ProjectID != f.projectID || terminal.PlanID != f.planID {
		t.Fatalf("terminal event lost provenance: %+v", terminal)
	}
	if _, active, err := f.ledger.ActiveTransitionBarrier(f.runID); err != nil || active {
		t.Fatalf("settled merge left transition barrier active: active=%v err=%v", active, err)
	}

	// Recovery of a completed terminal makes no provider call and no second event.
	before := len(events)
	again, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
	if err != nil || again.State != domain.StateMerged {
		t.Fatalf("terminal recovery = %+v, %v", again, err)
	}
	if got := len(readLifecycleEvents(t, f.ledger.Path())); got != before {
		t.Fatalf("terminal recovery appended a second event: %d -> %d", before, got)
	}
	if provider.submitCalls != 1 || provider.postMergeCalls != 1 {
		t.Fatal("terminal recovery called provider")
	}
}

func TestControllerAcceptsDescendantTargetContainment(t *testing.T) {
	f := newControllerFixture(t)
	provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationApplied, descendant: true}
	controller := f.controller(t, provider)
	defer controller.Close()
	result, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
	if err != nil || result.State != domain.StateMerged {
		t.Fatalf("descendant containment = %+v, %v", result, err)
	}
}

func TestConcurrentControllersSubmitExactAttemptOnce(t *testing.T) {
	f := newControllerFixture(t)
	provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationApplied}
	first := f.controller(t, provider)
	defer first.Close()
	second := f.controller(t, provider)
	defer second.Close()
	start := make(chan struct{})
	results := make(chan struct {
		result Result
		err    error
	}, 2)
	for _, controller := range []*Controller{first, second} {
		go func(controller *Controller) {
			<-start
			result, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
			results <- struct {
				result Result
				err    error
			}{result, err}
		}(controller)
	}
	close(start)
	for range 2 {
		outcome := <-results
		if outcome.err != nil || outcome.result.State != domain.StateMerged {
			t.Fatalf("concurrent controller = %+v, %v", outcome.result, outcome.err)
		}
	}
	if provider.submitCalls != 1 || provider.prepareCalls != 1 || provider.postMergeCalls != 1 {
		t.Fatalf("duplicate side effects: prepare=%d submit=%d post=%d", provider.prepareCalls, provider.submitCalls, provider.postMergeCalls)
	}
}

func TestControllerUnknownPreservesReadyAndReconcilesWithoutRetry(t *testing.T) {
	f := newControllerFixture(t)
	provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationUnknown, reconcileDisposition: githublifecycle.ReconciliationApplied}
	controller := f.controller(t, provider)
	defer controller.Close()

	first, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
	var lifecycleErr *Error
	if !errors.As(err, &lifecycleErr) || lifecycleErr.Code != CodeTargetUnknown || !first.Unresolved || first.State != domain.StateReadyForMerge {
		t.Fatalf("unknown result = %+v, %v", first, err)
	}
	if provider.submitCalls != 1 {
		t.Fatalf("target submissions = %d, want 1", provider.submitCalls)
	}
	if _, active, err := f.ledger.ActiveTransitionBarrier(f.runID); err != nil || !active {
		t.Fatalf("unknown submission did not preserve barrier: active=%v err=%v", active, err)
	}
	competing, _ := ledger.NewEvent(f.runID, "STATE_TRANSITION", "controller", "competitor")
	competing.ProjectID, competing.PlanID, competing.AttemptID = f.projectID, f.planID, f.attemptID
	competing.StateFrom, competing.StateTo = domain.StateReadyForMerge, domain.StateFailed
	if err := f.ledger.Append(competing); err == nil || !strings.Contains(err.Error(), "barrier") {
		t.Fatalf("competing transition passed unresolved barrier: %v", err)
	}

	second, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
	if err != nil || second.State != domain.StateMerged {
		t.Fatalf("reconciled result = %+v, %v", second, err)
	}
	if provider.submitCalls != 1 || provider.reconcileCalls != 1 {
		t.Fatalf("ambiguous write retried: submit=%d reconcile=%d", provider.submitCalls, provider.reconcileCalls)
	}
}

func TestTypedNotAppliedSelectsFailedWithoutCancellation(t *testing.T) {
	f := newControllerFixture(t)
	provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationNotApplied}
	controller := f.controller(t, provider)
	defer controller.Close()
	result, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
	if err != nil || result.State != domain.StateFailed || result.ReasonCode != CodeTargetNotApplied {
		t.Fatalf("typed NOT_APPLIED = %+v, %v", result, err)
	}
	if provider.submitCalls != 1 || provider.reconcileCalls != 0 {
		t.Fatalf("NOT_APPLIED was retried: submit=%d reconcile=%d", provider.submitCalls, provider.reconcileCalls)
	}
}

func TestControllerRejectsAuthorityAndStorageTampering(t *testing.T) {
	t.Run("provider capability", func(t *testing.T) {
		f := newControllerFixture(t)
		f.governed.ProviderCapability = githublifecycle.ProviderCapabilityV1{}
		provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationApplied}
		controller := f.controller(t, provider)
		defer controller.Close()
		if _, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID}); err == nil || provider.totalCalls() != 0 {
			t.Fatalf("invalid capability reached provider: %v calls=%d", err, provider.totalCalls())
		}
	})

	t.Run("later transition", func(t *testing.T) {
		f := newControllerFixture(t)
		event, _ := ledger.NewEvent(f.runID, "STATE_TRANSITION", "controller", "tamper")
		event.ProjectID, event.PlanID, event.AttemptID = f.projectID, f.planID, f.attemptID
		event.StateFrom, event.StateTo = domain.StateReadyForMerge, domain.StateFailed
		if err := f.ledger.Append(event); err != nil {
			t.Fatal(err)
		}
		provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationApplied}
		controller := f.controller(t, provider)
		defer controller.Close()
		result, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
		if err == nil || result.State != "" || provider.totalCalls() != 0 {
			t.Fatalf("stale READY handling = %+v, %v, calls=%d", result, err, provider.totalCalls())
		}
	})

	t.Run("unsafe attempt entry", func(t *testing.T) {
		f := newControllerFixture(t)
		provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationUnknown}
		controller := f.controller(t, provider)
		_, _ = controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
		controller.Close()
		attempts, err := os.ReadDir(filepath.Join(f.stateRoot, "attempts"))
		if err != nil || len(attempts) != 1 {
			t.Fatalf("attempt inventory: %v %#v", err, attempts)
		}
		unsafe := filepath.Join(f.stateRoot, "attempts", attempts[0].Name(), "tmp", "foreign")
		if err := os.Symlink("outside", unsafe); err != nil {
			t.Fatal(err)
		}
		provider2 := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationApplied}
		source, sourceErr := NewStaticAuthoritySource(f.governed)
		if sourceErr != nil {
			t.Fatal(sourceErr)
		}
		controller, err = New(Config{StateRoot: f.stateRoot, Ledger: f.ledger, AuthoritySource: source, Provider: provider2})
		if err == nil {
			defer controller.Close()
			_, err = controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
		}
		if err == nil || provider2.totalCalls() != 0 {
			t.Fatalf("unsafe recovery was not rejected before provider: %v calls=%d", err, provider2.totalCalls())
		}
	})
}

func TestCleanupFailureAfterAppliedProofDoesNotRelabelMerged(t *testing.T) {
	f := newControllerFixture(t)
	provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationUnknown, reconcileDisposition: githublifecycle.ReconciliationApplied}
	controller := f.controller(t, provider)
	defer controller.Close()
	if first, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID}); err == nil || !first.Unresolved {
		t.Fatalf("initial unknown = %+v, %v", first, err)
	}
	controller.beforeTerminal = func(attempt *attemptStore) error {
		data := []byte("recoverable")
		name := hex.EncodeToString([]byte("target-outcome.json")) + "-" + digest(data) + ".tmp"
		return os.WriteFile(filepath.Join(attempt.temporary, name), data, 0o600)
	}
	controller.store.remove = func(string) error { return errors.New("injected unlink failure") }

	result, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
	var lifecycleErr *Error
	if !errors.As(err, &lifecycleErr) || lifecycleErr.Code != CodeLocalCleanupFailed {
		t.Fatalf("cleanup failure was not isolated: %v", err)
	}
	if result.State != domain.StateMerged {
		t.Fatalf("cleanup relabeled applied merge: %+v", result)
	}
	events := readLifecycleEvents(t, f.ledger.Path())
	merged := 0
	for _, event := range events {
		if event.StateTo == domain.StateMerged {
			merged++
		}
		if event.StateTo == domain.StateFailed && event.StateFrom == domain.StateReadyForMerge {
			t.Fatal("cleanup emitted FAILED after accepted merge")
		}
	}
	if merged != 1 {
		t.Fatalf("MERGED event count = %d", merged)
	}
	if provider.submitCalls != 1 || provider.reconcileCalls != 1 {
		t.Fatalf("cleanup failure changed provider calls: submit=%d reconcile=%d", provider.submitCalls, provider.reconcileCalls)
	}
}

func TestCrashAfterPostMergeProofRecoversWithoutProviderCalls(t *testing.T) {
	f := newControllerFixture(t)
	provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationApplied}
	controller := f.controller(t, provider)
	defer controller.Close()
	controller.beforeTerminal = func(*attemptStore) error { return errors.New("injected crash before terminal intent") }
	if result, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID}); err == nil || result.State != "" {
		t.Fatalf("injected crash = %+v, %v", result, err)
	}
	if provider.submitCalls != 1 || provider.postMergeCalls != 1 {
		t.Fatalf("pre-crash provider calls: submit=%d post=%d", provider.submitCalls, provider.postMergeCalls)
	}
	controller.beforeTerminal = nil
	result, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
	if err != nil || result.State != domain.StateMerged {
		t.Fatalf("post-proof recovery = %+v, %v", result, err)
	}
	if provider.submitCalls != 1 || provider.reconcileCalls != 0 || provider.postMergeCalls != 1 {
		t.Fatalf("post-proof recovery called provider: submit=%d reconcile=%d post=%d", provider.submitCalls, provider.reconcileCalls, provider.postMergeCalls)
	}
}

func TestAmbiguousTerminalIntentSyncRecoversExactEvent(t *testing.T) {
	f := newControllerFixture(t)
	provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationApplied}
	controller := f.controller(t, provider)
	defer controller.Close()
	originalSync := controller.store.syncFile
	controller.beforeTerminal = func(*attemptStore) error {
		failed := false
		controller.store.syncFile = func(file *os.File) error {
			if !failed {
				failed = true
				return errors.New("injected ambiguous terminal fsync")
			}
			return originalSync(file)
		}
		return nil
	}
	if result, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID}); err == nil || result.State != "" {
		t.Fatalf("ambiguous terminal sync = %+v, %v", result, err)
	}
	controller.beforeTerminal = nil
	result, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
	if err != nil || result.State != domain.StateMerged {
		t.Fatalf("ambiguous terminal recovery = %+v, %v", result, err)
	}
	if provider.submitCalls != 1 || provider.reconcileCalls != 0 || provider.postMergeCalls != 1 {
		t.Fatalf("terminal recovery called provider: submit=%d reconcile=%d post=%d", provider.submitCalls, provider.reconcileCalls, provider.postMergeCalls)
	}
	events := readLifecycleEvents(t, f.ledger.Path())
	merged := 0
	for _, event := range events {
		if event.StateTo == domain.StateMerged {
			merged++
		}
	}
	if merged != 1 {
		t.Fatalf("MERGED event count = %d", merged)
	}
}

func TestCancellationBeforeAdmissionIsDurableAndSingleUse(t *testing.T) {
	f := newControllerFixture(t)
	provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationApplied}
	grant := cancellationGrant()
	source, err := NewStaticAuthoritySource(f.governed)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := New(Config{StateRoot: f.stateRoot, Ledger: f.ledger, AuthoritySource: source,
		CancellationSource: staticCancellationSource{grant: grant}, Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	result, err := controller.Cancel(context.Background(), CancelRequest{RunID: f.runID, SourceRequestID: "cancel-request-1"})
	if err != nil || result.State != domain.StateCancelled || result.ReasonCode != CodeCancelledBeforeSubmission {
		t.Fatalf("pre-admission cancellation = %+v, %v", result, err)
	}
	if provider.totalCalls() != 0 {
		t.Fatalf("cancellation invoked merge provider %d times", provider.totalCalls())
	}
	if _, err := controller.Cancel(context.Background(), CancelRequest{RunID: f.runID, SourceRequestID: "cancel-request-1"}); err == nil {
		t.Fatal("replayed cancellation request was accepted")
	}
	events := readLifecycleEvents(t, f.ledger.Path())
	if got := events[len(events)-1].StateTo; got != domain.StateCancelled {
		t.Fatalf("terminal cancellation state = %s", got)
	}
}

func TestAppliedTargetResultDefeatsPendingCancellation(t *testing.T) {
	f := newControllerFixture(t)
	provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationUnknown, reconcileDisposition: githublifecycle.ReconciliationApplied}
	source, err := NewStaticAuthoritySource(f.governed)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := New(Config{StateRoot: f.stateRoot, Ledger: f.ledger, AuthoritySource: source,
		CancellationSource: staticCancellationSource{grant: cancellationGrant()}, Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	if first, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID}); err == nil || !first.Unresolved {
		t.Fatalf("initial unknown = %+v, %v", first, err)
	}
	if pending, err := controller.Cancel(context.Background(), CancelRequest{RunID: f.runID, SourceRequestID: "cancel-pending-1"}); err != nil || !pending.Unresolved {
		t.Fatalf("pending cancellation = %+v, %v", pending, err)
	}
	result, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
	if err != nil || result.State != domain.StateMerged {
		t.Fatalf("APPLIED precedence = %+v, %v", result, err)
	}
	if provider.submitCalls != 1 || provider.reconcileCalls != 1 {
		t.Fatalf("pending cancellation changed submission semantics: submit=%d reconcile=%d", provider.submitCalls, provider.reconcileCalls)
	}
}

func TestTypedNotAppliedHonorsPendingCancellation(t *testing.T) {
	f := newControllerFixture(t)
	provider := &fakeProvider{t: t, disposition: githublifecycle.ReconciliationUnknown, reconcileDisposition: githublifecycle.ReconciliationNotApplied}
	source, err := NewStaticAuthoritySource(f.governed)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := New(Config{StateRoot: f.stateRoot, Ledger: f.ledger, AuthoritySource: source,
		CancellationSource: staticCancellationSource{grant: cancellationGrant()}, Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	if first, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID}); err == nil || !first.Unresolved {
		t.Fatalf("initial unknown = %+v, %v", first, err)
	}
	if pending, err := controller.Cancel(context.Background(), CancelRequest{RunID: f.runID, SourceRequestID: "cancel-pending-2"}); err != nil || !pending.Unresolved {
		t.Fatalf("pending cancellation = %+v, %v", pending, err)
	}
	result, err := controller.Execute(context.Background(), ExecuteRequest{RunID: f.runID})
	if err != nil || result.State != domain.StateCancelled || result.ReasonCode != CodeCancelledAfterNotApplied {
		t.Fatalf("NOT_APPLIED cancellation precedence = %+v, %v", result, err)
	}
	if provider.submitCalls != 1 || provider.reconcileCalls != 1 {
		t.Fatalf("pending cancellation retried write: submit=%d reconcile=%d", provider.submitCalls, provider.reconcileCalls)
	}
}

func cancellationGrant() CancellationGrant {
	authentication := evidenceRef("cancellation-authentication", "8")
	policy := evidenceRef("cancellation-policy", "9")
	requestEvidence := evidenceRef("cancellation-request", "a")
	return CancellationGrant{
		Requester:                 githublifecycle.StablePrincipalV1{Kind: "user", Identity: githublifecycle.StableIdentityV1{DatabaseID: 7, NodeID: "U_cancel"}},
		AuthenticationEvidence:    authentication,
		CancellationPolicyVersion: "cancel-v1",
		CancellationPolicySource:  policy,
		CancellationPolicySHA256:  strings.Repeat("b", 64),
		ScopedGrantSHA256:         strings.Repeat("c", 64),
		AllowDecisionSHA256:       strings.Repeat("d", 64),
		SourceKind:                "api",
		RequestEvidence:           requestEvidence,
		IngressID:                 "ingress-1",
		EvidenceRefs:              []ledger.EvidenceRef{authentication, policy, requestEvidence},
	}
}

type staticCancellationSource struct {
	grant CancellationGrant
}

func (s staticCancellationSource) ResolveCancellation(_ context.Context, _, _ string) (CancellationGrant, error) {
	return s.grant, nil
}

type controllerFixture struct {
	runID, projectID, planID, attemptID string
	stateRoot                           string
	ledger                              *ledger.JSONLLedger
	governed                            GovernedAuthority
}

func newControllerFixture(t *testing.T) controllerFixture {
	t.Helper()
	repositoryPath := filepath.Join(t.TempDir(), "repository")
	runGitTest(t, "", "init", "-b", "main", repositoryPath)
	runGitTest(t, repositoryPath, "config", "user.email", "controller@example.test")
	runGitTest(t, repositoryPath, "config", "user.name", "Controller")
	runGitTest(t, repositoryPath, "remote", "add", "origin", "https://github.com/octo-org/control-plane")
	planPath := filepath.Join(repositoryPath, "plan.md")
	os.WriteFile(planPath, []byte("# plan\n"), 0o600)
	runGitTest(t, repositoryPath, "add", "plan.md")
	runGitTest(t, repositoryPath, "commit", "-m", "base")
	baseSHA := runGitTest(t, repositoryPath, "rev-parse", "HEAD")
	runGitTest(t, repositoryPath, "checkout", "-b", "feature/exact-head")
	os.WriteFile(filepath.Join(repositoryPath, "feature.txt"), []byte("accepted\n"), 0o600)
	runGitTest(t, repositoryPath, "add", "feature.txt")
	runGitTest(t, repositoryPath, "commit", "-m", "accepted head")
	headSHA := runGitTest(t, repositoryPath, "rev-parse", "HEAD")
	headTree := runGitTest(t, repositoryPath, "rev-parse", "HEAD^{tree}")

	binary, _ := exec.LookPath("true")
	manifest := authority.Manifest{
		RunID: "run-merge-1", Repository: authority.RepositoryManifest{Path: repositoryPath, Identity: "octo-org/control-plane", Remotes: map[string]string{"origin": "https://github.com/octo-org/control-plane"}, DefaultBranch: "main", StartSHA: baseSHA},
		Plan:       authority.PlanManifest{Path: planPath, SHA256: hashFileTest(t, planPath)},
		Ralphex:    authority.RalphexManifest{BinaryPath: binary, BinarySHA256: hashFileTest(t, binary), SourceSHA: "source", Mode: ralphex.ModeTasksOnly, Timeout: "5s", WaitOnLimit: "0s"},
		Executor:   authority.ExecutorPolicy{Executor: "codex", TaskModel: "test", TaskEffort: "xhigh", ReviewModel: "test", ReviewEffort: "xhigh"},
		Acceptance: []authority.AcceptanceCommand{{Name: "test", Class: "unit", Required: true, Timeout: "5s", Argv: []string{binary}}}, PolicyVersion: "phase3-v1",
	}
	phase3, err := authority.New(manifest)
	if err != nil {
		t.Fatal(err)
	}
	evidenceRoot := filepath.Join(t.TempDir(), "evidence")
	writer, err := evidence.NewStore(evidenceRoot, manifest.RunID)
	if err != nil {
		t.Fatal(err)
	}
	decision, _ := json.Marshal(map[string]any{"state": "READY_FOR_MERGE", "combined": map[string]any{"target": map[string]any{"head_sha": headSHA, "integration": map[string]any{"integrated_head_sha": headSHA, "baseline_sha": baseSHA}}}})
	readyRef, err := writer.WriteBytes("ready.json", "serial-integration-gate-decision", decision)
	if err != nil {
		t.Fatal(err)
	}
	head, _ := githublifecycle.NewGitSHA(headSHA)
	base, _ := githublifecycle.NewGitSHA(baseSHA)
	expected, err := githublifecycle.DeriveExpectedMergeContent(context.Background(), repositoryPath, evidenceRoot, head, base, readyRef, "git")
	if err != nil {
		t.Fatal(err)
	}
	repository, _ := githublifecycle.NewRepository("octo-org", "control-plane")
	baseBranch, _ := githublifecycle.NewBranch("main")
	headBranch, _ := githublifecycle.NewBranch("feature/exact-head")
	pr, _ := githublifecycle.NewPullRequestIdentity(17, "PR_node_17")
	actor, _ := githublifecycle.NewAppInstallationIdentity("github-app:builder", 90210)
	configRef, err := writer.WriteBytes("repository-binding.json", "repository-binding", []byte(`{"repository":"octo-org/control-plane"}`))
	if err != nil {
		t.Fatal(err)
	}
	policyRef, err := writer.WriteBytes("merge-policy.json", "merge-policy", []byte(`{"policy":"merge-v1"}`))
	if err != nil {
		t.Fatal(err)
	}
	capability, err := githublifecycle.NewProviderCapabilityV1(githublifecycle.ProviderCapabilityV1Input{
		Name: githublifecycle.GitHubAtomicBaseHeadCapabilityV1, RepositoryNodeID: "R_repo", APIVersion: githublifecycle.GitHubAPIVersionV1,
		Atomic: true, AllOrNothing: true, SupportsNoOp: true, BaseThenHeadOrder: true, ForceFalse: true, EvidenceRefs: []ledger.EvidenceRef{configRef},
	}, githublifecycle.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(t.TempDir(), "ledger", "events.jsonl")
	events, _ := ledger.NewJSONLLedger(ledgerPath)
	states := []domain.State{domain.StateRunCreated, domain.StateAuthorityValidated, domain.StateExecutionStarting, domain.StateImplementing,
		domain.StateImplementationCompleted, domain.StateBranchAcceptancePending, domain.StateBranchAccepted, domain.StateIntegrationPending,
		domain.StateIntegrating, domain.StateIntegrationAccepted, domain.StateReadyForMerge}
	for index := 0; index < len(states)-1; index++ {
		event := ledger.Event{SchemaVersion: 1, EventID: "transition-" + string(rune('a'+index)), Timestamp: time.Unix(1700000000+int64(index), 0).UTC(),
			ProjectID: "project-1", PlanID: "plan-1", RunID: manifest.RunID, AttemptID: "attempt-1", EventType: "STATE_TRANSITION",
			StateFrom: states[index], StateTo: states[index+1], Actor: "controller", Source: "fixture"}
		if event.StateTo == domain.StateReadyForMerge {
			event.EventID, event.Actor, event.Source, event.EvidenceRefs = "ready-event-1", "controller", "integration-gate", []ledger.EvidenceRef{readyRef}
		}
		if err := events.Append(event); err != nil {
			t.Fatal(err)
		}
	}
	identity := githublifecycle.MergeCommitIdentityV1{Name: "ABCP", Email: "abcp@example.com", Timezone: "+0000"}
	governed := GovernedAuthority{
		Phase3Authority: phase3, ProjectID: "project-1", PlanID: "plan-1", AttemptID: "attempt-1",
		AcceptedSources:   []githublifecycle.AcceptedSourceCandidateV1{{ProjectID: "source-project", PlanID: "source-plan", RunID: "source-run", AttemptID: "source-attempt", RepositoryIdentity: "octo-org/control-plane", Branch: headBranch.String(), StartSHA: baseSHA, AcceptedHeadSHA: headSHA, AcceptancePolicyIdentity: "accept-v1", AcceptanceEvidence: []ledger.EvidenceRef{readyRef}}},
		RepositoryBinding: githublifecycle.RepositoryBindingV1Input{Phase3RepositoryIdentity: "octo-org/control-plane", Phase3RepositoryPath: repositoryPath, Phase3CanonicalRemote: "https://github.com/octo-org/control-plane", Phase3StartSHA: base, GitHubRepository: repository, GitHubRepositoryNodeID: "R_repo", GitHubRepositoryDatabaseID: 99, ConfigurationEvidence: configRef},
		Repository:        repository, BaseBranch: baseBranch, HeadBranch: headBranch, PullRequest: pr, ExpectedContent: expected,
		Policy: PolicyDefinition{Version: "merge-v1", SourceConfiguration: policyRef, RequiredPrincipal: actor,
			Recipe: githublifecycle.MergeCommitRecipePolicyV1{MessageTemplate: "Merge authorized head", TrailerTemplate: "ABCP-Write-ID", Author: identity, Committer: identity, TimestampDerivation: "ready-event-time", ObjectFormat: "sha1", OrderedParents: true}},
		ProviderCapability: capability, EvidenceClosure: []ledger.EvidenceRef{readyRef, configRef, policyRef}, EvidenceRoot: evidenceRoot,
	}
	stateRoot := t.TempDir()
	if err := os.Chmod(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = headTree
	return controllerFixture{manifest.RunID, "project-1", "plan-1", "attempt-1", stateRoot, events, governed}
}

func (f controllerFixture) controller(t *testing.T, provider Provider) *Controller {
	t.Helper()
	source, err := NewStaticAuthoritySource(f.governed)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := New(Config{StateRoot: f.stateRoot, Ledger: f.ledger, AuthoritySource: source, Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

type fakeProvider struct {
	t                     *testing.T
	mu                    sync.Mutex
	disposition           githublifecycle.ReconciliationDisposition
	reconcileDisposition  githublifecycle.ReconciliationDisposition
	prepareCalls          int
	prepareReconcileCalls int
	submitCalls           int
	reconcileCalls        int
	postMergeCalls        int
	observeCalls          int
	descendant            bool
	invalidPreparation    bool
	prepareError          bool
}

func (p *fakeProvider) ObserveAuthorization(ctx context.Context, phase ObservationPhase, auth githublifecycle.Authority) (AuthorizationObservation, error) {
	checkProviderBudget(p.t, ctx, ProviderCallPreSubmitV1, providerAccounting(100))
	p.mu.Lock()
	p.observeCalls++
	call := p.observeCalls
	p.mu.Unlock()
	start := time.Now().UTC().UnixNano()
	if call == 1 {
		start -= int64(time.Second)
	}
	return newAuthorizationObservation(p.t, auth, phase, start)
}

func (p *fakeProvider) PrepareResultCommit(ctx context.Context, recipe githublifecycle.MergeCommitRecipeV1) (CommitPreparation, error) {
	checkProviderBudget(p.t, ctx, ProviderCallCommitSubmissionV1, providerAccounting(100))
	p.mu.Lock()
	p.prepareCalls++
	p.mu.Unlock()
	preparation := exactCommitPreparation(recipe, "prepare", "1")
	if p.invalidPreparation {
		preparation.Observation.Message += " changed"
	}
	if p.prepareError {
		return preparation, errors.New("ambiguous commit preparation")
	}
	return preparation, nil
}
func (p *fakeProvider) ReconcileResultCommit(ctx context.Context, recipe githublifecycle.MergeCommitRecipeV1) (CommitPreparation, error) {
	checkProviderBudget(p.t, ctx, ProviderCallPreSubmitV1, providerAccounting(100))
	p.mu.Lock()
	p.prepareReconcileCalls++
	p.mu.Unlock()
	return exactCommitPreparation(recipe, "prepare-reconcile", "2"), nil
}
func (p *fakeProvider) SubmitTarget(ctx context.Context, input githublifecycle.MergeExecutionInputV1) (TargetOutcome, error) {
	checkProviderBudget(p.t, ctx, ProviderCallTargetV1, providerAccounting(100))
	p.mu.Lock()
	p.submitCalls++
	p.mu.Unlock()
	return p.outcome(input.SealedAuthorization(), input.TargetSubmission(), p.disposition)
}
func (p *fakeProvider) ReconcileTarget(ctx context.Context, input githublifecycle.ReconcileWriteInput) (TargetOutcome, error) {
	checkProviderBudget(p.t, ctx, ProviderCallReconciliationV1, providerAccounting(100))
	p.mu.Lock()
	p.reconcileCalls++
	p.mu.Unlock()
	sealed, _ := input.SealedAuthorization()
	submission, _ := input.TargetSubmission()
	disposition := p.reconcileDisposition
	if disposition == "" {
		disposition = p.disposition
	}
	return p.outcome(sealed, submission, disposition)
}
func (p *fakeProvider) outcome(sealed githublifecycle.SealedMergeAuthorizationV1, submission githublifecycle.TargetSubmissionV1, disposition githublifecycle.ReconciliationDisposition) (TargetOutcome, error) {
	evidence := []ledger.EvidenceRef{evidenceRef("target", "3")}
	result := TargetOutcome{Disposition: disposition, EvidenceRefs: evidence, RequestBytes: 100, Accounting: providerAccounting(100)}
	if disposition == githublifecycle.ReconciliationApplied {
		recipe := sealed.MergeInput().Recipe()
		auth := sealed.MergeInput().Authority()
		pr, _ := auth.PullRequest()
		snapshot, _ := githublifecycle.NewSnapshotIdentity("github", "target-result", time.Now().UnixNano())
		value, err := githublifecycle.NewMergeResult(githublifecycle.MergeResultInput{Snapshot: snapshot, Repository: auth.Repository(), PullRequest: pr, Actor: auth.Actor(),
			AcceptedHeadSHA: auth.HeadSHA(), AcceptedHeadTree: auth.ExpectedContent().ExpectedResultTreeSHA(), BaseBeforeSHA: auth.ExpectedBaseTipSHA(), Method: githublifecycle.MergeMethodMerge,
			ResultSHA: recipe.ExpectedResultSHA(), ResultTree: auth.ExpectedContent().ExpectedResultTreeSHA(), Parents: recipe.Input().Parents,
			EvidenceRefs: evidence, Attempt: sealed.MergeInput().Attempt(), ExpectedContent: auth.ExpectedContent(), SealedAuthorization: sealed, Recipe: recipe}, githublifecycle.DefaultLimits())
		if err != nil {
			return result, err
		}
		result.Result = value
	} else if disposition == githublifecycle.ReconciliationNotApplied {
		body := []byte(`{"data":{"updateRefs":null},"errors":[{"type":"FAILED_PRECONDITION","path":["updateRefs","refUpdates","beforeOid"],"extensions":{"code":"UPDATE_REFS_BEFORE_OID_MISMATCH","ref_update_index":0}}]}`)
		response, _ := githublifecycle.NewSnapshotIdentity("github", "target-not-applied", time.Now().UnixNano())
		bodyEvidence := ledger.EvidenceRef{URI: "evidence/target-not-applied-body", Kind: githublifecycle.GitHubTargetResponseBodyEvidenceKindV1, SHA256: digest(body)}
		envelope, err := githublifecycle.NewTargetResponseEnvelopeV1(githublifecycle.TargetResponseEnvelopeV1Input{
			Response: response, HTTPStatus: 200, ResponseBody: body, BodyEvidence: bodyEvidence, EnvelopeURI: "evidence/target-not-applied-envelope",
		}, submission, githublifecycle.DefaultLimits())
		if err != nil {
			return result, err
		}
		proofEvidence := bodyEvidence
		proofEvidence.Kind = githublifecycle.NotAppliedAtomicRejectionEvidenceKindV1
		proof, err := githublifecycle.NewNotAppliedProofV1(githublifecycle.NotAppliedProofV1Input{
			Kind: githublifecycle.NotAppliedAtomicBaseRejected, RequestBytes: 100, ResponseEnvelope: &envelope, Response: &response,
			HTTPStatus: 200, ResponseBodySHA256: digest(body), ResponseBody: body, EvidenceRef: proofEvidence,
		}, sealed, submission, githublifecycle.DefaultLimits())
		if err != nil {
			return result, err
		}
		result.NotAppliedProof = proof
		result.EvidenceRefs = []ledger.EvidenceRef{proofEvidence, bodyEvidence, envelope.EvidenceRef()}
	}
	return result, nil
}
func (p *fakeProvider) ObservePostMerge(ctx context.Context, input githublifecycle.ObservePostMergeInput) (PostMergeOutcome, error) {
	checkProviderBudget(p.t, ctx, ProviderCallPostMergeV1, providerAccounting(100))
	p.mu.Lock()
	p.postMergeCalls++
	p.mu.Unlock()
	result := input.Merge()
	r := result.Input()
	recipe := r.Recipe.Input()
	objectSnapshot, _ := githublifecycle.NewSnapshotIdentity("github", "result-object", time.Now().UnixNano())
	objectEvidence := []ledger.EvidenceRef{evidenceRef("result-object", "4")}
	object, err := githublifecycle.NewResultCommitObservationV1(githublifecycle.ResultCommitObservationV1Input{Snapshot: objectSnapshot, Repository: r.Repository,
		ResultSHA: r.ResultSHA, ResultTree: r.ResultTree, Parents: r.Parents, Message: recipe.Message, Author: recipe.Author, Committer: recipe.Committer,
		AuthorUnix: recipe.AuthorUnix, CommitterUnix: recipe.CommitterUnix, RecipeSHA256: r.Recipe.SHA256(), EvidenceRefs: objectEvidence}, githublifecycle.DefaultLimits())
	if err != nil {
		return PostMergeOutcome{}, err
	}
	containSnapshot, _ := githublifecycle.NewSnapshotIdentity("github", "containment", time.Now().UnixNano())
	containEvidence := []ledger.EvidenceRef{evidenceRef("containment", "5")}
	targetTip := r.ResultSHA
	status := githublifecycle.TargetContainmentIdentical
	distance := 0
	if p.descendant {
		targetTip = r.AcceptedHeadSHA
		status = githublifecycle.TargetContainmentAhead
		distance = 1
	}
	proof, err := githublifecycle.NewTargetContainmentProofV1(githublifecycle.TargetContainmentProofV1Input{Snapshot: containSnapshot, Repository: r.Repository,
		TargetRef: "refs/heads/" + input.Authority().BaseBranch().String(), ResultSHA: r.ResultSHA, ObservedTargetTipSHA: targetTip,
		Mechanism: githublifecycle.GitHubCompareProofV1, Status: status, MergeBaseSHA: r.ResultSHA, DescendantDistance: distance, EvidenceRefs: containEvidence}, githublifecycle.DefaultLimits())
	if err != nil {
		return PostMergeOutcome{}, err
	}
	observationEvidence := append(objectEvidence, containEvidence...)
	observation, err := githublifecycle.NewPostMergeObservation(githublifecycle.PostMergeObservationInput{Snapshot: containSnapshot, Repository: r.Repository,
		BaseBranch: input.Authority().BaseBranch(), PullRequest: r.PullRequest, Actor: r.Actor, AcceptedHeadSHA: r.AcceptedHeadSHA,
		AcceptedHeadTree: r.AcceptedHeadTree, BaseBeforeSHA: r.BaseBeforeSHA, Method: r.Method, ResultSHA: r.ResultSHA, ObservedTargetTipSHA: targetTip,
		ResultTree: r.ResultTree, Parents: r.Parents, Lineage: r.Lineage, EvidenceRefs: observationEvidence, Attempt: r.Attempt,
		ExpectedContent: r.ExpectedContent, SealedAuthorization: r.SealedAuthorization, ResultObject: object, ContainmentProof: proof}, githublifecycle.DefaultLimits())
	return PostMergeOutcome{Observation: observation, Accounting: providerAccounting(100)}, err
}
func (p *fakeProvider) totalCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.observeCalls + p.prepareCalls + p.prepareReconcileCalls + p.submitCalls + p.reconcileCalls + p.postMergeCalls
}

func newAuthorizationObservation(t *testing.T, auth githublifecycle.Authority, phase ObservationPhase, started int64) (AuthorizationObservation, error) {
	t.Helper()
	pr, _ := auth.PullRequest()
	repositoryNodeID := auth.ReadyBinding().RepositoryBinding().Input().GitHubRepositoryNodeID
	stamp := started + int64(time.Millisecond)
	reviews := emptyClosure(t, auth, githublifecycle.PaginationReviews, &pr, string(phase)+"-reviews", stamp)
	checks := emptyClosure(t, auth, githublifecycle.PaginationCheckRuns, nil, string(phase)+"-checks", stamp+int64(time.Millisecond))
	statuses := emptyClosure(t, auth, githublifecycle.PaginationCommitStatuses, nil, string(phase)+"-statuses", stamp+2*int64(time.Millisecond))
	prSnapshot, _ := githublifecycle.NewSnapshotIdentity("github", string(phase)+"-pr", stamp+3*int64(time.Millisecond))
	open, no := githublifecycle.PullRequestOpen, false
	body := []byte(`{"fixture":"pull-request"}`)
	bodyRef := ledger.EvidenceRef{URI: "evidence/" + string(phase) + "-pr-body", Kind: githublifecycle.GitHubPullRequestResponseEvidenceKindV1, SHA256: digest(body)}
	input := githublifecycle.AuthoritativePullRequestSnapshotV1Input{Snapshot: prSnapshot, ResponseBodySHA256: digest(body), APIVersion: githublifecycle.GitHubAPIVersionV1,
		RepositoryBinding: auth.ReadyBinding().RepositoryBinding(), PullRequest: pr, PullRequestDatabaseID: pr.Number(), BaseRepositoryNodeID: repositoryNodeID,
		BaseRef: "refs/heads/" + auth.BaseBranch().String(), BaseOID: auth.ExpectedBaseTipSHA(), HeadRepositoryNodeID: repositoryNodeID,
		HeadRef: "refs/heads/" + auth.HeadBranch().String(), HeadOID: auth.HeadSHA(), State: &open, IsDraft: &no, Merged: &no,
		Actor: auth.Actor(), ReviewsClosure: reviews}
	envelope, err := githublifecycle.NewPullRequestEnvelopeEvidenceV1("evidence/"+string(phase)+"-pr-envelope", input, githublifecycle.DefaultLimits())
	if err != nil {
		return AuthorizationObservation{}, err
	}
	input.EvidenceRefs = []ledger.EvidenceRef{bodyRef, envelope}
	snapshot, err := githublifecycle.NewAuthoritativePullRequestSnapshotV1(input, githublifecycle.DefaultLimits())
	if err != nil {
		return AuthorizationObservation{}, err
	}
	evidence := append([]ledger.EvidenceRef{}, input.EvidenceRefs...)
	evidence = append(evidence, reviews.Input().EvidenceRefs...)
	evidence = append(evidence, checks.Input().EvidenceRefs...)
	evidence = append(evidence, statuses.Input().EvidenceRefs...)
	return AuthorizationObservation{snapshot, nil, checks, statuses, evidence, started, stamp + 4*int64(time.Millisecond), githublifecycle.AuthorizationCountersV1{}, providerAccounting(100)}, nil
}

func exactCommitPreparation(recipe githublifecycle.MergeCommitRecipeV1, kind, fill string) CommitPreparation {
	i := recipe.Input()
	parents := make([]string, len(i.Parents))
	for index := range i.Parents {
		parents[index] = i.Parents[index].String()
	}
	evidence := []ledger.EvidenceRef{evidenceRef(kind, fill)}
	observation := CommitPreparationObservationV1{
		Schema: "merge-commit-preparation-observation-v1", Repository: i.Repository.String(), ResultSHA: i.ExpectedResultSHA.String(),
		ResultTree: i.ExpectedResultTree.String(), Parents: parents, Message: i.Message, Author: i.Author, Committer: i.Committer,
		AuthorUnix: i.AuthorUnix, CommitterUnix: i.CommitterUnix, ObjectFormat: i.ObjectFormat, ObjectBytes: recipe.CommitBytes(),
		ObjectBytesSHA256: digest(recipe.CommitBytes()), RecipeSHA256: recipe.SHA256(), EvidenceRefs: evidence,
	}
	return CommitPreparation{"merge-commit-preparation-v1", recipe.SHA256(), recipe.ExpectedResultSHA().String(), observation, evidence, providerAccounting(100)}
}

func providerAccounting(request int64) ProviderAccountingV1 {
	return ProviderAccountingV1{HTTPCalls: 1, RequestBytes: request, HeaderBytes: 64, CompressedResponseBytes: 128,
		DecompressedResponseBytes: 128, ActiveNanos: int64(time.Millisecond), InvocationNanos: int64(time.Second)}
}

func checkProviderBudget(t *testing.T, ctx context.Context, class ProviderCallClassV1, accounting ProviderAccountingV1) {
	t.Helper()
	budget, ok := ProviderBudgetFromContext(ctx)
	handoff, handoffOK := ProviderHTTPCallHandoffFromContext(ctx)
	if !ok || !handoffOK || budget.HTTPCalls <= 0 || accounting.RequestBytes > budget.RequestBytes || accounting.HeaderBytes > budget.HeaderBytes ||
		accounting.CompressedResponseBytes > budget.CompressedResponseBytes || accounting.DecompressedResponseBytes > budget.DecompressedResponseBytes ||
		accounting.ActiveNanos > budget.ActiveNanos {
		t.Fatalf("provider operation lacks an enforceable cumulative budget: %+v, present=%v", budget, ok)
	}
	reservation, err := handoff.ReserveHTTPCall(class)
	if err != nil {
		t.Fatalf("provider operation could not reserve its actual HTTP call: %v", err)
	}
	err = reservation.Complete(ProviderCallAccountingV1{RequestBytes: accounting.RequestBytes, HeaderBytes: accounting.HeaderBytes,
		CompressedResponseBytes: accounting.CompressedResponseBytes, DecompressedResponseBytes: accounting.DecompressedResponseBytes,
		ActiveNanos: accounting.ActiveNanos})
	if err != nil {
		t.Fatalf("provider operation could not complete its actual HTTP call: %v", err)
	}
}

func emptyClosure(t *testing.T, auth githublifecycle.Authority, source githublifecycle.PaginationSourceKind, pr *githublifecycle.PullRequestIdentity, requestID string, observed int64) githublifecycle.PaginationClosureV1 {
	t.Helper()
	scope := githublifecycle.PaginationQueryScopeV1{Source: source, Repository: auth.Repository(), RepositoryNodeID: auth.ReadyBinding().RepositoryBinding().Input().GitHubRepositoryNodeID, PullRequest: pr, HeadSHA: auth.HeadSHA()}
	query, err := githublifecycle.DerivePaginationQueryV1(scope, githublifecycle.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := githublifecycle.NewSnapshotIdentity("github", requestID, observed)
	body := evidenceRef(requestID+"-body", "7")
	body.Kind = githublifecycle.GitHubPaginationBodyEvidenceKindV1
	pageInput := githublifecycle.PaginationPageV1Input{Query: query, Ordinal: 0, RequestedPage: 1, Response: snapshot, RawBodySHA256: body.SHA256, ResponseEvidence: body, RESTLinkObserved: true}
	pageInput.EnvelopeEvidence, err = githublifecycle.NewPaginationEnvelopeEvidenceV1("evidence/"+requestID+"-envelope", pageInput, githublifecycle.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	page, err := githublifecycle.NewPaginationPageV1(pageInput, githublifecycle.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	closure, err := githublifecycle.NewPaginationClosureV1(githublifecycle.PaginationClosureV1Input{Query: query, Pages: []githublifecycle.PaginationPageV1{page}, EvidenceRefs: []ledger.EvidenceRef{body, pageInput.EnvelopeEvidence}}, githublifecycle.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return closure
}

func evidenceRef(kind, fill string) ledger.EvidenceRef {
	return ledger.EvidenceRef{URI: "evidence/" + kind, Kind: kind, SHA256: strings.Repeat(fill, 64)}
}
func hashFileTest(t *testing.T, path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func runGitTest(t *testing.T, directory string, args ...string) string {
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
func readLifecycleEvents(t *testing.T, path string) []ledger.Event {
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	var events []ledger.Event
	for scanner.Scan() {
		var event ledger.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

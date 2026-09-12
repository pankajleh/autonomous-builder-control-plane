package integrationgate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/combinedacceptance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/integrationworkspace"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/scheduler"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
)

func TestCleanGateOwnsProvenanceAndReachesReadyForMerge(t *testing.T) {
	fixture := newGateFixture(t, false, "pass")
	result, err := fixture.gate.Run(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != domain.StateReadyForMerge || result.Textual.Status() != integrationworkspace.StatusClean || result.Combined.Classification() != combinedacceptance.ClassificationClean {
		t.Fatalf("result = state %s textual %s combined %s", result.State, result.Textual.Status(), result.Combined.Classification())
	}
	want := []domain.State{domain.StateIntegrationPending, domain.StateIntegrating, domain.StateIntegrationAccepted, domain.StateReadyForMerge}
	if got := fixture.events.states(); !reflect.DeepEqual(got, want) {
		t.Fatalf("states = %v, want %v", got, want)
	}
	for _, event := range fixture.events.events {
		if event.ProjectID != fixture.request.Authority.Repository().Identity || event.PlanID != fixture.request.Authority.Plan().SHA256 ||
			event.AttemptID != fixture.request.Authority.RunID() || event.Actor != "controller" || event.Source != "integration-gate" {
			t.Fatalf("transition lost controller provenance: %+v", event)
		}
		if len(event.EvidenceRefs) == 0 {
			t.Fatalf("transition %s has no evidence", event.StateTo)
		}
		assertVerifiedRefs(t, fixture.gate.evidenceRoot, event.EvidenceRefs)
	}
	assertLedgerContinuity(t, fixture.events.events)
	if entries, err := os.ReadDir(fixture.temporaryRoot); err != nil || len(entries) != 0 {
		t.Fatalf("disposable target remains: %v, %v", entries, err)
	}
	if result.Combined.Target().Integration.IntegrationID != result.Textual.SHA256() {
		t.Fatal("Track C provenance was not bound to the exact Track B result")
	}
}

func TestGateMapsTextualAndSemanticAndUnavailableOutcomes(t *testing.T) {
	tests := []struct {
		name     string
		conflict bool
		mode     string
		want     domain.State
	}{
		{"textual", true, "pass", domain.StateIntegrationConflict},
		{"semantic", false, "fail", domain.StateSemanticConflict},
		{"unavailable", false, "unavailable", domain.StateValidationUnavailable},
		{"target-mutation", false, "move-head", domain.StateValidationUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGateFixture(t, test.conflict, test.mode)
			result, _ := fixture.gate.Run(context.Background(), fixture.request)
			if result.State != test.want {
				t.Fatalf("state = %s, want %s (reason %q)", result.State, test.want, result.FailureReason)
			}
			for _, event := range fixture.events.events {
				if event.StateTo == domain.StateIntegrationAccepted || event.StateTo == domain.StateReadyForMerge {
					t.Fatalf("non-clean path emitted %s", event.StateTo)
				}
			}
		})
	}
}

func TestGateRejectsChangedCandidateHeadsReviewsBlockersAndEvidence(t *testing.T) {
	t.Run("changed-head", func(t *testing.T) {
		fixture := newGateFixture(t, false, "pass")
		runGit(t, fixture.repository, "update-ref", "refs/heads/candidate-one", fixture.base)
		_, err := fixture.gate.Run(context.Background(), fixture.request)
		assertErrorContains(t, err, "source head changed")
	})
	t.Run("replacement-ref", func(t *testing.T) {
		fixture := newGateFixture(t, false, "pass")
		head := fixture.request.Candidates[0].Input().HeadSHA
		runGit(t, fixture.repository, "replace", head, fixture.base)
		_, err := fixture.gate.Run(context.Background(), fixture.request)
		assertErrorContains(t, err, "replacement refs")
	})
	t.Run("review-sha", func(t *testing.T) {
		fixture := newGateFixture(t, false, "pass")
		fixture.request.Reviews[0].ReviewedSHA = fixture.base
		_, err := fixture.gate.Run(context.Background(), fixture.request)
		assertErrorContains(t, err, "SHA does not match")
	})
	t.Run("review-verdict", func(t *testing.T) {
		fixture := newGateFixture(t, false, "pass")
		fixture.request.Reviews[0].Verdict = "MAJOR_FINDING"
		_, err := fixture.gate.Run(context.Background(), fixture.request)
		assertErrorContains(t, err, "substantive non-clean verdict")
	})
	t.Run("unresolved-blocker", func(t *testing.T) {
		fixture := newGateFixture(t, false, "pass")
		fixture.request.Prerequisites.Blockers = []Blocker{{ID: "security-review", Resolved: false}}
		_, err := fixture.gate.Run(context.Background(), fixture.request)
		assertErrorContains(t, err, "unresolved blocker")
	})
	t.Run("missing-evidence", func(t *testing.T) {
		fixture := newGateFixture(t, false, "pass")
		if err := os.Remove(fixture.request.Reviews[0].Evidence[0].URI); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.gate.Run(context.Background(), fixture.request)
		assertErrorContains(t, err, "evidence")
	})
	t.Run("mutated-evidence", func(t *testing.T) {
		fixture := newGateFixture(t, false, "pass")
		ref := fixture.request.Candidates[0].Input().AcceptanceEvidence[0]
		if err := os.WriteFile(ref.URI, []byte("changed"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.gate.Run(context.Background(), fixture.request)
		assertErrorContains(t, err, "SHA256 mismatch")
	})
}

func TestGateRejectsCallerSelectedReviewPolicyAndNonexistentReviewedCommit(t *testing.T) {
	t.Run("caller-selected-without-authority", func(t *testing.T) {
		fixture := newGateFixture(t, false, "pass")
		manifest := fixture.request.Authority.Manifest()
		manifest.MergeReview = nil
		governed, err := authority.New(manifest)
		if err != nil {
			t.Fatal(err)
		}
		fixture.request.Authority = governed
		_, err = fixture.gate.Run(context.Background(), fixture.request)
		assertErrorContains(t, err, "does not bind a merge review policy")
	})
	t.Run("caller-policy-mismatch", func(t *testing.T) {
		fixture := newGateFixture(t, false, "pass")
		fixture.request.ReviewPolicy.PolicyIdentity = "fabricated-clean-policy"
		_, err := fixture.gate.Run(context.Background(), fixture.request)
		assertErrorContains(t, err, "caller-selected review policy")
	})
	t.Run("nonexistent-authority-reviewed-commit", func(t *testing.T) {
		fixture := newGateFixture(t, false, "pass")
		manifest := fixture.request.Authority.Manifest()
		missing := strings.Repeat("d", 40)
		manifest.MergeReview.Required[0].ReviewedSHA = missing
		governed, err := authority.New(manifest)
		if err != nil {
			t.Fatal(err)
		}
		fixture.request.Authority = governed
		fixture.request.ReviewPolicy = *manifest.MergeReview
		fixture.request.Reviews[0].ReviewedSHA = missing
		_, err = fixture.gate.Run(context.Background(), fixture.request)
		assertErrorContains(t, err, "not an exact governed repository commit")
	})
}

func TestGateRequestHasNoCallerInjectableTrackBOrTrackCResult(t *testing.T) {
	typeOf := reflect.TypeOf(Request{})
	for _, forbidden := range []string{"Textual", "IntegratedHead", "Target", "Combined", "CombinedResult", "Provenance"} {
		if _, ok := typeOf.FieldByName(forbidden); ok {
			t.Fatalf("Request exposes caller-forgeable field %s", forbidden)
		}
	}
}

func TestGateInputEvidenceCanonicalizesEquivalentOrdering(t *testing.T) {
	fixture := newGateFixture(t, false, "pass")
	_, left, err := fixture.gate.validateRequest(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	rightRequest := fixture.request
	rightRequest.Reviews = []ReviewAttestation{fixture.request.Reviews[1], fixture.request.Reviews[0]}
	rightRequest.ReviewPolicy.Required = []ReviewRequirement{fixture.request.ReviewPolicy.Required[1], fixture.request.ReviewPolicy.Required[0]}
	_, right, err := fixture.gate.validateRequest(context.Background(), rightRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("equivalent input ordering changed canonical evidence:\n%s\n%s", left, right)
	}
}

func TestGateSupportsLargeLegitimateAcceptanceEvidenceFanout(t *testing.T) {
	fixture := newGateFixture(t, false, "pass")
	manifest := fixture.request.Authority.Manifest()
	command := manifest.Acceptance[0]
	manifest.Acceptance = make([]authority.AcceptanceCommand, 48)
	for index := range manifest.Acceptance {
		manifest.Acceptance[index] = command
		manifest.Acceptance[index].Name = fmt.Sprintf("combined-%03d", index+1)
	}
	governed, err := authority.New(manifest)
	if err != nil {
		t.Fatal(err)
	}
	fixture.request.Authority = governed
	result, err := fixture.gate.Run(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != domain.StateReadyForMerge || len(result.Combined.EvidenceRefs()) <= 128 {
		t.Fatalf("large evidence result = state %s, refs %d", result.State, len(result.Combined.EvidenceRefs()))
	}
}

func TestGateFanoutOverflowTerminalizesValidationUnavailable(t *testing.T) {
	fixture := newGateFixture(t, false, "pass")
	fixture.gate.workspace = &overflowEvidenceWorkspace{delegate: fixture.gate.workspace}
	result, err := fixture.gate.Run(context.Background(), fixture.request)
	if err != nil || result.State != domain.StateValidationUnavailable || !strings.Contains(result.FailureReason, "integration evidence count") {
		t.Fatalf("overflow result = state %s, error %v", result.State, err)
	}
	states := fixture.events.states()
	if len(states) == 0 || states[len(states)-1] != domain.StateValidationUnavailable {
		t.Fatalf("overflow left nonterminal states %v", states)
	}
	terminal := fixture.events.events[len(fixture.events.events)-1]
	if len(terminal.EvidenceRefs) == 0 || len(terminal.EvidenceRefs) > 16 {
		t.Fatalf("overflow terminal evidence was not reduced and bounded: %d refs", len(terminal.EvidenceRefs))
	}
}

type overflowEvidenceWorkspace struct{ delegate workspaceController }

func (w *overflowEvidenceWorkspace) Integrate(ctx context.Context, request integrationworkspace.Request) (integrationworkspace.Result, error) {
	return w.delegate.Integrate(ctx, request)
}

func (w *overflowEvidenceWorkspace) UseMaterialized(ctx context.Context, request integrationworkspace.Request, expected integrationworkspace.Result, prefix string, use func(integrationworkspace.MaterializedTarget) error) (integrationworkspace.MaterializationEvidence, error) {
	return w.delegate.UseMaterialized(ctx, request, expected, prefix, func(target integrationworkspace.MaterializedTarget) error {
		for len(target.Evidence) <= 64 {
			target.Evidence = append(target.Evidence, target.Evidence[0])
		}
		return use(target)
	})
}

func TestReadyForMergeCarriesDurableSourceHeadVerification(t *testing.T) {
	fixture := newGateFixture(t, false, "pass")
	result, err := fixture.gate.Run(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SourceVerificationEvidence) != 3 {
		t.Fatalf("source verification evidence count = %d, want 3", len(result.SourceVerificationEvidence))
	}
	ready := fixture.events.events[len(fixture.events.events)-1]
	found := false
	for _, ref := range ready.EvidenceRefs {
		if ref.Kind == sourceHeadsEvidenceKind {
			data, readErr := evidence.ReadVerifiedLocal(fixture.gate.evidenceRoot, ref, maxEvidenceArtifactBytes)
			if readErr != nil {
				t.Fatalf("ready source evidence = %s, %v", data, readErr)
			}
			if strings.Contains(string(data), `"boundary":"ready-for-merge"`) && strings.Contains(string(data), `"verified":true`) {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("READY_FOR_MERGE transition omitted final source-head verification evidence")
	}
}

func TestFinalSourceHeadDriftTerminalizesWithDurableEvidence(t *testing.T) {
	fixture := newGateFixture(t, false, "pass")
	first := fixture.request.Candidates[0].Input()
	fixture.gate.workspace = &moveSourceAfterMaterializationWorkspace{
		delegate: fixture.gate.workspace, repository: first.Repository, branch: first.Branch, target: fixture.base,
	}
	result, err := fixture.gate.Run(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != domain.StateFailed || !strings.Contains(result.FailureReason, "source head changed") {
		t.Fatalf("drift result = state %s, reason %q", result.State, result.FailureReason)
	}
	terminal := fixture.events.events[len(fixture.events.events)-1]
	foundFailureProof := false
	for _, ref := range terminal.EvidenceRefs {
		if ref.Kind != sourceHeadsEvidenceKind {
			continue
		}
		data, readErr := evidence.ReadVerifiedLocal(fixture.gate.evidenceRoot, ref, maxEvidenceArtifactBytes)
		if readErr == nil && strings.Contains(string(data), `"boundary":"ready-for-merge"`) && strings.Contains(string(data), `"verified":false`) {
			foundFailureProof = true
		}
	}
	if !foundFailureProof {
		t.Fatal("terminal transition omitted durable final source-head failure proof")
	}
	assertLedgerContinuity(t, fixture.events.events)
	assertVerifiedRefs(t, fixture.gate.evidenceRoot, terminal.EvidenceRefs)
}

func TestEvidenceVerificationFailuresTerminalizeFromEveryDurableGateState(t *testing.T) {
	t.Run("branch-accepted", func(t *testing.T) {
		fixture := newGateFixture(t, false, "pass")
		fixture.replaceWriter(t, &namedFaultWriter{ArtifactWriter: fixture.store, corruptSuffix: "-input.json"})
		result, err := fixture.gate.Run(context.Background(), fixture.request)
		if err != nil || result.State != domain.StateFailed {
			t.Fatalf("branch fallback = state %s, err %v, reason %q", result.State, err, result.FailureReason)
		}
		assertFallbackTerminal(t, fixture)
	})

	t.Run("integration-pending", func(t *testing.T) {
		fixture := newGateFixture(t, false, "pass")
		fixture.gate.events = &mutatingEvents{delegate: fixture.events, after: domain.StateIntegrationPending, mutate: func(event ledger.Event) error {
			return mutateFirstRefOfKind(event, gateInputEvidenceKind)
		}}
		result, err := fixture.gate.Run(context.Background(), fixture.request)
		if err != nil || result.State != domain.StateFailed {
			t.Fatalf("pending fallback = state %s, err %v, reason %q", result.State, err, result.FailureReason)
		}
		assertFallbackTerminal(t, fixture)
	})

	t.Run("integrating", func(t *testing.T) {
		fixture := newGateFixture(t, false, "pass")
		fixture.gate.workspace = &badMaterializationRefWorkspace{delegate: fixture.gate.workspace}
		result, err := fixture.gate.Run(context.Background(), fixture.request)
		if err != nil || result.State != domain.StateValidationUnavailable {
			t.Fatalf("integrating fallback = state %s, err %v, reason %q", result.State, err, result.FailureReason)
		}
		assertFallbackTerminal(t, fixture)
	})

	t.Run("integration-accepted", func(t *testing.T) {
		fixture := newGateFixture(t, false, "pass")
		fixture.gate.events = &mutatingEvents{delegate: fixture.events, after: domain.StateIntegrationAccepted, mutate: func(event ledger.Event) error {
			return mutateFirstRefOfKind(event, gateEvidenceKind)
		}}
		result, err := fixture.gate.Run(context.Background(), fixture.request)
		if err != nil || result.State != domain.StateFailed {
			t.Fatalf("accepted fallback = state %s, err %v, reason %q", result.State, err, result.FailureReason)
		}
		assertFallbackTerminal(t, fixture)
	})
}

func TestReadyForMergeRejectsMutatedTransitiveAcceptanceEvidence(t *testing.T) {
	fixture := newGateFixture(t, false, "pass")
	var mutatedRef, acceptedDecisionRef ledger.EvidenceRef
	fixture.gate.events = &mutatingEvents{delegate: fixture.events, after: domain.StateIntegrationAccepted, mutate: func(event ledger.Event) error {
		for _, ref := range event.EvidenceRefs {
			switch ref.Kind {
			case "acceptance-command-metadata":
				mutatedRef = ref
			case gateEvidenceKind:
				acceptedDecisionRef = ref
			}
		}
		if mutatedRef.URI == "" {
			return fmt.Errorf("event %s had no transitive acceptance evidence", event.StateTo)
		}
		if acceptedDecisionRef.URI == "" {
			return fmt.Errorf("event %s had no integration-accepted decision evidence", event.StateTo)
		}
		return os.WriteFile(mutatedRef.URI, []byte("mutated after durable integration acceptance"), 0o600)
	}}

	result, err := fixture.gate.Run(context.Background(), fixture.request)
	if err != nil || result.State != domain.StateFailed {
		t.Fatalf("transitive evidence fallback = state %s, err %v, reason %q", result.State, err, result.FailureReason)
	}
	for _, state := range fixture.events.states() {
		if state == domain.StateReadyForMerge {
			t.Fatal("mutated transitive acceptance evidence emitted READY_FOR_MERGE")
		}
	}
	assertLedgerContinuity(t, fixture.events.events)
	terminal := fixture.events.events[len(fixture.events.events)-1]
	if terminal.StateFrom != domain.StateIntegrationAccepted || terminal.StateTo != domain.StateFailed {
		t.Fatalf("terminal transition = %s -> %s, want INTEGRATION_ACCEPTED -> FAILED", terminal.StateFrom, terminal.StateTo)
	}
	for _, ref := range terminal.EvidenceRefs {
		if ref.URI == mutatedRef.URI && ref.SHA256 == mutatedRef.SHA256 && ref.Kind == mutatedRef.Kind {
			t.Fatalf("terminal transition retained rejected evidence: %#v", ref)
		}
	}
	assertVerifiedRefs(t, fixture.gate.evidenceRoot, terminal.EvidenceRefs)
	if _, err := evidence.ReadVerifiedLocal(fixture.gate.evidenceRoot, acceptedDecisionRef, maxEvidenceArtifactBytes); err != nil {
		t.Fatalf("integration-accepted decision artifact was not left untouched: %v", err)
	}
	assertFallbackTerminal(t, fixture)
}

func TestNormalDecisionEvidenceFailureUsesHealthyFallbackWriter(t *testing.T) {
	fixture := newGateFixture(t, false, "pass")
	fixture.replaceWriter(t, &namedFaultWriter{ArtifactWriter: fixture.store, corruptSuffix: "-decision-integration_accepted.json"})
	result, err := fixture.gate.Run(context.Background(), fixture.request)
	if err != nil || result.State != domain.StateValidationUnavailable || !strings.Contains(result.FailureReason, "EVIDENCE_PUBLICATION_FAILURE") {
		t.Fatalf("normal decision fallback = state %s, err %v, reason %q", result.State, err, result.FailureReason)
	}
	assertFallbackTerminal(t, fixture)
}

func TestFallbackWriterFailureDoesNotFabricateTransition(t *testing.T) {
	fixture := newGateFixture(t, false, "pass")
	fixture.replaceWriter(t, &namedFaultWriter{ArtifactWriter: fixture.store, failSuffix: "-decision-fallback-validation_unavailable.json"})
	fixture.gate.workspace = &badMaterializationRefWorkspace{delegate: fixture.gate.workspace}
	result, err := fixture.gate.Run(context.Background(), fixture.request)
	if err == nil || result.State != domain.StateIntegrating || !strings.Contains(result.FailureReason, "infrastructure failure") {
		t.Fatalf("fallback writer failure = state %s, err %v, reason %q", result.State, err, result.FailureReason)
	}
	assertLedgerContinuity(t, fixture.events.events)
	if states := fixture.events.states(); len(states) == 0 || states[len(states)-1] != domain.StateIntegrating {
		t.Fatalf("fallback writer failure fabricated terminal state: %v", states)
	}
}

func TestLedgerAppendFailureIsNotRetriedOrFallbackEmitted(t *testing.T) {
	fixture := newGateFixture(t, false, "pass")
	fixture.gate.events = &failingEvents{delegate: fixture.events, failState: domain.StateIntegrating}
	result, err := fixture.gate.Run(context.Background(), fixture.request)
	if err == nil || result.State != domain.StateIntegrationPending || !strings.Contains(result.FailureReason, "infrastructure failure") {
		t.Fatalf("append failure = state %s, err %v, reason %q", result.State, err, result.FailureReason)
	}
	if got := fixture.events.states(); !reflect.DeepEqual(got, []domain.State{domain.StateIntegrationPending}) {
		t.Fatalf("append failure was retried or terminalized ambiguously: %v", got)
	}
	assertLedgerContinuity(t, fixture.events.events)
}

type badMaterializationRefWorkspace struct{ delegate workspaceController }

func (w *badMaterializationRefWorkspace) Integrate(ctx context.Context, request integrationworkspace.Request) (integrationworkspace.Result, error) {
	return w.delegate.Integrate(ctx, request)
}

func (w *badMaterializationRefWorkspace) UseMaterialized(ctx context.Context, request integrationworkspace.Request, expected integrationworkspace.Result, prefix string, use func(integrationworkspace.MaterializedTarget) error) (integrationworkspace.MaterializationEvidence, error) {
	result, err := w.delegate.UseMaterialized(ctx, request, expected, prefix, use)
	result.CaptureRef.SHA256 = strings.Repeat("0", 64)
	return result, err
}

type namedFaultWriter struct {
	ArtifactWriter
	corruptSuffix string
	failSuffix    string
}

func (w *namedFaultWriter) WriteBytes(name, kind string, data []byte) (ledger.EvidenceRef, error) {
	if w.failSuffix != "" && strings.HasSuffix(name, w.failSuffix) {
		return ledger.EvidenceRef{}, fmt.Errorf("injected scoped fallback publication failure")
	}
	ref, err := w.ArtifactWriter.WriteBytes(name, kind, data)
	if err == nil && w.corruptSuffix != "" && strings.HasSuffix(name, w.corruptSuffix) {
		ref.SHA256 = strings.Repeat("0", 64)
	}
	return ref, err
}

type mutatingEvents struct {
	delegate EventAppender
	after    domain.State
	mutate   func(ledger.Event) error
}

type failingEvents struct {
	delegate  EventAppender
	failState domain.State
}

func (f *failingEvents) Append(event ledger.Event) error {
	if event.StateTo == f.failState {
		return fmt.Errorf("injected ledger append failure")
	}
	return f.delegate.Append(event)
}

func (m *mutatingEvents) Append(event ledger.Event) error {
	if err := m.delegate.Append(event); err != nil {
		return err
	}
	if event.StateTo == m.after {
		return m.mutate(event)
	}
	return nil
}

func mutateFirstRefOfKind(event ledger.Event, kind string) error {
	for _, ref := range event.EvidenceRefs {
		if ref.Kind == kind {
			return os.WriteFile(ref.URI, []byte("mutated after durable append"), 0o600)
		}
	}
	return fmt.Errorf("event %s had no evidence of kind %s", event.StateTo, kind)
}

type moveSourceAfterMaterializationWorkspace struct {
	delegate                   workspaceController
	repository, branch, target string
}

func (w *moveSourceAfterMaterializationWorkspace) Integrate(ctx context.Context, request integrationworkspace.Request) (integrationworkspace.Result, error) {
	return w.delegate.Integrate(ctx, request)
}

func (w *moveSourceAfterMaterializationWorkspace) UseMaterialized(ctx context.Context, request integrationworkspace.Request, expected integrationworkspace.Result, prefix string, use func(integrationworkspace.MaterializedTarget) error) (integrationworkspace.MaterializationEvidence, error) {
	evidence, err := w.delegate.UseMaterialized(ctx, request, expected, prefix, use)
	command := exec.Command("git", "update-ref", "refs/heads/"+w.branch, w.target)
	command.Dir = w.repository
	return evidence, errorsJoin(err, command.Run())
}

func TestCleanupFailureFailsClosed(t *testing.T) {
	fixture := newGateFixture(t, false, "pass")
	fixture.gate.workspace = &cleanupFailureWorkspace{delegate: fixture.gate.workspace}
	result, _ := fixture.gate.Run(context.Background(), fixture.request)
	if result.State != domain.StateValidationUnavailable || !strings.Contains(result.FailureReason, "cleanup failure") {
		t.Fatalf("cleanup result = %s, %q", result.State, result.FailureReason)
	}
	for _, state := range fixture.events.states() {
		if state == domain.StateIntegrationAccepted || state == domain.StateReadyForMerge {
			t.Fatalf("cleanup failure emitted %s", state)
		}
	}
}

type cleanupFailureWorkspace struct{ delegate workspaceController }

func (w *cleanupFailureWorkspace) Integrate(ctx context.Context, r integrationworkspace.Request) (integrationworkspace.Result, error) {
	return w.delegate.Integrate(ctx, r)
}
func (w *cleanupFailureWorkspace) UseMaterialized(ctx context.Context, r integrationworkspace.Request, result integrationworkspace.Result, prefix string, use func(integrationworkspace.MaterializedTarget) error) (integrationworkspace.MaterializationEvidence, error) {
	evidence, err := w.delegate.UseMaterialized(ctx, r, result, prefix, use)
	return evidence, errorsJoin(err, fmt.Errorf("cleanup failure"))
}
func errorsJoin(values ...error) error {
	var messages []string
	for _, value := range values {
		if value != nil {
			messages = append(messages, value.Error())
		}
	}
	if len(messages) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(messages, "; "))
}

type recordingEvents struct{ events []ledger.Event }

func (r *recordingEvents) Append(event ledger.Event) error {
	r.events = append(r.events, event)
	return nil
}
func (r *recordingEvents) states() []domain.State {
	values := make([]domain.State, len(r.events))
	for i, event := range r.events {
		values[i] = event.StateTo
	}
	return values
}

type gateFixture struct {
	gate                            *Gate
	request                         Request
	events                          *recordingEvents
	store                           *evidence.Store
	repository, base, temporaryRoot string
}

func (f *gateFixture) replaceWriter(t *testing.T, writer ArtifactWriter) {
	t.Helper()
	gate, err := New(Config{TemporaryRoot: f.temporaryRoot, Events: f.events, Artifacts: writer, Processes: supervisor.New()})
	if err != nil {
		t.Fatal(err)
	}
	f.gate = gate
}

func newGateFixture(t *testing.T, conflict bool, mode string) gateFixture {
	t.Helper()
	repository := t.TempDir()
	runGit(t, repository, "init", "-q", "--initial-branch=main")
	runGit(t, repository, "config", "user.name", "Gate Test")
	runGit(t, repository, "config", "user.email", "gate@example.invalid")
	runGit(t, repository, "remote", "add", "origin", "https://example.test/example/project.git")
	writeFile(t, filepath.Join(repository, "plan.md"), "plan\n")
	writeFile(t, filepath.Join(repository, "base.txt"), "base\n")
	if conflict {
		writeFile(t, filepath.Join(repository, "conflict.txt"), "base\n")
	}
	runGit(t, repository, "add", "--all")
	runGit(t, repository, "commit", "-qm", "base")
	base := gitOutput(t, repository, "rev-parse", "HEAD")
	runGit(t, repository, "checkout", "-qb", "candidate-one", base)
	name := "one.txt"
	if conflict {
		name = "conflict.txt"
	}
	writeFile(t, filepath.Join(repository, name), "one\n")
	runGit(t, repository, "add", "--all")
	runGit(t, repository, "commit", "-qm", "one")
	headOne := gitOutput(t, repository, "rev-parse", "HEAD")
	runGit(t, repository, "checkout", "-qb", "candidate-two", base)
	name = "two.txt"
	if conflict {
		name = "conflict.txt"
	}
	writeFile(t, filepath.Join(repository, name), "two\n")
	runGit(t, repository, "add", "--all")
	runGit(t, repository, "commit", "-qm", "two")
	headTwo := gitOutput(t, repository, "rev-parse", "HEAD")
	root := t.TempDir()
	store, err := evidence.NewStore(root, "gate")
	if err != nil {
		t.Fatal(err)
	}
	candidates := []scheduler.AcceptedCandidate{newCandidate(t, store, repository, "candidate-one", base, headOne, "one", 1), newCandidate(t, store, repository, "candidate-two", base, headTwo, "two", 2)}
	analyzer, err := scheduler.NewAnalyzer(scheduler.RiskPolicy{PolicyIdentity: "risk-v1"})
	if err != nil {
		t.Fatal(err)
	}
	risk, err := analyzer.Analyze(context.Background(), candidates)
	if err != nil {
		t.Fatal(err)
	}
	command := authority.AcceptanceCommand{Name: "combined", Class: "unit", Required: true, Timeout: "5s", Argv: helperArgv(mode)}
	if mode == "unavailable" {
		command.Argv = []string{"/definitely/not/a/program"}
	}
	shaB, shaC := headOne, headTwo
	reviewPolicy := ReviewPolicy{PolicyIdentity: "review-v1", Required: []ReviewRequirement{{Component: "track-b", ReviewedSHA: shaB, Verdict: VerdictCleanCriticalMajor}, {Component: "track-c", ReviewedSHA: shaC, Verdict: VerdictCleanCriticalMajor}}}
	governed, err := authority.New(authority.Manifest{RunID: "gate-run", Repository: authority.RepositoryManifest{Path: repository, Identity: "example/project", Remotes: map[string]string{"origin": "https://example.test/example/project.git"}, DefaultBranch: "main", StartSHA: base}, Plan: authority.PlanManifest{Path: filepath.Join(repository, "plan.md"), SHA256: fileDigest(t, filepath.Join(repository, "plan.md"))}, MergeReview: &reviewPolicy, Ralphex: authority.RalphexManifest{BinaryPath: os.Args[0], BinarySHA256: fileDigest(t, os.Args[0]), Mode: ralphex.ModeFull, Timeout: "5s", WaitOnLimit: "0s"}, Acceptance: []authority.AcceptanceCommand{command}, PolicyVersion: "policy-v1"})
	if err != nil {
		t.Fatal(err)
	}
	reviewOne := writeEvidence(t, store, "review-track-b.json", "review", []byte("track b clean"))
	reviewTwo := writeEvidence(t, store, "review-track-c.json", "review", []byte("track c clean"))
	request := Request{Authority: governed, BaselineSHA: base, Candidates: candidates, RiskReport: risk, CombinedPolicy: combinedacceptance.Policy{PolicyIdentity: "semantic-v1", CombinedAcceptancePolicyIdentity: "policy-v1", IntegrationPolicyIdentity: "integration-v1", RiskPolicyIdentity: "risk-v1", SemanticFailureClasses: []string{"unit"}}, ReviewPolicy: reviewPolicy, Reviews: []ReviewAttestation{{Component: "track-b", ReviewedSHA: shaB, Verdict: VerdictCleanCriticalMajor, Provider: "reviewer-b", Evidence: []ledger.EvidenceRef{reviewOne}}, {Component: "track-c", ReviewedSHA: shaC, Verdict: VerdictCleanCriticalMajor, Provider: "reviewer-c", Evidence: []ledger.EvidenceRef{reviewTwo}}}, EvidencePrefix: "serial"}
	events := &recordingEvents{}
	temporary := t.TempDir()
	gate, err := New(Config{TemporaryRoot: temporary, Events: events, Artifacts: store, Processes: supervisor.New()})
	if err != nil {
		t.Fatal(err)
	}
	return gateFixture{gate: gate, request: request, events: events, store: store, repository: repository, base: base, temporaryRoot: temporary}
}

func assertFallbackTerminal(t *testing.T, fixture gateFixture) {
	t.Helper()
	assertLedgerContinuity(t, fixture.events.events)
	if len(fixture.events.events) == 0 {
		t.Fatal("fallback emitted no terminal transition")
	}
	terminal := fixture.events.events[len(fixture.events.events)-1]
	if terminal.StateTo != domain.StateFailed && terminal.StateTo != domain.StateValidationUnavailable {
		t.Fatalf("fallback ended at nonterminal state %s", terminal.StateTo)
	}
	assertVerifiedRefs(t, fixture.gate.evidenceRoot, terminal.EvidenceRefs)
	if len(terminal.EvidenceRefs) > maxFallbackEvidenceRefs+1 {
		t.Fatalf("fallback transition evidence is not bounded: %d", len(terminal.EvidenceRefs))
	}
	foundFallback := false
	for _, ref := range terminal.EvidenceRefs {
		if ref.Kind == fallbackEvidenceKind {
			foundFallback = true
			data, err := evidence.ReadVerifiedLocal(fixture.gate.evidenceRoot, ref, maxEvidenceArtifactBytes)
			if err != nil {
				t.Fatal(err)
			}
			var record struct {
				AuthoritySHA256             string             `json:"authority_sha256"`
				RiskSHA256                  string             `json:"risk_sha256"`
				OriginalIntendedState       domain.State       `json:"original_intended_state"`
				FallbackClassification      string             `json:"fallback_classification"`
				EvidenceVerificationFailure string             `json:"evidence_verification_failure"`
				RejectedRefs                []rejectedEvidence `json:"rejected_refs"`
			}
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			if record.AuthoritySHA256 != fixture.request.Authority.SHA256() || record.RiskSHA256 != fixture.request.RiskReport.SHA256() || record.OriginalIntendedState == "" || record.FallbackClassification == "" || record.EvidenceVerificationFailure == "" || len(record.RejectedRefs) == 0 {
				t.Fatalf("fallback decision omitted bounded failure provenance: %s", data)
			}
		}
	}
	if !foundFallback {
		t.Fatalf("terminal transition omitted fresh fallback decision: %#v", terminal.EvidenceRefs)
	}
}

func assertLedgerContinuity(t *testing.T, events []ledger.Event) {
	t.Helper()
	previous := domain.StateBranchAccepted
	for index, event := range events {
		if event.StateFrom != previous {
			t.Fatalf("event %d continuity = %s -> %s after %s", index, event.StateFrom, event.StateTo, previous)
		}
		previous = event.StateTo
	}
}

func assertVerifiedRefs(t *testing.T, root string, refs []ledger.EvidenceRef) {
	t.Helper()
	for index, ref := range refs {
		if _, err := evidence.ReadVerifiedLocal(root, ref, maxEvidenceArtifactBytes); err != nil {
			t.Fatalf("terminal evidence %d did not verify independently: %#v: %v", index, ref, err)
		}
	}
}

func TestIntegrationGateHelperProcess(t *testing.T) {
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		return
	}
	switch os.Args[separator+1] {
	case "pass":
		fmt.Fprintln(os.Stdout, "pass")
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stderr, "semantic failure")
		os.Exit(17)
	case "move-head":
		command := exec.Command("git", "-c", "user.name=Gate Test", "-c", "user.email=gate@example.invalid", "commit", "--allow-empty", "-qm", "move")
		command.Dir = "."
		if err := command.Run(); err != nil {
			os.Exit(18)
		}
		os.Exit(0)
	default:
		os.Exit(91)
	}
}
func helperArgv(mode string) []string {
	return []string{os.Args[0], "-test.run=^TestIntegrationGateHelperProcess$", "--", mode}
}
func newCandidate(t *testing.T, store *evidence.Store, repository, branch, base, head, id string, offset int) scheduler.AcceptedCandidate {
	ref := writeEvidence(t, store, "acceptance-"+id+".json", "branch-acceptance", []byte("pass"))
	candidate, err := scheduler.NewAcceptedCandidate(scheduler.CandidateInput{ProjectID: "project", PlanID: "plan-" + id, RunID: "run-" + id, AttemptID: "attempt-" + id, Repository: repository, Branch: branch, StartSHA: base, HeadSHA: head, AcceptanceEvidence: []ledger.EvidenceRef{ref}, AcceptedAt: time.Unix(int64(offset), 0).UTC(), AcceptancePolicyIdentity: "policy-v1"})
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}
func writeEvidence(t *testing.T, store *evidence.Store, name, kind string, data []byte) ledger.EvidenceRef {
	t.Helper()
	ref, err := store.WriteBytes(name, kind, data)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}
func writeFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}
func runGit(t *testing.T, repository string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}
func gitOutput(t *testing.T, repository string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = repository
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(output))
}
func fileDigest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

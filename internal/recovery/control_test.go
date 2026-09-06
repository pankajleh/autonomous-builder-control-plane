package recovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/blocker"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

var controlTestTime = time.Date(2026, 9, 6, 13, 30, 0, 0, time.UTC)

func TestBlockerDecisionRequirementMatrix(t *testing.T) {
	tests := []struct {
		name        string
		from        domain.State
		diagnostics string
		want        domain.State
		requirement BlockerRequirement
	}{
		{
			name:        "human decision",
			from:        domain.StateImplementing,
			diagnostics: "human decision required",
			want:        domain.StateHumanDecisionRequired,
			requirement: BlockerRequirement{HumanDecision: &HumanDecisionRequirement{
				Question: "May the controller replace the stale attempt?", AcceptedAnswers: []string{"approve", "deny"}, RequiredAuthority: "repository-owner",
			}},
		},
		{
			name:        "secret identity",
			from:        domain.StateImplementing,
			diagnostics: "secret required",
			want:        domain.StateSecretRequired,
			requirement: BlockerRequirement{Secret: &SecretRequirement{
				SecretIdentity: "OPENAI_API_KEY", RequiredAuthority: "secret-provider",
			}},
		},
		{
			name:        "external dependency",
			from:        domain.StateImplementing,
			diagnostics: "external dependency",
			want:        domain.StateExternalDependency,
			requirement: BlockerRequirement{ExternalDependency: &ExternalDependencyRequirement{
				DependencyID: "change-window", ResolutionCondition: "approved change window is open", RequiredAuthority: "release-manager",
			}},
		},
		{
			name:        "validation unavailable",
			from:        domain.StateBranchAcceptancePending,
			diagnostics: "validator unavailable",
			want:        domain.StateValidationUnavailable,
			requirement: BlockerRequirement{Validation: &ValidationRequirement{
				ValidatorID: "go-test", RestorationCondition: "validator executes the pinned command", RequiredAuthority: "validation-controller",
			}},
		},
		{
			name:        "policy block",
			from:        domain.StateImplementing,
			diagnostics: "blocked by policy",
			want:        domain.StatePolicyBlocked,
			requirement: BlockerRequirement{Policy: &PolicyRequirement{
				PolicyRule: "recovery.owner-dead-proof", RequiredAuthority: "policy-owner",
			}},
		},
		{
			name:        "ambiguous recovery",
			from:        domain.StateImplementing,
			diagnostics: "unclassified controller failure",
			want:        domain.StateRecoveryRequired,
			requirement: BlockerRequirement{Recovery: &RecoveryRequirement{
				Reason: "no deterministic classifier rule matched", RequiredAuthority: "recovery-controller",
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, err := NewBlockerDecision(BlockerDecisionInput{
				Attempt:       controlAttempt("attempt-1"),
				StateFrom:     test.from,
				Failure:       blocker.FailureInput{Phase: blocker.PhaseExecution, Diagnostics: test.diagnostics},
				Requirement:   test.requirement,
				Actor:         "control-plane",
				Timestamp:     controlTestTime,
				PolicyVersion: "recovery-v1",
				EvidenceRefs:  []ledger.EvidenceRef{controlEvidence("classification")},
			})
			if err != nil {
				t.Fatal(err)
			}
			if decision.StateTo() != test.want {
				t.Fatalf("state = %s, want %s", decision.StateTo(), test.want)
			}
			event, err := decision.Event("recovery-controller")
			if err != nil {
				t.Fatal(err)
			}
			if event.EventType != EventBlockerDecision || event.StateFrom != test.from || event.StateTo != test.want {
				t.Fatalf("decision event = %#v", event)
			}
			if event.Actor != "control-plane" || event.AttemptID != "attempt-1" || event.TaskID != "task-1" || event.Timestamp != controlTestTime {
				t.Fatalf("decision attribution = %#v", event)
			}
			if len(event.EvidenceRefs) != 1 || event.EvidenceRefs[0] != controlEvidence("classification") {
				t.Fatalf("decision evidence = %#v", event.EvidenceRefs)
			}
		})
	}
}

func TestBlockerDecisionRejectsMissingOrMismatchedRequirement(t *testing.T) {
	base := BlockerDecisionInput{
		Attempt:       controlAttempt("attempt-1"),
		StateFrom:     domain.StateImplementing,
		Failure:       blocker.FailureInput{Phase: blocker.PhaseExecution, Diagnostics: "secret required"},
		Actor:         "control-plane",
		Timestamp:     controlTestTime,
		PolicyVersion: "recovery-v1",
		EvidenceRefs:  []ledger.EvidenceRef{controlEvidence("classification")},
	}
	if _, err := NewBlockerDecision(base); err == nil {
		t.Fatal("missing secret requirement was accepted")
	}
	base.Requirement.HumanDecision = &HumanDecisionRequirement{
		Question: "Use credentials?", AcceptedAnswers: []string{"yes", "no"}, RequiredAuthority: "operator",
	}
	if _, err := NewBlockerDecision(base); err == nil {
		t.Fatal("mismatched human requirement was accepted for SECRET_REQUIRED")
	}
	base.Requirement = BlockerRequirement{Secret: &SecretRequirement{SecretIdentity: "TOKEN=value", RequiredAuthority: "secret-provider"}}
	if _, err := NewBlockerDecision(base); err == nil {
		t.Fatal("secret value-shaped identity was accepted")
	}
}

func TestBlockerDecisionRedactsSecretBeforeSerializationAndLedgerAppend(t *testing.T) {
	const secretValue = "fake-secret-material-123456"
	decision, err := NewBlockerDecision(BlockerDecisionInput{
		Attempt:   controlAttempt("attempt-1"),
		StateFrom: domain.StateImplementing,
		Failure: blocker.FailureInput{
			Phase: blocker.PhaseExecution, Diagnostics: "secret required; api_key=" + secretValue,
		},
		Requirement: BlockerRequirement{Secret: &SecretRequirement{
			SecretIdentity: "OPENAI_API_KEY", RequiredAuthority: "secret-provider",
		}},
		Actor:         "operator:alice",
		Timestamp:     controlTestTime,
		PolicyVersion: "recovery-v1",
		EvidenceRefs:  []ledger.EvidenceRef{controlEvidence("redacted-diagnostic")},
	})
	if err != nil {
		t.Fatal(err)
	}
	recordJSON, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(recordJSON), secretValue) || !strings.Contains(string(recordJSON), `"secret_identity":"OPENAI_API_KEY"`) {
		t.Fatalf("unsafe or incomplete blocker record: %s", recordJSON)
	}

	event, err := decision.Event("recovery-controller")
	if err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(t.TempDir(), "events.jsonl")
	events, err := ledger.NewJSONLLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := events.Append(event); err != nil {
		t.Fatal(err)
	}
	ledgerJSON, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ledgerJSON), secretValue) || event.Actor != "operator:alice" {
		t.Fatalf("unsafe or unattributed event: %s", ledgerJSON)
	}
}

func TestResumeAuthorityBindsActorAttemptsTransitionAndEvidence(t *testing.T) {
	prior := controlAttempt("attempt-1")
	authorized := controlAttempt("attempt-2")
	authorized.AgentSessionID = "session-2"
	authority, err := NewResumeAuthority(ResumeAuthorityInput{
		PriorAttempt:      prior,
		AuthorizedAttempt: authorized,
		StateFrom:         domain.StateRecoveryRequired,
		StateTo:           domain.StateExecutionStarting,
		Actor:             "operator:alice",
		Decision:          "approve replacement after owner-dead snapshot",
		Action:            ResumeActionRestart,
		Timestamp:         controlTestTime,
		PolicyVersion:     "recovery-v1",
		EvidenceRefs:      []ledger.EvidenceRef{controlEvidence("recovery-snapshot-metadata")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateResumeAuthority(&authority, prior, authorized, domain.StateRecoveryRequired, domain.StateExecutionStarting); err != nil {
		t.Fatal(err)
	}
	event, err := authority.Event("recovery-controller")
	if err != nil {
		t.Fatal(err)
	}
	if event.EventType != EventResumeAuthority || event.Actor != "operator:alice" || event.AttemptID != "attempt-2" {
		t.Fatalf("resume authority attribution = %#v", event)
	}
	if event.StateFrom != domain.StateRecoveryRequired || event.StateTo != domain.StateExecutionStarting {
		t.Fatalf("resume authority transition = %s -> %s", event.StateFrom, event.StateTo)
	}
	if len(event.EvidenceRefs) != 1 || event.EvidenceRefs[0].Kind != "recovery-snapshot-metadata" {
		t.Fatalf("resume authority evidence = %#v", event.EvidenceRefs)
	}
	payloadPrior, ok := event.Payload["prior_attempt"].(AttemptIdentity)
	if !ok || payloadPrior != prior {
		t.Fatalf("prior attempt payload = %#v", event.Payload["prior_attempt"])
	}
}

func TestResumeAuthorityRejectsImplicitOrRetargetedUse(t *testing.T) {
	prior := controlAttempt("attempt-1")
	authorized := controlAttempt("attempt-2")
	if err := ValidateResumeAuthority(nil, prior, authorized, domain.StateRecoveryRequired, domain.StateExecutionStarting); err == nil {
		t.Fatal("missing resume authority was accepted")
	}
	var zero ResumeAuthority
	if _, err := zero.Event("recovery-controller"); err == nil {
		t.Fatal("zero resume authority emitted an event")
	}

	authority := mustResumeAuthority(t, prior, authorized)
	wrongPrior := prior
	wrongPrior.RunID = "run-2"
	if err := ValidateResumeAuthority(&authority, wrongPrior, authorized, domain.StateRecoveryRequired, domain.StateExecutionStarting); err == nil {
		t.Fatal("authority was reused for the wrong prior run")
	}
	wrongAuthorized := authorized
	wrongAuthorized.AttemptID = "attempt-3"
	if err := ValidateResumeAuthority(&authority, prior, wrongAuthorized, domain.StateRecoveryRequired, domain.StateExecutionStarting); err == nil {
		t.Fatal("authority was reused for the wrong authorized attempt")
	}
	if err := ValidateResumeAuthority(&authority, prior, authorized, domain.StateRecoveryRequired, domain.StateAuthorityValidated); err == nil {
		t.Fatal("authority was reused for a different state transition")
	}
}

func TestResumeAuthorityRejectsMissingAttributionAndAction(t *testing.T) {
	prior := controlAttempt("attempt-1")
	authorized := controlAttempt("attempt-2")
	valid := ResumeAuthorityInput{
		PriorAttempt:      prior,
		AuthorizedAttempt: authorized,
		StateFrom:         domain.StateRecoveryRequired,
		StateTo:           domain.StateExecutionStarting,
		Actor:             "operator:alice",
		Decision:          "approve restart",
		Action:            ResumeActionRestart,
		Timestamp:         controlTestTime,
		PolicyVersion:     "recovery-v1",
		EvidenceRefs:      []ledger.EvidenceRef{controlEvidence("recovery-snapshot-metadata")},
	}
	tests := []struct {
		name   string
		mutate func(*ResumeAuthorityInput)
	}{
		{"actor", func(input *ResumeAuthorityInput) { input.Actor = "" }},
		{"decision", func(input *ResumeAuthorityInput) { input.Decision = "" }},
		{"action", func(input *ResumeAuthorityInput) { input.Action = "" }},
		{"timestamp", func(input *ResumeAuthorityInput) { input.Timestamp = time.Time{} }},
		{"policy version", func(input *ResumeAuthorityInput) { input.PolicyVersion = "" }},
		{"evidence", func(input *ResumeAuthorityInput) { input.EvidenceRefs = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			test.mutate(&input)
			if _, err := NewResumeAuthority(input); err == nil {
				t.Fatalf("missing %s was accepted", test.name)
			}
		})
	}
}

func TestResumeAuthorityEnforcesAttemptActionAndStateMachine(t *testing.T) {
	prior := controlAttempt("attempt-1")
	authorized := controlAttempt("attempt-2")
	base := ResumeAuthorityInput{
		PriorAttempt:      prior,
		AuthorizedAttempt: authorized,
		StateFrom:         domain.StateRecoveryRequired,
		StateTo:           domain.StateExecutionStarting,
		Actor:             "operator:alice",
		Decision:          "approve restart",
		Action:            ResumeActionRestart,
		Timestamp:         controlTestTime,
		PolicyVersion:     "recovery-v1",
		EvidenceRefs:      []ledger.EvidenceRef{controlEvidence("recovery-snapshot-metadata")},
	}

	sameAttempt := base
	sameAttempt.AuthorizedAttempt = prior
	if _, err := NewResumeAuthority(sameAttempt); err == nil {
		t.Fatal("restart without a new attempt ID was accepted")
	}
	wrongRun := base
	wrongRun.AuthorizedAttempt.RunID = "run-2"
	if _, err := NewResumeAuthority(wrongRun); err == nil {
		t.Fatal("restart into a different run was accepted")
	}
	forbidden := base
	forbidden.StateTo = domain.StateReadyForMerge
	if _, err := NewResumeAuthority(forbidden); err == nil {
		t.Fatal("forbidden recovery-to-merge transition was accepted")
	}
	terminal := base
	terminal.StateFrom = domain.StateFailed
	if _, err := NewResumeAuthority(terminal); err == nil {
		t.Fatal("terminal FAILED state was implicitly resumed")
	}

	resume := base
	resume.PriorAttempt = prior
	resume.AuthorizedAttempt = prior
	resume.StateFrom = domain.StateSecretRequired
	resume.StateTo = domain.StateAuthorityValidated
	resume.Action = ResumeActionResume
	resume.Decision = "required secret identity is now available under policy"
	if _, err := NewResumeAuthority(resume); err != nil {
		t.Fatalf("valid same-attempt resume rejected: %v", err)
	}
}

func mustResumeAuthority(t *testing.T, prior, authorized AttemptIdentity) ResumeAuthority {
	t.Helper()
	authority, err := NewResumeAuthority(ResumeAuthorityInput{
		PriorAttempt:      prior,
		AuthorizedAttempt: authorized,
		StateFrom:         domain.StateRecoveryRequired,
		StateTo:           domain.StateExecutionStarting,
		Actor:             "operator:alice",
		Decision:          "approve restart",
		Action:            ResumeActionRestart,
		Timestamp:         controlTestTime,
		PolicyVersion:     "recovery-v1",
		EvidenceRefs:      []ledger.EvidenceRef{controlEvidence("recovery-snapshot-metadata")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func controlAttempt(attemptID string) AttemptIdentity {
	return AttemptIdentity{
		ProjectID: "project-1", PlanID: "plan-1", RunID: "run-1",
		AttemptID: attemptID, TaskID: "task-1", AgentSessionID: "session-1",
	}
}

func controlEvidence(kind string) ledger.EvidenceRef {
	return ledger.EvidenceRef{
		URI:    "evidence/run-1/" + kind + ".json",
		SHA256: strings.Repeat("a", 64),
		Kind:   kind,
	}
}

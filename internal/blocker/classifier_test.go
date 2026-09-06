package blocker

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
)

func TestClassifyFailureTaxonomy(t *testing.T) {
	tests := []struct {
		name         string
		input        FailureInput
		class        FailureClass
		state        domain.State
		action       RecommendedAction
		retryability Retryability
		automatic    bool
	}{
		{
			name: "EXP-03 transient retry",
			input: FailureInput{Phase: PhaseExecution, Outcome: supervisor.OutcomeExited, ExitCode: 1,
				Diagnostics: "task failed, retrying..."},
			class: ClassTransientExecution, state: domain.StateRetrying,
			action: ActionRetryExecution, retryability: RetryableNow, automatic: true,
		},
		{
			name: "EXP-06 capacity wait",
			input: FailureInput{Phase: PhaseExecution, Outcome: supervisor.OutcomeExited, ExitCode: 1,
				Diagnostics: `detected limit pattern: "Selected model is at capacity"`},
			class: ClassCapacityRateLimit, state: domain.StateCapacityWait,
			action: ActionWaitForCapacity, retryability: RetryableAfterWait, automatic: true,
		},
		{
			name: "hard quota",
			input: FailureInput{Phase: PhaseExecution, Outcome: supervisor.OutcomeExited, ExitCode: 1,
				Diagnostics: "request rejected: insufficient_quota"},
			class: ClassHardQuotaExhausted, state: domain.StateHumanDecisionRequired,
			action: ActionRequestUsageDecision, retryability: RetryRequiresAuthority,
		},
		{
			name: "authentication",
			input: FailureInput{Phase: PhaseExecution, Outcome: supervisor.OutcomeExited, ExitCode: 1,
				Diagnostics: "authentication failed: invalid api key"},
			class: ClassAuthentication, state: domain.StateSecretRequired,
			action: ActionResolveCredentials, retryability: RetryableAfterChange,
		},
		{
			name: "billing account",
			input: FailureInput{Phase: PhaseExecution, Outcome: supervisor.OutcomeExited, ExitCode: 1,
				Diagnostics: "billing account suspended; payment required"},
			class: ClassBillingAccount, state: domain.StateHumanDecisionRequired,
			action: ActionResolveAccount, retryability: RetryableAfterChange,
		},
		{
			name: "validator unavailable from diagnostic",
			input: FailureInput{Phase: PhaseValidation, Outcome: supervisor.OutcomeExited, ExitCode: 127,
				Diagnostics: "required validator cannot run"},
			class: ClassValidationUnavailable, state: domain.StateValidationUnavailable,
			action: ActionRestoreValidator, retryability: RetryableAfterChange,
		},
		{
			name: "policy block",
			input: FailureInput{Phase: PhaseExecution, Outcome: supervisor.OutcomeExited, ExitCode: 1,
				Diagnostics: "operation disallowed by policy"},
			class: ClassPolicyBlocked, state: domain.StatePolicyBlocked,
			action: ActionRequestPolicyChange, retryability: RetryRequiresAuthority,
		},
		{
			name: "external dependency",
			input: FailureInput{Phase: PhaseExecution, Outcome: supervisor.OutcomeExited, ExitCode: 1,
				Diagnostics: "external dependency is awaiting vendor approval"},
			class: ClassExternalDependency, state: domain.StateExternalDependency,
			action: ActionWaitForDependency, retryability: RetryableAfterChange,
		},
		{
			name: "secret requirement",
			input: FailureInput{Phase: PhaseExecution, Outcome: supervisor.OutcomeExited, ExitCode: 1,
				Diagnostics: "required secret is unavailable: DEPLOY_TOKEN"},
			class: ClassSecretRequired, state: domain.StateSecretRequired,
			action: ActionRequestSecret, retryability: RetryableAfterChange,
		},
		{
			name: "EXP-05 human decision",
			input: FailureInput{Phase: PhaseExecution, Outcome: supervisor.OutcomeExited, ExitCode: 1,
				Diagnostics: "TASK_FAILED: authority is unavailable"},
			class: ClassHumanDecision, state: domain.StateHumanDecisionRequired,
			action: ActionRequestHumanDecision, retryability: RetryRequiresAuthority,
		},
		{
			name: "unrecoverable",
			input: FailureInput{Phase: PhaseExecution, Outcome: supervisor.OutcomeExited, ExitCode: 70,
				Diagnostics: "fatal invariant violation: repository corruption"},
			class: ClassUnrecoverable, state: domain.StateFailed,
			action: ActionTerminateAttempt, retryability: NotRetryable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Classify(test.input)
			if got.Class != test.class || got.RecommendedState != test.state || got.RecommendedAction != test.action {
				t.Fatalf("Classify() = class %s, state %s, action %s; want %s, %s, %s", got.Class, got.RecommendedState, got.RecommendedAction, test.class, test.state, test.action)
			}
			if got.Retryability != test.retryability || got.AutomaticRetryAllowed != test.automatic {
				t.Fatalf("retry recommendation = %s, automatic=%t; want %s, automatic=%t", got.Retryability, got.AutomaticRetryAllowed, test.retryability, test.automatic)
			}
			if got.Confidence != ConfidenceHigh {
				t.Fatalf("confidence = %s, want %s", got.Confidence, ConfidenceHigh)
			}
			if len(got.EvidenceBasis) == 0 {
				t.Fatal("classification omitted evidence basis")
			}
			if got.DestructiveActionAllowed {
				t.Fatal("classifier granted destructive authority")
			}
		})
	}
}

func TestClassifyStructuredMetadata(t *testing.T) {
	tests := []struct {
		name       string
		input      FailureInput
		class      FailureClass
		confidence Confidence
	}{
		{"execution timeout", FailureInput{Phase: PhaseExecution, Outcome: supervisor.OutcomeTimedOut, ExitCode: -1}, ClassTransientExecution, ConfidenceMedium},
		{"temporary exit code", FailureInput{Phase: PhaseExecution, Outcome: supervisor.OutcomeExited, ExitCode: 75}, ClassTransientExecution, ConfidenceMedium},
		{"validator launch unavailable", FailureInput{Phase: PhaseValidation, Unavailable: true, ExitCode: -1}, ClassValidationUnavailable, ConfidenceHigh},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Classify(test.input)
			if got.Class != test.class || got.Confidence != test.confidence {
				t.Fatalf("Classify() = %s/%s, want %s/%s", got.Class, got.Confidence, test.class, test.confidence)
			}
		})
	}
}

func TestClassifyUnknownAndConflictingSignalsFailClosed(t *testing.T) {
	tests := []FailureInput{
		{Phase: PhaseExecution, Outcome: supervisor.OutcomeExited, ExitCode: 2, Diagnostics: "command failed"},
		{Phase: PhaseExecution, Outcome: supervisor.OutcomeExited, ExitCode: 1, Diagnostics: "rate limit followed by billing account suspension"},
		{Phase: PhaseExecution, Outcome: supervisor.OutcomeCanceled, ExitCode: -1},
	}
	for _, input := range tests {
		got := Classify(input)
		if got.Class != ClassUnknownAmbiguous || got.Confidence != ConfidenceAmbiguous {
			t.Fatalf("Classify(%q) = %s/%s, want fail-closed unknown", input.Diagnostics, got.Class, got.Confidence)
		}
		if got.RecommendedState != domain.StateRecoveryRequired || got.RecommendedAction != ActionInspectAndAuthorize {
			t.Fatalf("unknown recommendation = %s/%s", got.RecommendedState, got.RecommendedAction)
		}
		if got.AutomaticRetryAllowed || got.DestructiveActionAllowed {
			t.Fatal("unknown classification allowed an automatic or destructive action")
		}
		if got.Retryability != RetryRequiresAuthority {
			t.Fatalf("unknown retryability = %s", got.Retryability)
		}
	}
}

func TestClassifyRedactsCredentialLikeDiagnostics(t *testing.T) {
	privateKey := "-----BEGIN PRIVATE KEY-----\nvery-private-material\n-----END PRIVATE KEY-----"
	secrets := []string{
		"super-secret-password", "bearer-value-123456", "api-secret-123456",
		"url-password", "sk-abcdefghijklmnop", "ghp_abcdefghijklmnop",
		"glpat-abcdefghijklmnop", "npm_abcdefghijklmnop", "xoxb-abcdefghijklmnop",
		"AKIAABCDEFGHIJKLMNOP", "jwtpayloadvalue", "very-private-material",
		"aws-secret-access-value",
	}
	diagnostic := strings.Join([]string{
		"authentication failed",
		"password=super-secret-password",
		"Authorization: Bearer bearer-value-123456",
		`"api_key": "api-secret-123456"`,
		"AWS_SECRET_ACCESS_KEY=aws-secret-access-value",
		"remote=https://user:url-password@example.test/repo",
		"tokens sk-abcdefghijklmnop ghp_abcdefghijklmnop glpat-abcdefghijklmnop npm_abcdefghijklmnop xoxb-abcdefghijklmnop AKIAABCDEFGHIJKLMNOP",
		"jwt eyJheaderpart.jwtpayloadvalue.jwtsignaturevalue",
		privateKey,
	}, "\n")

	got := Classify(FailureInput{Phase: PhaseExecution, Outcome: supervisor.OutcomeExited, ExitCode: 1, Diagnostics: diagnostic})
	if got.Class != ClassAuthentication {
		t.Fatalf("class = %s, want %s", got.Class, ClassAuthentication)
	}
	if !strings.Contains(got.Diagnostics, "[REDACTED]") {
		t.Fatal("safe diagnostics did not record redaction")
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("classifier payload persisted secret %q", secret)
		}
	}
}

func TestClassifyBoundsDiagnosticsAndIsDeterministic(t *testing.T) {
	input := FailureInput{
		Phase:       PhaseExecution,
		Outcome:     supervisor.OutcomeExited,
		ExitCode:    1,
		Diagnostics: "temporary failure password=hidden-value " + strings.Repeat("x", maxSafeDiagnosticBytes*2),
	}
	first := Classify(input)
	second := Classify(input)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated classification differed:\nfirst: %#v\nsecond: %#v", first, second)
	}
	if !first.DiagnosticsTruncated || len(first.Diagnostics) > maxSafeDiagnosticBytes {
		t.Fatalf("diagnostic bounds = truncated %t, bytes %d", first.DiagnosticsTruncated, len(first.Diagnostics))
	}
	if strings.Contains(first.Diagnostics, "hidden-value") {
		t.Fatal("bounded diagnostics retained a secret value")
	}
}

func TestClassifyChecksSignalsBeyondPersistedDiagnosticLimit(t *testing.T) {
	input := FailureInput{
		Phase:       PhaseExecution,
		Outcome:     supervisor.OutcomeExited,
		ExitCode:    1,
		Diagnostics: "temporary failure " + strings.Repeat("x", maxSafeDiagnosticBytes) + " billing account suspended",
	}
	got := Classify(input)
	if got.Class != ClassUnknownAmbiguous || got.Confidence != ConfidenceAmbiguous {
		t.Fatalf("classification = %s/%s, want conflicting signals to fail closed", got.Class, got.Confidence)
	}
	if got.AutomaticRetryAllowed || got.DestructiveActionAllowed {
		t.Fatal("conflicting signal beyond persisted limit allowed action")
	}
	if !got.DiagnosticsTruncated || strings.Contains(got.Diagnostics, "billing account") {
		t.Fatal("persisted diagnostic was not independently bounded")
	}
}

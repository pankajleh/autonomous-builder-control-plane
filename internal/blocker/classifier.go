// Package blocker deterministically classifies execution failures without
// granting recovery or resume authority.
package blocker

import (
	"regexp"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
)

const maxSafeDiagnosticBytes = 16 << 10

// FailureClass is the controller's evidence-backed failure or blocker class.
type FailureClass string

const (
	ClassTransientExecution    FailureClass = "TRANSIENT_EXECUTION_FAILURE"
	ClassCapacityRateLimit     FailureClass = "CAPACITY_RATE_LIMIT"
	ClassHardQuotaExhausted    FailureClass = "HARD_QUOTA_EXHAUSTED"
	ClassAuthentication        FailureClass = "AUTHENTICATION_AUTHORIZATION"
	ClassBillingAccount        FailureClass = "BILLING_ACCOUNT_FAILURE"
	ClassValidationUnavailable FailureClass = "VALIDATION_UNAVAILABLE"
	ClassPolicyBlocked         FailureClass = "POLICY_BLOCKED"
	ClassExternalDependency    FailureClass = "EXTERNAL_DEPENDENCY"
	ClassSecretRequired        FailureClass = "SECRET_REQUIRED"
	ClassHumanDecision         FailureClass = "HUMAN_DECISION_REQUIRED"
	ClassUnrecoverable         FailureClass = "UNRECOVERABLE_FAILURE"
	ClassUnknownAmbiguous      FailureClass = "UNKNOWN_AMBIGUOUS"
)

// Confidence describes how directly the available evidence supports a class.
type Confidence string

const (
	ConfidenceHigh      Confidence = "HIGH"
	ConfidenceMedium    Confidence = "MEDIUM"
	ConfidenceAmbiguous Confidence = "AMBIGUOUS"
)

// Retryability states whether controller policy may retry without obtaining
// new external authority. It does not authorize worktree cleanup.
type Retryability string

const (
	RetryableNow           Retryability = "RETRYABLE"
	RetryableAfterWait     Retryability = "RETRYABLE_AFTER_WAIT"
	RetryableAfterChange   Retryability = "RETRYABLE_AFTER_CONDITION_CHANGE"
	RetryRequiresAuthority Retryability = "REQUIRES_EXPLICIT_AUTHORITY"
	NotRetryable           Retryability = "NOT_RETRYABLE"
)

// RecommendedAction is the next non-destructive controller action.
type RecommendedAction string

const (
	ActionRetryExecution       RecommendedAction = "RETRY_EXECUTION"
	ActionWaitForCapacity      RecommendedAction = "WAIT_FOR_CAPACITY"
	ActionRequestUsageDecision RecommendedAction = "REQUEST_USAGE_DECISION"
	ActionResolveCredentials   RecommendedAction = "RESOLVE_CREDENTIALS"
	ActionResolveAccount       RecommendedAction = "RESOLVE_ACCOUNT"
	ActionRestoreValidator     RecommendedAction = "RESTORE_VALIDATOR"
	ActionRequestPolicyChange  RecommendedAction = "REQUEST_POLICY_CHANGE"
	ActionWaitForDependency    RecommendedAction = "WAIT_FOR_DEPENDENCY"
	ActionRequestSecret        RecommendedAction = "REQUEST_SECRET"
	ActionRequestHumanDecision RecommendedAction = "REQUEST_HUMAN_DECISION"
	ActionTerminateAttempt     RecommendedAction = "TERMINATE_ATTEMPT"
	ActionInspectAndAuthorize  RecommendedAction = "INSPECT_AND_AUTHORIZE"
)

// FailurePhase identifies the controller-owned operation that failed.
type FailurePhase string

const (
	PhaseExecution  FailurePhase = "EXECUTION"
	PhaseValidation FailurePhase = "VALIDATION"
)

// FailureInput combines supervised process metadata with bounded diagnostic
// input. Unavailable means the operation could not be invoked or observed; it
// is distinct from an invoked validator returning a failing result.
type FailureInput struct {
	Phase       FailurePhase       `json:"phase"`
	Outcome     supervisor.Outcome `json:"outcome,omitempty"`
	ExitCode    int                `json:"exit_code,omitempty"`
	Unavailable bool               `json:"unavailable,omitempty"`
	Diagnostics string             `json:"diagnostics,omitempty"`
}

// Classification is safe to persist. Diagnostics is redacted and bounded;
// EvidenceBasis contains only fixed rule identifiers, never matched text.
type Classification struct {
	Class                    FailureClass      `json:"class"`
	Confidence               Confidence        `json:"confidence"`
	EvidenceBasis            []string          `json:"evidence_basis"`
	Retryability             Retryability      `json:"retryability"`
	RecommendedState         domain.State      `json:"recommended_state"`
	RecommendedAction        RecommendedAction `json:"recommended_action"`
	AutomaticRetryAllowed    bool              `json:"automatic_retry_allowed"`
	DestructiveActionAllowed bool              `json:"destructive_action_allowed"`
	Diagnostics              string            `json:"diagnostics,omitempty"`
	DiagnosticsTruncated     bool              `json:"diagnostics_truncated"`
}

type diagnosticRule struct {
	class   FailureClass
	basis   string
	phrases []string
}

var diagnosticRules = []diagnosticRule{
	{ClassTransientExecution, "diagnostic:transient-execution", []string{
		"transient failure", "temporary failure", "task failed, retrying",
		"connection reset", "connection timed out", "network timeout",
		"service temporarily unavailable", "try again later",
	}},
	{ClassCapacityRateLimit, "diagnostic:capacity-rate-limit", []string{
		"selected model is at capacity", "rate limit", "rate_limit",
		"too many requests", "http 429", "status code 429", "status=429",
	}},
	{ClassHardQuotaExhausted, "diagnostic:hard-quota", []string{
		"insufficient_quota", "quota exceeded", "quota has been exceeded",
		"usage limit reached", "usage limit exceeded", "monthly limit reached",
		"hard limit reached", "credit balance exhausted", "out of credits",
	}},
	{ClassAuthentication, "diagnostic:authentication-authorization", []string{
		"authentication failed", "authentication required", "invalid api key",
		"invalid access token", "unauthorized", "authorization failed",
		"not authorized", "http 401", "status code 401",
		"permission denied by provider", "forbidden by provider",
	}},
	{ClassBillingAccount, "diagnostic:billing-account", []string{
		"billing account", "billing failure", "payment required", "payment method",
		"account suspended", "account disabled", "delinquent account", "http 402",
	}},
	{ClassValidationUnavailable, "diagnostic:validator-unavailable", []string{
		"validator unavailable", "validation unavailable", "validation tool not found",
		"validator could not be started", "required validator cannot run",
	}},
	{ClassPolicyBlocked, "diagnostic:policy-block", []string{
		"policy blocked", "blocked by policy", "violates configured policy",
		"disallowed by policy", "policy violation", "operation not permitted by policy",
	}},
	{ClassExternalDependency, "diagnostic:external-dependency", []string{
		"external dependency", "waiting for external approval", "dependency unavailable",
		"upstream service unavailable", "dns resolution failed", "no route to host",
	}},
	{ClassSecretRequired, "diagnostic:secret-required", []string{
		"secret required", "missing api key", "api key is required",
		"missing access token", "credential is required", "credentials are required",
		"credential unavailable", "required secret is unavailable",
	}},
	{ClassHumanDecision, "diagnostic:human-decision", []string{
		"human decision required", "decision_required", "authority_missing",
		"authority is unavailable", "missing decision authority",
		"human authorization required", "operator decision required",
		"manual approval required",
	}},
	{ClassUnrecoverable, "diagnostic:unrecoverable", []string{
		"unrecoverable failure", "fatal invariant violation", "repository corruption",
		"corrupted repository", "data corruption", "permanent failure",
	}},
}

var (
	privateKeyPattern        = regexp.MustCompile(`(?is)-----BEGIN [^-\r\n]*PRIVATE KEY-----.*?-----END [^-\r\n]*PRIVATE KEY-----`)
	urlCredentialPattern     = regexp.MustCompile(`(?i)(https?://)[^/\s:@]+:[^/\s@]+@`)
	authorizationPattern     = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer|basic)\s+)[^\s,;]+`)
	secretAssignmentPattern  = regexp.MustCompile(`(?i)(["']?)([A-Za-z0-9_-]*(?:api[_-]?key|access[_-]?token|refresh[_-]?token|token|password|passwd|secret|credential)[A-Za-z0-9_-]*)(["']?)(\s*[:=]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;]+)`)
	standaloneSecretPatterns = []*regexp.Regexp{
		regexp.MustCompile(`\b(?:sk|rk|pk)-[A-Za-z0-9_-]{12,}\b`),
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{12,}\b`),
		regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{12,}\b`),
		regexp.MustCompile(`\bnpm_[A-Za-z0-9]{12,}\b`),
		regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{12,}\b`),
		regexp.MustCompile(`\bAKIA[A-Z0-9]{16}\b`),
		regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}(?:\.[A-Za-z0-9_-]{8,})?\b`),
	}
)

// Classify deterministically derives a failure class and safe recommendation.
// Conflicting or insufficient signals fail closed to RECOVERY_REQUIRED. No
// classification, including a retryable one, grants destructive authority.
func Classify(input FailureInput) Classification {
	redactedDiagnostics := redactDiagnostics(input.Diagnostics)
	safeDiagnostics, truncated := boundDiagnostics(redactedDiagnostics)
	result := Classification{
		Diagnostics:          safeDiagnostics,
		DiagnosticsTruncated: truncated,
	}

	// Classify the complete redacted input, not just the bounded excerpt that
	// is safe to persist. A conflicting signal beyond the excerpt must still
	// force an ambiguous, fail-closed result.
	lower := strings.ToLower(redactedDiagnostics)
	var matches []diagnosticRule
	for _, rule := range diagnosticRules {
		if containsAny(lower, rule.phrases) {
			matches = append(matches, rule)
		}
	}
	if len(matches) > 1 {
		result.EvidenceBasis = make([]string, 0, len(matches)+1)
		for _, match := range matches {
			result.EvidenceBasis = append(result.EvidenceBasis, match.basis)
		}
		result.EvidenceBasis = append(result.EvidenceBasis, "classifier:conflicting-signals")
		return applyRecommendation(result, ClassUnknownAmbiguous, ConfidenceAmbiguous)
	}
	if len(matches) == 1 {
		result.EvidenceBasis = []string{matches[0].basis}
		return applyRecommendation(result, matches[0].class, ConfidenceHigh)
	}

	if input.Phase == PhaseValidation && input.Unavailable {
		result.EvidenceBasis = []string{"metadata:validator-unavailable"}
		return applyRecommendation(result, ClassValidationUnavailable, ConfidenceHigh)
	}
	if input.Phase == PhaseExecution {
		switch {
		case input.Outcome == supervisor.OutcomeTimedOut:
			result.EvidenceBasis = []string{"metadata:execution-timeout"}
			return applyRecommendation(result, ClassTransientExecution, ConfidenceMedium)
		case input.Outcome == supervisor.OutcomeWaitDelay:
			result.EvidenceBasis = []string{"metadata:execution-pipe-wait-delay"}
			return applyRecommendation(result, ClassTransientExecution, ConfidenceMedium)
		case input.Outcome == supervisor.OutcomeExited && input.ExitCode == 75:
			result.EvidenceBasis = []string{"metadata:temporary-failure-exit-75"}
			return applyRecommendation(result, ClassTransientExecution, ConfidenceMedium)
		}
	}

	result.EvidenceBasis = []string{"classifier:no-specific-rule-matched"}
	return applyRecommendation(result, ClassUnknownAmbiguous, ConfidenceAmbiguous)
}

func applyRecommendation(result Classification, class FailureClass, confidence Confidence) Classification {
	result.Class = class
	result.Confidence = confidence
	// DestructiveActionAllowed intentionally remains false. Cleanup and restart
	// authority are separate controller records, never classifier side effects.
	switch class {
	case ClassTransientExecution:
		result.Retryability = RetryableNow
		result.RecommendedState = domain.StateRetrying
		result.RecommendedAction = ActionRetryExecution
		result.AutomaticRetryAllowed = true
	case ClassCapacityRateLimit:
		result.Retryability = RetryableAfterWait
		result.RecommendedState = domain.StateCapacityWait
		result.RecommendedAction = ActionWaitForCapacity
		result.AutomaticRetryAllowed = true
	case ClassHardQuotaExhausted:
		result.Retryability = RetryRequiresAuthority
		result.RecommendedState = domain.StateHumanDecisionRequired
		result.RecommendedAction = ActionRequestUsageDecision
	case ClassAuthentication:
		result.Retryability = RetryableAfterChange
		result.RecommendedState = domain.StateSecretRequired
		result.RecommendedAction = ActionResolveCredentials
	case ClassBillingAccount:
		result.Retryability = RetryableAfterChange
		result.RecommendedState = domain.StateHumanDecisionRequired
		result.RecommendedAction = ActionResolveAccount
	case ClassValidationUnavailable:
		result.Retryability = RetryableAfterChange
		result.RecommendedState = domain.StateValidationUnavailable
		result.RecommendedAction = ActionRestoreValidator
	case ClassPolicyBlocked:
		result.Retryability = RetryRequiresAuthority
		result.RecommendedState = domain.StatePolicyBlocked
		result.RecommendedAction = ActionRequestPolicyChange
	case ClassExternalDependency:
		result.Retryability = RetryableAfterChange
		result.RecommendedState = domain.StateExternalDependency
		result.RecommendedAction = ActionWaitForDependency
	case ClassSecretRequired:
		result.Retryability = RetryableAfterChange
		result.RecommendedState = domain.StateSecretRequired
		result.RecommendedAction = ActionRequestSecret
	case ClassHumanDecision:
		result.Retryability = RetryRequiresAuthority
		result.RecommendedState = domain.StateHumanDecisionRequired
		result.RecommendedAction = ActionRequestHumanDecision
	case ClassUnrecoverable:
		result.Retryability = NotRetryable
		result.RecommendedState = domain.StateFailed
		result.RecommendedAction = ActionTerminateAttempt
	default:
		result.Retryability = RetryRequiresAuthority
		result.RecommendedState = domain.StateRecoveryRequired
		result.RecommendedAction = ActionInspectAndAuthorize
	}
	return result
}

func containsAny(diagnostic string, phrases []string) bool {
	for _, phrase := range phrases {
		if strings.Contains(diagnostic, phrase) {
			return true
		}
	}
	return false
}

func redactDiagnostics(diagnostic string) string {
	diagnostic = strings.ToValidUTF8(diagnostic, "�")
	diagnostic = privateKeyPattern.ReplaceAllString(diagnostic, "[REDACTED PRIVATE KEY]")
	diagnostic = urlCredentialPattern.ReplaceAllString(diagnostic, "${1}[REDACTED]@")
	diagnostic = authorizationPattern.ReplaceAllString(diagnostic, "${1}[REDACTED]")
	diagnostic = secretAssignmentPattern.ReplaceAllString(diagnostic, "${1}${2}${3}${4}[REDACTED]")
	for _, pattern := range standaloneSecretPatterns {
		diagnostic = pattern.ReplaceAllString(diagnostic, "[REDACTED]")
	}
	return diagnostic
}

func boundDiagnostics(diagnostic string) (string, bool) {
	if len(diagnostic) <= maxSafeDiagnosticBytes {
		return diagnostic, false
	}
	return strings.ToValidUTF8(diagnostic[:maxSafeDiagnosticBytes], "�"), true
}

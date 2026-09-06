package recovery

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/blocker"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

const (
	controlRecordSchemaVersion = 1
	EventBlockerDecision       = "BLOCKER_DECISION_RECORDED"
	EventResumeAuthority       = "RESUME_AUTHORITY_RECORDED"
)

// HumanDecisionRequirement identifies the exact bounded decision that a
// human actor must make before the controller may proceed.
type HumanDecisionRequirement struct {
	Question          string   `json:"question"`
	AcceptedAnswers   []string `json:"accepted_answers"`
	RequiredAuthority string   `json:"required_authority"`
}

// SecretRequirement names the secret that must become available. It has no
// field capable of carrying a secret value.
type SecretRequirement struct {
	SecretIdentity    string `json:"secret_identity"`
	RequiredAuthority string `json:"required_authority"`
}

// ExternalDependencyRequirement identifies the external condition and the
// authority responsible for confirming that it has been satisfied.
type ExternalDependencyRequirement struct {
	DependencyID        string `json:"dependency_id"`
	ResolutionCondition string `json:"resolution_condition"`
	RequiredAuthority   string `json:"required_authority"`
}

// ValidationRequirement identifies an unavailable required validator and
// the condition that must be proven before validation can resume.
type ValidationRequirement struct {
	ValidatorID          string `json:"validator_id"`
	RestorationCondition string `json:"restoration_condition"`
	RequiredAuthority    string `json:"required_authority"`
}

// PolicyRequirement identifies the exact policy rule blocking progress and
// the authority required to change or grant an exception to that policy.
type PolicyRequirement struct {
	PolicyRule        string `json:"policy_rule"`
	RequiredAuthority string `json:"required_authority"`
}

// RecoveryRequirement captures the explicit authority required when the
// classifier cannot safely select a more specific blocker state.
type RecoveryRequirement struct {
	Reason            string `json:"reason"`
	RequiredAuthority string `json:"required_authority"`
}

// BlockerRequirement is a tagged-by-state union. Exactly one member must be
// supplied for states that require external authority or condition changes.
type BlockerRequirement struct {
	HumanDecision      *HumanDecisionRequirement      `json:"human_decision,omitempty"`
	Secret             *SecretRequirement             `json:"secret,omitempty"`
	ExternalDependency *ExternalDependencyRequirement `json:"external_dependency,omitempty"`
	Validation         *ValidationRequirement         `json:"validation,omitempty"`
	Policy             *PolicyRequirement             `json:"policy,omitempty"`
	Recovery           *RecoveryRequirement           `json:"recovery,omitempty"`
}

// BlockerDecisionInput contains raw failure metadata and the controller
// identity needed to construct one safe, immutable blocker decision.
type BlockerDecisionInput struct {
	Attempt       AttemptIdentity
	StateFrom     domain.State
	Failure       blocker.FailureInput
	Requirement   BlockerRequirement
	Actor         string
	Timestamp     time.Time
	PolicyVersion string
	EvidenceRefs  []ledger.EvidenceRef
}

type blockerDecisionRecord struct {
	SchemaVersion  int                    `json:"schema_version"`
	Attempt        AttemptIdentity        `json:"attempt"`
	StateFrom      domain.State           `json:"state_from"`
	StateTo        domain.State           `json:"state_to"`
	Classification blocker.Classification `json:"classification"`
	Requirement    BlockerRequirement     `json:"requirement"`
	Actor          string                 `json:"actor"`
	Timestamp      time.Time              `json:"timestamp"`
	PolicyVersion  string                 `json:"policy_version"`
	EvidenceRefs   []ledger.EvidenceRef   `json:"evidence_refs"`
}

// BlockerDecision is a validated immutable decision. NewBlockerDecision is
// the only way to populate it, ensuring raw diagnostics are classified and
// redacted before they can be serialized or emitted to the ledger.
type BlockerDecision struct {
	record blockerDecisionRecord
}

// NewBlockerDecision classifies the supplied failure, validates the matching
// requirement and state transition, and freezes the resulting record.
func NewBlockerDecision(input BlockerDecisionInput) (BlockerDecision, error) {
	classification := blocker.Classify(input.Failure)
	record := blockerDecisionRecord{
		SchemaVersion:  controlRecordSchemaVersion,
		Attempt:        input.Attempt,
		StateFrom:      input.StateFrom,
		StateTo:        classification.RecommendedState,
		Classification: cloneClassification(classification),
		Requirement:    cloneBlockerRequirement(input.Requirement),
		Actor:          input.Actor,
		Timestamp:      input.Timestamp.UTC(),
		PolicyVersion:  input.PolicyVersion,
		EvidenceRefs:   cloneEvidenceRefs(input.EvidenceRefs),
	}
	if err := validateBlockerDecision(record); err != nil {
		return BlockerDecision{}, err
	}
	return BlockerDecision{record: record}, nil
}

// MarshalJSON serializes the validated record while keeping its mutable
// slices and requirement pointers private from callers.
func (d BlockerDecision) MarshalJSON() ([]byte, error) {
	if err := validateBlockerDecision(d.record); err != nil {
		return nil, err
	}
	return json.Marshal(d.record)
}

// Classification returns a defensive copy of the safe classifier result.
func (d BlockerDecision) Classification() blocker.Classification {
	return cloneClassification(d.record.Classification)
}

// StateTo returns the existing domain state selected for this decision.
func (d BlockerDecision) StateTo() domain.State { return d.record.StateTo }

// Event produces the typed decision's append-only ledger representation.
func (d BlockerDecision) Event(source string) (ledger.Event, error) {
	if err := validateBlockerDecision(d.record); err != nil {
		return ledger.Event{}, err
	}
	event, err := ledger.NewEvent(d.record.Attempt.RunID, EventBlockerDecision, d.record.Actor, source)
	if err != nil {
		return ledger.Event{}, fmt.Errorf("create blocker decision event: %w", err)
	}
	event.Timestamp = d.record.Timestamp
	applyAttemptIdentity(&event, d.record.Attempt)
	event.StateFrom = d.record.StateFrom
	event.StateTo = d.record.StateTo
	event.Payload = map[string]any{
		"record_schema_version": d.record.SchemaVersion,
		"classification":        cloneClassification(d.record.Classification),
		"requirement":           cloneBlockerRequirement(d.record.Requirement),
		"policy_version":        d.record.PolicyVersion,
	}
	event.EvidenceRefs = cloneEvidenceRefs(d.record.EvidenceRefs)
	if err := event.Validate(); err != nil {
		return ledger.Event{}, fmt.Errorf("validate blocker decision event: %w", err)
	}
	return event, nil
}

// ResumeAction distinguishes continuing the same attempt from authorizing a
// fresh attempt after preserved recovery state.
type ResumeAction string

const (
	ResumeActionResume  ResumeAction = "RESUME_ATTEMPT"
	ResumeActionRestart ResumeAction = "RESTART_NEW_ATTEMPT"
)

// ResumeAuthorityInput binds an explicit actor decision and state edge to the
// prior attempt and the exact attempt authorized to continue.
type ResumeAuthorityInput struct {
	PriorAttempt      AttemptIdentity
	AuthorizedAttempt AttemptIdentity
	StateFrom         domain.State
	StateTo           domain.State
	Actor             string
	Decision          string
	Action            ResumeAction
	Timestamp         time.Time
	PolicyVersion     string
	EvidenceRefs      []ledger.EvidenceRef
}

type resumeAuthorityRecord struct {
	SchemaVersion     int                  `json:"schema_version"`
	PriorAttempt      AttemptIdentity      `json:"prior_attempt"`
	AuthorizedAttempt AttemptIdentity      `json:"authorized_attempt"`
	StateFrom         domain.State         `json:"state_from"`
	StateTo           domain.State         `json:"state_to"`
	Actor             string               `json:"actor"`
	Decision          string               `json:"decision"`
	Action            ResumeAction         `json:"action"`
	Timestamp         time.Time            `json:"timestamp"`
	PolicyVersion     string               `json:"policy_version"`
	EvidenceRefs      []ledger.EvidenceRef `json:"evidence_refs"`
}

// ResumeAuthority is an immutable permission record. Its zero value carries
// no authority and fails validation.
type ResumeAuthority struct {
	record resumeAuthorityRecord
}

// NewResumeAuthority validates and freezes one explicit resume or restart
// permission. Domain transition validation is performed before any event can
// be emitted.
func NewResumeAuthority(input ResumeAuthorityInput) (ResumeAuthority, error) {
	record := resumeAuthorityRecord{
		SchemaVersion:     controlRecordSchemaVersion,
		PriorAttempt:      input.PriorAttempt,
		AuthorizedAttempt: input.AuthorizedAttempt,
		StateFrom:         input.StateFrom,
		StateTo:           input.StateTo,
		Actor:             input.Actor,
		Decision:          input.Decision,
		Action:            input.Action,
		Timestamp:         input.Timestamp.UTC(),
		PolicyVersion:     input.PolicyVersion,
		EvidenceRefs:      cloneEvidenceRefs(input.EvidenceRefs),
	}
	if err := validateResumeRecord(record); err != nil {
		return ResumeAuthority{}, err
	}
	return ResumeAuthority{record: record}, nil
}

// MarshalJSON serializes the validated explicit authority record.
func (a ResumeAuthority) MarshalJSON() ([]byte, error) {
	if err := validateResumeRecord(a.record); err != nil {
		return nil, err
	}
	return json.Marshal(a.record)
}

// ValidateResumeAuthority rejects absent authority and prevents a valid
// record from being reused for a different attempt or state transition.
func ValidateResumeAuthority(authority *ResumeAuthority, prior, authorized AttemptIdentity, from, to domain.State) error {
	if authority == nil {
		return errors.New("explicit resume authority is required")
	}
	if err := validateResumeRecord(authority.record); err != nil {
		return err
	}
	if authority.record.PriorAttempt != prior {
		return errors.New("resume authority targets a different prior run or attempt")
	}
	if authority.record.AuthorizedAttempt != authorized {
		return errors.New("resume authority targets a different authorized run or attempt")
	}
	if authority.record.StateFrom != from || authority.record.StateTo != to {
		return errors.New("resume authority targets a different state transition")
	}
	return nil
}

// Event produces the explicit authority's append-only ledger representation.
func (a ResumeAuthority) Event(source string) (ledger.Event, error) {
	if err := validateResumeRecord(a.record); err != nil {
		return ledger.Event{}, err
	}
	event, err := ledger.NewEvent(a.record.AuthorizedAttempt.RunID, EventResumeAuthority, a.record.Actor, source)
	if err != nil {
		return ledger.Event{}, fmt.Errorf("create resume authority event: %w", err)
	}
	event.Timestamp = a.record.Timestamp
	applyAttemptIdentity(&event, a.record.AuthorizedAttempt)
	event.StateFrom = a.record.StateFrom
	event.StateTo = a.record.StateTo
	event.Payload = map[string]any{
		"record_schema_version": a.record.SchemaVersion,
		"prior_attempt":         a.record.PriorAttempt,
		"authorized_attempt":    a.record.AuthorizedAttempt,
		"decision":              a.record.Decision,
		"action":                a.record.Action,
		"policy_version":        a.record.PolicyVersion,
	}
	event.EvidenceRefs = cloneEvidenceRefs(a.record.EvidenceRefs)
	if err := event.Validate(); err != nil {
		return ledger.Event{}, fmt.Errorf("validate resume authority event: %w", err)
	}
	return event, nil
}

func validateBlockerDecision(record blockerDecisionRecord) error {
	if record.SchemaVersion != controlRecordSchemaVersion {
		return errors.New("unsupported blocker decision schema version")
	}
	if err := validateAttempt(record.Attempt); err != nil {
		return fmt.Errorf("blocker decision attempt: %w", err)
	}
	if record.Timestamp.IsZero() {
		return errors.New("blocker decision timestamp is required")
	}
	if err := validateControlText("blocker decision actor", record.Actor); err != nil {
		return err
	}
	if err := validateControlText("blocker decision policy version", record.PolicyVersion); err != nil {
		return err
	}
	if record.StateTo != record.Classification.RecommendedState {
		return errors.New("blocker decision state does not match classifier recommendation")
	}
	if err := domain.ValidateTransition(record.StateFrom, record.StateTo); err != nil {
		return fmt.Errorf("blocker decision transition: %w", err)
	}
	if err := validateBlockerRequirement(record.StateTo, record.Requirement); err != nil {
		return err
	}
	return validateEvidenceRefs(record.EvidenceRefs)
}

func validateBlockerRequirement(state domain.State, requirement BlockerRequirement) error {
	count := requirementCount(requirement)
	requireExactly := func(name string, present bool) error {
		if count != 1 || !present {
			return fmt.Errorf("%s requires exactly one matching requirement", state)
		}
		return nil
	}
	switch state {
	case domain.StateHumanDecisionRequired:
		if err := requireExactly("human decision", requirement.HumanDecision != nil); err != nil {
			return err
		}
		human := requirement.HumanDecision
		if err := validateControlText("human decision question", human.Question); err != nil {
			return err
		}
		if err := validateControlText("human decision required authority", human.RequiredAuthority); err != nil {
			return err
		}
		if len(human.AcceptedAnswers) == 0 {
			return errors.New("human decision accepted answer space is required")
		}
		seen := make(map[string]struct{}, len(human.AcceptedAnswers))
		for _, answer := range human.AcceptedAnswers {
			if err := validateControlText("human decision accepted answer", answer); err != nil {
				return err
			}
			if _, exists := seen[answer]; exists {
				return errors.New("human decision accepted answers must be unique")
			}
			seen[answer] = struct{}{}
		}
	case domain.StateSecretRequired:
		if err := requireExactly("secret", requirement.Secret != nil); err != nil {
			return err
		}
		if err := validateIdentifier("secret identity", requirement.Secret.SecretIdentity); err != nil {
			return err
		}
		return validateControlText("secret required authority", requirement.Secret.RequiredAuthority)
	case domain.StateExternalDependency:
		if err := requireExactly("external dependency", requirement.ExternalDependency != nil); err != nil {
			return err
		}
		if err := validateIdentifier("external dependency ID", requirement.ExternalDependency.DependencyID); err != nil {
			return err
		}
		if err := validateControlText("external dependency resolution condition", requirement.ExternalDependency.ResolutionCondition); err != nil {
			return err
		}
		return validateControlText("external dependency required authority", requirement.ExternalDependency.RequiredAuthority)
	case domain.StateValidationUnavailable:
		if err := requireExactly("validation", requirement.Validation != nil); err != nil {
			return err
		}
		if err := validateIdentifier("validator ID", requirement.Validation.ValidatorID); err != nil {
			return err
		}
		if err := validateControlText("validator restoration condition", requirement.Validation.RestorationCondition); err != nil {
			return err
		}
		return validateControlText("validator required authority", requirement.Validation.RequiredAuthority)
	case domain.StatePolicyBlocked:
		if err := requireExactly("policy block", requirement.Policy != nil); err != nil {
			return err
		}
		if err := validateControlText("policy rule", requirement.Policy.PolicyRule); err != nil {
			return err
		}
		return validateControlText("policy required authority", requirement.Policy.RequiredAuthority)
	case domain.StateRecoveryRequired:
		if err := requireExactly("recovery", requirement.Recovery != nil); err != nil {
			return err
		}
		if err := validateControlText("recovery reason", requirement.Recovery.Reason); err != nil {
			return err
		}
		return validateControlText("recovery required authority", requirement.Recovery.RequiredAuthority)
	case domain.StateRetrying, domain.StateCapacityWait, domain.StateFailed:
		if count != 0 {
			return fmt.Errorf("%s does not accept an external requirement", state)
		}
	default:
		return fmt.Errorf("state %s is not a recovery or blocker decision state", state)
	}
	return nil
}

func validateResumeRecord(record resumeAuthorityRecord) error {
	if record.SchemaVersion != controlRecordSchemaVersion {
		return errors.New("explicit resume authority is required")
	}
	if err := validateAttempt(record.PriorAttempt); err != nil {
		return fmt.Errorf("prior attempt: %w", err)
	}
	if err := validateAttempt(record.AuthorizedAttempt); err != nil {
		return fmt.Errorf("authorized attempt: %w", err)
	}
	if err := validateControlText("resume authority actor", record.Actor); err != nil {
		return err
	}
	if err := validateControlText("resume authority decision", record.Decision); err != nil {
		return err
	}
	if err := validateControlText("resume authority policy version", record.PolicyVersion); err != nil {
		return err
	}
	if record.Timestamp.IsZero() {
		return errors.New("resume authority timestamp is required")
	}
	if !resumeSourceState(record.StateFrom) {
		return fmt.Errorf("state %s cannot be resumed or restarted", record.StateFrom)
	}
	if err := domain.ValidateTransition(record.StateFrom, record.StateTo); err != nil {
		return fmt.Errorf("resume authority transition: %w", err)
	}
	if record.StateTo != domain.StateAuthorityValidated && record.StateTo != domain.StateExecutionStarting && record.StateTo != domain.StateBranchAcceptancePending {
		return fmt.Errorf("state %s is not a resume or restart boundary", record.StateTo)
	}
	switch record.Action {
	case ResumeActionResume:
		if record.PriorAttempt != record.AuthorizedAttempt {
			return errors.New("resume action must target the exact prior run and attempt")
		}
	case ResumeActionRestart:
		if record.PriorAttempt.RunID != record.AuthorizedAttempt.RunID ||
			record.PriorAttempt.ProjectID != record.AuthorizedAttempt.ProjectID ||
			record.PriorAttempt.PlanID != record.AuthorizedAttempt.PlanID ||
			record.PriorAttempt.TaskID != record.AuthorizedAttempt.TaskID {
			return errors.New("restart action must remain within the prior governed run scope")
		}
		if record.PriorAttempt.AttemptID == record.AuthorizedAttempt.AttemptID {
			return errors.New("restart action requires a new attempt ID")
		}
	default:
		return errors.New("resume authority action is required")
	}
	return validateEvidenceRefs(record.EvidenceRefs)
}

func resumeSourceState(state domain.State) bool {
	switch state {
	case domain.StateRetrying, domain.StateCapacityWait, domain.StateRecoveryRequired,
		domain.StateHumanDecisionRequired, domain.StateSecretRequired,
		domain.StateExternalDependency, domain.StateValidationUnavailable:
		return true
	default:
		return false
	}
}

func validateEvidenceRefs(refs []ledger.EvidenceRef) error {
	if len(refs) == 0 {
		return errors.New("at least one immutable evidence reference is required")
	}
	for index, ref := range refs {
		if strings.TrimSpace(ref.URI) == "" || strings.TrimSpace(ref.Kind) == "" {
			return fmt.Errorf("evidence reference %d requires URI and kind", index)
		}
		decoded, err := hex.DecodeString(ref.SHA256)
		if err != nil || len(decoded) != 32 || ref.SHA256 != strings.ToLower(ref.SHA256) {
			return fmt.Errorf("evidence reference %d requires a lowercase SHA256 digest", index)
		}
	}
	return nil
}

func validateControlText(name, value string) error {
	if value == "" || value != strings.TrimSpace(value) || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s is required without surrounding whitespace or NUL bytes", name)
	}
	return nil
}

func validateIdentifier(name, value string) error {
	if err := validateControlText(name, value); err != nil {
		return err
	}
	if len(value) > 256 || strings.ContainsAny(value, "\r\n\t =:") {
		return fmt.Errorf("%s must be a bounded identifier, never a value", name)
	}
	return nil
}

func requirementCount(requirement BlockerRequirement) int {
	count := 0
	for _, present := range []bool{
		requirement.HumanDecision != nil, requirement.Secret != nil,
		requirement.ExternalDependency != nil, requirement.Validation != nil,
		requirement.Policy != nil, requirement.Recovery != nil,
	} {
		if present {
			count++
		}
	}
	return count
}

func cloneClassification(classification blocker.Classification) blocker.Classification {
	classification.EvidenceBasis = append([]string(nil), classification.EvidenceBasis...)
	return classification
}

func cloneBlockerRequirement(requirement BlockerRequirement) BlockerRequirement {
	clone := requirement
	if requirement.HumanDecision != nil {
		value := *requirement.HumanDecision
		value.AcceptedAnswers = append([]string(nil), value.AcceptedAnswers...)
		clone.HumanDecision = &value
	}
	if requirement.Secret != nil {
		value := *requirement.Secret
		clone.Secret = &value
	}
	if requirement.ExternalDependency != nil {
		value := *requirement.ExternalDependency
		clone.ExternalDependency = &value
	}
	if requirement.Validation != nil {
		value := *requirement.Validation
		clone.Validation = &value
	}
	if requirement.Policy != nil {
		value := *requirement.Policy
		clone.Policy = &value
	}
	if requirement.Recovery != nil {
		value := *requirement.Recovery
		clone.Recovery = &value
	}
	return clone
}

func cloneEvidenceRefs(refs []ledger.EvidenceRef) []ledger.EvidenceRef {
	return append([]ledger.EvidenceRef(nil), refs...)
}

func applyAttemptIdentity(event *ledger.Event, attempt AttemptIdentity) {
	event.ProjectID = attempt.ProjectID
	event.PlanID = attempt.PlanID
	event.AttemptID = attempt.AttemptID
	event.TaskID = attempt.TaskID
	event.AgentSessionID = attempt.AgentSessionID
}

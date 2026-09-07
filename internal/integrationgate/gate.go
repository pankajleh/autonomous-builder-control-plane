// Package integrationgate is the serial EP-004 authority. It alone binds
// textual integration, combined acceptance, review/blocker prerequisites, and
// exact source heads into READY_FOR_MERGE.
package integrationgate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/acceptance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/combinedacceptance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/integrationworkspace"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/scheduler"
)

const (
	VerdictCleanCriticalMajor = "CLEAN_CRITICAL_MAJOR"
	eventStateTransition      = "STATE_TRANSITION"
	gateEvidenceKind          = "serial-integration-gate-decision"
	gateInputEvidenceKind     = "serial-integration-gate-input"
	sourceHeadsEvidenceKind   = "serial-source-head-verification"
	maxEvidenceArtifactBytes  = 16 * 1024 * 1024
	maxAcceptanceCommands     = 256
	maxBlockers               = 256
	maxCandidates             = 256
	maxCandidateEvidenceRefs  = 64
	maxReviewRequirements     = 256
	maxReviewEvidenceRefs     = 64
	maxFallbackEvidenceRefs   = 15
	maxFallbackRejectedRefs   = 64
	maxFallbackTextBytes      = 4096
	fallbackEvidenceKind      = "serial-integration-gate-fallback-decision"
)

type EventAppender interface{ Append(ledger.Event) error }

type ArtifactWriter interface {
	Root() string
	WriteBytes(name, kind string, data []byte) (ledger.EvidenceRef, error)
}

type Config struct {
	TemporaryRoot string
	Events        EventAppender
	Artifacts     ArtifactWriter
	Processes     acceptance.CommandRunner
}

type ReviewRequirement = authority.ReviewRequirement
type ReviewPolicy = authority.ReviewPolicy

type ReviewAttestation struct {
	Component   string               `json:"component"`
	ReviewedSHA string               `json:"reviewed_sha"`
	Verdict     string               `json:"verdict"`
	Provider    string               `json:"provider"`
	Evidence    []ledger.EvidenceRef `json:"evidence"`
}

type Blocker struct {
	ID       string `json:"id"`
	Resolved bool   `json:"resolved"`
}

type Prerequisites struct {
	Blockers []Blocker `json:"blockers"`
}

type Request struct {
	Authority      authority.Authority           `json:"-"`
	BaselineSHA    string                        `json:"baseline_sha"`
	Candidates     []scheduler.AcceptedCandidate `json:"candidates"`
	RiskReport     scheduler.RiskReport          `json:"risk_report"`
	CombinedPolicy combinedacceptance.Policy     `json:"combined_policy"`
	ReviewPolicy   ReviewPolicy                  `json:"review_policy"`
	Reviews        []ReviewAttestation           `json:"reviews"`
	Prerequisites  Prerequisites                 `json:"prerequisites"`
	EvidencePrefix string                        `json:"evidence_prefix"`
}

type Result struct {
	State                      domain.State
	Textual                    integrationworkspace.Result
	Combined                   combinedacceptance.Result
	Materialization            integrationworkspace.MaterializationEvidence
	DecisionEvidence           ledger.EvidenceRef
	SourceVerificationEvidence []ledger.EvidenceRef
	FailureReason              string
}

type combinedEvaluator interface {
	Evaluate(context.Context, authority.Authority, combinedacceptance.Policy, combinedacceptance.Target, []scheduler.AcceptedCandidate, scheduler.RiskReport) (combinedacceptance.Result, error)
}

type workspaceController interface {
	Integrate(context.Context, integrationworkspace.Request) (integrationworkspace.Result, error)
	UseMaterialized(context.Context, integrationworkspace.Request, integrationworkspace.Result, string, func(integrationworkspace.MaterializedTarget) error) (integrationworkspace.MaterializationEvidence, error)
}

type Gate struct {
	events       EventAppender
	artifacts    ArtifactWriter
	workspace    workspaceController
	combined     combinedEvaluator
	evidenceRoot string
}

func New(config Config) (*Gate, error) {
	if config.Events == nil || config.Artifacts == nil || config.Processes == nil {
		return nil, errors.New("event appender, evidence writer, and process runner are required")
	}
	workspace, err := integrationworkspace.NewController(integrationworkspace.Config{
		TemporaryRoot: config.TemporaryRoot, Artifacts: config.Artifacts, CleanupOnEvidenceFailure: true,
	})
	if err != nil {
		return nil, err
	}
	return &Gate{events: config.Events, artifacts: config.Artifacts, workspace: workspace,
		combined: combinedacceptance.New(config.Processes, config.Artifacts), evidenceRoot: config.Artifacts.Root()}, nil
}

// Run performs one serial integration decision. Callers supply only governed
// inputs; Track B and Track C results and target provenance are constructed by
// this method and cannot be injected.
func (g *Gate) Run(ctx context.Context, request Request) (Result, error) {
	result := Result{State: domain.StateBranchAccepted}
	if ctx == nil {
		return result, errors.New("context is required")
	}
	if g == nil || g.events == nil || g.artifacts == nil || g.workspace == nil || g.combined == nil {
		return result, errors.New("serial integration gate is required")
	}
	prepared, inputBytes, err := g.validateRequest(ctx, request)
	if err != nil {
		result.FailureReason = err.Error()
		return result, err
	}
	inputRef, err := g.writeVerified(prepared.EvidencePrefix+"-input.json", gateInputEvidenceKind, inputBytes)
	if err != nil {
		return g.fallback(prepared, result, domain.StateIntegrationPending, "publish verified gate input", nil, err)
	}
	initialHeadRef, err := g.verifySourceHeads(ctx, prepared, "initial")
	if initialHeadRef.URI != "" {
		result.SourceVerificationEvidence = append(result.SourceVerificationEvidence, initialHeadRef)
	}
	if err != nil {
		var publicationErr *artifactPublicationError
		if errors.As(err, &publicationErr) {
			return g.fallback(prepared, result, domain.StateFailed, err.Error(), []ledger.EvidenceRef{inputRef}, err)
		}
		finished, finishErr := g.finish(prepared, result, domain.StateFailed, err.Error(), inputRef)
		if finishErr != nil {
			return finished, finishErr
		}
		return finished, err
	}
	initialRefs := []ledger.EvidenceRef{inputRef, initialHeadRef}
	if err := g.transition(prepared.Authority.RunID(), result.State, domain.StateIntegrationPending, initialRefs, map[string]any{"risk_sha256": prepared.RiskReport.SHA256()}); err != nil {
		return g.handleTransitionFailure(prepared, result, domain.StateIntegrationPending, "emit integration pending", initialRefs, err)
	}
	result.State = domain.StateIntegrationPending
	if err := g.transition(prepared.Authority.RunID(), result.State, domain.StateIntegrating, initialRefs, nil); err != nil {
		return g.handleTransitionFailure(prepared, result, domain.StateIntegrating, "emit integrating", initialRefs, err)
	}
	result.State = domain.StateIntegrating

	workspaceRequest := integrationworkspace.Request{BaselineSHA: prepared.BaselineSHA, RiskReport: prepared.RiskReport, EvidencePrefix: prepared.EvidencePrefix + "-textual"}
	textual, textualErr := g.workspace.Integrate(ctx, workspaceRequest)
	result.Textual = textual
	if textual.Status() == integrationworkspace.StatusConflict {
		return g.finish(prepared, result, domain.StateIntegrationConflict, "textual conflict", inputRef)
	}
	if textual.Status() != integrationworkspace.StatusClean || textualErr != nil {
		reason := textual.Failure()
		if reason == "" && textualErr != nil {
			reason = textualErr.Error()
		}
		return g.finish(prepared, result, domain.StateValidationUnavailable, reason, inputRef)
	}

	var combinedResult combinedacceptance.Result
	materialization, materializeErr := g.workspace.UseMaterialized(ctx, workspaceRequest, textual, prepared.EvidencePrefix, func(target integrationworkspace.MaterializedTarget) error {
		provenance := combinedacceptance.IntegrationProvenance{
			IntegrationID: textual.SHA256(), RepositoryIdentity: prepared.Authority.Repository().Identity,
			BaselineSHA: prepared.BaselineSHA, IntegratedHeadSHA: target.HeadSHA,
			CandidateOrder: candidateOrder(prepared.Candidates), TextualIntegrationStatus: combinedacceptance.TextualIntegrationClean,
			IntegrationPolicyIdentity: prepared.CombinedPolicy.IntegrationPolicyIdentity,
			RiskEvidenceSHA256:        prepared.RiskReport.SHA256(), Evidence: append([]ledger.EvidenceRef(nil), target.Evidence...),
		}
		var evaluateErr error
		combinedResult, evaluateErr = g.combined.Evaluate(ctx, prepared.Authority, prepared.CombinedPolicy, combinedacceptance.Target{
			RepositoryPath: target.RepositoryPath, Branch: target.Branch, HeadSHA: target.HeadSHA, Integration: provenance,
		}, prepared.Candidates, prepared.RiskReport)
		headRef, headErr := g.verifySourceHeads(ctx, prepared, "post-acceptance")
		if headRef.URI != "" {
			result.SourceVerificationEvidence = append(result.SourceVerificationEvidence, headRef)
		}
		if headErr != nil {
			return headErr
		}
		if len(combinedResult.CanonicalJSON()) == 0 {
			return evaluateErr
		}
		return nil
	})
	result.Materialization = materialization
	result.Combined = combinedResult
	if materializeErr != nil {
		var publicationErr *artifactPublicationError
		if errors.As(materializeErr, &publicationErr) {
			return g.fallback(prepared, result, domain.StateValidationUnavailable, materializeErr.Error(), terminalRefs(result, inputRef), materializeErr)
		}
		return g.finish(prepared, result, domain.StateValidationUnavailable, materializeErr.Error(), inputRef)
	}
	switch combinedResult.Classification() {
	case combinedacceptance.ClassificationClean:
		accepted, finishErr := g.finish(prepared, result, domain.StateIntegrationAccepted, "", inputRef)
		if finishErr != nil {
			return accepted, finishErr
		}
		if accepted.State != domain.StateIntegrationAccepted {
			return accepted, nil
		}
		readyHeadRef, headErr := g.verifySourceHeads(ctx, prepared, "ready-for-merge")
		if readyHeadRef.URI != "" {
			accepted.SourceVerificationEvidence = append(accepted.SourceVerificationEvidence, readyHeadRef)
		}
		if headErr != nil {
			var publicationErr *artifactPublicationError
			if errors.As(headErr, &publicationErr) {
				return g.fallback(prepared, accepted, domain.StateReadyForMerge, headErr.Error(), terminalRefs(accepted, inputRef), headErr)
			}
			return g.finish(prepared, accepted, domain.StateFailed, headErr.Error(), inputRef)
		}
		refs := terminalRefs(accepted, inputRef)
		if err := g.transition(prepared.Authority.RunID(), accepted.State, domain.StateReadyForMerge, refs, map[string]any{"combined_acceptance_sha256": combinedResult.SHA256()}); err != nil {
			return g.handleTransitionFailure(prepared, accepted, domain.StateReadyForMerge, "emit ready for merge", refs, err)
		}
		accepted.State = domain.StateReadyForMerge
		return accepted, nil
	case combinedacceptance.ClassificationSemanticConflict:
		return g.finish(prepared, result, domain.StateSemanticConflict, "combined acceptance found a semantic conflict", inputRef)
	default:
		return g.finish(prepared, result, domain.StateValidationUnavailable, "combined acceptance was unavailable or ambiguous", inputRef)
	}
}

func (g *Gate) finish(request Request, result Result, state domain.State, reason string, inputRef ledger.EvidenceRef) (Result, error) {
	refs := terminalRefs(result, inputRef)
	if err := g.verifyTransitionEvidence(refs); err != nil {
		return g.fallback(request, result, state, reason, refs, err)
	}
	record := struct {
		SchemaVersion      int                                          `json:"schema_version"`
		State              domain.State                                 `json:"state"`
		AuthoritySHA256    string                                       `json:"authority_sha256"`
		RiskSHA256         string                                       `json:"risk_sha256"`
		TextualSHA256      string                                       `json:"textual_sha256,omitempty"`
		Textual            json.RawMessage                              `json:"textual,omitempty"`
		CombinedSHA256     string                                       `json:"combined_sha256,omitempty"`
		Combined           json.RawMessage                              `json:"combined,omitempty"`
		ReviewPolicy       ReviewPolicy                                 `json:"review_policy"`
		Reviews            []ReviewAttestation                          `json:"reviews"`
		Materialization    integrationworkspace.MaterializationEvidence `json:"materialization"`
		SourceVerification []ledger.EvidenceRef                         `json:"source_verification"`
		FailureReason      string                                       `json:"failure_reason,omitempty"`
	}{1, state, request.Authority.SHA256(), request.RiskReport.SHA256(), result.Textual.SHA256(), result.Textual.CanonicalJSON(), result.Combined.SHA256(), result.Combined.CanonicalJSON(), request.ReviewPolicy, request.Reviews, result.Materialization, append([]ledger.EvidenceRef(nil), result.SourceVerificationEvidence...), reason}
	data, err := json.Marshal(record)
	if err != nil {
		result.FailureReason = infrastructureFailure(reason, "marshal normal decision", err)
		return result, err
	}
	decisionRef, err := g.writeVerified(request.EvidencePrefix+"-decision-"+strings.ToLower(string(state))+".json", gateEvidenceKind, data)
	if err != nil {
		return g.fallback(request, result, state, reason, refs, err)
	}
	refs = collectRefs(append(refs, decisionRef)...)
	if err := g.transition(request.Authority.RunID(), result.State, state, refs, map[string]any{"reason": reason}); err != nil {
		var verificationErr *evidenceVerificationError
		if errors.As(err, &verificationErr) {
			return g.fallback(request, result, state, reason, refs, err)
		}
		result.FailureReason = infrastructureFailure(reason, "append normal transition", err)
		return result, err
	}
	result.DecisionEvidence = decisionRef
	result.FailureReason = reason
	result.State = state
	return result, nil
}

func (g *Gate) transition(runID string, from, to domain.State, refs []ledger.EvidenceRef, payload map[string]any) error {
	if err := g.verifyTransitionEvidence(refs); err != nil {
		return err
	}
	return g.appendTransition(runID, from, to, refs, payload)
}

func (g *Gate) appendTransition(runID string, from, to domain.State, refs []ledger.EvidenceRef, payload map[string]any) error {
	if err := domain.ValidateTransition(from, to); err != nil {
		return err
	}
	if len(refs) == 0 {
		return errors.New("every gate transition requires evidence")
	}
	event, err := ledger.NewEvent(runID, eventStateTransition, "integration-controller", "integration-gate")
	if err != nil {
		return err
	}
	event.StateFrom, event.StateTo, event.Payload, event.EvidenceRefs = from, to, payload, append([]ledger.EvidenceRef(nil), refs...)
	return g.events.Append(event)
}

type rejectedEvidence struct {
	URI    string `json:"uri,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Error  string `json:"error"`
}

type evidenceVerificationError struct {
	rejected []rejectedEvidence
}

func (e *evidenceVerificationError) Error() string {
	if e == nil || len(e.rejected) == 0 {
		return "evidence verification failed"
	}
	return fmt.Sprintf("evidence verification failed for %d ref(s): %s", len(e.rejected), e.rejected[0].Error)
}

type artifactPublicationError struct {
	operation string
	ref       ledger.EvidenceRef
	err       error
}

func (e *artifactPublicationError) Error() string {
	return fmt.Sprintf("%s: %v", e.operation, e.err)
}

func (e *artifactPublicationError) Unwrap() error { return e.err }

func (g *Gate) verifyTransitionEvidence(refs []ledger.EvidenceRef) error {
	if len(refs) == 0 {
		return &evidenceVerificationError{rejected: []rejectedEvidence{{Error: "complete evidence is required"}}}
	}
	var rejected []rejectedEvidence
	for _, ref := range refs {
		if _, err := evidence.ReadVerifiedLocal(g.evidenceRoot, ref, maxEvidenceArtifactBytes); err != nil {
			rejected = append(rejected, rejectedRef(ref, err))
		}
	}
	if len(rejected) != 0 {
		return &evidenceVerificationError{rejected: rejected}
	}
	return nil
}

func (g *Gate) handleTransitionFailure(request Request, result Result, intended domain.State, original string, refs []ledger.EvidenceRef, err error) (Result, error) {
	var verificationErr *evidenceVerificationError
	if errors.As(err, &verificationErr) {
		return g.fallback(request, result, intended, original, refs, err)
	}
	result.FailureReason = infrastructureFailure(original, "append transition", err)
	return result, err
}

func (g *Gate) fallback(request Request, result Result, intended domain.State, original string, candidates []ledger.EvidenceRef, trigger error) (Result, error) {
	if original == "" {
		original = fmt.Sprintf("intended transition to %s", intended)
	}
	target, err := fallbackTarget(result.State)
	if err != nil {
		result.FailureReason = infrastructureFailure(original, "select fallback transition", err)
		return result, err
	}
	if err := domain.ValidateTransition(result.State, target); err != nil {
		result.FailureReason = infrastructureFailure(original, "validate fallback transition", err)
		return result, err
	}

	if publicationErr := (*artifactPublicationError)(nil); errors.As(trigger, &publicationErr) && publicationErr.ref.URI != "" {
		candidates = append(candidates, publicationErr.ref)
	}
	verified, rejected := g.reduceFallbackEvidence(collectRefs(candidates...))
	if verificationErr := (*evidenceVerificationError)(nil); errors.As(trigger, &verificationErr) {
		for _, item := range verificationErr.rejected {
			appendRejected(&rejected, item)
		}
	}
	if len(rejected) > maxFallbackRejectedRefs {
		rejected = rejected[:maxFallbackRejectedRefs]
	}

	classification := "EVIDENCE_VERIFICATION_FAILURE"
	var publicationErr *artifactPublicationError
	if errors.As(trigger, &publicationErr) {
		classification = "EVIDENCE_PUBLICATION_FAILURE"
	}
	record := struct {
		SchemaVersion               int                `json:"schema_version"`
		State                       domain.State       `json:"state"`
		AuthoritySHA256             string             `json:"authority_sha256"`
		RiskSHA256                  string             `json:"risk_sha256"`
		TextualSHA256               string             `json:"textual_sha256,omitempty"`
		CombinedSHA256              string             `json:"combined_sha256,omitempty"`
		OriginalIntendedState       domain.State       `json:"original_intended_state"`
		OriginalFailure             string             `json:"original_failure"`
		FallbackClassification      string             `json:"fallback_classification"`
		EvidenceVerificationFailure string             `json:"evidence_verification_failure"`
		RejectedRefs                []rejectedEvidence `json:"rejected_refs"`
	}{
		SchemaVersion: 1, State: target, AuthoritySHA256: request.Authority.SHA256(), RiskSHA256: request.RiskReport.SHA256(),
		TextualSHA256: result.Textual.SHA256(), CombinedSHA256: result.Combined.SHA256(), OriginalIntendedState: intended,
		OriginalFailure: boundedText(original), FallbackClassification: classification,
		EvidenceVerificationFailure: boundedText(trigger.Error()), RejectedRefs: rejected,
	}
	data, err := json.Marshal(record)
	if err != nil {
		result.FailureReason = infrastructureFailure(original, "marshal fallback decision", err)
		return result, err
	}
	fallbackRef, err := g.writeVerified(request.EvidencePrefix+"-decision-fallback-"+strings.ToLower(string(target))+".json", fallbackEvidenceKind, data)
	if err != nil {
		result.FailureReason = infrastructureFailure(original, "publish or verify fallback decision", err)
		return result, err
	}
	refs := collectRefs(append(verified, fallbackRef)...)
	failureReason := fmt.Sprintf("original cause: %s; fallback classification: %s; evidence failure: %s", boundedText(original), classification, boundedText(trigger.Error()))
	if err := g.transition(request.Authority.RunID(), result.State, target, refs, map[string]any{"reason": failureReason}); err != nil {
		result.FailureReason = infrastructureFailure(failureReason, "verify or append one-shot fallback transition", err)
		return result, err
	}
	result.State = target
	result.DecisionEvidence = fallbackRef
	result.FailureReason = failureReason
	return result, nil
}

func fallbackTarget(state domain.State) (domain.State, error) {
	switch state {
	case domain.StateBranchAccepted, domain.StateIntegrationPending, domain.StateIntegrationAccepted:
		return domain.StateFailed, nil
	case domain.StateIntegrating:
		return domain.StateValidationUnavailable, nil
	default:
		return "", fmt.Errorf("no evidence fallback is defined from %s", state)
	}
}

func (g *Gate) reduceFallbackEvidence(candidates []ledger.EvidenceRef) ([]ledger.EvidenceRef, []rejectedEvidence) {
	verified := make([]ledger.EvidenceRef, 0, min(len(candidates), maxFallbackEvidenceRefs))
	var rejected []rejectedEvidence
	for _, ref := range candidates {
		if _, err := evidence.ReadVerifiedLocal(g.evidenceRoot, ref, maxEvidenceArtifactBytes); err != nil {
			appendRejected(&rejected, rejectedRef(ref, err))
			continue
		}
		if len(verified) < maxFallbackEvidenceRefs {
			verified = append(verified, ref)
		}
	}
	return verified, rejected
}

func rejectedRef(ref ledger.EvidenceRef, err error) rejectedEvidence {
	return rejectedEvidence{URI: boundedText(ref.URI), SHA256: boundedText(ref.SHA256), Kind: boundedText(ref.Kind), Error: boundedText(err.Error())}
}

func appendRejected(values *[]rejectedEvidence, candidate rejectedEvidence) {
	for _, value := range *values {
		if value.URI == candidate.URI && value.SHA256 == candidate.SHA256 && value.Kind == candidate.Kind {
			return
		}
	}
	if len(*values) < maxFallbackRejectedRefs {
		*values = append(*values, candidate)
	}
}

func boundedText(value string) string {
	if len(value) <= maxFallbackTextBytes {
		return value
	}
	return value[:maxFallbackTextBytes]
}

func infrastructureFailure(original, operation string, err error) string {
	if original == "" {
		original = "none"
	}
	return fmt.Sprintf("original cause: %s; infrastructure failure during %s: %v", boundedText(original), operation, err)
}

func terminalRefs(result Result, inputRef ledger.EvidenceRef) []ledger.EvidenceRef {
	refs := collectRefs(inputRef, result.DecisionEvidence, result.Textual.CaptureRef(), result.Textual.CleanupRef(), result.Materialization.CaptureRef, result.Materialization.CleanupRef)
	refs = append(refs, result.Combined.EvidenceRefs()...)
	refs = append(refs, result.SourceVerificationEvidence...)
	return collectRefs(refs...)
}

func (g *Gate) writeVerified(name, kind string, data []byte) (ledger.EvidenceRef, error) {
	ref, err := g.artifacts.WriteBytes(name, kind, data)
	if err != nil {
		return ledger.EvidenceRef{}, &artifactPublicationError{operation: "write evidence", err: err}
	}
	actual, err := evidence.ReadVerifiedLocal(g.evidenceRoot, ref, maxEvidenceArtifactBytes)
	if err != nil {
		return ref, &artifactPublicationError{operation: "verify published evidence", ref: ref, err: err}
	}
	if !bytes.Equal(actual, data) || ref.Kind != kind {
		return ref, &artifactPublicationError{operation: "verify published evidence", ref: ref, err: errors.New("evidence writer returned mismatched bytes, digest, or kind")}
	}
	return ref, nil
}

func (g *Gate) validateRequest(ctx context.Context, request Request) (Request, []byte, error) {
	if len(request.Authority.CanonicalJSON()) == 0 || digest(request.Authority.CanonicalJSON()) != request.Authority.SHA256() {
		return Request{}, nil, errors.New("validated authority is required")
	}
	if !validSHA(request.BaselineSHA) || request.BaselineSHA != request.Authority.Repository().StartSHA {
		return Request{}, nil, errors.New("baseline must match governed authority")
	}
	if len(request.Candidates) == 0 || request.RiskReport.SHA256() == "" || digest(request.RiskReport.CanonicalJSON()) != request.RiskReport.SHA256() {
		return Request{}, nil, errors.New("complete candidates and immutable risk report are required")
	}
	if len(request.Candidates) > maxCandidates {
		return Request{}, nil, fmt.Errorf("candidate count %d exceeds maximum %d", len(request.Candidates), maxCandidates)
	}
	if count := len(request.Authority.Acceptance()); count > maxAcceptanceCommands {
		return Request{}, nil, fmt.Errorf("governed acceptance command count %d exceeds merge-gate maximum %d", count, maxAcceptanceCommands)
	}
	if !validText(request.EvidencePrefix) || strings.ContainsAny(request.EvidencePrefix, `/\\`) {
		return Request{}, nil, errors.New("safe evidence prefix is required")
	}
	if err := validateBlockers(request.Prerequisites); err != nil {
		return Request{}, nil, err
	}
	boundReview, present := request.Authority.MergeReviewPolicy()
	if !present {
		return Request{}, nil, errors.New("controller authority does not bind a merge review policy")
	}
	canonicalBound := canonicalReviewPolicy(boundReview)
	if !emptyReviewPolicy(request.ReviewPolicy) && !sameReviewPolicy(canonicalReviewPolicy(request.ReviewPolicy), canonicalBound) {
		return Request{}, nil, errors.New("caller-selected review policy does not match controller authority")
	}
	request.ReviewPolicy = canonicalBound
	if err := g.validateReviews(ctx, request.Authority.Repository().Path, request.ReviewPolicy, request.Reviews); err != nil {
		return Request{}, nil, err
	}
	request = canonicalRequest(request)
	riskCandidates := request.RiskReport.CandidateDiffs()
	if len(riskCandidates) != len(request.Candidates) {
		return Request{}, nil, errors.New("risk candidate set is incomplete")
	}
	for i, candidate := range request.Candidates {
		validated, err := scheduler.NewAcceptedCandidate(candidate.Input())
		if err != nil {
			return Request{}, nil, fmt.Errorf("candidate %d: %w", i, err)
		}
		if validated.Key() != riskCandidates[i].Candidate.Key() || !sameCandidate(validated, riskCandidates[i].Candidate) {
			return Request{}, nil, errors.New("candidate order does not match exact risk provenance")
		}
		input := validated.Input()
		if input.Repository != request.Authority.Repository().Path || input.StartSHA != request.BaselineSHA || input.AcceptancePolicyIdentity != request.Authority.PolicyVersion() {
			return Request{}, nil, fmt.Errorf("candidate %d does not match authority or branch acceptance policy", i)
		}
		if len(input.AcceptanceEvidence) > maxCandidateEvidenceRefs {
			return Request{}, nil, fmt.Errorf("candidate %d acceptance evidence count %d exceeds maximum %d", i, len(input.AcceptanceEvidence), maxCandidateEvidenceRefs)
		}
		if err := g.verifyAllEvidence(input.AcceptanceEvidence); err != nil {
			return Request{}, nil, fmt.Errorf("candidate %d acceptance evidence: %w", i, err)
		}
	}
	if request.CombinedPolicy.CombinedAcceptancePolicyIdentity != request.Authority.PolicyVersion() || request.CombinedPolicy.RiskPolicyIdentity != request.RiskReport.PolicyIdentity() {
		return Request{}, nil, errors.New("combined policy identities do not match authority and risk report")
	}
	canonical := struct {
		SchemaVersion   int                           `json:"schema_version"`
		AuthoritySHA256 string                        `json:"authority_sha256"`
		Authority       json.RawMessage               `json:"authority"`
		BaselineSHA     string                        `json:"baseline_sha"`
		Candidates      []scheduler.AcceptedCandidate `json:"candidates"`
		Risk            json.RawMessage               `json:"risk"`
		CombinedPolicy  combinedacceptance.Policy     `json:"combined_policy"`
		ReviewPolicy    ReviewPolicy                  `json:"review_policy"`
		Reviews         []ReviewAttestation           `json:"reviews"`
		Prerequisites   Prerequisites                 `json:"prerequisites"`
	}{1, request.Authority.SHA256(), request.Authority.CanonicalJSON(), request.BaselineSHA, request.Candidates, request.RiskReport.CanonicalJSON(), request.CombinedPolicy, request.ReviewPolicy, request.Reviews, request.Prerequisites}
	data, err := json.Marshal(canonical)
	if err != nil {
		return Request{}, nil, err
	}
	return request, data, nil
}

func validateBlockers(prerequisites Prerequisites) error {
	if len(prerequisites.Blockers) > maxBlockers {
		return fmt.Errorf("blocker count %d exceeds maximum %d", len(prerequisites.Blockers), maxBlockers)
	}
	seen := map[string]struct{}{}
	for _, blocker := range prerequisites.Blockers {
		if !validText(blocker.ID) {
			return errors.New("blocker ID is invalid")
		}
		if _, ok := seen[blocker.ID]; ok {
			return errors.New("duplicate blocker state")
		}
		seen[blocker.ID] = struct{}{}
		if !blocker.Resolved {
			return fmt.Errorf("unresolved blocker %q", blocker.ID)
		}
	}
	return nil
}

func (g *Gate) validateReviews(ctx context.Context, repository string, policy ReviewPolicy, reviews []ReviewAttestation) error {
	if !validText(policy.PolicyIdentity) || len(policy.Required) == 0 || len(policy.Required) > maxReviewRequirements {
		return errors.New("complete review policy is required")
	}
	if len(reviews) > maxReviewRequirements {
		return fmt.Errorf("review attestation count %d exceeds maximum %d", len(reviews), maxReviewRequirements)
	}
	provided := map[string]ReviewAttestation{}
	for _, review := range reviews {
		if !validText(review.Component) || !validText(review.Provider) || !validText(review.Verdict) || !validSHA(review.ReviewedSHA) || len(review.Evidence) == 0 {
			return errors.New("complete review attestation is required")
		}
		if len(review.Evidence) > maxReviewEvidenceRefs {
			return fmt.Errorf("review %s evidence count %d exceeds maximum %d", review.Component, len(review.Evidence), maxReviewEvidenceRefs)
		}
		if err := g.verifyAllEvidence(review.Evidence); err != nil {
			return fmt.Errorf("review %s evidence: %w", review.Component, err)
		}
		if review.Verdict != VerdictCleanCriticalMajor {
			return fmt.Errorf("review %q has substantive non-clean verdict %q", review.Component, review.Verdict)
		}
		if _, ok := provided[review.Component]; ok {
			return errors.New("ambiguous duplicate review attestation")
		}
		provided[review.Component] = review
	}
	seen := map[string]struct{}{}
	for _, requirement := range policy.Required {
		if !validText(requirement.Component) || !validSHA(requirement.ReviewedSHA) || requirement.Verdict != VerdictCleanCriticalMajor {
			return errors.New("review requirement is invalid")
		}
		if _, ok := seen[requirement.Component]; ok {
			return errors.New("duplicate review requirement")
		}
		seen[requirement.Component] = struct{}{}
		review, ok := provided[requirement.Component]
		if !ok {
			return fmt.Errorf("required review %q is unavailable", requirement.Component)
		}
		if review.ReviewedSHA != requirement.ReviewedSHA {
			return fmt.Errorf("review %q SHA does not match required reviewed SHA", requirement.Component)
		}
		if review.Verdict != requirement.Verdict {
			return fmt.Errorf("review %q has substantive non-clean verdict %q", requirement.Component, review.Verdict)
		}
		if err := verifyCommit(ctx, repository, requirement.ReviewedSHA); err != nil {
			return fmt.Errorf("review %q reviewed SHA is not an exact governed repository commit: %w", requirement.Component, err)
		}
	}
	if len(provided) != len(seen) {
		return errors.New("review attestations contain a component not bound by controller authority")
	}
	return nil
}

type sourceVerificationCommand struct {
	Repository string   `json:"repository"`
	Argv       []string `json:"argv"`
	ExitCode   int      `json:"exit_code"`
	Stdout     string   `json:"stdout"`
	Stderr     string   `json:"stderr"`
	Truncated  bool     `json:"truncated"`
}

func (g *Gate) verifySourceHeads(ctx context.Context, request Request, boundary string) (ledger.EvidenceRef, error) {
	record := struct {
		SchemaVersion int                           `json:"schema_version"`
		Boundary      string                        `json:"boundary"`
		Candidates    []scheduler.CandidateIdentity `json:"candidates"`
		Commands      []sourceVerificationCommand   `json:"commands"`
		Verified      bool                          `json:"verified"`
		Failure       string                        `json:"failure,omitempty"`
	}{SchemaVersion: 1, Boundary: boundary, Candidates: candidateOrder(request.Candidates)}
	var verificationErr error
	checkedRepositories := make(map[string]struct{})
	for i, candidate := range request.Candidates {
		input := candidate.Input()
		if _, checked := checkedRepositories[input.Repository]; !checked {
			checkedRepositories[input.Repository] = struct{}{}
			observation, err := runSourceVerification(ctx, input.Repository, []string{"git", "--no-replace-objects", "for-each-ref", "--format=%(refname)", "refs/replace/"})
			record.Commands = append(record.Commands, observation)
			if err != nil || observation.Truncated || observation.ExitCode != 0 || observation.Stdout != "" || observation.Stderr != "" {
				verificationErr = errors.New("replacement refs exist or could not be ruled out")
				break
			}
		}
		observation, err := runSourceVerification(ctx, input.Repository, []string{"git", "--no-replace-objects", "show-ref", "--verify", "--hash", "refs/heads/" + input.Branch})
		record.Commands = append(record.Commands, observation)
		if err != nil || observation.Truncated || observation.ExitCode != 0 || observation.Stderr != "" || observation.Stdout != input.HeadSHA+"\n" {
			verificationErr = fmt.Errorf("candidate %d source head changed or could not be verified", i)
			break
		}
	}
	record.Verified = verificationErr == nil
	if verificationErr != nil {
		record.Failure = verificationErr.Error()
	}
	data, err := json.Marshal(record)
	if err != nil {
		return ledger.EvidenceRef{}, errors.Join(verificationErr, err)
	}
	ref, err := g.writeVerified(request.EvidencePrefix+"-source-heads-"+boundary+".json", sourceHeadsEvidenceKind, data)
	if err != nil {
		return ledger.EvidenceRef{}, errors.Join(verificationErr, err)
	}
	return ref, verificationErr
}

func runSourceVerification(ctx context.Context, repository string, argv []string) (sourceVerificationCommand, error) {
	record := sourceVerificationCommand{Repository: repository, Argv: append([]string(nil), argv...), ExitCode: -1}
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Dir, command.Env = repository, append(gitexec.Environment(), "LC_ALL=C")
	var stdout, stderr limitedBuffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	record.Stdout, record.Stderr, record.Truncated = stdout.String(), stderr.String(), stdout.truncated || stderr.truncated
	if command.ProcessState != nil {
		record.ExitCode = command.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		return record, ctx.Err()
	}
	return record, err
}

func verifyCommit(ctx context.Context, repository, reviewedSHA string) error {
	record, err := runSourceVerification(ctx, repository, []string{"git", "--no-replace-objects", "rev-parse", "--verify", "--end-of-options", reviewedSHA + "^{commit}"})
	if err != nil || record.Truncated || record.ExitCode != 0 || record.Stderr != "" || record.Stdout != reviewedSHA+"\n" {
		return errors.New("commit does not exist or resolved ambiguously")
	}
	return nil
}

type limitedBuffer struct {
	bytes.Buffer
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	const limit = 4096
	n := len(p)
	before := b.Len()
	if b.Len() < limit {
		keep := limit - b.Len()
		if keep > len(p) {
			keep = len(p)
		}
		_, _ = b.Buffer.Write(p[:keep])
	}
	if before+len(p) > limit {
		b.truncated = true
	}
	return n, nil
}

func candidateOrder(candidates []scheduler.AcceptedCandidate) []scheduler.CandidateIdentity {
	values := make([]scheduler.CandidateIdentity, len(candidates))
	for i, candidate := range candidates {
		in := candidate.Input()
		values[i] = scheduler.CandidateIdentity{ProjectID: in.ProjectID, PlanID: in.PlanID, RunID: in.RunID, AttemptID: in.AttemptID, HeadSHA: in.HeadSHA}
	}
	return values
}

func (g *Gate) verifyAllEvidence(refs []ledger.EvidenceRef) error {
	if len(refs) == 0 {
		return errors.New("complete evidence is required")
	}
	for i, ref := range refs {
		if _, err := evidence.ReadVerifiedLocal(g.evidenceRoot, ref, maxEvidenceArtifactBytes); err != nil {
			return fmt.Errorf("evidence %d: %w", i, err)
		}
	}
	return nil
}
func collectRefs(refs ...ledger.EvidenceRef) []ledger.EvidenceRef {
	seen := map[string]struct{}{}
	out := make([]ledger.EvidenceRef, 0, len(refs))
	for _, ref := range refs {
		if ref.URI == "" {
			continue
		}
		key := ref.URI + "\x00" + ref.SHA256 + "\x00" + ref.Kind
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ref)
	}
	return out
}
func sameCandidate(a, b scheduler.AcceptedCandidate) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}
func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}
func validSHA(value string) bool { return (len(value) == 40 || len(value) == 64) && validHex(value) }
func validHex(value string) bool {
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}
func validText(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}
func canonicalRequest(request Request) Request {
	request.Candidates = append([]scheduler.AcceptedCandidate(nil), request.Candidates...)
	request.CombinedPolicy.SemanticFailureClasses = append([]string(nil), request.CombinedPolicy.SemanticFailureClasses...)
	request.CombinedPolicy.FailClosedFailureClasses = append([]string(nil), request.CombinedPolicy.FailClosedFailureClasses...)
	sort.Strings(request.CombinedPolicy.SemanticFailureClasses)
	sort.Strings(request.CombinedPolicy.FailClosedFailureClasses)
	request.ReviewPolicy.Required = append([]ReviewRequirement(nil), request.ReviewPolicy.Required...)
	sort.Slice(request.ReviewPolicy.Required, func(i, j int) bool {
		return request.ReviewPolicy.Required[i].Component < request.ReviewPolicy.Required[j].Component
	})
	request.Reviews = append([]ReviewAttestation(nil), request.Reviews...)
	for i := range request.Reviews {
		request.Reviews[i].Evidence = append([]ledger.EvidenceRef(nil), request.Reviews[i].Evidence...)
		sort.Slice(request.Reviews[i].Evidence, func(a, b int) bool {
			return evidenceKey(request.Reviews[i].Evidence[a]) < evidenceKey(request.Reviews[i].Evidence[b])
		})
	}
	sort.Slice(request.Reviews, func(i, j int) bool { return request.Reviews[i].Component < request.Reviews[j].Component })
	request.Prerequisites.Blockers = append([]Blocker(nil), request.Prerequisites.Blockers...)
	sort.Slice(request.Prerequisites.Blockers, func(i, j int) bool {
		return request.Prerequisites.Blockers[i].ID < request.Prerequisites.Blockers[j].ID
	})
	return request
}

func canonicalReviewPolicy(policy ReviewPolicy) ReviewPolicy {
	policy.Required = append([]ReviewRequirement(nil), policy.Required...)
	sort.Slice(policy.Required, func(i, j int) bool { return policy.Required[i].Component < policy.Required[j].Component })
	return policy
}

func emptyReviewPolicy(policy ReviewPolicy) bool {
	return policy.PolicyIdentity == "" && len(policy.Required) == 0
}

func sameReviewPolicy(left, right ReviewPolicy) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func evidenceKey(ref ledger.EvidenceRef) string {
	return ref.URI + "\x00" + ref.SHA256 + "\x00" + ref.Kind
}

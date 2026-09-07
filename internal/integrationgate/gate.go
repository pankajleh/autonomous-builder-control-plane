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
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/acceptance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/combinedacceptance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
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
	maxEvidenceRefs           = 128
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

type ReviewRequirement struct {
	Component   string `json:"component"`
	ReviewedSHA string `json:"reviewed_sha"`
	Verdict     string `json:"verdict"`
}

type ReviewPolicy struct {
	PolicyIdentity string              `json:"policy_identity"`
	Required       []ReviewRequirement `json:"required"`
}

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
	State            domain.State
	Textual          integrationworkspace.Result
	Combined         combinedacceptance.Result
	Materialization  integrationworkspace.MaterializationEvidence
	DecisionEvidence ledger.EvidenceRef
	FailureReason    string
}

type combinedEvaluator interface {
	Evaluate(context.Context, authority.Authority, combinedacceptance.Policy, combinedacceptance.Target, []scheduler.AcceptedCandidate, scheduler.RiskReport) (combinedacceptance.Result, error)
}

type workspaceController interface {
	Integrate(context.Context, integrationworkspace.Request) (integrationworkspace.Result, error)
	UseMaterialized(context.Context, integrationworkspace.Request, integrationworkspace.Result, string, func(integrationworkspace.MaterializedTarget) error) (integrationworkspace.MaterializationEvidence, error)
}

type Gate struct {
	events    EventAppender
	artifacts ArtifactWriter
	workspace workspaceController
	combined  combinedEvaluator
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
		combined: combinedacceptance.New(config.Processes, config.Artifacts)}, nil
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
	prepared, inputBytes, err := validateRequest(request)
	if err != nil {
		result.FailureReason = err.Error()
		return result, err
	}
	inputRef, err := g.writeVerified(prepared.EvidencePrefix+"-input.json", gateInputEvidenceKind, inputBytes)
	if err != nil {
		result.FailureReason = err.Error()
		return result, err
	}
	if err := verifySourceHeads(ctx, prepared.Candidates); err != nil {
		result.FailureReason = err.Error()
		return result, err
	}
	if err := g.transition(prepared.Authority.RunID(), domain.StateBranchAccepted, domain.StateIntegrationPending, []ledger.EvidenceRef{inputRef}, map[string]any{"risk_sha256": prepared.RiskReport.SHA256()}); err != nil {
		return result, err
	}
	result.State = domain.StateIntegrationPending
	if err := g.transition(prepared.Authority.RunID(), domain.StateIntegrationPending, domain.StateIntegrating, []ledger.EvidenceRef{inputRef}, nil); err != nil {
		return result, err
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
		if headErr := verifySourceHeads(ctx, prepared.Candidates); headErr != nil {
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
		return g.finish(prepared, result, domain.StateValidationUnavailable, materializeErr.Error(), inputRef)
	}
	switch combinedResult.Classification() {
	case combinedacceptance.ClassificationClean:
		accepted, finishErr := g.finish(prepared, result, domain.StateIntegrationAccepted, "", inputRef)
		if finishErr != nil {
			return accepted, finishErr
		}
		refs := []ledger.EvidenceRef{inputRef, accepted.DecisionEvidence, accepted.Materialization.CleanupRef}
		if err := verifyAllEvidence(refs); err != nil {
			accepted.FailureReason = err.Error()
			return accepted, err
		}
		if err := verifySourceHeads(ctx, prepared.Candidates); err != nil {
			accepted.FailureReason = err.Error()
			return accepted, err
		}
		if err := g.transition(prepared.Authority.RunID(), domain.StateIntegrationAccepted, domain.StateReadyForMerge, refs, map[string]any{"combined_acceptance_sha256": combinedResult.SHA256()}); err != nil {
			return accepted, err
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
	record := struct {
		SchemaVersion   int                                          `json:"schema_version"`
		State           domain.State                                 `json:"state"`
		AuthoritySHA256 string                                       `json:"authority_sha256"`
		RiskSHA256      string                                       `json:"risk_sha256"`
		TextualSHA256   string                                       `json:"textual_sha256,omitempty"`
		Textual         json.RawMessage                              `json:"textual,omitempty"`
		CombinedSHA256  string                                       `json:"combined_sha256,omitempty"`
		Combined        json.RawMessage                              `json:"combined,omitempty"`
		ReviewPolicy    ReviewPolicy                                 `json:"review_policy"`
		Reviews         []ReviewAttestation                          `json:"reviews"`
		Materialization integrationworkspace.MaterializationEvidence `json:"materialization"`
		FailureReason   string                                       `json:"failure_reason,omitempty"`
	}{1, state, request.Authority.SHA256(), request.RiskReport.SHA256(), result.Textual.SHA256(), result.Textual.CanonicalJSON(), result.Combined.SHA256(), result.Combined.CanonicalJSON(), request.ReviewPolicy, request.Reviews, result.Materialization, reason}
	data, err := json.Marshal(record)
	if err != nil {
		return result, err
	}
	decisionRef, err := g.writeVerified(request.EvidencePrefix+"-decision-"+strings.ToLower(string(state))+".json", gateEvidenceKind, data)
	if err != nil {
		return result, err
	}
	result.DecisionEvidence = decisionRef
	result.FailureReason = reason
	refs := collectRefs(inputRef, decisionRef, result.Textual.CaptureRef(), result.Textual.CleanupRef(), result.Materialization.CaptureRef, result.Materialization.CleanupRef)
	refs = append(refs, result.Combined.EvidenceRefs()...)
	refs = collectRefs(refs...)
	if err := verifyAllEvidence(refs); err != nil {
		result.FailureReason = err.Error()
		return result, err
	}
	if err := g.transition(request.Authority.RunID(), domain.StateIntegrating, state, refs, map[string]any{"reason": reason}); err != nil {
		return result, err
	}
	result.State = state
	return result, nil
}

func (g *Gate) transition(runID string, from, to domain.State, refs []ledger.EvidenceRef, payload map[string]any) error {
	if err := domain.ValidateTransition(from, to); err != nil {
		return err
	}
	if len(refs) == 0 || len(refs) > maxEvidenceRefs {
		return errors.New("every gate transition requires bounded evidence")
	}
	if err := verifyAllEvidence(refs); err != nil {
		return err
	}
	event, err := ledger.NewEvent(runID, eventStateTransition, "integration-controller", "integration-gate")
	if err != nil {
		return err
	}
	event.StateFrom, event.StateTo, event.Payload, event.EvidenceRefs = from, to, payload, append([]ledger.EvidenceRef(nil), refs...)
	return g.events.Append(event)
}

func (g *Gate) writeVerified(name, kind string, data []byte) (ledger.EvidenceRef, error) {
	ref, err := g.artifacts.WriteBytes(name, kind, data)
	if err != nil {
		return ledger.EvidenceRef{}, err
	}
	actual, err := readVerified(ref)
	if err != nil || !bytes.Equal(actual, data) || ref.Kind != kind {
		return ledger.EvidenceRef{}, errors.New("evidence writer returned unavailable or mismatched evidence")
	}
	return ref, nil
}

func validateRequest(request Request) (Request, []byte, error) {
	if len(request.Authority.CanonicalJSON()) == 0 || digest(request.Authority.CanonicalJSON()) != request.Authority.SHA256() {
		return Request{}, nil, errors.New("validated authority is required")
	}
	if !validSHA(request.BaselineSHA) || request.BaselineSHA != request.Authority.Repository().StartSHA {
		return Request{}, nil, errors.New("baseline must match governed authority")
	}
	if len(request.Candidates) == 0 || request.RiskReport.SHA256() == "" || digest(request.RiskReport.CanonicalJSON()) != request.RiskReport.SHA256() {
		return Request{}, nil, errors.New("complete candidates and immutable risk report are required")
	}
	if !validText(request.EvidencePrefix) || strings.ContainsAny(request.EvidencePrefix, `/\\`) {
		return Request{}, nil, errors.New("safe evidence prefix is required")
	}
	if err := validateBlockers(request.Prerequisites); err != nil {
		return Request{}, nil, err
	}
	if err := validateReviews(request.ReviewPolicy, request.Reviews); err != nil {
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
		if err := verifyAllEvidence(input.AcceptanceEvidence); err != nil {
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

func validateReviews(policy ReviewPolicy, reviews []ReviewAttestation) error {
	if !validText(policy.PolicyIdentity) || len(policy.Required) == 0 {
		return errors.New("complete review policy is required")
	}
	provided := map[string]ReviewAttestation{}
	for _, review := range reviews {
		if !validText(review.Component) || !validText(review.Provider) || !validText(review.Verdict) || !validSHA(review.ReviewedSHA) || len(review.Evidence) == 0 {
			return errors.New("complete review attestation is required")
		}
		if err := verifyAllEvidence(review.Evidence); err != nil {
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
	}
	return nil
}

func verifySourceHeads(ctx context.Context, candidates []scheduler.AcceptedCandidate) error {
	checkedRepositories := make(map[string]struct{})
	for i, candidate := range candidates {
		input := candidate.Input()
		if _, checked := checkedRepositories[input.Repository]; !checked {
			checkedRepositories[input.Repository] = struct{}{}
			command := exec.CommandContext(ctx, "git", "--no-replace-objects", "for-each-ref", "--format=%(refname)", "refs/replace/")
			command.Dir, command.Env = input.Repository, append(gitexec.Environment(), "LC_ALL=C")
			var stdout, stderr limitedBuffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil || stdout.truncated || stderr.truncated || stdout.Len() != 0 || stderr.Len() != 0 {
				return errors.New("replacement refs exist or could not be ruled out")
			}
		}
		command := exec.CommandContext(ctx, "git", "--no-replace-objects", "show-ref", "--verify", "--hash", "refs/heads/"+input.Branch)
		command.Dir, command.Env = input.Repository, append(gitexec.Environment(), "LC_ALL=C")
		var stdout, stderr limitedBuffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || stdout.truncated || stderr.truncated || stderr.Len() != 0 || stdout.String() != input.HeadSHA+"\n" {
			return fmt.Errorf("candidate %d source head changed or could not be verified", i)
		}
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

func verifyAllEvidence(refs []ledger.EvidenceRef) error {
	if len(refs) == 0 || len(refs) > maxEvidenceRefs {
		return errors.New("complete bounded evidence is required")
	}
	for i, ref := range refs {
		if _, err := readVerified(ref); err != nil {
			return fmt.Errorf("evidence %d: %w", i, err)
		}
	}
	return nil
}
func readVerified(ref ledger.EvidenceRef) ([]byte, error) {
	if !filepath.IsAbs(ref.URI) || filepath.Clean(ref.URI) != ref.URI || !validDigest(ref.SHA256) || !validText(ref.Kind) {
		return nil, errors.New("malformed evidence reference")
	}
	info, err := os.Lstat(ref.URI)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("evidence is unavailable or not a regular file")
	}
	canonical, err := filepath.EvalSymlinks(ref.URI)
	if err != nil || canonical != ref.URI {
		return nil, errors.New("evidence path contains ambiguous symlink resolution")
	}
	data, err := os.ReadFile(ref.URI)
	if err != nil {
		return nil, err
	}
	if digest(data) != ref.SHA256 {
		return nil, errors.New("evidence digest mismatch")
	}
	return data, nil
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

func evidenceKey(ref ledger.EvidenceRef) string {
	return ref.URI + "\x00" + ref.SHA256 + "\x00" + ref.Kind
}

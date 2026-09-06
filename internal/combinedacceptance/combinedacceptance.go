// Package combinedacceptance evaluates one exact, textually clean integrated
// target under controller authority and deterministically classifies the
// resulting evidence. It does not create integration workspaces, transition
// state, resolve conflicts, or authorize a merge.
package combinedacceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/acceptance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/scheduler"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
)

// Classification is evidence-only output for later serial gate assembly.
// None of these values grants READY_FOR_MERGE.
type Classification string

const (
	ClassificationClean                 Classification = "CLEAN_COMBINED_ACCEPTANCE"
	ClassificationSemanticConflict      Classification = "SEMANTIC_CONFLICT"
	ClassificationValidationUnavailable Classification = "VALIDATION_UNAVAILABLE"
	ClassificationFailClosed            Classification = "FAIL_CLOSED"

	TextualIntegrationClean = "CLEAN"

	resultSchemaVersion    = 1
	gitOutputLimitBytes    = 1024 * 1024
	gitNoReplaceObjectsArg = "--no-replace-objects"
)

// Policy explicitly partitions required acceptance classes. A failed
// required check is a semantic conflict only when its class is governed as
// semantic; all other governed failures fail closed.
type Policy struct {
	PolicyIdentity                   string   `json:"policy_identity"`
	CombinedAcceptancePolicyIdentity string   `json:"combined_acceptance_policy_identity"`
	IntegrationPolicyIdentity        string   `json:"integration_policy_identity"`
	RiskPolicyIdentity               string   `json:"risk_policy_identity"`
	SemanticFailureClasses           []string `json:"semantic_failure_classes"`
	FailClosedFailureClasses         []string `json:"fail_closed_failure_classes"`
}

// IntegrationProvenance binds a caller-created integrated commit to the exact
// candidate application order and immutable textual-integration evidence.
type IntegrationProvenance struct {
	IntegrationID             string                        `json:"integration_id"`
	RepositoryIdentity        string                        `json:"repository_identity"`
	BaselineSHA               string                        `json:"baseline_sha"`
	IntegratedHeadSHA         string                        `json:"integrated_head_sha"`
	CandidateOrder            []scheduler.CandidateIdentity `json:"candidate_order"`
	TextualIntegrationStatus  string                        `json:"textual_integration_status"`
	IntegrationPolicyIdentity string                        `json:"integration_policy_identity"`
	RiskEvidenceSHA256        string                        `json:"risk_evidence_sha256"`
	Evidence                  []ledger.EvidenceRef          `json:"evidence"`
}

// Target is the exact caller-supplied integrated checkout. The branch and
// head are observed before and after acceptance; both must remain exact.
type Target struct {
	RepositoryPath string                `json:"repository_path"`
	Branch         string                `json:"branch"`
	HeadSHA        string                `json:"head_sha"`
	Integration    IntegrationProvenance `json:"integration"`
}

// Cause is a bounded, structured explanation. It deliberately excludes
// command output and free-form model narrative.
type Cause struct {
	Code              string               `json:"code"`
	CommandIndex      int                  `json:"command_index,omitempty"`
	CommandName       string               `json:"command_name,omitempty"`
	CommandClass      string               `json:"command_class,omitempty"`
	CommandOutcome    supervisor.Outcome   `json:"command_outcome,omitempty"`
	PolicyDisposition string               `json:"policy_disposition,omitempty"`
	EvidenceRefs      []ledger.EvidenceRef `json:"evidence_refs,omitempty"`
}

// GitObservation is immutable command evidence for one target-identity
// checkpoint. Output bytes remain in bounded evidence artifacts.
type GitObservation struct {
	HeadSHA     string              `json:"head_sha,omitempty"`
	BranchSHA   string              `json:"branch_sha,omitempty"`
	Dirty       bool                `json:"dirty"`
	Commands    []supervisor.Result `json:"commands"`
	MetadataRef ledger.EvidenceRef  `json:"metadata_ref,omitempty"`
}

// Result is immutable evidence for later serial gate assembly.
type Result struct {
	classification Classification
	policy         Policy
	target         Target
	candidates     []scheduler.AcceptedCandidate
	risk           scheduler.RiskReport
	acceptance     acceptance.Result
	initialGit     GitObservation
	finalGit       GitObservation
	causes         []Cause
	evidenceRefs   []ledger.EvidenceRef
	canonicalJSON  []byte
	digest         string
}

// Classification returns the deterministic combined-acceptance conclusion.
func (r Result) Classification() Classification { return r.classification }

// Policy returns a defensive copy of the complete classification policy.
func (r Result) Policy() Policy { return clonePolicy(r.policy) }

// Target returns a defensive copy of exact integration provenance.
func (r Result) Target() Target { return cloneTarget(r.target) }

// Candidates returns immutable accepted-candidate copies in integration order.
func (r Result) Candidates() []scheduler.AcceptedCandidate {
	return cloneCandidates(r.candidates)
}

// Risk returns the immutable final-diff risk evidence bound to this result.
func (r Result) Risk() scheduler.RiskReport { return r.risk }

// Acceptance returns the immutable controller-owned acceptance result.
func (r Result) Acceptance() acceptance.Result { return r.acceptance }

// InitialGit returns a defensive copy of the pre-acceptance observation.
func (r Result) InitialGit() GitObservation { return cloneObservation(r.initialGit) }

// FinalGit returns a defensive copy of the post-acceptance observation.
func (r Result) FinalGit() GitObservation { return cloneObservation(r.finalGit) }

// Causes returns bounded structured classification causes.
func (r Result) Causes() []Cause { return cloneCauses(r.causes) }

// EvidenceRefs returns defensive copies of all published artifact references.
func (r Result) EvidenceRefs() []ledger.EvidenceRef {
	return append([]ledger.EvidenceRef(nil), r.evidenceRefs...)
}

// CanonicalJSON returns immutable deterministic result bytes.
func (r Result) CanonicalJSON() []byte { return append([]byte(nil), r.canonicalJSON...) }

// SHA256 returns the lowercase digest of CanonicalJSON.
func (r Result) SHA256() string { return r.digest }

// MarshalJSON emits the same immutable bytes as CanonicalJSON.
func (r Result) MarshalJSON() ([]byte, error) {
	if len(r.canonicalJSON) == 0 {
		return nil, errors.New("combined acceptance result is incomplete")
	}
	return append([]byte(nil), r.canonicalJSON...), nil
}

type acceptanceExecutor interface {
	Run(context.Context, authority.Authority, acceptance.Target) (acceptance.Result, error)
}

// Evaluator owns the two target-identity checkpoints and delegates all
// acceptance-command execution to internal/acceptance.Executor.
type Evaluator struct {
	runner     acceptance.CommandRunner
	artifacts  supervisor.ArtifactWriter
	acceptance acceptanceExecutor
}

// New constructs a combined evaluator using the existing controller-owned
// branch acceptance executor.
func New(runner acceptance.CommandRunner, artifacts supervisor.ArtifactWriter) *Evaluator {
	return &Evaluator{
		runner:     runner,
		artifacts:  artifacts,
		acceptance: acceptance.New(runner, artifacts),
	}
}

// Evaluate validates exact provenance, observes the target, delegates combined
// commands to the acceptance executor, observes the target again, and derives
// a classification solely from governed policy and captured evidence.
func (e *Evaluator) Evaluate(
	ctx context.Context,
	governed authority.Authority,
	policy Policy,
	target Target,
	candidates []scheduler.AcceptedCandidate,
	risk scheduler.RiskReport,
) (Result, error) {
	if ctx == nil {
		return Result{}, errors.New("context is required")
	}
	if e == nil || e.runner == nil || e.acceptance == nil {
		return Result{}, errors.New("combined acceptance runner is required")
	}
	if e.artifacts == nil {
		return Result{}, errors.New("combined acceptance evidence writer is required")
	}

	preparedPolicy, preparedTarget, preparedCandidates, err := validateInputs(governed, policy, target, candidates, risk)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		classification: ClassificationValidationUnavailable,
		policy:         preparedPolicy,
		target:         preparedTarget,
		candidates:     preparedCandidates,
		risk:           risk,
	}

	initial, initialKind, initialErr := e.observeTarget(ctx, "initial", preparedTarget)
	result.initialGit = initial
	if initialErr != nil {
		result.classification = classificationForObservation(initialKind)
		result.causes = []Cause{{Code: initialKind, EvidenceRefs: observationRefs(initial)}}
		result.evidenceRefs = collectEvidence(result)
		if finalizeErr := finalize(&result, governed); finalizeErr != nil {
			return Result{}, finalizeErr
		}
		return result, initialErr
	}

	accepted, acceptanceErr := e.acceptance.Run(ctx, governed, acceptance.Target{
		RepositoryPath: preparedTarget.RepositoryPath,
		Branch:         preparedTarget.Branch,
		HeadSHA:        preparedTarget.HeadSHA,
	})
	result.acceptance = accepted

	final, finalKind, finalErr := e.observeTarget(ctx, "final", preparedTarget)
	result.finalGit = final
	if finalErr != nil {
		result.classification = classificationForObservation(finalKind)
		result.causes = []Cause{{Code: finalKind, EvidenceRefs: observationRefs(final)}}
	} else {
		classifyAcceptance(&result, acceptanceErr)
	}
	result.evidenceRefs = collectEvidence(result)
	if err := finalize(&result, governed); err != nil {
		return Result{}, err
	}
	if finalErr != nil {
		return result, finalErr
	}
	if acceptanceErr != nil {
		return result, acceptanceErr
	}
	return result, nil
}

func validateInputs(governed authority.Authority, policy Policy, target Target, candidates []scheduler.AcceptedCandidate, risk scheduler.RiskReport) (Policy, Target, []scheduler.AcceptedCandidate, error) {
	if len(governed.CanonicalJSON()) == 0 || governed.SHA256() == "" || sha256Hex(governed.CanonicalJSON()) != governed.SHA256() || governed.PolicyVersion() == "" {
		return Policy{}, Target{}, nil, errors.New("validated combined acceptance authority is required")
	}
	canonicalPolicy, err := canonicalPolicy(policy, governed.Acceptance())
	if err != nil {
		return Policy{}, Target{}, nil, err
	}
	if canonicalPolicy.CombinedAcceptancePolicyIdentity != governed.PolicyVersion() {
		return Policy{}, Target{}, nil, errors.New("combined acceptance policy identity does not match governed authority")
	}
	canonicalTarget, err := canonicalTarget(target)
	if err != nil {
		return Policy{}, Target{}, nil, err
	}
	if canonicalTarget.Integration.IntegrationPolicyIdentity != canonicalPolicy.IntegrationPolicyIdentity {
		return Policy{}, Target{}, nil, errors.New("integration policy identity does not match classification policy")
	}
	if canonicalTarget.Integration.RepositoryIdentity != governed.Repository().Identity {
		return Policy{}, Target{}, nil, errors.New("integration repository identity does not match governed authority")
	}
	if canonicalTarget.Integration.RiskEvidenceSHA256 != risk.SHA256() || canonicalPolicy.RiskPolicyIdentity != risk.PolicyIdentity() {
		return Policy{}, Target{}, nil, errors.New("risk evidence or policy identity does not match governed provenance")
	}
	if len(risk.CanonicalJSON()) == 0 || sha256Hex(risk.CanonicalJSON()) != risk.SHA256() {
		return Policy{}, Target{}, nil, errors.New("complete immutable risk evidence is required")
	}

	if len(candidates) == 0 || len(canonicalTarget.Integration.CandidateOrder) != len(candidates) {
		return Policy{}, Target{}, nil, errors.New("complete candidate integration order is required")
	}
	copied := make([]scheduler.AcceptedCandidate, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	var candidateRepository string
	for index, candidate := range candidates {
		input := candidate.Input()
		validated, candidateErr := scheduler.NewAcceptedCandidate(input)
		if candidateErr != nil {
			return Policy{}, Target{}, nil, fmt.Errorf("candidate %d: %w", index, candidateErr)
		}
		if candidateRepository == "" {
			candidateRepository = input.Repository
		} else if input.Repository != candidateRepository {
			return Policy{}, Target{}, nil, fmt.Errorf("candidate %d does not identify the same exact source repository", index)
		}
		if _, exists := seen[validated.Key()]; exists {
			return Policy{}, Target{}, nil, fmt.Errorf("candidate %d duplicates accepted identity", index)
		}
		seen[validated.Key()] = struct{}{}
		if canonicalTarget.Integration.CandidateOrder[index] != candidateIdentity(input) {
			return Policy{}, Target{}, nil, fmt.Errorf("candidate %d does not match integration order provenance", index)
		}
		copied[index] = validated
	}

	riskCandidates := risk.CandidateDiffs()
	if len(riskCandidates) != len(copied) {
		return Policy{}, Target{}, nil, errors.New("risk evidence candidate set is incomplete")
	}
	riskSet := make(map[string]string, len(riskCandidates))
	for index, evidence := range riskCandidates {
		input := evidence.Candidate.Input()
		validated, candidateErr := scheduler.NewAcceptedCandidate(input)
		if candidateErr != nil {
			return Policy{}, Target{}, nil, fmt.Errorf("risk candidate %d: %w", index, candidateErr)
		}
		serialized, _ := json.Marshal(input)
		if _, exists := riskSet[validated.Key()]; exists {
			return Policy{}, Target{}, nil, errors.New("risk evidence contains duplicate candidates")
		}
		riskSet[validated.Key()] = string(serialized)
	}
	for index, candidate := range copied {
		serialized, _ := json.Marshal(candidate.Input())
		if riskSet[candidate.Key()] != string(serialized) {
			return Policy{}, Target{}, nil, fmt.Errorf("candidate %d does not match exact risk provenance", index)
		}
	}
	return canonicalPolicy, canonicalTarget, copied, nil
}

func canonicalPolicy(policy Policy, commands []authority.AcceptanceCommand) (Policy, error) {
	for name, value := range map[string]string{
		"policy identity":                     policy.PolicyIdentity,
		"combined acceptance policy identity": policy.CombinedAcceptancePolicyIdentity,
		"integration policy identity":         policy.IntegrationPolicyIdentity,
		"risk policy identity":                policy.RiskPolicyIdentity,
	} {
		if !validText(value) {
			return Policy{}, fmt.Errorf("%s must be non-empty, trimmed valid UTF-8 with no control characters", name)
		}
	}
	var err error
	policy.SemanticFailureClasses, err = canonicalClasses("semantic failure classes", policy.SemanticFailureClasses)
	if err != nil {
		return Policy{}, err
	}
	policy.FailClosedFailureClasses, err = canonicalClasses("fail-closed failure classes", policy.FailClosedFailureClasses)
	if err != nil {
		return Policy{}, err
	}
	dispositions := make(map[string]string, len(policy.SemanticFailureClasses)+len(policy.FailClosedFailureClasses))
	for _, class := range policy.SemanticFailureClasses {
		dispositions[class] = "semantic_conflict"
	}
	for _, class := range policy.FailClosedFailureClasses {
		if _, exists := dispositions[class]; exists {
			return Policy{}, fmt.Errorf("acceptance class %q has ambiguous policy dispositions", class)
		}
		dispositions[class] = "fail_closed"
	}
	for index, command := range commands {
		if !command.Required {
			continue
		}
		if !validText(command.Class) {
			return Policy{}, fmt.Errorf("required acceptance command %d has no unambiguous class", index+1)
		}
		if _, exists := dispositions[command.Class]; !exists {
			return Policy{}, fmt.Errorf("required acceptance class %q has no policy disposition", command.Class)
		}
	}
	return clonePolicy(policy), nil
}

func canonicalClasses(label string, classes []string) ([]string, error) {
	seen := make(map[string]struct{}, len(classes))
	result := make([]string, 0, len(classes))
	for _, class := range classes {
		if !validText(class) {
			return nil, fmt.Errorf("%s must contain only non-empty, trimmed valid UTF-8 values with no control characters", label)
		}
		if _, exists := seen[class]; exists {
			return nil, fmt.Errorf("%s contains duplicate %q", label, class)
		}
		seen[class] = struct{}{}
		result = append(result, class)
	}
	sort.Strings(result)
	return result, nil
}

func canonicalTarget(target Target) (Target, error) {
	if target.RepositoryPath == "" || !filepath.IsAbs(target.RepositoryPath) || filepath.Clean(target.RepositoryPath) != target.RepositoryPath {
		return Target{}, errors.New("integrated repository path must be absolute and clean")
	}
	canonical, err := filepath.EvalSymlinks(target.RepositoryPath)
	if err != nil || canonical != target.RepositoryPath {
		return Target{}, errors.New("integrated repository path must exist and contain no ambiguous symlink resolution")
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return Target{}, errors.New("integrated repository path must be a directory")
	}
	if !validBranch(target.Branch) {
		return Target{}, errors.New("integrated branch is invalid")
	}
	if !validSHA(target.HeadSHA) {
		return Target{}, errors.New("integrated head must be an exact lowercase Git object ID")
	}
	integration := target.Integration
	for name, value := range map[string]string{
		"integration ID":              integration.IntegrationID,
		"repository identity":         integration.RepositoryIdentity,
		"integration policy identity": integration.IntegrationPolicyIdentity,
	} {
		if !validText(value) {
			return Target{}, fmt.Errorf("%s must be non-empty, trimmed valid UTF-8 with no control characters", name)
		}
	}
	if !validSHA(integration.BaselineSHA) || !validSHA(integration.IntegratedHeadSHA) {
		return Target{}, errors.New("integration baseline and head must be exact lowercase Git object IDs")
	}
	if target.HeadSHA != integration.IntegratedHeadSHA {
		return Target{}, errors.New("target head does not match integration provenance")
	}
	if integration.TextualIntegrationStatus != TextualIntegrationClean {
		return Target{}, errors.New("complete textually clean integration provenance is required")
	}
	if !validDigest(integration.RiskEvidenceSHA256) {
		return Target{}, errors.New("complete risk evidence digest is required")
	}
	if len(integration.CandidateOrder) == 0 || len(integration.Evidence) == 0 {
		return Target{}, errors.New("candidate order and integration evidence are required")
	}
	for index, identity := range integration.CandidateOrder {
		if !validText(identity.ProjectID) || !validText(identity.PlanID) || !validText(identity.RunID) || !validText(identity.AttemptID) || !validSHA(identity.HeadSHA) {
			return Target{}, fmt.Errorf("candidate order %d has incomplete provenance", index)
		}
	}
	for index, ref := range integration.Evidence {
		if err := validateEvidenceRef(ref); err != nil {
			return Target{}, fmt.Errorf("integration evidence %d: %w", index, err)
		}
	}
	return cloneTarget(target), nil
}

func (e *Evaluator) observeTarget(ctx context.Context, label string, target Target) (GitObservation, string, error) {
	observation := GitObservation{}
	commands := [][]string{
		{"git", gitNoReplaceObjectsArg, "rev-parse", "--verify", "--end-of-options", target.Integration.BaselineSHA + "^{commit}"},
		{"git", gitNoReplaceObjectsArg, "rev-parse", "--verify", "--end-of-options", target.HeadSHA + "^{commit}"},
		{"git", gitNoReplaceObjectsArg, "merge-base", "--is-ancestor", target.Integration.BaselineSHA, target.HeadSHA},
		{"git", gitNoReplaceObjectsArg, "rev-parse", "--verify", "HEAD"},
		{"git", gitNoReplaceObjectsArg, "status", "--porcelain=v1", "--untracked-files=normal"},
		{"git", gitNoReplaceObjectsArg, "show-ref", "--verify", "--hash", "refs/heads/" + target.Branch},
	}
	outputs := make([][]byte, 0, len(commands))
	for index, argv := range commands {
		process, err := e.runner.Run(ctx, supervisor.Command{
			Argv:             append([]string(nil), argv...),
			Cwd:              target.RepositoryPath,
			Env:              gitexec.Environment(),
			OutputLimitBytes: gitOutputLimitBytes,
			Stdout:           supervisor.EvidenceSink{Writer: e.artifacts, Name: fmt.Sprintf("combined-target-%s-%d-stdout.log", label, index+1), Kind: "combined-target-git-stdout"},
			Stderr:           supervisor.EvidenceSink{Writer: e.artifacts, Name: fmt.Sprintf("combined-target-%s-%d-stderr.log", label, index+1), Kind: "combined-target-git-stderr"},
		})
		observation.Commands = append(observation.Commands, process)
		if err != nil {
			return observation, "target_validation_unavailable", fmt.Errorf("observe integrated target %s command %d: %w", label, index+1, err)
		}
		if process.Outcome != supervisor.OutcomeSucceeded || process.StdoutTruncated || process.StderrTruncated {
			kind := "target_validation_unavailable"
			if process.Outcome == supervisor.OutcomeExited && !process.StdoutTruncated && !process.StderrTruncated {
				kind = "target_identity_mismatch"
			}
			return observation, kind, fmt.Errorf("observe integrated target %s command %d did not succeed", label, index+1)
		}
		stdout, err := readVerifiedArtifact(process.StdoutRef)
		if err != nil {
			return observation, "target_validation_unavailable", fmt.Errorf("read integrated target %s command %d evidence: %w", label, index+1, err)
		}
		stderr, err := readVerifiedArtifact(process.StderrRef)
		if err != nil || len(stderr) != 0 {
			return observation, "target_validation_unavailable", fmt.Errorf("integrated target %s command %d produced unavailable or unexpected stderr", label, index+1)
		}
		outputs = append(outputs, stdout)
	}

	baselineSHA := strings.TrimSpace(string(outputs[0]))
	integratedSHA := strings.TrimSpace(string(outputs[1]))
	ancestryOutput := outputs[2]
	observation.HeadSHA = strings.TrimSpace(string(outputs[3]))
	observation.Dirty = len(outputs[4]) != 0
	observation.BranchSHA = strings.TrimSpace(string(outputs[5]))
	if baselineSHA != target.Integration.BaselineSHA || integratedSHA != target.HeadSHA || len(ancestryOutput) != 0 ||
		!validSHA(observation.HeadSHA) || !validSHA(observation.BranchSHA) || observation.HeadSHA != target.HeadSHA || observation.BranchSHA != target.HeadSHA || observation.Dirty {
		return observation, "target_identity_mismatch", errors.New("integrated target identity moved or became ambiguous")
	}
	metadata, err := json.Marshal(struct {
		Label       string         `json:"label"`
		ExpectedSHA string         `json:"expected_sha"`
		Branch      string         `json:"branch"`
		Observation GitObservation `json:"observation"`
	}{Label: label, ExpectedSHA: target.HeadSHA, Branch: target.Branch, Observation: observation})
	if err != nil {
		return observation, "target_validation_unavailable", fmt.Errorf("marshal integrated target observation: %w", err)
	}
	observation.MetadataRef, err = e.artifacts.WriteBytes("combined-target-"+label+".json", "combined-target-git-metadata", metadata)
	if err != nil {
		return observation, "target_validation_unavailable", fmt.Errorf("publish integrated target observation: %w", err)
	}
	if _, err := readVerifiedArtifact(observation.MetadataRef); err != nil {
		return observation, "target_validation_unavailable", fmt.Errorf("verify integrated target observation: %w", err)
	}
	return observation, "", nil
}

func classifyAcceptance(result *Result, runErr error) {
	if runErr != nil || result.acceptance.Status() == acceptance.StatusUnavailable {
		result.classification = ClassificationValidationUnavailable
		result.causes = []Cause{{Code: "required_validation_unavailable", EvidenceRefs: result.acceptance.EvidenceRefs()}}
		return
	}
	if result.acceptance.Status() == acceptance.StatusPass && result.acceptance.Passed() {
		result.classification = ClassificationClean
		result.causes = nil
		return
	}
	for _, command := range result.acceptance.Commands() {
		if !command.Required || command.Process.Outcome == supervisor.OutcomeSucceeded {
			continue
		}
		if command.Process.Outcome != supervisor.OutcomeExited || !completeFailedCommandEvidence(command, result.target.RepositoryPath, result.policy.CombinedAcceptancePolicyIdentity) {
			result.classification = ClassificationValidationUnavailable
			result.causes = []Cause{{
				Code:           "required_validation_unavailable",
				CommandIndex:   command.Index,
				CommandName:    command.Name,
				CommandClass:   command.Class,
				CommandOutcome: command.Process.Outcome,
				EvidenceRefs:   commandEvidenceRefs(command),
			}}
			return
		}
		disposition := "fail_closed"
		result.classification = ClassificationFailClosed
		if contains(result.policy.SemanticFailureClasses, command.Class) {
			disposition = "semantic_conflict"
			result.classification = ClassificationSemanticConflict
		}
		result.causes = []Cause{{
			Code:              "required_check_failed",
			CommandIndex:      command.Index,
			CommandName:       command.Name,
			CommandClass:      command.Class,
			CommandOutcome:    command.Process.Outcome,
			PolicyDisposition: disposition,
			EvidenceRefs:      commandEvidenceRefs(command),
		}}
		return
	}
	result.classification = ClassificationFailClosed
	result.causes = []Cause{{Code: "acceptance_invariants_failed", EvidenceRefs: result.acceptance.EvidenceRefs()}}
}

func completeFailedCommandEvidence(command acceptance.CommandResult, repository, policyIdentity string) bool {
	process := command.Process
	if command.Index <= 0 || command.PolicyVersion != policyIdentity || command.EnvironmentPolicy != acceptance.EnvironmentPolicy ||
		len(process.Argv) == 0 || process.Cwd != repository || process.StartedAt.IsZero() || process.EndedAt.IsZero() || process.EndedAt.Before(process.StartedAt) ||
		process.StdoutTruncated || process.StderrTruncated {
		return false
	}
	for _, ref := range commandEvidenceRefs(command) {
		if _, err := readVerifiedArtifact(ref); err != nil {
			return false
		}
	}
	return true
}

func finalize(result *Result, governed authority.Authority) error {
	envelope := struct {
		SchemaVersion  int            `json:"schema_version"`
		Classification Classification `json:"classification"`
		Policy         Policy         `json:"policy"`
		Authority      struct {
			RunID         string `json:"run_id"`
			PolicyVersion string `json:"policy_version"`
			SHA256        string `json:"sha256"`
		} `json:"authority"`
		Target       Target                     `json:"target"`
		Candidates   []scheduler.CandidateInput `json:"candidates"`
		Risk         json.RawMessage            `json:"risk"`
		Acceptance   acceptanceEnvelope         `json:"acceptance"`
		InitialGit   GitObservation             `json:"initial_git"`
		FinalGit     GitObservation             `json:"final_git"`
		Causes       []Cause                    `json:"causes"`
		EvidenceRefs []ledger.EvidenceRef       `json:"evidence_refs"`
	}{
		SchemaVersion:  resultSchemaVersion,
		Classification: result.classification,
		Policy:         clonePolicy(result.policy),
		Target:         cloneTarget(result.target),
		Risk:           append(json.RawMessage(nil), result.risk.CanonicalJSON()...),
		Acceptance:     acceptanceEvidence(result.acceptance),
		InitialGit:     cloneObservation(result.initialGit),
		FinalGit:       cloneObservation(result.finalGit),
		Causes:         cloneCauses(result.causes),
		EvidenceRefs:   append([]ledger.EvidenceRef(nil), result.evidenceRefs...),
	}
	envelope.Authority.RunID = governed.RunID()
	envelope.Authority.PolicyVersion = governed.PolicyVersion()
	envelope.Authority.SHA256 = governed.SHA256()
	envelope.Candidates = make([]scheduler.CandidateInput, len(result.candidates))
	for index, candidate := range result.candidates {
		envelope.Candidates[index] = candidate.Input()
	}
	canonical, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal combined acceptance evidence: %w", err)
	}
	result.canonicalJSON = canonical
	result.digest = sha256Hex(canonical)
	return nil
}

type acceptanceEnvelope struct {
	Status       acceptance.Status          `json:"status"`
	Passed       bool                       `json:"passed"`
	Commands     []acceptance.CommandResult `json:"commands"`
	FinalGit     *acceptance.GitEvidence    `json:"final_git,omitempty"`
	EvidenceRefs []ledger.EvidenceRef       `json:"evidence_refs"`
}

func acceptanceEvidence(result acceptance.Result) acceptanceEnvelope {
	envelope := acceptanceEnvelope{Status: result.Status(), Passed: result.Passed(), Commands: result.Commands(), EvidenceRefs: result.EvidenceRefs()}
	if git, ok := result.FinalGit(); ok {
		envelope.FinalGit = &git
	}
	return envelope
}

func collectEvidence(result Result) []ledger.EvidenceRef {
	refs := append([]ledger.EvidenceRef(nil), result.target.Integration.Evidence...)
	refs = append(refs, observationRefs(result.initialGit)...)
	refs = append(refs, result.acceptance.EvidenceRefs()...)
	refs = append(refs, observationRefs(result.finalGit)...)
	seen := make(map[string]struct{}, len(refs))
	unique := refs[:0]
	for _, ref := range refs {
		if err := validateEvidenceRef(ref); err != nil {
			continue
		}
		key := ref.URI + "\x00" + ref.SHA256 + "\x00" + ref.Kind
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, ref)
	}
	return append([]ledger.EvidenceRef(nil), unique...)
}

func observationRefs(observation GitObservation) []ledger.EvidenceRef {
	var refs []ledger.EvidenceRef
	for _, command := range observation.Commands {
		refs = append(refs, command.StdoutRef, command.StderrRef)
	}
	if observation.MetadataRef.URI != "" {
		refs = append(refs, observation.MetadataRef)
	}
	return refs
}

func commandEvidenceRefs(command acceptance.CommandResult) []ledger.EvidenceRef {
	return []ledger.EvidenceRef{command.Process.StdoutRef, command.Process.StderrRef, command.MetadataRef}
}

func classificationForObservation(code string) Classification {
	if code == "target_identity_mismatch" {
		return ClassificationFailClosed
	}
	return ClassificationValidationUnavailable
}

func readVerifiedArtifact(ref ledger.EvidenceRef) ([]byte, error) {
	if err := validateEvidenceRef(ref); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(ref.URI)
	if err != nil {
		return nil, err
	}
	if sha256Hex(data) != ref.SHA256 {
		return nil, errors.New("evidence SHA256 mismatch")
	}
	return data, nil
}

func validateEvidenceRef(ref ledger.EvidenceRef) error {
	if !validText(ref.URI) || !validText(ref.Kind) || !validDigest(ref.SHA256) {
		return errors.New("complete bounded evidence reference is required")
	}
	if parsed, err := url.Parse(ref.URI); err == nil && parsed.Scheme != "" && (parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "") {
		return errors.New("evidence URI must not contain credentials, query values, or fragments")
	}
	return nil
}

func candidateIdentity(input scheduler.CandidateInput) scheduler.CandidateIdentity {
	return scheduler.CandidateIdentity{ProjectID: input.ProjectID, PlanID: input.PlanID, RunID: input.RunID, AttemptID: input.AttemptID, HeadSHA: input.HeadSHA}
}

func validText(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validSHA(value string) bool {
	return (len(value) == 40 || len(value) == 64) && validLowerHex(value)
}

func validDigest(value string) bool { return len(value) == sha256.Size*2 && validLowerHex(value) }

func validLowerHex(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func validBranch(value string) bool {
	if !validText(value) || value == "@" || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") ||
		strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || strings.Contains(value, "//") || strings.Contains(value, "..") ||
		strings.Contains(value, "@{") || strings.ContainsAny(value, " ~^:?*[\\") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func contains(values []string, value string) bool {
	index := sort.SearchStrings(values, value)
	return index < len(values) && values[index] == value
}

func sha256Hex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func clonePolicy(policy Policy) Policy {
	policy.SemanticFailureClasses = append([]string(nil), policy.SemanticFailureClasses...)
	policy.FailClosedFailureClasses = append([]string(nil), policy.FailClosedFailureClasses...)
	return policy
}

func cloneTarget(target Target) Target {
	target.Integration.CandidateOrder = append([]scheduler.CandidateIdentity(nil), target.Integration.CandidateOrder...)
	target.Integration.Evidence = append([]ledger.EvidenceRef(nil), target.Integration.Evidence...)
	return target
}

func cloneCandidates(candidates []scheduler.AcceptedCandidate) []scheduler.AcceptedCandidate {
	result := make([]scheduler.AcceptedCandidate, len(candidates))
	for index, candidate := range candidates {
		validated, _ := scheduler.NewAcceptedCandidate(candidate.Input())
		result[index] = validated
	}
	return result
}

func cloneObservation(observation GitObservation) GitObservation {
	observation.Commands = append([]supervisor.Result(nil), observation.Commands...)
	for index := range observation.Commands {
		observation.Commands[index].Argv = append([]string(nil), observation.Commands[index].Argv...)
	}
	return observation
}

func cloneCauses(causes []Cause) []Cause {
	result := make([]Cause, len(causes))
	copy(result, causes)
	for index := range result {
		result[index].EvidenceRefs = append([]ledger.EvidenceRef(nil), result[index].EvidenceRefs...)
	}
	return result
}

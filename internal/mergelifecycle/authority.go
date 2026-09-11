package mergelifecycle

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

type readyLedgerState struct {
	event        ledger.Event
	eventJSON    []byte
	eventOffset  int64
	prefix       []byte
	sequence     int64
	current      domain.State
	currentEvent ledger.Event
	records      int
}

type assembledAuthority struct {
	governed    GovernedAuthority
	repository  githublifecycle.RepositoryBindingV1
	ready       githublifecycle.ReadyAuthorityBindingV1
	policy      githublifecycle.MergePolicyV1
	authority   githublifecycle.Authority
	ledgerState readyLedgerState
	ledgerBytes []byte
	ledgerID    string
}

func assembleAuthority(governed GovernedAuthority, ledgerBytes []byte, ledgerID string, limits githublifecycle.Limits) (assembledAuthority, error) {
	var result assembledAuthority
	phase3, err := authority.New(governed.Phase3Authority.Manifest())
	if err != nil || phase3.RunID() == "" || !bytes.Equal(phase3.CanonicalJSON(), governed.Phase3Authority.CanonicalJSON()) || phase3.RunID() != governed.Phase3Authority.RunID() {
		return result, errors.Join(errors.New("Phase-3 authority fails immutable reconstruction"), err)
	}
	if governed.ProjectID == "" || governed.PlanID == "" || governed.AttemptID == "" || phase3.RunID() != governed.Phase3Authority.RunID() || phase3.RunID() == "" ||
		governed.Repository.String() == "" || governed.BaseBranch.String() == "" || governed.HeadBranch.String() == "" || governed.PullRequest.Number() <= 0 {
		return result, errors.New("governed merge authority is incomplete")
	}
	// The Phase-3 authority predates the lifecycle contracts' strict JSON
	// canonicalization rule: it is deterministic, but its struct field order is
	// not lexical. Normalize the independently reconstructed manifest before
	// binding it into the frozen READY contract.
	var phase3Generic any
	if err := json.Unmarshal(phase3.CanonicalJSON(), &phase3Generic); err != nil {
		return result, fmt.Errorf("decode Phase-3 authority: %w", err)
	}
	phase3JSON, err := json.Marshal(phase3Generic)
	if err != nil {
		return result, fmt.Errorf("canonicalize Phase-3 authority: %w", err)
	}
	repository, err := githublifecycle.NewRepositoryBindingV1(governed.RepositoryBinding)
	if err != nil {
		return result, fmt.Errorf("derive repository binding: %w", err)
	}
	if repository.Input().GitHubRepository != governed.Repository || phase3.Repository().Identity != repository.Input().Phase3RepositoryIdentity ||
		phase3.Repository().Path != repository.Input().Phase3RepositoryPath || phase3.Repository().StartSHA != repository.Input().Phase3StartSHA.String() {
		return result, errors.New("repository mapping does not match Phase-3 authority")
	}
	state, err := scanReadyLedger(ledgerBytes, governed.ProjectID, governed.PlanID, phase3.RunID(), governed.AttemptID, limits.MaxLedgerScanRecords)
	if err != nil {
		return result, err
	}
	expected, err := githublifecycle.ParseCanonicalExpectedMergeContent(governed.ExpectedContent.CanonicalJSON())
	if err != nil || expected.SHA256() != governed.ExpectedContent.SHA256() {
		return result, errors.Join(errors.New("expected merge content fails immutable reconstruction"), err)
	}
	if expected.SourceIntegratedHeadSHA() != mustSHA(governed.ExpectedContent.SourceIntegratedHeadSHA().String()) ||
		expected.SourceIntegrationEvidence() != governed.ExpectedContent.SourceIntegrationEvidence() {
		return result, errors.New("expected merge content changed during reconstruction")
	}
	readyInput := githublifecycle.ReadyAuthorityBindingV1Input{
		Phase3AuthorityJSON: phase3JSON, Phase3AuthoritySHA256: digest(phase3JSON), ProjectID: governed.ProjectID,
		PlanID: governed.PlanID, RunID: phase3.RunID(), AttemptID: governed.AttemptID, AcceptedSources: governed.AcceptedSources,
		RepositoryBinding: repository, ReadyEventJSON: state.eventJSON, ReadyEventSHA256: digest(state.eventJSON), ReadyEventID: state.event.EventID,
		ReadyEventUnixNano: state.event.Timestamp.UnixNano(), LedgerIdentity: ledgerID, ReadyEventByteOffset: state.eventOffset,
		ReadyRunStateSequence: state.sequence, LedgerPrefixLength: int64(len(state.prefix)), LedgerPrefixSHA256: digest(state.prefix),
		ReadyTransitionOrdinal: state.sequence, ReadyEvidenceRefs: state.event.EvidenceRefs,
		ReadyDecisionRef: expected.SourceIntegrationEvidence(), EvidenceClosureRefs: governed.EvidenceClosure,
		IntegratedHeadSHA: expected.SourceIntegratedHeadSHA(), BaselineSHA: expected.SourceBaselineSHA(), ExpectedTreeSHA: expected.ExpectedResultTreeSHA(),
	}
	ready, err := githublifecycle.NewReadyAuthorityBindingV1(readyInput, limits)
	if err != nil {
		return result, fmt.Errorf("derive READY binding: %w", err)
	}
	policyBinding, err := githublifecycle.NewPolicyAuthorityBindingV1(githublifecycle.PolicyAuthorityBindingV1Input{
		SourceConfiguration: governed.Policy.SourceConfiguration, RepositoryBindingSHA256: repository.SHA256(), Phase3AuthoritySHA256: digest(phase3JSON),
		ReadyBindingSHA256: ready.SHA256(), RequiredActingPrincipal: governed.Policy.RequiredPrincipal,
	})
	if err != nil {
		return result, fmt.Errorf("derive policy authority: %w", err)
	}
	policy, err := githublifecycle.NewMergePolicyV1(githublifecycle.MergePolicyV1Input{
		PolicyVersion: governed.Policy.Version, AuthorityBinding: policyBinding, Method: githublifecycle.MergeMethodMerge,
		RequiredChecks: governed.Policy.RequiredChecks, EligibleReviewers: governed.Policy.EligibleReviewers,
		RequiredReviewers: governed.Policy.RequiredReviewers, MinimumApprovals: governed.Policy.MinimumApprovals, Recipe: governed.Policy.Recipe,
	}, limits)
	if err != nil {
		return result, fmt.Errorf("derive merge policy: %w", err)
	}
	value, err := githublifecycle.NewAuthority(githublifecycle.AuthorityInput{
		Repository: governed.Repository, BaseBranch: governed.BaseBranch, HeadBranch: governed.HeadBranch,
		HeadSHA: expected.SourceIntegratedHeadSHA(), ExpectedBaseTipSHA: expected.SourceBaselineSHA(), PullRequest: &governed.PullRequest,
		AllowedMergeMethod: githublifecycle.MergeMethodMerge, Actor: governed.Policy.RequiredPrincipal,
		ExpectedContent: expected, ReadyBinding: ready, MergePolicy: policy,
	})
	if err != nil {
		return result, fmt.Errorf("derive lifecycle authority: %w", err)
	}
	capability, err := githublifecycle.ParseCanonicalProviderCapabilityV1(governed.ProviderCapability.CanonicalJSON(), limits)
	if err != nil || capability.SHA256() != governed.ProviderCapability.SHA256() {
		return result, errors.Join(errors.New("frozen provider capability fails independent reconstruction"), err)
	}
	result = assembledAuthority{governed, repository, ready, policy, value, state, append([]byte(nil), ledgerBytes...), ledgerID}
	return result, nil
}

func scanReadyLedger(data []byte, projectID, planID, runID, attemptID string, maxRecords int) (readyLedgerState, error) {
	var result readyLedgerState
	if len(data) == 0 || len(data) > 64<<20 || data[len(data)-1] != '\n' {
		return result, errors.New("authoritative ledger is empty, partial, or oversized")
	}
	seen := make(map[string]struct{})
	var offset int64
	var current domain.State
	var sequence int64
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if maxRecords <= 0 || len(lines) > maxRecords {
		return result, errors.New("authoritative ledger record bound exceeded")
	}
	for _, line := range lines {
		if len(line) == 0 || len(line) > 256<<10 {
			return result, errors.New("authoritative ledger contains an invalid line")
		}
		var event ledger.Event
		if err := strictCanonical(line, &event); err != nil || event.Validate() != nil {
			return result, errors.New("authoritative ledger contains invalid or non-canonical event bytes")
		}
		if _, exists := seen[event.EventID]; exists {
			return result, errors.New("authoritative ledger contains duplicate event identity")
		}
		seen[event.EventID] = struct{}{}
		boundRun := event.ProjectID == projectID && event.PlanID == planID && event.RunID == runID
		if event.RunID == runID && event.StateFrom != "" && !boundRun {
			return result, errors.New("authoritative ledger contains ambiguous run provenance")
		}
		if boundRun && event.StateFrom != "" {
			if len(result.eventJSON) == 0 && event.AttemptID != attemptID {
				return result, errors.New("authoritative pre-READY transition has ambiguous attempt provenance")
			}
			sequence++
			if sequence == 1 && event.StateFrom != domain.StateRunCreated || sequence > 1 && event.StateFrom != current {
				return result, errors.New("authoritative run transitions are discontinuous")
			}
			current = event.StateTo
			result.currentEvent = event
			if event.StateFrom == domain.StateIntegrationAccepted && event.StateTo == domain.StateReadyForMerge {
				if len(result.eventJSON) != 0 || event.AttemptID != attemptID {
					return result, errors.New("READY transition is duplicated or has wrong attempt provenance")
				}
				result.event, result.eventJSON, result.eventOffset, result.sequence = event, append([]byte(nil), line...), offset, sequence
				result.prefix = append([]byte(nil), data[:offset+int64(len(line))+1]...)
			}
		}
		offset += int64(len(line) + 1)
	}
	if len(result.eventJSON) == 0 {
		return result, errors.New("exact READY transition was not found")
	}
	result.current, result.records = current, len(lines)
	return result, nil
}

func currentReadyProof(a assembledAuthority, ledgerBytes []byte, observedUnixNano, sequence int64, limits githublifecycle.Limits) (githublifecycle.CurrentReadyProofV1, error) {
	state, err := scanReadyLedger(ledgerBytes, a.governed.ProjectID, a.governed.PlanID, a.governed.Phase3Authority.RunID(), a.governed.AttemptID, limits.MaxLedgerScanRecords)
	if err != nil || state.current != domain.StateReadyForMerge {
		return githublifecycle.CurrentReadyProofV1{}, errors.Join(errors.New("READY is not the current durable state"), err)
	}
	ref := ledger.EvidenceRef{URI: a.ledgerID, SHA256: digest(ledgerBytes), Kind: githublifecycle.CurrentReadyLedgerEvidenceKindV1}
	return githublifecycle.NewCurrentReadyProofV1(githublifecycle.CurrentReadyProofV1Input{
		ReadyBinding: a.ready, ControllerSequence: sequence, ObservedUnixNano: observedUnixNano, ObservedLedgerIdentity: a.ledgerID,
		ObservedBoundPrefixSHA256: digest(state.prefix), ObservedLedgerLength: int64(len(ledgerBytes)), ObservedLedgerSHA256: digest(ledgerBytes),
		ObservedLedgerJSONL: ledgerBytes, NoLaterTransition: true, EvidenceRefs: []ledger.EvidenceRef{ref},
	}, limits)
}

func mustSHA(value string) githublifecycle.GitSHA {
	result, _ := githublifecycle.NewGitSHA(value)
	return result
}

func deterministicWriteID(a assembledAuthority) string {
	payload, _ := json.Marshal(struct {
		Schema    string `json:"schema"`
		RunID     string `json:"run_id"`
		Ready     string `json:"ready_binding_sha256"`
		Authority string `json:"authority_sha256"`
	}{"merge-write-id-v1", a.governed.Phase3Authority.RunID(), a.ready.SHA256(), authorityDigest(a.authority)})
	return digest(payload)
}

func authorityDigest(value githublifecycle.Authority) string {
	result, _ := value.SHA256()
	return result
}

func attemptKey(a assembledAuthority, writeID string) string {
	payload := strings.Join([]string{a.repository.Input().GitHubRepositoryNodeID, "refs/heads/" + a.governed.BaseBranch.String(),
		a.governed.Phase3Authority.RunID(), a.ready.Input().ReadyEventID, a.ready.Input().ReadyEventSHA256, authorityDigest(a.authority), writeID}, "\x00")
	sum := sha256.Sum256([]byte(payload))
	return fmt.Sprintf("%x", sum[:])
}

package githublifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

const (
	CurrentReadyProofSchemaV1        = "current-ready-proof-v1"
	CurrentReadyLedgerEvidenceKindV1 = "current-ready-ledger-observation"
	FinalRevalidationSchemaV1        = "final-revalidation-v1"
)

type CurrentReadyProofV1Input struct {
	ReadyBinding              ReadyAuthorityBindingV1
	ControllerSequence        int64
	ObservedUnixNano          int64
	ObservedLedgerIdentity    string
	ObservedBoundPrefixSHA256 string
	ObservedLedgerLength      int64
	ObservedLedgerSHA256      string
	ObservedLedgerJSONL       []byte
	NoLaterTransition         bool
	EvidenceRefs              []ledger.EvidenceRef
}

type CurrentReadyProofV1 struct {
	input     CurrentReadyProofV1Input
	canonical []byte
	digest    string
	limitsSHA string
}

type currentReadyProofWireV1 struct {
	Schema                  string               `json:"schema"`
	ReadyBinding            json.RawMessage      `json:"ready_binding"`
	ReadyBindingSHA256      string               `json:"ready_binding_sha256"`
	ControllerSequence      int64                `json:"controller_sequence"`
	ObservedUnixNano        int64                `json:"observed_unix_nano"`
	LedgerIdentity          string               `json:"ledger_identity"`
	BoundPrefixLength       int64                `json:"bound_prefix_length"`
	BoundPrefixSHA256       string               `json:"bound_prefix_sha256"`
	ObservedLedgerLength    int64                `json:"observed_ledger_length"`
	ObservedLedgerSHA256    string               `json:"observed_ledger_sha256"`
	ObservedLedgerJSONL     []byte               `json:"observed_ledger_jsonl"`
	CurrentEventID          string               `json:"current_event_id"`
	CurrentEventSHA256      string               `json:"current_event_sha256"`
	CurrentRunStateSequence int64                `json:"current_run_state_sequence"`
	CurrentState            domain.State         `json:"current_state"`
	NoLaterTransition       bool                 `json:"no_later_transition"`
	EvidenceRefs            []ledger.EvidenceRef `json:"evidence_refs"`
	LimitsSHA256            string               `json:"limits_sha256"`
}

func NewCurrentReadyProofV1(input CurrentReadyProofV1Input, limits Limits) (CurrentReadyProofV1, error) {
	input.ReadyBinding = cloneReadyBinding(input.ReadyBinding)
	input.ObservedLedgerJSONL = append([]byte(nil), input.ObservedLedgerJSONL...)
	input.EvidenceRefs = append([]ledger.EvidenceRef(nil), input.EvidenceRefs...)
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return CurrentReadyProofV1{}, err
	}
	if !input.ReadyBinding.valid() {
		return CurrentReadyProofV1{}, errors.New("current READY proof lacks a READY binding")
	}
	recovered, err := ParseCanonicalReadyAuthorityBindingV1(input.ReadyBinding.CanonicalJSON(), limits)
	if err != nil || recovered.SHA256() != input.ReadyBinding.SHA256() {
		return CurrentReadyProofV1{}, errors.New("current READY proof binding fails independent validation")
	}
	ready := input.ReadyBinding.input
	if input.ControllerSequence <= 0 || input.ObservedUnixNano < ready.ReadyEventUnixNano ||
		!validText(input.ObservedLedgerIdentity, limits.MaxTextBytes, false) || input.ObservedLedgerIdentity != ready.LedgerIdentity ||
		input.ObservedBoundPrefixSHA256 != ready.LedgerPrefixSHA256 ||
		!input.NoLaterTransition || len(input.EvidenceRefs) == 0 || canonicalizeEvidence(&input.EvidenceRefs, limits) != nil {
		return CurrentReadyProofV1{}, errors.New("current READY proof is incomplete, stale, or unbounded")
	}
	if err := validateCurrentReadyLedger(input, limits); err != nil {
		return CurrentReadyProofV1{}, err
	}
	wire := currentReadyProofWire(input, limitsSHA)
	canonical, digest, err := canonicalJSON(wire)
	if err != nil {
		return CurrentReadyProofV1{}, err
	}
	return CurrentReadyProofV1{input, canonical, digest, limitsSHA}, nil
}

func currentReadyProofWire(input CurrentReadyProofV1Input, limitsSHA string) currentReadyProofWireV1 {
	ready := input.ReadyBinding.input
	return currentReadyProofWireV1{
		CurrentReadyProofSchemaV1, input.ReadyBinding.CanonicalJSON(), input.ReadyBinding.SHA256(), input.ControllerSequence,
		input.ObservedUnixNano, input.ObservedLedgerIdentity, ready.LedgerPrefixLength, ready.LedgerPrefixSHA256,
		input.ObservedLedgerLength, input.ObservedLedgerSHA256, input.ObservedLedgerJSONL, ready.ReadyEventID, ready.ReadyEventSHA256,
		ready.ReadyRunStateSequence, domain.StateReadyForMerge, input.NoLaterTransition, input.EvidenceRefs, limitsSHA,
	}
}

func (p CurrentReadyProofV1) Input() CurrentReadyProofV1Input {
	i := p.input
	i.ReadyBinding = cloneReadyBinding(i.ReadyBinding)
	i.ObservedLedgerJSONL = append([]byte(nil), i.ObservedLedgerJSONL...)
	i.EvidenceRefs = append([]ledger.EvidenceRef(nil), i.EvidenceRefs...)
	return i
}
func (p CurrentReadyProofV1) CanonicalJSON() []byte { return append([]byte(nil), p.canonical...) }
func (p CurrentReadyProofV1) SHA256() string        { return p.digest }
func (p CurrentReadyProofV1) valid() bool {
	return len(p.canonical) > 0 && validSHA256(p.digest) && digestBytes(p.canonical) == p.digest && validSHA256(p.limitsSHA)
}

func ParseCanonicalCurrentReadyProofV1(data []byte, limits Limits) (CurrentReadyProofV1, error) {
	var wire currentReadyProofWireV1
	if err := strictDecode(data, &wire); err != nil {
		return CurrentReadyProofV1{}, err
	}
	if wire.Schema != CurrentReadyProofSchemaV1 {
		return CurrentReadyProofV1{}, errors.New("unsupported current READY proof schema")
	}
	ready, err := ParseCanonicalReadyAuthorityBindingV1(wire.ReadyBinding, limits)
	if err != nil {
		return CurrentReadyProofV1{}, err
	}
	value, err := NewCurrentReadyProofV1(CurrentReadyProofV1Input{
		ReadyBinding: ready, ControllerSequence: wire.ControllerSequence, ObservedUnixNano: wire.ObservedUnixNano,
		ObservedLedgerIdentity:    wire.LedgerIdentity,
		ObservedBoundPrefixSHA256: wire.BoundPrefixSHA256,
		ObservedLedgerLength:      wire.ObservedLedgerLength, ObservedLedgerSHA256: wire.ObservedLedgerSHA256,
		ObservedLedgerJSONL: wire.ObservedLedgerJSONL,
		NoLaterTransition:   wire.NoLaterTransition, EvidenceRefs: wire.EvidenceRefs,
	}, limits)
	if err != nil {
		return CurrentReadyProofV1{}, err
	}
	rebuilt := currentReadyProofWire(value.input, value.limitsSHA)
	if wire.ReadyBindingSHA256 != ready.SHA256() || wire.LedgerIdentity != rebuilt.LedgerIdentity ||
		wire.BoundPrefixLength != rebuilt.BoundPrefixLength || wire.BoundPrefixSHA256 != rebuilt.BoundPrefixSHA256 ||
		wire.CurrentEventID != rebuilt.CurrentEventID || wire.CurrentEventSHA256 != rebuilt.CurrentEventSHA256 ||
		wire.CurrentRunStateSequence != rebuilt.CurrentRunStateSequence || wire.CurrentState != domain.StateReadyForMerge ||
		wire.LimitsSHA256 != value.limitsSHA {
		return CurrentReadyProofV1{}, errors.New("current READY proof derived identity disagrees")
	}
	if err := requireCanonical(data, value.canonical); err != nil {
		return CurrentReadyProofV1{}, err
	}
	return value, nil
}

func validateCurrentReadyLedger(input CurrentReadyProofV1Input, limits Limits) error {
	ready := input.ReadyBinding.input
	ledgerBytes := input.ObservedLedgerJSONL
	if len(ledgerBytes) == 0 || len(ledgerBytes) > limits.MaxPaginationClosureBytes ||
		input.ObservedLedgerLength != int64(len(ledgerBytes)) || input.ObservedLedgerLength < ready.LedgerPrefixLength ||
		input.ObservedLedgerSHA256 != digestBytes(ledgerBytes) || !containsEvidenceDigest(input.EvidenceRefs, CurrentReadyLedgerEvidenceKindV1, input.ObservedLedgerSHA256) {
		return errors.New("current READY proof lacks exact bounded ledger bytes and retained evidence")
	}
	if ready.LedgerPrefixLength > int64(len(ledgerBytes)) || digestBytes(ledgerBytes[:ready.LedgerPrefixLength]) != ready.LedgerPrefixSHA256 {
		return errors.New("current READY proof changed the exact bound ledger prefix")
	}
	eventEnd := ready.ReadyEventByteOffset + int64(len(ready.ReadyEventJSON))
	if eventEnd+1 != ready.LedgerPrefixLength || eventEnd >= int64(len(ledgerBytes)) || ledgerBytes[eventEnd] != '\n' ||
		!bytes.Equal(ledgerBytes[ready.ReadyEventByteOffset:eventEnd], ready.ReadyEventJSON) {
		return errors.New("current READY proof does not contain the bound READY event at its exact offset")
	}
	seenEventIDs := make(map[string]struct{})
	var currentState domain.State
	var transitionOrdinal int64
	readyFound := false
	rest := ledgerBytes
	var offset int64
	for len(rest) > 0 {
		newline := bytes.IndexByte(rest, '\n')
		if newline < 0 || newline == 0 {
			return errors.New("current READY ledger observation is not complete JSONL")
		}
		line := rest[:newline]
		var event ledger.Event
		if err := strictDecode(line, &event); err != nil || event.Validate() != nil {
			return errors.New("current READY ledger observation contains an invalid event")
		}
		canonical, _ := json.Marshal(event)
		if !bytes.Equal(canonical, line) {
			return errors.New("current READY ledger observation contains non-canonical event bytes")
		}
		if _, exists := seenEventIDs[event.EventID]; exists {
			return errors.New("current READY ledger observation contains a duplicate event identity")
		}
		seenEventIDs[event.EventID] = struct{}{}

		boundRun := event.ProjectID == ready.ProjectID && event.PlanID == ready.PlanID && event.RunID == ready.RunID
		if event.RunID == ready.RunID && event.StateFrom != "" && !boundRun {
			return errors.New("current READY ledger observation contains an ambiguous bound run identity")
		}
		if event.EventID == ready.ReadyEventID && (!boundRun || offset != ready.ReadyEventByteOffset) {
			return errors.New("current READY event identity appears at the wrong ledger position or run")
		}
		if boundRun && event.StateFrom != "" {
			transitionOrdinal++
			if transitionOrdinal == 1 && event.StateFrom != domain.StateRunCreated {
				return errors.New("current READY ledger history does not start from RUN_CREATED")
			}
			if currentState != "" && event.StateFrom != currentState {
				return errors.New("current READY ledger transitions are discontinuous")
			}
			currentState = event.StateTo
			if offset == ready.ReadyEventByteOffset {
				if readyFound || event.EventID != ready.ReadyEventID || !bytes.Equal(line, ready.ReadyEventJSON) ||
					transitionOrdinal != ready.ReadyTransitionOrdinal || transitionOrdinal != ready.ReadyRunStateSequence {
					return errors.New("current READY ledger event, sequence, or transition ordinal is incoherent")
				}
				readyFound = true
			} else if readyFound {
				return errors.New("current READY proof contains a later transition for the bound run")
			}
		} else if offset == ready.ReadyEventByteOffset {
			return errors.New("current READY offset does not identify the bound run transition")
		}
		offset += int64(newline + 1)
		rest = rest[newline+1:]
	}
	if !readyFound || currentState != domain.StateReadyForMerge {
		return errors.New("current READY ledger observation does not derive the bound current state")
	}
	return nil
}

func containsEvidenceDigest(refs []ledger.EvidenceRef, kind, digest string) bool {
	for _, ref := range refs {
		if ref.Kind == kind && ref.SHA256 == digest {
			return true
		}
	}
	return false
}

type FinalRevalidationV1Input struct {
	MergeInput               MergeInput
	ControllerSequence       int64
	StartedUnixNano          int64
	CompletedUnixNano        int64
	CurrentReadyProof        CurrentReadyProofV1
	PullRequest              AuthoritativePullRequestSnapshotV1
	Checks                   []Check
	CheckRunsClosure         PaginationClosureV1
	CommitStatusesClosure    PaginationClosureV1
	Capability               ProviderCapabilityV1
	Recipe                   MergeCommitRecipeV1
	Counters                 AuthorizationCountersV1
	NoTargetRequestAttempted bool
	EvidenceRefs             []ledger.EvidenceRef
}

type FinalRevalidationV1 struct {
	input          FinalRevalidationV1Input
	decisionSHA256 string
	canonical      []byte
	digest         string
	limitsSHA      string
}

type finalRevalidationWireV1 struct {
	Schema                      string                  `json:"schema"`
	MergeInputSHA256            string                  `json:"merge_input_sha256"`
	ControllerSequence          int64                   `json:"controller_sequence"`
	StartedUnixNano             int64                   `json:"started_unix_nano"`
	CompletedUnixNano           int64                   `json:"completed_unix_nano"`
	CurrentReadyProof           json.RawMessage         `json:"current_ready_proof"`
	CurrentReadyProofSHA256     string                  `json:"current_ready_proof_sha256"`
	PullRequest                 json.RawMessage         `json:"pull_request"`
	PullRequestSHA256           string                  `json:"pull_request_sha256"`
	Checks                      []checkWire             `json:"checks"`
	CheckRunsClosure            json.RawMessage         `json:"check_runs_closure"`
	CheckRunsClosureSHA256      string                  `json:"check_runs_closure_sha256"`
	CommitStatusesClosure       json.RawMessage         `json:"commit_statuses_closure"`
	CommitStatusesClosureSHA256 string                  `json:"commit_statuses_closure_sha256"`
	Capability                  json.RawMessage         `json:"provider_capability"`
	CapabilitySHA256            string                  `json:"provider_capability_sha256"`
	Recipe                      json.RawMessage         `json:"recipe"`
	RecipeSHA256                string                  `json:"recipe_sha256"`
	Counters                    AuthorizationCountersV1 `json:"counters"`
	NoTargetRequestAttempted    bool                    `json:"no_target_request_attempted"`
	Verdict                     string                  `json:"verdict"`
	PREligible                  bool                    `json:"pr_eligible"`
	FinalDecisionSHA256         string                  `json:"final_decision_sha256"`
	EvidenceRefs                []ledger.EvidenceRef    `json:"evidence_refs"`
	LimitsSHA256                string                  `json:"limits_sha256"`
}

func NewFinalRevalidationV1(input FinalRevalidationV1Input, limits Limits) (FinalRevalidationV1, error) {
	input = cloneFinalRevalidationInput(input)
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return FinalRevalidationV1{}, err
	}
	if err := validateMergeInput(input.MergeInput, limits); err != nil {
		return FinalRevalidationV1{}, err
	}
	input.Checks, err = canonicalizeChecksForHead(input.Checks, input.MergeInput.authority.HeadSHA(), limits)
	if err != nil {
		return FinalRevalidationV1{}, err
	}
	if !input.CurrentReadyProof.valid() || input.CurrentReadyProof.input.ReadyBinding.SHA256() != input.MergeInput.authority.ReadyBinding().SHA256() {
		return FinalRevalidationV1{}, errors.New("final revalidation does not bind current READY authority")
	}
	rebuiltReadyProof, err := NewCurrentReadyProofV1(input.CurrentReadyProof.input, limits)
	if err != nil || rebuiltReadyProof.SHA256() != input.CurrentReadyProof.SHA256() ||
		!bytes.Equal(rebuiltReadyProof.CanonicalJSON(), input.CurrentReadyProof.CanonicalJSON()) {
		return FinalRevalidationV1{}, errors.New("final current-READY proof fails independent validation")
	}
	admissionObservedUnixNano := latestObservationUnixNano(input.MergeInput.initialPullRequest, input.MergeInput.checkRunsClosure, input.MergeInput.commitStatusesClosure)
	if input.ControllerSequence <= input.CurrentReadyProof.input.ControllerSequence || input.StartedUnixNano <= admissionObservedUnixNano ||
		input.CurrentReadyProof.input.ObservedUnixNano <= admissionObservedUnixNano ||
		input.CurrentReadyProof.input.ObservedUnixNano > input.StartedUnixNano ||
		input.CompletedUnixNano < input.StartedUnixNano || input.PullRequest.input.Snapshot.ObservedUnixNano() < input.StartedUnixNano ||
		input.PullRequest.input.Snapshot.ObservedUnixNano() > input.CompletedUnixNano {
		return FinalRevalidationV1{}, errors.New("final revalidation controller ordering is invalid")
	}
	for _, closure := range []PaginationClosureV1{input.PullRequest.input.ReviewsClosure, input.CheckRunsClosure, input.CommitStatusesClosure} {
		for _, page := range closure.input.Pages {
			observed := page.input.Response.ObservedUnixNano()
			if observed < input.StartedUnixNano || observed > input.CompletedUnixNano {
				return FinalRevalidationV1{}, errors.New("final revalidation response falls outside the controller-ordered interval")
			}
		}
	}
	if err := EvaluateMergePolicyV1(input.MergeInput.authority, input.PullRequest, input.Checks, input.CheckRunsClosure, input.CommitStatusesClosure, limits); err != nil {
		return FinalRevalidationV1{}, err
	}
	sort.Slice(input.Checks, func(i, j int) bool { return checkKey(input.Checks[i]) < checkKey(input.Checks[j]) })
	if !input.Capability.valid() || input.Capability.SHA256() != input.MergeInput.capability.SHA256() ||
		!input.Recipe.valid() || input.Recipe.SHA256() != input.MergeInput.recipe.SHA256() || !input.Counters.valid() ||
		!input.NoTargetRequestAttempted || len(input.EvidenceRefs) == 0 || canonicalizeEvidence(&input.EvidenceRefs, limits) != nil {
		return FinalRevalidationV1{}, errors.New("final revalidation does not bind the unchanged authorized attempt")
	}
	requiredEvidence := append([]ledger.EvidenceRef(nil), input.CurrentReadyProof.input.EvidenceRefs...)
	requiredEvidence = append(requiredEvidence, input.PullRequest.input.EvidenceRefs...)
	requiredEvidence = append(requiredEvidence, input.PullRequest.input.ReviewsClosure.input.EvidenceRefs...)
	requiredEvidence = append(requiredEvidence, input.CheckRunsClosure.input.EvidenceRefs...)
	requiredEvidence = append(requiredEvidence, input.CommitStatusesClosure.input.EvidenceRefs...)
	for _, evidence := range requiredEvidence {
		if !containsEvidence(input.EvidenceRefs, evidence) {
			return FinalRevalidationV1{}, errors.New("final revalidation evidence closure omits an authority-bearing response")
		}
	}
	if err := requireFreshFinalRequests(input); err != nil {
		return FinalRevalidationV1{}, err
	}
	decision, err := finalDecisionDigest(input, limitsSHA)
	if err != nil {
		return FinalRevalidationV1{}, err
	}
	wire := finalRevalidationWire(input, decision, limitsSHA)
	canonical, digest, err := canonicalJSON(wire)
	if err != nil {
		return FinalRevalidationV1{}, err
	}
	return FinalRevalidationV1{input, decision, canonical, digest, limitsSHA}, nil
}

func finalDecisionDigest(input FinalRevalidationV1Input, limitsSHA string) (string, error) {
	authoritySHA, err := input.MergeInput.authority.SHA256()
	if err != nil {
		return "", err
	}
	decision := struct {
		Schema                   string                  `json:"schema"`
		Verdict                  string                  `json:"verdict"`
		PREligible               bool                    `json:"pr_eligible"`
		MergeInputSHA256         string                  `json:"merge_input_sha256"`
		AuthoritySHA256          string                  `json:"authority_sha256"`
		ReadyProofSHA256         string                  `json:"current_ready_proof_sha256"`
		PullRequestSHA256        string                  `json:"pull_request_sha256"`
		Checks                   []checkWire             `json:"checks"`
		CheckRunsSHA256          string                  `json:"check_runs_closure_sha256"`
		CommitStatusesSHA256     string                  `json:"commit_statuses_closure_sha256"`
		PolicySHA256             string                  `json:"policy_sha256"`
		CapabilitySHA256         string                  `json:"capability_sha256"`
		RecipeSHA256             string                  `json:"recipe_sha256"`
		ControllerSequence       int64                   `json:"controller_sequence"`
		CompletedUnixNano        int64                   `json:"completed_unix_nano"`
		Counters                 AuthorizationCountersV1 `json:"counters"`
		NoTargetRequestAttempted bool                    `json:"no_target_request_attempted"`
		LimitsSHA256             string                  `json:"limits_sha256"`
	}{"final-authorization-decision-v1", "authorized", true, input.MergeInput.SHA256(), authoritySHA,
		input.CurrentReadyProof.SHA256(), input.PullRequest.SHA256(), checkWires(input.Checks), input.CheckRunsClosure.SHA256(),
		input.CommitStatusesClosure.SHA256(), input.MergeInput.authority.MergePolicy().SHA256(), input.Capability.SHA256(),
		input.Recipe.SHA256(), input.ControllerSequence, input.CompletedUnixNano, input.Counters, input.NoTargetRequestAttempted, limitsSHA}
	_, digest, err := canonicalJSON(decision)
	return digest, err
}

func finalRevalidationWire(input FinalRevalidationV1Input, decision, limitsSHA string) finalRevalidationWireV1 {
	return finalRevalidationWireV1{
		FinalRevalidationSchemaV1, input.MergeInput.SHA256(), input.ControllerSequence, input.StartedUnixNano, input.CompletedUnixNano,
		input.CurrentReadyProof.CanonicalJSON(), input.CurrentReadyProof.SHA256(), input.PullRequest.CanonicalJSON(), input.PullRequest.SHA256(),
		checkWires(input.Checks), input.CheckRunsClosure.CanonicalJSON(), input.CheckRunsClosure.SHA256(),
		input.CommitStatusesClosure.CanonicalJSON(), input.CommitStatusesClosure.SHA256(), input.Capability.CanonicalJSON(), input.Capability.SHA256(),
		input.Recipe.CanonicalJSON(), input.Recipe.SHA256(), input.Counters, input.NoTargetRequestAttempted, "authorized", true, decision,
		input.EvidenceRefs, limitsSHA,
	}
}

func requireFreshFinalRequests(input FinalRevalidationV1Input) error {
	initial := map[string]struct{}{}
	if err := addUniqueRequestIDs(initial, input.MergeInput.initialPullRequest, input.MergeInput.checkRunsClosure, input.MergeInput.commitStatusesClosure); err != nil {
		return errors.New("admission request identities are ambiguous or duplicated")
	}
	final := map[string]struct{}{}
	for _, requestID := range requestIDs(input.PullRequest, input.CheckRunsClosure, input.CommitStatusesClosure) {
		if _, exists := initial[requestID]; exists {
			return errors.New("final revalidation reused an admission request identity")
		}
		if _, exists := final[requestID]; exists {
			return errors.New("final revalidation request identities are ambiguous or duplicated")
		}
		final[requestID] = struct{}{}
	}
	return nil
}

func latestObservationUnixNano(pr AuthoritativePullRequestSnapshotV1, closures ...PaginationClosureV1) int64 {
	latest := pr.input.Snapshot.ObservedUnixNano()
	closures = append([]PaginationClosureV1{pr.input.ReviewsClosure}, closures...)
	for _, closure := range closures {
		for _, page := range closure.input.Pages {
			if observed := page.input.Response.ObservedUnixNano(); observed > latest {
				latest = observed
			}
		}
	}
	return latest
}

func requestIDs(pr AuthoritativePullRequestSnapshotV1, closures ...PaginationClosureV1) []string {
	ids := []string{pr.input.Snapshot.RequestID()}
	closures = append([]PaginationClosureV1{pr.input.ReviewsClosure}, closures...)
	for _, closure := range closures {
		for _, page := range closure.input.Pages {
			ids = append(ids, page.input.Response.RequestID())
		}
	}
	return ids
}

func addUniqueRequestIDs(target map[string]struct{}, pr AuthoritativePullRequestSnapshotV1, closures ...PaginationClosureV1) error {
	for _, id := range requestIDs(pr, closures...) {
		if _, exists := target[id]; exists {
			return errors.New("provider request identity is duplicated across authoritative observations")
		}
		target[id] = struct{}{}
	}
	return nil
}

func (r FinalRevalidationV1) Input() FinalRevalidationV1Input {
	return cloneFinalRevalidationInput(r.input)
}
func (r FinalRevalidationV1) FinalDecisionSHA256() string { return r.decisionSHA256 }
func (r FinalRevalidationV1) CanonicalJSON() []byte       { return append([]byte(nil), r.canonical...) }
func (r FinalRevalidationV1) SHA256() string              { return r.digest }
func (r FinalRevalidationV1) valid() bool {
	return len(r.canonical) > 0 && validSHA256(r.decisionSHA256) && validSHA256(r.digest) && digestBytes(r.canonical) == r.digest && validSHA256(r.limitsSHA)
}

func ParseCanonicalFinalRevalidationV1(data []byte, input MergeInput, limits Limits) (FinalRevalidationV1, error) {
	var wire finalRevalidationWireV1
	if err := strictDecode(data, &wire); err != nil {
		return FinalRevalidationV1{}, err
	}
	if wire.Schema != FinalRevalidationSchemaV1 || wire.Verdict != "authorized" || !wire.PREligible {
		return FinalRevalidationV1{}, errors.New("unsupported or unauthorized final revalidation")
	}
	readyProof, err := ParseCanonicalCurrentReadyProofV1(wire.CurrentReadyProof, limits)
	if err != nil {
		return FinalRevalidationV1{}, err
	}
	pr, err := ParseCanonicalAuthoritativePullRequestSnapshotV1(wire.PullRequest, limits)
	if err != nil {
		return FinalRevalidationV1{}, err
	}
	checks, err := checksFromWire(wire.Checks)
	if err != nil {
		return FinalRevalidationV1{}, err
	}
	checkRuns, err := ParseCanonicalPaginationClosureV1(wire.CheckRunsClosure, limits)
	if err != nil {
		return FinalRevalidationV1{}, err
	}
	statuses, err := ParseCanonicalPaginationClosureV1(wire.CommitStatusesClosure, limits)
	if err != nil {
		return FinalRevalidationV1{}, err
	}
	capability, err := ParseCanonicalProviderCapabilityV1(wire.Capability, limits)
	if err != nil {
		return FinalRevalidationV1{}, err
	}
	recipe, err := ParseCanonicalMergeCommitRecipeV1(wire.Recipe, input.authority, limits)
	if err != nil {
		return FinalRevalidationV1{}, err
	}
	value, err := NewFinalRevalidationV1(FinalRevalidationV1Input{
		input, wire.ControllerSequence, wire.StartedUnixNano, wire.CompletedUnixNano, readyProof, pr, checks, checkRuns, statuses,
		capability, recipe, wire.Counters, wire.NoTargetRequestAttempted, wire.EvidenceRefs,
	}, limits)
	if err != nil {
		return FinalRevalidationV1{}, err
	}
	if wire.MergeInputSHA256 != input.SHA256() || wire.CurrentReadyProofSHA256 != readyProof.SHA256() ||
		wire.PullRequestSHA256 != pr.SHA256() || wire.CheckRunsClosureSHA256 != checkRuns.SHA256() ||
		wire.CommitStatusesClosureSHA256 != statuses.SHA256() || wire.CapabilitySHA256 != capability.SHA256() ||
		wire.RecipeSHA256 != recipe.SHA256() || wire.FinalDecisionSHA256 != value.decisionSHA256 || wire.LimitsSHA256 != value.limitsSHA {
		return FinalRevalidationV1{}, errors.New("final revalidation nested or derived digest disagrees")
	}
	if err := requireCanonical(data, value.canonical); err != nil {
		return FinalRevalidationV1{}, err
	}
	return value, nil
}

func checksFromWire(wire []checkWire) ([]Check, error) {
	checks := make([]Check, len(wire))
	for index, item := range wire {
		head, err := NewGitSHA(item.HeadSHA)
		if err != nil {
			return nil, err
		}
		checks[index] = Check{item.NodeID, item.Name, item.Identity, item.Status, item.Conclusion, head, append([]ledger.EvidenceRef(nil), item.EvidenceRefs...)}
	}
	return checks, nil
}

func cloneCurrentReadyProof(p CurrentReadyProofV1) CurrentReadyProofV1 {
	p.input = p.Input()
	p.canonical = append([]byte(nil), p.canonical...)
	return p
}

func cloneFinalRevalidationInput(input FinalRevalidationV1Input) FinalRevalidationV1Input {
	input.MergeInput = cloneLifecycleMergeInput(input.MergeInput)
	input.CurrentReadyProof = cloneCurrentReadyProof(input.CurrentReadyProof)
	input.PullRequest = cloneAuthoritativePR(input.PullRequest)
	input.Checks = cloneChecks(input.Checks)
	input.CheckRunsClosure = clonePaginationClosure(input.CheckRunsClosure)
	input.CommitStatusesClosure = clonePaginationClosure(input.CommitStatusesClosure)
	input.Capability = cloneCapability(input.Capability)
	input.Recipe = cloneRecipe(input.Recipe)
	input.EvidenceRefs = append([]ledger.EvidenceRef(nil), input.EvidenceRefs...)
	return input
}

func cloneFinalRevalidation(r FinalRevalidationV1) FinalRevalidationV1 {
	r.input = cloneFinalRevalidationInput(r.input)
	r.canonical = append([]byte(nil), r.canonical...)
	return r
}

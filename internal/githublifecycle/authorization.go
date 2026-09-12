package githublifecycle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

const (
	RepositoryBindingSchemaV1     = "repository-binding-v1"
	ReadyAuthorityBindingSchemaV1 = "ready-authority-binding-v1"
	MergePolicySchemaV1           = "merge-policy-v1"
	PolicyAuthoritySchemaV1       = "merge-policy-authority-v1"
	ReadyEventControllerActorV1   = "controller"
	ReadyEventControllerSourceV1  = "integration-gate"
)

type RepositoryBindingV1Input struct {
	Phase3RepositoryIdentity   string
	Phase3RepositoryPath       string
	Phase3CanonicalRemote      string
	Phase3StartSHA             GitSHA
	GitHubRepository           Repository
	GitHubRepositoryNodeID     string
	GitHubRepositoryDatabaseID int64
	ConfigurationEvidence      ledger.EvidenceRef
}

type RepositoryBindingV1 struct {
	input     RepositoryBindingV1Input
	canonical []byte
	digest    string
}

func NewRepositoryBindingV1(input RepositoryBindingV1Input) (RepositoryBindingV1, error) {
	if !validText(input.Phase3RepositoryIdentity, 4096, false) || !validText(input.Phase3RepositoryPath, 4096, false) ||
		!input.Phase3StartSHA.valid() || !input.GitHubRepository.valid() || !validOpaqueID(input.GitHubRepositoryNodeID, 256) ||
		input.GitHubRepositoryDatabaseID <= 0 || !validEvidenceRef(input.ConfigurationEvidence) {
		return RepositoryBindingV1{}, errors.New("repository binding contains an invalid stable identity")
	}
	remote, err := url.Parse(input.Phase3CanonicalRemote)
	if err != nil || remote.Scheme != "https" || remote.Host != "github.com" || remote.User != nil || remote.RawQuery != "" || remote.Fragment != "" {
		return RepositoryBindingV1{}, errors.New("repository binding requires a canonical HTTPS GitHub remote")
	}
	path := strings.TrimSuffix(remote.EscapedPath(), ".git")
	if path != "/"+input.GitHubRepository.Owner()+"/"+input.GitHubRepository.Name() {
		return RepositoryBindingV1{}, errors.New("canonical remote does not match the exact GitHub repository")
	}
	canonical, digest, err := canonicalJSON(repositoryBindingWire(input))
	if err != nil {
		return RepositoryBindingV1{}, err
	}
	return RepositoryBindingV1{input, canonical, digest}, nil
}

func (r RepositoryBindingV1) Input() RepositoryBindingV1Input { return r.input }
func (r RepositoryBindingV1) CanonicalJSON() []byte           { return append([]byte(nil), r.canonical...) }
func (r RepositoryBindingV1) SHA256() string                  { return r.digest }
func (r RepositoryBindingV1) MarshalJSON() ([]byte, error) {
	if !r.valid() {
		return nil, errors.New("repository binding is incomplete")
	}
	return r.CanonicalJSON(), nil
}
func (r RepositoryBindingV1) valid() bool {
	rebuilt, err := NewRepositoryBindingV1(r.input)
	return err == nil && rebuilt.digest == r.digest && bytes.Equal(rebuilt.canonical, r.canonical)
}

type repositoryBindingWireV1 struct {
	Schema                     string             `json:"schema"`
	Phase3RepositoryIdentity   string             `json:"phase3_repository_identity"`
	Phase3RepositoryPath       string             `json:"phase3_repository_path"`
	Phase3CanonicalRemote      string             `json:"phase3_canonical_remote"`
	Phase3StartSHA             string             `json:"phase3_start_sha"`
	GitHubRepository           repoWire           `json:"github_repository"`
	GitHubRepositoryNodeID     string             `json:"github_repository_node_id"`
	GitHubRepositoryDatabaseID int64              `json:"github_repository_database_id"`
	ConfigurationEvidence      ledger.EvidenceRef `json:"configuration_evidence"`
}

func repositoryBindingWire(i RepositoryBindingV1Input) repositoryBindingWireV1 {
	return repositoryBindingWireV1{RepositoryBindingSchemaV1, i.Phase3RepositoryIdentity, i.Phase3RepositoryPath,
		i.Phase3CanonicalRemote, i.Phase3StartSHA.String(), repositoryWire(i.GitHubRepository),
		i.GitHubRepositoryNodeID, i.GitHubRepositoryDatabaseID, i.ConfigurationEvidence}
}

func ParseCanonicalRepositoryBindingV1(data []byte) (RepositoryBindingV1, error) {
	var wire repositoryBindingWireV1
	if err := strictDecode(data, &wire); err != nil {
		return RepositoryBindingV1{}, err
	}
	if wire.Schema != RepositoryBindingSchemaV1 {
		return RepositoryBindingV1{}, errors.New("unsupported repository binding schema")
	}
	repository, err := NewRepository(wire.GitHubRepository.Owner, wire.GitHubRepository.Name)
	if err != nil {
		return RepositoryBindingV1{}, err
	}
	start, err := NewGitSHA(wire.Phase3StartSHA)
	if err != nil {
		return RepositoryBindingV1{}, err
	}
	value, err := NewRepositoryBindingV1(RepositoryBindingV1Input{wire.Phase3RepositoryIdentity, wire.Phase3RepositoryPath,
		wire.Phase3CanonicalRemote, start, repository, wire.GitHubRepositoryNodeID, wire.GitHubRepositoryDatabaseID, wire.ConfigurationEvidence})
	if err != nil {
		return RepositoryBindingV1{}, err
	}
	if err := requireCanonical(data, value.canonical); err != nil {
		return RepositoryBindingV1{}, err
	}
	return value, nil
}

type AcceptedSourceCandidateV1 struct {
	ProjectID                string               `json:"project_id"`
	PlanID                   string               `json:"plan_id"`
	RunID                    string               `json:"run_id"`
	AttemptID                string               `json:"attempt_id"`
	RepositoryIdentity       string               `json:"repository_identity"`
	Branch                   string               `json:"branch"`
	StartSHA                 string               `json:"start_sha"`
	AcceptedHeadSHA          string               `json:"accepted_head_sha"`
	AcceptancePolicyIdentity string               `json:"acceptance_policy_identity"`
	AcceptanceEvidence       []ledger.EvidenceRef `json:"acceptance_evidence"`
}

type ReadyAuthorityBindingV1Input struct {
	Phase3AuthorityJSON    []byte
	Phase3AuthoritySHA256  string
	ProjectID              string
	PlanID                 string
	RunID                  string
	AttemptID              string
	AcceptedSources        []AcceptedSourceCandidateV1
	RepositoryBinding      RepositoryBindingV1
	ReadyEventJSON         []byte
	ReadyEventSHA256       string
	ReadyEventID           string
	ReadyEventUnixNano     int64
	LedgerIdentity         string
	ReadyEventByteOffset   int64
	ReadyRunStateSequence  int64
	LedgerPrefixLength     int64
	LedgerPrefixSHA256     string
	ReadyTransitionOrdinal int64
	ReadyEvidenceRefs      []ledger.EvidenceRef
	ReadyDecisionRef       ledger.EvidenceRef
	EvidenceClosureRefs    []ledger.EvidenceRef
	IntegratedHeadSHA      GitSHA
	BaselineSHA            GitSHA
	ExpectedTreeSHA        GitSHA
}

type ReadyAuthorityBindingV1 struct {
	input     ReadyAuthorityBindingV1Input
	canonical []byte
	digest    string
}

func NewReadyAuthorityBindingV1(input ReadyAuthorityBindingV1Input, limits Limits) (ReadyAuthorityBindingV1, error) {
	input = cloneReadyBindingInput(input)
	if err := limits.Validate(); err != nil {
		return ReadyAuthorityBindingV1{}, err
	}
	if !validText(input.ProjectID, limits.MaxTextBytes, false) || !validText(input.PlanID, limits.MaxTextBytes, false) ||
		!validText(input.RunID, limits.MaxTextBytes, false) || !validText(input.AttemptID, limits.MaxTextBytes, false) ||
		!input.RepositoryBinding.valid() || !input.IntegratedHeadSHA.valid() || !input.BaselineSHA.valid() || !input.ExpectedTreeSHA.valid() ||
		!validOpaqueID(input.ReadyEventID, limits.MaxTextBytes) || input.ReadyEventUnixNano <= 0 ||
		!validText(input.LedgerIdentity, limits.MaxTextBytes, false) || input.ReadyEventByteOffset < 0 || input.ReadyRunStateSequence <= 0 ||
		input.LedgerPrefixLength <= 0 || input.ReadyTransitionOrdinal <= 0 || !validSHA256(input.LedgerPrefixSHA256) {
		return ReadyAuthorityBindingV1{}, errors.New("READY authority binding contains an invalid identity")
	}
	if len(input.Phase3AuthorityJSON) == 0 || len(input.Phase3AuthorityJSON) > limits.MaxCanonicalObjectBytes ||
		digestBytes(input.Phase3AuthorityJSON) != input.Phase3AuthoritySHA256 {
		return ReadyAuthorityBindingV1{}, errors.New("Phase-3 authority bytes and digest disagree")
	}
	var generic any
	if err := strictDecode(input.Phase3AuthorityJSON, &generic); err != nil {
		return ReadyAuthorityBindingV1{}, fmt.Errorf("Phase-3 authority: %w", err)
	}
	reencoded, err := json.Marshal(generic)
	if err != nil || !bytes.Equal(reencoded, input.Phase3AuthorityJSON) {
		return ReadyAuthorityBindingV1{}, errors.New("Phase-3 authority is not strict canonical JSON")
	}
	var phase3 struct {
		RunID      string `json:"run_id"`
		Repository struct {
			Path     string            `json:"path"`
			Identity string            `json:"identity"`
			Remotes  map[string]string `json:"remotes"`
			StartSHA string            `json:"start_sha"`
		} `json:"repository"`
		Plan struct {
			Path   string `json:"path"`
			SHA256 string `json:"sha256"`
		} `json:"plan"`
		PolicyVersion string `json:"policy_version"`
	}
	if err := json.Unmarshal(input.Phase3AuthorityJSON, &phase3); err != nil || phase3.RunID != input.RunID ||
		phase3.Repository.Identity != input.RepositoryBinding.input.Phase3RepositoryIdentity || phase3.Repository.Path != input.RepositoryBinding.input.Phase3RepositoryPath ||
		phase3.Repository.StartSHA != input.RepositoryBinding.input.Phase3StartSHA.String() || phase3.Repository.Remotes["origin"] != input.RepositoryBinding.input.Phase3CanonicalRemote ||
		!validText(phase3.Plan.Path, limits.MaxTextBytes, false) || !validSHA256(phase3.Plan.SHA256) || !validText(phase3.PolicyVersion, limits.MaxTextBytes, false) {
		return ReadyAuthorityBindingV1{}, errors.New("Phase-3 authority identities do not match the READY repository/run binding")
	}
	if len(input.ReadyEventJSON) == 0 || len(input.ReadyEventJSON) > limits.MaxLedgerLineBytes ||
		input.LedgerPrefixLength > int64(limits.MaxReadyLedgerSnapshotBytes) {
		return ReadyAuthorityBindingV1{}, errors.New("READY event or bound ledger prefix exceeds its stage-specific limit")
	}
	var readyEvent ledger.Event
	if err := strictDecode(input.ReadyEventJSON, &readyEvent); err != nil || readyEvent.Validate() != nil {
		return ReadyAuthorityBindingV1{}, errors.New("READY event is not valid canonical ledger evidence")
	}
	eventCanonical, _ := json.Marshal(readyEvent)
	if !bytes.Equal(eventCanonical, input.ReadyEventJSON) || digestBytes(input.ReadyEventJSON) != input.ReadyEventSHA256 ||
		readyEvent.EventID != input.ReadyEventID || readyEvent.Timestamp.UnixNano() != input.ReadyEventUnixNano || readyEvent.ProjectID != input.ProjectID ||
		readyEvent.PlanID != input.PlanID || readyEvent.RunID != input.RunID || readyEvent.AttemptID != input.AttemptID ||
		readyEvent.EventType != "STATE_TRANSITION" || readyEvent.StateFrom != domain.StateIntegrationAccepted || readyEvent.StateTo != domain.StateReadyForMerge ||
		readyEvent.Actor != ReadyEventControllerActorV1 || readyEvent.Source != ReadyEventControllerSourceV1 {
		return ReadyAuthorityBindingV1{}, errors.New("READY event does not prove the exact INTEGRATION_ACCEPTED to READY_FOR_MERGE transition")
	}
	if input.ReadyEventByteOffset+int64(len(input.ReadyEventJSON))+1 != input.LedgerPrefixLength ||
		input.ReadyRunStateSequence != input.ReadyTransitionOrdinal {
		return ReadyAuthorityBindingV1{}, errors.New("READY ledger offset, sequence, ordinal, and prefix are incoherent")
	}
	if len(input.AcceptedSources) == 0 || len(input.AcceptedSources) > limits.MaxReadyAcceptedSources {
		return ReadyAuthorityBindingV1{}, errors.New("accepted source closure is empty or excessive")
	}
	seenSources := map[string]struct{}{}
	integratedHeadSource := false
	for index := range input.AcceptedSources {
		source := &input.AcceptedSources[index]
		if !validText(source.ProjectID, limits.MaxTextBytes, false) || !validText(source.PlanID, limits.MaxTextBytes, false) || !validText(source.RunID, limits.MaxTextBytes, false) ||
			!validText(source.AttemptID, limits.MaxTextBytes, false) || !validText(source.RepositoryIdentity, limits.MaxTextBytes, false) ||
			!validBranchName(source.Branch) || !validText(source.AcceptancePolicyIdentity, limits.MaxTextBytes, false) {
			return ReadyAuthorityBindingV1{}, fmt.Errorf("accepted source %d is incomplete", index)
		}
		if _, err := NewGitSHA(source.StartSHA); err != nil {
			return ReadyAuthorityBindingV1{}, fmt.Errorf("accepted source %d start SHA: %w", index, err)
		}
		if _, err := NewGitSHA(source.AcceptedHeadSHA); err != nil {
			return ReadyAuthorityBindingV1{}, fmt.Errorf("accepted source %d head SHA: %w", index, err)
		}
		if len(source.AcceptanceEvidence) == 0 || canonicalizeEvidence(&source.AcceptanceEvidence, limits) != nil {
			return ReadyAuthorityBindingV1{}, fmt.Errorf("accepted source %d evidence is invalid", index)
		}
		key := source.ProjectID + "\x00" + source.PlanID + "\x00" + source.RunID + "\x00" + source.AttemptID
		if _, ok := seenSources[key]; ok {
			return ReadyAuthorityBindingV1{}, errors.New("accepted source identity is duplicated")
		}
		seenSources[key] = struct{}{}
		if source.RepositoryIdentity == input.RepositoryBinding.input.Phase3RepositoryIdentity &&
			source.AcceptedHeadSHA == input.IntegratedHeadSHA.String() {
			integratedHeadSource = true
		}
	}
	if !integratedHeadSource {
		return ReadyAuthorityBindingV1{}, errors.New("accepted source closure omits the integrated head")
	}
	sort.Slice(input.AcceptedSources, func(i, j int) bool {
		return acceptedSourceKey(input.AcceptedSources[i]) < acceptedSourceKey(input.AcceptedSources[j])
	})
	if len(input.ReadyEvidenceRefs) == 0 || canonicalizeEvidence(&input.ReadyEvidenceRefs, limits) != nil || !containsEvidence(input.ReadyEvidenceRefs, input.ReadyDecisionRef) ||
		!equalEvidence(input.ReadyEvidenceRefs, readyEvent.EvidenceRefs) {
		return ReadyAuthorityBindingV1{}, errors.New("READY event evidence and decision reference do not agree")
	}
	if len(input.EvidenceClosureRefs) == 0 || canonicalizeReadyEvidenceClosure(&input.EvidenceClosureRefs, limits) != nil || !containsEvidence(input.EvidenceClosureRefs, input.ReadyDecisionRef) {
		return ReadyAuthorityBindingV1{}, errors.New("READY evidence closure is incomplete")
	}
	requiredClosure := append([]ledger.EvidenceRef(nil), input.ReadyEvidenceRefs...)
	requiredClosure = append(requiredClosure, input.RepositoryBinding.input.ConfigurationEvidence)
	for _, source := range input.AcceptedSources {
		requiredClosure = append(requiredClosure, source.AcceptanceEvidence...)
	}
	for _, evidence := range requiredClosure {
		if !containsEvidence(input.EvidenceClosureRefs, evidence) {
			return ReadyAuthorityBindingV1{}, errors.New("READY evidence closure omits controller or accepted-source evidence")
		}
	}
	canonical, digest, err := canonicalJSON(readyBindingWire(input))
	if err != nil {
		return ReadyAuthorityBindingV1{}, err
	}
	return ReadyAuthorityBindingV1{input, canonical, digest}, nil
}

func (r ReadyAuthorityBindingV1) Input() ReadyAuthorityBindingV1Input {
	return cloneReadyBindingInput(r.input)
}
func (r ReadyAuthorityBindingV1) CanonicalJSON() []byte { return append([]byte(nil), r.canonical...) }
func (r ReadyAuthorityBindingV1) SHA256() string        { return r.digest }
func (r ReadyAuthorityBindingV1) RepositoryBinding() RepositoryBindingV1 {
	return r.input.RepositoryBinding
}
func (r ReadyAuthorityBindingV1) MarshalJSON() ([]byte, error) {
	if !r.valid() {
		return nil, errors.New("READY binding is incomplete")
	}
	return r.CanonicalJSON(), nil
}
func (r ReadyAuthorityBindingV1) valid() bool {
	return len(r.canonical) > 0 && validSHA256(r.digest) && digestBytes(r.canonical) == r.digest && r.input.RepositoryBinding.valid()
}

type readyBindingWireV1 struct {
	Schema                 string                      `json:"schema"`
	Phase3Authority        json.RawMessage             `json:"phase3_authority"`
	Phase3AuthoritySHA256  string                      `json:"phase3_authority_sha256"`
	ProjectID              string                      `json:"project_id"`
	PlanID                 string                      `json:"plan_id"`
	RunID                  string                      `json:"run_id"`
	AttemptID              string                      `json:"attempt_id"`
	AcceptedSources        []AcceptedSourceCandidateV1 `json:"accepted_sources"`
	RepositoryBinding      json.RawMessage             `json:"repository_binding"`
	ReadyEvent             json.RawMessage             `json:"ready_event"`
	ReadyEventSHA256       string                      `json:"ready_event_sha256"`
	ReadyEventID           string                      `json:"ready_event_id"`
	ReadyEventUnixNano     int64                       `json:"ready_event_unix_nano"`
	LedgerIdentity         string                      `json:"ledger_identity"`
	ReadyEventByteOffset   int64                       `json:"ready_event_byte_offset"`
	ReadyRunStateSequence  int64                       `json:"ready_run_state_sequence"`
	LedgerPrefixLength     int64                       `json:"ledger_prefix_length"`
	LedgerPrefixSHA256     string                      `json:"ledger_prefix_sha256"`
	ReadyTransitionOrdinal int64                       `json:"ready_transition_ordinal"`
	ReadyEvidenceRefs      []ledger.EvidenceRef        `json:"ready_evidence_refs"`
	ReadyDecisionRef       ledger.EvidenceRef          `json:"ready_decision_ref"`
	EvidenceClosureRefs    []ledger.EvidenceRef        `json:"evidence_closure_refs"`
	EvidenceClosureSHA256  string                      `json:"evidence_closure_sha256"`
	IntegratedHeadSHA      string                      `json:"integrated_head_sha"`
	BaselineSHA            string                      `json:"baseline_sha"`
	ExpectedTreeSHA        string                      `json:"expected_tree_sha"`
}

func readyBindingWire(i ReadyAuthorityBindingV1Input) readyBindingWireV1 {
	closure, _, _ := canonicalJSON(i.EvidenceClosureRefs)
	return readyBindingWireV1{ReadyAuthorityBindingSchemaV1, i.Phase3AuthorityJSON, i.Phase3AuthoritySHA256, i.ProjectID, i.PlanID, i.RunID, i.AttemptID,
		i.AcceptedSources, i.RepositoryBinding.CanonicalJSON(), i.ReadyEventJSON, i.ReadyEventSHA256, i.ReadyEventID, i.ReadyEventUnixNano,
		i.LedgerIdentity, i.ReadyEventByteOffset, i.ReadyRunStateSequence, i.LedgerPrefixLength, i.LedgerPrefixSHA256, i.ReadyTransitionOrdinal,
		i.ReadyEvidenceRefs, i.ReadyDecisionRef, i.EvidenceClosureRefs, digestBytes(closure), i.IntegratedHeadSHA.String(), i.BaselineSHA.String(), i.ExpectedTreeSHA.String()}
}

func ParseCanonicalReadyAuthorityBindingV1(data []byte, limits Limits) (ReadyAuthorityBindingV1, error) {
	var wire readyBindingWireV1
	if err := strictDecode(data, &wire); err != nil {
		return ReadyAuthorityBindingV1{}, err
	}
	if wire.Schema != ReadyAuthorityBindingSchemaV1 {
		return ReadyAuthorityBindingV1{}, errors.New("unsupported READY binding schema")
	}
	repository, err := ParseCanonicalRepositoryBindingV1(wire.RepositoryBinding)
	if err != nil {
		return ReadyAuthorityBindingV1{}, err
	}
	head, err := NewGitSHA(wire.IntegratedHeadSHA)
	if err != nil {
		return ReadyAuthorityBindingV1{}, err
	}
	base, err := NewGitSHA(wire.BaselineSHA)
	if err != nil {
		return ReadyAuthorityBindingV1{}, err
	}
	tree, err := NewGitSHA(wire.ExpectedTreeSHA)
	if err != nil {
		return ReadyAuthorityBindingV1{}, err
	}
	input := ReadyAuthorityBindingV1Input{wire.Phase3Authority, wire.Phase3AuthoritySHA256, wire.ProjectID, wire.PlanID, wire.RunID, wire.AttemptID,
		wire.AcceptedSources, repository, wire.ReadyEvent, wire.ReadyEventSHA256, wire.ReadyEventID, wire.ReadyEventUnixNano, wire.LedgerIdentity,
		wire.ReadyEventByteOffset, wire.ReadyRunStateSequence, wire.LedgerPrefixLength, wire.LedgerPrefixSHA256, wire.ReadyTransitionOrdinal,
		wire.ReadyEvidenceRefs, wire.ReadyDecisionRef, wire.EvidenceClosureRefs, head, base, tree}
	value, err := NewReadyAuthorityBindingV1(input, limits)
	if err != nil {
		return ReadyAuthorityBindingV1{}, err
	}
	if readyBindingWire(value.input).EvidenceClosureSHA256 != wire.EvidenceClosureSHA256 {
		return ReadyAuthorityBindingV1{}, errors.New("READY evidence closure digest disagrees")
	}
	if err := requireCanonical(data, value.canonical); err != nil {
		return ReadyAuthorityBindingV1{}, err
	}
	return value, nil
}

type StableIdentityV1 struct {
	DatabaseID int64  `json:"database_id"`
	NodeID     string `json:"node_id"`
}

func (i StableIdentityV1) valid() bool { return i.DatabaseID > 0 && validOpaqueID(i.NodeID, 256) }

type CheckSourceKind string

const (
	CheckSourceCheckRun     CheckSourceKind = "check_run"
	CheckSourceCommitStatus CheckSourceKind = "commit_status"
)

type TrustedCheckIdentityV1 struct {
	Context  string            `json:"context"`
	Source   CheckSourceKind   `json:"source"`
	Producer StableIdentityV1  `json:"producer"`
	App      *StableIdentityV1 `json:"app,omitempty"`
}

func (i TrustedCheckIdentityV1) valid(l Limits) bool {
	if !validText(i.Context, l.MaxTextBytes, false) || (i.Source != CheckSourceCheckRun && i.Source != CheckSourceCommitStatus) || !i.Producer.valid() {
		return false
	}
	return i.App == nil || i.App.valid()
}
func checkIdentityKey(i TrustedCheckIdentityV1) string {
	app := ""
	if i.App != nil {
		app = fmt.Sprintf("%020d/%s", i.App.DatabaseID, i.App.NodeID)
	}
	return string(i.Source) + "\x00" + i.Context + "\x00" + fmt.Sprintf("%020d/%s", i.Producer.DatabaseID, i.Producer.NodeID) + "\x00" + app
}

type MergeCommitIdentityV1 struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Timezone string `json:"timezone"`
}

func (i MergeCommitIdentityV1) valid(l Limits) bool {
	return validText(i.Name, l.MaxTextBytes, false) && validText(i.Email, l.MaxTextBytes, false) && strings.Contains(i.Email, "@") && len(i.Timezone) == 5 && (i.Timezone[0] == '+' || i.Timezone[0] == '-')
}

type MergeCommitRecipePolicyV1 struct {
	MessageTemplate     string                `json:"message_template"`
	TrailerTemplate     string                `json:"trailer_template"`
	Author              MergeCommitIdentityV1 `json:"author"`
	Committer           MergeCommitIdentityV1 `json:"committer"`
	TimestampDerivation string                `json:"timestamp_derivation"`
	ObjectFormat        string                `json:"object_format"`
	OrderedParents      bool                  `json:"ordered_parents"`
}

func (p MergeCommitRecipePolicyV1) valid(l Limits) bool {
	return validCommitMessage(p.MessageTemplate, l.MaxTextBytes) && !strings.Contains(p.MessageTemplate, "\n\n") && !strings.HasSuffix(p.MessageTemplate, "\n") &&
		validTrailerName(p.TrailerTemplate, l.MaxTextBytes) && p.Author.valid(l) && p.Committer.valid(l) &&
		p.TimestampDerivation == "ready-event-time" && (p.ObjectFormat == "sha1" || p.ObjectFormat == "sha256") && p.OrderedParents
}

func validTrailerName(value string, max int) bool {
	if !validText(value, max, false) || strings.ContainsAny(value, ":\r\n\x00") {
		return false
	}
	for _, r := range value {
		if !(r == '-' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
			return false
		}
	}
	return true
}

type PolicyAuthorityBindingV1Input struct {
	SourceConfiguration     ledger.EvidenceRef
	RepositoryBindingSHA256 string
	Phase3AuthoritySHA256   string
	ReadyBindingSHA256      string
	RequiredActingPrincipal ActingIdentity
}
type PolicyAuthorityBindingV1 struct {
	input     PolicyAuthorityBindingV1Input
	canonical []byte
	digest    string
}

func NewPolicyAuthorityBindingV1(i PolicyAuthorityBindingV1Input) (PolicyAuthorityBindingV1, error) {
	if !validEvidenceRef(i.SourceConfiguration) || !validSHA256(i.RepositoryBindingSHA256) || !validSHA256(i.Phase3AuthoritySHA256) || !validSHA256(i.ReadyBindingSHA256) || !i.RequiredActingPrincipal.valid() {
		return PolicyAuthorityBindingV1{}, errors.New("policy authority binding is invalid")
	}
	wire := struct {
		Schema     string             `json:"schema"`
		Source     ledger.EvidenceRef `json:"source_configuration"`
		Repository string             `json:"repository_binding_sha256"`
		Phase3     string             `json:"phase3_authority_sha256"`
		Ready      string             `json:"ready_binding_sha256"`
		Principal  actorWire          `json:"required_acting_principal"`
	}{PolicyAuthoritySchemaV1, i.SourceConfiguration, i.RepositoryBindingSHA256, i.Phase3AuthoritySHA256, i.ReadyBindingSHA256, actingWire(i.RequiredActingPrincipal)}
	canonical, digest, err := canonicalJSON(wire)
	if err != nil {
		return PolicyAuthorityBindingV1{}, err
	}
	return PolicyAuthorityBindingV1{i, canonical, digest}, nil
}
func (p PolicyAuthorityBindingV1) Input() PolicyAuthorityBindingV1Input { return p.input }
func (p PolicyAuthorityBindingV1) CanonicalJSON() []byte                { return append([]byte(nil), p.canonical...) }
func (p PolicyAuthorityBindingV1) SHA256() string                       { return p.digest }
func (p PolicyAuthorityBindingV1) valid() bool {
	rebuilt, err := NewPolicyAuthorityBindingV1(p.input)
	return err == nil && rebuilt.digest == p.digest && bytes.Equal(rebuilt.canonical, p.canonical)
}
func (p PolicyAuthorityBindingV1) MarshalJSON() ([]byte, error) {
	if !p.valid() {
		return nil, errors.New("policy authority binding incomplete")
	}
	return p.CanonicalJSON(), nil
}

type MergePolicyV1Input struct {
	PolicyVersion     string
	AuthorityBinding  PolicyAuthorityBindingV1
	Method            MergeMethod
	RequiredChecks    []TrustedCheckIdentityV1
	EligibleReviewers []StableIdentityV1
	RequiredReviewers []StableIdentityV1
	MinimumApprovals  int
	Recipe            MergeCommitRecipePolicyV1
}
type MergePolicyV1 struct {
	input     MergePolicyV1Input
	canonical []byte
	digest    string
}

func NewMergePolicyV1(input MergePolicyV1Input, limits Limits) (MergePolicyV1, error) {
	input = cloneMergePolicyInput(input)
	if err := limits.Validate(); err != nil {
		return MergePolicyV1{}, err
	}
	if !validText(input.PolicyVersion, limits.MaxTextBytes, false) || !input.AuthorityBinding.valid() || input.Method != MergeMethodMerge || !input.Recipe.valid(limits) || input.MinimumApprovals < 0 || input.MinimumApprovals > len(input.EligibleReviewers) {
		return MergePolicyV1{}, errors.New("merge policy is invalid")
	}
	checks := map[string]struct{}{}
	for _, v := range input.RequiredChecks {
		if !v.valid(limits) {
			return MergePolicyV1{}, errors.New("required check identity is invalid")
		}
		k := checkIdentityKey(v)
		if _, ok := checks[k]; ok {
			return MergePolicyV1{}, errors.New("required check identity is duplicated")
		}
		checks[k] = struct{}{}
	}
	sort.Slice(input.RequiredChecks, func(i, j int) bool {
		return checkIdentityKey(input.RequiredChecks[i]) < checkIdentityKey(input.RequiredChecks[j])
	})
	if len(input.RequiredChecks) > limits.MaxRequiredTrustedChecks ||
		len(input.EligibleReviewers) > limits.MaxEligibleReviewers ||
		len(input.RequiredReviewers) > limits.MaxRequiredReviewers ||
		input.MinimumApprovals > limits.MaxMinimumApprovals {
		return MergePolicyV1{}, errors.New("merge policy collection exceeds limits")
	}
	eligible := map[string]struct{}{}
	for _, v := range input.EligibleReviewers {
		if !v.valid() {
			return MergePolicyV1{}, errors.New("eligible reviewer is invalid")
		}
		k := stableIdentityKey(v)
		if _, ok := eligible[k]; ok {
			return MergePolicyV1{}, errors.New("eligible reviewer is duplicated")
		}
		eligible[k] = struct{}{}
	}
	required := map[string]struct{}{}
	for _, v := range input.RequiredReviewers {
		if !v.valid() {
			return MergePolicyV1{}, errors.New("required reviewer is invalid")
		}
		k := stableIdentityKey(v)
		if _, ok := eligible[k]; !ok {
			return MergePolicyV1{}, errors.New("required reviewer is not eligible")
		}
		if _, ok := required[k]; ok {
			return MergePolicyV1{}, errors.New("required reviewer is duplicated")
		}
		required[k] = struct{}{}
	}
	sort.Slice(input.EligibleReviewers, func(i, j int) bool {
		return stableIdentityKey(input.EligibleReviewers[i]) < stableIdentityKey(input.EligibleReviewers[j])
	})
	sort.Slice(input.RequiredReviewers, func(i, j int) bool {
		return stableIdentityKey(input.RequiredReviewers[i]) < stableIdentityKey(input.RequiredReviewers[j])
	})
	canonical, digest, err := canonicalJSON(mergePolicyWire(input))
	if err != nil {
		return MergePolicyV1{}, err
	}
	if err := requireCanonicalObjectSize(canonical, limits.MaxCanonicalObjectBytes, "merge policy"); err != nil {
		return MergePolicyV1{}, err
	}
	return MergePolicyV1{input, canonical, digest}, nil
}
func (p MergePolicyV1) Input() MergePolicyV1Input                  { return cloneMergePolicyInput(p.input) }
func (p MergePolicyV1) CanonicalJSON() []byte                      { return append([]byte(nil), p.canonical...) }
func (p MergePolicyV1) SHA256() string                             { return p.digest }
func (p MergePolicyV1) Method() MergeMethod                        { return p.input.Method }
func (p MergePolicyV1) AuthorityBinding() PolicyAuthorityBindingV1 { return p.input.AuthorityBinding }
func (p MergePolicyV1) MarshalJSON() ([]byte, error) {
	if !p.valid() {
		return nil, errors.New("merge policy incomplete")
	}
	return p.CanonicalJSON(), nil
}
func (p MergePolicyV1) valid() bool {
	return len(p.canonical) > 0 && validSHA256(p.digest) && digestBytes(p.canonical) == p.digest && p.input.AuthorityBinding.valid()
}

type mergePolicyWireV1 struct {
	Schema            string                    `json:"schema"`
	PolicyVersion     string                    `json:"policy_version"`
	AuthorityBinding  json.RawMessage           `json:"authority_binding"`
	Method            MergeMethod               `json:"method"`
	RequiredChecks    []TrustedCheckIdentityV1  `json:"required_checks"`
	EligibleReviewers []StableIdentityV1        `json:"eligible_reviewers"`
	RequiredReviewers []StableIdentityV1        `json:"required_reviewers"`
	MinimumApprovals  int                       `json:"minimum_approvals"`
	Recipe            MergeCommitRecipePolicyV1 `json:"merge_commit_recipe_policy"`
}

func mergePolicyWire(i MergePolicyV1Input) mergePolicyWireV1 {
	return mergePolicyWireV1{MergePolicySchemaV1, i.PolicyVersion, i.AuthorityBinding.CanonicalJSON(), i.Method, i.RequiredChecks, i.EligibleReviewers, i.RequiredReviewers, i.MinimumApprovals, i.Recipe}
}

func cloneMergePolicyInput(i MergePolicyV1Input) MergePolicyV1Input {
	i.RequiredChecks = append([]TrustedCheckIdentityV1(nil), i.RequiredChecks...)
	for x := range i.RequiredChecks {
		if i.RequiredChecks[x].App != nil {
			v := *i.RequiredChecks[x].App
			i.RequiredChecks[x].App = &v
		}
	}
	i.EligibleReviewers = append([]StableIdentityV1(nil), i.EligibleReviewers...)
	i.RequiredReviewers = append([]StableIdentityV1(nil), i.RequiredReviewers...)
	return i
}
func stableIdentityKey(i StableIdentityV1) string {
	return fmt.Sprintf("%020d/%s", i.DatabaseID, i.NodeID)
}

func ParseCanonicalMergePolicyV1(data []byte, limits Limits) (MergePolicyV1, error) {
	if err := limits.Validate(); err != nil {
		return MergePolicyV1{}, err
	}
	if err := requireCanonicalObjectSize(data, limits.MaxCanonicalObjectBytes, "merge policy"); err != nil {
		return MergePolicyV1{}, err
	}
	var w mergePolicyWireV1
	if err := strictDecode(data, &w); err != nil {
		return MergePolicyV1{}, err
	}
	if w.Schema != MergePolicySchemaV1 {
		return MergePolicyV1{}, errors.New("unsupported merge policy schema")
	}
	var aw struct {
		Schema     string             `json:"schema"`
		Source     ledger.EvidenceRef `json:"source_configuration"`
		Repository string             `json:"repository_binding_sha256"`
		Phase3     string             `json:"phase3_authority_sha256"`
		Ready      string             `json:"ready_binding_sha256"`
		Principal  actorWire          `json:"required_acting_principal"`
	}
	if err := strictDecode(w.AuthorityBinding, &aw); err != nil {
		return MergePolicyV1{}, err
	}
	if aw.Schema != PolicyAuthoritySchemaV1 {
		return MergePolicyV1{}, errors.New("unsupported policy authority schema")
	}
	actor, err := actorFromWire(aw.Principal)
	if err != nil {
		return MergePolicyV1{}, err
	}
	binding, err := NewPolicyAuthorityBindingV1(PolicyAuthorityBindingV1Input{aw.Source, aw.Repository, aw.Phase3, aw.Ready, actor})
	if err != nil {
		return MergePolicyV1{}, err
	}
	if err := requireCanonical(w.AuthorityBinding, binding.canonical); err != nil {
		return MergePolicyV1{}, err
	}
	value, err := NewMergePolicyV1(MergePolicyV1Input{w.PolicyVersion, binding, w.Method, w.RequiredChecks, w.EligibleReviewers, w.RequiredReviewers, w.MinimumApprovals, w.Recipe}, limits)
	if err != nil {
		return MergePolicyV1{}, err
	}
	if err := requireCanonical(data, value.canonical); err != nil {
		return MergePolicyV1{}, err
	}
	return value, nil
}

func actorFromWire(w actorWire) (ActingIdentity, error) {
	if w.Kind == ActingKindUser {
		if w.InstallationID != 0 {
			return ActingIdentity{}, errors.New("user actor has installation ID")
		}
		return NewUserIdentity(w.Subject)
	}
	if w.Kind == ActingKindAppInstallation {
		return NewAppInstallationIdentity(w.Subject, w.InstallationID)
	}
	return ActingIdentity{}, errors.New("unsupported actor kind")
}

func cloneReadyBindingInput(i ReadyAuthorityBindingV1Input) ReadyAuthorityBindingV1Input {
	i.Phase3AuthorityJSON = append([]byte(nil), i.Phase3AuthorityJSON...)
	i.ReadyEventJSON = append([]byte(nil), i.ReadyEventJSON...)
	i.AcceptedSources = append([]AcceptedSourceCandidateV1(nil), i.AcceptedSources...)
	for x := range i.AcceptedSources {
		i.AcceptedSources[x].AcceptanceEvidence = append([]ledger.EvidenceRef(nil), i.AcceptedSources[x].AcceptanceEvidence...)
	}
	i.ReadyEvidenceRefs = append([]ledger.EvidenceRef(nil), i.ReadyEvidenceRefs...)
	i.EvidenceClosureRefs = append([]ledger.EvidenceRef(nil), i.EvidenceClosureRefs...)
	return i
}
func acceptedSourceKey(s AcceptedSourceCandidateV1) string {
	return s.ProjectID + "\x00" + s.PlanID + "\x00" + s.RunID + "\x00" + s.AttemptID
}
func containsEvidence(refs []ledger.EvidenceRef, want ledger.EvidenceRef) bool {
	for _, r := range refs {
		if r == want {
			return true
		}
	}
	return false
}
func equalEvidence(a, b []ledger.EvidenceRef) bool {
	aa := append([]ledger.EvidenceRef(nil), a...)
	bb := append([]ledger.EvidenceRef(nil), b...)
	sort.Slice(aa, func(i, j int) bool { return evidenceKey(aa[i]) < evidenceKey(aa[j]) })
	sort.Slice(bb, func(i, j int) bool { return evidenceKey(bb[i]) < evidenceKey(bb[j]) })
	if len(aa) != len(bb) {
		return false
	}
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}

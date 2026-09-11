package githublifecycle

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

const (
	GenericStrategyResultSchemaV1    = "generic-strategy-result-v1"
	GenericStrategyPostMergeSchemaV1 = "generic-strategy-post-merge-v1"
)

// GenericStrategyResultV1 keeps squash/rebase evidence representable without
// granting either method production-v1 merge authority.
type GenericStrategyResultV1Input struct {
	Snapshot         SnapshotIdentity
	Repository       Repository
	PullRequest      PullRequestIdentity
	BaseBranch       Branch
	AcceptedHeadSHA  GitSHA
	AcceptedHeadTree GitSHA
	BaseBeforeSHA    GitSHA
	Method           MergeMethod
	ResultSHA        GitSHA
	ResultTree       GitSHA
	Parents          []GitSHA
	Lineage          []CommitLineage
	EvidenceRefs     []ledger.EvidenceRef
}

type GenericStrategyResultV1 struct {
	input     GenericStrategyResultV1Input
	canonical []byte
	digest    string
	limitsSHA string
}

func NewGenericStrategyResultV1(input GenericStrategyResultV1Input, limits Limits) (GenericStrategyResultV1, error) {
	input = cloneGenericStrategyInput(input)
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return GenericStrategyResultV1{}, err
	}
	if !input.Snapshot.valid() || !input.Repository.valid() || !input.PullRequest.valid() || !input.BaseBranch.valid() ||
		!input.AcceptedHeadSHA.valid() || !input.AcceptedHeadTree.valid() || !input.BaseBeforeSHA.valid() ||
		!input.ResultSHA.valid() || !input.ResultTree.valid() || input.ResultTree != input.AcceptedHeadTree ||
		(input.Method != MergeMethodSquash && input.Method != MergeMethodRebase) ||
		len(input.Parents) > limits.MaxContractParents || len(input.Lineage) > limits.MaxContractLineageEntries ||
		len(input.EvidenceRefs) == 0 || canonicalizeEvidence(&input.EvidenceRefs, limits) != nil {
		return GenericStrategyResultV1{}, errors.New("generic squash/rebase strategy result is invalid or unbounded")
	}
	if err := validateSHAs(input.Parents, "generic result parents"); err != nil {
		return GenericStrategyResultV1{}, err
	}
	for _, entry := range input.Lineage {
		if !entry.SourceSHA.valid() || !entry.SourceTree.valid() || !entry.ResultSHA.valid() || !entry.ResultTree.valid() ||
			len(entry.Parents) > limits.MaxContractParents || validateSHAs(entry.Parents, "generic lineage parents") != nil {
			return GenericStrategyResultV1{}, errors.New("generic strategy lineage is invalid")
		}
	}
	if err := verifyStrategy(input.Method, input.AcceptedHeadSHA, input.AcceptedHeadTree, input.BaseBeforeSHA,
		input.ResultSHA, input.ResultTree, input.Parents, input.Lineage); err != nil {
		return GenericStrategyResultV1{}, err
	}
	canonical, digest, err := canonicalJSON(genericStrategyWire(input, limitsSHA))
	if err != nil {
		return GenericStrategyResultV1{}, err
	}
	return GenericStrategyResultV1{input, canonical, digest, limitsSHA}, nil
}

func genericStrategyWire(input GenericStrategyResultV1Input, limitsSHA string) any {
	return struct {
		Schema           string               `json:"schema"`
		Snapshot         identityWire         `json:"snapshot"`
		Repository       repoWire             `json:"repository"`
		PullRequest      prIdentityWire       `json:"pull_request"`
		BaseBranch       string               `json:"base_branch"`
		AcceptedHeadSHA  string               `json:"accepted_head_sha"`
		AcceptedHeadTree string               `json:"accepted_head_tree"`
		BaseBeforeSHA    string               `json:"base_before_sha"`
		Method           MergeMethod          `json:"method"`
		ResultSHA        string               `json:"result_sha"`
		ResultTree       string               `json:"result_tree"`
		Parents          []string             `json:"parents"`
		Lineage          []lineageWire        `json:"lineage"`
		EvidenceRefs     []ledger.EvidenceRef `json:"evidence_refs"`
		LimitsSHA256     string               `json:"limits_sha256"`
	}{GenericStrategyResultSchemaV1, snapshotWire(input.Snapshot), repositoryWire(input.Repository), pullRequestWire(input.PullRequest),
		input.BaseBranch.String(), input.AcceptedHeadSHA.String(), input.AcceptedHeadTree.String(), input.BaseBeforeSHA.String(),
		input.Method, input.ResultSHA.String(), input.ResultTree.String(), shaStrings(input.Parents), lineageWires(input.Lineage),
		input.EvidenceRefs, limitsSHA}
}

func (r GenericStrategyResultV1) Input() GenericStrategyResultV1Input {
	return cloneGenericStrategyInput(r.input)
}
func (r GenericStrategyResultV1) CanonicalJSON() []byte { return append([]byte(nil), r.canonical...) }
func (r GenericStrategyResultV1) SHA256() string        { return r.digest }
func (r GenericStrategyResultV1) valid() bool {
	return validSHA256(r.digest) && digestBytes(r.canonical) == r.digest && validSHA256(r.limitsSHA)
}

type GenericStrategyPostMergeV1Input struct {
	Result               GenericStrategyResultV1
	Snapshot             SnapshotIdentity
	ObservedTargetTipSHA GitSHA
	ContainmentProof     TargetContainmentProofV1
	EvidenceRefs         []ledger.EvidenceRef
}

type GenericStrategyPostMergeV1 struct {
	input     GenericStrategyPostMergeV1Input
	canonical []byte
	digest    string
	limitsSHA string
}

func NewGenericStrategyPostMergeV1(input GenericStrategyPostMergeV1Input, limits Limits) (GenericStrategyPostMergeV1, error) {
	input.Result = cloneGenericStrategyResult(input.Result)
	input.ContainmentProof = cloneContainmentProof(input.ContainmentProof)
	input.EvidenceRefs = append([]ledger.EvidenceRef(nil), input.EvidenceRefs...)
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return GenericStrategyPostMergeV1{}, err
	}
	if !input.Result.valid() || requireLimitsSHA(limits, input.Result.limitsSHA) != nil || !input.Snapshot.valid() ||
		!input.ObservedTargetTipSHA.valid() || !input.ContainmentProof.valid() || len(input.EvidenceRefs) == 0 ||
		canonicalizeEvidence(&input.EvidenceRefs, limits) != nil {
		return GenericStrategyPostMergeV1{}, errors.New("generic post-merge strategy proof is invalid")
	}
	rebuilt, err := NewGenericStrategyResultV1(input.Result.input, limits)
	if err != nil || rebuilt.digest != input.Result.digest || !bytes.Equal(rebuilt.canonical, input.Result.canonical) {
		return GenericStrategyPostMergeV1{}, errors.New("generic strategy result fails independent validation")
	}
	result, proof := input.Result.input, input.ContainmentProof.input
	if proof.Snapshot != input.Snapshot || proof.Repository != result.Repository || proof.TargetRef != "refs/heads/"+result.BaseBranch.String() ||
		proof.ResultSHA != result.ResultSHA || proof.ObservedTargetTipSHA != input.ObservedTargetTipSHA {
		return GenericStrategyPostMergeV1{}, errors.New("generic containment proof changed repository, target, or result")
	}
	rebuiltProof, err := NewTargetContainmentProofV1(proof, limits)
	if err != nil || rebuiltProof.digest != input.ContainmentProof.digest || !bytes.Equal(rebuiltProof.canonical, input.ContainmentProof.canonical) {
		return GenericStrategyPostMergeV1{}, errors.New("generic containment proof fails independent validation")
	}
	canonical, digest, err := canonicalJSON(struct {
		Schema                 string               `json:"schema"`
		Result                 json.RawMessage      `json:"result"`
		ResultSHA256           string               `json:"result_sha256"`
		Snapshot               identityWire         `json:"snapshot"`
		ObservedTargetTipSHA   string               `json:"observed_target_tip_sha"`
		ContainmentProof       json.RawMessage      `json:"containment_proof"`
		ContainmentProofSHA256 string               `json:"containment_proof_sha256"`
		EvidenceRefs           []ledger.EvidenceRef `json:"evidence_refs"`
		LimitsSHA256           string               `json:"limits_sha256"`
	}{GenericStrategyPostMergeSchemaV1, input.Result.CanonicalJSON(), input.Result.SHA256(), snapshotWire(input.Snapshot),
		input.ObservedTargetTipSHA.String(), input.ContainmentProof.CanonicalJSON(), input.ContainmentProof.SHA256(),
		input.EvidenceRefs, limitsSHA})
	if err != nil {
		return GenericStrategyPostMergeV1{}, err
	}
	return GenericStrategyPostMergeV1{input, canonical, digest, limitsSHA}, nil
}

func (p GenericStrategyPostMergeV1) CanonicalJSON() []byte {
	return append([]byte(nil), p.canonical...)
}
func (p GenericStrategyPostMergeV1) SHA256() string { return p.digest }
func (p GenericStrategyPostMergeV1) Input() GenericStrategyPostMergeV1Input {
	input := p.input
	input.Result = cloneGenericStrategyResult(input.Result)
	input.ContainmentProof = cloneContainmentProof(input.ContainmentProof)
	input.EvidenceRefs = append([]ledger.EvidenceRef(nil), input.EvidenceRefs...)
	return input
}
func (p GenericStrategyPostMergeV1) valid() bool {
	return validSHA256(p.digest) && digestBytes(p.canonical) == p.digest && validSHA256(p.limitsSHA)
}

func cloneGenericStrategyInput(input GenericStrategyResultV1Input) GenericStrategyResultV1Input {
	input.Parents = append([]GitSHA(nil), input.Parents...)
	input.Lineage = cloneLineage(input.Lineage)
	input.EvidenceRefs = append([]ledger.EvidenceRef(nil), input.EvidenceRefs...)
	return input
}

func cloneGenericStrategyResult(result GenericStrategyResultV1) GenericStrategyResultV1 {
	result.input = cloneGenericStrategyInput(result.input)
	result.canonical = append([]byte(nil), result.canonical...)
	return result
}

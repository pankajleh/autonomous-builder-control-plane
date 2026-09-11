package githublifecycle

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

const (
	ResultCommitObservationSchemaV1 = "result-commit-observation-v1"
	TargetContainmentProofSchemaV1  = "target-containment-proof-v1"
	GitHubCompareProofV1            = "github-compare-v1"
)

type ResultCommitObservationV1Input struct {
	Snapshot      SnapshotIdentity
	Repository    Repository
	ResultSHA     GitSHA
	ResultTree    GitSHA
	Parents       []GitSHA
	Message       string
	Author        MergeCommitIdentityV1
	Committer     MergeCommitIdentityV1
	AuthorUnix    int64
	CommitterUnix int64
	RecipeSHA256  string
	EvidenceRefs  []ledger.EvidenceRef
}
type ResultCommitObservationV1 struct {
	input     ResultCommitObservationV1Input
	canonical []byte
	digest    string
	limitsSHA string
}

func NewResultCommitObservationV1(input ResultCommitObservationV1Input, limits Limits) (ResultCommitObservationV1, error) {
	input.Parents = append([]GitSHA(nil), input.Parents...)
	input.EvidenceRefs = append([]ledger.EvidenceRef(nil), input.EvidenceRefs...)
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return ResultCommitObservationV1{}, err
	}
	if !input.Snapshot.valid() || !input.Repository.valid() || !input.ResultSHA.valid() || !input.ResultTree.valid() || len(input.Parents) != 2 || validateSHAs(input.Parents, "result object parents") != nil || !validCommitMessage(input.Message, limits.MaxTextBytes) || !input.Author.valid(limits) || !input.Committer.valid(limits) || input.AuthorUnix <= 0 || input.CommitterUnix <= 0 || !validSHA256(input.RecipeSHA256) || len(input.EvidenceRefs) == 0 || canonicalizeEvidence(&input.EvidenceRefs, limits) != nil {
		return ResultCommitObservationV1{}, errors.New("exact result object observation is invalid")
	}
	wire := struct {
		Schema        string                `json:"schema"`
		Snapshot      identityWire          `json:"snapshot"`
		Repository    repoWire              `json:"repository"`
		ResultSHA     string                `json:"result_sha"`
		ResultTree    string                `json:"result_tree"`
		Parents       []string              `json:"parents"`
		Message       string                `json:"message"`
		Author        MergeCommitIdentityV1 `json:"author"`
		Committer     MergeCommitIdentityV1 `json:"committer"`
		AuthorUnix    int64                 `json:"author_unix"`
		CommitterUnix int64                 `json:"committer_unix"`
		RecipeSHA256  string                `json:"recipe_sha256"`
		EvidenceRefs  []ledger.EvidenceRef  `json:"evidence_refs"`
		LimitsSHA256  string                `json:"limits_sha256"`
	}{ResultCommitObservationSchemaV1, snapshotWire(input.Snapshot), repositoryWire(input.Repository), input.ResultSHA.String(), input.ResultTree.String(), shaStrings(input.Parents), input.Message, input.Author, input.Committer, input.AuthorUnix, input.CommitterUnix, input.RecipeSHA256, input.EvidenceRefs, limitsSHA}
	canonical, digest, err := canonicalJSON(wire)
	if err != nil {
		return ResultCommitObservationV1{}, err
	}
	if err := requireCanonicalObjectSize(canonical, limits.MaxCanonicalObjectBytes, "result commit observation"); err != nil {
		return ResultCommitObservationV1{}, err
	}
	return ResultCommitObservationV1{input, canonical, digest, limitsSHA}, nil
}
func (r ResultCommitObservationV1) CanonicalJSON() []byte { return append([]byte(nil), r.canonical...) }
func (r ResultCommitObservationV1) SHA256() string        { return r.digest }
func (r ResultCommitObservationV1) MarshalJSON() ([]byte, error) {
	if !r.valid() {
		return nil, errors.New("result object observation incomplete")
	}
	return r.CanonicalJSON(), nil
}
func (r ResultCommitObservationV1) valid() bool {
	return len(r.canonical) > 0 && validSHA256(r.digest) && digestBytes(r.canonical) == r.digest && validSHA256(r.limitsSHA)
}

type TargetContainmentStatusV1 string

const (
	TargetContainmentIdentical TargetContainmentStatusV1 = "identical"
	TargetContainmentAhead     TargetContainmentStatusV1 = "ahead"
)

type TargetContainmentProofV1Input struct {
	Snapshot             SnapshotIdentity
	Repository           Repository
	TargetRef            string
	ResultSHA            GitSHA
	ObservedTargetTipSHA GitSHA
	Mechanism            string
	Status               TargetContainmentStatusV1
	MergeBaseSHA         GitSHA
	DescendantDistance   int
	EvidenceRefs         []ledger.EvidenceRef
}
type TargetContainmentProofV1 struct {
	input     TargetContainmentProofV1Input
	canonical []byte
	digest    string
	limitsSHA string
}

func NewTargetContainmentProofV1(input TargetContainmentProofV1Input, limits Limits) (TargetContainmentProofV1, error) {
	input.EvidenceRefs = append([]ledger.EvidenceRef(nil), input.EvidenceRefs...)
	limitsSHA, err := limits.SHA256()
	if err != nil {
		return TargetContainmentProofV1{}, err
	}
	if !input.Snapshot.valid() || !input.Repository.valid() || !stringsHasFullHeadRef(input.TargetRef) || !input.ResultSHA.valid() || !input.ObservedTargetTipSHA.valid() || input.Mechanism != GitHubCompareProofV1 || !input.MergeBaseSHA.valid() || input.MergeBaseSHA != input.ResultSHA || input.DescendantDistance < 0 || input.DescendantDistance > limits.MaxDescendantDistance || len(input.EvidenceRefs) == 0 || canonicalizeEvidence(&input.EvidenceRefs, limits) != nil {
		return TargetContainmentProofV1{}, errors.New("target containment proof is invalid or unbounded")
	}
	if input.Status == TargetContainmentIdentical {
		if input.ObservedTargetTipSHA != input.ResultSHA || input.DescendantDistance != 0 {
			return TargetContainmentProofV1{}, errors.New("identical containment proof disagrees")
		}
	} else if input.Status == TargetContainmentAhead {
		if input.ObservedTargetTipSHA == input.ResultSHA || input.DescendantDistance <= 0 {
			return TargetContainmentProofV1{}, errors.New("descendant containment proof disagrees")
		}
	} else {
		return TargetContainmentProofV1{}, errors.New("unsupported containment status")
	}
	wire := struct {
		Schema               string                    `json:"schema"`
		Snapshot             identityWire              `json:"snapshot"`
		Repository           repoWire                  `json:"repository"`
		TargetRef            string                    `json:"target_ref"`
		ResultSHA            string                    `json:"result_sha"`
		ObservedTargetTipSHA string                    `json:"observed_target_tip_sha"`
		Mechanism            string                    `json:"mechanism"`
		Status               TargetContainmentStatusV1 `json:"status"`
		MergeBaseSHA         string                    `json:"merge_base_sha"`
		DescendantDistance   int                       `json:"descendant_distance"`
		EvidenceRefs         []ledger.EvidenceRef      `json:"evidence_refs"`
		LimitsSHA256         string                    `json:"limits_sha256"`
	}{TargetContainmentProofSchemaV1, snapshotWire(input.Snapshot), repositoryWire(input.Repository), input.TargetRef, input.ResultSHA.String(), input.ObservedTargetTipSHA.String(), input.Mechanism, input.Status, input.MergeBaseSHA.String(), input.DescendantDistance, input.EvidenceRefs, limitsSHA}
	canonical, digest, err := canonicalJSON(wire)
	if err != nil {
		return TargetContainmentProofV1{}, err
	}
	if err := requireCanonicalObjectSize(canonical, limits.MaxCanonicalObjectBytes, "target containment observation"); err != nil {
		return TargetContainmentProofV1{}, err
	}
	return TargetContainmentProofV1{input, canonical, digest, limitsSHA}, nil
}
func (p TargetContainmentProofV1) CanonicalJSON() []byte { return append([]byte(nil), p.canonical...) }
func (p TargetContainmentProofV1) SHA256() string        { return p.digest }
func (p TargetContainmentProofV1) MarshalJSON() ([]byte, error) {
	if !p.valid() {
		return nil, errors.New("target containment proof incomplete")
	}
	return p.CanonicalJSON(), nil
}
func (p TargetContainmentProofV1) valid() bool {
	return len(p.canonical) > 0 && validSHA256(p.digest) && digestBytes(p.canonical) == p.digest && validSHA256(p.limitsSHA)
}

func ValidateResultCommitObservationV1(sealed SealedMergeAuthorizationV1, result MergeResult, observation ResultCommitObservationV1, limits Limits) error {
	if !observation.valid() {
		return errors.New("result object observation incomplete")
	}
	if err := requireLimitsSHA(limits, observation.limitsSHA); err != nil {
		return err
	}
	rebuilt, err := NewResultCommitObservationV1(observation.input, limits)
	if err != nil || rebuilt.digest != observation.digest || !bytes.Equal(rebuilt.canonical, observation.canonical) {
		return errors.New("result object observation fails independent validation")
	}
	recipe := sealed.input.MergeInput.recipe
	i := observation.input
	if i.Repository != sealed.input.MergeInput.authority.Repository() || i.ResultSHA != recipe.ExpectedResultSHA() || i.ResultTree != recipe.input.ExpectedResultTree || !equalSHAs(i.Parents, recipe.input.Parents) || i.Message != recipe.input.Message || i.Author != recipe.input.Author || i.Committer != recipe.input.Committer || i.AuthorUnix != recipe.input.AuthorUnix || i.CommitterUnix != recipe.input.CommitterUnix || i.RecipeSHA256 != recipe.SHA256() || i.ResultSHA != result.immutable.data.ResultSHA {
		return errors.New("exact result object does not match recipe, authority, or result")
	}
	return nil
}
func ValidateTargetContainmentProofV1(sealed SealedMergeAuthorizationV1, result MergeResult, proof TargetContainmentProofV1, limits Limits) error {
	if !proof.valid() {
		return errors.New("target containment proof incomplete")
	}
	if err := requireLimitsSHA(limits, proof.limitsSHA); err != nil {
		return err
	}
	rebuilt, err := NewTargetContainmentProofV1(proof.input, limits)
	if err != nil || rebuilt.digest != proof.digest || !bytes.Equal(rebuilt.canonical, proof.canonical) {
		return errors.New("target containment proof fails independent validation")
	}
	i := proof.input
	authority := sealed.input.MergeInput.authority
	if i.Repository != authority.Repository() || i.TargetRef != "refs/heads/"+authority.BaseBranch().String() || i.ResultSHA != result.immutable.data.ResultSHA {
		return errors.New("target containment proof changed repository, ref, or result")
	}
	return nil
}
func stringsHasFullHeadRef(value string) bool {
	return len(value) > len("refs/heads/") && value[:len("refs/heads/")] == "refs/heads/"
}
func cloneResultObjectProof(r ResultCommitObservationV1) ResultCommitObservationV1 {
	r.input.Parents = append([]GitSHA(nil), r.input.Parents...)
	r.input.EvidenceRefs = append([]ledger.EvidenceRef(nil), r.input.EvidenceRefs...)
	r.canonical = append([]byte(nil), r.canonical...)
	return r
}
func cloneContainmentProof(p TargetContainmentProofV1) TargetContainmentProofV1 {
	p.input.EvidenceRefs = append([]ledger.EvidenceRef(nil), p.input.EvidenceRefs...)
	p.canonical = append([]byte(nil), p.canonical...)
	return p
}

var _ = json.RawMessage{}

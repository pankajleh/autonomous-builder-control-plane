package githublifecycle

import (
	"bytes"
	"errors"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

const CancellationReplayIdentitySchemaV1 = "cancellation-replay-identity-v1"

type CancellationReplayIdentityV1 struct {
	sourceKind      string
	sourceRequestID string
	authorityID     string
	replayKey       string
	indexEvidence   ledger.EvidenceRef
	canonical       []byte
	digest          string
}

type cancellationReplayIdentityWireV1 struct {
	Schema          string             `json:"schema"`
	SourceKind      string             `json:"source_kind"`
	SourceRequestID string             `json:"source_request_id"`
	AuthorityID     string             `json:"authority_id"`
	ReplayKey       string             `json:"replay_key"`
	IndexEvidence   ledger.EvidenceRef `json:"index_evidence"`
}

func NewCancellationReplayIdentityV1(authority CancellationAuthorityV1, indexEvidence ledger.EvidenceRef, limits Limits) (CancellationReplayIdentityV1, error) {
	if !authority.valid() || requireLimitsSHA(limits, authority.limitsSHA) != nil || !validEvidenceRef(indexEvidence) {
		return CancellationReplayIdentityV1{}, errors.New("cancellation replay identity is invalid")
	}
	keyBytes, _, err := canonicalJSON(struct {
		Schema          string `json:"schema"`
		SourceKind      string `json:"source_kind"`
		SourceRequestID string `json:"source_request_id"`
	}{"cancellation-replay-key-v1", authority.input.SourceKind, authority.input.SourceRequestID})
	if err != nil {
		return CancellationReplayIdentityV1{}, err
	}
	replayKey := digestBytes(keyBytes)
	canonical, digest, err := canonicalJSON(cancellationReplayIdentityWireV1{
		CancellationReplayIdentitySchemaV1, authority.input.SourceKind, authority.input.SourceRequestID, authority.id, replayKey, indexEvidence,
	})
	if err != nil {
		return CancellationReplayIdentityV1{}, err
	}
	return CancellationReplayIdentityV1{
		sourceKind: authority.input.SourceKind, sourceRequestID: authority.input.SourceRequestID, authorityID: authority.id,
		replayKey: replayKey, indexEvidence: indexEvidence, canonical: canonical, digest: digest,
	}, nil
}

func (r CancellationReplayIdentityV1) CanonicalJSON() []byte {
	return append([]byte(nil), r.canonical...)
}
func (r CancellationReplayIdentityV1) SHA256() string { return r.digest }
func (r CancellationReplayIdentityV1) valid() bool {
	return r.sourceKind != "" && r.sourceRequestID != "" && validSHA256(r.authorityID) && validSHA256(r.replayKey) &&
		validEvidenceRef(r.indexEvidence) && validSHA256(r.digest) && digestBytes(r.canonical) == r.digest
}

func validateCancellationReplayIdentityV1(authority CancellationAuthorityV1, replay CancellationReplayIdentityV1, limits Limits) error {
	if !replay.valid() {
		return errors.New("cancellation replay identity is incomplete")
	}
	rebuilt, err := NewCancellationReplayIdentityV1(authority, replay.indexEvidence, limits)
	if err != nil || rebuilt.digest != replay.digest || !bytes.Equal(rebuilt.canonical, replay.canonical) ||
		replay.sourceKind != authority.input.SourceKind || replay.sourceRequestID != authority.input.SourceRequestID {
		return errors.New("cancellation replay identity fails independent source-kind/request-ID validation")
	}
	return nil
}

func ParseCanonicalCancellationReplayIdentityV1(data []byte, authority CancellationAuthorityV1, limits Limits) (CancellationReplayIdentityV1, error) {
	var wire cancellationReplayIdentityWireV1
	if err := strictDecode(data, &wire); err != nil {
		return CancellationReplayIdentityV1{}, err
	}
	if wire.Schema != CancellationReplayIdentitySchemaV1 || wire.SourceKind != authority.input.SourceKind ||
		wire.SourceRequestID != authority.input.SourceRequestID || wire.AuthorityID != authority.id {
		return CancellationReplayIdentityV1{}, errors.New("cancellation replay identity wire changed its authority or source request")
	}
	value, err := NewCancellationReplayIdentityV1(authority, wire.IndexEvidence, limits)
	if err != nil || value.replayKey != wire.ReplayKey {
		return CancellationReplayIdentityV1{}, errors.New("cancellation replay identity wire changed its derived replay key")
	}
	if err := requireCanonical(data, value.canonical); err != nil {
		return CancellationReplayIdentityV1{}, err
	}
	return value, nil
}

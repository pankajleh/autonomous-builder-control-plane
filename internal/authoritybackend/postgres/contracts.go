package postgres

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

type PublisherOpenRequestV1 struct {
	Kind                    string `json:"kind"`
	SchemaVersion           string `json:"schema_version"`
	AuthorityDomain         string `json:"authority_domain"`
	RequestID               string `json:"request_id"`
	PublisherID             string `json:"publisher_id"`
	LineageSHA256           string `json:"lineage_sha256"`
	Stage                   string `json:"stage"`
	SubjectSHA256           string `json:"subject_sha256"`
	CanonicalReservationB64 string `json:"canonical_reservation_b64"`
	ReservationSHA256       string `json:"reservation_sha256"`
	OwnerReservationSHA256  string `json:"owner_reservation_sha256"`
	OwnerExecutionSHA256    string `json:"owner_execution_sha256"`
	RuntimeOwnerSHA256      string `json:"runtime_owner_sha256"`
	HeartbeatAt             string `json:"heartbeat_at"`
	ExpectedStageRevision   uint64 `json:"expected_stage_revision"`
	NewStageRevision        uint64 `json:"new_stage_revision"`
}

type PublisherOpenResultV1 struct {
	Kind              string `json:"kind"`
	SchemaVersion     string `json:"schema_version"`
	RequestID         string `json:"request_id"`
	PublisherID       string `json:"publisher_id"`
	StageRevision     uint64 `json:"stage_revision"`
	PublisherRevision uint64 `json:"publisher_revision"`
	Applied           bool   `json:"applied"`
}

type PublisherAppendRequestV1 struct {
	Kind                      string `json:"kind"`
	SchemaVersion             string `json:"schema_version"`
	AuthorityDomain           string `json:"authority_domain"`
	RequestID                 string `json:"request_id"`
	PublisherID               string `json:"publisher_id"`
	LineageSHA256             string `json:"lineage_sha256"`
	Stage                     string `json:"stage"`
	SubjectSHA256             string `json:"subject_sha256"`
	OccurrenceID              string `json:"occurrence_id"`
	OccurrenceOrdinal         uint64 `json:"occurrence_ordinal"`
	ArtifactSHA256            string `json:"artifact_sha256"`
	ArtifactKind              string `json:"artifact_kind"`
	Outcome                   string `json:"outcome"`
	ByteSize                  uint64 `json:"byte_size"`
	ExpectedStageRevision     uint64 `json:"expected_stage_revision"`
	ExpectedPublisherRevision uint64 `json:"expected_publisher_revision"`
	NewStageRevision          uint64 `json:"new_stage_revision"`
	NewPublisherRevision      uint64 `json:"new_publisher_revision"`
	HeartbeatAt               string `json:"heartbeat_at"`
}

type PublisherAppendResultV1 struct {
	Kind              string `json:"kind"`
	SchemaVersion     string `json:"schema_version"`
	RequestID         string `json:"request_id"`
	PublisherID       string `json:"publisher_id"`
	OccurrenceID      string `json:"occurrence_id"`
	OccurrenceOrdinal uint64 `json:"occurrence_ordinal"`
	StageRevision     uint64 `json:"stage_revision"`
	PublisherRevision uint64 `json:"publisher_revision"`
	Applied           bool   `json:"applied"`
}

type PublisherOccurrenceInsertV1 struct {
	PublisherID       string `json:"publisher_id"`
	LineageSHA256     string `json:"lineage_sha256"`
	Stage             string `json:"stage"`
	SubjectSHA256     string `json:"subject_sha256"`
	OccurrenceID      string `json:"occurrence_id"`
	OccurrenceOrdinal uint64 `json:"occurrence_ordinal"`
	ArtifactSHA256    string `json:"artifact_sha256"`
	ArtifactKind      string `json:"artifact_kind"`
	Outcome           string `json:"outcome"`
	ByteSize          uint64 `json:"byte_size"`
}

type PublisherFinalizeRequestV1 struct {
	Kind                      string                       `json:"kind"`
	SchemaVersion             string                       `json:"schema_version"`
	AuthorityDomain           string                       `json:"authority_domain"`
	RequestID                 string                       `json:"request_id"`
	PublisherID               string                       `json:"publisher_id"`
	LineageSHA256             string                       `json:"lineage_sha256"`
	Stage                     string                       `json:"stage"`
	SubjectSHA256             string                       `json:"subject_sha256"`
	ExpectedStageRevision     uint64                       `json:"expected_stage_revision"`
	ExpectedPublisherRevision uint64                       `json:"expected_publisher_revision"`
	NewStageRevision          uint64                       `json:"new_stage_revision"`
	NewPublisherRevision      uint64                       `json:"new_publisher_revision"`
	FinalOccurrence           *PublisherOccurrenceInsertV1 `json:"final_occurrence,omitempty"`
	HeartbeatAt               string                       `json:"heartbeat_at"`
}

type PublisherFinalizeResultV1 struct {
	Kind              string `json:"kind"`
	SchemaVersion     string `json:"schema_version"`
	RequestID         string `json:"request_id"`
	PublisherID       string `json:"publisher_id"`
	StageRevision     uint64 `json:"stage_revision"`
	PublisherRevision uint64 `json:"publisher_revision"`
	FinalOccurrenceID string `json:"final_occurrence_id,omitempty"`
	Applied           bool   `json:"applied"`
}

type PublisherAbandonRequestV1 struct {
	Kind                       string `json:"kind"`
	SchemaVersion              string `json:"schema_version"`
	AuthorityDomain            string `json:"authority_domain"`
	RequestID                  string `json:"request_id"`
	PublisherID                string `json:"publisher_id"`
	LineageSHA256              string `json:"lineage_sha256"`
	Stage                      string `json:"stage"`
	SubjectSHA256              string `json:"subject_sha256"`
	RecoveryProofSHA256        string `json:"recovery_proof_sha256"`
	RuntimeOwnerSHA256         string `json:"runtime_owner_sha256"`
	LastOwnerHeartbeatRevision uint64 `json:"last_owner_heartbeat_revision"`
	ExpectedStageRevision      uint64 `json:"expected_stage_revision"`
	ExpectedPublisherRevision  uint64 `json:"expected_publisher_revision"`
	NewStageRevision           uint64 `json:"new_stage_revision"`
	NewPublisherRevision       uint64 `json:"new_publisher_revision"`
	AbandonCapabilityID        string `json:"abandon_capability_id"`
	AbandonAuthorizationSHA256 string `json:"abandon_authorization_sha256"`
	TransitionStartedAt        string `json:"transition_started_at"`
}

type PublisherAbandonResultV1 struct {
	Kind                string `json:"kind"`
	SchemaVersion       string `json:"schema_version"`
	RequestID           string `json:"request_id"`
	PublisherID         string `json:"publisher_id"`
	StageRevision       uint64 `json:"stage_revision"`
	PublisherRevision   uint64 `json:"publisher_revision"`
	RecoveryProofSHA256 string `json:"recovery_proof_sha256"`
	Applied             bool   `json:"applied"`
}

type CloseStageRequestV1 struct {
	Kind                  string `json:"kind"`
	SchemaVersion         string `json:"schema_version"`
	AuthorityDomain       string `json:"authority_domain"`
	RequestID             string `json:"request_id"`
	LineageSHA256         string `json:"lineage_sha256"`
	Stage                 string `json:"stage"`
	SubjectSHA256         string `json:"subject_sha256"`
	ExpectedStageRevision uint64 `json:"expected_stage_revision"`
	NewStageRevision      uint64 `json:"new_stage_revision"`
	ActiveLimit           uint64 `json:"active_limit"`
}

type CloseStageResultV1 struct {
	Kind                     string   `json:"kind"`
	SchemaVersion            string   `json:"schema_version"`
	RequestID                string   `json:"request_id"`
	Closed                   bool     `json:"closed"`
	StageRevision            uint64   `json:"stage_revision"`
	ThroughOccurrenceOrdinal *uint64  `json:"through_occurrence_ordinal,omitempty"`
	ActivePublisherIDs       []string `json:"active_publisher_ids,omitempty"`
}

type PredecessorWriterOpenRequestV1 struct {
	Kind                      string `json:"kind"`
	SchemaVersion             string `json:"schema_version"`
	AuthorityDomain           string `json:"authority_domain"`
	RequestID                 string `json:"request_id"`
	ControllerIdentity        string `json:"controller_identity"`
	ExpectedDirectoryRevision uint64 `json:"expected_directory_revision"`
	NewDirectoryRevision      uint64 `json:"new_directory_revision"`
	NewCanonicalDirectoryB64  string `json:"new_canonical_directory_b64"`
	NewDirectorySHA256        string `json:"new_directory_sha256"`
	WriterLeaseSHA256         string `json:"writer_lease_sha256"`
}

type PredecessorWriterOpenResultV1 struct {
	Kind               string `json:"kind"`
	SchemaVersion      string `json:"schema_version"`
	RequestID          string `json:"request_id"`
	ControllerIdentity string `json:"controller_identity"`
	DirectoryRevision  uint64 `json:"directory_revision"`
	WriterLeaseSHA256  string `json:"writer_lease_sha256"`
	Applied            bool   `json:"applied"`
}

type PredecessorWriterReleaseRequestV1 struct {
	Kind                      string `json:"kind"`
	SchemaVersion             string `json:"schema_version"`
	AuthorityDomain           string `json:"authority_domain"`
	RequestID                 string `json:"request_id"`
	ControllerIdentity        string `json:"controller_identity"`
	ExpectedDirectoryRevision uint64 `json:"expected_directory_revision"`
	NewDirectoryRevision      uint64 `json:"new_directory_revision"`
	NewCanonicalDirectoryB64  string `json:"new_canonical_directory_b64"`
	NewDirectorySHA256        string `json:"new_directory_sha256"`
	ReleasedWriterLeaseSHA256 string `json:"released_writer_lease_sha256"`
}

type PredecessorWriterReleaseResultV1 struct {
	Kind                      string `json:"kind"`
	SchemaVersion             string `json:"schema_version"`
	RequestID                 string `json:"request_id"`
	ControllerIdentity        string `json:"controller_identity"`
	DirectoryRevision         uint64 `json:"directory_revision"`
	ReleasedWriterLeaseSHA256 string `json:"released_writer_lease_sha256"`
	Applied                   bool   `json:"applied"`
}

type CutoverRequestV1 struct {
	Kind                      string `json:"kind"`
	SchemaVersion             string `json:"schema_version"`
	AuthorityDomain           string `json:"authority_domain"`
	RequestID                 string `json:"request_id"`
	ControllerIdentity        string `json:"controller_identity"`
	ExpectedDirectoryRevision uint64 `json:"expected_directory_revision"`
	WriterEpoch               uint64 `json:"writer_epoch"`
	TombstonedDirectoryB64    string `json:"tombstoned_directory_b64"`
	TombstonedDirectorySHA256 string `json:"tombstoned_directory_sha256"`
	PredecessorStateB64       string `json:"predecessor_state_b64"`
	PredecessorStateSHA256    string `json:"predecessor_state_sha256"`
	PredecessorStateRevision  uint64 `json:"predecessor_state_revision"`
	PredecessorArtifactSHA256 string `json:"predecessor_artifact_sha256"`
	CutoverSHA256             string `json:"cutover_sha256"`
	SuccessorStateB64         string `json:"successor_state_b64"`
	SuccessorStateSHA256      string `json:"successor_state_sha256"`
	SuccessorRevision         uint64 `json:"successor_revision"`
}

type CutoverResultV1 struct {
	Kind               string `json:"kind"`
	SchemaVersion      string `json:"schema_version"`
	RequestID          string `json:"request_id"`
	ControllerIdentity string `json:"controller_identity"`
	DirectoryRevision  uint64 `json:"directory_revision"`
	SuccessorRevision  uint64 `json:"successor_revision"`
	CutoverSHA256      string `json:"cutover_sha256"`
	Applied            bool   `json:"applied"`
}

type WorkerReserveRequestV1 struct {
	Kind                             string `json:"kind"`
	SchemaVersion                    string `json:"schema_version"`
	AuthorityDomain                  string `json:"authority_domain"`
	RequestID                        string `json:"request_id"`
	WorkerIdentity                   string `json:"worker_identity"`
	ReservationID                    string `json:"reservation_id"`
	ControllerIdentity               string `json:"controller_identity"`
	LineageSHA256                    string `json:"lineage_sha256"`
	ObservationKeyRegistrationSHA256 string `json:"observation_key_registration_sha256"`
	ParentReservationSHA256          string `json:"parent_reservation_sha256,omitempty"`
	AccountingMode                   string `json:"accounting_mode"`
	CanonicalReservationB64          string `json:"canonical_reservation_b64"`
	ReservationSHA256                string `json:"reservation_sha256"`
	ExpectedWorkerRevision           uint64 `json:"expected_worker_revision"`
	NewWorkerRevision                uint64 `json:"new_worker_revision"`
}

type WorkerReserveResultV1 struct {
	Kind                 string  `json:"kind"`
	SchemaVersion        string  `json:"schema_version"`
	RequestID            string  `json:"request_id"`
	WorkerIdentity       string  `json:"worker_identity"`
	ReservationID        string  `json:"reservation_id"`
	WorkerRevision       uint64  `json:"worker_revision"`
	ReservationRevision  uint64  `json:"reservation_revision"`
	ParentFrozenRevision *uint64 `json:"parent_frozen_revision,omitempty"`
	Applied              bool    `json:"applied"`
}

type WorkerTransitionRequestV1 struct {
	Kind                             string `json:"kind"`
	SchemaVersion                    string `json:"schema_version"`
	AuthorityDomain                  string `json:"authority_domain"`
	RequestID                        string `json:"request_id"`
	WorkerIdentity                   string `json:"worker_identity"`
	ReservationID                    string `json:"reservation_id"`
	ControllerIdentity               string `json:"controller_identity"`
	LineageSHA256                    string `json:"lineage_sha256"`
	ObservationKeyRegistrationSHA256 string `json:"observation_key_registration_sha256"`
	ExpectedWorkerRevision           uint64 `json:"expected_worker_revision"`
	NewWorkerRevision                uint64 `json:"new_worker_revision"`
	ExpectedReservationRevision      uint64 `json:"expected_reservation_revision"`
	NewReservationRevision           uint64 `json:"new_reservation_revision"`
	ExpectedLifecycle                string `json:"expected_lifecycle"`
	NewLifecycle                     string `json:"new_lifecycle"`
	TransitionCause                  string `json:"transition_cause"`
	ExpectedReservationSHA256        string `json:"expected_reservation_sha256"`
	TransitionedReservationB64       string `json:"transitioned_reservation_b64"`
	TransitionedReservationSHA256    string `json:"transitioned_reservation_sha256"`
	TransitionEvidenceSHA256         string `json:"transition_evidence_sha256,omitempty"`
}

type WorkerTransitionResultV1 struct {
	Kind                     string `json:"kind"`
	SchemaVersion            string `json:"schema_version"`
	RequestID                string `json:"request_id"`
	WorkerIdentity           string `json:"worker_identity"`
	ReservationID            string `json:"reservation_id"`
	WorkerRevision           uint64 `json:"worker_revision"`
	ReservationRevision      uint64 `json:"reservation_revision"`
	Lifecycle                string `json:"lifecycle"`
	TransitionEvidenceSHA256 string `json:"transition_evidence_sha256,omitempty"`
	Applied                  bool   `json:"applied"`
}

type operationContractV1 struct {
	requestKind, requestSchema string
	resultKind, resultSchema   string
	maximum                    int
}

var operationContractsV1 = map[string]operationContractV1{
	"abcp_publisher_open_v1":             {"PublisherOpenRequestV1", "publisher-open-request-v1", "PublisherOpenResultV1", "publisher-open-result-v1", MaxCanonicalRequestBytesV1},
	"abcp_publisher_append_v1":           {"PublisherAppendRequestV1", "publisher-append-request-v1", "PublisherAppendResultV1", "publisher-append-result-v1", MaxCanonicalRequestBytesV1},
	"abcp_publisher_finalize_v1":         {"PublisherFinalizeRequestV1", "publisher-finalize-request-v1", "PublisherFinalizeResultV1", "publisher-finalize-result-v1", MaxCanonicalRequestBytesV1},
	"abcp_publisher_abandon_v1":          {"PublisherAbandonRequestV1", "publisher-abandon-request-v1", "PublisherAbandonResultV1", "publisher-abandon-result-v1", MaxCanonicalRequestBytesV1},
	"abcp_close_stage_v1":                {"CloseStageRequestV1", "close-stage-request-v1", "CloseStageResultV1", "close-stage-result-v1", MaxCanonicalRequestBytesV1},
	"abcp_predecessor_writer_open_v1":    {"PredecessorWriterOpenRequestV1", "predecessor-writer-open-request-v1", "PredecessorWriterOpenResultV1", "predecessor-writer-open-result-v1", MaxCanonicalRequestBytesV1},
	"abcp_predecessor_writer_release_v1": {"PredecessorWriterReleaseRequestV1", "predecessor-writer-release-request-v1", "PredecessorWriterReleaseResultV1", "predecessor-writer-release-result-v1", MaxCanonicalRequestBytesV1},
	"abcp_cutover_v1":                    {"CutoverRequestV1", "cutover-request-v1", "CutoverResultV1", "cutover-result-v1", MaxCutoverRequestBytesV1},
	"abcp_worker_reserve_v1":             {"WorkerReserveRequestV1", "worker-reserve-request-v1", "WorkerReserveResultV1", "worker-reserve-result-v1", MaxCanonicalRequestBytesV1},
	"abcp_worker_transition_v1":          {"WorkerTransitionRequestV1", "worker-transition-request-v1", "WorkerTransitionResultV1", "worker-transition-result-v1", MaxCanonicalRequestBytesV1},
}

func marshalOperationRequestV1(function string, request any) ([]byte, operationContractV1, error) {
	contract, ok := operationContractsV1[function]
	if !ok {
		return nil, contract, errors.New("PostgreSQL operation is not in the frozen function set")
	}
	if !operationRequestTypeV1(function, request) {
		return nil, contract, fmt.Errorf("%s requires its exact frozen Go request type", function)
	}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, contract, fmt.Errorf("marshal %s: %w", contract.requestKind, err)
	}
	if err := validateCanonicalJSONV1(data, contract.maximum); err != nil {
		return nil, contract, err
	}
	var envelope struct {
		Kind            string `json:"kind"`
		SchemaVersion   string `json:"schema_version"`
		AuthorityDomain string `json:"authority_domain"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Kind != contract.requestKind || envelope.SchemaVersion != contract.requestSchema || validateAuthorityDomainV1(envelope.AuthorityDomain) != nil {
		return nil, contract, errors.New("operation request kind, schema, or authority domain is invalid")
	}
	if function == "abcp_cutover_v1" {
		var request CutoverRequestV1
		if err := json.Unmarshal(data, &request); err != nil {
			return nil, contract, err
		}
		for _, field := range []string{request.TombstonedDirectoryB64, request.PredecessorStateB64, request.SuccessorStateB64} {
			decoded, err := base64.StdEncoding.Strict().DecodeString(field)
			if err != nil || len(decoded) == 0 || len(decoded) > MaxCanonicalRequestBytesV1 {
				return nil, contract, errors.New("cutover decoded field is outside the one-MiB bound")
			}
		}
		base64Bytes := len(request.TombstonedDirectoryB64) + len(request.PredecessorStateB64) + len(request.SuccessorStateB64)
		if len(data)-base64Bytes > MaxCutoverJSONOverheadBytesV1 {
			return nil, contract, errors.New("cutover non-base64 JSON overhead exceeds 65,536 bytes")
		}
	}
	return data, contract, nil
}

func operationRequestTypeV1(function string, request any) bool {
	switch function {
	case "abcp_publisher_open_v1":
		switch request.(type) {
		case PublisherOpenRequestV1, *PublisherOpenRequestV1:
			return true
		}
	case "abcp_publisher_append_v1":
		switch request.(type) {
		case PublisherAppendRequestV1, *PublisherAppendRequestV1:
			return true
		}
	case "abcp_publisher_finalize_v1":
		switch request.(type) {
		case PublisherFinalizeRequestV1, *PublisherFinalizeRequestV1:
			return true
		}
	case "abcp_publisher_abandon_v1":
		switch request.(type) {
		case PublisherAbandonRequestV1, *PublisherAbandonRequestV1:
			return true
		}
	case "abcp_close_stage_v1":
		switch request.(type) {
		case CloseStageRequestV1, *CloseStageRequestV1:
			return true
		}
	case "abcp_predecessor_writer_open_v1":
		switch request.(type) {
		case PredecessorWriterOpenRequestV1, *PredecessorWriterOpenRequestV1:
			return true
		}
	case "abcp_predecessor_writer_release_v1":
		switch request.(type) {
		case PredecessorWriterReleaseRequestV1, *PredecessorWriterReleaseRequestV1:
			return true
		}
	case "abcp_cutover_v1":
		switch request.(type) {
		case CutoverRequestV1, *CutoverRequestV1:
			return true
		}
	case "abcp_worker_reserve_v1":
		switch request.(type) {
		case WorkerReserveRequestV1, *WorkerReserveRequestV1:
			return true
		}
	case "abcp_worker_transition_v1":
		switch request.(type) {
		case WorkerTransitionRequestV1, *WorkerTransitionRequestV1:
			return true
		}
	default:
		return false
	}
	return false
}

func unmarshalOperationResultV1(data []byte, contract operationContractV1, result any) error {
	if err := validateCanonicalJSONV1(data, MaxCanonicalRequestBytesV1); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		return fmt.Errorf("strict-parse %s: %w", contract.resultKind, err)
	}
	var envelope struct {
		Kind          string `json:"kind"`
		SchemaVersion string `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Kind != contract.resultKind || envelope.SchemaVersion != contract.resultSchema {
		return errors.New("operation result kind or schema is invalid")
	}
	return nil
}

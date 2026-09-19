package serviceapi

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
)

const (
	MaxCommandReasonBytes   = 1 << 10
	MaxActionPayloadBytes   = 16 << 10
	MaxActionPayloadDepth   = 8
	MaxRunTaskMarkdownBytes = 64 << 10
)

var stateNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

type DelegatedActorV1 struct {
	SubjectID   string        `json:"subject_id"`
	SubjectType PrincipalType `json:"subject_type"`
}

// CommandEnvelopeV1 freezes the common bounded command shape. Track D owns
// action-specific payload decoding and may not accept raw paths, PIDs, argv,
// manifests, or caller-supplied evidence references through Payload.
type CommandEnvelopeV1 struct {
	SchemaVersion    int               `json:"schema_version"`
	RequestID        string            `json:"request_id"`
	AttemptID        string            `json:"attempt_id"`
	ExpectedState    string            `json:"expected_state"`
	ExpectedRevision string            `json:"expected_revision"`
	Reason           string            `json:"reason"`
	DelegatedActor   *DelegatedActorV1 `json:"delegated_actor,omitempty"`
	Payload          json.RawMessage   `json:"payload"`
}

type ActionStatusV1 struct {
	OperationID           string   `json:"operation_id"`
	Status                string   `json:"status"`
	StatusURL             string   `json:"status_url"`
	AuthoritativeEventIDs []string `json:"authoritative_event_ids,omitempty"`
}

// RunAdmissionRequestV1 is the complete product-visible run admission shape.
// Execution policy and controller filesystem details deliberately have no
// representation in this DTO.
type RunAdmissionRequestV1 struct {
	SchemaVersion          int              `json:"schema_version"`
	RequestID              string           `json:"request_id"`
	ProfileID              string           `json:"profile_id"`
	ProductAuthorizationID string           `json:"product_authorization_id"`
	ProductTaskID          string           `json:"product_task_id"`
	ProductVersionID       string           `json:"product_version_id"`
	ProductManifestSHA256  string           `json:"product_manifest_sha256"`
	RepositoryBaseSHA      string           `json:"repository_base_sha"`
	TaskMarkdown           string           `json:"task_markdown"`
	DelegatedActor         DelegatedActorV1 `json:"delegated_actor"`
}

type RunAdmissionResponseV1 struct {
	RunID  string `json:"run_id"`
	RunURL string `json:"run_url"`
}

func ValidateRunAdmissionRequestV1(request RunAdmissionRequestV1) error {
	if request.SchemaVersion != 1 || ValidatePrincipalID(request.RequestID) != nil ||
		runtimecatalog.ValidateIdentifier(request.ProfileID) != nil || len(request.ProfileID) > 128 ||
		ValidatePrincipalID(request.ProductAuthorizationID) != nil || ValidatePrincipalID(request.ProductTaskID) != nil ||
		ValidatePrincipalID(request.ProductVersionID) != nil {
		return errors.New("invalid run admission identity")
	}
	if !isLowerSHA256(request.ProductManifestSHA256) || !isLowerGitObjectID(request.RepositoryBaseSHA) {
		return errors.New("invalid run admission digest")
	}
	if request.TaskMarkdown == "" || len(request.TaskMarkdown) > MaxRunTaskMarkdownBytes ||
		!utf8.ValidString(request.TaskMarkdown) || strings.ContainsRune(request.TaskMarkdown, 0) {
		return errors.New("invalid run task markdown")
	}
	if ValidatePrincipalID(request.DelegatedActor.SubjectID) != nil ||
		(request.DelegatedActor.SubjectType != PrincipalUser && request.DelegatedActor.SubjectType != PrincipalOperator) {
		return errors.New("invalid delegated actor")
	}
	return nil
}

func ValidateCommandEnvelopeV1(command CommandEnvelopeV1, delegatedActorRequired bool) error {
	if command.SchemaVersion != 1 || ValidatePrincipalID(command.RequestID) != nil || runtimecatalog.ValidateIdentifier(command.AttemptID) != nil {
		return errors.New("invalid command identity")
	}
	if !stateNamePattern.MatchString(command.ExpectedState) || !isLowerSHA256(command.ExpectedRevision) {
		return errors.New("invalid command precondition")
	}
	if len(command.Reason) > MaxCommandReasonBytes || !utf8.ValidString(command.Reason) || strings.ContainsRune(command.Reason, 0) {
		return errors.New("invalid command reason")
	}
	if delegatedActorRequired && command.DelegatedActor == nil {
		return errors.New("delegated actor is required")
	}
	if command.DelegatedActor != nil {
		if ValidatePrincipalID(command.DelegatedActor.SubjectID) != nil || (command.DelegatedActor.SubjectType != PrincipalUser && command.DelegatedActor.SubjectType != PrincipalOperator) {
			return errors.New("invalid delegated actor")
		}
	}
	if len(command.Payload) == 0 || len(command.Payload) > MaxActionPayloadBytes || jsonDepth(command.Payload) > MaxActionPayloadDepth {
		return errors.New("invalid action payload")
	}
	return nil
}

func isLowerSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func isLowerGitObjectID(value string) bool {
	if (len(value) != 40 && len(value) != 64) || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func jsonDepth(data []byte) int {
	decoder := json.NewDecoder(bytes.NewReader(data))
	depth, maximum := 0, 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return MaxActionPayloadDepth + 1
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{', '[':
				depth++
				if depth > maximum {
					maximum = depth
				}
			case '}', ']':
				depth--
			}
		}
	}
	if depth != 0 || rejectDuplicateJSONFields(data) != nil {
		return MaxActionPayloadDepth + 1
	}
	return maximum
}

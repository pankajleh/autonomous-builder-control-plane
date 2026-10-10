package serviceapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
)

const (
	MaxCommandReasonBytes   = 1 << 10
	MaxActionPayloadBytes   = 16 << 10
	MaxActionPayloadDepth   = 8
	MaxRunTaskMarkdownBytes = 64 << 10
	// MaxRunTaskMarkdownLineBytes stays below the pinned Ralphex plan parser's
	// bufio.Scanner token ceiling. The total task bound remains unchanged.
	MaxRunTaskMarkdownLineBytes = (64 << 10) - 1
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
	// Template names the one exact library template the build starts from
	// (Repo C design note DECORATION.md, phase 2). Omitted when the build
	// starts fresh, so every request without it keeps its digest.
	Template *RunTemplateV1 `json:"template,omitempty"`
}

// RunTemplateV1 names a template of the library by its version tag's commit and
// the tree of its assembled app (dist/<template_id>). The builder copies those
// files first; ABCP checks the copy against the profile's read-only mirror
// before acceptance. The task markdown carries how to copy them.
type RunTemplateV1 struct {
	TemplateID string `json:"template_id"`
	Version    string `json:"version"`
	CommitSHA  string `json:"commit_sha"`
	TreeSHA    string `json:"tree_sha"`
}

var (
	runTemplateIDPattern      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	runTemplateVersionPattern = regexp.MustCompile(`^[0-9]{1,6}\.[0-9]{1,6}\.[0-9]{1,6}$`)
)

// ValidateRunTemplateV1 accepts a template id, a dotted version and two
// lowercase Git object IDs.
func ValidateRunTemplateV1(template RunTemplateV1) error {
	if !runTemplateIDPattern.MatchString(template.TemplateID) || !runTemplateVersionPattern.MatchString(template.Version) ||
		!isLowerGitObjectID(template.CommitSHA) || !isLowerGitObjectID(template.TreeSHA) {
		return errors.New("invalid run template")
	}
	return nil
}

// DevelopmentRunAdmissionRequestV1 is the complete development-only run
// admission shape. Private execution and filesystem policy deliberately have
// no representation in this DTO.
type DevelopmentRunAdmissionRequestV1 struct {
	SchemaVersion            int              `json:"schema_version"`
	RequestID                string           `json:"request_id"`
	ProfileID                string           `json:"profile_id"`
	DevelopmentCapsuleID     string           `json:"development_capsule_id"`
	DevelopmentSliceID       string           `json:"development_slice_id"`
	DevelopmentCapsuleSHA256 string           `json:"development_capsule_sha256"`
	RepositoryBaseSHA        string           `json:"repository_base_sha"`
	TaskMarkdown             string           `json:"task_markdown"`
	DelegatedActor           DelegatedActorV1 `json:"delegated_actor"`
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
		!utf8.ValidString(request.TaskMarkdown) || strings.ContainsRune(request.TaskMarkdown, 0) ||
		maxLineBytes(request.TaskMarkdown) > MaxRunTaskMarkdownLineBytes {
		return errors.New("invalid run task markdown")
	}
	if ValidatePrincipalID(request.DelegatedActor.SubjectID) != nil ||
		(request.DelegatedActor.SubjectType != PrincipalUser && request.DelegatedActor.SubjectType != PrincipalOperator) {
		return errors.New("invalid delegated actor")
	}
	if request.Template != nil {
		if err := ValidateRunTemplateV1(*request.Template); err != nil {
			return err
		}
	}
	return nil
}

func ValidateDevelopmentRunAdmissionRequestV1(request DevelopmentRunAdmissionRequestV1) error {
	if request.SchemaVersion != 1 || ValidatePrincipalID(request.RequestID) != nil ||
		runtimecatalog.ValidateIdentifier(request.ProfileID) != nil || len(request.ProfileID) > 128 ||
		ValidatePrincipalID(request.DevelopmentCapsuleID) != nil || ValidatePrincipalID(request.DevelopmentSliceID) != nil {
		return errors.New("invalid development run admission identity")
	}
	if !isLowerSHA256(request.DevelopmentCapsuleSHA256) || !isLowerGitObjectID(request.RepositoryBaseSHA) {
		return errors.New("invalid development run admission digest")
	}
	if request.TaskMarkdown == "" || len(request.TaskMarkdown) > MaxRunTaskMarkdownBytes ||
		!utf8.ValidString(request.TaskMarkdown) || strings.ContainsRune(request.TaskMarkdown, 0) ||
		maxLineBytes(request.TaskMarkdown) > MaxRunTaskMarkdownLineBytes {
		return errors.New("invalid development task markdown")
	}
	capsuleDigest := sha256.Sum256([]byte(request.TaskMarkdown))
	if hex.EncodeToString(capsuleDigest[:]) != request.DevelopmentCapsuleSHA256 {
		return errors.New("development capsule digest mismatch")
	}
	if ValidatePrincipalID(request.DelegatedActor.SubjectID) != nil ||
		(request.DelegatedActor.SubjectType != PrincipalUser && request.DelegatedActor.SubjectType != PrincipalOperator) {
		return errors.New("invalid delegated actor")
	}
	return nil
}

func maxLineBytes(value string) int {
	maximum, current := 0, 0
	for index := 0; index < len(value); index++ {
		if value[index] == '\n' {
			if current > maximum {
				maximum = current
			}
			current = 0
			continue
		}
		current++
	}
	if current > maximum {
		maximum = current
	}
	return maximum
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

// PdlcExperienceCapabilitiesV1 leaves the original strict capabilities DTO intact.
type PdlcExperienceCapabilitiesV1 struct {
	SchemaVersion  string `json:"schema_version"`
	ActivityStream bool   `json:"activity_stream"`
	PreviewRuntime bool   `json:"preview_runtime"`
}

// Preview commands identify governed objects only. Runtime policy is protected
// controller configuration and has no representation in these commands.
type PreviewRequestV1 struct {
	SchemaVersion        int              `json:"schema_version"`
	RequestID            string           `json:"request_id"`
	ExpectedRunID        string           `json:"expected_run_id"`
	CheckpointActivityID string           `json:"checkpoint_activity_id"`
	ProfileID            string           `json:"profile_id"`
	DelegatedActor       DelegatedActorV1 `json:"delegated_actor"`
}
type PreviewStopRequestV1 struct {
	SchemaVersion     int              `json:"schema_version"`
	RequestID         string           `json:"request_id"`
	ExpectedRunID     string           `json:"expected_run_id"`
	ExpectedPreviewID string           `json:"expected_preview_id"`
	DelegatedActor    DelegatedActorV1 `json:"delegated_actor"`
}
type PreviewV1 struct {
	SchemaVersion          string `json:"schema_version"`
	PreviewID              string `json:"preview_id"`
	Revision               uint64 `json:"revision"`
	ProductAuthorizationID string `json:"product_authorization_id"`
	ProductTaskID          string `json:"product_task_id"`
	ProductVersionID       string `json:"product_version_id"`
	RunID                  string `json:"run_id"`
	CheckpointActivityID   string `json:"checkpoint_activity_id"`
	SourceSHA              string `json:"source_sha"`
	ProfileID              string `json:"profile_id"`
	ProfileDigest          string `json:"profile_digest"`
	Status                 string `json:"status"`
	Health                 string `json:"health"`
	CreatedAt              string `json:"created_at"`
	ExpiresAt              string `json:"expires_at"`
	StoppedAt              string `json:"stopped_at,omitempty"`
	ValidationID           string `json:"validation_id"`
	EvidenceID             string `json:"evidence_id"`
	RequestID              string `json:"request_id"`
	RequestDigest          string `json:"request_digest"`
	// RouteHandle is opaque, server-only and usable only while READY. It is
	// neither a public URL nor a browser access grant.
	RouteHandle string `json:"route_handle,omitempty"`
}
type PreviewListV1 struct {
	SchemaVersion string      `json:"schema_version"`
	RunID         string      `json:"run_id"`
	Previews      []PreviewV1 `json:"previews"`
}

// PreviewRouteV1 is a server-only presentation target, never a browser access
// grant. It binds one current route handle to its exact preview and expiry.
type PreviewRouteV1 struct {
	SchemaVersion string `json:"schema_version"`
	RunID         string `json:"run_id"`
	PreviewID     string `json:"preview_id"`
	RouteHandle   string `json:"route_handle"`
	ExpiresAt     string `json:"expires_at"`
	TargetURL     string `json:"target_url"`
}

func ValidatePreviewRouteV1(v PreviewRouteV1) error {
	if v.SchemaVersion != "PreviewRouteV1" || runtimecatalog.ValidateIdentifier(v.RunID) != nil || !isLowerSHA256(v.PreviewID) || !isLowerSHA256(v.RouteHandle) {
		return errors.New("invalid preview route identity")
	}
	if _, err := time.Parse(time.RFC3339Nano, v.ExpiresAt); err != nil {
		return errors.New("invalid preview route expiry")
	}
	return ValidatePreviewTargetURL(v.TargetURL)
}

// ValidatePreviewTargetURL accepts only a canonical HTTP origin with a literal
// loopback IP and explicit nonzero port. DNS, credentials, paths, queries and
// fragments cannot supply alternate destinations or private runtime details.
func ValidatePreviewTargetURL(value string) error {
	authority, ok := strings.CutPrefix(value, "http://")
	host, port, err := net.SplitHostPort(authority)
	if !ok || err != nil {
		return errors.New("invalid preview target")
	}
	ip, err := netip.ParseAddr(host)
	n, portErr := strconv.Atoi(port)
	if err != nil || !ip.IsLoopback() || ip.Zone() != "" || portErr != nil || n < 1 || n > 65535 || value != "http://"+net.JoinHostPort(ip.String(), strconv.Itoa(n)) {
		return errors.New("invalid preview target")
	}
	return nil
}

func validPreviewActor(a DelegatedActorV1) bool {
	return ValidatePrincipalID(a.SubjectID) == nil && (a.SubjectType == PrincipalUser || a.SubjectType == PrincipalOperator)
}
func ValidatePreviewRequestV1(c PreviewRequestV1, run string) error {
	if c.SchemaVersion != 1 || ValidatePrincipalID(c.RequestID) != nil || runtimecatalog.ValidateIdentifier(run) != nil || c.ExpectedRunID != run || !isLowerSHA256(c.CheckpointActivityID) || ValidatePrincipalID(c.ProfileID) != nil || !validPreviewActor(c.DelegatedActor) {
		return errors.New("invalid preview command")
	}
	return nil
}
func ValidatePreviewStopRequestV1(c PreviewStopRequestV1, run, preview string) error {
	if c.SchemaVersion != 1 || ValidatePrincipalID(c.RequestID) != nil || runtimecatalog.ValidateIdentifier(run) != nil || c.ExpectedRunID != run || !isLowerSHA256(preview) || c.ExpectedPreviewID != preview || !validPreviewActor(c.DelegatedActor) {
		return errors.New("invalid preview stop command")
	}
	return nil
}

func ValidatePreviewV1(v PreviewV1) error {
	if v.SchemaVersion != "PreviewV1" || !isLowerSHA256(v.PreviewID) || v.Revision == 0 || runtimecatalog.ValidateIdentifier(v.RunID) != nil || ValidatePrincipalID(v.ProductAuthorizationID) != nil || ValidatePrincipalID(v.ProductTaskID) != nil || ValidatePrincipalID(v.ProductVersionID) != nil || !isLowerSHA256(v.CheckpointActivityID) || !isLowerGitObjectID(v.SourceSHA) || ValidatePrincipalID(v.ProfileID) != nil || !isLowerSHA256(v.ProfileDigest) || !isLowerSHA256(v.ValidationID) || !isLowerSHA256(v.EvidenceID) || ValidatePrincipalID(v.RequestID) != nil || !isLowerSHA256(v.RequestDigest) {
		return errors.New("invalid preview identity")
	}
	created, e1 := time.Parse(time.RFC3339Nano, v.CreatedAt)
	expires, e2 := time.Parse(time.RFC3339Nano, v.ExpiresAt)
	if e1 != nil || e2 != nil || !expires.After(created) || expires.Sub(created) > time.Hour {
		return errors.New("invalid preview lifetime")
	}
	switch v.Status {
	case "REQUESTED", "VALIDATING", "STARTING":
		if v.Health != "UNKNOWN" || v.StoppedAt != "" || v.RouteHandle != "" {
			return errors.New("invalid pending preview")
		}
	case "READY":
		if (v.Health != "HEALTHY" && v.Health != "DEGRADED") || v.StoppedAt != "" || !isLowerSHA256(v.RouteHandle) {
			return errors.New("invalid ready preview")
		}
	case "FAILED", "STOPPED", "EXPIRED":
		stopped, err := time.Parse(time.RFC3339Nano, v.StoppedAt)
		if err != nil || stopped.Before(created) || v.Health != "UNKNOWN" || v.RouteHandle != "" {
			return errors.New("invalid terminal preview")
		}
	default:
		return errors.New("invalid preview state")
	}
	return nil
}

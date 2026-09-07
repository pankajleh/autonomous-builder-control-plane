// Package scheduler defines the controller-owned accepted-candidate queue and
// the frozen evidence contract used by later EP-004 integration tracks.
//
// This package selects candidates for integration evaluation. It does not
// perform state transitions and cannot grant READY_FOR_MERGE.
package scheduler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

// CandidateInput is the serializable controller input used to construct an
// immutable AcceptedCandidate.
type CandidateInput struct {
	ProjectID                string               `json:"project_id"`
	PlanID                   string               `json:"plan_id"`
	RunID                    string               `json:"run_id"`
	AttemptID                string               `json:"attempt_id"`
	Repository               string               `json:"repository"`
	Branch                   string               `json:"branch"`
	StartSHA                 string               `json:"start_sha"`
	HeadSHA                  string               `json:"head_sha"`
	AcceptanceEvidence       []ledger.EvidenceRef `json:"acceptance_evidence"`
	AcceptedAt               time.Time            `json:"accepted_at"`
	AcceptancePolicyIdentity string               `json:"acceptance_policy_identity"`
}

// AcceptedCandidate is a validated immutable controller acceptance record.
// Mutable data is private and every accessor returns a copy.
type AcceptedCandidate struct {
	data CandidateInput
}

// NewAcceptedCandidate validates exact identity and complete acceptance
// provenance, then freezes a defensive copy of the input.
func NewAcceptedCandidate(input CandidateInput) (AcceptedCandidate, error) {
	input = cloneCandidateInput(input)
	fields := []struct {
		name  string
		value string
	}{
		{"project_id", input.ProjectID}, {"plan_id", input.PlanID}, {"run_id", input.RunID},
		{"attempt_id", input.AttemptID}, {"repository", input.Repository}, {"branch", input.Branch},
		{"acceptance_policy_identity", input.AcceptancePolicyIdentity},
	}
	for _, field := range fields {
		if field.value == "" || strings.TrimSpace(field.value) != field.value || containsControl(field.value) || !utf8.ValidString(field.value) {
			return AcceptedCandidate{}, fmt.Errorf("%s must be non-empty, trimmed valid UTF-8 with no control characters", field.name)
		}
	}
	if !filepath.IsAbs(input.Repository) || filepath.Clean(input.Repository) != input.Repository {
		return AcceptedCandidate{}, errors.New("repository must be an absolute clean path")
	}
	if !validBranchName(input.Branch) {
		return AcceptedCandidate{}, errors.New("branch must be a valid full Git branch name")
	}
	if !validObjectID(input.StartSHA) || !validObjectID(input.HeadSHA) {
		return AcceptedCandidate{}, errors.New("start_sha and head_sha must be exact lowercase Git object IDs")
	}
	if input.AcceptedAt.IsZero() {
		return AcceptedCandidate{}, errors.New("accepted_at is required")
	}
	input.AcceptedAt = input.AcceptedAt.UTC()
	if len(input.AcceptanceEvidence) == 0 {
		return AcceptedCandidate{}, errors.New("at least one acceptance evidence reference is required")
	}
	seenEvidence := make(map[string]struct{}, len(input.AcceptanceEvidence))
	for index, ref := range input.AcceptanceEvidence {
		if err := validateEvidenceRef(ref); err != nil {
			return AcceptedCandidate{}, fmt.Errorf("acceptance evidence %d: %w", index, err)
		}
		key := evidenceKey(ref)
		if _, exists := seenEvidence[key]; exists {
			return AcceptedCandidate{}, fmt.Errorf("acceptance evidence %d duplicates an earlier reference", index)
		}
		seenEvidence[key] = struct{}{}
	}
	sort.Slice(input.AcceptanceEvidence, func(i, j int) bool {
		return evidenceKey(input.AcceptanceEvidence[i]) < evidenceKey(input.AcceptanceEvidence[j])
	})
	return AcceptedCandidate{data: input}, nil
}

// Input returns a deep copy of the canonical candidate record.
func (c AcceptedCandidate) Input() CandidateInput {
	return cloneCandidateInput(c.data)
}

// Key identifies one accepted run attempt. It is stable across processes.
func (c AcceptedCandidate) Key() string {
	return strings.Join([]string{c.data.ProjectID, c.data.PlanID, c.data.RunID, c.data.AttemptID}, "\x00")
}

// MarshalJSON emits the canonical accepted-candidate record.
func (c AcceptedCandidate) MarshalJSON() ([]byte, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(c.data)
}

func (c AcceptedCandidate) validate() error {
	validated, err := NewAcceptedCandidate(c.data)
	if err != nil {
		return err
	}
	if validated.Key() != c.Key() {
		return errors.New("candidate identity is invalid")
	}
	return nil
}

func cloneCandidate(candidate AcceptedCandidate) AcceptedCandidate {
	return AcceptedCandidate{data: cloneCandidateInput(candidate.data)}
}

func cloneCandidateInput(input CandidateInput) CandidateInput {
	input.AcceptanceEvidence = append([]ledger.EvidenceRef(nil), input.AcceptanceEvidence...)
	return input
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validBranchName(value string) bool {
	if value == "@" || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") ||
		strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || strings.Contains(value, "//") ||
		strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.ContainsAny(value, " ~^:?*[\\") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func validateEvidenceRef(ref ledger.EvidenceRef) error {
	if ref.URI == "" || strings.TrimSpace(ref.URI) != ref.URI || containsControl(ref.URI) || !utf8.ValidString(ref.URI) {
		return errors.New("URI must be non-empty, trimmed, and contain no control characters")
	}
	if ref.Kind == "" || strings.TrimSpace(ref.Kind) != ref.Kind || containsControl(ref.Kind) || !utf8.ValidString(ref.Kind) {
		return errors.New("kind must be non-empty, trimmed, and contain no control characters")
	}
	if len(ref.SHA256) != sha256.Size*2 {
		return errors.New("SHA256 must be a complete lowercase hexadecimal digest")
	}
	decoded, err := hex.DecodeString(ref.SHA256)
	if err != nil || hex.EncodeToString(decoded) != ref.SHA256 {
		return errors.New("SHA256 must be a complete lowercase hexadecimal digest")
	}
	return nil
}

func evidenceKey(ref ledger.EvidenceRef) string {
	return strings.Join([]string{ref.URI, ref.SHA256, ref.Kind}, "\x00")
}

func validateRepositoryPath(value string) error {
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return errors.New("repository must be an absolute clean path")
	}
	return nil
}

func validateGitPath(value string) error {
	if value == "" || !utf8.ValidString(value) || strings.HasPrefix(value, "/") || path.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return fmt.Errorf("non-canonical repository-relative path %q", value)
	}
	return nil
}

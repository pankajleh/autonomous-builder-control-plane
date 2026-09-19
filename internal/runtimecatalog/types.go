// Package runtimecatalog owns the bounded EP-006 service root and its
// immutable runtime registrations and active-owner records.
package runtimecatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/recovery"
)

const (
	MaxRuns             = 10_000
	MaxAttemptsPerRun   = 1_000
	MaxRegistrationSize = 64 << 10
	MaxCatalogReadBytes = 16 << 20
	MaxCatalogPageSize  = 200
	MaxRunIDIndexSize   = 257
	MaxActiveOwners     = 256
	LockTimeout         = 2 * time.Second
	CatalogSchemaV1     = "runtime-catalog-v1"

	maxDirectStorageComponentBytes = 240
	encodedStorageComponentPrefix  = "sha256-"
)

var (
	ErrUnsupported       = errors.New("runtime catalog strong filesystem guarantees are unsupported on this platform")
	ErrInvalidIdentifier = errors.New("invalid runtime catalog identifier")
	ErrIntegrity         = errors.New("runtime catalog integrity failure")
	ErrConflict          = errors.New("runtime catalog immutable record conflict")
	ErrBusy              = errors.New("runtime catalog lock acquisition timed out")
	ErrOwnerNotActive    = errors.New("active owner lease is unavailable")
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,255}$`)
var linuxBootIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ValidIdentifier reports whether an exact service-visible run or attempt ID
// is safe as one catalog path component.
func ValidIdentifier(value string) bool { return identifierPattern.MatchString(value) }

// ValidateIdentifier rejects traversal, separators, empty values and values
// outside the frozen 256-byte grammar.
func ValidateIdentifier(value string) error {
	if !ValidIdentifier(value) {
		return ErrInvalidIdentifier
	}
	return nil
}

// RunRegistrationV1 is immutable controller-owned discovery metadata. Paths
// in this record are internal and must never be copied to service DTOs.
type RunRegistrationV1 struct {
	Kind                         string             `json:"kind"`
	RunID                        string             `json:"run_id"`
	RepositoryIdentityDigest     string             `json:"repository_identity_digest"`
	AuthorityDigest              string             `json:"authority_digest"`
	CanonicalLedgerPath          string             `json:"canonical_ledger_path"`
	LedgerGeneration             LedgerGenerationV1 `json:"ledger_generation"`
	CanonicalEvidenceRoot        string             `json:"canonical_evidence_root"`
	InitialRegistrationTimestamp string             `json:"initial_registration_timestamp"`
}

// LedgerGenerationV1 is controller-owned immutable authority for the exact
// parent and file objects registered as one run's authoritative ledger. It is
// internal catalog data and is never copied to a service-visible DTO.
type LedgerGenerationV1 struct {
	Kind         string `json:"kind"`
	ParentDevice uint64 `json:"parent_device"`
	ParentInode  uint64 `json:"parent_inode"`
	FileDevice   uint64 `json:"file_device"`
	FileInode    uint64 `json:"file_inode"`
}

// Valid reports whether the generation contains one complete physical
// parent/file binding.
func (g LedgerGenerationV1) Valid() bool {
	return g.Kind == "LedgerGenerationV1" && g.ParentDevice != 0 && g.ParentInode != 0 && g.FileDevice != 0 && g.FileInode != 0
}

// AttemptRegistrationV1 independently binds one attempt to its run and
// admitted authority.
type AttemptRegistrationV1 struct {
	Kind                  string `json:"kind"`
	RunID                 string `json:"run_id"`
	AttemptID             string `json:"attempt_id"`
	AuthorityDigest       string `json:"authority_digest"`
	RegistrationTimestamp string `json:"registration_timestamp"`
}

type OwnerLeaseState string

const (
	OwnerLeaseActive  OwnerLeaseState = "ACTIVE"
	OwnerLeaseClosing OwnerLeaseState = "CLOSING"
	OwnerLeaseRetired OwnerLeaseState = "RETIRED"
)

// ActiveOwnerLeaseV1 is the durable, generation-bound exact process owner.
// DrainThroughJournalSequence is present only once the owner is closing.
type ActiveOwnerLeaseV1 struct {
	Kind                        string                   `json:"kind"`
	RunID                       string                   `json:"run_id"`
	AttemptID                   string                   `json:"attempt_id"`
	LeaseGeneration             uint64                   `json:"lease_generation"`
	Process                     recovery.ProcessIdentity `json:"process"`
	LeaseID                     string                   `json:"lease_id"`
	State                       OwnerLeaseState          `json:"state"`
	DrainThroughJournalSequence *uint64                  `json:"drain_through_journal_sequence,omitempty"`
}

func RepositoryIdentityDigest(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])
}

// storageComponent preserves the readable layout for ordinary identifiers and
// deterministically encodes values which cannot fit once record suffixes are
// added on common Linux filesystems. The encoding prefix is reserved so a
// service-visible identifier cannot alias another identifier's encoded name.
// The full validated ID remains in immutable storage and is rechecked against
// this digest on every read.
func storageComponent(identifier string) string {
	if len(identifier) <= maxDirectStorageComponentBytes && !strings.HasPrefix(identifier, encodedStorageComponentPrefix) {
		return identifier
	}
	sum := sha256.Sum256([]byte(identifier))
	return encodedStorageComponentPrefix + hex.EncodeToString(sum[:])
}

func NewRunRegistrationV1(runID, repositoryIdentity, authorityDigest, ledgerPath, evidenceRoot string, at time.Time) (RunRegistrationV1, error) {
	generation, err := observeRegisteredLedgerGeneration(ledgerPath)
	if err != nil {
		return RunRegistrationV1{}, fmt.Errorf("observe registered ledger generation: %w", err)
	}
	r := RunRegistrationV1{
		Kind: "RunRegistrationV1", RunID: runID,
		RepositoryIdentityDigest: RepositoryIdentityDigest(repositoryIdentity),
		AuthorityDigest:          authorityDigest, CanonicalLedgerPath: ledgerPath,
		LedgerGeneration:             generation,
		CanonicalEvidenceRoot:        evidenceRoot,
		InitialRegistrationTimestamp: canonicalTime(at),
	}
	return r, validateRunRegistration(r)
}

func NewAttemptRegistrationV1(runID, attemptID, authorityDigest string, at time.Time) (AttemptRegistrationV1, error) {
	r := AttemptRegistrationV1{Kind: "AttemptRegistrationV1", RunID: runID, AttemptID: attemptID, AuthorityDigest: authorityDigest, RegistrationTimestamp: canonicalTime(at)}
	return r, validateAttemptRegistration(r)
}

func canonicalTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func validateRunRegistration(r RunRegistrationV1) error {
	if r.Kind != "RunRegistrationV1" || ValidateIdentifier(r.RunID) != nil {
		return fmt.Errorf("%w: invalid run registration identity", ErrIntegrity)
	}
	if !isSHA256(r.RepositoryIdentityDigest) || !isSHA256(r.AuthorityDigest) {
		return fmt.Errorf("%w: invalid run registration digest", ErrIntegrity)
	}
	if !canonicalAbsolute(r.CanonicalLedgerPath) || !canonicalAbsolute(r.CanonicalEvidenceRoot) {
		return fmt.Errorf("%w: invalid registered controller path", ErrIntegrity)
	}
	if !r.LedgerGeneration.Valid() {
		return fmt.Errorf("%w: invalid registered ledger generation", ErrIntegrity)
	}
	if !validCanonicalTime(r.InitialRegistrationTimestamp) {
		return fmt.Errorf("%w: invalid registration timestamp", ErrIntegrity)
	}
	return nil
}

func validateAttemptRegistration(r AttemptRegistrationV1) error {
	if r.Kind != "AttemptRegistrationV1" || ValidateIdentifier(r.RunID) != nil || ValidateIdentifier(r.AttemptID) != nil {
		return fmt.Errorf("%w: invalid attempt registration identity", ErrIntegrity)
	}
	if !isSHA256(r.AuthorityDigest) || !validCanonicalTime(r.RegistrationTimestamp) {
		return fmt.Errorf("%w: invalid attempt registration", ErrIntegrity)
	}
	return nil
}

func validateLease(lease ActiveOwnerLeaseV1) error {
	if lease.Kind != "ActiveOwnerLeaseV1" || ValidateIdentifier(lease.RunID) != nil || ValidateIdentifier(lease.AttemptID) != nil || lease.LeaseGeneration == 0 {
		return fmt.Errorf("%w: invalid owner lease identity", ErrIntegrity)
	}
	if lease.Process.PID <= 0 || lease.Process.LinuxStartTicks == 0 || !linuxBootIDPattern.MatchString(lease.Process.BootID) {
		return fmt.Errorf("%w: incomplete owner process identity", ErrIntegrity)
	}
	if lease.LeaseID != leaseIdentityDigest(lease.RunID, lease.AttemptID, lease.LeaseGeneration, lease.Process) {
		return fmt.Errorf("%w: owner lease digest mismatch", ErrIntegrity)
	}
	switch lease.State {
	case OwnerLeaseActive:
		if lease.DrainThroughJournalSequence != nil {
			return fmt.Errorf("%w: active owner has a closing watermark", ErrIntegrity)
		}
	case OwnerLeaseClosing:
		if lease.DrainThroughJournalSequence == nil {
			return fmt.Errorf("%w: closing owner lacks a watermark", ErrIntegrity)
		}
	case OwnerLeaseRetired:
		if lease.DrainThroughJournalSequence == nil {
			return fmt.Errorf("%w: retired owner lacks a closing watermark", ErrIntegrity)
		}
	default:
		return fmt.Errorf("%w: invalid owner lease state", ErrIntegrity)
	}
	return nil
}

func leaseIdentityDigest(runID, attemptID string, generation uint64, process recovery.ProcessIdentity) string {
	identity := struct {
		Kind            string                   `json:"kind"`
		RunID           string                   `json:"run_id"`
		AttemptID       string                   `json:"attempt_id"`
		LeaseGeneration uint64                   `json:"lease_generation"`
		Process         recovery.ProcessIdentity `json:"process"`
	}{"ActiveOwnerLeaseIdentityV1", runID, attemptID, generation, process}
	data, _ := json.Marshal(identity)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func isSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func canonicalAbsolute(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value
}

func validCanonicalTime(value string) bool {
	t, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && value == canonicalTime(t)
}

func canonicalJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if len(data) > MaxRegistrationSize {
		return nil, fmt.Errorf("%w: record exceeds size bound", ErrIntegrity)
	}
	return data, nil
}

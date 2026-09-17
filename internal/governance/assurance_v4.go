package governance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// FailureDimension is the frozen, directly serialized V4 failure vocabulary.
// It deliberately has no prose-label normalization layer.
type FailureDimension string

const (
	FailureAuthorityIdentity                 FailureDimension = "AUTHORITY_IDENTITY"
	FailureCancellationAmbiguity             FailureDimension = "CANCELLATION_AMBIGUITY"
	FailureCompatibilityMigrationVersionSkew FailureDimension = "COMPATIBILITY_MIGRATION_VERSION_SKEW"
	FailureConcurrencyRaces                  FailureDimension = "CONCURRENCY_RACES"
	FailureCrashConsistency                  FailureDimension = "CRASH_CONSISTENCY"
	FailureExternalDependency                FailureDimension = "EXTERNAL_DEPENDENCY"
	FailureFilesystemIdentity                FailureDimension = "FILESYSTEM_IDENTITY"
	FailureIntegrationE2E                    FailureDimension = "INTEGRATION_E2E"
	FailureMalformedForgedInput              FailureDimension = "MALFORMED_FORGED_INPUT"
	FailurePartialFailure                    FailureDimension = "PARTIAL_FAILURE"
	FailurePhysicalResourceExhaustion        FailureDimension = "PHYSICAL_RESOURCE_EXHAUSTION"
	FailureRestartRecoveryRollback           FailureDimension = "RESTART_RECOVERY_ROLLBACK"
	FailureRetryReplayIdempotency            FailureDimension = "RETRY_REPLAY_IDEMPOTENCY"
	FailureSecurityPrivacyBoundary           FailureDimension = "SECURITY_PRIVACY_BOUNDARY"
)

var allFailureDimensions = []FailureDimension{
	FailureAuthorityIdentity,
	FailureCancellationAmbiguity,
	FailureCompatibilityMigrationVersionSkew,
	FailureConcurrencyRaces,
	FailureCrashConsistency,
	FailureExternalDependency,
	FailureFilesystemIdentity,
	FailureIntegrationE2E,
	FailureMalformedForgedInput,
	FailurePartialFailure,
	FailurePhysicalResourceExhaustion,
	FailureRestartRecoveryRollback,
	FailureRetryReplayIdempotency,
	FailureSecurityPrivacyBoundary,
}

// FailureDimensions returns a defensive copy in lexical wire order.
func FailureDimensions() []FailureDimension {
	return append([]FailureDimension(nil), allFailureDimensions...)
}

func (dimension FailureDimension) Valid() bool {
	index := sort.Search(len(allFailureDimensions), func(index int) bool {
		return allFailureDimensions[index] >= dimension
	})
	return index < len(allFailureDimensions) && allFailureDimensions[index] == dimension
}

// ScenarioV1 is the frozen nested assurance-model failure scenario.
type ScenarioV1 struct {
	ID           string           `json:"id"`
	Dimension    FailureDimension `json:"dimension"`
	Scenario     string           `json:"scenario"`
	InvariantIDs []string         `json:"invariant_ids"`
	ProofIDs     []string         `json:"proof_ids"`
}

func (scenario ScenarioV1) Validate() error {
	if !validStableID(scenario.ID, "AG-S-", 1, 70) {
		return errors.New("scenario ID is outside the frozen AG-S-001..070 registry")
	}
	if !scenario.Dimension.Valid() {
		return fmt.Errorf("scenario %s has invalid failure dimension %q", scenario.ID, scenario.Dimension)
	}
	if scenario.Scenario == "" || len(scenario.Scenario) > 4096 {
		return fmt.Errorf("scenario %s has invalid scenario text", scenario.ID)
	}
	if err := validateStableIDSet(scenario.InvariantIDs, "AG-I", 1, 39); err != nil {
		return fmt.Errorf("scenario %s invariants: %w", scenario.ID, err)
	}
	if err := validateStableIDSet(scenario.ProofIDs, "AG-PO-", 1, 64); err != nil {
		return fmt.Errorf("scenario %s proofs: %w", scenario.ID, err)
	}
	return nil
}

// CanonicalJSON returns the exact field-ordered ScenarioV1 wire.
func (scenario ScenarioV1) CanonicalJSON() ([]byte, error) {
	if err := scenario.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(scenario)
}

// FrozenFailureScenariosV1 returns all 70 direct wire records. The registry is
// parsed from a generated copy of the accepted A table and validated on every
// call before any caller can use it as authority.
func FrozenFailureScenariosV1() ([]ScenarioV1, error) {
	result := make([]ScenarioV1, 0, len(frozenScenarioRows))
	dimensions := make(map[FailureDimension]struct{}, len(allFailureDimensions))
	for index, row := range frozenScenarioRows {
		cells := strings.Split(row, "|")
		if len(cells) != 5 {
			return nil, fmt.Errorf("frozen scenario row %d has %d cells", index+1, len(cells))
		}
		scenario := ScenarioV1{
			ID:           cells[0],
			Dimension:    FailureDimension(cells[1]),
			Scenario:     cells[2],
			InvariantIDs: splitNonEmpty(cells[3]),
			ProofIDs:     splitNonEmpty(cells[4]),
		}
		if want := fmt.Sprintf("AG-S-%03d", index+1); scenario.ID != want {
			return nil, fmt.Errorf("frozen scenario row %d is %s, want %s", index+1, scenario.ID, want)
		}
		if err := scenario.Validate(); err != nil {
			return nil, err
		}
		dimensions[scenario.Dimension] = struct{}{}
		result = append(result, scenario)
	}
	if len(result) != 70 || len(dimensions) != len(allFailureDimensions) {
		return nil, fmt.Errorf("frozen scenario registry has %d rows and %d dimensions", len(result), len(dimensions))
	}
	return result, nil
}

// CanonicalFailureScenariosV1 serializes the complete registry without an
// intermediate dimension-label mapping.
func CanonicalFailureScenariosV1() ([]byte, error) {
	scenarios, err := FrozenFailureScenariosV1()
	if err != nil {
		return nil, err
	}
	return json.Marshal(scenarios)
}

func splitNonEmpty(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}

func validStableID(value, prefix string, minimum, maximum int) bool {
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	number, err := strconv.Atoi(strings.TrimPrefix(value, prefix))
	return err == nil && number >= minimum && number <= maximum
}

func validateStableIDSet(values []string, prefix string, minimum, maximum int) error {
	if len(values) == 0 || len(values) > 256 {
		return errors.New("stable ID set must contain between 1 and 256 members")
	}
	previous := ""
	for _, value := range values {
		if value <= previous || !validStableID(value, prefix, minimum, maximum) {
			return fmt.Errorf("stable ID set is not sorted, unique, or in range at %q", value)
		}
		previous = value
	}
	return nil
}

// SHA256Canonical returns the lowercase digest of an already validated
// canonical wire. It rejects non-canonical encodings before hashing.
func SHA256Canonical(data []byte, target any) (string, error) {
	if err := ParseCanonical(data, target); err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

// Generated from the accepted A scenario table; populated below.
var frozenScenarioRows = []string{
	"AG-S-001|CRASH_CONSISTENCY|process dies after successor policy artifact durability but before activation CAS|AG-I01,AG-I10|AG-PO-001,AG-PO-003,AG-PO-007,AG-PO-020,AG-PO-038",
	"AG-S-002|CRASH_CONSISTENCY|process dies after successor activation CAS response is lost/ambiguous|AG-I01,AG-I10,AG-I14|AG-PO-003,AG-PO-020,AG-PO-021,AG-PO-038",
	"AG-S-003|CRASH_CONSISTENCY|process dies while publishing A model/grant/checkpoint artifact before controller CAS|AG-I02,AG-I10,AG-I13|AG-PO-007,AG-PO-009,AG-PO-020",
	"AG-S-004|CRASH_CONSISTENCY|process dies while publishing evidence/stage checkpoint|AG-I05,AG-I10,AG-I16|AG-PO-007,AG-PO-014,AG-PO-020,AG-PO-033",
	"AG-S-005|CRASH_CONSISTENCY|process dies while publishing failed-C record or correction-B grant/counter|AG-I07,AG-I10|AG-PO-017,AG-PO-018,AG-PO-019,AG-PO-020",
	"AG-S-006|CONCURRENCY_RACES|two successor activation installations race|AG-I01,AG-I10|AG-PO-003,AG-PO-020,AG-PO-038",
	"AG-S-007|CONCURRENCY_RACES|competing A→B/B→C/correction derivations race|AG-I07,AG-I10,AG-I13|AG-PO-009,AG-PO-010,AG-PO-011,AG-PO-019,AG-PO-020",
	"AG-S-008|CONCURRENCY_RACES|evidence/stage writers or resource reservations race|AG-I05,AG-I10,AG-I12,AG-I16|AG-PO-014,AG-PO-020,AG-PO-032,AG-PO-033",
	"AG-S-009|RETRY_REPLAY_IDEMPOTENCY|exact activation/model/grant/checkpoint/evidence request is retried|AG-I08,AG-I10,AG-I16|AG-PO-003,AG-PO-007,AG-PO-033",
	"AG-S-010|RETRY_REPLAY_IDEMPOTENCY|conflicting duplicate durable record uses same logical identity with different bytes|AG-I08,AG-I10|AG-PO-004,AG-PO-007",
	"AG-S-011|RETRY_REPLAY_IDEMPOTENCY|failed-C/correction request is replayed after success/restart|AG-I07,AG-I12|AG-PO-017,AG-PO-018,AG-PO-019",
	"AG-S-012|AUTHORITY_IDENTITY|policy/model/matrix/candidate bytes or digest are stale/replaced|AG-I01,AG-I09,AG-I13|AG-PO-001,AG-PO-009,AG-PO-010,AG-PO-011,AG-PO-023,AG-PO-038",
	"AG-S-013|AUTHORITY_IDENTITY|V4 activation is attempted while predecessor V3 is not drained, or historical V3 authority is presented operationally after V4 activation|AG-I01,AG-I08,AG-I09,AG-I25|AG-PO-002,AG-PO-005,AG-PO-023,AG-PO-049",
	"AG-S-014|PARTIAL_FAILURE|content-addressed child is durable but no controller-state CAS references it|AG-I10|AG-PO-007,AG-PO-020",
	"AG-S-015|PARTIAL_FAILURE|controller state attempts to reference missing/conflicting child artifact|AG-I09,AG-I10|AG-PO-007,AG-PO-020,AG-PO-023",
	"AG-S-016|CANCELLATION_AMBIGUITY|cancellation occurs before durable child artifact|AG-I14|AG-PO-021",
	"AG-S-017|CANCELLATION_AMBIGUITY|cancellation/timeout occurs after CAS may have been submitted|AG-I10,AG-I14|AG-PO-020,AG-PO-021",
	"AG-S-018|CANCELLATION_AMBIGUITY|validator times out after partial stdout/stderr/evidence|AG-I05,AG-I12,AG-I14,AG-I16|AG-PO-014,AG-PO-021,AG-PO-025,AG-PO-033",
	"AG-S-019|PHYSICAL_RESOURCE_EXHAUSTION|assurance model/proof/evidence cardinality or bytes exceed profile|AG-I08,AG-I12|AG-PO-004,AG-PO-025,AG-PO-032",
	"AG-S-020|PHYSICAL_RESOURCE_EXHAUSTION|candidate/result churn attempts to reset validator/retry/evidence ceilings|AG-I12|AG-PO-019,AG-PO-025,AG-PO-032",
	"AG-S-021|PHYSICAL_RESOURCE_EXHAUSTION|validator leaks descriptors/processes or exceeds memory/output/time/fan-out|AG-I12|AG-PO-025,AG-PO-032",
	"AG-S-022|RESTART_RECOVERY_ROLLBACK|fresh process opens after any durable publication boundary|AG-I09,AG-I10,AG-I18|AG-PO-020,AG-PO-023,AG-PO-034",
	"AG-S-023|MALFORMED_FORGED_INPUT|duplicate/unknown fields, invalid IDs/stages, forged digests, or oversized canonical input|AG-I02,AG-I08,AG-I12|AG-PO-004,AG-PO-006,AG-PO-025",
	"AG-S-024|FILESYSTEM_IDENTITY|repository/controller path, symlink, or object identity is substituted|AG-I09,AG-I11|AG-PO-023,AG-PO-024",
	"AG-S-025|EXTERNAL_DEPENDENCY|authoritative Git object/ref or PostgreSQL backend is unavailable/delayed|AG-I09,AG-I14,AG-I18|AG-PO-022,AG-PO-023,AG-PO-034",
	"AG-S-026|EXTERNAL_DEPENDENCY|Git/ref/backend observation changes, duplicates, or is inconsistent between observation/use|AG-I09,AG-I14,AG-I18|AG-PO-022,AG-PO-023,AG-PO-034",
	"AG-S-027|COMPATIBILITY_MIGRATION_VERSION_SKEW|activation is attempted while predecessor V3 has a nonterminal lineage or unresolved effect|AG-I01,AG-I08,AG-I25|AG-PO-002,AG-PO-005,AG-PO-049",
	"AG-S-028|COMPATIBILITY_MIGRATION_VERSION_SKEW|drained historical V3 authority is replayed operationally after V4 activation|AG-I01,AG-I08,AG-I09,AG-I25|AG-PO-005,AG-PO-023,AG-PO-049",
	"AG-S-029|SECURITY_PRIVACY_BOUNDARY|another repository/controller identity reuses assurance/grant/evidence/backend namespace|AG-I09,AG-I11,AG-I18|AG-PO-012,AG-PO-023,AG-PO-024,AG-PO-034",
	"AG-S-030|MALFORMED_FORGED_INPUT|B attempts to change/N-A/weaken model, matrix, stage, validator, or journey|AG-I02,AG-I06|AG-PO-006,AG-PO-013,AG-PO-027",
	"AG-S-031|SECURITY_PRIVACY_BOUNDARY|C reviewer/finding attempts direct repository mutation|AG-I03,AG-I07|AG-PO-017,AG-PO-018",
	"AG-S-032|MALFORMED_FORGED_INPUT|missing/misclassified scenario or evidence requirement attempts correction B|AG-I06,AG-I07|AG-PO-013,AG-PO-026",
	"AG-S-033|SECURITY_PRIVACY_BOUNDARY|governance/runtime-affecting diff claims `documentation_only`|AG-I04,AG-I09|AG-PO-008,AG-PO-023",
	"AG-S-034|AUTHORITY_IDENTITY|evidence from wrong stage/subject/weaker class/attempt tries to satisfy a cell|AG-I05,AG-I09,AG-I16|AG-PO-014,AG-PO-015,AG-PO-016,AG-PO-027,AG-PO-033",
	"AG-S-035|INTEGRATION_E2E|mock/fake substitutes for required production-composition boundary|AG-I05|AG-PO-015,AG-PO-016,AG-PO-028,AG-PO-030,AG-PO-034",
	"AG-S-036|INTEGRATION_E2E|assembled CLI/controller/Ralphex journey disagrees with isolated tests|AG-I02,AG-I05,AG-I09|AG-PO-015,AG-PO-016,AG-PO-029,AG-PO-030,AG-PO-035",
	"AG-S-037|PHYSICAL_RESOURCE_EXHAUSTION|two correction-B grants succeed, then a third is attempted after restart/new C|AG-I07,AG-I12|AG-PO-011,AG-PO-018,AG-PO-019",
	"AG-S-038|AUTHORITY_IDENTITY|foreseeable Critical/Major first appears in C and continuation is attempted without new A|AG-I06,AG-I07,AG-I09|AG-PO-026",
	"AG-S-039|AUTHORITY_IDENTITY|durable grant/checkpoint is reused after its state successor invalidates it, or an expired execution/mutation lease is replayed|AG-I09,AG-I15|AG-PO-031",
	"AG-S-040|PHYSICAL_RESOURCE_EXHAUSTION|process dies/competes after resource reservation but before validator finalization/cleanup|AG-I10,AG-I12,AG-I16|AG-PO-020,AG-PO-032,AG-PO-033",
	"AG-S-041|RETRY_REPLAY_IDEMPOTENCY|multiple validator attempts exist for one cell/subject and a stale/failed attempt is selected|AG-I05,AG-I16|AG-PO-014,AG-PO-033",
	"AG-S-042|RESTART_RECOVERY_ROLLBACK|PostgreSQL CAS/bootstrap/auth namespace is missing, duplicated, unavailable, or returns ambiguous commit outcome|AG-I09,AG-I10,AG-I18|AG-PO-020,AG-PO-034",
	"AG-S-043|SECURITY_PRIVACY_BOUNDARY|integration dogfood attempts nested dogfood or resets/escapes parent counters/containment|AG-I12,AG-I16|AG-PO-025,AG-PO-032,AG-PO-035",
	"AG-S-044|INTEGRATION_E2E|named Go validator exits zero while zero expected test events actually ran|AG-I05,AG-I16|AG-PO-033,AG-PO-036",
	"AG-S-045|CONCURRENCY_RACES|integration fails or C invalidates concurrently with merge authorization/publication/post-merge|AG-I13,AG-I17,AG-I20|AG-PO-016,AG-PO-017,AG-PO-037,AG-PO-043",
	"AG-S-046|SECURITY_PRIVACY_BOUNDARY|credential-bearing Codex/PostgreSQL execution prints, copies, or exposes credential bytes to a tool subprocess, diff, or retained evidence|AG-I11,AG-I19|AG-PO-041",
	"AG-S-047|CONCURRENCY_RACES|C invalidation races the last controller decision before the provider merge effect, or the provider result is ambiguous after lease issuance|AG-I10,AG-I17,AG-I20|AG-PO-020,AG-PO-037,AG-PO-043",
	"AG-S-048|COMPATIBILITY_MIGRATION_VERSION_SKEW|drained historical V3 bytes are reinterpreted as V4 authority or used to mint a V4 descendant without the successor activation/A chain|AG-I01,AG-I08,AG-I09,AG-I25|AG-PO-002,AG-PO-005,AG-PO-042,AG-PO-049",
	"AG-S-049|MALFORMED_FORGED_INPUT|syntactically valid registries contain semantically disconnected or duplicate scenario→invariant→proof→cell→validator links|AG-I02,AG-I05|AG-PO-006,AG-PO-040",
	"AG-S-050|AUTHORITY_IDENTITY|a depth-1 child activation escapes its parent reservation/repository/budget or attempts publication, merge, or nested dogfood|AG-I09,AG-I12,AG-I16|AG-PO-035,AG-PO-044",
	"AG-S-051|CONCURRENCY_RACES|two hosts/processes observe the same merge-authorized tip and both attempt provider submission|AG-I20,AG-I21|AG-PO-043,AG-PO-045",
	"AG-S-052|SECURITY_PRIVACY_BOUNDARY|one runtime credential directly selects/updates another authority domain/controller row or artifact|AG-I11,AG-I18,AG-I22|AG-PO-046",
	"AG-S-053|RESTART_RECOVERY_ROLLBACK|fresh controller host has state digests but cannot load predecessor/model/grant/evidence bytes|AG-I09,AG-I23|AG-PO-050",
	"AG-S-054|COMPATIBILITY_MIGRATION_VERSION_SKEW|V4 activation occurs while predecessor V3 has active invocation/grant/lease/nonterminal checkpoint|AG-I08,AG-I25|AG-PO-049",
	"AG-S-055|CONCURRENCY_RACES|C/integration/ledger invalidation races PR claim/call admission, or process dies on either side of the call-admission boundary|AG-I17,AG-I21,AG-I24,AG-I26|AG-PO-054,AG-PO-055",
	"AG-S-056|CRASH_CONSISTENCY|ledger READY/MERGED/FAILED state and PostgreSQL stage tip diverge across crash boundary|AG-I10,AG-I17,AG-I24|AG-PO-055",
	"AG-S-057|PHYSICAL_RESOURCE_EXHAUSTION|caller selects a weaker resource/secret profile or container escapes aggregate reservation|AG-I12,AG-I16,AG-I29|AG-PO-051",
	"AG-S-058|SECURITY_PRIVACY_BOUNDARY|B, integration, post-merge, failed or orphan evidence containing a credential is omitted from later scan|AG-I19,AG-I28|AG-PO-041,AG-PO-052",
	"AG-S-059|AUTHORITY_IDENTITY|J-05 reuses parent ABCP assurance model for toy repository or runs Ralphex without exact frozen plan/capsule authority|AG-I09,AG-I30|AG-PO-044,AG-PO-053",
	"AG-S-060|AUTHORITY_IDENTITY|reviewer/finding packet supplies correction paths not derivable from frozen A semantic registry|AG-I07,AG-I27|AG-PO-018,AG-PO-048",
	"AG-S-061|RETRY_REPLAY_IDEMPOTENCY|same digest/logical artifact identity is absent, cross-domain, oversized, or resolves to conflicting bytes|AG-I08,AG-I10,AG-I23|AG-PO-007,AG-PO-050",
	"AG-S-062|AUTHORITY_IDENTITY|identity is read with credential A but mutation uses credential B, GitHub App identity is mistaken for installation repository scope, or wrong principal/repository/ref/document/route bytes attempt APPLIED|AG-I21,AG-I31|AG-PO-054,AG-PO-056",
	"AG-S-063|SECURITY_PRIVACY_BOUNDARY|an opaque/foreign commitment, constructor-chosen random/time field, byte-different event, or competing FAILED/CANCELLED/other terminal is presented through the active barrier|AG-I24,AG-I32|AG-PO-055,AG-PO-057",
	"AG-S-064|CRASH_CONSISTENCY|process is lost after result durability but before settlement, or after CALL_POSSIBLE before result, and recovery cannot locate the result/prove absence or fabricates a response|AG-I14,AG-I21,AG-I33|AG-PO-045,AG-PO-055,AG-PO-058",
	"AG-S-065|INTEGRATION_E2E|a PR request depends on READY bytes that depend on that request, integration emits READY before PR APPLIED, or merge authority consumes non-PR-derived readiness|AG-I17,AG-I24,AG-I34|AG-PO-037,AG-PO-054,AG-PO-059",
	"AG-S-066|SECURITY_PRIVACY_BOUNDARY|a fresh bootstrap has wrong PostgreSQL creator-membership/SET semantics, skips separate final migrator hardening, lacks a function body/wire, lets definer `current_user` break domain lookup, bypasses forced RLS, or accesses another runtime domain|AG-I18,AG-I22,AG-I35|AG-PO-046,AG-PO-060",
	"AG-S-067|RESTART_RECOVERY_ROLLBACK|a generic absence blob, stale/wrong-host PID probe or unsigned observer claim abandons an ACTIVE publisher, close blocks forever, or abandon races a late append|AG-I10,AG-I23,AG-I36|AG-PO-050,AG-PO-061",
	"AG-S-068|AUTHORITY_IDENTITY|scan uses an empty/unrelated/reversed/stale base-candidate pair while repeating the authorized stage subject digest|AG-I19,AG-I28,AG-I37|AG-PO-052,AG-PO-062",
	"AG-S-069|PHYSICAL_RESOURCE_EXHAUSTION|a root has no representable direct allocation, split profiles omit combined needs, cross-category totals exceed the worker, or J-05 children/siblings consume more than the immutable parent envelope|AG-I12,AG-I29,AG-I38|AG-PO-035,AG-PO-051,AG-PO-063",
	"AG-S-070|MALFORMED_FORGED_INPUT|a relative path is used as a host path, wire identities cannot fit DDL, or an ambiguous field/digest/bound/literal rejection reaches implementation/vector generation|AG-I08,AG-I11,AG-I39|AG-PO-024,AG-PO-042,AG-PO-046,AG-PO-047,AG-PO-064",
}

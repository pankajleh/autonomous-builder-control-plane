package governance

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	CanonicalVectorDescriptorVersion = "CANONICAL-VECTOR-CATALOG-V3"
	CanonicalPositiveRecipe          = "MINIMAL-VALID-V3"
)

type WireJSONType string

const (
	WireString  WireJSONType = "STRING"
	WireInteger WireJSONType = "INTEGER"
	WireBoolean WireJSONType = "BOOLEAN"
	WireObject  WireJSONType = "OBJECT"
	WireArray   WireJSONType = "ARRAY"
	WireRawJSON WireJSONType = "RAW_JSON"
)

type PredicateOperator string

const (
	PredicateAllEqual        PredicateOperator = "ALL_EQUAL"
	PredicateExactLiteral    PredicateOperator = "EXACT_LITERAL"
	PredicateIfAndOnlyIf     PredicateOperator = "IF_AND_ONLY_IF"
	PredicateImplies         PredicateOperator = "IMPLIES"
	PredicateExactlyOne      PredicateOperator = "EXACTLY_ONE"
	PredicateSubset          PredicateOperator = "SUBSET"
	PredicateOrdinalSuccess  PredicateOperator = "ORDINAL_SUCCESSOR"
	PredicateStateTransition PredicateOperator = "STATE_TRANSITION"
	PredicateTypedReference  PredicateOperator = "TYPED_REFERENCE"
	PredicateDigestPreimage  PredicateOperator = "DIGEST_PREIMAGE"
	PredicateDerivation      PredicateOperator = "DERIVATION"
	PredicateAggregateLEQ    PredicateOperator = "AGGREGATE_LEQ"
)

type CanonicalRequiredError string

const (
	CanonicalNonCanonical     CanonicalRequiredError = "NON_CANONICAL"
	CanonicalUnknownField     CanonicalRequiredError = "UNKNOWN_FIELD"
	CanonicalDuplicateField   CanonicalRequiredError = "DUPLICATE_FIELD"
	CanonicalMissingField     CanonicalRequiredError = "MISSING_FIELD"
	CanonicalTypeInvalid      CanonicalRequiredError = "TYPE_INVALID"
	CanonicalBoundInvalid     CanonicalRequiredError = "BOUND_INVALID"
	CanonicalEnumInvalid      CanonicalRequiredError = "ENUM_INVALID"
	CanonicalDigestInvalid    CanonicalRequiredError = "DIGEST_INVALID"
	CanonicalReferenceInvalid CanonicalRequiredError = "REFERENCE_TYPE_INVALID"
	CanonicalOrderInvalid     CanonicalRequiredError = "ORDER_INVALID"
	CanonicalPredicateInvalid CanonicalRequiredError = "PREDICATE_INVALID"
)

// WireFieldDescriptorV1 is the complete, store-compatible field description
// frozen by AG-PO-047/064. Pointer bounds distinguish an absent constraint
// from an explicit zero lower bound.
type WireFieldDescriptorV1 struct {
	SchemaID     string       `json:"schema_id"`
	FieldPath    string       `json:"field_path"`
	Ordinal      uint64       `json:"ordinal"`
	JSONType     WireJSONType `json:"json_type"`
	ValueType    string       `json:"value_type"`
	MinU64       *uint64      `json:"min_u64,omitempty"`
	MaxU64       *uint64      `json:"max_u64,omitempty"`
	MinBytes     *uint64      `json:"min_bytes,omitempty"`
	MaxBytes     *uint64      `json:"max_bytes,omitempty"`
	MinItems     *uint64      `json:"min_items,omitempty"`
	MaxItems     *uint64      `json:"max_items,omitempty"`
	RecordTarget string       `json:"record_target,omitempty"`
	DigestTarget string       `json:"digest_target,omitempty"`
	Optional     bool         `json:"optional"`
	LiteralValue string       `json:"literal_value,omitempty"`
}

type PredicateDescriptorV1 struct {
	PredicateID   string                 `json:"predicate_id"`
	SchemaID      string                 `json:"schema_id"`
	FieldPaths    []string               `json:"field_paths"`
	Operator      PredicateOperator      `json:"operator"`
	Arguments     []string               `json:"arguments"`
	RequiredError CanonicalRequiredError `json:"required_error"`
}

type CanonicalRejectionVectorV1 struct {
	VectorID      string                 `json:"vector_id"`
	Mutation      string                 `json:"mutation"`
	RequiredError CanonicalRequiredError `json:"required_error"`
}

type CanonicalVectorEntryV1 struct {
	SchemaID       string                       `json:"schema_id"`
	RecordName     string                       `json:"record_name"`
	Fields         []WireFieldDescriptorV1      `json:"fields"`
	Predicates     []PredicateDescriptorV1      `json:"predicates"`
	PositiveRecipe string                       `json:"positive_recipe"`
	Rejections     []CanonicalRejectionVectorV1 `json:"rejections"`
}

type CanonicalVectorCatalogV1 struct {
	Kind              string                   `json:"kind"`
	SchemaVersion     string                   `json:"schema_version"`
	DescriptorVersion string                   `json:"descriptor_version"`
	Entries           []CanonicalVectorEntryV1 `json:"entries"`
	CatalogSHA256     string                   `json:"catalog_sha256"`
}

// WireFieldSpecV1 is the extraction input for one source-of-truth field token.
type WireFieldSpecV1 struct {
	FieldPath string
	Type      string
	Optional  bool
}

type WirePredicateSpecV1 struct {
	PredicateID string
	FieldPaths  []string
	Operator    PredicateOperator
	Arguments   []string
}

type WireSchemaDefinitionV1 struct {
	SchemaID   string
	RecordName string
	Nested     bool
	Fields     []WireFieldSpecV1
	Predicates []WirePredicateSpecV1
}

// BuildCanonicalVectorCatalogV1 resolves definitions, rejects every
// unbounded/untyped/unresolved token, and deterministically emits positive and
// per-constraint rejection recipes.
func BuildCanonicalVectorCatalogV1(definitions []WireSchemaDefinitionV1) (CanonicalVectorCatalogV1, error) {
	if len(definitions) == 0 || len(definitions) > 256 {
		return CanonicalVectorCatalogV1{}, errors.New("wire catalog must contain between 1 and 256 schema definitions")
	}
	definitions = append([]WireSchemaDefinitionV1(nil), definitions...)
	sort.Slice(definitions, func(i, j int) bool { return definitions[i].SchemaID < definitions[j].SchemaID })
	entries := make([]CanonicalVectorEntryV1, 0, len(definitions))
	previous := ""
	for _, definition := range definitions {
		if !validID(definition.SchemaID) && !strings.HasPrefix(definition.SchemaID, "nested:") {
			return CanonicalVectorCatalogV1{}, fmt.Errorf("invalid schema ID %q", definition.SchemaID)
		}
		if definition.SchemaID <= previous {
			return CanonicalVectorCatalogV1{}, fmt.Errorf("duplicate or unsorted schema ID %q", definition.SchemaID)
		}
		previous = definition.SchemaID
		entry, err := buildCanonicalVectorEntry(definition)
		if err != nil {
			return CanonicalVectorCatalogV1{}, fmt.Errorf("schema %s: %w", definition.SchemaID, err)
		}
		entries = append(entries, entry)
	}
	return SealCanonicalVectorCatalogV1(CanonicalVectorCatalogV1{
		Kind:              "CanonicalVectorCatalogV1",
		SchemaVersion:     "canonical-vector-catalog-v1",
		DescriptorVersion: CanonicalVectorDescriptorVersion,
		Entries:           entries,
	})
}

func buildCanonicalVectorEntry(definition WireSchemaDefinitionV1) (CanonicalVectorEntryV1, error) {
	if !validID(definition.RecordName) || len(definition.Fields) == 0 || len(definition.Fields) > 256 {
		return CanonicalVectorEntryV1{}, errors.New("record name or field cardinality is invalid")
	}
	fields := make([]WireFieldDescriptorV1, 0, len(definition.Fields))
	predicates := make([]PredicateDescriptorV1, 0, len(definition.Fields)+len(definition.Predicates))
	seenFields := make(map[string]struct{}, len(definition.Fields))
	fieldOrdinals := make(map[string]int, len(definition.Fields))
	for index, specification := range definition.Fields {
		if specification.FieldPath == "" || len(specification.FieldPath) > 512 {
			return CanonicalVectorEntryV1{}, fmt.Errorf("field %d has invalid path", index+1)
		}
		if _, duplicate := seenFields[specification.FieldPath]; duplicate {
			return CanonicalVectorEntryV1{}, fmt.Errorf("duplicate field %s", specification.FieldPath)
		}
		seenFields[specification.FieldPath] = struct{}{}
		fieldOrdinals[specification.FieldPath] = index
		descriptor, err := ResolveWireFieldDescriptorV1(definition.SchemaID, specification.FieldPath, uint64(index+1), specification.Type, specification.Optional)
		if err != nil {
			return CanonicalVectorEntryV1{}, err
		}
		fields = append(fields, descriptor)
		if descriptor.LiteralValue != "" {
			predicates = append(predicates, PredicateDescriptorV1{
				PredicateID: "P-" + definition.SchemaID + "-EXACT_LITERAL-" + specification.FieldPath,
				SchemaID:    definition.SchemaID, FieldPaths: []string{specification.FieldPath},
				Operator: PredicateExactLiteral, Arguments: []string{descriptor.LiteralValue}, RequiredError: CanonicalPredicateInvalid,
			})
		}
		if descriptor.DigestTarget != "" {
			operator := PredicateTypedReference
			if isSelfDigestDescriptorV1(definition.RecordName, definition.SchemaID, definition.Fields, index, descriptor) {
				operator = PredicateDigestPreimage
			}
			predicates = append(predicates, PredicateDescriptorV1{
				PredicateID: "P-" + definition.SchemaID + "-" + string(operator) + "-" + specification.FieldPath,
				SchemaID:    definition.SchemaID, FieldPaths: []string{specification.FieldPath}, Operator: operator,
				Arguments: []string{descriptor.DigestTarget}, RequiredError: CanonicalPredicateInvalid,
			})
		}
	}
	for _, specification := range definition.Predicates {
		if !validPredicateOperator(specification.Operator) || specification.PredicateID == "" || len(specification.FieldPaths) == 0 || len(specification.FieldPaths) > 64 {
			return CanonicalVectorEntryV1{}, fmt.Errorf("predicate %s is incomplete", specification.PredicateID)
		}
		paths := append([]string(nil), specification.FieldPaths...)
		sort.Slice(paths, func(i, j int) bool {
			if paths[i] == "$" {
				return true
			}
			if paths[j] == "$" {
				return false
			}
			return fieldOrdinals[paths[i]] < fieldOrdinals[paths[j]]
		})
		for _, path := range paths {
			if path != "$" {
				if _, exists := seenFields[path]; !exists {
					return CanonicalVectorEntryV1{}, fmt.Errorf("predicate %s names unknown field %s", specification.PredicateID, path)
				}
			}
		}
		predicates = append(predicates, PredicateDescriptorV1{specification.PredicateID, definition.SchemaID, paths, specification.Operator, append([]string(nil), specification.Arguments...), CanonicalPredicateInvalid})
	}
	sort.Slice(predicates, func(i, j int) bool { return predicates[i].PredicateID < predicates[j].PredicateID })
	for index := 1; index < len(predicates); index++ {
		if predicates[index-1].PredicateID == predicates[index].PredicateID {
			return CanonicalVectorEntryV1{}, fmt.Errorf("duplicate predicate ID %s", predicates[index].PredicateID)
		}
	}
	rejections := generateCanonicalRejectionVectorsV1(definition.SchemaID, definition.RecordName, fields, predicates)
	if definition.RecordName == "ArtifactBlobV1" && definition.SchemaID == "artifact-blob-v1" {
		rejections = []CanonicalRejectionVectorV1{
			{definition.SchemaID + "/$/BELOW_MIN", "BELOW_MIN", CanonicalBoundInvalid},
			{definition.SchemaID + "/$/ABOVE_MAX", "ABOVE_MAX", CanonicalBoundInvalid},
		}
		predicates = nil
	}
	return CanonicalVectorEntryV1{definition.SchemaID, definition.RecordName, fields, predicates, CanonicalPositiveRecipe, rejections}, nil
}

// ResolveWireFieldDescriptorV1 resolves one explicit or lexicon-inferred field
// and fails closed when its JSON type, bound, record target, or digest target
// is not frozen.
func ResolveWireFieldDescriptorV1(schemaID, fieldPath string, ordinal uint64, sourceType string, optional bool) (WireFieldDescriptorV1, error) {
	descriptor := WireFieldDescriptorV1{SchemaID: schemaID, FieldPath: fieldPath, Ordinal: ordinal, Optional: optional}
	sourceType = strings.TrimSpace(sourceType)
	if sourceType == "" {
		sourceType = inferBareWireType(fieldPath)
	}
	if sourceType == "" || strings.HasPrefix(sourceType, "[]") && !strings.Contains(sourceType, "(") && !strings.HasPrefix(sourceType, "[]\"") {
		return descriptor, fmt.Errorf("field %s is untyped or has a bare array", fieldPath)
	}
	if strings.HasPrefix(sourceType, "[") && strings.HasSuffix(sourceType, "]") {
		elements := splitTopLevel(strings.TrimSuffix(strings.TrimPrefix(sourceType, "["), "]"), ',')
		if len(elements) == 1 && strings.TrimSpace(elements[0]) == "" {
			elements = nil
		}
		descriptor.JSONType, descriptor.ValueType, descriptor.LiteralValue = WireArray, "exact-literal-array", sourceType
		descriptor.MinItems, descriptor.MaxItems = u64ptr(uint64(len(elements))), u64ptr(uint64(len(elements)))
		return descriptor, nil
	}

	if literal, primitive, ok := splitWireLiteral(sourceType); ok {
		descriptor.LiteralValue = literal
		sourceType = primitive
	}

	if strings.HasPrefix(sourceType, "[]") {
		return resolveArrayDescriptor(descriptor, sourceType)
	}
	if strings.HasPrefix(sourceType, "sha256<") && strings.HasSuffix(sourceType, ">") {
		target := strings.TrimSuffix(strings.TrimPrefix(sourceType, "sha256<"), ">")
		if target == "" {
			return descriptor, fmt.Errorf("field %s has unresolved digest target", fieldPath)
		}
		descriptor.JSONType, descriptor.ValueType, descriptor.DigestTarget = WireString, sourceType, target
		descriptor.MinBytes, descriptor.MaxBytes = u64ptr(64), u64ptr(64)
		return descriptor, nil
	}
	if sourceType == "blob256" {
		descriptor.JSONType, descriptor.ValueType, descriptor.DigestTarget = WireString, sourceType, "exact-bytes"
		descriptor.MinBytes, descriptor.MaxBytes = u64ptr(64), u64ptr(64)
		return descriptor, nil
	}
	if strings.HasPrefix(sourceType, "{") && strings.HasSuffix(sourceType, "}") {
		descriptor.JSONType, descriptor.ValueType = WireString, sourceType
		values := strings.Split(strings.TrimSuffix(strings.TrimPrefix(sourceType, "{"), "}"), ",")
		if len(values) == 0 {
			return descriptor, fmt.Errorf("field %s has empty enum", fieldPath)
		}
		minimum, maximum := len(values[0]), len(values[0])
		for _, value := range values[1:] {
			if len(value) < minimum {
				minimum = len(value)
			}
			if len(value) > maximum {
				maximum = len(value)
			}
		}
		descriptor.MinBytes, descriptor.MaxBytes = u64ptr(uint64(minimum)), u64ptr(uint64(maximum))
		return descriptor, nil
	}
	if sourceType == "bool" {
		descriptor.JSONType, descriptor.ValueType = WireBoolean, sourceType
		return descriptor, nil
	}
	if strings.HasPrefix(sourceType, "u64") {
		descriptor.JSONType, descriptor.ValueType = WireInteger, "u64"
		descriptor.MinU64, descriptor.MaxU64 = u64ptr(0), u64ptr(9223372036854775807)
		applyNumericBounds(&descriptor, strings.TrimPrefix(sourceType, "u64"))
		return descriptor, nil
	}
	if sourceType == "RAW_JSON" || strings.HasPrefix(sourceType, "RAW_JSON[") {
		descriptor.JSONType, descriptor.ValueType = WireRawJSON, sourceType
		descriptor.MinBytes, descriptor.MaxBytes = u64ptr(1), u64ptr(1048576)
		if strings.Contains(sourceType, "16384") {
			descriptor.MaxBytes = u64ptr(16384)
		}
		return descriptor, nil
	}
	if bounds, exists := primitiveWireBounds[sourceType]; exists {
		descriptor.JSONType, descriptor.ValueType = bounds.JSONType, sourceType
		descriptor.MinBytes, descriptor.MaxBytes = u64ptr(bounds.MinBytes), u64ptr(bounds.MaxBytes)
		return descriptor, nil
	}
	if base, maximum, ok := parseBoundedStringType(sourceType); ok {
		bounds, exists := primitiveWireBounds[base]
		if !exists {
			return descriptor, fmt.Errorf("field %s has unknown bounded primitive %s", fieldPath, base)
		}
		descriptor.JSONType, descriptor.ValueType = bounds.JSONType, sourceType
		descriptor.MinBytes, descriptor.MaxBytes = u64ptr(bounds.MinBytes), u64ptr(maximum)
		return descriptor, nil
	}
	if isClosedEnumType(sourceType) {
		values := frozenEnumValues[sourceType]
		descriptor.JSONType, descriptor.ValueType = WireString, sourceType
		minimum, maximum := len(values[0]), len(values[0])
		for _, value := range values[1:] {
			if len(value) < minimum {
				minimum = len(value)
			}
			if len(value) > maximum {
				maximum = len(value)
			}
		}
		descriptor.MinBytes, descriptor.MaxBytes = u64ptr(uint64(minimum)), u64ptr(uint64(maximum))
		return descriptor, nil
	}
	if strings.HasSuffix(sourceType, "V1") || strings.HasSuffix(sourceType, "V2") || strings.HasSuffix(sourceType, "V3") || strings.HasSuffix(sourceType, "V4") {
		descriptor.JSONType, descriptor.ValueType, descriptor.RecordTarget = WireObject, sourceType, sourceType
		return descriptor, nil
	}
	return descriptor, fmt.Errorf("field %s has unknown or unbounded type %q", fieldPath, sourceType)
}

type primitiveBounds struct {
	JSONType           WireJSONType
	MinBytes, MaxBytes uint64
}

var primitiveWireBounds = map[string]primitiveBounds{
	"id": {WireString, 1, 128}, "identity": {WireString, 1, 1024}, "authorityDomain": {WireString, 1, 128},
	"branch": {WireString, 1, 255}, "ref": {WireString, 6, 1024}, "origin": {WireString, 1, 2048},
	"routeTemplate": {WireString, 1, 2048}, "route": {WireString, 1, 2048}, "query": {WireString, 0, 4096},
	"prTitle": {WireString, 1, 1024}, "prBody": {WireString, 0, 1024}, "path": {WireString, 1, 1024},
	"absPath": {WireString, 1, 4096}, "procStart": {WireString, 1, 19}, "hex32": {WireString, 32, 32},
	"ed25519PublicKeyHex": {WireString, 64, 64}, "ed25519SignatureHex": {WireString, 128, 128},
	"text": {WireString, 1, 4096}, "document": {WireString, 0, 65536}, "command": {WireString, 1, 8192},
	"URL": {WireString, 1, 2048}, "gitOID": {WireString, 40, 40}, "hexid": {WireString, 64, 64},
	"dbIdentity": {WireString, 64, 64}, "ledgerEventID": {WireString, 32, 64}, "v4LedgerEventID": {WireString, 32, 32},
	"timestamp": {WireString, 20, 20}, "ledgerTimestamp": {WireString, 20, 35}, "artifactURI": {WireString, 87, 87},
	"b64MiB": {WireString, 4, 1398104}, "cutoverRequestBytes": {WireRawJSON, 1, 4259848},
}

var frozenEnumValues = map[string][]string{
	"FailureDimension": stringFailureDimensions(),
	"EvidenceClass":    {"contract", "crash_restart", "e2e", "fault_injection", "integration", "migration", "race_concurrency", "replay_idempotency", "resource", "security_negative", "smoke", "static", "unit"},
	"phase":            {"A_DESIGN", "B_IMPLEMENTATION", "C_ACCEPTANCE_MERGE"},
	"operation":        {"acceptance", "deployment", "design-planning", "design-review", "dogfood-delegation", "final-review", "implementation", "implementation-review", "maintenance", "merge-authorization", "post-merge-acceptance", "pr-publication", "recovery"},
	"review_profile":   {"CORRECTION", "INITIAL_IMPLEMENTATION", "NONE"},
	"effect_kind":      {"MERGE", "PR"}, "evidence_stage": {"B_IMPLEMENTATION", "C_BRANCH_ACCEPTANCE", "INTEGRATION_ACCEPTANCE", "POST_MERGE_ACCEPTANCE"},
	"finding_class": {"ASSURANCE_MODEL_GAP", "IMPLEMENTATION_FINDING"}, "work_class": {"CODE_BEARING", "DOCUMENTATION_ONLY"},
	"provider_disposition": {"APPLIED", "NOT_APPLIED", "UNKNOWN"}, "merge_method": {"merge", "rebase", "squash"},
	"writer_state": {"OPEN", "TOMBSTONED"}, "binding_kind": {"LEDGER", "MERGE_STATE_STORE", "PR_ADMISSION_STORE", "WORKFLOW_STATE"},
	"checkpoint_kind":             {"ACCEPTANCE_PASSED", "DESIGN_ACCEPTED", "FINAL_REVIEW_CLEAN", "IMPLEMENTATION_CONVERGED", "INTEGRATION_ACCEPTED", "MERGE_APPLIED", "MERGE_AUTHORIZED", "POST_MERGE_ACCEPTED", "POST_MERGE_FAILED", "PR_PUBLISHED"},
	"correction_relation":         {"A_REQUIRED", "IMPLEMENTATION_CORRECTABLE"},
	"secret_capability":           {"CODEX_PROVIDER", "DOCKER_PASSWORD", "NONE", "POSTGRES"},
	"network_policy":              {"DENY_ALL", "DOCKER_SOCKET", "LOOPBACK_BROKER", "LOOPBACK_POSTGRES"},
	"evidence_outcome":            {"FAIL", "ORPHAN", "PASS", "UNSELECTED", "VALIDATION_UNAVAILABLE"},
	"artifact_kind":               {"AUTHORITY", "CANDIDATE_DIFF", "EVIDENCE", "INCIDENT", "INVENTORY_MANIFEST", "INVENTORY_PAGE", "PREDECESSOR_SNAPSHOT", "PROVIDER_OBSERVATION", "RESOURCE_EVIDENCE", "SECRET_SCAN", "STDERR", "STDOUT", "STRUCTURED_RESULT"},
	"coordination_state":          {"BRANCH_ACCEPTED", "DESIGN_ACCEPTED", "FINAL_REVIEW_CLEAN", "IMPLEMENTATION_CONVERGED", "INTEGRATION_ACCEPTED", "MERGE_APPLIED", "MERGE_AUTHORIZED", "MERGE_NOT_APPLIED", "MERGE_SUBMITTING", "NONE", "POST_MERGE_ACCEPTED", "POST_MERGE_FAILED", "PR_NOT_APPLIED", "PR_PUBLISHED", "PR_SUBMITTING", "RECOVERY_REQUIRED"},
	"effect_attempt_state":        {"APPLIED", "CALL_POSSIBLE", "CLAIMED_PRE_CALL", "INTENT_DURABLE", "NONE", "NOT_APPLIED", "RECOVERY_REQUIRED", "UNKNOWN"},
	"provider_observation_source": {"MUTATION_RESPONSE", "READ_ONLY_RECONCILIATION"},
	"barrier_action":              {"RESOLVE", "RETAIN"}, "barrier_resolution_reason": {"SETTLED_APPLIED", "SETTLED_NOT_APPLIED", "UNCLAIMED_ABANDONMENT"},
	"reservation_lifecycle": {"RECOVERY_HOLD", "RELEASED", "RESERVED", "RUNNING", "SETTLING"},
	"publisher_lifecycle":   {"ABANDONED", "ACTIVE", "FINALIZED"}, "stage_seal_state": {"CLOSED", "OPEN"},
	"cleanup_outcome": {"ABSENT_PROVED", "CLEAN", "RECOVERY_HOLD"}, "recovery_outcome": {"APPLIED", "NOT_APPLIED", "RECOVERY_REQUIRED", "UNKNOWN"},
	"post_merge_reason": {"POST_MERGE_SECRET_SCAN_FAILED", "POST_MERGE_SUBJECT_MISMATCH", "POST_MERGE_VALIDATION_UNAVAILABLE", "POST_MERGE_VALIDATOR_FAILED"},
	"http_method":       {"GET", "PATCH", "POST"}, "crash_family": {"ARTIFACT", "CORRECTION", "CUTOVER", "MERGE_EFFECT", "POST_MERGE", "PR_EFFECT", "RESOURCE", "STAGE"},
	"crash_boundary":           {"ADMISSION_RESPONSE_LOST", "AFTER_ADMISSION_BEFORE_RESULT", "AFTER_BARRIER_BEFORE_CLAIM", "AFTER_CLAIM_BEFORE_ADMISSION", "AFTER_DURABLE_CHILD_BEFORE_CAS", "AFTER_LEDGER_BEFORE_PG", "AFTER_PG_BEFORE_BARRIER_RESOLVE", "AFTER_RESULT_BEFORE_SETTLEMENT", "BEFORE_DURABLE_CHILD", "CAS_RESPONSE_LOST"},
	"provider_principal_type":  {"GITHUB_APP_INSTALLATION", "GITHUB_USER"},
	"provider_credential_kind": {"GITHUB_APP_JWT", "GITHUB_INSTALLATION_TOKEN", "GITHUB_USER_TOKEN"},
	"credential_role":          {"APP_IDENTITY", "MUTATION"},
	"provider_read_purpose":    {"APP_IDENTITY", "COMMIT_RECONCILIATION", "INSTALLATION_IDENTITY", "INSTALLATION_REPOSITORIES", "PR_RECONCILIATION", "REF_RECONCILIATION", "USER_IDENTITY", "USER_REPOSITORY_ACCESS"},
	"provider_parser":          {"GITHUB_APP_V1", "GITHUB_GIT_COMMIT_V1", "GITHUB_GIT_REF_V1", "GITHUB_INSTALLATION_REPOSITORIES_PAGE_V1", "GITHUB_INSTALLATION_V1", "GITHUB_PULL_REQUEST_PAGE_V1", "GITHUB_PULL_REQUEST_V1", "GITHUB_REPOSITORY_V1", "GITHUB_USER_V1"},
	"repository_permission":    {"ADMIN", "MAINTAIN", "READ", "TRIAGE", "WRITE"}, "pr_mutation_mode": {"CREATE", "UPDATE"},
	"ledger_effect_outcome": {"APPLIED", "NOT_APPLIED", "UNCLAIMED"}, "barrier_proof_kind": {"EXACT_SETTLEMENT", "UNCLAIMED_ABANDONMENT"},
	"publisher_terminal_operation": {"ABANDON", "FINALIZE"}, "process_absence_disposition": {"PID_ABSENT", "PID_REUSED"},
	"container_absence_disposition": {"CONTAINER_ABSENT"}, "cgroup_absence_disposition": {"CGROUP_UNPOPULATED"},
	"observer_probe":          {"CGROUP_V2_EVENTS_V1", "DOCKER_INSPECT_NOT_FOUND_V1", "PROCFS_PID_STARTTIME_V1"},
	"resource_composition":    {"FLATTENED_SUM_V1", "INCLUSIVE_PARENT_SUBALLOCATION_V1"},
	"worker_transition_cause": {"CRASH_OR_AMBIGUITY", "EXECUTION_SETTLING", "EXECUTION_STARTED", "RELEASE_PROVED"},
}

func stringFailureDimensions() []string {
	values := make([]string, len(allFailureDimensions))
	for index, value := range allFailureDimensions {
		values[index] = string(value)
	}
	return values
}

func isClosedEnumType(value string) bool { _, exists := frozenEnumValues[value]; return exists }

func inferBareWireType(field string) string {
	base := strings.TrimPrefix(field, "$")
	if base == "kind" || base == "schema_version" || base == "provider" || base == "model" {
		return "id"
	}
	if base == "base_branch" {
		return "branch"
	}
	if base == "authority_domain" {
		return "authorityDomain"
	}
	if base == "controller_identity" || base == "worker_identity" || base == "host_identity" || base == "observer_identity" {
		return "dbIdentity"
	}
	if base == "repository_identity" || base == "actor" || base == "acting_principal" || base == "classifier_identity" || strings.HasSuffix(base, "_identity") {
		return "identity"
	}
	if strings.HasSuffix(base, "_at") || base == "deadline" || base == "valid_from" || base == "valid_through" {
		return "timestamp"
	}
	if strings.HasSuffix(base, "_sha256") {
		return inferBareDigestType(base)
	}
	if base == "stage" {
		return "phase"
	}
	if base == "effect_kind" {
		return "effect_kind"
	}
	if base == "evidence_class" {
		return "EvidenceClass"
	}
	if base == "review_profile" {
		return "review_profile"
	}
	if base == "classification" || base == "finding_class" {
		return "finding_class"
	}
	if base == "outcome" {
		return "evidence_outcome"
	}
	if base == "artifact_kind" {
		return "artifact_kind"
	}
	if base == "coordination_state" || base == "next_coordination_state" {
		return "coordination_state"
	}
	if base == "attempt_state" || base == "effect_attempt_state" {
		return "effect_attempt_state"
	}
	if base == "observation_source" {
		return "provider_observation_source"
	}
	if base == "barrier_action" {
		return "barrier_action"
	}
	if base == "resolution_reason" {
		return "barrier_resolution_reason"
	}
	if base == "cleanup_outcome" {
		return "cleanup_outcome"
	}
	if base == "recovery_outcome" {
		return "recovery_outcome"
	}
	if base == "reason_code" {
		return "post_merge_reason"
	}
	if base == "method" {
		return "http_method"
	}
	if base == "family" {
		return "crash_family"
	}
	if base == "boundary" {
		return "crash_boundary"
	}
	if base == "correction_relation" {
		return "correction_relation"
	}
	if base == "secret_capability" {
		return "secret_capability"
	}
	if base == "network_policy" {
		return "network_policy"
	}
	if base == "principal_type" {
		return "provider_principal_type"
	}
	if base == "credential_kind" {
		return "provider_credential_kind"
	}
	if base == "credential_role" {
		return "credential_role"
	}
	if base == "read_purpose" {
		return "provider_read_purpose"
	}
	if base == "parser" {
		return "provider_parser"
	}
	if base == "repository_permission" || base == "permission" {
		return "repository_permission"
	}
	if base == "mutation_mode" {
		return "pr_mutation_mode"
	}
	if base == "proof_kind" {
		return "barrier_proof_kind"
	}
	if base == "terminal_operation" {
		return "publisher_terminal_operation"
	}
	if base == "process_disposition" {
		return "process_absence_disposition"
	}
	if base == "container_disposition" {
		return "container_absence_disposition"
	}
	if base == "cgroup_disposition" {
		return "cgroup_absence_disposition"
	}
	if base == "probe_method" {
		return "observer_probe"
	}
	if base == "composition" {
		return "resource_composition"
	}
	if base == "transition_cause" {
		return "worker_transition_cause"
	}
	if base == "writer_state" {
		return "writer_state"
	}
	if base == "binding_kind" {
		return "binding_kind"
	}
	if base == "merge_method" {
		return "merge_method"
	}
	if base == "disposition" || base == "settled_disposition" || base == "outcome_disposition" {
		return "provider_disposition"
	}
	if strings.HasPrefix(base, "is_") || strings.HasSuffix(base, "_valid") || strings.HasSuffix(base, "_present") {
		return "bool"
	}
	return "text"
}

func inferBareDigestType(field string) string {
	if target, exists := exhaustiveDigestTargets[field]; exists {
		return "sha256<" + target + ">"
	}
	if strings.HasSuffix(field, "_artifact_sha256") {
		return "sha256<ArtifactBlobV1>"
	}
	return ""
}

var exhaustiveDigestTargets = map[string]string{
	"policy_manifest_sha256": "AssurancePolicyManifestV1", "assurance_model_sha256": "AssuranceModelV1",
	"semantic_registry_sha256": "SemanticAuthorityRegistryV2", "proof_obligation_set_sha256": "ProofObligationSetV1",
	"evidence_matrix_sha256": "EvidenceMatrixV1", "resource_profile_set_sha256": "ResourceProfileSetV1",
	"canonical_vector_catalog_sha256": "CanonicalVectorCatalogV1", "work_classification_sha256": "WorkClassificationV1",
	"final_review_profile_sha256": "FinalReviewProfileV1",
	"accepted_a_lineage_sha256":   "AcceptedALineageV1", "failed_stage_sha256": "FailedStageV1", "failed_c_capsule_sha256": "ContextCapsuleV4",
	"prior_c_tip_sha256": "PhaseCheckpointV2", "predecessor_stage_tip_sha256": "PhaseCheckpointV2", "current_stage_tip_sha256": "PhaseCheckpointV2",
	"active_stage_tip_sha256": "PhaseCheckpointV2", "invalidated_stage_tip_sha256": "PhaseCheckpointV2", "integration_tip_sha256": "PhaseCheckpointV2",
	"final_review_tip_sha256": "PhaseCheckpointV2", "pr_published_tip_sha256": "PhaseCheckpointV2", "merge_authorized_tip_sha256": "PhaseCheckpointV2",
	"selected_stage_evidence_sha256": "StageEvidenceSelectionV1", "pr_effect_attempt_index_sha256": "EffectAttemptIndexV1[effect_kind=PR]",
	"merge_effect_attempt_index_sha256": "EffectAttemptIndexV1[effect_kind=MERGE]", "original_b_scope_sha256": "PathSetV1",
	"lineage_budget_sha256": "ResourceBudgetV1", "remaining_resource_budget_sha256": "ResourceBudgetV1", "max_child_budget_sha256": "ResourceBudgetV1",
	"child_budget_sha256": "ResourceBudgetV1", "worker_capacity_sha256": "WorkerCapacityV1", "activation_v2_sha256": "GovernanceActivationV2",
	"predecessor_cutover_sha256": "PredecessorCutoverV1", "event_preimage_sha256": "EffectLedgerEventPreimageV1",
	"authentication_context_sha256": "ProviderAuthenticationContextV1", "observation_authentication_context_sha256": "ProviderAuthenticationContextV1",
	"mutation_credential_use_sha256": "ProviderCredentialUseV1", "app_identity_credential_use_sha256": "ProviderCredentialUseV1",
	"credential_use_sha256": "ProviderCredentialUseV1", "observation_credential_use_sha256": "ProviderCredentialUseV1",
	"pr_publication_authority_sha256": "PRPublicationAuthorityV1", "merge_authority_adapter_sha256": "GitHubMergeAuthorityAdapterV1",
	"runtime_owner_sha256": "RuntimeOwnerIdentityV1", "observer_key_sha256": "WorkerObservationKeyV1",
	"observation_key_registration_sha256": "WorkerObservationKeyRegistrationV1", "allocation_profile_sha256": "MaterializedResourceProfileV1",
	"direct_profile_sha256": "MaterializedResourceProfileV1", "allocation_materialized_resource_profile_sha256": "MaterializedResourceProfileV1",
	"direct_materialized_resource_profile_sha256": "MaterializedResourceProfileV1", "materialized_resource_profile_sha256": "MaterializedResourceProfileV1",
	"environment_policy_sha256": "EnvironmentPolicyV1", "toolchain_sha256": "ToolchainIdentityV1", "inventory_sha256": "RetainedArtifactInventoryV2",
	"secret_scan_sha256": "SecretScanEvidenceV2", "stage_subject_sha256": "StageSubjectV1", "subject_sha256": "StageSubjectV1",
	"candidate_diff_sha256": "CandidateDiffArtifactV1", "secret_set_commitment_sha256": "StageSecretSetV1",
}

func splitWireLiteral(source string) (literal, primitive string, ok bool) {
	if index := strings.Index(source, "="); index > 0 {
		if source[index-1] == '<' || source[index-1] == '>' || strings.Contains(source[index+1:], "..") {
			return "", "", false
		}
		primitive = strings.TrimSpace(source[:index])
		literal = strings.Trim(strings.TrimSpace(source[index+1:]), "\"")
		if primitive == "" {
			primitive = "text"
		}
		return literal, primitive, true
	}
	if strings.HasPrefix(source, "\"") && strings.HasSuffix(source, "\"") {
		return strings.Trim(source, "\""), "text", true
	}
	return "", "", false
}

func resolveArrayDescriptor(descriptor WireFieldDescriptorV1, source string) (WireFieldDescriptorV1, error) {
	open := strings.LastIndex(source, "(")
	if open < 2 || !strings.HasSuffix(source, ")") {
		return descriptor, fmt.Errorf("field %s has a bare array", descriptor.FieldPath)
	}
	element := strings.TrimPrefix(source[:open], "[]")
	constraint := strings.TrimSuffix(source[open+1:], ")")
	parts := strings.Split(constraint, ",")
	descriptor.JSONType, descriptor.ValueType = WireArray, source
	descriptor.MinItems, descriptor.MaxItems = u64ptr(0), u64ptr(256)
	if len(parts) > 1 {
		bounds := strings.Split(parts[len(parts)-1], "..")
		if len(bounds) != 2 {
			return descriptor, fmt.Errorf("field %s has invalid array cardinality", descriptor.FieldPath)
		}
		minimum, err1 := strconv.ParseUint(bounds[0], 10, 64)
		maximum, err2 := strconv.ParseUint(bounds[1], 10, 64)
		if err1 != nil || err2 != nil || minimum > maximum {
			return descriptor, fmt.Errorf("field %s has invalid array bounds", descriptor.FieldPath)
		}
		descriptor.MinItems, descriptor.MaxItems = u64ptr(minimum), u64ptr(maximum)
	}
	if strings.HasPrefix(element, "sha256<") {
		descriptor.DigestTarget = strings.TrimSuffix(strings.TrimPrefix(element, "sha256<"), ">")
		if descriptor.DigestTarget == "" {
			return descriptor, fmt.Errorf("field %s has unresolved array digest target", descriptor.FieldPath)
		}
	} else if strings.HasSuffix(element, "V1") || strings.HasSuffix(element, "V2") || strings.HasSuffix(element, "V3") || strings.HasSuffix(element, "V4") {
		descriptor.RecordTarget = element
	} else if element == "" {
		return descriptor, fmt.Errorf("field %s has untyped array elements", descriptor.FieldPath)
	}
	return descriptor, nil
}

func applyNumericBounds(descriptor *WireFieldDescriptorV1, expression string) {
	expression = strings.TrimSpace(expression)
	if strings.HasPrefix(expression, ">=") {
		if value, err := strconv.ParseUint(strings.TrimPrefix(expression, ">="), 10, 64); err == nil {
			descriptor.MinU64 = u64ptr(value)
		}
	} else if strings.HasPrefix(expression, "<=") {
		if value, err := strconv.ParseUint(strings.TrimPrefix(expression, "<="), 10, 64); err == nil {
			descriptor.MaxU64 = u64ptr(value)
		}
	} else if strings.HasPrefix(expression, "=") {
		bounds := strings.Split(strings.TrimPrefix(expression, "="), "..")
		if len(bounds) == 1 {
			if value, err := strconv.ParseUint(bounds[0], 10, 64); err == nil {
				descriptor.MinU64 = u64ptr(value)
				descriptor.MaxU64 = u64ptr(value)
			}
		}
		if len(bounds) == 2 {
			minimum, e1 := strconv.ParseUint(bounds[0], 10, 64)
			maximum, e2 := strconv.ParseUint(bounds[1], 10, 64)
			if e1 == nil && e2 == nil {
				descriptor.MinU64 = u64ptr(minimum)
				descriptor.MaxU64 = u64ptr(maximum)
			}
		}
	}
}

func parseBoundedStringType(value string) (string, uint64, bool) {
	parts := strings.Split(value, "<=")
	if len(parts) != 2 {
		return "", 0, false
	}
	maximum, err := strconv.ParseUint(parts[1], 10, 64)
	return parts[0], maximum, err == nil
}

func u64ptr(value uint64) *uint64 { return &value }

func validPredicateOperator(operator PredicateOperator) bool {
	switch operator {
	case PredicateAllEqual, PredicateExactLiteral, PredicateIfAndOnlyIf, PredicateImplies, PredicateExactlyOne,
		PredicateSubset, PredicateOrdinalSuccess, PredicateStateTransition, PredicateTypedReference,
		PredicateDigestPreimage, PredicateDerivation, PredicateAggregateLEQ:
		return true
	default:
		return false
	}
}

func GenerateCanonicalRejectionVectorsV1(schemaID string, fields []WireFieldDescriptorV1, predicates []PredicateDescriptorV1) []CanonicalRejectionVectorV1 {
	return generateCanonicalRejectionVectorsV1(schemaID, "", fields, predicates)
}

func generateCanonicalRejectionVectorsV1(schemaID, recordName string, fields []WireFieldDescriptorV1, predicates []PredicateDescriptorV1) []CanonicalRejectionVectorV1 {
	vectors := make([]CanonicalRejectionVectorV1, 0, len(fields)*5+len(predicates)+4)
	appendVector := func(path, mutation string, required CanonicalRequiredError) {
		vectors = append(vectors, CanonicalRejectionVectorV1{schemaID + "/" + path + "/" + mutation, mutation, required})
	}
	appendVector("$", "UNKNOWN_FIRST", CanonicalUnknownField)
	appendVector("$", "UNKNOWN_LAST", CanonicalUnknownField)
	appendVector("$", "DUPLICATE_FIELD", CanonicalDuplicateField)
	appendVector("$", "INVALID_UTF8", CanonicalNonCanonical)
	appendVector("$", "TRAILING_JSON", CanonicalNonCanonical)
	for fieldIndex, field := range fields {
		path := strings.ReplaceAll(field.FieldPath, "~", "~0")
		path = strings.ReplaceAll(path, "/", "~1")
		if !field.Optional {
			appendVector(path, "REMOVE", CanonicalMissingField)
		}
		appendVector(path, "NULL", CanonicalTypeInvalid)
		appendVector(path, "WRONG_TYPE", CanonicalTypeInvalid)
		if field.MinBytes != nil && *field.MinBytes > 0 && field.JSONType != WireRawJSON {
			appendVector(path, "BELOW_MIN", CanonicalBoundInvalid)
		}
		if field.MaxBytes != nil {
			appendVector(path, "OVERSIZE", CanonicalBoundInvalid)
		}
		if field.MinU64 != nil && *field.MinU64 > 0 {
			appendVector(path, "BELOW_MIN", CanonicalBoundInvalid)
		}
		if field.MaxU64 != nil {
			appendVector(path, "ABOVE_MAX", CanonicalBoundInvalid)
		}
		if field.MinItems != nil && *field.MinItems > 0 {
			appendVector(path, "BELOW_MIN", CanonicalBoundInvalid)
		}
		if field.MaxItems != nil {
			appendVector(path, "ABOVE_MAX", CanonicalBoundInvalid)
		}
		if field.DigestTarget != "" && (recordName == "" || isSelfDigestFieldIndexV1(recordName, schemaID, fields, fieldIndex)) {
			appendVector(path, "SELF_DIGEST_MISMATCH", CanonicalDigestInvalid)
		}
		if field.LiteralValue != "" {
			appendVector(path, "CONSTANT_CHANGED", CanonicalPredicateInvalid)
		}
		if isClosedEnumType(field.ValueType) || strings.HasPrefix(field.ValueType, "{") {
			appendVector(path, "ENUM_INVALID", CanonicalEnumInvalid)
		}
		if strings.Contains(field.ValueType, "(set") {
			appendVector(path, "UNSORTED", CanonicalOrderInvalid)
			appendVector(path, "DUPLICATE_MEMBER", CanonicalOrderInvalid)
		}
		if isHexWireType(field.ValueType) {
			appendVector(path, "UPPERCASE", CanonicalBoundInvalid)
			appendVector(path, "SHORT", CanonicalBoundInvalid)
			appendVector(path, "BAD_CHAR", CanonicalBoundInvalid)
		}
		elementType := arrayElementTypeV1(field.ValueType)
		if field.ValueType == "path" || field.ValueType == "absPath" || field.ValueType == "authorityDomain" ||
			elementType == "path" || elementType == "absPath" || elementType == "authorityDomain" {
			appendVector(path, "BAD_CHAR", CanonicalBoundInvalid)
		}
	}
	for _, predicate := range predicates {
		path := "$"
		if len(predicate.FieldPaths) > 0 {
			path = predicate.FieldPaths[0]
		}
		appendVector(path, "PREDICATE_"+predicate.PredicateID, predicate.RequiredError)
	}
	sort.Slice(vectors, func(i, j int) bool { return vectors[i].VectorID < vectors[j].VectorID })
	result := vectors[:0]
	for _, vector := range vectors {
		if len(result) == 0 || result[len(result)-1].VectorID != vector.VectorID {
			result = append(result, vector)
		}
	}
	return result
}

func isHexWireType(value string) bool {
	return value == "blob256" || value == "gitOID" || value == "hexid" || value == "dbIdentity" || value == "ledgerEventID" || value == "v4LedgerEventID" || value == "hex32" || value == "ed25519PublicKeyHex" || value == "ed25519SignatureHex" || strings.HasPrefix(value, "sha256<")
}

func SealCanonicalVectorCatalogV1(catalog CanonicalVectorCatalogV1) (CanonicalVectorCatalogV1, error) {
	if catalog.Kind != "CanonicalVectorCatalogV1" || catalog.SchemaVersion != "canonical-vector-catalog-v1" || catalog.DescriptorVersion != CanonicalVectorDescriptorVersion {
		return CanonicalVectorCatalogV1{}, errors.New("canonical vector catalog identity is invalid")
	}
	if len(catalog.Entries) == 0 || len(catalog.Entries) > 256 {
		return CanonicalVectorCatalogV1{}, errors.New("canonical vector catalog entry count is invalid")
	}
	previous := ""
	for _, entry := range catalog.Entries {
		if entry.SchemaID <= previous || entry.PositiveRecipe != CanonicalPositiveRecipe || len(entry.Fields) == 0 || len(entry.Rejections) == 0 {
			return CanonicalVectorCatalogV1{}, fmt.Errorf("canonical vector entry %s is incomplete or unsorted", entry.SchemaID)
		}
		previous = entry.SchemaID
		for index, field := range entry.Fields {
			if field.SchemaID != entry.SchemaID || field.Ordinal != uint64(index+1) {
				return CanonicalVectorCatalogV1{}, fmt.Errorf("canonical vector entry %s has invalid field ordinal", entry.SchemaID)
			}
		}
	}
	preimage := struct {
		Kind              string                   `json:"kind"`
		SchemaVersion     string                   `json:"schema_version"`
		DescriptorVersion string                   `json:"descriptor_version"`
		Entries           []CanonicalVectorEntryV1 `json:"entries"`
	}{catalog.Kind, catalog.SchemaVersion, catalog.DescriptorVersion, catalog.Entries}
	bytes, err := json.Marshal(preimage)
	if err != nil {
		return CanonicalVectorCatalogV1{}, err
	}
	digest := sha256.Sum256(bytes)
	catalog.CatalogSHA256 = hex.EncodeToString(digest[:])
	return catalog, nil
}

func (catalog CanonicalVectorCatalogV1) Validate() error {
	sealed, err := SealCanonicalVectorCatalogV1(catalog)
	if err != nil {
		return err
	}
	if sealed.CatalogSHA256 != catalog.CatalogSHA256 {
		return errors.New("canonical vector catalog digest mismatch")
	}
	return nil
}

func (catalog CanonicalVectorCatalogV1) CanonicalJSON() ([]byte, error) {
	if err := catalog.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(catalog)
}

// ConstantChangedV1 applies the frozen primitive-valid literal mutation.
func ConstantChangedV1(descriptor WireFieldDescriptorV1) (string, error) {
	literal := descriptor.LiteralValue
	if literal == "" {
		return "", errors.New("descriptor has no exact literal")
	}
	if descriptor.JSONType == WireArray {
		var elements []json.RawMessage
		if json.Unmarshal([]byte(literal), &elements) != nil || len(elements) == 0 {
			return "", errors.New("invalid or empty exact literal array")
		}
		var element string
		if json.Unmarshal(elements[0], &element) != nil {
			return "", errors.New("exact literal array element is not a string")
		}
		changed, err := ConstantChangedV1(WireFieldDescriptorV1{
			JSONType: WireString, ValueType: "text", LiteralValue: element,
		})
		if err != nil {
			return "", err
		}
		elements[0], _ = json.Marshal(changed)
		encoded, err := json.Marshal(elements)
		return string(encoded), err
	}
	if descriptor.JSONType == WireBoolean {
		if literal == "true" {
			return "false", nil
		}
		if literal == "false" {
			return "true", nil
		}
		return "", errors.New("invalid boolean literal")
	}
	if descriptor.JSONType == WireInteger {
		value, err := strconv.ParseUint(literal, 10, 64)
		if err != nil {
			return "", err
		}
		if descriptor.MaxU64 == nil || value < *descriptor.MaxU64 {
			return strconv.FormatUint(value+1, 10), nil
		}
		if value > 0 {
			return strconv.FormatUint(value-1, 10), nil
		}
		return "", errors.New("integer literal cannot be changed within primitive bounds")
	}
	if values, exists := frozenEnumValues[descriptor.ValueType]; exists {
		for _, candidate := range values {
			if candidate != literal {
				return candidate, nil
			}
		}
		return "", errors.New("closed enum has no different member")
	}
	bytes := []byte(literal)
	for index := len(bytes) - 1; index >= 0; index-- {
		candidates := []byte{'a', 'b', '0', '1', '-', '_'}
		if bytes[index] >= '0' && bytes[index] <= '8' {
			candidates = append([]byte{bytes[index] + 1}, candidates...)
		}
		if bytes[index] == '9' {
			candidates = append([]byte{'8'}, candidates...)
		}
		for _, replacement := range candidates {
			if replacement == bytes[index] {
				continue
			}
			candidate := append([]byte(nil), bytes...)
			candidate[index] = replacement
			if ValidatePrimitiveWireValueV1(descriptor.ValueType, string(candidate)) == nil {
				return string(candidate), nil
			}
		}
	}
	return "", errors.New("literal has no deterministic primitive-valid mutation")
}

var authorityDomainPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)

// ValidatePrimitiveWireValueV1 validates the Task-1 primitive types used by
// descriptors and rejection generation.
func ValidatePrimitiveWireValueV1(valueType, value string) error {
	base := valueType
	if strings.Contains(base, "<=") {
		base = strings.Split(base, "<=")[0]
	}
	bounds, exists := primitiveWireBounds[base]
	if !exists {
		if values, enum := frozenEnumValues[base]; enum {
			index := sort.SearchStrings(values, value)
			if index >= len(values) || values[index] != value {
				return errors.New("value is outside closed enum")
			}
			return nil
		}
		if strings.HasPrefix(base, "{") {
			for _, member := range strings.Split(strings.Trim(base, "{}"), ",") {
				if member == value {
					return nil
				}
			}
			return errors.New("value is outside inline enum")
		}
		return fmt.Errorf("unknown primitive %s", valueType)
	}
	if !utf8.ValidString(value) || uint64(len(value)) < bounds.MinBytes || uint64(len(value)) > bounds.MaxBytes {
		return errors.New("primitive byte bound is invalid")
	}
	switch base {
	case "id":
		if !idPattern.MatchString(value) {
			return errors.New("ID syntax is invalid")
		}
	case "authorityDomain":
		if !authorityDomainPattern.MatchString(value) {
			return errors.New("authority domain syntax is invalid")
		}
	case "path":
		if err := validateRelativeWirePath(value); err != nil {
			return err
		}
	case "absPath":
		if err := validateAbsoluteWirePath(value); err != nil {
			return err
		}
	case "procStart":
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil || parsed == 0 || parsed > 9223372036854775807 {
			return errors.New("process start ticks are invalid")
		}
	case "gitOID", "hexid", "dbIdentity", "hex32", "ed25519PublicKeyHex", "ed25519SignatureHex", "v4LedgerEventID":
		if err := validateExactLowerHex(value, bounds.MinBytes); err != nil {
			return err
		}
	case "ledgerEventID":
		if len(value) != 32 && len(value) != 64 {
			return errors.New("retained event ID width is invalid")
		}
		if err := validateExactLowerHex(value, uint64(len(value))); err != nil {
			return err
		}
	case "blob256":
		if err := validateExactLowerHex(value, 64); err != nil {
			return err
		}
	}
	return nil
}

func validateExactLowerHex(value string, characters uint64) error {
	if uint64(len(value)) != characters || value != strings.ToLower(value) {
		return errors.New("hex value has invalid width or case")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded)*2 != len(value) || hex.EncodeToString(decoded) != value {
		return errors.New("hex value is invalid")
	}
	return nil
}

func validateRelativeWirePath(value string) error {
	if strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || strings.Contains(value, "\x00") || strings.Contains(value, "//") || strings.HasSuffix(value, "/") {
		return errors.New("repository-relative path syntax is invalid")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("repository-relative path segment is invalid")
		}
	}
	return nil
}

func validateAbsoluteWirePath(value string) error {
	if value == "/" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.Contains(value, "\\") || strings.Contains(value, "\x00") || strings.Contains(value, "//") || strings.HasSuffix(value, "/") || filepath.Clean(value) != value {
		return errors.New("canonical absolute path syntax is invalid")
	}
	for _, part := range strings.Split(strings.TrimPrefix(value, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("canonical absolute path segment is invalid")
		}
	}
	return nil
}

// GenerateMinimalCanonicalVectorV1 emits deterministic field-ordered bytes
// from descriptors. It is intentionally mechanical; semantic predicates are
// represented by their independently executable rejection recipes.
func GenerateMinimalCanonicalVectorV1(entry CanonicalVectorEntryV1) ([]byte, error) {
	if entry.RecordName == "ArtifactBlobV1" && entry.SchemaID == "artifact-blob-v1" {
		return []byte{'a'}, nil
	}
	values := make(map[string]json.RawMessage, len(entry.Fields))
	for _, field := range entry.Fields {
		if field.Optional {
			continue
		}
		value, err := minimalDescriptorJSON(field)
		if err != nil {
			return nil, fmt.Errorf("field %s: %w", field.FieldPath, err)
		}
		values[field.FieldPath] = value
	}
	if err := applyPositivePredicatesV1(entry, values); err != nil {
		return nil, err
	}
	if err := applyEd25519VectorsV1(entry, values); err != nil {
		return nil, err
	}
	for fieldIndex, field := range entry.Fields {
		if !isSelfDigestFieldIndexV1(entry.RecordName, entry.SchemaID, entry.Fields, fieldIndex) {
			continue
		}
		preimage, err := marshalCanonicalVectorObjectV1(entry.Fields, values, field.FieldPath)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(preimage)
		encoded, _ := json.Marshal(hex.EncodeToString(digest[:]))
		values[field.FieldPath] = encoded
	}
	return marshalCanonicalVectorObjectV1(entry.Fields, values, "")
}

func minimalDescriptorJSON(field WireFieldDescriptorV1) ([]byte, error) {
	if field.LiteralValue != "" {
		switch field.JSONType {
		case WireInteger, WireBoolean:
			return []byte(field.LiteralValue), nil
		case WireArray:
			return []byte(field.LiteralValue), nil
		default:
			return json.Marshal(field.LiteralValue)
		}
	}
	switch field.JSONType {
	case WireBoolean:
		return []byte("false"), nil
	case WireInteger:
		value := uint64(0)
		if field.MinU64 != nil {
			value = *field.MinU64
		}
		return []byte(strconv.FormatUint(value, 10)), nil
	case WireArray:
		minimum := uint64(0)
		if field.MinItems != nil {
			minimum = *field.MinItems
		}
		elementType := arrayElementTypeV1(field.ValueType)
		values := make([]json.RawMessage, 0, minimum)
		for index := uint64(0); index < minimum; index++ {
			value, err := minimalArrayElementJSONV1(field, elementType, index)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		return json.Marshal(values)
	case WireObject:
		return []byte("{}"), nil
	case WireRawJSON:
		return []byte("{}"), nil
	case WireString:
		value := "a"
		if values, ok := frozenEnumValues[field.ValueType]; ok {
			value = values[0]
		} else {
			switch {
			case strings.HasPrefix(field.ValueType, "{") && strings.HasSuffix(field.ValueType, "}"):
				value = strings.Split(strings.Trim(field.ValueType, "{}"), ",")[0]
			case field.ValueType == "ed25519PublicKeyHex":
				value = "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a"
			case field.ValueType == "ed25519SignatureHex":
				value = strings.Repeat("a", 128)
			case isHexWireType(field.ValueType):
				count := 64
				if field.MinBytes != nil {
					count = int(*field.MinBytes)
				}
				value = strings.Repeat("a", count)
			case field.ValueType == "path":
				value = "a"
			case field.ValueType == "absPath":
				value = "/a"
			case field.ValueType == "authorityDomain":
				value = "a"
			case field.ValueType == "ref":
				value = "refs/a"
			case field.ValueType == "origin":
				value = "https://a"
			case field.ValueType == "route" || field.ValueType == "routeTemplate":
				value = "/a"
			case field.ValueType == "timestamp":
				value = "2000-01-01T00:00:00Z"
			case field.ValueType == "ledgerTimestamp":
				value = "2000-01-01T00:00:00Z"
			case field.ValueType == "procStart":
				value = "1"
			}
		}
		if field.MinBytes != nil && uint64(len(value)) < *field.MinBytes {
			value += strings.Repeat("a", int(*field.MinBytes)-len(value))
		}
		return json.Marshal(value)
	default:
		return nil, fmt.Errorf("unsupported JSON type %s", field.JSONType)
	}
}

func isSelfDigestDescriptorV1(recordName, schemaID string, specifications []WireFieldSpecV1, index int, descriptor WireFieldDescriptorV1) bool {
	return index == len(specifications)-1 && (descriptor.DigestTarget == recordName || descriptor.DigestTarget == schemaID)
}

func isSelfDigestFieldIndexV1(recordName, schemaID string, fields []WireFieldDescriptorV1, index int) bool {
	if index < 0 || index >= len(fields) {
		return false
	}
	field := fields[index]
	return index == len(fields)-1 && (field.DigestTarget == recordName || field.DigestTarget == schemaID)
}

func arrayElementTypeV1(valueType string) string {
	if !strings.HasPrefix(valueType, "[]") {
		return ""
	}
	valueType = strings.TrimPrefix(valueType, "[]")
	if index := strings.LastIndex(valueType, "("); index >= 0 {
		valueType = valueType[:index]
	}
	return valueType
}

func minimalArrayElementJSONV1(parent WireFieldDescriptorV1, elementType string, ordinal uint64) ([]byte, error) {
	if elementType == "" {
		return nil, errors.New("array element type is unresolved")
	}
	if strings.HasSuffix(elementType, "V1") || strings.HasSuffix(elementType, "V2") || strings.HasSuffix(elementType, "V3") || strings.HasSuffix(elementType, "V4") {
		return []byte("{}"), nil
	}
	descriptor, err := ResolveWireFieldDescriptorV1(parent.SchemaID, parent.FieldPath, parent.Ordinal, elementType, false)
	if err != nil {
		return nil, err
	}
	value, err := minimalDescriptorJSON(descriptor)
	if err != nil {
		return nil, err
	}
	if ordinal == 0 || descriptor.JSONType != WireString {
		return value, nil
	}
	var text string
	if json.Unmarshal(value, &text) != nil {
		return value, nil
	}
	if len(text) != 0 {
		replacement := byte('b' + byte((ordinal-1)%24))
		candidate := string(replacement) + text[1:]
		if ValidatePrimitiveWireValueV1(descriptor.ValueType, candidate) == nil {
			return json.Marshal(candidate)
		}
	}
	return value, nil
}

func applyPositivePredicatesV1(entry CanonicalVectorEntryV1, values map[string]json.RawMessage) error {
	fields := make(map[string]WireFieldDescriptorV1, len(entry.Fields))
	for _, field := range entry.Fields {
		fields[field.FieldPath] = field
	}
	for _, predicate := range entry.Predicates {
		switch predicate.Operator {
		case PredicateExactLiteral, PredicateTypedReference, PredicateDigestPreimage, PredicateDerivation, PredicateStateTransition, PredicateAggregateLEQ:
			// Exact literals and digest/reference shapes are installed by field
			// generation; minimal numeric values satisfy aggregate ceilings.
		case PredicateAllEqual:
			if len(predicate.FieldPaths) < 2 {
				continue
			}
			value, ok := values[predicate.FieldPaths[0]]
			if !ok {
				descriptor, exists := fields[predicate.FieldPaths[0]]
				if !exists {
					return fmt.Errorf("predicate %s has no executable operand", predicate.PredicateID)
				}
				var err error
				value, err = minimalDescriptorJSON(descriptor)
				if err != nil {
					return err
				}
				values[predicate.FieldPaths[0]] = value
			}
			for _, path := range predicate.FieldPaths[1:] {
				values[path] = append(json.RawMessage(nil), value...)
			}
		case PredicateExactlyOne:
			present := 0
			for _, path := range predicate.FieldPaths {
				if _, ok := values[path]; ok {
					present++
				}
			}
			if present == 0 && len(predicate.FieldPaths) != 0 {
				descriptor, ok := fields[predicate.FieldPaths[0]]
				if !ok {
					return fmt.Errorf("predicate %s has no executable arm", predicate.PredicateID)
				}
				value, err := minimalDescriptorJSON(descriptor)
				if err != nil {
					return err
				}
				values[predicate.FieldPaths[0]] = value
			}
		case PredicateOrdinalSuccess:
			paths := integerPredicatePathsV1(predicate.FieldPaths, fields)
			if len(paths) >= 2 {
				var predecessor uint64
				if json.Unmarshal(values[paths[0]], &predecessor) == nil {
					values[paths[1]] = json.RawMessage(strconv.FormatUint(predecessor+1, 10))
				}
			}
		case PredicateSubset:
			if len(predicate.FieldPaths) >= 2 {
				values[predicate.FieldPaths[0]] = []byte("[]")
			}
		case PredicateIfAndOnlyIf, PredicateImplies:
			// Optional operands are absent in the minimal record, making the
			// implication antecedent false. Explicit boolean operands use false.
		}
	}
	return nil
}

func integerPredicatePathsV1(paths []string, fields map[string]WireFieldDescriptorV1) []string {
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if field, ok := fields[path]; ok && field.JSONType == WireInteger {
			result = append(result, path)
		}
	}
	return result
}

func applyEd25519VectorsV1(entry CanonicalVectorEntryV1, values map[string]json.RawMessage) error {
	seed, err := hex.DecodeString("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
	if err != nil {
		return err
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	if hex.EncodeToString(publicKey) != "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a" {
		return errors.New("RFC 8032 vector public key disagrees")
	}
	for _, field := range entry.Fields {
		if field.ValueType == "ed25519PublicKeyHex" {
			encoded, _ := json.Marshal(hex.EncodeToString(publicKey))
			values[field.FieldPath] = encoded
		}
	}
	for _, field := range entry.Fields {
		if field.ValueType != "ed25519SignatureHex" {
			continue
		}
		message, err := marshalCanonicalVectorObjectV1(entry.Fields, values, field.FieldPath)
		if err != nil {
			return err
		}
		signature := ed25519.Sign(privateKey, message)
		if len(signature) != ed25519.SignatureSize {
			return errors.New("RFC 8032 vector signature has the wrong width")
		}
		encoded, _ := json.Marshal(hex.EncodeToString(signature))
		values[field.FieldPath] = encoded
	}
	return nil
}

func marshalCanonicalVectorObjectV1(fields []WireFieldDescriptorV1, values map[string]json.RawMessage, omitted string) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.WriteByte('{')
	wrote := false
	for _, field := range fields {
		if field.FieldPath == omitted {
			continue
		}
		value, exists := values[field.FieldPath]
		if !exists {
			continue
		}
		if !json.Valid(value) {
			return nil, fmt.Errorf("field %s generated invalid JSON", field.FieldPath)
		}
		if wrote {
			buffer.WriteByte(',')
		}
		name, _ := json.Marshal(field.FieldPath)
		buffer.Write(name)
		buffer.WriteByte(':')
		buffer.Write(value)
		wrote = true
	}
	buffer.WriteByte('}')
	return buffer.Bytes(), nil
}

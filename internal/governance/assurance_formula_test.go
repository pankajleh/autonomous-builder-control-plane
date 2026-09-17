package governance

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
)

func TestEffectLedgerEventIDUsesFrozenDomainTuple(t *testing.T) {
	payload := json.RawMessage(`{"effect_kind":"PR","effect_ordinal":1,"evidence_sha256":"` + strings.Repeat("3", 64) + `","intent_sha256":"` + strings.Repeat("1", 64) + `","provider_request_sha256":"` + strings.Repeat("4", 64) + `","winning_outcome_sha256":"` + strings.Repeat("2", 64) + `"}`)
	values := map[string]json.RawMessage{
		"effect_kind": json.RawMessage(`"PR"`),
		"state_to":    json.RawMessage(`"READY_FOR_MERGE"`),
		"payload":     payload,
	}
	got, err := effectLedgerEventIDV1(values)
	if err != nil {
		t.Fatal(err)
	}
	var tuple bytes.Buffer
	for index, component := range []string{"ABCP-V4-LEDGER-EVENT-ID-V1", "PR", strings.Repeat("1", 64), strings.Repeat("2", 64), "READY_FOR_MERGE"} {
		if index != 0 {
			tuple.WriteByte(0)
		}
		tuple.WriteString(component)
	}
	digest := sha256.Sum256(tuple.Bytes())
	want := hex.EncodeToString(digest[:16])
	if got != want || got != "ea37f4c9c8980503b89b191b34f4382a" {
		t.Fatalf("event ID = %s, want exact tuple ID %s", got, want)
	}
	projection, err := json.Marshal(map[string]any{
		"effect_kind": "PR", "intent_digest": strings.Repeat("1", 64),
		"winning_outcome_digest": strings.Repeat("2", 64), "state_to": "READY_FOR_MERGE",
	})
	if err != nil {
		t.Fatal(err)
	}
	projectionDigest := sha256.Sum256(projection)
	if got == hex.EncodeToString(projectionDigest[:16]) {
		t.Fatal("event ID incorrectly equals a JSON-projection hash")
	}
}

func TestAbsenceSignaturesUseFrozenDomainDigests(t *testing.T) {
	extraction, err := ExtractFrozenCanonicalVectorCatalogV1(readFrozenAssuranceModel(t))
	if err != nil {
		t.Fatal(err)
	}
	positives, err := GenerateCanonicalCatalogPositiveVectorsV1(extraction.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := hex.DecodeString("d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a")
	if err != nil {
		t.Fatal(err)
	}
	for schemaID, domain := range map[string]string{
		"process-absence-observation-v1":   "ABCP-PROCESS-ABSENCE-V1",
		"cgroup-absence-observation-v1":    "ABCP-CGROUP-ABSENCE-V1",
		"container-absence-observation-v1": "ABCP-CONTAINER-ABSENCE-V1",
	} {
		t.Run(schemaID, func(t *testing.T) {
			positive := positives[schemaID]
			marker := []byte(`,"signature_hex":`)
			index := bytes.LastIndex(positive, marker)
			if index < 0 {
				t.Fatal("signature field is missing")
			}
			unsigned := append(append([]byte(nil), positive[:index]...), '}')
			var values map[string]json.RawMessage
			if json.Unmarshal(positive, &values) != nil {
				t.Fatal("positive is not JSON")
			}
			var signatureHex string
			if json.Unmarshal(values["signature_hex"], &signatureHex) != nil {
				t.Fatal("signature is not a string")
			}
			signature, err := hex.DecodeString(signatureHex)
			if err != nil {
				t.Fatal(err)
			}
			preimage := append(append([]byte(domain), 0), unsigned...)
			message := sha256.Sum256(preimage)
			if !ed25519.Verify(ed25519.PublicKey(publicKey), message[:], signature) {
				t.Fatal("signature does not verify over domain-separated SHA-256 message")
			}
			if ed25519.Verify(ed25519.PublicKey(publicKey), unsigned, signature) {
				t.Fatal("signature incorrectly verifies over raw unsigned JSON")
			}
		})
	}
}

func TestTypedReferenceUsesActualCatalogRecordType(t *testing.T) {
	definitions := []WireSchemaDefinitionV1{
		{SchemaID: "artifact-blob-v1", RecordName: "ArtifactBlobV1", Fields: []WireFieldSpecV1{{"artifact_sha256", "blob256", false}, {"byte_size", "u64=1..33554432", false}}},
		{SchemaID: "foreign-v1", RecordName: "ForeignV1", Fields: []WireFieldSpecV1{{"kind", `id="ForeignV1"`, false}, {"schema_version", `id="foreign-v1"`, false}}},
		{SchemaID: "typed-reference-v1", RecordName: "TypedReferenceV1", Fields: []WireFieldSpecV1{{"kind", `id="TypedReferenceV1"`, false}, {"schema_version", `id="typed-reference-v1"`, false}, {"artifact_sha256", "sha256<ArtifactBlobV1>", false}}},
	}
	catalog, err := BuildCanonicalVectorCatalogV1(definitions)
	if err != nil {
		t.Fatal(err)
	}
	positives, context, err := generateCanonicalCatalogPositiveVectorsV1(catalog)
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := GenerateExecutableCanonicalCatalogVectorsV1(catalog)
	if err != nil {
		t.Fatal(err)
	}
	var positiveValues map[string]json.RawMessage
	_ = json.Unmarshal(positives["typed-reference-v1"], &positiveValues)
	var positiveDigest string
	_ = json.Unmarshal(positiveValues["artifact_sha256"], &positiveDigest)
	if positiveDigest != sha256Hex(positives["artifact-blob-v1"]) {
		t.Fatalf("positive digest = %s, want ArtifactBlobV1 record digest", positiveDigest)
	}
	var rejection ExecutableCanonicalVectorV1
	for _, candidate := range vectors["typed-reference-v1"].Rejections {
		if strings.Contains(candidate.VectorID, "PREDICATE_P-typed-reference-v1-TYPED_REFERENCE-artifact_sha256") {
			rejection = candidate
			break
		}
	}
	if len(rejection.Bytes) == 0 {
		t.Fatal("typed-reference rejection is missing")
	}
	var rejectedValues map[string]json.RawMessage
	_ = json.Unmarshal(rejection.Bytes, &rejectedValues)
	var rejectedDigest string
	_ = json.Unmarshal(rejectedValues["artifact_sha256"], &rejectedDigest)
	if rejectedDigest == positiveDigest {
		t.Fatal("typed-reference rejection retained the target-type digest")
	}
	matchedForeignRecord := false
	for recordType, record := range context.recordPositiveByType {
		if recordType != "ArtifactBlobV1" && recordType != "exact-bytes" && rejectedDigest == sha256Hex(record) {
			matchedForeignRecord = true
		}
	}
	if !matchedForeignRecord {
		t.Fatalf("rejection digest %s is not a different catalog record digest", rejectedDigest)
	}

	standalone, err := buildCanonicalVectorEntry(WireSchemaDefinitionV1{
		SchemaID: "unresolved-reference-v1", RecordName: "UnresolvedReferenceV1",
		Fields: []WireFieldSpecV1{{"kind", `id="UnresolvedReferenceV1"`, false}, {"schema_version", `id="unresolved-reference-v1"`, false}, {"target_sha256", "sha256<UnavailableV1>", false}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateExecutableCanonicalVectorsV1(standalone); err == nil {
		t.Fatal("standalone typed-reference generation did not fail closed without target-type context")
	}
}

func TestRecordWidePredicateWithoutNamedOperandsFailsClosed(t *testing.T) {
	entry, err := buildCanonicalVectorEntry(WireSchemaDefinitionV1{
		SchemaID: "unresolved-operands-v1", RecordName: "UnresolvedOperandsV1",
		Fields:     []WireFieldSpecV1{{"kind", `id="UnresolvedOperandsV1"`, false}, {"schema_version", `id="unresolved-operands-v1"`, false}, {"left", "bool", false}, {"right", "bool", false}},
		Predicates: []WirePredicateSpecV1{{"P-TEST-UNRESOLVED", []string{"$"}, PredicateIfAndOnlyIf, []string{"record-wide relation"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateExecutableCanonicalVectorsV1(entry); err == nil || !strings.Contains(err.Error(), "does not name operands") {
		t.Fatalf("unresolved record-wide predicate did not fail closed: %v", err)
	}
}

func TestAggregateUsesOnlyNamedAddendsAndExactDelta(t *testing.T) {
	entry, err := buildCanonicalVectorEntry(WireSchemaDefinitionV1{
		SchemaID: "named-aggregate-v1", RecordName: "NamedAggregateV1",
		Fields:     []WireFieldSpecV1{{"kind", `id="NamedAggregateV1"`, false}, {"schema_version", `id="named-aggregate-v1"`, false}, {"first", "u64", false}, {"unrelated", "u64", false}, {"second", "u64", false}, {"limit", "u64", false}},
		Predicates: []WirePredicateSpecV1{{"P-TEST-NAMED-AGGREGATE", []string{"first", "second", "limit"}, PredicateAggregateLEQ, []string{"first plus second <= limit"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	positive, err := GenerateMinimalCanonicalVectorV1(entry)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]json.RawMessage
	_ = json.Unmarshal(positive, &values)
	values["first"], values["unrelated"], values["second"], values["limit"] = []byte("2"), []byte("100"), []byte("3"), []byte("9")
	positive, err = marshalCanonicalVectorObjectV1(entry.Fields, values, "")
	if err != nil {
		t.Fatal(err)
	}
	set, err := generateExecutableCanonicalVectorsFromPositiveV1(entry, positive, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, vector := range set.Rejections {
		if !strings.Contains(vector.VectorID, "PREDICATE_P-TEST-NAMED-AGGREGATE") {
			continue
		}
		var mutated map[string]json.RawMessage
		if json.Unmarshal(vector.Bytes, &mutated) != nil {
			t.Fatal("aggregate rejection is not JSON")
		}
		if string(mutated["first"]) != "7" || string(mutated["second"]) != "3" || string(mutated["limit"]) != "9" || string(mutated["unrelated"]) != "100" {
			t.Fatalf("aggregate mutation = %#v, want first += limit-sum+1 and unrelated unchanged", mutated)
		}
		return
	}
	t.Fatal("named aggregate rejection is missing")
}

func TestDiffDerivationExecutesStageFormula(t *testing.T) {
	entry, err := buildCanonicalVectorEntry(WireSchemaDefinitionV1{
		SchemaID: "diff-derivation-v1", RecordName: "DiffDerivationV1",
		Fields:     []WireFieldSpecV1{{"kind", `id="DiffDerivationV1"`, false}, {"schema_version", `id="diff-derivation-v1"`, false}, {"stage", "evidence_stage", false}, {"derivation_rule", "{B_GRANT_BASE_TO_B_CANDIDATE,C_GRANT_BASE_TO_UNCHANGED_C_CANDIDATE,INTEGRATION_BASE_TO_INTEGRATED_HEAD,PREMERGE_BASE_TO_MERGE_RESULT}", false}},
		Predicates: []WirePredicateSpecV1{{"P-DIFF-001", []string{"stage", "derivation_rule"}, PredicateDerivation, []string{"stage selects derivation_rule"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	positive, err := GenerateMinimalCanonicalVectorV1(entry)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]json.RawMessage
	_ = json.Unmarshal(positive, &values)
	if string(values["stage"]) != `"B_IMPLEMENTATION"` || string(values["derivation_rule"]) != `"B_GRANT_BASE_TO_B_CANDIDATE"` {
		t.Fatalf("derivation positive does not execute the stage formula: %s", positive)
	}
	values["derivation_rule"] = json.RawMessage(`"INTEGRATION_BASE_TO_INTEGRATED_HEAD"`)
	wrong, _ := marshalCanonicalVectorObjectV1(entry.Fields, values, "")
	var classified *canonicalVectorValidationErrorV1
	validationErr := ValidateCanonicalVectorBytesV1(entry, wrong)
	if !errors.As(validationErr, &classified) || !containsCanonicalVectorStringV1(classified.falsePredicates, "P-DIFF-001") {
		t.Fatalf("independently wrong derived output classified as %v", validationErr)
	}

	unresolved, err := buildCanonicalVectorEntry(WireSchemaDefinitionV1{
		SchemaID: "unresolved-derivation-v1", RecordName: "UnresolvedDerivationV1",
		Fields:     []WireFieldSpecV1{{"kind", `id="UnresolvedDerivationV1"`, false}, {"schema_version", `id="unresolved-derivation-v1"`, false}, {"input", "id", false}, {"output", "id", false}},
		Predicates: []WirePredicateSpecV1{{"P-TEST-UNRESOLVED-DERIVATION", []string{"input", "output"}, PredicateDerivation, []string{"output derives from input"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateExecutableCanonicalVectorsV1(unresolved); err == nil || !strings.Contains(err.Error(), "no frozen formula") {
		t.Fatalf("unresolved derivation did not fail closed: %v", err)
	}
}

func TestTypedReferencesUseFinalizedTargetBytesAndFailClosed(t *testing.T) {
	definitions := []WireSchemaDefinitionV1{
		{SchemaID: "artifact-blob-v1", RecordName: "ArtifactBlobV1", Fields: []WireFieldSpecV1{{"artifact_sha256", "blob256", false}, {"byte_size", "u64=1..33554432", false}}},
		{SchemaID: "final-target-v1", RecordName: "FinalTargetV1", Fields: []WireFieldSpecV1{{"kind", `id="FinalTargetV1"`, false}, {"schema_version", `id="final-target-v1"`, false}, {"artifact_sha256", "sha256<ArtifactBlobV1>", false}, {"target_sha256", "sha256<FinalTargetV1>", false}}},
		{SchemaID: "final-holder-v1", RecordName: "FinalHolderV1", Fields: []WireFieldSpecV1{{"kind", `id="FinalHolderV1"`, false}, {"schema_version", `id="final-holder-v1"`, false}, {"target_sha256", "sha256<FinalTargetV1>", false}}},
	}
	catalog, err := BuildCanonicalVectorCatalogV1(definitions)
	if err != nil {
		t.Fatal(err)
	}
	positives, context, err := generateCanonicalCatalogPositiveVectorsV1(catalog)
	if err != nil {
		t.Fatal(err)
	}
	var targetEntry CanonicalVectorEntryV1
	for _, entry := range catalog.Entries {
		if entry.SchemaID == "final-target-v1" {
			targetEntry = entry
		}
	}
	prototype, err := GenerateMinimalCanonicalVectorV1(targetEntry)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(prototype, positives["final-target-v1"]) {
		t.Fatal("fixture did not create prototype/final target drift")
	}
	var holder map[string]json.RawMessage
	if json.Unmarshal(positives["final-holder-v1"], &holder) != nil {
		t.Fatal("holder positive is not JSON")
	}
	var digest string
	_ = json.Unmarshal(holder["target_sha256"], &digest)
	if digest != sha256Hex(positives["final-target-v1"]) || digest == sha256Hex(prototype) {
		t.Fatalf("holder digest = %s, want exact finalized target %s", digest, sha256Hex(positives["final-target-v1"]))
	}
	if _, ok := context.recordForDigest("FinalTargetV1", digest); !ok {
		t.Fatal("final target bytes are absent from typed context")
	}

	unavailable, err := BuildCanonicalVectorCatalogV1([]WireSchemaDefinitionV1{{
		SchemaID: "unavailable-holder-v1", RecordName: "UnavailableHolderV1",
		Fields: []WireFieldSpecV1{{"kind", `id="UnavailableHolderV1"`, false}, {"schema_version", `id="unavailable-holder-v1"`, false}, {"target_sha256", "sha256<UnavailableV1>", false}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateCanonicalCatalogPositiveVectorsV1(unavailable); err == nil || !strings.Contains(err.Error(), "cannot resolve emitted target type UnavailableV1") {
		t.Fatalf("unavailable target did not fail closed: %v", err)
	}
}

func TestDiffAuthorityAndCheckpointParentUseSourceRecords(t *testing.T) {
	context := newEmptyCanonicalPredicateContextV1()
	grant := []byte(`{"kind":"StageGrantV2","base_sha":"` + strings.Repeat("b", 40) + `","candidate_sha":"` + strings.Repeat("c", 40) + `"}`)
	context.addRecord("StageGrantV2", grant)
	values := map[string]json.RawMessage{
		"stage": mustMarshalCanonicalVectorStringV1("B_IMPLEMENTATION"), "base_oid": mustMarshalCanonicalVectorStringV1(strings.Repeat("b", 40)),
		"candidate_oid": mustMarshalCanonicalVectorStringV1(strings.Repeat("a", 40)), "derivation_authority_sha256": mustMarshalCanonicalVectorStringV1(sha256Hex(grant)),
	}
	if !frozenStageDiffAuthorityValidV1(values, context) {
		t.Fatal("P-DIFF-002 rejected the exact active-stage authority base")
	}
	values["base_oid"] = mustMarshalCanonicalVectorStringV1(strings.Repeat("a", 40))
	if frozenStageDiffAuthorityValidV1(values, context) {
		t.Fatal("P-DIFF-002 accepted a local minimal base instead of the stage authority base")
	}

	checkpoint := []byte(`{"kind":"C_ACCEPTED","repository":"repo","sequence":7,"candidate_sha":"` + strings.Repeat("d", 40) + `","next_stage_grant_sha256":"` + strings.Repeat("e", 64) + `","checkpoint_sha256":"` + strings.Repeat("f", 64) + `"}`)
	context.addRecord("PhaseCheckpointV2", checkpoint)
	parent := map[string]json.RawMessage{
		"kind": json.RawMessage(`"C_ACCEPTED"`), "repository": json.RawMessage(`"repo"`), "sequence": json.RawMessage(`7`), "candidate_sha": mustMarshalCanonicalVectorStringV1(strings.Repeat("d", 40)),
	}
	if !frozenCheckpointParentProjectionValidV1(parent, context) {
		t.Fatal("P-PARENT-001 rejected the complete checkpoint projection")
	}
	parent["candidate_sha"] = mustMarshalCanonicalVectorStringV1(strings.Repeat("a", 40))
	if frozenCheckpointParentProjectionValidV1(parent, context) {
		t.Fatal("P-PARENT-001 ignored a copied checkpoint field while sequence still matched")
	}
}

func resourceComponentFixtureV1(overrides map[string]any) []byte {
	values := map[string]any{"id": "component", "container_read_only_root": false, "secret_capability": "NONE", "network_policy": "DENY_ALL"}
	for _, path := range append(append([]string{}, resourceSummedFieldsV1...), resourcePeriodFieldsV1...) {
		values[path] = uint64(0)
	}
	for path, value := range overrides {
		values[path] = value
	}
	encoded, _ := json.Marshal(values)
	return encoded
}

func TestMaterializedResourceProfileFrozenSemantics(t *testing.T) {
	context := newEmptyCanonicalPredicateContextV1()
	image := strings.Repeat("1", 64)
	first := resourceComponentFixtureV1(map[string]any{
		"id": "first", "process_memory_bytes": uint64(2), "process_cpu_period_micros": uint64(100), "container_memory_bytes": uint64(3),
		"container_cpu_period_micros": uint64(100), "container_read_only_root": true, "container_image_sha256": image,
		"secret_capability": "NONE", "network_policy": "LOOPBACK_BROKER",
	})
	second := resourceComponentFixtureV1(map[string]any{
		"id": "second", "process_memory_bytes": uint64(4), "process_cpu_period_micros": uint64(100),
		"secret_capability": "POSTGRES", "network_policy": "LOOPBACK_POSTGRES",
	})
	context.addRecordVariant("ResourceProfileV1", first)
	context.addRecordVariant("ResourceProfileV1", second)
	digests := []string{sha256Hex(first), sha256Hex(second)}
	sort.Strings(digests)
	digestJSON, _ := json.Marshal(digests)
	values := map[string]json.RawMessage{"component_profile_sha256s": digestJSON}
	expected, err := materializedResourceProfileValuesV1(values, context)
	if err != nil {
		t.Fatal(err)
	}
	for path, value := range expected {
		values[path] = value
	}
	if string(values["process_memory_bytes"]) != "6" || string(values["process_cpu_period_micros"]) != "100" || string(values["container_read_only_root"]) != "true" {
		t.Fatalf("resource numeric/period/read-only aggregate is wrong: %v", values)
	}
	if string(values["secret_capabilities"]) != `["POSTGRES"]` || string(values["network_policies"]) != `["LOOPBACK_BROKER","LOOPBACK_POSTGRES"]` || string(values["container_image_sha256"]) != `"`+image+`"` {
		t.Fatalf("resource union/image aggregate is wrong: %v", values)
	}
	predicate := PredicateDescriptorV1{PredicateID: "P-RESOURCE-001", Operator: PredicateAggregateLEQ}
	if valid, handled := frozenCanonicalPredicateValidV1(CanonicalVectorEntryV1{}, predicate, nil, nil, values, context); !handled || !valid {
		t.Fatal("exact materialized resource aggregate did not validate")
	}
	values["container_read_only_root"] = []byte("false")
	if valid, _ := frozenCanonicalPredicateValidV1(CanonicalVectorEntryV1{}, predicate, nil, nil, values, context); valid {
		t.Fatal("container read-only-root iff violation validated")
	}
	values["container_read_only_root"] = []byte("true")

	entry, err := buildCanonicalVectorEntry(WireSchemaDefinitionV1{
		SchemaID: "resource-fixture-v1", RecordName: "MaterializedResourceProfileV1",
		Fields:     []WireFieldSpecV1{{"component_profile_sha256s", "[]sha256<ResourceProfileV1>(set,1..4)", false}, {"process_memory_bytes", "u64", false}, {"container_read_only_root", "bool", false}},
		Predicates: []WirePredicateSpecV1{{"P-RESOURCE-001", []string{"container_read_only_root"}, PredicateAggregateLEQ, []string{"frozen flattened resource semantics"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mutatePredicateV1(entry, predicate, values, nil, context); err != nil {
		t.Fatal(err)
	}
	if string(values["process_memory_bytes"]) != "7" {
		t.Fatalf("aggregate rejection = %s, want exact sum+1 violation 7", values["process_memory_bytes"])
	}

	periodMismatch := resourceComponentFixtureV1(map[string]any{"id": "period-mismatch", "process_cpu_period_micros": uint64(200)})
	context.addRecordVariant("ResourceProfileV1", periodMismatch)
	badDigests, _ := json.Marshal([]string{sha256Hex(first), sha256Hex(periodMismatch)})
	if _, err := materializedResourceProfileValuesV1(map[string]json.RawMessage{"component_profile_sha256s": badDigests}, context); err == nil || !strings.Contains(err.Error(), "disagree") {
		t.Fatalf("period mismatch did not fail closed: %v", err)
	}
	imageMismatch := resourceComponentFixtureV1(map[string]any{"id": "image-mismatch", "container_memory_bytes": uint64(1), "container_read_only_root": true, "container_image_sha256": strings.Repeat("2", 64)})
	context.addRecordVariant("ResourceProfileV1", imageMismatch)
	badDigests, _ = json.Marshal([]string{sha256Hex(first), sha256Hex(imageMismatch)})
	if _, err := materializedResourceProfileValuesV1(map[string]json.RawMessage{"component_profile_sha256s": badDigests}, context); err == nil || !strings.Contains(err.Error(), "images are incompatible") {
		t.Fatalf("image mismatch did not fail closed: %v", err)
	}
	denyAll := resourceComponentFixtureV1(map[string]any{"id": "deny", "network_policy": "DENY_ALL"})
	context.addRecordVariant("ResourceProfileV1", denyAll)
	badDigests, _ = json.Marshal([]string{sha256Hex(second), sha256Hex(denyAll)})
	if _, err := materializedResourceProfileValuesV1(map[string]json.RawMessage{"component_profile_sha256s": badDigests}, context); err == nil || !strings.Contains(err.Error(), "DENY_ALL") {
		t.Fatalf("incompatible networks did not fail closed: %v", err)
	}
}

func TestFrozenPredicateMutationHasNoFallbackPathSearch(t *testing.T) {
	entry, err := buildCanonicalVectorEntry(WireSchemaDefinitionV1{
		SchemaID: "unresolved-frozen-v1", RecordName: "UnresolvedFrozenV1",
		Fields:     []WireFieldSpecV1{{"kind", `id="UnresolvedFrozenV1"`, false}, {"schema_version", `id="unresolved-frozen-v1"`, false}, {"unrelated", "id", false}},
		Predicates: []WirePredicateSpecV1{{"P-DIFF-003", []string{"$"}, PredicateDerivation, []string{"unresolved frozen source relation"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	positive, err := GenerateMinimalCanonicalVectorV1(entry)
	if err != nil {
		t.Fatal(err)
	}
	values, _, _, _ := decodeCanonicalVectorObjectV1(positive)
	predicate, _ := canonicalVectorPredicateV1(entry.Predicates, "P-DIFF-003")
	if err := mutatePredicateV1(entry, predicate, values, nil, newEmptyCanonicalPredicateContextV1()); err == nil || !strings.Contains(err.Error(), "does not name operands") {
		t.Fatalf("unresolved predicate used a fallback descriptor path: %v", err)
	}
}

func TestFrozenCatalogTypedDigestsResolveExactContextBytes(t *testing.T) {
	extraction, err := ExtractFrozenCanonicalVectorCatalogV1(readFrozenAssuranceModel(t))
	if err != nil {
		t.Fatal(err)
	}
	positives, context, err := generateCanonicalCatalogPositiveVectorsV1(extraction.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range extraction.Catalog.Entries {
		if entry.RecordName == "ArtifactBlobV1" {
			continue
		}
		values, _, _, err := decodeCanonicalVectorObjectV1(positives[entry.SchemaID])
		if err != nil {
			t.Fatal(err)
		}
		if falseIDs := falseCanonicalPredicatesV1(entry, values, context); len(falseIDs) != 0 {
			t.Fatalf("%s positive has false predicates %v", entry.SchemaID, falseIDs)
		}
		for index, field := range entry.Fields {
			if field.DigestTarget == "" || isSelfDigestFieldIndexV1(entry.RecordName, entry.SchemaID, entry.Fields, index) {
				continue
			}
			raw, present := values[field.FieldPath]
			if !present {
				continue
			}
			candidates := []json.RawMessage{raw}
			if field.JSONType == WireArray {
				_ = json.Unmarshal(raw, &candidates)
			}
			for _, candidate := range candidates {
				var digest string
				_ = json.Unmarshal(candidate, &digest)
				if context.digestValidForSelectedTarget(field.DigestTarget, digest, values) || frozenExactBytesDigestValidV1(entry, field.FieldPath, digest, values) {
					continue
				}
				t.Fatalf("%s.%s digest %s has no exact emitted target bytes", entry.SchemaID, field.FieldPath, digest)
			}
		}
	}
}

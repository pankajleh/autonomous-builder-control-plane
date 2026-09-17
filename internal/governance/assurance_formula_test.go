package governance

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	contextcapsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
)

type frozenCrossRecordCatalogBindingTestV1 struct {
	entry     CanonicalVectorEntryV1
	predicate PredicateDescriptorV1
}

func frozenCrossRecordCatalogBindingsForTestV1(t *testing.T) map[string]frozenCrossRecordCatalogBindingTestV1 {
	t.Helper()
	extraction, err := ExtractFrozenCanonicalVectorCatalogV1(readFrozenAssuranceModel(t))
	if err != nil {
		t.Fatal(err)
	}
	bindings := make(map[string]frozenCrossRecordCatalogBindingTestV1)
	for _, entry := range extraction.Catalog.Entries {
		for _, predicate := range entry.Predicates {
			key := frozenCrossRecordSemanticKeyV1(entry.RecordName, predicate.PredicateID)
			if _, registered := frozenCrossRecordSemanticRegistryV1[key]; registered {
				bindings[key] = frozenCrossRecordCatalogBindingTestV1{entry: entry, predicate: predicate}
			}
		}
	}
	return bindings
}

func cloneRawMessageMapForTestV1(values map[string]json.RawMessage) map[string]json.RawMessage {
	clone := make(map[string]json.RawMessage, len(values))
	for path, value := range values {
		clone[path] = append(json.RawMessage(nil), value...)
	}
	return clone
}

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
			var entry CanonicalVectorEntryV1
			for _, candidate := range extraction.Catalog.Entries {
				if candidate.SchemaID == schemaID {
					entry = candidate
					break
				}
			}
			positive, err := GenerateMinimalCanonicalVectorV1(entry)
			if err != nil {
				t.Fatal(err)
			}
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
		if recordType != "ArtifactBlobV1" && rejectedDigest == sha256Hex(record) {
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
	if context.digestValidForTarget("FinalTargetV1", sha256Hex(prototype)) {
		t.Fatal("prototype digest was admitted as typed target authority")
	}
	for _, entry := range catalog.Entries {
		if entry.RecordName == "ArtifactBlobV1" {
			continue
		}
		values, _, _, decodeErr := decodeCanonicalVectorObjectV1(positives[entry.SchemaID])
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		for index, field := range entry.Fields {
			if field.DigestTarget == "" || isSelfDigestFieldIndexV1(entry.RecordName, entry.SchemaID, entry.Fields, index) {
				continue
			}
			var actual string
			if json.Unmarshal(values[field.FieldPath], &actual) != nil {
				t.Fatalf("%s.%s is not a scalar typed digest", entry.SchemaID, field.FieldPath)
			}
			members := selectedDigestTargetMembersV1(field.DigestTarget, values)
			if len(members) != 1 {
				t.Fatalf("%s.%s has unresolved selected targets %v", entry.SchemaID, field.FieldPath, members)
			}
			if actual != sha256Hex(context.recordPositiveByType[members[0]]) {
				t.Fatalf("%s.%s does not hash exact finalized %s bytes", entry.SchemaID, field.FieldPath, members[0])
			}
		}
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
	projection, err := checkpointGrantParentProjectionV1(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	context.checkpointProjection = projection
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
	if err := mutatePredicateV1(entry, predicate, values, nil, newEmptyCanonicalPredicateContextV1()); err == nil || !strings.Contains(err.Error(), "no explicit frozen semantic evaluator") {
		t.Fatalf("unresolved predicate used a fallback descriptor path: %v", err)
	}

	registered := frozenCrossRecordCatalogBindingsForTestV1(t)[frozenCrossRecordSemanticKeyV1("CandidateDiffArtifactV1", "P-DIFF-003")]
	drifted := registered.predicate
	drifted.FieldPaths = []string{"repository_identity"}
	values = map[string]json.RawMessage{"repository_identity": json.RawMessage(`"unchanged"`)}
	before := cloneRawMessageMapForTestV1(values)
	if err := mutateFrozenCrossRecordSemanticV1(registered.entry, drifted, values, newEmptyCanonicalPredicateContextV1()); err == nil || !strings.Contains(err.Error(), "do not match frozen rejection paths") {
		t.Fatalf("registered predicate used a path fallback after descriptor drift: %v", err)
	}
	if !reflect.DeepEqual(values, before) {
		t.Fatal("descriptor drift mutated a fallback field before failing closed")
	}
}

func TestFrozenCatalogFailsClosedWithoutIncompleteSemanticAuthority(t *testing.T) {
	extraction, err := ExtractFrozenCanonicalVectorCatalogV1(readFrozenAssuranceModel(t))
	if err != nil {
		t.Fatal(err)
	}
	positives, context, err := generateCanonicalCatalogPositiveVectorsV1(extraction.Catalog)
	if err == nil {
		t.Fatal("incomplete frozen semantics did not fail closed")
	}
	if positives != nil || context != nil {
		t.Fatal("cycle failure exposed partial/prototype digest authority")
	}
}

func TestTypedTargetCycleFailsClosedWithoutPrototypeAuthority(t *testing.T) {
	catalog, err := BuildCanonicalVectorCatalogV1([]WireSchemaDefinitionV1{
		{SchemaID: "cycle-a-v1", RecordName: "CycleAV1", Fields: []WireFieldSpecV1{{"kind", `id="CycleAV1"`, false}, {"schema_version", `id="cycle-a-v1"`, false}, {"b_sha256", "sha256<CycleBV1>", false}}},
		{SchemaID: "cycle-b-v1", RecordName: "CycleBV1", Fields: []WireFieldSpecV1{{"kind", `id="CycleBV1"`, false}, {"schema_version", `id="cycle-b-v1"`, false}, {"a_sha256", "sha256<CycleAV1>", false}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	positives, context, err := generateCanonicalCatalogPositiveVectorsV1(catalog)
	if err == nil || !strings.Contains(err.Error(), "required record target cycle") {
		t.Fatalf("typed digest cycle did not fail closed: %v", err)
	}
	if positives != nil || context != nil {
		t.Fatal("cycle failure exposed partial/prototype digest authority")
	}
}

func TestEveryFrozenCrossRecordPredicateHasExplicitSemanticEvaluator(t *testing.T) {
	extraction, err := ExtractFrozenCanonicalVectorCatalogV1(readFrozenAssuranceModel(t))
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]struct{})
	for _, entry := range extraction.Catalog.Entries {
		for _, predicate := range entry.Predicates {
			if _, ok := frozenCrossRecordPredicateIDsV1[predicate.PredicateID]; ok {
				key := frozenCrossRecordSemanticKeyV1(entry.RecordName, predicate.PredicateID)
				if _, explicit := frozenCrossRecordSemanticRegistryV1[key]; !explicit {
					t.Errorf("%s has no explicit semantic evaluator", key)
				}
				seen[key] = struct{}{}
			}
		}
	}
	for key := range frozenCrossRecordSemanticRegistryV1 {
		if _, registered := seen[key]; !registered {
			t.Errorf("semantic evaluator %s is not a registered frozen predicate", key)
		}
	}
}

func frozenCrossRecordMutationFixtureForTestV1(t *testing.T, key string, entry CanonicalVectorEntryV1) (map[string]json.RawMessage, *canonicalPredicateContextV1) {
	t.Helper()
	context := newEmptyCanonicalPredicateContextV1()
	switch key {
	case frozenCrossRecordSemanticKeyV1("CandidateDiffArtifactV1", "P-DIFF-003"):
		lineage := []byte(`{"repository_identity":"repo","accepted_a_lineage_sha256":"` + strings.Repeat("1", 64) + `","base_oid":"` + strings.Repeat("2", 40) + `","candidate_oid":"` + strings.Repeat("3", 40) + `"}`)
		context.addRecord("StageDiffLineageV1", lineage)
		subject := []byte(`{"repository_identity":"repo","accepted_a_lineage_sha256":"` + strings.Repeat("1", 64) + `","diff_base_oid":"` + strings.Repeat("2", 40) + `","diff_candidate_oid":"` + strings.Repeat("3", 40) + `","diff_lineage_sha256":"` + sha256Hex(lineage) + `"}`)
		context.addRecord("StageSubjectV1", subject)
		return map[string]json.RawMessage{
			"repository_identity":       mustMarshalCanonicalVectorStringV1("repo"),
			"accepted_a_lineage_sha256": mustMarshalCanonicalVectorStringV1(strings.Repeat("1", 64)),
			"stage_subject_sha256":      mustMarshalCanonicalVectorStringV1(sha256Hex(subject)),
			"diff_lineage_sha256":       mustMarshalCanonicalVectorStringV1(sha256Hex(lineage)),
			"base_oid":                  mustMarshalCanonicalVectorStringV1(strings.Repeat("2", 40)),
			"candidate_oid":             mustMarshalCanonicalVectorStringV1(strings.Repeat("3", 40)),
		}, context
	case frozenCrossRecordSemanticKeyV1("CheckpointGrantParentV2", "P-PARENT-001"):
		positive, err := GenerateMinimalCanonicalVectorV1(entry)
		if err != nil {
			t.Fatal(err)
		}
		values, _, duplicate, err := decodeCanonicalVectorObjectV1(positive)
		if err != nil || duplicate {
			t.Fatalf("decode checkpoint parent fixture: %v", err)
		}
		context.checkpointProjection = cloneRawMessageMapForTestV1(values)
		return values, context
	case frozenCrossRecordSemanticKeyV1("ProcessAbsenceObservationV1", "P-process-absence-observation-v1-ROW-001"):
		return map[string]json.RawMessage{
			"expected_pid": []byte("7"), "proc_stat_path": json.RawMessage(`"/proc/7/stat"`),
		}, context
	case frozenCrossRecordSemanticKeyV1("PublisherAbandonAuthorizationV1", "P-PUBLISHER-006"):
		return map[string]json.RawMessage{
			"proof_assembled_at": json.RawMessage(`"2026-09-17T00:00:00Z"`),
			"valid_from":         json.RawMessage(`"2026-09-17T00:00:00Z"`),
			"expires_at":         json.RawMessage(`"2026-09-17T00:00:01Z"`),
		}, context
	case frozenCrossRecordSemanticKeyV1("StageDiffLineageV1", "P-DIFF-002"):
		grant := []byte(`{"base_sha":"` + strings.Repeat("2", 40) + `"}`)
		context.addRecord("StageGrantV2", grant)
		return map[string]json.RawMessage{
			"stage":                       json.RawMessage(`"B_IMPLEMENTATION"`),
			"derivation_authority_sha256": mustMarshalCanonicalVectorStringV1(sha256Hex(grant)),
			"base_oid":                    mustMarshalCanonicalVectorStringV1(strings.Repeat("2", 40)),
			"candidate_oid":               mustMarshalCanonicalVectorStringV1(strings.Repeat("3", 40)),
		}, context
	case frozenCrossRecordSemanticKeyV1("StageSubjectV1", "P-SUBJECT-002"):
		lineage := []byte(`{"repository_identity":"repo","accepted_a_lineage_sha256":"` + strings.Repeat("1", 64) + `","stage":"B_IMPLEMENTATION","base_oid":"` + strings.Repeat("2", 40) + `","candidate_oid":"` + strings.Repeat("3", 40) + `"}`)
		context.addRecord("StageDiffLineageV1", lineage)
		return map[string]json.RawMessage{
			"diff_lineage_sha256":       mustMarshalCanonicalVectorStringV1(sha256Hex(lineage)),
			"repository_identity":       mustMarshalCanonicalVectorStringV1("repo"),
			"accepted_a_lineage_sha256": mustMarshalCanonicalVectorStringV1(strings.Repeat("1", 64)),
			"stage":                     json.RawMessage(`"B_IMPLEMENTATION"`),
			"diff_base_oid":             mustMarshalCanonicalVectorStringV1(strings.Repeat("2", 40)),
			"diff_candidate_oid":        mustMarshalCanonicalVectorStringV1(strings.Repeat("3", 40)),
		}, context
	case frozenCrossRecordSemanticKeyV1("WorkerObservationKeyV1", "P-worker-observation-key-v1-ROW-002"):
		values := map[string]json.RawMessage{
			"worker_identity": json.RawMessage(`"worker"`), "host_identity": json.RawMessage(`"host"`), "key_id": json.RawMessage(`"key"`),
		}
		digest := sha256.Sum256([]byte("ABCP-WORKER-OBSERVER-V1\x00worker\x00host\x00key"))
		values["observer_identity"] = mustMarshalCanonicalVectorStringV1(hex.EncodeToString(digest[:]))
		return values, context
	default:
		t.Fatalf("no cross-record mutation fixture for %q", key)
		return nil, nil
	}
}

func TestEveryExecutableCrossRecordSemanticHasExactDeterministicMutation(t *testing.T) {
	type expectedBinding struct {
		family   frozenCrossRecordSemanticFamilyV1
		operator PredicateOperator
		kind     frozenCrossRecordMutationKindV1
		operand  string
		source   string
	}
	expected := map[string]expectedBinding{
		frozenCrossRecordSemanticKeyV1("CandidateDiffArtifactV1", "P-DIFF-003"): {
			frozenCrossDiffArtifactV1, PredicateImplies, frozenCrossConsequentFalseV1, "base_oid", "",
		},
		frozenCrossRecordSemanticKeyV1("CheckpointGrantParentV2", "P-PARENT-001"): {
			frozenCrossCheckpointV1, PredicateDerivation, frozenCrossDerivedOutputV1, "kind", "",
		},
		frozenCrossRecordSemanticKeyV1("ProcessAbsenceObservationV1", "P-process-absence-observation-v1-ROW-001"): {
			frozenCrossProcessProbeV1, PredicateImplies, frozenCrossConsequentFalseV1, "proc_stat_path", "",
		},
		frozenCrossRecordSemanticKeyV1("PublisherAbandonAuthorizationV1", "P-PUBLISHER-006"): {
			frozenCrossPublisherTimeV1, PredicateStateTransition, frozenCrossStateDestinationV1, "expires_at", "valid_from",
		},
		frozenCrossRecordSemanticKeyV1("StageDiffLineageV1", "P-DIFF-002"): {
			frozenCrossStageLineageV1, PredicateImplies, frozenCrossConsequentFalseV1, "base_oid", "",
		},
		frozenCrossRecordSemanticKeyV1("StageSubjectV1", "P-SUBJECT-002"): {
			frozenCrossStageSubjectV1, PredicateImplies, frozenCrossConsequentFalseV1, "repository_identity", "",
		},
		frozenCrossRecordSemanticKeyV1("WorkerObservationKeyV1", "P-worker-observation-key-v1-ROW-002"): {
			frozenCrossWorkerKeyV1, PredicateImplies, frozenCrossConsequentFalseV1, "observer_identity", "",
		},
	}
	bindings := frozenCrossRecordCatalogBindingsForTestV1(t)
	for key, family := range frozenCrossRecordSemanticRegistryV1 {
		_, executable := frozenCrossRecordMutationRegistryV1[key]
		if family == frozenCrossFailClosedV1 && executable {
			t.Errorf("fail-closed semantic %q received a rejection mutator", key)
		}
		if family != frozenCrossFailClosedV1 && !executable {
			t.Errorf("executable semantic %q has no rejection mutator", key)
		}
	}
	if len(frozenCrossRecordMutationRegistryV1) != len(expected) {
		t.Fatalf("executable mutation registry has %d entries, want %d", len(frozenCrossRecordMutationRegistryV1), len(expected))
	}
	for key, want := range expected {
		t.Run(strings.ReplaceAll(key, "\x00", "/"), func(t *testing.T) {
			binding, present := bindings[key]
			if !present {
				t.Fatal("executable semantic is absent from the frozen catalog")
			}
			spec, present := frozenCrossRecordMutationRegistryV1[key]
			if !present {
				t.Fatal("executable semantic has no bound mutation")
			}
			if spec.family != want.family || spec.operator != want.operator || spec.mutationKind != want.kind || spec.mutationOperand != want.operand || spec.mutationSource != want.source {
				t.Fatalf("mutation binding = family:%s operator:%s kind:%s operand:%s source:%s, want %+v", spec.family, spec.operator, spec.mutationKind, spec.mutationOperand, spec.mutationSource, want)
			}
			if err := validateFrozenCrossRecordMutationSpecV1(binding.entry, binding.predicate, spec); err != nil {
				t.Fatal(err)
			}
			values, context := frozenCrossRecordMutationFixtureForTestV1(t, key, binding.entry)
			if !frozenCrossRecordSemanticValidV1(binding.entry, binding.predicate, values, context) {
				t.Fatal("fixture does not satisfy the exact frozen formula")
			}
			before := cloneRawMessageMapForTestV1(values)
			first := cloneRawMessageMapForTestV1(values)
			second := cloneRawMessageMapForTestV1(values)
			if err := mutateFrozenCrossRecordSemanticV1(binding.entry, binding.predicate, first, context); err != nil {
				t.Fatal(err)
			}
			if err := mutateFrozenCrossRecordSemanticV1(binding.entry, binding.predicate, second, context); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(first, second) {
				t.Fatal("exact bound rejection mutation is not deterministic")
			}
			for path, original := range before {
				changed := !bytes.Equal(first[path], original)
				if path == spec.mutationOperand && !changed {
					t.Fatalf("bound operand %s did not change", path)
				}
				if path != spec.mutationOperand && changed {
					t.Fatalf("unbound operand %s changed", path)
				}
			}
			var exactChange json.RawMessage
			if spec.mutationKind == frozenCrossStateDestinationV1 {
				exactChange = before[spec.mutationSource]
			} else {
				field, _ := canonicalVectorFieldV1(binding.entry.Fields, spec.mutationOperand)
				exactChange, _ = primitiveValidChangeV1(field, before[spec.mutationOperand])
			}
			if !bytes.Equal(first[spec.mutationOperand], exactChange) {
				t.Fatalf("bound operand %s = %s, want frozen operator result %s", spec.mutationOperand, first[spec.mutationOperand], exactChange)
			}
			if frozenCrossRecordSemanticValidV1(binding.entry, binding.predicate, first, context) {
				t.Fatal("exact bound rejection left the target formula true")
			}
		})
	}
}

func TestEveryIncompleteCrossRecordSemanticFailsClosedBeforeMutation(t *testing.T) {
	bindings := frozenCrossRecordCatalogBindingsForTestV1(t)
	for key, family := range frozenCrossRecordSemanticRegistryV1 {
		if family != frozenCrossFailClosedV1 {
			continue
		}
		t.Run(strings.ReplaceAll(key, "\x00", "/"), func(t *testing.T) {
			binding, present := bindings[key]
			if !present {
				t.Fatal("fail-closed semantic is absent from the frozen catalog")
			}
			values := map[string]json.RawMessage{"sentinel": json.RawMessage(`"unchanged"`)}
			before := cloneRawMessageMapForTestV1(values)
			err := mutateFrozenCrossRecordSemanticV1(binding.entry, binding.predicate, values, newEmptyCanonicalPredicateContextV1())
			if err == nil || !strings.Contains(err.Error(), "no complete explicit frozen rejection mutator") {
				t.Fatalf("incomplete semantic did not fail closed: %v", err)
			}
			if !reflect.DeepEqual(values, before) {
				t.Fatal("incomplete semantic mutated values before failing closed")
			}
		})
	}
}

func TestCandidateDiffFullRelationUsesSubjectAndLineageRecords(t *testing.T) {
	context := newEmptyCanonicalPredicateContextV1()
	lineage := []byte(`{"repository_identity":"repo","accepted_a_lineage_sha256":"` + strings.Repeat("1", 64) + `","base_oid":"` + strings.Repeat("2", 40) + `","candidate_oid":"` + strings.Repeat("3", 40) + `"}`)
	context.addRecord("StageDiffLineageV1", lineage)
	subject := []byte(`{"repository_identity":"repo","accepted_a_lineage_sha256":"` + strings.Repeat("1", 64) + `","diff_base_oid":"` + strings.Repeat("2", 40) + `","diff_candidate_oid":"` + strings.Repeat("3", 40) + `","diff_lineage_sha256":"` + sha256Hex(lineage) + `"}`)
	context.addRecord("StageSubjectV1", subject)
	values := map[string]json.RawMessage{
		"repository_identity":       mustMarshalCanonicalVectorStringV1("repo"),
		"accepted_a_lineage_sha256": mustMarshalCanonicalVectorStringV1(strings.Repeat("1", 64)),
		"stage_subject_sha256":      mustMarshalCanonicalVectorStringV1(sha256Hex(subject)),
		"diff_lineage_sha256":       mustMarshalCanonicalVectorStringV1(sha256Hex(lineage)),
		"base_oid":                  mustMarshalCanonicalVectorStringV1(strings.Repeat("2", 40)),
		"candidate_oid":             mustMarshalCanonicalVectorStringV1(strings.Repeat("3", 40)),
	}
	if !frozenCandidateDiffRelationValidV1(values, context) {
		t.Fatal("exact P-DIFF-003 relation was rejected")
	}
	for _, path := range []string{"repository_identity", "accepted_a_lineage_sha256", "base_oid", "candidate_oid"} {
		original := values[path]
		values[path] = mustMarshalCanonicalVectorStringV1("x")
		if frozenCandidateDiffRelationValidV1(values, context) {
			t.Fatalf("P-DIFF-003 ignored %s mismatch", path)
		}
		values[path] = original
	}
	binding := frozenCrossRecordCatalogBindingsForTestV1(t)[frozenCrossRecordSemanticKeyV1("CandidateDiffArtifactV1", "P-DIFF-003")]
	entry, predicate := binding.entry, binding.predicate
	repositoryBefore := append(json.RawMessage(nil), values["repository_identity"]...)
	baseBefore := append(json.RawMessage(nil), values["base_oid"]...)
	if err := mutatePredicateV1(entry, predicate, values, nil, context); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(values["repository_identity"], repositoryBefore) {
		t.Fatal("P-DIFF-003 rejection searched the earlier repository field instead of its bound derived output")
	}
	if bytes.Equal(values["base_oid"], baseBefore) {
		t.Fatal("P-DIFF-003 rejection did not mutate its exact frozen base_oid output")
	}
	if frozenCandidateDiffRelationValidV1(values, context) {
		t.Fatal("formula-derived P-DIFF-003 mutation left the relation true")
	}
}

func TestCrossRecordSemanticFamiliesEvaluateOrFailClosed(t *testing.T) {
	process := map[string]json.RawMessage{"expected_pid": []byte("7"), "proc_stat_path": json.RawMessage(`"/proc/7/stat"`)}
	processEntry := CanonicalVectorEntryV1{RecordName: "ProcessAbsenceObservationV1"}
	processPredicate := PredicateDescriptorV1{PredicateID: "P-process-absence-observation-v1-ROW-001"}
	if !frozenCrossRecordSemanticValidV1(processEntry, processPredicate, process, newEmptyCanonicalPredicateContextV1()) {
		t.Fatal("process probe semantic family rejected the exact procfs relation")
	}
	process["proc_stat_path"] = json.RawMessage(`"/proc/8/stat"`)
	if frozenCrossRecordSemanticValidV1(processEntry, processPredicate, process, newEmptyCanonicalPredicateContextV1()) {
		t.Fatal("process probe semantic family accepted a mismatched PID path")
	}

	worker := map[string]json.RawMessage{
		"worker_identity": json.RawMessage(`"worker"`), "host_identity": json.RawMessage(`"host"`), "key_id": json.RawMessage(`"key"`),
	}
	digest := sha256.Sum256([]byte("ABCP-WORKER-OBSERVER-V1\x00worker\x00host\x00key"))
	worker["observer_identity"] = mustMarshalCanonicalVectorStringV1(hex.EncodeToString(digest[:]))
	workerEntry := CanonicalVectorEntryV1{RecordName: "WorkerObservationKeyV1"}
	workerPredicate := PredicateDescriptorV1{PredicateID: "P-worker-observation-key-v1-ROW-002"}
	if !frozenCrossRecordSemanticValidV1(workerEntry, workerPredicate, worker, newEmptyCanonicalPredicateContextV1()) {
		t.Fatal("worker-key semantic family rejected its domain-separated identity")
	}

	unimplementedEntry := CanonicalVectorEntryV1{RecordName: "ArtifactPublisherOpenV1"}
	unimplementedPredicate := PredicateDescriptorV1{PredicateID: "P-PUBLISHER-003"}
	if frozenCrossRecordSemanticValidV1(unimplementedEntry, unimplementedPredicate, map[string]json.RawMessage{}, newEmptyCanonicalPredicateContextV1()) {
		t.Fatal("incomplete cross-record formula did not fail closed")
	}
}

func TestRetainedGovernanceActivationUsesFullFinalCanonicalBytes(t *testing.T) {
	records, err := frozenRetainedCanonicalRecordsV1()
	if err != nil {
		t.Fatal(err)
	}
	encoded := records["GovernanceActivationV1"]
	var activation GovernanceActivationV1
	if err := json.Unmarshal(encoded, &activation); err != nil {
		t.Fatal(err)
	}
	if err := ValidateGovernanceActivationV1(activation); err != nil {
		t.Fatalf("retained activation is not an actual finalized record: %v", err)
	}
	if activation.PolicySHA256 == "" || activation.ActivationRepositoryCommit == "" || activation.ActivationSequence == 0 || activation.ActivationTime == "" || activation.GrandfatheredV2Digests == nil || activation.ActivationSHA256 == "" {
		t.Fatal("retained activation is an incomplete synthetic subset")
	}
	context := newEmptyCanonicalPredicateContextV1()
	context.addRecord("GovernanceActivationV1", encoded)
	if !context.digestValidForTarget("GovernanceActivationV1", sha256Hex(encoded)) {
		t.Fatal("full retained activation bytes were not installed as typed authority")
	}
	if len(records) != 3 {
		t.Fatalf("retained context installed %d records; only three constructor-finalized types are available", len(records))
	}
	var bounds contextcapsule.ExecutionBoundsV1
	if err := json.Unmarshal(records["ExecutionBoundsV1"], &bounds); err != nil || contextcapsule.ValidateExecutionBoundsV1(bounds) != nil {
		t.Fatalf("retained execution bounds are not constructor-valid: %v", err)
	}
	var checkpoint PhaseCheckpointV1
	if err := json.Unmarshal(records["PhaseCheckpointV1"], &checkpoint); err != nil || ValidatePhaseCheckpointV1(checkpoint) != nil {
		t.Fatalf("retained checkpoint is not constructor-finalized: %v", err)
	}
	for _, unavailable := range []string{"AuthorizationSealV1", "ContextCapsuleV3", "ExpectedMergeContentV1", "GitHubLifecycleLimitsV1", "MergePolicyV1", "ReadyAuthorityBindingV1", "SealedMergeAuthorizationV1", "TargetRefCommitmentV1"} {
		if _, present := records[unavailable]; present {
			t.Fatalf("unavailable retained type %s was populated by synthetic bytes", unavailable)
		}
	}
}

func TestBlobDigestsAreNotGenericTypedTargets(t *testing.T) {
	descriptor, err := ResolveWireFieldDescriptorV1("fixture-v1", "digest", 1, "blob256", false)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.DigestTarget != "" {
		t.Fatalf("blob256 retained generic digest target %q", descriptor.DigestTarget)
	}
}

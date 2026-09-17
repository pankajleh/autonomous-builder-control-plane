package governance

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func TestAssuranceCanonicalUnitSemantics(t *testing.T) {
	scenarios, err := FrozenFailureScenariosV1()
	if err != nil {
		t.Fatal(err)
	}
	if len(scenarios) != 70 {
		t.Fatalf("got %d scenarios, want 70", len(scenarios))
	}
	dimensions := make(map[FailureDimension]int)
	for index, scenario := range scenarios {
		if scenario.ID != "AG-S-"+leftPad3(index+1) {
			t.Fatalf("scenario %d has ID %s", index, scenario.ID)
		}
		canonical, err := scenario.CanonicalJSON()
		if err != nil {
			t.Fatal(err)
		}
		var direct struct {
			Dimension FailureDimension `json:"dimension"`
		}
		if err := json.Unmarshal(canonical, &direct); err != nil || direct.Dimension != scenario.Dimension {
			t.Fatalf("scenario dimension was not serialized directly: %s (%v)", canonical, err)
		}
		dimensions[scenario.Dimension]++
	}
	if len(dimensions) != 14 {
		t.Fatalf("got %d represented failure dimensions, want 14", len(dimensions))
	}

	type strictRecord struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}
	valid := []byte(`{"kind":"StrictRecordV1","id":"a"}`)
	var record strictRecord
	if err := ParseCanonical(valid, &record); err != nil {
		t.Fatalf("valid strict wire rejected: %v", err)
	}
	for name, invalid := range map[string][]byte{
		"duplicate":  []byte(`{"kind":"StrictRecordV1","id":"a","id":"a"}`),
		"unknown":    []byte(`{"kind":"StrictRecordV1","id":"a","extra":false}`),
		"reordered":  []byte(`{"id":"a","kind":"StrictRecordV1"}`),
		"whitespace": []byte("{\n\"kind\":\"StrictRecordV1\",\"id\":\"a\"}"),
		"trailing":   []byte(`{"kind":"StrictRecordV1","id":"a"}{}`),
	} {
		t.Run(name, func(t *testing.T) {
			var target strictRecord
			if err := ParseCanonical(invalid, &target); err == nil {
				t.Fatalf("non-canonical input accepted: %s", invalid)
			}
		})
	}
}

func TestFrozenFailureScenariosMatchAcceptedAByteExactly(t *testing.T) {
	model := readFrozenAssuranceModel(t)
	scanner := bufio.NewScanner(bytes.NewReader(model))
	rows := make([]string, 0, 70)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "| AG-S-") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) != 7 {
			t.Fatalf("accepted A scenario row has %d cells: %q", len(cells), line)
		}
		for index := 1; index <= 5; index++ {
			cells[index] = strings.TrimSpace(cells[index])
		}
		rows = append(rows, strings.Join(cells[1:6], "|"))
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 70 {
		t.Fatalf("accepted A contains %d scenario rows, want 70", len(rows))
	}
	if len(frozenScenarioRows) != len(rows) {
		t.Fatalf("implementation contains %d scenario rows, want %d", len(frozenScenarioRows), len(rows))
	}
	for index := range rows {
		if frozenScenarioRows[index] != rows[index] {
			t.Fatalf("scenario row %d differs from accepted A\nimplementation: %q\naccepted A:    %q", index+1, frozenScenarioRows[index], rows[index])
		}
	}
}

func TestAssuranceWireCatalogPathIdentityAndLiteralVectors(t *testing.T) {
	model := readFrozenAssuranceModel(t)
	extraction, err := ExtractFrozenCanonicalVectorCatalogV1(model)
	if err != nil {
		t.Fatalf("extract frozen catalog: %v", err)
	}
	if extraction.DefinitionCount < 100 {
		t.Fatalf("catalog extracted only %d definitions", extraction.DefinitionCount)
	}
	if len(extraction.UnresolvedDigestTargets) != 0 {
		t.Fatalf("unresolved targets: %v", extraction.UnresolvedDigestTargets)
	}
	if err := extraction.Catalog.Validate(); err != nil {
		t.Fatal(err)
	}

	for valueType, positive := range map[string]string{
		"path": "a", "absPath": "/a", "authorityDomain": "a",
		"dbIdentity": strings.Repeat("a", 64),
	} {
		if err := ValidatePrimitiveWireValueV1(valueType, positive); err != nil {
			t.Fatalf("%s positive rejected: %v", valueType, err)
		}
	}
	for valueType, negative := range map[string]string{
		"path": "/a", "absPath": "a", "authorityDomain": "a/b",
		"dbIdentity": strings.Repeat("a", 63),
	} {
		if err := ValidatePrimitiveWireValueV1(valueType, negative); err == nil {
			t.Fatalf("%s negative %q accepted", valueType, negative)
		}
	}
	if err := ValidatePrimitiveWireValueV1("absPath", "/a/../b"); err == nil {
		t.Fatal("non-canonical absolute path accepted")
	}
	if err := ValidatePrimitiveWireValueV1("dbIdentity", "A"+strings.Repeat("a", 63)); err == nil {
		t.Fatal("uppercase database identity accepted")
	}

	descriptor, err := ResolveWireFieldDescriptorV1("context-capsule-v4", "policy_version", 1, `id="context-capsule-v4"`, false)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := ConstantChangedV1(descriptor)
	if err != nil || changed != "context-capsule-v5" {
		t.Fatalf("literal mutation = %q, %v", changed, err)
	}

	definitions := []WireSchemaDefinitionV1{{
		SchemaID: "ordered-test-v1", RecordName: "OrderedTestV1",
		Fields: []WireFieldSpecV1{{"kind", `id="OrderedTestV1"`, false}, {"schema_version", `id="ordered-test-v1"`, false}, {"count", "u64>=1", false}},
	}}
	catalog, err := BuildCanonicalVectorCatalogV1(definitions)
	if err != nil {
		t.Fatal(err)
	}
	positive, err := GenerateMinimalCanonicalVectorV1(catalog.Entries[0])
	if err != nil {
		t.Fatal(err)
	}
	want := `{"kind":"OrderedTestV1","schema_version":"ordered-test-v1","count":1}`
	if string(positive) != want {
		t.Fatalf("positive = %s, want %s", positive, want)
	}
	foundConstant := false
	for _, vector := range catalog.Entries[0].Rejections {
		foundConstant = foundConstant || vector.Mutation == "CONSTANT_CHANGED"
	}
	if !foundConstant {
		t.Fatal("literal CONSTANT_CHANGED rejection was not generated")
	}

	assertFrozenDigest(t,
		`{"kind":"WorkClassificationV1","schema_version":"work-classification-v1","classification":"CODE_BEARING","eligible_paths":[],"rationale":"ABCP assurance-governance enforcement changes authority-bearing code","classifier_identity":"controller"}`,
		"d79b2b9c6e77db88f096bfe5597ec56599369ee4d34cf3c23a64ae685fd5f684")
	assertFrozenDigest(t,
		`{"kind":"FinalReviewProfileV1","schema_version":"final-review-profile-v1","profile_id":"ABCP_XHIGH_FRESH_V1","provider":"codex","model":"gpt-5.6-sol","reasoning_effort":"xhigh","fresh_session":true,"required_critical":0,"required_major":0,"classification_vocabulary":["ASSURANCE_MODEL_GAP","IMPLEMENTATION_FINDING"]}`,
		"073885532416375d9cb7e81d674812131eabfd1929867daa041490c854b49553")
}

func TestExecutableCanonicalVectorsAreGeneratedAndValidated(t *testing.T) {
	definitions := []WireSchemaDefinitionV1{{
		SchemaID: "executable-vector-v1", RecordName: "ExecutableVectorV1",
		Fields: []WireFieldSpecV1{
			{"kind", `id="ExecutableVectorV1"`, false},
			{"schema_version", `id="executable-vector-v1"`, false},
			{"repository_path", "path", false},
			{"workdir", "absPath", false},
			{"authority_domain", "authorityDomain", false},
			{"controller_identity", "dbIdentity", false},
			{"artifact_sha256", "sha256<ArtifactBlobV1>", false},
			{"mode", "{ALPHA,BETA}", false},
			{"members", "[]id(set,2..3)", false},
		},
	}}
	catalog, err := BuildCanonicalVectorCatalogV1(definitions)
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := GenerateExecutableCanonicalVectorsV1(catalog.Entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors.Rejections) != len(catalog.Entries[0].Rejections) {
		t.Fatalf("generated %d rejections, want %d", len(vectors.Rejections), len(catalog.Entries[0].Rejections))
	}
	mutations := make(map[string]bool)
	for _, vector := range vectors.Rejections {
		parts := strings.Split(vector.VectorID, "/")
		mutations[parts[len(parts)-1]] = true
	}
	for _, required := range []string{"INVALID_UTF8", "BAD_CHAR", "SHORT", "UPPERCASE", "UNSORTED", "DUPLICATE_MEMBER", "CONSTANT_CHANGED"} {
		if !mutations[required] {
			t.Fatalf("executable catalog omitted %s", required)
		}
	}
}

func TestFrozenCatalogEveryPositiveRecipeProducesValidatedBytes(t *testing.T) {
	extraction, err := ExtractFrozenCanonicalVectorCatalogV1(readFrozenAssuranceModel(t))
	if err != nil {
		t.Fatal(err)
	}
	positives, err := GenerateCanonicalCatalogPositiveVectorsV1(extraction.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	if string(positives["artifact-blob-v1"]) != "a" {
		t.Fatalf("ArtifactBlobV1 positive = %q, want raw byte 0x61", positives["artifact-blob-v1"])
	}
	var authority map[string]json.RawMessage
	if err := json.Unmarshal(positives["pr-publication-authority-v1"], &authority); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(authority["repository"], []byte("{}")) || bytes.Equal(authority["actor"], []byte("{}")) {
		t.Fatal("required nested records were replaced by empty objects")
	}
	var publisher map[string]json.RawMessage
	if err := json.Unmarshal(positives["artifact-publisher-reservation-v1"], &publisher); err != nil {
		t.Fatal(err)
	}
	var lifecycle string
	_ = json.Unmarshal(publisher["lifecycle"], &lifecycle)
	if lifecycle != "ACTIVE" {
		t.Fatalf("ArtifactPublisherReservationV1 lifecycle = %q, want ACTIVE", lifecycle)
	}
	for _, terminal := range []string{"terminal_revision", "terminal_operation", "recovery_proof_sha256"} {
		if _, present := publisher[terminal]; present {
			t.Fatalf("ACTIVE ArtifactPublisherReservationV1 unexpectedly carries %s", terminal)
		}
	}
}

func TestFrozenCatalogEveryRejectionUsesRecursivePositiveAndValidates(t *testing.T) {
	extraction, err := ExtractFrozenCanonicalVectorCatalogV1(readFrozenAssuranceModel(t))
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := GenerateExecutableCanonicalCatalogVectorsV1(extraction.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(vectors) != len(extraction.Catalog.Entries) {
		t.Fatalf("materialized %d catalog entries, want %d", len(vectors), len(extraction.Catalog.Entries))
	}
	_, predicateContext, err := generateCanonicalCatalogPositiveVectorsV1(extraction.Catalog)
	if err != nil {
		t.Fatal(err)
	}

	covered := map[string]bool{}
	for _, entry := range extraction.Catalog.Entries {
		set, ok := vectors[entry.SchemaID]
		if entry.RecordName == "ArtifactBlobV1" {
			if !ok || string(set.Positive) != "a" {
				t.Fatal("raw ArtifactBlobV1 positive was not preserved")
			}
		}
		if !ok || len(set.Rejections) != len(entry.Rejections) {
			t.Fatalf("entry %s has %d executable rejections, want %d", entry.SchemaID, len(set.Rejections), len(entry.Rejections))
		}
		for _, vector := range set.Rejections {
			covered[string(vector.RequiredError)+"/"+vectorMutationV1(vector.VectorID)] = true
			if strings.Contains(vector.VectorID, "/PREDICATE_") {
				var classified *canonicalVectorValidationErrorV1
				validationErr := validateCanonicalVectorBytesV1(entry, vector.Bytes, predicateContext)
				if !errors.As(validationErr, &classified) || classified.code != vector.RequiredError {
					t.Fatalf("predicate vector %s classified as %v, want %s", vector.VectorID, validationErr, vector.RequiredError)
				}
				actual := append([]string(nil), classified.falsePredicates...)
				sort.Strings(actual)
				if !equalVectorStringsV1(actual, vector.FalsePredicateIDs) {
					t.Fatalf("predicate vector %s false set = %v, generated %v", vector.VectorID, actual, vector.FalsePredicateIDs)
				}
				target := strings.TrimPrefix(vectorMutationV1(vector.VectorID), "PREDICATE_")
				if !containsCanonicalVectorStringV1(actual, target) {
					t.Fatalf("predicate vector %s left its named target true; false set %v", vector.VectorID, actual)
				}
			}
		}
	}

	for _, schemaID := range []string{"pr-publication-authority-v1", "nested:CanonicalVectorEntryV1"} {
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(vectors[schemaID].Positive, &decoded); err != nil {
			t.Fatalf("decode %s positive: %v", schemaID, err)
		}
		for name, raw := range decoded {
			if bytes.Equal(raw, []byte("{}")) {
				t.Fatalf("%s.%s retained placeholder object", schemaID, name)
			}
			var elements []json.RawMessage
			if len(raw) != 0 && raw[0] == '[' && json.Unmarshal(raw, &elements) == nil {
				for index, element := range elements {
					if bytes.Equal(element, []byte("{}")) {
						t.Fatalf("%s.%s[%d] retained placeholder record", schemaID, name, index)
					}
				}
			}
		}
	}

	required := []string{
		string(CanonicalNonCanonical) + "/INVALID_UTF8",
		string(CanonicalBoundInvalid) + "/BAD_CHAR",
		string(CanonicalDigestInvalid) + "/SELF_DIGEST_MISMATCH",
		string(CanonicalBoundInvalid) + "/SHORT",
		string(CanonicalBoundInvalid) + "/UPPERCASE",
	}
	for _, key := range required {
		if !covered[key] {
			t.Fatalf("full frozen rejection corpus omitted %s", key)
		}
	}
	for _, schemaID := range []string{"path-set-v1", "nested:PathIdentityV1"} {
		found := false
		for _, vector := range vectors[schemaID].Rejections {
			found = found || vectorMutationV1(vector.VectorID) == "BAD_CHAR"
		}
		if !found {
			t.Fatalf("%s omitted required path BAD_CHAR rejection", schemaID)
		}
	}

	var keyValues map[string]json.RawMessage
	if err := json.Unmarshal(vectors["worker-observation-key-v1"].Positive, &keyValues); err != nil {
		t.Fatal(err)
	}
	var publicKey string
	if json.Unmarshal(keyValues["public_key_hex"], &publicKey) != nil || publicKey != "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a" {
		t.Fatalf("frozen RFC 8032 public key = %q", publicKey)
	}
	var observationEntry CanonicalVectorEntryV1
	for _, entry := range extraction.Catalog.Entries {
		if entry.SchemaID == "process-absence-observation-v1" {
			observationEntry = entry
			break
		}
	}
	if observationEntry.SchemaID == "" {
		t.Fatal("process absence observation entry is missing")
	}
	var observationValues map[string]json.RawMessage
	if err := json.Unmarshal(vectors[observationEntry.SchemaID].Positive, &observationValues); err != nil {
		t.Fatal(err)
	}
	var signature string
	if json.Unmarshal(observationValues["signature_hex"], &signature) != nil {
		t.Fatal("process absence signature is not a string")
	}
	message, err := canonicalEd25519MessageV1(observationEntry, observationValues, "signature_hex")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyEd25519CanonicalVectorV1(publicKey, signature, message); err != nil {
		t.Fatalf("frozen Ed25519 positive did not verify: %v", err)
	}
	for _, target := range []string{"public_key_hex", "signature_hex"} {
		for _, mutation := range []string{"SHORT", "UPPERCASE", "BAD_CHAR"} {
			found := false
			sets := []ExecutableCanonicalVectorSetV1{vectors["worker-observation-key-v1"], vectors["process-absence-observation-v1"]}
			for _, set := range sets {
				for _, vector := range set.Rejections {
					found = found || strings.Contains(vector.VectorID, "/"+target+"/"+mutation)
				}
			}
			if !found {
				t.Fatalf("Ed25519 %s omitted %s rejection", target, mutation)
			}
		}
	}
	selfDigestFound := false
	for _, vector := range vectors["worker-observation-key-v1"].Rejections {
		selfDigestFound = selfDigestFound || strings.Contains(vector.VectorID, "/key_sha256/SELF_DIGEST_MISMATCH") && vector.RequiredError == CanonicalDigestInvalid
	}
	if !selfDigestFound {
		t.Fatal("terminal self-digest rejection was not executed")
	}
}

func TestCanonicalPredicateOperatorsEvaluateActualOperands(t *testing.T) {
	type fixture struct {
		name       string
		schemaID   string
		recordName string
		fields     []WireFieldSpecV1
		predicate  *WirePredicateSpecV1
		target     string
		assert     func(*testing.T, []byte, []byte)
	}
	baseFields := func(record, schema string) []WireFieldSpecV1 {
		return []WireFieldSpecV1{{"kind", `id="` + record + `"`, false}, {"schema_version", `id="` + schema + `"`, false}}
	}
	withBase := func(record, schema string, fields ...WireFieldSpecV1) []WireFieldSpecV1 {
		return append(baseFields(record, schema), fields...)
	}
	boolPair := func(t *testing.T, _ []byte, mutated []byte) {
		t.Helper()
		var values map[string]json.RawMessage
		if err := json.Unmarshal(mutated, &values); err != nil {
			t.Fatal(err)
		}
		if string(values["antecedent"]) != "true" || string(values["consequent"]) != "false" {
			t.Fatalf("implication mutation = antecedent:%s consequent:%s", values["antecedent"], values["consequent"])
		}
	}
	fixtures := []fixture{
		{name: "EXACT_LITERAL", schemaID: "exact-op-v1", recordName: "ExactOpV1", fields: withBase("ExactOpV1", "exact-op-v1", WireFieldSpecV1{"mode", `id="ALPHA"`, false}), target: "P-exact-op-v1-EXACT_LITERAL-mode"},
		{name: "ALL_EQUAL", schemaID: "all-equal-op-v1", recordName: "AllEqualOpV1", fields: withBase("AllEqualOpV1", "all-equal-op-v1", WireFieldSpecV1{"left", "id", false}, WireFieldSpecV1{"right", "id", false}), predicate: &WirePredicateSpecV1{"P-TEST-ALL-EQUAL", []string{"left", "right"}, PredicateAllEqual, []string{"left equals right"}}, target: "P-TEST-ALL-EQUAL"},
		{name: "IF_AND_ONLY_IF", schemaID: "iff-op-v1", recordName: "IFFOpV1", fields: withBase("IFFOpV1", "iff-op-v1", WireFieldSpecV1{"antecedent", "bool", false}, WireFieldSpecV1{"consequent", "bool", false}), predicate: &WirePredicateSpecV1{"P-TEST-IFF", []string{"antecedent", "consequent"}, PredicateIfAndOnlyIf, []string{"antecedent iff consequent"}}, target: "P-TEST-IFF", assert: boolPair},
		{name: "IMPLIES", schemaID: "implies-op-v1", recordName: "ImpliesOpV1", fields: withBase("ImpliesOpV1", "implies-op-v1", WireFieldSpecV1{"antecedent", "bool", false}, WireFieldSpecV1{"consequent", "bool", false}), predicate: &WirePredicateSpecV1{"P-TEST-IMPLIES", []string{"antecedent", "consequent"}, PredicateImplies, []string{"antecedent requires consequent"}}, target: "P-TEST-IMPLIES", assert: boolPair},
		{name: "EXACTLY_ONE", schemaID: "one-op-v1", recordName: "OneOpV1", fields: withBase("OneOpV1", "one-op-v1", WireFieldSpecV1{"first", "id", true}, WireFieldSpecV1{"second", "id", true}), predicate: &WirePredicateSpecV1{"P-TEST-ONE", []string{"first", "second"}, PredicateExactlyOne, []string{"exactly one arm"}}, target: "P-TEST-ONE"},
		{name: "SUBSET", schemaID: "subset-op-v1", recordName: "SubsetOpV1", fields: withBase("SubsetOpV1", "subset-op-v1", WireFieldSpecV1{"subset", "[]id(set,0..4)", false}, WireFieldSpecV1{"superset", "[]id(set,0..4)", false}), predicate: &WirePredicateSpecV1{"P-TEST-SUBSET", []string{"subset", "superset"}, PredicateSubset, []string{"subset must be a subset of superset"}}, target: "P-TEST-SUBSET"},
		{name: "ORDINAL_SUCCESSOR", schemaID: "ordinal-op-v1", recordName: "OrdinalOpV1", fields: withBase("OrdinalOpV1", "ordinal-op-v1", WireFieldSpecV1{"predecessor", "u64", false}, WireFieldSpecV1{"successor", "u64", false}), predicate: &WirePredicateSpecV1{"P-TEST-ORDINAL", []string{"predecessor", "successor"}, PredicateOrdinalSuccess, []string{"successor ordinal"}}, target: "P-TEST-ORDINAL"},
		{name: "STATE_TRANSITION", schemaID: "state-op-v1", recordName: "StateOpV1", fields: withBase("StateOpV1", "state-op-v1", WireFieldSpecV1{"source", "{A,B,C}", false}, WireFieldSpecV1{"destination", "{A,B,C}", false}), predicate: &WirePredicateSpecV1{"P-TEST-STATE", []string{"source", "destination"}, PredicateStateTransition, []string{"allowed transition A→B"}}, target: "P-TEST-STATE", assert: func(t *testing.T, positive, mutated []byte) {
			var before, after map[string]json.RawMessage
			_ = json.Unmarshal(positive, &before)
			_ = json.Unmarshal(mutated, &after)
			if string(before["destination"]) != `"B"` || string(after["destination"]) != `"A"` {
				t.Fatalf("state destinations = %s -> %s, want B -> A", before["destination"], after["destination"])
			}
		}},
		{name: "TYPED_REFERENCE", schemaID: "typed-op-v1", recordName: "TypedOpV1", fields: withBase("TypedOpV1", "typed-op-v1", WireFieldSpecV1{"reference_sha256", "sha256<ArtifactBlobV1>", false}), target: "P-typed-op-v1-TYPED_REFERENCE-reference_sha256", assert: func(t *testing.T, positive, mutated []byte) {
			var before, after map[string]json.RawMessage
			_ = json.Unmarshal(positive, &before)
			_ = json.Unmarshal(mutated, &after)
			if bytes.Equal(before["reference_sha256"], after["reference_sha256"]) {
				t.Fatal("typed-reference mutation did not substitute a foreign catalog digest")
			}
		}},
		{name: "DIGEST_PREIMAGE", schemaID: "digest-op-v1", recordName: "DigestOpV1", fields: withBase("DigestOpV1", "digest-op-v1", WireFieldSpecV1{"payload", "id", false}, WireFieldSpecV1{"record_sha256", "sha256<DigestOpV1>", false}), target: "P-digest-op-v1-DIGEST_PREIMAGE-record_sha256"},
		{name: "DERIVATION", schemaID: "derivation-op-v1", recordName: "DerivationOpV1", fields: withBase("DerivationOpV1", "derivation-op-v1", WireFieldSpecV1{"stage", "evidence_stage", false}, WireFieldSpecV1{"derivation_rule", "{B_GRANT_BASE_TO_B_CANDIDATE,C_GRANT_BASE_TO_UNCHANGED_C_CANDIDATE,INTEGRATION_BASE_TO_INTEGRATED_HEAD,PREMERGE_BASE_TO_MERGE_RESULT}", false}), predicate: &WirePredicateSpecV1{"P-DIFF-001", []string{"stage", "derivation_rule"}, PredicateDerivation, []string{"stage selects derivation_rule"}}, target: "P-DIFF-001"},
		{name: "AGGREGATE_LEQ", schemaID: "aggregate-op-v1", recordName: "AggregateOpV1", fields: withBase("AggregateOpV1", "aggregate-op-v1", WireFieldSpecV1{"first", "u64", false}, WireFieldSpecV1{"second", "u64", false}, WireFieldSpecV1{"limit", "u64", false}), predicate: &WirePredicateSpecV1{"P-TEST-AGGREGATE", []string{"first", "second", "limit"}, PredicateAggregateLEQ, []string{"sum addends <= limit"}}, target: "P-TEST-AGGREGATE", assert: func(t *testing.T, _, mutated []byte) {
			var values struct {
				First  uint64 `json:"first"`
				Second uint64 `json:"second"`
				Limit  uint64 `json:"limit"`
			}
			if err := json.Unmarshal(mutated, &values); err != nil {
				t.Fatal(err)
			}
			if values.First != values.Limit-values.Second+1 {
				t.Fatalf("aggregate first = %d, want limit-sum+1 = %d", values.First, values.Limit-values.Second+1)
			}
		}},
	}

	for _, test := range fixtures {
		t.Run(test.name, func(t *testing.T) {
			definition := WireSchemaDefinitionV1{SchemaID: test.schemaID, RecordName: test.recordName, Fields: test.fields}
			if test.predicate != nil {
				definition.Predicates = []WirePredicateSpecV1{*test.predicate}
			}
			entry, err := buildCanonicalVectorEntry(definition)
			if err != nil {
				t.Fatal(err)
			}
			set, err := GenerateExecutableCanonicalVectorsV1(entry)
			if err != nil {
				t.Fatal(err)
			}
			var selected *ExecutableCanonicalVectorV1
			for index := range set.Rejections {
				if set.Rejections[index].RequiredError == CanonicalPredicateInvalid && vectorMutationV1(set.Rejections[index].VectorID) == "PREDICATE_"+test.target {
					selected = &set.Rejections[index]
					break
				}
			}
			if selected == nil {
				t.Fatalf("target rejection %s is missing", test.target)
			}
			if !containsCanonicalVectorStringV1(selected.FalsePredicateIDs, test.target) {
				t.Fatalf("false set %v omits target %s", selected.FalsePredicateIDs, test.target)
			}
			if test.assert != nil {
				test.assert(t, set.Positive, selected.Bytes)
			}
		})
	}
}

func vectorMutationV1(vectorID string) string {
	index := strings.LastIndexByte(vectorID, '/')
	if index < 0 {
		return vectorID
	}
	return vectorID[index+1:]
}

func TestCanonicalPositiveEvaluatesSelfDigestAndNonCanonicalAbsolutePath(t *testing.T) {
	definition := WireSchemaDefinitionV1{
		SchemaID: "self-digest-vector-v1", RecordName: "SelfDigestVectorV1",
		Fields: []WireFieldSpecV1{{"kind", `id="SelfDigestVectorV1"`, false}, {"schema_version", `id="self-digest-vector-v1"`, false}, {"workdir", "absPath", false}, {"record_sha256", "sha256<SelfDigestVectorV1>", false}},
	}
	entry, err := buildCanonicalVectorEntry(definition)
	if err != nil {
		t.Fatal(err)
	}
	positive, err := GenerateMinimalCanonicalVectorV1(entry)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalVectorBytesV1(entry, positive); err != nil {
		t.Fatalf("self-digest positive rejected: %v", err)
	}
	if _, err := GenerateExecutableCanonicalVectorsV1(entry); err != nil {
		t.Fatalf("self-digest executable vectors: %v", err)
	}
	var values map[string]json.RawMessage
	_ = json.Unmarshal(positive, &values)
	values["workdir"], _ = json.Marshal("/a/../b")
	mutated, _ := marshalCanonicalVectorObjectV1(entry.Fields, values, "")
	var classified *canonicalVectorValidationErrorV1
	if err := ValidateCanonicalVectorBytesV1(entry, mutated); !errors.As(err, &classified) || classified.code != CanonicalNonCanonical {
		t.Fatalf("non-normal absolute path classified as %v", err)
	}
}

func readFrozenAssuranceModel(t *testing.T) []byte {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	path := filepath.Join(filepath.Dir(source), "..", "..", "docs", "plans", "assurance-proof-obligation-governance-enforcement-assurance.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertFrozenDigest(t *testing.T, wire, want string) {
	t.Helper()
	digest := sha256.Sum256([]byte(wire))
	if got := hex.EncodeToString(digest[:]); got != want {
		t.Fatalf("digest = %s, want %s", got, want)
	}
}

func leftPad3(value int) string {
	if value < 10 {
		return "00" + string(rune('0'+value))
	}
	if value < 100 {
		return "0" + string(rune('0'+value/10)) + string(rune('0'+value%10))
	}
	return "" + string(rune('0'+value/100)) + string(rune('0'+value/10%10)) + string(rune('0'+value%10))
}

func TestStrictCanonicalDigestDoesNotAllocateAuthorityFromDuplicateFields(t *testing.T) {
	data := []byte(`{"id":"a","id":"b"}`)
	var target struct {
		ID string `json:"id"`
	}
	if _, err := SHA256Canonical(data, &target); err == nil {
		t.Fatal("duplicate fields received a canonical digest")
	}
	if bytes.Equal(data, []byte(`{"id":"b"}`)) {
		t.Fatal("test fixture unexpectedly collapsed")
	}
}

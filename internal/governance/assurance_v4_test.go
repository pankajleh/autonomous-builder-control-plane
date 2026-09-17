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
			{"public_key", "ed25519PublicKeyHex", false},
			{"signature", "ed25519SignatureHex", false},
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
	var positive map[string]json.RawMessage
	if err := json.Unmarshal(vectors.Positive, &positive); err != nil {
		t.Fatal(err)
	}
	var publicKey, signature string
	_ = json.Unmarshal(positive["public_key"], &publicKey)
	_ = json.Unmarshal(positive["signature"], &signature)
	if publicKey != "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a" || len(signature) != 128 {
		t.Fatalf("Ed25519 vector widths/seed differ: key=%q signature-bytes=%d", publicKey, len(signature)/2)
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
			if strings.Contains(vector.VectorID, "/PREDICATE_") && len(vector.FalsePredicateIDs) == 0 {
				t.Fatalf("predicate vector %s omitted its complete false-predicate set", vector.VectorID)
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
	message, err := marshalCanonicalVectorObjectV1(observationEntry.Fields, observationValues, "signature_hex")
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

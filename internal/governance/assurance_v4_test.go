package governance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

package workflowauthoritypg

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	governancev3 "github.com/pankajleh/autonomous-builder-control-plane/internal/governance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
)

func TestParseConfigurationStrict(t *testing.T) {
	valid := []byte(`{"schema_version":1,"connection_string":"postgres://example.test/abcp","authority_domain_sha256":"` + strings.Repeat("a", 64) + `"}`)
	configuration, err := parseConfiguration(valid)
	if err != nil || configuration.SchemaVersion != 1 || configuration.ConnectionString == "" {
		t.Fatalf("valid config = %+v, %v", configuration, err)
	}
	for name, data := range map[string][]byte{
		"duplicate":    []byte(`{"schema_version":1,"schema_version":1,"connection_string":"x","authority_domain_sha256":"` + strings.Repeat("a", 64) + `"}`),
		"unknown":      []byte(`{"schema_version":1,"connection_string":"x","authority_domain_sha256":"` + strings.Repeat("a", 64) + `","extra":true}`),
		"case-alias":   []byte(`{"Schema_Version":1,"connection_string":"x","authority_domain_sha256":"` + strings.Repeat("a", 64) + `"}`),
		"invalid-utf8": append([]byte(`{"schema_version":1,"connection_string":"postgres://exa`), append([]byte{0xff}, []byte(`mple.test/abcp","authority_domain_sha256":"`+strings.Repeat("a", 64)+`"}`)...)...),
		"bad-domain":   []byte(`{"schema_version":1,"connection_string":"x","authority_domain_sha256":"ABC"}`),
	} {
		if _, err := parseConfiguration(data); err == nil {
			t.Fatalf("%s config accepted", name)
		}
	}
}

func TestInitialAndDesiredRecordsAreCanonicalAndBound(t *testing.T) {
	controller := strings.Repeat("1", 64)
	domain := strings.Repeat("2", 64)
	initial, err := initialRecord(controller, domain, "example/product")
	if err != nil {
		t.Fatal(err)
	}
	if initial.revision != 1 || initial.stateSHA256 != digest(initial.canonicalState) {
		t.Fatalf("initial record = %+v", initial)
	}
	var state governancev3.ControllerStateV1
	if err := governancev3.ParseCanonical(initial.canonicalState, &state); err != nil {
		t.Fatal(err)
	}
	if state.ControllerIdentity != controller || state.RepositoryIdentity != "example/product" ||
		state.IssuedV2Authorities == nil || state.FindingEvidence == nil || state.ExecutionState.AggregateElapsed != "0s" {
		t.Fatalf("initial state = %+v", state)
	}
	state.Revision = 2
	state.ExecutionState = ralphex.ExecutionStateV1{AggregateElapsed: "1s"}
	next, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	desired, err := desiredRecord(controller, domain, 1, next)
	if err != nil {
		t.Fatal(err)
	}
	if desired.revision != 2 || desired.repositoryIdentity != "example/product" {
		t.Fatalf("desired record = %+v", desired)
	}
	if committed, err := reconcileCAS(desired, desired); err != nil || !committed {
		t.Fatalf("exact reconcile = %t, %v", committed, err)
	}
	tampered := desired
	tampered.stateSHA256 = strings.Repeat("f", 64)
	if committed, err := reconcileCAS(tampered, desired); err != nil || committed {
		t.Fatalf("tampered reconcile = %t, %v", committed, err)
	}
}

func TestValidateConfigFileRequiresProtectedRegularFile(t *testing.T) {
	root := t.TempDir()
	data := []byte(`{"schema_version":1,"connection_string":"postgres://example.test/abcp","authority_domain_sha256":"` + strings.Repeat("a", 64) + `"}`)
	path := filepath.Join(root, "workflow.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateConfigFile(path); err != nil {
		t.Fatalf("protected config rejected: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateConfigFile(path); err == nil {
		t.Fatal("world-readable config accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "workflow-link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := ValidateConfigFile(link); err == nil {
		t.Fatal("symlink config accepted")
	}
}

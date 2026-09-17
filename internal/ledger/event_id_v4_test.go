package ledger

import (
	"strings"
	"testing"
)

func TestAssuranceLedgerEventIDCompatibility(t *testing.T) {
	retained32 := "00112233445566778899aabbccddeeff"
	retained64 := retained32 + retained32
	for _, identifier := range []string{retained32, retained64} {
		if err := ValidateRetainedEventID(identifier); err != nil {
			t.Fatalf("retained ID %q rejected: %v", identifier, err)
		}
	}
	for _, identifier := range []string{
		strings.Repeat("a", 30), strings.Repeat("a", 34), strings.Repeat("a", 62), strings.Repeat("a", 66),
		"A" + strings.Repeat("a", 31), strings.Repeat("z", 32),
	} {
		if err := ValidateRetainedEventID(identifier); err == nil {
			t.Fatalf("malformed retained ID %q accepted", identifier)
		}
	}
	if err := ValidateV4EventID(retained32); err != nil {
		t.Fatalf("32-character V4 ID rejected: %v", err)
	}
	if err := ValidateV4EventID(retained64); err == nil {
		t.Fatal("64-character retained ID accepted as a V4 ID")
	}
	identifier, err := DeriveV4EffectEventID("PR", strings.Repeat("1", 64), strings.Repeat("2", 64), "READY_FOR_MERGE")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ea37f4c9c8980503b89b191b34f4382a"; identifier != want {
		t.Fatalf("V4 event ID = %s, want %s", identifier, want)
	}
}

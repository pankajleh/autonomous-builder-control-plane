package authority

import (
	"strings"
	"testing"
)

// Acceptance is the controller's independent verification of a candidate. A
// command that cannot fail turns every run into BRANCH_ACCEPTED, so placeholder
// commands must be rejected at manifest validation.
func TestAcceptanceCommandMustBeAbleToFail(t *testing.T) {
	noOps := [][]string{
		{"/usr/bin/true"},
		{"true"},
		{"/bin/true"},
		{":"},
		{"/usr/bin/bash", "-lc", "true"},
		{"/usr/bin/bash", "-lc", ":"},
		{"/usr/bin/bash", "-lc", "exit 0"},
		{"/usr/bin/bash", "-lc", ""},
		{"/usr/bin/bash", "-c", "set -euo pipefail; true"},
		{"/usr/bin/sh", "-c", "set -e; exit 0"},
		{"/usr/bin/bash", "-lc", "true\ntrue"},
	}
	for _, argv := range noOps {
		if reason := acceptanceCommandCannotVerify(argv); reason == "" {
			t.Fatalf("argv %v must be rejected: it can never fail", argv)
		}
	}
}

func TestRealAcceptanceCommandsAreAccepted(t *testing.T) {
	real := [][]string{
		{"go", "test", "./..."},
		{"/usr/bin/bash", "-lc", "set -euo pipefail; git diff --check; test -f VERIFICATION.md"},
		{"/usr/bin/bash", "-lc", "class=$(bash tools/classify.sh); exec bash tools/accept.sh"},
		{"/usr/bin/mvnw", "--offline", "--batch-mode", "test"},
		{"./mvnw", "-Dtest='*IT'", "test"},
		{"/usr/bin/bash", "-lc", "echo checking; true"},
	}
	for _, argv := range real {
		if reason := acceptanceCommandCannotVerify(argv); reason != "" {
			t.Fatalf("argv %v must be accepted as a plausible check, got: %s", argv, reason)
		}
	}
}

// A long option containing 'c' must not be mistaken for a command flag, or the
// following operand would be misread as a shell body.
func TestLongOptionsAreNotTreatedAsCommandFlags(t *testing.T) {
	if reason := acceptanceCommandCannotVerify([]string{"/usr/bin/bash", "--norc", "-c", "go test ./..."}); reason != "" {
		t.Fatalf("--norc must not be misread as -c: %s", reason)
	}
	if reason := acceptanceCommandCannotVerify([]string{"/usr/bin/bash", "--norc", "true"}); reason != "" {
		t.Fatalf("a non-command long option followed by a word is not a shell body check: %s", reason)
	}
}

func TestScriptIsNoOpClassification(t *testing.T) {
	noOp := []string{"", "   ", "true", ":", "exit 0", "set -euo pipefail; true",
		"set -e; :", "true && true", "set -eu\n:\nexit 0"}
	for _, script := range noOp {
		if !scriptIsNoOp(script) {
			t.Fatalf("script %q must classify as a no-op", script)
		}
	}
	real := []string{"git diff --check", "test -f VERIFICATION.md", "echo ok",
		"set -euo pipefail; true; test -f x", "exit 1"}
	for _, script := range real {
		if scriptIsNoOp(script) {
			t.Fatalf("script %q must not classify as a no-op", script)
		}
	}
}

// The rejection must be a validation error on the manifest, not merely a helper
// result, so a placeholder profile cannot be admitted at all. This uses the
// package's fully valid manifest fixture so the only defect under test is the
// no-op acceptance command.
func TestValidateRequiredRejectsNoOpAcceptanceCommand(t *testing.T) {
	manifest := fixtureManifest(t)
	if err := validateRequired(manifest); err != nil {
		t.Fatalf("baseline manifest must validate: %v", err)
	}
	manifest.Acceptance = []AcceptanceCommand{{
		Name: "local-smoke", Required: true, Timeout: "5m", Argv: []string{"/usr/bin/true"},
	}}
	err := validateRequired(manifest)
	if err == nil {
		t.Fatal("a no-op acceptance command must be rejected by manifest validation")
	}
	if !strings.Contains(err.Error(), "cannot verify a candidate") {
		t.Fatalf("error must explain the reason, got: %v", err)
	}
}

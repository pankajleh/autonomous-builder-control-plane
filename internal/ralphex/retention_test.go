package ralphex

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const retentionTestSource = "abcdef0123456789"

func probeBinary(t *testing.T, extra string) (string, string) {
	t.Helper()
	probe := `{"kind":"RalphexCapabilityProbeV1","source_sha":"` + retentionTestSource + `","max_iterations_flag":true,"session_timeout_flag":true,"idle_timeout_flag":true,"skip_finalize_flag":true,"base_ref_flag":true,"executor_model_effort_flags":true,"isolated_config":true,"governed_handoff":"LEGACY_INTERNAL_REVIEW","linux_containment":false,"internal_review_budget_v1":true,"orchestrator_subprocess_wait_v1":true,"human_session_identity_v1":true` + extra + `}`
	path := filepath.Join(t.TempDir(), "ralphex")
	script := "#!/bin/sh\nif [ \"$1\" = \"--abcp-governance-capability-v1\" ]; then printf '%s\\n' '" + probe + "'; exit 0; fi\nexit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(script))
	return path, hex.EncodeToString(sum[:])
}

func TestInvocationKeepsWorktreeOnlyInWorktreeMode(t *testing.T) {
	inv := Invocation{BinaryPath: "/opt/ralphex", PlanPath: "plan.md", Mode: ModeFull, Worktree: true, Branch: "abcp/run", KeepWorktree: true}
	got, err := inv.Argv()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/opt/ralphex", "--worktree", "--branch", "abcp/run", "--keep-worktree", "plan.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv mismatch\nwant: %#v\n got: %#v", want, got)
	}
	inv.KeepWorktree = false
	if got, err = inv.Argv(); err != nil || strings.Contains(strings.Join(got, " "), "--keep-worktree") {
		t.Fatal("a non-retaining invocation passed --keep-worktree", got, err)
	}
	if _, err = (Invocation{BinaryPath: "/opt/ralphex", PlanPath: "plan.md", Mode: ModeFull, KeepWorktree: true}).Argv(); err == nil {
		t.Fatal("worktree retention without worktree mode was accepted")
	}
}

func TestWorktreeRetentionCapabilityIsProvenByThePinnedProbe(t *testing.T) {
	retaining, retainingSum := probeBinary(t, `,"worktree_retention_v1":true`)
	legacy, legacySum := probeBinary(t, "")

	// The strict probe decoder accepts both generations for ordinary governed runs.
	for path, sum := range map[string]string{retaining: retainingSum, legacy: legacySum} {
		if err := VerifyGovernedExecutionCapabilityV1(path, sum, retentionTestSource); err != nil {
			t.Fatalf("governed capability rejected %s: %v", filepath.Base(filepath.Dir(path)), err)
		}
	}
	if err := VerifyWorktreeRetentionCapabilityV1(retaining, retainingSum, retentionTestSource); err != nil {
		t.Fatal("retaining binary rejected", err)
	}
	if err := VerifyWorktreeRetentionCapabilityV1(legacy, legacySum, retentionTestSource); err == nil || !strings.Contains(err.Error(), "worktree retention") {
		t.Fatal("binary without the retention capability was accepted", err)
	}
	if err := VerifyWorktreeRetentionCapabilityV1(retaining, legacySum, retentionTestSource); err == nil {
		t.Fatal("retention accepted a binary that does not match its pinned digest")
	}
}

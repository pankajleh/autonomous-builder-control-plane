package authority

import "testing"

func TestApplyExecutionDefaultsProduct(t *testing.T) {
	manifest := Manifest{
		PolicyVersion: PolicyVersionProductDefaultV1,
		Acceptance:    []AcceptanceCommand{{Required: true}},
	}
	applyExecutionDefaults(&manifest)

	if manifest.Ralphex.Timeout != "6h" {
		t.Fatalf("timeout = %q", manifest.Ralphex.Timeout)
	}
	if manifest.Ralphex.SessionTimeout != "90m" {
		t.Fatalf("session timeout = %q", manifest.Ralphex.SessionTimeout)
	}
	if manifest.Ralphex.IdleTimeout != "30m" {
		t.Fatalf("idle timeout = %q", manifest.Ralphex.IdleTimeout)
	}
	if manifest.Ralphex.MaxIterations != 12 {
		t.Fatalf("max iterations = %d", manifest.Ralphex.MaxIterations)
	}
	if manifest.Ralphex.MaxInternalReviewPasses != 2 {
		t.Fatalf("review passes = %d", manifest.Ralphex.MaxInternalReviewPasses)
	}
	if manifest.Ralphex.LongRunningSubprocessMode != "orchestrator" {
		t.Fatalf("subprocess mode = %q", manifest.Ralphex.LongRunningSubprocessMode)
	}
	if manifest.Acceptance[0].Timeout != "90m" {
		t.Fatalf("acceptance timeout = %q", manifest.Acceptance[0].Timeout)
	}
}

func TestApplyExecutionDefaultsDevelopmentV2(t *testing.T) {
	manifest := Manifest{
		PolicyVersion: PolicyVersionRepoCDevelopmentV2,
		Acceptance:    []AcceptanceCommand{{Required: true}},
	}
	applyExecutionDefaults(&manifest)

	if manifest.Ralphex.Timeout != "90m" {
		t.Fatalf("timeout = %q", manifest.Ralphex.Timeout)
	}
	if manifest.Ralphex.SessionTimeout != "45m" {
		t.Fatalf("session timeout = %q", manifest.Ralphex.SessionTimeout)
	}
	if manifest.Ralphex.IdleTimeout != "15m" {
		t.Fatalf("idle timeout = %q", manifest.Ralphex.IdleTimeout)
	}
	if manifest.Ralphex.MaxIterations != 8 {
		t.Fatalf("max iterations = %d", manifest.Ralphex.MaxIterations)
	}
	if manifest.Ralphex.MaxInternalReviewPasses != 2 {
		t.Fatalf("review passes = %d", manifest.Ralphex.MaxInternalReviewPasses)
	}
	if manifest.Acceptance[0].Timeout != "45m" {
		t.Fatalf("acceptance timeout = %q", manifest.Acceptance[0].Timeout)
	}
}

func TestApplyExecutionDefaultsPreservesExplicitOverrides(t *testing.T) {
	manifest := Manifest{
		PolicyVersion: PolicyVersionProductDefaultV1,
		Ralphex: RalphexManifest{
			Timeout:                   "2h",
			SessionTimeout:            "30m",
			IdleTimeout:               "10m",
			MaxIterations:             7,
			MaxInternalReviewPasses:   1,
			LongRunningSubprocessMode: "legacy-agent",
		},
		Acceptance: []AcceptanceCommand{{Required: true, Timeout: "20m"}},
	}
	applyExecutionDefaults(&manifest)

	if manifest.Ralphex.Timeout != "2h" ||
		manifest.Ralphex.SessionTimeout != "30m" ||
		manifest.Ralphex.IdleTimeout != "10m" ||
		manifest.Ralphex.MaxIterations != 7 ||
		manifest.Ralphex.MaxInternalReviewPasses != 1 ||
		manifest.Ralphex.LongRunningSubprocessMode != "legacy-agent" ||
		manifest.Acceptance[0].Timeout != "20m" {
		t.Fatalf("explicit overrides changed: %+v", manifest.Ralphex)
	}
}

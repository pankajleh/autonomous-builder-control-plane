package authority

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	contextcapsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
	governancev3 "github.com/pankajleh/autonomous-builder-control-plane/internal/governance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
)

func TestNewRejectsMissingRequiredFields(t *testing.T) {
	valid := fixtureManifest(t)
	tests := []struct {
		name  string
		field string
		clear func(*Manifest)
	}{
		{name: "run ID", field: "run_id", clear: func(m *Manifest) { m.RunID = "" }},
		{name: "repository path", field: "repository.path", clear: func(m *Manifest) { m.Repository.Path = "" }},
		{name: "repository identity", field: "repository.identity", clear: func(m *Manifest) { m.Repository.Identity = "" }},
		{name: "repository remotes", field: "repository.remotes", clear: func(m *Manifest) { m.Repository.Remotes = nil }},
		{name: "default branch", field: "repository.default_branch", clear: func(m *Manifest) { m.Repository.DefaultBranch = "" }},
		{name: "start SHA", field: "repository.start_sha", clear: func(m *Manifest) { m.Repository.StartSHA = "" }},
		{name: "plan path", field: "plan.path", clear: func(m *Manifest) { m.Plan.Path = "" }},
		{name: "plan hash", field: "plan.sha256", clear: func(m *Manifest) { m.Plan.SHA256 = "" }},
		{name: "binary path", field: "ralphex.binary_path", clear: func(m *Manifest) { m.Ralphex.BinaryPath = "" }},
		{name: "binary hash", field: "ralphex.binary_sha256", clear: func(m *Manifest) { m.Ralphex.BinarySHA256 = "" }},
		{name: "Ralphex timeout", field: "ralphex.timeout", clear: func(m *Manifest) { m.Ralphex.Timeout = "" }},
		{name: "wait on limit", field: "ralphex.wait_on_limit", clear: func(m *Manifest) { m.Ralphex.WaitOnLimit = "" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := cloneManifest(valid)
			test.clear(&manifest)
			_, err := New(manifest)
			if err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("expected error naming %s, got %v", test.field, err)
			}
		})
	}
}

func TestNewRejectsEmptyAcceptanceArgv(t *testing.T) {
	manifest := fixtureManifest(t)
	manifest.Acceptance[0].Argv = nil
	_, err := New(manifest)
	if err == nil || !strings.Contains(err.Error(), "argv") {
		t.Fatalf("expected empty argv error, got %v", err)
	}
}

func TestNewRejectsAcceptancePolicyWithoutRequiredCommand(t *testing.T) {
	manifest := fixtureManifest(t)
	manifest.Acceptance[0].Required = false
	_, err := New(manifest)
	if err == nil || !strings.Contains(err.Error(), "required acceptance") {
		t.Fatalf("expected required acceptance error, got %v", err)
	}
}

func TestNewRejectsRepositoryIdentityNotBoundToRemote(t *testing.T) {
	manifest := fixtureManifest(t)
	manifest.Repository.Identity = "different/project"
	_, err := New(manifest)
	if err == nil || !strings.Contains(err.Error(), "does not match any governed remote") {
		t.Fatalf("expected repository identity mismatch, got %v", err)
	}
}

func TestNewRejectsCredentialBearingRemoteURLs(t *testing.T) {
	remoteURLs := []string{
		"https://user:token@example.test/example/project.git",
		"https://token@example.test/example/project.git",
		"https://example.test/example/project.git?access_token=secret",
	}
	for _, remoteURL := range remoteURLs {
		t.Run(remoteURL, func(t *testing.T) {
			manifest := fixtureManifest(t)
			manifest.Repository.Remotes["origin"] = remoteURL
			_, err := New(manifest)
			if err == nil || !strings.Contains(err.Error(), "credentials") {
				t.Fatalf("expected credential-bearing remote rejection, got %v", err)
			}
		})
	}
}

func TestNewAllowsSSHRemoteUsername(t *testing.T) {
	manifest := fixtureManifest(t)
	manifest.Repository.Remotes["origin"] = "ssh://git@example.test/example/project.git"
	if _, err := New(manifest); err != nil {
		t.Fatalf("SSH username should not be treated as a persisted secret: %v", err)
	}
}

func TestNewRejectsInvalidTimingPolicy(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "zero Ralphex timeout", mutate: func(m *Manifest) { m.Ralphex.Timeout = "0s" }},
		{name: "negative wait on limit", mutate: func(m *Manifest) { m.Ralphex.WaitOnLimit = "-1s" }},
		{name: "missing acceptance timeout", mutate: func(m *Manifest) { m.Acceptance[0].Timeout = "" }},
		{name: "invalid acceptance timeout", mutate: func(m *Manifest) { m.Acceptance[0].Timeout = "later" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := fixtureManifest(t)
			test.mutate(&manifest)
			if _, err := New(manifest); err == nil {
				t.Fatal("authority accepted invalid timing policy")
			}
		})
	}
}

func TestNewRejectsPlanOutsideRepository(t *testing.T) {
	manifest := fixtureManifest(t)
	outside := filepath.Join(t.TempDir(), "outside.md")
	writeFile(t, outside, []byte("outside"), 0o600)
	manifest.Plan.Path = outside
	manifest.Plan.SHA256 = fileHash(t, outside)

	_, err := New(manifest)
	if err == nil || !strings.Contains(err.Error(), "outside governed repository") {
		t.Fatalf("expected path-boundary error, got %v", err)
	}
}

func TestNewRejectsPlanSymlinkOutsideRepository(t *testing.T) {
	manifest := fixtureManifest(t)
	outside := filepath.Join(t.TempDir(), "outside.md")
	writeFile(t, outside, []byte("outside"), 0o600)
	link := filepath.Join(manifest.Repository.Path, "linked-plan.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	manifest.Plan.Path = "linked-plan.md"
	manifest.Plan.SHA256 = fileHash(t, outside)

	_, err := New(manifest)
	if err == nil || !strings.Contains(err.Error(), "outside governed repository") {
		t.Fatalf("expected symlink path-boundary error, got %v", err)
	}
}

func TestNewSupportsRalphexModes(t *testing.T) {
	for _, mode := range []ralphex.Mode{ralphex.ModeFull, ralphex.ModeTasksOnly, ralphex.ModeReview} {
		t.Run(string(mode), func(t *testing.T) {
			manifest := fixtureManifest(t)
			manifest.Ralphex.Mode = mode
			if mode == ralphex.ModeReview {
				manifest.Worktree = WorktreePolicy{}
			}
			authority, err := New(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if got := authority.Ralphex().Mode; got != mode {
				t.Fatalf("mode mismatch: got %q, want %q", got, mode)
			}
		})
	}
}

func TestNewRejectsReviewWorktree(t *testing.T) {
	manifest := fixtureManifest(t)
	manifest.Ralphex.Mode = ralphex.ModeReview
	_, err := New(manifest)
	if err == nil || !strings.Contains(err.Error(), "review mode") {
		t.Fatalf("expected review worktree rejection, got %v", err)
	}
}

func TestNewRejectsUnsupportedMode(t *testing.T) {
	manifest := fixtureManifest(t)
	manifest.Ralphex.Mode = "turbo"
	_, err := New(manifest)
	if err == nil || !strings.Contains(err.Error(), "unsupported ralphex mode") {
		t.Fatalf("expected unsupported mode error, got %v", err)
	}
}

func TestNewRequiresExplicitBranchForWorktree(t *testing.T) {
	manifest := fixtureManifest(t)
	manifest.Worktree.Branch = ""
	_, err := New(manifest)
	if err == nil || !strings.Contains(err.Error(), "worktree.branch") {
		t.Fatalf("expected missing worktree branch error, got %v", err)
	}
}

func TestNewCanonicalizesPathsAndProducesStableHash(t *testing.T) {
	manifest := fixtureManifest(t)
	repositoryLink := filepath.Join(t.TempDir(), "repository-link")
	if err := os.Symlink(manifest.Repository.Path, repositoryLink); err != nil {
		t.Fatal(err)
	}
	firstInput := cloneManifest(manifest)
	firstInput.Repository.Path = repositoryLink
	firstInput.Plan.Path = filepath.Join("docs", "..", "plan.md")
	firstInput.Repository.Remotes = map[string]string{
		"upstream": "https://example.test/upstream.git",
		"origin":   "https://example.test/example/project.git",
	}

	secondInput := cloneManifest(manifest)
	secondInput.Ralphex.Timeout = "600s"
	secondInput.Ralphex.WaitOnLimit = "0"
	secondInput.Acceptance[0].Timeout = "300s"
	secondInput.Repository.Remotes = map[string]string{
		"origin":   "https://example.test/example/project.git",
		"upstream": "https://example.test/upstream.git",
	}

	first, err := New(firstInput)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(secondInput)
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256() != second.SHA256() {
		t.Fatalf("same semantic manifest hashed differently:\n%s\n%s", first.SHA256(), second.SHA256())
	}
	if string(first.CanonicalJSON()) != string(second.CanonicalJSON()) {
		t.Fatalf("canonical serialization differs:\n%s\n%s", first.CanonicalJSON(), second.CanonicalJSON())
	}
	if got := first.Repository().Path; got != manifest.Repository.Path {
		t.Fatalf("repository path was not canonicalized: got %q", got)
	}
	if got := first.Plan().Path; got != filepath.Join(manifest.Repository.Path, "plan.md") {
		t.Fatalf("plan path was not canonicalized: got %q", got)
	}
}

func TestNewValidatesAndCanonicalizesExecutorPolicy(t *testing.T) {
	manifest := fixtureManifest(t)
	manifest.Executor.Executor = "  CODEX  "
	canonical, err := New(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if got := canonical.Executor().Executor; got != "codex" {
		t.Fatalf("canonical executor = %q, want codex", got)
	}

	equivalent := cloneManifest(manifest)
	equivalent.Executor.Executor = "codex"
	plain, err := New(equivalent)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.SHA256() != plain.SHA256() {
		t.Fatalf("equivalent executor policies produced different hashes: %s != %s", canonical.SHA256(), plain.SHA256())
	}

	invalid := cloneManifest(manifest)
	invalid.Executor.Executor = "bogus"
	if _, err := New(invalid); err == nil || !strings.Contains(err.Error(), "unsupported executor") {
		t.Fatalf("expected unsupported executor rejection, got %v", err)
	}

	invalid = cloneManifest(manifest)
	invalid.Executor.TaskModel = "model:high"
	if _, err := New(invalid); err == nil || !strings.Contains(err.Error(), "task_model") {
		t.Fatalf("expected invalid task model rejection, got %v", err)
	}
}

func TestAuthorityDoesNotExposeMutableState(t *testing.T) {
	manifest := fixtureManifest(t)
	authority, err := New(manifest)
	if err != nil {
		t.Fatal(err)
	}
	wantHash := authority.SHA256()
	wantJSON := string(authority.CanonicalJSON())

	manifest.Repository.Remotes["origin"] = "changed"
	manifest.Acceptance[0].Argv[0] = "changed"
	copyManifest := authority.Manifest()
	copyManifest.Repository.Remotes["origin"] = "changed-again"
	copyManifest.Acceptance[0].Argv[0] = "changed-again"
	bytes := authority.CanonicalJSON()
	bytes[0] = '['

	if authority.SHA256() != wantHash || string(authority.CanonicalJSON()) != wantJSON {
		t.Fatal("validated authority changed through mutable input or accessor")
	}
	if got := authority.Acceptance()[0].Argv[0]; got != "go" {
		t.Fatalf("acceptance argv mutated: got %q", got)
	}
}

func TestNewRejectsHashMismatch(t *testing.T) {
	manifest := fixtureManifest(t)
	manifest.Plan.SHA256 = strings.Repeat("0", 64)
	_, err := New(manifest)
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("expected hash mismatch error, got %v", err)
	}
}

func TestNewValidatesOptionalContextCapsuleBinding(t *testing.T) {
	manifest := boundCapsuleManifest(t)
	governed, err := New(manifest)
	if err != nil {
		t.Fatal(err)
	}
	binding, present := governed.ContextCapsule()
	if !present || binding.SHA256 != manifest.ContextCapsule.SHA256 {
		t.Fatalf("canonical authority lost capsule binding: %+v, %t", binding, present)
	}
	manifest.ContextCapsule.Path = "mutated"
	if got, _ := governed.ContextCapsule(); got.Path == "mutated" {
		t.Fatal("authority exposed mutable capsule binding")
	}
}

func TestNewRejectsContextCapsuleHashMismatch(t *testing.T) {
	manifest := boundCapsuleManifest(t)
	manifest.ContextCapsule.SHA256 = strings.Repeat("0", 64)
	if _, err := New(manifest); err == nil || !strings.Contains(err.Error(), "context capsule SHA256") {
		t.Fatalf("expected capsule binding hash rejection, got %v", err)
	}
}

func TestNewRejectsContextCapsuleSourceDrift(t *testing.T) {
	manifest := boundCapsuleManifest(t)
	writeFile(t, filepath.Join(manifest.Repository.Path, "source.md"), []byte("drifted"), 0o600)
	if _, err := New(manifest); err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("expected capsule source-drift rejection, got %v", err)
	}
}

func TestNewPreservesPrePolicyManifestShape(t *testing.T) {
	manifest := fixtureManifest(t)
	governed, err := New(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := governed.ContextCapsule(); present {
		t.Fatal("pre-policy manifest unexpectedly gained a capsule binding")
	}
	if strings.Contains(string(governed.CanonicalJSON()), "context_capsule") {
		t.Fatal("optional capsule field changed canonical pre-policy manifest JSON")
	}
	if _, present := governed.MergeReviewPolicy(); present || strings.Contains(string(governed.CanonicalJSON()), "merge_review") {
		t.Fatal("optional merge review field changed canonical pre-policy manifest JSON")
	}
}

func TestNewFreezesAndCanonicalizesMergeReviewPolicy(t *testing.T) {
	manifest := fixtureManifest(t)
	manifest.MergeReview = &ReviewPolicy{PolicyIdentity: "review-v1", Required: []ReviewRequirement{
		{Component: "track-c", ReviewedSHA: strings.Repeat("c", 40), Verdict: "CLEAN_CRITICAL_MAJOR"},
		{Component: "track-b", ReviewedSHA: strings.Repeat("b", 40), Verdict: "CLEAN_CRITICAL_MAJOR"},
	}}
	governed, err := New(manifest)
	if err != nil {
		t.Fatal(err)
	}
	policy, present := governed.MergeReviewPolicy()
	if !present || len(policy.Required) != 2 || policy.Required[0].Component != "track-b" {
		t.Fatalf("canonical merge review policy = %#v, %t", policy, present)
	}
	manifest.MergeReview.Required[0].Component = "mutated-input"
	policy.Required[0].Component = "mutated-copy"
	unchanged, _ := governed.MergeReviewPolicy()
	if unchanged.Required[0].Component != "track-b" {
		t.Fatal("authority exposed mutable merge review state")
	}
}

func TestNewRejectsInvalidMergeReviewPolicy(t *testing.T) {
	manifest := fixtureManifest(t)
	manifest.MergeReview = &ReviewPolicy{PolicyIdentity: "review-v1", Required: []ReviewRequirement{
		{Component: "track-b", ReviewedSHA: strings.Repeat("b", 40), Verdict: "CLEAN_CRITICAL_MAJOR"},
		{Component: "track-b", ReviewedSHA: strings.Repeat("c", 40), Verdict: "CLEAN_CRITICAL_MAJOR"},
	}}
	if _, err := New(manifest); err == nil || !strings.Contains(err.Error(), "repeats component") {
		t.Fatalf("duplicate review requirement error = %v", err)
	}
}

func TestNewAdmitsV3BOnlyWithStructuralCapabilityAndCounters(t *testing.T) {
	manifest := v3BoundManifest(t)
	governed, err := New(manifest)
	if err != nil {
		t.Fatal(err)
	}
	admission, present := governed.Governance()
	if !present || admission.Operation != contextcapsule.OperationImplementation {
		t.Fatalf("V3 governance admission = %#v, present=%t", admission, present)
	}
	invalid := cloneManifest(manifest)
	invalid.Ralphex.Capability.IdleTimeoutFlag = false
	if _, err := New(invalid); err == nil || !strings.Contains(err.Error(), "EXECUTION_BOUNDS_INVALID") {
		t.Fatalf("unsupported V3 capability was admitted: %v", err)
	}
	invalid = cloneManifest(manifest)
	binary, err := os.ReadFile(invalid.Ralphex.BinaryPath)
	if err != nil {
		t.Fatal(err)
	}
	binary = []byte(strings.Replace(string(binary), "\"idle_timeout_flag\":true", "\"idle_timeout_flag\":false", 1))
	writeFile(t, invalid.Ralphex.BinaryPath, binary, 0o700)
	invalid.Ralphex.BinarySHA256 = fileHash(t, invalid.Ralphex.BinaryPath)
	invalid.Ralphex.Capability.BinarySHA256 = invalid.Ralphex.BinarySHA256
	if _, err := New(invalid); err == nil || !strings.Contains(err.Error(), "differs from the pinned binary probe") {
		t.Fatalf("caller-asserted capability overrode binary probe: %v", err)
	}
	// Restore the shared fixture binary before testing independent state input.
	manifest = v3BoundManifest(t)
	invalid = cloneManifest(manifest)
	invalid.Ralphex.ExecutionState = &ralphex.ExecutionStateV1{AggregateElapsed: "0s"}
	if _, err := New(invalid); err == nil || !strings.Contains(err.Error(), "controller-owned") {
		t.Fatalf("caller-owned cumulative state was admitted: %v", err)
	}
}

func TestNewRejectsV2ABCGovernanceAssertionWithoutActivation(t *testing.T) {
	manifest := boundCapsuleManifest(t)
	head := manifest.Repository.StartSHA
	spec := contextcapsule.Spec{
		PolicyVersion: contextcapsule.PolicyVersionV2, Project: "ABCP", Plan: "legacy", RoadmapPhase: "test", ExecutionPack: "test",
		Task: "Task 1", Repository: manifest.Repository.Identity, BaseSHA: head,
		OperationContext: &contextcapsule.OperationContext{Kind: contextcapsule.OperationImplementation, OwnedScope: []string{"task"}, BlockingCriteria: []string{"major"}},
		Invariants:       []string{"Fail closed."}, NonGoals: []string{"No network."}, PredecessorOutcomes: []contextcapsule.Outcome{}, Sources: []string{"source.md"},
	}
	_, data, err := contextcapsule.Build(manifest.Repository.Path, spec)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, manifest.ContextCapsule.Path, data, 0o600)
	manifest.ContextCapsule.SHA256 = fileHash(t, manifest.ContextCapsule.Path)
	manifest.Governance = &GovernanceManifest{Operation: contextcapsule.OperationImplementation}
	if _, err := New(manifest); err == nil || !strings.Contains(err.Error(), "cannot assert A/B/C") {
		t.Fatalf("V2 governance assertion was admitted: %v", err)
	}
}

func TestControllerAdmissionRejectsPostActivationV2WhenGovernanceIsOmitted(t *testing.T) {
	manifest := boundCapsuleManifest(t)
	head := manifest.Repository.StartSHA
	spec := contextcapsule.Spec{
		PolicyVersion: contextcapsule.PolicyVersionV2, Project: "ABCP", Plan: "legacy", RoadmapPhase: "test", ExecutionPack: "test",
		Task: "Task 1", Repository: manifest.Repository.Identity, BaseSHA: head,
		OperationContext: &contextcapsule.OperationContext{Kind: contextcapsule.OperationImplementation, OwnedScope: []string{"task"}, BlockingCriteria: []string{"major"}},
		Invariants:       []string{"Fail closed."}, NonGoals: []string{"No network."}, PredecessorOutcomes: []contextcapsule.Outcome{}, Sources: []string{"source.md"},
	}
	_, data, err := contextcapsule.Build(manifest.Repository.Path, spec)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, manifest.ContextCapsule.Path, data, 0o600)
	manifest.ContextCapsule.SHA256 = fileHash(t, manifest.ContextCapsule.Path)
	legacy, err := New(manifest)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := governancev3.OpenControllerV1(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.AdmitWorkflowAuthority(contextcapsule.PolicyVersionV2, legacy.SHA256()); err != nil {
		t.Fatal(err)
	}
	activation, err := governancev3.SealGovernanceActivationV1(governancev3.GovernanceActivationV1{
		Kind: "GovernanceActivationV1", PolicyVersion: contextcapsule.PolicyVersionV3, PolicySHA256: strings.Repeat("a", 64),
		ActivationRepositoryCommit: strings.Repeat("b", 40), ActivationSequence: 2, ActivationTime: "1970-01-01T00:00:00Z", GrandfatheredV2Digests: []string{legacy.SHA256()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.InstallActivationV1(activation); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWithGovernanceController(manifest, controller); err != nil {
		t.Fatalf("exact grandfathered authority was rejected: %v", err)
	}
	manifest.RunID = "post-activation-copy"
	if _, err := NewWithGovernanceController(manifest, controller); governancev3.ClassOf(err) != governancev3.CapsuleLineageInvalid {
		t.Fatalf("omitted governance bypass class = %q, err=%v", governancev3.ClassOf(err), err)
	}
}

func v3BoundManifest(t *testing.T) Manifest {
	t.Helper()
	manifest := boundCapsuleManifest(t)
	writeFile(t, manifest.Ralphex.BinaryPath, []byte("#!/bin/sh\nif [ \"$1\" = \"--abcp-governance-capability-v1\" ]; then printf '%s\\n' '{\"kind\":\"RalphexCapabilityProbeV1\",\"source_sha\":\"abcdef0123456789\",\"max_iterations_flag\":true,\"session_timeout_flag\":true,\"idle_timeout_flag\":true,\"skip_finalize_flag\":true,\"base_ref_flag\":true,\"executor_model_effort_flags\":true,\"isolated_config\":true,\"governed_handoff\":\"TASKS_ONLY\",\"linux_containment\":true}'; exit 0; fi\nexit 0\n"), 0o700)
	manifest.Ralphex.BinarySHA256 = fileHash(t, manifest.Ralphex.BinaryPath)
	planPath := filepath.Join(manifest.Repository.Path, manifest.Plan.Path)
	writeFile(t, planPath, []byte("### Task 1: bounded\n\n- [ ] implement\n"), 0o600)
	gitAuthorityCommand(t, manifest.Repository.Path, "add", "plan.md")
	gitAuthorityCommand(t, manifest.Repository.Path, "commit", "-m", "bounded plan")
	head := gitAuthorityCommand(t, manifest.Repository.Path, "rev-parse", "HEAD")
	manifest.Repository.StartSHA = head
	manifest.Plan.SHA256 = fileHash(t, planPath)
	bounds := &contextcapsule.ExecutionBoundsV1{
		MaxIterations: 3, SessionTimeout: "30m0s", IdleTimeout: "10m0s", WallClockTimeout: "1h0m0s", AggregateWallClockTimeout: "2h0m0s",
		MaxIncompleteTasks: 1, MaxInitialActiveFindings: 3, MaxRalphexInvocations: 2, MaxReviewReports: 3,
		MaxMutationLeases: 2, MaxTotalFixBatches: 2, MaxChangedFiles: 10, MaxChangedBytes: 1000,
	}
	spec := contextcapsule.Spec{
		PolicyVersion: contextcapsule.PolicyVersionV3, Project: "ABCP", Plan: "v3", RoadmapPhase: "test", ExecutionPack: "test",
		Task: "Task 1", Repository: manifest.Repository.Identity, BaseSHA: head, Invariants: []string{"Fail closed."},
		NonGoals: []string{"No network."}, PredecessorOutcomes: []contextcapsule.Outcome{}, Sources: []string{"source.md"},
		PhaseAuthority: &contextcapsule.PhaseAuthorityV3{
			Stage: contextcapsule.StageBImplementation, AllowedOperations: []contextcapsule.OperationKind{contextcapsule.OperationImplementation, contextcapsule.OperationImplementationReview},
			Parent:                 &contextcapsule.PhaseParentV1{CapsuleFileSHA256: strings.Repeat("a", 64), CapsuleSHA256: strings.Repeat("b", 64), Stage: contextcapsule.StageADesign, CheckpointSHA256: strings.Repeat("c", 64), CandidateSHA: head, GrantSHA256: strings.Repeat("d", 64)},
			SemanticRegistrySHA256: strings.Repeat("e", 64), ObservationScopeIDs: []string{"rule.one"}, BlockingScopeIDs: []string{"rule.one"}, MutationScopeIDs: []string{"rule.one"},
			AuthorizedFindingIDs: []string{}, AuthorizedInvariantIDs: []string{"rule.one"}, AllowedPaths: []string{"source.md"}, ReviewProfile: contextcapsule.ReviewProfileInitialImplementation, ExecutionBounds: bounds,
		},
	}
	_, data, err := contextcapsule.Build(manifest.Repository.Path, spec)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, manifest.ContextCapsule.Path, data, 0o600)
	manifest.ContextCapsule.SHA256 = fileHash(t, manifest.ContextCapsule.Path)
	manifest.Ralphex.Mode = ralphex.ModeTasksOnly
	manifest.Ralphex.Timeout = bounds.WallClockTimeout
	manifest.Ralphex.WaitOnLimit = "0s"
	manifest.Executor.Executor = "codex"
	manifest.Executor.TaskEffort = "xhigh"
	manifest.Executor.ReviewEffort = "xhigh"
	manifest.Worktree = WorktreePolicy{}
	manifest.Ralphex.Capability = &ralphex.CapabilityV1{
		Kind: "RalphexCapabilityV1", BinarySHA256: manifest.Ralphex.BinarySHA256, SourceSHA: manifest.Ralphex.SourceSHA,
		MaxIterationsFlag: true, SessionTimeoutFlag: true, IdleTimeoutFlag: true, SkipFinalizeFlag: true, BaseRefFlag: true,
		ExecutorModelEffortFlags: true, IsolatedConfig: true, GovernedHandoff: ralphex.HandoffTasksOnly, LinuxContainment: true,
	}
	manifest.Ralphex.ExecutionState = nil
	manifest.Governance = &GovernanceManifest{Operation: contextcapsule.OperationImplementation, Mutation: true}
	return manifest
}

func boundCapsuleManifest(t *testing.T) Manifest {
	t.Helper()
	manifest := fixtureManifest(t)
	writeFile(t, filepath.Join(manifest.Repository.Path, "source.md"), []byte("governed source"), 0o600)
	gitAuthorityCommand(t, manifest.Repository.Path, "init", "-b", "main")
	gitAuthorityCommand(t, manifest.Repository.Path, "config", "user.email", "authority@example.test")
	gitAuthorityCommand(t, manifest.Repository.Path, "config", "user.name", "Authority Test")
	gitAuthorityCommand(t, manifest.Repository.Path, "remote", "add", "origin", "https://example.test/example/project.git")
	gitAuthorityCommand(t, manifest.Repository.Path, "add", "plan.md", "source.md")
	gitAuthorityCommand(t, manifest.Repository.Path, "commit", "-m", "governed inputs")
	head := gitAuthorityCommand(t, manifest.Repository.Path, "rev-parse", "HEAD")
	manifest.Repository.StartSHA = head
	spec := contextcapsule.Spec{
		PolicyVersion: contextcapsule.PolicyVersionV1,
		Project:       "ABCP", Plan: "EP-004", RoadmapPhase: "Phase 3", ExecutionPack: "EP-004",
		Task: "Task 1", Repository: "example/project", BaseSHA: head,
		Invariants: []string{"Fail closed."}, NonGoals: []string{"No retrieval."}, Sources: []string{"source.md"},
	}
	_, data, err := contextcapsule.Build(manifest.Repository.Path, spec)
	if err != nil {
		t.Fatal(err)
	}
	capsulePath := filepath.Join(t.TempDir(), "context-capsule.json")
	writeFile(t, capsulePath, data, 0o600)
	manifest.ContextCapsule = &ContextCapsuleManifest{Path: capsulePath, SHA256: fileHash(t, capsulePath)}
	return manifest
}

func gitAuthorityCommand(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func fixtureManifest(t *testing.T) Manifest {
	t.Helper()
	repository := t.TempDir()
	planPath := filepath.Join(repository, "plan.md")
	binaryPath := filepath.Join(t.TempDir(), "ralphex")
	writeFile(t, planPath, []byte("# governed plan\n"), 0o600)
	writeFile(t, binaryPath, []byte("#!/bin/sh\nexit 0\n"), 0o700)
	return Manifest{
		RunID: "run-123",
		Repository: RepositoryManifest{
			Path:          repository,
			Identity:      "example/project",
			Remotes:       map[string]string{"origin": "https://example.test/example/project.git"},
			DefaultBranch: "main",
			StartSHA:      "0123456789abcdef",
		},
		Plan: PlanManifest{
			Path:   "plan.md",
			SHA256: fileHash(t, planPath),
		},
		Ralphex: RalphexManifest{
			BinaryPath:   binaryPath,
			BinarySHA256: fileHash(t, binaryPath),
			SourceSHA:    "abcdef0123456789",
			Mode:         ralphex.ModeFull,
			Timeout:      "10m",
			WaitOnLimit:  "0s",
		},
		Executor: ExecutorPolicy{
			Executor:     "codex",
			TaskModel:    "gpt-test",
			TaskEffort:   "high",
			ReviewModel:  "gpt-review",
			ReviewEffort: "medium",
		},
		Worktree: WorktreePolicy{Enabled: true, Branch: "governed-plan"},
		Acceptance: []AcceptanceCommand{{
			Name:     "unit tests",
			Class:    "unit",
			Required: true,
			Timeout:  "5m",
			Argv:     []string{"go", "test", "./..."},
		}},
		PolicyVersion: "branch-v1",
	}
}

func writeFile(t *testing.T, path string, contents []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, contents, mode); err != nil {
		t.Fatal(err)
	}
}

func fileHash(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

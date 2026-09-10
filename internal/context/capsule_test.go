package context

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildIsDeterministicAndCompact(t *testing.T) {
	repository, head := capsuleRepository(t)
	spec := fixtureSpec(head)
	spec.Sources = []string{"docs/second.md", "docs/source.md"}
	first, firstJSON, err := Build(repository, spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Sources = []string{"docs/source.md", "docs/second.md"}
	second, secondJSON, err := Build(repository, spec)
	if err != nil {
		t.Fatal(err)
	}
	if first.CapsuleSHA256 != second.CapsuleSHA256 || !bytes.Equal(firstJSON, secondJSON) {
		t.Fatal("equivalent specs did not produce identical canonical capsules")
	}
	if first.Sources[0].Path != "docs/second.md" || first.Sources[1].Path != "docs/source.md" {
		t.Fatalf("sources are not canonically sorted: %+v", first.Sources)
	}
	if bytes.Contains(firstJSON, []byte("source document body that must not be copied")) {
		t.Fatal("capsule copied source contents instead of retaining a hash reference")
	}
	parsed, err := Parse(firstJSON)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := Verify(repository, parsed)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.OperationContext != nil || verified.PolicyVersion != PolicyVersionV1 || verified.OperationKind != "" {
		t.Fatalf("historical v1 capsule changed meaning: parsed=%+v verified=%+v", parsed.OperationContext, verified)
	}
	if bytes.Contains(firstJSON, []byte("operation_context")) {
		t.Fatal("historical v1 canonical JSON gained v2 fields")
	}
}

func TestBuildV2BindsOperationContext(t *testing.T) {
	repository, head := capsuleRepository(t)
	spec := v2FixtureSpec(head, OperationImplementation)
	capsule, data, err := Build(repository, spec)
	if err != nil {
		t.Fatal(err)
	}
	if capsule.OperationContext == nil || capsule.OperationContext.Kind != OperationImplementation {
		t.Fatalf("v2 operation context = %+v", capsule.OperationContext)
	}
	parsed, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := Verify(repository, parsed)
	if err != nil {
		t.Fatal(err)
	}
	if verified.PolicyVersion != PolicyVersionV2 || verified.OperationKind != OperationImplementation || verified.BaseSHA != head {
		t.Fatalf("v2 verification = %+v", verified)
	}
	if len(parsed.NonGoals) == 0 || len(parsed.PredecessorOutcomes) == 0 || len(parsed.Sources) == 0 {
		t.Fatalf("v2 omitted bound context: %+v", parsed)
	}
}

func TestBuildV2RequiresOperationFields(t *testing.T) {
	repository, head := capsuleRepository(t)
	tests := []struct {
		name   string
		mutate func(*Spec)
		field  string
	}{
		{name: "operation context", mutate: func(spec *Spec) { spec.OperationContext = nil }, field: "operation_context"},
		{name: "recognized kind", mutate: func(spec *Spec) { spec.OperationContext.Kind = "unknown" }, field: "operation kind"},
		{name: "owned scope", mutate: func(spec *Spec) { spec.OperationContext.OwnedScope = nil }, field: "owned_scope"},
		{name: "blocking criteria", mutate: func(spec *Spec) { spec.OperationContext.BlockingCriteria = nil }, field: "blocking_criteria"},
		{name: "explicit non-goals", mutate: func(spec *Spec) { spec.NonGoals = nil }, field: "non_goals"},
		{name: "exact base", mutate: func(spec *Spec) { spec.BaseSHA = "" }, field: "base_sha"},
		{name: "predecessor outcomes", mutate: func(spec *Spec) { spec.PredecessorOutcomes = nil }, field: "predecessor_outcomes"},
		{name: "hashed sources", mutate: func(spec *Spec) { spec.Sources = nil }, field: "sources"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := v2FixtureSpec(head, OperationImplementation)
			test.mutate(&spec)
			if _, _, err := Build(repository, spec); err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("expected %s rejection, got %v", test.field, err)
			}
		})
	}
}

func TestBuildV2RecognizesEveryOperationKind(t *testing.T) {
	repository, head := capsuleRepository(t)
	kinds := []OperationKind{
		OperationDesignPlanning, OperationDesignReview, OperationImplementation,
		OperationImplementationReview, OperationAcceptance, OperationMergeAuthorization,
		OperationDeployment, OperationRecovery, OperationMaintenance,
	}
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			if _, _, err := Build(repository, v2FixtureSpec(head, kind)); err != nil {
				t.Fatalf("recognized operation kind %q was rejected: %v", kind, err)
			}
		})
	}
}

func TestBuildV1RejectsV2OperationContext(t *testing.T) {
	repository, head := capsuleRepository(t)
	spec := fixtureSpec(head)
	spec.OperationContext = v2FixtureSpec(head, OperationImplementation).OperationContext
	if _, _, err := Build(repository, spec); err == nil || !strings.Contains(err.Error(), "not valid") {
		t.Fatalf("expected mixed-version rejection, got %v", err)
	}
}

func TestVerifyRejectsChangedSource(t *testing.T) {
	repository, head := capsuleRepository(t)
	_, data, err := Build(repository, fixtureSpec(head))
	if err != nil {
		t.Fatal(err)
	}
	capsule, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	writeContextFile(t, filepath.Join(repository, "docs", "source.md"), []byte("changed"))
	if _, err := Verify(repository, capsule); err == nil || !strings.Contains(err.Error(), "SHA256 mismatch") {
		t.Fatalf("expected changed-source rejection, got %v", err)
	}
}

func TestBuildRejectsWrongBaseSHA(t *testing.T) {
	repository, head := capsuleRepository(t)
	spec := fixtureSpec(head)
	spec.BaseSHA = strings.Repeat("0", 40)
	if _, _, err := Build(repository, spec); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected base-SHA rejection, got %v", err)
	}
}

func TestBuildRejectsWrongRepositoryIdentity(t *testing.T) {
	repository, head := capsuleRepository(t)
	spec := fixtureSpec(head)
	spec.Repository = "different/project"
	if _, _, err := Build(repository, spec); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected repository-identity rejection, got %v", err)
	}
}

func TestBuildRejectsTraversalAndSymlinks(t *testing.T) {
	repository, head := capsuleRepository(t)
	spec := fixtureSpec(head)
	spec.Sources = []string{"../outside.md"}
	if _, _, err := Build(repository, spec); err == nil || !strings.Contains(err.Error(), "canonical") && !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("expected traversal rejection, got %v", err)
	}

	outside := filepath.Join(t.TempDir(), "outside.md")
	writeContextFile(t, outside, []byte("outside"))
	link := filepath.Join(repository, "docs", "linked.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	spec.Sources = []string{"docs/linked.md"}
	if _, _, err := Build(repository, spec); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink rejection, got %v", err)
	}
}

func TestBuildRejectsDuplicateSource(t *testing.T) {
	repository, head := capsuleRepository(t)
	spec := fixtureSpec(head)
	spec.Sources = []string{"docs/source.md", "docs/source.md"}
	if _, _, err := Build(repository, spec); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate-source rejection, got %v", err)
	}
}

func TestParseRejectsOversizeOrNonCanonicalCapsule(t *testing.T) {
	if _, err := Parse(make([]byte, MaxCapsuleBytes+1)); err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("expected oversize rejection, got %v", err)
	}
	repository, head := capsuleRepository(t)
	_, data, err := Build(repository, fixtureSpec(head))
	if err != nil {
		t.Fatal(err)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, data, "", "  "); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(pretty.Bytes()); err == nil || !strings.Contains(err.Error(), "not canonical") {
		t.Fatalf("expected non-canonical rejection, got %v", err)
	}
}

func TestBuildRejectsOversizeCapsule(t *testing.T) {
	repository, head := capsuleRepository(t)
	spec := fixtureSpec(head)
	large := strings.Repeat("x", MaxStringBytes)
	spec.Invariants = make([]string, MaxListItems)
	spec.NonGoals = make([]string, MaxListItems)
	for index := 0; index < MaxListItems; index++ {
		spec.Invariants[index] = large
		spec.NonGoals[index] = large
	}
	if _, _, err := Build(repository, spec); err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("expected built-capsule size rejection, got %v", err)
	}
}

func TestVerifyRejectsTamperedCapsuleHash(t *testing.T) {
	repository, head := capsuleRepository(t)
	capsule, _, err := Build(repository, fixtureSpec(head))
	if err != nil {
		t.Fatal(err)
	}
	capsule.Task = "tampered"
	if _, err := Verify(repository, capsule); err == nil || !strings.Contains(err.Error(), "capsule SHA256 mismatch") {
		t.Fatalf("expected capsule-hash rejection, got %v", err)
	}
}

func TestV3VerifyUsesImmutableBaseTree(t *testing.T) {
	repository, head := capsuleRepository(t)
	spec := v3FixtureSpec(head, StageADesign)
	capsule, data, err := Build(repository, spec)
	if err != nil {
		t.Fatal(err)
	}
	writeContextFile(t, filepath.Join(repository, "docs", "source.md"), []byte("mutable worktree drift"))
	gitContextCommand(t, repository, "add", "docs/source.md")
	gitContextCommand(t, repository, "commit", "-m", "later candidate")
	parsed, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(repository, parsed); err != nil {
		t.Fatalf("V3 verification read mutable checkout instead of base tree: %v", err)
	}
	if capsule.BaseSHA != head {
		t.Fatalf("V3 base changed: %s", capsule.BaseSHA)
	}
}

func TestV3RejectsStagePathAndFloorShapeViolations(t *testing.T) {
	repository, head := capsuleRepository(t)
	tests := []struct {
		name   string
		mutate func(*Spec)
		class  string
	}{
		{name: "B without parent", mutate: func(spec *Spec) { spec.PhaseAuthority.Parent = nil }, class: "CAPSULE_LINEAGE_INVALID"},
		{name: "block outside observation", mutate: func(spec *Spec) { spec.PhaseAuthority.ObservationScopeIDs = []string{} }, class: "CAPSULE_STAGE_INVALID"},
		{name: "broad root", mutate: func(spec *Spec) { spec.PhaseAuthority.AllowedPaths = []string{"**"} }, class: "CAPSULE_STAGE_INVALID"},
		{name: "bounds widen", mutate: func(spec *Spec) { spec.PhaseAuthority.ExecutionBounds.MaxIterations = 11 }, class: "EXECUTION_BOUNDS_INVALID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := v3FixtureSpec(head, StageBImplementation)
			test.mutate(&spec)
			if _, _, err := Build(repository, spec); err == nil || !strings.Contains(err.Error(), test.class) {
				t.Fatalf("expected %s, got %v", test.class, err)
			}
		})
	}
}

func TestV3RejectsSymlinkDerivedAllowedPath(t *testing.T) {
	repository, _ := capsuleRepository(t)
	if err := os.Symlink("docs", filepath.Join(repository, "linked-docs")); err != nil {
		t.Fatal(err)
	}
	gitContextCommand(t, repository, "add", "linked-docs")
	gitContextCommand(t, repository, "commit", "-m", "symlink path")
	head := gitContextCommand(t, repository, "rev-parse", "HEAD")
	spec := v3FixtureSpec(head, StageADesign)
	spec.PhaseAuthority.AllowedPaths = []string{"linked-docs/**"}
	if _, _, err := Build(repository, spec); err == nil || !strings.Contains(err.Error(), "non-directory") {
		t.Fatalf("expected symlink-derived allowed path rejection, got %v", err)
	}
}

func TestCanonicalGoldenCapsulesV1V2V3Stages(t *testing.T) {
	base := strings.Repeat("a", 40)
	sourceHash := strings.Repeat("b", 64)
	common := Capsule{
		Project: "ABCP", Plan: "golden", RoadmapPhase: "governance", ExecutionPack: "golden",
		Task: "Task 1", Repository: "example/project", BaseSHA: base,
		Invariants: []string{"Fail closed."}, NonGoals: []string{"No network."},
		PredecessorOutcomes: []Outcome{}, Sources: []Source{{Path: "docs/source.md", SHA256: sourceHash}},
	}
	v1 := common
	v1.PolicyVersion = PolicyVersionV1
	v2 := common
	v2.PolicyVersion = PolicyVersionV2
	v2.OperationContext = &OperationContext{Kind: OperationImplementation, OwnedScope: []string{"Task 1"}, BlockingCriteria: []string{"Major findings."}}
	stages := []struct {
		name        string
		capsule     Capsule
		wantPayload string
		wantJSON    string
	}{
		{name: "v1", capsule: v1, wantPayload: "d91aae2edaa5e9579fa135fd4b1075553981cd6b9d74e6677283b825110d315a", wantJSON: "3285975755bba5b12926d5c574f2b88740d9f9bd3bd986672c4de70d71116b84"},
		{name: "v2", capsule: v2, wantPayload: "e7ebca3cf597eb18afc1ff33d01f64a50646bedde77110ea1cdb17e6f0821456", wantJSON: "a58e827e4989f39fed6e192c1ee1e9a874cd0c8179ad0385d93064c92b9451ad"},
		{name: "A", capsule: goldenV3Capsule(common, StageADesign), wantPayload: "ae1cb622bfd30eb7478fbb4fed47df0e309d00b6f193ed15198dcd89b858cd1a", wantJSON: "0a037c8c85983a86e6218ee1a526c815986ed0df3446971b0359e64b01d329a2"},
		{name: "B", capsule: goldenV3Capsule(common, StageBImplementation), wantPayload: "3c639706853f591cdd8a004755205e667fdcc44eeb9bbefeff2e53d4dd255ccd", wantJSON: "2a25227399fda7c07d3eb4b059030cd43073211e072e36b42ba52ff9ca2eb6cb"},
		{name: "C", capsule: goldenV3Capsule(common, StageCAcceptanceMerge), wantPayload: "ff85948d84e3b5ae9e51b4117e540beae4775d3b8fd78781ac25fd525672418b", wantJSON: "8535a803b79c0a2187056284748ad911b81d37915983767a4236010e4cd3e6c0"},
	}
	for _, test := range stages {
		t.Run(test.name, func(t *testing.T) {
			digest, err := payloadHash(test.capsule)
			if err != nil {
				t.Fatal(err)
			}
			test.capsule.CapsuleSHA256 = digest
			data, err := json.Marshal(test.capsule)
			if err != nil {
				t.Fatal(err)
			}
			jsonDigest := sha256.Sum256(data)
			jsonHash := hex.EncodeToString(jsonDigest[:])
			if test.wantPayload == "" || test.wantJSON == "" {
				t.Fatalf("set golden payload=%s json=%s", digest, jsonHash)
			}
			if digest != test.wantPayload || jsonHash != test.wantJSON {
				t.Fatalf("golden changed\nwant payload=%s json=%s\n got payload=%s json=%s", test.wantPayload, test.wantJSON, digest, jsonHash)
			}
		})
	}
}

func v3FixtureSpec(head string, stage Stage) Spec {
	spec := fixtureSpec(head)
	spec.PolicyVersion = PolicyVersionV3
	spec.PredecessorOutcomes = []Outcome{}
	spec.PhaseAuthority = goldenPhaseAuthority(stage)
	return spec
}

func goldenV3Capsule(common Capsule, stage Stage) Capsule {
	capsule := common
	capsule.PolicyVersion = PolicyVersionV3
	capsule.PhaseAuthority = goldenPhaseAuthority(stage)
	return capsule
}

func goldenPhaseAuthority(stage Stage) *PhaseAuthorityV3 {
	authority := &PhaseAuthorityV3{
		Stage: stage, SemanticRegistrySHA256: strings.Repeat("c", 64),
		ObservationScopeIDs: []string{"rule.invariant"}, BlockingScopeIDs: []string{"rule.invariant"},
		MutationScopeIDs: []string{"rule.invariant"}, AuthorizedFindingIDs: []string{},
		AuthorizedInvariantIDs: []string{"rule.invariant"}, AllowedPaths: []string{"docs/**"}, ReviewProfile: ReviewProfileNone,
	}
	switch stage {
	case StageADesign:
		authority.AllowedOperations = []OperationKind{OperationDesignPlanning, OperationDesignReview}
	case StageBImplementation:
		authority.AllowedOperations = []OperationKind{OperationImplementation, OperationImplementationReview}
		authority.Parent = &PhaseParentV1{
			CapsuleFileSHA256: strings.Repeat("d", 64), CapsuleSHA256: strings.Repeat("e", 64), Stage: StageADesign,
			CheckpointSHA256: strings.Repeat("f", 64), CandidateSHA: strings.Repeat("a", 40), GrantSHA256: strings.Repeat("1", 64),
		}
		authority.ReviewProfile = ReviewProfileInitialImplementation
		authority.ExecutionBounds = goldenBounds()
	case StageCAcceptanceMerge:
		authority.AllowedOperations = []OperationKind{OperationAcceptance, OperationFinalReview, OperationMergeAuthorization, OperationPostMergeAcceptance, OperationPRPublication}
		authority.Parent = &PhaseParentV1{
			CapsuleFileSHA256: strings.Repeat("d", 64), CapsuleSHA256: strings.Repeat("e", 64), Stage: StageBImplementation,
			CheckpointSHA256: strings.Repeat("f", 64), CandidateSHA: strings.Repeat("a", 40), GrantSHA256: strings.Repeat("1", 64),
		}
		authority.MutationScopeIDs = []string{}
	}
	return authority
}

func goldenBounds() *ExecutionBoundsV1 {
	return &ExecutionBoundsV1{
		MaxIterations: 3, SessionTimeout: "30m0s", IdleTimeout: "10m0s", WallClockTimeout: "1h0m0s", AggregateWallClockTimeout: "2h0m0s",
		Finalize: false, MaxIncompleteTasks: 1, MaxInitialActiveFindings: 4, MaxRalphexInvocations: 3,
		MaxReviewReports: 4, MaxMutationLeases: 3, MaxTotalFixBatches: 3, MaxChangedFiles: 12, MaxChangedBytes: 5000,
	}
}

func capsuleRepository(t *testing.T) (string, string) {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repository")
	gitContextCommand(t, "", "init", "-b", "main", repository)
	gitContextCommand(t, repository, "config", "user.email", "capsule@example.test")
	gitContextCommand(t, repository, "config", "user.name", "Capsule Test")
	gitContextCommand(t, repository, "remote", "add", "origin", "https://example.test/example/project.git")
	if err := os.MkdirAll(filepath.Join(repository, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeContextFile(t, filepath.Join(repository, "docs", "source.md"), []byte("source document body that must not be copied"))
	writeContextFile(t, filepath.Join(repository, "docs", "second.md"), []byte("second source"))
	gitContextCommand(t, repository, "add", "docs")
	gitContextCommand(t, repository, "commit", "-m", "source documents")
	return repository, gitContextCommand(t, repository, "rev-parse", "HEAD")
}

func fixtureSpec(head string) Spec {
	return Spec{
		PolicyVersion: PolicyVersionV1,
		Project:       "Autonomous Builder Control Plane",
		Plan:          "EP-004 plan",
		RoadmapPhase:  "Phase 3",
		ExecutionPack: "EP-004",
		Task:          "Task 1",
		Repository:    "example/project",
		BaseSHA:       head,
		Invariants:    []string{"Fail closed on authority drift."},
		NonGoals:      []string{"No semantic retrieval."},
		PredecessorOutcomes: []Outcome{{
			Task: "EP-003", Summary: "Added recovery control.", CommitSHA: head,
		}},
		Sources: []string{"docs/source.md"},
	}
}

func v2FixtureSpec(head string, kind OperationKind) Spec {
	spec := fixtureSpec(head)
	spec.PolicyVersion = PolicyVersionV2
	spec.OperationContext = &OperationContext{
		Kind:             kind,
		OwnedScope:       []string{"The exact task and files named by this capsule."},
		BlockingCriteria: []string{"Current owned-scope Critical or Major findings."},
	}
	return spec
}

func writeContextFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitContextCommand(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

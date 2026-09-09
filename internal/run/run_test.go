package run

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	contextcapsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
)

func TestRunnerSuccessReachesBranchAcceptedWithOrderedEvidence(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "true"))
	result := fixture.execute(t)
	if !result.Accepted() {
		t.Fatalf("expected accepted result, got state %s, reason %q", result.State, result.FailureReason)
	}
	if result.Ralphex.Outcome != supervisor.OutcomeSucceeded {
		t.Fatalf("unexpected Ralphex outcome %s", result.Ralphex.Outcome)
	}
	if len(result.Ralphex.Argv) < 3 || result.Ralphex.Argv[0] != fixture.authority.Ralphex().BinaryPath || result.Ralphex.Argv[1] != "--config-dir" {
		t.Fatalf("Ralphex argv lacks isolated config directory: %#v", result.Ralphex.Argv)
	}
	configDir := result.Ralphex.Argv[2]
	wantArgs := []string{
		"--codex", "--wait", "0s", "--task-model", "test-model:xhigh", "--review-model", "test-review:xhigh", "--tasks-only", fixture.authority.Plan().Path,
	}
	if !reflect.DeepEqual(result.Ralphex.Argv[3:], wantArgs) {
		t.Fatalf("Ralphex args mismatch\nwant: %#v\n got: %#v", wantArgs, result.Ralphex.Argv[3:])
	}
	if filepath.Base(configDir) == ".ralphex" || !strings.HasPrefix(filepath.Base(configDir), "abcp-ralphex-config-") {
		t.Fatalf("Ralphex config directory was not controller-owned and isolated: %q", configDir)
	}
	if _, err := os.Stat(configDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary Ralphex config directory was not removed: %v", err)
	}
	if result.Ralphex.Timeout != 5*time.Second {
		t.Fatalf("Ralphex timeout = %s, want 5s", result.Ralphex.Timeout)
	}

	events := readEvents(t, fixture.ledgerPath)
	wantStates := []domain.State{
		domain.StateRunCreated,
		domain.StateAuthorityValidated,
		domain.StateExecutionStarting,
		domain.StateImplementing,
		domain.StateImplementationCompleted,
		domain.StateBranchAcceptancePending,
		domain.StateBranchAccepted,
	}
	if got := eventStates(events); !reflect.DeepEqual(got, wantStates) {
		t.Fatalf("event state order mismatch\nwant: %#v\n got: %#v", wantStates, got)
	}
	assertEventEvidence(t, events)
	for _, event := range events {
		if event.StateTo == domain.StateIntegrationPending || event.StateTo == domain.StateReadyForMerge ||
			event.StateTo == domain.StateMerged || event.StateTo == domain.StateCompleted {
			t.Fatalf("EP-002 emitted prohibited state %s", event.StateTo)
		}
	}
	if len(events[len(events)-1].EvidenceRefs) < 8 {
		t.Fatalf("BRANCH_ACCEPTED lacks complete acceptance evidence: %#v", events[len(events)-1].EvidenceRefs)
	}
}

func TestRunnerRalphexFailureRecordsEvidenceWithoutImplementationCompleted(t *testing.T) {
	fixture := newRunFixture(t, 17, commandPath(t, "true"))
	result := fixture.execute(t)
	if result.State != domain.StateFailed || result.Ralphex.ExitCode != 17 {
		t.Fatalf("unexpected failure result: state=%s exit=%d", result.State, result.Ralphex.ExitCode)
	}
	if result.Accepted() {
		t.Fatal("Ralphex failure must not be accepted")
	}

	events := readEvents(t, fixture.ledgerPath)
	wantStates := []domain.State{
		domain.StateRunCreated,
		domain.StateAuthorityValidated,
		domain.StateExecutionStarting,
		domain.StateImplementing,
		domain.StateFailed,
	}
	if got := eventStates(events); !reflect.DeepEqual(got, wantStates) {
		t.Fatalf("event state order mismatch\nwant: %#v\n got: %#v", wantStates, got)
	}
	terminal := events[len(events)-1]
	if len(terminal.EvidenceRefs) != 3 {
		t.Fatalf("Ralphex failure should reference stdout, stderr, and metadata: %#v", terminal.EvidenceRefs)
	}
	assertEventEvidence(t, events)
}

func TestRunnerAppliesGovernedRalphexTimeout(t *testing.T) {
	fixture := newRunFixtureWithScript(t, "#!/bin/sh\nwhile :; do sleep 60; done\n", authority.WorktreePolicy{}, commandPath(t, "true"))
	manifest := fixture.authority.Manifest()
	manifest.Ralphex.Timeout = "50ms"
	governed, err := authority.New(manifest)
	if err != nil {
		t.Fatal(err)
	}
	fixture.authority = governed
	result := fixture.execute(t)
	if result.State != domain.StateFailed || result.Ralphex.Outcome != supervisor.OutcomeTimedOut {
		t.Fatalf("timed Ralphex result = %#v", result)
	}
}

func TestRunnerReverifiesBoundContextCapsuleBeforeRalphexLaunch(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "true"))
	manifest := fixture.authority.Manifest()
	sourcePath := filepath.Join(manifest.Repository.Path, "context.md")
	writeTestFile(t, sourcePath, []byte("governed context"), 0o600)
	runGit(t, manifest.Repository.Path, "add", "context.md")
	runGit(t, manifest.Repository.Path, "commit", "-m", "context source")
	manifest.Repository.StartSHA = runGit(t, manifest.Repository.Path, "rev-parse", "HEAD")
	spec := contextcapsule.Spec{
		PolicyVersion: contextcapsule.PolicyVersionV2,
		Project:       "ABCP", Plan: "EP-004", RoadmapPhase: "Phase 3", ExecutionPack: "EP-004",
		Task: "Task 1", Repository: "example/project", BaseSHA: manifest.Repository.StartSHA,
		OperationContext: &contextcapsule.OperationContext{
			Kind: contextcapsule.OperationImplementation, OwnedScope: []string{"Task 1"},
			BlockingCriteria: []string{"Current owned-scope Critical or Major findings."},
		},
		Invariants: []string{"Fail closed."}, NonGoals: []string{"No retrieval."},
		PredecessorOutcomes: []contextcapsule.Outcome{}, Sources: []string{"context.md"},
	}
	_, capsuleJSON, err := contextcapsule.Build(manifest.Repository.Path, spec)
	if err != nil {
		t.Fatal(err)
	}
	capsulePath := filepath.Join(t.TempDir(), "capsule.json")
	writeTestFile(t, capsulePath, capsuleJSON, 0o600)
	manifest.ContextCapsule = &authority.ContextCapsuleManifest{Path: capsulePath, SHA256: testHash(t, capsulePath)}
	fixture.authority, err = authority.New(manifest)
	if err != nil {
		t.Fatal(err)
	}
	runner := fixture.runner(t)

	writeTestFile(t, sourcePath, []byte("drift after authority construction"), 0o600)
	result, err := runner.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "verify context capsule before execution") {
		t.Fatalf("expected pre-launch capsule verification failure, got result=%+v err=%v", result, err)
	}
	if _, statErr := os.Stat(filepath.Join(manifest.Repository.Path, "candidate.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("Ralphex launched despite capsule drift: %v", statErr)
	}
}

func TestNewRequiresVerifiedV2OperationCapsule(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "true"))

	missing := fixture.authority.Manifest()
	missing.ContextCapsule = nil
	governed, err := authority.New(missing)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.construct(t, governed); err == nil || !strings.Contains(err.Error(), "verified v2 context capsule is required") {
		t.Fatalf("missing capsule error = %v", err)
	}

	historical := fixture.authority.Manifest()
	bindHistoricalCapsule(t, &historical)
	governed, err = authority.New(historical)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.construct(t, governed); err == nil || !strings.Contains(err.Error(), contextcapsule.PolicyVersionV2) {
		t.Fatalf("historical v1 capsule authorized new execution: %v", err)
	}
}

func TestNewRequiresExactOperationBase(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "true"))
	manifest := fixture.authority.Manifest()
	manifest.Repository.StartSHA = strings.Repeat("0", 40)
	governed, err := authority.New(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.construct(t, governed); err == nil || !strings.Contains(err.Error(), "does not match governed start SHA") {
		t.Fatalf("operation/base mismatch error = %v", err)
	}
}

func TestNewEnforcesCodexXHighEffort(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "true"))
	tests := []struct {
		name   string
		mutate func(*authority.ExecutorPolicy)
		field  string
	}{
		{name: "high task", mutate: func(policy *authority.ExecutorPolicy) { policy.TaskEffort = "high" }, field: "task_effort"},
		{name: "empty task", mutate: func(policy *authority.ExecutorPolicy) { policy.TaskEffort = "" }, field: "task_effort"},
		{name: "high review", mutate: func(policy *authority.ExecutorPolicy) { policy.ReviewEffort = "high" }, field: "review_effort"},
		{name: "empty review", mutate: func(policy *authority.ExecutorPolicy) { policy.ReviewEffort = "" }, field: "review_effort"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := fixture.authority.Manifest()
			test.mutate(&manifest.Executor)
			governed, err := authority.New(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.construct(t, governed); err == nil || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("effort policy error = %v", err)
			}
		})
	}
	if _, err := fixture.construct(t, fixture.authority); err != nil {
		t.Fatalf("xhigh Codex policy was rejected: %v", err)
	}
}

func TestNewEnforcesOperationKindAndRalphexModeMapping(t *testing.T) {
	tests := []struct {
		name    string
		kind    contextcapsule.OperationKind
		mode    ralphex.Mode
		wantErr bool
	}{
		{name: "implementation full", kind: contextcapsule.OperationImplementation, mode: ralphex.ModeFull},
		{name: "implementation tasks-only", kind: contextcapsule.OperationImplementation, mode: ralphex.ModeTasksOnly},
		{name: "design review", kind: contextcapsule.OperationDesignReview, mode: ralphex.ModeReview},
		{name: "implementation review", kind: contextcapsule.OperationImplementationReview, mode: ralphex.ModeReview},
		{name: "design review cannot run full", kind: contextcapsule.OperationDesignReview, mode: ralphex.ModeFull, wantErr: true},
		{name: "design review cannot run tasks-only", kind: contextcapsule.OperationDesignReview, mode: ralphex.ModeTasksOnly, wantErr: true},
		{name: "implementation review cannot run full", kind: contextcapsule.OperationImplementationReview, mode: ralphex.ModeFull, wantErr: true},
		{name: "implementation review cannot run tasks-only", kind: contextcapsule.OperationImplementationReview, mode: ralphex.ModeTasksOnly, wantErr: true},
		{name: "implementation cannot run review", kind: contextcapsule.OperationImplementation, mode: ralphex.ModeReview, wantErr: true},
		{name: "acceptance cannot run review", kind: contextcapsule.OperationAcceptance, mode: ralphex.ModeReview, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunFixture(t, 0, commandPath(t, "true"))
			manifest := fixture.authority.Manifest()
			manifest.Ralphex.Mode = test.mode
			bindOperationCapsule(t, &manifest, test.kind)
			governed, err := authority.New(manifest)
			if err != nil {
				t.Fatal(err)
			}
			_, err = fixture.construct(t, governed)
			if test.wantErr {
				if err == nil || !strings.Contains(err.Error(), "requires operation kind") {
					t.Fatalf("operation kind %q with mode %q error = %v", test.kind, test.mode, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("operation kind %q with mode %q rejected: %v", test.kind, test.mode, err)
			}
		})
	}
}

func TestNewRejectsMultipleIncompleteImplementationSectionsInEveryImplementationMode(t *testing.T) {
	for _, mode := range []ralphex.Mode{ralphex.ModeFull, ralphex.ModeTasksOnly} {
		t.Run(string(mode), func(t *testing.T) {
			fixture := newRunFixture(t, 0, commandPath(t, "true"))
			manifest := fixture.authority.Manifest()
			manifest.Ralphex.Mode = mode
			plan := []byte("### Task 1: first\n\n- [ ] first action\n\n### Task 2: second\n\n- [ ] second action\n")
			writeTestFile(t, manifest.Plan.Path, plan, 0o600)
			manifest.Plan.SHA256 = testHash(t, manifest.Plan.Path)
			governed, err := authority.New(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.construct(t, governed); err == nil || !strings.Contains(err.Error(), "2 incomplete executable") {
				t.Fatalf("multiple incomplete task error = %v", err)
			}
		})
	}
}

func TestCountIncompleteExecutableSections(t *testing.T) {
	tests := []struct {
		name string
		plan string
		want int
	}{
		{
			name: "backtick fence",
			plan: "## Overview\n- [ ] not executable\n\n### Task 1: done\n- [x] complete\n\n```md\n### Task 99: example\n- [ ] ignored\n```\n\n### Iteration 2: active\n- [ ] action\n",
			want: 1,
		},
		{
			name: "indented backtick fence with longer closer",
			plan: "   ````markdown\n### Task 98: example\n- [ ] ignored\n```\n   `````  \n\n### Task 1: active\n- [ ] action\n",
			want: 1,
		},
		{
			name: "tilde fence with backticks in info string",
			plan: "~~~ language=`markdown`\n### Task 97: example\n- [ ] ignored\n  ~~~\n\n### Task 1: active\n- [ ] action\n",
			want: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sections, err := countIncompleteExecutableSections([]byte(test.plan))
			if err != nil {
				t.Fatal(err)
			}
			if sections != test.want {
				t.Fatalf("incomplete executable sections = %d, want %d", sections, test.want)
			}
		})
	}
}

func TestCountIncompleteExecutableSectionsDoesNotTreatFourSpaceIndentAsFence(t *testing.T) {
	plan := []byte("    ```\n\n### Task 1: first\n- [ ] first action\n\n### Task 2: second\n- [ ] second action\n")
	sections, err := countIncompleteExecutableSections(plan)
	if err != nil {
		t.Fatal(err)
	}
	if sections != 2 {
		t.Fatalf("four-space-indented backticks hid executable sections: got %d, want 2", sections)
	}
}

func TestCountIncompleteExecutableSectionsRejectsUnterminatedFence(t *testing.T) {
	plan := []byte("```markdown\n### Task 1: hidden\n- [ ] hidden action\n")
	if _, err := countIncompleteExecutableSections(plan); err == nil || !strings.Contains(err.Error(), "unterminated Markdown") {
		t.Fatalf("unterminated fence error = %v", err)
	}
}

func TestRunnerRalphexCancellationRecordsCancelledWithoutCompletion(t *testing.T) {
	readyPath := filepath.Join(t.TempDir(), "ralphex-ready")
	script := fmt.Sprintf("#!/bin/sh\nprintf ready > %s\nwhile :; do sleep 60; done\n", readyPath)
	fixture := newRunFixtureWithScript(t, script, authority.WorktreePolicy{}, commandPath(t, "true"))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	type response struct {
		result Result
		err    error
	}
	done := make(chan response, 1)
	runner := fixture.runner(t)
	go func() {
		result, err := runner.Run(ctx)
		done <- response{result: result, err: err}
	}()
	waitForRunFile(t, readyPath)
	cancel()
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.result.State != domain.StateCancelled || got.result.Ralphex.Outcome != supervisor.OutcomeCanceled {
		t.Fatalf("canceled run result = %#v", got.result)
	}
	want := []domain.State{domain.StateRunCreated, domain.StateAuthorityValidated, domain.StateExecutionStarting, domain.StateImplementing, domain.StateCancelled}
	if states := eventStates(readEvents(t, fixture.ledgerPath)); !reflect.DeepEqual(states, want) {
		t.Fatalf("canceled Ralphex states = %#v, want %#v", states, want)
	}
}

func TestRunnerAcceptanceFailureStopsBeforeBranchAccepted(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "false"))
	result := fixture.execute(t)
	if result.State != domain.StateFailed {
		t.Fatalf("expected FAILED, got %s", result.State)
	}
	if result.Acceptance.Passed() || result.Accepted() {
		t.Fatal("failed controller acceptance must not yield BRANCH_ACCEPTED")
	}

	events := readEvents(t, fixture.ledgerPath)
	wantStates := []domain.State{
		domain.StateRunCreated,
		domain.StateAuthorityValidated,
		domain.StateExecutionStarting,
		domain.StateImplementing,
		domain.StateImplementationCompleted,
		domain.StateBranchAcceptancePending,
		domain.StateFailed,
	}
	if got := eventStates(events); !reflect.DeepEqual(got, wantStates) {
		t.Fatalf("event state order mismatch\nwant: %#v\n got: %#v", wantStates, got)
	}
	assertEventEvidence(t, events)
}

func TestRunnerAcceptanceCancellationRecordsCancelled(t *testing.T) {
	readyPath := filepath.Join(t.TempDir(), "acceptance-ready")
	t.Setenv("GO_WANT_RUN_WAIT_HELPER", "1")
	fixture := newRunFixture(t, 0, os.Args[0], "-test.run=^TestRunnerWaitHelper$", "--", readyPath)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	type response struct {
		result Result
		err    error
	}
	done := make(chan response, 1)
	runner := fixture.runner(t)
	go func() {
		result, err := runner.Run(ctx)
		done <- response{result: result, err: err}
	}()
	waitForRunFile(t, readyPath)
	cancel()
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.result.State != domain.StateCancelled {
		t.Fatalf("acceptance cancellation state = %s, want CANCELLED", got.result.State)
	}
	want := []domain.State{
		domain.StateRunCreated, domain.StateAuthorityValidated, domain.StateExecutionStarting,
		domain.StateImplementing, domain.StateImplementationCompleted,
		domain.StateBranchAcceptancePending, domain.StateCancelled,
	}
	if states := eventStates(readEvents(t, fixture.ledgerPath)); !reflect.DeepEqual(states, want) {
		t.Fatalf("acceptance cancellation states = %#v, want %#v", states, want)
	}
}

func TestRunnerWorktreeAcceptsActualCandidateBranch(t *testing.T) {
	worktreePath := filepath.Join(t.TempDir(), "fake-ralphex-worktree")
	script := fmt.Sprintf(`#!/bin/sh
branch=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--branch" ]; then branch="$2"; shift 2; else shift; fi
done
git worktree add -q -b "$branch" %s HEAD || exit 20
printf 'candidate only\n' > %s/candidate.txt
git -C %s add candidate.txt || exit 21
git -C %s commit -qm 'candidate implementation' || exit 22
git worktree remove -f %s || exit 23
`, worktreePath, worktreePath, worktreePath, worktreePath, worktreePath)
	fixture := newRunFixtureWithScript(t, script, authority.WorktreePolicy{Enabled: true, Branch: "candidate-branch"}, commandPath(t, "test"), "-f", "candidate.txt")
	result := fixture.execute(t)
	if !result.Accepted() {
		t.Fatalf("worktree candidate was not accepted: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(fixture.authority.Repository().Path, "candidate.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default checkout unexpectedly contains candidate file: %v", err)
	}
	gitEvidence, ok := result.Acceptance.FinalGit()
	if !ok || gitEvidence.Branch != "candidate-branch" {
		t.Fatalf("candidate Git evidence = %#v, captured=%t", gitEvidence, ok)
	}
	wantHead := runGit(t, fixture.authority.Repository().Path, "rev-parse", "candidate-branch")
	if gitEvidence.HeadSHA != wantHead {
		t.Fatalf("accepted HEAD = %s, want candidate %s", gitEvidence.HeadSHA, wantHead)
	}
	if records := result.Acceptance.Commands(); len(records) != 1 || records[0].Process.Cwd == fixture.authority.Repository().Path {
		t.Fatalf("acceptance did not run in isolated candidate checkout: %#v", records)
	}
}

func TestRunnerRejectsNonWorktreeCandidateOutsideGovernedHistory(t *testing.T) {
	script := `#!/bin/sh
git switch --orphan unrelated >/dev/null 2>&1 || exit 20
git rm -rf --cached . >/dev/null 2>&1 || true
rm -f plan.md
printf 'unrelated\n' > unrelated.txt
git add unrelated.txt || exit 22
git commit -qm 'unrelated root' || exit 23
`
	fixture := newRunFixtureWithScript(t, script, authority.WorktreePolicy{}, commandPath(t, "true"))
	result, err := fixture.runner(t).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "does not descend from governed start SHA") {
		t.Fatalf("expected unauthorized-history rejection, got %v", err)
	}
	if result.Accepted() || result.State != domain.StateFailed {
		t.Fatalf("unrelated candidate result = %#v", result)
	}
}

func TestRunnerRejectsUnchangedImplementationCandidate(t *testing.T) {
	fixture := newRunFixtureWithScript(t, "#!/bin/sh\nexit 0\n", authority.WorktreePolicy{}, commandPath(t, "true"))
	result, err := fixture.runner(t).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "did not advance beyond governed start SHA") {
		t.Fatalf("expected unchanged candidate rejection, got %v", err)
	}
	if result.Accepted() || result.State != domain.StateFailed {
		t.Fatalf("unchanged candidate result = %#v", result)
	}
}

func TestRunnerRejectsIdentityChangedAfterValidation(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "true"))
	if err := os.WriteFile(fixture.authority.Plan().Path, []byte("changed plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.runner(t).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "plan SHA256 changed") {
		t.Fatalf("expected pinned plan validation error, got %v", err)
	}
	if result.State != domain.StateFailed {
		t.Fatalf("expected FAILED, got %s", result.State)
	}
	if got := eventStates(readEvents(t, fixture.ledgerPath)); !reflect.DeepEqual(got, []domain.State{domain.StateRunCreated, domain.StateFailed}) {
		t.Fatalf("unexpected states after identity rejection: %#v", got)
	}
}

func TestRunnerRejectsDirtyInitialWorkingTree(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "true"))
	if err := os.WriteFile(filepath.Join(fixture.authority.Repository().Path, "untracked.txt"), []byte("outside authority\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.runner(t).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "working tree is not clean") {
		t.Fatalf("expected dirty working-tree rejection, got %v", err)
	}
	if result.State != domain.StateFailed {
		t.Fatalf("dirty repository state = %s, want FAILED", result.State)
	}
	if got := eventStates(readEvents(t, fixture.ledgerPath)); !reflect.DeepEqual(got, []domain.State{domain.StateRunCreated, domain.StateFailed}) {
		t.Fatalf("unexpected states after dirty repository rejection: %#v", got)
	}
}

func TestRalphexEnvironmentUsesAuthorityBoundContextCapsule(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "true"))
	governed := fixture.authority
	capsule, present := governed.ContextCapsule()
	if !present {
		t.Fatal("validated authority lost context capsule binding")
	}

	t.Setenv("ABCP_CONTEXT_CAPSULE_PATH", "/tmp/attacker-capsule.json")
	t.Setenv("ABCP_CONTEXT_CAPSULE_SHA256", strings.Repeat("0", 64))

	got := environmentMap(ralphexEnvironment(governed.Executor().Executor, capsule))
	if got["ABCP_CONTEXT_CAPSULE_PATH"] != capsule.Path {
		t.Fatalf("capsule path = %q, want authority path %q", got["ABCP_CONTEXT_CAPSULE_PATH"], capsule.Path)
	}
	if got["ABCP_CONTEXT_CAPSULE_SHA256"] != capsule.SHA256 {
		t.Fatalf("capsule SHA256 = %q, want authority SHA256 %q", got["ABCP_CONTEXT_CAPSULE_SHA256"], capsule.SHA256)
	}
}

func environmentMap(environment []string) map[string]string {
	result := make(map[string]string, len(environment))
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			result[key] = value
		}
	}
	return result
}

func TestRunnerUsesAllowlistedRalphexEnvironment(t *testing.T) {
	t.Setenv("ABCP_TEST_SECRET", "must-not-leak")
	t.Setenv("OPENAI_API_KEY", "authorized-provider-key")
	t.Setenv("ABCP_CONTEXT_CAPSULE_PATH", "/attacker/ambient-capsule.json")
	t.Setenv("ABCP_CONTEXT_CAPSULE_SHA256", strings.Repeat("f", 64))
	script := `#!/bin/sh
if [ -n "$ABCP_TEST_SECRET" ]; then exit 41; fi
if [ "$OPENAI_API_KEY" != "authorized-provider-key" ]; then exit 42; fi
if [ "$ABCP_CONTEXT_CAPSULE_PATH" = "/attacker/ambient-capsule.json" ]; then exit 45; fi
if [ ! -f "$ABCP_CONTEXT_CAPSULE_PATH" ]; then exit 46; fi
actual_capsule_sha="$(sha256sum "$ABCP_CONTEXT_CAPSULE_PATH" | awk '{print $1}')"
if [ "$actual_capsule_sha" != "$ABCP_CONTEXT_CAPSULE_SHA256" ]; then exit 47; fi
printf 'candidate\n' > candidate.txt
git add candidate.txt || exit 43
git commit -qm 'candidate implementation' || exit 44
`
	fixture := newRunFixtureWithScript(t, script, authority.WorktreePolicy{}, commandPath(t, "true"))
	result := fixture.execute(t)
	if !result.Accepted() {
		t.Fatalf("allowlisted environment run was not accepted: %#v", result)
	}
	metadata, err := os.ReadFile(result.RalphexMetadataRef.URI)
	if err != nil {
		t.Fatal(err)
	}
	var recorded struct {
		EnvironmentPolicy string `json:"environment_policy"`
	}
	if err := json.Unmarshal(metadata, &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded.EnvironmentPolicy != ralphexEnvironmentPolicy {
		t.Fatalf("environment policy = %q, want %q", recorded.EnvironmentPolicy, ralphexEnvironmentPolicy)
	}
}

func TestRunnerIgnoresAmbientGitRepositoryOverrides(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "true"))
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), "attacker.git"))
	t.Setenv("GIT_WORK_TREE", t.TempDir())
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "core.fsmonitor")
	t.Setenv("GIT_CONFIG_VALUE_0", "attacker-helper")
	t.Setenv("ABCP_CONTROLLER_SECRET", "must-not-reach-git")

	result := fixture.execute(t)
	if !result.Accepted() {
		t.Fatalf("ambient Git overrides redirected governed validation: %#v", result)
	}
}

func TestRunnerAcceptsNonWorktreeCandidateFromCleanDetachedCheckout(t *testing.T) {
	script := `#!/bin/sh
printf 'candidate\n' > candidate.txt
git add candidate.txt || exit 20
git commit -qm 'candidate implementation' || exit 21
printf 'uncommitted output\n' > uncommitted.txt
`
	fixture := newRunFixtureWithScript(t, script, authority.WorktreePolicy{}, commandPath(t, "test"), "!", "-e", "uncommitted.txt")

	result := fixture.execute(t)
	if !result.Accepted() {
		t.Fatalf("committed candidate was not accepted independently of uncommitted Ralphex output: %#v", result)
	}
	records := result.Acceptance.Commands()
	if len(records) != 1 || records[0].Process.Cwd == fixture.authority.Repository().Path {
		t.Fatalf("non-worktree acceptance did not use a clean detached checkout: %#v", records)
	}
	if _, err := os.Stat(filepath.Join(fixture.authority.Repository().Path, "uncommitted.txt")); err != nil {
		t.Fatalf("fixture did not leave uncommitted Ralphex output in source checkout: %v", err)
	}
}

func TestRunnerCancellationDuringIdentityValidationRecordsCancelled(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "true"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := fixture.runner(t).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != domain.StateCancelled {
		t.Fatalf("identity-validation cancellation state = %s, want CANCELLED", result.State)
	}
	want := []domain.State{domain.StateRunCreated, domain.StateCancelled}
	if states := eventStates(readEvents(t, fixture.ledgerPath)); !reflect.DeepEqual(states, want) {
		t.Fatalf("identity-validation cancellation states = %#v, want %#v", states, want)
	}
}

func TestRunnerRejectsRepositorySubdirectoryAsGovernedRoot(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "true"))
	manifest := fixture.authority.Manifest()
	subdirectory := filepath.Join(manifest.Repository.Path, "subdirectory")
	if err := os.Mkdir(subdirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(subdirectory, "plan.md")
	if err := os.Rename(manifest.Plan.Path, planPath); err != nil {
		t.Fatal(err)
	}
	manifest.Repository.Path = subdirectory
	manifest.Plan.Path = planPath
	manifest.Plan.SHA256 = testHash(t, planPath)
	if _, err := authority.New(manifest); err == nil || !strings.Contains(err.Error(), "not Git root") {
		t.Fatalf("expected capsule-bound repository-root rejection, got %v", err)
	}
}

func TestRunnerRejectsAdditionalUnpinnedRemote(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "true"))
	runGit(t, fixture.authority.Repository().Path, "remote", "add", "unexpected", "https://example.test/unexpected.git")
	result, err := fixture.runner(t).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "remote set") {
		t.Fatalf("expected complete remote-set rejection, got %v", err)
	}
	if result.State != domain.StateFailed {
		t.Fatalf("additional-remote state = %s, want FAILED", result.State)
	}
}

func TestRunnerRejectsUngovernedPushURL(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "true"))
	runGit(t, fixture.authority.Repository().Path, "remote", "set-url", "--add", "--push", "origin", "https://attacker.example/steal/project.git")
	result, err := fixture.runner(t).Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "push URL does not match governed URL") {
		t.Fatalf("expected ungoverned push URL rejection, got %v", err)
	}
	if result.State != domain.StateFailed {
		t.Fatalf("ungoverned push URL state = %s, want FAILED", result.State)
	}
}

func TestRunnerAllowsRepositoryLocalRalphexRuntimeState(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "true"))
	ralphexDir := filepath.Join(fixture.authority.Repository().Path, ".ralphex")
	if err := os.MkdirAll(ralphexDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(ralphexDir, ".gitignore"), []byte(".gitignore\nprogress/\nworktrees/\n"), 0o600)
	if err := os.MkdirAll(filepath.Join(ralphexDir, "progress"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(ralphexDir, "progress", "run.txt"), []byte("runtime progress\n"), 0o600)
	if err := os.MkdirAll(filepath.Join(ralphexDir, "worktrees", "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(ralphexDir, "worktrees", "run", "HEAD"), []byte("runtime worktree\n"), 0o600)

	result, err := fixture.runner(t).Run(context.Background())
	if err != nil {
		t.Fatalf("benign repository-local Ralphex runtime state was rejected: %v", err)
	}
	if result.State != domain.StateBranchAccepted {
		t.Fatalf("benign Ralphex runtime state result = %s, want BRANCH_ACCEPTED", result.State)
	}
}

func TestRunnerRejectsRepositoryLocalRalphexConfiguration(t *testing.T) {
	for _, surface := range []string{"config", "prompts", "agents"} {
		t.Run(surface, func(t *testing.T) {
			fixture := newRunFixture(t, 0, commandPath(t, "true"))
			overrideFile := filepath.Join(fixture.authority.Repository().Path, ".ralphex", surface)
			if surface != "config" {
				overrideFile = filepath.Join(overrideFile, "override.md")
			}
			if err := os.MkdirAll(filepath.Dir(overrideFile), 0o700); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, overrideFile, []byte("local override\n"), 0o600)
			runGit(t, fixture.authority.Repository().Path, "add", filepath.Join(".ralphex", surface))
			runGit(t, fixture.authority.Repository().Path, "commit", "-m", "add local Ralphex override")
			fixture = fixture.atCurrentHead(t)

			result, err := fixture.runner(t).Run(context.Background())
			if err == nil || !strings.Contains(err.Error(), "repository-local .ralphex/"+surface+" configuration is not allowed") {
				t.Fatalf("expected local Ralphex %s rejection, got %v", surface, err)
			}
			if result.State != domain.StateFailed {
				t.Fatalf("local Ralphex %s state = %s, want FAILED", surface, result.State)
			}
		})
	}
}

func TestRunnerRejectsUnsafeRepositoryLocalRalphexBoundary(t *testing.T) {
	for _, test := range []struct {
		name       string
		create     func(*testing.T, string)
		wantReason string
	}{
		{
			name: "symlink",
			create: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Symlink(t.TempDir(), path); err != nil {
					t.Fatal(err)
				}
			},
			wantReason: "must not be a symlink",
		},
		{
			name: "regular file",
			create: func(t *testing.T, path string) {
				t.Helper()
				writeTestFile(t, path, []byte("not a directory\n"), 0o600)
			},
			wantReason: "must be a directory",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRunFixture(t, 0, commandPath(t, "true"))
			boundary := filepath.Join(fixture.authority.Repository().Path, ".ralphex")
			test.create(t, boundary)
			runGit(t, fixture.authority.Repository().Path, "add", ".ralphex")
			runGit(t, fixture.authority.Repository().Path, "commit", "-m", "add unsafe Ralphex boundary")
			fixture = fixture.atCurrentHead(t)

			result, err := fixture.runner(t).Run(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.wantReason) {
				t.Fatalf("expected unsafe Ralphex boundary rejection, got %v", err)
			}
			if result.State != domain.StateFailed {
				t.Fatalf("unsafe Ralphex boundary state = %s, want FAILED", result.State)
			}
		})
	}
}

func TestRunnerTransitionRejectsInvalidDomainEdgeBeforeAppend(t *testing.T) {
	fixture := newRunFixture(t, 0, commandPath(t, "true"))
	events := &recordingEventAppender{}
	runner := &Runner{governed: fixture.authority, events: events}

	err := runner.transition(domain.StateRunCreated, domain.StateImplementing, "test", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "transition RUN_CREATED -> IMPLEMENTING is not allowed") {
		t.Fatalf("expected invalid domain transition rejection, got %v", err)
	}
	if len(events.events) != 0 {
		t.Fatalf("invalid transition appended %d events", len(events.events))
	}
}

func TestEP002StateCapExcludesIntegrationAndCompletion(t *testing.T) {
	for _, state := range []domain.State{
		domain.StateIntegrationPending,
		domain.StateReadyForMerge,
		domain.StateMerged,
		domain.StateCompleted,
	} {
		if ep002State(state) {
			t.Fatalf("EP-002 runner unexpectedly permits %s", state)
		}
	}
}

type runFixture struct {
	authority  authority.Authority
	ledgerPath string
	evidence   string
}

type recordingEventAppender struct {
	events []ledger.Event
}

func (a *recordingEventAppender) Append(event ledger.Event) error {
	a.events = append(a.events, event)
	return nil
}

func newRunFixture(t *testing.T, ralphexExit int, acceptanceArgv ...string) runFixture {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
printf 'fake ralphex stdout\n'
printf 'fake ralphex stderr\n' >&2
if [ %s -ne 0 ]; then exit %s; fi
printf 'candidate\n' > candidate.txt
git add candidate.txt || exit 90
git commit -qm 'candidate implementation' || exit 91
`, strconv.Itoa(ralphexExit), strconv.Itoa(ralphexExit))
	return newRunFixtureWithScript(t, script, authority.WorktreePolicy{}, acceptanceArgv...)
}

func newRunFixtureWithScript(t *testing.T, script string, worktree authority.WorktreePolicy, acceptanceArgv ...string) runFixture {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repository")
	runGit(t, "", "init", "-b", "main", repository)
	runGit(t, repository, "config", "user.email", "controller@example.test")
	runGit(t, repository, "config", "user.name", "Controller Test")
	runGit(t, repository, "remote", "add", "origin", "https://example.test/example/project.git")
	planPath := filepath.Join(repository, "plan.md")
	writeTestFile(t, planPath, []byte("# governed plan\n"), 0o600)
	writeTestFile(t, filepath.Join(repository, "context.md"), []byte("governed operation context\n"), 0o600)
	runGit(t, repository, "add", "plan.md", "context.md")
	runGit(t, repository, "commit", "-m", "initial plan")
	startSHA := runGit(t, repository, "rev-parse", "HEAD")

	binaryPath := filepath.Join(t.TempDir(), "fake-ralphex")
	writeTestFile(t, binaryPath, []byte(script), 0o700)

	manifest := authority.Manifest{
		RunID: "run-fixture",
		Repository: authority.RepositoryManifest{
			Path:          repository,
			Identity:      "example/project",
			Remotes:       map[string]string{"origin": "https://example.test/example/project.git"},
			DefaultBranch: "main",
			StartSHA:      startSHA,
		},
		Plan: authority.PlanManifest{Path: planPath, SHA256: testHash(t, planPath)},
		Ralphex: authority.RalphexManifest{
			BinaryPath: binaryPath, BinarySHA256: testHash(t, binaryPath), SourceSHA: "source-test", Mode: ralphex.ModeTasksOnly, Timeout: "5s", WaitOnLimit: "0s",
		},
		Executor: authority.ExecutorPolicy{
			Executor: "codex", TaskModel: "test-model", TaskEffort: "xhigh",
			ReviewModel: "test-review", ReviewEffort: "xhigh",
		},
		Worktree: worktree,
		Acceptance: []authority.AcceptanceCommand{{
			Name: "deterministic check", Class: "unit", Required: true, Timeout: "5s", Argv: acceptanceArgv,
		}},
		PolicyVersion: "branch-test-v1",
	}
	bindOperationCapsule(t, &manifest, contextcapsule.OperationImplementation)
	governed, err := authority.New(manifest)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	return runFixture{
		authority:  governed,
		ledgerPath: filepath.Join(root, "events", "run.jsonl"),
		evidence:   filepath.Join(root, "evidence"),
	}
}

func TestRunnerWaitHelper(t *testing.T) {
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 {
		return
	}
	if separator+1 >= len(os.Args) {
		os.Exit(90)
	}
	if err := os.WriteFile(os.Args[separator+1], []byte("ready"), 0o600); err != nil {
		os.Exit(91)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func waitForRunFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (f runFixture) runner(t *testing.T) *Runner {
	t.Helper()
	runner, err := f.construct(t, f.authority)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func (f runFixture) construct(t *testing.T, governed authority.Authority) (*Runner, error) {
	t.Helper()
	events, err := ledger.NewJSONLLedger(f.ledgerPath)
	if err != nil {
		return nil, err
	}
	artifacts, err := evidence.NewStore(f.evidence, governed.RunID())
	if err != nil {
		return nil, err
	}
	return New(governed, events, artifacts, supervisor.New())
}

func (f runFixture) execute(t *testing.T) Result {
	t.Helper()
	result, err := f.runner(t).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (f runFixture) atCurrentHead(t *testing.T) runFixture {
	t.Helper()
	manifest := f.authority.Manifest()
	manifest.Repository.StartSHA = runGit(t, manifest.Repository.Path, "rev-parse", "HEAD")
	bindOperationCapsule(t, &manifest, contextcapsule.OperationImplementation)
	governed, err := authority.New(manifest)
	if err != nil {
		t.Fatal(err)
	}
	f.authority = governed
	return f
}

func bindOperationCapsule(t *testing.T, manifest *authority.Manifest, kind contextcapsule.OperationKind) {
	t.Helper()
	spec := contextcapsule.Spec{
		PolicyVersion: contextcapsule.PolicyVersionV2,
		Project:       "ABCP", Plan: "governed plan", RoadmapPhase: "test", ExecutionPack: "test",
		Task: "Task 1", Repository: manifest.Repository.Identity, BaseSHA: manifest.Repository.StartSHA,
		OperationContext: &contextcapsule.OperationContext{
			Kind: kind, OwnedScope: []string{"Task 1"},
			BlockingCriteria: []string{"Current owned-scope Critical or Major findings."},
		},
		Invariants: []string{"Fail closed."}, NonGoals: []string{"No out-of-scope changes."},
		PredecessorOutcomes: []contextcapsule.Outcome{},
		Sources:             []string{"context.md"},
	}
	_, data, err := contextcapsule.Build(manifest.Repository.Path, spec)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "context-capsule.json")
	writeTestFile(t, path, data, 0o600)
	manifest.ContextCapsule = &authority.ContextCapsuleManifest{Path: path, SHA256: testHash(t, path)}
}

func bindHistoricalCapsule(t *testing.T, manifest *authority.Manifest) {
	t.Helper()
	spec := contextcapsule.Spec{
		PolicyVersion: contextcapsule.PolicyVersionV1,
		Project:       "ABCP", Plan: "historical plan", RoadmapPhase: "test", ExecutionPack: "test",
		Task: "Task 1", Repository: manifest.Repository.Identity, BaseSHA: manifest.Repository.StartSHA,
		Invariants: []string{"Fail closed."}, NonGoals: []string{"No out-of-scope changes."},
		Sources: []string{"context.md"},
	}
	_, data, err := contextcapsule.Build(manifest.Repository.Path, spec)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "historical-context-capsule.json")
	writeTestFile(t, path, data, 0o600)
	manifest.ContextCapsule = &authority.ContextCapsuleManifest{Path: path, SHA256: testHash(t, path)}
}

func readEvents(t *testing.T, path string) []ledger.Event {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var events []ledger.Event
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event ledger.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func eventStates(events []ledger.Event) []domain.State {
	states := make([]domain.State, 0, len(events))
	for _, event := range events {
		if event.EventType == string(domain.StateRunCreated) {
			states = append(states, domain.StateRunCreated)
			continue
		}
		states = append(states, event.StateTo)
	}
	return states
}

func assertEventEvidence(t *testing.T, events []ledger.Event) {
	t.Helper()
	for index, event := range events {
		if index > 0 && event.Timestamp.Before(events[index-1].Timestamp) {
			t.Fatalf("event %d timestamp precedes prior event", index)
		}
		for _, ref := range event.EvidenceRefs {
			data, err := os.ReadFile(ref.URI)
			if err != nil {
				t.Fatalf("read evidence %s: %v", ref.URI, err)
			}
			digest := sha256.Sum256(data)
			if hex.EncodeToString(digest[:]) != ref.SHA256 {
				t.Fatalf("evidence digest mismatch for %s", ref.URI)
			}
		}
	}
}

func runGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	if directory != "" {
		command.Dir = directory
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func commandPath(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func writeTestFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
}

func testHash(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

package integrationgate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/combinedacceptance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/integrationworkspace"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/scheduler"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
)

func TestCleanGateOwnsProvenanceAndReachesReadyForMerge(t *testing.T) {
	fixture := newGateFixture(t, false, "pass")
	result, err := fixture.gate.Run(context.Background(), fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != domain.StateReadyForMerge || result.Textual.Status() != integrationworkspace.StatusClean || result.Combined.Classification() != combinedacceptance.ClassificationClean {
		t.Fatalf("result = state %s textual %s combined %s", result.State, result.Textual.Status(), result.Combined.Classification())
	}
	want := []domain.State{domain.StateIntegrationPending, domain.StateIntegrating, domain.StateIntegrationAccepted, domain.StateReadyForMerge}
	if got := fixture.events.states(); !reflect.DeepEqual(got, want) {
		t.Fatalf("states = %v, want %v", got, want)
	}
	for _, event := range fixture.events.events {
		if len(event.EvidenceRefs) == 0 {
			t.Fatalf("transition %s has no evidence", event.StateTo)
		}
	}
	if entries, err := os.ReadDir(fixture.temporaryRoot); err != nil || len(entries) != 0 {
		t.Fatalf("disposable target remains: %v, %v", entries, err)
	}
	if result.Combined.Target().Integration.IntegrationID != result.Textual.SHA256() {
		t.Fatal("Track C provenance was not bound to the exact Track B result")
	}
}

func TestGateMapsTextualAndSemanticAndUnavailableOutcomes(t *testing.T) {
	tests := []struct {
		name     string
		conflict bool
		mode     string
		want     domain.State
	}{
		{"textual", true, "pass", domain.StateIntegrationConflict},
		{"semantic", false, "fail", domain.StateSemanticConflict},
		{"unavailable", false, "unavailable", domain.StateValidationUnavailable},
		{"target-mutation", false, "move-head", domain.StateValidationUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGateFixture(t, test.conflict, test.mode)
			result, _ := fixture.gate.Run(context.Background(), fixture.request)
			if result.State != test.want {
				t.Fatalf("state = %s, want %s (reason %q)", result.State, test.want, result.FailureReason)
			}
			for _, event := range fixture.events.events {
				if event.StateTo == domain.StateIntegrationAccepted || event.StateTo == domain.StateReadyForMerge {
					t.Fatalf("non-clean path emitted %s", event.StateTo)
				}
			}
		})
	}
}

func TestGateRejectsChangedCandidateHeadsReviewsBlockersAndEvidence(t *testing.T) {
	t.Run("changed-head", func(t *testing.T) {
		fixture := newGateFixture(t, false, "pass")
		runGit(t, fixture.repository, "update-ref", "refs/heads/candidate-one", fixture.base)
		_, err := fixture.gate.Run(context.Background(), fixture.request)
		assertErrorContains(t, err, "source head changed")
	})
	t.Run("replacement-ref", func(t *testing.T) {
		fixture := newGateFixture(t, false, "pass")
		head := fixture.request.Candidates[0].Input().HeadSHA
		runGit(t, fixture.repository, "replace", head, fixture.base)
		_, err := fixture.gate.Run(context.Background(), fixture.request)
		assertErrorContains(t, err, "replacement refs")
	})
	t.Run("review-sha", func(t *testing.T) {
		fixture := newGateFixture(t, false, "pass")
		fixture.request.Reviews[0].ReviewedSHA = strings.Repeat("c", 40)
		_, err := fixture.gate.Run(context.Background(), fixture.request)
		assertErrorContains(t, err, "SHA does not match")
	})
	t.Run("review-verdict", func(t *testing.T) {
		fixture := newGateFixture(t, false, "pass")
		fixture.request.Reviews[0].Verdict = "MAJOR_FINDING"
		_, err := fixture.gate.Run(context.Background(), fixture.request)
		assertErrorContains(t, err, "substantive non-clean verdict")
	})
	t.Run("unresolved-blocker", func(t *testing.T) {
		fixture := newGateFixture(t, false, "pass")
		fixture.request.Prerequisites.Blockers = []Blocker{{ID: "security-review", Resolved: false}}
		_, err := fixture.gate.Run(context.Background(), fixture.request)
		assertErrorContains(t, err, "unresolved blocker")
	})
	t.Run("missing-evidence", func(t *testing.T) {
		fixture := newGateFixture(t, false, "pass")
		if err := os.Remove(fixture.request.Reviews[0].Evidence[0].URI); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.gate.Run(context.Background(), fixture.request)
		assertErrorContains(t, err, "evidence")
	})
	t.Run("mutated-evidence", func(t *testing.T) {
		fixture := newGateFixture(t, false, "pass")
		ref := fixture.request.Candidates[0].Input().AcceptanceEvidence[0]
		if err := os.WriteFile(ref.URI, []byte("changed"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.gate.Run(context.Background(), fixture.request)
		assertErrorContains(t, err, "digest mismatch")
	})
}

func TestGateRequestHasNoCallerInjectableTrackBOrTrackCResult(t *testing.T) {
	typeOf := reflect.TypeOf(Request{})
	for _, forbidden := range []string{"Textual", "IntegratedHead", "Target", "Combined", "CombinedResult", "Provenance"} {
		if _, ok := typeOf.FieldByName(forbidden); ok {
			t.Fatalf("Request exposes caller-forgeable field %s", forbidden)
		}
	}
}

func TestGateInputEvidenceCanonicalizesEquivalentOrdering(t *testing.T) {
	fixture := newGateFixture(t, false, "pass")
	_, left, err := validateRequest(fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	rightRequest := fixture.request
	rightRequest.Reviews = []ReviewAttestation{fixture.request.Reviews[1], fixture.request.Reviews[0]}
	rightRequest.ReviewPolicy.Required = []ReviewRequirement{fixture.request.ReviewPolicy.Required[1], fixture.request.ReviewPolicy.Required[0]}
	_, right, err := validateRequest(rightRequest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("equivalent input ordering changed canonical evidence:\n%s\n%s", left, right)
	}
}

func TestCleanupFailureFailsClosed(t *testing.T) {
	fixture := newGateFixture(t, false, "pass")
	fixture.gate.workspace = &cleanupFailureWorkspace{delegate: fixture.gate.workspace}
	result, _ := fixture.gate.Run(context.Background(), fixture.request)
	if result.State != domain.StateValidationUnavailable || !strings.Contains(result.FailureReason, "cleanup failure") {
		t.Fatalf("cleanup result = %s, %q", result.State, result.FailureReason)
	}
	for _, state := range fixture.events.states() {
		if state == domain.StateIntegrationAccepted || state == domain.StateReadyForMerge {
			t.Fatalf("cleanup failure emitted %s", state)
		}
	}
}

type cleanupFailureWorkspace struct{ delegate workspaceController }

func (w *cleanupFailureWorkspace) Integrate(ctx context.Context, r integrationworkspace.Request) (integrationworkspace.Result, error) {
	return w.delegate.Integrate(ctx, r)
}
func (w *cleanupFailureWorkspace) UseMaterialized(ctx context.Context, r integrationworkspace.Request, result integrationworkspace.Result, prefix string, use func(integrationworkspace.MaterializedTarget) error) (integrationworkspace.MaterializationEvidence, error) {
	evidence, err := w.delegate.UseMaterialized(ctx, r, result, prefix, use)
	return evidence, errorsJoin(err, fmt.Errorf("cleanup failure"))
}
func errorsJoin(values ...error) error {
	var messages []string
	for _, value := range values {
		if value != nil {
			messages = append(messages, value.Error())
		}
	}
	if len(messages) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(messages, "; "))
}

type recordingEvents struct{ events []ledger.Event }

func (r *recordingEvents) Append(event ledger.Event) error {
	r.events = append(r.events, event)
	return nil
}
func (r *recordingEvents) states() []domain.State {
	values := make([]domain.State, len(r.events))
	for i, event := range r.events {
		values[i] = event.StateTo
	}
	return values
}

type gateFixture struct {
	gate                            *Gate
	request                         Request
	events                          *recordingEvents
	repository, base, temporaryRoot string
}

func newGateFixture(t *testing.T, conflict bool, mode string) gateFixture {
	t.Helper()
	repository := t.TempDir()
	runGit(t, repository, "init", "-q", "--initial-branch=main")
	runGit(t, repository, "config", "user.name", "Gate Test")
	runGit(t, repository, "config", "user.email", "gate@example.invalid")
	runGit(t, repository, "remote", "add", "origin", "https://example.test/example/project.git")
	writeFile(t, filepath.Join(repository, "plan.md"), "plan\n")
	writeFile(t, filepath.Join(repository, "base.txt"), "base\n")
	if conflict {
		writeFile(t, filepath.Join(repository, "conflict.txt"), "base\n")
	}
	runGit(t, repository, "add", "--all")
	runGit(t, repository, "commit", "-qm", "base")
	base := gitOutput(t, repository, "rev-parse", "HEAD")
	runGit(t, repository, "checkout", "-qb", "candidate-one", base)
	name := "one.txt"
	if conflict {
		name = "conflict.txt"
	}
	writeFile(t, filepath.Join(repository, name), "one\n")
	runGit(t, repository, "add", "--all")
	runGit(t, repository, "commit", "-qm", "one")
	headOne := gitOutput(t, repository, "rev-parse", "HEAD")
	runGit(t, repository, "checkout", "-qb", "candidate-two", base)
	name = "two.txt"
	if conflict {
		name = "conflict.txt"
	}
	writeFile(t, filepath.Join(repository, name), "two\n")
	runGit(t, repository, "add", "--all")
	runGit(t, repository, "commit", "-qm", "two")
	headTwo := gitOutput(t, repository, "rev-parse", "HEAD")
	root := t.TempDir()
	store, err := evidence.NewStore(root, "gate")
	if err != nil {
		t.Fatal(err)
	}
	candidates := []scheduler.AcceptedCandidate{newCandidate(t, store, repository, "candidate-one", base, headOne, "one", 1), newCandidate(t, store, repository, "candidate-two", base, headTwo, "two", 2)}
	analyzer, err := scheduler.NewAnalyzer(scheduler.RiskPolicy{PolicyIdentity: "risk-v1"})
	if err != nil {
		t.Fatal(err)
	}
	risk, err := analyzer.Analyze(context.Background(), candidates)
	if err != nil {
		t.Fatal(err)
	}
	command := authority.AcceptanceCommand{Name: "combined", Class: "unit", Required: true, Timeout: "5s", Argv: helperArgv(mode)}
	if mode == "unavailable" {
		command.Argv = []string{"/definitely/not/a/program"}
	}
	governed, err := authority.New(authority.Manifest{RunID: "gate-run", Repository: authority.RepositoryManifest{Path: repository, Identity: "example/project", Remotes: map[string]string{"origin": "https://example.test/example/project.git"}, DefaultBranch: "main", StartSHA: base}, Plan: authority.PlanManifest{Path: filepath.Join(repository, "plan.md"), SHA256: fileDigest(t, filepath.Join(repository, "plan.md"))}, Ralphex: authority.RalphexManifest{BinaryPath: os.Args[0], BinarySHA256: fileDigest(t, os.Args[0]), Mode: ralphex.ModeFull, Timeout: "5s", WaitOnLimit: "0s"}, Acceptance: []authority.AcceptanceCommand{command}, PolicyVersion: "policy-v1"})
	if err != nil {
		t.Fatal(err)
	}
	reviewOne := writeEvidence(t, store, "review-track-b.json", "review", []byte("track b clean"))
	reviewTwo := writeEvidence(t, store, "review-track-c.json", "review", []byte("track c clean"))
	shaB, shaC := strings.Repeat("a", 40), strings.Repeat("b", 40)
	request := Request{Authority: governed, BaselineSHA: base, Candidates: candidates, RiskReport: risk, CombinedPolicy: combinedacceptance.Policy{PolicyIdentity: "semantic-v1", CombinedAcceptancePolicyIdentity: "policy-v1", IntegrationPolicyIdentity: "integration-v1", RiskPolicyIdentity: "risk-v1", SemanticFailureClasses: []string{"unit"}}, ReviewPolicy: ReviewPolicy{PolicyIdentity: "review-v1", Required: []ReviewRequirement{{Component: "track-b", ReviewedSHA: shaB, Verdict: VerdictCleanCriticalMajor}, {Component: "track-c", ReviewedSHA: shaC, Verdict: VerdictCleanCriticalMajor}}}, Reviews: []ReviewAttestation{{Component: "track-b", ReviewedSHA: shaB, Verdict: VerdictCleanCriticalMajor, Provider: "reviewer-b", Evidence: []ledger.EvidenceRef{reviewOne}}, {Component: "track-c", ReviewedSHA: shaC, Verdict: VerdictCleanCriticalMajor, Provider: "reviewer-c", Evidence: []ledger.EvidenceRef{reviewTwo}}}, EvidencePrefix: "serial"}
	events := &recordingEvents{}
	temporary := t.TempDir()
	gate, err := New(Config{TemporaryRoot: temporary, Events: events, Artifacts: store, Processes: supervisor.New()})
	if err != nil {
		t.Fatal(err)
	}
	return gateFixture{gate: gate, request: request, events: events, repository: repository, base: base, temporaryRoot: temporary}
}

func TestIntegrationGateHelperProcess(t *testing.T) {
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		return
	}
	switch os.Args[separator+1] {
	case "pass":
		fmt.Fprintln(os.Stdout, "pass")
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stderr, "semantic failure")
		os.Exit(17)
	case "move-head":
		command := exec.Command("git", "-c", "user.name=Gate Test", "-c", "user.email=gate@example.invalid", "commit", "--allow-empty", "-qm", "move")
		command.Dir = "."
		if err := command.Run(); err != nil {
			os.Exit(18)
		}
		os.Exit(0)
	default:
		os.Exit(91)
	}
}
func helperArgv(mode string) []string {
	return []string{os.Args[0], "-test.run=^TestIntegrationGateHelperProcess$", "--", mode}
}
func newCandidate(t *testing.T, store *evidence.Store, repository, branch, base, head, id string, offset int) scheduler.AcceptedCandidate {
	ref := writeEvidence(t, store, "acceptance-"+id+".json", "branch-acceptance", []byte("pass"))
	candidate, err := scheduler.NewAcceptedCandidate(scheduler.CandidateInput{ProjectID: "project", PlanID: "plan-" + id, RunID: "run-" + id, AttemptID: "attempt-" + id, Repository: repository, Branch: branch, StartSHA: base, HeadSHA: head, AcceptanceEvidence: []ledger.EvidenceRef{ref}, AcceptedAt: time.Unix(int64(offset), 0).UTC(), AcceptancePolicyIdentity: "policy-v1"})
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}
func writeEvidence(t *testing.T, store *evidence.Store, name, kind string, data []byte) ledger.EvidenceRef {
	t.Helper()
	ref, err := store.WriteBytes(name, kind, data)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}
func writeFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}
func runGit(t *testing.T, repository string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}
func gitOutput(t *testing.T, repository string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = repository
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(output))
}
func fileDigest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

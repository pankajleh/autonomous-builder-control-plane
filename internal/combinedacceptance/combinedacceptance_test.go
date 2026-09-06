package combinedacceptance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/scheduler"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
)

func TestEvaluatorPassesCombinedAcceptanceWithExactImmutableEvidence(t *testing.T) {
	fixture := newFixture(t, []authority.AcceptanceCommand{{
		Name: "combined unit", Class: "unit", Required: true, Timeout: "5s", Argv: helperArgv("pass"),
	}})

	result, err := fixture.evaluate(t, fixture.policy([]string{"unit"}, nil), fixture.target, fixture.candidates)
	if err != nil {
		t.Fatal(err)
	}
	if result.Classification() != ClassificationClean || !result.Acceptance().Passed() {
		t.Fatalf("classification = %s, acceptance = %s", result.Classification(), result.Acceptance().Status())
	}
	if len(result.Causes()) != 0 {
		t.Fatalf("clean result has causes: %#v", result.Causes())
	}
	if result.InitialGit().HeadSHA != fixture.target.HeadSHA || result.FinalGit().HeadSHA != fixture.target.HeadSHA {
		t.Fatal("target observations do not bind the exact integrated head")
	}
	if len(result.InitialGit().Commands) != 6 || len(result.FinalGit().Commands) != 6 {
		t.Fatal("target observations do not contain complete structured Git evidence")
	}
	for _, observation := range []GitObservation{result.InitialGit(), result.FinalGit()} {
		for _, command := range observation.Commands {
			if len(command.Argv) < 3 || command.Argv[0] != "git" || command.Argv[1] != gitNoReplaceObjectsArg || command.OutputLimitBytes != gitOutputLimitBytes {
				t.Fatalf("unbounded or ambiguous Git evidence: %#v", command)
			}
		}
	}
	if len(result.CanonicalJSON()) == 0 || result.SHA256() != sha256Hex(result.CanonicalJSON()) || !json.Valid(result.CanonicalJSON()) {
		t.Fatal("canonical evidence or digest is incomplete")
	}
	if strings.Contains(string(result.CanonicalJSON()), "combined helper output") {
		t.Fatal("canonical result embedded command output instead of bounded evidence references")
	}
	if !strings.Contains(string(result.CanonicalJSON()), fixture.risk.SHA256()) || !strings.Contains(string(result.CanonicalJSON()), fixture.candidates[0].Input().AcceptanceEvidence[0].SHA256) {
		t.Fatal("canonical result omits exact risk or candidate acceptance provenance")
	}

	policyCopy := result.Policy()
	policyCopy.SemanticFailureClasses[0] = "mutated"
	targetCopy := result.Target()
	targetCopy.Integration.CandidateOrder[0].RunID = "mutated"
	targetCopy.Integration.Evidence[0].URI = "mutated"
	candidateCopy := result.Candidates()
	candidateInput := candidateCopy[0].Input()
	candidateInput.AcceptanceEvidence[0].URI = "mutated"
	causesCopy := result.Causes()
	refsCopy := result.EvidenceRefs()
	refsCopy[0].URI = "mutated"
	jsonCopy := result.CanonicalJSON()
	jsonCopy[0] = '!'
	initialCopy := result.InitialGit()
	initialCopy.Commands[0].Argv[0] = "mutated"
	if result.Policy().SemanticFailureClasses[0] != "unit" || result.Target().Integration.CandidateOrder[0].RunID == "mutated" ||
		result.Target().Integration.Evidence[0].URI == "mutated" || result.Candidates()[0].Input().AcceptanceEvidence[0].URI == "mutated" ||
		len(causesCopy) != 0 || result.EvidenceRefs()[0].URI == "mutated" || result.CanonicalJSON()[0] == '!' || result.InitialGit().Commands[0].Argv[0] == "mutated" {
		t.Fatal("result exposed mutable internal evidence")
	}
}

func TestEvaluatorClassifiesGovernedRequiredCheckFailure(t *testing.T) {
	fixture := newFixture(t, []authority.AcceptanceCommand{{
		Name: "combined static", Class: "static", Required: true, Timeout: "5s", Argv: helperArgv("fail"),
	}})
	result, err := fixture.evaluate(t, fixture.policy([]string{"unit"}, []string{"static"}), fixture.target, fixture.candidates)
	if err != nil {
		t.Fatal(err)
	}
	if result.Classification() != ClassificationFailClosed {
		t.Fatalf("classification = %s, want %s", result.Classification(), ClassificationFailClosed)
	}
	causes := result.Causes()
	if len(causes) != 1 || causes[0].Code != "required_check_failed" || causes[0].CommandClass != "static" || causes[0].PolicyDisposition != "fail_closed" || len(causes[0].EvidenceRefs) != 3 {
		t.Fatalf("failure causes = %#v", causes)
	}
}

func TestEvaluatorClassifiesSemanticConflictOnlyFromGovernedFailureClass(t *testing.T) {
	fixture := newFixture(t, []authority.AcceptanceCommand{{
		Name: "combined unit", Class: "unit", Required: true, Timeout: "5s", Argv: helperArgv("fail"),
	}})
	result, err := fixture.evaluate(t, fixture.policy([]string{"unit"}, nil), fixture.target, fixture.candidates)
	if err != nil {
		t.Fatal(err)
	}
	if result.Classification() != ClassificationSemanticConflict {
		t.Fatalf("classification = %s, want %s", result.Classification(), ClassificationSemanticConflict)
	}
	causes := result.Causes()
	if len(causes) != 1 || causes[0].CommandOutcome != supervisor.OutcomeExited || causes[0].PolicyDisposition != "semantic_conflict" {
		t.Fatalf("semantic causes = %#v", causes)
	}
}

func TestEvaluatorRejectsHeadMovedDuringAcceptance(t *testing.T) {
	fixture := newFixture(t, []authority.AcceptanceCommand{{
		Name: "mutating check", Class: "unit", Required: true, Timeout: "5s", Argv: helperArgv("move-head"),
	}})
	result, err := fixture.evaluate(t, fixture.policy([]string{"unit"}, nil), fixture.target, fixture.candidates)
	if err == nil {
		t.Fatal("expected moved-head rejection")
	}
	if result.Classification() != ClassificationFailClosed || len(result.Causes()) != 1 || result.Causes()[0].Code != "target_identity_mismatch" {
		t.Fatalf("moved-head result = %s, causes=%#v", result.Classification(), result.Causes())
	}
	if result.FinalGit().HeadSHA == fixture.target.HeadSHA || result.FinalGit().BranchSHA == fixture.target.HeadSHA {
		t.Fatal("final observation did not preserve moved target identity")
	}
	if len(result.CanonicalJSON()) == 0 {
		t.Fatal("moved target did not return immutable fail-closed evidence")
	}
}

func TestEvaluatorReturnsValidationUnavailableWhenRequiredCheckCannotRun(t *testing.T) {
	fixture := newFixture(t, []authority.AcceptanceCommand{{
		Name: "missing validator", Class: "unit", Required: true, Timeout: "5s", Argv: []string{filepath.Join(t.TempDir(), "missing-validator")},
	}})
	result, err := fixture.evaluate(t, fixture.policy([]string{"unit"}, nil), fixture.target, fixture.candidates)
	if err == nil {
		t.Fatal("expected unavailable validator error")
	}
	if result.Classification() != ClassificationValidationUnavailable || len(result.Causes()) != 1 || result.Causes()[0].Code != "required_validation_unavailable" {
		t.Fatalf("unavailable result = %s, causes=%#v", result.Classification(), result.Causes())
	}
}

func TestEvaluatorDoesNotMisclassifyTimedOutValidationAsSemanticConflict(t *testing.T) {
	fixture := newFixture(t, []authority.AcceptanceCommand{{
		Name: "timed validator", Class: "unit", Required: true, Timeout: "50ms", Argv: helperArgv("wait"),
	}})
	result, err := fixture.evaluate(t, fixture.policy([]string{"unit"}, nil), fixture.target, fixture.candidates)
	if err != nil {
		t.Fatal(err)
	}
	if result.Classification() != ClassificationValidationUnavailable || len(result.Causes()) != 1 || result.Causes()[0].CommandOutcome != supervisor.OutcomeTimedOut {
		t.Fatalf("timed validation result = %s, causes=%#v", result.Classification(), result.Causes())
	}
}

func TestEvaluatorRejectsIncompleteOrAmbiguousProvenance(t *testing.T) {
	fixture := newFixture(t, []authority.AcceptanceCommand{{
		Name: "combined unit", Class: "unit", Required: true, Timeout: "5s", Argv: helperArgv("pass"),
	}})
	validPolicy := fixture.policy([]string{"unit"}, nil)
	tests := []struct {
		name       string
		policy     Policy
		target     Target
		candidates []scheduler.AcceptedCandidate
	}{
		{name: "missing candidate provenance", policy: validPolicy, target: withCandidateOrder(fixture.target, nil), candidates: fixture.candidates},
		{name: "missing integration evidence", policy: validPolicy, target: withIntegrationEvidence(fixture.target, nil), candidates: fixture.candidates},
		{name: "wrong integrated head", policy: validPolicy, target: withProvenanceHead(fixture.target, strings.Repeat("a", 40)), candidates: fixture.candidates},
		{name: "zero candidate", policy: validPolicy, target: fixture.target, candidates: []scheduler.AcceptedCandidate{{}, fixture.candidates[1]}},
		{name: "unclassified required class", policy: fixture.policy([]string{"integration"}, nil), target: fixture.target, candidates: fixture.candidates},
		{name: "ambiguous class disposition", policy: fixture.policy([]string{"unit"}, []string{"unit"}), target: fixture.target, candidates: fixture.candidates},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := evidence.NewStore(t.TempDir(), "invalid-input")
			if err != nil {
				t.Fatal(err)
			}
			_, err = New(supervisor.New(), store).Evaluate(context.Background(), fixture.authority, test.policy, test.target, test.candidates, fixture.risk)
			if err == nil {
				t.Fatal("expected incomplete or ambiguous input rejection")
			}
			if entries, readErr := os.ReadDir(store.RunDir()); readErr != nil || len(entries) != 0 {
				t.Fatalf("invalid input executed commands or inspection failed: entries=%d err=%v", len(entries), readErr)
			}
		})
	}
}

func TestEvaluatorRejectsAlreadyStaleIntegratedTarget(t *testing.T) {
	fixture := newFixture(t, []authority.AcceptanceCommand{{
		Name: "combined unit", Class: "unit", Required: true, Timeout: "5s", Argv: helperArgv("pass"),
	}})
	runGit(t, fixture.target.RepositoryPath, "commit", "--allow-empty", "-qm", "move before evaluation")
	result, err := fixture.evaluate(t, fixture.policy([]string{"unit"}, nil), fixture.target, fixture.candidates)
	if err == nil || result.Classification() != ClassificationFailClosed || result.Causes()[0].Code != "target_identity_mismatch" {
		t.Fatalf("stale target result = %s, err=%v, causes=%#v", result.Classification(), err, result.Causes())
	}
	if len(result.Acceptance().Commands()) != 0 {
		t.Fatal("acceptance ran against an initially stale target")
	}
}

func TestEvaluatorFailsClosedWhenIntegrationBaselineIsUnavailable(t *testing.T) {
	fixture := newFixture(t, []authority.AcceptanceCommand{{
		Name: "combined unit", Class: "unit", Required: true, Timeout: "5s", Argv: helperArgv("pass"),
	}})
	target := cloneTarget(fixture.target)
	target.Integration.BaselineSHA = strings.Repeat("a", 40)
	result, err := fixture.evaluate(t, fixture.policy([]string{"unit"}, nil), target, fixture.candidates)
	if err == nil || result.Classification() != ClassificationFailClosed || result.Causes()[0].Code != "target_identity_mismatch" {
		t.Fatalf("missing baseline result = %s, err=%v, causes=%#v", result.Classification(), err, result.Causes())
	}
}

func TestCanonicalEvidenceIsDeterministicForEquivalentPolicyInput(t *testing.T) {
	fixture := newFixture(t, []authority.AcceptanceCommand{{
		Name: "combined unit", Class: "unit", Required: true, Timeout: "5s", Argv: helperArgv("pass"),
	}})
	leftPolicy := fixture.policy([]string{"unit", "integration"}, []string{"static", "security"})
	rightPolicy := fixture.policy([]string{"integration", "unit"}, []string{"security", "static"})
	leftPolicy, err := canonicalPolicy(leftPolicy, fixture.authority.Acceptance())
	if err != nil {
		t.Fatal(err)
	}
	rightPolicy, err = canonicalPolicy(rightPolicy, fixture.authority.Acceptance())
	if err != nil {
		t.Fatal(err)
	}
	left := Result{classification: ClassificationClean, policy: leftPolicy, target: fixture.target, candidates: cloneCandidates(fixture.candidates), risk: fixture.risk}
	right := Result{classification: ClassificationClean, policy: rightPolicy, target: fixture.target, candidates: cloneCandidates(fixture.candidates), risk: fixture.risk}
	if err := finalize(&left, fixture.authority); err != nil {
		t.Fatal(err)
	}
	if err := finalize(&right, fixture.authority); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left.CanonicalJSON(), right.CanonicalJSON()) || left.SHA256() != right.SHA256() {
		t.Fatal("equivalent governed policy input produced different canonical evidence")
	}
}

func TestCombinedAcceptanceHelperProcess(t *testing.T) {
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
	switch os.Args[separator+1] {
	case "pass":
		fmt.Fprintln(os.Stdout, "combined helper output")
		os.Exit(0)
	case "fail":
		fmt.Fprintln(os.Stdout, "bounded output before failure")
		fmt.Fprintln(os.Stderr, "bounded validation failure")
		os.Exit(17)
	case "move-head":
		command := exec.Command("git", "-c", "user.name=Combined Test", "-c", "user.email=combined@example.invalid", "commit", "--allow-empty", "-qm", "move during acceptance")
		command.Dir = "."
		if output, err := command.CombinedOutput(); err != nil {
			fmt.Fprintln(os.Stderr, err, string(output))
			os.Exit(18)
		}
		os.Exit(0)
	case "wait":
		for {
			time.Sleep(time.Hour)
		}
	default:
		os.Exit(91)
	}
}

type testFixture struct {
	authority  authority.Authority
	policyBase Policy
	target     Target
	candidates []scheduler.AcceptedCandidate
	risk       scheduler.RiskReport
}

func newFixture(t *testing.T, commands []authority.AcceptanceCommand) testFixture {
	t.Helper()
	source := t.TempDir()
	runGit(t, source, "init", "-q")
	runGit(t, source, "remote", "add", "origin", "https://example.test/example/project.git")
	writeFile(t, filepath.Join(source, "plan.md"), "governed plan\n")
	writeFile(t, filepath.Join(source, "base.txt"), "base\n")
	runGit(t, source, "add", "--all")
	commit(t, source, "base")
	base := gitOutput(t, source, "rev-parse", "HEAD")
	defaultBranch := gitOutput(t, source, "symbolic-ref", "--quiet", "--short", "HEAD")

	runGit(t, source, "checkout", "-qb", "candidate-one", base)
	writeFile(t, filepath.Join(source, "one.txt"), "one\n")
	runGit(t, source, "add", "--all")
	commit(t, source, "candidate one")
	firstHead := gitOutput(t, source, "rev-parse", "HEAD")
	runGit(t, source, "checkout", "-qb", "candidate-two", base)
	writeFile(t, filepath.Join(source, "two.txt"), "two\n")
	runGit(t, source, "add", "--all")
	commit(t, source, "candidate two")
	secondHead := gitOutput(t, source, "rev-parse", "HEAD")

	candidates := []scheduler.AcceptedCandidate{
		acceptedCandidate(t, source, "plan-one", "run-one", "attempt-one", "candidate-one", base, firstHead, 1),
		acceptedCandidate(t, source, "plan-two", "run-two", "attempt-two", "candidate-two", base, secondHead, 2),
	}
	analyzer, err := scheduler.NewAnalyzer(scheduler.RiskPolicy{PolicyIdentity: "risk-v1"})
	if err != nil {
		t.Fatal(err)
	}
	risk, err := analyzer.Analyze(context.Background(), candidates)
	if err != nil {
		t.Fatal(err)
	}

	integrated := filepath.Join(t.TempDir(), "integrated")
	command := exec.Command("git", "clone", "-q", source, integrated)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("clone integrated repository: %v: %s", err, output)
	}
	runGit(t, integrated, "checkout", "-qb", "combined", base)
	runGit(t, integrated, "cherry-pick", firstHead)
	runGit(t, integrated, "cherry-pick", secondHead)
	integratedHead := gitOutput(t, integrated, "rev-parse", "HEAD")
	integrationEvidencePath := filepath.Join(t.TempDir(), "textual-integration.json")
	writeFile(t, integrationEvidencePath, `{"status":"clean"}`)
	integrationEvidence := evidenceRef(t, integrationEvidencePath, "textual-integration-metadata")

	governed, err := authority.New(authority.Manifest{
		RunID: "combined-acceptance-test",
		Repository: authority.RepositoryManifest{
			Path: source, Identity: "example/project", Remotes: map[string]string{"origin": "https://example.test/example/project.git"}, DefaultBranch: defaultBranch, StartSHA: base,
		},
		Plan:       authority.PlanManifest{Path: filepath.Join(source, "plan.md"), SHA256: fileSHA256(t, filepath.Join(source, "plan.md"))},
		Ralphex:    authority.RalphexManifest{BinaryPath: os.Args[0], BinarySHA256: fileSHA256(t, os.Args[0]), Mode: ralphex.ModeFull, Timeout: "5s", WaitOnLimit: "0s"},
		Acceptance: commands, PolicyVersion: "combined-policy-v1",
	})
	if err != nil {
		t.Fatal(err)
	}

	order := make([]scheduler.CandidateIdentity, len(candidates))
	for index, candidate := range candidates {
		order[index] = candidateIdentity(candidate.Input())
	}
	target := Target{
		RepositoryPath: integrated,
		Branch:         "combined",
		HeadSHA:        integratedHead,
		Integration: IntegrationProvenance{
			IntegrationID:             "integration-test-1",
			RepositoryIdentity:        "example/project",
			BaselineSHA:               base,
			IntegratedHeadSHA:         integratedHead,
			CandidateOrder:            order,
			TextualIntegrationStatus:  TextualIntegrationClean,
			IntegrationPolicyIdentity: "integration-policy-v1",
			RiskEvidenceSHA256:        risk.SHA256(),
			Evidence:                  []ledger.EvidenceRef{integrationEvidence},
		},
	}
	return testFixture{
		authority: governed,
		policyBase: Policy{
			PolicyIdentity:                   "semantic-policy-v1",
			CombinedAcceptancePolicyIdentity: "combined-policy-v1",
			IntegrationPolicyIdentity:        "integration-policy-v1",
			RiskPolicyIdentity:               "risk-v1",
		},
		target: target, candidates: candidates, risk: risk,
	}
}

func (f testFixture) policy(semantic, failClosed []string) Policy {
	policy := clonePolicy(f.policyBase)
	policy.SemanticFailureClasses = append([]string(nil), semantic...)
	policy.FailClosedFailureClasses = append([]string(nil), failClosed...)
	return policy
}

func (f testFixture) evaluate(t *testing.T, policy Policy, target Target, candidates []scheduler.AcceptedCandidate) (Result, error) {
	t.Helper()
	store, err := evidence.NewStore(t.TempDir(), "combined-evaluation")
	if err != nil {
		t.Fatal(err)
	}
	return New(supervisor.New(), store).Evaluate(context.Background(), f.authority, policy, target, candidates, f.risk)
}

func acceptedCandidate(t *testing.T, repository, planID, runID, attemptID, branch, base, head string, offset int) scheduler.AcceptedCandidate {
	t.Helper()
	evidencePath := filepath.Join(t.TempDir(), runID+"-acceptance.json")
	writeFile(t, evidencePath, `{"status":"pass"}`)
	candidate, err := scheduler.NewAcceptedCandidate(scheduler.CandidateInput{
		ProjectID: "project", PlanID: planID, RunID: runID, AttemptID: attemptID,
		Repository: repository, Branch: branch, StartSHA: base, HeadSHA: head,
		AcceptanceEvidence:       []ledger.EvidenceRef{evidenceRef(t, evidencePath, "branch-acceptance")},
		AcceptedAt:               time.Unix(int64(offset), 0).UTC(),
		AcceptancePolicyIdentity: "branch-policy-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func withCandidateOrder(target Target, order []scheduler.CandidateIdentity) Target {
	target = cloneTarget(target)
	target.Integration.CandidateOrder = order
	return target
}

func withIntegrationEvidence(target Target, refs []ledger.EvidenceRef) Target {
	target = cloneTarget(target)
	target.Integration.Evidence = refs
	return target
}

func withProvenanceHead(target Target, head string) Target {
	target = cloneTarget(target)
	target.Integration.IntegratedHeadSHA = head
	return target
}

func helperArgv(mode string) []string {
	return []string{os.Args[0], "-test.run=^TestCombinedAcceptanceHelperProcess$", "--", mode}
}

func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, repository, message string) {
	t.Helper()
	command := exec.Command("git", "-c", "user.name=Combined Test", "-c", "user.email=combined@example.invalid", "commit", "-qm", message)
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("commit failed: %v: %s", err, output)
	}
}

func runGit(t *testing.T, repository string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v: %s", strings.Join(arguments, " "), err, output)
	}
}

func gitOutput(t *testing.T, repository string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = repository
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s failed: %v", strings.Join(arguments, " "), err)
	}
	return strings.TrimSpace(string(output))
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256Hex(data)
}

func evidenceRef(t *testing.T, path, kind string) ledger.EvidenceRef {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return ledger.EvidenceRef{URI: path, SHA256: hex.EncodeToString(digest[:]), Kind: kind}
}

func TestResultMarshalJSONMatchesCanonicalEvidence(t *testing.T) {
	fixture := newFixture(t, []authority.AcceptanceCommand{{Name: "unit", Class: "unit", Required: true, Timeout: "5s", Argv: helperArgv("pass")}})
	result, err := fixture.evaluate(t, fixture.policy([]string{"unit"}, nil), fixture.target, fixture.candidates)
	if err != nil {
		t.Fatal(err)
	}
	marshaled, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(marshaled, result.CanonicalJSON()) {
		t.Fatal("MarshalJSON differs from immutable canonical evidence")
	}
}

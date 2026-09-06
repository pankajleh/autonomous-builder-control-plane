package scheduler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRiskAnalyzerClassifiesDisjointCommittedDiffsDeterministically(t *testing.T) {
	repository, base := newRiskRepository(t, map[string]string{"base.txt": "base\n"})
	leftHead := commitRiskBranch(t, repository, base, "candidate-left", map[string]string{"left.txt": "left\n"})
	rightHead := commitRiskBranch(t, repository, base, "candidate-right", map[string]string{"right.txt": "right\n"})
	left := riskCandidate(t, repository, "run-left", base, leftHead, 2)
	right := riskCandidate(t, repository, "run-right", base, rightHead, 1)
	analyzer := mustAnalyzer(t, RiskPolicy{PolicyIdentity: "risk-v1"})

	report, err := analyzer.Analyze(context.Background(), []AcceptedCandidate{left, right})
	if err != nil {
		t.Fatal(err)
	}
	if report.Class() != RiskDisjointPaths {
		t.Fatalf("risk class = %s, want %s", report.Class(), RiskDisjointPaths)
	}
	pairs := report.PairRisks()
	if len(pairs) != 1 || pairs[0].Class != RiskDisjointPaths || len(pairs[0].OverlappingPaths) != 0 {
		t.Fatalf("pair evidence = %#v", pairs)
	}
	if got := report.CandidateDiffs()[0].Candidate.Input().RunID; got != "run-right" {
		t.Fatalf("candidate evidence order starts with %q, want run-right", got)
	}

	reversed, err := analyzer.Analyze(context.Background(), []AcceptedCandidate{right, left})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(report.CanonicalJSON(), reversed.CanonicalJSON()) || report.SHA256() != reversed.SHA256() {
		t.Fatal("risk evidence changed when input order changed")
	}
}

func TestRiskAnalyzerClassifiesOrdinaryAndProtectedOverlap(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		policy RiskPolicy
		want   RiskClass
	}{
		{name: "ordinary", path: "internal/service/value.go", policy: RiskPolicy{PolicyIdentity: "risk-v1"}, want: RiskOverlappingPaths},
		{name: "contract-sensitive", path: "internal/scheduler/contract.go", policy: RiskPolicy{PolicyIdentity: "risk-v1", ContractSensitivePaths: []string{"internal/scheduler"}}, want: RiskProtectedOverlap},
		{name: "shared-authority", path: "docs/architecture/STATE_MACHINE.md", policy: RiskPolicy{PolicyIdentity: "risk-v1", SharedAuthorityPaths: []string{"docs/architecture"}}, want: RiskProtectedOverlap},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, base := newRiskRepository(t, map[string]string{test.path: "base\n"})
			leftHead := commitRiskBranch(t, repository, base, "candidate-left", map[string]string{test.path: "left\n"})
			rightHead := commitRiskBranch(t, repository, base, "candidate-right", map[string]string{test.path: "right\n"})
			analyzer := mustAnalyzer(t, test.policy)
			report, err := analyzer.Analyze(context.Background(), []AcceptedCandidate{
				riskCandidate(t, repository, "run-left", base, leftHead, 1),
				riskCandidate(t, repository, "run-right", base, rightHead, 2),
			})
			if err != nil {
				t.Fatal(err)
			}
			if report.Class() != test.want {
				t.Fatalf("risk class = %s, want %s", report.Class(), test.want)
			}
			pair := report.PairRisks()[0]
			if !reflect.DeepEqual(pair.OverlappingPaths, []string{test.path}) {
				t.Fatalf("overlap = %v, want %s", pair.OverlappingPaths, test.path)
			}
			if test.want == RiskProtectedOverlap && !reflect.DeepEqual(pair.ProtectedPaths, []string{test.path}) {
				t.Fatalf("protected paths = %v, want %s", pair.ProtectedPaths, test.path)
			}
		})
	}
}

func TestRiskAnalyzerClassifiesDifferentPathsUnderSharedAuthorityRoot(t *testing.T) {
	repository, base := newRiskRepository(t, map[string]string{
		"docs/architecture/left.md":  "base\n",
		"docs/architecture/right.md": "base\n",
	})
	leftHead := commitRiskBranch(t, repository, base, "candidate-left", map[string]string{"docs/architecture/left.md": "left\n"})
	rightHead := commitRiskBranch(t, repository, base, "candidate-right", map[string]string{"docs/architecture/right.md": "right\n"})
	analyzer := mustAnalyzer(t, RiskPolicy{PolicyIdentity: "risk-v1", SharedAuthorityPaths: []string{"docs/architecture"}})
	report, err := analyzer.Analyze(context.Background(), []AcceptedCandidate{
		riskCandidate(t, repository, "run-left", base, leftHead, 1),
		riskCandidate(t, repository, "run-right", base, rightHead, 2),
	})
	if err != nil {
		t.Fatal(err)
	}
	pair := report.PairRisks()[0]
	if report.Class() != RiskProtectedOverlap || len(pair.OverlappingPaths) != 0 || !reflect.DeepEqual(pair.ProtectedRoots, []string{"docs/architecture"}) {
		t.Fatalf("shared-authority pair evidence = %#v", pair)
	}
}

func TestRiskAnalyzerEvidencePinsExactSHAsAndIgnoresDirtyWorktree(t *testing.T) {
	repository, base := newRiskRepository(t, map[string]string{"tracked.txt": "base\n"})
	head := commitRiskBranch(t, repository, base, "candidate", map[string]string{"committed.txt": "committed\n"})
	candidate := riskCandidate(t, repository, "run", base, head, 1)
	analyzer := mustAnalyzer(t, RiskPolicy{PolicyIdentity: "risk-v1"})
	clean, err := analyzer.Analyze(context.Background(), []AcceptedCandidate{candidate})
	if err != nil {
		t.Fatal(err)
	}

	commands := clean.CandidateDiffs()[0].Commands
	if len(commands) != 4 {
		t.Fatalf("Git evidence command count = %d, want 4", len(commands))
	}
	if !containsArg(commands[0].Argv, base+"^{commit}") || !containsArg(commands[1].Argv, head+"^{commit}") || !containsArg(commands[2].Argv, base) || !containsArg(commands[2].Argv, head) || !containsArg(commands[3].Argv, base) || !containsArg(commands[3].Argv, head) {
		t.Fatalf("Git evidence does not pin exact start/head SHAs: %#v", commands)
	}
	if !strings.Contains(string(clean.CanonicalJSON()), base) || !strings.Contains(string(clean.CanonicalJSON()), head) {
		t.Fatal("canonical risk evidence omits exact SHA provenance")
	}

	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "untracked.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirty, err := analyzer.Analyze(context.Background(), []AcceptedCandidate{candidate})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(clean.CanonicalJSON(), dirty.CanonicalJSON()) {
		t.Fatal("dirty worktree affected committed final-diff evidence")
	}
}

func TestRiskAnalyzerRejectsRenameAmbiguity(t *testing.T) {
	repository, base := newRiskRepository(t, map[string]string{"old.txt": "same content\n"})
	runRiskGit(t, repository, "checkout", "-qb", "candidate-rename", base)
	runRiskGit(t, repository, "mv", "old.txt", "new.txt")
	runRiskGit(t, repository, "commit", "-qm", "rename")
	head := riskGitOutput(t, repository, "rev-parse", "HEAD")
	analyzer := mustAnalyzer(t, RiskPolicy{PolicyIdentity: "risk-v1"})
	_, err := analyzer.Analyze(context.Background(), []AcceptedCandidate{riskCandidate(t, repository, "run", base, head, 1)})
	if err == nil || !strings.Contains(err.Error(), "rename/copy") {
		t.Fatalf("rename ambiguity error = %v", err)
	}
}

func TestRiskAnalyzerRejectsInvalidAncestryAndMissingObjects(t *testing.T) {
	repository, base := newRiskRepository(t, map[string]string{"base.txt": "base\n"})
	leftHead := commitRiskBranch(t, repository, base, "candidate-left", map[string]string{"left.txt": "left\n"})
	rightHead := commitRiskBranch(t, repository, base, "candidate-right", map[string]string{"right.txt": "right\n"})
	analyzer := mustAnalyzer(t, RiskPolicy{PolicyIdentity: "risk-v1"})

	invalidHistory := riskCandidate(t, repository, "invalid-history", leftHead, rightHead, 1)
	if _, err := analyzer.Analyze(context.Background(), []AcceptedCandidate{invalidHistory}); err == nil || !strings.Contains(err.Error(), "not a descendant") {
		t.Fatalf("invalid ancestry error = %v", err)
	}
	missing := riskCandidate(t, repository, "missing", base, strings.Repeat("f", 40), 2)
	if _, err := analyzer.Analyze(context.Background(), []AcceptedCandidate{missing}); err == nil || !strings.Contains(err.Error(), "missing or non-commit") {
		t.Fatalf("missing object error = %v", err)
	}
}

func TestRiskAnalyzerRejectsMalformedGitOutput(t *testing.T) {
	repository := filepath.Clean(t.TempDir())
	base := strings.Repeat("a", 40)
	head := strings.Repeat("b", 40)
	runner := malformedDiffRunner{}
	analyzer, err := newAnalyzer(RiskPolicy{PolicyIdentity: "risk-v1"}, runner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = analyzer.Analyze(context.Background(), []AcceptedCandidate{riskCandidate(t, repository, "run", base, head, 1)})
	if err == nil || !strings.Contains(err.Error(), "missing NUL terminator") {
		t.Fatalf("malformed Git output error = %v", err)
	}
}

func TestRiskReportAccessorsReturnImmutableCopies(t *testing.T) {
	repository, base := newRiskRepository(t, map[string]string{"base.txt": "base\n"})
	head := commitRiskBranch(t, repository, base, "candidate", map[string]string{"file.txt": "value\n"})
	analyzer := mustAnalyzer(t, RiskPolicy{PolicyIdentity: "risk-v1"})
	report, err := analyzer.Analyze(context.Background(), []AcceptedCandidate{riskCandidate(t, repository, "run", base, head, 1)})
	if err != nil {
		t.Fatal(err)
	}
	originalJSON := report.CanonicalJSON()
	diffs := report.CandidateDiffs()
	diffs[0].Changes[0].Path = "mutated"
	diffs[0].Commands[0].Argv[0] = "mutated"
	diffs[0].Candidate.data.AcceptanceEvidence[0].URI = "mutated"
	bytes := report.CanonicalJSON()
	bytes[0] = '!'
	if !reflect.DeepEqual(originalJSON, report.CanonicalJSON()) || report.CandidateDiffs()[0].Changes[0].Path == "mutated" {
		t.Fatal("risk report exposed mutable internal values")
	}
}

type malformedDiffRunner struct{}

func (malformedDiffRunner) Run(_ context.Context, _ string, args ...string) (gitResult, error) {
	result := gitResult{Argv: append([]string{"git"}, args...), ExitCode: 0}
	switch args[0] {
	case "rev-parse":
		result.Stdout = []byte(strings.TrimSuffix(args[len(args)-1], "^{commit}") + "\n")
	case "diff":
		result.Stdout = []byte("M\x00file.txt")
	}
	return result, nil
}

func newRiskRepository(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	repository := filepath.Clean(t.TempDir())
	runRiskGit(t, repository, "init", "-q")
	runRiskGit(t, repository, "config", "user.name", "Scheduler Test")
	runRiskGit(t, repository, "config", "user.email", "scheduler@example.invalid")
	writeRiskFiles(t, repository, files)
	runRiskGit(t, repository, "add", "--all")
	runRiskGit(t, repository, "commit", "-qm", "base")
	return repository, riskGitOutput(t, repository, "rev-parse", "HEAD")
}

func commitRiskBranch(t *testing.T, repository, base, branch string, files map[string]string) string {
	t.Helper()
	runRiskGit(t, repository, "checkout", "-qb", branch, base)
	writeRiskFiles(t, repository, files)
	runRiskGit(t, repository, "add", "--all")
	runRiskGit(t, repository, "commit", "-qm", branch)
	return riskGitOutput(t, repository, "rev-parse", "HEAD")
}

func writeRiskFiles(t *testing.T, repository string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		filename := filepath.Join(repository, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func riskCandidate(t *testing.T, repository, runID, startSHA, headSHA string, acceptedSecond int64) AcceptedCandidate {
	t.Helper()
	input := candidateInput(repository, runID, "attempt-1", startSHA, headSHA, time.Unix(acceptedSecond, 0))
	digest := sha256.Sum256([]byte(runID + headSHA))
	input.AcceptanceEvidence[0].SHA256 = hex.EncodeToString(digest[:])
	return mustCandidate(t, input)
}

func mustAnalyzer(t *testing.T, policy RiskPolicy) *Analyzer {
	t.Helper()
	analyzer, err := NewAnalyzer(policy)
	if err != nil {
		t.Fatal(err)
	}
	return analyzer
}

func runRiskGit(t *testing.T, repository string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v: %s", args, err, output)
	}
}

func riskGitOutput(t *testing.T, repository string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = repository
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v failed: %v", args, err)
	}
	return strings.TrimSpace(string(output))
}

func containsArg(argv []string, wanted string) bool {
	for _, value := range argv {
		if value == wanted {
			return true
		}
	}
	return false
}

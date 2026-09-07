package scheduler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
)

// RiskClass is a deterministic final-diff relationship. Classes describe
// evidence only; they do not authorize a state transition.
type RiskClass string

const (
	RiskDisjointPaths    RiskClass = "DISJOINT_PATHS"
	RiskOverlappingPaths RiskClass = "OVERLAPPING_PATHS"
	RiskProtectedOverlap RiskClass = "CONTRACT_SENSITIVE_OR_SHARED_AUTHORITY_OVERLAP"

	riskSchemaVersion         = 2
	gitStdoutLimitBytes       = 16 * 1024 * 1024
	gitStderrLimitBytes       = 1024 * 1024
	gitNoReplaceObjectsOption = "--no-replace-objects"
)

// RiskPolicy identifies contract-sensitive and shared-authority path roots.
// Roots use repository-relative slash-separated paths and match themselves and
// their descendants.
type RiskPolicy struct {
	PolicyIdentity         string   `json:"policy_identity"`
	ContractSensitivePaths []string `json:"contract_sensitive_paths"`
	SharedAuthorityPaths   []string `json:"shared_authority_paths"`
}

// PathChange is one unambiguous path in a committed start..head diff.
type PathChange struct {
	Status string `json:"status"`
	Path   string `json:"path"`
}

// GitCommandEvidence proves which structured Git operation produced a result.
// Output content is represented by hashes; parsed paths are returned separately.
type GitCommandEvidence struct {
	Argv             []string `json:"argv"`
	ExitCode         int      `json:"exit_code"`
	StdoutSHA256     string   `json:"stdout_sha256"`
	StderrSHA256     string   `json:"stderr_sha256"`
	StdoutBytes      int      `json:"stdout_bytes"`
	StderrBytes      int      `json:"stderr_bytes"`
	StdoutLimitBytes int      `json:"stdout_limit_bytes"`
	StderrLimitBytes int      `json:"stderr_limit_bytes"`
	StdoutTruncated  bool     `json:"stdout_truncated"`
	StderrTruncated  bool     `json:"stderr_truncated"`
}

// CandidateDiffEvidence binds parsed final paths to exact accepted provenance.
type CandidateDiffEvidence struct {
	Candidate AcceptedCandidate    `json:"candidate"`
	Changes   []PathChange         `json:"changes"`
	Commands  []GitCommandEvidence `json:"commands"`
}

// CandidateIdentity is the exact controller identity used in pair evidence.
type CandidateIdentity struct {
	ProjectID string `json:"project_id"`
	PlanID    string `json:"plan_id"`
	RunID     string `json:"run_id"`
	AttemptID string `json:"attempt_id"`
	HeadSHA   string `json:"head_sha"`
}

// PairRiskEvidence explains the classification between two candidates.
type PairRiskEvidence struct {
	Left             CandidateIdentity `json:"left"`
	Right            CandidateIdentity `json:"right"`
	Class            RiskClass         `json:"class"`
	OverlappingPaths []string          `json:"overlapping_paths,omitempty"`
	ProtectedPaths   []string          `json:"protected_paths,omitempty"`
	ProtectedRoots   []string          `json:"protected_roots,omitempty"`
	Explanation      string            `json:"explanation"`
}

// RiskReport is an immutable deterministic report. It intentionally contains
// no generation timestamp or worktree status.
type RiskReport struct {
	policy        RiskPolicy
	class         RiskClass
	candidates    []CandidateDiffEvidence
	pairs         []PairRiskEvidence
	canonicalJSON []byte
	digest        string
}

// Analyzer derives risk from committed Git objects in the candidate repository.
type Analyzer struct {
	policy RiskPolicy
	runner gitRunner
}

// NewAnalyzer validates and freezes a risk policy.
func NewAnalyzer(policy RiskPolicy) (*Analyzer, error) {
	return newAnalyzer(policy, execGitRunner{
		stdoutLimitBytes: gitStdoutLimitBytes,
		stderrLimitBytes: gitStderrLimitBytes,
	})
}

func newAnalyzer(policy RiskPolicy, runner gitRunner) (*Analyzer, error) {
	if runner == nil {
		return nil, errors.New("Git runner is required")
	}
	canonical, err := canonicalRiskPolicy(policy)
	if err != nil {
		return nil, err
	}
	return &Analyzer{policy: canonical, runner: runner}, nil
}

// Analyze validates every accepted candidate against exact committed objects,
// computes each start..head final diff, and classifies every candidate pair.
func (a *Analyzer) Analyze(ctx context.Context, candidates []AcceptedCandidate) (RiskReport, error) {
	if ctx == nil {
		return RiskReport{}, errors.New("context is required")
	}
	if a == nil || a.runner == nil {
		return RiskReport{}, errors.New("analyzer is required")
	}
	if len(candidates) == 0 {
		return RiskReport{}, errors.New("at least one accepted candidate is required")
	}

	ordered := make([]AcceptedCandidate, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	var repository string
	for index, candidate := range candidates {
		if err := candidate.validate(); err != nil {
			return RiskReport{}, fmt.Errorf("candidate %d: %w", index, err)
		}
		input := candidate.data
		if err := validateRepositoryPath(input.Repository); err != nil {
			return RiskReport{}, fmt.Errorf("candidate %d: %w", index, err)
		}
		if repository == "" {
			repository = input.Repository
		} else if repository != input.Repository {
			return RiskReport{}, errors.New("all candidates must identify the same exact repository")
		}
		if _, exists := seen[candidate.Key()]; exists {
			return RiskReport{}, fmt.Errorf("candidate %d duplicates an accepted identity", index)
		}
		seen[candidate.Key()] = struct{}{}
		ordered[index] = cloneCandidate(candidate)
	}
	sort.Slice(ordered, func(i, j int) bool { return candidateOrderKey(ordered[i]) < candidateOrderKey(ordered[j]) })

	diffs := make([]CandidateDiffEvidence, 0, len(ordered))
	casePaths := make(map[string]string)
	for _, candidate := range ordered {
		diff, err := a.candidateDiff(ctx, candidate)
		if err != nil {
			input := candidate.data
			return RiskReport{}, fmt.Errorf("candidate %s/%s: %w", input.RunID, input.AttemptID, err)
		}
		for _, change := range diff.Changes {
			folded := strings.ToLower(change.Path)
			if previous, exists := casePaths[folded]; exists && previous != change.Path {
				return RiskReport{}, fmt.Errorf("ambiguous case-folded paths %q and %q", previous, change.Path)
			}
			casePaths[folded] = change.Path
		}
		diffs = append(diffs, diff)
	}

	pairs := make([]PairRiskEvidence, 0, len(diffs)*(len(diffs)-1)/2)
	overall := RiskDisjointPaths
	for left := 0; left < len(diffs); left++ {
		for right := left + 1; right < len(diffs); right++ {
			pair := a.classifyPair(diffs[left], diffs[right])
			pairs = append(pairs, pair)
			overall = higherRisk(overall, pair.Class)
		}
	}

	report := RiskReport{
		policy:     cloneRiskPolicy(a.policy),
		class:      overall,
		candidates: cloneCandidateDiffs(diffs),
		pairs:      clonePairRisks(pairs),
	}
	canonical, err := json.Marshal(struct {
		SchemaVersion int                     `json:"schema_version"`
		Policy        RiskPolicy              `json:"policy"`
		Class         RiskClass               `json:"class"`
		Candidates    []CandidateDiffEvidence `json:"candidates"`
		Pairs         []PairRiskEvidence      `json:"pairs"`
	}{riskSchemaVersion, report.policy, report.class, report.candidates, report.pairs})
	if err != nil {
		return RiskReport{}, fmt.Errorf("marshal risk evidence: %w", err)
	}
	digest := sha256.Sum256(canonical)
	report.canonicalJSON = canonical
	report.digest = hex.EncodeToString(digest[:])
	return report, nil
}

// Class returns the report's highest pairwise risk.
func (r RiskReport) Class() RiskClass { return r.class }

// PolicyIdentity returns the exact risk policy identity.
func (r RiskReport) PolicyIdentity() string { return r.policy.PolicyIdentity }

// Policy returns a deep copy of the complete canonical risk policy bound into
// this report's evidence.
func (r RiskReport) Policy() RiskPolicy { return cloneRiskPolicy(r.policy) }

// CandidateDiffs returns a deep copy of committed-diff evidence.
func (r RiskReport) CandidateDiffs() []CandidateDiffEvidence {
	return cloneCandidateDiffs(r.candidates)
}

// PairRisks returns a deep copy of pairwise classifications.
func (r RiskReport) PairRisks() []PairRiskEvidence { return clonePairRisks(r.pairs) }

// CanonicalJSON returns deterministic evidence bytes suitable for immutable
// artifact storage by a later integration controller.
func (r RiskReport) CanonicalJSON() []byte { return append([]byte(nil), r.canonicalJSON...) }

// SHA256 returns the lowercase digest of CanonicalJSON.
func (r RiskReport) SHA256() string { return r.digest }

// MarshalJSON emits the same deterministic bytes as CanonicalJSON.
func (r RiskReport) MarshalJSON() ([]byte, error) {
	if len(r.canonicalJSON) == 0 {
		return nil, errors.New("risk report is incomplete")
	}
	return append([]byte(nil), r.canonicalJSON...), nil
}

func (a *Analyzer) candidateDiff(ctx context.Context, candidate AcceptedCandidate) (CandidateDiffEvidence, error) {
	input := candidate.data
	evidence := CandidateDiffEvidence{Candidate: cloneCandidate(candidate)}
	for _, objectID := range []string{input.StartSHA, input.HeadSHA} {
		result, err := a.run(ctx, input.Repository, "rev-parse", "--verify", "--end-of-options", objectID+"^{commit}")
		evidence.Commands = append(evidence.Commands, commandEvidence(result))
		if err != nil {
			return evidence, fmt.Errorf("verify commit %s: %w", objectID, err)
		}
		if result.ExitCode != 0 {
			return evidence, fmt.Errorf("missing or non-commit Git object %s", objectID)
		}
		if len(result.Stderr) != 0 || string(result.Stdout) != objectID+"\n" {
			return evidence, fmt.Errorf("malformed object verification output for %s", objectID)
		}
	}

	ancestry, err := a.run(ctx, input.Repository, "merge-base", "--is-ancestor", input.StartSHA, input.HeadSHA)
	evidence.Commands = append(evidence.Commands, commandEvidence(ancestry))
	if err != nil {
		return evidence, fmt.Errorf("verify ancestry: %w", err)
	}
	if len(ancestry.Stdout) != 0 || len(ancestry.Stderr) != 0 {
		return evidence, errors.New("malformed ancestry verification output")
	}
	if ancestry.ExitCode == 1 {
		return evidence, errors.New("candidate head is not a descendant of its start SHA")
	}
	if ancestry.ExitCode != 0 {
		return evidence, fmt.Errorf("Git ancestry verification failed with exit code %d", ancestry.ExitCode)
	}

	diff, err := a.run(ctx, input.Repository, "diff", "--name-status", "-z", "--no-ext-diff", "--find-renames=50%", "--find-copies=50%", input.StartSHA, input.HeadSHA, "--")
	evidence.Commands = append(evidence.Commands, commandEvidence(diff))
	if err != nil {
		return evidence, fmt.Errorf("read committed final diff: %w", err)
	}
	if diff.ExitCode != 0 {
		return evidence, fmt.Errorf("Git final diff failed with exit code %d", diff.ExitCode)
	}
	if len(diff.Stderr) != 0 {
		return evidence, errors.New("Git final diff produced unexpected stderr")
	}
	changes, err := parseNameStatus(diff.Stdout)
	if err != nil {
		return evidence, err
	}
	evidence.Changes = changes
	return evidence, nil
}

func (a *Analyzer) run(ctx context.Context, repository string, args ...string) (gitResult, error) {
	safeArgs := append([]string{gitNoReplaceObjectsOption}, args...)
	result, err := a.runner.Run(ctx, repository, safeArgs...)
	if len(result.Argv) == 0 {
		result.Argv = append([]string{"git"}, safeArgs...)
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return result, gitOutputTruncationError(result)
	}
	return result, err
}

func (a *Analyzer) classifyPair(left, right CandidateDiffEvidence) PairRiskEvidence {
	leftPaths := make(map[string]struct{}, len(left.Changes))
	for _, change := range left.Changes {
		leftPaths[change.Path] = struct{}{}
	}
	var overlap []string
	for _, change := range right.Changes {
		if _, exists := leftPaths[change.Path]; exists {
			overlap = append(overlap, change.Path)
		}
	}
	sort.Strings(overlap)
	protected := make([]string, 0, len(overlap))
	for _, changedPath := range overlap {
		if matchesAnyRoot(changedPath, a.policy.ContractSensitivePaths) || matchesAnyRoot(changedPath, a.policy.SharedAuthorityPaths) {
			protected = append(protected, changedPath)
		}
	}
	protectedRoots := commonProtectedRoots(left.Changes, right.Changes, append(append([]string(nil), a.policy.ContractSensitivePaths...), a.policy.SharedAuthorityPaths...))
	pair := PairRiskEvidence{Left: candidateIdentity(left.Candidate), Right: candidateIdentity(right.Candidate), OverlappingPaths: overlap, ProtectedPaths: protected, ProtectedRoots: protectedRoots}
	switch {
	case len(protected) > 0 || len(protectedRoots) > 0:
		pair.Class = RiskProtectedOverlap
		pair.Explanation = "both committed final diffs touch the same contract-sensitive or shared-authority root"
	case len(overlap) == 0:
		pair.Class = RiskDisjointPaths
		pair.Explanation = "committed final-diff path sets are disjoint"
	default:
		pair.Class = RiskOverlappingPaths
		pair.Explanation = "committed final-diff path sets overlap"
	}
	return pair
}

func canonicalRiskPolicy(policy RiskPolicy) (RiskPolicy, error) {
	if policy.PolicyIdentity == "" || strings.TrimSpace(policy.PolicyIdentity) != policy.PolicyIdentity || containsControl(policy.PolicyIdentity) || !utf8.ValidString(policy.PolicyIdentity) {
		return RiskPolicy{}, errors.New("risk policy identity must be non-empty, trimmed, and contain no control characters")
	}
	var err error
	policy.ContractSensitivePaths, err = canonicalRoots(policy.ContractSensitivePaths)
	if err != nil {
		return RiskPolicy{}, fmt.Errorf("contract-sensitive paths: %w", err)
	}
	policy.SharedAuthorityPaths, err = canonicalRoots(policy.SharedAuthorityPaths)
	if err != nil {
		return RiskPolicy{}, fmt.Errorf("shared-authority paths: %w", err)
	}
	return policy, nil
}

func canonicalRoots(values []string) ([]string, error) {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if err := validateGitPath(value); err != nil {
			return nil, err
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func parseNameStatus(output []byte) ([]PathChange, error) {
	if len(output) == 0 {
		return []PathChange{}, nil
	}
	if output[len(output)-1] != 0 {
		return nil, errors.New("malformed Git name-status output: missing NUL terminator")
	}
	tokens := bytes.Split(output[:len(output)-1], []byte{0})
	changes := make([]PathChange, 0, len(tokens)/2)
	seen := make(map[string]struct{})
	for index := 0; index < len(tokens); {
		if index+1 >= len(tokens) || len(tokens[index]) == 0 || len(tokens[index+1]) == 0 {
			return nil, errors.New("malformed Git name-status output: incomplete entry")
		}
		status := string(tokens[index])
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			return nil, fmt.Errorf("ambiguous rename/copy path state %q", status)
		}
		if status != "A" && status != "D" && status != "M" && status != "T" {
			return nil, fmt.Errorf("malformed or unsupported Git path status %q", status)
		}
		changedPath := string(tokens[index+1])
		if err := validateGitPath(changedPath); err != nil {
			return nil, fmt.Errorf("malformed Git name-status output: %w", err)
		}
		if _, exists := seen[changedPath]; exists {
			return nil, fmt.Errorf("ambiguous duplicate path state %q", changedPath)
		}
		seen[changedPath] = struct{}{}
		changes = append(changes, PathChange{Status: status, Path: changedPath})
		index += 2
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Path == changes[j].Path {
			return changes[i].Status < changes[j].Status
		}
		return changes[i].Path < changes[j].Path
	})
	return changes, nil
}

func matchesAnyRoot(changedPath string, roots []string) bool {
	for _, root := range roots {
		if changedPath == root || strings.HasPrefix(changedPath, root+"/") {
			return true
		}
	}
	return false
}

func commonProtectedRoots(left, right []PathChange, roots []string) []string {
	common := make([]string, 0)
	seen := make(map[string]struct{})
	for _, root := range roots {
		if _, exists := seen[root]; exists {
			continue
		}
		leftTouches := false
		rightTouches := false
		for _, change := range left {
			leftTouches = leftTouches || matchesAnyRoot(change.Path, []string{root})
		}
		for _, change := range right {
			rightTouches = rightTouches || matchesAnyRoot(change.Path, []string{root})
		}
		if leftTouches && rightTouches {
			common = append(common, root)
			seen[root] = struct{}{}
		}
	}
	sort.Strings(common)
	return common
}

func higherRisk(left, right RiskClass) RiskClass {
	rank := map[RiskClass]int{RiskDisjointPaths: 0, RiskOverlappingPaths: 1, RiskProtectedOverlap: 2}
	if rank[right] > rank[left] {
		return right
	}
	return left
}

func candidateIdentity(candidate AcceptedCandidate) CandidateIdentity {
	input := candidate.data
	return CandidateIdentity{ProjectID: input.ProjectID, PlanID: input.PlanID, RunID: input.RunID, AttemptID: input.AttemptID, HeadSHA: input.HeadSHA}
}

func cloneCandidateDiffs(values []CandidateDiffEvidence) []CandidateDiffEvidence {
	cloned := make([]CandidateDiffEvidence, len(values))
	for index, value := range values {
		cloned[index] = value
		cloned[index].Candidate = cloneCandidate(value.Candidate)
		cloned[index].Changes = append([]PathChange(nil), value.Changes...)
		cloned[index].Commands = make([]GitCommandEvidence, len(value.Commands))
		for commandIndex, command := range value.Commands {
			cloned[index].Commands[commandIndex] = command
			cloned[index].Commands[commandIndex].Argv = append([]string(nil), command.Argv...)
		}
	}
	return cloned
}

func clonePairRisks(values []PairRiskEvidence) []PairRiskEvidence {
	cloned := make([]PairRiskEvidence, len(values))
	for index, value := range values {
		cloned[index] = value
		cloned[index].OverlappingPaths = append([]string(nil), value.OverlappingPaths...)
		cloned[index].ProtectedPaths = append([]string(nil), value.ProtectedPaths...)
		cloned[index].ProtectedRoots = append([]string(nil), value.ProtectedRoots...)
	}
	return cloned
}

func cloneRiskPolicy(policy RiskPolicy) RiskPolicy {
	policy.ContractSensitivePaths = append(make([]string, 0, len(policy.ContractSensitivePaths)), policy.ContractSensitivePaths...)
	policy.SharedAuthorityPaths = append(make([]string, 0, len(policy.SharedAuthorityPaths)), policy.SharedAuthorityPaths...)
	return policy
}

func commandEvidence(result gitResult) GitCommandEvidence {
	stdout := sha256.Sum256(result.Stdout)
	stderr := sha256.Sum256(result.Stderr)
	return GitCommandEvidence{
		Argv: append([]string(nil), result.Argv...), ExitCode: result.ExitCode,
		StdoutSHA256: hex.EncodeToString(stdout[:]), StderrSHA256: hex.EncodeToString(stderr[:]),
		StdoutBytes: len(result.Stdout), StderrBytes: len(result.Stderr),
		StdoutLimitBytes: result.StdoutLimitBytes, StderrLimitBytes: result.StderrLimitBytes,
		StdoutTruncated: result.StdoutTruncated, StderrTruncated: result.StderrTruncated,
	}
}

type gitResult struct {
	Argv             []string
	Stdout           []byte
	Stderr           []byte
	ExitCode         int
	StdoutLimitBytes int
	StderrLimitBytes int
	StdoutTruncated  bool
	StderrTruncated  bool
}

type gitRunner interface {
	Run(context.Context, string, ...string) (gitResult, error)
}

type execGitRunner struct {
	stdoutLimitBytes int
	stderrLimitBytes int
}

func (runner execGitRunner) Run(ctx context.Context, repository string, args ...string) (gitResult, error) {
	argv := append([]string{"git"}, args...)
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = repository
	command.Env = gitexec.Environment()
	stdoutLimit, stderrLimit := runner.outputLimits()
	stdout := boundedBuffer{limit: stdoutLimit}
	stderr := boundedBuffer{limit: stderrLimit}
	command.Stdout = &stdout
	command.Stderr = &stderr
	result := gitResult{Argv: argv, ExitCode: -1, StdoutLimitBytes: stdoutLimit, StderrLimitBytes: stderrLimit}
	err := command.Run()
	result.Stdout = stdout.bytes()
	result.Stderr = stderr.bytes()
	result.StdoutTruncated = stdout.truncated
	result.StderrTruncated = stderr.truncated
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if err == nil {
		result.ExitCode = 0
		return result, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.ExitCode = exitError.ExitCode()
		return result, nil
	}
	return result, err
}

func (runner execGitRunner) outputLimits() (int, int) {
	stdoutLimit := runner.stdoutLimitBytes
	if stdoutLimit <= 0 {
		stdoutLimit = gitStdoutLimitBytes
	}
	stderrLimit := runner.stderrLimitBytes
	if stderrLimit <= 0 {
		stderrLimit = gitStderrLimitBytes
	}
	return stdoutLimit, stderrLimit
}

type boundedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	written := len(value)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > len(value) {
		remaining = len(value)
	}
	if remaining > 0 {
		_, _ = buffer.buffer.Write(value[:remaining])
	}
	if remaining < len(value) {
		buffer.truncated = true
	}
	return written, nil
}

func (buffer *boundedBuffer) bytes() []byte {
	return append([]byte(nil), buffer.buffer.Bytes()...)
}

func gitOutputTruncationError(result gitResult) error {
	streams := make([]string, 0, 2)
	if result.StdoutTruncated {
		streams = append(streams, fmt.Sprintf("stdout exceeded %d-byte limit", result.StdoutLimitBytes))
	}
	if result.StderrTruncated {
		streams = append(streams, fmt.Sprintf("stderr exceeded %d-byte limit", result.StderrLimitBytes))
	}
	return fmt.Errorf("Git command output truncated: %s", strings.Join(streams, "; "))
}

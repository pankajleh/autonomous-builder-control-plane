package integrationworkspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/scheduler"
)

const (
	resultSchemaVersion   = 1
	defaultStdoutLimit    = 4 * 1024 * 1024
	defaultStderrLimit    = 1024 * 1024
	maximumCandidateCount = 256
	captureEvidenceKind   = "textual-integration-capture"
	cleanupEvidenceKind   = "textual-integration-cleanup"
)

// ArtifactWriter publishes immutable evidence. evidence.Store implements this
// interface directly.
type ArtifactWriter interface {
	Root() string
	WriteBytes(name, kind string, data []byte) (ledger.EvidenceRef, error)
}

// Config defines the explicit controller-owned temporary root and immutable
// evidence publisher. The temporary root must already exist as a canonical,
// non-symlink directory.
type Config struct {
	TemporaryRoot    string
	Artifacts        ArtifactWriter
	StdoutLimitBytes int
	StderrLimitBytes int
}

// Request binds one integration evaluation to an exact baseline, an immutable
// scheduler risk report, and unique evidence artifact names.
type Request struct {
	BaselineSHA    string
	RiskReport     scheduler.RiskReport
	EvidencePrefix string
}

// Controller owns creation and bounded cleanup of disposable repositories.
type Controller struct {
	temporaryRoot string
	evidenceRoot  string
	artifacts     ArtifactWriter
	runner        gitRunner
	removeAll     func(string) error
}

// NewController validates and freezes the disposable-workspace boundary.
func NewController(config Config) (*Controller, error) {
	if config.Artifacts == nil {
		return nil, errors.New("artifact writer is required")
	}
	root, err := validateCanonicalDirectory("temporary root", config.TemporaryRoot)
	if err != nil {
		return nil, err
	}
	evidenceRoot, err := validateCanonicalDirectory("evidence root", config.Artifacts.Root())
	if err != nil {
		return nil, err
	}
	if pathsOverlap(root, evidenceRoot) {
		return nil, errors.New("temporary and evidence roots must be disjoint")
	}
	stdoutLimit := config.StdoutLimitBytes
	if stdoutLimit == 0 {
		stdoutLimit = defaultStdoutLimit
	}
	stderrLimit := config.StderrLimitBytes
	if stderrLimit == 0 {
		stderrLimit = defaultStderrLimit
	}
	if stdoutLimit < 1 || stderrLimit < 1 {
		return nil, errors.New("Git output limits must be positive")
	}
	return &Controller{
		temporaryRoot: root,
		evidenceRoot:  evidenceRoot,
		artifacts:     config.Artifacts,
		runner:        execGitRunner{stdoutLimitBytes: stdoutLimit, stderrLimitBytes: stderrLimit},
		removeAll:     os.RemoveAll,
	}, nil
}

// Integrate evaluates all candidates in scheduler-governed order. Textual
// conflict is a successful evidence outcome. Any ambiguity returns an immutable
// UNAVAILABLE result and a non-nil error.
func (controller *Controller) Integrate(ctx context.Context, request Request) (Result, error) {
	record := resultRecord{SchemaVersion: resultSchemaVersion, Status: StatusUnavailable, BaselineSHA: request.BaselineSHA}
	if ctx == nil {
		return unavailable(record, errors.New("context is required"))
	}
	if controller == nil || controller.runner == nil || controller.artifacts == nil || controller.removeAll == nil {
		return unavailable(record, errors.New("controller is required"))
	}
	if err := validateObjectID(request.BaselineSHA); err != nil {
		return unavailable(record, fmt.Errorf("baseline SHA: %w", err))
	}
	if err := validateEvidencePrefix(request.EvidencePrefix); err != nil {
		return unavailable(record, err)
	}
	candidates, err := validateRiskReport(request.RiskReport, request.BaselineSHA)
	if err != nil {
		return unavailable(record, err)
	}
	record.RiskReportSHA256 = request.RiskReport.SHA256()
	record.RiskClass = request.RiskReport.Class()
	record.Candidates = cloneCandidates(candidates)

	repository := candidates[0].Input().Repository
	if _, err := validateCanonicalDirectory("candidate repository", repository); err != nil {
		return unavailable(record, err)
	}
	if pathsOverlap(repository, controller.temporaryRoot) || pathsOverlap(repository, controller.evidenceRoot) {
		return unavailable(record, errors.New("candidate repository, temporary root, and evidence root must be disjoint"))
	}

	run := func(directory string, arguments ...string) (gitResult, error) {
		arguments = append([]string{noReplaceObjectsOption}, arguments...)
		result, runErr := controller.runner.Run(ctx, directory, arguments...)
		record.Commands = append(record.Commands, commandEvidence(result))
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if result.StdoutTruncated || result.StderrTruncated {
			return result, truncationError(result)
		}
		return result, runErr
	}

	if err := verifySourceObjects(run, repository, request.BaselineSHA, candidates); err != nil {
		return controller.finishUnavailable(request.EvidencePrefix, record, err)
	}

	workspace, err := os.MkdirTemp(controller.temporaryRoot, "integration-")
	if err != nil {
		return controller.finishUnavailable(request.EvidencePrefix, record, fmt.Errorf("create disposable workspace: %w", err))
	}
	workspaceID := filepath.Base(workspace)
	record.Cleanup.TemporaryRoot = controller.temporaryRoot
	record.Cleanup.WorkspacePath = workspace
	record.Cleanup.WorkspaceID = workspaceID
	if err := validateCreatedWorkspace(controller.temporaryRoot, workspace); err != nil {
		return controller.finishUnavailable(request.EvidencePrefix, record, err)
	}

	integrationErr := controller.integrateWorkspace(run, workspace, repository, request.BaselineSHA, candidates, &record)
	if integrationErr != nil {
		record.Status = StatusUnavailable
		record.Failure = integrationErr.Error()
	}

	captureBytes, err := json.Marshal(captureRecord(record))
	if err != nil {
		return unavailable(record, fmt.Errorf("marshal pre-cleanup evidence: %w", err))
	}
	captureRef, err := controller.artifacts.WriteBytes(request.EvidencePrefix+"-capture.json", captureEvidenceKind, captureBytes)
	if err != nil {
		// Evidence was not preserved, so leave the exact workspace intact.
		return unavailable(record, fmt.Errorf("publish pre-cleanup evidence; workspace preserved at %s: %w", workspace, err))
	}
	if err := verifyPublishedEvidence(captureRef, captureEvidenceKind, captureBytes, controller.evidenceRoot, workspace); err != nil {
		return unavailable(record, fmt.Errorf("verify pre-cleanup evidence; workspace preserved at %s: %w", workspace, err))
	}
	record.CaptureRef = captureRef

	if err := controller.cleanupWorkspace(workspace); err != nil {
		return unavailable(record, err)
	}
	record.Cleanup.WorkspaceRemoved = true
	cleanupBytes, err := json.Marshal(struct {
		SchemaVersion int                `json:"schema_version"`
		CaptureRef    ledger.EvidenceRef `json:"capture_ref"`
		Cleanup       CleanupEvidence    `json:"cleanup"`
	}{resultSchemaVersion, captureRef, record.Cleanup})
	if err != nil {
		return unavailable(record, fmt.Errorf("marshal cleanup evidence: %w", err))
	}
	cleanupRef, err := controller.artifacts.WriteBytes(request.EvidencePrefix+"-cleanup.json", cleanupEvidenceKind, cleanupBytes)
	if err != nil {
		return unavailable(record, fmt.Errorf("publish cleanup evidence: %w", err))
	}
	if err := verifyPublishedEvidence(cleanupRef, cleanupEvidenceKind, cleanupBytes, controller.evidenceRoot, ""); err != nil {
		return unavailable(record, fmt.Errorf("verify cleanup evidence: %w", err))
	}
	record.CleanupRef = cleanupRef
	if integrationErr != nil {
		return newResult(record), integrationErr
	}
	return newResult(record), nil
}

func (controller *Controller) integrateWorkspace(
	run func(string, ...string) (gitResult, error),
	workspace, repository, baselineSHA string,
	candidates []scheduler.AcceptedCandidate,
	record *resultRecord,
) error {
	commands := [][]string{
		{"init", "--quiet", "--initial-branch=integration"},
		append([]string{"fetch", "--quiet", "--no-tags", "--force", "--no-write-fetch-head", repository}, objectIDs(baselineSHA, candidates)...),
		{"checkout", "--quiet", "--detach", "--force", baselineSHA, "--"},
	}
	for _, arguments := range commands {
		result, err := run(workspace, arguments...)
		if err != nil {
			return fmt.Errorf("Git %s: %w", arguments[0], err)
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("Git %s failed with exit code %d", arguments[0], result.ExitCode)
		}
	}
	if err := verifyDetachedCleanHead(run, workspace, baselineSHA); err != nil {
		return err
	}

	for _, candidate := range candidates {
		input := candidate.Input()
		commandStart := len(record.Commands)
		before, err := exactHead(run, workspace)
		if err != nil {
			return err
		}
		merge, err := run(workspace,
			"-c", "user.name=ABCP Integration Controller",
			"-c", "user.email=integration@invalid",
			"-c", "commit.gpgSign=false",
			"merge", "--no-ff", "--no-edit", "--no-stat", "--no-progress", input.HeadSHA)
		if err != nil {
			return fmt.Errorf("apply candidate %s/%s: %w", input.RunID, input.AttemptID, err)
		}
		step := CandidateStep{Candidate: candidate, BeforeSHA: before, CommandStart: commandStart}
		switch merge.ExitCode {
		case 0:
			after, headErr := exactHead(run, workspace)
			if headErr != nil {
				return headErr
			}
			step.AfterSHA = after
			if after == before {
				if err := verifyAncestor(run, workspace, input.HeadSHA, after, "already-contained candidate"); err != nil {
					return err
				}
				step.Outcome = StepAlreadyContained
			} else {
				if err := verifyMergeParents(run, workspace, after, before, input.HeadSHA); err != nil {
					return err
				}
				step.Outcome = StepMerged
			}
			if err := verifyCleanNoMergeState(run, workspace); err != nil {
				return err
			}
			record.Steps = append(record.Steps, step)
		case 1:
			paths, conflictErr := verifyConflict(run, workspace, input.HeadSHA)
			if conflictErr != nil {
				return fmt.Errorf("candidate merge exited 1 without unambiguous textual conflicts: %w", conflictErr)
			}
			step.Outcome = StepConflict
			step.ConflictPaths = paths
			step.CommandEnd = len(record.Commands)
			record.Steps = append(record.Steps, step)
			record.Status = StatusConflict
			return nil
		default:
			return fmt.Errorf("candidate merge failed with exit code %d", merge.ExitCode)
		}
		record.Steps[len(record.Steps)-1].CommandEnd = len(record.Commands)
	}
	record.Status = StatusClean
	return nil
}

func verifySourceObjects(run func(string, ...string) (gitResult, error), repository, baseline string, candidates []scheduler.AcceptedCandidate) error {
	seenHeads := make(map[string]struct{}, len(candidates))
	for _, objectID := range objectIDs(baseline, candidates) {
		result, err := run(repository, "rev-parse", "--verify", "--end-of-options", objectID+"^{commit}")
		if err != nil {
			return fmt.Errorf("verify commit %s: %w", objectID, err)
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("missing or non-commit Git object %s", objectID)
		}
		if len(result.Stderr) != 0 || string(result.Stdout) != objectID+"\n" {
			return fmt.Errorf("ambiguous object verification output for %s", objectID)
		}
	}
	for _, candidate := range candidates {
		input := candidate.Input()
		if _, exists := seenHeads[input.HeadSHA]; exists {
			return fmt.Errorf("duplicate accepted candidate head %s", input.HeadSHA)
		}
		seenHeads[input.HeadSHA] = struct{}{}
		if err := verifyAncestor(run, repository, input.StartSHA, input.HeadSHA, "accepted candidate"); err != nil {
			return err
		}
	}
	return nil
}

func verifyAncestor(run func(string, ...string) (gitResult, error), repository, ancestor, descendant, label string) error {
	result, err := run(repository, "merge-base", "--is-ancestor", ancestor, descendant)
	if err != nil {
		return fmt.Errorf("verify %s ancestry: %w", label, err)
	}
	if len(result.Stdout) != 0 || len(result.Stderr) != 0 {
		return fmt.Errorf("ambiguous %s ancestry output", label)
	}
	if result.ExitCode == 1 {
		return fmt.Errorf("invalid %s ancestry", label)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("verify %s ancestry exited %d", label, result.ExitCode)
	}
	return nil
}

func verifyDetachedCleanHead(run func(string, ...string) (gitResult, error), workspace, baseline string) error {
	head, err := exactHead(run, workspace)
	if err != nil {
		return err
	}
	if head != baseline {
		return errors.New("disposable workspace did not check out the exact baseline")
	}
	symbolic, err := run(workspace, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		return fmt.Errorf("verify detached HEAD: %w", err)
	}
	if symbolic.ExitCode != 1 || len(symbolic.Stdout) != 0 || len(symbolic.Stderr) != 0 {
		return errors.New("disposable workspace HEAD is not unambiguously detached")
	}
	return verifyCleanNoMergeState(run, workspace)
}

func exactHead(run func(string, ...string) (gitResult, error), workspace string) (string, error) {
	result, err := run(workspace, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve disposable HEAD: %w", err)
	}
	if result.ExitCode != 0 || len(result.Stderr) != 0 {
		return "", errors.New("resolve disposable HEAD produced an ambiguous outcome")
	}
	value := strings.TrimSuffix(string(result.Stdout), "\n")
	if string(result.Stdout) != value+"\n" || validateObjectID(value) != nil {
		return "", errors.New("resolve disposable HEAD produced malformed output")
	}
	return value, nil
}

func verifyMergeParents(run func(string, ...string) (gitResult, error), workspace, merged, before, candidate string) error {
	result, err := run(workspace, "rev-list", "--parents", "-n", "1", merged)
	if err != nil {
		return fmt.Errorf("verify integration commit parents: %w", err)
	}
	expected := strings.Join([]string{merged, before, candidate}, " ") + "\n"
	if result.ExitCode != 0 || len(result.Stderr) != 0 || string(result.Stdout) != expected {
		return errors.New("integration commit does not have the exact governed parents")
	}
	return nil
}

func verifyCleanNoMergeState(run func(string, ...string) (gitResult, error), workspace string) error {
	status, err := run(workspace, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("verify disposable status: %w", err)
	}
	if status.ExitCode != 0 || len(status.Stdout) != 0 || len(status.Stderr) != 0 {
		return errors.New("unexpected dirty or ambiguous disposable repository state")
	}
	mergeHead, err := run(workspace, "rev-parse", "-q", "--verify", "MERGE_HEAD")
	if err != nil {
		return fmt.Errorf("verify merge state: %w", err)
	}
	if mergeHead.ExitCode != 1 || len(mergeHead.Stdout) != 0 || len(mergeHead.Stderr) != 0 {
		return errors.New("unexpected active or ambiguous merge state")
	}
	return nil
}

func verifyConflict(run func(string, ...string) (gitResult, error), workspace, candidateHead string) ([]string, error) {
	mergeHead, err := run(workspace, "rev-parse", "--verify", "MERGE_HEAD^{commit}")
	if err != nil {
		return nil, err
	}
	if mergeHead.ExitCode != 0 || len(mergeHead.Stderr) != 0 || string(mergeHead.Stdout) != candidateHead+"\n" {
		return nil, errors.New("MERGE_HEAD does not match the exact candidate head")
	}
	diff, err := run(workspace, "diff", "--name-only", "--diff-filter=U", "-z", "--")
	if err != nil {
		return nil, err
	}
	if diff.ExitCode != 0 || len(diff.Stderr) != 0 {
		return nil, errors.New("conflict path query failed or produced stderr")
	}
	paths, err := parseNULPaths(diff.Stdout)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, errors.New("no unmerged paths")
	}
	unmerged, err := run(workspace, "ls-files", "--unmerged", "-z")
	if err != nil {
		return nil, err
	}
	if unmerged.ExitCode != 0 || len(unmerged.Stderr) != 0 {
		return nil, errors.New("unmerged index query failed or produced stderr")
	}
	indexPaths, err := parseUnmergedIndex(unmerged.Stdout)
	if err != nil {
		return nil, err
	}
	if !equalStrings(paths, indexPaths) {
		return nil, errors.New("conflict path queries disagree")
	}
	return paths, nil
}

func validateRiskReport(report scheduler.RiskReport, baseline string) ([]scheduler.AcceptedCandidate, error) {
	canonical, err := report.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("risk report is required: %w", err)
	}
	digest := sha256.Sum256(canonical)
	if report.SHA256() != hex.EncodeToString(digest[:]) || !bytes.Equal(canonical, report.CanonicalJSON()) {
		return nil, errors.New("risk report canonical evidence is inconsistent")
	}
	diffs := report.CandidateDiffs()
	if len(diffs) == 0 {
		return nil, errors.New("risk report has no accepted candidates")
	}
	if len(diffs) > maximumCandidateCount {
		return nil, fmt.Errorf("risk report exceeds %d candidates", maximumCandidateCount)
	}
	candidates := make([]scheduler.AcceptedCandidate, len(diffs))
	var repository string
	seenIdentity := make(map[string]struct{}, len(diffs))
	for index, diff := range diffs {
		candidateBytes, marshalErr := json.Marshal(diff.Candidate)
		if marshalErr != nil || len(candidateBytes) == 0 {
			return nil, fmt.Errorf("risk candidate %d is invalid", index)
		}
		input := diff.Candidate.Input()
		if input.StartSHA != baseline {
			return nil, fmt.Errorf("risk candidate %d start SHA does not equal the exact integration baseline", index)
		}
		if repository == "" {
			repository = input.Repository
		} else if input.Repository != repository {
			return nil, errors.New("risk candidates identify different repositories")
		}
		if _, exists := seenIdentity[diff.Candidate.Key()]; exists {
			return nil, errors.New("risk report repeats an accepted candidate identity")
		}
		seenIdentity[diff.Candidate.Key()] = struct{}{}
		candidates[index] = diff.Candidate
	}
	ordered := cloneCandidates(candidates)
	sort.Slice(ordered, func(left, right int) bool {
		return candidateOrderKey(ordered[left]) < candidateOrderKey(ordered[right])
	})
	for index := range candidates {
		if candidates[index].Key() != ordered[index].Key() {
			return nil, errors.New("risk candidates are not in deterministic governed order")
		}
	}
	return candidates, nil
}

func candidateOrderKey(candidate scheduler.AcceptedCandidate) string {
	input := candidate.Input()
	return strings.Join([]string{
		input.AcceptedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
		input.ProjectID, input.PlanID, input.RunID, input.AttemptID, input.Repository,
		input.Branch, input.StartSHA, input.HeadSHA,
	}, "\x00")
}

func objectIDs(baseline string, candidates []scheduler.AcceptedCandidate) []string {
	values := []string{baseline}
	seen := map[string]struct{}{baseline: {}}
	for _, candidate := range candidates {
		input := candidate.Input()
		for _, value := range []string{input.StartSHA, input.HeadSHA} {
			if _, exists := seen[value]; !exists {
				values = append(values, value)
				seen[value] = struct{}{}
			}
		}
	}
	return values
}

func (controller *Controller) cleanupWorkspace(workspace string) error {
	if err := validateCreatedWorkspace(controller.temporaryRoot, workspace); err != nil {
		return fmt.Errorf("refuse uncertain cleanup: %w", err)
	}
	if err := controller.removeAll(workspace); err != nil {
		return fmt.Errorf("remove disposable workspace: %w", err)
	}
	if _, err := os.Lstat(workspace); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return errors.New("cleanup uncertainty: disposable workspace still exists")
		}
		return fmt.Errorf("cleanup uncertainty: inspect removed workspace: %w", err)
	}
	return nil
}

func (controller *Controller) finishUnavailable(prefix string, record resultRecord, cause error) (Result, error) {
	record.Status = StatusUnavailable
	record.Failure = cause.Error()
	data, marshalErr := json.Marshal(captureRecord(record))
	if marshalErr != nil {
		return unavailable(record, errors.Join(cause, marshalErr))
	}
	ref, publishErr := controller.artifacts.WriteBytes(prefix+"-capture.json", captureEvidenceKind, data)
	if publishErr == nil {
		if verifyErr := verifyPublishedEvidence(ref, captureEvidenceKind, data, controller.evidenceRoot, ""); verifyErr == nil {
			record.CaptureRef = ref
		} else {
			publishErr = verifyErr
		}
	}
	if publishErr != nil {
		cause = errors.Join(cause, fmt.Errorf("publish unavailable evidence: %w", publishErr))
	}
	return newResult(record), cause
}

func unavailable(record resultRecord, cause error) (Result, error) {
	record.Status = StatusUnavailable
	record.Failure = cause.Error()
	return newResult(record), cause
}

func captureRecord(record resultRecord) resultRecord {
	record.CaptureRef = ledger.EvidenceRef{}
	record.CleanupRef = ledger.EvidenceRef{}
	record.Cleanup.WorkspaceRemoved = false
	return record
}

func verifyPublishedEvidence(ref ledger.EvidenceRef, kind string, expected []byte, evidenceRoot, forbiddenRoot string) error {
	if ref.Kind != kind || ref.URI == "" || !filepath.IsAbs(ref.URI) || filepath.Clean(ref.URI) != ref.URI {
		return errors.New("artifact writer returned an invalid evidence reference")
	}
	if !pathWithin(evidenceRoot, ref.URI) {
		return errors.New("artifact writer published evidence outside its controller-owned root")
	}
	if forbiddenRoot != "" && pathWithin(forbiddenRoot, ref.URI) {
		return errors.New("artifact writer published evidence inside the disposable workspace")
	}
	info, err := os.Lstat(ref.URI)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("published evidence is not a regular non-symlink file")
	}
	canonical, err := filepath.EvalSymlinks(ref.URI)
	if err != nil || canonical != ref.URI {
		return errors.New("published evidence path is not canonical")
	}
	data, err := os.ReadFile(ref.URI)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	if !bytes.Equal(data, expected) || ref.SHA256 != hex.EncodeToString(digest[:]) {
		return errors.New("published evidence bytes or digest do not match")
	}
	return nil
}

func validateCanonicalDirectory(label, value string) (string, error) {
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return "", fmt.Errorf("%s must be an absolute clean path", label)
	}
	info, err := os.Lstat(value)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", label, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s must be a non-symlink directory", label)
	}
	canonical, err := filepath.EvalSymlinks(value)
	if err != nil {
		return "", fmt.Errorf("canonicalize %s: %w", label, err)
	}
	if canonical != value {
		return "", fmt.Errorf("%s contains a symlink or non-canonical component", label)
	}
	return canonical, nil
}

func validateCreatedWorkspace(root, workspace string) error {
	if !pathWithin(root, workspace) || filepath.Dir(workspace) != root {
		return errors.New("disposable workspace escapes the controller temporary root")
	}
	canonical, err := validateCanonicalDirectory("disposable workspace", workspace)
	if err != nil {
		return err
	}
	if canonical != workspace {
		return errors.New("disposable workspace canonical path changed")
	}
	return nil
}

func pathWithin(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func pathsOverlap(left, right string) bool {
	return left == right || pathWithin(left, right) || pathWithin(right, left)
}

func validateObjectID(value string) error {
	if len(value) != 40 && len(value) != 64 {
		return errors.New("must be an exact lowercase Git object ID")
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return errors.New("must be an exact lowercase Git object ID")
		}
	}
	return nil
}

func validateEvidencePrefix(value string) error {
	if value == "" || value == "." || value == ".." || strings.TrimSpace(value) != value || !utf8.ValidString(value) ||
		strings.IndexFunc(value, func(character rune) bool { return character < ' ' || character == 0x7f }) >= 0 ||
		filepath.Base(value) != value || strings.ContainsAny(value, `/\\`) {
		return errors.New("evidence prefix must be one safe path component")
	}
	return nil
}

func parseNULPaths(output []byte) ([]string, error) {
	if len(output) == 0 || output[len(output)-1] != 0 {
		return nil, errors.New("malformed conflict paths: missing NUL-terminated entries")
	}
	tokens := bytes.Split(output[:len(output)-1], []byte{0})
	seen := make(map[string]struct{}, len(tokens))
	paths := make([]string, 0, len(tokens))
	for _, token := range tokens {
		value := string(token)
		if err := validateGitPath(value); err != nil {
			return nil, err
		}
		if _, exists := seen[value]; exists {
			return nil, fmt.Errorf("duplicate conflict path %q", value)
		}
		seen[value] = struct{}{}
		paths = append(paths, value)
	}
	sort.Strings(paths)
	return paths, nil
}

func parseUnmergedIndex(output []byte) ([]string, error) {
	if len(output) == 0 || output[len(output)-1] != 0 {
		return nil, errors.New("malformed unmerged index output")
	}
	entries := bytes.Split(output[:len(output)-1], []byte{0})
	stages := make(map[string]map[int]struct{})
	for _, entry := range entries {
		tab := bytes.IndexByte(entry, '\t')
		if tab <= 0 || tab == len(entry)-1 {
			return nil, errors.New("malformed unmerged index entry")
		}
		metadata := strings.Split(string(entry[:tab]), " ")
		if len(metadata) != 3 || len(metadata[0]) != 6 || validateObjectID(metadata[1]) != nil {
			return nil, errors.New("malformed unmerged index metadata")
		}
		stage, err := strconv.Atoi(metadata[2])
		if err != nil || stage < 1 || stage > 3 {
			return nil, errors.New("malformed unmerged index stage")
		}
		conflictPath := string(entry[tab+1:])
		if err := validateGitPath(conflictPath); err != nil {
			return nil, err
		}
		if stages[conflictPath] == nil {
			stages[conflictPath] = make(map[int]struct{})
		}
		if _, exists := stages[conflictPath][stage]; exists {
			return nil, fmt.Errorf("duplicate unmerged index stage for %q", conflictPath)
		}
		stages[conflictPath][stage] = struct{}{}
	}
	paths := make([]string, 0, len(stages))
	for conflictPath, pathStages := range stages {
		if len(pathStages) < 2 {
			return nil, fmt.Errorf("incomplete unmerged index stages for %q", conflictPath)
		}
		paths = append(paths, conflictPath)
	}
	sort.Strings(paths)
	return paths, nil
}

func validateGitPath(value string) error {
	if value == "" || !utf8.ValidString(value) || strings.HasPrefix(value, "/") || path.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return fmt.Errorf("unsafe or non-canonical repository-relative path %q", value)
	}
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

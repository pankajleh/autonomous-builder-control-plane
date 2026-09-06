package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/blocker"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

const (
	EventRecoveryInspected         = "RECOVERY_INSPECTED"
	EventRecoverySnapshotPublished = "RECOVERY_SNAPSHOT_PUBLISHED"
	EventRecoveryCleanupAuthorized = "RECOVERY_CLEANUP_AUTHORIZED"
	EventRecoveryCleanupCompleted  = "RECOVERY_CLEANUP_COMPLETED"
	EventRecoveryCleanupFailed     = "RECOVERY_CLEANUP_FAILED"
	EventRestartPrepared           = "RESTART_PREPARED"
)

// EventAppender is the append-only ledger operation required by recovery.
type EventAppender interface {
	Append(ledger.Event) error
}

// RecoveryAuthorization is an explicit actor decision. It is validated
// before cleanup and converted to a durable ResumeAuthority only after the
// governed stale state has been cleaned successfully.
type RecoveryAuthorization struct {
	AuthorizedAttempt AttemptIdentity `json:"authorized_attempt"`
	Actor             string          `json:"actor"`
	Decision          string          `json:"decision"`
	Action            ResumeAction    `json:"action"`
	Timestamp         time.Time       `json:"timestamp"`
	PolicyVersion     string          `json:"policy_version"`
	Boundary          domain.State    `json:"boundary"`
}

// WorkflowRequest contains the exact stale attempt, evidence sources,
// failure signal, and explicit authority needed for one bounded recovery.
type WorkflowRequest struct {
	SnapshotID    string                 `json:"snapshot_id"`
	Ownership     Ownership              `json:"ownership"`
	ProgressRoot  string                 `json:"progress_root,omitempty"`
	ProgressPaths []string               `json:"progress_paths,omitempty"`
	Limits        SnapshotLimits         `json:"limits,omitempty"`
	StateFrom     domain.State           `json:"state_from"`
	Failure       blocker.FailureInput   `json:"failure"`
	Requirement   BlockerRequirement     `json:"requirement"`
	Authorization *RecoveryAuthorization `json:"authorization"`
}

// CleanupRequest is pinned to the successful snapshot and exact governed
// paths. Cleaners must not infer additional runtime state to remove.
type CleanupRequest struct {
	Ownership     Ownership
	Snapshot      PublishedSnapshot
	ProgressRoot  string
	ProgressPaths []string
	Limits        SnapshotLimits
}

// CleanupReport records the exact bounded mutations attempted by a cleaner.
type CleanupReport struct {
	RepositoryPath       string   `json:"repository_path"`
	WorktreePath         string   `json:"worktree_path"`
	Branch               string   `json:"branch"`
	PreservedHeadSHA     string   `json:"preserved_head_sha"`
	WorktreeRemoved      bool     `json:"worktree_removed"`
	RemovedProgressPaths []string `json:"removed_progress_paths,omitempty"`
}

// Cleaner performs only the destructive portion of a recovery workflow.
type Cleaner interface {
	Cleanup(context.Context, CleanupRequest) (CleanupReport, error)
}

// GovernedCleaner removes one registered worktree and explicitly named
// regular progress files. It never deletes the governed branch ref.
type GovernedCleaner struct{}

func (GovernedCleaner) Cleanup(ctx context.Context, request CleanupRequest) (CleanupReport, error) {
	report := CleanupReport{
		RepositoryPath:   request.Ownership.Worktree.RepositoryPath,
		WorktreePath:     request.Ownership.Worktree.Path,
		Branch:           request.Ownership.Worktree.Branch,
		PreservedHeadSHA: request.Snapshot.Metadata.HeadSHA,
	}
	if ctx == nil {
		return report, errors.New("context is required")
	}
	inspection := Inspect(ctx, request.Ownership)
	if !positiveOwnerDead(inspection) {
		return report, fmt.Errorf("cleanup requires current positive owner-dead proof: classification=%s proof=%s", inspection.Classification, inspection.OwnerProof)
	}
	if request.Snapshot.Metadata.Attempt != request.Ownership.Attempt ||
		request.Snapshot.Metadata.Repository != inspection.Ownership.Worktree.RepositoryPath ||
		request.Snapshot.Metadata.Worktree != inspection.Ownership.Worktree.Path ||
		request.Snapshot.Metadata.Branch != inspection.Ownership.Worktree.Branch ||
		request.Snapshot.Metadata.OwnerProof.Ownership != inspection.Ownership {
		return report, errors.New("cleanup target does not match the published recovery snapshot")
	}
	if err := validatePublishedRef(request.Snapshot.MetadataRef, "recovery-snapshot-metadata"); err != nil {
		return report, fmt.Errorf("cleanup snapshot proof: %w", err)
	}

	branchHead, err := cleanupGitOutput(ctx, inspection.Ownership.Worktree.RepositoryPath,
		"rev-parse", "--verify", "refs/heads/"+inspection.Ownership.Worktree.Branch)
	if err != nil {
		return report, fmt.Errorf("resolve governed branch before cleanup: %w", err)
	}
	if branchHead != request.Snapshot.Metadata.HeadSHA {
		return report, errors.New("governed branch changed after snapshot publication")
	}

	limits, err := normalizeSnapshotLimits(request.Limits)
	if err != nil {
		return report, fmt.Errorf("validate cleanup snapshot limits: %w", err)
	}
	status, err := captureGitOutput(ctx, inspection.Ownership.Worktree.Path, limits.StatusBytes,
		"status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return report, fmt.Errorf("verify cleanup status: %w", err)
	}
	if err := verifySnapshotCapture("status", status, request.Snapshot.Metadata.Status); err != nil {
		return report, err
	}
	diff, err := captureGitOutput(ctx, inspection.Ownership.Worktree.Path, limits.DiffBytes,
		"diff", "--binary", "--no-ext-diff", "--no-textconv", "HEAD", "--")
	if err != nil {
		return report, fmt.Errorf("verify cleanup diff: %w", err)
	}
	if err := verifySnapshotCapture("diff", diff, request.Snapshot.Metadata.Diff); err != nil {
		return report, err
	}
	progress, _, err := captureProgress(request.ProgressRoot, request.ProgressPaths, limits.ProgressBytes)
	if err != nil {
		return report, fmt.Errorf("verify cleanup progress: %w", err)
	}
	if err := verifySnapshotCapture("progress", progress, request.Snapshot.Metadata.Progress); err != nil {
		return report, err
	}

	command := exec.CommandContext(ctx, "git", "-C", inspection.Ownership.Worktree.RepositoryPath,
		"worktree", "remove", "--force", "--", inspection.Ownership.Worktree.Path)
	command.Env = gitexec.Environment()
	if output, err := command.CombinedOutput(); err != nil {
		return report, fmt.Errorf("remove governed worktree: %w: %s", err, strings.TrimSpace(string(output)))
	}
	report.WorktreeRemoved = true

	for _, path := range request.ProgressPaths {
		if err := os.Remove(path); err != nil {
			return report, fmt.Errorf("remove governed progress file %q: %w", path, err)
		}
		report.RemovedProgressPaths = append(report.RemovedProgressPaths, path)
	}

	branchHead, err = cleanupGitOutput(ctx, inspection.Ownership.Worktree.RepositoryPath,
		"rev-parse", "--verify", "refs/heads/"+inspection.Ownership.Worktree.Branch)
	if err != nil {
		return report, fmt.Errorf("verify governed branch after cleanup: %w", err)
	}
	if branchHead != request.Snapshot.Metadata.HeadSHA {
		return report, errors.New("cleanup did not preserve governed branch history")
	}
	return report, nil
}

func verifySnapshotCapture(name string, capture boundedCapture, metadata ArtifactMetadata) error {
	digest := sha256.Sum256(capture.data)
	if hex.EncodeToString(digest[:]) != metadata.Ref.SHA256 || capture.total != metadata.SourceBytes ||
		int64(len(capture.data)) != metadata.CapturedBytes || capture.truncated != metadata.Truncated {
		return fmt.Errorf("governed %s state changed after snapshot publication", name)
	}
	return nil
}

func cleanupGitOutput(ctx context.Context, repository string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", repository}, arguments...)...)
	command.Env = gitexec.Environment()
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", arguments[0], err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

// Workflow executes one fail-closed recovery and stops at an EP-002 restart
// boundary. It has no integration, merge, or completion capability.
type Workflow struct {
	events      EventAppender
	artifacts   ArtifactWriter
	snapshotter *Snapshotter
	cleaner     Cleaner
}

// WorkflowResult is restart preparation, not restarted execution.
type WorkflowResult struct {
	Inspection     Inspection             `json:"inspection"`
	Snapshot       PublishedSnapshot      `json:"snapshot"`
	Classification blocker.Classification `json:"classification"`
	Cleanup        CleanupReport          `json:"cleanup"`
	Authorized     AttemptIdentity        `json:"authorized_attempt"`
	Boundary       domain.State           `json:"boundary"`
	EvidenceRefs   []ledger.EvidenceRef   `json:"evidence_refs"`
}

func NewWorkflow(events EventAppender, artifacts ArtifactWriter, cleaner Cleaner) (*Workflow, error) {
	if events == nil {
		return nil, errors.New("recovery event ledger is required")
	}
	if artifacts == nil {
		return nil, errors.New("recovery artifact writer is required")
	}
	if cleaner == nil {
		return nil, errors.New("recovery cleaner is required")
	}
	snapshotter, err := NewSnapshotter(artifacts)
	if err != nil {
		return nil, err
	}
	return &Workflow{events: events, artifacts: artifacts, snapshotter: snapshotter, cleaner: cleaner}, nil
}

// Recover performs inspect, proof, snapshot, classification/authorization,
// cleanup, new-attempt authority, and restart preparation in that order.
func (workflow *Workflow) Recover(ctx context.Context, request WorkflowRequest) (WorkflowResult, error) {
	var result WorkflowResult
	if workflow == nil || workflow.events == nil || workflow.artifacts == nil || workflow.snapshotter == nil || workflow.cleaner == nil {
		return result, errors.New("recovery workflow is not configured")
	}
	if ctx == nil {
		return result, errors.New("context is required")
	}
	if err := validateSnapshotID(request.SnapshotID); err != nil {
		return result, err
	}

	result.Inspection = Inspect(ctx, request.Ownership)
	inspectionRef, err := workflow.writeJSON("recovery-"+request.SnapshotID+"-inspection.json", "recovery-inspection", result.Inspection)
	if err != nil {
		return result, fmt.Errorf("publish recovery inspection: %w", err)
	}
	result.EvidenceRefs = append(result.EvidenceRefs, inspectionRef)
	if err := workflow.appendAction(request.Ownership.Attempt, EventRecoveryInspected, "recovery-inspector", map[string]any{
		"classification": result.Inspection.Classification,
		"owner_proof":    result.Inspection.OwnerProof,
	}, result.EvidenceRefs); err != nil {
		return result, err
	}
	if !positiveOwnerDead(result.Inspection) {
		return result, fmt.Errorf("recovery refused without positive owner-dead proof: classification=%s proof=%s", result.Inspection.Classification, result.Inspection.OwnerProof)
	}

	result.Snapshot, err = workflow.snapshotter.Capture(ctx, SnapshotRequest{
		SnapshotID: request.SnapshotID, Ownership: request.Ownership,
		ProgressRoot: request.ProgressRoot, ProgressPaths: request.ProgressPaths, Limits: request.Limits,
	})
	if err != nil {
		return result, fmt.Errorf("publish pre-cleanup recovery snapshot: %w", err)
	}
	snapshotRefs := []ledger.EvidenceRef{
		result.Snapshot.Metadata.Status.Ref, result.Snapshot.Metadata.Diff.Ref,
		result.Snapshot.Metadata.Progress.Ref, result.Snapshot.MetadataRef,
	}
	result.EvidenceRefs = append(result.EvidenceRefs, snapshotRefs...)
	if err := workflow.appendAction(request.Ownership.Attempt, EventRecoverySnapshotPublished, "recovery-snapshotter", map[string]any{
		"snapshot_id": request.SnapshotID,
		"dirty":       result.Snapshot.Metadata.Dirty,
	}, result.EvidenceRefs); err != nil {
		return result, err
	}

	decision, err := NewBlockerDecision(BlockerDecisionInput{
		Attempt: request.Ownership.Attempt, StateFrom: request.StateFrom, Failure: request.Failure,
		Requirement: request.Requirement, Actor: "recovery-controller", Timestamp: time.Now().UTC(),
		PolicyVersion: authorizationPolicy(request.Authorization), EvidenceRefs: result.EvidenceRefs,
	})
	if err != nil {
		return result, fmt.Errorf("classify recovery blocker: %w", err)
	}
	result.Classification = decision.Classification()
	decisionEvent, err := decision.Event("recovery-classifier")
	if err != nil {
		return result, err
	}
	if err := workflow.events.Append(decisionEvent); err != nil {
		return result, fmt.Errorf("append recovery blocker decision: %w", err)
	}

	_, err = buildRecoveryAuthority(request, decision.StateTo(), result.EvidenceRefs)
	if err != nil {
		return result, fmt.Errorf("validate recovery authority: %w", err)
	}
	if request.Authorization.Action != ResumeActionRestart {
		return result, errors.New("stale worktree cleanup requires restart authority for a new attempt")
	}
	if err := workflow.appendAction(request.Ownership.Attempt, EventRecoveryCleanupAuthorized, request.Authorization.Actor, map[string]any{
		"authorized_attempt": request.Authorization.AuthorizedAttempt,
		"decision":           request.Authorization.Decision,
		"worktree":           request.Ownership.Worktree.Path,
		"progress_paths":     append([]string(nil), request.ProgressPaths...),
	}, result.EvidenceRefs); err != nil {
		return result, err
	}

	result.Cleanup, err = workflow.cleaner.Cleanup(ctx, CleanupRequest{
		Ownership: request.Ownership, Snapshot: result.Snapshot,
		ProgressRoot: request.ProgressRoot, ProgressPaths: request.ProgressPaths, Limits: request.Limits,
	})
	if err != nil {
		failureRef, evidenceErr := workflow.writeJSON("recovery-"+request.SnapshotID+"-cleanup-failed.json", "recovery-cleanup-failure", struct {
			Report CleanupReport `json:"report"`
			Error  string        `json:"error"`
		}{Report: result.Cleanup, Error: err.Error()})
		if evidenceErr == nil {
			result.EvidenceRefs = append(result.EvidenceRefs, failureRef)
			eventErr := workflow.appendAction(request.Ownership.Attempt, EventRecoveryCleanupFailed, "recovery-cleaner", map[string]any{"error": err.Error()}, result.EvidenceRefs)
			return result, errors.Join(fmt.Errorf("cleanup governed stale state: %w", err), eventErr)
		}
		return result, errors.Join(fmt.Errorf("cleanup governed stale state: %w", err), fmt.Errorf("publish cleanup failure evidence: %w", evidenceErr))
	}

	cleanupRef, err := workflow.writeJSON("recovery-"+request.SnapshotID+"-cleanup.json", "recovery-cleanup", result.Cleanup)
	if err != nil {
		return result, fmt.Errorf("publish recovery cleanup result: %w", err)
	}
	result.EvidenceRefs = append(result.EvidenceRefs, cleanupRef)
	if err := workflow.appendAction(request.Ownership.Attempt, EventRecoveryCleanupCompleted, "recovery-cleaner", map[string]any{
		"worktree_removed":       result.Cleanup.WorktreeRemoved,
		"removed_progress_files": len(result.Cleanup.RemovedProgressPaths),
		"preserved_head_sha":     result.Cleanup.PreservedHeadSHA,
	}, result.EvidenceRefs); err != nil {
		return result, err
	}

	finalAuthority, err := buildRecoveryAuthority(request, decision.StateTo(), result.EvidenceRefs)
	if err != nil {
		return result, fmt.Errorf("create new attempt authority: %w", err)
	}
	if err := ValidateResumeAuthority(&finalAuthority, request.Ownership.Attempt, request.Authorization.AuthorizedAttempt,
		decision.StateTo(), request.Authorization.Boundary); err != nil {
		return result, err
	}
	authorityEvent, err := finalAuthority.Event("recovery-controller")
	if err != nil {
		return result, err
	}
	if err := workflow.events.Append(authorityEvent); err != nil {
		return result, fmt.Errorf("append new attempt authority: %w", err)
	}

	result.Authorized = request.Authorization.AuthorizedAttempt
	result.Boundary = request.Authorization.Boundary
	if err := workflow.appendAction(result.Authorized, EventRestartPrepared, "recovery-controller", map[string]any{
		"boundary":      result.Boundary,
		"prior_attempt": request.Ownership.Attempt,
	}, result.EvidenceRefs); err != nil {
		return result, err
	}
	return result, nil
}

func buildRecoveryAuthority(request WorkflowRequest, from domain.State, refs []ledger.EvidenceRef) (ResumeAuthority, error) {
	if request.Authorization == nil {
		return ResumeAuthority{}, errors.New("explicit recovery authorization is required")
	}
	return NewResumeAuthority(ResumeAuthorityInput{
		PriorAttempt: request.Ownership.Attempt, AuthorizedAttempt: request.Authorization.AuthorizedAttempt,
		StateFrom: from, StateTo: request.Authorization.Boundary, Actor: request.Authorization.Actor,
		Decision: request.Authorization.Decision, Action: request.Authorization.Action,
		Timestamp: request.Authorization.Timestamp, PolicyVersion: request.Authorization.PolicyVersion,
		EvidenceRefs: refs,
	})
}

func authorizationPolicy(authorization *RecoveryAuthorization) string {
	if authorization == nil || strings.TrimSpace(authorization.PolicyVersion) == "" {
		return "recovery-authority-missing"
	}
	return authorization.PolicyVersion
}

func positiveOwnerDead(inspection Inspection) bool {
	if inspection.Classification != ClassificationStaleOwnerDead {
		return false
	}
	switch inspection.OwnerProof {
	case OwnerProofProcessAbsent, OwnerProofIdentityMismatch, OwnerProofProcessExited:
		return true
	default:
		return false
	}
}

func (workflow *Workflow) writeJSON(name, kind string, value any) (ledger.EvidenceRef, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return ledger.EvidenceRef{}, err
	}
	return workflow.artifacts.WriteBytes(name, kind, data)
}

func (workflow *Workflow) appendAction(attempt AttemptIdentity, eventType, actor string, payload map[string]any, refs []ledger.EvidenceRef) error {
	event, err := ledger.NewEvent(attempt.RunID, eventType, actor, "recovery-workflow")
	if err != nil {
		return fmt.Errorf("create %s event: %w", eventType, err)
	}
	applyAttemptIdentity(&event, attempt)
	event.Payload = payload
	event.EvidenceRefs = cloneEvidenceRefs(refs)
	if err := workflow.events.Append(event); err != nil {
		return fmt.Errorf("append %s event: %w", eventType, err)
	}
	return nil
}

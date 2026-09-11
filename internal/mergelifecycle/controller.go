package mergelifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

type Config struct {
	StateRoot          string
	Ledger             *ledger.JSONLLedger
	AuthoritySource    AuthoritySource
	CancellationSource CancellationSource
	Provider           Provider
}

type Controller struct {
	ledger         *ledger.JSONLLedger
	source         AuthoritySource
	cancellations  CancellationSource
	provider       Provider
	store          *durableStore
	limits         Limits
	contracts      githublifecycle.Limits
	now            func() time.Time
	beforeTerminal func(*attemptStore) error
}

func New(config Config) (*Controller, error) {
	if config.Ledger == nil || config.AuthoritySource == nil || config.Provider == nil {
		return nil, errors.New("ledger, authority source, and network-free provider are required")
	}
	limits := productionLimits()
	store, err := newDurableStore(config.StateRoot, limits)
	if err != nil {
		return nil, wrapUnsupported(err)
	}
	return &Controller{ledger: config.Ledger, source: config.AuthoritySource, cancellations: config.CancellationSource, provider: config.Provider,
		store: store, limits: limits, contracts: githublifecycle.DefaultLimits(), now: func() time.Time { return time.Now().UTC() }}, nil
}

func (c *Controller) Close() error {
	if c == nil {
		return nil
	}
	return c.store.close()
}

// Execute admits or recovers exactly one controller-selected merge attempt.
// The request contains only a run identity and cannot inject repository,
// method, actor, policy, cancellation authority, or provider capability.
func (c *Controller) Execute(ctx context.Context, request ExecuteRequest) (Result, error) {
	var zero Result
	if c == nil || c.store == nil || ctx == nil || request.RunID == "" {
		return zero, errors.New("controller, context, and run ID are required")
	}
	invocation, cancel := context.WithTimeout(ctx, c.limits.invocationTimeout)
	defer cancel()
	lease, err := c.ledger.AcquireRunTransition(request.RunID)
	if err != nil {
		return zero, err
	}
	defer lease.Close()
	ledgerBytes, ledgerID, err := c.ledger.Snapshot()
	if err != nil {
		return zero, err
	}
	governed, err := c.source.Resolve(invocation, request.RunID)
	if err != nil {
		return zero, wrap(CodeInvalidAuthority, false, "", err)
	}
	if governed.Phase3Authority.RunID() != request.RunID {
		return zero, wrap(CodeInvalidAuthority, false, "", errors.New("authority source returned a different run"))
	}
	assembled, err := assembleAuthority(governed, ledgerBytes, ledgerID, c.contracts)
	if err != nil {
		return zero, wrap(CodeInvalidAuthority, false, "", err)
	}
	if assembled.ledgerState.current != domain.StateReadyForMerge {
		if terminal, ok := existingTerminal(assembled.ledgerState); ok {
			return c.recoverTerminal(assembled, terminal)
		}
		return zero, wrap(CodeStaleReadyAuthority, false, "", errors.New("READY is not current"))
	}
	writeID := deterministicWriteID(assembled)
	key := attemptKey(assembled, writeID)
	attempt, err := c.store.openAttempt(key)
	if err != nil {
		return zero, wrap(CodeLocalStorageIntegrityFailure, false, writeID, err)
	}
	defer attempt.close()
	return c.executeAttempt(invocation, lease, assembled, attempt, writeID)
}

func (c *Controller) executeAttempt(ctx context.Context, lease *ledger.RunTransitionLease, assembled assembledAuthority, attempt *attemptStore, writeID string) (Result, error) {
	mergeInput, found, err := c.loadAdmission(attempt)
	if err != nil {
		return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, writeID, err)
	}
	if !found {
		if _, err := attempt.reserveCounter("pre-submit"); err != nil {
			return c.failBeforeSubmission(lease, assembled, attempt, writeID, CodeAuthorizationFailed, err)
		}
		initial, err := c.observe(ctx, ObservationInitial, assembled.authority)
		if err != nil {
			return c.failBeforeSubmission(lease, assembled, attempt, writeID, CodeAuthorizationFailed, err)
		}
		recipe, err := githublifecycle.NewMergeCommitRecipeV1(writeID, assembled.authority, c.contracts)
		if err != nil {
			return c.failBeforeSubmission(lease, assembled, attempt, writeID, CodeUnsupportedMergeMethod, err)
		}
		decision := digest(bytes.Join([][]byte{assembled.policy.CanonicalJSON(), initial.PullRequest.CanonicalJSON(),
			initial.CheckRunsClosure.CanonicalJSON(), initial.CommitStatusClosure.CanonicalJSON()}, nil))
		mergeInput, err = githublifecycle.NewMergeInput(githublifecycle.MergeAuthorizationInputV1{
			Authority: assembled.authority, PolicyDecisionSHA256: decision, InitialPullRequest: initial.PullRequest, Checks: initial.Checks,
			CheckRunsClosure: initial.CheckRunsClosure, CommitStatusesClosure: initial.CommitStatusClosure,
			Capability: assembled.governed.ProviderCapability, Recipe: recipe, EvidenceRefs: initial.EvidenceRefs,
		}, writeID, c.contracts)
		if err != nil {
			return c.failBeforeSubmission(lease, assembled, attempt, writeID, CodeAuthorizationFailed, err)
		}
		if err := c.persistAdmission(attempt, assembled, mergeInput); err != nil {
			return c.failBeforeSubmission(lease, assembled, attempt, writeID, CodeLocalStorageIntegrityFailure, err)
		}
	}
	if mergeInput.Attempt().WriteID() != writeID || mergeInput.Authority().ReadyBinding().SHA256() != assembled.ready.SHA256() ||
		authorityDigest(mergeInput.Authority()) != authorityDigest(assembled.authority) {
		return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, writeID, errors.New("durable admission changed controller authority"))
	}
	if err := c.ensureCommitPreparation(ctx, attempt, mergeInput); err != nil {
		return c.failBeforeSubmission(lease, assembled, attempt, writeID, CodeCommitPreparationFailed, err)
	}
	sealed, submission, submitted, barrier, err := c.ensureSealedSubmission(ctx, assembled, attempt, mergeInput)
	if err != nil {
		if barrier.SHA256 != "" {
			if active, found, barrierErr := c.ledger.ActiveTransitionBarrier(assembled.governed.Phase3Authority.RunID()); barrierErr != nil {
				return Result{}, errors.Join(err, barrierErr)
			} else if found && active.SHA256 == barrier.SHA256 {
				return Result{State: domain.StateReadyForMerge, ReasonCode: CodeTargetUnknown, AttemptID: writeID, Unresolved: true},
					wrap(CodeTargetUnknown, true, writeID, err)
			}
		}
		return c.failBeforeSubmission(lease, assembled, attempt, writeID, CodeAuthorizationFailed, err)
	}
	if !submitted {
		if _, err := attempt.reserveCounter("target-submission"); err != nil {
			return c.failBeforeSubmission(lease, assembled, attempt, writeID, CodeAuthorizationFailed, err)
		}
		execution, err := githublifecycle.NewMergeExecutionInputV1(sealed, submission, c.contracts)
		if err != nil {
			return c.failBeforeSubmission(lease, assembled, attempt, writeID, CodeAuthorizationFailed, err)
		}
		callContext, callCancel := context.WithTimeout(ctx, c.limits.providerCallTimeout)
		outcome, callErr := c.provider.SubmitTarget(callContext, execution)
		callCancel()
		if callErr != nil {
			outcome = TargetOutcome{Disposition: githublifecycle.ReconciliationUnknown, RequestBytes: 1,
				EvidenceRefs: []ledger.EvidenceRef{localEvidence(attempt, "target-submission.json", submission.CanonicalJSON(), "target-submission")}}
		}
		if err := c.validateTargetOutcome(sealed, submission, outcome); err != nil {
			outcome = TargetOutcome{Disposition: githublifecycle.ReconciliationUnknown, RequestBytes: max64(1, outcome.RequestBytes),
				EvidenceRefs: []ledger.EvidenceRef{localEvidence(attempt, "target-submission.json", submission.CanonicalJSON(), "target-submission")}}
		}
		if err := c.persistTargetOutcome(attempt, "target-outcome.json", outcome); err != nil {
			return Result{State: domain.StateReadyForMerge, AttemptID: writeID, Unresolved: true}, wrap(CodeTargetUnknown, true, writeID, err)
		}
		return c.settle(ctx, lease, assembled, attempt, sealed, submission, barrier, outcome)
	}
	if mergeResult, postMerge, resultFound, postFound, err := c.recoverAppliedRecords(attempt, sealed); err != nil {
		return Result{}, wrap(CodeLocalStorageIntegrityFailure, true, writeID, err)
	} else if postFound {
		return c.terminalize(lease, assembled, attempt, terminalSelection{sealed: sealed, submission: submission, mergeResult: mergeResult,
			postMerge: postMerge, destination: domain.StateMerged, reason: CodeMergeAppliedAccepted, barrier: &barrier})
	} else if resultFound {
		if _, err := attempt.reserveCounter("post-merge"); err != nil {
			return c.terminalize(lease, assembled, attempt, terminalSelection{sealed: sealed, submission: submission, mergeResult: mergeResult,
				destination: domain.StateFailed, reason: CodePostMergeAcceptanceFailed, barrier: &barrier})
		}
		observeInput, err := githublifecycle.NewObservePostMergeInput(sealed, mergeResult, c.contracts)
		if err != nil {
			return Result{}, err
		}
		callContext, cancel := context.WithTimeout(ctx, c.limits.providerCallTimeout)
		postMerge, err = c.provider.ObservePostMerge(callContext, observeInput)
		cancel()
		if err != nil || githublifecycle.VerifyPostMerge(sealed, mergeResult, postMerge, c.contracts) != nil {
			return c.terminalize(lease, assembled, attempt, terminalSelection{sealed: sealed, submission: submission, mergeResult: mergeResult,
				destination: domain.StateFailed, reason: CodePostMergeAcceptanceFailed, barrier: &barrier})
		}
		if _, _, err := attempt.publish("post-merge.json", postMerge.CanonicalJSON()); err != nil {
			return Result{}, err
		}
		return c.terminalize(lease, assembled, attempt, terminalSelection{sealed: sealed, submission: submission, mergeResult: mergeResult,
			postMerge: postMerge, destination: domain.StateMerged, reason: CodeMergeAppliedAccepted, barrier: &barrier})
	}
	return c.reconcile(ctx, lease, assembled, attempt, sealed, submission, barrier)
}

func (c *Controller) observe(ctx context.Context, phase ObservationPhase, authority githublifecycle.Authority) (AuthorizationObservation, error) {
	callContext, cancel := context.WithTimeout(ctx, c.limits.providerCallTimeout)
	defer cancel()
	observation, err := c.provider.ObserveAuthorization(callContext, phase, authority)
	if err != nil {
		return observation, err
	}
	if len(observation.EvidenceRefs) == 0 || validateEvidence(observation.EvidenceRefs) != nil || observation.StartedUnixNano <= 0 || observation.CompletedUnixNano < observation.StartedUnixNano {
		return observation, errors.New("provider authorization observation is incomplete")
	}
	if err := githublifecycle.ValidateAuthoritativePullRequestSnapshotV1(authority, observation.PullRequest, c.contracts); err != nil {
		return observation, err
	}
	if err := githublifecycle.EvaluateMergePolicyV1(authority, observation.PullRequest, observation.Checks, observation.CheckRunsClosure, observation.CommitStatusClosure, c.contracts); err != nil {
		return observation, err
	}
	return observation, nil
}

func (c *Controller) ensureCommitPreparation(ctx context.Context, attempt *attemptStore, input githublifecycle.MergeInput) error {
	if data, found, err := attempt.read("commit-preparation.json"); err != nil {
		return err
	} else if found {
		var preparation CommitPreparation
		if err := strictCanonical(data, &preparation); err != nil {
			return err
		}
		return preparation.validate(input.Recipe())
	}
	_, markerFound, err := attempt.read("commit-prepare-marker.json")
	if err != nil {
		return err
	}
	marker, _ := json.Marshal(struct {
		Schema       string `json:"schema"`
		Attempt      string `json:"attempt_sha256"`
		RecipeSHA256 string `json:"recipe_sha256"`
	}{"commit-prepare-submitted-v1", input.SHA256(), input.Recipe().SHA256()})
	_, _, err = attempt.publish("commit-prepare-marker.json", marker)
	if err != nil {
		return err
	}
	var preparation CommitPreparation
	if _, existing, err := attempt.read("commit-preparation.json"); err != nil {
		return err
	} else if existing {
		return nil
	}
	if counters, _, _ := attempt.read("commit-prepare-marker.json"); len(counters) == 0 {
		return errors.New("commit preparation marker is not durable")
	}
	if markerFound {
		if _, reserveErr := attempt.reserveCounter("reconciliation-call"); reserveErr != nil {
			return reserveErr
		}
		callContext, cancel := context.WithTimeout(ctx, c.limits.providerCallTimeout)
		preparation, err = c.provider.ReconcileResultCommit(callContext, input.Recipe())
		cancel()
	} else {
		if _, err := attempt.reserveCounter("commit-submission"); err != nil {
			return err
		}
		callContext, cancel := context.WithTimeout(ctx, c.limits.providerCallTimeout)
		preparation, err = c.provider.PrepareResultCommit(callContext, input.Recipe())
		cancel()
		if err != nil {
			// The marker removes retry authority. Reconciliation is read-only.
			if _, reserveErr := attempt.reserveCounter("reconciliation-call"); reserveErr != nil {
				return errors.Join(err, reserveErr)
			}
			callContext, cancel = context.WithTimeout(ctx, c.limits.providerCallTimeout)
			preparation, err = c.provider.ReconcileResultCommit(callContext, input.Recipe())
			cancel()
		}
	}
	if err != nil || preparation.validate(input.Recipe()) != nil {
		return errors.Join(errors.New("exact result commit preparation is unresolved"), err, preparation.validate(input.Recipe()))
	}
	data, _ := json.Marshal(preparation)
	_, _, err = attempt.publish("commit-preparation.json", data)
	return err
}

func (c *Controller) ensureSealedSubmission(ctx context.Context, assembled assembledAuthority, attempt *attemptStore, input githublifecycle.MergeInput) (githublifecycle.SealedMergeAuthorizationV1, githublifecycle.TargetSubmissionV1, bool, ledger.TransitionBarrier, error) {
	var sealed githublifecycle.SealedMergeAuthorizationV1
	var submission githublifecycle.TargetSubmissionV1
	sealData, sealFound, err := attempt.read("authorization-seal.json")
	if err != nil {
		return sealed, submission, false, ledger.TransitionBarrier{}, err
	}
	var seal githublifecycle.AuthorizationSealV1
	if sealFound {
		seal, err = githublifecycle.ParseCanonicalAuthorizationSealV1(sealData, input, c.contracts)
		if err != nil {
			return sealed, submission, false, ledger.TransitionBarrier{}, err
		}
	} else {
		ledgerBytes, _, err := c.ledger.Snapshot()
		if err != nil {
			return sealed, submission, false, ledger.TransitionBarrier{}, err
		}
		now := c.now().UnixNano()
		proof, err := currentReadyProof(assembled, ledgerBytes, now, 1, c.contracts)
		if err != nil {
			return sealed, submission, false, ledger.TransitionBarrier{}, err
		}
		if _, err := attempt.reserveCounter("pre-submit"); err != nil {
			return sealed, submission, false, ledger.TransitionBarrier{}, err
		}
		final, err := c.observe(ctx, ObservationFinal, assembled.authority)
		if err != nil {
			return sealed, submission, false, ledger.TransitionBarrier{}, err
		}
		counters := authorizationCounters(input, final, ledgerBytes)
		if final.StartedUnixNano <= now {
			return sealed, submission, false, ledger.TransitionBarrier{}, errors.New("final observation did not occur after current READY proof")
		}
		finalEvidence := append([]ledger.EvidenceRef(nil), final.EvidenceRefs...)
		finalEvidence = append(finalEvidence, proof.Input().EvidenceRefs...)
		validation, err := githublifecycle.NewFinalRevalidationV1(githublifecycle.FinalRevalidationV1Input{
			MergeInput: input, ControllerSequence: 2, StartedUnixNano: final.StartedUnixNano, CompletedUnixNano: final.CompletedUnixNano,
			CurrentReadyProof: proof, PullRequest: final.PullRequest, Checks: final.Checks, CheckRunsClosure: final.CheckRunsClosure,
			CommitStatusesClosure: final.CommitStatusClosure, Capability: assembled.governed.ProviderCapability, Recipe: input.Recipe(),
			Counters: counters, NoTargetRequestAttempted: true, EvidenceRefs: finalEvidence,
		}, c.contracts)
		if err != nil {
			return sealed, submission, false, ledger.TransitionBarrier{}, err
		}
		if _, _, err := attempt.publish("final-revalidation.json", validation.CanonicalJSON()); err != nil {
			return sealed, submission, false, ledger.TransitionBarrier{}, err
		}
		seal, err = githublifecycle.NewAuthorizationSealV1(githublifecycle.AuthorizationSealV1Input{MergeInput: input, FinalRevalidation: validation}, c.contracts)
		if err != nil {
			return sealed, submission, false, ledger.TransitionBarrier{}, err
		}
		if _, _, err := attempt.publish("authorization-seal.json", seal.CanonicalJSON()); err != nil {
			return sealed, submission, false, ledger.TransitionBarrier{}, err
		}
	}
	commitment, err := githublifecycle.NewTargetRefCommitmentV1(input, seal, c.contracts)
	if err != nil {
		return sealed, submission, false, ledger.TransitionBarrier{}, err
	}
	if _, _, err := attempt.publish("target-commitment.json", commitment.CanonicalJSON()); err != nil {
		return sealed, submission, false, ledger.TransitionBarrier{}, err
	}
	sealed, err = githublifecycle.NewSealedMergeAuthorizationV1(githublifecycle.SealedMergeAuthorizationV1Input{MergeInput: input, Seal: seal, Commitment: commitment}, c.contracts)
	if err != nil {
		return sealed, submission, false, ledger.TransitionBarrier{}, err
	}
	if _, _, err := attempt.publish("sealed-authorization.json", sealed.CanonicalJSON()); err != nil {
		return sealed, submission, false, ledger.TransitionBarrier{}, err
	}
	barrier, err := ledger.NewTransitionBarrier(assembled.governed.Phase3Authority.RunID(), input.Attempt().WriteID(), commitment.SHA256(), time.Unix(0, seal.Input().FinalRevalidation.Input().CompletedUnixNano).UTC())
	if err != nil {
		return sealed, submission, false, barrier, err
	}
	if err := c.ledger.InstallTransitionBarrier(barrier); err != nil {
		return sealed, submission, false, barrier, err
	}
	submissionData, found, err := attempt.read("target-submission.json")
	if err != nil {
		return sealed, submission, false, barrier, err
	}
	if found {
		submission, err = githublifecycle.ParseCanonicalTargetSubmissionV1(submissionData, sealed, c.contracts)
		return sealed, submission, true, barrier, err
	}
	submission, err = githublifecycle.NewTargetSubmissionV1(input.Attempt().WriteID(), sealed, c.contracts)
	if err != nil {
		return sealed, submission, false, barrier, err
	}
	_, _, err = attempt.publish("target-submission.json", submission.CanonicalJSON())
	return sealed, submission, false, barrier, err
}

func (c *Controller) reconcile(ctx context.Context, lease *ledger.RunTransitionLease, assembled assembledAuthority, attempt *attemptStore, sealed githublifecycle.SealedMergeAuthorizationV1, submission githublifecycle.TargetSubmissionV1, barrier ledger.TransitionBarrier) (Result, error) {
	if _, err := attempt.reserveCounter("reconciliation-round"); err != nil {
		return Result{State: domain.StateReadyForMerge, AttemptID: submission.InvocationID(), Unresolved: true}, wrap(CodeTargetUnknown, true, submission.InvocationID(), err)
	}
	if _, err := attempt.reserveCounter("reconciliation-call"); err != nil {
		return Result{State: domain.StateReadyForMerge, AttemptID: submission.InvocationID(), Unresolved: true}, wrap(CodeTargetUnknown, true, submission.InvocationID(), err)
	}
	evidence := []ledger.EvidenceRef{localEvidence(attempt, "target-submission.json", submission.CanonicalJSON(), "target-submission")}
	input, err := githublifecycle.NewMergeReconcileWriteInput(sealed, submission, evidence, c.contracts)
	if err != nil {
		return Result{}, err
	}
	callContext, cancel := context.WithTimeout(ctx, c.limits.providerCallTimeout)
	outcome, err := c.provider.ReconcileTarget(callContext, input)
	cancel()
	if err != nil {
		outcome = TargetOutcome{Disposition: githublifecycle.ReconciliationUnknown, RequestBytes: 1, EvidenceRefs: evidence}
	}
	if err := c.validateTargetOutcome(sealed, submission, outcome); err != nil {
		outcome = TargetOutcome{Disposition: githublifecycle.ReconciliationUnknown, RequestBytes: max64(1, outcome.RequestBytes), EvidenceRefs: evidence}
	}
	reconciliation, err := githublifecycle.NewMergeReconciliationResult(sealed, submission, outcome.Disposition, resultPointer(outcome), proofPointer(outcome), outcome.EvidenceRefs, c.contracts)
	if err != nil {
		return Result{}, err
	}
	if err := githublifecycle.ValidateReconciliationResult(sealed, submission, reconciliation, c.contracts); err != nil {
		return Result{}, err
	}
	outcomeData := canonicalTargetOutcome(outcome)
	reconciliationData, _ := json.Marshal(reconciliationRecordV1{
		Schema:           "merge-reconciliation-result-v1",
		SealSHA256:       sealed.SHA256(),
		SubmissionSHA256: submission.SHA256(),
		LimitsSHA256:     ProductionLimitsSHA256(),
		Outcome:          outcomeData,
		OutcomeSHA256:    digest(outcomeData),
	})
	if _, err := c.store.appendChannel("reconciliations", "merge-reconciliation-result", digest(reconciliationData), reconciliationData, MaxTerminalRecordBytes); err != nil {
		return Result{}, err
	}
	if outcome.Disposition != githublifecycle.ReconciliationUnknown {
		if err := c.persistTargetOutcome(attempt, "target-reconciliation.json", outcome); err != nil {
			return Result{}, err
		}
	}
	return c.settle(ctx, lease, assembled, attempt, sealed, submission, barrier, outcome)
}

func (c *Controller) settle(ctx context.Context, lease *ledger.RunTransitionLease, assembled assembledAuthority, attempt *attemptStore, sealed githublifecycle.SealedMergeAuthorizationV1, submission githublifecycle.TargetSubmissionV1, barrier ledger.TransitionBarrier, outcome TargetOutcome) (Result, error) {
	switch outcome.Disposition {
	case githublifecycle.ReconciliationUnknown:
		return Result{State: domain.StateReadyForMerge, ReasonCode: CodeTargetUnknown, AttemptID: sealed.MergeInput().Attempt().WriteID(), Unresolved: true}, wrap(CodeTargetUnknown, true, sealed.MergeInput().Attempt().WriteID(), errors.New("target submission remains ambiguous"))
	case githublifecycle.ReconciliationNotApplied:
		if data, found, err := attempt.read("pending-cancellation.json"); err != nil {
			return Result{}, err
		} else if found {
			durable, err := githublifecycle.ParseCanonicalDurableCancellationAuthorityV1(data, c.contracts)
			if err != nil {
				return Result{}, err
			}
			expected := cancellationExpectation(durable.Authority(), sealed)
			if err := githublifecycle.AuthorizeCancelledV1(durable, expected, githublifecycle.ReconciliationNotApplied, c.contracts, outcome.NotAppliedProof); err != nil {
				return Result{}, err
			}
			return c.terminalize(lease, assembled, attempt, terminalSelection{sealed: sealed, submission: submission,
				notApplied: outcome.NotAppliedProof, cancellation: durable, destination: domain.StateCancelled,
				reason: CodeCancelledAfterNotApplied, barrier: &barrier})
		}
		return c.terminalize(lease, assembled, attempt, terminalSelection{sealed: sealed, submission: submission, notApplied: outcome.NotAppliedProof,
			destination: domain.StateFailed, reason: CodeTargetNotApplied, barrier: &barrier})
	case githublifecycle.ReconciliationApplied:
		result := outcome.Result
		if err := githublifecycle.ValidateMergeResult(sealed, result, c.contracts); err != nil {
			return Result{}, err
		}
		if _, _, err := attempt.publish("merge-result.json", result.CanonicalJSON()); err != nil {
			return Result{}, err
		}
		if _, err := attempt.reserveCounter("post-merge"); err != nil {
			return c.terminalize(lease, assembled, attempt, terminalSelection{sealed: sealed, submission: submission, mergeResult: result,
				destination: domain.StateFailed, reason: CodePostMergeAcceptanceFailed, barrier: &barrier})
		}
		observeInput, err := githublifecycle.NewObservePostMergeInput(sealed, result, c.contracts)
		if err != nil {
			return Result{}, err
		}
		callContext, cancel := context.WithTimeout(ctx, c.limits.providerCallTimeout)
		post, err := c.provider.ObservePostMerge(callContext, observeInput)
		cancel()
		if err != nil || githublifecycle.VerifyPostMerge(sealed, result, post, c.contracts) != nil {
			return c.terminalize(lease, assembled, attempt, terminalSelection{sealed: sealed, submission: submission, mergeResult: result,
				destination: domain.StateFailed, reason: CodePostMergeAcceptanceFailed, barrier: &barrier})
		}
		if _, _, err := attempt.publish("post-merge.json", post.CanonicalJSON()); err != nil {
			return Result{}, err
		}
		// MERGED selection immediately follows durable proof. Cleanup is strictly
		// after the terminal core and exact ledger event.
		return c.terminalize(lease, assembled, attempt, terminalSelection{sealed: sealed, submission: submission, mergeResult: result,
			postMerge: post, destination: domain.StateMerged, reason: CodeMergeAppliedAccepted, barrier: &barrier})
	default:
		return Result{}, errors.New("provider returned an unsupported target disposition")
	}
}

func (c *Controller) validateTargetOutcome(sealed githublifecycle.SealedMergeAuthorizationV1, submission githublifecycle.TargetSubmissionV1, outcome TargetOutcome) error {
	if outcome.RequestBytes < 0 || outcome.RequestBytes > c.limits.cumulativeRequestBytes ||
		len(outcome.EvidenceRefs) > c.contracts.MaxEvidenceRefs || validateEvidence(outcome.EvidenceRefs) != nil {
		return errors.New("target outcome evidence is invalid")
	}
	switch outcome.Disposition {
	case githublifecycle.ReconciliationApplied:
		if outcome.NotAppliedProof.SHA256() != "" {
			return errors.New("APPLIED outcome contains NOT_APPLIED proof")
		}
		return githublifecycle.ValidateMergeResult(sealed, outcome.Result, c.contracts)
	case githublifecycle.ReconciliationNotApplied:
		if outcome.Result.SHA256() != "" {
			return errors.New("NOT_APPLIED outcome contains a merge result")
		}
		return githublifecycle.ValidateNotAppliedProofV1(sealed, submission, outcome.NotAppliedProof, c.contracts)
	case githublifecycle.ReconciliationUnknown:
		if outcome.Result.SHA256() != "" || outcome.NotAppliedProof.SHA256() != "" {
			return errors.New("UNKNOWN outcome claims a result or non-application proof")
		}
		return nil
	default:
		return errors.New("target outcome disposition is invalid")
	}
}

func (c *Controller) persistTargetOutcome(attempt *attemptStore, name string, outcome TargetOutcome) error {
	data := canonicalTargetOutcome(outcome)
	_, _, err := attempt.publish(name, data)
	return err
}

func canonicalTargetOutcome(outcome TargetOutcome) []byte {
	record := targetOutcomeRecordV1{Schema: "merge-target-outcome-v1", Disposition: outcome.Disposition, RequestBytes: outcome.RequestBytes, EvidenceRefs: outcome.EvidenceRefs}
	if outcome.Result.SHA256() != "" {
		record.Result, record.ResultSHA256 = outcome.Result.CanonicalJSON(), outcome.Result.SHA256()
	}
	if outcome.NotAppliedProof.SHA256() != "" {
		record.NotApplied, record.NotAppliedSHA = outcome.NotAppliedProof.CanonicalJSON(), outcome.NotAppliedProof.SHA256()
	}
	data, _ := json.Marshal(record)
	return data
}

func (c *Controller) loadAdmission(attempt *attemptStore) (githublifecycle.MergeInput, bool, error) {
	data, found, err := attempt.read("admission.json")
	if err != nil || !found {
		return githublifecycle.MergeInput{}, false, err
	}
	var record admissionRecordV1
	if err := strictCanonical(data, &record); err != nil || record.Schema != "merge-admission-v1" || record.LimitsSHA256 != ProductionLimitsSHA256() || digest(record.MergeInput) != record.MergeInputSHA256 {
		return githublifecycle.MergeInput{}, false, errors.Join(errors.New("durable admission record is invalid"), err)
	}
	input, err := githublifecycle.ParseCanonicalMergeInput(record.MergeInput, c.contracts)
	if err != nil || input.SHA256() != record.MergeInputSHA256 || input.Attempt().WriteID() != record.WriteID {
		return githublifecycle.MergeInput{}, false, errors.Join(errors.New("durable merge input fails strict recovery"), err)
	}
	return input, true, nil
}

func (c *Controller) persistAdmission(attempt *attemptStore, assembled assembledAuthority, input githublifecycle.MergeInput) error {
	record := admissionRecordV1{"merge-admission-v1", assembled.governed.Phase3Authority.RunID(), assembled.governed.ProjectID, assembled.governed.PlanID,
		assembled.governed.AttemptID, input.Attempt().WriteID(), input.CanonicalPayload(), input.SHA256(), ProductionLimitsSHA256()}
	data, _ := json.Marshal(record)
	_, _, err := attempt.publish("admission.json", data)
	return err
}

func (c *Controller) failBeforeSubmission(lease *ledger.RunTransitionLease, assembled assembledAuthority, attempt *attemptStore, writeID, reason string, cause error) (Result, error) {
	result, terminalErr := c.terminalize(lease, assembled, attempt, terminalSelection{destination: domain.StateFailed, reason: reason, writeID: writeID})
	return result, errors.Join(wrap(reason, false, writeID, cause), terminalErr)
}

func authorizationCounters(input githublifecycle.MergeInput, final AuthorizationObservation, ledgerBytes []byte) githublifecycle.AuthorizationCountersV1 {
	initial := input.InitialPullRequest()
	admissionPages, admissionItems, admissionBytes := paginationTotals(initial.Input().ReviewsClosure, inputClosure(input, true), inputClosure(input, false))
	finalPages, finalItems, finalBytes := paginationTotals(final.PullRequest.Input().ReviewsClosure, final.CheckRunsClosure, final.CommitStatusClosure)
	admissionCalls := admissionPages + 1
	finalCalls := finalPages + 1
	duration := final.CompletedUnixNano - final.StartedUnixNano
	return githublifecycle.AuthorizationCountersV1{
		AdmissionHTTPCalls: admissionCalls, AdmissionObservedChecks: len(inputChecks(input)), AdmissionObservedReviews: len(initial.Input().Reviews),
		AdmissionPaginationSources: 3, AdmissionPaginationPages: admissionPages, AdmissionPaginationItems: admissionItems, AdmissionPaginationClosureBytes: admissionBytes,
		FinalRevalidationHTTPCalls: finalCalls, FinalObservedChecks: len(final.Checks), FinalObservedReviews: len(final.PullRequest.Input().Reviews),
		FinalPaginationSources: 3, FinalPaginationPages: finalPages, FinalPaginationItems: finalItems, FinalPaginationClosureBytes: finalBytes,
		ReadyLedgerBytes: int64(len(ledgerBytes)), ReadyLedgerRecords: bytes.Count(ledgerBytes, []byte{'\n'}), PreSubmitHTTPCalls: admissionCalls + finalCalls,
		CommitObjectCreationSubmissions: 1, TotalHTTPCalls: admissionCalls + finalCalls + 1, ControllerInvocationNanos: duration,
	}
}

// MergeInput intentionally exposes closures/checks only through its canonical
// record. Strictly decode the small selected fields for counter construction;
// the contract revalidates the resulting exact counts against its internals.
func inputProjection(input githublifecycle.MergeInput) struct {
	Checks    []json.RawMessage `json:"checks"`
	CheckRuns struct {
		InlineBytes []byte `json:"inline_bytes"`
	} `json:"check_runs_closure"`
	CommitStatuses struct {
		InlineBytes []byte `json:"inline_bytes"`
	} `json:"commit_statuses_closure"`
} {
	var wire struct {
		Checks struct {
			InlineBytes []byte `json:"inline_bytes"`
		} `json:"checks"`
		CheckRuns struct {
			InlineBytes []byte `json:"inline_bytes"`
		} `json:"check_runs_closure"`
		CommitStatuses struct {
			InlineBytes []byte `json:"inline_bytes"`
		} `json:"commit_statuses_closure"`
	}
	_ = json.Unmarshal(input.CanonicalPayload(), &wire)
	var checks []json.RawMessage
	_ = json.Unmarshal(wire.Checks.InlineBytes, &checks)
	return struct {
		Checks    []json.RawMessage `json:"checks"`
		CheckRuns struct {
			InlineBytes []byte `json:"inline_bytes"`
		} `json:"check_runs_closure"`
		CommitStatuses struct {
			InlineBytes []byte `json:"inline_bytes"`
		} `json:"commit_statuses_closure"`
	}{checks, wire.CheckRuns, wire.CommitStatuses}
}

func inputChecks(input githublifecycle.MergeInput) []json.RawMessage {
	return inputProjection(input).Checks
}
func inputClosure(input githublifecycle.MergeInput, checkRuns bool) githublifecycle.PaginationClosureV1 {
	projection := inputProjection(input)
	data := projection.CommitStatuses.InlineBytes
	if checkRuns {
		data = projection.CheckRuns.InlineBytes
	}
	closure, _ := githublifecycle.ParseCanonicalPaginationClosureV1(data, githublifecycle.DefaultLimits())
	return closure
}

func paginationTotals(closures ...githublifecycle.PaginationClosureV1) (int, int, int64) {
	var pages, items int
	var bytesTotal int64
	for _, closure := range closures {
		input := closure.Input()
		pages += len(input.Pages)
		for _, page := range input.Pages {
			items += len(page.Input().Items)
		}
		bytesTotal += int64(len(closure.CanonicalJSON()))
	}
	return pages, items, bytesTotal
}

func localEvidence(attempt *attemptStore, name string, data []byte, kind string) ledger.EvidenceRef {
	return ledger.EvidenceRef{URI: attempt.root + "/records/" + name, SHA256: digest(data), Kind: kind}
}

func resultPointer(outcome TargetOutcome) *githublifecycle.MergeResult {
	if outcome.Disposition != githublifecycle.ReconciliationApplied {
		return nil
	}
	return &outcome.Result
}
func proofPointer(outcome TargetOutcome) *githublifecycle.NotAppliedProofV1 {
	if outcome.Disposition != githublifecycle.ReconciliationNotApplied {
		return nil
	}
	return &outcome.NotAppliedProof
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func existingTerminal(state readyLedgerState) (Result, bool) {
	if state.current != domain.StateMerged && state.current != domain.StateFailed && state.current != domain.StateCancelled {
		return Result{}, false
	}
	reason, terminal := "", ""
	if state.currentEvent.Payload != nil {
		reason, _ = state.currentEvent.Payload["reason_code"].(string)
		terminal, _ = state.currentEvent.Payload["terminal_core_sha256"].(string)
	}
	return Result{State: state.current, ReasonCode: reason, AttemptID: state.currentEvent.AttemptID, TerminalSHA256: terminal}, true
}

func wrapUnsupported(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errUnsupportedDurability) {
		return wrap(CodeUnsupportedPlatform, false, "", err)
	}
	return err
}

var _ = fmt.Sprintf

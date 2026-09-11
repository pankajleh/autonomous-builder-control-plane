package mergelifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

// Cancel authenticates one controller-resolved source request and either
// selects CANCELLED at a proven safe boundary or durably records precedence
// while an earlier target submission remains UNKNOWN.
func (c *Controller) Cancel(ctx context.Context, request CancelRequest) (Result, error) {
	if c == nil || c.store == nil || ctx == nil || request.RunID == "" || request.SourceRequestID == "" {
		return Result{}, errors.New("controller, context, run ID, and source request ID are required")
	}
	if c.cancellations == nil {
		return Result{}, wrap(CodeAuthorizationFailed, false, "", errors.New("controller cancellation source is not configured"))
	}
	invocation, cancel := context.WithTimeout(ctx, c.limits.invocationTimeout)
	defer cancel()
	lease, err := c.ledger.AcquireRunTransition(request.RunID)
	if err != nil {
		return Result{}, err
	}
	defer lease.Close()
	ledgerBytes, ledgerID, err := c.ledger.Snapshot()
	if err != nil {
		return Result{}, err
	}
	governed, err := c.source.Resolve(invocation, request.RunID)
	if err != nil {
		return Result{}, wrap(CodeInvalidAuthority, false, "", err)
	}
	assembled, err := assembleAuthority(governed, ledgerBytes, ledgerID, c.contracts)
	if err != nil {
		return Result{}, wrap(CodeInvalidAuthority, false, "", err)
	}
	if assembled.ledgerState.current != domain.StateReadyForMerge {
		return Result{}, wrap(CodeStaleReadyAuthority, false, "", errors.New("cancellation requires current READY"))
	}
	grant, err := c.cancellations.ResolveCancellation(invocation, request.RunID, request.SourceRequestID)
	if err != nil {
		return Result{}, wrap(CodeAuthorizationFailed, false, "", err)
	}
	writeID := deterministicWriteID(assembled)
	attempt, err := c.store.openAttempt(attemptKey(assembled, writeID))
	if err != nil {
		return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, writeID, err)
	}
	defer attempt.close()
	if _, presented, err := attempt.read("pending-cancellation.json"); err != nil {
		return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, writeID, err)
	} else if presented {
		return Result{}, wrap(CodeAuthorizationFailed, false, writeID, errors.New("cancellation source request was already presented"))
	}
	_, draftFound, err := attempt.read("cancellation-authority.json")
	if err != nil {
		return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, writeID, err)
	}
	if _, replayed, err := c.store.readChannel("cancellation-replay", cancellationReplayKey(grant.SourceKind, request.SourceRequestID), MaxTerminalRecordBytes); err != nil {
		return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, writeID, err)
	} else if replayed && !draftFound {
		return Result{}, wrap(CodeAuthorizationFailed, false, writeID, errors.New("cancellation source request is globally single-use"))
	}

	authority, expectation, sealed, notApplied, barrier, pending, err := c.buildCancellation(
		assembled, attempt, request.SourceRequestID, grant, ledgerBytes, writeID,
	)
	if err != nil {
		return Result{}, wrap(CodeAuthorizationFailed, false, writeID, err)
	}
	channelEvidence, err := c.store.appendChannel("cancellation-authorities", "merge-cancellation-authority", authority.SHA256(), authority.CanonicalJSON(), MaxTerminalRecordBytes)
	if err != nil {
		return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, writeID, err)
	}
	replay, err := githublifecycle.NewCancellationReplayIdentityV1(authority, channelEvidence, c.contracts)
	if err != nil {
		return Result{}, wrap(CodeAuthorizationFailed, false, writeID, err)
	}
	if _, err := c.store.appendChannel("cancellation-replay", "merge-cancellation-replay", cancellationReplayKey(grant.SourceKind, request.SourceRequestID), replay.CanonicalJSON(), MaxTerminalRecordBytes); err != nil {
		return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, writeID, err)
	}
	durable, err := githublifecycle.NewDurableCancellationAuthorityV1(authority, channelEvidence, replay, c.contracts)
	if err != nil {
		return Result{}, wrap(CodeAuthorizationFailed, false, writeID, err)
	}
	if _, err := c.store.appendChannel("durable-cancellations", "merge-durable-cancellation", durable.SHA256(), durable.CanonicalJSON(), MaxTerminalRecordBytes); err != nil {
		return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, writeID, err)
	}
	if pending {
		if _, _, err := attempt.publish("pending-cancellation.json", durable.CanonicalJSON()); err != nil {
			return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, writeID, err)
		}
		return Result{State: domain.StateReadyForMerge, ReasonCode: CodeTargetUnknown, AttemptID: writeID, Unresolved: true}, nil
	}
	proofs := []githublifecycle.NotAppliedProofV1(nil)
	if notApplied.SHA256() != "" {
		proofs = append(proofs, notApplied)
	}
	if err := githublifecycle.AuthorizeCancelledV1(durable, expectation, githublifecycle.ReconciliationNotApplied, c.contracts, proofs...); err != nil {
		return Result{}, wrap(CodeAuthorizationFailed, false, writeID, err)
	}
	reason := CodeCancelledBeforeSubmission
	if notApplied.SHA256() != "" && notApplied.Input().RequestBytes > 0 {
		reason = CodeCancelledAfterNotApplied
	}
	selection := terminalSelection{sealed: sealed, cancellation: durable, notApplied: notApplied, destination: domain.StateCancelled,
		reason: reason, writeID: writeID, selectedUnixNano: authority.Input().ReceiptUnixNano}
	if barrier.SHA256 != "" {
		selection.barrier = &barrier
	}
	return c.terminalize(lease, assembled, attempt, selection)
}

func (c *Controller) buildCancellation(assembled assembledAuthority, attempt *attemptStore, sourceRequestID string, grant CancellationGrant, ledgerBytes []byte, writeID string) (
	githublifecycle.CancellationAuthorityV1, githublifecycle.CancellationAuthorityExpectationV1,
	githublifecycle.SealedMergeAuthorizationV1, githublifecycle.NotAppliedProofV1, ledger.TransitionBarrier, bool, error,
) {
	var authority githublifecycle.CancellationAuthorityV1
	var expectation githublifecycle.CancellationAuthorityExpectationV1
	var sealed githublifecycle.SealedMergeAuthorizationV1
	var notApplied githublifecycle.NotAppliedProofV1
	var barrier ledger.TransitionBarrier

	// An immutable draft makes a crash after request acceptance replay the exact
	// receipt/proof identity instead of minting a second authority.
	if data, found, err := attempt.read("cancellation-authority.json"); err != nil {
		return authority, expectation, sealed, notApplied, barrier, false, err
	} else if found {
		authority, err = githublifecycle.ParseCanonicalCancellationAuthorityV1(data, c.contracts)
		if err != nil {
			return authority, expectation, sealed, notApplied, barrier, false, err
		}
		sealed, notApplied, barrier, err = c.cancellationSubmittedState(attempt, authority.Input().Boundary)
		if err != nil {
			return authority, expectation, sealed, notApplied, barrier, false, err
		}
		if notApplied.SHA256() == "" {
			if bound := authority.Input().SubmissionProof.Input().NotAppliedProof; bound != nil {
				notApplied = *bound
			}
		}
		expectation = cancellationExpectation(authority, sealed)
		if err := validateGrant(authority, grant, sourceRequestID); err != nil {
			return authority, expectation, sealed, notApplied, barrier, false, err
		}
		return authority, expectation, sealed, notApplied, barrier, authority.Input().Boundary == githublifecycle.CancellationTargetSubmissionUnknown, nil
	}

	proof, sealedValue, notAppliedValue, barrierValue, boundary, pending, err := c.cancellationBoundary(attempt)
	if err != nil {
		return authority, expectation, sealed, notApplied, barrier, false, err
	}
	sealed, notApplied, barrier = sealedValue, notAppliedValue, barrierValue
	observed := c.now().UnixNano()
	readyProof, err := currentReadyProof(assembled, ledgerBytes, observed, 3, c.contracts)
	if err != nil {
		return authority, expectation, sealed, notApplied, barrier, false, err
	}
	receipt := c.now().UnixNano()
	if receipt <= observed {
		receipt = observed + 1
	}
	input := assembled.ready.Input()
	evidence := append([]ledger.EvidenceRef(nil), grant.EvidenceRefs...)
	evidence = append(evidence, readyProof.Input().EvidenceRefs...)
	evidence = append(evidence, proof.Input().EvidenceRef)
	authority, err = githublifecycle.NewCancellationAuthorityV1(githublifecycle.CancellationAuthorityV1Input{
		ProjectID: assembled.governed.ProjectID, PlanID: assembled.governed.PlanID, RunID: assembled.governed.Phase3Authority.RunID(),
		RepositoryBindingSHA256: assembled.repository.SHA256(), Phase3AuthoritySHA256: input.Phase3AuthoritySHA256,
		ReadyEventSHA256: input.ReadyEventSHA256, ReadyEventID: input.ReadyEventID, ReadyRunStateSequence: input.ReadyRunStateSequence,
		ReadyBindingSHA256: assembled.ready.SHA256(), LedgerPrefixSHA256: input.LedgerPrefixSHA256, LedgerPrefixLength: input.LedgerPrefixLength,
		CurrentReadyProof: readyProof, Boundary: boundary, ReceiptUnixNano: receipt, IngressSequence: 1,
		AdmissionSHA256: proof.Input().AdmissionSHA256, Attempt: proof.Input().Attempt, SealSHA256: proof.Input().SealSHA256,
		CommitmentSHA256: proof.Input().CommitmentSHA256, SubmissionProof: proof, Requester: grant.Requester,
		AuthenticationEvidence: grant.AuthenticationEvidence, CancellationPolicyVersion: grant.CancellationPolicyVersion,
		CancellationPolicySource: grant.CancellationPolicySource, CancellationPolicySHA256: grant.CancellationPolicySHA256,
		ScopedGrantSHA256: grant.ScopedGrantSHA256, AllowDecisionSHA256: grant.AllowDecisionSHA256,
		SourceRequestID: sourceRequestID, SourceKind: grant.SourceKind, RequestEvidence: grant.RequestEvidence,
		IngressID: grant.IngressID, EvidenceRefs: evidence,
	}, c.contracts)
	if err != nil {
		return authority, expectation, sealed, notApplied, barrier, false, err
	}
	if _, _, err := attempt.publish("cancellation-authority.json", authority.CanonicalJSON()); err != nil {
		return authority, expectation, sealed, notApplied, barrier, false, err
	}
	expectation = cancellationExpectation(authority, sealed)
	if err := githublifecycle.ValidateCancellationAuthorityV1(authority, expectation, c.contracts); err != nil {
		return authority, expectation, sealed, notApplied, barrier, false, err
	}
	return authority, expectation, sealed, notApplied, barrier, pending, nil
}

func (c *Controller) cancellationBoundary(attempt *attemptStore) (githublifecycle.CancellationSubmissionProofV1,
	githublifecycle.SealedMergeAuthorizationV1, githublifecycle.NotAppliedProofV1, ledger.TransitionBarrier,
	githublifecycle.CancellationBoundaryV1, bool, error,
) {
	var sealed githublifecycle.SealedMergeAuthorizationV1
	var notApplied githublifecycle.NotAppliedProofV1
	var barrier ledger.TransitionBarrier
	mergeInput, admitted, err := c.loadAdmission(attempt)
	if err != nil {
		return githublifecycle.CancellationSubmissionProofV1{}, sealed, notApplied, barrier, "", false, err
	}
	if !admitted {
		ref, err := c.cancellationBoundaryEvidence(attempt, "no-admission", githublifecycle.ReconciliationNotApplied, 0, "cancellation-no-admission")
		if err != nil {
			return githublifecycle.CancellationSubmissionProofV1{}, sealed, notApplied, barrier, "", false, err
		}
		proof, err := githublifecycle.NewCancellationSubmissionProofV1(githublifecycle.CancellationSubmissionProofV1Input{
			Kind: githublifecycle.CancellationProofNoAdmission, SubmissionState: githublifecycle.ReconciliationNotApplied, EvidenceRef: ref,
		}, c.contracts)
		return proof, sealed, notApplied, barrier, githublifecycle.CancellationPreAdmission, false, err
	}
	sealedData, sealedFound, err := attempt.read("sealed-authorization.json")
	if err != nil {
		return githublifecycle.CancellationSubmissionProofV1{}, sealed, notApplied, barrier, "", false, err
	}
	if !sealedFound {
		ref, err := c.cancellationBoundaryEvidence(attempt, "admitted-zero-request", githublifecycle.ReconciliationNotApplied, 0, "cancellation-zero-request")
		if err != nil {
			return githublifecycle.CancellationSubmissionProofV1{}, sealed, notApplied, barrier, "", false, err
		}
		value := mergeInput.Attempt()
		proof, err := githublifecycle.NewCancellationSubmissionProofV1(githublifecycle.CancellationSubmissionProofV1Input{
			Kind: githublifecycle.CancellationProofZeroRequestBytes, AdmissionSHA256: mergeInput.SHA256(), Attempt: &value,
			SubmissionState: githublifecycle.ReconciliationNotApplied, EvidenceRef: ref,
		}, c.contracts)
		return proof, sealed, notApplied, barrier, githublifecycle.CancellationAdmittedPreTargetSubmission, false, err
	}
	sealed, err = githublifecycle.ParseCanonicalSealedMergeAuthorizationV1(sealedData, c.contracts)
	if err != nil || sealed.MergeInput().SHA256() != mergeInput.SHA256() {
		return githublifecycle.CancellationSubmissionProofV1{}, sealed, notApplied, barrier, "", false, errors.Join(errors.New("sealed cancellation admission changed"), err)
	}
	submissionData, submissionFound, err := attempt.read("target-submission.json")
	if err != nil {
		return githublifecycle.CancellationSubmissionProofV1{}, sealed, notApplied, barrier, "", false, err
	}
	var submission githublifecycle.TargetSubmissionV1
	if submissionFound {
		submission, err = githublifecycle.ParseCanonicalTargetSubmissionV1(submissionData, sealed, c.contracts)
	} else {
		submission, err = githublifecycle.NewTargetSubmissionV1(mergeInput.Attempt().WriteID(), sealed, c.contracts)
		if err == nil {
			_, _, err = attempt.publish("target-submission.json", submission.CanonicalJSON())
		}
	}
	if err != nil {
		return githublifecycle.CancellationSubmissionProofV1{}, sealed, notApplied, barrier, "", false, err
	}
	barrier, err = c.ensureCancellationBarrier(sealed)
	if err != nil {
		return githublifecycle.CancellationSubmissionProofV1{}, sealed, notApplied, barrier, "", false, err
	}
	if !submissionFound {
		ref, err := c.cancellationBoundaryEvidence(attempt, "sealed-zero-request", githublifecycle.ReconciliationNotApplied, 0, githublifecycle.NotAppliedZeroByteEvidenceKindV1)
		if err != nil {
			return githublifecycle.CancellationSubmissionProofV1{}, sealed, notApplied, barrier, "", false, err
		}
		notApplied, err = githublifecycle.NewNotAppliedProofV1(githublifecycle.NotAppliedProofV1Input{Kind: githublifecycle.NotAppliedZeroRequestBytes, EvidenceRef: ref}, sealed, submission, c.contracts)
		if err != nil {
			return githublifecycle.CancellationSubmissionProofV1{}, sealed, notApplied, barrier, "", false, err
		}
		value := mergeInput.Attempt()
		proof, err := githublifecycle.NewCancellationSubmissionProofV1(githublifecycle.CancellationSubmissionProofV1Input{
			Kind: githublifecycle.CancellationProofSealedZeroRequestBytes, AdmissionSHA256: mergeInput.SHA256(), Attempt: &value,
			SealSHA256: sealed.Seal().SHA256(), CommitmentSHA256: sealed.Commitment().SHA256(), TargetSubmission: &submission,
			SubmissionState: githublifecycle.ReconciliationNotApplied, NotAppliedProof: &notApplied, EvidenceRef: ref,
		}, c.contracts)
		return proof, sealed, notApplied, barrier, githublifecycle.CancellationTargetNotApplied, false, err
	}
	outcome, found, err := c.loadLatestTargetOutcome(attempt, sealed, submission)
	if err != nil {
		return githublifecycle.CancellationSubmissionProofV1{}, sealed, notApplied, barrier, "", false, err
	}
	if !found {
		counters, err := attempt.currentCounters()
		if err != nil {
			return githublifecycle.CancellationSubmissionProofV1{}, sealed, notApplied, barrier, "", false, err
		}
		if counters.TargetSubmissions == 0 {
			ref, err := c.cancellationBoundaryEvidence(attempt, "sealed-zero-request", githublifecycle.ReconciliationNotApplied, 0, githublifecycle.NotAppliedZeroByteEvidenceKindV1)
			if err != nil {
				return githublifecycle.CancellationSubmissionProofV1{}, sealed, notApplied, barrier, "", false, err
			}
			notApplied, err = githublifecycle.NewNotAppliedProofV1(githublifecycle.NotAppliedProofV1Input{Kind: githublifecycle.NotAppliedZeroRequestBytes, EvidenceRef: ref}, sealed, submission, c.contracts)
			if err != nil {
				return githublifecycle.CancellationSubmissionProofV1{}, sealed, notApplied, barrier, "", false, err
			}
			value := mergeInput.Attempt()
			proof, err := githublifecycle.NewCancellationSubmissionProofV1(githublifecycle.CancellationSubmissionProofV1Input{
				Kind: githublifecycle.CancellationProofSealedZeroRequestBytes, AdmissionSHA256: mergeInput.SHA256(), Attempt: &value,
				SealSHA256: sealed.Seal().SHA256(), CommitmentSHA256: sealed.Commitment().SHA256(), TargetSubmission: &submission,
				SubmissionState: githublifecycle.ReconciliationNotApplied, NotAppliedProof: &notApplied, EvidenceRef: ref,
			}, c.contracts)
			return proof, sealed, notApplied, barrier, githublifecycle.CancellationTargetNotApplied, false, err
		}
		outcome = TargetOutcome{Disposition: githublifecycle.ReconciliationUnknown, RequestBytes: submission.RequestBodyBytes(),
			EvidenceRefs: []ledger.EvidenceRef{localEvidence(attempt, "target-submission.json", submission.CanonicalJSON(), "target-submission")}}
	}
	if outcome.Disposition == githublifecycle.ReconciliationApplied {
		return githublifecycle.CancellationSubmissionProofV1{}, sealed, notApplied, barrier, "", false, errors.New("APPLIED target result defeats cancellation")
	}
	value := mergeInput.Attempt()
	proofInput := githublifecycle.CancellationSubmissionProofV1Input{AdmissionSHA256: mergeInput.SHA256(), Attempt: &value,
		SealSHA256: sealed.Seal().SHA256(), CommitmentSHA256: sealed.Commitment().SHA256(), TargetSubmission: &submission,
		RequestBytes: outcome.RequestBytes, SubmissionState: outcome.Disposition, EvidenceRef: outcome.EvidenceRefs[0]}
	boundary, pending := githublifecycle.CancellationTargetSubmissionUnknown, true
	proofInput.Kind = githublifecycle.CancellationProofUnresolvedSubmission
	if outcome.Disposition == githublifecycle.ReconciliationNotApplied {
		boundary, pending, notApplied = githublifecycle.CancellationTargetNotApplied, false, outcome.NotAppliedProof
		proofInput.Kind, proofInput.NotAppliedProof = githublifecycle.CancellationProofAuthenticatedNotApplied, &notApplied
	}
	proof, err := githublifecycle.NewCancellationSubmissionProofV1(proofInput, c.contracts)
	return proof, sealed, notApplied, barrier, boundary, pending, err
}

func (c *Controller) cancellationBoundaryEvidence(attempt *attemptStore, boundary string, disposition githublifecycle.ReconciliationDisposition, requestBytes int64, kind string) (ledger.EvidenceRef, error) {
	data, _ := json.Marshal(struct {
		Schema       string                                    `json:"schema"`
		AttemptID    string                                    `json:"attempt_id"`
		Boundary     string                                    `json:"boundary"`
		Disposition  githublifecycle.ReconciliationDisposition `json:"disposition"`
		RequestBytes int64                                     `json:"request_bytes"`
	}{"cancellation-boundary-observation-v1", attempt.id, boundary, disposition, requestBytes})
	return c.store.appendChannel("cancellation-observations", kind, digest(data), data, MaxTerminalRecordBytes)
}

func (c *Controller) ensureCancellationBarrier(sealed githublifecycle.SealedMergeAuthorizationV1) (ledger.TransitionBarrier, error) {
	input := sealed.MergeInput()
	barrier, err := ledger.NewTransitionBarrier(input.Authority().ReadyBinding().Input().RunID, input.Attempt().WriteID(), sealed.Commitment().SHA256(),
		time.Unix(0, sealed.Seal().Input().FinalRevalidation.Input().CompletedUnixNano).UTC())
	if err != nil {
		return barrier, err
	}
	if err := c.ledger.InstallTransitionBarrier(barrier); err != nil {
		return barrier, err
	}
	return barrier, nil
}

func (c *Controller) loadLatestTargetOutcome(attempt *attemptStore, sealed githublifecycle.SealedMergeAuthorizationV1, submission githublifecycle.TargetSubmissionV1) (TargetOutcome, bool, error) {
	for _, name := range []string{"target-reconciliation.json", "target-outcome.json"} {
		data, found, err := attempt.read(name)
		if err != nil || !found {
			if err != nil {
				return TargetOutcome{}, false, err
			}
			continue
		}
		var record targetOutcomeRecordV1
		if err := strictCanonical(data, &record); err != nil || record.Schema != "merge-target-outcome-v1" || len(record.EvidenceRefs) == 0 {
			return TargetOutcome{}, false, errors.Join(errors.New("target outcome record is invalid"), err)
		}
		outcome := TargetOutcome{Disposition: record.Disposition, RequestBytes: record.RequestBytes, EvidenceRefs: record.EvidenceRefs}
		if record.Disposition == githublifecycle.ReconciliationApplied {
			if len(record.Result) == 0 || digest(record.Result) != record.ResultSHA256 || validateEvidence(record.EvidenceRefs) != nil {
				return TargetOutcome{}, false, errors.New("APPLIED target result record is invalid")
			}
			return outcome, true, nil
		}
		if len(record.Result) > 0 {
			return TargetOutcome{}, false, errors.New("non-APPLIED target outcome contains a result")
		}
		if len(record.NotApplied) > 0 {
			value, err := githublifecycle.ParseCanonicalNotAppliedProofV1(record.NotApplied, sealed, submission, c.contracts)
			if err != nil || value.SHA256() != record.NotAppliedSHA {
				return TargetOutcome{}, false, errors.Join(errors.New("NOT_APPLIED recovery failed"), err)
			}
			outcome.NotAppliedProof = value
		}
		if err := c.validateTargetOutcome(sealed, submission, outcome); err != nil {
			return TargetOutcome{}, false, err
		}
		return outcome, true, nil
	}
	return TargetOutcome{}, false, nil
}

func (c *Controller) cancellationSubmittedState(attempt *attemptStore, boundary githublifecycle.CancellationBoundaryV1) (githublifecycle.SealedMergeAuthorizationV1, githublifecycle.NotAppliedProofV1, ledger.TransitionBarrier, error) {
	var sealed githublifecycle.SealedMergeAuthorizationV1
	var proof githublifecycle.NotAppliedProofV1
	var barrier ledger.TransitionBarrier
	if boundary != githublifecycle.CancellationTargetSubmissionUnknown && boundary != githublifecycle.CancellationTargetNotApplied {
		return sealed, proof, barrier, nil
	}
	data, found, err := attempt.read("sealed-authorization.json")
	if err != nil || !found {
		return sealed, proof, barrier, errors.Join(errors.New("submitted cancellation lost sealed authorization"), err)
	}
	sealed, err = githublifecycle.ParseCanonicalSealedMergeAuthorizationV1(data, c.contracts)
	if err != nil {
		return sealed, proof, barrier, err
	}
	barrier, err = c.ensureCancellationBarrier(sealed)
	if err != nil {
		return sealed, proof, barrier, err
	}
	if boundary == githublifecycle.CancellationTargetNotApplied {
		submissionData, _, err := attempt.read("target-submission.json")
		if err != nil {
			return sealed, proof, barrier, err
		}
		submission, err := githublifecycle.ParseCanonicalTargetSubmissionV1(submissionData, sealed, c.contracts)
		if err != nil {
			return sealed, proof, barrier, err
		}
		outcome, _, err := c.loadLatestTargetOutcome(attempt, sealed, submission)
		if err == nil {
			proof = outcome.NotAppliedProof
		}
		return sealed, proof, barrier, err
	}
	return sealed, proof, barrier, nil
}

func cancellationExpectation(authority githublifecycle.CancellationAuthorityV1, sealed githublifecycle.SealedMergeAuthorizationV1) githublifecycle.CancellationAuthorityExpectationV1 {
	i := authority.Input()
	return githublifecycle.CancellationAuthorityExpectationV1{
		ProjectID: i.ProjectID, PlanID: i.PlanID, RunID: i.RunID, ReadyBinding: i.CurrentReadyProof.Input().ReadyBinding,
		CurrentReadyProof: i.CurrentReadyProof, AdmissionSHA256: i.AdmissionSHA256, Requester: i.Requester,
		AuthenticationEvidence: i.AuthenticationEvidence, CancellationPolicyVersion: i.CancellationPolicyVersion,
		CancellationPolicySource: i.CancellationPolicySource, CancellationPolicySHA256: i.CancellationPolicySHA256,
		ScopedGrantSHA256: i.ScopedGrantSHA256, AllowDecisionSHA256: i.AllowDecisionSHA256, Boundary: i.Boundary,
		Attempt: i.Attempt, SealSHA256: i.SealSHA256, CommitmentSHA256: i.CommitmentSHA256, SealedAuthorization: sealed,
		SubmissionProof: i.SubmissionProof, SourceRequestID: i.SourceRequestID, SourceKind: i.SourceKind,
		RequestEvidence: i.RequestEvidence, IngressID: i.IngressID, ReceiptUnixNano: i.ReceiptUnixNano,
		IngressSequence: i.IngressSequence, EvidenceRefs: i.EvidenceRefs,
	}
}

func validateGrant(authority githublifecycle.CancellationAuthorityV1, grant CancellationGrant, sourceRequestID string) error {
	i := authority.Input()
	if i.SourceRequestID != sourceRequestID || i.Requester != grant.Requester || i.AuthenticationEvidence != grant.AuthenticationEvidence ||
		i.CancellationPolicyVersion != grant.CancellationPolicyVersion || i.CancellationPolicySource != grant.CancellationPolicySource ||
		i.CancellationPolicySHA256 != grant.CancellationPolicySHA256 || i.ScopedGrantSHA256 != grant.ScopedGrantSHA256 ||
		i.AllowDecisionSHA256 != grant.AllowDecisionSHA256 || i.SourceKind != grant.SourceKind || i.RequestEvidence != grant.RequestEvidence ||
		i.IngressID != grant.IngressID {
		return fmt.Errorf("cancellation grant changed during recovery")
	}
	for _, ref := range grant.EvidenceRefs {
		if !containsRef(i.EvidenceRefs, ref) {
			return fmt.Errorf("cancellation grant evidence changed during recovery")
		}
	}
	return nil
}

func containsRef(refs []ledger.EvidenceRef, expected ledger.EvidenceRef) bool {
	for _, ref := range refs {
		if ref == expected {
			return true
		}
	}
	return false
}

func cancellationReplayKey(sourceKind, sourceRequestID string) string {
	data, _ := json.Marshal(struct {
		Schema          string `json:"schema"`
		SourceKind      string `json:"source_kind"`
		SourceRequestID string `json:"source_request_id"`
	}{"cancellation-replay-key-v1", sourceKind, sourceRequestID})
	return digest(data)
}

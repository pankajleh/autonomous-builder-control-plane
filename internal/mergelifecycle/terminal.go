package mergelifecycle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

func (c *Controller) finishSelectedTerminal(ctx context.Context, lease *ledger.RunTransitionLease, assembled assembledAuthority, core TerminalCoreV1, coreBytes []byte, coreSHA string) (Result, error) {
	preAuthorityFailure := core.Destination == domain.StateFailed && core.ReasonCode == CodeInvalidAuthority && core.AuthoritySHA256 == "" && core.PolicySHA256 == ""
	if err := core.validate(); err != nil || digest(coreBytes) != coreSHA || core.ProjectID != assembled.governed.ProjectID ||
		core.PlanID != assembled.governed.PlanID || core.RunID != assembled.governed.Phase3Authority.RunID() ||
		core.ReadyEventID != assembled.ready.Input().ReadyEventID || core.ReadyEventSHA256 != assembled.ready.Input().ReadyEventSHA256 ||
		core.ReadySequence != assembled.ready.Input().ReadyRunStateSequence || !preAuthorityFailure &&
		(core.AuthoritySHA256 != authorityDigest(assembled.authority) || core.PolicySHA256 != assembled.policy.SHA256()) {
		return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, core.WriteID, errors.Join(errors.New("durable terminal core is not independently bound to current controller authority"), err))
	}
	attempt, err := c.store.openAttempt(attemptKey(assembled, deterministicWriteID(assembled)))
	if err != nil {
		return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, core.WriteID, err)
	}
	defer attempt.close()
	result := Result{State: core.Destination, ReasonCode: core.ReasonCode, AttemptID: core.WriteID, TerminalSHA256: coreSHA}
	var sealed githublifecycle.SealedMergeAuthorizationV1
	if core.MergeInputSHA256 != "" {
		input, found, loadErr := c.loadAdmission(attempt)
		if loadErr != nil || !found || input.SHA256() != core.MergeInputSHA256 || input.Attempt().WriteID() != core.WriteID {
			return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, core.WriteID, errors.Join(errors.New("terminal core admission binding is invalid"), loadErr))
		}
	}
	if core.SealSHA256 != "" || core.CommitmentSHA256 != "" {
		sealedData, found, readErr := attempt.read("sealed-authorization.json")
		if readErr != nil || !found {
			return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, core.WriteID, errors.Join(errors.New("terminal core sealed authorization is missing"), readErr))
		}
		sealed, err = githublifecycle.ParseCanonicalSealedMergeAuthorizationV1(sealedData, c.contracts)
		if err != nil || sealed.Seal().SHA256() != core.SealSHA256 || sealed.Commitment().SHA256() != core.CommitmentSHA256 {
			return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, core.WriteID, errors.Join(errors.New("terminal core seal or commitment binding is invalid"), err))
		}
	}
	if core.Destination == domain.StateMerged {
		mergeResult, postMerge, resultFound, postFound, recoverErr := c.recoverAppliedRecords(attempt, sealed)
		if recoverErr != nil || !resultFound || !postFound || mergeResult.SHA256() != core.ResultSHA256 || postMerge.SHA256() != core.VerificationSHA256 {
			return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, core.WriteID, errors.Join(errors.New("MERGED terminal proof chain is invalid"), recoverErr))
		}
		result.MergeResult, result.PostMergeProof = mergeResult, postMerge
	}
	if core.Destination == domain.StateCancelled {
		durable, expectation, found, recoverErr := c.indexedCancellation(ctx, assembled, attempt)
		if recoverErr != nil || !found || durable.SHA256() != core.CancellationSHA256 {
			return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, core.WriteID, errors.Join(errors.New("CANCELLED terminal authority is invalid"), recoverErr))
		}
		var proofs []githublifecycle.NotAppliedProofV1
		if core.NotAppliedProofSHA256 != "" {
			submissionData, submissionFound, readErr := attempt.read("target-submission.json")
			if readErr != nil || !submissionFound {
				return Result{}, errors.Join(errors.New("CANCELLED terminal target submission is missing"), readErr)
			}
			submission, parseErr := githublifecycle.ParseCanonicalTargetSubmissionV1(submissionData, sealed, c.contracts)
			if parseErr != nil {
				return Result{}, parseErr
			}
			outcome, outcomeFound, outcomeErr := c.loadLatestTargetOutcome(attempt, sealed, submission)
			if outcomeErr != nil || !outcomeFound || outcome.NotAppliedProof.SHA256() != core.NotAppliedProofSHA256 {
				return Result{}, errors.Join(errors.New("CANCELLED terminal NOT_APPLIED proof is invalid"), outcomeErr)
			}
			proofs = append(proofs, outcome.NotAppliedProof)
		}
		if err := githublifecycle.AuthorizeCancelledV1(durable, expectation, githublifecycle.ReconciliationNotApplied, c.contracts, proofs...); err != nil {
			return Result{}, err
		}
	}
	event, eventBytes, err := deterministicTerminalEvent(core, coreSHA)
	if err != nil {
		return Result{}, err
	}
	if assembled.ledgerState.current == domain.StateReadyForMerge {
		barrier, active, barrierErr := c.ledger.ActiveTransitionBarrier(core.RunID)
		if barrierErr != nil {
			return Result{}, barrierErr
		}
		if active {
			if barrier.AttemptID != core.AttemptID || core.CommitmentSHA256 != "" && barrier.CommitmentSHA256 != core.CommitmentSHA256 {
				return Result{}, errors.New("durable terminal core conflicts with active transition barrier")
			}
			err = c.ledger.AppendOrVerifyTransition(event, barrier.SHA256)
		} else {
			err = c.ledger.AppendOrVerifyLeased(event, lease)
		}
		if err != nil {
			return Result{}, err
		}
	} else {
		currentBytes, marshalErr := json.Marshal(assembled.ledgerState.currentEvent)
		if marshalErr != nil || assembled.ledgerState.current != core.Destination || !bytes.Equal(currentBytes, eventBytes) {
			return Result{}, errors.Join(errors.New("durable terminal core conflicts with ledger state"), marshalErr)
		}
	}
	terminal := FinalTerminalV1{"merge-final-terminal-v1", coreBytes, coreSHA, eventBytes, digest(eventBytes), true}
	terminalBytes, _ := json.Marshal(terminal)
	if _, err := c.store.appendChannel("final-terminals", "merge-final-terminal", coreSHA, terminalBytes, MaxTerminalRecordBytes); err != nil {
		return Result{}, err
	}
	if barrier, active, barrierErr := c.ledger.ActiveTransitionBarrier(core.RunID); barrierErr != nil {
		return Result{}, barrierErr
	} else if active {
		if err := c.ledger.ResolveTransitionBarrier(barrier); err != nil {
			return Result{}, err
		}
	}
	if err := c.cleanupTerminalAttempt(attempt, coreSHA); err != nil {
		incident, incidentErr := c.recordCleanupIncident(attempt, coreSHA, event.EventID,
			terminalSelection{writeID: core.WriteID, resultSHA256: core.ResultSHA256, verificationSHA256: core.VerificationSHA256}, err, 0)
		if incidentErr == nil {
			result.CleanupIncidents = append(result.CleanupIncidents, incident)
		}
		return result, wrap(CodeLocalCleanupFailed, false, core.WriteID, errors.Join(err, incidentErr))
	}
	return result, nil
}

type terminalSelection struct {
	sealed             githublifecycle.SealedMergeAuthorizationV1
	submission         githublifecycle.TargetSubmissionV1
	mergeResult        githublifecycle.MergeResult
	postMerge          githublifecycle.PostMergeObservation
	notApplied         githublifecycle.NotAppliedProofV1
	cancellation       githublifecycle.DurableCancellationAuthorityV1
	destination        domain.State
	reason             string
	writeID            string
	barrier            *ledger.TransitionBarrier
	selectedUnixNano   int64
	resultSHA256       string
	verificationSHA256 string
}

// terminalizeIdentifiedAuthorityFailure is the narrow pre-authority path used
// only after the ledger independently proves one unique current READY event.
// It deliberately carries no fabricated authority/policy digest.
func (c *Controller) terminalizeIdentifiedAuthorityFailure(lease *ledger.RunTransitionLease, governed GovernedAuthority, state readyLedgerState) (Result, error) {
	barrier, active, err := c.ledger.ActiveTransitionBarrier(governed.Phase3Authority.RunID())
	if err != nil {
		return Result{}, err
	}
	if active {
		cause := errors.New("active target-submission barrier prevents pre-authority terminal selection")
		return Result{State: domain.StateReadyForMerge, ReasonCode: CodeTargetUnknown, AttemptID: barrier.AttemptID, Unresolved: true},
			wrap(CodeTargetUnknown, true, barrier.AttemptID, cause)
	}
	writeID := digest([]byte("merge-pre-authority-failure-v1\x00" + governed.Phase3Authority.RunID() + "\x00" + state.event.EventID + "\x00" + digest(state.eventJSON)))
	core := TerminalCoreV1{Schema: "merge-terminal-core-v1", ProjectID: governed.ProjectID, PlanID: governed.PlanID,
		RunID: governed.Phase3Authority.RunID(), AttemptID: writeID, WriteID: writeID, ReadyEventID: state.event.EventID,
		ReadyEventSHA256: digest(state.eventJSON), ReadySequence: state.sequence, Destination: domain.StateFailed,
		ReasonCode: CodeInvalidAuthority, SelectedUnixNano: state.event.Timestamp.UnixNano() + 1}
	coreBytes, err := json.Marshal(core)
	if err != nil || core.validate() != nil {
		return Result{}, errors.Join(errors.New("identified READY failure core is invalid"), err, core.validate())
	}
	coreSHA := digest(coreBytes)
	if selected, selectedBytes, selectedSHA, found, findErr := c.store.findTerminalCore(core.RunID); findErr != nil {
		return Result{}, findErr
	} else if found && (selectedSHA != coreSHA || !bytes.Equal(selectedBytes, coreBytes) || selected != core) {
		return Result{}, errors.New("identified READY failure conflicts with a durable terminal selection")
	}
	if _, err := c.store.appendChannel("terminal-intents", "merge-terminal-core", coreSHA, coreBytes, MaxTerminalRecordBytes); err != nil {
		return Result{}, err
	}
	if c.afterTerminalCore != nil {
		if err := c.afterTerminalCore(core); err != nil {
			return Result{}, err
		}
	}
	event, eventBytes, err := deterministicTerminalEvent(core, coreSHA)
	if err != nil {
		return Result{}, err
	}
	if state.current == domain.StateReadyForMerge {
		if err := c.ledger.AppendOrVerifyLeased(event, lease); err != nil {
			return Result{}, err
		}
	} else {
		currentBytes, marshalErr := json.Marshal(state.currentEvent)
		if marshalErr != nil || state.current != domain.StateFailed || !bytes.Equal(currentBytes, eventBytes) {
			return Result{}, errors.Join(errors.New("identified READY failure conflicts with ledger terminal"), marshalErr)
		}
	}
	if c.afterTerminalEvent != nil {
		if err := c.afterTerminalEvent(core); err != nil {
			return Result{}, err
		}
	}
	terminal := FinalTerminalV1{"merge-final-terminal-v1", coreBytes, coreSHA, eventBytes, digest(eventBytes), true}
	terminalBytes, _ := json.Marshal(terminal)
	if _, err := c.store.appendChannel("final-terminals", "merge-final-terminal", coreSHA, terminalBytes, MaxTerminalRecordBytes); err != nil {
		return Result{}, err
	}
	return Result{State: domain.StateFailed, ReasonCode: CodeInvalidAuthority, AttemptID: writeID, TerminalSHA256: coreSHA}, nil
}

func (c *Controller) terminalize(lease *ledger.RunTransitionLease, assembled assembledAuthority, attempt *attemptStore, selection terminalSelection) (Result, error) {
	var result Result
	if selection.destination != domain.StateMerged && selection.destination != domain.StateFailed && selection.destination != domain.StateCancelled {
		return result, errors.New("terminal destination is invalid")
	}
	if c.beforeTerminal != nil {
		if err := c.beforeTerminal(attempt); err != nil {
			return result, err
		}
	}
	writeID := selection.writeID
	mergeInputSHA, policySHA, sealSHA, commitmentSHA, submissionSHA := "", assembled.policy.SHA256(), "", "", ""
	if selection.sealed.SHA256() != "" {
		input := selection.sealed.MergeInput()
		writeID, mergeInputSHA = input.Attempt().WriteID(), input.SHA256()
		policySHA = input.Authority().MergePolicy().SHA256()
		sealSHA, commitmentSHA = selection.sealed.Seal().SHA256(), selection.sealed.Commitment().SHA256()
	}
	if selection.submission.SHA256() != "" {
		submissionSHA = selection.submission.SHA256()
	}
	if writeID == "" {
		writeID = assembled.governed.AttemptID
	}
	selectedUnixNano := selection.selectedUnixNano
	if selectedUnixNano <= 0 && selection.cancellation.SHA256() != "" {
		selectedUnixNano = selection.cancellation.Authority().Input().ReceiptUnixNano
	}
	if selectedUnixNano <= 0 && selection.sealed.SHA256() != "" {
		selectedUnixNano = selection.sealed.Seal().Input().FinalRevalidation.Input().CompletedUnixNano
	}
	if selectedUnixNano <= 0 {
		selectedUnixNano = assembled.ready.Input().ReadyEventUnixNano + 1
	}
	core := TerminalCoreV1{
		Schema: "merge-terminal-core-v1", ProjectID: assembled.governed.ProjectID, PlanID: assembled.governed.PlanID,
		RunID: assembled.governed.Phase3Authority.RunID(), AttemptID: writeID, WriteID: writeID,
		ReadyEventID: assembled.ready.Input().ReadyEventID, ReadyEventSHA256: assembled.ready.Input().ReadyEventSHA256,
		ReadySequence: assembled.ready.Input().ReadyRunStateSequence, AuthoritySHA256: authorityDigest(assembled.authority), PolicySHA256: policySHA,
		MergeInputSHA256: mergeInputSHA, SealSHA256: sealSHA, CommitmentSHA256: commitmentSHA, SubmissionSHA256: submissionSHA,
		ResultSHA256: selection.mergeResult.SHA256(), VerificationSHA256: selection.postMerge.SHA256(),
		CancellationSHA256: selection.cancellation.SHA256(), NotAppliedProofSHA256: selection.notApplied.SHA256(),
		Destination: selection.destination, ReasonCode: selection.reason, SelectedUnixNano: selectedUnixNano,
	}
	if err := core.validate(); err != nil {
		return result, err
	}
	if core.Destination == domain.StateMerged && (!validDigest(core.ResultSHA256) || !validDigest(core.VerificationSHA256)) {
		return result, errors.New("MERGED terminal requires durable result and post-merge proof")
	}
	if core.Destination == domain.StateCancelled && !validDigest(core.CancellationSHA256) {
		return result, errors.New("CANCELLED terminal requires durable validated cancellation authority")
	}
	coreBytes, err := json.Marshal(core)
	if err != nil || len(coreBytes) > c.limits.terminalRecordBytes {
		return result, errors.Join(errors.New("terminal core exceeds its bound"), err)
	}
	coreSHA := digest(coreBytes)
	event, eventBytes, err := deterministicTerminalEvent(core, coreSHA)
	if err != nil {
		return result, err
	}
	// Terminal intent durability is the irreversible selection point. For an
	// accepted applied proof, nothing (including cleanup) runs before this.
	if _, err := c.store.appendChannel("terminal-intents", "merge-terminal-core", coreSHA, coreBytes, MaxTerminalRecordBytes); err != nil {
		return result, err
	}
	if c.afterTerminalCore != nil {
		if err := c.afterTerminalCore(core); err != nil {
			return result, err
		}
	}
	if selection.barrier != nil {
		if err := c.ledger.AppendOrVerifyTransition(event, selection.barrier.SHA256); err != nil {
			return result, err
		}
	} else {
		if err := c.ledger.AppendOrVerifyLeased(event, lease); err != nil {
			return result, err
		}
	}
	if c.afterTerminalEvent != nil {
		if err := c.afterTerminalEvent(core); err != nil {
			return result, err
		}
	}
	terminal := FinalTerminalV1{"merge-final-terminal-v1", coreBytes, coreSHA, eventBytes, digest(eventBytes), true}
	terminalBytes, _ := json.Marshal(terminal)
	if _, err := c.store.appendChannel("final-terminals", "merge-final-terminal", coreSHA, terminalBytes, MaxTerminalRecordBytes); err != nil {
		return result, err
	}
	if selection.barrier != nil {
		if err := c.ledger.ResolveTransitionBarrier(*selection.barrier); err != nil {
			return result, err
		}
	}
	result = Result{State: selection.destination, ReasonCode: selection.reason, AttemptID: writeID, TerminalSHA256: coreSHA,
		MergeResult: selection.mergeResult, PostMergeProof: selection.postMerge}
	if err := c.cleanupTerminalAttempt(attempt, coreSHA); err != nil {
		incident, incidentErr := c.recordCleanupIncident(attempt, coreSHA, event.EventID, selection, err, 1)
		if incidentErr != nil {
			return result, errors.Join(err, incidentErr)
		}
		result.CleanupIncidents = append(result.CleanupIncidents, incident)
		return result, wrap(CodeLocalCleanupFailed, false, writeID, err)
	}
	return result, nil
}

func deterministicTerminalEvent(core TerminalCoreV1, coreSHA string) (ledger.Event, []byte, error) {
	idHash := sha256.Sum256(append([]byte("merge-terminal-event-v1\x00"), []byte(coreSHA)...))
	event := ledger.Event{
		SchemaVersion: ledger.CurrentSchemaVersion, EventID: hex.EncodeToString(idHash[:]), Timestamp: time.Unix(0, core.SelectedUnixNano).UTC(),
		ProjectID: core.ProjectID, PlanID: core.PlanID, RunID: core.RunID, AttemptID: core.AttemptID,
		EventType: "STATE_TRANSITION", StateFrom: domain.StateReadyForMerge, StateTo: core.Destination,
		Actor: "controller", Source: "merge-lifecycle", Payload: map[string]any{
			"terminal_core_sha256": coreSHA, "reason_code": core.ReasonCode, "ready_event_id": core.ReadyEventID,
			"ready_event_sha256": core.ReadyEventSHA256, "ready_sequence": core.ReadySequence,
		},
	}
	if err := event.Validate(); err != nil {
		return event, nil, err
	}
	data, err := json.Marshal(event)
	return event, data, err
}

func (c *Controller) recoverTerminal(ctx context.Context, assembled assembledAuthority, result Result) (Result, error) {
	if !validDigest(result.TerminalSHA256) {
		return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, result.AttemptID, errors.New("terminal ledger event lacks a core identity"))
	}
	coreBytes, found, err := c.store.readChannel("terminal-intents", result.TerminalSHA256, MaxTerminalRecordBytes)
	if err != nil || !found {
		return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, result.AttemptID, errors.Join(errors.New("terminal intent is missing"), err))
	}
	core, err := ParseTerminalCoreV1(coreBytes)
	preAuthorityFailure := core.Destination == domain.StateFailed && core.ReasonCode == CodeInvalidAuthority && core.AuthoritySHA256 == "" && core.PolicySHA256 == ""
	if err != nil || digest(coreBytes) != result.TerminalSHA256 || core.RunID != assembled.governed.Phase3Authority.RunID() ||
		core.ProjectID != assembled.governed.ProjectID || core.PlanID != assembled.governed.PlanID ||
		core.ReadyEventID != assembled.ready.Input().ReadyEventID || core.ReadyEventSHA256 != assembled.ready.Input().ReadyEventSHA256 ||
		core.ReadySequence != assembled.ready.Input().ReadyRunStateSequence || !preAuthorityFailure &&
		(core.AuthoritySHA256 != authorityDigest(assembled.authority) || core.PolicySHA256 != assembled.policy.SHA256()) ||
		core.Destination != result.State || core.AttemptID != result.AttemptID || core.ReasonCode != result.ReasonCode {
		return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, result.AttemptID, errors.Join(errors.New("terminal intent disagrees with ledger terminal"), err))
	}
	event, eventBytes, err := deterministicTerminalEvent(core, result.TerminalSHA256)
	currentEventBytes, marshalErr := json.Marshal(assembled.ledgerState.currentEvent)
	if err != nil || marshalErr != nil || event.EventID != assembled.ledgerState.currentEvent.EventID || !bytes.Equal(eventBytes, currentEventBytes) {
		return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, result.AttemptID, errors.Join(errors.New("terminal event identity disagrees"), err, marshalErr))
	}
	terminal := FinalTerminalV1{"merge-final-terminal-v1", coreBytes, result.TerminalSHA256, eventBytes, digest(eventBytes), true}
	terminalBytes, _ := json.Marshal(terminal)
	if _, err := ParseFinalTerminalV1(terminalBytes); err != nil {
		return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, result.AttemptID, err)
	}
	if _, err := c.store.appendChannel("final-terminals", "merge-final-terminal", result.TerminalSHA256, terminalBytes, MaxTerminalRecordBytes); err != nil {
		return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, result.AttemptID, err)
	}
	if barrier, active, err := c.ledger.ActiveTransitionBarrier(core.RunID); err != nil {
		return Result{}, err
	} else if active {
		if barrier.AttemptID != core.AttemptID {
			return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, result.AttemptID, errors.New("terminal event has a different active barrier"))
		}
		if err := c.ledger.ResolveTransitionBarrier(barrier); err != nil {
			return Result{}, err
		}
	}
	attempt, err := c.store.openAttempt(attemptKey(assembled, deterministicWriteID(assembled)))
	if err != nil {
		return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, result.AttemptID, err)
	}
	defer attempt.close()
	if result.State == domain.StateMerged {
		sealedData, found, err := attempt.read("sealed-authorization.json")
		if err != nil || !found {
			return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, result.AttemptID, errors.Join(errors.New("MERGED recovery lacks sealed authorization"), err))
		}
		sealed, err := githublifecycle.ParseCanonicalSealedMergeAuthorizationV1(sealedData, c.contracts)
		if err != nil {
			return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, result.AttemptID, err)
		}
		mergeResult, postMerge, resultFound, postFound, err := c.recoverAppliedRecords(attempt, sealed)
		if err != nil || !resultFound || !postFound || mergeResult.SHA256() != core.ResultSHA256 || postMerge.SHA256() != core.VerificationSHA256 {
			return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, result.AttemptID, errors.Join(errors.New("MERGED recovery proof chain disagrees"), err))
		}
		result.MergeResult, result.PostMergeProof = mergeResult, postMerge
	}
	if result.State == domain.StateCancelled {
		durable, expectation, found, err := c.indexedCancellationForTerminal(ctx, assembled, attempt, core)
		if err != nil || !found || durable.SHA256() != core.CancellationSHA256 {
			return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, result.AttemptID, errors.Join(errors.New("CANCELLED recovery authority disagrees"), err))
		}
		var proofs []githublifecycle.NotAppliedProofV1
		if bound := durable.Authority().Input().SubmissionProof.Input().NotAppliedProof; bound != nil {
			proofs = append(proofs, *bound)
		}
		if err := githublifecycle.AuthorizeCancelledV1(durable, expectation, githublifecycle.ReconciliationNotApplied, c.contracts, proofs...); err != nil {
			return Result{}, err
		}
	}
	if err := c.cleanupTerminalAttempt(attempt, result.TerminalSHA256); err != nil {
		incident, incidentErr := c.recordCleanupIncident(attempt, result.TerminalSHA256, event.EventID,
			terminalSelection{writeID: core.WriteID, resultSHA256: core.ResultSHA256, verificationSHA256: core.VerificationSHA256}, err, 1)
		if incidentErr == nil {
			result.CleanupIncidents = append(result.CleanupIncidents, incident)
		}
		return result, wrap(CodeLocalCleanupFailed, false, result.AttemptID, errors.Join(err, incidentErr))
	}
	return result, nil
}

func (c *Controller) recordCleanupIncident(attempt *attemptStore, coreSHA, eventID string, selection terminalSelection, cause error, sequence int) (LocalCleanupIncidentV1, error) {
	sequence, exhausted, err := c.store.nextCleanupSequence(coreSHA)
	if err != nil {
		return LocalCleanupIncidentV1{}, err
	}
	if exhausted {
		return LocalCleanupIncidentV1{}, errors.New(CodeLocalCleanupExhausted)
	}
	incident := LocalCleanupIncidentV1{
		Schema: "merge-local-cleanup-incident-v1", Sequence: sequence, AttemptID: attempt.id, WriteID: selection.writeID,
		TerminalCoreSHA256: coreSHA, EventID: eventID, Operation: "remove-classified-temporary", Boundary: "post-terminal-event",
		ErrorClass: "local-filesystem", ErrorSHA256: digest([]byte(cause.Error())), ResultSHA256: selection.mergeResult.SHA256(),
		VerificationSHA256: selection.postMerge.SHA256(),
	}
	if sequence == c.limits.cleanupAttempts {
		incident.Operation = "automatic-cleanup-exhausted"
		incident.Boundary = "cleanup-recovery-limit"
		incident.ErrorClass = CodeLocalCleanupExhausted
	}
	if incident.ResultSHA256 == "" {
		incident.ResultSHA256 = selection.resultSHA256
	}
	if incident.VerificationSHA256 == "" {
		incident.VerificationSHA256 = selection.verificationSHA256
	}
	if incident.WriteID == "" && selection.sealed.SHA256() != "" {
		incident.WriteID = selection.sealed.MergeInput().Attempt().WriteID()
	}
	copy := incident
	copy.IncidentID = ""
	data, _ := json.Marshal(copy)
	incident.IncidentID = digest(data)
	data, _ = json.Marshal(incident)
	if len(data) > c.limits.cleanupIncidentBytes {
		return incident, errors.New("cleanup incident exceeds its bound")
	}
	_, err = c.store.appendChannel("cleanup-incidents", "merge-local-cleanup-incident", incident.IncidentID, data, MaxCleanupIncidentBytes)
	return incident, err
}

func (c *Controller) cleanupTerminalAttempt(attempt *attemptStore, coreSHA string) error {
	_, exhausted, err := c.store.nextCleanupSequence(coreSHA)
	if err != nil {
		return err
	}
	if exhausted {
		return errors.New(CodeLocalCleanupExhausted)
	}
	return attempt.cleanupTemporary()
}

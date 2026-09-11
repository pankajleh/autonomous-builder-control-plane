package mergelifecycle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

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
	if selection.barrier != nil {
		if err := c.ledger.AppendOrVerifyTransition(event, selection.barrier.SHA256); err != nil {
			return result, err
		}
	} else {
		if err := c.ledger.AppendOrVerifyLeased(event, lease); err != nil {
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
	if err := attempt.cleanupTemporary(); err != nil {
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

func (c *Controller) recoverTerminal(assembled assembledAuthority, result Result) (Result, error) {
	if !validDigest(result.TerminalSHA256) {
		return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, result.AttemptID, errors.New("terminal ledger event lacks a core identity"))
	}
	coreBytes, found, err := c.store.readChannel("terminal-intents", result.TerminalSHA256, MaxTerminalRecordBytes)
	if err != nil || !found {
		return Result{}, wrap(CodeLocalStorageIntegrityFailure, false, result.AttemptID, errors.Join(errors.New("terminal intent is missing"), err))
	}
	core, err := ParseTerminalCoreV1(coreBytes)
	if err != nil || digest(coreBytes) != result.TerminalSHA256 || core.RunID != assembled.governed.Phase3Authority.RunID() ||
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
	if err := attempt.cleanupTemporary(); err != nil {
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
	incident := LocalCleanupIncidentV1{
		Schema: "merge-local-cleanup-incident-v1", Sequence: sequence, AttemptID: attempt.id, WriteID: selection.writeID,
		TerminalCoreSHA256: coreSHA, EventID: eventID, Operation: "remove-classified-temporary", Boundary: "post-terminal-event",
		ErrorClass: "local-filesystem", ErrorSHA256: digest([]byte(cause.Error())), ResultSHA256: selection.mergeResult.SHA256(),
		VerificationSHA256: selection.postMerge.SHA256(),
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
	_, err := c.store.appendChannel("cleanup-incidents", "merge-local-cleanup-incident", incident.IncidentID, data, MaxCleanupIncidentBytes)
	return incident, err
}

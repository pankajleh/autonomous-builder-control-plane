package timeline

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runfailure"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
)

// failureRun is a run's ledger up to a stop transition, as the runner writes it, read back as the read model reads
// it (numbers as json.Number), with the stop's evidence written by a real evidence store.
type failureRun struct {
	runID        string
	registration runtimecatalog.RunRegistrationV1
	store        *evidence.Store
	events       []ledger.Event
}

func newFailureRun(t *testing.T, runID string) *failureRun {
	t.Helper()
	registration := timelineRegistration(runID, t.TempDir())
	store, err := evidence.NewStore(registration.CanonicalEvidenceRoot, runID)
	if err != nil {
		t.Fatal(err)
	}
	run := &failureRun{runID: runID, registration: registration, store: store, events: []ledger.Event{timelineCreated(runID, "event-1")}}
	for _, edge := range [][2]domain.State{
		{domain.StateRunCreated, domain.StateAuthorityValidated}, {domain.StateAuthorityValidated, domain.StateExecutionStarting},
		{domain.StateExecutionStarting, domain.StateImplementing},
	} {
		run.transition(edge[0], edge[1], nil)
	}
	return run
}

func (f *failureRun) transition(from, to domain.State, payload map[string]any, refs ...ledger.EvidenceRef) ledger.Event {
	event := timelineEvent(f.runID, "event-"+string(rune('a'+len(f.events))), len(f.events))
	event.EventType, event.StateFrom, event.StateTo, event.Payload, event.EvidenceRefs = "STATE_TRANSITION", from, to, payload, refs
	f.events = append(f.events, event)
	return event
}

func (f *failureRun) evidence(t *testing.T, name, kind string, data []byte) ledger.EvidenceRef {
	t.Helper()
	ref, err := f.store.WriteBytes(name, kind, data)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func (f *failureRun) service(t *testing.T) *Service {
	t.Helper()
	events := make([]ledger.Event, len(f.events))
	for i, event := range f.events {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&events[i]); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := timelineSnapshot(t, f.runID, events)
	snapshot.Projection.CurrentState = string(events[len(events)-1].StateTo)
	return timelineService(t, &snapshotFixture{snapshot: snapshot}, f.registration, nil)
}

func (f *failureRun) read(t *testing.T) (RunFailureV1, string) {
	t.Helper()
	raw, err := f.service(t).ReadRunFailure(context.Background(), f.runID)
	if err != nil {
		t.Fatal(err)
	}
	var response RunFailureV1
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}
	return response, string(raw)
}

func validationDetail() runfailure.Detail {
	code := 1
	return runfailure.Detail{Cause: runfailure.CauseValidationFailed, Phase: runfailure.PhaseImplementation, Check: "node-app-check",
		CheckState: runfailure.CheckExited, CheckExitCode: &code, RepairTurns: 2, TaskRetries: 1, TaskRetryLimit: 1, Attempts: 1, Engine: "codex"}
}

func TestRunFailureAnswersTheRecordedCauseAndTheChecksOwnOutput(t *testing.T) {
	run := newFailureRun(t, "failure-recorded")
	stdout := run.evidence(t, "ralphex-stdout.log", "ralphex-stdout", []byte("stdout"))
	body, _ := json.Marshal(runfailure.Evidence{SchemaVersion: runfailure.EvidenceSchemaVersion, Cause: runfailure.CauseValidationFailed,
		Check: "node-app-check", CheckOutput: "NODE_APP_CHECK_FAILED: tests\nnot ok 3 - tests/browser.test.js", LastError: "runner: task phase: …"})
	detailRef := run.evidence(t, runfailure.EvidenceName, runfailure.EvidenceKind, body)
	stop := run.transition(domain.StateImplementing, domain.StateFailed, map[string]any{
		"outcome": "exited_nonzero", "exit_code": 1, "failure_class": "TASK_NOT_COMPLETED", "retryability": "RETRYABLE_AFTER_CONDITION_CHANGE",
		runfailure.PayloadKey: validationDetail().Payload(),
	}, stdout, detailRef)

	response, raw := run.read(t)
	failure := response.Failure
	if response.SchemaVersion != RunFailureSchemaVersion || response.RunID != run.runID || response.CurrentState != "FAILED" || failure == nil {
		t.Fatalf("response = %s", raw)
	}
	if !failure.Recorded || failure.Cause != "VALIDATION_FAILED" || failure.Phase != "IMPLEMENTATION" || failure.State != "FAILED" ||
		failure.StateFrom != "IMPLEMENTING" || failure.Event.Ordinal != 5 || failure.Event.EventID != stop.EventID ||
		failure.FailureClass != "TASK_NOT_COMPLETED" || failure.Retryability != "RETRYABLE_AFTER_CONDITION_CHANGE" ||
		failure.Check != "node-app-check" || failure.CheckState != "EXITED" || failure.CheckExitCode == nil || *failure.CheckExitCode != 1 ||
		failure.CheckOutput != "NODE_APP_CHECK_FAILED: tests\nnot ok 3 - tests/browser.test.js" || failure.RepairTurns != 2 ||
		failure.TaskRetries != 1 || failure.TaskRetryLimit != 1 || failure.Attempts != 1 || failure.Engine != "codex" {
		t.Fatalf("failure = %s", raw)
	}
	// Each evidence item is the one GET /v1/runs/{run}/evidence/{id} downloads; the Ralphex last error stays there.
	if len(failure.Evidence) != 2 || failure.Evidence[0].Kind != "ralphex-stdout" || failure.Evidence[1].Kind != runfailure.EvidenceKind {
		t.Fatalf("evidence = %+v", failure.Evidence)
	}
	downloaded, err := run.service(t).ReadEvidence(context.Background(), run.runID, failure.Evidence[1].EvidenceID)
	if err != nil || !bytes.Equal(downloaded, body) {
		t.Fatalf("download = %s, %v", downloaded, err)
	}
	if strings.Contains(raw, "last_error") || strings.Contains(raw, run.registration.CanonicalEvidenceRoot) {
		t.Fatalf("the answer carries the last error or a path: %s", raw)
	}
}

// The answer's exact shape, which Repo C parses strictly.
func TestRunFailureShape(t *testing.T) {
	run := newFailureRun(t, "failure-shape")
	run.transition(domain.StateImplementing, domain.StateFailed, map[string]any{"reason": "BUILD_WALL_FAILED",
		runfailure.PayloadKey: runfailure.Detail{Cause: runfailure.CauseSandbox, Phase: runfailure.PhaseSetup, Engine: "codex", Attempts: 1, TaskRetryLimit: 1}.Payload()})
	_, raw := run.read(t)
	want := `{"schema_version":"RunFailureV1","run_id":"failure-shape","projection_revision":"<rev>","current_state":"FAILED","failure":{"event":{"ordinal":5,"event_id":"event-e","timestamp":"<ts>"},"state":"FAILED","state_from":"IMPLEMENTING","recorded":true,"cause":"SANDBOX","phase":"SETUP","reason":"BUILD_WALL_FAILED","repair_turns":0,"task_retries":0,"task_retry_limit":1,"attempts":1,"engine":"codex","evidence":[]}}`
	var response RunFailureV1
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		t.Fatal(err)
	}
	got := strings.Replace(strings.Replace(raw, response.ProjectionRevision, "<rev>", 1), response.Failure.Event.Timestamp, "<ts>", 1)
	if got != want {
		t.Fatalf("shape:\n got %s\nwant %s", got, want)
	}
}

func TestRunFailureIsAbsentWhileARunGoesOnAndOnceItsBranchIsAccepted(t *testing.T) {
	running := newFailureRun(t, "failure-running")
	if response, raw := running.read(t); response.Failure != nil || response.CurrentState != "IMPLEMENTING" {
		t.Fatalf("running = %s", raw)
	}
	accepted := newFailureRun(t, "failure-accepted")
	accepted.transition(domain.StateImplementing, domain.StateImplementationCompleted, nil)
	accepted.transition(domain.StateImplementationCompleted, domain.StateBranchAcceptancePending, nil)
	accepted.transition(domain.StateBranchAcceptancePending, domain.StateBranchAccepted, map[string]any{"status": "PASS"})
	if response, raw := accepted.read(t); response.Failure != nil || strings.Contains(raw, `"failure"`) {
		t.Fatalf("accepted = %s", raw)
	}
}

// A stop recorded before R1, or one whose detail is not exactly R1's form, is told from its fixed codes only, and says
// it was not recorded; no free text reaches the answer.
func TestRunFailureTellsAnOlderStopFromItsFixedCodes(t *testing.T) {
	for _, test := range []struct {
		name    string
		to      domain.State
		payload map[string]any
		cause   string
	}{
		{"a wall that did not start", domain.StateFailed, map[string]any{"reason": "BUILD_WALL_FAILED"}, "SANDBOX"},
		{"an older Ralphex failure", domain.StateFailed, map[string]any{"outcome": "exited_nonzero", "failure_class": "TASK_NOT_COMPLETED"}, "UNCLASSIFIED"},
		{"an older timeout", domain.StateFailed, map[string]any{"outcome": "timed_out", "failure_class": "TRANSIENT_EXECUTION_FAILURE"}, "TIMEOUT"},
		{"an older limit", domain.StateFailed, map[string]any{"outcome": "exited_nonzero", "failure_class": "HARD_QUOTA_EXHAUSTED"}, "PROVIDER_LIMIT"},
		{"another step", domain.StateFailed, map[string]any{"reason": "GOVERNANCE_CONTROLLER_FAILED"}, "CONTROLLER_ERROR"},
		{"a Stop build an API cancellation took over", domain.StateCancelled, map[string]any{"reason": "service API cancellation requested", "operation_id": "op-1"}, "CANCELLED"},
		{"a stop by a signal", domain.StateCancelled, map[string]any{"outcome": "canceled"}, "INTERRUPTED"},
		{"a detail with free text", domain.StateFailed, map[string]any{"reason": "ACCEPTANCE_FAILED", runfailure.PayloadKey: map[string]any{
			"record_schema_version": 1, "cause": "ACCEPTANCE_FAILED", "phase": "ACCEPTANCE", "note": "the person's /home/words"}}, "ACCEPTANCE_FAILED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := newFailureRun(t, "failure-older")
			run.transition(domain.StateImplementing, test.to, test.payload)
			response, raw := run.read(t)
			if response.Failure == nil || response.Failure.Recorded || response.Failure.Cause != test.cause || response.Failure.Phase != "IMPLEMENTATION" {
				t.Fatalf("answer = %s", raw)
			}
			if strings.Contains(raw, "words") || strings.Contains(raw, "service API") {
				t.Fatalf("free text in the answer: %s", raw)
			}
		})
	}
}

// The check's output is answered only from a body that is exactly R1's evidence for the same cause and check, and that
// still verifies; otherwise the codes stand without it.
func TestRunFailureLeavesOutAnOutputItCannotTrust(t *testing.T) {
	for _, test := range []struct {
		name string
		body func(t *testing.T, run *failureRun) ledger.EvidenceRef
	}{
		{"another check's", func(t *testing.T, run *failureRun) ledger.EvidenceRef {
			body, _ := json.Marshal(runfailure.Evidence{SchemaVersion: runfailure.EvidenceSchemaVersion, Cause: runfailure.CauseValidationFailed, Check: "lint", CheckOutput: "lint says"})
			return run.evidence(t, runfailure.EvidenceName, runfailure.EvidenceKind, body)
		}},
		{"an unknown field", func(t *testing.T, run *failureRun) ledger.EvidenceRef {
			return run.evidence(t, runfailure.EvidenceName, runfailure.EvidenceKind, []byte(`{"schema_version":"RunFailureDetailV1","cause":"VALIDATION_FAILED","check":"node-app-check","check_output":"x","extra":1}`))
		}},
		{"an output over 4 KB", func(t *testing.T, run *failureRun) ledger.EvidenceRef {
			body, _ := json.Marshal(runfailure.Evidence{SchemaVersion: runfailure.EvidenceSchemaVersion, Cause: runfailure.CauseValidationFailed, Check: "node-app-check", CheckOutput: strings.Repeat("x", runfailure.MaxCheckOutputBytes+1)})
			return run.evidence(t, runfailure.EvidenceName, runfailure.EvidenceKind, body)
		}},
		{"a body changed since", func(t *testing.T, run *failureRun) ledger.EvidenceRef {
			body, _ := json.Marshal(runfailure.Evidence{SchemaVersion: runfailure.EvidenceSchemaVersion, Cause: runfailure.CauseValidationFailed, Check: "node-app-check", CheckOutput: "x"})
			ref := run.evidence(t, runfailure.EvidenceName, runfailure.EvidenceKind, body)
			ref.SHA256 = strings.Repeat("0", 64)
			return ref
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := newFailureRun(t, "failure-untrusted")
			run.transition(domain.StateImplementing, domain.StateFailed, map[string]any{runfailure.PayloadKey: validationDetail().Payload()}, test.body(t, run))
			response, raw := run.read(t)
			if response.Failure == nil || !response.Failure.Recorded || response.Failure.Check != "node-app-check" || response.Failure.CheckOutput != "" || len(response.Failure.Evidence) != 1 {
				t.Fatalf("answer = %s", raw)
			}
		})
	}
}

func TestRunFailureAnswersAVanishedRunAsTheTimelineDoes(t *testing.T) {
	run := newFailureRun(t, "failure-missing")
	service := run.service(t)
	if _, err := service.ReadRunFailure(context.Background(), "not a run id"); err == nil {
		t.Fatal("an invalid run id was read")
	}
	if _, err := service.ReadRunFailure(context.Background(), "another-run"); err == nil {
		t.Fatal("another run's snapshot was answered")
	}
}

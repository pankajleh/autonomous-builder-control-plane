package serviceapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCommandEnvelopeFoundationBounds(t *testing.T) {
	valid := CommandEnvelopeV1{
		SchemaVersion: 1, RequestID: "request-1", AttemptID: "attempt-1",
		ExpectedState: "EXECUTING", ExpectedRevision: strings.Repeat("a", 64),
		Reason: "operator request", DelegatedActor: &DelegatedActorV1{SubjectID: "user-1", SubjectType: PrincipalUser},
		Payload: json.RawMessage(`{"decision_request_id":"event-1","answer":"yes"}`),
	}
	if err := ValidateCommandEnvelopeV1(valid, true); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.RequestID = strings.Repeat("r", 129)
	if err := ValidateCommandEnvelopeV1(invalid, true); err == nil {
		t.Fatal("oversized request ID accepted")
	}
	invalid = valid
	invalid.Reason = strings.Repeat("x", MaxCommandReasonBytes+1)
	if err := ValidateCommandEnvelopeV1(invalid, true); err == nil {
		t.Fatal("oversized reason accepted")
	}
	invalid = valid
	invalid.DelegatedActor = nil
	if err := ValidateCommandEnvelopeV1(invalid, true); err == nil {
		t.Fatal("missing delegated actor accepted")
	}
	invalid = valid
	invalid.Payload = json.RawMessage(`{"a":{"b":{"c":{"d":{"e":{"f":{"g":{"h":{"i":true}}}}}}}}}`)
	if err := ValidateCommandEnvelopeV1(invalid, false); err == nil {
		t.Fatal("excessively nested payload accepted")
	}
	invalid = valid
	invalid.Payload = json.RawMessage(`{"a":1,"a":2}`)
	if err := ValidateCommandEnvelopeV1(invalid, false); err == nil {
		t.Fatal("duplicate payload field accepted")
	}
}

func TestRunAdmissionTaskMarkdownLineBoundMatchesPinnedPlanParser(t *testing.T) {
	valid := RunAdmissionRequestV1{
		SchemaVersion: 1, RequestID: "request-1", ProfileID: "default",
		ProductAuthorizationID: "authorization-1", ProductTaskID: "task-1", ProductVersionID: "version-1",
		ProductManifestSHA256: strings.Repeat("a", 64), RepositoryBaseSHA: strings.Repeat("b", 40),
		TaskMarkdown:   strings.Repeat("x", MaxRunTaskMarkdownLineBytes) + "\n",
		DelegatedActor: DelegatedActorV1{SubjectID: "user-1", SubjectType: PrincipalUser},
	}
	if err := ValidateRunAdmissionRequestV1(valid); err != nil {
		t.Fatalf("maximum parser-safe line rejected: %v", err)
	}
	invalid := valid
	invalid.TaskMarkdown = strings.Repeat("x", MaxRunTaskMarkdownLineBytes+1)
	if err := ValidateRunAdmissionRequestV1(invalid); err == nil {
		t.Fatal("task markdown line exceeding the pinned parser limit was accepted")
	}
}

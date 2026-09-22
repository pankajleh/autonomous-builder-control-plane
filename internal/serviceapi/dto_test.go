package serviceapi

import (
	"crypto/sha256"
	"encoding/hex"
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

func TestDevelopmentRunAdmissionValidatesExactCapsuleDigestAndBounds(t *testing.T) {
	markdown := strings.Repeat("x", MaxRunTaskMarkdownLineBytes) + "\n"
	sum := sha256.Sum256([]byte(markdown))
	valid := DevelopmentRunAdmissionRequestV1{
		SchemaVersion: 1, RequestID: "request-1", ProfileID: "repo-c-development-v1",
		DevelopmentCapsuleID: "capsule-004", DevelopmentSliceID: "slice-1",
		DevelopmentCapsuleSHA256: hex.EncodeToString(sum[:]), RepositoryBaseSHA: strings.Repeat("b", 40),
		TaskMarkdown: markdown, DelegatedActor: DelegatedActorV1{SubjectID: "user-1", SubjectType: PrincipalUser},
	}
	if err := ValidateDevelopmentRunAdmissionRequestV1(valid); err != nil {
		t.Fatalf("valid development request rejected: %v", err)
	}
	for name, mutate := range map[string]func(*DevelopmentRunAdmissionRequestV1){
		"capsule digest": func(request *DevelopmentRunAdmissionRequestV1) {
			request.DevelopmentCapsuleSHA256 = strings.Repeat("a", 64)
		},
		"line bound": func(request *DevelopmentRunAdmissionRequestV1) {
			request.TaskMarkdown = strings.Repeat("x", MaxRunTaskMarkdownLineBytes+1)
			sum := sha256.Sum256([]byte(request.TaskMarkdown))
			request.DevelopmentCapsuleSHA256 = hex.EncodeToString(sum[:])
		},
		"missing actor": func(request *DevelopmentRunAdmissionRequestV1) { request.DelegatedActor = DelegatedActorV1{} },
	} {
		t.Run(name, func(t *testing.T) {
			request := valid
			mutate(&request)
			if err := ValidateDevelopmentRunAdmissionRequestV1(request); err == nil {
				t.Fatal("invalid development admission accepted")
			}
		})
	}
}

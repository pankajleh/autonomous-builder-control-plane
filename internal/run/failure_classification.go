package run

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/blocker"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/recovery"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
)

// maxFailureDiagnosticBytes bounds the diagnostic excerpt passed to the
// classifier. Classification redacts and bounds again; this first bound keeps
// an evidence file from being read whole into memory.
const maxFailureDiagnosticBytes = 16 << 10

// humanDecisionRequirement is the controller-authored default requirement for a
// failure the classifier escalates to HUMAN_DECISION_REQUIRED. The operator is
// presented an explicit bounded question and must answer with one of the two
// accepted answers: "proceed" restarts the attempt, "abort" fails it. This is a
// conservative default and may be replaced by profile-specific policy under
// separate authority.
func humanDecisionRequirement() recovery.BlockerRequirement {
	return recovery.BlockerRequirement{HumanDecision: &recovery.HumanDecisionRequirement{
		Question:          "Proceed with the next attempt?",
		AcceptedAnswers:   []string{"proceed", "abort"},
		RequiredAuthority: "operator",
	}}
}

// classificationOfRalphexFailure derives a bounded, redacted failure input from
// one supervised Ralphex process result and classifies it with the existing
// deterministic classifier. Diagnostics come from the process error and the
// captured stderr tail, so classification reflects the implementer's own output
// rather than a guessed reason.
func classificationOfRalphexFailure(process supervisor.Result, processErr error) blocker.Classification {
	return blocker.Classify(blocker.FailureInput{
		Phase:       blocker.PhaseExecution,
		Outcome:     process.Outcome,
		ExitCode:    process.ExitCode,
		Unavailable: processErr != nil,
		Diagnostics: failureDiagnostics(process, processErr),
	})
}

func failureDiagnostics(process supervisor.Result, processErr error) string {
	var builder strings.Builder
	if processErr != nil {
		builder.WriteString(processErr.Error())
		builder.WriteByte('\n')
	}
	for _, ref := range []ledger.EvidenceRef{process.StderrRef, process.StdoutRef} {
		if ref.URI == "" || builder.Len() >= maxFailureDiagnosticBytes {
			continue
		}
		data, err := readBoundedEvidenceTail(ref, maxFailureDiagnosticBytes-builder.Len())
		if err != nil || len(data) == 0 {
			continue
		}
		builder.Write(data)
		builder.WriteByte('\n')
	}
	return builder.String()
}

// readBoundedEvidenceTail reads the tail of one captured evidence artifact,
// verifies its recorded SHA-256, and returns at most maximumBytes. A corrupt or
// unreadable artifact yields an empty result rather than classification input.
func readBoundedEvidenceTail(ref ledger.EvidenceRef, maximumBytes int) ([]byte, error) {
	if maximumBytes < 1 || ref.URI == "" {
		return nil, fmt.Errorf("invalid evidence tail read")
	}
	data, err := os.ReadFile(ref.URI)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != ref.SHA256 {
		return nil, fmt.Errorf("evidence SHA-256 mismatch")
	}
	if len(data) <= maximumBytes {
		return data, nil
	}
	return data[len(data)-maximumBytes:], nil
}

// humanDecisionBlockerBuild constructs the validated blocker decision from the
// raw Ralphex failure. NewBlockerDecision re-classifies its input, so this
// passes the raw diagnostics rather than the already-redacted classification
// excerpt: redaction would otherwise strip the very phrase that must reproduce
// the HUMAN_DECISION_REQUIRED classification.
func (r *Runner) humanDecisionBlockerBuild(from domain.State, process supervisor.Result, processErr error, refs []ledger.EvidenceRef) (recovery.BlockerDecision, error) {
	return recovery.NewBlockerDecision(recovery.BlockerDecisionInput{
		Attempt: recovery.AttemptIdentity{
			ProjectID: r.provenance.ProjectID,
			PlanID:    r.provenance.PlanID,
			RunID:     r.governed.RunID(),
			AttemptID: r.provenance.AttemptID,
		},
		StateFrom: from,
		Failure: blocker.FailureInput{
			Phase:       blocker.PhaseExecution,
			Outcome:     process.Outcome,
			ExitCode:    process.ExitCode,
			Unavailable: processErr != nil,
			Diagnostics: failureDiagnostics(process, processErr),
		},
		Requirement:   humanDecisionRequirement(),
		Actor:         actorController,
		Timestamp:     time.Now().UTC(),
		PolicyVersion: r.governed.PolicyVersion(),
		EvidenceRefs:  refs,
	})
}

package scheduler

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

func TestQueueDeterministicOrderingAndImmutableValues(t *testing.T) {
	repository := filepath.Clean(t.TempDir())
	acceptedAt := time.Date(2026, 9, 6, 10, 0, 0, 0, time.FixedZone("test", 2*60*60))
	firstInput := candidateInput(repository, "run-b", "attempt-1", strings.Repeat("a", 40), strings.Repeat("b", 40), acceptedAt)
	secondInput := candidateInput(repository, "run-a", "attempt-1", strings.Repeat("a", 40), strings.Repeat("c", 40), acceptedAt)
	first := mustCandidate(t, firstInput)
	second := mustCandidate(t, secondInput)

	// Mutating constructor input cannot mutate the accepted value.
	firstInput.AcceptanceEvidence[0].URI = "mutated"
	if first.Input().AcceptanceEvidence[0].URI == "mutated" {
		t.Fatal("candidate retained caller-owned evidence slice")
	}

	queue := NewQueue()
	if err := queue.Append(first); err != nil {
		t.Fatal(err)
	}
	if err := queue.Append(second); err != nil {
		t.Fatal(err)
	}
	snapshot := queue.Snapshot()
	if got := []string{snapshot[0].Input().RunID, snapshot[1].Input().RunID}; got[0] != "run-a" || got[1] != "run-b" {
		t.Fatalf("snapshot order = %v, want [run-a run-b]", got)
	}
	if snapshot[0].Input().AcceptedAt.Location() != time.UTC {
		t.Fatal("accepted timestamp was not canonicalized to UTC")
	}

	mutated := snapshot[0].Input()
	mutated.AcceptanceEvidence[0].URI = "mutated-again"
	snapshot[0].data.AcceptanceEvidence[0].URI = "mutated-in-package-test"
	if queue.Snapshot()[0].Input().AcceptanceEvidence[0].URI == "mutated-again" || queue.Snapshot()[0].Input().AcceptanceEvidence[0].URI == "mutated-in-package-test" {
		t.Fatal("snapshot exposed queue-owned mutable data")
	}
}

func TestQueueRejectsDuplicatesAndProvenanceReplayWithoutMutation(t *testing.T) {
	repository := filepath.Clean(t.TempDir())
	base := strings.Repeat("a", 40)
	original := mustCandidate(t, candidateInput(repository, "run-1", "attempt-1", base, strings.Repeat("b", 40), time.Unix(1, 0)))
	queue := NewQueue()
	if err := queue.Append(original); err != nil {
		t.Fatal(err)
	}
	if err := queue.Append(original); !errors.Is(err, ErrDuplicateCandidate) {
		t.Fatalf("duplicate append error = %v, want %v", err, ErrDuplicateCandidate)
	}

	replayedHeadInput := candidateInput(repository, "run-2", "attempt-1", base, strings.Repeat("b", 40), time.Unix(2, 0))
	replayedHead := mustCandidate(t, replayedHeadInput)
	if err := queue.Append(replayedHead); !errors.Is(err, ErrReplayCandidate) {
		t.Fatalf("head replay error = %v, want %v", err, ErrReplayCandidate)
	}

	replayedEvidenceInput := candidateInput(repository, "run-3", "attempt-1", base, strings.Repeat("c", 40), time.Unix(3, 0))
	replayedEvidenceInput.AcceptanceEvidence = original.Input().AcceptanceEvidence
	replayedEvidence := mustCandidate(t, replayedEvidenceInput)
	if err := queue.Append(replayedEvidence); !errors.Is(err, ErrReplayCandidate) {
		t.Fatalf("evidence replay error = %v, want %v", err, ErrReplayCandidate)
	}
	if queue.Len() != 1 {
		t.Fatalf("queue length = %d after rejected appends, want 1", queue.Len())
	}
}

func TestAcceptedCandidateRejectsIncompleteAcceptanceProvenance(t *testing.T) {
	repository := filepath.Clean(t.TempDir())
	valid := candidateInput(repository, "run-1", "attempt-1", strings.Repeat("a", 40), strings.Repeat("b", 40), time.Unix(1, 0))
	tests := map[string]func(*CandidateInput){
		"attempt":  func(input *CandidateInput) { input.AttemptID = "" },
		"evidence": func(input *CandidateInput) { input.AcceptanceEvidence = nil },
		"digest":   func(input *CandidateInput) { input.AcceptanceEvidence[0].SHA256 = "short" },
		"kind":     func(input *CandidateInput) { input.AcceptanceEvidence[0].Kind = "" },
		"policy":   func(input *CandidateInput) { input.AcceptancePolicyIdentity = "" },
		"time":     func(input *CandidateInput) { input.AcceptedAt = time.Time{} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := cloneCandidateInput(valid)
			mutate(&input)
			if _, err := NewAcceptedCandidate(input); err == nil {
				t.Fatal("incomplete acceptance provenance was accepted")
			}
		})
	}
}

func TestAcceptedCandidateRejectsUnsafeBranchNames(t *testing.T) {
	repository := filepath.Clean(t.TempDir())
	valid := candidateInput(repository, "run-1", "attempt-1", strings.Repeat("a", 40), strings.Repeat("b", 40), time.Unix(1, 0))
	for _, branch := range []string{"-unsafe", "topic/-unsafe"} {
		t.Run(branch, func(t *testing.T) {
			input := cloneCandidateInput(valid)
			input.Branch = branch
			_, err := NewAcceptedCandidate(input)
			if branch == "-unsafe" {
				if err == nil {
					t.Fatal("branch rejected by git check-ref-format --branch was accepted")
				}
				return
			}
			if err != nil {
				t.Fatalf("valid branch %q rejected: %v", branch, err)
			}
		})
	}
}

func candidateInput(repository, runID, attemptID, startSHA, headSHA string, acceptedAt time.Time) CandidateInput {
	digest := strings.Repeat("d", 64)
	return CandidateInput{
		ProjectID: "project", PlanID: "plan", RunID: runID, AttemptID: attemptID,
		Repository: repository, Branch: "candidate/" + runID, StartSHA: startSHA, HeadSHA: headSHA,
		AcceptanceEvidence: []ledger.EvidenceRef{{URI: "evidence/" + runID + "/" + attemptID + "/" + headSHA, SHA256: digest, Kind: "acceptance-result"}},
		AcceptedAt:         acceptedAt, AcceptancePolicyIdentity: "acceptance-policy-v1",
	}
}

func mustCandidate(t *testing.T, input CandidateInput) AcceptedCandidate {
	t.Helper()
	candidate, err := NewAcceptedCandidate(input)
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

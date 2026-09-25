package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/recovery"
)

// resumeAnswer is the durable answer a human recorded for one paused run.
type resumeAnswer struct {
	proceed   bool
	decisionID string
}

// resumeFromHumanDecision re-enters a run that paused at HUMAN_DECISION_REQUIRED.
// It reads the recorded decision from the authoritative ledger and performs the
// resume transition with the runner's own transition machinery (which resolves
// the run-transition barrier correctly):
//
//   - "proceed" -> HUMAN_DECISION_REQUIRED -> AUTHORITY_VALIDATED (result carries
//     StateAuthorityValidated so the caller continues implementation);
//   - "abort"   -> HUMAN_DECISION_REQUIRED -> FAILED (result carries StateFailed).
//
// A run that is not paused, or that has no matching recorded decision, fails
// closed rather than resuming.
func (r *Runner) resumeFromHumanDecision(ctx context.Context, result Result) (Result, error) {
	if ctx == nil {
		return result, errors.New("context is required")
	}
	snapshotter, ok := r.events.(eventSnapshotter)
	if !ok {
		return result, errors.New("durable ledger snapshot is required for resume")
	}
	current, err := r.currentStateUnderTransitionLease(snapshotter)
	if err != nil {
		return result, fmt.Errorf("resolve current run state for resume: %w", err)
	}
	if current != domain.StateHumanDecisionRequired {
		return result, fmt.Errorf("resume requires %s, found %s", domain.StateHumanDecisionRequired, current)
	}
	events, err := readRunEvents(snapshotter)
	if err != nil {
		return result, fmt.Errorf("read run events for resume: %w", err)
	}
	// The resume attempt re-runs Ralphex and therefore writes fresh evidence; a
	// later attempt must not collide with earlier attempt evidence. The ordinal
	// is one greater than the number of prior blocker decisions.
	ordinal := 1
	for _, event := range events {
		if event.EventType == recovery.EventBlockerDecision {
			ordinal++
		}
	}
	r.resumeOrdinal = ordinal
	answer, err := pendingResumeAnswer(events)
	if err != nil {
		return result, err
	}
	if !answer.proceed {
		result.State = domain.StateFailed
		result.FailureReason = "human decision aborted the attempt"
		if err := r.transition(ctx, domain.StateHumanDecisionRequired, domain.StateFailed, "resume-controller",
			map[string]any{"answer": "abort", "decision_request_id": answer.decisionID}, nil); err != nil {
			return result, err
		}
		return result, nil
	}
	result.State = domain.StateAuthorityValidated
	if err := r.transition(ctx, domain.StateHumanDecisionRequired, domain.StateAuthorityValidated, "resume-controller",
		map[string]any{"answer": "proceed", "decision_request_id": answer.decisionID}, nil); err != nil {
		return result, err
	}
	return result, nil
}

// readRunEvents reads and validates the complete event history from one snapshot.
func readRunEvents(snapshotter eventSnapshotter) ([]ledger.Event, error) {
	data, _, err := snapshotter.Snapshot()
	if err != nil || len(data) == 0 || data[len(data)-1] != '\n' {
		return nil, errors.Join(errors.New("run snapshot is unavailable"), err)
	}
	scanner := newEventScanner(data)
	var events []ledger.Event
	seen := make(map[string]struct{})
	for scanner.Scan() {
		var event ledger.Event
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Validate() != nil {
			return nil, errors.New("run snapshot contains an invalid event")
		}
		if _, duplicate := seen[event.EventID]; duplicate {
			return nil, errors.New("run snapshot contains a duplicate event")
		}
		seen[event.EventID] = struct{}{}
		events = append(events, event)
	}
	if scanner.Err() != nil {
		return nil, scanner.Err()
	}
	return events, nil
}

// pendingResumeAnswer finds the most recent open blocker decision and the human
// answer recorded for it. It fails closed when the blocker and answer do not
// pair, or when an answer is not one of the two accepted values.
func pendingResumeAnswer(events []ledger.Event) (resumeAnswer, error) {
	var blockerID string
	var blockerSeen bool
	var answer *resumeAnswer
	for index := range events {
		event := events[index]
		switch event.EventType {
		case recovery.EventBlockerDecision:
			if blockerSeen {
				return resumeAnswer{}, errors.New("multiple blocker decisions are unresolved")
			}
			blockerID = event.EventID
			blockerSeen = true
		case "API_HUMAN_DECISION_RECORDED":
			requestID, _ := event.Payload["decision_request_id"].(string)
			if requestID != "" && requestID == blockerID {
				value, _ := event.Payload["answer"].(string)
				switch value {
				case "proceed":
					answer = &resumeAnswer{proceed: true, decisionID: requestID}
				case "abort":
					answer = &resumeAnswer{proceed: false, decisionID: requestID}
				default:
					return resumeAnswer{}, fmt.Errorf("human decision answer %q is not accepted", value)
				}
			}
		}
	}
	if !blockerSeen || blockerID == "" {
		return resumeAnswer{}, errors.New("no blocker decision is recorded for this run")
	}
	if answer == nil {
		return resumeAnswer{}, errors.New("no human decision is recorded for the open blocker")
	}
	return *answer, nil
}

// newEventScanner wraps bytes.Reader as a scanner-compatible line reader.
type eventScanner struct {
	lines [][]byte
	index int
}

func newEventScanner(data []byte) *eventScanner {
	var lines [][]byte
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) > 0 {
			lines = append(lines, line)
		}
	}
	return &eventScanner{lines: lines}
}

func (s *eventScanner) Scan() bool {
	if s.index >= len(s.lines) {
		return false
	}
	s.index++
	return true
}

func (s *eventScanner) Bytes() []byte { return s.lines[s.index-1] }
func (s *eventScanner) Err() error    { return nil }

// ralphexArtifactName scopes per-attempt Ralphex evidence so a resume attempt
// does not collide with immutable evidence from an earlier attempt. Attempt 1
// keeps the historical names.
func (r *Runner) ralphexArtifactName(base string) string {
	if r.resume && r.resumeOrdinal > 1 {
		return fmt.Sprintf("%s-attempt-%d%s",
			strings.TrimSuffix(base, pathExt(base)), r.resumeOrdinal, pathExt(base))
	}
	return base
}

func pathExt(name string) string {
	if index := strings.LastIndex(name, "."); index >= 0 {
		return name[index:]
	}
	return ""
}

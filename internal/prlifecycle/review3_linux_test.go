//go:build linux

package prlifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func requireLifecycleCode(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), code) {
		t.Fatalf("wanted %s, got %v", code, err)
	}
}

func review3Key(t *testing.T, controller *Controller, request Request) PRResourceKeyV1 {
	t.Helper()
	key, err := NewPRResourceKey(request.Authority.Repository(), request.Authority.BaseBranch(), request.Authority.HeadBranch())
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func confirmThenAbandon(t *testing.T, mutation string) (*Controller, *review2Fixture, Request, Request, PRResourceKeyV1, func()) {
	t.Helper()
	authority := lifecycleAuthority(t)
	fixture := newReview2Fixture(authority)
	controller, _, _, server := makeCorrectionController(t, authority, fixture, "review3-"+mutation, time.Now, nil)
	first := Request{RunID: "review3-" + mutation, Authority: authority, Title: "first", Body: "body"}
	second := Request{RunID: "review3-" + mutation, Authority: authority, Title: "second", Body: "body"}
	if _, err := controller.Upsert(context.Background(), first); err != nil {
		server.Close()
		t.Fatal(err)
	}
	controller.github.writeConstructionHook = func() error { return errors.New("abandon after revision") }
	if _, err := controller.Upsert(context.Background(), second); err == nil {
		server.Close()
		t.Fatal("write-construction fault did not abandon revision")
	}
	controller.github.writeConstructionHook = nil
	return controller, fixture, first, second, review3Key(t, controller, first), server.Close
}

func TestAbandonedRevisionUsesResourceBarrier(t *testing.T) {
	for _, mutation := range []string{"closed", "open", "document"} {
		t.Run(mutation, func(t *testing.T) {
			controller, fixture, first, _, key, closeServer := confirmThenAbandon(t, mutation)
			defer closeServer()
			fixture.mu.Lock()
			listBefore, createsBefore := fixture.listReads, fixture.prCreates
			switch mutation {
			case "closed":
				fixture.state = "closed"
			case "document":
				fixture.title = "remote edit"
			}
			fixture.mu.Unlock()
			third := Request{RunID: controller.artifacts.RunID(), Authority: first.Authority, Title: "third", Body: "body"}
			result, err := controller.Upsert(context.Background(), third)
			switch mutation {
			case "closed":
				requireLifecycleCode(t, err, CodeExistingIneligible)
			case "document":
				requireLifecycleCode(t, err, CodeRevisionConflict)
			default:
				if err != nil || result.Core().Revision != 3 || result.Core().PRNumber != 7 || result.Core().PRNodeID != "PR_7" {
					t.Fatalf("barrier update failed: %#v %v", result.Core(), err)
				}
			}
			fixture.mu.Lock()
			writes, creates, lists := fixture.writes, fixture.prCreates, fixture.listReads
			fixture.mu.Unlock()
			wantWrites := 1
			if mutation == "open" {
				wantWrites = 2
			}
			if writes != wantWrites || creates != createsBefore || lists != listBefore {
				t.Fatalf("barrier path changed submission/discovery: writes=%d creates=%d lists=%d/%d", writes, creates, lists, listBefore)
			}
			if _, err := os.Stat(filepath.Join(controller.store.Root(), recordPrefix(key, 2)+"superseded.json")); err != nil {
				t.Fatal("abandoned revision was not durably superseded")
			}
			for _, kind := range []string{"generation.json", "submitted.json", "terminal.json"} {
				if mutation != "open" {
					if _, statErr := os.Stat(filepath.Join(controller.store.Root(), recordPrefix(key, 3)+kind)); !os.IsNotExist(statErr) {
						t.Fatalf("failed barrier path created rev3 %s", kind)
					}
				}
				if _, statErr := os.Stat(filepath.Join(controller.store.Root(), recordPrefix(key, 2)+kind)); !os.IsNotExist(statErr) {
					t.Fatalf("abandoned revision created %s", kind)
				}
			}
			if mutation != "open" {
				matches, _ := filepath.Glob(filepath.Join(controller.store.Root(), recordPrefix(key, 3)+"prepare-run-*.json"))
				if len(matches) != 1 {
					t.Fatalf("failed barrier path prepare record count = %d", len(matches))
				}
			} else {
				terminals, _ := filepath.Glob(filepath.Join(controller.store.Root(), "*-terminal.json"))
				if len(terminals) != 2 {
					t.Fatalf("terminal count = %d", len(terminals))
				}
				for _, path := range terminals {
					data, readErr := os.ReadFile(path)
					var terminal terminalV1
					if readErr != nil || strictJSON(data, &terminal) != nil || terminal.Core.ResultCore.PRNumber != 7 || terminal.Core.ResultCore.PRNodeID != "PR_7" {
						t.Fatalf("terminal changed physical PR identity: %s", path)
					}
				}
			}
		})
	}
}

func TestSameRequestReentryUsesResourceBarrier(t *testing.T) {
	for _, mutation := range []string{"document", "closed", "open"} {
		t.Run(mutation, func(t *testing.T) {
			controller, fixture, _, second, _, closeServer := confirmThenAbandon(t, "reentry-"+mutation)
			defer closeServer()
			fixture.mu.Lock()
			listBefore := fixture.listReads
			switch mutation {
			case "document":
				fixture.title = "remote edit"
			case "closed":
				fixture.state = "closed"
			}
			fixture.mu.Unlock()
			result, err := controller.Upsert(context.Background(), second)
			switch mutation {
			case "document":
				requireLifecycleCode(t, err, CodeRevisionConflict)
			case "closed":
				requireLifecycleCode(t, err, CodeExistingIneligible)
			default:
				if err != nil || result.Core().Revision != 2 || result.Core().PRNumber != 7 {
					t.Fatalf("same request did not resume abandoned revision as UPDATE: %#v %v", result.Core(), err)
				}
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			wantWrites := 1
			if mutation == "open" {
				wantWrites = 2
			}
			if fixture.writes != wantWrites || fixture.prCreates != 1 || fixture.listReads != listBefore {
				t.Fatalf("re-entry changed submission/discovery path: writes=%d creates=%d lists=%d/%d", fixture.writes, fixture.prCreates, fixture.listReads, listBefore)
			}
		})
	}
}

func TestSameRequestReentryRejectsLegacyCreateAboveBarrier(t *testing.T) {
	controller, fixture, _, second, key, closeServer := confirmThenAbandon(t, "legacy-create")
	defer closeServer()
	path := filepath.Join(controller.store.Root(), recordPrefix(key, 2)+"revision.json")
	data, err := os.ReadFile(path)
	var revision revisionRecord
	if err != nil || strictJSON(data, &revision) != nil {
		t.Fatal("cannot read abandoned revision")
	}
	revision.Mode, revision.PRNumber, revision.PRNodeID = "CREATE", 0, ""
	data, _ = json.Marshal(revision)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	before := fixture.requests
	fixture.mu.Unlock()
	_, err = controller.Upsert(context.Background(), second)
	requireLifecycleCode(t, err, CodeIntegrityFailure)
	fixture.mu.Lock()
	after := fixture.requests
	fixture.mu.Unlock()
	if after != before {
		t.Fatalf("legacy CREATE guard made %d GitHub requests", after-before)
	}
}

func TestFirstRevisionReentryStillDiscovers(t *testing.T) {
	authority := lifecycleAuthority(t)
	fixture := newReview2Fixture(authority)
	controller, _, _, server := makeCorrectionController(t, authority, fixture, "no-barrier", time.Now, nil)
	defer server.Close()
	request := Request{RunID: "no-barrier", Authority: authority, Title: "first", Body: "body"}
	controller.github.writeConstructionHook = func() error { return errors.New("abandon first revision") }
	if _, err := controller.Upsert(context.Background(), request); err == nil {
		t.Fatal("first revision was not abandoned")
	}
	controller.github.writeConstructionHook = nil
	fixture.mu.Lock()
	listsBefore := fixture.listReads
	fixture.mu.Unlock()
	if _, err := controller.Upsert(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.listReads <= listsBefore || fixture.prCreates != 1 {
		t.Fatalf("no-barrier re-entry skipped discovery: lists=%d/%d creates=%d", listsBefore, fixture.listReads, fixture.prCreates)
	}
}

func TestMarkerAbsentResumeUsesResourceBarrier(t *testing.T) {
	for _, mutation := range []string{"document", "open"} {
		t.Run(mutation, func(t *testing.T) {
			authority := lifecycleAuthority(t)
			fixture := newReview2Fixture(authority)
			controller, _, _, server := makeCorrectionController(t, authority, fixture, "resume-"+mutation, time.Now, nil)
			defer server.Close()
			first := Request{RunID: "resume-" + mutation, Authority: authority, Title: "first", Body: "body"}
			second := Request{RunID: "resume-" + mutation, Authority: authority, Title: "second", Body: "body"}
			if _, err := controller.Upsert(context.Background(), first); err != nil {
				t.Fatal(err)
			}
			controller.beforeMarker = func() error { return errors.New("before marker") }
			if _, err := controller.Upsert(context.Background(), second); err == nil {
				t.Fatal("before-marker fault did not stop submission")
			}
			controller.beforeMarker = nil
			fixture.mu.Lock()
			listBefore := fixture.listReads
			if mutation == "document" {
				fixture.title = "remote edit"
			}
			fixture.mu.Unlock()
			result, err := controller.Upsert(context.Background(), second)
			if mutation == "document" {
				requireLifecycleCode(t, err, CodeRevisionConflict)
			} else if err != nil || result.Core().Revision != 2 || result.Core().PRNumber != 7 {
				t.Fatalf("marker-absent resume failed: %#v %v", result.Core(), err)
			}
			key := review3Key(t, controller, first)
			marker := filepath.Join(controller.store.Root(), recordPrefix(key, 2)+"submitted.json")
			_, statErr := os.Stat(marker)
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			wantWrites := 1
			if mutation == "open" {
				wantWrites = 2
				if statErr != nil {
					t.Fatal("resume did not create submitted marker")
				}
			} else if !os.IsNotExist(statErr) {
				t.Fatal("failed resume created submitted marker")
			}
			if fixture.writes != wantWrites || fixture.listReads != listBefore {
				t.Fatalf("resume changed submission/discovery: writes=%d lists=%d/%d", fixture.writes, fixture.listReads, listBefore)
			}
		})
	}
}

func TestMarkerPresentResumeDoesNotLoadBarrier(t *testing.T) {
	authority := lifecycleAuthority(t)
	fixture := newReview2Fixture(authority)
	now := time.Unix(1700100000, 0).UTC()
	controller, _, _, server := makeCorrectionController(t, authority, fixture, "marker-present", func() time.Time { return now }, nil)
	defer server.Close()
	first := Request{RunID: "marker-present", Authority: authority, Title: "first", Body: "body"}
	second := Request{RunID: "marker-present", Authority: authority, Title: "second", Body: "body"}
	if _, err := controller.Upsert(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	controller.postSubmitFailure = func(stage string) error {
		if stage == "terminal" {
			return errors.New("stop before terminal")
		}
		return nil
	}
	if _, err := controller.Upsert(context.Background(), second); err == nil {
		t.Fatal("post-submit terminal fault did not fire")
	}
	controller.postSubmitFailure = nil
	key := review3Key(t, controller, first)
	barrierPath := filepath.Join(controller.store.Root(), recordPrefix(key, 1)+"terminal.json")
	if err := os.WriteFile(barrierPath, []byte(`{"corrupt":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	before := fixture.requests
	fixture.mu.Unlock()
	_, err := controller.Upsert(context.Background(), second)
	requireLifecycleCode(t, err, CodeReconcileBudgetExhausted)
	fixture.mu.Lock()
	after := fixture.requests
	fixture.mu.Unlock()
	if after != before {
		t.Fatalf("marker-present resume made %d remote calls", after-before)
	}
}

func writeAbandonedRevision(t *testing.T, controller *Controller, key PRResourceKeyV1, ordinal uint64, template revisionRecord, requestSHA string) {
	t.Helper()
	template.Ordinal = ordinal
	template.RequestSHA256 = requestSHA
	data, err := json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controller.store.Root(), recordPrefix(key, ordinal)+"revision.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestNonConfirmedLowerBarrierCannotBeBypassed(t *testing.T) {
	authority := lifecycleAuthority(t)
	fixture := newReview2Fixture(authority)
	fixture.principalMismatchAfter = true
	controller, _, _, server := makeCorrectionController(t, authority, fixture, "nonconfirmed", time.Now, nil)
	defer server.Close()
	first := Request{RunID: "nonconfirmed", Authority: authority, Title: "first", Body: "body"}
	result, firstErr := controller.Upsert(context.Background(), first)
	requireLifecycleCode(t, firstErr, CodeRemoteDivergedAfterWrite)
	key := review3Key(t, controller, first)
	revision1, err := readRevision(&resourceTxn{store: controller.store, key: key}, 1)
	if err != nil {
		t.Fatal(err)
	}
	writeAbandonedRevision(t, controller, key, 2, revision1, digestBytes([]byte("different-abandoned-request")))
	fixture.mu.Lock()
	beforeRequests, beforeWrites := fixture.requests, fixture.writes
	fixture.mu.Unlock()
	third := Request{RunID: "nonconfirmed", Authority: authority, Title: "third", Body: "body"}
	_, err = controller.Upsert(context.Background(), third)
	requireLifecycleCode(t, err, CodeRevisionConflict)
	fixture.mu.Lock()
	afterRequests, afterWrites := fixture.requests, fixture.writes
	fixture.mu.Unlock()
	if afterRequests != beforeRequests || afterWrites != beforeWrites {
		t.Fatal("non-confirmed barrier contacted GitHub")
	}
	recovered, replayErr := controller.Upsert(context.Background(), first)
	requireLifecycleCode(t, replayErr, CodeRemoteDivergedAfterWrite)
	fixture.mu.Lock()
	finalRequests := fixture.requests
	fixture.mu.Unlock()
	if recovered.TerminalSHA256() != result.TerminalSHA256() || finalRequests != afterRequests {
		t.Fatal("zero-write lower replay did not recover the identical divergence terminal")
	}
}

func TestLowerBarrierReplayBlockedByLaterGeneration(t *testing.T) {
	for _, markerPresent := range []bool{false, true} {
		t.Run(map[bool]string{false: "without-marker", true: "with-marker"}[markerPresent], func(t *testing.T) {
			authority := lifecycleAuthority(t)
			fixture := newReview2Fixture(authority)
			controller, _, _, server := makeCorrectionController(t, authority, fixture, "lower-replay", time.Now, nil)
			defer server.Close()
			first := Request{RunID: "lower-replay", Authority: authority, Title: "first", Body: "body"}
			second := Request{RunID: "lower-replay", Authority: authority, Title: "second", Body: "body"}
			if _, err := controller.Upsert(context.Background(), first); err != nil {
				t.Fatal(err)
			}
			if markerPresent {
				controller.postSubmitFailure = func(stage string) error {
					if stage == "terminal" {
						return errors.New("stop after marker")
					}
					return nil
				}
			} else {
				controller.beforeMarker = func() error { return errors.New("stop before marker") }
			}
			if _, err := controller.Upsert(context.Background(), second); err == nil {
				t.Fatal("generation fault did not fire")
			}
			controller.postSubmitFailure, controller.beforeMarker = nil, nil
			fixture.mu.Lock()
			before := fixture.requests
			fixture.mu.Unlock()
			_, err := controller.Upsert(context.Background(), first)
			requireLifecycleCode(t, err, CodeRevisionConflict)
			fixture.mu.Lock()
			after := fixture.requests
			fixture.mu.Unlock()
			if after != before {
				t.Fatalf("older terminal replay made %d remote calls", after-before)
			}
		})
	}
}

func rewriteTerminal(t *testing.T, path string, mutate func(*terminalV1)) {
	t.Helper()
	data, err := os.ReadFile(path)
	var terminal terminalV1
	if err != nil || strictJSON(data, &terminal) != nil {
		t.Fatalf("read terminal for mutation: %v", err)
	}
	mutate(&terminal)
	resultBytes, _ := terminal.Core.ResultCore.CanonicalJSON()
	if len(resultBytes) > 0 {
		terminal.Core.ResultCoreSHA256 = digestBytes(resultBytes)
	}
	coreBytes, err := json.Marshal(terminal.Core)
	if err != nil {
		t.Fatal(err)
	}
	terminal.TerminalCoreSHA256 = digestBytes(coreBytes)
	_, eventBytes, err := deterministicTerminalEvent(terminal.Core)
	if err != nil {
		t.Fatal(err)
	}
	terminal.MaterialEvent = eventBytes
	terminal.MaterialEventSHA = digestBytes(eventBytes)
	data, err = json.Marshal(terminal)
	if err != nil || os.WriteFile(path, data, 0o600) != nil {
		t.Fatalf("write mutated terminal: %v", err)
	}
}

func TestLowerBarrierAuthentication(t *testing.T) {
	for _, kind := range []string{"generation", "submitted", "revision", "terminal"} {
		t.Run(kind, func(t *testing.T) {
			controller, fixture, first, _, key, closeServer := confirmThenAbandon(t, "tamper-"+kind)
			defer closeServer()
			path := filepath.Join(controller.store.Root(), recordPrefix(key, 1)+kind+".json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, append(data, ' '), 0o600); err != nil {
				t.Fatal(err)
			}
			fixture.mu.Lock()
			before := fixture.requests
			fixture.mu.Unlock()
			request := Request{RunID: controller.artifacts.RunID(), Authority: first.Authority, Title: "third", Body: "body"}
			_, err = controller.Upsert(context.Background(), request)
			requireLifecycleCode(t, err, CodeIntegrityFailure)
			fixture.mu.Lock()
			after := fixture.requests
			fixture.mu.Unlock()
			if after != before {
				t.Fatalf("tampered %s caused remote requests", kind)
			}
		})
	}
}

func TestLowerBarrierPolicyAndAuthorityGates(t *testing.T) {
	for _, mutation := range []string{"policy", "limits", "repository", "base", "head"} {
		t.Run(mutation, func(t *testing.T) {
			controller, fixture, first, _, key, closeServer := confirmThenAbandon(t, "gate-"+mutation)
			defer closeServer()
			terminalPath := filepath.Join(controller.store.Root(), recordPrefix(key, 1)+"terminal.json")
			rewriteTerminal(t, terminalPath, func(terminal *terminalV1) {
				switch mutation {
				case "policy":
					terminal.Core.PolicySHA256 = strings.Repeat("0", 64)
				case "limits":
					terminal.Core.LimitsSHA256 = strings.Repeat("0", 64)
				case "repository":
					terminal.Core.ResultCore.Repository = "other/repository"
				case "base":
					terminal.Core.ResultCore.BaseBranch = "other-base"
				case "head":
					terminal.Core.ResultCore.HeadBranch = "other-head"
				}
			})
			fixture.mu.Lock()
			before := fixture.requests
			fixture.mu.Unlock()
			_, err := controller.Upsert(context.Background(), Request{RunID: controller.artifacts.RunID(), Authority: first.Authority, Title: "third", Body: "body"})
			if mutation == "policy" || mutation == "limits" {
				requireLifecycleCode(t, err, CodePolicyMismatch)
			} else {
				requireLifecycleCode(t, err, CodeIntegrityFailure)
			}
			fixture.mu.Lock()
			after := fixture.requests
			fixture.mu.Unlock()
			if after != before {
				t.Fatal("local barrier gate contacted GitHub")
			}
		})
	}
}

func TestBarrierPathReadsOneTerminalAndNoDiscovery(t *testing.T) {
	controller, fixture, first, _, _, closeServer := confirmThenAbandon(t, "bounded-reads")
	defer closeServer()
	terminalReads := 0
	controller.store.afterRecordRead = func(name string) {
		if strings.HasSuffix(name, "-terminal.json") {
			terminalReads++
		}
	}
	fixture.mu.Lock()
	listBefore, requestsBefore, prReadsBefore := fixture.listReads, fixture.requests, fixture.prReads
	fixture.mu.Unlock()
	request := Request{RunID: controller.artifacts.RunID(), Authority: first.Authority, Title: "third", Body: "body"}
	if _, err := controller.Upsert(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	listAfter, requestsAfter, prReadsAfter := fixture.listReads, fixture.requests, fixture.prReads
	fixture.mu.Unlock()
	if terminalReads != 1 || listAfter != listBefore || requestsAfter-requestsBefore != 9 || prReadsAfter-prReadsBefore != 2 {
		t.Fatalf("unbounded barrier path: terminal=%d lists=%d/%d requests=%d prReads=%d", terminalReads, listBefore, listAfter, requestsAfter-requestsBefore, prReadsAfter-prReadsBefore)
	}
}

func seedStructuralRecord(t *testing.T, store *PRWriteAdmissionStore, key PRResourceKeyV1, ordinal uint64, kind string) {
	t.Helper()
	path := filepath.Join(store.Root(), recordPrefix(key, ordinal)+kind+".json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestResourceStructuralScanRejectsImpossibleStates(t *testing.T) {
	tests := map[string]func(*testing.T, *PRWriteAdmissionStore, PRResourceKeyV1){
		"generation_below_max": func(t *testing.T, store *PRWriteAdmissionStore, key PRResourceKeyV1) {
			seedStructuralRecord(t, store, key, 1, "revision")
			seedStructuralRecord(t, store, key, 1, "generation")
			seedStructuralRecord(t, store, key, 2, "revision")
		},
		"submitted_without_generation": func(t *testing.T, store *PRWriteAdmissionStore, key PRResourceKeyV1) {
			seedStructuralRecord(t, store, key, 1, "revision")
			seedStructuralRecord(t, store, key, 1, "submitted")
		},
		"terminal_without_submitted": func(t *testing.T, store *PRWriteAdmissionStore, key PRResourceKeyV1) {
			seedStructuralRecord(t, store, key, 1, "revision")
			seedStructuralRecord(t, store, key, 1, "generation")
			seedStructuralRecord(t, store, key, 1, "terminal")
		},
		"revision_gap": func(t *testing.T, store *PRWriteAdmissionStore, key PRResourceKeyV1) {
			seedStructuralRecord(t, store, key, 1, "revision")
			seedStructuralRecord(t, store, key, 1, "superseded")
			seedStructuralRecord(t, store, key, 3, "revision")
		},
		"superseded_generation": func(t *testing.T, store *PRWriteAdmissionStore, key PRResourceKeyV1) {
			seedStructuralRecord(t, store, key, 1, "revision")
			seedStructuralRecord(t, store, key, 1, "generation")
			seedStructuralRecord(t, store, key, 1, "superseded")
		},
	}
	for name, seed := range tests {
		t.Run(name, func(t *testing.T) {
			authority := lifecycleAuthority(t)
			fixture := newReview2Fixture(authority)
			store := provisionStore(t)
			key, _ := NewPRResourceKey(authority.Repository(), authority.BaseBranch(), authority.HeadBranch())
			seed(t, store, key)
			controller, _, _, server := makeCorrectionController(t, authority, fixture, "scan-"+name, time.Now, store)
			defer server.Close()
			_, err := controller.Upsert(context.Background(), Request{RunID: "scan-" + name, Authority: authority, Title: "title", Body: "body"})
			requireLifecycleCode(t, err, CodeIntegrityFailure)
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if fixture.requests != 0 {
				t.Fatalf("structural failure made %d GitHub requests", fixture.requests)
			}
		})
	}
}

func TestPrepareOnlyOrdinalsDoNotEnterResourceState(t *testing.T) {
	authority := lifecycleAuthority(t)
	fixture := newReview2Fixture(authority)
	store := provisionStore(t)
	key, _ := NewPRResourceKey(authority.Repository(), authority.BaseBranch(), authority.HeadBranch())
	seedPrepareFailures(t, store, key, 1, MaxPrepareHistoryRecordsPerPendingRevision)
	controller, _, _, server := makeCorrectionController(t, authority, fixture, "prepare-only", time.Now, store)
	defer server.Close()
	_, err := controller.Upsert(context.Background(), Request{RunID: "prepare-only", Authority: authority, Title: "title", Body: "body"})
	requireLifecycleCode(t, err, CodePreflightHistoryExhausted)
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.requests != 0 {
		t.Fatalf("prepare-only state made %d remote requests", fixture.requests)
	}
}

func TestNonMatchingLeftoverOnlyBlocksCreation(t *testing.T) {
	authority := lifecycleAuthority(t)
	fixture := newReview2Fixture(authority)
	controller, _, _, server := makeCorrectionController(t, authority, fixture, "leftover", time.Now, nil)
	defer server.Close()
	first := Request{RunID: "leftover", Authority: authority, Title: "first", Body: "body"}
	original, err := controller.Upsert(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	key := review3Key(t, controller, first)
	leftover := filepath.Join(controller.store.Root(), recordPrefix(key, 1)+"terminal.json.saved")
	if err := os.WriteFile(leftover, []byte("leftover"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	before := fixture.requests
	fixture.mu.Unlock()
	replayed, err := controller.Upsert(context.Background(), first)
	if err != nil || replayed.TerminalSHA256() != original.TerminalSHA256() {
		t.Fatalf("read-only replay rejected non-matching leftover: %v", err)
	}
	fixture.mu.Lock()
	afterReplay := fixture.requests
	fixture.mu.Unlock()
	if afterReplay != before {
		t.Fatal("read-only replay contacted GitHub")
	}
	_, err = controller.Upsert(context.Background(), Request{RunID: "leftover", Authority: authority, Title: "second", Body: "body"})
	requireLifecycleCode(t, err, CodeIntegrityFailure)
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.writes != 1 {
		t.Fatal("non-matching leftover allowed a remote write")
	}
}

func TestSupersededRevisionCannotBeResurrected(t *testing.T) {
	controller, fixture, first, second, key, closeServer := confirmThenAbandon(t, "resurrection")
	defer closeServer()
	fixture.mu.Lock()
	fixture.state = "closed"
	fixture.mu.Unlock()
	third := Request{RunID: controller.artifacts.RunID(), Authority: first.Authority, Title: "third", Body: "body"}
	_, err := controller.Upsert(context.Background(), third)
	requireLifecycleCode(t, err, CodeExistingIneligible)
	supersededPath := filepath.Join(controller.store.Root(), recordPrefix(key, 2)+"superseded.json")
	firstBytes, err := os.ReadFile(supersededPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(controller.store.Root(), recordPrefix(key, 3)+"revision.json")); !os.IsNotExist(statErr) {
		t.Fatal("failed third request created revision 3")
	}
	fixture.mu.Lock()
	fixture.state, fixture.title = "open", "first"
	beforeRequests, beforeWrites := fixture.requests, fixture.writes
	fixture.mu.Unlock()
	_, err = controller.Upsert(context.Background(), second)
	requireLifecycleCode(t, err, CodeRevisionConflict)
	fixture.mu.Lock()
	afterRequests, afterWrites := fixture.requests, fixture.writes
	fixture.mu.Unlock()
	if afterRequests != beforeRequests || afterWrites != beforeWrites {
		t.Fatal("superseded request contacted GitHub or wrote")
	}
	if _, statErr := os.Stat(filepath.Join(controller.store.Root(), recordPrefix(key, 2)+"generation.json")); !os.IsNotExist(statErr) {
		t.Fatal("superseded request gained a generation")
	}
	fourth := Request{RunID: controller.artifacts.RunID(), Authority: first.Authority, Title: "fourth", Body: "body"}
	result, err := controller.Upsert(context.Background(), fourth)
	if err != nil || result.Core().Revision != 3 || result.Core().PRNumber != 7 {
		t.Fatalf("later differing request did not advance: %#v %v", result.Core(), err)
	}
	secondBytes, err := os.ReadFile(supersededPath)
	if err != nil || string(firstBytes) != string(secondBytes) {
		t.Fatal("re-supersede changed immutable settlement bytes")
	}
	structural, _ := filepath.Glob(filepath.Join(controller.store.Root(), recordPrefix(key, 2)+"*.json"))
	structuralKinds := 0
	for _, path := range structural {
		base := filepath.Base(path)
		if strings.HasSuffix(base, "-revision.json") || strings.HasSuffix(base, "-superseded.json") || strings.HasSuffix(base, "-generation.json") || strings.HasSuffix(base, "-submitted.json") || strings.HasSuffix(base, "-terminal.json") {
			structuralKinds++
		}
	}
	if structuralKinds != 2 {
		t.Fatalf("abandoned ordinal structural record count = %d", structuralKinds)
	}
}

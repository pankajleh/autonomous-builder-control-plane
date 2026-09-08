//go:build linux

package prlifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

type review2Fixture struct {
	mu                     sync.Mutex
	headSHA                string
	exists                 bool
	writes                 int
	prCreates              int
	prReads                int
	requests               int
	listReads              int
	title                  string
	body                   string
	state                  string
	merged                 bool
	nodeID                 string
	baseOwner              string
	headOwner              string
	baseRef                string
	headRef                string
	headLabel              string
	principalFailures      []string
	principalMismatchAfter bool
}

func newReview2Fixture(authority githublifecycle.Authority) *review2Fixture {
	return &review2Fixture{
		headSHA: authority.HeadSHA().String(), state: "open", nodeID: "PR_7",
		baseOwner: "octo", headOwner: "octo", baseRef: "main", headRef: "feature", headLabel: "octo:feature",
	}
}

func (f *review2Fixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/user":
		if f.exists && len(f.principalFailures) > 0 {
			failure := f.principalFailures[0]
			f.principalFailures = f.principalFailures[1:]
			if failure == "malformed" {
				_, _ = io.WriteString(w, `{broken`)
				return
			}
			if failure == "403" {
				w.WriteHeader(http.StatusForbidden)
			} else {
				w.WriteHeader(http.StatusInternalServerError)
			}
			_, _ = io.WriteString(w, `{"provider_secret":"must-not-escape"}`)
			return
		}
		id, node, login := int64(42), "U_42", "octocat"
		if f.exists && f.principalMismatchAfter {
			id, node, login = 99, "U_99", "intruder"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "node_id": node, "login": login})
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/ref/heads/"):
		branch := strings.TrimPrefix(r.URL.Path, "/repos/octo/control/git/ref/heads/")
		_ = json.NewEncoder(w).Encode(map[string]any{"ref": "refs/heads/" + branch, "object": map[string]any{"type": "commit", "sha": f.headSHA}})
	case r.Method == http.MethodGet && r.URL.Path == "/repos/octo/control/pulls":
		f.listReads++
		if f.exists && f.state == "open" && !f.merged {
			_ = json.NewEncoder(w).Encode([]map[string]any{{"number": 7, "node_id": f.nodeID}})
		} else {
			_, _ = io.WriteString(w, `[]`)
		}
	case r.Method == http.MethodGet && r.URL.Path == "/repos/octo/control/pulls/7":
		f.prReads++
		if !f.exists {
			http.NotFound(w, r)
			return
		}
		f.writePR(w)
	case (r.Method == http.MethodPost && r.URL.Path == "/repos/octo/control/pulls") || (r.Method == http.MethodPatch && r.URL.Path == "/repos/octo/control/pulls/7"):
		var input struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		f.title, f.body, f.exists = input.Title, input.Body, true
		f.writes++
		if r.Method == http.MethodPost {
			f.prCreates++
			w.WriteHeader(http.StatusCreated)
		}
		f.writePR(w)
	default:
		http.NotFound(w, r)
	}
}

func (f *review2Fixture) writePR(w io.Writer) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"number": 7, "node_id": f.nodeID, "state": f.state, "merged": f.merged, "title": f.title, "body": f.body,
		"user": map[string]any{"id": 42, "node_id": "U_42", "login": "octocat"},
		"base": map[string]any{"ref": f.baseRef, "sha": f.headSHA, "repo": map[string]any{"name": "control", "owner": map[string]any{"login": f.baseOwner}}},
		"head": map[string]any{"ref": f.headRef, "sha": f.headSHA, "label": f.headLabel, "repo": map[string]any{"name": "control", "owner": map[string]any{"login": f.headOwner}}},
	})
}

func TestLifecycleDocumentBoundsAndCanonicalWriteConstruction(t *testing.T) {
	authority := lifecycleAuthority(t)
	f := newReview2Fixture(authority)
	fixed := time.Unix(1700060000, 0).UTC()
	controller, _, _, server := makeCorrectionController(t, authority, f, "document-bounds", func() time.Time { return fixed }, nil)
	defer server.Close()
	createTitle := strings.Repeat("<", PRLifecycleDocumentMaxBytes)
	createBody := strings.Repeat("&", PRLifecycleDocumentMaxBytes)
	if _, err := controller.Upsert(context.Background(), Request{RunID: "document-bounds", Authority: authority, Title: createTitle, Body: createBody}); err != nil {
		t.Fatalf("CREATE at exact document bound failed: %v", err)
	}
	fixed = fixed.Add(time.Minute)
	updateTitle := strings.Repeat(">", PRLifecycleDocumentMaxBytes)
	updateBody := strings.Repeat("<", PRLifecycleDocumentMaxBytes)
	if _, err := controller.Upsert(context.Background(), Request{RunID: "document-bounds", Authority: authority, Title: updateTitle, Body: updateBody}); err != nil {
		t.Fatalf("UPDATE at exact document bound failed: %v", err)
	}
	f.mu.Lock()
	if f.writes != 2 || f.title != updateTitle || f.body != updateBody {
		t.Fatalf("bounded documents did not round-trip: writes=%d title=%d body=%d", f.writes, len(f.title), len(f.body))
	}
	f.mu.Unlock()

	if PRLifecycleDocumentMaxBytes > lifecycleRemoteTextLimit(githublifecycle.DefaultLimits()) {
		t.Fatal("admitted document bound exceeds observable remote-text limit")
	}
	write, err := controller.github.prepareWrite(context.Background(), authority.Repository(), 0, createTitle, createBody, authority.BaseBranch(), authority.HeadBranch())
	if err != nil {
		t.Fatal(err)
	}
	defer write.cancel()
	encoded, err := io.ReadAll(write.request.Body)
	if err != nil || len(encoded) > MaxRequestBytes || int(write.request.ContentLength) != len(encoded) {
		t.Fatalf("escape-maximal canonical request violates cap: bytes=%d content-length=%d err=%v", len(encoded), write.request.ContentLength, err)
	}

	for _, field := range []string{"title", "body"} {
		t.Run("reject_1025_"+field, func(t *testing.T) {
			other := newReview2Fixture(authority)
			candidate, _, _, candidateServer := makeCorrectionController(t, authority, other, "reject-"+field, func() time.Time { return fixed }, nil)
			defer candidateServer.Close()
			title, body := "title", "body"
			if field == "title" {
				title = strings.Repeat("x", PRLifecycleDocumentMaxBytes+1)
			} else {
				body = strings.Repeat("x", PRLifecycleDocumentMaxBytes+1)
			}
			if _, err := candidate.Upsert(context.Background(), Request{RunID: "reject-" + field, Authority: authority, Title: title, Body: body}); err == nil {
				t.Fatal("oversized document was admitted")
			}
			other.mu.Lock()
			defer other.mu.Unlock()
			if other.requests != 0 || other.writes != 0 {
				t.Fatalf("oversized document made remote calls: requests=%d writes=%d", other.requests, other.writes)
			}
			matches, _ := filepath.Glob(filepath.Join(candidate.store.Root(), "*-generation.json"))
			markers, _ := filepath.Glob(filepath.Join(candidate.store.Root(), "*-submitted.json"))
			if len(matches)+len(markers) != 0 {
				t.Fatal("oversized document created generation or marker")
			}
		})
	}
}

func TestWriteConstructionFailureIsZeroSubmission(t *testing.T) {
	authority := lifecycleAuthority(t)
	f := newReview2Fixture(authority)
	controller, _, _, server := makeCorrectionController(t, authority, f, "construct-fail", time.Now, nil)
	defer server.Close()
	controller.github.writeConstructionHook = func() error { return errors.New("injected local construction failure") }
	result, err := controller.Upsert(context.Background(), Request{RunID: "construct-fail", Authority: authority, Title: "title", Body: "body"})
	if err == nil || result.Core().Disposition == AppliedReconciled {
		t.Fatalf("construction failure reconciled as applied: %#v %v", result.Core(), err)
	}
	f.mu.Lock()
	writes := f.writes
	f.mu.Unlock()
	if writes != 0 {
		t.Fatalf("construction failure submitted %d writes", writes)
	}
	markers, _ := filepath.Glob(filepath.Join(controller.store.Root(), "*-submitted.json"))
	if len(markers) != 0 {
		t.Fatal("construction failure published submitted marker")
	}
}

func TestPrincipalReadFailuresRemainAmbiguousAndRecover(t *testing.T) {
	for _, failure := range []string{"500", "403", "malformed"} {
		t.Run(failure, func(t *testing.T) {
			authority := lifecycleAuthority(t)
			f := newReview2Fixture(authority)
			f.principalFailures = []string{failure}
			now := time.Unix(1700070000, 0).UTC()
			controller, _, _, server := makeCorrectionController(t, authority, f, "principal-"+failure, func() time.Time { return now }, nil)
			defer server.Close()
			request := Request{RunID: "principal-" + failure, Authority: authority, Title: "title", Body: "body"}
			if _, err := controller.Upsert(context.Background(), request); err == nil || !strings.Contains(err.Error(), CodeAmbiguousUnresolved) {
				t.Fatalf("principal failure did not remain ambiguous: %v", err)
			}
			terminals, _ := filepath.Glob(filepath.Join(controller.store.Root(), "*-terminal.json"))
			if len(terminals) != 0 {
				t.Fatal("principal read failure wrote divergence terminal")
			}
			now = now.Add(MinReconciliationInterval + time.Second)
			result, err := controller.Upsert(context.Background(), request)
			if err != nil || result.Core().Disposition != AppliedReconciled {
				t.Fatalf("later bounded reconciliation did not recover: %#v %v", result.Core(), err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writes != 1 {
				t.Fatalf("principal recovery submitted %d writes", f.writes)
			}
		})
	}
}

func TestPrincipalMismatchTerminalBindsAuthorityIdentityAndRecovers(t *testing.T) {
	authority := lifecycleAuthority(t)
	f := newReview2Fixture(authority)
	f.principalMismatchAfter = true
	controller, _, _, server := makeCorrectionController(t, authority, f, "principal-mismatch", time.Now, nil)
	defer server.Close()
	request := Request{RunID: "principal-mismatch", Authority: authority, Title: "title", Body: "body"}
	result, err := controller.Upsert(context.Background(), request)
	if err == nil || result.Core().Disposition != RemoteDivergedAfterWrite || result.Core().PrincipalID != 42 || result.Core().PrincipalNodeID != "" || result.Core().PrincipalLogin != "" {
		t.Fatalf("principal mismatch formed mixed terminal identity: %#v %v", result.Core(), err)
	}
	terminalPaths, _ := filepath.Glob(filepath.Join(controller.store.Root(), "*-terminal.json"))
	if len(terminalPaths) != 1 {
		t.Fatalf("principal mismatch terminal count = %d", len(terminalPaths))
	}
	terminalBytes, readErr := os.ReadFile(terminalPaths[0])
	var terminal terminalV1
	if readErr != nil || strictJSON(terminalBytes, &terminal) != nil || terminal.Core.Principal.ID != 99 || terminal.Core.Principal.NodeID != "U_99" || terminal.Core.Principal.Login != "intruder" {
		t.Fatalf("complete mismatching principal tuple was not retained as terminal evidence: %v", readErr)
	}
	f.mu.Lock()
	before, writes := f.requests, f.writes
	f.mu.Unlock()
	recovered, recoverErr := controller.Upsert(context.Background(), request)
	f.mu.Lock()
	after := f.requests
	f.mu.Unlock()
	if recoverErr == nil || recovered.TerminalSHA256() != result.TerminalSHA256() || before != after || writes != 1 {
		t.Fatalf("principal mismatch recovery contacted GitHub or changed result: %v %d/%d writes=%d", recoverErr, before, after, writes)
	}
}

func TestEveryInjectedPostSubmitFailureHasControllerProvenance(t *testing.T) {
	for _, stage := range []string{"terminal", "evidence", "admission", "ledger"} {
		t.Run(stage, func(t *testing.T) {
			authority := lifecycleAuthority(t)
			f := newReview2Fixture(authority)
			controller, _, _, server := makeCorrectionController(t, authority, f, "post-submit-"+stage, time.Now, nil)
			defer server.Close()
			controller.postSubmitFailure = func(candidate string) error {
				if candidate == stage {
					return errors.New("provider-secret-injected")
				}
				return nil
			}
			_, operationErr := controller.Upsert(context.Background(), Request{RunID: "post-submit-" + stage, Authority: authority, Title: "title", Body: "body"})
			var lifecycleErr *Error
			if !errors.As(operationErr, &lifecycleErr) || !lifecycleErr.Submitted || lifecycleErr.Attempt == "" || lifecycleErr.Code == "" || strings.Contains(operationErr.Error(), "provider-secret-injected") {
				t.Fatalf("post-submit %s failure lost controller provenance: %#v", stage, operationErr)
			}
		})
	}
}

func TestPriorConfirmedPRMustBeDirectlyReproven(t *testing.T) {
	mutations := []string{"closed", "node", "repository", "ref", "document"}
	for _, mutation := range mutations {
		t.Run(mutation, func(t *testing.T) {
			authority := lifecycleAuthority(t)
			f := newReview2Fixture(authority)
			controller, _, _, server := makeCorrectionController(t, authority, f, "prior-"+mutation, time.Now, nil)
			defer server.Close()
			if _, err := controller.Upsert(context.Background(), Request{RunID: "prior-" + mutation, Authority: authority, Title: "first", Body: "body"}); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			listBefore := f.listReads
			switch mutation {
			case "closed":
				f.state = "closed"
			case "node":
				f.nodeID = "PR_OTHER"
			case "repository":
				f.headOwner = "intruder"
			case "ref":
				f.headRef = "other"
			case "document":
				f.title = "remote-edit"
			}
			f.mu.Unlock()
			if _, err := controller.Upsert(context.Background(), Request{RunID: "prior-" + mutation, Authority: authority, Title: "second", Body: "body"}); err == nil {
				t.Fatal("later revision admitted without prior exact PR proof")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writes != 1 || f.listReads != listBefore {
				t.Fatalf("failed prior proof switched discovery/write path: writes=%d list=%d/%d", f.writes, listBefore, f.listReads)
			}
		})
	}
}

type rawRunArtifacts struct {
	runID  string
	runDir string
}

func (w *rawRunArtifacts) RunID() string  { return w.runID }
func (w *rawRunArtifacts) RunDir() string { return w.runDir }
func (w *rawRunArtifacts) WriteBytes(string, string, []byte) (ledger.EvidenceRef, error) {
	return ledger.EvidenceRef{}, errors.New("unexpected evidence write")
}

func makeRawRunController(t *testing.T, store *PRWriteAdmissionStore, adapter *GitHubAdapter, runID string) *Controller {
	t.Helper()
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	recorder, err := newMaterialLedgerRecorder(filepath.Join(parent, "material.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	writer := &rawRunArtifacts{runID: runID, runDir: t.TempDir()}
	controller, err := newController(ControllerConfig{Store: store, GitHub: adapter, Artifacts: writer, Ledger: recorder, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	return controller
}

func TestPrepareHistoryIsRunScopedStrictAndResourceBounded(t *testing.T) {
	authority := lifecycleAuthority(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusInternalServerError)
	}))
	defer server.Close()
	adapter, err := newGitHubAdapter(server.Client().Transport, server.URL, githublifecycle.DefaultLimits(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	store := provisionStore(t)
	runA := makeRawRunController(t, store, adapter, "run-A")
	requestA := Request{RunID: "run-A", Authority: authority, Title: "title", Body: "body"}
	for range 3 {
		if _, err := runA.Upsert(context.Background(), requestA); err == nil {
			t.Fatal("prepare failure unexpectedly succeeded")
		}
	}
	before := countPrepareRecords(t, store.Root())
	if _, err := runA.Upsert(context.Background(), requestA); err == nil || !strings.Contains(err.Error(), CodePreflightBudgetExhausted) {
		t.Fatalf("same run prepare budget reset: %v", err)
	}
	if countPrepareRecords(t, store.Root()) != before {
		t.Fatal("exhausted run created another prepare record")
	}
	runB := makeRawRunController(t, store, adapter, "run-B")
	requestB := Request{RunID: "run-B", Authority: authority, Title: "title", Body: "body"}
	for range 3 {
		if _, err := runB.Upsert(context.Background(), requestB); err == nil {
			t.Fatal("fresh run prepare failure unexpectedly succeeded")
		}
	}
	if countPrepareRecords(t, store.Root()) != 6 {
		t.Fatal("different governed run did not receive a fresh three-round budget")
	}

	hostile := strings.Repeat("../hostile-", 80)
	hostileController := makeRawRunController(t, store, adapter, hostile)
	if _, err := hostileController.Upsert(context.Background(), Request{RunID: hostile, Authority: authority, Title: "title", Body: "body"}); err == nil {
		t.Fatal("hostile run prepare unexpectedly succeeded")
	}
	digest := digestBytes([]byte(hostile))
	matches, _ := filepath.Glob(filepath.Join(store.Root(), "*-prepare-run-"+digest+"-1.json"))
	if len(matches) != 1 || strings.Contains(filepath.Base(matches[0]), "hostile") || filepath.Dir(matches[0]) != store.Root() {
		t.Fatalf("hostile run ID escaped fixed filename grammar: %v", matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil || !strings.Contains(string(data), hostile) {
		t.Fatal("prepare record did not bind exact raw run ID")
	}

	if err := os.WriteFile(matches[0], []byte(`{"schema_version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := hostileController.Upsert(context.Background(), Request{RunID: hostile, Authority: authority, Title: "title", Body: "body"}); err == nil || !strings.Contains(err.Error(), CodeIntegrityFailure) {
		t.Fatalf("malformed prepare history reset to round one: %v", err)
	}
}

func TestPrepareHistoryCeilingAndRevisionIndependence(t *testing.T) {
	authority := lifecycleAuthority(t)
	f := newReview2Fixture(authority)
	store := provisionStore(t)
	key, _ := NewPRResourceKey(authority.Repository(), authority.BaseBranch(), authority.HeadBranch())
	seedPrepareFailures(t, store, key, 1, 23)
	controller, _, _, successServer := makeCorrectionController(t, authority, f, "successful-24", time.Now, store)
	defer successServer.Close()
	if _, err := controller.Upsert(context.Background(), Request{RunID: "successful-24", Authority: authority, Title: "first", Body: "body"}); err != nil {
		t.Fatalf("24th prepare record could not complete revision: %v", err)
	}
	if countPrepareRecords(t, store.Root()) != MaxPrepareHistoryRecordsPerPendingRevision {
		t.Fatal("successful revision did not retain its bounded prepare provenance")
	}
	if _, err := controller.Upsert(context.Background(), Request{RunID: "successful-24", Authority: authority, Title: "second", Body: "body"}); err != nil {
		t.Fatalf("next revision did not receive independent prepare allowance: %v", err)
	}
	if countPrepareRecords(t, store.Root()) != MaxPrepareHistoryRecordsPerPendingRevision+1 {
		t.Fatal("next revision prepare record was not independently retained")
	}

	otherKey := testResourceKey(t, "unrelated-history")
	if err := store.withResource(otherKey, func(tx *resourceTxn) error {
		runID := "unrelated"
		record := prepareRecordV1{SchemaVersion: SchemaVersion, RunID: runID, RunSHA256: digestBytes([]byte(runID)), ResourceKey: otherKey.String(), Revision: 1, Round: 1, Outcome: CodeRemoteReadFailed}
		_, _, err := tx.createJSON(prepareRecordName(otherKey, 1, record.RunSHA256, 1), record, false, false)
		return err
	}); err != nil {
		t.Fatalf("unrelated resource could not create prepare provenance: %v", err)
	}

	ceilingStore := provisionStore(t)
	if err := ceilingStore.withResource(key, func(tx *resourceTxn) error {
		return controller.ensureResourceRecord(tx, authority)
	}); err != nil {
		t.Fatal(err)
	}
	seedPrepareFailures(t, ceilingStore, key, 1, MaxPrepareHistoryRecordsPerPendingRevision)
	ceilingController := makeRawRunController(t, ceilingStore, controller.github, "ninth-run")
	entriesBefore, err := os.ReadDir(ceilingStore.Root())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ceilingController.Upsert(context.Background(), Request{RunID: "ninth-run", Authority: authority, Title: "title", Body: "body"}); err == nil || !strings.Contains(err.Error(), CodePreflightHistoryExhausted) {
		t.Fatalf("25th prepare record was not resource-locally rejected: %v", err)
	}
	entriesAfter, err := os.ReadDir(ceilingStore.Root())
	if err != nil {
		t.Fatal(err)
	}
	if len(entriesAfter) != len(entriesBefore) {
		t.Fatal("history-exhausted request created a file")
	}
}

func seedPrepareFailures(t *testing.T, store *PRWriteAdmissionStore, key PRResourceKeyV1, revision uint64, count int) {
	t.Helper()
	if err := store.withResource(key, func(tx *resourceTxn) error {
		for index := 0; index < count; index++ {
			runID := "seed-run-" + fmtInt(int64(index/3+1))
			round := index%3 + 1
			runSHA := digestBytes([]byte(runID))
			record := prepareRecordV1{SchemaVersion: SchemaVersion, RunID: runID, RunSHA256: runSHA, ResourceKey: key.String(), Revision: revision, Round: round, Outcome: CodeRemoteReadFailed}
			if _, _, err := tx.createJSON(prepareRecordName(key, revision, runSHA, round), record, false, false); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func countPrepareRecords(t *testing.T, root string) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, "*-prepare-run-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	return len(matches)
}

func TestTerminalBudgetUsesCanonicalEscapingAndPrincipalCap(t *testing.T) {
	authority := make([]byte, 16<<10)
	attempt := make([]byte, 8<<10)
	title := strings.Repeat("<", PRLifecycleDocumentMaxBytes)
	body := strings.Repeat("&", PRLifecycleDocumentMaxBytes)
	budget, err := NewTerminalBudget(authority, attempt, title, body)
	if err != nil || budget.DocumentBytes <= len(title)+len(body) || budget.WorstCaseBytes > MaxTerminalBytes {
		t.Fatalf("canonical escaped terminal reservation is invalid: %#v %v", budget, err)
	}
	if _, err := NewTerminalBudget(authority, attempt, strings.Repeat("x", PRLifecycleDocumentMaxBytes+1), body); err == nil {
		t.Fatal("1025-byte title fit terminal reservation")
	}
	principal := RemotePrincipalObservation{Request: HTTPRequestIdentity{Method: strings.Repeat("x", 8<<10)}, ID: 42, NodeID: "node", Login: "login"}
	if _, err := observationDigest(principal, 8<<10); err == nil {
		t.Fatal("oversized principal observation fit its configured cap")
	}

	lifecycle := lifecycleAuthority(t)
	f := newReview2Fixture(lifecycle)
	controller, _, _, server := makeCorrectionController(t, lifecycle, f, "principal-cap", time.Now, nil)
	defer server.Close()
	reads := 0
	controller.github.principalObserveHook = func(observation *RemotePrincipalObservation) {
		reads++
		if reads == 2 {
			observation.Request.Method = strings.Repeat("x", 8<<10)
		}
	}
	_, operationErr := controller.Upsert(context.Background(), Request{RunID: "principal-cap", Authority: lifecycle, Title: "title", Body: "body"})
	var lifecycleErr *Error
	if !errors.As(operationErr, &lifecycleErr) || !lifecycleErr.Submitted || lifecycleErr.Attempt == "" || lifecycleErr.Code == "" {
		t.Fatalf("post-write principal cap failure lost submitted provenance: %#v", operationErr)
	}
	terminals, _ := filepath.Glob(filepath.Join(controller.store.Root(), "*-terminal.json"))
	if len(terminals) != 0 {
		t.Fatal("oversized principal observation was published in a terminal")
	}
}

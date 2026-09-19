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
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

type requestAuthenticatorFunc func(*http.Request) error

func (f requestAuthenticatorFunc) AuthenticateGitHubRequest(request *http.Request) error {
	return f(request)
}

func TestProductionConstructionPinsHostRootAndExactLedger(t *testing.T) {
	first := provisionStore(t)
	old := productionAdmissionRoot
	productionAdmissionRoot = first.Root()
	t.Cleanup(func() { productionAdmissionRoot = old })
	adapter, err := newGitHubAdapter(http.DefaultTransport, "https://example.invalid", githublifecycle.DefaultLimits(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := evidence.NewStore(filepath.Join(t.TempDir(), "evidence"), "production-run")
	if err != nil {
		t.Fatal(err)
	}
	ledgerParent := t.TempDir()
	if err := os.Chmod(ledgerParent, 0o700); err != nil {
		t.Fatal(err)
	}
	authoritative, err := ledger.NewJSONLLedger(filepath.Join(ledgerParent, "authoritative.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	config := ProductionControllerConfig{GitHub: adapter, Artifacts: artifacts, AuthoritativeLedger: authoritative}
	one, err := NewProductionController(config)
	if err != nil {
		t.Fatal(err)
	}
	secondRoot := provisionStore(t).Root()
	t.Setenv("ABCP_PR_ADMISSION_ROOT", secondRoot)
	two, err := NewProductionController(config)
	if err != nil {
		t.Fatal(err)
	}
	if one.store.Root() != first.Root() || two.store.Root() != first.Root() {
		t.Fatal("process-startup admission identity was re-keyed by later host input")
	}
	if one.ledger.path != authoritative.Path() || two.ledger.path != authoritative.Path() {
		t.Fatal("production recorder did not derive the exact authoritative ledger path")
	}
	config.AuthoritativeLedger = nil
	if _, err := NewProductionController(config); err == nil {
		t.Fatal("nil production ledger binding was accepted")
	}
	adapter, err = NewGitHubAdapter(requestAuthenticatorFunc(func(*http.Request) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	sealed, ok := adapter.client.Transport.(*authenticatedGitHubTransport)
	if !ok || sealed.base.MaxResponseHeaderBytes != MaxResponseHeaderBytes {
		t.Fatal("sealed authenticated transport lacks network response-header cap")
	}
}

func TestAdmissionRootAndAcquiredLockReplacementFailClosed(t *testing.T) {
	store := provisionStore(t)
	key := testResourceKey(t, "replacement-race")
	if err := store.ensureResourceLock(key); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(store.Root(), "r-"+key.String()+".lock")
	store.afterResourceLockOpen = func() {
		store.afterResourceLockOpen = nil
		if err := os.Rename(lockPath, lockPath+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.withResource(key, func(*resourceTxn) error { return nil }); err == nil || !strings.Contains(err.Error(), CodeIntegrityFailure) {
		t.Fatalf("lock replacement during open/flock was accepted: %v", err)
	}

	root := store.Root()
	if err := os.Rename(root, root+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "capacity.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.withResource(testResourceKey(t, "root-replaced"), func(*resourceTxn) error { return nil }); err == nil || !strings.Contains(err.Error(), CodeIntegrityFailure) {
		t.Fatalf("root path replacement was accepted: %v", err)
	}
}

func TestCrossProcessAdmissionUsesOneLockInode(t *testing.T) {
	if os.Getenv("ABCP_PR_LOCK_HELPER") == "1" {
		store, err := newPRWriteAdmissionStore(os.Getenv("ABCP_PR_LOCK_ROOT"))
		if err != nil {
			t.Fatal(err)
		}
		key := testResourceKey(t, "cross-process")
		if err := store.withResource(key, func(*resourceTxn) error { return nil }); err == nil || !strings.Contains(err.Error(), "locked by another process") {
			t.Fatalf("helper acquired a second physical-resource lock: %v", err)
		}
		return
	}
	store := provisionStore(t)
	key := testResourceKey(t, "cross-process")
	if err := store.withResource(key, func(*resourceTxn) error {
		command := exec.Command(os.Args[0], "-test.run=^TestCrossProcessAdmissionUsesOneLockInode$")
		command.Env = append(os.Environ(), "ABCP_PR_LOCK_HELPER=1", "ABCP_PR_LOCK_ROOT="+store.Root())
		if output, commandErr := command.CombinedOutput(); commandErr != nil {
			return errors.New(string(output))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type correctionFixture struct {
	mu           sync.Mutex
	headSHA      string
	posted       bool
	writes       int
	requests     int
	mutation     string
	beforeRemote func()
}

func (f *correctionFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	if f.posted && f.beforeRemote != nil {
		f.beforeRemote()
	}
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/user":
		_, _ = io.WriteString(w, `{"id":42,"node_id":"U_42","login":"octocat"}`)
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/ref/heads/"):
		branch := strings.TrimPrefix(r.URL.Path, "/repos/octo/control/git/ref/heads/")
		sha := f.headSHA
		if f.posted && (f.mutation == "moved-head" && branch == "feature" || f.mutation == "moved-base" && branch == "main") {
			sha = strings.Repeat("f", 40)
		}
		_, _ = io.WriteString(w, `{"ref":"refs/heads/`+branch+`","object":{"type":"commit","sha":"`+sha+`"}}`)
	case r.Method == http.MethodGet && r.URL.Path == "/repos/octo/control/pulls":
		if f.posted {
			node := "PR_7"
			if f.mutation == "node-mismatch" {
				node = "PR_LIST_OTHER"
			}
			_, _ = io.WriteString(w, `[{"number":7,"node_id":"`+node+`"}]`)
		} else {
			_, _ = io.WriteString(w, `[]`)
		}
	case r.Method == http.MethodPost && r.URL.Path == "/repos/octo/control/pulls":
		f.posted = true
		f.writes++
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, f.prJSON(false))
	case r.Method == http.MethodGet && r.URL.Path == "/repos/octo/control/pulls/7":
		_, _ = io.WriteString(w, f.prJSON(true))
	default:
		http.NotFound(w, r)
	}
}

func (f *correctionFixture) prJSON(postflight bool) string {
	state, merged, title, author := "open", false, "Governed PR", int64(42)
	baseRef, headRef, headLabel := "main", "feature", "octo:feature"
	baseRepo, headRepo := "octo", "octo"
	if postflight {
		switch f.mutation {
		case "closed":
			state = "closed"
		case "merged":
			state, merged = "closed", true
		case "repository":
			headRepo = "intruder"
		case "ref":
			headRef = "other"
		case "head-label":
			headLabel = "intruder:feature"
		case "document":
			title = "remote edit"
		case "author":
			author = 99
		}
	}
	return `{"number":7,"node_id":"PR_7","state":"` + state + `","merged":` + boolText(merged) + `,"title":"` + title + `","body":"exact body","user":{"id":` + intText(author) + `,"node_id":"U_42","login":"octocat"},"base":{"ref":"` + baseRef + `","sha":"` + f.headSHA + `","repo":{"name":"control","owner":{"login":"` + baseRepo + `"}}},"head":{"ref":"` + headRef + `","sha":"` + f.headSHA + `","label":"` + headLabel + `","repo":{"name":"control","owner":{"login":"` + headRepo + `"}}}}`
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func intText(value int64) string { return fmtInt(value) }

func fmtInt(value int64) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	i := len(buffer)
	for value > 0 {
		i--
		buffer[i] = digits[value%10]
		value /= 10
	}
	return string(buffer[i:])
}

func makeCorrectionController(t *testing.T, authority githublifecycle.Authority, fixture http.Handler, runID string, now func() time.Time, store *PRWriteAdmissionStore) (*Controller, *evidence.Store, string, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(fixture)
	adapter, err := newGitHubAdapter(server.Client().Transport, server.URL, githublifecycle.DefaultLimits(), now)
	if err != nil {
		t.Fatal(err)
	}
	if store == nil {
		store = provisionStore(t)
	}
	artifacts, err := evidence.NewStore(filepath.Join(t.TempDir(), "evidence"), runID)
	if err != nil {
		t.Fatal(err)
	}
	ledgerParent := t.TempDir()
	if err := os.Chmod(ledgerParent, 0o700); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(ledgerParent, "material.jsonl")
	recorder, err := newMaterialLedgerRecorder(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := newController(ControllerConfig{Store: store, GitHub: adapter, Artifacts: artifacts, Ledger: recorder, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	_ = authority
	return controller, artifacts, ledgerPath, server
}

func TestReconciliationStartPrecedesRemoteAndImmediateRetryMakesNoCalls(t *testing.T) {
	authority := lifecycleAuthority(t)
	fixed := time.Unix(1700010000, 0).UTC()
	fixture := &correctionFixture{headSHA: authority.HeadSHA().String()}
	store := provisionStore(t)
	key, _ := NewPRResourceKey(authority.Repository(), authority.BaseBranch(), authority.HeadBranch())
	fixture.beforeRemote = func() {
		matches, _ := filepath.Glob(filepath.Join(store.Root(), recordPrefix(key, 1)+"reconcile-1-start.json"))
		if len(matches) != 1 {
			t.Error("post-submit remote read occurred before durable round start")
		}
	}
	controller, _, _, server := makeCorrectionController(t, authority, fixture, "round-run", func() time.Time { return fixed }, store)
	defer server.Close()
	request := Request{RunID: "round-run", Authority: authority, Title: "Governed PR", Body: "exact body"}
	if _, err := controller.Upsert(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	// Remove the terminal to model a submitted/no-terminal crash after the
	// fully recorded first round. The admission records remain immutable.
	terminal := filepath.Join(store.Root(), recordPrefix(key, 1)+"terminal.json")
	if err := os.Rename(terminal, terminal+".saved"); err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	before := fixture.requests
	fixture.mu.Unlock()
	if _, err := controller.Upsert(context.Background(), request); err == nil || !strings.Contains(err.Error(), CodeReconcileBudgetExhausted) {
		t.Fatalf("immediate submitted retry was admitted: %v", err)
	}
	fixture.mu.Lock()
	after := fixture.requests
	fixture.mu.Unlock()
	if after != before {
		t.Fatalf("immediate retry made %d GitHub calls", after-before)
	}
}

func TestPostWriteDivergenceIsDurableWithoutSnapshot(t *testing.T) {
	mutations := []string{"moved-head", "moved-base", "closed", "merged", "repository", "ref", "head-label", "document", "author", "node-mismatch"}
	for _, mutation := range mutations {
		t.Run(mutation, func(t *testing.T) {
			authority := lifecycleAuthority(t)
			fixed := time.Unix(1700020000, 0).UTC()
			fixture := &correctionFixture{headSHA: authority.HeadSHA().String(), mutation: mutation}
			controller, _, _, server := makeCorrectionController(t, authority, fixture, "diverge-"+mutation, func() time.Time { return fixed }, nil)
			defer server.Close()
			request := Request{RunID: "diverge-" + mutation, Authority: authority, Title: "Governed PR", Body: "exact body"}
			result, err := controller.Upsert(context.Background(), request)
			if err == nil || result.Core().Disposition != RemoteDivergedAfterWrite || result.Core().SnapshotSHA256 != "" {
				t.Fatalf("divergence did not produce unresolved snapshot-free terminal: result=%#v err=%v", result.Core(), err)
			}
			fixture.mu.Lock()
			before, writes := fixture.requests, fixture.writes
			fixture.mu.Unlock()
			recovered, recoverErr := controller.Upsert(context.Background(), request)
			fixture.mu.Lock()
			after := fixture.requests
			fixture.mu.Unlock()
			if recoverErr == nil || recovered.Core().Disposition != RemoteDivergedAfterWrite || recovered.TerminalSHA256() != result.TerminalSHA256() || after != before || writes != 1 {
				t.Fatalf("divergence recovery changed result or contacted GitHub: %#v %v calls=%d/%d writes=%d", recovered.Core(), recoverErr, before, after, writes)
			}
		})
	}
}

func TestCrossRunTerminalRecoveryRepublishesCurrentEvidence(t *testing.T) {
	authority := lifecycleAuthority(t)
	fixed := time.Unix(1700030000, 0).UTC()
	fixture := &correctionFixture{headSHA: authority.HeadSHA().String()}
	store := provisionStore(t)
	first, _, ledgerPath, server := makeCorrectionController(t, authority, fixture, "run-origin", func() time.Time { return fixed }, store)
	defer server.Close()
	request := Request{RunID: "run-origin", Authority: authority, Title: "Governed PR", Body: "exact body"}
	original, err := first.Upsert(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	before := fixture.requests
	fixture.mu.Unlock()
	currentArtifacts, err := evidence.NewStore(filepath.Join(t.TempDir(), "evidence-current"), "run-current")
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := newMaterialLedgerRecorder(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newController(ControllerConfig{Store: store, GitHub: first.github, Artifacts: currentArtifacts, Ledger: recorder, Now: func() time.Time { return fixed }})
	if err != nil {
		t.Fatal(err)
	}
	request.RunID = "run-current"
	recovered, err := second.Upsert(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	after := fixture.requests
	fixture.mu.Unlock()
	if after != before || recovered.TerminalSHA256() != original.TerminalSHA256() || !reflect.DeepEqual(recovered.Core(), original.Core()) {
		t.Fatal("cross-run recovery changed immutable result or contacted GitHub")
	}
	for _, ref := range recovered.EvidenceRefs() {
		if filepath.Dir(ref.URI) != currentArtifacts.RunDir() || !strings.HasPrefix(filepath.Base(ref.URI), "run-current-") {
			t.Fatalf("recovery returned originating-run evidence URI: %#v", ref)
		}
	}
}

func TestConcurrentControllersSerializeOneSubmission(t *testing.T) {
	authority := lifecycleAuthority(t)
	fixed := time.Unix(1700035000, 0).UTC()
	fixture := &correctionFixture{headSHA: authority.HeadSHA().String()}
	storeOne := provisionStore(t)
	first, _, ledgerPath, server := makeCorrectionController(t, authority, fixture, "concurrent-one", func() time.Time { return fixed }, storeOne)
	defer server.Close()
	storeTwo, err := newPRWriteAdmissionStore(storeOne.Root())
	if err != nil {
		t.Fatal(err)
	}
	secondArtifacts, _ := evidence.NewStore(filepath.Join(t.TempDir(), "evidence-two"), "concurrent-two")
	secondRecorder, _ := newMaterialLedgerRecorder(ledgerPath)
	second, err := newController(ControllerConfig{Store: storeTwo, GitHub: first.github, Artifacts: secondArtifacts, Ledger: secondRecorder, Now: func() time.Time { return fixed }})
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type concurrentItem struct {
		controller *Controller
		runID      string
	}
	items := []concurrentItem{{first, "concurrent-one"}, {second, "concurrent-two"}}
	type concurrentResult struct {
		index int
		err   error
	}
	results := make(chan concurrentResult, 2)
	for index, item := range items {
		index, item := index, item
		go func() {
			<-start
			_, runErr := item.controller.Upsert(context.Background(), Request{RunID: item.runID, Authority: authority, Title: "Governed PR", Body: "exact body"})
			results <- concurrentResult{index, runErr}
		}()
	}
	close(start)
	var failed []int
	for range 2 {
		result := <-results
		if result.err != nil {
			failed = append(failed, result.index)
		}
	}
	if len(failed) > 1 {
		t.Fatalf("both concurrent controllers failed admission: %v", failed)
	}
	for _, index := range failed {
		item := items[index]
		if _, err := item.controller.Upsert(context.Background(), Request{RunID: item.runID, Authority: authority, Title: "Governed PR", Body: "exact body"}); err != nil {
			t.Fatalf("serialized retry did not recover terminal: %v", err)
		}
	}
	fixture.mu.Lock()
	writes := fixture.writes
	fixture.mu.Unlock()
	if writes != 1 {
		t.Fatalf("concurrent controllers submitted %d writes", writes)
	}
}

func TestRepeatedReconciliationFailuresStopAtEightStarts(t *testing.T) {
	authority := lifecycleAuthority(t)
	fixture := &correctionFixture{headSHA: authority.HeadSHA().String()}
	server := httptest.NewServer(fixture)
	defer server.Close()
	var mu sync.Mutex
	postSubmitted := false
	remoteCalls := 0
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		mu.Lock()
		remoteCalls++
		fail := postSubmitted && request.Method == http.MethodGet
		mu.Unlock()
		if fail {
			return nil, errors.New("provider unavailable")
		}
		response, err := server.Client().Transport.RoundTrip(request)
		if request.Method == http.MethodPost {
			mu.Lock()
			postSubmitted = true
			mu.Unlock()
		}
		return response, err
	})
	now := time.Unix(1700038000, 0).UTC()
	adapter, _ := newGitHubAdapter(transport, server.URL, githublifecycle.DefaultLimits(), func() time.Time { return now })
	store := provisionStore(t)
	artifacts, _ := evidence.NewStore(filepath.Join(t.TempDir(), "round-evidence"), "eight-rounds")
	ledgerParent := t.TempDir()
	_ = os.Chmod(ledgerParent, 0o700)
	recorder, _ := newMaterialLedgerRecorder(filepath.Join(ledgerParent, "material"))
	controller, _ := newController(ControllerConfig{Store: store, GitHub: adapter, Artifacts: artifacts, Ledger: recorder, Now: func() time.Time { return now }})
	request := Request{RunID: "eight-rounds", Authority: authority, Title: "Governed PR", Body: "exact body"}
	if _, err := controller.Upsert(context.Background(), request); err == nil {
		t.Fatal("failed first reconciliation unexpectedly succeeded")
	}
	for range MaxReconciliationRounds - 1 {
		now = now.Add(MinReconciliationInterval + time.Second)
		if _, err := controller.Upsert(context.Background(), request); err == nil {
			t.Fatal("failed reconciliation unexpectedly succeeded")
		}
	}
	mu.Lock()
	before := remoteCalls
	mu.Unlock()
	now = now.Add(MinReconciliationInterval + time.Second)
	if _, err := controller.Upsert(context.Background(), request); err == nil || !strings.Contains(err.Error(), CodeReconcileBudgetExhausted) {
		t.Fatalf("ninth reconciliation was not rejected: %v", err)
	}
	mu.Lock()
	after := remoteCalls
	mu.Unlock()
	if after != before {
		t.Fatalf("budget-exhausted reconciliation made %d remote calls", after-before)
	}
	key, _ := NewPRResourceKey(authority.Repository(), authority.BaseBranch(), authority.HeadBranch())
	starts, _ := filepath.Glob(filepath.Join(store.Root(), recordPrefix(key, 1)+"reconcile-*-start.json"))
	observations, _ := filepath.Glob(filepath.Join(store.Root(), recordPrefix(key, 1)+"reconcile-*-observation.json"))
	if len(starts) != MaxReconciliationRounds || len(observations) != MaxReconciliationRounds {
		t.Fatalf("unexpected reconciliation records: starts=%d observations=%d", len(starts), len(observations))
	}
}

func TestEvidenceReplacementAndSecretBearingTransportFailClosed(t *testing.T) {
	store, err := evidence.NewStore(filepath.Join(t.TempDir(), "evidence"), "safe-read")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.WriteBytes("proof.json", "proof", []byte(`{"safe":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(ref.URI, ref.URI+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ref.URI+".old", ref.URI); err != nil {
		t.Fatal(err)
	}
	if _, err := captureArtifacts(store, []ledger.EvidenceRef{ref}); err == nil {
		t.Fatal("symlink-substituted evidence was captured")
	}
	if _, err := publishOrVerify(store, "proof.json", "proof", []byte(`{"safe":true}`)); err == nil {
		t.Fatal("symlink-substituted existing evidence was trusted")
	}

	authority := lifecycleAuthority(t)
	secret := "ghp_FAKE_SECRET_MUST_NOT_PERSIST"
	fixture := &correctionFixture{headSHA: authority.HeadSHA().String()}
	server := httptest.NewServer(fixture)
	defer server.Close()
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost || request.Method == http.MethodPatch {
			return nil, errors.New(secret)
		}
		return server.Client().Transport.RoundTrip(request)
	})
	fixed := time.Unix(1700040000, 0).UTC()
	adapter, _ := newGitHubAdapter(transport, server.URL, githublifecycle.DefaultLimits(), func() time.Time { return fixed })
	admission := provisionStore(t)
	artifacts, _ := evidence.NewStore(filepath.Join(t.TempDir(), "secret-evidence"), "secret-run")
	ledgerParent := t.TempDir()
	_ = os.Chmod(ledgerParent, 0o700)
	ledgerPath := filepath.Join(ledgerParent, "material")
	recorder, _ := newMaterialLedgerRecorder(ledgerPath)
	controller, _ := newController(ControllerConfig{Store: admission, GitHub: adapter, Artifacts: artifacts, Ledger: recorder, Now: func() time.Time { return fixed }})
	_, operationErr := controller.Upsert(context.Background(), Request{RunID: "secret-run", Authority: authority, Title: "Governed PR", Body: "exact body"})
	if operationErr == nil || strings.Contains(operationErr.Error(), secret) {
		t.Fatalf("transport secret escaped in returned diagnostic: %v", operationErr)
	}
	for _, root := range []string{admission.Root(), artifacts.Root(), ledgerParent} {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr == nil && !entry.IsDir() {
				data, _ := os.ReadFile(path)
				if strings.Contains(string(data), secret) {
					t.Errorf("transport secret persisted in %s", path)
				}
			}
			return nil
		})
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestTerminalBudgetMaximaAndReconciliationStartConsumption(t *testing.T) {
	authority := make([]byte, 16<<10)
	attempt := make([]byte, 8<<10)
	budget, err := NewTerminalBudget(authority, attempt, strings.Repeat("<", PRLifecycleDocumentMaxBytes), strings.Repeat("&", PRLifecycleDocumentMaxBytes))
	if err != nil || budget.WorstCaseBytes > MaxTerminalBytes {
		t.Fatalf("maximal admitted terminal profile is not bounded: %#v %v", budget, err)
	}
	if _, err := NewTerminalBudget(append(authority, 'x'), attempt, "title", ""); err == nil {
		t.Fatal("terminal overflow profile was admitted")
	}
	maxJSON := func(size int) []byte {
		return []byte(`"` + strings.Repeat("x", size-2) + `"`)
	}
	digest := strings.Repeat("a", 64)
	remote := strings.Repeat("r", maxLifecycleRemoteText)
	requestIdentity := HTTPRequestIdentity{Method: remote, PathTemplate: remote, EscapedPath: remote, APIOrigin: remote, APIVersion: remote, Accept: remote, ContentType: remote, ExpectedStatus: []int{200}, RequestBodySHA256: digest, BoundedResponseBytes: MaxResponseBytes}
	principal := RemotePrincipalObservation{Request: requestIdentity, ID: 1, NodeID: remote, Login: remote, LimitsSHA256: digest}
	pr := RemotePRObservation{Request: requestIdentity, Number: 1, NodeID: remote, RepositoryOwner: remote, RepositoryName: remote, BaseRepository: remote, HeadRepository: remote, BaseRef: remote, HeadRef: remote, BaseSHA: strings.Repeat("b", 40), HeadSHA: strings.Repeat("b", 40), HeadLabel: remote, AuthorID: 1, AuthorNodeID: remote, AuthorLogin: remote, State: "open", Title: remote, Body: remote, LimitsSHA256: digest}
	refObservation := RemoteRefObservation{Request: requestIdentity, EchoedRef: remote, ObjectType: "commit", SHA: strings.Repeat("b", 40), LimitsSHA256: digest}
	core := terminalCoreV1{
		SchemaVersion: SchemaVersion, PolicySHA256: digest, LimitsSHA256: digest, ResourceKey: digest, Revision: 1, Generation: 1,
		GenerationSHA256: digest, SubmittedSHA256: digest, Attempt: maxJSON(8 << 10), AttemptSHA256: digest,
		SourceAuthority: maxJSON(16 << 10), SourceAuthoritySHA: digest, DerivedAuthority: maxJSON(16 << 10), DerivedAuthoritySHA: digest,
		ExpectedContentSHA: digest, Title: strings.Repeat("<", PRLifecycleDocumentMaxBytes), Body: strings.Repeat("&", PRLifecycleDocumentMaxBytes), DocumentSHA256: digest,
		Snapshot: maxJSON(MaxTerminalSnapshotBytes), SnapshotSHA256: digest, SnapshotRecovery: snapshotRecoveryWire{Provider: remote, RequestID: remote, RepositoryOwn: remote, Repository: remote, PRNodeID: remote, BaseBranch: remote, BaseTipSHA: strings.Repeat("b", 40), HeadBranch: remote, HeadSHA: strings.Repeat("b", 40), State: "open"},
		Principal: principal, PullRequest: pr, Head: refObservation, Base: refObservation, Reconciliation: maxJSON(MaxTerminalArtifactBytes),
		ResultCore: PRLifecycleResultCoreV1{Disposition: AppliedConfirmed}, ResultCoreSHA256: digest, Reason: strings.Repeat("q", 1024), RunID: "max-run", TerminalUnixNano: 1,
		EvidenceRefs: []ledger.EvidenceRef{{URI: "/" + remote, SHA256: digest, Kind: remote}}, EvidenceArtifacts: []terminalArtifact{{Name: remote, Kind: remote, Bytes: maxJSON(MaxTerminalArtifactBytes), SHA256: digest}},
	}
	_, eventBytes, err := deterministicTerminalEvent(core)
	if err != nil {
		t.Fatal(err)
	}
	coreBytes, _ := json.Marshal(core)
	terminalBytes, err := json.Marshal(terminalV1{Core: core, TerminalCoreSHA256: digestBytes(coreBytes), MaterialEvent: eventBytes, MaterialEventSHA: digestBytes(eventBytes)})
	if err != nil || len(terminalBytes) > budget.WorstCaseBytes || budget.WorstCaseBytes > MaxTerminalBytes {
		t.Fatalf("maximal persisted terminal components exceed pre-submit proof: terminal=%d proof=%d err=%v", len(terminalBytes), budget.WorstCaseBytes, err)
	}

	lifecycle := lifecycleAuthority(t)
	store := provisionStore(t)
	key, _ := NewPRResourceKey(lifecycle.Repository(), lifecycle.BaseBranch(), lifecycle.HeadBranch())
	fixed := time.Unix(1700050000, 0).UTC()
	controller := &Controller{store: store, github: &GitHubAdapter{limits: githublifecycle.DefaultLimits()}, now: func() time.Time { return fixed }}
	revision := revisionRecord{Ordinal: 1}
	generation := generationRecord{WriteID: "write", AttemptSHA256: strings.Repeat("a", 64)}
	if err := store.withResource(key, func(tx *resourceTxn) error {
		round, _, err := controller.beginReconciliation(tx, revision, generation)
		if err != nil || round != 1 {
			return firstError(err, errors.New("first reconciliation start was not consumed"))
		}
		_, _, err = controller.beginReconciliation(tx, revision, generation)
		if err == nil || !strings.Contains(err.Error(), CodeReconcileBudgetExhausted) {
			return errors.New("start-only crash did not consume the interval")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

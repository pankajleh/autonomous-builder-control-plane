//go:build linux

package prlifecycle

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
)

func lifecycleAuthority(t *testing.T) githublifecycle.Authority {
	t.Helper()
	repositoryPath := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(repositoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repositoryPath, "init", "-q")
	runGitTest(t, repositoryPath, "config", "user.name", "ABCP Test")
	runGitTest(t, repositoryPath, "config", "user.email", "abcp@example.invalid")
	if err := os.WriteFile(filepath.Join(repositoryPath, "content"), []byte("accepted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitTest(t, repositoryPath, "add", "content")
	runGitTest(t, repositoryPath, "commit", "-q", "-m", "accepted")
	shaText := strings.TrimSpace(runGitTest(t, repositoryPath, "rev-parse", "HEAD"))
	sha, _ := githublifecycle.NewGitSHA(shaText)
	phase3, err := evidence.NewStore(filepath.Join(t.TempDir(), "phase3"), "phase3-run")
	if err != nil {
		t.Fatal(err)
	}
	decision := []byte(`{"state":"READY_FOR_MERGE","combined":{"target":{"head_sha":"` + shaText + `","integration":{"integrated_head_sha":"` + shaText + `","baseline_sha":"` + shaText + `"}}}}`)
	ref, err := phase3.WriteBytes("ready.json", "serial-integration-gate-decision", decision)
	if err != nil {
		t.Fatal(err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := githublifecycle.DeriveExpectedMergeContent(context.Background(), repositoryPath, phase3.Root(), sha, sha, ref, gitPath)
	if err != nil {
		t.Fatal(err)
	}
	repository, _ := githublifecycle.NewRepository("octo", "control")
	base, _ := githublifecycle.NewBranch("main")
	head, _ := githublifecycle.NewBranch("feature")
	actor, _ := githublifecycle.NewUserIdentity("github-user-id:42")
	authority, err := githublifecycle.NewAuthority(githublifecycle.AuthorityInput{
		Repository: repository, BaseBranch: base, HeadBranch: head, HeadSHA: sha, ExpectedBaseTipSHA: sha,
		AllowedMergeMethod: githublifecycle.MergeMethodSquash, Actor: actor, ExpectedContent: expected,
	})
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func runGitTest(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "LC_ALL=C")
	b, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, b)
	}
	return string(b)
}

type githubFixture struct {
	mu       sync.Mutex
	posted   bool
	requests int
	headSHA  string
}

func (f *githubFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-GitHub-Request-Id", "request-safe")
	if r.Header.Get("Accept") != githubAccept || r.Header.Get("X-GitHub-Api-Version") != githubAPIVer || r.Header.Get("Authorization") != "Bearer sealed" {
		http.Error(w, "bad sealed headers", http.StatusBadRequest)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/user":
		_, _ = io.WriteString(w, `{"id":42,"node_id":"U_42","login":"octocat","extra":"ignored"}`)
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/ref/heads/"):
		branch := strings.TrimPrefix(r.URL.Path, "/repos/octo/control/git/ref/heads/")
		_, _ = io.WriteString(w, `{"ref":"refs/heads/`+branch+`","object":{"type":"commit","sha":"`+f.headSHA+`"}}`)
	case r.Method == http.MethodGet && r.URL.Path == "/repos/octo/control/pulls":
		if r.URL.Query().Encode() != "base=main&direction=asc&head=octo%3Afeature&page=1&per_page=100&sort=created&state=open" {
			http.Error(w, "bad query", http.StatusBadRequest)
			return
		}
		if f.posted {
			_, _ = io.WriteString(w, `[{"number":7,"node_id":"PR_7"}]`)
		} else {
			_, _ = io.WriteString(w, `[]`)
		}
	case r.Method == http.MethodPost && r.URL.Path == "/repos/octo/control/pulls":
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "missing content type", http.StatusBadRequest)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body) != 4 || body["title"] != "Governed PR" || body["body"] != "exact body" || body["head"] != "octo:feature" || body["base"] != "main" {
			http.Error(w, "bad write body", http.StatusBadRequest)
			return
		}
		f.posted = true
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, f.pullRequestJSON())
	case r.Method == http.MethodGet && r.URL.Path == "/repos/octo/control/pulls/7":
		_, _ = io.WriteString(w, f.pullRequestJSON())
	default:
		http.Error(w, "unexpected", http.StatusNotFound)
	}
}

func (f *githubFixture) pullRequestJSON() string {
	return `{"number":7,"node_id":"PR_7","state":"open","merged":false,"title":"Governed PR","body":"exact body","user":{"id":42,"node_id":"U_42","login":"octocat"},"base":{"ref":"main","sha":"` + f.headSHA + `","repo":{"name":"control","owner":{"login":"octo"}}},"head":{"ref":"feature","sha":"` + f.headSHA + `","label":"octo:feature","repo":{"name":"control","owner":{"login":"octo"}}}}`
}

type authTransport struct{ base http.RoundTripper }

func (t authTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer sealed")
	return t.base.RoundTrip(clone)
}

func TestControllerCreatesExactHeadPRAndRecoversWithoutRemoteCall(t *testing.T) {
	authority := lifecycleAuthority(t)
	fixture := &githubFixture{headSHA: authority.HeadSHA().String()}
	server := httptest.NewServer(fixture)
	defer server.Close()
	fixed := time.Unix(1700000000, 123).UTC()
	adapter, err := newGitHubAdapter(authTransport{server.Client().Transport}, server.URL, githublifecycle.DefaultLimits(), func() time.Time { return fixed })
	if err != nil {
		t.Fatal(err)
	}
	store := provisionStore(t)
	artifacts, err := evidence.NewStore(filepath.Join(t.TempDir(), "evidence"), "run-pr")
	if err != nil {
		t.Fatal(err)
	}
	ledgerParent := t.TempDir()
	if err := os.Chmod(ledgerParent, 0o700); err != nil {
		t.Fatal(err)
	}
	recorder, err := NewMaterialLedgerRecorder(filepath.Join(ledgerParent, "material.jsonl"), nil)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := NewController(ControllerConfig{Store: store, GitHub: adapter, Artifacts: artifacts, Ledger: recorder, Now: func() time.Time { return fixed }})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{RunID: "run-pr", Authority: authority, Title: "Governed PR", Body: "exact body"}
	result, err := controller.Upsert(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Core().Disposition != AppliedConfirmed || result.Core().PRNumber != 7 || result.TerminalSHA256() == "" || len(result.EvidenceRefs()) != 2 {
		t.Fatalf("unexpected result: %#v", result.Core())
	}
	fixture.mu.Lock()
	requestsAfterLive := fixture.requests
	fixture.mu.Unlock()
	recovered, err := controller.Upsert(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	requestsAfterRecovery := fixture.requests
	fixture.mu.Unlock()
	if requestsAfterRecovery != requestsAfterLive {
		t.Fatalf("terminal recovery made GitHub calls: before=%d after=%d", requestsAfterLive, requestsAfterRecovery)
	}
	if string(result.CanonicalJSON()) != string(recovered.CanonicalJSON()) {
		t.Fatal("restart did not reconstruct identical material result")
	}
	ledgerBytes, err := os.ReadFile(filepath.Join(ledgerParent, "material.jsonl"))
	if err != nil || strings.Count(string(ledgerBytes), "\n") != 1 {
		t.Fatalf("deterministic material event was not exactly-once: %v %q", err, ledgerBytes)
	}
	terminals, _ := filepath.Glob(filepath.Join(store.Root(), "*-terminal.json"))
	if len(terminals) != 1 {
		t.Fatalf("expected one terminal, got %v", terminals)
	}
	terminalBytes, _ := os.ReadFile(terminals[0])
	if err := os.WriteFile(terminals[0], append(terminalBytes, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Upsert(context.Background(), request); err == nil || !strings.Contains(err.Error(), CodeIntegrityFailure) {
		t.Fatalf("non-canonical terminal tamper was accepted: %v", err)
	}
}

func TestGitHubAdapterRejectsDiscoveryNextLinkAndMalformedWriteSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link", `<https://api.github.com/next>; rel="next"`)
		_, _ = io.WriteString(w, `[]`)
	}))
	defer server.Close()
	adapter, err := newGitHubAdapter(server.Client().Transport, server.URL, githublifecycle.DefaultLimits(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	repo, _ := githublifecycle.NewRepository("octo", "control")
	base, _ := githublifecycle.NewBranch("main")
	head, _ := githublifecycle.NewBranch("feature")
	if _, _, err := adapter.discover(context.Background(), repo, base, head); err == nil || !strings.Contains(err.Error(), CodeDiscoveryTruncated) {
		t.Fatalf("next-page discovery accepted: %v", err)
	}

	malformed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer malformed.Close()
	adapter, _ = newGitHubAdapter(malformed.Client().Transport, malformed.URL, githublifecycle.DefaultLimits(), time.Now)
	if _, err := adapter.write(context.Background(), repo, 0, "title", "", base, head); err == nil {
		t.Fatal("malformed successful write response accepted")
	} else if operation, ok := err.(*Error); !ok || !operation.Submitted {
		t.Fatalf("malformed write response lost submitted provenance: %T %v", err, err)
	}
}

func TestSubmittedAmbiguityNeverCreatesSecondGenerationOrSubmission(t *testing.T) {
	authority := lifecycleAuthority(t)
	var mu sync.Mutex
	postCount := 0
	fixed := time.Unix(1700000100, 0).UTC()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/user":
			_, _ = io.WriteString(w, `{"id":42,"node_id":"U_42","login":"octocat"}`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/ref/heads/"):
			branch := strings.TrimPrefix(r.URL.Path, "/repos/octo/control/git/ref/heads/")
			_, _ = io.WriteString(w, `{"ref":"refs/heads/`+branch+`","object":{"type":"commit","sha":"`+authority.HeadSHA().String()+`"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/octo/control/pulls":
			_, _ = io.WriteString(w, `[]`)
		case r.Method == http.MethodPost:
			mu.Lock()
			postCount++
			mu.Unlock()
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = io.WriteString(w, `{"message":"race"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	adapter, _ := newGitHubAdapter(server.Client().Transport, server.URL, githublifecycle.DefaultLimits(), func() time.Time { return fixed })
	store := provisionStore(t)
	artifacts, _ := evidence.NewStore(filepath.Join(t.TempDir(), "evidence"), "run-ambiguous")
	ledgerParent := t.TempDir()
	_ = os.Chmod(ledgerParent, 0o700)
	recorder, _ := NewMaterialLedgerRecorder(filepath.Join(ledgerParent, "ledger"), nil)
	controller, _ := NewController(ControllerConfig{Store: store, GitHub: adapter, Artifacts: artifacts, Ledger: recorder, Now: func() time.Time { return fixed }})
	request := Request{RunID: "run-ambiguous", Authority: authority, Title: "title", Body: ""}
	if _, err := controller.Upsert(context.Background(), request); err == nil {
		t.Fatal("ambiguous submitted response reported success")
	} else if lifecycleErr, ok := err.(*Error); !ok || !lifecycleErr.Submitted {
		t.Fatalf("ambiguity lost submitted provenance: %T %v", err, err)
	}
	if _, err := controller.Upsert(context.Background(), request); err == nil {
		t.Fatal("immediate ambiguous recovery reported success")
	}
	mu.Lock()
	count := postCount
	mu.Unlock()
	if count != 1 {
		t.Fatalf("submitted revision was replayed %d times", count)
	}
	key, _ := NewPRResourceKey(authority.Repository(), authority.BaseBranch(), authority.HeadBranch())
	matches, _ := filepath.Glob(filepath.Join(store.Root(), recordPrefix(key, 1)+"generation.json"))
	if len(matches) != 1 {
		t.Fatalf("expected exactly one durable generation, got %d", len(matches))
	}
}

func TestExternalHeadPreconditionCreatesNoGeneration(t *testing.T) {
	authority := lifecycleAuthority(t)
	fixture := &githubFixture{headSHA: strings.Repeat("f", 40)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	fixed := time.Unix(1700000200, 0).UTC()
	adapter, _ := newGitHubAdapter(authTransport{server.Client().Transport}, server.URL, githublifecycle.DefaultLimits(), func() time.Time { return fixed })
	store := provisionStore(t)
	artifacts, _ := evidence.NewStore(filepath.Join(t.TempDir(), "evidence"), "run-head")
	ledgerParent := t.TempDir()
	_ = os.Chmod(ledgerParent, 0o700)
	recorder, _ := NewMaterialLedgerRecorder(filepath.Join(ledgerParent, "ledger"), nil)
	controller, _ := NewController(ControllerConfig{Store: store, GitHub: adapter, Artifacts: artifacts, Ledger: recorder, Now: func() time.Time { return fixed }})
	_, err := controller.Upsert(context.Background(), Request{RunID: "run-head", Authority: authority, Title: "title"})
	if err == nil || !strings.Contains(err.Error(), CodeRemoteHeadDiverged) {
		t.Fatalf("wrong remote head was not rejected: %v", err)
	}
	files, _ := filepath.Glob(filepath.Join(store.Root(), "*-generation.json"))
	if len(files) != 0 {
		t.Fatalf("head precondition allocated generations: %v", files)
	}
}

func TestExactRefEscapesBranchAsOnePathComponent(t *testing.T) {
	sha := strings.Repeat("a", 40)
	var requestURI string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestURI = r.RequestURI
		_, _ = io.WriteString(w, `{"ref":"refs/heads/feature/topic","object":{"type":"commit","sha":"`+sha+`"}}`)
	}))
	defer server.Close()
	adapter, _ := newGitHubAdapter(server.Client().Transport, server.URL, githublifecycle.DefaultLimits(), time.Now)
	repository, _ := githublifecycle.NewRepository("octo", "control")
	branch, _ := githublifecycle.NewBranch("feature/topic")
	if _, err := adapter.ref(context.Background(), repository, branch); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(requestURI, "feature%2Ftopic") {
		t.Fatalf("branch was not escaped as one component: %q", requestURI)
	}
}

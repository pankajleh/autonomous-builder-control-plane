//go:build linux

package cilifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
)

type authFunc func(*http.Request) error

func (f authFunc) AuthenticateGitHubRequest(request *http.Request) error { return f(request) }

var sealedTestAuth = authFunc(func(request *http.Request) error {
	request.Header.Set("Authorization", "Bearer sealed-test-token")
	return nil
})

type incrementingClock struct{ value atomic.Int64 }

func newIncrementingClock() *incrementingClock {
	clock := &incrementingClock{}
	clock.value.Store(time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC).UnixNano())
	return clock
}

func (c *incrementingClock) Now() time.Time {
	return time.Unix(0, c.value.Add(1)).UTC()
}

func testCollectionAuthority(t *testing.T) githublifecycle.Authority {
	t.Helper()
	repositoryPath := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(repositoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) string {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = repositoryPath
		command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "LC_ALL=C")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return string(output)
	}
	runGit("init", "-q")
	runGit("config", "user.name", "ABCP Test")
	runGit("config", "user.email", "abcp@example.invalid")
	if err := os.WriteFile(filepath.Join(repositoryPath, "content.txt"), []byte("accepted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "content.txt")
	runGit("commit", "-q", "-m", "accepted")
	head, err := githublifecycle.NewGitSHA(strings.TrimSpace(runGit("rev-parse", "HEAD")))
	if err != nil {
		t.Fatal(err)
	}
	store, err := evidence.NewStore(filepath.Join(t.TempDir(), "evidence"), "phase3")
	if err != nil {
		t.Fatal(err)
	}
	decision, _ := json.Marshal(map[string]any{
		"state": "READY_FOR_MERGE",
		"combined": map[string]any{"target": map[string]any{
			"head_sha":    head.String(),
			"integration": map[string]any{"integrated_head_sha": head.String(), "baseline_sha": head.String()},
		}},
	})
	ref, err := store.WriteBytes("ready.json", "serial-integration-gate-decision", decision)
	if err != nil {
		t.Fatal(err)
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	expected, err := githublifecycle.DeriveExpectedMergeContent(context.Background(), repositoryPath, store.Root(), head, head, ref, gitPath)
	if err != nil {
		t.Fatal(err)
	}
	repository, _ := githublifecycle.NewRepository("Octo-Org", "control-plane")
	base, _ := githublifecycle.NewBranch("main")
	branch, _ := githublifecycle.NewBranch("feature/exact-head")
	actor, _ := githublifecycle.NewUserIdentity("github-user-id:42")
	authority, err := githublifecycle.NewAuthority(githublifecycle.AuthorityInput{
		Repository: repository, BaseBranch: base, HeadBranch: branch, HeadSHA: head,
		ExpectedBaseTipSHA: head, AllowedMergeMethod: githublifecycle.MergeMethodSquash,
		Actor: actor, ExpectedContent: expected,
	})
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

type fixtureMode string

const (
	modeEmpty          fixtureMode = "empty"
	modePending        fixtureMode = "pending"
	modeFailure        fixtureMode = "failure"
	modeSuccess        fixtureMode = "success"
	modeRerequest      fixtureMode = "rerequest"
	modeStatusMutation fixtureMode = "status-mutation"
	modeBadState       fixtureMode = "bad-state"
	modeBadSuiteSHA    fixtureMode = "bad-suite-sha"
	modeBadRelation    fixtureMode = "bad-relation"
)

type githubFixture struct {
	t         *testing.T
	authority githublifecycle.Authority
	mode      fixtureMode
	requests  atomic.Int64
	suites    atomic.Int64
	statuses  atomic.Int64
	heads     atomic.Int64
	moveHead  int64
	requestID atomic.Int64
}

func (f *githubFixture) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	f.requests.Add(1)
	if request.Method != http.MethodGet || request.ContentLength != 0 {
		f.t.Errorf("unsafe request: method=%s body=%v length=%d", request.Method, request.Body, request.ContentLength)
	}
	if request.Header.Get("Authorization") != "Bearer sealed-test-token" || request.Header.Get("Accept") != githubAccept || request.Header.Get("X-GitHub-Api-Version") != githubAPIVer {
		f.t.Errorf("missing sealed headers: %#v", request.Header)
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("X-GitHub-Request-Id", "request-"+strconv.FormatInt(f.requestID.Add(1), 10))
	headSHA := f.authority.HeadSHA().String()
	repositoryPrefix := "/repos/" + f.authority.Repository().Owner() + "/" + f.authority.Repository().Name()
	switch {
	case request.URL.Path == "/user":
		writeFixtureJSON(f.t, writer, map[string]any{"id": 42, "node_id": "U_kw/+==", "login": "ExactCase"})
	case strings.HasPrefix(request.URL.Path, repositoryPrefix+"/git/ref/heads/"):
		call := f.heads.Add(1)
		if f.moveHead == call {
			headSHA = strings.Repeat("f", 40)
		}
		writeFixtureJSON(f.t, writer, map[string]any{"ref": "refs/heads/" + f.authority.HeadBranch().String(), "object": map[string]any{"type": "commit", "sha": headSHA}})
	case request.URL.Path == repositoryPrefix+"/commits/"+headSHA+"/check-suites":
		f.requirePageQuery(request, true)
		call := f.suites.Add(1)
		items := f.suiteItems(call, headSHA)
		writeFixtureJSON(f.t, writer, map[string]any{"total_count": len(items), "check_suites": items})
	case request.URL.Path == repositoryPrefix+"/commits/"+headSHA+"/check-runs":
		f.requirePageQuery(request, true)
		writeFixtureJSON(f.t, writer, map[string]any{"total_count": len(f.runItems(headSHA)), "check_runs": f.runItems(headSHA)})
	case request.URL.Path == repositoryPrefix+"/commits/"+headSHA+"/statuses":
		f.requirePageQuery(request, false)
		call := f.statuses.Add(1)
		writeFixtureJSON(f.t, writer, f.statusItems(call, headSHA))
	default:
		f.t.Errorf("unexpected endpoint %s?%s", request.URL.Path, request.URL.RawQuery)
		http.NotFound(writer, request)
	}
}

func (f *githubFixture) requirePageQuery(request *http.Request, filter bool) {
	f.t.Helper()
	query := request.URL.Query()
	if query.Get("page") != "1" || query.Get("per_page") != "64" || (filter && query.Get("filter") != "all") || (!filter && query.Has("filter")) {
		f.t.Errorf("unexpected query: %s", request.URL.RawQuery)
	}
}

func (f *githubFixture) suiteItems(call int64, headSHA string) []any {
	if f.mode == modeEmpty {
		return []any{}
	}
	status := "completed"
	var conclusion any = "success"
	if f.mode == modePending || (f.mode == modeRerequest && call == 2) {
		status, conclusion = "queued", nil
	}
	if f.mode == modeFailure {
		conclusion = "failure"
	}
	if f.mode == modeBadState {
		status, conclusion = "mystery", nil
	}
	if f.mode == modeBadSuiteSHA {
		headSHA = strings.Repeat("e", 40)
	}
	return []any{map[string]any{
		"id": 11, "node_id": "CS_kw/+==", "head_sha": headSHA, "status": status, "conclusion": conclusion,
		"app": map[string]any{"id": 7}, "created_at": "2026-09-08T10:00:00Z", "updated_at": "2026-09-08T10:01:00Z",
	}}
}

func (f *githubFixture) runItems(headSHA string) []any {
	if f.mode == modeEmpty {
		return []any{}
	}
	status := "completed"
	var conclusion any = "success"
	var completed any = "2026-09-08T10:02:00Z"
	if f.mode == modePending {
		status, conclusion, completed = "pending", nil, nil
	}
	if f.mode == modeFailure {
		conclusion = "failure"
	}
	suiteID := int64(11)
	if f.mode == modeBadRelation {
		suiteID = 99
	}
	return []any{map[string]any{
		"id": 21, "node_id": "CR_kw/+==", "name": "Build", "head_sha": headSHA,
		"status": status, "conclusion": conclusion, "check_suite": map[string]any{"id": suiteID}, "app": map[string]any{"id": 7},
		"started_at": "2026-09-08T10:01:00Z", "completed_at": completed,
	}}
}

func (f *githubFixture) statusItems(call int64, headSHA string) []any {
	if f.mode == modeEmpty {
		return []any{}
	}
	state := "success"
	if f.mode == modeFailure {
		state = "failure"
	}
	if f.mode == modeStatusMutation && call == 2 {
		state = "pending"
	}
	return []any{map[string]any{
		"id": 31, "node_id": "SC_kw/+==", "state": state, "context": "Exact/Context", "description": "kept",
		"sha": headSHA, "created_at": "2026-09-08T09:59:00Z", "updated_at": "2026-09-08T10:03:00Z",
		"creator": map[string]any{"id": 8, "node_id": "U_bot/+==", "login": "CaseBot", "type": "Bot"},
	}}
}

func writeFixtureJSON(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Error(err)
	}
}

func collectFixture(t *testing.T, authority githublifecycle.Authority, mode fixtureMode, moveHead int64, attempt string) (CIEvidenceBundleV1, error, *githubFixture) {
	t.Helper()
	fixture := &githubFixture{t: t, authority: authority, mode: mode, moveHead: moveHead}
	server := httptest.NewServer(fixture)
	t.Cleanup(server.Close)
	clock := newIncrementingClock()
	adapter, err := newGitHubAdapter(server.Client().Transport, sealedTestAuth, server.URL, productionLimits(), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	bundle, collectErr := adapter.collectRemote(context.Background(), CollectRequest{RunID: "run-ci", AttemptID: attempt, Authority: authority})
	return bundle, collectErr, fixture
}

func TestStableEvidenceIsNeutralAcrossProviderStates(t *testing.T) {
	authority := testCollectionAuthority(t)
	for _, mode := range []fixtureMode{modeEmpty, modePending, modeFailure, modeSuccess} {
		t.Run(string(mode), func(t *testing.T) {
			bundle, err, fixture := collectFixture(t, authority, mode, 0, "attempt-"+string(mode))
			if err != nil {
				t.Fatal(err)
			}
			input := bundle.Input()
			if input.Outcome != OutcomeStable || input.CollectionIdentitySHA256 == "" || input.SemanticDigestA != input.SemanticDigestB {
				t.Fatalf("provider state was interpreted as policy: %#v", input)
			}
			if fixture.requests.Load() != 10 || len(input.RequestProvenance) != 10 || len(input.HeadObservations) != 3 {
				t.Fatalf("fixed sequence: requests=%d provenance=%d heads=%d", fixture.requests.Load(), len(input.RequestProvenance), len(input.HeadObservations))
			}
			if strings.Contains(string(bundle.CanonicalJSON()), "ci_"+"satisfied") || strings.Contains(string(bundle.CanonicalJSON()), "merge_"+"approved") {
				t.Fatal("neutral evidence contains a policy result")
			}
			for _, item := range input.RequestProvenance {
				p := item.Input()
				if p.Method != http.MethodGet || p.RequestSHA256 == "" || p.ResponseBodySHA256 == "" || p.ResponseEnvelopeSHA256 == "" {
					t.Fatalf("incomplete provenance: %#v", p)
				}
			}
		})
	}
}

func TestTwoSweepAndHeadMutationsFailClosed(t *testing.T) {
	authority := testCollectionAuthority(t)
	cases := []struct {
		name     string
		mode     fixtureMode
		moveHead int64
		outcome  CollectionOutcome
		code     string
	}{
		{"suite-rerequest", modeRerequest, 0, OutcomeUnstable, "semantic_sweeps_differ"},
		{"status-in-place", modeStatusMutation, 0, OutcomeUnstable, "semantic_sweeps_differ"},
		{"moved-h0", modeSuccess, 1, OutcomeStaleHead, "head_moved_h0"},
		{"moved-h1", modeSuccess, 2, OutcomeStaleHead, "head_moved_h1"},
		{"moved-h2", modeSuccess, 3, OutcomeStaleHead, "head_moved_h2"},
		{"unsupported-state", modeBadState, 0, OutcomeMalformed, "malformed_suite"},
		{"item-sha", modeBadSuiteSHA, 0, OutcomeStaleHead, "suite_head_mismatch"},
		{"suite-relationship", modeBadRelation, 0, OutcomeMalformed, "invalid_semantic_sweep"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			bundle, err, _ := collectFixture(t, authority, test.mode, test.moveHead, "attempt-"+test.name)
			var collectionErr *CollectionError
			if !errors.As(err, &collectionErr) || collectionErr.Outcome != test.outcome || collectionErr.FailureCode != test.code {
				t.Fatalf("got error %#v, want %s/%s", err, test.outcome, test.code)
			}
			if bundle.Input().Outcome != test.outcome || bundle.Input().CollectionIdentitySHA256 != "" {
				t.Fatalf("unexpected failure bundle: %#v", bundle.Input())
			}
		})
	}
}

func TestRequestAndResponseVariationDoesNotAlterSemanticStability(t *testing.T) {
	authority := testCollectionAuthority(t)
	fixture := &githubFixture{t: t, authority: authority, mode: modeSuccess}
	server := httptest.NewServer(fixture)
	defer server.Close()
	clock := newIncrementingClock()
	adapter, err := newGitHubAdapter(server.Client().Transport, sealedTestAuth, server.URL, productionLimits(), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	first, firstErr := adapter.collectRemote(context.Background(), CollectRequest{RunID: "run-ci", AttemptID: "first", Authority: authority})
	second, secondErr := adapter.collectRemote(context.Background(), CollectRequest{RunID: "run-ci", AttemptID: "second", Authority: authority})
	if firstErr != nil || secondErr != nil {
		t.Fatalf("collections: %v / %v", firstErr, secondErr)
	}
	a, b := first.Input(), second.Input()
	if a.SemanticDigestA != b.SemanticDigestA || a.CollectionIdentitySHA256 != b.CollectionIdentitySHA256 {
		t.Fatal("request IDs, observation times, or attempt identity altered semantic/collection identity")
	}
	if a.AttemptKeySHA256 == b.AttemptKeySHA256 {
		t.Fatal("distinct attempt IDs did not alter attempt identity")
	}
}

func strictPaginationLimits() Limits {
	limits := productionLimits()
	limits.maxSuites, limits.maxRuns, limits.maxStatuses = 2, 2, 2
	limits.itemsPerPage = 1
	limits.maxSuitePages, limits.maxRunPages, limits.maxStatusPages = 2, 2, 3
	limits.maxCollectionRequests = 18
	return limits
}

func TestPaginationCompletenessAndInstability(t *testing.T) {
	headSHA := strings.Repeat("a", 40)
	suite := func(id int64) map[string]any {
		return map[string]any{"id": id, "node_id": "node-" + strconv.FormatInt(id, 10), "head_sha": headSHA, "status": "queued", "conclusion": nil, "app": map[string]any{"id": 7}, "created_at": "2026-09-08T10:00:00Z", "updated_at": "2026-09-08T10:00:00Z"}
	}
	tests := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		outcome CollectionOutcome
		code    string
		count   int
	}{
		{"local-page-generation", func(w http.ResponseWriter, r *http.Request) {
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			writeFixtureJSON(t, w, map[string]any{"total_count": 2, "check_suites": []any{suite(int64(page))}})
		}, "", "", 2},
		{"changed-total", func(w http.ResponseWriter, r *http.Request) {
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			writeFixtureJSON(t, w, map[string]any{"total_count": 3 - page, "check_suites": []any{suite(int64(page))}})
		}, OutcomeUnstable, "suite_total_changed", 2},
		{"short-before-total", func(w http.ResponseWriter, _ *http.Request) {
			writeFixtureJSON(t, w, map[string]any{"total_count": 2, "check_suites": []any{}})
		}, OutcomeUnstable, "suite_short_page", 1},
		{"total-over-limit", func(w http.ResponseWriter, _ *http.Request) {
			writeFixtureJSON(t, w, map[string]any{"total_count": 3, "check_suites": []any{suite(1)}})
		}, OutcomeTruncated, "suite_total_too_large", 1},
		{"duplicate-across-pages", func(w http.ResponseWriter, _ *http.Request) {
			writeFixtureJSON(t, w, map[string]any{"total_count": 2, "check_suites": []any{suite(1)}})
		}, OutcomeUnstable, "duplicate_id_across_pages", 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				test.handler(w, r)
			}))
			defer server.Close()
			clock := newIncrementingClock()
			adapter, err := newGitHubAdapter(server.Client().Transport, sealedTestAuth, server.URL, strictPaginationLimits(), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			session := &requestSession{adapter: adapter}
			items, total, failed := session.suites(context.Background(), "o", "r", headSHA, "suites_a")
			if test.outcome == "" {
				if failed != nil || len(items) != 2 || total != 2 {
					t.Fatalf("complete pagination failed: items=%d total=%d failure=%#v", len(items), total, failed)
				}
			} else if failed == nil || failed.outcome != test.outcome || failed.code != test.code {
				t.Fatalf("got %#v, want %s/%s", failed, test.outcome, test.code)
			}
			if got := int(calls.Load()); got != test.count {
				t.Fatalf("calls=%d want=%d", got, test.count)
			}
		})
	}
}

func TestLegacyStatusSentinelPage(t *testing.T) {
	headSHA := strings.Repeat("a", 40)
	status := func(id int64) map[string]any {
		return map[string]any{"id": id, "node_id": "status-" + strconv.FormatInt(id, 10), "state": "pending", "context": "Build", "description": nil, "sha": headSHA, "created_at": "2026-09-08T10:00:00Z", "updated_at": "2026-09-08T10:00:00Z", "creator": map[string]any{"id": 7, "node_id": "creator", "login": "Bot", "type": "Bot"}}
	}
	for _, nonemptySentinel := range []bool{false, true} {
		t.Run("nonempty="+strconv.FormatBool(nonemptySentinel), func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				calls.Add(1)
				if page <= 2 || (page == 3 && nonemptySentinel) {
					writeFixtureJSON(t, w, []any{status(int64(page))})
					return
				}
				writeFixtureJSON(t, w, []any{})
			}))
			defer server.Close()
			clock := newIncrementingClock()
			adapter, err := newGitHubAdapter(server.Client().Transport, sealedTestAuth, server.URL, strictPaginationLimits(), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			items, failed := (&requestSession{adapter: adapter}).statuses(context.Background(), "o", "r", headSHA, "statuses_a")
			if nonemptySentinel {
				if failed == nil || failed.outcome != OutcomeTruncated || failed.code != "status_total_too_large" {
					t.Fatalf("sentinel failure=%#v", failed)
				}
			} else if failed != nil || len(items) != 2 {
				t.Fatalf("complete status history: items=%d failure=%#v", len(items), failed)
			}
			if calls.Load() != 3 {
				t.Fatalf("sentinel requests=%d", calls.Load())
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func responseTransport(body []byte, header http.Header) http.RoundTripper {
	return roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: header.Clone(), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
}

func TestTransportBodyAndHeaderExactBounds(t *testing.T) {
	limits := productionLimits()
	limits.maxResponseBodyBytes = 16
	limits.maxResponseHeaderBytes = 16
	clock := newIncrementingClock()
	spec := requestSpec{phase: "principal", pathTemplate: "/user", escapedPath: "/user"}
	tests := []struct {
		name    string
		body    int
		header  int
		outcome CollectionOutcome
		code    string
	}{
		{"at-cap", 16, 16, "", ""},
		{"body-cap-plus-one", 17, 16, OutcomeTruncated, "response_body_too_large"},
		{"header-cap-plus-one", 16, 17, OutcomeTruncated, "response_headers_too_large"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header := http.Header{"X": []string{strings.Repeat("h", test.header-7)}}
			adapter, err := newGitHubAdapter(responseTransport([]byte(strings.Repeat("b", test.body)), header), sealedTestAuth, "https://example.invalid", limits, clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			session := &requestSession{adapter: adapter}
			_, failed := session.get(context.Background(), spec)
			if test.outcome == "" && failed != nil {
				t.Fatal(failed)
			}
			if test.outcome != "" && (failed == nil || failed.outcome != test.outcome || failed.code != test.code) {
				t.Fatalf("failure=%#v want=%s/%s", failed, test.outcome, test.code)
			}
			if len(session.provenance) != 1 || session.provenance[0].Input().ResponseBytes != int64(min(test.body, 17)) {
				t.Fatalf("unexpected provenance: %#v", session.provenance)
			}
		})
	}
}

func TestTransportCancellationRedirectAndZeroRetries(t *testing.T) {
	limits := productionLimits()
	limits.callTimeout = 5 * time.Millisecond
	limits.collectionTimeout = 20 * time.Millisecond
	var calls atomic.Int64
	blocking := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	clock := newIncrementingClock()
	adapter, err := newGitHubAdapter(blocking, sealedTestAuth, "https://example.invalid", limits, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, failed := (&requestSession{adapter: adapter}).get(context.Background(), requestSpec{phase: "principal", pathTemplate: "/user", escapedPath: "/user"})
	if failed == nil || failed.outcome != OutcomeProviderUnavailable || failed.code != "request_timeout" || calls.Load() != 1 {
		t.Fatalf("timeout/retry result: failure=%#v calls=%d", failed, calls.Load())
	}

	var redirectCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectCalls.Add(1)
		http.Redirect(w, r, "/other", http.StatusFound)
	}))
	defer server.Close()
	adapter, _ = newGitHubAdapter(server.Client().Transport, sealedTestAuth, server.URL, productionLimits(), clock.Now)
	session := &requestSession{adapter: adapter}
	_, failed = session.get(context.Background(), requestSpec{phase: "principal", pathTemplate: "/user", escapedPath: "/user"})
	if failed == nil || failed.outcome != OutcomeProviderUnavailable || redirectCalls.Load() != 1 {
		t.Fatalf("redirect followed or misclassified: failure=%#v calls=%d", failed, redirectCalls.Load())
	}
}

func TestCollectionDeadlineBoundsTheWholeObservation(t *testing.T) {
	authority := testCollectionAuthority(t)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/user" {
			writeFixtureJSON(t, w, map[string]any{"id": 42, "node_id": "user-node", "login": "User"})
			return
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	limits := productionLimits()
	limits.callTimeout = 10 * time.Millisecond
	limits.collectionTimeout = 10 * time.Millisecond
	clock := newIncrementingClock()
	adapter, err := newGitHubAdapter(server.Client().Transport, sealedTestAuth, server.URL, limits, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	bundle, collectErr := adapter.collectRemote(context.Background(), CollectRequest{RunID: "deadline", AttemptID: "one", Authority: authority})
	var typed *CollectionError
	if !errors.As(collectErr, &typed) || typed.Outcome != OutcomeProviderUnavailable || typed.FailureCode != "request_timeout" {
		t.Fatalf("collection deadline result: bundle=%s error=%#v", bundle.CanonicalJSON(), collectErr)
	}
	if calls.Load() != 2 || len(bundle.Input().RequestProvenance) != 2 {
		t.Fatalf("deadline retried or lost provenance: calls=%d provenance=%d", calls.Load(), len(bundle.Input().RequestProvenance))
	}
}

func TestPrincipalIntegrityAndMalformedJSON(t *testing.T) {
	authority := testCollectionAuthority(t)
	fixture := &githubFixture{t: t, authority: authority, mode: modeSuccess}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			writeFixtureJSON(t, w, map[string]any{"id": 99, "node_id": "other", "login": "Other"})
			return
		}
		fixture.ServeHTTP(w, r)
	}))
	defer server.Close()
	clock := newIncrementingClock()
	adapter, err := newGitHubAdapter(server.Client().Transport, sealedTestAuth, server.URL, productionLimits(), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	bundle, collectErr := adapter.collectRemote(context.Background(), CollectRequest{RunID: "principal", AttemptID: "mismatch", Authority: authority})
	var typed *CollectionError
	if !errors.As(collectErr, &typed) || typed.Outcome != OutcomeIntegrityFailure || typed.FailureCode != "authenticated_user_mismatch" || bundle.Input().AuthenticatedID != 99 {
		t.Fatalf("principal mismatch: bundle=%#v error=%#v", bundle.Input(), collectErr)
	}

	malformed := responseTransport([]byte(`{"total_count":`), http.Header{})
	adapter, _ = newGitHubAdapter(malformed, sealedTestAuth, "https://example.invalid", productionLimits(), clock.Now)
	_, _, failed := (&requestSession{adapter: adapter}).suites(context.Background(), "o", "r", strings.Repeat("a", 40), "suites_a")
	if failed == nil || failed.outcome != OutcomeMalformed || failed.code != "malformed_suite_page" {
		t.Fatalf("malformed JSON result: %#v", failed)
	}
}

func TestTextLinkRequestAndClockCaps(t *testing.T) {
	limits := productionLimits()
	limits.maxTextBytes = 8
	clock := newIncrementingClock()
	principalBody := []byte(`{"id":42,"node_id":"123456789","login":"user"}`)
	adapter, err := newGitHubAdapter(responseTransport(principalBody, http.Header{}), sealedTestAuth, "https://example.invalid", limits, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, failed := (&requestSession{adapter: adapter}).principal(context.Background())
	if failed == nil || failed.outcome != OutcomeTruncated || failed.code != "principal_text_too_large" {
		t.Fatalf("text cap result: %#v", failed)
	}

	limits = productionLimits()
	limits.maxLinkBytes = 8
	adapter, _ = newGitHubAdapter(responseTransport([]byte(`{}`), http.Header{"Link": []string{strings.Repeat("x", 9)}}), sealedTestAuth, "https://example.invalid", limits, clock.Now)
	_, failed = (&requestSession{adapter: adapter}).get(context.Background(), requestSpec{phase: "principal", pathTemplate: "/user", escapedPath: "/user"})
	if failed == nil || failed.outcome != OutcomeTruncated || failed.code != "link_header_too_large" {
		t.Fatalf("Link cap result: %#v", failed)
	}

	limits = productionLimits()
	limits.maxCollectionRequests = 1
	adapter, _ = newGitHubAdapter(responseTransport([]byte(`{}`), http.Header{}), sealedTestAuth, "https://example.invalid", limits, clock.Now)
	session := &requestSession{adapter: adapter}
	if _, failed = session.get(context.Background(), requestSpec{phase: "one", pathTemplate: "/user", escapedPath: "/user"}); failed != nil {
		t.Fatal(failed)
	}
	if _, failed = session.get(context.Background(), requestSpec{phase: "two", pathTemplate: "/user", escapedPath: "/user"}); failed == nil || failed.outcome != OutcomeTruncated || failed.code != "request_limit_exceeded" {
		t.Fatalf("request cap result: %#v", failed)
	}

	var tick atomic.Int64
	invertingClock := func() time.Time {
		if tick.Add(1) == 1 {
			return time.Unix(0, 2).UTC()
		}
		return time.Unix(0, 1).UTC()
	}
	adapter, _ = newGitHubAdapter(responseTransport([]byte(`{}`), http.Header{}), sealedTestAuth, "https://example.invalid", productionLimits(), invertingClock)
	_, failed = (&requestSession{adapter: adapter}).get(context.Background(), requestSpec{phase: "clock", pathTemplate: "/user", escapedPath: "/user"})
	if failed == nil || failed.outcome != OutcomeIntegrityFailure || failed.code != "clock_inversion" {
		t.Fatalf("clock inversion result: %#v", failed)
	}
}

func TestSealedAuthenticatorRejectsRequestMutationBeforeNetwork(t *testing.T) {
	mutations := map[string]func(*http.Request){
		"method":         func(r *http.Request) { r.Method = "POST" },
		"url":            func(r *http.Request) { r.URL.Path = "/changed" },
		"host":           func(r *http.Request) { r.Host = "changed.invalid" },
		"header":         func(r *http.Request) { r.Header.Set("X-Changed", "yes") },
		"content-length": func(r *http.Request) { r.ContentLength = 1 },
		"body":           func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("x")) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int64
			base := roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return nil, errors.New("must not run")
			})
			auth := authFunc(func(request *http.Request) error {
				request.Header.Set("Authorization", "Bearer token")
				mutate(request)
				return nil
			})
			clock := newIncrementingClock()
			adapter, err := newGitHubAdapter(base, auth, "https://example.invalid", productionLimits(), clock.Now)
			if err != nil {
				t.Fatal(err)
			}
			_, failed := (&requestSession{adapter: adapter}).get(context.Background(), requestSpec{phase: "principal", pathTemplate: "/user", escapedPath: "/user"})
			if failed == nil || failed.outcome != OutcomeIntegrityFailure || failed.code != "authenticator_mutated_request" || calls.Load() != 0 {
				t.Fatalf("mutation escaped seal: failure=%#v calls=%d", failed, calls.Load())
			}
		})
	}
	transport := &authenticatedGitHubTransport{base: responseTransport([]byte("{}"), http.Header{}), authenticator: sealedTestAuth}
	request, _ := http.NewRequest("POST", "https://example.invalid/user", strings.NewReader("x"))
	if _, err := transport.RoundTrip(request); err == nil {
		t.Fatal("authenticated transport accepted non-GET construction")
	}
}

func TestRequestChainAndCollectionIdentityGolden(t *testing.T) {
	provenance := make([]RequestProvenanceV1, 2)
	for i, bodyDigest := range []string{strings.Repeat("b", 64), strings.Repeat("d", 64)} {
		input := RequestProvenanceV1Input{
			Sequence: i + 1, Phase: "test", Method: http.MethodGet, PathTemplate: "/user", EscapedPath: "/user",
			CanonicalQuery: "page=" + strconv.Itoa(i+1), APIOrigin: "https://example.invalid", APIVersion: githubAPIVer, Accept: githubAccept,
			HTTPStatus: http.StatusOK, ResponseBodySHA256: bodyDigest,
			RequestStartedUnixNano: int64(i + 1), ResponseObservedUnixNano: int64(i + 2),
		}
		input.RequestSHA256 = requestIdentitySHA256(input)
		input.ResponseEnvelopeSHA256 = responseEnvelopeSHA256(input)
		value, err := NewRequestProvenanceV1(input)
		if err != nil {
			t.Fatal(err)
		}
		provenance[i] = value
	}
	chain, links, err := requestResponseChain(provenance)
	if err != nil {
		t.Fatal(err)
	}
	identity := collectionIdentitySHA256("Octo", "Repo", strings.Repeat("1", 40), strings.Repeat("e", 64), links)
	if chain != "e751f434aebde978b28bbdbeaf80b2b3f9ba98b70253df00a2e943e48b0c3fd0" {
		t.Fatalf("chain golden changed: %s", chain)
	}
	if identity != "fa41284457511bf70cdef9c0ed141e87023de0e197ffd96a4a75d15f150e62b9" {
		t.Fatalf("collection identity golden changed: %s", identity)
	}
	for name, invalid := range map[string][]RequestProvenanceV1{
		"omitted":    {provenance[1]},
		"reordered":  {provenance[1], provenance[0]},
		"duplicated": {provenance[0], provenance[0]},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := requestResponseChain(invalid); err == nil {
				t.Fatal("invalid provenance chain accepted")
			}
		})
	}
}

func TestPreNetworkValidationAndRequestIdentity(t *testing.T) {
	var calls atomic.Int64
	base := roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unexpected")
	})
	clock := newIncrementingClock()
	adapter, err := newGitHubAdapter(base, sealedTestAuth, "https://example.invalid", productionLimits(), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, collectErr := adapter.collectRemote(context.Background(), CollectRequest{RunID: "bad", AttemptID: "unsafe/path"})
	if collectErr == nil || calls.Load() != 0 {
		t.Fatalf("invalid request reached network: err=%v calls=%d", collectErr, calls.Load())
	}
	authority := testCollectionAuthority(t)
	first, firstErr := validateCollectRequest(CollectRequest{RunID: "run", AttemptID: "a", Authority: authority})
	second, secondErr := validateCollectRequest(CollectRequest{RunID: "run", AttemptID: "b", Authority: authority})
	if firstErr != nil || secondErr != nil || first.attemptKey == second.attemptKey || first.authoritySHA256 != second.authoritySHA256 {
		t.Fatalf("attempt identity binding failed: %#v %#v", first, second)
	}
}

func TestExactBodyDigest(t *testing.T) {
	body := []byte("exact bounded body")
	if got := digestBytes(body); got != func() string {
		sum := sha256.Sum256(body)
		return hex.EncodeToString(sum[:])
	}() {
		t.Fatalf("body digest mismatch: %s", got)
	}
}

package githubmergeprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/mergelifecycle"
)

func TestTask3ClosureC01ReconciliationIdentityContainment(t *testing.T) {
	fixture := newProviderFixture(t, nil)
	tests := []struct {
		name       string
		baseTip    string
		mutate     func(*targetVerificationResponse)
		comparison compareResponse
		want       githublifecycle.ReconciliationDisposition
	}{
		{name: "fresh exact identity and identical containment", comparison: exactComparison(fixture, "identical", 0, 0), want: githublifecycle.ReconciliationApplied},
		{name: "fresh exact identity and descendant containment", baseTip: strings.Repeat("5", 40),
			comparison: exactComparison(fixture, "ahead", 1, 0), want: githublifecycle.ReconciliationApplied},
		{name: "open pull request", mutate: func(value *targetVerificationResponse) {
			open, no := "OPEN", false
			value.Data.Repository.PullRequest.State, value.Data.Repository.PullRequest.Merged, value.Data.Repository.PullRequest.MergedAt = open, &no, nil
		}, comparison: exactComparison(fixture, "identical", 0, 0), want: githublifecycle.ReconciliationUnknown},
		{name: "wrong authenticated principal", mutate: func(value *targetVerificationResponse) { value.Data.Viewer.ID = "U_other" },
			comparison: exactComparison(fixture, "identical", 0, 0), want: githublifecycle.ReconciliationUnknown},
		{name: "wrong repository identity", mutate: func(value *targetVerificationResponse) { value.Data.Repository.ID = "R_other" },
			comparison: exactComparison(fixture, "identical", 0, 0), want: githublifecycle.ReconciliationUnknown},
		{name: "missing exact result object", mutate: func(value *targetVerificationResponse) { value.Data.Repository.Object = nil },
			comparison: exactComparison(fixture, "identical", 0, 0), want: githublifecycle.ReconciliationUnknown},
		{name: "diverged containment", comparison: exactComparison(fixture, "diverged", 0, 0), want: githublifecycle.ReconciliationUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			baseTip := test.baseTip
			if baseTip == "" {
				baseTip = fixture.recipe.ExpectedResultSHA().String()
			}
			body := targetVerificationBody(t, fixture, baseTip, true)
			if test.mutate != nil {
				var value targetVerificationResponse
				if err := json.Unmarshal(body, &value); err != nil {
					t.Fatal(err)
				}
				test.mutate(&value)
				body = mustValue(json.Marshal(value))
			}
			transport := &scriptTransport{handler: func(call recordedRequest) (*http.Response, error) {
				if call.Method == http.MethodPost {
					var request struct {
						Query string `json:"query"`
					}
					if json.Unmarshal(call.Body, &request) != nil || request.Query != targetVerificationQueryV1 {
						t.Fatal("reconciliation reached a mutation query")
					}
					return githubResponse(200, "fresh-identity", body), nil
				}
				if strings.Contains(call.URL, "/compare/") {
					return githubResponse(200, "fresh-compare", mustValue(json.Marshal(test.comparison))), nil
				}
				return nil, errors.New("unexpected reconciliation route")
			}}
			provider := mustValue(newTestProvider(fixture.auth, fixture.limits, transport))
			input := mustValue(githublifecycle.NewMergeReconcileWriteInput(fixture.sealed, fixture.submission,
				[]ledger.EvidenceRef{closureEvidenceRef()}, fixture.limits))
			outcome, err := provider.ReconcileTarget(providerTestContext(), input)
			if err != nil || outcome.Disposition != test.want {
				t.Fatalf("disposition=%s want=%s err=%v", outcome.Disposition, test.want, err)
			}
		})
	}
}

func exactComparison(f providerFixture, status string, ahead, behind int) compareResponse {
	value := compareResponse{Status: status, AheadBy: ahead, BehindBy: behind, TotalCommits: ahead}
	value.BaseCommit.SHA = f.recipe.ExpectedResultSHA().String()
	value.MergeBaseCommit.SHA = f.recipe.ExpectedResultSHA().String()
	return value
}

func closureEvidenceRef() ledger.EvidenceRef {
	return ledger.EvidenceRef{URI: "evidence/closure-submission", Kind: "target-submission", SHA256: strings.Repeat("a", 64)}
}

type recordingHandoff struct {
	mu     sync.Mutex
	budget mergelifecycle.ProviderBudgetV1
	events []string
}

type recordingReservation struct {
	owner *recordingHandoff
	class mergelifecycle.ProviderCallClassV1
}

func (h *recordingHandoff) ReserveHTTPCall(class mergelifecycle.ProviderCallClassV1) (mergelifecycle.ProviderHTTPCallReservationV1, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, "reserve:"+string(class))
	return &recordingReservation{owner: h, class: class}, nil
}
func (r *recordingReservation) Budget() mergelifecycle.ProviderBudgetV1 { return r.owner.budget }
func (r *recordingReservation) Complete(mergelifecycle.ProviderCallAccountingV1) error {
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	r.owner.events = append(r.owner.events, "complete:"+string(r.class))
	return nil
}
func (h *recordingHandoff) snapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.events...)
}

type advancingBody struct {
	reader  io.Reader
	advance func()
	once    sync.Once
	closed  atomic.Bool
}

func (b *advancingBody) Read(data []byte) (int, error) {
	b.once.Do(b.advance)
	return b.reader.Read(data)
}
func (b *advancingBody) Close() error {
	b.advance()
	b.closed.Store(true)
	return nil
}

func TestTask3ClosureM01ProviderRequestBoundary(t *testing.T) {
	fixture := newProviderFixture(t, nil)
	var network atomic.Int64
	provider := mustValue(newTestProvider(fixture.auth, fixture.limits, roundTripFunc(func(*http.Request) (*http.Response, error) {
		network.Add(1)
		return githubResponse(200, "unexpected", []byte(`{}`)), nil
	})))
	if _, err := provider.PrepareResultCommit(context.Background(), fixture.recipe); err == nil || network.Load() != 0 {
		t.Fatalf("missing controller handoff reached network: calls=%d err=%v", network.Load(), err)
	}

	var clockMu sync.Mutex
	clock := time.Unix(1700000000, 0)
	advance := func() { clockMu.Lock(); clock = clock.Add(7 * time.Millisecond); clockMu.Unlock() }
	provider.now = func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return clock }
	budget := providerTestBudget()
	budget.ActiveNanos = int64(100 * time.Millisecond)
	handoff := &recordingHandoff{budget: budget}
	provider.readClient = &http.Client{Transport: sealedRoundTripper{auth: fixture.auth, base: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		deadline, ok := request.Context().Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 150*time.Millisecond {
			t.Fatalf("request deadline was not capped by remaining active time: %v, %v", deadline, ok)
		}
		body := &advancingBody{reader: bytes.NewReader([]byte(`{"ok":true}`)), advance: advance}
		return &http.Response{StatusCode: 200, Header: http.Header{"X-Github-Request-Id": []string{"bounded-call"}}, Body: body}, nil
	})}}
	ctx := mergelifecycle.WithProviderHTTPCallHandoffV1(context.Background(), budget, handoff)
	meter, err := provider.startMeter(ctx)
	if err != nil {
		t.Fatal(err)
	}
	path := repoPath(fixture.repository) + "/git/commits/" + fixture.recipe.ExpectedResultSHA().String()
	if _, err := provider.readJSONOnce(ctx, meter, mergelifecycle.ProviderCallPreSubmitV1, http.MethodGet, path, nil); err != nil {
		t.Fatal(err)
	}
	accounting := provider.finishMeter(meter)
	if accounting.HTTPCalls != 1 || accounting.ActiveNanos != int64(14*time.Millisecond) || accounting.InvocationNanos < accounting.ActiveNanos {
		t.Fatalf("full body/decompression/close accounting = %+v", accounting)
	}
	want := []string{"reserve:pre-submit", "complete:pre-submit"}
	if got := handoff.snapshot(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("reserve/network/account sequence = %v", got)
	}
}

func TestTask3ClosureM02RepeatedHeaders(t *testing.T) {
	limits := githublifecycle.DefaultLimits()
	provider := &Provider{limits: limits, now: time.Now}
	budget := providerTestBudget()
	for name, header := range map[string]http.Header{
		"repeated Link":             {"X-Github-Request-Id": {"request-link"}, "Link": {"<https://api.github.com/x?page=2>; rel=\"next\"", "<https://api.github.com/x?page=3>; rel=\"last\""}},
		"repeated Content-Encoding": {"X-Github-Request-Id": {"request-encoding"}, "Content-Encoding": {"identity", "gzip"}},
		"composed Content-Encoding": {"X-Github-Request-Id": {"request-composed"}, "Content-Encoding": {"gzip, identity"}},
	} {
		t.Run(name, func(t *testing.T) {
			body := &closeTrackingBody{Reader: strings.NewReader(`{}`)}
			meter := &callMeter{budget: budget, started: time.Now()}
			if _, err := provider.consumeResponse(&http.Response{StatusCode: 200, Header: header, Body: body}, meter); err == nil || !body.wasClosed() {
				t.Fatalf("repeated singleton header was accepted or body leaked: err=%v closed=%v", err, body.wasClosed())
			}
		})
	}
}

func TestTask3ClosureM03AppInstallationIdentity(t *testing.T) {
	if auth, err := NewAppInstallationAuthenticator("token", "I_installation", 42); err == nil || auth != nil {
		t.Fatal("app-installation credential was admitted without independent remote identity")
	}
	limits := githublifecycle.DefaultLimits()
	forged := &Authenticator{token: []byte("token"), kind: githublifecycle.ActingKindAppInstallation, subject: "I_installation", installationID: 42}
	var network atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		network.Add(1)
		return githubResponse(200, "forbidden-app", []byte(`{}`)), nil
	})}
	if provider, err := newProvider(forged, limits, client, func(*submissionTracker) *http.Client { return client }); err == nil || provider != nil || network.Load() != 0 {
		t.Fatal("forged app-installation authenticator reached provider admission or network")
	}
}

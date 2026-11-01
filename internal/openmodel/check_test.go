package openmodel

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "gsk_test_key_value_never_shown_4Hq9"

func key() Key { return Key{value: testKey, sum: "digest"} }

func TestTheModelsCheckSendsTheKeyAndReadsTheAnswer(t *testing.T) {
	var auth atomic.Value
	status, body, retry := http.StatusOK, `{"data":[{"id":"openai/gpt-oss-120b"}]}`, ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Method + " " + r.URL.Path + " " + r.Header.Get("Authorization"))
		if retry != "" {
			w.Header().Set("Retry-After", retry)
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	check := CheckModels(context.Background(), nil, server.URL+"/openai/v1", key(), "openai/gpt-oss-120b")
	if check.Status != CheckOK || check.HTTPStatus != 200 || auth.Load() != "GET /openai/v1/models Bearer "+testKey {
		t.Fatalf("check = %+v, request %v", check, auth.Load())
	}
	if got := CheckModels(context.Background(), nil, server.URL, key(), "zai-org/GLM-5.3"); got.Status != CheckModelMissing {
		t.Fatalf("a model the provider does not list = %+v", got)
	}
	body = `[{"id":"zai-org/GLM-5.3"}]`
	if got := CheckModels(context.Background(), nil, server.URL, key(), "zai-org/GLM-5.3"); got.Status != CheckOK {
		t.Fatalf("Together's bare list = %+v", got)
	}
	for _, refused := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		status = refused
		if got := CheckModels(context.Background(), nil, server.URL, key(), "x"); got.Status != CheckKeyRefused || got.HTTPStatus != refused {
			t.Fatalf("HTTP %d = %+v", refused, got)
		}
	}
	status, retry = http.StatusTooManyRequests, "12"
	if got := CheckModels(context.Background(), nil, server.URL, key(), "x"); got.Status != CheckUnavailable || got.RetryAfter != 12*time.Second {
		t.Fatalf("a 429 = %+v", got)
	}
	status, retry, body = http.StatusOK, "", "not json"
	if got := CheckModels(context.Background(), nil, server.URL, key(), "x"); got.Status != CheckUnavailable {
		t.Fatalf("an odd answer = %+v", got)
	}
	server.Close()
	if got := CheckModels(context.Background(), nil, server.URL, key(), "x"); got.Status != CheckUnavailable || got.HTTPStatus != 0 {
		t.Fatalf("no answer = %+v", got)
	}
}

func TestTheModelsCheckNeverFollowsARedirectWithTheKey(t *testing.T) {
	var reached atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer elsewhere.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/models", http.StatusFound)
	}))
	defer server.Close()
	if got := CheckModels(context.Background(), nil, server.URL, key(), "x"); got.Status != CheckUnavailable || got.HTTPStatus != http.StatusFound {
		t.Fatalf("a redirect = %+v", got)
	}
	if reached.Load() {
		t.Fatal("the key followed a redirect to another address")
	}
}

// clock is a fake clock whose sleeps advance it.
type clock struct {
	now    time.Time
	sleeps []time.Duration
}

func (c *clock) Now() time.Time { return c.now }
func (c *clock) Sleep(_ context.Context, d time.Duration) error {
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	return nil
}

func TestThePreflightHonoursRetryAfterWithExponentialBackoff(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 2:
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusTooManyRequests)
		case 3:
			w.WriteHeader(http.StatusBadGateway)
		default:
			fmt.Fprint(w, `{"data":[{"id":"m"}]}`)
		}
	}))
	defer server.Close()
	fake := &clock{now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	check := Preflight(context.Background(), nil, server.URL, key(), "m", fake.now.Add(10*time.Minute), Backoff{Initial: 2 * time.Second, Max: time.Minute}, fake.Now, fake.Sleep)
	if check.Status != CheckOK || check.Attempts != 4 {
		t.Fatalf("preflight = %+v", check)
	}
	// 2s, then the provider's 30s over the backoff's 4s, then 8s.
	if fmt.Sprint(fake.sleeps) != "[2s 30s 8s]" {
		t.Fatalf("waits = %v", fake.sleeps)
	}
}

func TestThePreflightStopsAtTheDeadlineAndAtARefusal(t *testing.T) {
	var calls atomic.Int32
	status := http.StatusServiceUnavailable
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(status)
	}))
	defer server.Close()
	fake := &clock{now: time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)}
	check := Preflight(context.Background(), nil, server.URL, key(), "m", fake.now.Add(29*time.Second), Backoff{Initial: 2 * time.Second, Max: 8 * time.Second}, fake.Now, fake.Sleep)
	// Waits of 2, 4, 8 and 8 seconds fit in 29; the next 8 would not.
	if check.Status != CheckUnavailable || check.Attempts != 5 || check.HTTPStatus != 503 || fmt.Sprint(fake.sleeps) != "[2s 4s 8s 8s]" {
		t.Fatalf("preflight = %+v after %v", check, fake.sleeps)
	}
	status, fake.sleeps = http.StatusUnauthorized, nil
	if check := Preflight(context.Background(), nil, server.URL, key(), "m", fake.now.Add(time.Hour), DefaultBackoff, fake.Now, fake.Sleep); check.Status != CheckKeyRefused || len(fake.sleeps) != 0 {
		t.Fatalf("a refused key is retried: %+v, %v", check, fake.sleeps)
	}
}

func TestTheEnginesCheckIsCachedPerProviderModelAndKey(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"data":[{"id":"openai/gpt-oss-120b"}]}`)
	}))
	defer server.Close()
	provider := groq()
	provider.ID, provider.BaseURL = "cache-test", server.URL
	model := provider.Models[0]
	for range 3 {
		if check := CachedCheck(context.Background(), nil, provider, model, key()); check.Status != CheckOK || check.CheckedAt == "" {
			t.Fatalf("check = %+v", check)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("the provider was asked %d times", calls.Load())
	}
	CachedCheck(context.Background(), nil, provider, model, Key{value: testKey, sum: "another"})
	if calls.Load() != 2 {
		t.Fatal("a changed key reused the old answer")
	}
}

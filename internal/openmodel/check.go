package openmodel

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Check statuses: what a provider's /models said about the key and the model.
const (
	CheckOK           = "ok"
	CheckKeyRefused   = "key_refused"
	CheckModelMissing = "model_missing"
	CheckUnavailable  = "unavailable"
)

// MaxModelsBody bounds a /models answer (Together's lists every model it
// serves).
const MaxModelsBody = 16 << 20

// MaxRetryAfter bounds how long one Retry-After may hold a build back.
const MaxRetryAfter = 5 * time.Minute

// Check is one answer of a provider's /models. It names no key and no model
// other than the one asked about.
type Check struct {
	Status     string        `json:"status"`
	HTTPStatus int           `json:"http_status,omitempty"`
	Attempts   int           `json:"attempts,omitempty"`
	CheckedAt  string        `json:"checked_at,omitempty"`
	RetryAfter time.Duration `json:"-"`
}

// NewClient answers the HTTP client a check uses: a bounded time, and no
// redirect, so the key is never sent to another address.
func NewClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// CheckModels asks GET <base>/models, with the key as a bearer token, whether
// the provider answers, takes the key, and lists the model's id. It costs no
// tokens. A 401 or 403 is a refused key; 429, 5xx and network trouble are
// "unavailable", with the provider's Retry-After when it gives one.
func CheckModels(ctx context.Context, client *http.Client, baseURL string, key Key, modelID string) Check {
	if client == nil {
		client = NewClient(10 * time.Second)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return Check{Status: CheckUnavailable}
	}
	request.Header.Set("Authorization", "Bearer "+key.value)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "abcp-open-model-check/1")
	response, err := client.Do(request)
	if err != nil {
		return Check{Status: CheckUnavailable}
	}
	defer response.Body.Close()
	check := Check{HTTPStatus: response.StatusCode, RetryAfter: retryAfter(response.Header.Get("Retry-After"), time.Now())}
	switch {
	case response.StatusCode == http.StatusOK:
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		check.Status = CheckKeyRefused
		return check
	default:
		check.Status = CheckUnavailable
		return check
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxModelsBody+1))
	if err != nil || len(body) > MaxModelsBody {
		check.Status = CheckUnavailable
		return check
	}
	ids, err := ModelIDs(body)
	if err != nil {
		check.Status = CheckUnavailable
		return check
	}
	check.Status = CheckModelMissing
	for _, id := range ids {
		if id == modelID {
			check.Status = CheckOK
		}
	}
	return check
}

// ModelIDs reads the ids of a /models answer: OpenAI's {"data": [{"id"}]}
// (Groq) or a bare array of models (Together).
func ModelIDs(body []byte) ([]string, error) {
	type entry struct {
		ID string `json:"id"`
	}
	var list []entry
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, err
		}
	} else {
		var wrapped struct {
			Data []entry `json:"data"`
		}
		if err := json.Unmarshal(body, &wrapped); err != nil {
			return nil, err
		}
		if wrapped.Data == nil {
			return nil, errors.New("models answer has no data")
		}
		list = wrapped.Data
	}
	ids := make([]string, 0, len(list))
	for _, model := range list {
		if model.ID != "" {
			ids = append(ids, model.ID)
		}
	}
	return ids, nil
}

// retryAfter reads a Retry-After header: seconds, or an HTTP date.
func retryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		return min(time.Duration(seconds*float64(time.Second)), MaxRetryAfter)
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return min(at.Sub(now), MaxRetryAfter)
	}
	return 0
}

// Backoff is how a build's preflight waits between tries.
type Backoff struct {
	Initial, Max time.Duration
}

// DefaultBackoff starts at 2 seconds and doubles up to a minute.
var DefaultBackoff = Backoff{Initial: 2 * time.Second, Max: time.Minute}

// Preflight asks the provider until it says the key and the model answer, or
// refuses the key or the model, or the deadline would pass before the next
// try. Between tries it waits the provider's Retry-After when it is longer
// than the backoff, which doubles from Initial up to Max.
func Preflight(ctx context.Context, client *http.Client, baseURL string, key Key, modelID string, deadline time.Time, backoff Backoff, now func() time.Time, sleep func(context.Context, time.Duration) error) Check {
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = sleepContext
	}
	if backoff.Initial <= 0 {
		backoff = DefaultBackoff
	}
	if backoff.Max < backoff.Initial {
		backoff.Max = backoff.Initial
	}
	wait := backoff.Initial
	for attempt := 1; ; attempt++ {
		check := CheckModels(ctx, client, baseURL, key, modelID)
		check.Attempts = attempt
		check.CheckedAt = now().UTC().Format(time.RFC3339)
		if check.Status != CheckUnavailable {
			return check
		}
		delay := wait
		if check.RetryAfter > delay {
			delay = check.RetryAfter
		}
		if ctx.Err() != nil || now().Add(delay).After(deadline) {
			return check
		}
		if err := sleep(ctx, delay); err != nil {
			return check
		}
		wait = min(wait*2, backoff.Max)
	}
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}

// checkCache keeps the engines answer's checks for CheckTTL, so Repo C's
// reads do not reach the provider each time.
var checkCache = struct {
	sync.Mutex
	entries map[string]Check
	at      map[string]time.Time
}{entries: map[string]Check{}, at: map[string]time.Time{}}

// CheckTTL is how long an engines check is reused.
const CheckTTL = 5 * time.Minute

// CachedCheck answers one try of CheckModels, reused for CheckTTL per
// provider address, model and key.
func CachedCheck(ctx context.Context, client *http.Client, p Provider, m Model, key Key) Check {
	cacheKey := p.ID + "\x00" + p.BaseURL + "\x00" + m.ID + "\x00" + key.SHA256()
	checkCache.Lock()
	if at, ok := checkCache.at[cacheKey]; ok && time.Since(at) < CheckTTL {
		check := checkCache.entries[cacheKey]
		checkCache.Unlock()
		return check
	}
	checkCache.Unlock()
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	check := CheckModels(checkCtx, client, p.BaseURL, key, m.ID)
	check.Attempts = 1
	check.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	checkCache.Lock()
	checkCache.entries[cacheKey], checkCache.at[cacheKey] = check, time.Now()
	checkCache.Unlock()
	return check
}

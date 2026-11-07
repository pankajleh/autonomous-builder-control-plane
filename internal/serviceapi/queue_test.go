package serviceapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

type testAdmissionQueue map[string]QueuedRunV1

func (q testAdmissionQueue) QueuedRun(run string) (QueuedRunV1, bool) {
	queued, ok := q[run]
	return queued, ok
}

// R7 (docs/plans/build-recovery.md §6.3): a run admitted and queued but not registered answers 404 run_queued on every route
// that checks the run's registration (requireRegisteredRun; /failure here) (retryable, with queued_since and the cause in details); once its wait ended, 404 admission_expired; any
// other unknown run 404 not_found as before.
func TestAQueuedRunsRoutesAnswerRunQueuedThenAdmissionExpired(t *testing.T) {
	s, _, catalog := forgetServer(t, PrincipalService)
	s.reserved.Failures = &testRunFailures{}
	queue := testAdmissionQueue{"run-1": {QueuedSince: "2026-11-07T18:00:00Z", Cause: "workflow_database_unavailable"}}
	s.reserved.AdmissionQueue = queue
	catalog.readErr = os.ErrNotExist
	get := func(path string) (int, ErrorV1) {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, authenticatedRequest(http.MethodGet, path, nil))
		var body ErrorResponseV1
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %q", path, w.Body.String())
		}
		return w.Code, body.Error
	}
	for _, path := range []string{"/v1/runs/run-1/failure", "/v1/runs/run-1/activity"} {
		status, apiError := get(path)
		if status != http.StatusNotFound || apiError.Code != "run_queued" || !apiError.Retryable ||
			apiError.Details["queued_since"] != "2026-11-07T18:00:00Z" || apiError.Details["cause"] != "workflow_database_unavailable" {
			t.Fatalf("%s = %d %+v", path, status, apiError)
		}
	}
	queue["run-1"] = QueuedRunV1{Expired: true, QueuedSince: "2026-11-07T18:00:00Z", Cause: "build_wall_unavailable"}
	if status, apiError := get("/v1/runs/run-1/failure"); status != http.StatusNotFound || apiError.Code != "admission_expired" || !apiError.Retryable ||
		apiError.Details["cause"] != "build_wall_unavailable" {
		t.Fatalf("expired = %d %+v", status, apiError)
	}
	if status, apiError := get("/v1/runs/run-2/failure"); status != http.StatusNotFound || apiError.Code != "not_found" {
		t.Fatalf("an unknown run = %d %+v", status, apiError)
	}
}

package serviceapi

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestSafeRequestIDKeepsTheSharedFormatAndMakesReqIDs(t *testing.T) {
	for _, keep := range []string{"req-7f3a91c2", "req-handoff-check1", "123e4567-e89b-12d3-a456-426614174000", "a:b.c_d-e", "-leading", strings.Repeat("x", 100)} {
		if got := safeRequestID(keep); got != keep {
			t.Fatalf("well-formed ID %q replaced by %q", keep, got)
		}
	}
	made := regexp.MustCompile(`^req-[0-9a-f]{32}$`)
	for _, replace := range []string{"", "short", "/private/path", "bad id<>", "line\nbreak", strings.Repeat("x", 101)} {
		if got := safeRequestID(replace); !made.MatchString(got) {
			t.Fatalf("malformed ID %q kept as %q", replace, got)
		}
	}
}

func TestRequestLogWritesOneSafeLine(t *testing.T) {
	var log bytes.Buffer
	request := httptest.NewRequest(http.MethodGet, "/v1/runs/run-1%0Aabcp%20forged?cursor=secret-cursor", nil)
	record := &requestRecord{ResponseWriter: httptest.NewRecorder()}
	record.WriteHeader(http.StatusNotFound)
	logRequest(&log, time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC), "req-abc123", request, record, 12*time.Millisecond)
	line := log.String()
	if strings.Count(line, "\n") != 1 || strings.Contains(line, "secret-cursor") {
		t.Fatalf("unsafe log line %q", line)
	}
	want := "abcp request at=2026-10-04T08:00:00.000Z id=req-abc123 method=GET path=/v1/runs/run-1%0Aabcp%20forged status=404 ms=12\n"
	if line != want {
		t.Fatalf("log line\n got %q\nwant %q", line, want)
	}
	log.Reset()
	created := &requestRecord{ResponseWriter: httptest.NewRecorder()}
	noteRun(created, "run-42")
	logRequest(&log, time.Now(), "req-abc123", httptest.NewRequest(http.MethodPost, "/v1/development-runs", nil), created, 0)
	if !strings.HasSuffix(log.String(), " status=200 ms=0 run=run-42\n") {
		t.Fatalf("admission line does not name its run: %q", log.String())
	}
	logRequest(nil, time.Now(), "req-abc123", request, record, 0) // no writer: nothing to do, no panic
}

func TestRequestRecordKeepsStreamsFlushing(t *testing.T) {
	inner := httptest.NewRecorder()
	var writer http.ResponseWriter = &requestRecord{ResponseWriter: inner}
	if _, ok := writer.(http.Flusher); !ok {
		t.Fatal("record hides http.Flusher")
	}
	if err := http.NewResponseController(writer).Flush(); err != nil {
		t.Fatalf("response controller cannot flush through the record: %v", err)
	}
	if !inner.Flushed {
		t.Fatal("flush did not reach the underlying writer")
	}
}

func TestServerLogsEveryRequestWithItsID(t *testing.T) {
	server := newTestServer(t, &testCatalog{}, time.Now().UTC())
	var log bytes.Buffer
	server.reserved.RequestLog = &log
	for _, presented := range []string{"req-1e-check", "bad id"} {
		request := httptest.NewRequest(http.MethodGet, "/v1/capabilities", nil)
		request.Header.Set("Authorization", "Bearer valid")
		request.Header.Set("X-Request-ID", presented)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		id := response.Header().Get("X-Request-ID")
		if presented == "req-1e-check" && id != presented {
			t.Fatalf("presented ID not kept: %q", id)
		}
		if !strings.Contains(log.String(), "id="+id+" method=GET path=/v1/capabilities status=200 ") {
			t.Fatalf("no log line for %q: %q", id, log.String())
		}
	}
	if strings.Count(log.String(), "\n") != 2 {
		t.Fatalf("want one line per request: %q", log.String())
	}
}

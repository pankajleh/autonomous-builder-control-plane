package serviceapi

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"
)

// requestIDPattern is the request ID shared across Autobuild's services (Repo C design note REQUEST_IDS.md): 6 to 100
// letters, digits, '-', '_', ':' or '.'. Product API IDs look like req-<32 hex>; callers may also send a UUID.
var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_:.-]{6,100}$`)

// safeRequestID keeps a well-formed presented ID and otherwise makes req-<32 hex>.
func safeRequestID(presented string) string {
	if requestIDPattern.MatchString(presented) {
		return presented
	}
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return "req-" + hex.EncodeToString(value[:])
	}
	return "request-id-unavailable"
}

// requestRecord remembers what one request's log line needs: the status written and, for an admission, the run it
// created. It passes Flush and Unwrap through so activity streams keep flushing.
type requestRecord struct {
	http.ResponseWriter
	status int
	run    string
}

func (r *requestRecord) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *requestRecord) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(data)
}

func (r *requestRecord) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *requestRecord) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// noteRun names the run an admission created on that request's log line.
func noteRun(writer http.ResponseWriter, runID string) {
	if record, ok := writer.(*requestRecord); ok {
		record.run = runID
	}
}

var requestLogMu sync.Mutex

// logRequest writes one line per request: no query, no headers, no body, and the path escaped, so nothing a caller
// sends can add a line or reveal a cursor or a token.
func logRequest(log io.Writer, at time.Time, requestID string, request *http.Request, record *requestRecord, elapsed time.Duration) {
	if log == nil {
		return
	}
	path := ""
	if request.URL != nil {
		path = request.URL.EscapedPath()
	}
	if len(path) > 200 {
		path = path[:200] + "..."
	}
	status := record.status
	if status == 0 {
		status = http.StatusOK
	}
	line := fmt.Sprintf("abcp request at=%s id=%s method=%s path=%s status=%d ms=%d",
		at.UTC().Format("2006-01-02T15:04:05.000Z"), requestID, request.Method, path, status, elapsed.Milliseconds())
	if record.run != "" {
		line += " run=" + record.run
	}
	requestLogMu.Lock()
	defer requestLogMu.Unlock()
	_, _ = io.WriteString(log, line+"\n")
}

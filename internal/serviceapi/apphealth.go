package serviceapi

import (
	"net/http"
	"strconv"
)

// AppHealthEventV1 is one change in how an app ABCP runs is answering (Repo C design note
// BF07_WHEN_A_RUNNING_APP_GOES_WRONG §4.5, 7e): "down" once it has been unwell for ten minutes, since when; "up" once it
// answers again. Kind is "hosted" (a share link, by its hosting key) or "site" (an app on a server, by target and site).
type AppHealthEventV1 struct {
	Seq        uint64 `json:"seq"`
	At         string `json:"at"`
	Kind       string `json:"kind"`
	HostingKey string `json:"hosting_key,omitempty"`
	Target     string `json:"target,omitempty"`
	Site       string `json:"site,omitempty"`
	State      string `json:"state"`
	Since      string `json:"since"`
}

// AppHealthEventsV1 is a page of events after a cursor, oldest first. LastSeq is the newest event there is; Truncated
// says events after the cursor were already dropped (at most 500 are kept).
type AppHealthEventsV1 struct {
	SchemaVersion string             `json:"schema_version"`
	Events        []AppHealthEventV1 `json:"events"`
	LastSeq       uint64             `json:"last_seq"`
	Truncated     bool               `json:"truncated"`
}

const AppHealthEventsSchemaV1 = "AppHealthEventsV1"

// AppHealthEventLimit bounds one page.
const AppHealthEventLimit = 100

// AppHealthFeed answers the events after a cursor.
type AppHealthFeed interface {
	Events(after uint64, limit int) AppHealthEventsV1
}

// GET /v1/app-health/events?after=<seq>&limit=<n>, for the principal that controls hosting (Repo C's).
func (s *Server) appHealthEvents(w http.ResponseWriter, r *http.Request, principal Principal, id string) {
	if r.Method != http.MethodGet || !requestBodyEmpty(r) {
		s.writeError(w, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid app health request", RequestID: id})
		return
	}
	if !s.mayControlHosting(principal) {
		s.writeDependencyError(w, id, ErrAuthorityDenied)
		return
	}
	if s.reserved.AppHealth == nil {
		s.writeDependencyError(w, id, ErrUnsupportedCapability)
		return
	}
	query := r.URL.Query()
	after, limit := uint64(0), AppHealthEventLimit
	for name, values := range query {
		bad := len(values) != 1
		switch name {
		case "after":
			n, err := strconv.ParseUint(values[0], 10, 64)
			bad, after = bad || err != nil, n
		case "limit":
			n, err := strconv.Atoi(values[0])
			bad, limit = bad || err != nil || n < 1 || n > AppHealthEventLimit, n
		default:
			bad = true
		}
		if bad {
			s.writeError(w, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid app health request", RequestID: id})
			return
		}
	}
	v := s.reserved.AppHealth.Events(after, limit)
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, v)
}

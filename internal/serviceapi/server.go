package serviceapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
)

const (
	MaxInFlightRequests     = 64
	MaxActivityStreams      = 16
	MaxRequestBodyBytes     = 1 << 20
	DefaultRunsPageSize     = 50
	MaxRunsPageSize         = 200
	DefaultEventsPageSize   = 100
	MaxEventsPageSize       = 500
	DefaultTimelinePageSize = 100
	MaxTimelinePageSize     = 500
	DefaultEvidencePageSize = 50
	MaxEvidencePageSize     = 200
	MaxEvidenceDownloadSize = 16 << 20
	maxEvidenceBuffers      = 4
	ReadHeaderTimeout       = 5 * time.Second
	ReadTimeout             = 10 * time.Second
	WriteTimeout            = 30 * time.Second
	IdleTimeout             = 60 * time.Second
)

var (
	ErrActivityUnavailable             = errors.New("activity extension unavailable")
	ErrUnsupportedCapability           = errors.New("unsupported capability")
	ErrDependencyNotFound              = errors.New("dependency resource not found")
	ErrAuthoritativeReadBusy           = errors.New("authoritative read busy")
	ErrProjectionLineageChanged        = errors.New("projection lineage changed")
	ErrProjectionIntegrity             = errors.New("projection integrity failure")
	ErrEvidenceIntegrityChanged        = errors.New("evidence integrity changed")
	ErrStaleExpectedState              = errors.New("stale expected state")
	ErrStaleExpectedRevision           = errors.New("stale expected revision")
	ErrRequestIDConflict               = errors.New("request id conflict")
	ErrActionJournalExhausted          = errors.New("action journal exhausted")
	ErrActionStatusNotFound            = errors.New("action status not found")
	ErrActionTargetNotActive           = errors.New("action target not active")
	ErrActionStateAdvanced             = errors.New("action state advanced")
	ErrDecisionRequestInvalid          = errors.New("decision request invalid")
	ErrDecisionAlreadyResolved         = errors.New("decision already resolved")
	ErrDecisionAlreadyRecorded         = errors.New("decision already recorded")
	ErrReconciliationRequired          = errors.New("reconciliation required")
	ErrUnknownAdmissionProfile         = errors.New("unknown admission profile")
	ErrRepositoryBaseMismatch          = errors.New("repository base mismatch")
	ErrAdmissionUnavailable            = errors.New("run admission unavailable")
	ErrUnsafeAdmissionMaterialization  = errors.New("unsafe admission materialization")
	ErrInternalDurableSubstrateFailure = errors.New("internal durable substrate failure")
	ErrInternalDurableSubstrate        = ErrInternalDurableSubstrateFailure
	errRequestBodyTooLarge             = errors.New("request body exceeds limit")
)

// CatalogReader is the complete durable dependency of Track-A HTTP routes.
// In particular, it has no ledger-snapshot method.
type CatalogReader interface {
	ListRuns(after string, limit int) ([]runtimecatalog.RunRegistrationV1, bool, error)
	ReadRun(runID string) (runtimecatalog.RunRegistrationV1, error)
}

// Later-track dependency seams are intentionally frozen without providing a
// Track-A fallback implementation that could fabricate success.
type RunProjectionReader interface {
	ReadRunProjection(context.Context, string) (json.RawMessage, error)
}

type PageRequestV1 struct {
	PageSize int
	Cursor   string
}

type EventProjectionReader interface {
	ReadEventProjection(context.Context, string, PageRequestV1) (json.RawMessage, error)
}

type TimelineReader interface {
	ReadTimeline(context.Context, string, PageRequestV1) (json.RawMessage, error)
}

type EvidenceReader interface {
	ListEvidence(context.Context, string, PageRequestV1) (json.RawMessage, error)
	ReadEvidence(context.Context, string, string) ([]byte, error)
}

type ActionKind string

const (
	ActionCancel   ActionKind = "cancel"
	ActionDecision ActionKind = "decision"
)

type ActionController interface {
	AdmitAction(context.Context, Principal, string, ActionKind, CommandEnvelopeV1) (ActionStatusV1, error)
	ReadAction(context.Context, Principal, string, string) (ActionStatusV1, error)
}

type RunAdmissionController interface {
	AdmitRun(context.Context, Principal, RunAdmissionRequestV1) (RunAdmissionResponseV1, error)
}

type DevelopmentRunAdmissionController interface {
	AdmitDevelopmentRun(context.Context, Principal, DevelopmentRunAdmissionRequestV1) (RunAdmissionResponseV1, error)
}

// Activity is an additive dependency; provider implementation types never
// enter the existing run/event DTOs or authority controller interfaces.
type ActivityFrame struct {
	Ordinal uint64
	Data    json.RawMessage
}
type ActivitySubscription interface {
	Next(context.Context) (ActivityFrame, error)
	Close() error
}
type ActivityReader interface {
	ReadActivity(context.Context, string, PageRequestV1) (json.RawMessage, error)
	OpenActivityStream(context.Context, string, string) (ActivitySubscription, error)
}

type ServerConfig struct {
	Activity                ActivityReader
	Authenticator           Authenticator
	Authority               *AuthorityMatcher
	Catalog                 CatalogReader
	CursorSigner            *CursorSigner
	Clock                   func() time.Time
	RunProjections          RunProjectionReader
	Events                  EventProjectionReader
	Timeline                TimelineReader
	Evidence                EvidenceReader
	Actions                 ActionController
	RunAdmission            RunAdmissionController
	DevelopmentRunAdmission DevelopmentRunAdmissionController
}

type Server struct {
	activityStreams chan struct{}
	authenticator   Authenticator
	authority       *AuthorityMatcher
	catalog         CatalogReader
	cursors         *CursorSigner
	now             func() time.Time
	inFlight        chan struct{}
	evidenceBuffers chan struct{}
	reserved        ServerConfig
}

func NewServer(config ServerConfig) (*Server, error) {
	if config.Authenticator == nil {
		return nil, errors.New("authenticator is required")
	}
	if !config.Authority.valid() {
		return nil, errors.New("authority matcher is required")
	}
	if config.Catalog == nil {
		return nil, errors.New("runtime catalog is required")
	}
	if !config.CursorSigner.valid() {
		return nil, errors.New("cursor signer is required")
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	return &Server{
		activityStreams: make(chan struct{}, MaxActivityStreams),
		authenticator:   config.Authenticator, authority: config.Authority,
		catalog: config.Catalog, cursors: config.CursorSigner, now: config.Clock,
		inFlight:        make(chan struct{}, MaxInFlightRequests),
		evidenceBuffers: make(chan struct{}, maxEvidenceBuffers),
		reserved:        config,
	}, nil
}

func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }

// HTTPServer returns the concrete server with the exact frozen timeout set.
func (s *Server) HTTPServer(address string) (*http.Server, error) {
	if err := ValidateLoopbackAddress(address); err != nil {
		return nil, err
	}
	return &http.Server{
		Addr: address, Handler: s.Handler(), ReadHeaderTimeout: ReadHeaderTimeout,
		ReadTimeout: ReadTimeout, WriteTimeout: WriteTimeout, IdleTimeout: IdleTimeout,
		MaxHeaderBytes: 64 << 10,
	}, nil
}

func ValidateLoopbackAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" || port == "" {
		return errors.New("listen address must be an explicit loopback IP and port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("listen address is not loopback")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return errors.New("listen port is invalid")
	}
	return nil
}

// ListenAndServe binds only after validating both the requested and actual
// listener addresses. Context cancellation performs a bounded graceful stop.
func (s *Server) ListenAndServe(ctx context.Context, address string) error {
	if ctx == nil {
		return errors.New("server context is required")
	}
	httpServer, err := s.HTTPServer(address)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	if tcp, ok := listener.Addr().(*net.TCPAddr); !ok || tcp.IP == nil || !tcp.IP.IsLoopback() {
		listener.Close()
		return errors.New("listener is not loopback")
	}
	done := make(chan error, 1)
	go func() { done <- httpServer.Serve(listener) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shutdownErr := httpServer.Shutdown(shutdownContext)
		serveErr := <-done
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(shutdownErr, serveErr)
	}
}

type CapabilitiesV1 struct {
	RunAdmission     bool `json:"run_admission"`
	Retry            bool `json:"retry"`
	Resume           bool `json:"resume"`
	Recovery         bool `json:"recovery"`
	Cancel           bool `json:"cancel"`
	Decision         bool `json:"decision"`
	EvidenceDownload bool `json:"evidence_download"`
}

type RunSummaryV1 struct {
	RunID                        string `json:"run_id"`
	RepositoryIdentityDigest     string `json:"repository_identity_digest"`
	InitialRegistrationTimestamp string `json:"initial_registration_timestamp"`
	DetailURL                    string `json:"detail_url"`
}

type RunsPageV1 struct {
	Runs       []RunSummaryV1 `json:"runs"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

type ErrorV1 struct {
	Code                   string         `json:"code"`
	Message                string         `json:"message"`
	RequestID              string         `json:"request_id"`
	Retryable              bool           `json:"retryable"`
	ReconciliationRequired bool           `json:"reconciliation_required"`
	Details                map[string]any `json:"details,omitempty"`
}

type ErrorResponseV1 struct {
	Error ErrorV1 `json:"error"`
}

func (s *Server) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	requestID := safeRequestID(request.Header.Get("X-Request-ID"))
	writer.Header().Set("X-Request-ID", requestID)
	// Bound every stream request before authentication, body reads, or catalog
	// access. Its dedicated slot covers both preprocessing and stream lifetime.
	streamRoute := request.URL != nil && request.URL.RawPath == "" && strings.HasPrefix(request.URL.Path, "/v1/runs/") && strings.HasSuffix(request.URL.Path, "/activity/stream")
	if streamRoute {
		select {
		case s.activityStreams <- struct{}{}:
			defer func() { <-s.activityStreams }()
		default:
			// Skip net/http's implicit body drain on this unadmitted request.
			writer.Header().Set("Connection", "close")
			writer.Header().Set("Retry-After", "1")
			s.writeError(writer, http.StatusServiceUnavailable, ErrorV1{Code: "activity_busy", Message: "activity stream capacity is busy", RequestID: requestID, Retryable: true})
			return
		}
	} else {
		select {
		case s.inFlight <- struct{}{}:
			defer func() { <-s.inFlight }()
		default:
			s.writeError(writer, http.StatusServiceUnavailable, ErrorV1{Code: "service_busy", Message: "service request capacity is busy", RequestID: requestID, Retryable: true})
			return
		}
	}
	if request.URL == nil || (request.URL.Path != "/v1" && !strings.HasPrefix(request.URL.Path, "/v1/")) {
		s.writeError(writer, http.StatusNotFound, ErrorV1{Code: "not_found", Message: "resource not found", RequestID: requestID})
		return
	}
	principal, err := s.authenticator.Authenticate(request)
	if err != nil || validatePrincipal(principal) != nil {
		writer.Header().Set("WWW-Authenticate", "Bearer")
		s.writeError(writer, http.StatusUnauthorized, ErrorV1{Code: "unauthenticated", Message: "authentication required", RequestID: requestID})
		return
	}
	if request.ContentLength > MaxRequestBodyBytes {
		s.writeError(writer, http.StatusRequestEntityTooLarge, ErrorV1{Code: "invalid_request", Message: "request body exceeds limit", RequestID: requestID})
		return
	}
	if request.Body == nil {
		request.Body = http.NoBody
	}
	request.Body = http.MaxBytesReader(writer, request.Body, MaxRequestBodyBytes)
	switch request.URL.Path {
	case "/v1/extensions/pdlc-experience":
		s.experienceCapabilities(writer, request, requestID)
	case "/v1/capabilities":
		s.capabilities(writer, request, requestID)
	case "/v1/runs":
		s.runs(writer, request, principal, requestID)
	case "/v1/development-runs":
		s.developmentRunAdmission(writer, request, principal, requestID)
	default:
		s.runRoute(writer, request, principal, requestID)
	}
}

func (s *Server) capabilities(writer http.ResponseWriter, request *http.Request, requestID string) {
	if request.Method != http.MethodGet || !requestBodyEmpty(request) || request.URL.RawQuery != "" {
		s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid capabilities request", RequestID: requestID})
		return
	}
	s.writeJSON(writer, http.StatusOK, CapabilitiesV1{
		RunAdmission: s.reserved.RunAdmission != nil,
		Cancel:       s.reserved.Actions != nil, Decision: s.reserved.Actions != nil,
		EvidenceDownload: s.reserved.Evidence != nil,
	})
}

func (s *Server) runRoute(writer http.ResponseWriter, request *http.Request, principal Principal, requestID string) {
	if !strings.HasPrefix(request.URL.Path, "/v1/runs/") || request.URL.RawPath != "" {
		s.writeError(writer, http.StatusNotFound, ErrorV1{Code: "not_found", Message: "resource not found", RequestID: requestID})
		return
	}
	parts := strings.Split(strings.TrimPrefix(request.URL.Path, "/v1/runs/"), "/")
	if len(parts) == 0 || runtimecatalog.ValidateIdentifier(parts[0]) != nil {
		s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid run route", RequestID: requestID})
		return
	}
	runID := parts[0]
	switch {
	case len(parts) == 2 && parts[1] == "activity":
		s.runActivity(writer, request, requestID, runID)
	case len(parts) == 3 && parts[1] == "activity" && parts[2] == "stream":
		s.runActivityStream(writer, request, requestID, runID)
	case len(parts) == 1:
		s.runDetail(writer, request, requestID, runID)
	case len(parts) == 2 && parts[1] == "events":
		s.runEvents(writer, request, requestID, runID)
	case len(parts) == 2 && parts[1] == "timeline":
		s.runTimeline(writer, request, requestID, runID)
	case len(parts) == 2 && parts[1] == "evidence":
		s.runEvidence(writer, request, requestID, runID)
	case len(parts) == 3 && parts[1] == "evidence":
		s.runEvidenceDownload(writer, request, requestID, runID, parts[2])
	case len(parts) == 3 && parts[1] == "actions" && request.Method == http.MethodPost && (parts[2] == string(ActionCancel) || parts[2] == string(ActionDecision)):
		s.runActionAdmission(writer, request, principal, requestID, runID, ActionKind(parts[2]))
	case len(parts) == 3 && parts[1] == "actions" && request.Method == http.MethodGet:
		s.runActionStatus(writer, request, principal, requestID, runID, parts[2])
	default:
		s.writeError(writer, http.StatusNotFound, ErrorV1{Code: "not_found", Message: "resource not found", RequestID: requestID})
	}
}

func (s *Server) runDetail(writer http.ResponseWriter, request *http.Request, requestID, runID string) {
	if err := validEmptyReadRequest(request); err != nil {
		s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid run-detail request", RequestID: requestID})
		return
	}
	if s.reserved.RunProjections == nil {
		s.writeDependencyError(writer, requestID, ErrUnsupportedCapability)
		return
	}
	if !s.requireRegisteredRun(writer, requestID, runID) {
		return
	}
	response, err := s.reserved.RunProjections.ReadRunProjection(request.Context(), runID)
	if err != nil {
		s.writeDependencyError(writer, requestID, err)
		return
	}
	s.writeDependencyJSON(writer, requestID, response)
}

func (s *Server) runEvents(writer http.ResponseWriter, request *http.Request, requestID, runID string) {
	if request.Method != http.MethodGet || !requestBodyEmpty(request) {
		s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid events request", RequestID: requestID})
		return
	}
	page, err := parsePageQuery(request.URL.Query(), DefaultEventsPageSize, MaxEventsPageSize)
	if err != nil {
		s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid events query", RequestID: requestID})
		return
	}
	if s.reserved.Events == nil {
		s.writeDependencyError(writer, requestID, ErrUnsupportedCapability)
		return
	}
	if !s.requireRegisteredRun(writer, requestID, runID) {
		return
	}
	response, err := s.reserved.Events.ReadEventProjection(request.Context(), runID, page)
	if err != nil {
		s.writeDependencyError(writer, requestID, err)
		return
	}
	s.writeDependencyJSON(writer, requestID, response)
}

func (s *Server) runTimeline(writer http.ResponseWriter, request *http.Request, requestID, runID string) {
	if request.Method != http.MethodGet || !requestBodyEmpty(request) {
		s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid timeline request", RequestID: requestID})
		return
	}
	page, err := parsePageQuery(request.URL.Query(), DefaultTimelinePageSize, MaxTimelinePageSize)
	if err != nil {
		s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid timeline query", RequestID: requestID})
		return
	}
	if s.reserved.Timeline == nil {
		s.writeDependencyError(writer, requestID, ErrUnsupportedCapability)
		return
	}
	if !s.requireRegisteredRun(writer, requestID, runID) {
		return
	}
	response, err := s.reserved.Timeline.ReadTimeline(request.Context(), runID, page)
	if err != nil {
		s.writeDependencyError(writer, requestID, err)
		return
	}
	s.writeDependencyJSON(writer, requestID, response)
}

func (s *Server) runEvidence(writer http.ResponseWriter, request *http.Request, requestID, runID string) {
	if request.Method != http.MethodGet || !requestBodyEmpty(request) {
		s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid evidence-list request", RequestID: requestID})
		return
	}
	page, err := parsePageQuery(request.URL.Query(), DefaultEvidencePageSize, MaxEvidencePageSize)
	if err != nil {
		s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid evidence-list query", RequestID: requestID})
		return
	}
	if s.reserved.Evidence == nil {
		s.writeDependencyError(writer, requestID, ErrUnsupportedCapability)
		return
	}
	if !s.requireRegisteredRun(writer, requestID, runID) {
		return
	}
	if err := s.acquireEvidenceBuffer(request.Context()); err != nil {
		s.writeDependencyError(writer, requestID, err)
		return
	}
	defer func() { <-s.evidenceBuffers }()
	response, err := s.reserved.Evidence.ListEvidence(request.Context(), runID, page)
	if err != nil {
		s.writeDependencyError(writer, requestID, err)
		return
	}
	s.writeDependencyJSON(writer, requestID, response)
}

func (s *Server) runEvidenceDownload(writer http.ResponseWriter, request *http.Request, requestID, runID, evidenceID string) {
	if runtimecatalog.ValidateIdentifier(evidenceID) != nil || validEmptyReadRequest(request) != nil {
		s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid evidence request", RequestID: requestID})
		return
	}
	if s.reserved.Evidence == nil {
		s.writeDependencyError(writer, requestID, ErrUnsupportedCapability)
		return
	}
	if !s.requireRegisteredRun(writer, requestID, runID) {
		return
	}
	if err := s.acquireEvidenceBuffer(request.Context()); err != nil {
		s.writeDependencyError(writer, requestID, err)
		return
	}
	defer func() { <-s.evidenceBuffers }()
	data, err := s.reserved.Evidence.ReadEvidence(request.Context(), runID, evidenceID)
	if err != nil {
		s.writeDependencyError(writer, requestID, err)
		return
	}
	if len(data) > MaxEvidenceDownloadSize {
		s.writeDependencyError(writer, requestID, ErrEvidenceIntegrityChanged)
		return
	}
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(data)
}

func (s *Server) acquireEvidenceBuffer(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.evidenceBuffers <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) runActionAdmission(writer http.ResponseWriter, request *http.Request, principal Principal, requestID, runID string, kind ActionKind) {
	if request.URL.RawQuery != "" {
		s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid action request", RequestID: requestID})
		return
	}
	if s.reserved.Actions == nil {
		s.writeDependencyError(writer, requestID, ErrUnsupportedCapability)
		return
	}
	command, err := decodeCommandRequest(request, kind == ActionDecision)
	if errors.Is(err, errRequestBodyTooLarge) {
		s.writeError(writer, http.StatusRequestEntityTooLarge, ErrorV1{Code: "invalid_request", Message: "request body exceeds limit", RequestID: requestID})
		return
	}
	if err != nil {
		s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid action command", RequestID: requestID})
		return
	}
	if !s.requireRegisteredRun(writer, requestID, runID) {
		return
	}
	status, err := s.reserved.Actions.AdmitAction(request.Context(), principal, runID, kind, command)
	if err != nil {
		s.writeDependencyError(writer, requestID, err)
		return
	}
	if validateActionStatus(status, runID) != nil {
		s.writeDependencyError(writer, requestID, ErrInternalDurableSubstrate)
		return
	}
	s.writeJSON(writer, http.StatusAccepted, status)
}

func (s *Server) runActionStatus(writer http.ResponseWriter, request *http.Request, principal Principal, requestID, runID, operationID string) {
	if runtimecatalog.ValidateIdentifier(operationID) != nil || validEmptyReadRequest(request) != nil {
		s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid action-status request", RequestID: requestID})
		return
	}
	if s.reserved.Actions == nil {
		s.writeDependencyError(writer, requestID, ErrUnsupportedCapability)
		return
	}
	if !s.requireRegisteredRun(writer, requestID, runID) {
		return
	}
	status, err := s.reserved.Actions.ReadAction(request.Context(), principal, runID, operationID)
	if err != nil {
		s.writeDependencyError(writer, requestID, err)
		return
	}
	if status.OperationID != operationID || validateActionStatus(status, runID) != nil {
		s.writeDependencyError(writer, requestID, ErrInternalDurableSubstrate)
		return
	}
	s.writeJSON(writer, http.StatusOK, status)
}

func (s *Server) runs(writer http.ResponseWriter, request *http.Request, principal Principal, requestID string) {
	if request.Method == http.MethodPost {
		s.runAdmission(writer, request, principal, requestID)
		return
	}
	if request.Method != http.MethodGet || !requestBodyEmpty(request) {
		s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid run-list request", RequestID: requestID})
		return
	}
	pageSize, cursor, err := parseRunsQuery(request.URL.Query())
	if err != nil {
		s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid run-list query", RequestID: requestID})
		return
	}
	const filters = "runs-v1:none"
	after := ""
	if cursor != "" {
		payload, err := s.cursors.VerifyCatalog(cursor, filters)
		if errors.Is(err, ErrCursorEpochChanged) {
			s.writeError(writer, http.StatusConflict, ErrorV1{Code: "cursor_epoch_changed", Message: "cursor key epoch changed", RequestID: requestID})
			return
		}
		if err != nil {
			s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_cursor", Message: "cursor is invalid", RequestID: requestID})
			return
		}
		after = payload.LastRunID
	}
	registrations, hasMore, err := s.catalog.ListRuns(after, pageSize)
	if err != nil {
		code, status, retryable := "catalog_integrity_failure", http.StatusInternalServerError, false
		if errors.Is(err, runtimecatalog.ErrBusy) {
			code, status, retryable = "authoritative_read_busy", http.StatusServiceUnavailable, true
		}
		s.writeError(writer, status, ErrorV1{Code: code, Message: "runtime catalog read failed", RequestID: requestID, Retryable: retryable})
		return
	}
	response := RunsPageV1{Runs: make([]RunSummaryV1, 0, len(registrations))}
	for _, registration := range registrations {
		response.Runs = append(response.Runs, RunSummaryV1{
			RunID: registration.RunID, RepositoryIdentityDigest: registration.RepositoryIdentityDigest,
			InitialRegistrationTimestamp: registration.InitialRegistrationTimestamp,
			DetailURL:                    "/v1/runs/" + registration.RunID,
		})
	}
	if hasMore && len(registrations) > 0 {
		response.NextCursor, err = s.cursors.SignCatalog(registrations[len(registrations)-1].RunID, filters, s.now().UTC())
		if err != nil {
			s.writeError(writer, http.StatusInternalServerError, ErrorV1{Code: "internal_durable_substrate_failure", Message: "cursor creation failed", RequestID: requestID})
			return
		}
	}
	s.writeJSON(writer, http.StatusOK, response)
}

func (s *Server) runAdmission(writer http.ResponseWriter, request *http.Request, principal Principal, requestID string) {
	if s.reserved.RunAdmission == nil {
		s.writeError(writer, http.StatusNotImplemented, ErrorV1{Code: "unsupported_capability", Message: "run admission is not available", RequestID: requestID})
		return
	}
	if principal.PrincipalType != PrincipalService || !s.authority.MayAssertDelegatedActor(principal) {
		s.writeDependencyError(writer, requestID, ErrAuthorityDenied)
		return
	}
	if request.URL.RawQuery != "" {
		s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid run admission request", RequestID: requestID})
		return
	}
	data, err := io.ReadAll(request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(writer, http.StatusRequestEntityTooLarge, ErrorV1{Code: "invalid_request", Message: "request body exceeds limit", RequestID: requestID})
			return
		}
		s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid run admission request", RequestID: requestID})
		return
	}
	var admission RunAdmissionRequestV1
	if len(data) == 0 || decodeStrictJSON(data, &admission) != nil || ValidateRunAdmissionRequestV1(admission) != nil {
		s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid run admission request", RequestID: requestID})
		return
	}
	response, err := s.reserved.RunAdmission.AdmitRun(request.Context(), principal, admission)
	if err != nil {
		s.writeDependencyError(writer, requestID, err)
		return
	}
	if runtimecatalog.ValidateIdentifier(response.RunID) != nil || response.RunURL != "/v1/runs/"+response.RunID {
		s.writeDependencyError(writer, requestID, ErrInternalDurableSubstrate)
		return
	}
	s.writeJSON(writer, http.StatusAccepted, response)
}

func (s *Server) developmentRunAdmission(writer http.ResponseWriter, request *http.Request, principal Principal, requestID string) {
	if s.reserved.DevelopmentRunAdmission == nil {
		s.writeError(writer, http.StatusNotImplemented, ErrorV1{Code: "unsupported_capability", Message: "development run admission is not available", RequestID: requestID})
		return
	}
	if principal.PrincipalType != PrincipalService || !s.authority.MayAssertDelegatedActor(principal) {
		s.writeDependencyError(writer, requestID, ErrAuthorityDenied)
		return
	}
	if request.Method != http.MethodPost || request.URL.RawQuery != "" {
		s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid development run admission request", RequestID: requestID})
		return
	}
	data, err := io.ReadAll(request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(writer, http.StatusRequestEntityTooLarge, ErrorV1{Code: "invalid_request", Message: "request body exceeds limit", RequestID: requestID})
			return
		}
		s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid development run admission request", RequestID: requestID})
		return
	}
	var admission DevelopmentRunAdmissionRequestV1
	if len(data) == 0 || decodeStrictJSON(data, &admission) != nil || ValidateDevelopmentRunAdmissionRequestV1(admission) != nil {
		s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid development run admission request", RequestID: requestID})
		return
	}
	response, err := s.reserved.DevelopmentRunAdmission.AdmitDevelopmentRun(request.Context(), principal, admission)
	if err != nil {
		s.writeDependencyError(writer, requestID, err)
		return
	}
	if runtimecatalog.ValidateIdentifier(response.RunID) != nil || response.RunURL != "/v1/runs/"+response.RunID {
		s.writeDependencyError(writer, requestID, ErrInternalDurableSubstrate)
		return
	}
	s.writeJSON(writer, http.StatusAccepted, response)
}

func validEmptyReadRequest(request *http.Request) error {
	if request.Method != http.MethodGet || request.URL.RawQuery != "" || !requestBodyEmpty(request) {
		return errors.New("read request must be an empty GET")
	}
	return nil
}

func (s *Server) requireRegisteredRun(writer http.ResponseWriter, requestID, runID string) bool {
	_, err := s.catalog.ReadRun(runID)
	if err == nil {
		return true
	}
	if errors.Is(err, os.ErrNotExist) {
		err = ErrDependencyNotFound
	}
	s.writeDependencyError(writer, requestID, err)
	return false
}

func parsePageQuery(query url.Values, defaultSize, maximumSize int) (PageRequestV1, error) {
	for key, values := range query {
		if (key != "page_size" && key != "cursor") || len(values) != 1 {
			return PageRequestV1{}, errors.New("unknown or repeated query field")
		}
	}
	pageSize := defaultSize
	if value := query.Get("page_size"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > maximumSize || strconv.Itoa(parsed) != value {
			return PageRequestV1{}, errors.New("invalid page size")
		}
		pageSize = parsed
	}
	cursor := query.Get("cursor")
	if len(cursor) > MaxCursorTokenBytes {
		return PageRequestV1{}, ErrInvalidCursor
	}
	return PageRequestV1{PageSize: pageSize, Cursor: cursor}, nil
}

func decodeCommandRequest(request *http.Request, delegatedActorRequired bool) (CommandEnvelopeV1, error) {
	data, err := io.ReadAll(request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return CommandEnvelopeV1{}, errRequestBodyTooLarge
		}
		return CommandEnvelopeV1{}, err
	}
	if len(data) == 0 || len(data) > MaxRequestBodyBytes {
		if len(data) > MaxRequestBodyBytes {
			return CommandEnvelopeV1{}, errRequestBodyTooLarge
		}
		return CommandEnvelopeV1{}, errors.New("missing action command")
	}
	var command CommandEnvelopeV1
	if err := decodeStrictJSON(data, &command); err != nil {
		return CommandEnvelopeV1{}, err
	}
	if err := ValidateCommandEnvelopeV1(command, delegatedActorRequired); err != nil {
		return CommandEnvelopeV1{}, err
	}
	return command, nil
}

func validateActionStatus(status ActionStatusV1, runID string) error {
	if runtimecatalog.ValidateIdentifier(status.OperationID) != nil || !stateNamePattern.MatchString(status.Status) {
		return errors.New("invalid action status identity")
	}
	if status.StatusURL != "/v1/runs/"+runID+"/actions/"+status.OperationID || len(status.AuthoritativeEventIDs) > 256 {
		return errors.New("invalid action status reference")
	}
	for _, eventID := range status.AuthoritativeEventIDs {
		if runtimecatalog.ValidateIdentifier(eventID) != nil {
			return errors.New("invalid authoritative event identity")
		}
	}
	return nil
}

func (s *Server) writeDependencyJSON(writer http.ResponseWriter, requestID string, response json.RawMessage) {
	if len(response) == 0 || !json.Valid(response) {
		s.writeDependencyError(writer, requestID, ErrInternalDurableSubstrate)
		return
	}
	s.writeJSON(writer, http.StatusOK, response)
}

func (s *Server) writeDependencyError(writer http.ResponseWriter, requestID string, err error) {
	apiError := ErrorV1{Code: "internal_durable_substrate_failure", Message: "service dependency failed", RequestID: requestID}
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrUnsupportedCapability):
		status, apiError.Code, apiError.Message = http.StatusNotImplemented, "unsupported_capability", "capability is not available"
	case errors.Is(err, ErrDependencyNotFound):
		status, apiError.Code, apiError.Message = http.StatusNotFound, "not_found", "resource not found"
	case errors.Is(err, ErrActionStatusNotFound):
		status, apiError.Code, apiError.Message = http.StatusNotFound, "action_status_not_found", "action status not found"
	case errors.Is(err, ErrActivityUnavailable):
		status, apiError.Code, apiError.Message, apiError.Retryable = http.StatusServiceUnavailable, "activity_unavailable", "activity extension is unavailable", true
	case errors.Is(err, ErrAuthorityDenied):
		status, apiError.Code, apiError.Message = http.StatusForbidden, "authority_denied", "authority denied"
	case errors.Is(err, ErrAuthoritativeReadBusy), errors.Is(err, runtimecatalog.ErrBusy), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		status, apiError.Code, apiError.Message, apiError.Retryable = http.StatusServiceUnavailable, "authoritative_read_busy", "authoritative read is busy", true
	case errors.Is(err, ErrProjectionLineageChanged):
		status, apiError.Code, apiError.Message = http.StatusConflict, "projection_lineage_changed", "projection lineage changed"
	case errors.Is(err, ErrCursorEpochChanged):
		status, apiError.Code, apiError.Message = http.StatusConflict, "cursor_epoch_changed", "cursor key epoch changed"
	case errors.Is(err, ErrInvalidCursor):
		status, apiError.Code, apiError.Message = http.StatusBadRequest, "invalid_cursor", "cursor is invalid"
	case errors.Is(err, ErrEvidenceIntegrityChanged):
		status, apiError.Code, apiError.Message = http.StatusConflict, "evidence_integrity_changed", "evidence integrity changed"
	case errors.Is(err, ErrStaleExpectedState):
		status, apiError.Code, apiError.Message = http.StatusConflict, "stale_expected_state", "expected state is stale"
	case errors.Is(err, ErrStaleExpectedRevision):
		status, apiError.Code, apiError.Message = http.StatusConflict, "stale_expected_revision", "expected revision is stale"
	case errors.Is(err, ErrRequestIDConflict):
		status, apiError.Code, apiError.Message = http.StatusConflict, "request_id_conflict", "request identifier conflicts with an existing operation"
	case errors.Is(err, ErrUnknownAdmissionProfile):
		status, apiError.Code, apiError.Message = http.StatusNotFound, "unknown_profile", "admission profile is not available"
	case errors.Is(err, ErrRepositoryBaseMismatch):
		status, apiError.Code, apiError.Message = http.StatusConflict, "repository_base_mismatch", "repository base does not match"
	case errors.Is(err, ErrAdmissionUnavailable):
		status, apiError.Code, apiError.Message, apiError.Retryable = http.StatusServiceUnavailable, "admission_unavailable", "run admission is temporarily unavailable", true
	case errors.Is(err, ErrUnsafeAdmissionMaterialization):
		status, apiError.Code, apiError.Message = http.StatusInternalServerError, "unsafe_admission_materialization", "run admission materialization is unsafe"
	case errors.Is(err, ErrActionJournalExhausted):
		status, apiError.Code, apiError.Message = http.StatusInsufficientStorage, "action_journal_exhausted", "action journal is exhausted"
	case errors.Is(err, ErrActionTargetNotActive):
		status, apiError.Code, apiError.Message = http.StatusConflict, "action_target_not_active", "action target is not active"
	case errors.Is(err, ErrActionStateAdvanced):
		status, apiError.Code, apiError.Message = http.StatusConflict, "action_state_advanced", "action target state advanced"
	case errors.Is(err, ErrDecisionRequestInvalid):
		status, apiError.Code, apiError.Message = http.StatusBadRequest, "decision_request_invalid", "decision request is invalid"
	case errors.Is(err, ErrDecisionAlreadyResolved):
		status, apiError.Code, apiError.Message = http.StatusConflict, "decision_already_resolved", "decision request is already resolved"
	case errors.Is(err, ErrDecisionAlreadyRecorded):
		status, apiError.Code, apiError.Message = http.StatusConflict, "decision_already_recorded", "decision answer is already recorded"
	case errors.Is(err, ErrReconciliationRequired):
		status, apiError.Code, apiError.Message, apiError.ReconciliationRequired = http.StatusConflict, "reconciliation_required", "action reconciliation is required", true
	case errors.Is(err, ErrProjectionIntegrity):
		apiError.Code, apiError.Message = "projection_integrity_failure", "projection integrity check failed"
	case errors.Is(err, runtimecatalog.ErrIntegrity):
		apiError.Code, apiError.Message = "catalog_integrity_failure", "runtime catalog integrity check failed"
	case errors.Is(err, ErrInternalDurableSubstrate):
		apiError.Code, apiError.Message = "internal_durable_substrate_failure", "durable service substrate failed"
	}
	s.writeError(writer, status, apiError)
}

func requestBodyEmpty(request *http.Request) bool {
	if request.ContentLength == 0 {
		return true
	}
	if request.ContentLength > 0 {
		return false
	}
	var probe [1]byte
	_, err := request.Body.Read(probe[:])
	return errors.Is(err, io.EOF)
}

func parseRunsQuery(query url.Values) (int, string, error) {
	page, err := parsePageQuery(query, DefaultRunsPageSize, MaxRunsPageSize)
	return page.PageSize, page.Cursor, err
}

func safeRequestID(presented string) string {
	if ValidatePrincipalID(presented) == nil {
		return presented
	}
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return "request-id-unavailable"
}

func (s *Server) writeError(writer http.ResponseWriter, status int, apiError ErrorV1) {
	if len(apiError.Message) > 256 {
		apiError.Message = "service request failed"
	}
	if apiError.Details != nil {
		encoded, err := json.Marshal(apiError.Details)
		if err != nil || len(encoded) > 4096 {
			apiError.Details = nil
		}
	}
	s.writeJSON(writer, status, ErrorResponseV1{Error: apiError})
}

func (s *Server) writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(status)
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(value); err != nil {
		// Headers are already committed; never copy the encoder error (which may
		// contain a caller-controlled writer diagnostic) into a response.
		return
	}
}

func (s *Server) experienceCapabilities(w http.ResponseWriter, r *http.Request, id string) {
	if validEmptyReadRequest(r) != nil {
		s.writeError(w, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid extension request", RequestID: id})
		return
	}
	s.writeJSON(w, http.StatusOK, PdlcExperienceCapabilitiesV1{SchemaVersion: "PdlcExperienceCapabilitiesV1", ActivityStream: s.reserved.Activity != nil, PreviewRuntime: false})
}
func (s *Server) runActivity(w http.ResponseWriter, r *http.Request, id, run string) {
	if r.Method != http.MethodGet || !requestBodyEmpty(r) {
		s.writeError(w, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid activity request", RequestID: id})
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		s.writeDependencyError(w, id, ErrInvalidCursor)
		return
	}
	page, err := parsePageQuery(query, 100, 500)
	if err != nil {
		s.writeDependencyError(w, id, ErrInvalidCursor)
		return
	}
	if !s.requireRegisteredRun(w, id, run) {
		return
	}
	if s.reserved.Activity == nil {
		s.writeDependencyError(w, id, ErrUnsupportedCapability)
		return
	}
	data, err := s.reserved.Activity.ReadActivity(r.Context(), run, page)
	if err != nil {
		s.writeDependencyError(w, id, err)
		return
	}
	s.writeDependencyJSON(w, id, data)
}
func (s *Server) runActivityStream(w http.ResponseWriter, r *http.Request, id, run string) {
	if validEmptyReadRequest(r) != nil || len(r.Header.Values("Last-Event-ID")) > 1 || len(r.Header.Get("Last-Event-ID")) > 512 {
		s.writeDependencyError(w, id, ErrInvalidCursor)
		return
	}
	if !s.requireRegisteredRun(w, id, run) {
		return
	}
	if s.reserved.Activity == nil {
		s.writeDependencyError(w, id, ErrUnsupportedCapability)
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		s.writeDependencyError(w, id, ErrActivityUnavailable)
		return
	}
	subscription, err := s.reserved.Activity.OpenActivityStream(r.Context(), run, r.Header.Get("Last-Event-ID"))
	if err != nil {
		s.writeDependencyError(w, id, err)
		return
	}
	defer subscription.Close()
	control := http.NewResponseController(w)
	// Each write gets a bounded deadline, replacing the ordinary request-wide
	// timeout only for this admitted stream.
	if err = control.SetWriteDeadline(time.Now().Add(WriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		s.writeDependencyError(w, id, ErrActivityUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if err = control.Flush(); err != nil {
		return
	}
	for {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		frame, readErr := subscription.Next(ctx)
		cancel()
		if r.Context().Err() != nil {
			return
		}
		if readErr != nil && !errors.Is(readErr, context.DeadlineExceeded) {
			return
		}
		if err = control.SetWriteDeadline(time.Now().Add(WriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return
		}
		if readErr != nil {
			_, err = io.WriteString(w, ": keepalive\n\n")
		} else {
			if frame.Ordinal == 0 || !json.Valid(frame.Data) || len(frame.Data) > 32<<10 {
				return
			}
			// Compact JSON prohibits provider text or a dependency newline from
			// injecting SSE fields into the frame.
			var compact bytes.Buffer
			if json.Compact(&compact, frame.Data) != nil {
				return
			}
			_, err = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", frame.Ordinal, compact.Bytes())
		}
		if err != nil || control.Flush() != nil {
			return
		}
	}
}

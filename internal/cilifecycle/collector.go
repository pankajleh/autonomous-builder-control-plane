package cilifecycle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

const ciEvidenceOutcomeEventType = "ci_evidence_collection_outcome"

// ArtifactStore is the immutable evidence boundary required by Controller.
// evidence.Store implements this interface; the interface exists so tests can
// inject errors after an otherwise successful immutable publication.
type ArtifactStore interface {
	Root() string
	RunID() string
	RunDir() string
	WriteBytes(name, kind string, data []byte) (ledger.EvidenceRef, error)
}

// ControllerConfig binds collection to controller-owned durable roots. The
// attempt root and capacity.lock must already exist with modes 0700 and 0600.
type ControllerConfig struct {
	AttemptRoot         string
	GitHub              *GitHubAdapter
	Artifacts           ArtifactStore
	AuthoritativeLedger *ledger.JSONLLedger
}

// Controller publishes exactly one immutable result and one deterministic
// authoritative outcome event for each durably completed attempt.
type Controller struct {
	github    *GitHubAdapter
	attempts  *attemptStore
	artifacts *artifactBoundary
	material  *materialLedger
}

// NewController pins and verifies every local durability boundary.
func NewController(config ControllerConfig) (*Controller, error) {
	if config.GitHub == nil || config.Artifacts == nil || config.AuthoritativeLedger == nil {
		return nil, errors.New("GitHub adapter, evidence store, and authoritative ledger are required")
	}
	if !limitsAtMostProduction(config.GitHub.limits) {
		return nil, errors.New("GitHub adapter limits exceed production")
	}
	attempts, err := newAttemptStore(config.AttemptRoot, productionAttemptLimits())
	if err != nil {
		return nil, fmt.Errorf("open CI attempt allocator: %w", err)
	}
	artifacts, err := newArtifactBoundary(config.Artifacts)
	if err != nil {
		_ = attempts.close()
		return nil, fmt.Errorf("open CI evidence boundary: %w", err)
	}
	material, err := newMaterialLedger(config.AuthoritativeLedger)
	if err != nil {
		_ = artifacts.close()
		_ = attempts.close()
		return nil, fmt.Errorf("open CI material ledger: %w", err)
	}
	return &Controller{github: config.GitHub, attempts: attempts, artifacts: artifacts, material: material}, nil
}

// Close releases pinned local directory descriptors. It does not remove or
// rewrite reservations, evidence, or ledger records.
func (c *Controller) Close() error {
	if c == nil {
		return nil
	}
	return errors.Join(c.material.close(), c.artifacts.close(), c.attempts.close())
}

// Collect returns the authoritative bundle after immutable publication and
// ledger confirmation. A completed non-STABLE attempt returns its bundle with
// a typed CollectionError. Local durability failure returns an empty bundle.
func (c *Controller) Collect(ctx context.Context, request CollectRequest) (CIEvidenceBundleV1, error) {
	identity, requestErr := validateCollectRequest(request)
	if requestErr != nil {
		return CIEvidenceBundleV1{}, requestErr
	}
	if c == nil || c.github == nil || c.attempts == nil || c.artifacts == nil || c.material == nil {
		return CIEvidenceBundleV1{}, integrityError("controller_unavailable", nil)
	}
	if ctx == nil {
		return CIEvidenceBundleV1{}, integrityError("missing_context", nil)
	}
	if request.RunID != c.artifacts.runID() {
		return CIEvidenceBundleV1{}, integrityError("evidence_run_mismatch", nil)
	}

	reservation := newAttemptReservation(request, identity)
	lease, err := c.attempts.acquire(reservation)
	if err != nil {
		return CIEvidenceBundleV1{}, integrityError("attempt_reservation_failed", err)
	}
	defer lease.close()

	eventID := outcomeEventID(identity.attemptKey)
	if lease.needsRepair {
		_, eventFound, inspectErr := c.material.find(eventID)
		if inspectErr != nil {
			return CIEvidenceBundleV1{}, integrityError("reservation_material_inspection_failed", inspectErr)
		}
		if eventFound {
			return CIEvidenceBundleV1{}, integrityError("incomplete_reservation_has_event", lease.poison())
		}
		_, _, artifactFound, inspectErr := c.readAttemptBundle(request, identity)
		if inspectErr != nil {
			if artifactFound {
				inspectErr = errors.Join(inspectErr, lease.poison())
			}
			return CIEvidenceBundleV1{}, integrityError("reservation_material_inspection_failed", inspectErr)
		}
		if artifactFound {
			return CIEvidenceBundleV1{}, integrityError("incomplete_reservation_has_bundle", lease.poison())
		}
		if err := lease.repair(); err != nil {
			return CIEvidenceBundleV1{}, integrityError("reservation_recovery_failed", err)
		}
	}

	observed, found, err := c.material.find(eventID)
	if err != nil {
		return CIEvidenceBundleV1{}, integrityError("ledger_scan_failed", err)
	}
	if lease.created && found {
		return CIEvidenceBundleV1{}, integrityError("event_missing_reservation", lease.poison())
	}
	if found {
		return c.replay(request, identity, observed)
	}

	bundle, ref, artifactFound, err := c.readAttemptBundle(request, identity)
	if err != nil {
		return CIEvidenceBundleV1{}, integrityError("attempt_bundle_recovery_failed", err)
	}
	if artifactFound {
		if lease.created {
			return CIEvidenceBundleV1{}, integrityError("bundle_missing_reservation", lease.poison())
		}
		if err := c.completeEvent(bundle, ref); err != nil {
			return CIEvidenceBundleV1{}, integrityError("ledger_completion_failed", err)
		}
		return bundle, bundleOutcomeError(bundle)
	}

	collected, collectErr := c.github.collectRemote(ctx, request)
	if len(collected.CanonicalJSON()) == 0 {
		return CIEvidenceBundleV1{}, collectErr
	}
	ref, err = c.publishOrVerify(collected)
	if err != nil {
		return CIEvidenceBundleV1{}, integrityError("bundle_publication_failed", err)
	}
	if err := c.completeEvent(collected, ref); err != nil {
		return CIEvidenceBundleV1{}, integrityError("ledger_completion_failed", err)
	}
	if collectErr != nil {
		return collected, collectErr
	}
	return collected, nil
}

type materialEvent struct {
	event     ledger.Event
	canonical []byte
}

func (c *Controller) replay(request CollectRequest, identity attemptIdentity, observed materialEvent) (CIEvidenceBundleV1, error) {
	if observed.event.SchemaVersion != ledger.CurrentSchemaVersion || observed.event.EventID != outcomeEventID(identity.attemptKey) ||
		observed.event.RunID != request.RunID || observed.event.AttemptID != request.AttemptID ||
		observed.event.EventType != ciEvidenceOutcomeEventType || observed.event.Actor != "controller" ||
		observed.event.Source != "cilifecycle" || len(observed.event.EvidenceRefs) != 1 {
		return CIEvidenceBundleV1{}, integrityError("conflicting_outcome_event", nil)
	}
	name := attemptBundleName(identity.attemptKey)
	ref := observed.event.EvidenceRefs[0]
	data, err := c.artifacts.readVerified(name, ref, MaxBundleBytes)
	if err != nil {
		return CIEvidenceBundleV1{}, integrityError("replay_bundle_unavailable", err)
	}
	bundle, err := ReadCIEvidenceBundleV1(data)
	if err != nil || !bytes.Equal(data, bundle.CanonicalJSON()) {
		return CIEvidenceBundleV1{}, integrityError("replay_bundle_invalid", err)
	}
	if err := rebindBundle(bundle, request, identity); err != nil {
		return CIEvidenceBundleV1{}, integrityError("replay_bundle_mismatch", err)
	}
	expected, canonical, err := deterministicOutcomeEvent(bundle, ref)
	if err != nil || expected.EventID != observed.event.EventID || !bytes.Equal(canonical, observed.canonical) {
		return CIEvidenceBundleV1{}, integrityError("conflicting_outcome_event", err)
	}
	return bundle, bundleOutcomeError(bundle)
}

func (c *Controller) readAttemptBundle(request CollectRequest, identity attemptIdentity) (CIEvidenceBundleV1, ledger.EvidenceRef, bool, error) {
	name := attemptBundleName(identity.attemptKey)
	data, ref, found, err := c.artifacts.readExisting(name, EvidenceBundleKindV1, MaxBundleBytes)
	if err != nil || !found {
		return CIEvidenceBundleV1{}, ledger.EvidenceRef{}, found, err
	}
	if err := c.artifacts.stabilize(name); err != nil {
		return CIEvidenceBundleV1{}, ledger.EvidenceRef{}, true, err
	}
	data, err = c.artifacts.readVerified(name, ref, MaxBundleBytes)
	if err != nil {
		return CIEvidenceBundleV1{}, ledger.EvidenceRef{}, true, err
	}
	bundle, err := ReadCIEvidenceBundleV1(data)
	if err != nil || !bytes.Equal(data, bundle.CanonicalJSON()) {
		return CIEvidenceBundleV1{}, ledger.EvidenceRef{}, true, errors.New("deterministic bundle is malformed or noncanonical")
	}
	if err := rebindBundle(bundle, request, identity); err != nil {
		return CIEvidenceBundleV1{}, ledger.EvidenceRef{}, true, err
	}
	return bundle, ref, true, nil
}

func (c *Controller) publishOrVerify(bundle CIEvidenceBundleV1) (ledger.EvidenceRef, error) {
	data := bundle.CanonicalJSON()
	if len(data) == 0 || len(data) > MaxBundleBytes {
		return ledger.EvidenceRef{}, errors.New("invalid bundle publication bytes")
	}
	name := attemptBundleName(bundle.Input().AttemptKeySHA256)
	written, writeErr := c.artifacts.store.WriteBytes(name, EvidenceBundleKindV1, data)
	expected := ledger.EvidenceRef{
		URI: filepath.Join(c.artifacts.runDir(), name), SHA256: bundle.SHA256(), Kind: EvidenceBundleKindV1,
	}
	if writeErr == nil && written != expected {
		return ledger.EvidenceRef{}, errors.New("evidence store returned a conflicting reference")
	}
	if writeErr != nil && !errors.Is(writeErr, evidence.ErrArtifactExists) {
		return ledger.EvidenceRef{}, writeErr
	}
	if err := c.artifacts.stabilize(name); err != nil {
		return ledger.EvidenceRef{}, errors.Join(writeErr, err)
	}
	verified, readErr := c.artifacts.readVerified(name, expected, MaxBundleBytes)
	if readErr != nil {
		return ledger.EvidenceRef{}, errors.Join(writeErr, readErr)
	}
	if !bytes.Equal(verified, data) {
		return ledger.EvidenceRef{}, errors.New("existing deterministic bundle conflicts")
	}
	reconstructed, err := ReadCIEvidenceBundleV1(verified)
	if err != nil || reconstructed.SHA256() != bundle.SHA256() {
		return ledger.EvidenceRef{}, errors.New("published bundle reconstruction failed")
	}
	return expected, nil
}

func (c *Controller) completeEvent(bundle CIEvidenceBundleV1, ref ledger.EvidenceRef) error {
	event, canonical, err := deterministicOutcomeEvent(bundle, ref)
	if err != nil {
		return err
	}
	if err := c.material.record(event, canonical); err != nil {
		return err
	}
	observed, found, err := c.material.find(event.EventID)
	if err != nil || !found || !bytes.Equal(observed.canonical, canonical) {
		return errors.Join(errors.New("outcome event was not confirmed"), err)
	}
	return nil
}

func deterministicOutcomeEvent(bundle CIEvidenceBundleV1, ref ledger.EvidenceRef) (ledger.Event, []byte, error) {
	in := bundle.Input()
	if len(bundle.CanonicalJSON()) == 0 || ref.Kind != EvidenceBundleKindV1 || ref.SHA256 != bundle.SHA256() ||
		!validDigest(ref.SHA256) || !validText(ref.URI, MaxEvidenceURIBytes) || filepath.Base(ref.URI) != attemptBundleName(in.AttemptKeySHA256) {
		return ledger.Event{}, nil, errors.New("invalid authoritative bundle reference")
	}
	payload := map[string]any{
		"schema_version":     SchemaVersionV1,
		"attempt_key_sha256": in.AttemptKeySHA256,
		"authority_sha256":   in.AuthoritySHA256,
		"limits_sha256":      in.LimitsSHA256,
		"outcome":            in.Outcome,
		"bundle_sha256":      bundle.SHA256(),
	}
	if in.Outcome == OutcomeStable {
		payload["collection_identity_sha256"] = in.CollectionIdentitySHA256
	}
	event := ledger.Event{
		SchemaVersion: ledger.CurrentSchemaVersion,
		EventID:       outcomeEventID(in.AttemptKeySHA256),
		Timestamp:     time.Unix(0, in.AttemptEndedUnixNano).UTC(),
		RunID:         in.RunID,
		AttemptID:     in.AttemptID,
		EventType:     ciEvidenceOutcomeEventType,
		Actor:         "controller",
		Source:        "cilifecycle",
		Payload:       payload,
		EvidenceRefs:  []ledger.EvidenceRef{ref},
	}
	if err := event.Validate(); err != nil {
		return ledger.Event{}, nil, err
	}
	canonical, err := json.Marshal(event)
	if err != nil || len(canonical) > MaxLedgerEventBytes {
		return ledger.Event{}, nil, errors.New("deterministic outcome event exceeds its bound")
	}
	return event, canonical, nil
}

func outcomeEventID(attemptKey string) string {
	return digestBytes([]byte("ci-evidence-outcome-v1\x00" + attemptKey))
}

func attemptBundleName(attemptKey string) string { return "ci-" + attemptKey + ".json" }

func rebindBundle(bundle CIEvidenceBundleV1, request CollectRequest, identity attemptIdentity) error {
	in := bundle.Input()
	if in.RunID != request.RunID || in.AttemptID != request.AttemptID || in.AttemptKeySHA256 != identity.attemptKey ||
		in.AuthoritySHA256 != identity.authoritySHA256 || in.LimitsSHA256 != ProductionLimitsSHA256() ||
		in.RepositoryOwner != identity.owner || in.RepositoryName != identity.repository || in.HeadBranch != identity.branch ||
		in.HeadSHA != identity.headSHA || in.ActingKind != identity.actingKind || in.ActingSubject != identity.actingSubject {
		return errors.New("bundle is bound to a different request")
	}
	return nil
}

func bundleOutcomeError(bundle CIEvidenceBundleV1) error {
	in := bundle.Input()
	if in.Outcome == OutcomeStable {
		return nil
	}
	return &CollectionError{Outcome: in.Outcome, FailureCode: in.FailureCode, FailedPhase: in.FailedPhase, FailedPage: in.FailedPage, Cause: errors.New(in.FailureCode)}
}

func integrityError(code string, cause error) *CollectionError {
	return &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: code, Cause: cause}
}

// CollectRequest binds one stable retry identity to the exact governed GitHub
// lifecycle authority. AttemptID chooses a fresh observation only when the
// caller intentionally supplies a new value.
type CollectRequest struct {
	RunID     string
	AttemptID string
	Authority githublifecycle.Authority
}

type requestKeyWireV1 struct {
	SchemaVersion   int    `json:"schema_version"`
	RunID           string `json:"run_id"`
	AuthoritySHA256 string `json:"authority_sha256"`
	RepositoryOwner string `json:"repository_owner"`
	RepositoryName  string `json:"repository_name"`
	HeadBranch      string `json:"head_branch"`
	HeadSHA         string `json:"head_sha"`
	ActingKind      string `json:"acting_kind"`
	ActingSubject   string `json:"acting_subject"`
	LimitsSHA256    string `json:"limits_sha256"`
}

type attemptKeyWireV1 struct {
	RequestKey string `json:"request_key"`
	AttemptID  string `json:"attempt_id"`
}

type chainLinkWireV1 struct {
	Sequence               int    `json:"sequence"`
	RequestSHA256          string `json:"request_sha256"`
	ResponseEnvelopeSHA256 string `json:"response_envelope_sha256"`
}

type collectionIdentityWireV1 struct {
	SchemaVersion                     int               `json:"schema_version"`
	RepositoryOwner                   string            `json:"repository_owner"`
	RepositoryName                    string            `json:"repository_name"`
	HeadSHA                           string            `json:"head_sha"`
	NormalizedSemanticSweepSHA256     string            `json:"normalized_semantic_sweep_sha256"`
	OrderedRequestResponseDigestLinks []chainLinkWireV1 `json:"ordered_request_response_digests"`
}

type attemptIdentity struct {
	authoritySHA256 string
	attemptKey      string
	userID          int64
	owner           string
	repository      string
	branch          string
	headSHA         string
	actingKind      string
	actingSubject   string
}

type attemptReservationV1 struct {
	SchemaVersion         int    `json:"schema_version"`
	Kind                  string `json:"kind"`
	RunID                 string `json:"run_id"`
	AttemptID             string `json:"attempt_id"`
	AttemptKeySHA256      string `json:"attempt_key_sha256"`
	AuthoritySHA256       string `json:"authority_sha256"`
	LimitsSHA256          string `json:"limits_sha256"`
	RepositoryOwner       string `json:"repository_owner"`
	RepositoryName        string `json:"repository_name"`
	HeadBranch            string `json:"head_branch"`
	HeadSHA               string `json:"head_sha"`
	ActingKind            string `json:"acting_kind"`
	ActingSubject         string `json:"acting_subject"`
	ReservedEvidenceBytes int64  `json:"reserved_evidence_bytes"`
}

func newAttemptReservation(request CollectRequest, identity attemptIdentity) attemptReservationV1 {
	return attemptReservationV1{
		SchemaVersion: SchemaVersionV1, Kind: "ci_attempt_reservation_v1", RunID: request.RunID,
		AttemptID: request.AttemptID, AttemptKeySHA256: identity.attemptKey,
		AuthoritySHA256: identity.authoritySHA256, LimitsSHA256: ProductionLimitsSHA256(),
		RepositoryOwner: identity.owner, RepositoryName: identity.repository, HeadBranch: identity.branch,
		HeadSHA: identity.headSHA, ActingKind: identity.actingKind, ActingSubject: identity.actingSubject,
		ReservedEvidenceBytes: MaxCompletedAttemptBytes,
	}
}

func (r attemptReservationV1) canonicalJSON() ([]byte, error) {
	if r.SchemaVersion != SchemaVersionV1 || r.Kind != "ci_attempt_reservation_v1" ||
		!validText(r.RunID, MaxTextBytes) || !validOpaque(r.AttemptID, MaxAttemptIDBytes) ||
		!validDigest(r.AttemptKeySHA256) || !validDigest(r.AuthoritySHA256) || r.LimitsSHA256 != ProductionLimitsSHA256() ||
		!validRepositoryComponent(r.RepositoryOwner) || !validRepositoryComponent(r.RepositoryName) ||
		!validText(r.HeadBranch, MaxTextBytes) || !validGitSHA(r.HeadSHA) || r.ActingKind != "user" ||
		!validUserSubject(r.ActingSubject) || r.ReservedEvidenceBytes != MaxCompletedAttemptBytes {
		return nil, errors.New("invalid CI attempt reservation")
	}
	want := deriveAttemptKey(r.RunID, r.AttemptID, r.AuthoritySHA256, r.RepositoryOwner, r.RepositoryName,
		r.HeadBranch, r.HeadSHA, r.ActingKind, r.ActingSubject)
	if r.AttemptKeySHA256 != want {
		return nil, errors.New("CI attempt reservation identity mismatch")
	}
	return json.Marshal(r)
}

func parseAttemptReservation(data []byte) (attemptReservationV1, error) {
	var reservation attemptReservationV1
	if err := strictDecode(data, &reservation); err != nil {
		return attemptReservationV1{}, err
	}
	canonical, err := reservation.canonicalJSON()
	if err != nil {
		return attemptReservationV1{}, err
	}
	if !bytes.Equal(data, canonical) {
		return attemptReservationV1{}, errors.New("CI attempt reservation is not canonical")
	}
	return reservation, nil
}

func validateCollectRequest(request CollectRequest) (attemptIdentity, *CollectionError) {
	if !validText(request.RunID, MaxTextBytes) || !validOpaque(request.AttemptID, MaxAttemptIDBytes) {
		return attemptIdentity{}, &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: "invalid_collection_request"}
	}
	authorityJSON, err := request.Authority.CanonicalJSON()
	if err != nil || len(authorityJSON) == 0 {
		return attemptIdentity{}, &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: "invalid_authority"}
	}
	authoritySHA, err := request.Authority.SHA256()
	if err != nil || !validDigest(authoritySHA) {
		return attemptIdentity{}, &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: "invalid_authority"}
	}
	actor := request.Authority.Actor()
	userID, ok := parseCanonicalUserSubject(actor.Subject())
	if actor.Kind() != githublifecycle.ActingKindUser || actor.InstallationID() != 0 || !ok {
		return attemptIdentity{}, &CollectionError{Outcome: OutcomeUnsupportedPrincipal, FailureCode: "unsupported_principal"}
	}
	repository := request.Authority.Repository()
	identity := attemptIdentity{
		authoritySHA256: authoritySHA, userID: userID, owner: repository.Owner(), repository: repository.Name(),
		branch: request.Authority.HeadBranch().String(), headSHA: request.Authority.HeadSHA().String(),
		actingKind: string(actor.Kind()), actingSubject: actor.Subject(),
	}
	identity.attemptKey = deriveAttemptKey(request.RunID, request.AttemptID, authoritySHA, identity.owner, identity.repository,
		identity.branch, identity.headSHA, identity.actingKind, identity.actingSubject)
	return identity, nil
}

func deriveAttemptKey(runID, attemptID, authoritySHA, owner, repository, branch, headSHA, actingKind, actingSubject string) string {
	requestKey := digestJSON(requestKeyWireV1{
		SchemaVersionV1, runID, authoritySHA, owner, repository, branch, headSHA, actingKind, actingSubject, ProductionLimitsSHA256(),
	})
	return digestJSON(attemptKeyWireV1{requestKey, attemptID})
}

func parseCanonicalUserSubject(subject string) (int64, bool) {
	value, ok := strings.CutPrefix(subject, "github-user-id:")
	if !ok || value == "" || strings.HasPrefix(value, "+") || (len(value) > 1 && value[0] == '0') {
		return 0, false
	}
	id, err := strconv.ParseInt(value, 10, 64)
	return id, err == nil && id > 0 && strconv.FormatInt(id, 10) == value
}

// collectRemote performs one bounded observation in memory. Task 3 wraps this
// operation in immutable attempt allocation, publication, ledger completion,
// and replay.
func (a *GitHubAdapter) collectRemote(ctx context.Context, request CollectRequest) (CIEvidenceBundleV1, error) {
	identity, requestErr := validateCollectRequest(request)
	if requestErr != nil {
		return CIEvidenceBundleV1{}, requestErr
	}
	if ctx == nil {
		return CIEvidenceBundleV1{}, &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: "missing_context"}
	}
	attemptStarted := a.now().UTC().UnixNano()
	if attemptStarted <= 0 {
		return CIEvidenceBundleV1{}, &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: "invalid_clock"}
	}
	collectionCtx, cancel := context.WithTimeout(ctx, a.limits.collectionTimeout)
	defer cancel()
	session := &requestSession{adapter: a}
	state := remoteCollectionState{request: request, identity: identity, attemptStarted: attemptStarted, session: session}

	principal, failed := session.principal(collectionCtx)
	if failed != nil {
		return state.finish(failed)
	}
	state.principal = principal
	if principal.ID != identity.userID {
		return state.finish(failure(OutcomeIntegrityFailure, "authenticated_user_mismatch", "principal", 0, nil))
	}
	state.collectionStarted = a.now().UTC().UnixNano()
	if state.collectionStarted < attemptStarted {
		return CIEvidenceBundleV1{}, &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: "clock_inversion"}
	}

	head, failed := session.head(collectionCtx, identity.owner, identity.repository, identity.branch, "H0")
	if failed != nil {
		return state.finish(failed)
	}
	state.heads = append(state.heads, head)
	if head.Input().SHA != identity.headSHA {
		return state.finish(failure(OutcomeStaleHead, "head_moved_h0", "H0", 0, nil))
	}

	sweepA, failed := session.sweep(collectionCtx, identity.owner, identity.repository, identity.headSHA, "a")
	if failed != nil {
		return state.finish(failed)
	}
	state.sweepA = &sweepA

	head, failed = session.head(collectionCtx, identity.owner, identity.repository, identity.branch, "H1")
	if failed != nil {
		return state.finish(failed)
	}
	state.heads = append(state.heads, head)
	if head.Input().SHA != identity.headSHA {
		return state.finish(failure(OutcomeStaleHead, "head_moved_h1", "H1", 0, nil))
	}

	sweepB, failed := session.sweep(collectionCtx, identity.owner, identity.repository, identity.headSHA, "b")
	if failed != nil {
		return state.finish(failed)
	}
	state.sweepB = &sweepB

	head, failed = session.head(collectionCtx, identity.owner, identity.repository, identity.branch, "H2")
	if failed != nil {
		return state.finish(failed)
	}
	state.heads = append(state.heads, head)
	if head.Input().SHA != identity.headSHA {
		return state.finish(failure(OutcomeStaleHead, "head_moved_h2", "H2", 0, nil))
	}
	state.collectionEnded = a.now().UTC().UnixNano()
	if state.collectionEnded < state.collectionStarted {
		return CIEvidenceBundleV1{}, &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: "clock_inversion"}
	}
	if sweepA.SHA256() != sweepB.SHA256() || !bytes.Equal(sweepA.CanonicalJSON(), sweepB.CanonicalJSON()) {
		return state.finish(failure(OutcomeUnstable, "semantic_sweeps_differ", "comparison", 0, nil))
	}
	return state.finish(nil)
}

type remoteCollectionState struct {
	request           CollectRequest
	identity          attemptIdentity
	attemptStarted    int64
	collectionStarted int64
	collectionEnded   int64
	principal         principalObservation
	heads             []HeadObservationV1
	sweepA            *CISemanticSweepV1
	sweepB            *CISemanticSweepV1
	session           *requestSession
}

func (s *remoteCollectionState) finish(failed *collectionFailure) (CIEvidenceBundleV1, error) {
	ended := s.session.adapter.now().UTC().UnixNano()
	provenance := s.session.requestProvenance()
	if ended < s.attemptStarted || !attemptContainsProvenance(s.attemptStarted, ended, provenance) {
		return CIEvidenceBundleV1{}, &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: "clock_inversion"}
	}
	if s.collectionStarted > 0 && s.collectionEnded == 0 {
		s.collectionEnded = ended
	}
	if s.collectionEnded > 0 && s.collectionEnded < s.collectionStarted {
		return CIEvidenceBundleV1{}, &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: "clock_inversion"}
	}
	outcome := OutcomeStable
	failureCode, failedPhase, failedPage := "", "", 0
	if failed != nil {
		outcome, failureCode, failedPhase, failedPage = failed.outcome, failed.code, failed.phase, failed.page
	}
	chainDigest, links, err := requestResponseChain(provenance)
	if err != nil {
		return CIEvidenceBundleV1{}, &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: "invalid_request_chain"}
	}
	firstResponse, lastResponse := responseRange(provenance)
	earliestProvider, latestProvider := providerTimestampRange(s.sweepA, s.sweepB)
	semanticA, semanticB, collectionIdentity := "", "", ""
	if s.sweepA != nil {
		semanticA = s.sweepA.SHA256()
	}
	if s.sweepB != nil {
		semanticB = s.sweepB.SHA256()
	}
	if outcome == OutcomeStable {
		collectionIdentity = collectionIdentitySHA256(s.identity.owner, s.identity.repository, s.identity.headSHA, semanticA, links)
	}
	input := CIEvidenceBundleV1Input{
		RunID: s.request.RunID, AttemptID: s.request.AttemptID, AttemptKeySHA256: s.identity.attemptKey,
		AuthoritySHA256: s.identity.authoritySHA256, LimitsSHA256: ProductionLimitsSHA256(),
		RepositoryOwner: s.identity.owner, RepositoryName: s.identity.repository, HeadBranch: s.identity.branch,
		HeadSHA: s.identity.headSHA, ActingKind: s.identity.actingKind, ActingSubject: s.identity.actingSubject,
		AuthenticatedID: s.principal.ID, AuthenticatedNode: s.principal.NodeID, AuthenticatedLogin: s.principal.Login,
		Outcome: outcome, FailureCode: failureCode, FailedPhase: failedPhase, FailedPage: failedPage,
		AttemptStartedUnixNano: s.attemptStarted, AttemptEndedUnixNano: ended,
		CollectionStartedUnixNano: s.collectionStarted, CollectionEndedUnixNano: s.collectionEnded,
		FirstResponseObservedUnixNano: firstResponse, LastResponseObservedUnixNano: lastResponse,
		EarliestProviderStateAt: earliestProvider, LatestProviderStateAt: latestProvider,
		HeadObservations: append([]HeadObservationV1(nil), s.heads...), SweepA: s.sweepA, SweepB: s.sweepB,
		SemanticDigestA: semanticA, SemanticDigestB: semanticB, RequestProvenance: provenance,
		RequestResponseChainSHA256: chainDigest, CollectionIdentitySHA256: collectionIdentity,
	}
	bundle, constructErr := NewCIEvidenceBundleV1(input)
	if constructErr != nil || len(bundle.CanonicalJSON()) > s.session.adapter.limits.maxBundleBytes {
		return CIEvidenceBundleV1{}, &CollectionError{Outcome: OutcomeIntegrityFailure, FailureCode: "bundle_construction_failed"}
	}
	if failed != nil {
		return bundle, &CollectionError{
			Outcome: failed.outcome, FailureCode: failed.code, FailedPhase: failed.phase, FailedPage: failed.page,
			Cause: errors.New(failed.code),
		}
	}
	return bundle, nil
}

func attemptContainsProvenance(started, ended int64, provenance []RequestProvenanceV1) bool {
	for _, item := range provenance {
		input := item.Input()
		if input.RequestStartedUnixNano < started || input.ResponseObservedUnixNano > ended {
			return false
		}
	}
	return true
}

func requestResponseChain(provenance []RequestProvenanceV1) (string, []chainLinkWireV1, error) {
	links := make([]chainLinkWireV1, len(provenance))
	for i, item := range provenance {
		input := item.Input()
		if input.Sequence != i+1 || !validDigest(input.RequestSHA256) || !validDigest(input.ResponseEnvelopeSHA256) {
			return "", nil, errors.New("request provenance chain is invalid")
		}
		links[i] = chainLinkWireV1{input.Sequence, input.RequestSHA256, input.ResponseEnvelopeSHA256}
	}
	data, err := json.Marshal(links)
	if err != nil {
		return "", nil, err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), links, nil
}

func collectionIdentitySHA256(owner, repository, headSHA, semanticDigest string, links []chainLinkWireV1) string {
	return digestJSON(collectionIdentityWireV1{
		SchemaVersionV1, owner, repository, headSHA, semanticDigest, append([]chainLinkWireV1(nil), links...),
	})
}

func responseRange(provenance []RequestProvenanceV1) (int64, int64) {
	if len(provenance) == 0 {
		return 0, 0
	}
	first := provenance[0].Input().ResponseObservedUnixNano
	last := first
	for _, item := range provenance[1:] {
		observed := item.Input().ResponseObservedUnixNano
		if observed < first {
			first = observed
		}
		if observed > last {
			last = observed
		}
	}
	return first, last
}

func providerTimestampRange(sweeps ...*CISemanticSweepV1) (string, string) {
	var values []string
	for _, sweep := range sweeps {
		if sweep == nil {
			continue
		}
		input := sweep.Input()
		for _, suite := range input.CheckSuites {
			item := suite.Input()
			values = append(values, item.CreatedAt, item.UpdatedAt)
		}
		for _, run := range input.CheckRuns {
			item := run.Input()
			if item.StartedAt != nil {
				values = append(values, *item.StartedAt)
			}
			if item.CompletedAt != nil {
				values = append(values, *item.CompletedAt)
			}
		}
		for _, status := range input.CommitStatuses {
			item := status.Input()
			values = append(values, item.CreatedAt, item.UpdatedAt)
		}
	}
	if len(values) == 0 {
		return "", ""
	}
	earliest, latest := values[0], values[0]
	for _, value := range values[1:] {
		when, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			continue
		}
		first, _ := time.Parse(time.RFC3339Nano, earliest)
		last, _ := time.Parse(time.RFC3339Nano, latest)
		if when.Before(first) {
			earliest = value
		}
		if when.After(last) {
			latest = value
		}
	}
	return earliest, latest
}

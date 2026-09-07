//go:build linux

package prlifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

type ArtifactWriter interface {
	RunID() string
	RunDir() string
	WriteBytes(name, kind string, data []byte) (ledger.EvidenceRef, error)
}

type ControllerConfig struct {
	Store     *PRWriteAdmissionStore
	GitHub    *GitHubAdapter
	Artifacts ArtifactWriter
	Ledger    *MaterialLedgerRecorder
	Now       func() time.Time
}

type Controller struct {
	store     *PRWriteAdmissionStore
	github    *GitHubAdapter
	artifacts ArtifactWriter
	ledger    *MaterialLedgerRecorder
	now       func() time.Time
}

func newController(config ControllerConfig) (*Controller, error) {
	if config.Store == nil || config.GitHub == nil || config.Artifacts == nil || config.Ledger == nil {
		return nil, errors.New("PR lifecycle store, GitHub adapter, artifacts, and material ledger are required")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if !limitsAllowed(config.GitHub.limits) {
		return nil, errors.New(CodePolicyMismatch)
	}
	return &Controller{config.Store, config.GitHub, config.Artifacts, config.Ledger, config.Now}, nil
}

type Request struct {
	RunID     string
	Authority githublifecycle.Authority
	Title     string
	Body      string
}

type resourceRecord struct {
	SchemaVersion  int    `json:"schema_version"`
	ResourceKey    string `json:"resource_key"`
	Repository     string `json:"repository"`
	BaseBranch     string `json:"base_branch"`
	HeadRepository string `json:"head_repository"`
	HeadBranch     string `json:"head_branch"`
	PolicySHA256   string `json:"admission_policy_sha256"`
	LimitsSHA256   string `json:"production_limits_sha256"`
}

type revisionRecord struct {
	SchemaVersion   int             `json:"schema_version"`
	ResourceKey     string          `json:"resource_key"`
	Ordinal         uint64          `json:"ordinal"`
	RequestSHA256   string          `json:"request_sha256"`
	SourceAuthority json.RawMessage `json:"source_authority"`
	SourceSHA256    string          `json:"source_authority_sha256"`
	Authority       json.RawMessage `json:"derived_authority"`
	AuthoritySHA256 string          `json:"derived_authority_sha256"`
	HeadSHA         string          `json:"accepted_head_sha"`
	ActorSubject    string          `json:"actor_subject"`
	Title           string          `json:"title"`
	Body            string          `json:"body"`
	DocumentSHA256  string          `json:"document_sha256"`
	Mode            string          `json:"mode"`
	PRNumber        int64           `json:"pr_number,omitempty"`
	PRNodeID        string          `json:"pr_node_id,omitempty"`
	PolicySHA256    string          `json:"admission_policy_sha256"`
	LimitsSHA256    string          `json:"production_limits_sha256"`
}

type generationRecord struct {
	SchemaVersion   int             `json:"schema_version"`
	ResourceKey     string          `json:"resource_key"`
	Revision        uint64          `json:"revision"`
	Generation      uint64          `json:"generation"`
	WriteID         string          `json:"write_id"`
	Attempt         json.RawMessage `json:"write_attempt"`
	AttemptSHA256   string          `json:"write_attempt_sha256"`
	AuthoritySHA256 string          `json:"authority_sha256"`
	PayloadSHA256   string          `json:"payload_sha256"`
	Mode            string          `json:"mode"`
	PRNumber        int64           `json:"pr_number,omitempty"`
	PRNodeID        string          `json:"pr_node_id,omitempty"`
	PrepareSHA256   string          `json:"prepare_proof_sha256"`
}

type submittedRecord struct {
	SchemaVersion int    `json:"schema_version"`
	ResourceKey   string `json:"resource_key"`
	Revision      uint64 `json:"revision"`
	Generation    uint64 `json:"generation"`
	WriteID       string `json:"write_id"`
	GenerationSHA string `json:"generation_sha256"`
	FreshProofSHA string `json:"fresh_proof_sha256"`
	SubmittedAt   int64  `json:"submitted_unix_nano"`
	Reservation   int64  `json:"terminal_byte_reservation"`
}

type prepareBundle struct {
	SchemaVersion int                        `json:"schema_version"`
	Principal     RemotePrincipalObservation `json:"principal"`
	Head          RemoteRefObservation       `json:"head"`
	Base          RemoteRefObservation       `json:"base"`
	Discovery     json.RawMessage            `json:"discovery,omitempty"`
	Selected      *RemotePRObservation       `json:"selected,omitempty"`
	Mode          string                     `json:"mode"`
	AuthoritySHA  string                     `json:"authority_sha256"`
}

type snapshotRecoveryWire struct {
	Provider      string `json:"provider"`
	RequestID     string `json:"request_id"`
	ObservedAt    int64  `json:"observed_unix_nano"`
	RepositoryOwn string `json:"repository_owner"`
	Repository    string `json:"repository_name"`
	PRNumber      int64  `json:"pr_number"`
	PRNodeID      string `json:"pr_node_id"`
	BaseBranch    string `json:"base_branch"`
	BaseTipSHA    string `json:"base_tip_sha"`
	HeadBranch    string `json:"head_branch"`
	HeadSHA       string `json:"head_sha"`
	State         string `json:"state"`
}

type terminalCoreV1 struct {
	SchemaVersion       int                        `json:"schema_version"`
	PolicySHA256        string                     `json:"admission_policy_sha256"`
	LimitsSHA256        string                     `json:"production_limits_sha256"`
	ResourceKey         string                     `json:"resource_key"`
	Revision            uint64                     `json:"revision"`
	Generation          uint64                     `json:"generation"`
	GenerationSHA256    string                     `json:"generation_record_sha256"`
	SubmittedSHA256     string                     `json:"submitted_marker_sha256"`
	Attempt             json.RawMessage            `json:"write_attempt"`
	AttemptSHA256       string                     `json:"write_attempt_sha256"`
	SourceAuthority     json.RawMessage            `json:"source_authority"`
	SourceAuthoritySHA  string                     `json:"source_authority_sha256"`
	DerivedAuthority    json.RawMessage            `json:"derived_authority"`
	DerivedAuthoritySHA string                     `json:"derived_authority_sha256"`
	ExpectedContentSHA  string                     `json:"expected_merge_content_sha256"`
	Title               string                     `json:"title"`
	Body                string                     `json:"body"`
	DocumentSHA256      string                     `json:"document_sha256"`
	Snapshot            json.RawMessage            `json:"snapshot"`
	SnapshotSHA256      string                     `json:"snapshot_sha256"`
	SnapshotRecovery    snapshotRecoveryWire       `json:"snapshot_recovery"`
	Principal           RemotePrincipalObservation `json:"principal"`
	PullRequest         RemotePRObservation        `json:"pull_request"`
	Head                RemoteRefObservation       `json:"head_ref"`
	Base                RemoteRefObservation       `json:"base_ref"`
	Reconciliation      json.RawMessage            `json:"reconciliation,omitempty"`
	ResultCore          PRLifecycleResultCoreV1    `json:"result_core"`
	ResultCoreSHA256    string                     `json:"result_core_sha256"`
	Reason              string                     `json:"reason"`
	FailureCode         string                     `json:"failure_code,omitempty"`
	RunID               string                     `json:"run_id"`
	TerminalUnixNano    int64                      `json:"terminal_unix_nano"`
	EvidenceRefs        []ledger.EvidenceRef       `json:"evidence_refs"`
	EvidenceArtifacts   []terminalArtifact         `json:"evidence_artifacts"`
}

type terminalArtifact struct {
	Name   string          `json:"name"`
	Kind   string          `json:"kind"`
	Bytes  json.RawMessage `json:"bytes"`
	SHA256 string          `json:"sha256"`
}

type terminalV1 struct {
	Core               terminalCoreV1  `json:"core"`
	TerminalCoreSHA256 string          `json:"terminal_core_sha256"`
	MaterialEvent      json.RawMessage `json:"material_event"`
	MaterialEventSHA   string          `json:"material_event_sha256"`
}

type prepared struct {
	bundle    prepareBundle
	authority githublifecycle.Authority
	mode      string
	pr        *RemotePRObservation
	snapshot  githublifecycle.PullRequestSnapshot
}

func (c *Controller) Upsert(ctx context.Context, request Request) (PRLifecycleResultV1, error) {
	var result PRLifecycleResultV1
	if ctx == nil || request.RunID == "" || request.RunID != c.artifacts.RunID() {
		return result, errors.New("context and matching controller run identity are required")
	}
	limits := c.github.limits
	if err := validateDocument(request.Title, request.Body, limits.MaxTextBytes); err != nil {
		return result, err
	}
	authorityBytes, err := request.Authority.CanonicalJSON()
	if err != nil {
		return result, err
	}
	actorID, err := trackActorID(request.Authority.Actor())
	if err != nil {
		return result, err
	}
	_ = actorID
	key, err := NewPRResourceKey(request.Authority.Repository(), request.Authority.BaseBranch(), request.Authority.HeadBranch())
	if err != nil {
		return result, err
	}
	requestSHA, err := canonicalDigest(struct {
		Authority json.RawMessage `json:"authority"`
		Title     string          `json:"title"`
		Body      string          `json:"body"`
	}{authorityBytes, request.Title, request.Body})
	if err != nil {
		return result, err
	}
	err = c.store.withResource(key, func(tx *resourceTxn) error {
		var runErr error
		result, runErr = c.upsertLocked(ctx, tx, request, requestSHA)
		return runErr
	})
	return result, err
}

func (c *Controller) upsertLocked(ctx context.Context, tx *resourceTxn, request Request, requestSHA string) (PRLifecycleResultV1, error) {
	var zero PRLifecycleResultV1
	if err := c.ensureResourceRecord(tx, request.Authority); err != nil {
		return zero, err
	}
	latest, err := latestRevision(c.store, tx.key)
	if err != nil {
		return zero, err
	}
	var previous *terminalV1
	var ordinal uint64 = 1
	if latest > 0 {
		ordinal = latest
		revision, err := readRevision(tx, latest)
		if err != nil {
			return zero, err
		}
		terminalName := recordPrefix(tx.key, latest) + "terminal.json"
		terminalExists, err := tx.exists(terminalName)
		if err != nil {
			return zero, err
		}
		if revision.RequestSHA256 == requestSHA && terminalExists {
			return c.recoverTerminal(tx, terminalName, request.RunID)
		}
		generationExists, err := tx.exists(recordPrefix(tx.key, latest) + "generation.json")
		if err != nil {
			return zero, err
		}
		if revision.RequestSHA256 != requestSHA {
			if generationExists {
				if !terminalExists {
					return zero, &Error{Code: CodeRevisionConflict, Cause: errors.New("active revision has a generation")}
				}
				prior, err := readTerminal(tx, terminalName)
				if err != nil {
					return zero, err
				}
				_, priorGenerationSHA, priorMarkerSHA, err := readGenerationAndMarker(tx, latest)
				if err != nil || prior.Core.GenerationSHA256 != priorGenerationSHA || prior.Core.SubmittedSHA256 != priorMarkerSHA {
					return zero, &Error{Code: CodeIntegrityFailure, Cause: firstError(err, errors.New("prior terminal admission chain differs"))}
				}
				if prior.Core.ResultCore.Disposition != AppliedConfirmed {
					return zero, &Error{Code: CodeRevisionConflict, Cause: errors.New("ambiguous or divergent history barriers later revisions")}
				}
				previous = &prior
			} else {
				_, _, err := tx.createJSON(recordPrefix(tx.key, latest)+"superseded.json", struct {
					SchemaVersion int    `json:"schema_version"`
					ResourceKey   string `json:"resource_key"`
					Revision      uint64 `json:"revision"`
					Reason        string `json:"reason"`
				}{SchemaVersion, tx.key.String(), latest, "superseded_zero_write"}, false, false)
				if err != nil {
					return zero, err
				}
			}
			ordinal = latest + 1
		} else if generationExists {
			return c.resumeGeneration(ctx, tx, request, revision)
		}
	}

	prepareRound := nextLegacyRound(c.store, tx.key, ordinal, "prepare", c.github.limits.MaxReadRetries+1)
	if prepareRound == 0 {
		return zero, &Error{Code: CodePreflightBudgetExhausted, Cause: errors.New("bounded prepare rounds exhausted")}
	}
	prep, prepBytes, prepRef, err := c.prepare(ctx, tx.key, ordinal, prepareRound, "prepare", request, previous)
	if err != nil {
		failure, _ := json.Marshal(struct {
			SchemaVersion int    `json:"schema_version"`
			Error         string `json:"error"`
		}{SchemaVersion, boundedError(err)})
		_, _, _ = tx.create(recordPrefix(tx.key, ordinal)+"prepare-"+strconv.Itoa(prepareRound)+".json", failure, false, false)
		return zero, err
	}
	revision, err := c.makeRevision(tx.key, ordinal, request, requestSHA, prep)
	if err != nil {
		return zero, err
	}
	revisionName := recordPrefix(tx.key, ordinal) + "revision.json"
	if _, _, err := tx.createJSON(revisionName, revision, false, false); err != nil {
		return zero, err
	}
	prepareName := recordPrefix(tx.key, ordinal) + "prepare-" + strconv.Itoa(prepareRound) + ".json"
	if _, _, err := tx.create(prepareName, prepBytes, false, false); err != nil {
		return zero, err
	}
	return c.submit(ctx, tx, request, revision, prep, prepBytes, []ledger.EvidenceRef{prepRef}, false)
}

func (c *Controller) ensureResourceRecord(tx *resourceTxn, authority githublifecycle.Authority) error {
	policySHA, _ := c.store.policy.SHA256()
	limitsSHA, _ := c.github.limits.SHA256()
	record := resourceRecord{SchemaVersion, tx.key.String(), authority.Repository().String(), authority.BaseBranch().String(), authority.Repository().String(), authority.HeadBranch().String(), policySHA, limitsSHA}
	name := "r-" + tx.key.String() + "-resource.json"
	exists, err := tx.exists(name)
	if err != nil {
		return err
	}
	if !exists {
		_, _, err = tx.createJSON(name, record, false, false)
		return err
	}
	b, err := tx.read(name, 16<<10)
	if err != nil {
		return err
	}
	expected, _ := json.Marshal(record)
	if !bytes.Equal(b, expected) {
		return &Error{Code: CodePolicyMismatch, Cause: errors.New("stored physical-resource policy or identity differs")}
	}
	return nil
}

func (c *Controller) prepare(ctx context.Context, key PRResourceKeyV1, ordinal uint64, round int, phase string, request Request, previous *terminalV1) (prepared, []byte, ledger.EvidenceRef, error) {
	var out prepared
	principal, err := c.github.principal(ctx)
	if err != nil {
		return out, nil, ledger.EvidenceRef{}, err
	}
	expectedActorID, _ := trackActorID(request.Authority.Actor())
	if principal.ID != expectedActorID {
		return out, nil, ledger.EvidenceRef{}, errors.New("authenticated GitHub user does not match Authority actor")
	}
	head, err := c.github.ref(ctx, request.Authority.Repository(), request.Authority.HeadBranch())
	if err != nil || head.SHA != request.Authority.HeadSHA().String() {
		return out, nil, ledger.EvidenceRef{}, &Error{Code: CodeRemoteHeadDiverged, Cause: firstError(err, errors.New("accepted head is not published at exact SHA"))}
	}
	base, err := c.github.ref(ctx, request.Authority.Repository(), request.Authority.BaseBranch())
	if err != nil || base.SHA != request.Authority.ExpectedBaseTipSHA().String() {
		return out, nil, ledger.EvidenceRef{}, &Error{Code: CodeStaleAuthority, Cause: firstError(err, errors.New("base branch advanced from READY_FOR_MERGE authority"))}
	}
	out.authority = request.Authority
	out.mode = "CREATE"
	var discovery json.RawMessage
	if bound, ok := request.Authority.PullRequest(); ok {
		pr, err := c.github.pullRequest(ctx, request.Authority.Repository(), bound.Number())
		if err != nil || pr.NodeID != bound.NodeID() {
			return out, nil, ledger.EvidenceRef{}, &Error{Code: CodeExistingIneligible, Cause: firstError(err, errors.New("authority-bound PR identity differs"))}
		}
		out.pr = &pr
		out.mode = "UPDATE"
	} else {
		items, raw, err := c.github.discover(ctx, request.Authority.Repository(), request.Authority.BaseBranch(), request.Authority.HeadBranch())
		if err != nil {
			return out, nil, ledger.EvidenceRef{}, err
		}
		discovery = raw
		if len(items) > 1 {
			return out, nil, ledger.EvidenceRef{}, errors.New("multiple filtered open pull requests are ambiguous")
		}
		if len(items) == 1 {
			pr, err := c.github.pullRequest(ctx, request.Authority.Repository(), items[0].Number)
			if err != nil || pr.NodeID != items[0].NodeID {
				return out, nil, ledger.EvidenceRef{}, &Error{Code: CodeExistingIneligible, Cause: firstError(err, errors.New("list and full PR identity differ"))}
			}
			identity, err := githublifecycle.NewPullRequestIdentity(pr.Number, pr.NodeID)
			if err != nil {
				return out, nil, ledger.EvidenceRef{}, err
			}
			input := request.Authority.Input()
			input.PullRequest = &identity
			out.authority, err = githublifecycle.NewAuthority(input)
			if err != nil {
				return out, nil, ledger.EvidenceRef{}, err
			}
			out.pr = &pr
			out.mode = "UPDATE"
		}
	}
	if out.pr != nil {
		snapshot, err := validateExactPR(out.authority, *out.pr, head, base, c.github.limits)
		if err != nil {
			return out, nil, ledger.EvidenceRef{}, &Error{Code: CodeExistingIneligible, Cause: err}
		}
		out.snapshot = snapshot
		if previous != nil && (previous.Core.Title != out.pr.Title || previous.Core.Body != out.pr.Body || previous.Core.ResultCore.PRNumber != out.pr.Number || previous.Core.ResultCore.PRNodeID != out.pr.NodeID) {
			return out, nil, ledger.EvidenceRef{}, &Error{Code: CodeRevisionConflict, Cause: errors.New("prior confirmed document changed remotely")}
		}
	}
	authoritySHA, _ := out.authority.SHA256()
	out.bundle = prepareBundle{SchemaVersion, principal, head, base, discovery, out.pr, out.mode, authoritySHA}
	b, err := json.Marshal(out.bundle)
	if err != nil || len(b) > 128<<10 {
		return out, nil, ledger.EvidenceRef{}, errors.New("preflight proof exceeds bound")
	}
	name := evidenceName(request.RunID, key, ordinal, round, phase)
	ref, err := publishOrVerify(c.artifacts, name, "pr-lifecycle-preflight", b)
	return out, b, ref, err
}

func (c *Controller) makeRevision(key PRResourceKeyV1, ordinal uint64, request Request, requestSHA string, prep prepared) (revisionRecord, error) {
	source, _ := request.Authority.CanonicalJSON()
	sourceSHA, _ := request.Authority.SHA256()
	derived, _ := prep.authority.CanonicalJSON()
	derivedSHA, _ := prep.authority.SHA256()
	policySHA, _ := c.store.policy.SHA256()
	limitsSHA, _ := c.github.limits.SHA256()
	docSHA := documentDigest(request.Title, request.Body)
	r := revisionRecord{SchemaVersion, key.String(), ordinal, requestSHA, source, sourceSHA, derived, derivedSHA, request.Authority.HeadSHA().String(), request.Authority.Actor().Subject(), request.Title, request.Body, docSHA, prep.mode, 0, "", policySHA, limitsSHA}
	if prep.pr != nil {
		r.PRNumber, r.PRNodeID = prep.pr.Number, prep.pr.NodeID
	}
	return r, nil
}

func (c *Controller) submit(ctx context.Context, tx *resourceTxn, request Request, revision revisionRecord, prep prepared, proof []byte, refs []ledger.EvidenceRef, resumed bool) (PRLifecycleResultV1, error) {
	var zero PRLifecycleResultV1
	writeID := deterministicWriteID(tx.key, revision.Ordinal)
	input, err := githublifecycle.NewUpsertPullRequestInput(prep.authority, revision.Title, revision.Body, writeID, c.github.limits)
	if err != nil {
		return zero, err
	}
	attemptBytes, err := input.Attempt().CanonicalJSON()
	if err != nil || len(attemptBytes) > 8<<10 {
		return zero, errors.New("write attempt exceeds terminal profile")
	}
	authorityBytes, _ := prep.authority.CanonicalJSON()
	if _, err := NewTerminalBudget(authorityBytes, attemptBytes, revision.Title, revision.Body); err != nil {
		return zero, err
	}
	generation := generationRecord{SchemaVersion, tx.key.String(), revision.Ordinal, 1, writeID, attemptBytes, digestBytes(attemptBytes), revision.AuthoritySHA256, input.Attempt().PayloadSHA256(), revision.Mode, revision.PRNumber, revision.PRNodeID, digestBytes(proof)}
	genName := recordPrefix(tx.key, revision.Ordinal) + "generation.json"
	var genBytes []byte
	var genSHA string
	if resumed {
		genBytes, err = tx.read(genName, 128<<10)
		if err != nil {
			return zero, err
		}
		var stored generationRecord
		if err := strictJSON(genBytes, &stored); err != nil || stored.ResourceKey != generation.ResourceKey || stored.Revision != generation.Revision || stored.Generation != 1 || stored.WriteID != generation.WriteID || !bytes.Equal(stored.Attempt, generation.Attempt) || stored.AuthoritySHA256 != generation.AuthoritySHA256 || stored.PayloadSHA256 != generation.PayloadSHA256 || stored.Mode != generation.Mode || stored.PRNumber != generation.PRNumber || stored.PRNodeID != generation.PRNodeID {
			return zero, &Error{Code: CodeIntegrityFailure, Cause: firstError(err, errors.New("resumed generation differs from immutable allocation"))}
		}
		generation = stored
		genSHA = digestBytes(genBytes)
	} else {
		genBytes, genSHA, err = tx.createJSON(genName, generation, false, false)
		if err != nil {
			return zero, err
		}
	}
	_ = genBytes
	marker := submittedRecord{SchemaVersion, tx.key.String(), revision.Ordinal, 1, writeID, genSHA, digestBytes(proof), c.now().UTC().UnixNano(), MaxTerminalBytes}
	markerName := recordPrefix(tx.key, revision.Ordinal) + "submitted.json"
	_, markerSHA, err := tx.createJSON(markerName, marker, true, false)
	if err != nil {
		return zero, err // marker durability is uncertain; never call HTTP.
	}
	prNumber := int64(0)
	if revision.Mode == "UPDATE" {
		prNumber = revision.PRNumber
	}
	_, writeErr := c.github.write(ctx, prep.authority.Repository(), prNumber, revision.Title, revision.Body, prep.authority.BaseBranch(), prep.authority.HeadBranch())
	return c.reconcile(ctx, tx, request, revision, prep.authority, input, generation, genSHA, markerSHA, refs, writeErr)
}

func (c *Controller) resumeGeneration(ctx context.Context, tx *resourceTxn, request Request, revision revisionRecord) (PRLifecycleResultV1, error) {
	storedGeneration, _, err := readGeneration(tx, revision.Ordinal)
	if err != nil || storedGeneration.AuthoritySHA256 != revision.AuthoritySHA256 || storedGeneration.Mode != revision.Mode || storedGeneration.PRNumber != revision.PRNumber || storedGeneration.PRNodeID != revision.PRNodeID {
		return PRLifecycleResultV1{}, &Error{Code: CodeIntegrityFailure, Cause: firstError(err, errors.New("generation does not bind active revision"))}
	}
	markerName := recordPrefix(tx.key, revision.Ordinal) + "submitted.json"
	marker, err := tx.exists(markerName)
	if err != nil {
		return PRLifecycleResultV1{}, err
	}
	if marker {
		authority, err := authorityForGeneration(request.Authority, revision)
		if err != nil {
			return PRLifecycleResultV1{}, err
		}
		input, err := githublifecycle.NewUpsertPullRequestInput(authority, revision.Title, revision.Body, deterministicWriteID(tx.key, revision.Ordinal), c.github.limits)
		if err != nil {
			return PRLifecycleResultV1{}, err
		}
		generation, genSHA, markerSHA, err := readGenerationAndMarker(tx, revision.Ordinal)
		if err != nil {
			return PRLifecycleResultV1{}, err
		}
		return c.reconcile(ctx, tx, request, revision, authority, input, generation, genSHA, markerSHA, nil, &Error{Code: CodeAmbiguousUnresolved, Submitted: true})
	}
	resumeRound := nextLegacyRound(c.store, tx.key, revision.Ordinal, "resume", MaxResumeRounds)
	if resumeRound == 0 {
		return PRLifecycleResultV1{}, &Error{Code: CodeResumeBudgetExhausted, Cause: errors.New("four resume rounds exhausted")}
	}
	prep, proof, ref, err := c.prepare(ctx, tx.key, revision.Ordinal, resumeRound, "resume", request, nil)
	if err != nil {
		return PRLifecycleResultV1{}, err
	}
	name := recordPrefix(tx.key, revision.Ordinal) + "resume-" + strconv.Itoa(resumeRound) + ".json"
	if _, _, err := tx.create(name, proof, false, false); err != nil {
		return PRLifecycleResultV1{}, err
	}
	return c.submit(ctx, tx, request, revision, prep, proof, []ledger.EvidenceRef{ref}, true)
}

func authorityForGeneration(source githublifecycle.Authority, revision revisionRecord) (githublifecycle.Authority, error) {
	input := source.Input()
	if revision.PRNumber > 0 {
		identity, err := githublifecycle.NewPullRequestIdentity(revision.PRNumber, revision.PRNodeID)
		if err != nil {
			return githublifecycle.Authority{}, err
		}
		input.PullRequest = &identity
	}
	authority, err := githublifecycle.NewAuthority(input)
	if err != nil {
		return authority, err
	}
	sha, _ := authority.SHA256()
	if sha != revision.AuthoritySHA256 {
		return githublifecycle.Authority{}, &Error{Code: CodeIntegrityFailure, Cause: errors.New("request cannot reconstruct generation authority")}
	}
	return authority, nil
}

type reconciliationStartV1 struct {
	SchemaVersion       int    `json:"schema_version"`
	Round               int    `json:"round"`
	ResourceKey         string `json:"resource_key"`
	Revision            uint64 `json:"revision"`
	Generation          uint64 `json:"generation"`
	WriteID             string `json:"write_id"`
	AttemptSHA256       string `json:"write_attempt_sha256"`
	PreviousRoundSHA256 string `json:"previous_round_sha256,omitempty"`
	AdmittedUnixNano    int64  `json:"admitted_unix_nano"`
	PolicySHA256        string `json:"admission_policy_sha256"`
	LimitsSHA256        string `json:"production_limits_sha256"`
}

type reconciliationObservationV1 struct {
	SchemaVersion    int                        `json:"schema_version"`
	Round            int                        `json:"round"`
	StartSHA256      string                     `json:"start_sha256"`
	Outcome          string                     `json:"outcome"`
	Principal        RemotePrincipalObservation `json:"principal"`
	Candidate        *discoveryCandidate        `json:"candidate,omitempty"`
	PullRequest      RemotePRObservation        `json:"pull_request"`
	Head             RemoteRefObservation       `json:"head_ref"`
	Base             RemoteRefObservation       `json:"base_ref"`
	ObservedUnixNano int64                      `json:"observed_unix_nano"`
}

func (c *Controller) beginReconciliation(tx *resourceTxn, revision revisionRecord, generation generationRecord) (int, string, error) {
	entries, err := c.store.readDir()
	if err != nil {
		return 0, "", err
	}
	prefix := recordPrefix(tx.key, revision.Ordinal) + "reconcile-"
	policySHA, _ := c.store.policy.SHA256()
	limitsSHA, _ := c.github.limits.SHA256()
	latest := 0
	starts := make(map[int]bool)
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, "-start.json") {
			raw := strings.TrimSuffix(strings.TrimPrefix(name, prefix), "-start.json")
			n, parseErr := strconv.Atoi(raw)
			if parseErr != nil || n < 1 || n > MaxReconciliationRounds {
				return 0, "", &Error{Code: CodeIntegrityFailure, Cause: errors.New("malformed reconciliation start name")}
			}
			if n > latest {
				latest = n
			}
			starts[n] = true
		}
	}
	if latest >= MaxReconciliationRounds {
		return 0, "", &Error{Code: CodeReconcileBudgetExhausted, Submitted: true, Attempt: generation.WriteID, Cause: errors.New("reconciliation rounds exhausted")}
	}
	previousSHA := ""
	if latest > 0 {
		var previous reconciliationStartV1
		for ordinal := 1; ordinal <= latest; ordinal++ {
			if !starts[ordinal] {
				return 0, "", &Error{Code: CodeIntegrityFailure, Cause: errors.New("reconciliation start sequence has a gap")}
			}
			previousBytes, readErr := tx.read(prefix+strconv.Itoa(ordinal)+"-start.json", DefaultReconciliationPolicy().MaxBytesPerRound)
			var observed reconciliationStartV1
			if readErr != nil || strictJSON(previousBytes, &observed) != nil || observed.Round != ordinal || observed.ResourceKey != tx.key.String() || observed.Revision != revision.Ordinal || observed.Generation != 1 || observed.WriteID != generation.WriteID || observed.AttemptSHA256 != generation.AttemptSHA256 || observed.PreviousRoundSHA256 != previousSHA || observed.AdmittedUnixNano <= 0 || observed.PolicySHA256 != policySHA || observed.LimitsSHA256 != limitsSHA {
				return 0, "", &Error{Code: CodeIntegrityFailure, Cause: errors.New("prior reconciliation start is malformed")}
			}
			previous = observed
			previousSHA = digestBytes(previousBytes)
		}
		if c.now().UTC().UnixNano()-previous.AdmittedUnixNano < int64(MinReconciliationInterval) {
			return 0, "", &Error{Code: CodeReconcileBudgetExhausted, Submitted: true, Attempt: generation.WriteID, Cause: errors.New("reconciliation minimum interval has not elapsed")}
		}
	}
	round := latest + 1
	start := reconciliationStartV1{SchemaVersion, round, tx.key.String(), revision.Ordinal, 1, generation.WriteID, generation.AttemptSHA256, previousSHA, c.now().UTC().UnixNano(), policySHA, limitsSHA}
	startBytes, _ := json.Marshal(start)
	if err := c.checkReconciliationAggregate(tx, revision.Ordinal, int64(len(startBytes))); err != nil {
		return 0, "", err
	}
	_, startSHA, err := tx.create(prefix+strconv.Itoa(round)+"-start.json", startBytes, false, false)
	return round, startSHA, err
}

func (c *Controller) checkReconciliationAggregate(tx *resourceTxn, revision uint64, add int64) error {
	entries, err := c.store.readDir()
	if err != nil {
		return err
	}
	var total int64
	needle := recordPrefix(tx.key, revision) + "reconcile-"
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), needle) && strings.Contains(entry.Name(), "-reconcile-") {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			total += info.Size()
		}
	}
	if add > int64(DefaultReconciliationPolicy().MaxBytesPerRound) || total+add > int64(DefaultReconciliationPolicy().MaxAggregateBytes) {
		return &Error{Code: CodeReconcileBudgetExhausted, Submitted: true, Cause: errors.New("reconciliation byte budget exhausted")}
	}
	return nil
}

func (c *Controller) reconcile(ctx context.Context, tx *resourceTxn, request Request, revision revisionRecord, authority githublifecycle.Authority, input githublifecycle.UpsertPullRequestInput, generation generationRecord, genSHA, markerSHA string, refs []ledger.EvidenceRef, writeErr error) (PRLifecycleResultV1, error) {
	if _, err := githublifecycle.NewReconcileWriteInput(input.Attempt(), c.github.limits); err != nil {
		return PRLifecycleResultV1{}, &Error{Code: CodeIntegrityFailure, Submitted: true, Attempt: generation.WriteID, Cause: err}
	}
	round, startSHA, err := c.beginReconciliation(tx, revision, generation)
	if err != nil {
		return PRLifecycleResultV1{}, err
	}
	principal, principalErr := c.github.principal(ctx)
	expectedID, _ := trackActorID(authority.Actor())
	exactClass := ""
	if principalErr == nil && principal.ID != expectedID {
		principalErr = errors.New("reconciliation principal differs from authority")
		exactClass = "DIVERGED_AUTHENTICATED_PRINCIPAL"
	}
	var candidate *discoveryCandidate
	var discoveryErr error
	prNumber := revision.PRNumber
	if prNumber == 0 {
		items, _, readErr := c.github.discover(ctx, authority.Repository(), authority.BaseBranch(), authority.HeadBranch())
		if readErr != nil || len(items) != 1 {
			discoveryErr = firstError(readErr, errors.New("post-write PR identity is not unique"))
		} else {
			candidate = &items[0]
			prNumber = items[0].Number
		}
	}
	var pr RemotePRObservation
	var prErr error
	if prNumber > 0 {
		pr, prErr = c.github.pullRequest(ctx, authority.Repository(), prNumber)
	}
	head, headErr := c.github.ref(ctx, authority.Repository(), authority.HeadBranch())
	base, baseErr := c.github.ref(ctx, authority.Repository(), authority.BaseBranch())
	postAuthority := authority
	identityErr := error(nil)
	if candidate != nil && prErr == nil && (pr.Number != candidate.Number || pr.NodeID != candidate.NodeID) {
		identityErr = errors.New("CREATE discovery and full PR identities differ")
		exactClass = "DIVERGED_CREATE_LIST_FULL_IDENTITY"
	}
	if _, bound := authority.PullRequest(); !bound && pr.Number > 0 {
		identity, err := githublifecycle.NewPullRequestIdentity(pr.Number, pr.NodeID)
		if err != nil {
			identityErr = err
		} else {
			data := authority.Input()
			data.PullRequest = &identity
			derived, derivedErr := githublifecycle.NewAuthority(data)
			if derivedErr != nil {
				identityErr = firstError(identityErr, derivedErr)
			} else {
				postAuthority = derived
			}
		}
	}
	exactErr := firstError(principalErr, discoveryErr, prErr, headErr, baseErr, identityErr)
	var snapshot githublifecycle.PullRequestSnapshot
	if exactErr == nil {
		snapshot, exactErr = validateExactPR(postAuthority, pr, head, base, c.github.limits)
		if exactErr != nil {
			exactClass = classifyExactDivergence(postAuthority, pr, head, base)
		}
	}
	if exactErr == nil && (pr.Title != revision.Title || pr.Body != revision.Body) {
		exactErr = errors.New("postflight title/body differs from immutable desired document")
		exactClass = "DIVERGED_DESIRED_DOCUMENT"
	}
	if exactErr == nil && revision.Mode == "CREATE" && (pr.AuthorID != principal.ID || pr.AuthorNodeID != principal.NodeID) {
		exactErr = errors.New("created PR author differs from authenticated principal")
		exactClass = "DIVERGED_CREATE_AUTHOR"
	}
	if exactErr == nil {
		writeResult, err := githublifecycle.NewPullRequestWriteResult(input, snapshot, c.github.limits)
		if err == nil {
			err = githublifecycle.ValidatePullRequestWriteResult(input, writeResult, c.github.limits)
		}
		if err != nil {
			exactErr = err
			exactClass = "DIVERGED_WRITE_RESULT_VALIDATION"
		}
	}
	if exactErr != nil && discoveryErr == nil && prErr == nil && headErr == nil && baseErr == nil {
		if exactClass == "" {
			exactClass = "DIVERGED_EXACT_POSTFLIGHT"
		}
		exactErr = &Error{Code: exactClass, Cause: errors.New("bounded exact-postflight validation failed")}
	}
	outcome := "exact_applied"
	if exactErr != nil {
		outcome = boundedError(exactErr)
	}
	observation := reconciliationObservationV1{SchemaVersion, round, startSHA, outcome, principal, candidate, pr, head, base, c.now().UTC().UnixNano()}
	observationBytes, _ := json.Marshal(observation)
	if err := c.checkReconciliationAggregate(tx, revision.Ordinal, int64(len(observationBytes))); err != nil {
		return PRLifecycleResultV1{}, err
	}
	observationName := recordPrefix(tx.key, revision.Ordinal) + "reconcile-" + strconv.Itoa(round) + "-observation.json"
	if _, _, err := tx.create(observationName, observationBytes, false, false); err != nil {
		return PRLifecycleResultV1{}, err
	}
	postRef, err := publishOrVerify(c.artifacts, evidenceName(request.RunID, tx.key, revision.Ordinal, round, "reconcile"), "pr-lifecycle-reconciliation", observationBytes)
	if err != nil {
		return PRLifecycleResultV1{}, &Error{Submitted: true, Attempt: generation.WriteID, Cause: err}
	}
	refs = append(refs, postRef)
	if discoveryErr != nil || prErr != nil || headErr != nil || baseErr != nil {
		return PRLifecycleResultV1{}, &Error{Code: CodeAmbiguousUnresolved, Submitted: true, Attempt: generation.WriteID, Cause: errors.New(boundedError(firstError(discoveryErr, prErr, headErr, baseErr)))}
	}
	disposition := AppliedConfirmed
	if writeErr != nil {
		disposition = AppliedReconciled
	}
	if exactErr != nil {
		disposition = RemoteDivergedAfterWrite
	}
	result, terminalErr := c.terminalize(tx, request, revision, authority, input, generation, genSHA, markerSHA, principal, pr, head, base, snapshot, disposition, observationBytes, refs, exactErr)
	if terminalErr != nil {
		return PRLifecycleResultV1{}, terminalErr
	}
	if exactErr != nil {
		return result, &Error{Code: CodeRemoteDivergedAfterWrite, Submitted: true, Attempt: generation.WriteID, Cause: errors.New(boundedError(exactErr))}
	}
	return result, nil
}

func classifyExactDivergence(authority githublifecycle.Authority, pr RemotePRObservation, head, base RemoteRefObservation) string {
	repository := authority.Repository()
	if pr.State != "open" || pr.Merged {
		return "DIVERGED_PR_STATE"
	}
	if pr.RepositoryOwner != repository.Owner() || pr.RepositoryName != repository.Name() || pr.BaseRepository != repository.String() || pr.HeadRepository != repository.String() {
		return "DIVERGED_REPOSITORY_IDENTITY"
	}
	if pr.BaseRef != authority.BaseBranch().String() || pr.HeadRef != authority.HeadBranch().String() || pr.HeadLabel != repository.Owner()+":"+authority.HeadBranch().String() {
		return "DIVERGED_REF_IDENTITY"
	}
	if pr.HeadSHA != head.SHA || head.SHA != authority.HeadSHA().String() {
		return "DIVERGED_HEAD_SHA"
	}
	if base.SHA != authority.ExpectedBaseTipSHA().String() {
		return "DIVERGED_BASE_SHA"
	}
	if expected, ok := authority.PullRequest(); ok && (pr.Number != expected.Number() || pr.NodeID != expected.NodeID()) {
		return "DIVERGED_PR_IDENTITY"
	}
	return "DIVERGED_EXACT_POSTFLIGHT"
}

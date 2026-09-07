//go:build linux

package prlifecycle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

func (c *Controller) terminalize(tx *resourceTxn, request Request, revision revisionRecord, authority githublifecycle.Authority, input githublifecycle.UpsertPullRequestInput, generation generationRecord, genSHA, markerSHA string, principal RemotePrincipalObservation, pr RemotePRObservation, head, base RemoteRefObservation, snapshot githublifecycle.PullRequestSnapshot, disposition Disposition, reconciliation json.RawMessage, refs []ledger.EvidenceRef, dispositionErr error) (PRLifecycleResultV1, error) {
	var zero PRLifecycleResultV1
	if c.postSubmitFailure != nil {
		if err := c.postSubmitFailure("terminal"); err != nil {
			return zero, err
		}
	}
	var snapshotBytes []byte
	snapshotSHA := ""
	if disposition == AppliedConfirmed || disposition == AppliedReconciled {
		snapshotBytes = snapshot.CanonicalJSON()
		snapshotSHA = snapshot.SHA256()
		if len(snapshotBytes) == 0 || len(snapshotBytes) > MaxTerminalSnapshotBytes || !validDigest(snapshotSHA) {
			return zero, &Error{Code: CodeIntegrityFailure, Submitted: true, Attempt: generation.WriteID, Cause: errors.New("successful snapshot cannot fit terminal profile")}
		}
	} else if disposition != RemoteDivergedAfterWrite {
		return zero, &Error{Code: CodeIntegrityFailure, Submitted: true, Attempt: generation.WriteID, Cause: errors.New("unknown terminal disposition")}
	}
	prSHA, err := observationDigest(pr, 32<<10)
	if err != nil {
		return zero, submittedControllerError(err, generation.WriteID)
	}
	headSHA, err := observationDigest(head, 8<<10)
	if err != nil {
		return zero, submittedControllerError(err, generation.WriteID)
	}
	baseSHA, err := observationDigest(base, 8<<10)
	if err != nil {
		return zero, submittedControllerError(err, generation.WriteID)
	}
	if _, err := observationDigest(principal, 8<<10); err != nil {
		return zero, submittedControllerError(err, generation.WriteID)
	}
	reconciliationSHA := ""
	if len(reconciliation) > 0 {
		if len(reconciliation) > 32<<10 || !json.Valid(reconciliation) {
			return zero, submittedControllerError(errors.New("invalid reconciliation terminal material"), generation.WriteID)
		}
		reconciliationSHA = digestBytes(reconciliation)
	}
	authorityPrincipalID, err := trackActorID(authority.Actor())
	if err != nil {
		return zero, submittedControllerError(err, generation.WriteID)
	}
	principalNodeID, principalLogin := "", ""
	if principal.ID == authorityPrincipalID {
		principalNodeID, principalLogin = principal.NodeID, principal.Login
	}
	core := PRLifecycleResultCoreV1{
		SchemaVersion: SchemaVersion, Disposition: disposition, ResourceKey: tx.key.String(), Revision: revision.Ordinal, Generation: 1,
		WriteID: generation.WriteID, Repository: authority.Repository().String(), BaseBranch: authority.BaseBranch().String(), HeadBranch: authority.HeadBranch().String(),
		HeadSHA: authority.HeadSHA().String(), ExpectedBaseTipSHA: authority.ExpectedBaseTipSHA().String(), PRNumber: pr.Number, PRNodeID: pr.NodeID,
		DocumentSHA256: revision.DocumentSHA256, PrincipalID: authorityPrincipalID, PrincipalNodeID: principalNodeID, PrincipalLogin: principalLogin,
		SnapshotSHA256: snapshotSHA, PRObservationSHA: prSHA, HeadObservationSHA: headSHA, BaseObservationSHA: baseSHA, ReconciliationSHA: reconciliationSHA,
	}
	coreBytes, err := core.CanonicalJSON()
	if err != nil || len(coreBytes) > 16<<10 {
		return zero, submittedControllerError(firstError(err, errors.New("result core exceeds terminal profile")), generation.WriteID)
	}
	coreSHA := digestBytes(coreBytes)
	writeAuthority := input.Authority()
	authorityBytes, _ := writeAuthority.CanonicalJSON()
	attemptBytes, _ := input.Attempt().CanonicalJSON()
	// The normalized reconciliation observation is sufficient to reconstruct
	// current-run evidence. Do not duplicate earlier preflight evidence inside
	// the terminal.
	terminalRefs := refs
	if len(terminalRefs) > 1 {
		terminalRefs = terminalRefs[len(terminalRefs)-1:]
	}
	if c.postSubmitFailure != nil {
		if err := c.postSubmitFailure("evidence"); err != nil {
			return zero, err
		}
	}
	artifacts, err := captureArtifacts(c.artifacts, terminalRefs)
	if err != nil {
		return zero, submittedControllerError(err, generation.WriteID)
	}
	policySHA, _ := c.store.policy.SHA256()
	limitsSHA, _ := c.github.limits.SHA256()
	var recovery snapshotRecoveryWire
	if len(snapshotBytes) > 0 {
		recovery = snapshotRecoveryWire{
			Provider: "github", RequestID: snapshot.Input().Snapshot.RequestID(), ObservedAt: snapshot.Input().Snapshot.ObservedUnixNano(),
			RepositoryOwn: authority.Repository().Owner(), Repository: authority.Repository().Name(), PRNumber: pr.Number, PRNodeID: pr.NodeID,
			BaseBranch: authority.BaseBranch().String(), BaseTipSHA: base.SHA, HeadBranch: authority.HeadBranch().String(), HeadSHA: head.SHA, State: string(snapshot.Input().State),
		}
	}
	reason, failureCode := string(disposition), ""
	if dispositionErr != nil {
		reason, failureCode = boundedError(dispositionErr), CodeRemoteDivergedAfterWrite
	}
	terminalCore := terminalCoreV1{
		SchemaVersion: SchemaVersion, PolicySHA256: policySHA, LimitsSHA256: limitsSHA, ResourceKey: tx.key.String(), Revision: revision.Ordinal, Generation: 1,
		GenerationSHA256: genSHA, SubmittedSHA256: markerSHA, Attempt: attemptBytes, AttemptSHA256: digestBytes(attemptBytes),
		SourceAuthority: revision.SourceAuthority, SourceAuthoritySHA: revision.SourceSHA256, DerivedAuthority: authorityBytes, DerivedAuthoritySHA: revision.AuthoritySHA256,
		ExpectedContentSHA: writeAuthority.ExpectedContent().SHA256(), Title: revision.Title, Body: revision.Body, DocumentSHA256: revision.DocumentSHA256,
		Snapshot: snapshotBytes, SnapshotSHA256: snapshotSHA, SnapshotRecovery: recovery, Principal: principal, PullRequest: pr, Head: head, Base: base,
		Reconciliation: reconciliation, ResultCore: core, ResultCoreSHA256: coreSHA, Reason: reason, FailureCode: failureCode, RunID: request.RunID,
		TerminalUnixNano: c.now().UTC().UnixNano(), EvidenceRefs: append([]ledger.EvidenceRef(nil), terminalRefs...), EvidenceArtifacts: artifacts,
	}
	terminalCoreBytes, err := json.Marshal(terminalCore)
	if err != nil {
		return zero, submittedControllerError(err, generation.WriteID)
	}
	terminalCoreSHA := digestBytes(terminalCoreBytes)
	event, eventBytes, err := deterministicTerminalEvent(terminalCore)
	if err != nil {
		return zero, submittedControllerError(err, generation.WriteID)
	}
	terminal := terminalV1{terminalCore, terminalCoreSHA, eventBytes, digestBytes(eventBytes)}
	terminalBytes, err := json.Marshal(terminal)
	budget, budgetErr := NewTerminalBudget(authorityBytes, attemptBytes, revision.Title, revision.Body)
	if err != nil || budgetErr != nil || len(terminalBytes) > budget.WorstCaseBytes || len(terminalBytes) > MaxTerminalBytes {
		return zero, &Error{Code: CodeIntegrityFailure, Submitted: true, Attempt: generation.WriteID, Cause: errors.New("final terminal exceeds 256-KiB cap")}
	}
	terminalName := recordPrefix(tx.key, revision.Ordinal) + "terminal.json"
	if c.postSubmitFailure != nil {
		if err := c.postSubmitFailure("admission"); err != nil {
			return zero, err
		}
	}
	stored, terminalSHA, err := tx.create(terminalName, terminalBytes, false, true)
	if err != nil {
		return zero, submittedControllerError(err, generation.WriteID)
	}
	if !bytes.Equal(stored, terminalBytes) {
		return zero, submittedControllerError(errors.New(CodeIntegrityFailure), generation.WriteID)
	}
	if c.postSubmitFailure != nil {
		if err := c.postSubmitFailure("ledger"); err != nil {
			return zero, err
		}
	}
	if err := c.ledger.Record(event, eventBytes); err != nil {
		return zero, submittedControllerError(err, generation.WriteID)
	}
	return PRLifecycleResultV1{core: core, terminalSHA: terminalSHA, evidenceRefs: append([]ledger.EvidenceRef(nil), terminalRefs...)}, nil
}

func deterministicTerminalEvent(core terminalCoreV1) (ledger.Event, []byte, error) {
	b, err := json.Marshal(core)
	if err != nil {
		return ledger.Event{}, nil, err
	}
	coreSHA := digestBytes(b)
	idHash := sha256.Sum256(append([]byte("pr-lifecycle-terminal-event-v1"), []byte(coreSHA)...))
	event := ledger.Event{
		SchemaVersion: ledger.CurrentSchemaVersion, EventID: hex.EncodeToString(idHash[:]), Timestamp: time.Unix(0, core.TerminalUnixNano).UTC(),
		RunID: core.RunID, AttemptID: core.ResultCore.WriteID, EventType: "pr_lifecycle_terminal", Actor: "controller", Source: "prlifecycle",
		Payload: map[string]any{"terminal_core_sha256": coreSHA, "resource_key": core.ResourceKey, "revision": core.Revision, "generation": core.Generation, "disposition": core.ResultCore.Disposition},
	}
	if err := event.Validate(); err != nil {
		return ledger.Event{}, nil, err
	}
	eventBytes, err := json.Marshal(event)
	return event, eventBytes, err
}

func (c *Controller) recoverTerminal(tx *resourceTxn, name, runID string) (PRLifecycleResultV1, error) {
	var zero PRLifecycleResultV1
	b, err := tx.read(name, MaxTerminalBytes)
	if err != nil {
		return zero, err
	}
	var terminal terminalV1
	if err := strictJSON(b, &terminal); err != nil {
		return zero, &Error{Code: CodeIntegrityFailure, Cause: err}
	}
	canonicalTerminal, err := json.Marshal(terminal)
	if err != nil || !bytes.Equal(canonicalTerminal, b) {
		return zero, &Error{Code: CodeIntegrityFailure, Cause: errors.New("terminal is not canonical JSON")}
	}
	coreBytes, _ := json.Marshal(terminal.Core)
	if digestBytes(coreBytes) != terminal.TerminalCoreSHA256 || terminal.Core.RunID == "" || terminal.Core.ResourceKey != tx.key.String() {
		return zero, &Error{Code: CodeIntegrityFailure, Cause: errors.New("terminal core identity mismatch")}
	}
	policySHA, _ := c.store.policy.SHA256()
	limitsSHA, _ := c.github.limits.SHA256()
	if terminal.Core.PolicySHA256 != policySHA || terminal.Core.LimitsSHA256 != limitsSHA {
		return zero, &Error{Code: CodePolicyMismatch, Cause: errors.New("terminal policy identity mismatch")}
	}
	generation, generationSHA, submittedSHA, err := readGenerationAndMarker(tx, terminal.Core.Revision)
	if err != nil || generationSHA != terminal.Core.GenerationSHA256 || submittedSHA != terminal.Core.SubmittedSHA256 || generation.WriteID != terminal.Core.ResultCore.WriteID {
		return zero, &Error{Code: CodeIntegrityFailure, Cause: firstError(err, errors.New("terminal admission-chain mismatch"))}
	}
	revision, err := readRevision(tx, terminal.Core.Revision)
	if err != nil || revision.SourceSHA256 != terminal.Core.SourceAuthoritySHA || revision.AuthoritySHA256 != terminal.Core.DerivedAuthoritySHA || revision.DocumentSHA256 != terminal.Core.DocumentSHA256 ||
		generation.AuthoritySHA256 != terminal.Core.DerivedAuthoritySHA || generation.AttemptSHA256 != terminal.Core.AttemptSHA256 || !bytes.Equal(generation.Attempt, terminal.Core.Attempt) {
		return zero, &Error{Code: CodeIntegrityFailure, Cause: firstError(err, errors.New("terminal revision/generation binding mismatch"))}
	}
	if err := validateCanonicalAuthority(terminal.Core.SourceAuthority, terminal.Core.SourceAuthoritySHA); err != nil {
		return zero, err
	}
	if err := validateCanonicalAuthority(terminal.Core.DerivedAuthority, terminal.Core.DerivedAuthoritySHA); err != nil {
		return zero, err
	}
	var authority authorityMirror
	if err := strictJSON(terminal.Core.DerivedAuthority, &authority); err != nil || authority.Repository.Owner+"/"+authority.Repository.Name != terminal.Core.ResultCore.Repository ||
		authority.BaseBranch != terminal.Core.ResultCore.BaseBranch || authority.HeadBranch != terminal.Core.ResultCore.HeadBranch || authority.HeadSHA != terminal.Core.ResultCore.HeadSHA ||
		authority.ExpectedBaseTipSHA != terminal.Core.ResultCore.ExpectedBaseTipSHA || authority.Actor.Subject != "github-user-id:"+strconv.FormatInt(terminal.Core.ResultCore.PrincipalID, 10) ||
		digestBytes(authority.ExpectedContent) != terminal.Core.ExpectedContentSHA {
		return zero, &Error{Code: CodeIntegrityFailure, Cause: firstError(err, errors.New("mirrored authority fields differ from result"))}
	}
	var attempt struct {
		Repository struct {
			Owner string `json:"owner"`
			Name  string `json:"name"`
		} `json:"repository"`
		Actor struct {
			Kind           string `json:"kind"`
			Subject        string `json:"subject"`
			InstallationID int64  `json:"installation_id,omitempty"`
		} `json:"actor"`
		Operation       string `json:"operation_kind"`
		WriteID         string `json:"write_id"`
		AuthoritySHA256 string `json:"authority_sha256"`
		PayloadSHA256   string `json:"canonical_payload_sha256"`
		LimitsSHA256    string `json:"limits_sha256"`
	}
	if err := strictJSON(terminal.Core.Attempt, &attempt); err != nil || attempt.Operation != "pr_upsert" || attempt.WriteID != terminal.Core.ResultCore.WriteID ||
		attempt.AuthoritySHA256 != terminal.Core.DerivedAuthoritySHA || attempt.PayloadSHA256 != generation.PayloadSHA256 || attempt.LimitsSHA256 != limitsSHA ||
		attempt.Repository.Owner+"/"+attempt.Repository.Name != terminal.Core.ResultCore.Repository || attempt.Actor.Subject != authority.Actor.Subject {
		return zero, &Error{Code: CodeIntegrityFailure, Cause: firstError(err, errors.New("write-attempt primitive fields mismatch"))}
	}
	if digestBytes(terminal.Core.Attempt) != terminal.Core.AttemptSHA256 || documentDigest(terminal.Core.Title, terminal.Core.Body) != terminal.Core.DocumentSHA256 {
		return zero, &Error{Code: CodeIntegrityFailure, Cause: errors.New("terminal attempt, document, or snapshot digest mismatch")}
	}
	if terminal.Core.ResultCore.Disposition == RemoteDivergedAfterWrite {
		snapshotAbsent := len(terminal.Core.Snapshot) == 0 || bytes.Equal(terminal.Core.Snapshot, []byte("null"))
		if snapshotAbsent && terminal.Core.SnapshotSHA256 != "" || !snapshotAbsent && digestBytes(terminal.Core.Snapshot) != terminal.Core.SnapshotSHA256 {
			return zero, &Error{Code: CodeIntegrityFailure, Cause: errors.New("optional divergence snapshot digest mismatch")}
		}
	} else if len(terminal.Core.Snapshot) == 0 || digestBytes(terminal.Core.Snapshot) != terminal.Core.SnapshotSHA256 {
		return zero, &Error{Code: CodeIntegrityFailure, Cause: errors.New("successful terminal snapshot digest mismatch")}
	}
	authorityPrincipalID, principalIDErr := strconv.ParseInt(strings.TrimPrefix(authority.Actor.Subject, "github-user-id:"), 10, 64)
	principalBindingValid := principalIDErr == nil && authorityPrincipalID == terminal.Core.ResultCore.PrincipalID
	if terminal.Core.Principal.ID == authorityPrincipalID {
		principalBindingValid = principalBindingValid && terminal.Core.ResultCore.PrincipalNodeID == terminal.Core.Principal.NodeID && terminal.Core.ResultCore.PrincipalLogin == terminal.Core.Principal.Login
	} else {
		principalBindingValid = principalBindingValid && terminal.Core.ResultCore.Disposition == RemoteDivergedAfterWrite && terminal.Core.ResultCore.PrincipalNodeID == "" && terminal.Core.ResultCore.PrincipalLogin == ""
	}
	if terminal.Core.ResultCore.ResourceKey != terminal.Core.ResourceKey || terminal.Core.ResultCore.Revision != terminal.Core.Revision || terminal.Core.ResultCore.Generation != terminal.Core.Generation ||
		terminal.Core.ResultCore.DocumentSHA256 != terminal.Core.DocumentSHA256 || terminal.Core.ResultCore.PRNumber != terminal.Core.PullRequest.Number || terminal.Core.ResultCore.PRNodeID != terminal.Core.PullRequest.NodeID ||
		!principalBindingValid {
		return zero, &Error{Code: CodeIntegrityFailure, Cause: errors.New("terminal primitive result bindings mismatch")}
	}
	if len(terminal.Core.Reconciliation) == 0 && terminal.Core.ResultCore.ReconciliationSHA != "" || len(terminal.Core.Reconciliation) > 0 && digestBytes(terminal.Core.Reconciliation) != terminal.Core.ResultCore.ReconciliationSHA {
		return zero, &Error{Code: CodeIntegrityFailure, Cause: errors.New("terminal reconciliation binding mismatch")}
	}
	if len(terminal.Core.Snapshot) > 0 && !bytes.Equal(terminal.Core.Snapshot, []byte("null")) {
		snapshot, rebuildErr := rebuildSnapshot(terminal.Core.SnapshotRecovery, c.github.limits)
		if rebuildErr != nil || !bytes.Equal(snapshot.CanonicalJSON(), terminal.Core.Snapshot) || snapshot.SHA256() != terminal.Core.SnapshotSHA256 {
			return zero, &Error{Code: CodeIntegrityFailure, Cause: firstError(rebuildErr, errors.New("primitive snapshot recovery mismatch"))}
		}
	}
	resultBytes, err := terminal.Core.ResultCore.CanonicalJSON()
	if err != nil || digestBytes(resultBytes) != terminal.Core.ResultCoreSHA256 {
		return zero, &Error{Code: CodeIntegrityFailure, Cause: errors.New("stored result core mismatch")}
	}
	if prSHA, _ := observationDigest(terminal.Core.PullRequest, 32<<10); prSHA != terminal.Core.ResultCore.PRObservationSHA {
		return zero, &Error{Code: CodeIntegrityFailure, Cause: errors.New("stored PR observation mismatch")}
	}
	if headSHA, _ := observationDigest(terminal.Core.Head, 8<<10); headSHA != terminal.Core.ResultCore.HeadObservationSHA {
		return zero, &Error{Code: CodeIntegrityFailure, Cause: errors.New("stored head observation mismatch")}
	}
	if baseSHA, _ := observationDigest(terminal.Core.Base, 8<<10); baseSHA != terminal.Core.ResultCore.BaseObservationSHA {
		return zero, &Error{Code: CodeIntegrityFailure, Cause: errors.New("stored base observation mismatch")}
	}
	event, eventBytes, err := deterministicTerminalEvent(terminal.Core)
	if err != nil || !bytes.Equal(eventBytes, terminal.MaterialEvent) || digestBytes(eventBytes) != terminal.MaterialEventSHA {
		return zero, &Error{Code: CodeIntegrityFailure, Cause: errors.New("deterministic material event mismatch")}
	}
	refs := make([]ledger.EvidenceRef, 0, len(terminal.Core.EvidenceArtifacts))
	for i, artifact := range terminal.Core.EvidenceArtifacts {
		if digestBytes(artifact.Bytes) != artifact.SHA256 {
			return zero, &Error{Code: CodeIntegrityFailure, Cause: errors.New("terminal evidence artifact mismatch")}
		}
		if i >= len(terminal.Core.EvidenceRefs) || filepath.Base(terminal.Core.EvidenceRefs[i].URI) != artifact.Name || terminal.Core.EvidenceRefs[i].Kind != artifact.Kind || terminal.Core.EvidenceRefs[i].SHA256 != artifact.SHA256 || !strings.HasPrefix(artifact.Name, terminal.Core.RunID+"-") {
			return zero, &Error{Code: CodeIntegrityFailure, Cause: errors.New("terminal evidence provenance differs")}
		}
		currentName := runID + strings.TrimPrefix(artifact.Name, terminal.Core.RunID)
		ref, err := publishOrVerify(c.artifacts, currentName, artifact.Kind, artifact.Bytes)
		if err != nil {
			return zero, err
		}
		refs = append(refs, ref)
	}
	if len(refs) != len(terminal.Core.EvidenceRefs) {
		return zero, &Error{Code: CodeIntegrityFailure, Cause: errors.New("recovered evidence count differs")}
	}
	if err := c.ledger.Record(event, eventBytes); err != nil {
		return zero, err
	}
	result := PRLifecycleResultV1{core: terminal.Core.ResultCore, terminalSHA: digestBytes(b), evidenceRefs: refs}
	if terminal.Core.ResultCore.Disposition == RemoteDivergedAfterWrite {
		return result, &Error{Code: CodeRemoteDivergedAfterWrite, Submitted: true, Attempt: terminal.Core.ResultCore.WriteID, Cause: errors.New("durable unresolved divergence")}
	}
	return result, nil
}

type authorityMirror struct {
	Repository struct {
		Owner string `json:"owner"`
		Name  string `json:"name"`
	} `json:"repository"`
	BaseBranch         string `json:"base_branch"`
	HeadBranch         string `json:"head_branch"`
	HeadSHA            string `json:"head_sha"`
	ExpectedBaseTipSHA string `json:"expected_base_tip_sha"`
	PullRequest        *struct {
		Number int64  `json:"number"`
		NodeID string `json:"node_id"`
	} `json:"pull_request,omitempty"`
	AllowedMergeMethod string `json:"allowed_merge_method"`
	Actor              struct {
		Kind           string `json:"kind"`
		Subject        string `json:"subject"`
		InstallationID int64  `json:"installation_id,omitempty"`
	} `json:"actor"`
	ExpectedContent json.RawMessage `json:"expected_merge_content"`
}

func validateCanonicalAuthority(raw json.RawMessage, expectedSHA string) error {
	if len(raw) == 0 || len(raw) > 32<<10 || digestBytes(raw) != expectedSHA {
		return &Error{Code: CodeIntegrityFailure, Cause: errors.New("authority bytes or digest invalid")}
	}
	var mirror authorityMirror
	if err := strictJSON(raw, &mirror); err != nil {
		return &Error{Code: CodeIntegrityFailure, Cause: err}
	}
	reencoded, err := json.Marshal(mirror)
	if err != nil || !bytes.Equal(reencoded, raw) || !json.Valid(mirror.ExpectedContent) {
		return &Error{Code: CodeIntegrityFailure, Cause: errors.New("authority is not strict canonical mirror")}
	}
	if _, err := githublifecycle.NewRepository(mirror.Repository.Owner, mirror.Repository.Name); err != nil {
		return &Error{Code: CodeIntegrityFailure, Cause: err}
	}
	if _, err := githublifecycle.NewBranch(mirror.BaseBranch); err != nil {
		return &Error{Code: CodeIntegrityFailure, Cause: err}
	}
	if _, err := githublifecycle.NewBranch(mirror.HeadBranch); err != nil {
		return &Error{Code: CodeIntegrityFailure, Cause: err}
	}
	if _, err := githublifecycle.NewGitSHA(mirror.HeadSHA); err != nil {
		return &Error{Code: CodeIntegrityFailure, Cause: err}
	}
	if _, err := githublifecycle.NewGitSHA(mirror.ExpectedBaseTipSHA); err != nil {
		return &Error{Code: CodeIntegrityFailure, Cause: err}
	}
	return nil
}

func rebuildSnapshot(w snapshotRecoveryWire, limits githublifecycle.Limits) (githublifecycle.PullRequestSnapshot, error) {
	repository, err := githublifecycle.NewRepository(w.RepositoryOwn, w.Repository)
	if err != nil {
		return githublifecycle.PullRequestSnapshot{}, err
	}
	pr, err := githublifecycle.NewPullRequestIdentity(w.PRNumber, w.PRNodeID)
	if err != nil {
		return githublifecycle.PullRequestSnapshot{}, err
	}
	base, err := githublifecycle.NewBranch(w.BaseBranch)
	if err != nil {
		return githublifecycle.PullRequestSnapshot{}, err
	}
	head, err := githublifecycle.NewBranch(w.HeadBranch)
	if err != nil {
		return githublifecycle.PullRequestSnapshot{}, err
	}
	baseSHA, err := githublifecycle.NewGitSHA(w.BaseTipSHA)
	if err != nil {
		return githublifecycle.PullRequestSnapshot{}, err
	}
	headSHA, err := githublifecycle.NewGitSHA(w.HeadSHA)
	if err != nil {
		return githublifecycle.PullRequestSnapshot{}, err
	}
	identity, err := githublifecycle.NewSnapshotIdentity(w.Provider, w.RequestID, w.ObservedAt)
	if err != nil {
		return githublifecycle.PullRequestSnapshot{}, err
	}
	return githublifecycle.NewPullRequestSnapshot(githublifecycle.PullRequestSnapshotInput{
		Snapshot: identity, Repository: repository, PullRequest: pr, BaseBranch: base, BaseTipSHA: baseSHA,
		HeadBranch: head, HeadSHA: headSHA, State: githublifecycle.PullRequestState(w.State), Reviews: []githublifecycle.Review{}, EvidenceRefs: []ledger.EvidenceRef{}, Metadata: map[string]string{},
	}, limits)
}

func captureArtifacts(writer ArtifactWriter, refs []ledger.EvidenceRef) ([]terminalArtifact, error) {
	artifacts := make([]terminalArtifact, 0, len(refs))
	for _, ref := range refs {
		if filepath.Dir(ref.URI) != writer.RunDir() || filepath.Base(ref.URI) == "." || ref.SHA256 == "" || ref.Kind == "" {
			return nil, errors.New("evidence reference escapes controller run directory")
		}
		b, err := evidence.ReadVerifiedLocal(writer.RunDir(), ref, MaxTerminalArtifactBytes)
		if err != nil || !json.Valid(b) {
			return nil, errors.New("evidence artifact is unavailable, oversized, or mismatched")
		}
		artifacts = append(artifacts, terminalArtifact{filepath.Base(ref.URI), ref.Kind, b, ref.SHA256})
	}
	return artifacts, nil
}

func equalEvidence(a, b []ledger.EvidenceRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func readTerminal(tx *resourceTxn, name string) (terminalV1, error) {
	var value terminalV1
	b, err := tx.read(name, MaxTerminalBytes)
	if err != nil {
		return value, err
	}
	if err := strictJSON(b, &value); err != nil {
		return value, &Error{Code: CodeIntegrityFailure, Cause: err}
	}
	canonical, err := json.Marshal(value)
	coreBytes, coreErr := json.Marshal(value.Core)
	event, eventBytes, eventErr := deterministicTerminalEvent(value.Core)
	_ = event
	if err != nil || coreErr != nil || eventErr != nil || !bytes.Equal(canonical, b) || digestBytes(coreBytes) != value.TerminalCoreSHA256 ||
		!bytes.Equal(eventBytes, value.MaterialEvent) || digestBytes(eventBytes) != value.MaterialEventSHA {
		return value, &Error{Code: CodeIntegrityFailure, Cause: errors.New("terminal canonical hash chain mismatch")}
	}
	return value, nil
}

func readRevision(tx *resourceTxn, ordinal uint64) (revisionRecord, error) {
	var value revisionRecord
	b, err := tx.read(recordPrefix(tx.key, ordinal)+"revision.json", 128<<10)
	if err != nil {
		return value, err
	}
	if err := strictJSON(b, &value); err != nil || value.SchemaVersion != SchemaVersion || value.ResourceKey != tx.key.String() || value.Ordinal != ordinal {
		return value, &Error{Code: CodeIntegrityFailure, Cause: firstError(err, errors.New("revision identity mismatch"))}
	}
	return value, nil
}

func readGenerationAndMarker(tx *resourceTxn, revision uint64) (generationRecord, string, string, error) {
	generation, generationSHA, err := readGeneration(tx, revision)
	if err != nil {
		return generation, "", "", err
	}
	mb, err := tx.read(recordPrefix(tx.key, revision)+"submitted.json", 32<<10)
	if err != nil {
		return generation, "", "", err
	}
	var marker submittedRecord
	if err := strictJSON(mb, &marker); err != nil || marker.GenerationSHA != generationSHA || marker.WriteID != generation.WriteID || marker.Reservation != MaxTerminalBytes {
		return generation, "", "", &Error{Code: CodeIntegrityFailure, Cause: firstError(err, errors.New("submitted marker mismatch"))}
	}
	return generation, generationSHA, digestBytes(mb), nil
}

func readGeneration(tx *resourceTxn, revision uint64) (generationRecord, string, error) {
	var generation generationRecord
	gb, err := tx.read(recordPrefix(tx.key, revision)+"generation.json", 128<<10)
	if err != nil {
		return generation, "", err
	}
	if err := strictJSON(gb, &generation); err != nil || generation.Revision != revision || generation.Generation != 1 || generation.ResourceKey != tx.key.String() ||
		generation.AttemptSHA256 != digestBytes(generation.Attempt) || !validDigest(generation.AuthoritySHA256) || !validDigest(generation.PayloadSHA256) || !validDigest(generation.PrepareSHA256) {
		return generation, "", &Error{Code: CodeIntegrityFailure, Cause: firstError(err, errors.New("generation mismatch"))}
	}
	canonical, _ := json.Marshal(generation)
	if !bytes.Equal(canonical, gb) {
		return generation, "", &Error{Code: CodeIntegrityFailure, Cause: errors.New("generation is not canonical")}
	}
	return generation, digestBytes(gb), nil
}

func latestRevision(store *PRWriteAdmissionStore, key PRResourceKeyV1) (uint64, error) {
	entries, err := store.readDir()
	if err != nil {
		return 0, err
	}
	prefix, suffix := "r-"+key.String()+"-rev-", "-revision.json"
	var latest uint64
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, suffix) {
			ordinal, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(name, prefix), suffix), 10, 64)
			if err != nil || ordinal == 0 {
				return 0, errors.New(CodeIntegrityFailure + ": malformed revision name")
			}
			if ordinal > latest {
				latest = ordinal
			}
		}
	}
	return latest, nil
}

func nextLegacyRound(store *PRWriteAdmissionStore, key PRResourceKeyV1, revision uint64, kind string, maximum int) int {
	entries, err := store.readDir()
	if err != nil {
		return 0
	}
	prefix := recordPrefix(key, revision) + kind + "-"
	latest := 0
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".json") {
			n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".json"))
			if n > latest {
				latest = n
			}
		}
	}
	if latest >= maximum {
		return 0
	}
	return latest + 1
}

func prepareRecordName(key PRResourceKeyV1, revision uint64, runSHA string, round int) string {
	return recordPrefix(key, revision) + "prepare-run-" + runSHA + "-" + strconv.Itoa(round) + ".json"
}

func nextPrepareRound(tx *resourceTxn, runID string, revision uint64, maximum int) (int, error) {
	if tx == nil || runID == "" || maximum <= 0 || maximum > 3 {
		return 0, &Error{Code: CodeIntegrityFailure, Cause: errors.New("invalid prepare-round request")}
	}
	entries, err := tx.store.readDir()
	if err != nil {
		return 0, &Error{Code: CodeIntegrityFailure, Cause: errors.New("prepare history inventory failed")}
	}
	prefix := recordPrefix(tx.key, revision) + "prepare-run-"
	type runRounds map[int]bool
	runs := make(map[string]runRounds)
	total := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		match := admissionName.FindStringSubmatch(name)
		if match == nil || match[1] != tx.key.String() || match[2] != strconv.FormatUint(revision, 10) {
			return 0, &Error{Code: CodeIntegrityFailure, Cause: errors.New("malformed prepare history name")}
		}
		rest := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".json")
		separator := strings.LastIndexByte(rest, '-')
		if separator != 64 || len(rest) != 66 {
			return 0, &Error{Code: CodeIntegrityFailure, Cause: errors.New("malformed prepare history grammar")}
		}
		runSHA := rest[:separator]
		round, parseErr := strconv.Atoi(rest[separator+1:])
		if !validDigest(runSHA) || parseErr != nil || round < 1 || round > maximum {
			return 0, &Error{Code: CodeIntegrityFailure, Cause: errors.New("malformed prepare history identity")}
		}
		data, readErr := tx.read(name, MaxPrepareRecordBytes)
		var record prepareRecordV1
		if readErr != nil || strictJSON(data, &record) != nil {
			return 0, &Error{Code: CodeIntegrityFailure, Cause: errors.New("prepare history record is unreadable")}
		}
		canonical, marshalErr := json.Marshal(record)
		if marshalErr != nil || !bytes.Equal(canonical, data) || record.SchemaVersion != SchemaVersion || record.RunID == "" || record.RunSHA256 != runSHA || digestBytes([]byte(record.RunID)) != runSHA || record.ResourceKey != tx.key.String() || record.Revision != revision || record.Round != round || record.Outcome == "" || record.Outcome == "prepared" && (len(record.Proof) == 0 || !json.Valid(record.Proof)) || record.Outcome != "prepared" && len(record.Proof) != 0 {
			return 0, &Error{Code: CodeIntegrityFailure, Cause: errors.New("prepare history record binding differs")}
		}
		if runs[runSHA] == nil {
			runs[runSHA] = make(runRounds)
		}
		if runs[runSHA][round] {
			return 0, &Error{Code: CodeIntegrityFailure, Cause: errors.New("duplicate prepare history round")}
		}
		runs[runSHA][round] = true
		total++
	}
	for _, rounds := range runs {
		for round := 1; round <= len(rounds); round++ {
			if !rounds[round] {
				return 0, &Error{Code: CodeIntegrityFailure, Cause: errors.New("prepare history sequence has a gap")}
			}
		}
	}
	if total >= MaxPrepareHistoryRecordsPerPendingRevision {
		return 0, &Error{Code: CodePreflightHistoryExhausted, Cause: errors.New("pending revision prepare history exhausted")}
	}
	runSHA := digestBytes([]byte(runID))
	next := len(runs[runSHA]) + 1
	if next > maximum {
		return 0, &Error{Code: CodePreflightBudgetExhausted, Cause: errors.New("governed run prepare rounds exhausted")}
	}
	return next, nil
}

func submittedControllerError(err error, attempt string) error {
	if err == nil {
		return nil
	}
	code := CodeIntegrityFailure
	var lifecycle *Error
	if errors.As(err, &lifecycle) && lifecycle.Code != "" {
		code = lifecycle.Code
	}
	return &Error{Code: code, Submitted: true, Attempt: attempt, Cause: errors.New("post-submit controller operation failed")}
}

func publishOrVerify(writer ArtifactWriter, name, kind string, data []byte) (ledger.EvidenceRef, error) {
	ref, err := writer.WriteBytes(name, kind, data)
	if err == nil {
		return ref, nil
	}
	if !errors.Is(err, evidence.ErrArtifactExists) {
		return ledger.EvidenceRef{}, err
	}
	path := filepath.Join(writer.RunDir(), name)
	if filepath.Dir(path) != writer.RunDir() {
		return ledger.EvidenceRef{}, errors.New("evidence path escapes run directory")
	}
	expected := ledger.EvidenceRef{URI: path, SHA256: digestBytes(data), Kind: kind}
	existing, readErr := evidence.ReadVerifiedLocal(writer.RunDir(), expected, int64(MaxTerminalArtifactBytes))
	if readErr != nil || !bytes.Equal(existing, data) {
		return ledger.EvidenceRef{}, &Error{Code: CodeIntegrityFailure, Cause: errors.New("immutable evidence name conflicts")}
	}
	return ledger.EvidenceRef{URI: path, SHA256: digestBytes(data), Kind: kind}, nil
}

func evidenceName(runID string, key PRResourceKeyV1, revision uint64, round int, phase string) string {
	return runID + "-" + key.String() + "-r" + strconv.FormatUint(revision, 10) + "-n" + strconv.Itoa(round) + "-" + phase + ".json"
}

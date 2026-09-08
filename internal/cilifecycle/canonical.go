package cilifecycle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type immutableV1[T any] struct {
	data      T
	canonical []byte
	digest    string
}

func newImmutable[T any, W any](data T, wire W) (immutableV1[T], error) {
	b, err := json.Marshal(wire)
	if err != nil {
		return immutableV1[T]{}, err
	}
	d := sha256.Sum256(b)
	return immutableV1[T]{data, b, hex.EncodeToString(d[:])}, nil
}
func (v immutableV1[T]) bytes() []byte { return append([]byte(nil), v.canonical...) }
func (v immutableV1[T]) marshal() ([]byte, error) {
	if len(v.canonical) == 0 {
		return nil, errors.New("uninitialized immutable v1 value")
	}
	return v.bytes(), nil
}

var repoPart = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,98}[A-Za-z0-9_-])?$`)

func validRepositoryComponent(v string) bool { return repoPart.MatchString(v) && v != "." && v != ".." }
func validText(v string, max int) bool       { return v != "" && validOptionalText(v, max) }
func validOptionalText(v string, max int) bool {
	return len(v) <= max && utf8.ValidString(v) && strings.TrimSpace(v) == v && strings.IndexFunc(v, unicode.IsControl) < 0
}
func validOpaque(v string, max int) bool {
	return validText(v, max) && !strings.ContainsAny(v, "/\\?#")
}
func validOptionalString(v *string, max int) bool { return v == nil || validOptionalText(*v, max) }
func cloneString(v *string) *string {
	if v == nil {
		return nil
	}
	copy := *v
	return &copy
}
func validGitSHA(v string) bool { return (len(v) == 40 || len(v) == 64) && isLowerHex(v) }
func isLowerHex(v string) bool {
	if v == "" {
		return false
	}
	for _, c := range v {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
func validDigest(v string) bool         { return len(v) == sha256.Size*2 && isLowerHex(v) }
func validOptionalDigest(v string) bool { return v == "" || validDigest(v) }
func validOptionalToken(v string, max int) bool {
	if v == "" {
		return true
	}
	return len(v) <= max && validText(v, max) && !strings.ContainsAny(v, " \t\r\n")
}
func validTimestamp(v string) bool {
	if len(v) == 0 || len(v) > MaxTimestampBytes {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	return err == nil && !t.IsZero() && t.Location() == time.UTC && t.Format(time.RFC3339Nano) == v
}
func timestampAfter(first, second string) bool {
	a, errA := time.Parse(time.RFC3339Nano, first)
	b, errB := time.Parse(time.RFC3339Nano, second)
	return errA == nil && errB == nil && a.After(b)
}
func validOptionalTimestamp(v *string) bool     { return v == nil || validTimestamp(*v) }
func validOptionalTimestampValue(v string) bool { return v == "" || validTimestamp(v) }
func validPhase(v string) bool                  { return validOptionalToken(v, MaxStateBytes) && v != "" }

func normalizeAndValidateSweep(in *CISemanticSweepV1Input) error {
	sort.Slice(in.CheckSuites, func(i, j int) bool {
		return in.CheckSuites[i].value.data.ProviderID < in.CheckSuites[j].value.data.ProviderID
	})
	sort.Slice(in.CheckRuns, func(i, j int) bool {
		return in.CheckRuns[i].value.data.ProviderID < in.CheckRuns[j].value.data.ProviderID
	})
	sort.Slice(in.CommitStatuses, func(i, j int) bool {
		return in.CommitStatuses[i].value.data.ProviderID < in.CommitStatuses[j].value.data.ProviderID
	})
	suites := make(map[int64]int64, len(in.CheckSuites))
	suiteNodes := map[string]int64{}
	for i, o := range in.CheckSuites {
		d := o.value.data
		if len(o.value.canonical) == 0 || d.HeadSHA != in.HeadSHA {
			return errors.New("suite is invalid or bound to a different head")
		}
		if _, ok := suites[d.ProviderID]; ok {
			return errors.New("duplicate check suite provider ID")
		}
		suites[d.ProviderID] = d.AppID
		if d.ProviderNodeID != "" {
			if old, ok := suiteNodes[d.ProviderNodeID]; ok && old != d.ProviderID {
				return errors.New("conflicting check suite node ID")
			}
			suiteNodes[d.ProviderNodeID] = d.ProviderID
		}
		_ = i
	}
	runIDs := map[int64]struct{}{}
	runNodes := map[string]int64{}
	for _, o := range in.CheckRuns {
		d := o.value.data
		if len(o.value.canonical) == 0 || d.HeadSHA != in.HeadSHA {
			return errors.New("run is invalid or bound to a different head")
		}
		if _, ok := runIDs[d.ProviderID]; ok {
			return errors.New("duplicate check run provider ID")
		}
		runIDs[d.ProviderID] = struct{}{}
		if app, ok := suites[d.CheckSuiteID]; !ok || app != d.AppID {
			return errors.New("check run has unresolved or app-inconsistent suite")
		}
		if d.ProviderNodeID != "" {
			if old, ok := runNodes[d.ProviderNodeID]; ok && old != d.ProviderID {
				return errors.New("conflicting check run node ID")
			}
			runNodes[d.ProviderNodeID] = d.ProviderID
		}
	}
	statusIDs := map[int64]struct{}{}
	statusNodes := map[string]int64{}
	for _, o := range in.CommitStatuses {
		d := o.value.data
		if len(o.value.canonical) == 0 || d.HeadSHA != in.HeadSHA {
			return errors.New("status is invalid or bound to a different head")
		}
		if _, ok := statusIDs[d.ProviderID]; ok {
			return errors.New("duplicate commit status provider ID")
		}
		statusIDs[d.ProviderID] = struct{}{}
		if d.ProviderNodeID != "" {
			if old, ok := statusNodes[d.ProviderNodeID]; ok && old != d.ProviderID {
				return errors.New("conflicting commit status node ID")
			}
			statusNodes[d.ProviderNodeID] = d.ProviderID
		}
	}
	return nil
}

func strictDecode(data []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return errors.New("canonical JSON has trailing data")
	}
	return nil
}
func requireSame(data, canonical []byte) error {
	if !bytes.Equal(data, canonical) {
		return errors.New("JSON is valid but not canonical")
	}
	return nil
}

func ParseCheckSuiteObservationV1(data []byte) (CheckSuiteObservationV1, error) {
	var w checkSuiteWireV1
	if err := strictDecode(data, &w); err != nil {
		return CheckSuiteObservationV1{}, err
	}
	v, err := NewCheckSuiteObservationV1(CheckSuiteObservationV1Input{w.ProviderID, w.ProviderNodeID, w.AppID, w.HeadSHA, w.Status, w.Conclusion, w.CreatedAt, w.UpdatedAt})
	if err != nil {
		return v, err
	}
	return v, requireSame(data, v.CanonicalJSON())
}
func ParseCheckRunObservationV1(data []byte) (CheckRunObservationV1, error) {
	var w checkRunWireV1
	if err := strictDecode(data, &w); err != nil {
		return CheckRunObservationV1{}, err
	}
	v, err := NewCheckRunObservationV1(CheckRunObservationV1Input{w.ProviderID, w.ProviderNodeID, w.CheckSuiteID, w.AppID, w.Name, w.HeadSHA, w.Status, w.Conclusion, w.StartedAt, w.CompletedAt})
	if err != nil {
		return v, err
	}
	return v, requireSame(data, v.CanonicalJSON())
}
func ParseCommitStatusObservationV1(data []byte) (CommitStatusObservationV1, error) {
	var w commitStatusWireV1
	if err := strictDecode(data, &w); err != nil {
		return CommitStatusObservationV1{}, err
	}
	v, err := NewCommitStatusObservationV1(CommitStatusObservationV1Input{w.ProviderID, w.ProviderNodeID, w.State, w.Context, w.Description, w.HeadSHA, w.CreatedAt, w.UpdatedAt, w.CreatorID, w.CreatorNodeID, w.CreatorLogin, w.CreatorType})
	if err != nil {
		return v, err
	}
	return v, requireSame(data, v.CanonicalJSON())
}

func ParseCISemanticSweepV1(data []byte) (CISemanticSweepV1, error) {
	var w struct {
		SchemaVersion        int               `json:"schema_version"`
		Kind                 string            `json:"kind"`
		RepositoryOwner      string            `json:"repository_owner"`
		RepositoryName       string            `json:"repository_name"`
		HeadSHA              string            `json:"head_sha"`
		CheckSuiteTotalCount int               `json:"check_suite_total_count"`
		CheckRunTotalCount   int               `json:"check_run_total_count"`
		CheckSuites          []json.RawMessage `json:"check_suites"`
		CheckRuns            []json.RawMessage `json:"check_runs"`
		CommitStatuses       []json.RawMessage `json:"commit_statuses"`
	}
	if err := strictDecode(data, &w); err != nil {
		return CISemanticSweepV1{}, err
	}
	if w.SchemaVersion != SchemaVersionV1 || w.Kind != SemanticSweepKindV1 {
		return CISemanticSweepV1{}, errors.New("unsupported semantic sweep schema")
	}
	in := CISemanticSweepV1Input{RepositoryOwner: w.RepositoryOwner, RepositoryName: w.RepositoryName, HeadSHA: w.HeadSHA, CheckSuiteTotalCount: w.CheckSuiteTotalCount, CheckRunTotalCount: w.CheckRunTotalCount}
	for _, raw := range w.CheckSuites {
		v, e := ParseCheckSuiteObservationV1(raw)
		if e != nil {
			return CISemanticSweepV1{}, e
		}
		in.CheckSuites = append(in.CheckSuites, v)
	}
	for _, raw := range w.CheckRuns {
		v, e := ParseCheckRunObservationV1(raw)
		if e != nil {
			return CISemanticSweepV1{}, e
		}
		in.CheckRuns = append(in.CheckRuns, v)
	}
	for _, raw := range w.CommitStatuses {
		v, e := ParseCommitStatusObservationV1(raw)
		if e != nil {
			return CISemanticSweepV1{}, e
		}
		in.CommitStatuses = append(in.CommitStatuses, v)
	}
	v, err := NewCISemanticSweepV1(in)
	if err != nil {
		return v, err
	}
	return v, requireSame(data, v.CanonicalJSON())
}

func ParseHeadObservationV1(data []byte) (HeadObservationV1, error) {
	var w headObservationWireV1
	if err := strictDecode(data, &w); err != nil {
		return HeadObservationV1{}, err
	}
	v, err := NewHeadObservationV1(HeadObservationV1Input{w.Phase, w.Ref, w.ObjectType, w.SHA, w.RequestSequence, w.ResponseObservedUnixNano})
	if err != nil {
		return v, err
	}
	return v, requireSame(data, v.CanonicalJSON())
}
func ParseRequestProvenanceV1(data []byte) (RequestProvenanceV1, error) {
	var w requestProvenanceWireV1
	if err := strictDecode(data, &w); err != nil {
		return RequestProvenanceV1{}, err
	}
	v, err := NewRequestProvenanceV1(RequestProvenanceV1Input{w.Sequence, w.Phase, w.Page, w.Method, w.PathTemplate, w.EscapedPath, w.CanonicalQuery, w.APIOrigin, w.APIVersion, w.Accept, w.RequestSHA256, w.HTTPStatus, w.ResponseBodySHA256, w.ResponseEnvelopeSHA256, w.CapturedPrefixSHA256, w.BodyTruncated, w.RequestID, w.RequestStartedUnixNano, w.ResponseObservedUnixNano, w.ResponseBytes, w.FailureCode})
	if err != nil {
		return v, err
	}
	return v, requireSame(data, v.CanonicalJSON())
}

func ParseCIEvidenceBundleV1(data []byte) (CIEvidenceBundleV1, error) {
	var w struct {
		SchemaVersion                 int               `json:"schema_version"`
		Kind                          string            `json:"kind"`
		RunID                         string            `json:"run_id"`
		AttemptID                     string            `json:"attempt_id"`
		AttemptKeySHA256              string            `json:"attempt_key_sha256"`
		AuthoritySHA256               string            `json:"authority_sha256"`
		LimitsSHA256                  string            `json:"limits_sha256"`
		RepositoryOwner               string            `json:"repository_owner"`
		RepositoryName                string            `json:"repository_name"`
		HeadBranch                    string            `json:"head_branch"`
		HeadSHA                       string            `json:"head_sha"`
		ActingKind                    string            `json:"acting_kind"`
		ActingSubject                 string            `json:"acting_subject"`
		AuthenticatedID               int64             `json:"authenticated_user_id"`
		AuthenticatedNode             string            `json:"authenticated_user_node_id"`
		AuthenticatedLogin            string            `json:"authenticated_user_login"`
		Outcome                       CollectionOutcome `json:"outcome"`
		FailureCode                   string            `json:"failure_code"`
		FailedPhase                   string            `json:"failed_phase"`
		FailedPage                    int               `json:"failed_page"`
		AttemptStartedUnixNano        int64             `json:"attempt_started_unix_nano"`
		AttemptEndedUnixNano          int64             `json:"attempt_ended_unix_nano"`
		CollectionStartedUnixNano     int64             `json:"collection_started_unix_nano"`
		CollectionEndedUnixNano       int64             `json:"collection_ended_unix_nano"`
		FirstResponseObservedUnixNano int64             `json:"first_response_observed_unix_nano"`
		LastResponseObservedUnixNano  int64             `json:"last_response_observed_unix_nano"`
		EarliestProviderStateAt       string            `json:"earliest_provider_state_at"`
		LatestProviderStateAt         string            `json:"latest_provider_state_at"`
		HeadObservations              []json.RawMessage `json:"head_observations"`
		SweepA                        json.RawMessage   `json:"sweep_a"`
		SweepB                        json.RawMessage   `json:"sweep_b"`
		SemanticDigestA               string            `json:"semantic_digest_a"`
		SemanticDigestB               string            `json:"semantic_digest_b"`
		RequestProvenance             []json.RawMessage `json:"request_provenance"`
		RequestResponseChainSHA256    string            `json:"request_response_chain_sha256"`
		CollectionIdentitySHA256      string            `json:"collection_identity_sha256"`
	}
	if err := strictDecode(data, &w); err != nil {
		return CIEvidenceBundleV1{}, err
	}
	if w.SchemaVersion != SchemaVersionV1 || w.Kind != EvidenceBundleKindV1 {
		return CIEvidenceBundleV1{}, errors.New("unsupported evidence bundle schema")
	}
	in := CIEvidenceBundleV1Input{RunID: w.RunID, AttemptID: w.AttemptID, AttemptKeySHA256: w.AttemptKeySHA256, AuthoritySHA256: w.AuthoritySHA256, LimitsSHA256: w.LimitsSHA256, RepositoryOwner: w.RepositoryOwner, RepositoryName: w.RepositoryName, HeadBranch: w.HeadBranch, HeadSHA: w.HeadSHA, ActingKind: w.ActingKind, ActingSubject: w.ActingSubject, AuthenticatedID: w.AuthenticatedID, AuthenticatedNode: w.AuthenticatedNode, AuthenticatedLogin: w.AuthenticatedLogin, Outcome: w.Outcome, FailureCode: w.FailureCode, FailedPhase: w.FailedPhase, FailedPage: w.FailedPage, AttemptStartedUnixNano: w.AttemptStartedUnixNano, AttemptEndedUnixNano: w.AttemptEndedUnixNano, CollectionStartedUnixNano: w.CollectionStartedUnixNano, CollectionEndedUnixNano: w.CollectionEndedUnixNano, FirstResponseObservedUnixNano: w.FirstResponseObservedUnixNano, LastResponseObservedUnixNano: w.LastResponseObservedUnixNano, EarliestProviderStateAt: w.EarliestProviderStateAt, LatestProviderStateAt: w.LatestProviderStateAt, SemanticDigestA: w.SemanticDigestA, SemanticDigestB: w.SemanticDigestB, RequestResponseChainSHA256: w.RequestResponseChainSHA256, CollectionIdentitySHA256: w.CollectionIdentitySHA256}
	for _, raw := range w.HeadObservations {
		v, e := ParseHeadObservationV1(raw)
		if e != nil {
			return CIEvidenceBundleV1{}, e
		}
		in.HeadObservations = append(in.HeadObservations, v)
	}
	if len(w.SweepA) > 0 && string(w.SweepA) != "null" {
		v, e := ParseCISemanticSweepV1(w.SweepA)
		if e != nil {
			return CIEvidenceBundleV1{}, e
		}
		in.SweepA = &v
	}
	if len(w.SweepB) > 0 && string(w.SweepB) != "null" {
		v, e := ParseCISemanticSweepV1(w.SweepB)
		if e != nil {
			return CIEvidenceBundleV1{}, e
		}
		in.SweepB = &v
	}
	for _, raw := range w.RequestProvenance {
		v, e := ParseRequestProvenanceV1(raw)
		if e != nil {
			return CIEvidenceBundleV1{}, e
		}
		in.RequestProvenance = append(in.RequestProvenance, v)
	}
	v, err := NewCIEvidenceBundleV1(in)
	if err != nil {
		return v, err
	}
	return v, requireSame(data, v.CanonicalJSON())
}

// Read aliases make the strict retained readers explicit at persistence
// boundaries. They reject unknown fields, trailing values, and noncanonical
// encodings before returning reconstructed immutable values.
func ReadCheckSuiteObservationV1(data []byte) (CheckSuiteObservationV1, error) {
	return ParseCheckSuiteObservationV1(data)
}
func ReadCheckRunObservationV1(data []byte) (CheckRunObservationV1, error) {
	return ParseCheckRunObservationV1(data)
}
func ReadCommitStatusObservationV1(data []byte) (CommitStatusObservationV1, error) {
	return ParseCommitStatusObservationV1(data)
}
func ReadCISemanticSweepV1(data []byte) (CISemanticSweepV1, error) {
	return ParseCISemanticSweepV1(data)
}
func ReadHeadObservationV1(data []byte) (HeadObservationV1, error) {
	return ParseHeadObservationV1(data)
}
func ReadRequestProvenanceV1(data []byte) (RequestProvenanceV1, error) {
	return ParseRequestProvenanceV1(data)
}
func ReadCIEvidenceBundleV1(data []byte) (CIEvidenceBundleV1, error) {
	return ParseCIEvidenceBundleV1(data)
}

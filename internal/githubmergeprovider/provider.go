package githubmergeprovider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/mergelifecycle"
)

const (
	apiOrigin                     = "https://api.github.com"
	apiHost                       = "api.github.com"
	apiVersion                    = githublifecycle.GitHubAPIVersionV1
	providerImplementation        = "githubmergeprovider-v1"
	capabilityEvidenceKind        = "github-capability-contract"
	capabilityEvidenceURI         = "embedded://internal/githubmergeprovider/capability_record.json"
	maxReadAttempts               = 2
	defaultUserAgent              = "abcp-github-merge-provider/1"
	contractSchemaSHA256          = "5bdd74993318c503f1fce94cfe098aa3f013df8e1741af7b5fd3f2348cf331fe"
	conformanceFixtureSHA256      = "cbfee2f3f21b871c30880072d5a2bee5f7eab4c25d7133c012becaceeeaa2c85"
	contractSchemaCanonicalV1     = "UpdateRefsInput{clientMutationId:String,refUpdates:[RefUpdate!]!,repositoryId:ID!};RefUpdate{afterOid:GitObjectID!,beforeOid:GitObjectID!,force:Boolean,name:GitRefname!};atomic=true;all_or_nothing=true"
	conformanceFixtureCanonicalV1 = "accepted:base(before->result,false),head(head->head,false);wrong-base:no-change;wrong-head:no-change;refs=disposable"
	officialUpdateRefsDocument    = "https://docs.github.com/en/graphql/reference/git#updaterefs"
	capabilityRecordSchema        = "github-update-refs-capability-record-v1"
	productionDeploymentIdentity  = "github.com"
)

//go:embed capability_record.json
var embeddedCapabilityRecord []byte

type capabilityRecordV1 struct {
	Schema                        string            `json:"schema"`
	Name                          string            `json:"name"`
	OfficialDocumentation         string            `json:"official_documentation"`
	ContractSchemaSHA256          string            `json:"contract_schema_sha256"`
	UpdateRefsInput               map[string]string `json:"update_refs_input"`
	RefUpdate                     map[string]string `json:"ref_update"`
	Atomic                        bool              `json:"atomic"`
	AllOrNothing                  bool              `json:"all_or_nothing"`
	SupportsSameOIDNoOp           bool              `json:"supports_same_oid_no_op"`
	BaseThenHeadOrder             bool              `json:"base_then_head_order"`
	ForceFalse                    bool              `json:"force_false"`
	APIOrigin                     string            `json:"api_origin"`
	GraphQLPath                   string            `json:"graphql_path"`
	APIVersion                    string            `json:"api_version"`
	Deployment                    string            `json:"deployment"`
	ProviderImplementationVersion string            `json:"provider_implementation_version"`
	ConformanceFixtureSHA256      string            `json:"conformance_fixture_sha256"`
}

var (
	frozenCapabilityRecord capabilityRecordV1
	frozenCapabilitySHA256 string
)

func init() {
	record, digest, err := validateCapabilityRecord(embeddedCapabilityRecord)
	if err != nil {
		panic("githubmergeprovider: invalid embedded capability record: " + err.Error())
	}
	frozenCapabilityRecord, frozenCapabilitySHA256 = record, digest
}

func validateCapabilityRecord(data []byte) (capabilityRecordV1, string, error) {
	if digest([]byte(contractSchemaCanonicalV1)) != contractSchemaSHA256 || digest([]byte(conformanceFixtureCanonicalV1)) != conformanceFixtureSHA256 {
		return capabilityRecordV1{}, "", errors.New("compiled capability digest preimages disagree")
	}
	var record capabilityRecordV1
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return record, "", errors.New("capability record cannot be decoded")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return record, "", err
	}
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(bytes.TrimSpace(data), canonical) {
		return record, "", errors.New("capability record is not canonical JSON")
	}
	wantUpdate := map[string]string{"clientMutationId": "String", "refUpdates": "[RefUpdate!]!", "repositoryId": "ID!"}
	wantRef := map[string]string{"afterOid": "GitObjectID!", "beforeOid": "GitObjectID!", "force": "Boolean", "name": "GitRefname!"}
	if record.Schema != capabilityRecordSchema || record.Name != githublifecycle.GitHubAtomicBaseHeadCapabilityV1 ||
		record.OfficialDocumentation != officialUpdateRefsDocument || record.ContractSchemaSHA256 != contractSchemaSHA256 ||
		!equalStringMap(record.UpdateRefsInput, wantUpdate) || !equalStringMap(record.RefUpdate, wantRef) ||
		!record.Atomic || !record.AllOrNothing || !record.SupportsSameOIDNoOp || !record.BaseThenHeadOrder || !record.ForceFalse ||
		record.APIOrigin != apiOrigin || record.GraphQLPath != githublifecycle.GitHubGraphQLPathV1 || record.APIVersion != apiVersion ||
		record.Deployment != productionDeploymentIdentity || record.ProviderImplementationVersion != providerImplementation ||
		record.ConformanceFixtureSHA256 != conformanceFixtureSHA256 {
		return record, "", errors.New("capability record disagrees with compiled provider semantics")
	}
	return record, digest(data), nil
}

func equalStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

// Authenticator is a sealed bearer credential plus its non-secret principal
// binding. It never exposes, formats, or logs the credential.
type Authenticator struct {
	token          []byte
	kind           githublifecycle.ActingKind
	subject        string
	installationID int64
}

func NewUserAuthenticator(token, stableNodeID string) (*Authenticator, error) {
	identity, err := githublifecycle.NewUserIdentity(stableNodeID)
	if err != nil {
		return nil, err
	}
	return newAuthenticator(token, identity)
}

func NewAppInstallationAuthenticator(token, stableNodeID string, installationID int64) (*Authenticator, error) {
	identity, err := githublifecycle.NewAppInstallationIdentity(stableNodeID, installationID)
	if err != nil {
		return nil, err
	}
	return newAuthenticator(token, identity)
}

func newAuthenticator(token string, identity githublifecycle.ActingIdentity) (*Authenticator, error) {
	if len(token) == 0 || len(token) > 16*1024 || bytes.IndexByte([]byte(token), 0) >= 0 || bytes.ContainsAny([]byte(token), "\r\n") {
		return nil, errors.New("a bounded bearer credential is required")
	}
	return &Authenticator{token: append([]byte(nil), token...), kind: identity.Kind(), subject: identity.Subject(), installationID: identity.InstallationID()}, nil
}

func (a *Authenticator) matches(identity githublifecycle.ActingIdentity) bool {
	if a == nil {
		return false
	}
	return a.kind == identity.Kind() && subtle.ConstantTimeCompare([]byte(a.subject), []byte(identity.Subject())) == 1 &&
		a.installationID == identity.InstallationID()
}

func (a *Authenticator) authorizationValue() string {
	return "Bearer " + string(a.token)
}

// Provider is the merge-only GitHub implementation. Its clients, origin,
// endpoints, limits, and mutation ceilings are entirely provider-owned.
type Provider struct {
	auth           *Authenticator
	limits         githublifecycle.Limits
	readClient     *http.Client
	mutationClient func(*submissionTracker) *http.Client
	now            func() time.Time

	mutationMu      sync.Mutex
	commitSubmitted map[string]struct{}
	targetSubmitted map[string]struct{}
}

func New(auth *Authenticator) (*Provider, error) {
	if auth == nil || len(auth.token) == 0 {
		return nil, errors.New("sealed authenticator is required")
	}
	limits := githublifecycle.DefaultLimits()
	readClient := productionReadClient(auth, limits)
	return newProvider(auth, limits, readClient, func(tracker *submissionTracker) *http.Client {
		return productionMutationClient(auth, limits, tracker)
	})
}

func newProvider(auth *Authenticator, limits githublifecycle.Limits, readClient *http.Client, mutationClient func(*submissionTracker) *http.Client) (*Provider, error) {
	if auth == nil || len(auth.token) == 0 || limits.Validate() != nil || readClient == nil || mutationClient == nil {
		return nil, errors.New("valid sealed provider dependencies are required")
	}
	if _, _, err := validateCapabilityRecord(embeddedCapabilityRecord); err != nil {
		return nil, err
	}
	return &Provider{auth: auth, limits: limits, readClient: readClient, mutationClient: mutationClient, now: time.Now,
		commitSubmitted: make(map[string]struct{}), targetSubmitted: make(map[string]struct{})}, nil
}

// Capability returns the only production capability, bound to one stable
// same-repository node identity and the embedded reviewed record.
func (p *Provider) Capability(repositoryNodeID string) (githublifecycle.ProviderCapabilityV1, error) {
	if p == nil {
		return githublifecycle.ProviderCapabilityV1{}, errors.New("provider is nil")
	}
	if _, _, err := validateCapabilityRecord(embeddedCapabilityRecord); err != nil {
		return githublifecycle.ProviderCapabilityV1{}, err
	}
	evidence := ledger.EvidenceRef{URI: capabilityEvidenceURI, Kind: capabilityEvidenceKind, SHA256: frozenCapabilitySHA256}
	return githublifecycle.NewProviderCapabilityV1(githublifecycle.ProviderCapabilityV1Input{
		Name: githublifecycle.GitHubAtomicBaseHeadCapabilityV1, RepositoryNodeID: repositoryNodeID, APIVersion: apiVersion,
		Atomic: true, AllOrNothing: true, SupportsNoOp: true, BaseThenHeadOrder: true, ForceFalse: true,
		EvidenceRefs: []ledger.EvidenceRef{evidence},
	}, p.limits)
}

func (p *Provider) validateAuthority(authority githublifecycle.Authority) error {
	if p == nil || p.auth == nil {
		return errors.New("provider is unavailable")
	}
	if authority.AllowedMergeMethod() != githublifecycle.MergeMethodMerge {
		return errors.New("production GitHub provider supports merge only")
	}
	if !p.auth.matches(authority.Actor()) {
		return errors.New("sealed credential principal does not match controller authority")
	}
	if _, ok := authority.PullRequest(); !ok {
		return errors.New("an exact pull request identity is required")
	}
	return nil
}

func (p *Provider) validateSealedCapability(sealed githublifecycle.SealedMergeAuthorizationV1) error {
	if _, _, err := validateCapabilityRecord(embeddedCapabilityRecord); err != nil {
		return err
	}
	input := sealed.MergeInput()
	if err := p.validateAuthority(input.Authority()); err != nil {
		return err
	}
	repositoryNodeID := input.Authority().ReadyBinding().RepositoryBinding().Input().GitHubRepositoryNodeID
	want, err := p.Capability(repositoryNodeID)
	if err != nil {
		return err
	}
	got := input.Capability()
	if got.SHA256() != want.SHA256() || !bytes.Equal(got.CanonicalJSON(), want.CanonicalJSON()) {
		return errors.New("sealed capability does not match the frozen embedded capability record")
	}
	return nil
}

func (p *Provider) startMeter(ctx context.Context) *callMeter {
	budget, ok := mergelifecycle.ProviderBudgetFromContext(ctx)
	if !ok {
		budget = mergelifecycle.ProviderBudgetV1{
			RequestBytes: p.limits.MaxCumulativeRequestBytes, HeaderBytes: p.limits.MaxCumulativeResponseHeaderBytes,
			CompressedResponseBytes:   p.limits.MaxCumulativeCompressedResponseBytes,
			DecompressedResponseBytes: p.limits.MaxCumulativeDecompressedResponseBytes,
			ActiveNanos:               int64(p.limits.MaxCumulativeActiveProviderCallTime),
		}
	}
	return &callMeter{budget: budget, started: p.now()}
}

func (p *Provider) finishMeter(meter *callMeter) mergelifecycle.ProviderAccountingV1 {
	if meter == nil {
		return mergelifecycle.ProviderAccountingV1{}
	}
	meter.accounting.InvocationNanos = p.now().Sub(meter.started).Nanoseconds()
	if meter.accounting.InvocationNanos < meter.accounting.ActiveNanos {
		meter.accounting.InvocationNanos = meter.accounting.ActiveNanos
	}
	return meter.accounting
}

func (p *Provider) reserveCommitMutation(key string) bool {
	p.mutationMu.Lock()
	defer p.mutationMu.Unlock()
	if _, exists := p.commitSubmitted[key]; exists {
		return false
	}
	p.commitSubmitted[key] = struct{}{}
	return true
}

func (p *Provider) reserveTargetMutation(key string) bool {
	p.mutationMu.Lock()
	defer p.mutationMu.Unlock()
	if _, exists := p.targetSubmitted[key]; exists {
		return false
	}
	p.targetSubmitted[key] = struct{}{}
	return true
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func evidence(uri, kind string, data []byte) ledger.EvidenceRef {
	return ledger.EvidenceRef{URI: uri, Kind: kind, SHA256: digest(data)}
}

func evidenceURI(kind, requestID string) string {
	return "github-evidence://" + kind + "/" + requestID
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("response contains more than one JSON value")
	} else if !errors.Is(err, io.EOF) {
		return errors.New("response has trailing non-JSON data")
	}
	return nil
}

var _ mergelifecycle.Provider = (*Provider)(nil)

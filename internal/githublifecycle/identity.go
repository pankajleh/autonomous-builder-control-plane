package githublifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var repositoryComponent = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,98}[A-Za-z0-9_-])?$`)

// Repository is an exact provider repository identity. Case is preserved and
// remains significant; constructors never normalize identities into equality.
type Repository struct{ owner, name string }

func NewRepository(owner, name string) (Repository, error) {
	if !repositoryComponent.MatchString(owner) || !repositoryComponent.MatchString(name) || name == "." || name == ".." {
		return Repository{}, errors.New("repository owner and name must be safe exact components")
	}
	return Repository{owner: owner, name: name}, nil
}

func (r Repository) Owner() string { return r.owner }
func (r Repository) Name() string  { return r.name }
func (r Repository) String() string {
	if r.owner == "" || r.name == "" {
		return ""
	}
	return r.owner + "/" + r.name
}

func (r Repository) valid() bool {
	validated, err := NewRepository(r.owner, r.name)
	return err == nil && validated == r
}

// Branch is an exact full Git branch name, without refs/heads/. Case is
// preserved and significant.
type Branch struct{ value string }

func NewBranch(value string) (Branch, error) {
	if !validText(value, 255, false) || !validBranchName(value) {
		return Branch{}, errors.New("branch must be a safe exact full Git branch name")
	}
	return Branch{value: value}, nil
}

func (b Branch) String() string { return b.value }
func (b Branch) valid() bool {
	validated, err := NewBranch(b.value)
	return err == nil && validated == b
}

// GitSHA is one complete lowercase SHA-1 or SHA-256 Git object ID.
type GitSHA struct{ value string }

func NewGitSHA(value string) (GitSHA, error) {
	if (len(value) != 40 && len(value) != 64) || !isLowerHex(value) {
		return GitSHA{}, errors.New("Git SHA must be a complete lowercase SHA-1 or SHA-256 object ID")
	}
	return GitSHA{value: value}, nil
}

func (s GitSHA) String() string { return s.value }
func (s GitSHA) valid() bool {
	validated, err := NewGitSHA(s.value)
	return err == nil && validated == s
}

// PullRequestIdentity is the stable number and provider node identity of one
// pull request. Both values are required together when an authority has a PR.
type PullRequestIdentity struct {
	number int64
	nodeID string
}

func NewPullRequestIdentity(number int64, nodeID string) (PullRequestIdentity, error) {
	if number <= 0 || !validOpaqueID(nodeID, 256) {
		return PullRequestIdentity{}, errors.New("pull request number and safe node identity are required")
	}
	return PullRequestIdentity{number: number, nodeID: nodeID}, nil
}

func (p PullRequestIdentity) Number() int64  { return p.number }
func (p PullRequestIdentity) NodeID() string { return p.nodeID }
func (p PullRequestIdentity) valid() bool {
	validated, err := NewPullRequestIdentity(p.number, p.nodeID)
	return err == nil && validated == p
}

// SnapshotIdentity identifies one bounded remote observation.
type SnapshotIdentity struct {
	provider   string
	requestID  string
	observedAt int64
}

func NewSnapshotIdentity(provider, requestID string, observedUnixNano int64) (SnapshotIdentity, error) {
	if !validOpaqueID(provider, 64) || !validOpaqueID(requestID, 256) || observedUnixNano <= 0 {
		return SnapshotIdentity{}, errors.New("snapshot provider, request identity, and observation time are required")
	}
	return SnapshotIdentity{provider: provider, requestID: requestID, observedAt: observedUnixNano}, nil
}

func (s SnapshotIdentity) Provider() string        { return s.provider }
func (s SnapshotIdentity) RequestID() string       { return s.requestID }
func (s SnapshotIdentity) ObservedUnixNano() int64 { return s.observedAt }
func (s SnapshotIdentity) valid() bool {
	validated, err := NewSnapshotIdentity(s.provider, s.requestID, s.observedAt)
	return err == nil && validated == s
}

type MergeMethod string

const (
	MergeMethodMerge  MergeMethod = "merge"
	MergeMethodSquash MergeMethod = "squash"
	MergeMethodRebase MergeMethod = "rebase"
)

func (m MergeMethod) Valid() bool {
	return m == MergeMethodMerge || m == MergeMethodSquash || m == MergeMethodRebase
}

type ActingKind string

const (
	ActingKindUser            ActingKind = "user"
	ActingKindAppInstallation ActingKind = "app_installation"
)

// ActingIdentity is non-secret authenticated provenance. It intentionally has
// no credential or token field.
type ActingIdentity struct {
	kind           ActingKind
	subject        string
	installationID int64
}

func NewUserIdentity(subject string) (ActingIdentity, error) {
	if !validOpaqueID(subject, 256) {
		return ActingIdentity{}, errors.New("authenticated user subject is required")
	}
	return ActingIdentity{kind: ActingKindUser, subject: subject}, nil
}

func NewAppInstallationIdentity(subject string, installationID int64) (ActingIdentity, error) {
	if !validOpaqueID(subject, 256) || installationID <= 0 {
		return ActingIdentity{}, errors.New("authenticated app subject and installation ID are required")
	}
	return ActingIdentity{kind: ActingKindAppInstallation, subject: subject, installationID: installationID}, nil
}

func (a ActingIdentity) Kind() ActingKind      { return a.kind }
func (a ActingIdentity) Subject() string       { return a.subject }
func (a ActingIdentity) InstallationID() int64 { return a.installationID }
func (a ActingIdentity) valid() bool {
	switch a.kind {
	case ActingKindUser:
		validated, err := NewUserIdentity(a.subject)
		return err == nil && validated == a
	case ActingKindAppInstallation:
		validated, err := NewAppInstallationIdentity(a.subject, a.installationID)
		return err == nil && validated == a
	default:
		return false
	}
}

// AuthorityInput binds every identity that can authorize a later remote write.
type AuthorityInput struct {
	Repository         Repository
	BaseBranch         Branch
	HeadBranch         Branch
	HeadSHA            GitSHA
	ExpectedBaseTipSHA GitSHA
	PullRequest        *PullRequestIdentity
	AllowedMergeMethod MergeMethod
	Actor              ActingIdentity
	ExpectedContent    ExpectedMergeContent
}

// Authority is immutable and safe to copy.
type Authority struct{ data AuthorityInput }

func NewAuthority(input AuthorityInput) (Authority, error) {
	if !input.Repository.valid() || !input.BaseBranch.valid() || !input.HeadBranch.valid() ||
		!input.HeadSHA.valid() || !input.ExpectedBaseTipSHA.valid() || !input.AllowedMergeMethod.Valid() || !input.Actor.valid() || !input.ExpectedContent.valid() {
		return Authority{}, errors.New("GitHub lifecycle authority contains an invalid identity")
	}
	if input.ExpectedContent.SourceIntegratedHeadSHA() != input.HeadSHA || input.ExpectedContent.SourceBaselineSHA() != input.ExpectedBaseTipSHA {
		return Authority{}, errors.New("expected merge content is not derived from the accepted head and base")
	}
	if input.BaseBranch == input.HeadBranch {
		return Authority{}, errors.New("base and head branches must be distinct")
	}
	if input.PullRequest != nil && !input.PullRequest.valid() {
		return Authority{}, errors.New("pull request identity is invalid")
	}
	input = cloneAuthorityInput(input)
	return Authority{data: input}, nil
}

func (a Authority) Input() AuthorityInput           { return cloneAuthorityInput(a.data) }
func (a Authority) Repository() Repository          { return a.data.Repository }
func (a Authority) BaseBranch() Branch              { return a.data.BaseBranch }
func (a Authority) HeadBranch() Branch              { return a.data.HeadBranch }
func (a Authority) HeadSHA() GitSHA                 { return a.data.HeadSHA }
func (a Authority) ExpectedBaseTipSHA() GitSHA      { return a.data.ExpectedBaseTipSHA }
func (a Authority) AllowedMergeMethod() MergeMethod { return a.data.AllowedMergeMethod }
func (a Authority) Actor() ActingIdentity           { return a.data.Actor }
func (a Authority) ExpectedContent() ExpectedMergeContent {
	return cloneExpectedContent(a.data.ExpectedContent)
}
func (a Authority) PullRequest() (PullRequestIdentity, bool) {
	if a.data.PullRequest == nil {
		return PullRequestIdentity{}, false
	}
	return *a.data.PullRequest, true
}
func (a Authority) valid() bool {
	_, err := NewAuthority(a.data)
	return err == nil
}

// CanonicalJSON returns the exact stable authority bytes used for identity and
// evidence binding.
func (a Authority) CanonicalJSON() ([]byte, error) {
	if !a.valid() {
		return nil, errors.New("GitHub lifecycle authority is incomplete")
	}
	var pullRequest *prIdentityWire
	if a.data.PullRequest != nil {
		wire := pullRequestWire(*a.data.PullRequest)
		pullRequest = &wire
	}
	return json.Marshal(struct {
		Repository         repoWire        `json:"repository"`
		BaseBranch         string          `json:"base_branch"`
		HeadBranch         string          `json:"head_branch"`
		HeadSHA            string          `json:"head_sha"`
		ExpectedBaseTipSHA string          `json:"expected_base_tip_sha"`
		PullRequest        *prIdentityWire `json:"pull_request,omitempty"`
		AllowedMergeMethod MergeMethod     `json:"allowed_merge_method"`
		Actor              actorWire       `json:"actor"`
		ExpectedContent    json.RawMessage `json:"expected_merge_content"`
	}{
		repositoryWire(a.data.Repository), a.data.BaseBranch.String(), a.data.HeadBranch.String(), a.data.HeadSHA.String(),
		a.data.ExpectedBaseTipSHA.String(), pullRequest, a.data.AllowedMergeMethod, actingWire(a.data.Actor), a.data.ExpectedContent.CanonicalJSON(),
	})
}

func (a Authority) SHA256() (string, error) {
	data, err := a.CanonicalJSON()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func (a Authority) MarshalJSON() ([]byte, error) { return a.CanonicalJSON() }

func cloneAuthorityInput(input AuthorityInput) AuthorityInput {
	if input.PullRequest != nil {
		copy := *input.PullRequest
		input.PullRequest = &copy
	}
	input.ExpectedContent = cloneExpectedContent(input.ExpectedContent)
	return input
}

func cloneExpectedContent(input ExpectedMergeContent) ExpectedMergeContent {
	input.canonical = append([]byte(nil), input.canonical...)
	return input
}

func validBranchName(value string) bool {
	if value == "@" || value == "HEAD" || strings.HasPrefix(value, "refs/") || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") ||
		strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || strings.Contains(value, "//") ||
		strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.ContainsAny(value, " ~^:?*[\\") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func validText(value string, max int, allowEmpty bool) bool {
	return (allowEmpty || value != "") && len(value) <= max && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func validOpaqueID(value string, max int) bool {
	return validText(value, max, false) && !strings.ContainsAny(value, `/\\?#`)
}

func isLowerHex(value string) bool {
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func requireAuthority(authority Authority) error {
	if !authority.valid() {
		return fmt.Errorf("invalid lifecycle authority")
	}
	return nil
}

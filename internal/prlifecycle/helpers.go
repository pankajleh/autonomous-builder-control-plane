package prlifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

var githubUserSubject = regexp.MustCompile(`^github-user-id:([1-9][0-9]*)$`)

func trackActorID(actor githublifecycle.ActingIdentity) (int64, error) {
	if actor.Kind() != githublifecycle.ActingKindUser || actor.InstallationID() != 0 {
		return 0, errors.New("only stable GitHub user authority is supported")
	}
	match := githubUserSubject.FindStringSubmatch(actor.Subject())
	if match == nil {
		return 0, errors.New("actor subject must be github-user-id:<positive-decimal-id>")
	}
	id, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != match[1] {
		return 0, errors.New("actor subject has non-canonical numeric ID")
	}
	return id, nil
}

func validateDocument(title, body string, maximum int) error {
	valid := func(value string, allowEmpty bool) bool {
		return (allowEmpty || value != "") && len(value) <= maximum && utf8.ValidString(value) && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
	}
	if !valid(title, false) || !valid(body, true) {
		return errors.New("PR title/body must be bounded single-line canonical text")
	}
	return nil
}

func validateExactPR(authority githublifecycle.Authority, pr RemotePRObservation, head, base RemoteRefObservation, limits githublifecycle.Limits) (githublifecycle.PullRequestSnapshot, error) {
	limitsSHA, _ := limits.SHA256()
	if pr.LimitsSHA256 != limitsSHA || head.LimitsSHA256 != limitsSHA || base.LimitsSHA256 != limitsSHA {
		return githublifecycle.PullRequestSnapshot{}, errors.New("remote observation limits identity differs")
	}
	repository := authority.Repository()
	if pr.RepositoryOwner != repository.Owner() || pr.RepositoryName != repository.Name() || pr.BaseRepository != repository.String() || pr.HeadRepository != repository.String() ||
		pr.BaseRef != authority.BaseBranch().String() || pr.HeadRef != authority.HeadBranch().String() || pr.HeadLabel != repository.Owner()+":"+authority.HeadBranch().String() ||
		pr.State != "open" || pr.Merged || pr.HeadSHA != head.SHA || head.SHA != authority.HeadSHA().String() || base.SHA != authority.ExpectedBaseTipSHA().String() ||
		head.EchoedRef != "refs/heads/"+authority.HeadBranch().String() || base.EchoedRef != "refs/heads/"+authority.BaseBranch().String() || head.ObjectType != "commit" || base.ObjectType != "commit" {
		return githublifecycle.PullRequestSnapshot{}, errors.New("pull request fails exact-open repository/ref/head predicate")
	}
	if expected, ok := authority.PullRequest(); ok && (pr.Number != expected.Number() || pr.NodeID != expected.NodeID()) {
		return githublifecycle.PullRequestSnapshot{}, errors.New("pull request identity differs from authority")
	}
	if _, err := githublifecycle.NewGitSHA(pr.HeadSHA); err != nil {
		return githublifecycle.PullRequestSnapshot{}, errors.New("PR-reported head SHA is invalid")
	}
	if _, err := githublifecycle.NewGitSHA(pr.BaseSHA); err != nil {
		return githublifecycle.PullRequestSnapshot{}, errors.New("PR-reported base SHA is invalid")
	}
	prIdentity, err := githublifecycle.NewPullRequestIdentity(pr.Number, pr.NodeID)
	if err != nil {
		return githublifecycle.PullRequestSnapshot{}, err
	}
	requestDigest := sha256.Sum256([]byte(digestMust(pr) + digestMust(head) + digestMust(base)))
	observed := pr.ObservedUnixNano
	if head.ObservedUnixNano > observed {
		observed = head.ObservedUnixNano
	}
	if base.ObservedUnixNano > observed {
		observed = base.ObservedUnixNano
	}
	identity, err := githublifecycle.NewSnapshotIdentity("github", hex.EncodeToString(requestDigest[:]), observed)
	if err != nil {
		return githublifecycle.PullRequestSnapshot{}, err
	}
	headSHA, _ := githublifecycle.NewGitSHA(head.SHA)
	baseSHA, _ := githublifecycle.NewGitSHA(base.SHA)
	snapshot, err := githublifecycle.NewPullRequestSnapshot(githublifecycle.PullRequestSnapshotInput{
		Snapshot: identity, Repository: repository, PullRequest: prIdentity, BaseBranch: authority.BaseBranch(), BaseTipSHA: baseSHA,
		HeadBranch: authority.HeadBranch(), HeadSHA: headSHA, State: githublifecycle.PullRequestOpen,
		Reviews: []githublifecycle.Review{}, EvidenceRefs: []ledger.EvidenceRef{}, Metadata: map[string]string{},
	}, limits)
	if err != nil {
		return snapshot, err
	}
	if _, err := githublifecycle.SelectPullRequest(authority, []githublifecycle.PullRequestSnapshot{snapshot}, limits); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func digestMust(value any) string {
	b, _ := json.Marshal(value)
	return digestBytes(b)
}

func documentDigest(title, body string) string {
	b, _ := json.Marshal(struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}{title, body})
	return digestBytes(b)
}

func deterministicWriteID(key PRResourceKeyV1, revision uint64) string {
	digest := sha256.Sum256([]byte("pr-write-v1\x00" + key.String() + "\x00" + strconv.FormatUint(revision, 10) + "\x001"))
	return "pr-write-v1-" + hex.EncodeToString(digest[:])
}

func canonicalDigest(value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return digestBytes(b), nil
}

func digestBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func validRemoteText(value string, maximum int, allowEmpty bool) bool {
	return (allowEmpty || value != "") && len(value) <= maximum && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func firstError(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return boundedError(err)
}

func boundedError(err error) string {
	if err == nil {
		return ""
	}
	var lifecycle *Error
	if errors.As(err, &lifecycle) && lifecycle.Code != "" {
		return lifecycle.Code
	}
	// Persisted diagnostics are controller-owned classes, never provider or
	// transport Error() strings.
	return "CONTROLLER_OPERATION_FAILED"
}

func NewTerminalBudget(authority, attempt []byte, title, body string) (TerminalBudgetV1, error) {
	if len(authority) == 0 || len(authority) > 16<<10 || len(attempt) == 0 || len(attempt) > 8<<10 || validateDocument(title, body, githublifecycle.DefaultLimits().MaxTextBytes) != nil {
		return TerminalBudgetV1{}, errors.New("terminal input exceeds an individual wire cap")
	}
	// Complete pre-submit bound: source+derived authority, attempt, desired
	// document, primitive PR/principal/ref observations, optional snapshot,
	// reconciliation wire, one self-contained normalized evidence artifact,
	// result/reason/event material, digests and JSON framing. Lifecycle remote
	// text is independently capped at 1 KiB, making the observation envelope
	// at most 32 KiB.
	worst := 2*len(authority) + len(attempt) + len(title) + len(body) +
		3*MaxTerminalArtifactBytes + MaxTerminalSnapshotBytes + (48 << 10)
	if worst > MaxTerminalBytes {
		return TerminalBudgetV1{}, errors.New("terminal worst-case profile exceeds terminal cap")
	}
	return TerminalBudgetV1{len(authority), len(attempt), len(title) + len(body), worst}, nil
}

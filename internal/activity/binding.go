package activity

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runadmission"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/strictjson"
)

type Catalog interface {
	ReadRun(string) (runtimecatalog.RunRegistrationV1, error)
}

type Scope struct {
	RunID, Repository, RepositoryIdentity, Project, Branch, Worktree, Base, AuthorityDigest string
	Ralphex                                                                                 authority.RalphexManifest
}

type Resolver struct {
	Root    string
	Catalog Catalog
}

var gitSHA = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func (r Resolver) Resolve(ctx context.Context, run string) (Scope, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	fail := func() (Scope, error) { return Scope{}, ErrUnavailable }
	root, rootErr := privateDirectory(r.Root, false)
	if rootErr != nil {
		return fail()
	}
	root.Close()
	reg, err := r.Catalog.ReadRun(run)
	if err != nil || reg.RunID != run {
		return fail()
	}
	dir, err := privateDirectory(filepath.Join(r.Root, "admissions"), false)
	if err != nil {
		return fail()
	}
	entries, err := dir.ReadDir(20001)
	dir.Close()
	if err != nil && err != io.EOF || len(entries) > 20000 {
		return fail()
	}
	var binding runadmission.AdmissionBindingV1
	matches := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".binding.json") {
			continue
		}
		data, e := readFile(filepath.Join(r.Root, "admissions", entry.Name()), runadmission.MaxBindingBytes, true)
		if e != nil {
			return fail()
		}
		var b runadmission.AdmissionBindingV1
		if strictjson.Decode(data, &b) != nil {
			return fail()
		}
		if b.RunID == run {
			matches++
			binding = b
		}
	}
	b := binding
	if matches != 1 || b.Kind != "AdmissionBindingV1" || b.SchemaVersion != 1 || b.AuthorityDigest != reg.AuthorityDigest || b.RepositoryIdentityDigest != reg.RepositoryIdentityDigest || runtimecatalog.RepositoryIdentityDigest(b.RepositoryIdentity) != reg.RepositoryIdentityDigest || b.CanonicalLedgerPath != reg.CanonicalLedgerPath || b.CanonicalEvidenceRoot != reg.CanonicalEvidenceRoot || b.CanonicalLedgerPath != filepath.Join(b.LedgerRoot, run, "events.jsonl") || b.CanonicalEvidenceRoot != filepath.Join(b.EvidenceRoot, run) {
		return fail()
	}
	if !filepath.IsAbs(b.RepositoryPath) || filepath.Clean(b.RepositoryPath) != b.RepositoryPath || b.InputDirectory == "" || filepath.IsAbs(b.InputDirectory) || filepath.Clean(b.InputDirectory) != b.InputDirectory || b.InputDirectory == ".." || strings.HasPrefix(b.InputDirectory, "../") {
		return fail()
	}
	admittedDir := filepath.Join(b.RepositoryPath, b.InputDirectory, run)
	if b.ManifestPath != filepath.Join(admittedDir, "manifest.json") || b.PlanPath != filepath.Join(admittedDir, "plan.md") {
		return fail()
	}
	data, err := readFile(b.ManifestPath, runadmission.MaxManifestTemplateBytes, false)
	if err != nil || digest(data) != reg.AuthorityDigest {
		return fail()
	}
	var manifest authority.Manifest
	if strictjson.Decode(data, &manifest) != nil || manifest.RunID != run || manifest.Repository.Path != b.RepositoryPath || manifest.Repository.Identity != b.RepositoryIdentity || !gitSHA.MatchString(manifest.Repository.StartSHA) || !manifest.Worktree.Enabled || manifest.Worktree.Branch != "abcp/"+run || !filepath.IsAbs(manifest.Ralphex.BinaryPath) || filepath.Clean(manifest.Ralphex.BinaryPath) != manifest.Ralphex.BinaryPath || len(manifest.Ralphex.BinarySHA256) != 64 || !gitSHA.MatchString(manifest.Ralphex.SourceSHA) || manifest.Plan.Path != b.PlanPath {
		return fail()
	}
	repo, err := openDirectory(b.RepositoryPath)
	if err != nil {
		return fail()
	}
	repo.Close()
	plan, err := readFile(b.PlanPath, runadmission.MaxManifestTemplateBytes, false)
	if err != nil || digest(plan) != manifest.Plan.SHA256 {
		return fail()
	}
	// Admission writes these metadata lines immediately after the heading.
	// Read only that controller-created prefix, never later task prose.
	project := ""
	lines := strings.Split(string(plan), "\n")
	for _, line := range lines[1:] {
		if strings.HasPrefix(line, "Project: ") {
			if project != "" {
				return fail()
			}
			project = strings.TrimPrefix(line, "Project: ")
			continue
		}
		if strings.HasPrefix(line, "Display-Title: ") || strings.HasPrefix(line, "Repository: ") || strings.HasPrefix(line, "Admission-ID: ") {
			continue
		}
		break
	}
	scope := Scope{RunID: run, Repository: b.RepositoryPath, RepositoryIdentity: b.RepositoryIdentity, Project: project, Branch: manifest.Worktree.Branch, Base: manifest.Repository.StartSHA, AuthorityDigest: reg.AuthorityDigest, Ralphex: manifest.Ralphex}
	scope.Worktree, err = resolveWorktree(ctx, scope)
	if err != nil {
		return fail()
	}
	return scope, nil
}

// Git subprocesses are bounded and inherit the repository's existing sanitized
// Git environment, including protection from GIT_DIR/worktree overrides.
func git(ctx context.Context, path string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...)
	cmd.Env = gitexec.Environment()
	var out limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil || out.exceeded {
		return "", ErrUnavailable
	}
	return strings.TrimSpace(out.String()), nil
}

type limitedBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		b.exceeded = true
		return 0, ErrExhausted
	}
	return b.Buffer.Write(p)
}

func resolveWorktree(ctx context.Context, scope Scope) (string, error) {
	text, err := git(ctx, scope.Repository, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return "", err
	}
	path, found := "", ""
	for _, field := range strings.Split(text, "\x00") {
		if strings.HasPrefix(field, "worktree ") {
			path = strings.TrimPrefix(field, "worktree ")
		}
		if field == "branch refs/heads/"+scope.Branch {
			if found != "" {
				return "", ErrIntegrity
			}
			found = path
		}
	}
	if found == "" {
		return "", ErrUnavailable
	}
	d, err := openDirectory(found)
	if err != nil {
		return "", err
	}
	defer d.Close()
	branch, err := git(ctx, found, "symbolic-ref", "--quiet", "HEAD")
	if err != nil || branch != "refs/heads/"+scope.Branch {
		return "", ErrIntegrity
	}
	root, err := git(ctx, found, "rev-parse", "--show-toplevel")
	if err != nil || root != found {
		return "", ErrIntegrity
	}
	common, err := git(ctx, scope.Repository, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", ErrIntegrity
	}
	workCommon, err := git(ctx, found, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || workCommon != common {
		return "", ErrIntegrity
	}
	return found, nil
}

func checkpoint(ctx context.Context, scope Scope, now time.Time) (Event, bool) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	path, err := resolveWorktree(ctx, scope)
	if err != nil || path != scope.Worktree {
		return Event{}, false
	}
	status, err := git(ctx, path, "status", "--porcelain", "--untracked-files=all")
	if err != nil || status != "" {
		return Event{}, false
	}
	head, err := git(ctx, path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || !gitSHA.MatchString(head) || head == scope.Base {
		return Event{}, false
	}
	if _, err = git(ctx, path, "merge-base", "--is-ancestor", scope.Base, head); err != nil {
		return Event{}, false
	}
	if _, err = git(ctx, path, "merge-base", "--is-ancestor", head, "refs/heads/"+scope.Branch); err != nil {
		return Event{}, false
	}
	// Recheck after ancestry work so a changed branch, HEAD, or dirty tree cannot
	// inherit a checkpoint sampled before that change.
	again, err := resolveWorktree(ctx, scope)
	if err != nil || again != path {
		return Event{}, false
	}
	next, err := git(ctx, path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || next != head {
		return Event{}, false
	}
	status, err = git(ctx, path, "status", "--porcelain", "--untracked-files=all")
	if err != nil || status != "" {
		return Event{}, false
	}
	e := baseEvent(scope.RunID, now)
	e.AuthorityLevel, e.Category, e.Status, e.Title = "ABCP_STATE", "CHECKPOINT", "AVAILABLE", "Source checkpoint available"
	e.SourceKind, e.SourceDigest = "ABCP_CHECKPOINT_OBSERVER", identity(scope.RunID, head)
	e.CheckpointSHA, e.CheckpointClean = head, true
	e.ActivityID = identity(scope.RunID, "checkpoint", head)
	return e, true
}

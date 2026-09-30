package activity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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

// errWorktreeMissing reports that the governed worktree branch does not exist
// yet. It remains ErrUnavailable for every caller.
var errWorktreeMissing = fmt.Errorf("%w: governed worktree branch is not present", ErrUnavailable)

type Catalog interface {
	ReadRun(string) (runtimecatalog.RunRegistrationV1, error)
}

type Scope struct {
	RunID, Repository, RepositoryIdentity, Project, Branch, Worktree, Base, AuthorityDigest string
	Ralphex                                                                                 authority.RalphexManifest
	// Retain reports the manifest's controller-owned worktree retention policy.
	Retain bool
}

type Resolver struct {
	Root    string
	Catalog Catalog
}

var gitSHA = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// Resolve verifies the run's immutable admission binding and its live governed
// worktree.
func (r Resolver) Resolve(ctx context.Context, run string) (Scope, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	scope, err := r.resolveBinding(ctx, run)
	if err != nil {
		return Scope{}, err
	}
	scope.Worktree, err = resolveWorktree(ctx, scope)
	if err != nil {
		if errors.Is(err, errWorktreeMissing) {
			// Keep the identity so refresh can recognise a not-yet-created worktree.
			return Scope{}, atStep("worktree", err)
		}
		return Scope{}, annotate("worktree", err, ErrUnavailable)
	}
	return scope, nil
}

// ResolveBinding verifies everything Resolve does except the live worktree:
// registration, admission binding, manifest, plan and repository. It is the
// binding a controller-evicted run keeps once its worktree is removed.
func (r Resolver) ResolveBinding(ctx context.Context, run string) (Scope, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return r.resolveBinding(ctx, run)
}

func (r Resolver) resolveBinding(ctx context.Context, run string) (Scope, error) {
	// Every failure remains ErrUnavailable; the step only names it for diagnostics.
	fail := func(step string) (Scope, error) { return Scope{}, atStep(step, ErrUnavailable) }
	root, rootErr := privateDirectory(r.Root, false)
	if rootErr != nil {
		return fail("service-root")
	}
	root.Close()
	reg, err := r.Catalog.ReadRun(run)
	if err != nil || reg.RunID != run {
		return fail("registration")
	}
	dir, err := privateDirectory(filepath.Join(r.Root, "admissions"), false)
	if err != nil {
		return fail("admissions")
	}
	entries, err := dir.ReadDir(20001)
	dir.Close()
	if err != nil && err != io.EOF || len(entries) > 20000 {
		return fail("admissions")
	}
	var binding runadmission.AdmissionBindingV1
	matches := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".binding.json") {
			continue
		}
		data, e := readFile(filepath.Join(r.Root, "admissions", entry.Name()), runadmission.MaxBindingBytes, true)
		if e != nil {
			return fail("binding-read")
		}
		var b runadmission.AdmissionBindingV1
		if strictjson.Decode(data, &b) != nil {
			return fail("binding-decode")
		}
		if b.RunID == run {
			matches++
			binding = b
		}
	}
	b := binding
	if matches != 1 || b.Kind != "AdmissionBindingV1" || b.SchemaVersion != 1 || b.AuthorityDigest != reg.AuthorityDigest || b.RepositoryIdentityDigest != reg.RepositoryIdentityDigest || runtimecatalog.RepositoryIdentityDigest(b.RepositoryIdentity) != reg.RepositoryIdentityDigest || b.CanonicalLedgerPath != reg.CanonicalLedgerPath || b.CanonicalEvidenceRoot != reg.CanonicalEvidenceRoot || b.CanonicalLedgerPath != filepath.Join(b.LedgerRoot, run, "events.jsonl") || b.CanonicalEvidenceRoot != filepath.Join(b.EvidenceRoot, run) {
		return fail("binding-match")
	}
	if !filepath.IsAbs(b.RepositoryPath) || filepath.Clean(b.RepositoryPath) != b.RepositoryPath || b.InputDirectory == "" || filepath.IsAbs(b.InputDirectory) || filepath.Clean(b.InputDirectory) != b.InputDirectory || b.InputDirectory == ".." || strings.HasPrefix(b.InputDirectory, "../") {
		return fail("binding-paths")
	}
	admittedDir := filepath.Join(b.RepositoryPath, b.InputDirectory, run)
	if b.ManifestPath != filepath.Join(admittedDir, "manifest.json") || b.PlanPath != filepath.Join(admittedDir, "plan.md") {
		return fail("binding-paths")
	}
	data, err := readFile(b.ManifestPath, runadmission.MaxManifestTemplateBytes, false)
	if err != nil || digest(data) != reg.AuthorityDigest {
		return fail("manifest-digest")
	}
	var manifest authority.Manifest
	if strictjson.Decode(data, &manifest) != nil || manifest.RunID != run || manifest.Repository.Path != b.RepositoryPath || manifest.Repository.Identity != b.RepositoryIdentity || !gitSHA.MatchString(manifest.Repository.StartSHA) || !manifest.Worktree.Enabled || manifest.Worktree.Branch != "abcp/"+run || !filepath.IsAbs(manifest.Ralphex.BinaryPath) || filepath.Clean(manifest.Ralphex.BinaryPath) != manifest.Ralphex.BinaryPath || len(manifest.Ralphex.BinarySHA256) != 64 || !gitSHA.MatchString(manifest.Ralphex.SourceSHA) || manifest.Plan.Path != b.PlanPath {
		return fail("manifest")
	}
	repo, err := openDirectory(b.RepositoryPath)
	if err != nil {
		return fail("repository")
	}
	repo.Close()
	plan, err := readFile(b.PlanPath, runadmission.MaxManifestTemplateBytes, false)
	if err != nil || digest(plan) != manifest.Plan.SHA256 {
		return fail("plan-digest")
	}
	// Admission writes these metadata lines immediately after the heading.
	// Read only that controller-created prefix, never later task prose.
	project := ""
	lines := strings.Split(string(plan), "\n")
	for _, line := range lines[1:] {
		if strings.HasPrefix(line, "Project: ") {
			if project != "" {
				return fail("plan-project")
			}
			project = strings.TrimPrefix(line, "Project: ")
			continue
		}
		if strings.HasPrefix(line, "Display-Title: ") || strings.HasPrefix(line, "Repository: ") || strings.HasPrefix(line, "Admission-ID: ") {
			continue
		}
		break
	}
	return Scope{RunID: run, Repository: b.RepositoryPath, RepositoryIdentity: b.RepositoryIdentity, Project: project, Branch: manifest.Worktree.Branch, Base: manifest.Repository.StartSHA, AuthorityDigest: reg.AuthorityDigest, Ralphex: manifest.Ralphex, Retain: manifest.Worktree.Retain}, nil
}

// WorktreePath returns the path of the run's governed worktree, or "" when no
// worktree holds its branch.
func WorktreePath(ctx context.Context, scope Scope) (string, error) {
	path, err := resolveWorktree(ctx, scope)
	if errors.Is(err, errWorktreeMissing) {
		return "", nil
	}
	return path, err
}

// worktreeOffBranch reports whether path, the worktree ABCP last bound for the
// run, is still listed by Git at the same path while no worktree holds the
// run's branch, and the branch ref still exists: for example a detached HEAD
// during a rebase. Any other absence, or a Git failure, is not off-branch and
// keeps the missing-worktree rule.
func worktreeOffBranch(ctx context.Context, scope Scope, path string) bool {
	if path == "" {
		return false
	}
	text, err := git(ctx, scope.Repository, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return false
	}
	listed := false
	for _, field := range strings.Split(text, "\x00") {
		if field == "worktree "+path {
			listed = true
		}
		if field == "branch refs/heads/"+scope.Branch {
			return false
		}
	}
	if !listed {
		return false
	}
	_, err = git(ctx, scope.Repository, "rev-parse", "--verify", "--quiet", "refs/heads/"+scope.Branch+"^{commit}")
	return err == nil
}

// historyKept reports whether every checkpoint already recorded for the run is
// an ancestor of the run's branch head. A checkpoint that is not, or that Git
// cannot show to be, means the history was rewritten. An interrupted check
// returns an error so the caller tries again without deciding.
func historyKept(ctx context.Context, scope Scope, checkpoints []string) (bool, error) {
	for _, sha := range checkpoints {
		check, cancel := context.WithTimeout(ctx, 5*time.Second)
		cmd := exec.CommandContext(check, "git", "-C", scope.Repository, "merge-base", "--is-ancestor", sha, "refs/heads/"+scope.Branch)
		cmd.Env = gitexec.Environment()
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		err := cmd.Run()
		interrupted := check.Err() != nil
		cancel()
		if interrupted {
			return false, atStep("history-check", ErrUnavailable)
		}
		if err != nil {
			return false, nil
		}
	}
	return true, nil
}

// Git subprocesses are bounded and inherit the repository's existing sanitized
// Git environment, including protection from GIT_DIR/worktree overrides.
func git(ctx context.Context, path string, args ...string) (string, error) {
	return gitEnvironment(ctx, path, nil, args...)
}

func gitEnvironment(ctx context.Context, path string, extra []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...)
	cmd.Env = append(gitexec.Environment(), extra...)
	var out limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil || out.exceeded {
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			return "", atStep("git-deadline", ErrUnavailable)
		case ctx.Err() != nil:
			return "", atStep("git-canceled", ErrUnavailable)
		case out.exceeded:
			return "", atStep("git-output-limit", ErrUnavailable)
		}
		return "", atStep("git-exit", ErrUnavailable)
	}
	return strings.TrimSpace(out.String()), nil
}

// Never let index stat caches or hidden-worktree flags certify source content.
// A fresh private index starts at the immutable HEAD tree with no cached stat
// data, so really-refresh must hash the tracked worktree content independently.
// The governed index is only read, never refreshed or modified by this observer.
func checkpointClean(ctx context.Context, path, head string) bool {
	flags, err := git(ctx, path, "ls-files", "-v", "-z")
	if err != nil {
		return false
	}
	for _, entry := range strings.Split(flags, "\x00") {
		if entry != "" && !strings.HasPrefix(entry, "H ") {
			return false
		}
	}
	status, err := git(ctx, path, "status", "--porcelain", "--untracked-files=all")
	if err != nil || status != "" {
		return false
	}
	dir, err := os.MkdirTemp("", "abcp-checkpoint-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(dir, "index")}
	config := []string{"-c", "core.ignorestat=false", "-c", "core.filemode=true", "-c", "core.sparseCheckout=false", "-c", "core.splitIndex=false"}
	if _, err = gitEnvironment(ctx, path, env, append(config, "read-tree", "--no-sparse-checkout", head)...); err != nil {
		return false
	}
	if _, err = gitEnvironment(ctx, path, env, append(config, "update-index", "--really-refresh")...); err != nil {
		return false
	}
	_, err = gitEnvironment(ctx, path, env, append(config, "diff-files", "--quiet", "--no-ext-diff", "--ignore-submodules=none", "--")...)
	return err == nil
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
		return "", annotate("list", err, ErrUnavailable)
	}
	path, found := "", ""
	for _, field := range strings.Split(text, "\x00") {
		if strings.HasPrefix(field, "worktree ") {
			path = strings.TrimPrefix(field, "worktree ")
		}
		if field == "branch refs/heads/"+scope.Branch {
			if found != "" {
				return "", atStep("duplicate-branch", ErrIntegrity)
			}
			found = path
		}
	}
	if found == "" {
		return "", atStep("branch-missing", errWorktreeMissing)
	}
	d, err := openDirectory(found)
	if err != nil {
		return "", atStep("open", err)
	}
	defer d.Close()
	branch, err := git(ctx, found, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return "", annotate("head", err, ErrIntegrity)
	}
	if branch != "refs/heads/"+scope.Branch {
		return "", atStep("head-branch", ErrIntegrity)
	}
	root, err := git(ctx, found, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", annotate("toplevel", err, ErrIntegrity)
	}
	if root != found {
		return "", atStep("toplevel-path", ErrIntegrity)
	}
	common, err := git(ctx, scope.Repository, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", annotate("common-dir", err, ErrIntegrity)
	}
	workCommon, err := git(ctx, found, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", annotate("worktree-common-dir", err, ErrIntegrity)
	}
	if workCommon != common {
		return "", atStep("worktree-common-dir-path", ErrIntegrity)
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
	head, err := git(ctx, path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || !gitSHA.MatchString(head) || head == scope.Base {
		return Event{}, false
	}
	if !checkpointClean(ctx, path, head) {
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
	if !checkpointClean(ctx, path, head) {
		return Event{}, false
	}
	e := baseEvent(scope.RunID, now)
	e.AuthorityLevel, e.Category, e.Status, e.Title = "ABCP_STATE", "CHECKPOINT", "AVAILABLE", "Source checkpoint available"
	e.SourceKind, e.SourceDigest = "ABCP_CHECKPOINT_OBSERVER", identity(scope.RunID, head)
	e.CheckpointSHA, e.CheckpointClean = head, true
	e.ActivityID = identity(scope.RunID, "checkpoint", head)
	return e, true
}

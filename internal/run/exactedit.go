package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

// Exact edits (Repo C design note BF01D_QUICK_EDIT.md §4.5): a run whose
// authority carries exact replacements makes its branch with no builder and no
// model. Its first commit copies the version it changes (the source commit's
// tree, as a change build's first commit copies the version before), its
// second applies the replacements, and nothing else is written; the acceptance
// commands then run as for any build.

// maxExactEditFileBytes bounds a file an exact edit reads and rewrites.
const maxExactEditFileBytes = 2 << 20

// exactEditIdentity signs the commits an exact edit makes.
var exactEditIdentity = []string{
	"GIT_AUTHOR_NAME=Autobuild", "GIT_AUTHOR_EMAIL=builder@autobuild.invalid",
	"GIT_COMMITTER_NAME=Autobuild", "GIT_COMMITTER_EMAIL=builder@autobuild.invalid",
}

// ErrExactEditDoesNotApply refuses an exact edit whose source commit is not a
// run's, whose file changed, or whose text is not found exactly once.
var ErrExactEditDoesNotApply = errors.New("exact edit does not apply")

// exactEditFile is one replacement read at the source commit: the file's mode
// and blob there, and its content once the replacement is made.
type exactEditFile struct {
	path, mode, blob string
	updated          []byte
}

// ExactEditResultV1 is the run's evidence of what it wrote.
type ExactEditResultV1 struct {
	Kind      string                  `json:"kind"`
	SourceSHA string                  `json:"source_sha"`
	CopySHA   string                  `json:"copy_sha"`
	HeadSHA   string                  `json:"head_sha"`
	Branch    string                  `json:"branch"`
	Worktree  string                  `json:"worktree,omitempty"`
	Files     []ExactEditFileResultV1 `json:"files"`
}

// ExactEditFileResultV1 names one file the edit changed, by its blobs.
type ExactEditFileResultV1 struct {
	Path     string `json:"path"`
	BaseBlob string `json:"base_blob"`
	NewBlob  string `json:"new_blob"`
}

// CheckExactEdit reads, without writing anything, the files an exact edit names
// at its source commit and answers ErrExactEditDoesNotApply when it would be
// refused: admission calls it before a run is made.
func CheckExactEdit(ctx context.Context, repository string, edit authority.ExactEditManifest) error {
	_, err := readExactEdit(ctx, repository, edit)
	return err
}

func readExactEdit(ctx context.Context, repository string, edit authority.ExactEditManifest) ([]exactEditFile, error) {
	if _, err := gitBytes(ctx, repository, nil, nil, "cat-file", "-e", edit.SourceSHA+"^{commit}"); err != nil {
		return nil, fmt.Errorf("%w: the source commit is not in the repository", ErrExactEditDoesNotApply)
	}
	// Only a version an earlier run made can be changed this way.
	holder, err := gitBytes(ctx, repository, nil, nil, "for-each-ref", "--count=1", "--contains", edit.SourceSHA, "--format=%(refname)", "refs/heads/abcp/")
	if err != nil {
		return nil, fmt.Errorf("find the run that made the source commit: %w", err)
	}
	if len(bytes.TrimSpace(holder)) == 0 {
		return nil, fmt.Errorf("%w: no run's branch holds the source commit", ErrExactEditDoesNotApply)
	}
	files := make([]exactEditFile, 0, len(edit.Replacements))
	for _, replacement := range edit.Replacements {
		if !authority.ValidExactEditPath(replacement.Path) {
			return nil, fmt.Errorf("%w: %s: the path is refused", ErrExactEditDoesNotApply, replacement.Path)
		}
		listed, err := gitBytes(ctx, repository, nil, []string{"GIT_LITERAL_PATHSPECS=1"}, "ls-tree", "-z", "--full-tree", edit.SourceSHA, "--", replacement.Path)
		if err != nil {
			return nil, fmt.Errorf("read %s at the source commit: %w", replacement.Path, err)
		}
		entries := bytes.Split(bytes.TrimSuffix(listed, []byte{0}), []byte{0})
		if len(listed) == 0 || len(entries) != 1 {
			return nil, fmt.Errorf("%w: %s: the file is not in the source commit", ErrExactEditDoesNotApply, replacement.Path)
		}
		meta, path, ok := strings.Cut(string(entries[0]), "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 || path != replacement.Path {
			return nil, fmt.Errorf("%w: %s: the file is not in the source commit", ErrExactEditDoesNotApply, replacement.Path)
		}
		if fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
			return nil, fmt.Errorf("%w: %s: not a regular file", ErrExactEditDoesNotApply, replacement.Path)
		}
		if fields[2] != replacement.BaseBlob {
			return nil, fmt.Errorf("%w: %s: the file changed", ErrExactEditDoesNotApply, replacement.Path)
		}
		size, err := gitBytes(ctx, repository, nil, nil, "cat-file", "-s", replacement.BaseBlob)
		if err != nil {
			return nil, fmt.Errorf("read the size of %s: %w", replacement.Path, err)
		}
		if n, err := strconv.Atoi(strings.TrimSpace(string(size))); err != nil || n > maxExactEditFileBytes {
			return nil, fmt.Errorf("%w: %s: the file is larger than %d bytes", ErrExactEditDoesNotApply, replacement.Path, maxExactEditFileBytes)
		}
		content, err := gitBytes(ctx, repository, nil, nil, "cat-file", "blob", replacement.BaseBlob)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", replacement.Path, err)
		}
		find := []byte(replacement.Find)
		at := bytes.Index(content, find)
		if at < 0 {
			return nil, fmt.Errorf("%w: %s: the text is not found", ErrExactEditDoesNotApply, replacement.Path)
		}
		// Overlapping occurrences count too: the text must be found exactly once.
		if bytes.Index(content[at+1:], find) >= 0 {
			return nil, fmt.Errorf("%w: %s: the text is found more than once", ErrExactEditDoesNotApply, replacement.Path)
		}
		updated := make([]byte, 0, len(content)-len(find)+len(replacement.Replace))
		updated = append(updated, content[:at]...)
		updated = append(updated, replacement.Replace...)
		updated = append(updated, content[at+len(find):]...)
		files = append(files, exactEditFile{path: replacement.Path, mode: fields[0], blob: fields[2], updated: updated})
	}
	return files, nil
}

// makeExactEditBranch writes the run's two commits and points branch, which
// must not exist yet, at the second. It changes no checkout and no index: the
// commits are made from objects alone.
func makeExactEditBranch(ctx context.Context, repository, startSHA, branch, sourceSHA string, files []exactEditFile) (string, string, error) {
	exists, err := localBranchExists(ctx, repository, branch)
	if err != nil {
		return "", "", fmt.Errorf("check branch %q: %w", branch, err)
	}
	if exists {
		return "", "", fmt.Errorf("branch %q already exists", branch)
	}
	sourceTree, err := gitBytes(ctx, repository, nil, nil, "rev-parse", "--verify", sourceSHA+"^{tree}")
	if err != nil {
		return "", "", fmt.Errorf("read the source tree: %w", err)
	}
	copyMessage := "Copy of version " + sourceSHA + "\n"
	copySHA, err := gitBytes(ctx, repository, []byte(copyMessage), exactEditIdentity, "commit-tree", strings.TrimSpace(string(sourceTree)), "-p", startSHA, "-F", "-")
	if err != nil {
		return "", "", fmt.Errorf("write the copy commit: %w", err)
	}
	copyCommit := strings.TrimSpace(string(copySHA))

	indexDirectory, err := os.MkdirTemp("", "abcp-exact-edit-")
	if err != nil {
		return "", "", fmt.Errorf("create the edit's index directory: %w", err)
	}
	defer os.RemoveAll(indexDirectory)
	index := []string{"GIT_INDEX_FILE=" + filepath.Join(indexDirectory, "index")}
	if _, err := gitBytes(ctx, repository, nil, index, "read-tree", copyCommit); err != nil {
		return "", "", fmt.Errorf("read the copy into the edit's index: %w", err)
	}
	var entries bytes.Buffer
	for _, file := range files {
		blob, err := gitBytes(ctx, repository, file.updated, nil, "hash-object", "-w", "--no-filters", "--stdin")
		if err != nil {
			return "", "", fmt.Errorf("write %s: %w", file.path, err)
		}
		fmt.Fprintf(&entries, "%s %s\t%s\x00", file.mode, strings.TrimSpace(string(blob)), file.path)
	}
	if _, err := gitBytes(ctx, repository, entries.Bytes(), index, "update-index", "-z", "--index-info"); err != nil {
		return "", "", fmt.Errorf("put the changed files in the edit's index: %w", err)
	}
	tree, err := gitBytes(ctx, repository, nil, index, "write-tree")
	if err != nil {
		return "", "", fmt.Errorf("write the edit's tree: %w", err)
	}
	message := fmt.Sprintf("Words changed in %d file(s)\n\n", len(files))
	for _, file := range files {
		message += "- " + file.path + "\n"
	}
	head, err := gitBytes(ctx, repository, []byte(message), exactEditIdentity, "commit-tree", strings.TrimSpace(string(tree)), "-p", copyCommit, "-F", "-")
	if err != nil {
		return "", "", fmt.Errorf("write the edit commit: %w", err)
	}
	headCommit := strings.TrimSpace(string(head))
	// An empty old value creates the branch only if no one else made it meanwhile.
	if _, err := gitBytes(ctx, repository, nil, nil, "update-ref", "-m", "abcp exact edit", "refs/heads/"+branch, headCommit, ""); err != nil {
		return "", "", fmt.Errorf("create branch %q: %w", branch, err)
	}
	return copyCommit, headCommit, nil
}

// verifyExactEdit checks the branch as Git holds it, in place of a builder's
// review: the copy commit is the source commit's tree on the run's start, the
// head is one commit after it, and that commit changes exactly the declared
// files, each from its blob at the source commit to the declared new content.
// It answers each file's new blob.
func verifyExactEdit(ctx context.Context, repository, startSHA, branch, sourceSHA, copySHA, headSHA string, files []exactEditFile) ([]ExactEditFileResultV1, error) {
	tip, err := gitBytes(ctx, repository, nil, nil, "rev-parse", "--verify", "refs/heads/"+branch+"^{commit}")
	if err != nil || strings.TrimSpace(string(tip)) != headSHA {
		return nil, fmt.Errorf("branch %q does not point at the edit commit", branch)
	}
	for _, check := range []struct{ commit, parents string }{{copySHA, copySHA + " " + startSHA}, {headSHA, headSHA + " " + copySHA}} {
		parents, err := gitBytes(ctx, repository, nil, nil, "rev-list", "--parents", "-n", "1", check.commit)
		if err != nil || strings.TrimSpace(string(parents)) != check.parents {
			return nil, fmt.Errorf("commit %s does not have the expected single parent", check.commit)
		}
	}
	copyTree, err1 := gitBytes(ctx, repository, nil, nil, "rev-parse", copySHA+"^{tree}")
	sourceTree, err2 := gitBytes(ctx, repository, nil, nil, "rev-parse", sourceSHA+"^{tree}")
	if err1 != nil || err2 != nil || !bytes.Equal(copyTree, sourceTree) {
		return nil, errors.New("the copy commit does not hold the source version's files")
	}
	want := map[string]exactEditFile{}
	for _, file := range files {
		want[file.path] = file
	}
	diff, err := gitBytes(ctx, repository, nil, nil, "diff-tree", "-r", "-z", "--no-renames", "--no-commit-id", copySHA, headSHA)
	if err != nil {
		return nil, fmt.Errorf("read the edit commit's changes: %w", err)
	}
	fields := bytes.Split(bytes.TrimSuffix(diff, []byte{0}), []byte{0})
	if len(diff) == 0 || len(fields)%2 != 0 || len(fields)/2 != len(files) {
		return nil, errors.New("the edit commit does not change exactly the declared files")
	}
	results := make([]ExactEditFileResultV1, 0, len(files))
	for i := 0; i < len(fields); i += 2 {
		status := strings.Fields(strings.TrimPrefix(string(fields[i]), ":"))
		path := string(fields[i+1])
		file, ok := want[path]
		if !ok || len(status) != 5 || status[4] != "M" || status[0] != file.mode || status[1] != file.mode || status[2] != file.blob {
			return nil, fmt.Errorf("the edit commit changes %s in a way not declared", path)
		}
		expected, err := gitBytes(ctx, repository, file.updated, nil, "hash-object", "--no-filters", "--stdin")
		if err != nil {
			return nil, fmt.Errorf("hash the declared content of %s: %w", path, err)
		}
		if status[3] != strings.TrimSpace(string(expected)) {
			return nil, fmt.Errorf("the edit commit's %s is not the declared content", path)
		}
		delete(want, path)
		results = append(results, ExactEditFileResultV1{Path: path, BaseBlob: file.blob, NewBlob: status[3]})
	}
	if len(want) != 0 {
		return nil, errors.New("the edit commit does not change every declared file")
	}
	return results, nil
}

// exactEditWorktreePath is where the builder keeps a run's worktree, so the
// activity, checkpoint and eviction paths find an exact edit's the same way.
func exactEditWorktreePath(repository, branch string) string {
	return filepath.Join(repository, ".ralphex", "worktrees", filepath.FromSlash(branch))
}

// checkoutExactEdit checks the branch out in the run's worktree.
func checkoutExactEdit(ctx context.Context, repository, branch string) (string, error) {
	path := exactEditWorktreePath(repository, branch)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create the worktree's folder: %w", err)
	}
	if _, err := gitBytes(ctx, repository, nil, nil, "worktree", "add", "--quiet", path, branch); err != nil {
		return "", fmt.Errorf("check out branch %q: %w", branch, err)
	}
	return path, nil
}

func removeExactEditWorktree(ctx context.Context, repository, path string) error {
	_, err := gitBytes(context.WithoutCancel(ctx), repository, nil, nil, "worktree", "remove", "--force", path)
	return err
}

// gitBytes runs a controller-owned Git command in repository with stdin and
// the extra environment, and answers its standard output unchanged.
func gitBytes(ctx context.Context, repository string, stdin []byte, extra []string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = repository
	command.Env = append(gitexec.Environment(), extra...)
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

// runExactEdit is the implementation of an exact edit run, from
// AUTHORITY_VALIDATED to IMPLEMENTATION_COMPLETED, then the branch acceptance
// every run has. release frees the run's repository lease and slots once the
// branch and its worktree exist.
func (r *Runner) runExactEdit(ctx context.Context, result Result, edit authority.ExactEditManifest, release func() error) (Result, error) {
	if err := r.transition(ctx, domain.StateAuthorityValidated, domain.StateExecutionStarting, "governed-runner", map[string]any{
		"exact_edit": true, "source_sha": edit.SourceSHA, "replacements": len(edit.Replacements),
	}, nil); err != nil {
		return result, errors.Join(err, release())
	}
	result.State = domain.StateExecutionStarting
	if err := r.transition(ctx, domain.StateExecutionStarting, domain.StateImplementing, "exact-edit", map[string]any{"source_sha": edit.SourceSHA}, nil); err != nil {
		return result, errors.Join(err, release())
	}
	result.State = domain.StateImplementing
	evidence, err := r.applyExactEdit(ctx, edit)
	err = errors.Join(err, release())
	if err != nil {
		if ctx.Err() != nil {
			return r.cancel(ctx, result, domain.StateImplementing, "exact-edit", err, nil)
		}
		return r.fail(ctx, result, domain.StateImplementing, "exact-edit", err, nil)
	}
	ref, err := r.writeJSON("exact-edit.json", "exact-edit", evidence)
	if err != nil {
		return r.fail(ctx, result, domain.StateImplementing, "exact-edit", err, nil)
	}
	implementationRefs := []ledger.EvidenceRef{ref}
	if err := r.transition(ctx, domain.StateImplementing, domain.StateImplementationCompleted, "exact-edit", map[string]any{
		"head_sha": evidence.HeadSHA, "files": len(evidence.Files),
	}, implementationRefs); err != nil {
		return result, err
	}
	result.State = domain.StateImplementationCompleted
	return r.acceptBranch(ctx, result, implementationRefs)
}

// applyExactEdit reads, writes and checks the edit's branch and checks it out
// in the run's worktree, kept when the authority retains worktrees.
func (r *Runner) applyExactEdit(ctx context.Context, edit authority.ExactEditManifest) (ExactEditResultV1, error) {
	repository := r.governed.Repository()
	branch := r.governed.Worktree().Branch
	files, err := readExactEdit(ctx, repository.Path, edit)
	if err != nil {
		return ExactEditResultV1{}, err
	}
	copySHA, headSHA, err := makeExactEditBranch(ctx, repository.Path, repository.StartSHA, branch, edit.SourceSHA, files)
	if err != nil {
		return ExactEditResultV1{}, err
	}
	changed, err := verifyExactEdit(ctx, repository.Path, repository.StartSHA, branch, edit.SourceSHA, copySHA, headSHA, files)
	if err != nil {
		return ExactEditResultV1{}, err
	}
	worktree, err := checkoutExactEdit(ctx, repository.Path, branch)
	if err != nil {
		return ExactEditResultV1{}, err
	}
	if !r.governed.Worktree().Retain {
		if err := removeExactEditWorktree(ctx, repository.Path, worktree); err != nil {
			return ExactEditResultV1{}, fmt.Errorf("remove the edit's worktree: %w", err)
		}
		worktree = ""
	}
	return ExactEditResultV1{
		Kind: "ExactEditResultV1", SourceSHA: edit.SourceSHA, CopySHA: copySHA, HeadSHA: headSHA,
		Branch: branch, Worktree: worktree, Files: changed,
	}, nil
}

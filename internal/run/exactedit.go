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
//
// A template's copy with exact writes (Repo C design note
// TEMPLATE_LIBRARY_ROUND_2.md §10.5) is the same run for an app's first
// version: its exact edit names no source commit, and its first commit copies
// the template's dist/<template_id> tree from the profile's mirror, whose
// objects ABCP copies into the repository (templateobjects.go); the
// template-copy check of acceptance then finds the copy in that commit.

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

// ErrTemplateNotHeld refuses a template's copy with exact writes whose mirror
// does not hold the template, or holds it in objects the repository does not
// name the same way: admission answers it as template_not_accepted.
var ErrTemplateNotHeld = errors.New("the mirror does not hold the template")

var (
	errTextNotFound = errors.New("the text is not found")
	errTextTwice    = errors.New("the text is found more than once")
)

// exactEditFile is one replacement read at the source commit: the file's mode
// and blob there, and its content once the replacement is made.
type exactEditFile struct {
	path, mode, blob string
	updated          []byte
}

// ExactEditResultV1 is the run's evidence of what it wrote.
type ExactEditResultV1 struct {
	Kind      string `json:"kind"`
	SourceSHA string `json:"source_sha,omitempty"`
	// Template is the template whose copy the run's first commit is, for a
	// template's copy with exact writes; it then has no SourceSHA.
	Template *ExactEditTemplateV1    `json:"template,omitempty"`
	CopySHA  string                  `json:"copy_sha"`
	HeadSHA  string                  `json:"head_sha"`
	Branch   string                  `json:"branch"`
	Worktree string                  `json:"worktree,omitempty"`
	Files    []ExactEditFileResultV1 `json:"files"`
	// Regenerated: a site-kit website's pages the export wrote or removed after the edit, one commit after it
	// (siteexport.go); HeadSHA is then that commit.
	Regenerated []string `json:"regenerated,omitempty"`
}

// ExactEditTemplateV1 names the library template a run's copy commit holds:
// the tree of dist/<template_id> at the version tag's commit.
type ExactEditTemplateV1 struct {
	TemplateID string `json:"template_id"`
	Version    string `json:"version"`
	CommitSHA  string `json:"commit_sha"`
	TreeSHA    string `json:"tree_sha"`
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
		mode, content, err := readEditFile(ctx, inRepository(repository), edit.SourceSHA, "the source commit", replacement.Path, replacement.BaseBlob)
		if err != nil {
			return nil, err
		}
		if replacement.JSON() {
			updated, err := replaceJSONValue(content, replacement.Pointer, []byte(replacement.From), []byte(replacement.To))
			if err != nil {
				return nil, fmt.Errorf("%w: %s: %v", ErrExactEditDoesNotApply, replacement.Path, err)
			}
			files = append(files, exactEditFile{path: replacement.Path, mode: mode, blob: replacement.BaseBlob, updated: updated})
			continue
		}
		updated, err := replaceWords(content, replacement.Find, replacement.Replace)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrExactEditDoesNotApply, replacement.Path, err)
		}
		files = append(files, exactEditFile{path: replacement.Path, mode: mode, blob: replacement.BaseBlob, updated: updated})
	}
	return files, nil
}

// gitRunner runs a read-only Git command in one repository, or a bare mirror.
type gitRunner func(ctx context.Context, stdin []byte, extra []string, args ...string) ([]byte, error)

func inRepository(repository string) gitRunner {
	return func(ctx context.Context, stdin []byte, extra []string, args ...string) ([]byte, error) {
		return gitBytes(ctx, repository, stdin, extra, args...)
	}
}

// inMirror reads a bare mirror: it is named as the Git directory, and no
// command run through it writes there.
func inMirror(mirror string) gitRunner {
	return func(ctx context.Context, stdin []byte, extra []string, args ...string) ([]byte, error) {
		return gitBytes(ctx, mirror, stdin, extra, append([]string{"--git-dir=" + mirror}, args...)...)
	}
}

// readEditFile reads the file at path in tree (a commit or a tree, which where
// names in a refusal) through git: its mode and content, refused unless it is a
// regular file whose blob is baseBlob, of at most maxExactEditFileBytes.
func readEditFile(ctx context.Context, git gitRunner, tree, where, path, baseBlob string) (string, []byte, error) {
	listed, err := git(ctx, nil, []string{"GIT_LITERAL_PATHSPECS=1"}, "ls-tree", "-z", "--full-tree", tree, "--", path)
	if err != nil {
		return "", nil, fmt.Errorf("read %s at %s: %w", path, where, err)
	}
	entries := bytes.Split(bytes.TrimSuffix(listed, []byte{0}), []byte{0})
	if len(listed) == 0 || len(entries) != 1 {
		return "", nil, fmt.Errorf("%w: %s: the file is not in %s", ErrExactEditDoesNotApply, path, where)
	}
	meta, listedPath, ok := strings.Cut(string(entries[0]), "\t")
	fields := strings.Fields(meta)
	if !ok || len(fields) != 3 || listedPath != path {
		return "", nil, fmt.Errorf("%w: %s: the file is not in %s", ErrExactEditDoesNotApply, path, where)
	}
	if fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
		return "", nil, fmt.Errorf("%w: %s: not a regular file", ErrExactEditDoesNotApply, path)
	}
	if fields[2] != baseBlob {
		return "", nil, fmt.Errorf("%w: %s: the file changed", ErrExactEditDoesNotApply, path)
	}
	size, err := git(ctx, nil, nil, "cat-file", "-s", baseBlob)
	if err != nil {
		return "", nil, fmt.Errorf("read the size of %s: %w", path, err)
	}
	if n, err := strconv.Atoi(strings.TrimSpace(string(size))); err != nil || n > maxExactEditFileBytes {
		return "", nil, fmt.Errorf("%w: %s: the file is larger than %d bytes", ErrExactEditDoesNotApply, path, maxExactEditFileBytes)
	}
	content, err := git(ctx, nil, nil, "cat-file", "blob", baseBlob)
	if err != nil {
		return "", nil, fmt.Errorf("read %s: %w", path, err)
	}
	return fields[0], content, nil
}

// replaceWords puts replace in the place of find, which must be in content
// exactly once (overlapping occurrences count too).
func replaceWords(content []byte, find, replace string) ([]byte, error) {
	at := bytes.Index(content, []byte(find))
	if at < 0 {
		return nil, errTextNotFound
	}
	if bytes.Index(content[at+1:], []byte(find)) >= 0 {
		return nil, errTextTwice
	}
	updated := make([]byte, 0, len(content)-len(find)+len(replace))
	updated = append(updated, content[:at]...)
	updated = append(updated, replace...)
	updated = append(updated, content[at+len(find):]...)
	return updated, nil
}

// CheckTemplateExactEdit reads, without writing anything, the mirror's
// template and the files a template's copy with exact writes names in its tree
// (Repo C design note TEMPLATE_LIBRARY_ROUND_2.md §10.5), and answers
// ErrTemplateNotHeld when the mirror cannot give the repository the template,
// or ErrExactEditDoesNotApply when the writes would be refused: admission calls
// it before a run is made. Neither the repository nor the mirror is written.
func CheckTemplateExactEdit(ctx context.Context, repository string, template authority.TemplateCopyManifest, edit authority.ExactEditManifest) error {
	_, err := readTemplateExactEdit(ctx, repository, template, edit)
	return err
}

// readTemplateExactEdit reads the files of a template's copy with exact writes
// from the mirror: each file once, from its blob in the template's tree, with
// all of its replacements made (several JSON values, or one replacement of
// words); the file before and after is at most maxExactEditFileBytes.
func readTemplateExactEdit(ctx context.Context, repository string, template authority.TemplateCopyManifest, edit authority.ExactEditManifest) ([]exactEditFile, error) {
	if err := authority.ValidateTemplateExactEdit(edit); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrExactEditDoesNotApply, err)
	}
	if err := requireTemplateHeld(ctx, repository, template); err != nil {
		return nil, err
	}
	var order []string
	byPath := map[string][]authority.ExactEditReplacement{}
	for _, replacement := range edit.Replacements {
		if byPath[replacement.Path] == nil {
			order = append(order, replacement.Path)
		}
		byPath[replacement.Path] = append(byPath[replacement.Path], replacement)
	}
	where := "the template " + template.TemplateID + " " + template.Version
	files := make([]exactEditFile, 0, len(order))
	for _, path := range order {
		replacements := byPath[path]
		blob := replacements[0].BaseBlob
		for _, replacement := range replacements[1:] {
			if replacement.BaseBlob != blob {
				return nil, fmt.Errorf("%w: %s: the file changed", ErrExactEditDoesNotApply, path)
			}
		}
		mode, content, err := readEditFile(ctx, inMirror(template.MirrorPath), template.TreeSHA, where, path, blob)
		if err != nil {
			return nil, err
		}
		var updated []byte
		if replacements[0].JSON() {
			writes := make([]jsonWrite, 0, len(replacements))
			for _, replacement := range replacements {
				writes = append(writes, jsonWrite{pointer: replacement.Pointer, from: []byte(replacement.From), to: []byte(replacement.To)})
			}
			updated, err = replaceJSONValues(content, writes)
		} else {
			// Words are their file's only replacement (authority.ValidateTemplateExactEdit).
			updated, err = replaceWords(content, replacements[0].Find, replacements[0].Replace)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrExactEditDoesNotApply, path, err)
		}
		if len(updated) > maxExactEditFileBytes {
			return nil, fmt.Errorf("%w: %s: the file written would be larger than %d bytes", ErrExactEditDoesNotApply, path, maxExactEditFileBytes)
		}
		files = append(files, exactEditFile{path: path, mode: mode, blob: blob, updated: updated})
	}
	return files, nil
}

// makeExactEditBranch writes the run's two commits and points branch, which
// must not exist yet, at the second. It changes no checkout and no index: the
// commits are made from objects alone.
func makeExactEditBranch(ctx context.Context, repository, startSHA, branch, sourceSHA string, files []exactEditFile) (string, string, error) {
	sourceTree, err := gitBytes(ctx, repository, nil, nil, "rev-parse", "--verify", sourceSHA+"^{tree}")
	if err != nil {
		return "", "", fmt.Errorf("read the source tree: %w", err)
	}
	return makeEditBranch(ctx, repository, startSHA, branch, strings.TrimSpace(string(sourceTree)), "Copy of version "+sourceSHA+"\n", "Words changed", files)
}

// makeEditBranch is makeExactEditBranch for any copy: its first commit holds
// copyTree on the start, with copyMessage, and its second, titled
// "<editTitle> in <n> file(s)", the files' new content.
func makeEditBranch(ctx context.Context, repository, startSHA, branch, copyTree, copyMessage, editTitle string, files []exactEditFile) (string, string, error) {
	exists, err := localBranchExists(ctx, repository, branch)
	if err != nil {
		return "", "", fmt.Errorf("check branch %q: %w", branch, err)
	}
	if exists {
		return "", "", fmt.Errorf("branch %q already exists", branch)
	}
	copySHA, err := gitBytes(ctx, repository, []byte(copyMessage), exactEditIdentity, "commit-tree", copyTree, "-p", startSHA, "-F", "-")
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
	message := fmt.Sprintf("%s in %d file(s)\n\n", editTitle, len(files))
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
	sourceTree, err := gitBytes(ctx, repository, nil, nil, "rev-parse", sourceSHA+"^{tree}")
	if err != nil {
		return nil, errors.New("the copy commit does not hold the source version's files")
	}
	return verifyEditBranch(ctx, repository, startSHA, branch, strings.TrimSpace(string(sourceTree)), "the source version's files", copySHA, headSHA, files)
}

// verifyEditBranch is verifyExactEdit for any copy: the copy commit's tree is
// copyTree (what names in a refusal).
func verifyEditBranch(ctx context.Context, repository, startSHA, branch, copyTree, what, copySHA, headSHA string, files []exactEditFile) ([]ExactEditFileResultV1, error) {
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
	held, err := gitBytes(ctx, repository, nil, nil, "rev-parse", copySHA+"^{tree}")
	if err != nil || strings.TrimSpace(string(held)) != copyTree {
		return nil, errors.New("the copy commit does not hold " + what)
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
	// The ledger names the version an edit changes, or the template whose copy it writes on: never a file or words.
	starting := map[string]any{"exact_edit": true, "source_sha": edit.SourceSHA, "replacements": len(edit.Replacements)}
	implementing := map[string]any{"source_sha": edit.SourceSHA}
	if template, ok := r.templateWrites(edit); ok {
		starting = map[string]any{"exact_edit": true, "template_id": template.TemplateID, "template_version": template.Version, "replacements": len(edit.Replacements)}
		implementing = map[string]any{"template_id": template.TemplateID, "template_version": template.Version}
	}
	if err := r.transition(ctx, domain.StateAuthorityValidated, domain.StateExecutionStarting, "governed-runner", starting, nil); err != nil {
		return result, errors.Join(err, release())
	}
	result.State = domain.StateExecutionStarting
	if err := r.transition(ctx, domain.StateExecutionStarting, domain.StateImplementing, "exact-edit", implementing, nil); err != nil {
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
	return r.acceptBranch(ctx, result, implementationRefs, false)
}

// templateWrites answers the template whose copy the run's exact edit writes
// on (Repo C TEMPLATE_LIBRARY_ROUND_2 §10.5): an edit with no source commit,
// beside the authority's template copy.
func (r *Runner) templateWrites(edit authority.ExactEditManifest) (authority.TemplateCopyManifest, bool) {
	template, ok := r.governed.TemplateCopy()
	return template, ok && edit.SourceSHA == ""
}

// applyExactEdit reads, writes and checks the edit's branch and checks it out
// in the run's worktree, kept when the authority retains worktrees.
func (r *Runner) applyExactEdit(ctx context.Context, edit authority.ExactEditManifest) (ExactEditResultV1, error) {
	repository := r.governed.Repository()
	branch := r.governed.Worktree().Branch
	var (
		copySHA, headSHA string
		changed          []ExactEditFileResultV1
		named            *ExactEditTemplateV1
	)
	if template, ok := r.templateWrites(edit); ok {
		// Everything is read and checked before anything is written: the template's objects, then the two commits.
		files, err := readTemplateExactEdit(ctx, repository.Path, template, edit)
		if err != nil {
			return ExactEditResultV1{}, err
		}
		if exists, err := localBranchExists(ctx, repository.Path, branch); err != nil || exists {
			return ExactEditResultV1{}, fmt.Errorf("branch %q already exists or cannot be read: %v", branch, err)
		}
		if err := copyTemplateObjects(ctx, template.MirrorPath, repository.Path, template.TreeSHA); err != nil {
			return ExactEditResultV1{}, err
		}
		copyMessage := fmt.Sprintf("Copy of template %s %s\n\nThe library's dist/%s at %s.\n", template.TemplateID, template.Version, template.TemplateID, template.CommitSHA)
		copySHA, headSHA, err = makeEditBranch(ctx, repository.Path, repository.StartSHA, branch, template.TreeSHA, copyMessage, "Settings written", files)
		if err != nil {
			return ExactEditResultV1{}, err
		}
		what := "the template " + template.TemplateID + " " + template.Version + "'s files"
		if changed, err = verifyEditBranch(ctx, repository.Path, repository.StartSHA, branch, template.TreeSHA, what, copySHA, headSHA, files); err != nil {
			return ExactEditResultV1{}, err
		}
		named = &ExactEditTemplateV1{TemplateID: template.TemplateID, Version: template.Version, CommitSHA: template.CommitSHA, TreeSHA: template.TreeSHA}
	} else {
		files, err := readExactEdit(ctx, repository.Path, edit)
		if err != nil {
			return ExactEditResultV1{}, err
		}
		copySHA, headSHA, err = makeExactEditBranch(ctx, repository.Path, repository.StartSHA, branch, edit.SourceSHA, files)
		if err != nil {
			return ExactEditResultV1{}, err
		}
		if changed, err = verifyExactEdit(ctx, repository.Path, repository.StartSHA, branch, edit.SourceSHA, copySHA, headSHA, files); err != nil {
			return ExactEditResultV1{}, err
		}
	}
	wall, err := r.exportWall(ctx, repository.Path, headSHA)
	if err != nil {
		return ExactEditResultV1{}, err
	}
	regenerated, headSHA, err := exportSitePages(ctx, wall, repository.Path, branch, headSHA)
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
		Kind: "ExactEditResultV1", SourceSHA: edit.SourceSHA, Template: named, CopySHA: copySHA, HeadSHA: headSHA,
		Branch: branch, Worktree: worktree, Files: changed, Regenerated: regenerated,
	}, nil
}

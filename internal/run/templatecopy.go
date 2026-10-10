package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
)

const (
	// maxTemplateCopyCommits bounds how far into a run's history the copy is
	// looked for: the builder copies the template before anything else.
	maxTemplateCopyCommits = 16
	// maxTemplateCopyFiles bounds the template trees ABCP compares.
	maxTemplateCopyFiles = 20000
)

// requireTemplateCopy checks that one of the run's first commits after its start
// holds every file of the template's tree with the same content (Repo C design
// note DECORATION.md, phase 2): a build that skipped the copy, or changed a file
// while copying, is refused before acceptance. The mirror is only read. It
// answers the commit that holds the copy.
func requireTemplateCopy(ctx context.Context, repository, startSHA, headSHA string, template authority.TemplateCopyManifest) (string, error) {
	tree, err := mirrorOutput(ctx, template.MirrorPath, "rev-parse", "--verify", template.CommitSHA+":dist/"+template.TemplateID)
	if err != nil || tree != template.TreeSHA {
		return "", fmt.Errorf("template %s %s: the mirror's tree does not match the run's", template.TemplateID, template.Version)
	}
	want, err := treeBlobs(ctx, "", template.MirrorPath, template.TreeSHA)
	if err != nil {
		return "", fmt.Errorf("template %s %s: read the template's files: %w", template.TemplateID, template.Version, err)
	}
	if len(want) == 0 {
		return "", fmt.Errorf("template %s %s: the template has no files", template.TemplateID, template.Version)
	}
	listed, err := gitOutput(ctx, repository, "rev-list", "--reverse", startSHA+".."+headSHA)
	if err != nil {
		return "", fmt.Errorf("template %s %s: list the run's commits: %w", template.TemplateID, template.Version, err)
	}
	commits := strings.Fields(listed)
	if len(commits) > maxTemplateCopyCommits {
		commits = commits[:maxTemplateCopyCommits]
	}
	bestMissing, bestPath := len(want)+1, ""
	for _, commit := range commits {
		have, err := treeBlobs(ctx, repository, "", commit)
		if err != nil {
			return "", fmt.Errorf("template %s %s: read commit %s: %w", template.TemplateID, template.Version, commit, err)
		}
		missing, first := 0, ""
		for path, object := range want {
			if have[path] != object {
				missing++
				if first == "" || path < first {
					first = path
				}
			}
		}
		if missing == 0 {
			return commit, nil
		}
		if missing < bestMissing {
			bestMissing, bestPath = missing, first
		}
	}
	if bestPath == "" {
		return "", fmt.Errorf("template %s %s: the run made no commit that holds the template's files", template.TemplateID, template.Version)
	}
	return "", fmt.Errorf("template %s %s: none of the run's first %d commits holds the template's files unchanged (%d of %d differ or are missing, first %s)",
		template.TemplateID, template.Version, len(commits), bestMissing, len(want), bestPath)
}

// treeBlobs lists a tree's files, each path with its blob, from a working
// repository or a bare mirror. Submodules and other non-file entries are left out.
func treeBlobs(ctx context.Context, repository, mirror, treeish string) (map[string]string, error) {
	var command *exec.Cmd
	if mirror != "" {
		command = exec.CommandContext(ctx, "git", "--git-dir="+mirror, "ls-tree", "-r", "-z", "--full-tree", treeish)
	} else {
		command = exec.CommandContext(ctx, "git", "ls-tree", "-r", "-z", "--full-tree", treeish)
		command.Dir = repository
	}
	command.Env = gitexec.Environment()
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	files := map[string]string{}
	for _, entry := range bytes.Split(output, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		meta, path, ok := strings.Cut(string(entry), "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 {
			return nil, errors.New("unexpected git ls-tree output")
		}
		if fields[1] != "blob" {
			continue
		}
		files[path] = fields[2]
		if len(files) > maxTemplateCopyFiles {
			return nil, fmt.Errorf("more than %d files", maxTemplateCopyFiles)
		}
	}
	return files, nil
}

func mirrorOutput(ctx context.Context, mirror string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"--git-dir=" + mirror}, args...)...)
	command.Env = gitexec.Environment()
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

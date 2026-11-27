package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
)

// A template's copy with exact writes (Repo C design note TEMPLATE_LIBRARY_ROUND_2.md §10.5) commits the template's tree
// in the product repository, so the repository must hold its objects. They come from the profile's mirror, which ABCP
// only reads: the mirror lists the tree's objects (rev-list) and packs exactly them onto a pipe (pack-objects
// --stdout), and the repository unpacks the pack as loose objects (unpack-objects skips those it holds already). No
// ref, alternate, configuration or other file is written in the mirror, and none but those objects in the repository:
// the library's history and its other templates stay out, and the copy commit then keeps the objects it names.

// requireTemplateHeld checks, only reading, that the mirror holds the template: its commit, whose dist/<template_id>
// is the template's tree, named as the repository names objects (a SHA-256 library cannot give a SHA-1 repository its
// tree). It answers ErrTemplateNotHeld otherwise.
func requireTemplateHeld(ctx context.Context, repository string, template authority.TemplateCopyManifest) error {
	mirror := inMirror(template.MirrorPath)
	if _, err := mirror(ctx, nil, nil, "cat-file", "-e", template.CommitSHA+"^{commit}"); err != nil {
		return fmt.Errorf("%w: template %s %s: the commit is not in the mirror", ErrTemplateNotHeld, template.TemplateID, template.Version)
	}
	tree, err := mirror(ctx, nil, nil, "rev-parse", "--verify", template.CommitSHA+":dist/"+template.TemplateID)
	if err != nil || strings.TrimSpace(string(tree)) != template.TreeSHA {
		return fmt.Errorf("%w: template %s %s: the mirror's tree does not match the run's", ErrTemplateNotHeld, template.TemplateID, template.Version)
	}
	mirrorFormat, err1 := mirror(ctx, nil, nil, "rev-parse", "--show-object-format")
	repositoryFormat, err2 := gitBytes(ctx, repository, nil, nil, "rev-parse", "--show-object-format")
	if err := errors.Join(err1, err2); err != nil {
		return fmt.Errorf("read the object formats of the mirror and the repository: %w", err)
	}
	if !bytes.Equal(bytes.TrimSpace(mirrorFormat), bytes.TrimSpace(repositoryFormat)) {
		return fmt.Errorf("%w: template %s %s: the mirror names objects by %s, the repository by %s", ErrTemplateNotHeld,
			template.TemplateID, template.Version, bytes.TrimSpace(mirrorFormat), bytes.TrimSpace(repositoryFormat))
	}
	return nil
}

// copyTemplateObjects copies tree and every tree and blob under it from the mirror into the repository, and checks
// that the repository then holds each of them.
func copyTemplateObjects(ctx context.Context, mirror, repository, tree string) error {
	listed, err := inMirror(mirror)(ctx, nil, nil, "rev-list", "--objects", "--no-object-names", tree)
	if err != nil {
		return fmt.Errorf("list the template's objects: %w", err)
	}
	objects := strings.Fields(string(listed))
	if len(objects) == 0 || objects[0] != tree {
		return errors.New("list the template's objects: the mirror listed no tree")
	}
	list := []byte(strings.Join(objects, "\n") + "\n")

	pack := exec.CommandContext(ctx, "git", "--git-dir="+mirror, "pack-objects", "--stdout", "-q")
	pack.Dir = mirror
	pack.Env = gitexec.Environment()
	pack.Stdin = bytes.NewReader(list)
	unpack := exec.CommandContext(ctx, "git", "unpack-objects", "-q")
	unpack.Dir = repository
	unpack.Env = gitexec.Environment()
	reader, writer, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("copy the template's objects: %w", err)
	}
	var packErr, unpackErr bytes.Buffer
	pack.Stdout, pack.Stderr = writer, &packErr
	unpack.Stdin, unpack.Stderr = reader, &unpackErr
	if err := unpack.Start(); err != nil {
		reader.Close()
		writer.Close()
		return fmt.Errorf("copy the template's objects: %w", err)
	}
	reader.Close()
	startErr := pack.Start()
	writer.Close()
	var waitPack error
	if startErr == nil {
		waitPack = pack.Wait()
	}
	waitUnpack := unpack.Wait()
	if startErr != nil || waitPack != nil || waitUnpack != nil {
		return fmt.Errorf("copy the template's objects: pack: %v %v: %s; unpack: %v: %s", startErr, waitPack,
			strings.TrimSpace(packErr.String()), waitUnpack, strings.TrimSpace(unpackErr.String()))
	}

	held, err := gitBytes(ctx, repository, list, nil, "cat-file", "--batch-check=%(objectname) %(objecttype)")
	if err != nil {
		return fmt.Errorf("check the template's objects: %w", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(held), "\n"), "\n")
	if len(lines) != len(objects) {
		return errors.New("check the template's objects: the repository answered for another number of objects")
	}
	for i, line := range lines {
		name, kind, _ := strings.Cut(line, " ")
		if name != objects[i] || (kind != "tree" && kind != "blob") {
			return fmt.Errorf("check the template's objects: the repository does not hold %s", objects[i])
		}
	}
	return nil
}

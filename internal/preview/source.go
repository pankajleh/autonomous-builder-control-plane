package preview

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
)

type boundedBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		b.overflow = true
		return 0, ErrUnavailable
	}
	return b.Buffer.Write(p)
}
func execute(ctx context.Context, binary string, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	if configureProcess(cmd) != nil {
		return "", ErrUnavailable
	}
	cmd.Cancel = func() error { return cancelProcess(cmd) }
	cmd.WaitDelay = time.Second
	defer cancelProcess(cmd)
	cmd.Env = env
	var out boundedBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil || out.overflow {
		return "", ErrUnavailable
	}
	return strings.TrimSpace(out.String()), nil
}
func git(ctx context.Context, path string, args ...string) (string, error) {
	return execute(ctx, "git", gitexec.Environment(), append([]string{"-C", path}, args...)...)
}

type Materializer interface {
	Materialize(context.Context, string, Source) (string, error)
	Remove(string) error
	Reconcile() error
}
type Checkout struct{ Root string }

func (c Checkout) Materialize(ctx context.Context, id string, s Source) (path string, err error) {
	if !sha256Pattern.MatchString(id) || !gitSHA.MatchString(s.SHA) || !filepath.IsAbs(s.Repository) || filepath.Clean(s.Repository) != s.Repository {
		return "", ErrIntegrity
	}
	d, err := privateDirectory(c.Root, true)
	if err != nil {
		return "", err
	}
	d.Close()
	path = filepath.Join(c.Root, id)
	if _, err = os.Lstat(path); !os.IsNotExist(err) {
		return "", ErrIntegrity
	}
	success := false
	defer func() {
		if !success {
			_ = c.Remove(id)
		}
	}()
	// Clone into a fresh repository, so candidate .gitattributes cannot activate
	// executable filters from the governed repository's local configuration.
	// --no-local avoids hardlinks and permits the depth bound for local transport.
	_, err = execute(ctx, "git", gitexec.Environment(), "-c", "protocol.file.allow=always", "clone", "--no-local", "--no-checkout", "--no-tags", "--depth=1", "--single-branch", "--branch", s.Branch, "--", s.Repository, path)
	if err != nil {
		return "", err
	}
	if _, err = git(ctx, path, "-c", "protocol.file.allow=always", "fetch", "--no-tags", "--depth=1", "origin", s.SHA); err != nil {
		return "", err
	}
	if _, err = git(ctx, path, "-c", "core.hooksPath="+os.DevNull, "checkout", "--detach", s.SHA, "--"); err != nil {
		return "", err
	}
	head, e := git(ctx, path, "rev-parse", "--verify", "HEAD^{commit}")
	if e != nil || head != s.SHA {
		return "", ErrIntegrity
	}
	if _, e = git(ctx, path, "symbolic-ref", "--quiet", "HEAD"); e == nil {
		return "", ErrIntegrity
	}
	// Drop origin metadata so container source readers learn no host path.
	if _, err = git(ctx, path, "remote", "remove", "origin"); err != nil {
		return "", err
	}
	// Fetch records and reflogs also contain the local origin path and the
	// controller's generated committer identity; neither is candidate source.
	for _, name := range []string{"FETCH_HEAD", "logs"} {
		if err = os.RemoveAll(filepath.Join(path, ".git", name)); err != nil {
			return "", err
		}
	}
	if err = readableSource(path); err != nil {
		return "", err
	}
	success = true
	return path, nil
}

// The bind root must be readable by any configured non-root container UID,
// independently of the controller's umask. Its parent remains private (0700).
// WalkDir never follows candidate symlinks, and executable files stay executable.
func readableSource(path string) error {
	return filepath.WalkDir(path, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.Type()&os.ModeSymlink != 0 {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return ErrIntegrity
		}
		mode := os.FileMode(0644)
		if info.IsDir() || info.Mode().Perm()&0111 != 0 {
			mode = 0755
		}
		return os.Chmod(name, mode)
	})
}

func (c Checkout) Remove(id string) error {
	if !sha256Pattern.MatchString(id) {
		return ErrIntegrity
	}
	d, err := privateDirectory(c.Root, false)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	d.Close()
	// RemoveAll never follows symlinks. IDs are generated digests, not paths.
	return os.RemoveAll(filepath.Join(c.Root, id))
}
func (c Checkout) Reconcile() error {
	d, err := privateDirectory(c.Root, true)
	if err != nil {
		return err
	}
	defer d.Close()
	entries, err := d.ReadDir(1001)
	if err != nil && err != io.EOF {
		return err
	}
	if len(entries) > 1000 {
		return ErrIntegrity
	}
	for _, e := range entries {
		if !sha256Pattern.MatchString(e.Name()) {
			return ErrIntegrity
		}
		if err = c.Remove(e.Name()); err != nil {
			return err
		}
	}
	return nil
}

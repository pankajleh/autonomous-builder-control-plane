//go:build linux

package housekeeping

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
)

// Linux values the syscall package does not name on every architecture.
const (
	oPath       = 0x200000 // O_PATH
	atRemoveDir = 0x200    // AT_REMOVEDIR

	maxSessionDepth = 16
	// maxFirstRecord bounds the read of a Codex log's first record (its session_meta, which carries the engine's
	// instructions).
	maxFirstRecord = 1 << 20
)

// sessionSweep walks one engine session folder by descriptors (RemoveOldSessionLogs).
type sessionSweep struct {
	cutoff   time.Time
	finished func(string) bool
	entries  int
	removed  int
}

func removeOldSessionLogs(root string, cutoff time.Time, finished func(string) bool) (int, error) {
	fd, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	dir := os.NewFile(uintptr(fd), root)
	defer dir.Close()
	s := &sessionSweep{cutoff: cutoff, finished: finished}
	names, err := s.names(dir)
	if err != nil {
		return 0, err
	}
	for _, name := range names {
		switch {
		case name == "runs":
			err = s.runs(dir)
		case buildProject.MatchString(name):
			// A build's Claude Code project folder from before builds were walled.
			if s.finished(buildProject.FindStringSubmatch(name)[1]) {
				err = s.tree(dir, name, 1)
			}
		case datedFolder(name, 1):
			err = s.dated(dir, name, 1)
		}
		if err != nil {
			return s.removed, err
		}
	}
	return s.removed, nil
}

// runs ages the session folders of finished runs in runs/.
func (s *sessionSweep) runs(parent *os.File) error {
	dir, err := openDirectory(parent, "runs")
	if err != nil || dir == nil {
		return err
	}
	defer dir.Close()
	names, err := s.names(dir)
	if err != nil {
		return err
	}
	for _, run := range names {
		if !runtimecatalog.ValidIdentifier(run) || !s.finished(run) {
			continue
		}
		if err := s.tree(dir, run, 1); err != nil {
			return err
		}
	}
	return nil
}

// tree removes, in parent's folder name, every file or link last changed before the cutoff, then each of its folders
// (itself included) left empty that was last changed before then. A memory/ folder is never entered.
func (s *sessionSweep) tree(parent *os.File, name string, depth int) error {
	if depth > maxSessionDepth || name == "memory" {
		return nil
	}
	before, ok, err := lstatAt(parent, name)
	if err != nil || !ok || before.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return err
	}
	dir, err := openDirectory(parent, name)
	if err != nil || dir == nil {
		return err
	}
	names, err := s.names(dir)
	if err != nil {
		dir.Close()
		return err
	}
	for _, child := range names {
		if child == "memory" {
			continue
		}
		st, ok, err := lstatAt(dir, child)
		if err != nil {
			dir.Close()
			return err
		}
		if !ok {
			continue
		}
		switch st.Mode & syscall.S_IFMT {
		case syscall.S_IFDIR:
			err = s.tree(dir, child, depth+1)
		case syscall.S_IFREG, syscall.S_IFLNK:
			if changedAt(st).Before(s.cutoff) {
				err = s.unlink(dir, child)
			}
		}
		if err != nil {
			dir.Close()
			return err
		}
	}
	dir.Close()
	// Its own time from before this walk: a folder this walk emptied, unchanged for 30 days but for that, goes.
	if changedAt(before).Before(s.cutoff) {
		_ = unlinkAt(parent, name, atRemoveDir) // a folder that still holds anything stays
	}
	return nil
}

// dated finds Codex logs in YYYY/MM/DD folders whose first record names a finished build's working folder, and removes
// those last changed before the cutoff. Every other log there (a person's own Codex sessions) stays, as do the folders.
func (s *sessionSweep) dated(parent *os.File, name string, depth int) error {
	dir, err := openDirectory(parent, name)
	if err != nil || dir == nil {
		return err
	}
	defer dir.Close()
	names, err := s.names(dir)
	if err != nil {
		return err
	}
	for _, child := range names {
		if depth < 3 {
			if datedFolder(child, depth+1) {
				if err := s.dated(dir, child, depth+1); err != nil {
					return err
				}
			}
			continue
		}
		if !strings.HasPrefix(child, "rollout-") || !strings.HasSuffix(child, ".jsonl") {
			continue
		}
		st, ok, err := lstatAt(dir, child)
		if err != nil {
			return err
		}
		if !ok || st.Mode&syscall.S_IFMT != syscall.S_IFREG || !changedAt(st).Before(s.cutoff) {
			continue
		}
		first, err := firstRecord(dir, child)
		if err != nil {
			return err
		}
		if run := codexLogRun(first); run != "" && s.finished(run) {
			if err := s.unlink(dir, child); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *sessionSweep) unlink(dir *os.File, name string) error {
	if err := unlinkAt(dir, name, 0); err != nil && !errors.Is(err, syscall.ENOENT) {
		return err
	}
	s.removed++
	return nil
}

// names lists the folder dir holds open, counting its entries against the walk's bound.
func (s *sessionSweep) names(dir *os.File) ([]string, error) {
	var all []string
	for {
		names, err := dir.Readdirnames(1024)
		all = append(all, names...)
		if s.entries += len(names); s.entries > maxSessionEntries {
			return nil, errInvalid
		}
		if errors.Is(err, io.EOF) {
			return all, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// openDirectory opens the folder name in parent, never through a link; nil when it is not there or not a folder.
func openDirectory(parent *os.File, name string) (*os.File, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

// lstatAt is name's status in dir, of a link itself, never of what it names; ok=false when it is not there.
func lstatAt(dir *os.File, name string) (syscall.Stat_t, bool, error) {
	var st syscall.Stat_t
	fd, err := syscall.Openat(int(dir.Fd()), name, oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) {
		return st, false, nil
	}
	if err != nil {
		return st, false, err
	}
	defer syscall.Close(fd)
	if err := syscall.Fstat(fd, &st); err != nil {
		return st, false, err
	}
	return st, true, nil
}

// firstRecord reads a log's first line, at most maxFirstRecord bytes; nil when it has none that fits.
func firstRecord(dir *os.File, name string) ([]byte, error) {
	fd, err := syscall.Openat(int(dir.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ELOOP) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	if info, err := file.Stat(); err != nil || !info.Mode().IsRegular() {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFirstRecord))
	if err != nil {
		return nil, err
	}
	line, _, found := bytes.Cut(data, []byte{'\n'})
	if !found {
		return nil, nil
	}
	return line, nil
}

// unlinkAt removes name from dir: a file or a link (never what it names), or with atRemoveDir an empty folder.
func unlinkAt(dir *os.File, name string, flags int) error {
	path, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	_, _, errno := syscall.Syscall(syscall.SYS_UNLINKAT, dir.Fd(), uintptr(unsafe.Pointer(path)), uintptr(flags))
	if errno != 0 {
		return errno
	}
	return nil
}

func changedAt(st syscall.Stat_t) time.Time { return time.Unix(st.Mtim.Unix()) }

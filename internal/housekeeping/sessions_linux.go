//go:build linux

package housekeeping

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strconv"
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

// The system calls the walk makes, replaced only by tests to make an entry fail as a build could make it.
var (
	sysOpenat   = syscall.Openat
	sysUnlinkat = unlinkat
)

// sessionSweep walks one engine session folder by descriptors (RemoveOldSessionLogs).
type sessionSweep struct {
	cutoff   time.Time
	lane     bool
	finished func(string) bool
	entries  int
	result   SessionSweep
	first    error
}

func removeOldSessionLogs(root string, cutoff time.Time, policy SessionPolicy, finished func(string) bool) (SessionSweep, error) {
	fd, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return SessionSweep{}, err
	}
	dir := os.NewFile(uintptr(fd), root)
	defer dir.Close()
	s := &sessionSweep{cutoff: cutoff, lane: policy == LaneFolder, finished: finished}
	names, err := s.names(dir)
	if err != nil {
		return SessionSweep{}, err
	}
	for _, name := range names {
		switch {
		case name == "memory":
		case name == "runs":
			err = s.runs(dir)
		case buildProject.MatchString(name):
			// A build's Claude Code project folder from before builds were walled: only once the build has finished.
			if s.finished(buildProject.FindStringSubmatch(name)[1]) {
				err = s.tree(dir, name, 1, false, false)
			}
		case s.lane:
			// A lane's own folder: everything else ages too (a change job's sessions among it), but a running
			// build's Codex logs.
			err = s.entry(dir, name, 1, true)
		case datedFolder(name, 1):
			err = s.dated(dir, name, 1)
		}
		if err != nil {
			return s.result, err
		}
	}
	return s.result, s.first
}

// skip counts an entry the walk could not read or remove, and keeps the first such error, so one odd entry (a folder
// a build made unreadable, a file swapped for a folder) never stops the rest of the sweep.
func (s *sessionSweep) skip(err error) {
	s.result.Skipped++
	if s.first == nil {
		s.first = err
	}
}

// runs ages the session folders of finished runs in runs/. A run's own folder is the build's to have left in any
// state, so a folder in it the build made unreadable is made readable again.
func (s *sessionSweep) runs(parent *os.File) error {
	dir, err := openDirectory(parent, "runs", false)
	if err != nil || dir == nil {
		if err != nil {
			s.skip(err)
		}
		return nil
	}
	defer dir.Close()
	names, err := s.names(dir)
	if err != nil {
		return s.namesFailed(err)
	}
	for _, run := range names {
		if !runtimecatalog.ValidIdentifier(run) || !s.finished(run) {
			continue
		}
		if err := s.tree(dir, run, 1, true, false); err != nil {
			return err
		}
	}
	return nil
}

// entry ages one entry of parent: a folder's tree, or a file or link last changed before the cutoff.
func (s *sessionSweep) entry(parent *os.File, name string, depth int, rollouts bool) error {
	st, ok, err := lstatAt(parent, name)
	if err != nil {
		s.skip(err)
		return nil
	}
	if !ok {
		return nil
	}
	switch st.Mode & syscall.S_IFMT {
	case syscall.S_IFDIR:
		return s.tree(parent, name, depth, false, rollouts)
	case syscall.S_IFREG, syscall.S_IFLNK:
		if changedAt(st).Before(s.cutoff) && !(rollouts && s.runningBuildsLog(parent, name, st)) {
			s.unlink(parent, name)
		}
	}
	return nil
}

// tree removes, in parent's folder name, every file or link last changed before the cutoff, then each of its folders
// (itself included) left empty that was last changed before then. A memory/ folder is never entered. repair makes a
// folder the walk may not open readable first (a finished run's own folder); rollouts keeps a running build's Codex
// logs (a lane's own folders). It answers an error only when the walk must stop (its bound).
func (s *sessionSweep) tree(parent *os.File, name string, depth int, repair, rollouts bool) error {
	if depth > maxSessionDepth || name == "memory" {
		return nil
	}
	before, ok, err := lstatAt(parent, name)
	if err != nil {
		s.skip(err)
		return nil
	}
	if !ok || before.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return nil
	}
	dir, err := openDirectory(parent, name, repair)
	if err != nil {
		s.skip(err)
		return nil
	}
	if dir == nil {
		return nil
	}
	names, err := s.names(dir)
	if err != nil {
		dir.Close()
		return s.namesFailed(err)
	}
	for _, child := range names {
		if child == "memory" {
			continue
		}
		st, ok, err := lstatAt(dir, child)
		if err != nil {
			s.skip(err)
			continue
		}
		if !ok {
			continue
		}
		switch st.Mode & syscall.S_IFMT {
		case syscall.S_IFDIR:
			if err := s.tree(dir, child, depth+1, repair, rollouts); err != nil {
				dir.Close()
				return err
			}
		case syscall.S_IFREG, syscall.S_IFLNK:
			if changedAt(st).Before(s.cutoff) && !(rollouts && s.runningBuildsLog(dir, child, st)) {
				s.unlink(dir, child)
			}
		}
	}
	dir.Close()
	// Its own time from before this walk: a folder this walk emptied, unchanged for 30 days but for that, goes.
	if changedAt(before).Before(s.cutoff) {
		_ = sysUnlinkat(parent, name, atRemoveDir) // a folder that still holds anything stays
	}
	return nil
}

// runningBuildsLog reports whether a lane's file is a Codex log codex exec wrote in the working folder of a build that
// has not finished: kept. A log that cannot be read is not kept for that.
func (s *sessionSweep) runningBuildsLog(dir *os.File, name string, st syscall.Stat_t) bool {
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
		return false
	}
	first, err := firstRecord(dir, name)
	if err != nil {
		s.skip(err)
		return true
	}
	run := codexLogRun(first)
	return run != "" && !s.finished(run)
}

// dated finds Codex logs in YYYY/MM/DD folders whose first record names a finished build's working folder, and removes
// those last changed before the cutoff. Every other log there (a person's own Codex sessions) stays, as do the folders.
func (s *sessionSweep) dated(parent *os.File, name string, depth int) error {
	dir, err := openDirectory(parent, name, false)
	if err != nil {
		s.skip(err)
		return nil
	}
	if dir == nil {
		return nil
	}
	defer dir.Close()
	names, err := s.names(dir)
	if err != nil {
		return s.namesFailed(err)
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
			s.skip(err)
			continue
		}
		if !ok || st.Mode&syscall.S_IFMT != syscall.S_IFREG || !changedAt(st).Before(s.cutoff) {
			continue
		}
		first, err := firstRecord(dir, child)
		if err != nil {
			s.skip(err)
			continue
		}
		if run := codexLogRun(first); run != "" && s.finished(run) {
			s.unlink(dir, child)
		}
	}
	return nil
}

func (s *sessionSweep) unlink(dir *os.File, name string) {
	err := sysUnlinkat(dir, name, 0)
	switch {
	case err == nil:
		s.result.Removed++
	case !errors.Is(err, syscall.ENOENT):
		s.skip(err)
	}
}

// namesFailed skips a folder that cannot be listed; only the walk's bound stops it.
func (s *sessionSweep) namesFailed(err error) error {
	if errors.Is(err, errInvalid) {
		return err
	}
	s.skip(err)
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

// openDirectory opens the folder name in parent, never through a link; nil when it is not there or not a folder. With
// repair, a folder it may not open is made the user's to read (0700), through a descriptor of the folder itself, and
// opened again.
func openDirectory(parent *os.File, name string, repair bool) (*os.File, error) {
	const flags = syscall.O_RDONLY | syscall.O_DIRECTORY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
	fd, err := sysOpenat(int(parent.Fd()), name, flags, 0)
	if repair && (errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)) && makeReadable(parent, name) == nil {
		fd, err = sysOpenat(int(parent.Fd()), name, flags, 0)
	}
	if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

// makeReadable sets a folder in parent to 0700 without following a link: it is opened as a path (O_PATH|O_NOFOLLOW),
// must be a folder, and is changed through that descriptor's /proc entry, which names the folder itself.
func makeReadable(parent *os.File, name string) error {
	fd, err := syscall.Openat(int(parent.Fd()), name, oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return syscall.ENOTDIR
	}
	return syscall.Chmod("/proc/self/fd/"+strconv.Itoa(fd), 0o700)
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

// firstRecord reads a log's first line, stopping at its newline and at most maxFirstRecord bytes in; nil when it has
// none that fits.
func firstRecord(dir *os.File, name string) ([]byte, error) {
	fd, err := sysOpenat(int(dir.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
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
	var line []byte
	chunk := make([]byte, 32<<10)
	for len(line) < maxFirstRecord {
		n, err := file.Read(chunk[:min(len(chunk), maxFirstRecord-len(line))])
		if i := bytes.IndexByte(chunk[:n], '\n'); i >= 0 {
			return append(line, chunk[:i]...), nil
		}
		line = append(line, chunk[:n]...)
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
	}
	return nil, nil
}

// unlinkat removes name from dir: a file or a link (never what it names), or with atRemoveDir an empty folder.
func unlinkat(dir *os.File, name string, flags int) error {
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

//go:build linux

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

// abcp serve hands abcp run its launch lock as file 3; abcp run marks it, and every other file it was given above
// stderr, close-on-exec before it starts anything, so no build reaches one from inside its wall.
func TestRunMarksEveryFileItWasGivenCloseOnExec(t *testing.T) {
	fd, err := syscall.Open(filepath.Join(t.TempDir(), "launch.lock"), syscall.O_RDWR|syscall.O_CREAT, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	const inherited = 78
	if _, err := os.Lstat("/proc/self/fd/" + strconv.Itoa(inherited)); err == nil {
		t.Fatalf("file %d is in use", inherited)
	}
	if err := syscall.Dup3(fd, inherited, 0); err != nil {
		t.Fatal(err)
	}
	syscall.Close(fd)
	defer syscall.Close(inherited)
	closeOnExec := func() bool {
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, inherited, syscall.F_GETFD, 0)
		if errno != 0 {
			t.Fatal(errno)
		}
		return flags&syscall.FD_CLOEXEC != 0
	}
	if closeOnExec() {
		t.Fatal("the inherited file is close-on-exec already")
	}
	var stdout, stderr bytes.Buffer
	// Even a run that refuses its arguments marks them first.
	runCLI([]string{"run"}, &stdout, &stderr)
	if !closeOnExec() {
		t.Fatal("abcp run left a file it was given open for what it starts")
	}
}

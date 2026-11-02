//go:build linux

package run

import (
	"os"
	"strconv"
	"syscall"
)

// CloseInheritedFilesOnExec marks every file this process was given above stderr close-on-exec, so nothing it starts
// (the cgroup trampoline, bubblewrap, Ralphex, the agent) inherits it. abcp serve hands abcp run its launch lock as
// file 3; abcp run keeps its own copy, so the lock is held while it runs, but a build must never reach it from inside
// its wall (/proc/self/fd/3). Files Go opens are close-on-exec already. abcp run calls it before it starts anything.
func CloseInheritedFilesOnExec() error {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil || fd <= 2 {
			continue
		}
		// The folder ReadDir read is closed already; any other file is marked.
		syscall.CloseOnExec(fd)
	}
	return nil
}

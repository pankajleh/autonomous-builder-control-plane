//go:build linux

package activity

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func pinExecutable(cmd *exec.Cmd, binary *os.File) {
	cmd.Path = "/proc/self/fd/3"
	cmd.ExtraFiles = []*os.File{binary}
}

// Linux ties Pdeathsig to the spawning thread. Keep that thread alive until
// Wait completes; otherwise Go thread retirement could kill a healthy sidecar.
// Go's fork/exec implementation also checks for parent death during startup.
func startContained(cmd *exec.Cmd, done chan struct{}) error {
	started := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(done)
		cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
		err := cmd.Start()
		started <- err
		if err == nil {
			_ = cmd.Wait()
		}
	}()
	return <-started
}
func ownsListener(pid, port int) bool {
	data, err := os.ReadFile("/proc/net/tcp")
	if err != nil {
		return false
	}
	inode := ""
	address := fmt.Sprintf("0100007F:%04X", port)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 9 && fields[1] == address && fields[3] == "0A" {
			if inode != "" {
				return false
			}
			inode = fields[9]
		}
	}
	if inode == "" {
		return false
	}
	// A walled provider listens from inside its wall: a descendant of the started process, a few levels down.
	for _, candidate := range descendants(pid, 4) {
		base := "/proc/" + strconv.Itoa(candidate) + "/fd"
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			target, err := os.Readlink(filepath.Join(base, entry.Name()))
			if err == nil && target == "socket:["+inode+"]" {
				return true
			}
		}
	}
	return false
}

// descendants answers pid and its descendants up to depth levels down, from /proc/<pid>/task/*/children.
func descendants(pid, depth int) []int {
	out := []int{pid}
	level := []int{pid}
	for ; depth > 0 && len(level) > 0; depth-- {
		var next []int
		for _, parent := range level {
			tasks, err := os.ReadDir("/proc/" + strconv.Itoa(parent) + "/task")
			if err != nil {
				continue
			}
			for _, task := range tasks {
				data, err := os.ReadFile("/proc/" + strconv.Itoa(parent) + "/task/" + task.Name() + "/children")
				if err != nil {
					continue
				}
				for _, field := range strings.Fields(string(data)) {
					if child, err := strconv.Atoi(field); err == nil && child > 0 && len(out) < 64 {
						out, next = append(out, child), append(next, child)
					}
				}
			}
		}
		level = next
	}
	return out
}

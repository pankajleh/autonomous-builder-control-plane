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
	base := "/proc/" + strconv.Itoa(pid) + "/fd"
	entries, err := os.ReadDir(base)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(base, entry.Name()))
		if err == nil && target == "socket:["+inode+"]" {
			return true
		}
	}
	return false
}

//go:build linux

package activity

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func pinExecutable(cmd *exec.Cmd, binary *os.File) {
	cmd.Path = "/proc/self/fd/3"
	cmd.ExtraFiles = []*os.File{binary}
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

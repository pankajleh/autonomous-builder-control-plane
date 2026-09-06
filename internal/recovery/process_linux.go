//go:build linux

package recovery

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var errProcessNotFound = errors.New("process not found")

type processObservation struct {
	identity ProcessIdentity
	alive    bool
}

func observeProcess(pid int) (processObservation, error) {
	if pid <= 0 {
		return processObservation{}, errors.New("process PID must be positive")
	}
	bootIDBytes, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return processObservation{}, fmt.Errorf("read Linux boot identity: %w", err)
	}
	bootID := strings.TrimSpace(string(bootIDBytes))
	if bootID == "" {
		return processObservation{}, errors.New("Linux boot identity is empty")
	}
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if errors.Is(err, os.ErrNotExist) {
		return processObservation{}, errProcessNotFound
	}
	if err != nil {
		return processObservation{}, fmt.Errorf("read Linux process stat: %w", err)
	}
	state, startTicks, err := parseLinuxProcessStat(stat)
	if err != nil {
		return processObservation{}, err
	}
	return processObservation{
		identity: ProcessIdentity{PID: pid, LinuxStartTicks: startTicks, BootID: bootID},
		alive:    state != "Z" && state != "X" && state != "x",
	}, nil
}

func parseLinuxProcessStat(stat []byte) (string, uint64, error) {
	closing := strings.LastIndexByte(string(stat), ')')
	if closing < 0 || closing+1 >= len(stat) {
		return "", 0, errors.New("malformed Linux process stat")
	}
	// The suffix starts at field 3 (state); starttime is field 22.
	fields := strings.Fields(string(stat[closing+1:]))
	const startTimeIndex = 22 - 3
	if len(fields) <= startTimeIndex {
		return "", 0, errors.New("Linux process stat lacks start identity")
	}
	startTicks, err := strconv.ParseUint(fields[startTimeIndex], 10, 64)
	if err != nil || startTicks == 0 {
		return "", 0, errors.New("Linux process start identity is invalid")
	}
	return fields[0], startTicks, nil
}

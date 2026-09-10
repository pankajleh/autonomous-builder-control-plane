//go:build linux

package run

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ValidateCgroupV2ScopeV1 proves that scope is a controller-created cgroup v2
// directory below root with the membership/kill controls needed by a
// ContainedCommandRunner. It never treats a process group as equivalent.
func ValidateCgroupV2ScopeV1(root, scope string) error {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: resolve cgroup root: %w", err)
	}
	scope, err = filepath.EvalSymlinks(scope)
	if err != nil {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: resolve cgroup scope: %w", err)
	}
	relative, err := filepath.Rel(root, scope)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("EXECUTION_BOUNDS_INVALID: cgroup scope is not a concrete child of the controller root")
	}
	if err := requireRegular(filepath.Join(root, "cgroup.controllers")); err != nil {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: cgroup v2 is unavailable: %w", err)
	}
	for _, name := range []string{"cgroup.procs", "cgroup.events", "cgroup.kill"} {
		if err := requireRegular(filepath.Join(scope, name)); err != nil {
			return fmt.Errorf("EXECUTION_BOUNDS_INVALID: scope lacks %s: %w", name, err)
		}
	}
	for _, name := range []string{"cgroup.procs", "cgroup.kill"} {
		control, err := os.OpenFile(filepath.Join(scope, name), os.O_WRONLY, 0)
		if err != nil {
			return fmt.Errorf("EXECUTION_BOUNDS_INVALID: scope %s is not controller-writable: %w", name, err)
		}
		if err := control.Close(); err != nil {
			return fmt.Errorf("EXECUTION_BOUNDS_INVALID: close scope %s capability probe: %w", name, err)
		}
	}
	return nil
}

// VerifyCgroupMembershipV1 proves every expected process is in the exact
// scope. Descendants must be checked again immediately before teardown.
func VerifyCgroupMembershipV1(scope string, expectedPIDs []int) error {
	if len(expectedPIDs) == 0 {
		return errors.New("EXECUTION_BOUNDS_INVALID: expected cgroup membership is empty")
	}
	actual, err := readPIDs(filepath.Join(scope, "cgroup.procs"))
	if err != nil {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: read cgroup membership: %w", err)
	}
	for _, pid := range expectedPIDs {
		if pid <= 0 {
			return errors.New("EXECUTION_BOUNDS_INVALID: expected PID is invalid")
		}
		if _, ok := actual[pid]; !ok {
			return fmt.Errorf("EXECUTION_BOUNDS_INVALID: PID %d is outside the governed cgroup", pid)
		}
	}
	return nil
}

// VerifyCgroupEmptyV1 is the required proof after graceful and forced scope
// termination. A missing or unreadable scope is not accepted as proof.
func VerifyCgroupEmptyV1(scope string) error {
	pids, err := readPIDs(filepath.Join(scope, "cgroup.procs"))
	if err != nil {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: prove empty cgroup: %w", err)
	}
	if len(pids) != 0 {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: cgroup still contains %d process(es)", len(pids))
	}
	return nil
}

func requireRegular(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("must be a non-symlink regular control file")
	}
	return nil
}

func readPIDs(path string) (map[int]struct{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	result := make(map[int]struct{})
	for _, field := range strings.Fields(string(data)) {
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 0 {
			return nil, errors.New("cgroup.procs contains an invalid PID")
		}
		result[pid] = struct{}{}
	}
	return result, nil
}

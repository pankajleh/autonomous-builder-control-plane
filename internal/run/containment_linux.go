//go:build linux

package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
)

const containmentChildArgumentV1 = "__abcp_cgroup_v2_exec_v1"

type containmentChildRequestV1 struct {
	CgroupRoot  string   `json:"cgroup_root"`
	Scope       string   `json:"scope"`
	Argv        []string `json:"argv"`
	Environment []string `json:"environment"`
	MarkerPath  string   `json:"marker_path"`
	Marker      string   `json:"marker"`
}

// LinuxContainedCommandRunner is the concrete production cgroup-v2 adapter.
// A small re-exec trampoline joins the cgroup before the governed binary can
// execute or fork, after which cgroup inheritance contains every descendant.
type LinuxContainedCommandRunner struct {
	inner CommandRunner
	root  string
}

var _ ContainedCommandRunner = (*LinuxContainedCommandRunner)(nil)

// NewLinuxContainedCommandRunner builds a fail-closed Linux containment
// runner. Scope availability and permissions are re-verified per invocation.
func NewLinuxContainedCommandRunner(inner CommandRunner, cgroupRoot string) (*LinuxContainedCommandRunner, error) {
	if inner == nil {
		return nil, errors.New("EXECUTION_BOUNDS_INVALID: process supervisor is required")
	}
	if cgroupRoot == "" {
		cgroupRoot = "/sys/fs/cgroup"
	}
	root, err := filepath.EvalSymlinks(cgroupRoot)
	if err != nil {
		return nil, fmt.Errorf("EXECUTION_BOUNDS_INVALID: resolve cgroup root: %w", err)
	}
	if err := requireRegular(filepath.Join(root, "cgroup.controllers")); err != nil {
		return nil, fmt.Errorf("EXECUTION_BOUNDS_INVALID: cgroup v2 is unavailable: %w", err)
	}
	return &LinuxContainedCommandRunner{inner: inner, root: root}, nil
}

// Run preserves legacy uncontained behavior. V3 callers use RunContained.
func (r *LinuxContainedCommandRunner) Run(ctx context.Context, command supervisor.Command) (supervisor.Result, error) {
	return r.inner.Run(ctx, command)
}

// RunContained creates a fresh scope, starts a pre-exec membership trampoline,
// verifies its proof, kills the whole descendant scope, and proves it empty.
func (r *LinuxContainedCommandRunner) RunContained(ctx context.Context, command supervisor.Command, request ContainmentRequestV1) (supervisor.Result, ContainmentEvidenceV1, error) {
	evidence := ContainmentEvidenceV1{Kind: "ContainmentEvidenceV1", Primitive: "cgroup-v2"}
	if ctx == nil || request.Identity == "" {
		return supervisor.Result{}, evidence, errors.New("EXECUTION_BOUNDS_INVALID: containment context and identity are required")
	}
	if _, err := time.ParseDuration(request.WallClockTimeout); err != nil {
		return supervisor.Result{}, evidence, fmt.Errorf("EXECUTION_BOUNDS_INVALID: containment wall clock is invalid: %w", err)
	}
	prefix := "abcp-" + safeScopeIdentity(request.Identity) + "-"
	scope, err := os.MkdirTemp(r.root, prefix)
	if err != nil {
		return supervisor.Result{}, evidence, fmt.Errorf("EXECUTION_BOUNDS_INVALID: create cgroup v2 scope: %w", err)
	}
	evidence.ScopeIdentity = scope
	removeScope := func() error { return os.Remove(scope) }
	if err := ValidateCgroupV2ScopeV1(r.root, scope); err != nil {
		_ = removeScope()
		return supervisor.Result{}, evidence, err
	}
	requestDir, err := os.MkdirTemp("", "abcp-containment-")
	if err != nil {
		_ = killAndEmptyCgroupV1(scope)
		_ = removeScope()
		return supervisor.Result{}, evidence, fmt.Errorf("EXECUTION_BOUNDS_INVALID: create containment request directory: %w", err)
	}
	defer os.RemoveAll(requestDir)
	marker := filepath.Join(requestDir, "membership.ok")
	markerValue := scope + "\n"
	childRequest := containmentChildRequestV1{CgroupRoot: r.root, Scope: scope, Argv: append([]string(nil), command.Argv...), Environment: append([]string(nil), command.Env...), MarkerPath: marker, Marker: markerValue}
	payload, err := json.Marshal(childRequest)
	if err != nil {
		_ = killAndEmptyCgroupV1(scope)
		_ = removeScope()
		return supervisor.Result{}, evidence, err
	}
	requestPath := filepath.Join(requestDir, "request.json")
	if err := os.WriteFile(requestPath, payload, 0o600); err != nil {
		_ = killAndEmptyCgroupV1(scope)
		_ = removeScope()
		return supervisor.Result{}, evidence, fmt.Errorf("EXECUTION_BOUNDS_INVALID: write containment request: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		_ = killAndEmptyCgroupV1(scope)
		_ = removeScope()
		return supervisor.Result{}, evidence, fmt.Errorf("EXECUTION_BOUNDS_INVALID: resolve controller executable: %w", err)
	}
	containedCommand := command
	containedCommand.Argv = []string{executable, containmentChildArgumentV1, requestPath}
	containedCommand.Env = nil
	result, runErr := r.inner.Run(ctx, containedCommand)
	result.Argv = append([]string(nil), command.Argv...)
	result.Cwd = command.Cwd
	markerBytes, markerErr := os.ReadFile(marker)
	evidence.MembershipVerified = markerErr == nil && bytes.Equal(markerBytes, []byte(markerValue))
	teardownErr := killAndEmptyCgroupV1(scope)
	if teardownErr == nil {
		evidence.EmptyScopeVerified = true
		teardownErr = removeScope()
	}
	if !evidence.MembershipVerified {
		runErr = errors.Join(runErr, errors.New("EXECUTION_BOUNDS_INVALID: governed child did not prove cgroup membership before exec"))
	}
	if teardownErr != nil {
		runErr = errors.Join(runErr, teardownErr)
	}
	return result, evidence, runErr
}

// IsContainmentChildV1 identifies the private controller re-exec path.
func IsContainmentChildV1(args []string) bool {
	return len(args) == 2 && args[0] == containmentChildArgumentV1 && args[1] != ""
}

// RunContainmentChildV1 joins and verifies the exact cgroup before replacing
// itself with the structured governed argv. It is called before CLI parsing.
func RunContainmentChildV1(args []string) int {
	if !IsContainmentChildV1(args) {
		return 125
	}
	data, err := os.ReadFile(args[1])
	if err != nil || len(data) == 0 || len(data) > 1<<20 {
		return 125
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	request := containmentChildRequestV1{}
	if err := decoder.Decode(&request); err != nil {
		return 125
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return 125
	}
	canonical, err := json.Marshal(request)
	if err != nil || !bytes.Equal(data, canonical) || len(request.Argv) == 0 || request.Argv[0] == "" || request.MarkerPath == "" || request.Marker == "" {
		return 125
	}
	if err := ValidateCgroupV2ScopeV1(request.CgroupRoot, request.Scope); err != nil {
		return 125
	}
	member := []byte(strconv.Itoa(os.Getpid()) + "\n")
	if err := writeCgroupControlV1(filepath.Join(request.Scope, "cgroup.procs"), member); err != nil {
		return 125
	}
	if err := VerifyCgroupMembershipV1(request.Scope, []int{os.Getpid()}); err != nil {
		return 125
	}
	if err := os.WriteFile(request.MarkerPath, []byte(request.Marker), 0o600); err != nil {
		return 125
	}
	if err := syscall.Exec(request.Argv[0], request.Argv, request.Environment); err != nil {
		return 126
	}
	return 126
}

func killAndEmptyCgroupV1(scope string) error {
	if err := writeCgroupControlV1(filepath.Join(scope, "cgroup.kill"), []byte("1\n")); err != nil {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: terminate cgroup scope: %w", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := VerifyCgroupEmptyV1(scope); err == nil {
			return nil
		} else if time.Now().After(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func writeCgroupControlV1(path string, value []byte) error {
	control, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, writeErr := control.Write(value)
	closeErr := control.Close()
	return errors.Join(writeErr, closeErr)
}

func safeScopeIdentity(identity string) string {
	var builder strings.Builder
	for _, character := range identity {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' {
			builder.WriteRune(character)
		}
		if builder.Len() >= 32 {
			break
		}
	}
	if builder.Len() == 0 {
		return "run"
	}
	return builder.String()
}

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
	events, err := os.ReadFile(filepath.Join(scope, "cgroup.events"))
	if err != nil {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: read cgroup population state: %w", err)
	}
	populated := ""
	fields := strings.Fields(string(events))
	for index := 0; index+1 < len(fields); index += 2 {
		if fields[index] == "populated" {
			populated = fields[index+1]
			break
		}
	}
	if populated != "0" {
		return errors.New("EXECUTION_BOUNDS_INVALID: cgroup descendants are not proven empty")
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

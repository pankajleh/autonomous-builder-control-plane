package ralphex

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	contextcapsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
)

type Mode string

const (
	ModeFull      Mode = "full"
	ModeTasksOnly Mode = "tasks-only"
	ModeReview    Mode = "review"
)

type Invocation struct {
	BinaryPath   string
	PlanPath     string
	ConfigDir    string
	Mode         Mode
	Codex        bool
	Worktree     bool
	Branch       string
	TaskModel    string
	TaskEffort   string
	ReviewModel  string
	ReviewEffort string
	WaitOnLimit  string
	BaseRef      string
	Bounds       *contextcapsule.ExecutionBoundsV1
	Capability   *CapabilityV1
	BinarySHA256 string
	SourceSHA    string
}

// HandoffMode identifies the controller-visible review->lease->fix boundary.
type HandoffMode string

const (
	HandoffTasksOnly          HandoffMode = "TASKS_ONLY"
	HandoffReviewOnly         HandoffMode = "REVIEW_ONLY"
	HandoffStopBeforeFixBatch HandoffMode = "CONTROLLER_STOP_BEFORE_FIX_BATCH"
)

// CapabilityV1 is pinned evidence that one Ralphex binary supports every
// structural execution control required by a B V3 invocation.
type CapabilityV1 struct {
	Kind                     string      `json:"kind"`
	BinarySHA256             string      `json:"binary_sha256"`
	SourceSHA                string      `json:"source_sha"`
	MaxIterationsFlag        bool        `json:"max_iterations_flag"`
	SessionTimeoutFlag       bool        `json:"session_timeout_flag"`
	IdleTimeoutFlag          bool        `json:"idle_timeout_flag"`
	SkipFinalizeFlag         bool        `json:"skip_finalize_flag"`
	BaseRefFlag              bool        `json:"base_ref_flag"`
	ExecutorModelEffortFlags bool        `json:"executor_model_effort_flags"`
	IsolatedConfig           bool        `json:"isolated_config"`
	GovernedHandoff          HandoffMode `json:"governed_handoff"`
	LinuxContainment         bool        `json:"linux_containment"`
}

// CapabilityProbeV1 is emitted by the exact selected binary when invoked with
// --abcp-governance-capability-v1. The controller supplies the binary digest,
// so probe output cannot redirect attestation to a different executable.
type CapabilityProbeV1 struct {
	Kind                     string      `json:"kind"`
	SourceSHA                string      `json:"source_sha"`
	MaxIterationsFlag        bool        `json:"max_iterations_flag"`
	SessionTimeoutFlag       bool        `json:"session_timeout_flag"`
	IdleTimeoutFlag          bool        `json:"idle_timeout_flag"`
	SkipFinalizeFlag         bool        `json:"skip_finalize_flag"`
	BaseRefFlag              bool        `json:"base_ref_flag"`
	ExecutorModelEffortFlags bool        `json:"executor_model_effort_flags"`
	IsolatedConfig           bool        `json:"isolated_config"`
	GovernedHandoff          HandoffMode `json:"governed_handoff"`
	LinuxContainment         bool        `json:"linux_containment"`
}

// ExecutionStateV1 contains durable B-wide counters which process restarts do
// not reset.
type ExecutionStateV1 struct {
	RalphexInvocations int    `json:"ralphex_invocations"`
	ReviewReports      int    `json:"review_reports"`
	MutationLeases     int    `json:"mutation_leases"`
	TotalFixBatches    int    `json:"total_fix_batches"`
	AggregateElapsed   string `json:"aggregate_elapsed"`
}

func (i Invocation) Argv() ([]string, error) {
	if i.BinaryPath == "" {
		return nil, fmt.Errorf("ralphex binary path is required")
	}
	if i.PlanPath == "" {
		return nil, fmt.Errorf("plan path is required")
	}
	if i.Mode == "" {
		i.Mode = ModeFull
	}
	if i.Mode != ModeFull && i.Mode != ModeTasksOnly && i.Mode != ModeReview {
		return nil, fmt.Errorf("unsupported ralphex mode %q", i.Mode)
	}
	if i.Mode == ModeReview && i.Worktree {
		return nil, fmt.Errorf("worktree is not valid for review-only invocation")
	}
	if i.Worktree && i.Branch == "" {
		return nil, fmt.Errorf("branch override is required for worktree invocation")
	}
	if i.Branch != "" && !i.Worktree {
		return nil, fmt.Errorf("branch override requires worktree invocation")
	}
	if i.Bounds != nil {
		if i.Capability == nil {
			return nil, fmt.Errorf("EXECUTION_BOUNDS_INVALID: pinned Ralphex capability is required")
		}
		if err := ValidateCapabilityV1(*i.Capability, i.BinarySHA256, i.SourceSHA, i.Mode); err != nil {
			return nil, err
		}
		if err := contextcapsule.ValidateExecutionBoundsV1(*i.Bounds); err != nil {
			return nil, err
		}
		if i.BaseRef == "" {
			return nil, fmt.Errorf("EXECUTION_BOUNDS_INVALID: base-ref is required")
		}
		if i.TaskEffort != "xhigh" || i.ReviewEffort != "xhigh" {
			return nil, fmt.Errorf("EXECUTION_BOUNDS_INVALID: Codex task and review effort must be xhigh")
		}
	}

	argv := []string{i.BinaryPath}
	if i.ConfigDir != "" {
		argv = append(argv, "--config-dir", i.ConfigDir)
	}
	if i.Codex {
		argv = append(argv, "--codex")
	}
	if i.WaitOnLimit != "" {
		argv = append(argv, "--wait", i.WaitOnLimit)
	}
	if i.Bounds != nil {
		argv = append(argv,
			"--max-iterations", strconv.Itoa(i.Bounds.MaxIterations),
			"--session-timeout", i.Bounds.SessionTimeout,
			"--idle-timeout", i.Bounds.IdleTimeout,
			"--skip-finalize",
			"--base-ref", i.BaseRef,
		)
	}
	taskModel, err := modelSpec(i.TaskModel, i.TaskEffort)
	if err != nil {
		return nil, fmt.Errorf("task model policy: %w", err)
	}
	if taskModel != "" {
		argv = append(argv, "--task-model", taskModel)
	}
	reviewModel, err := modelSpec(i.ReviewModel, i.ReviewEffort)
	if err != nil {
		return nil, fmt.Errorf("review model policy: %w", err)
	}
	if reviewModel != "" {
		argv = append(argv, "--review-model", reviewModel)
	}
	switch i.Mode {
	case ModeTasksOnly:
		argv = append(argv, "--tasks-only")
	case ModeReview:
		argv = append(argv, "--review")
	}
	if i.Worktree {
		argv = append(argv, "--worktree")
		argv = append(argv, "--branch", i.Branch)
	}
	argv = append(argv, i.PlanPath)
	return argv, nil
}

// ValidateCapabilityV1 fails admission unless every selected bound and
// handoff property is proven by the pinned binary/source identity.
func ValidateCapabilityV1(capability CapabilityV1, binarySHA256, sourceSHA string, mode Mode) error {
	if capability.Kind != "RalphexCapabilityV1" || capability.BinarySHA256 == "" || capability.BinarySHA256 != binarySHA256 || capability.SourceSHA == "" || capability.SourceSHA != sourceSHA {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: Ralphex capability identity does not match pinned binary/source")
	}
	if !capability.MaxIterationsFlag || !capability.SessionTimeoutFlag || !capability.IdleTimeoutFlag || !capability.SkipFinalizeFlag || !capability.BaseRefFlag || !capability.ExecutorModelEffortFlags || !capability.IsolatedConfig || !capability.LinuxContainment {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: Ralphex capability does not prove every structural bound and Linux containment")
	}
	switch mode {
	case ModeTasksOnly:
		if capability.GovernedHandoff != HandoffTasksOnly {
			return fmt.Errorf("EXECUTION_BOUNDS_INVALID: tasks-only invocation requires TASKS_ONLY handoff capability")
		}
	case ModeReview:
		if capability.GovernedHandoff != HandoffReviewOnly {
			return fmt.Errorf("EXECUTION_BOUNDS_INVALID: review invocation must stop before mutation")
		}
	case ModeFull:
		if capability.GovernedHandoff != HandoffStopBeforeFixBatch {
			return fmt.Errorf("EXECUTION_BOUNDS_INVALID: native review/fix lacks controller stop-before-fix handoff")
		}
	default:
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: unsupported Ralphex mode %q", mode)
	}
	return nil
}

// VerifyBinaryCapabilityV1 queries the already hash-pinned executable and
// requires its strict-canonical response to equal the requested capability.
// Caller-set booleans alone are never sufficient for V3 admission.
func VerifyBinaryCapabilityV1(binaryPath string, expected CapabilityV1, binarySHA256, sourceSHA string, mode Mode) error {
	file, err := os.Open(binaryPath)
	if err != nil {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: open pinned Ralphex binary: %w", err)
	}
	hasher := sha256.New()
	_, copyErr := io.Copy(hasher, io.LimitReader(file, 1<<30))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || hex.EncodeToString(hasher.Sum(nil)) != binarySHA256 {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: selected Ralphex binary does not match its pinned digest")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binaryPath, "--abcp-governance-capability-v1").Output()
	if err != nil {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: pinned Ralphex capability probe failed: %w", err)
	}
	if len(output) == 0 || len(output) > 64<<10 {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: pinned Ralphex capability probe output is empty or oversized")
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	probe := CapabilityProbeV1{}
	if err := decoder.Decode(&probe); err != nil {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: decode pinned Ralphex capability probe: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: capability probe contains trailing JSON")
	}
	canonical, err := json.Marshal(probe)
	if err != nil || !bytes.Equal(bytes.TrimSpace(output), canonical) {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: capability probe is not strict canonical JSON")
	}
	if probe.Kind != "RalphexCapabilityProbeV1" {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: capability probe kind is invalid")
	}
	actual := CapabilityV1{
		Kind: "RalphexCapabilityV1", BinarySHA256: binarySHA256, SourceSHA: probe.SourceSHA,
		MaxIterationsFlag: probe.MaxIterationsFlag, SessionTimeoutFlag: probe.SessionTimeoutFlag,
		IdleTimeoutFlag: probe.IdleTimeoutFlag, SkipFinalizeFlag: probe.SkipFinalizeFlag,
		BaseRefFlag: probe.BaseRefFlag, ExecutorModelEffortFlags: probe.ExecutorModelEffortFlags,
		IsolatedConfig: probe.IsolatedConfig, GovernedHandoff: probe.GovernedHandoff,
		LinuxContainment: probe.LinuxContainment,
	}
	actualJSON, actualErr := json.Marshal(actual)
	expectedJSON, expectedErr := json.Marshal(expected)
	if actualErr != nil || expectedErr != nil || !bytes.Equal(actualJSON, expectedJSON) {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: manifest capability differs from the pinned binary probe")
	}
	return ValidateCapabilityV1(actual, binarySHA256, sourceSHA, mode)
}

// ValidateExecutionStateV1 enforces B-wide cumulative ceilings without reset.
func ValidateExecutionStateV1(bounds contextcapsule.ExecutionBoundsV1, state ExecutionStateV1) error {
	if err := contextcapsule.ValidateExecutionBoundsV1(bounds); err != nil {
		return err
	}
	elapsed, err := time.ParseDuration(state.AggregateElapsed)
	maximum, maxErr := time.ParseDuration(bounds.AggregateWallClockTimeout)
	wall, wallErr := time.ParseDuration(bounds.WallClockTimeout)
	if err != nil || maxErr != nil || wallErr != nil || elapsed < 0 || elapsed.String() != state.AggregateElapsed || elapsed >= maximum || elapsed+wall > maximum {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: aggregate elapsed time is invalid or exhausted")
	}
	if state.RalphexInvocations < 0 || state.RalphexInvocations >= bounds.MaxRalphexInvocations || state.ReviewReports < 0 || state.ReviewReports > bounds.MaxReviewReports || state.MutationLeases < 0 || state.MutationLeases > bounds.MaxMutationLeases || state.TotalFixBatches < 0 || state.TotalFixBatches > bounds.MaxTotalFixBatches {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: a durable cumulative counter is invalid or exhausted")
	}
	return nil
}

// ValidateSingleIncompleteTaskV1 proves that a plan exposes exactly one
// executable Task/Iteration section with unchecked items.
func ValidateSingleIncompleteTaskV1(plan []byte) error {
	if len(plan) == 0 || len(plan) > 4<<20 {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: plan is empty or oversized")
	}
	scanner := bufio.NewScanner(strings.NewReader(string(plan)))
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	inSection, sectionIncomplete, inFence := false, false, false
	incompleteSections, fenceLength := 0, 0
	var fenceMarker byte
	flush := func() {
		if inSection && sectionIncomplete {
			incompleteSections++
		}
	}
	for scanner.Scan() {
		line := scanner.Text()
		if inFence {
			if closesFence(line, fenceMarker, fenceLength) {
				inFence = false
			}
			continue
		}
		if marker, length, opens := opensFence(line); opens {
			inFence, fenceMarker, fenceLength = true, marker, length
			continue
		}
		level := headingLevel(line)
		if level == 3 && taskHeading(line) {
			flush()
			inSection, sectionIncomplete = true, false
			continue
		}
		if level > 0 && level <= 3 {
			flush()
			inSection, sectionIncomplete = false, false
			continue
		}
		trimmed := strings.TrimLeft(line, " \t")
		if inSection && (strings.HasPrefix(trimmed, "- [ ]") || strings.HasPrefix(trimmed, "* [ ]") || strings.HasPrefix(trimmed, "+ [ ]")) {
			sectionIncomplete = true
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: scan plan: %w", err)
	}
	if inFence {
		return errors.New("EXECUTION_BOUNDS_INVALID: unterminated Markdown fence")
	}
	flush()
	if incompleteSections != 1 {
		return fmt.Errorf("EXECUTION_BOUNDS_INVALID: plan has %d incomplete executable sections; exactly 1 is required", incompleteSections)
	}
	return nil
}

func opensFence(line string) (byte, int, bool) {
	content, ok := fenceContent(line)
	if !ok || len(content) < 3 || content[0] != '`' && content[0] != '~' {
		return 0, 0, false
	}
	marker, count := content[0], 0
	for count < len(content) && content[count] == marker {
		count++
	}
	if count < 3 || marker == '`' && strings.ContainsRune(content[count:], '`') {
		return 0, 0, false
	}
	return marker, count, true
}

func closesFence(line string, marker byte, openingLength int) bool {
	content, ok := fenceContent(line)
	if !ok || len(content) < openingLength || content[0] != marker {
		return false
	}
	count := 0
	for count < len(content) && content[count] == marker {
		count++
	}
	return count >= openingLength && strings.Trim(content[count:], " \t") == ""
}

func fenceContent(line string) (string, bool) {
	column := 0
	for index := 0; index < len(line); index++ {
		switch line[index] {
		case ' ':
			column++
		case '\t':
			column += 4 - column%4
		default:
			return line[index:], column <= 3
		}
		if column > 3 {
			return "", false
		}
	}
	return "", false
}

func headingLevel(line string) int {
	spaces := 0
	for spaces < len(line) && line[spaces] == ' ' {
		spaces++
	}
	if spaces > 3 {
		return 0
	}
	line = line[spaces:]
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level == len(line) || line[level] != ' ' && line[level] != '\t' {
		return 0
	}
	return level
}

func taskHeading(line string) bool {
	spaces := 0
	for spaces < len(line) && line[spaces] == ' ' {
		spaces++
	}
	title := strings.TrimSpace(line[spaces+3:])
	for _, prefix := range []string{"Task ", "Iteration "} {
		if !strings.HasPrefix(title, prefix) {
			continue
		}
		remainder := title[len(prefix):]
		digits := 0
		for digits < len(remainder) && remainder[digits] >= '0' && remainder[digits] <= '9' {
			digits++
		}
		return digits > 0 && digits < len(remainder) && remainder[digits] == ':'
	}
	return false
}

func modelSpec(model, effort string) (string, error) {
	if strings.Contains(model, ":") || strings.Contains(effort, ":") {
		return "", fmt.Errorf("model and effort must not contain ':'")
	}
	if model == "" && effort == "" {
		return "", nil
	}
	if effort == "" {
		return model, nil
	}
	return model + ":" + effort, nil
}

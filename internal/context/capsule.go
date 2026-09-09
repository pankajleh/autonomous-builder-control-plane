// Package context builds and verifies compact, deterministic context capsules.
package context

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
)

const (
	// PolicyVersionV1 is the historical deterministic context-capsule format.
	PolicyVersionV1 = "context-capsule-v1"
	// PolicyVersionV2 adds purpose-specific operation authority.
	PolicyVersionV2 = "context-capsule-v2"
	// PolicyVersion is the current format for newly built operation capsules.
	PolicyVersion = PolicyVersionV2

	// Conservative bounds keep capsules small enough to be fresh-task context.
	MaxSources      = 32
	MaxOutcomes     = 32
	MaxListItems    = 32
	MaxStringBytes  = 2048
	MaxSourceBytes  = 4 << 20
	MaxCapsuleBytes = 64 << 10
)

// OperationKind identifies the governed purpose for one context capsule.
type OperationKind string

const (
	OperationDesignPlanning       OperationKind = "design-planning"
	OperationDesignReview         OperationKind = "design-review"
	OperationImplementation       OperationKind = "implementation"
	OperationImplementationReview OperationKind = "implementation-review"
	OperationAcceptance           OperationKind = "acceptance"
	OperationMergeAuthorization   OperationKind = "merge-authorization"
	OperationDeployment           OperationKind = "deployment"
	OperationRecovery             OperationKind = "recovery"
	OperationMaintenance          OperationKind = "maintenance"
)

// OperationContext bounds one governed operation to an explicit purpose,
// owned scope, and blocking policy.
type OperationContext struct {
	Kind             OperationKind `json:"kind"`
	OwnedScope       []string      `json:"owned_scope"`
	BlockingCriteria []string      `json:"blocking_criteria"`
}

// Spec is the structured input used to build a Capsule. Sources contains only
// repository-relative paths; source contents are never copied into a capsule.
type Spec struct {
	PolicyVersion       string            `json:"policy_version"`
	Project             string            `json:"project"`
	Plan                string            `json:"plan"`
	RoadmapPhase        string            `json:"roadmap_phase"`
	ExecutionPack       string            `json:"execution_pack"`
	Task                string            `json:"task"`
	OperationContext    *OperationContext `json:"operation_context,omitempty"`
	Repository          string            `json:"repository"`
	BaseSHA             string            `json:"base_sha"`
	Invariants          []string          `json:"invariants"`
	NonGoals            []string          `json:"non_goals"`
	PredecessorOutcomes []Outcome         `json:"predecessor_outcomes"`
	Sources             []string          `json:"sources"`
}

// Source binds a repository-relative path to the SHA256 of its exact bytes.
type Source struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Outcome compactly records an accepted predecessor result.
type Outcome struct {
	Task      string `json:"task"`
	Summary   string `json:"summary"`
	CommitSHA string `json:"commit_sha"`
}

// Capsule is the compact, self-verifying context supplied to a fresh task.
// CapsuleSHA256 hashes the canonical JSON payload excluding that field.
type Capsule struct {
	PolicyVersion       string            `json:"policy_version"`
	Project             string            `json:"project"`
	Plan                string            `json:"plan"`
	RoadmapPhase        string            `json:"roadmap_phase"`
	ExecutionPack       string            `json:"execution_pack"`
	Task                string            `json:"task"`
	OperationContext    *OperationContext `json:"operation_context,omitempty"`
	Repository          string            `json:"repository"`
	BaseSHA             string            `json:"base_sha"`
	Invariants          []string          `json:"invariants"`
	NonGoals            []string          `json:"non_goals"`
	PredecessorOutcomes []Outcome         `json:"predecessor_outcomes"`
	Sources             []Source          `json:"sources"`
	CapsuleSHA256       string            `json:"capsule_sha256"`
}

// Verification summarizes a successful fail-closed capsule verification.
type Verification struct {
	SHA256          string        `json:"sha256,omitempty"`
	CapsuleSHA256   string        `json:"capsule_sha256"`
	BaseSHA         string        `json:"base_sha"`
	SourcesVerified int           `json:"sources_verified"`
	PolicyVersion   string        `json:"policy_version"`
	OperationKind   OperationKind `json:"operation_kind,omitempty"`
}

type payload struct {
	PolicyVersion       string            `json:"policy_version"`
	Project             string            `json:"project"`
	Plan                string            `json:"plan"`
	RoadmapPhase        string            `json:"roadmap_phase"`
	ExecutionPack       string            `json:"execution_pack"`
	Task                string            `json:"task"`
	OperationContext    *OperationContext `json:"operation_context,omitempty"`
	Repository          string            `json:"repository"`
	BaseSHA             string            `json:"base_sha"`
	Invariants          []string          `json:"invariants"`
	NonGoals            []string          `json:"non_goals"`
	PredecessorOutcomes []Outcome         `json:"predecessor_outcomes"`
	Sources             []Source          `json:"sources"`
}

// Build resolves and hashes the explicit sources in spec and returns canonical
// capsule JSON. The repository must be at the exact base SHA in the spec.
func Build(repository string, spec Spec) (Capsule, []byte, error) {
	if err := validateSpec(spec); err != nil {
		return Capsule{}, nil, err
	}
	repository, err := verifyRepository(repository, spec.Repository, spec.BaseSHA)
	if err != nil {
		return Capsule{}, nil, err
	}

	paths := append([]string(nil), spec.Sources...)
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if _, exists := seen[path]; exists {
			return Capsule{}, nil, fmt.Errorf("duplicate source path %q", path)
		}
		seen[path] = struct{}{}
	}
	sort.Strings(paths)
	sources := make([]Source, 0, len(paths))
	for _, path := range paths {
		hash, err := hashRepositoryFile(repository, path)
		if err != nil {
			return Capsule{}, nil, fmt.Errorf("source %q: %w", path, err)
		}
		sources = append(sources, Source{Path: path, SHA256: hash})
	}

	capsule := Capsule{
		PolicyVersion:       spec.PolicyVersion,
		Project:             spec.Project,
		Plan:                spec.Plan,
		RoadmapPhase:        spec.RoadmapPhase,
		ExecutionPack:       spec.ExecutionPack,
		Task:                spec.Task,
		OperationContext:    cloneOperationContext(spec.OperationContext),
		Repository:          spec.Repository,
		BaseSHA:             strings.ToLower(spec.BaseSHA),
		Invariants:          append([]string{}, spec.Invariants...),
		NonGoals:            append([]string{}, spec.NonGoals...),
		PredecessorOutcomes: append([]Outcome{}, spec.PredecessorOutcomes...),
		Sources:             sources,
	}
	capsule.CapsuleSHA256, err = payloadHash(capsule)
	if err != nil {
		return Capsule{}, nil, err
	}
	data, err := json.Marshal(capsule)
	if err != nil {
		return Capsule{}, nil, fmt.Errorf("marshal capsule: %w", err)
	}
	if len(data) > MaxCapsuleBytes {
		return Capsule{}, nil, fmt.Errorf("capsule is %d bytes; maximum is %d", len(data), MaxCapsuleBytes)
	}
	return capsule, data, nil
}

// Parse accepts only the canonical JSON representation emitted by Build.
func Parse(data []byte) (Capsule, error) {
	if len(data) == 0 {
		return Capsule{}, errors.New("capsule is empty")
	}
	if len(data) > MaxCapsuleBytes {
		return Capsule{}, fmt.Errorf("capsule is %d bytes; maximum is %d", len(data), MaxCapsuleBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var capsule Capsule
	if err := decoder.Decode(&capsule); err != nil {
		return Capsule{}, fmt.Errorf("decode capsule: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Capsule{}, errors.New("decode capsule: multiple JSON values")
		}
		return Capsule{}, fmt.Errorf("decode capsule: %w", err)
	}
	canonical, err := json.Marshal(capsule)
	if err != nil {
		return Capsule{}, fmt.Errorf("marshal canonical capsule: %w", err)
	}
	if !bytes.Equal(data, canonical) {
		return Capsule{}, errors.New("capsule JSON is not canonical")
	}
	return capsule, nil
}

// Verify checks the capsule hash, bounds, repository HEAD, and every exact
// source byte hash. Verification fails on duplicates, traversal, or symlinks.
func Verify(repository string, capsule Capsule) (Verification, error) {
	if err := validateCapsule(capsule); err != nil {
		return Verification{}, err
	}
	repository, err := verifyRepository(repository, capsule.Repository, capsule.BaseSHA)
	if err != nil {
		return Verification{}, err
	}
	expected, err := payloadHash(capsule)
	if err != nil {
		return Verification{}, err
	}
	if capsule.CapsuleSHA256 != expected {
		return Verification{}, fmt.Errorf("capsule SHA256 mismatch: expected %s, got %s", expected, capsule.CapsuleSHA256)
	}

	previous := ""
	for _, source := range capsule.Sources {
		if source.Path <= previous {
			if source.Path == previous {
				return Verification{}, fmt.Errorf("duplicate source path %q", source.Path)
			}
			return Verification{}, errors.New("capsule sources are not in canonical path order")
		}
		previous = source.Path
		actual, err := hashRepositoryFile(repository, source.Path)
		if err != nil {
			return Verification{}, fmt.Errorf("source %q: %w", source.Path, err)
		}
		if actual != source.SHA256 {
			return Verification{}, fmt.Errorf("source %q SHA256 mismatch: expected %s, got %s", source.Path, source.SHA256, actual)
		}
	}
	verified := Verification{
		CapsuleSHA256:   capsule.CapsuleSHA256,
		BaseSHA:         capsule.BaseSHA,
		SourcesVerified: len(capsule.Sources),
		PolicyVersion:   capsule.PolicyVersion,
	}
	if capsule.OperationContext != nil {
		verified.OperationKind = capsule.OperationContext.Kind
	}
	return verified, nil
}

// VerifyFile parses and verifies a canonical capsule file. The capsule path
// itself must be a regular file reached without traversing symlinks.
func VerifyFile(repository, capsulePath string) (Verification, error) {
	file, err := openRegularNoSymlinks(capsulePath, MaxCapsuleBytes)
	if err != nil {
		return Verification{}, fmt.Errorf("open capsule: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxCapsuleBytes+1))
	if err != nil {
		return Verification{}, fmt.Errorf("read capsule: %w", err)
	}
	capsule, err := Parse(data)
	if err != nil {
		return Verification{}, err
	}
	verified, err := Verify(repository, capsule)
	if err != nil {
		return Verification{}, err
	}
	digest := sha256.Sum256(data)
	verified.SHA256 = hex.EncodeToString(digest[:])
	return verified, nil
}

func validateSpec(spec Spec) error {
	switch spec.PolicyVersion {
	case PolicyVersionV1:
		if spec.OperationContext != nil {
			return errors.New("operation_context is not valid for context-capsule-v1")
		}
	case PolicyVersionV2:
		if spec.OperationContext == nil {
			return errors.New("operation_context is required for context-capsule-v2")
		}
		if err := validateOperationContext(*spec.OperationContext); err != nil {
			return err
		}
		if spec.PredecessorOutcomes == nil {
			return errors.New("predecessor_outcomes must be an explicit array for context-capsule-v2")
		}
	default:
		return fmt.Errorf("unsupported context policy version %q", spec.PolicyVersion)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "project", value: spec.Project},
		{name: "plan", value: spec.Plan},
		{name: "roadmap_phase", value: spec.RoadmapPhase},
		{name: "execution_pack", value: spec.ExecutionPack},
		{name: "task", value: spec.Task},
		{name: "repository", value: spec.Repository},
	} {
		if err := validateText(field.name, field.value); err != nil {
			return err
		}
	}
	if err := validateCommitSHA("base_sha", spec.BaseSHA); err != nil {
		return err
	}
	if len(spec.Invariants) == 0 || len(spec.Invariants) > MaxListItems {
		return fmt.Errorf("invariants count must be between 1 and %d", MaxListItems)
	}
	if len(spec.NonGoals) == 0 || len(spec.NonGoals) > MaxListItems {
		return fmt.Errorf("non_goals count must be between 1 and %d", MaxListItems)
	}
	if len(spec.Sources) == 0 || len(spec.Sources) > MaxSources {
		return fmt.Errorf("sources count must be between 1 and %d", MaxSources)
	}
	if len(spec.PredecessorOutcomes) > MaxOutcomes {
		return fmt.Errorf("predecessor_outcomes count must not exceed %d", MaxOutcomes)
	}
	for index, item := range spec.Invariants {
		if err := validateText(fmt.Sprintf("invariants[%d]", index), item); err != nil {
			return err
		}
	}
	for index, item := range spec.NonGoals {
		if err := validateText(fmt.Sprintf("non_goals[%d]", index), item); err != nil {
			return err
		}
	}
	for index, outcome := range spec.PredecessorOutcomes {
		if err := validateOutcome(index, outcome); err != nil {
			return err
		}
	}
	for index, path := range spec.Sources {
		if err := validateSourcePath(path); err != nil {
			return fmt.Errorf("sources[%d]: %w", index, err)
		}
	}
	return nil
}

func validateCapsule(capsule Capsule) error {
	spec := Spec{
		PolicyVersion: capsule.PolicyVersion, Project: capsule.Project, Plan: capsule.Plan,
		RoadmapPhase: capsule.RoadmapPhase, ExecutionPack: capsule.ExecutionPack, Task: capsule.Task,
		OperationContext: capsule.OperationContext,
		Repository:       capsule.Repository, BaseSHA: capsule.BaseSHA, Invariants: capsule.Invariants,
		NonGoals: capsule.NonGoals, PredecessorOutcomes: capsule.PredecessorOutcomes,
		Sources: make([]string, len(capsule.Sources)),
	}
	for index, source := range capsule.Sources {
		spec.Sources[index] = source.Path
		if err := validateSHA256(fmt.Sprintf("sources[%d].sha256", index), source.SHA256); err != nil {
			return err
		}
	}
	if err := validateSpec(spec); err != nil {
		return err
	}
	if err := validateSHA256("capsule_sha256", capsule.CapsuleSHA256); err != nil {
		return err
	}
	data, err := json.Marshal(capsule)
	if err != nil {
		return fmt.Errorf("marshal capsule: %w", err)
	}
	if len(data) > MaxCapsuleBytes {
		return fmt.Errorf("capsule is %d bytes; maximum is %d", len(data), MaxCapsuleBytes)
	}
	return nil
}

func validateOperationContext(operation OperationContext) error {
	switch operation.Kind {
	case OperationDesignPlanning, OperationDesignReview, OperationImplementation,
		OperationImplementationReview, OperationAcceptance, OperationMergeAuthorization,
		OperationDeployment, OperationRecovery, OperationMaintenance:
	default:
		return fmt.Errorf("unsupported operation kind %q", operation.Kind)
	}
	if len(operation.OwnedScope) == 0 || len(operation.OwnedScope) > MaxListItems {
		return fmt.Errorf("operation_context.owned_scope count must be between 1 and %d", MaxListItems)
	}
	if len(operation.BlockingCriteria) == 0 || len(operation.BlockingCriteria) > MaxListItems {
		return fmt.Errorf("operation_context.blocking_criteria count must be between 1 and %d", MaxListItems)
	}
	for index, item := range operation.OwnedScope {
		if err := validateText(fmt.Sprintf("operation_context.owned_scope[%d]", index), item); err != nil {
			return err
		}
	}
	for index, item := range operation.BlockingCriteria {
		if err := validateText(fmt.Sprintf("operation_context.blocking_criteria[%d]", index), item); err != nil {
			return err
		}
	}
	return nil
}

func validateOutcome(index int, outcome Outcome) error {
	if err := validateText(fmt.Sprintf("predecessor_outcomes[%d].task", index), outcome.Task); err != nil {
		return err
	}
	if err := validateText(fmt.Sprintf("predecessor_outcomes[%d].summary", index), outcome.Summary); err != nil {
		return err
	}
	return validateCommitSHA(fmt.Sprintf("predecessor_outcomes[%d].commit_sha", index), outcome.CommitSHA)
}

func validateText(field, value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must be non-empty without surrounding whitespace", field)
	}
	if !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%s must be valid UTF-8 without NUL bytes", field)
	}
	if len(value) > MaxStringBytes {
		return fmt.Errorf("%s exceeds %d bytes", field, MaxStringBytes)
	}
	return nil
}

func validateCommitSHA(field, value string) error {
	if value != strings.ToLower(value) || (len(value) != 40 && len(value) != 64) {
		return fmt.Errorf("%s must be a lowercase full Git object ID", field)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("%s must be hexadecimal", field)
	}
	return nil
}

func validateSHA256(field, value string) error {
	if value != strings.ToLower(value) || len(value) != sha256.Size*2 {
		return fmt.Errorf("%s must be 64 lowercase hexadecimal characters", field)
	}
	if _, err := hex.DecodeString(value); err != nil {
		return fmt.Errorf("%s must be 64 lowercase hexadecimal characters", field)
	}
	return nil
}

func validateSourcePath(path string) error {
	if path == "" || filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return errors.New("source path must be repository-relative")
	}
	if strings.Contains(path, "\\") || filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))) != path || path == "." {
		return errors.New("source path must be a canonical slash-separated repository-relative path")
	}
	if path == ".." || strings.HasPrefix(path, "../") {
		return errors.New("source path escapes repository")
	}
	return nil
}

func verifyRepository(repository, identity, baseSHA string) (string, error) {
	if err := validateCommitSHA("base_sha", strings.ToLower(baseSHA)); err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(repository)
	if err != nil {
		return "", fmt.Errorf("resolve repository path: %w", err)
	}
	repository, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve repository path: %w", err)
	}
	info, err := os.Stat(repository)
	if err != nil || !info.IsDir() {
		return "", errors.New("repository path must be an existing directory")
	}
	root, err := gitOutput(repository, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("resolve Git repository root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve Git repository root: %w", err)
	}
	if filepath.Clean(root) != filepath.Clean(repository) {
		return "", fmt.Errorf("repository path %q is not Git root %q", repository, root)
	}
	head, err := gitOutput(repository, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve repository HEAD: %w", err)
	}
	if head != strings.ToLower(baseSHA) {
		return "", fmt.Errorf("repository HEAD %s does not match capsule base SHA %s", head, strings.ToLower(baseSHA))
	}
	remotes, err := gitOutput(repository, "remote")
	if err != nil {
		return "", fmt.Errorf("enumerate repository remotes: %w", err)
	}
	identityMatched := false
	for _, name := range strings.Fields(remotes) {
		remoteURLs, remoteErr := gitOutput(repository, "remote", "get-url", "--all", name)
		if remoteErr != nil {
			return "", fmt.Errorf("resolve repository remote %q: %w", name, remoteErr)
		}
		for _, remoteURL := range strings.Split(remoteURLs, "\n") {
			identityMatched = identityMatched || remoteIdentity(remoteURL) == identity
		}
	}
	if !identityMatched {
		return "", fmt.Errorf("capsule repository %q does not match any repository remote", identity)
	}
	return filepath.Clean(repository), nil
}

func remoteIdentity(remoteURL string) string {
	path := remoteURL
	if parsed, err := url.Parse(remoteURL); err == nil && parsed.Scheme != "" {
		path = parsed.Path
	} else if colon := strings.IndexByte(remoteURL, ':'); colon >= 0 && !strings.Contains(remoteURL[:colon], "/") {
		path = remoteURL[colon+1:]
	}
	path = strings.Trim(strings.ReplaceAll(path, "\\", "/"), "/")
	return strings.TrimSuffix(path, ".git")
}

func hashRepositoryFile(repository, relative string) (string, error) {
	if err := validateSourcePath(relative); err != nil {
		return "", err
	}
	path := filepath.Join(repository, filepath.FromSlash(relative))
	file, err := openRegularNoSymlinks(path, MaxSourceBytes)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func openRegularNoSymlinks(path string, maximum int64) (*os.File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	volume := filepath.VolumeName(absolute)
	cursor := volume + string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(absolute, cursor), string(filepath.Separator))
	for _, part := range parts {
		if part == "" {
			continue
		}
		cursor = filepath.Join(cursor, part)
		info, err := os.Lstat(cursor)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("path component %q is a symlink", cursor)
		}
	}
	file, err := os.Open(absolute)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("must be a regular file")
	}
	if info.Size() > maximum {
		file.Close()
		return nil, fmt.Errorf("file is %d bytes; maximum is %d", info.Size(), maximum)
	}
	return file, nil
}

func payloadHash(capsule Capsule) (string, error) {
	data, err := json.Marshal(payload{
		PolicyVersion: capsule.PolicyVersion, Project: capsule.Project, Plan: capsule.Plan,
		RoadmapPhase: capsule.RoadmapPhase, ExecutionPack: capsule.ExecutionPack, Task: capsule.Task,
		OperationContext: capsule.OperationContext,
		Repository:       capsule.Repository, BaseSHA: capsule.BaseSHA, Invariants: capsule.Invariants,
		NonGoals: capsule.NonGoals, PredecessorOutcomes: capsule.PredecessorOutcomes, Sources: capsule.Sources,
	})
	if err != nil {
		return "", fmt.Errorf("marshal capsule payload: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func cloneOperationContext(operation *OperationContext) *OperationContext {
	if operation == nil {
		return nil
	}
	clone := *operation
	clone.OwnedScope = append([]string{}, operation.OwnedScope...)
	clone.BlockingCriteria = append([]string{}, operation.BlockingCriteria...)
	return &clone
}

func gitOutput(repository string, args ...string) (string, error) {
	command := exec.Command("git", args...)
	command.Dir = repository
	command.Env = gitexec.Environment()
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

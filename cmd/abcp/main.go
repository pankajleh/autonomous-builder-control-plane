package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	contextcapsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	governancev3 "github.com/pankajleh/autonomous-builder-control-plane/internal/governance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/recovery"
	runctl "github.com/pankajleh/autonomous-builder-control-plane/internal/run"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
)

const version = "0.1.0-dev"

func main() {
	if runctl.IsContainmentChildV1(os.Args[1:]) {
		os.Exit(runctl.RunContainmentChildV1(os.Args[1:]))
	}
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

func runCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		usage(stderr)
		return 2
	}

	switch args[0] {
	case "version":
		if len(args) != 1 {
			usage(stderr)
			return 2
		}
		fmt.Fprintln(stdout, version)
		return 0
	case "validate-transition":
		if len(args) != 3 {
			fmt.Fprintln(stderr, "usage: abcp validate-transition <from> <to>")
			return 2
		}
		if err := domain.ValidateTransition(domain.State(args[1]), domain.State(args[2])); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "VALID")
		return 0
	case "run":
		return runCommand(args[1:], stdout, stderr)
	case "context-build":
		return contextBuildCommand(args[1:], stdout, stderr)
	case "context-verify":
		return contextVerifyCommand(args[1:], stdout, stderr)
	case "governance-usage-validate", "governance-checkpoint-validate", "governance-grant-validate",
		"governance-derivation-validate", "governance-candidate-validate", "governance-review-validate",
		"governance-review-advance", "governance-lease-issue", "governance-lease-begin", "governance-receipt-validate",
		"governance-activation-validate", "governance-activation-install":
		return governanceCommand(args[0], args[1:], stdout, stderr)
	case "recovery-inspect":
		return recoveryInspectCommand(args[1:], stdout, stderr)
	case "recovery-resume":
		return recoveryResumeCommand(args[1:], stdout, stderr)
	default:
		usage(stderr)
		return 2
	}
}

type usageValidationRequest struct {
	Repository  string                       `json:"repository,omitempty"`
	Capsule     contextcapsule.Capsule       `json:"capsule"`
	Operation   contextcapsule.OperationKind `json:"operation"`
	Mutation    bool                         `json:"mutation"`
	LeaseSHA256 string                       `json:"lease_sha256,omitempty"`
}

type candidateValidationRequest struct {
	Repository   string                 `json:"repository"`
	Capsule      contextcapsule.Capsule `json:"capsule"`
	CandidateSHA string                 `json:"candidate_sha"`
}

type reviewValidationRequest struct {
	Repository        string                                   `json:"repository,omitempty"`
	Capsule           contextcapsule.Capsule                   `json:"capsule"`
	CapsuleFileSHA256 string                                   `json:"capsule_file_sha256"`
	Registry          governancev3.SemanticAuthorityRegistryV1 `json:"registry"`
	Evidence          []governancev3.FindingEvidenceV1         `json:"evidence"`
	Previous          *governancev3.ReviewScopeReportV1        `json:"previous,omitempty"`
	Report            governancev3.ReviewScopeReportV1         `json:"report"`
}

type leaseIssueRequest struct {
	Repository string                                   `json:"repository"`
	Capsule    contextcapsule.Capsule                   `json:"capsule"`
	Registry   governancev3.SemanticAuthorityRegistryV1 `json:"registry"`
	Report     governancev3.ReviewScopeReportV1         `json:"report"`
	Limits     governancev3.MutationLimitsV1            `json:"limits"`
}

type receiptValidationRequest struct {
	Repository   string                 `json:"repository"`
	Capsule      contextcapsule.Capsule `json:"capsule"`
	LeaseSHA256  string                 `json:"lease_sha256"`
	CandidateSHA string                 `json:"candidate_sha"`
}

type leaseBeginRequest struct {
	Repository  string `json:"repository"`
	LeaseSHA256 string `json:"lease_sha256"`
}

type activationInstallRequest struct {
	Repository         string                              `json:"repository"`
	RepositoryIdentity string                              `json:"repository_identity"`
	Activation         governancev3.GovernanceActivationV1 `json:"activation"`
}

func governanceCommand(name string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	input := flags.String("input", "", "path to strict canonical governance JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *input == "" {
		fmt.Fprintf(stderr, "usage: abcp %s --input <path>\n", name)
		return 2
	}
	var result any = struct {
		Valid bool `json:"valid"`
	}{Valid: true}
	var err error
	switch name {
	case "governance-usage-validate":
		request := usageValidationRequest{}
		err = loadCanonicalGovernance(*input, &request)
		if err == nil {
			if request.Operation == contextcapsule.OperationImplementationReview && request.Mutation {
				var controller *governancev3.ControllerV1
				controller, err = governancev3.OpenControllerV1(request.Repository)
				if err == nil {
					err = controller.ValidateCapsuleUsageV3(request.Capsule, request.Operation, request.Mutation, request.LeaseSHA256)
				}
			} else {
				err = governancev3.ValidateCapsuleUsageV3(request.Capsule, request.Operation, request.Mutation, request.LeaseSHA256)
			}
		}
	case "governance-checkpoint-validate":
		request := governancev3.CheckpointAdvanceV1{}
		err = loadCanonicalGovernance(*input, &request)
		if err == nil {
			var controller *governancev3.ControllerV1
			controller, err = governancev3.OpenControllerV1(request.Repository)
			if err == nil {
				err = controller.AdvanceCheckpointV1(request)
			}
		}
	case "governance-grant-validate":
		request := governancev3.NextStageGrantV1{}
		err = loadCanonicalGovernance(*input, &request)
		if err == nil {
			err = governancev3.ValidateNextStageGrantV1(request)
		}
	case "governance-derivation-validate":
		request := governancev3.DerivationV3{}
		err = loadCanonicalGovernance(*input, &request)
		if err == nil {
			err = governancev3.ValidateDerivationV3(request)
		}
	case "governance-candidate-validate":
		request := candidateValidationRequest{}
		err = loadCanonicalGovernance(*input, &request)
		if err == nil {
			result, err = governancev3.ValidateCandidateV1(request.Repository, request.Capsule, request.CandidateSHA)
		}
	case "governance-review-validate":
		request := reviewValidationRequest{}
		err = loadCanonicalGovernance(*input, &request)
		if err == nil {
			err = governancev3.ValidateReviewScopeReportV1(request.Capsule, request.CapsuleFileSHA256, request.Registry, request.Evidence, request.Previous, request.Report)
		}
	case "governance-review-advance":
		request := reviewValidationRequest{}
		err = loadCanonicalGovernance(*input, &request)
		if err == nil {
			var controller *governancev3.ControllerV1
			controller, err = governancev3.OpenControllerV1(request.Repository)
			if err == nil {
				err = controller.AdvanceReviewTipV1(request.Repository, request.Capsule, request.CapsuleFileSHA256, request.Registry, request.Report)
			}
		}
	case "governance-lease-issue":
		request := leaseIssueRequest{}
		err = loadCanonicalGovernance(*input, &request)
		if err == nil {
			var controller *governancev3.ControllerV1
			controller, err = governancev3.OpenControllerV1(request.Repository)
			if err == nil {
				result, err = controller.IssueMutationLeaseV1(request.Capsule, request.Registry, request.Report, request.Limits)
			}
		}
	case "governance-lease-begin":
		request := leaseBeginRequest{}
		err = loadCanonicalGovernance(*input, &request)
		if err == nil {
			var controller *governancev3.ControllerV1
			controller, err = governancev3.OpenControllerV1(request.Repository)
			if err == nil {
				err = controller.BeginMutationLeaseV1(request.LeaseSHA256)
			}
		}
	case "governance-receipt-validate":
		request := receiptValidationRequest{}
		err = loadCanonicalGovernance(*input, &request)
		if err == nil {
			var controller *governancev3.ControllerV1
			controller, err = governancev3.OpenControllerV1(request.Repository)
			if err == nil {
				result, err = controller.CompleteMutationReceiptV1(request.Repository, request.Capsule, request.LeaseSHA256, request.CandidateSHA)
			}
		}
	case "governance-activation-validate":
		request := governancev3.GovernanceActivationV1{}
		err = loadCanonicalGovernance(*input, &request)
		if err == nil {
			err = governancev3.ValidateGovernanceActivationV1(request)
		}
	case "governance-activation-install":
		request := activationInstallRequest{}
		err = loadCanonicalGovernance(*input, &request)
		if err == nil {
			var controller *governancev3.ControllerV1
			controller, err = governancev3.OpenControllerV1(request.Repository)
			if err == nil {
				err = controller.InstallActivationV1(request.Repository, request.RepositoryIdentity, request.Activation)
			}
		}
	default:
		err = errors.New("unsupported governance diagnostic")
	}
	if err != nil {
		class := governancev3.ClassOf(err)
		if class != "" {
			fmt.Fprintf(stderr, "%s\n", class)
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := writeJSONOutput(stdout, result); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func loadCanonicalGovernance(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read governance input: %w", err)
	}
	if err := governancev3.ParseCanonical(data, target); err != nil {
		return fmt.Errorf("decode strict canonical governance input: %w", err)
	}
	return nil
}

func contextBuildCommand(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("context-build", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repository := flags.String("repository", "", "path to the governed Git repository root")
	specPath := flags.String("spec", "", "path to the structured context capsule spec JSON")
	outputPath := flags.String("output", "", "path for canonical context capsule JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *repository == "" || *specPath == "" || *outputPath == "" {
		fmt.Fprintln(stderr, "usage: abcp context-build --repository <path> --spec <path> --output <path>")
		return 2
	}
	spec, err := loadJSONFile[contextcapsule.Spec](*specPath, "context capsule spec")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	capsule, data, err := contextcapsule.Build(*repository, spec)
	if err != nil {
		fmt.Fprintf(stderr, "build context capsule: %v\n", err)
		return 1
	}
	if err := writeAtomicFile(*outputPath, data, 0o600); err != nil {
		fmt.Fprintf(stderr, "write context capsule: %v\n", err)
		return 1
	}
	if err := writeJSONOutput(stdout, struct {
		Path          string `json:"path"`
		SHA256        string `json:"sha256"`
		CapsuleSHA256 string `json:"capsule_sha256"`
	}{Path: *outputPath, SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), CapsuleSHA256: capsule.CapsuleSHA256}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func contextVerifyCommand(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("context-verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	repository := flags.String("repository", "", "path to the governed Git repository root")
	capsulePath := flags.String("capsule", "", "path to canonical context capsule JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *repository == "" || *capsulePath == "" {
		fmt.Fprintln(stderr, "usage: abcp context-verify --repository <path> --capsule <path>")
		return 2
	}
	verified, err := contextcapsule.VerifyFile(*repository, *capsulePath)
	if err != nil {
		fmt.Fprintf(stderr, "verify context capsule: %v\n", err)
		return 1
	}
	if err := writeJSONOutput(stdout, verified); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func writeAtomicFile(path string, data []byte, mode os.FileMode) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	directory := filepath.Dir(absolute)
	temporary, err := os.CreateTemp(directory, ".abcp-context-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, absolute)
}

func recoveryInspectCommand(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("recovery-inspect", flag.ContinueOnError)
	flags.SetOutput(stderr)
	ownershipPath := flags.String("ownership", "", "path to governed recovery ownership JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *ownershipPath == "" {
		fmt.Fprintln(stderr, "usage: abcp recovery-inspect --ownership <path>")
		return 2
	}
	ownership, err := loadJSONFile[recovery.Ownership](*ownershipPath, "recovery ownership")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	inspection := recovery.Inspect(context.Background(), ownership)
	if err := writeJSONOutput(stdout, inspection); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func recoveryResumeCommand(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("recovery-resume", flag.ContinueOnError)
	flags.SetOutput(stderr)
	requestPath := flags.String("request", "", "path to an explicitly authorized recovery request")
	ledgerPath := flags.String("ledger", "", "path to the append-only JSONL ledger")
	evidenceRoot := flags.String("evidence-root", "", "root directory for immutable recovery evidence")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *requestPath == "" || *ledgerPath == "" || *evidenceRoot == "" {
		fmt.Fprintln(stderr, "usage: abcp recovery-resume --request <path> --ledger <path> --evidence-root <path>")
		return 2
	}

	request, err := loadJSONFile[recovery.WorkflowRequest](*requestPath, "recovery request")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	canonicalLedger, err := canonicalLedgerDestination(*ledgerPath, *evidenceRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	artifacts, err := evidence.NewStore(*evidenceRoot, request.Ownership.Attempt.RunID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	events, err := ledger.NewJSONLLedger(canonicalLedger)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	workflow, err := recovery.NewWorkflow(events, artifacts, recovery.GovernedCleaner{})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := workflow.Recover(ctx, request)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := writeJSONOutput(stdout, result); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func loadJSONFile[T any](path, description string) (T, error) {
	var value T
	file, err := os.Open(path)
	if err != nil {
		return value, fmt.Errorf("open %s: %w", description, err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("decode %s: %w", description, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return value, fmt.Errorf("decode %s: multiple JSON values", description)
		}
		return value, fmt.Errorf("decode %s: %w", description, err)
	}
	return value, nil
}

func writeJSONOutput(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode output: %w", err)
	}
	return nil
}

func runCommand(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "", "path to the governed authority manifest")
	ledgerPath := flags.String("ledger", "", "path to the append-only JSONL ledger")
	evidenceRoot := flags.String("evidence-root", "", "root directory for immutable run evidence")
	cgroupRoot := flags.String("cgroup-root", "/sys/fs/cgroup", "controller cgroup v2 root")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *manifestPath == "" || *ledgerPath == "" || *evidenceRoot == "" {
		fmt.Fprintln(stderr, "usage: abcp run --manifest <path> --ledger <path> --evidence-root <path>")
		return 2
	}

	manifest, err := loadManifest(*manifestPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	controller, err := governancev3.OpenControllerV1(manifest.Repository.Path)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	governed, err := authority.NewWithGovernanceController(manifest, controller)
	if err != nil {
		fmt.Fprintf(stderr, "validate authority: %v\n", err)
		return 1
	}
	canonicalLedger, err := canonicalLedgerDestination(*ledgerPath, *evidenceRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	artifacts, err := evidence.NewStore(*evidenceRoot, governed.RunID())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	events, err := ledger.NewJSONLLedger(canonicalLedger)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	processes, err := runctl.NewLinuxContainedCommandRunner(supervisor.New(), *cgroupRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	runner, err := runctl.NewWithController(governed, events, artifacts, processes, controller)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result, err := runner.Run(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		if result.State != "" {
			fmt.Fprintln(stderr, result.State)
		}
		return 1
	}
	if result.State == domain.StateImplementationCompleted {
		fmt.Fprintln(stdout, result.State)
		return 0
	}
	if !result.Accepted() {
		fmt.Fprintln(stderr, result.State)
		if result.FailureReason != "" {
			fmt.Fprintln(stderr, result.FailureReason)
		}
		return 1
	}
	fmt.Fprintln(stdout, result.State)
	return 0
}

func canonicalLedgerDestination(path, evidenceRoot string) (string, error) {
	canonical, err := canonicalFuturePath(path)
	if err != nil {
		return "", fmt.Errorf("resolve ledger path: %w", err)
	}
	evidenceRoot, err = canonicalFuturePath(evidenceRoot)
	if err != nil {
		return "", fmt.Errorf("canonicalize evidence root: %w", err)
	}
	relative, err := filepath.Rel(evidenceRoot, canonical)
	if err != nil {
		return "", fmt.Errorf("compare ledger and evidence paths: %w", err)
	}
	if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("ledger path must be outside the evidence root")
	}
	return canonical, nil
}

func canonicalFuturePath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return resolveFuturePath(filepath.Clean(absolute), 0)
}

func resolveFuturePath(path string, symlinkDepth int) (string, error) {
	if symlinkDepth > 255 {
		return "", errors.New("too many symlinks while resolving path")
	}
	volume := filepath.VolumeName(path)
	root := volume + string(filepath.Separator)
	relative := strings.TrimPrefix(path, root)
	components := strings.Split(relative, string(filepath.Separator))
	cursor := root
	for index, component := range components {
		if component == "" {
			continue
		}
		candidate := filepath.Join(cursor, component)
		info, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return filepath.Join(append([]string{candidate}, components[index+1:]...)...), nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			cursor = candidate
			continue
		}
		target, err := os.Readlink(candidate)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(cursor, target)
		}
		return resolveFuturePath(filepath.Join(append([]string{target}, components[index+1:]...)...), symlinkDepth+1)
	}
	return filepath.Clean(cursor), nil
}

func loadManifest(path string) (authority.Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return authority.Manifest{}, fmt.Errorf("open manifest: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var manifest authority.Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return authority.Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return authority.Manifest{}, fmt.Errorf("decode manifest: multiple JSON values")
		}
		return authority.Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	return manifest, nil
}

func usage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: abcp <version|validate-transition|context-build|context-verify|governance-*-validate|governance-lease-issue|run|recovery-inspect|recovery-resume>")
}

package main

import (
	"context"
	"crypto/rand"
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
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/actionapi"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/actioncontrol"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	contextcapsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	governancev3 "github.com/pankajleh/autonomous-builder-control-plane/internal/governance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/readmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/recovery"
	runctl "github.com/pankajleh/autonomous-builder-control-plane/internal/run"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runadmission"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi/platformbridge"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/supervisor"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/timeline"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/workflowauthoritypg"
)

const version = "0.1.0-dev"

type workflowAuthorityBackend interface {
	governancev3.WorkflowAuthorityBackendV1
	EnsureInitialized(context.Context, string, string) error
	Close()
}

var openWorkflowAuthorityBackend = func(ctx context.Context, path string) (workflowAuthorityBackend, error) {
	return workflowauthoritypg.Open(ctx, path)
}

func main() {
	if platformbridge.IsContainmentChildV1(os.Args[1:]) {
		os.Exit(platformbridge.RunContainmentChildV1(os.Args[1:]))
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
	case "serve":
		return serveCommand(args[1:], stderr)
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
	serviceRoot := flags.String("service-root", "", "optional EP-006 service root for runtime registration")
	workflowAuthorityConfigFile := flags.String("workflow-authority-config-file", "", "optional protected PostgreSQL workflow-authority config file")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *manifestPath == "" || *ledgerPath == "" || *evidenceRoot == "" {
		fmt.Fprintln(stderr, "usage: abcp run --manifest <path> --ledger <path> --evidence-root <path> [--workflow-authority-config-file <protected-file>]")
		return 2
	}

	manifest, err := loadManifest(*manifestPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var controller *governancev3.ControllerV1
	var workflowBackend workflowAuthorityBackend
	if *workflowAuthorityConfigFile != "" {
		workflowBackend, err = openWorkflowAuthorityBackend(context.Background(), *workflowAuthorityConfigFile)
		if err != nil {
			fmt.Fprintln(stderr, "open workflow authority backend")
			return 1
		}
		defer workflowBackend.Close()
		controller, err = governancev3.OpenControllerWithAuthorityBackendV1(manifest.Repository.Path, workflowBackend)
		if err == nil {
			err = workflowBackend.EnsureInitialized(context.Background(), controller.ControllerIdentity(), controller.RepositoryIdentity())
		}
	} else {
		controller, err = governancev3.OpenControllerV1(manifest.Repository.Path)
	}
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
	defer events.Close()
	processes, err := platformbridge.NewContainedCommandRunner(supervisor.New(), *cgroupRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	runner, err := runctl.NewWithController(governed, events, artifacts, processes, controller)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancelAPI := context.WithCancelCause(signalCtx)
	defer cancelAPI(nil)
	var catalog *runtimecatalog.Catalog
	var owner runtimecatalog.ActiveOwnerLeaseV1
	var watcher *actioncontrol.CancelWatcher
	var actionJournal *actioncontrol.Journal
	var runnerSnapshots *actioncontrol.RunnerSnapshotCoordinator
	if *serviceRoot != "" {
		catalog, owner, err = registerRuntimeOwner(*serviceRoot, governed, events, artifacts.RunDir())
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		defer catalog.Close()
		runnerSnapshots, err = actioncontrol.NewRunnerSnapshotCoordinator(*serviceRoot)
		if err != nil {
			fmt.Fprintln(stderr, "initialize service runner snapshot authority")
			return 1
		}
		defer runnerSnapshots.Close()
		if err := runner.SetSnapshotCoordinator(runnerSnapshots); err != nil {
			fmt.Fprintln(stderr, "initialize service runner snapshot authority")
			return 1
		}
		actionJournal, err = actioncontrol.Open(*serviceRoot)
		if err != nil {
			fmt.Fprintln(stderr, "open service action journal safely")
			return 1
		}
		defer actionJournal.Close()
		var watcherKey [32]byte
		if _, err = rand.Read(watcherKey[:]); err != nil {
			fmt.Fprintln(stderr, "initialize service owner watcher")
			return 1
		}
		watcherSigner, signerErr := serviceapi.NewCursorSigner("owner-watcher", watcherKey[:])
		if signerErr != nil {
			fmt.Fprintln(stderr, "initialize service owner watcher")
			return 1
		}
		localWatcherModel, modelErr := readmodel.New(catalog, watcherSigner)
		if modelErr != nil {
			fmt.Fprintln(stderr, "initialize service owner watcher")
			return 1
		}
		watcherModel, modelErr := actioncontrol.NewGuardedReadModel(*serviceRoot, localWatcherModel)
		if modelErr != nil {
			fmt.Fprintln(stderr, "initialize service owner watcher")
			return 1
		}
		defer watcherModel.Close()
		watcher, err = actioncontrol.NewCancelWatcher(actioncontrol.CancelWatcherConfig{
			Journal: actionJournal, Catalog: catalog, ReadModel: watcherModel, Owner: owner, CancelCause: cancelAPI,
		})
		if err != nil {
			fmt.Fprintln(stderr, "initialize service owner watcher")
			return 1
		}
		if err := runner.SetFinalizationHook(watcher); err != nil {
			fmt.Fprintln(stderr, "initialize service owner finalization")
			return 1
		}
		watcher.Start()
	}
	result, err := runner.Run(ctx)
	if watcher != nil {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		retireErr := watcher.Close(closeCtx)
		closeCancel()
		if retireErr != nil {
			fmt.Fprintln(stderr, retireErr)
			return 1
		}
	}
	if runnerSnapshots != nil {
		if closeErr := runnerSnapshots.Close(); closeErr != nil {
			fmt.Fprintln(stderr, "close service runner snapshot authority safely")
			return 1
		}
	}
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

func registerRuntimeOwner(serviceRoot string, governed authority.Authority, events *ledger.JSONLLedger, evidenceRoot string) (*runtimecatalog.Catalog, runtimecatalog.ActiveOwnerLeaseV1, error) {
	catalog, err := runtimecatalog.Open(serviceRoot)
	if err != nil {
		return nil, runtimecatalog.ActiveOwnerLeaseV1{}, errors.New("open service runtime catalog safely")
	}
	fail := func(message string, _ error) (*runtimecatalog.Catalog, runtimecatalog.ActiveOwnerLeaseV1, error) {
		catalog.Close()
		return nil, runtimecatalog.ActiveOwnerLeaseV1{}, errors.New(message)
	}
	canonicalEvidence, err := canonicalFuturePath(evidenceRoot)
	if err != nil {
		return fail("canonicalize registered evidence root", err)
	}
	if events == nil {
		return fail("identify exact registered ledger generation", errors.New("ledger is required"))
	}
	exactGeneration, err := events.PhysicalGeneration()
	if err != nil {
		return fail("identify exact registered ledger generation", err)
	}
	ledgerPath := events.Path()
	now := time.Now().UTC()
	runRegistration, err := runtimecatalog.NewRunRegistrationV1(governed.RunID(), governed.Repository().Identity, governed.SHA256(), ledgerPath, canonicalEvidence, now)
	if err != nil {
		return fail("construct run registration", err)
	}
	registeredGeneration := runRegistration.LedgerGeneration
	if exactGeneration.ParentDevice != registeredGeneration.ParentDevice || exactGeneration.ParentInode != registeredGeneration.ParentInode ||
		exactGeneration.FileDevice != registeredGeneration.FileDevice || exactGeneration.FileInode != registeredGeneration.FileInode {
		return fail("bind exact registered ledger generation", runtimecatalog.ErrIntegrity)
	}
	if existing, readErr := catalog.ReadRun(governed.RunID()); readErr == nil {
		runRegistration.InitialRegistrationTimestamp = existing.InitialRegistrationTimestamp
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return fail("read existing run registration", readErr)
	}
	if err := catalog.RegisterRun(runRegistration); err != nil {
		return fail("register service run", err)
	}
	attemptID := runctl.DerivedTransitionProvenance(governed).AttemptID
	attemptRegistration, err := runtimecatalog.NewAttemptRegistrationV1(governed.RunID(), attemptID, governed.SHA256(), now)
	if err != nil {
		return fail("construct attempt registration", err)
	}
	if existing, readErr := catalog.ReadAttempt(governed.RunID(), attemptID); readErr == nil {
		attemptRegistration.RegistrationTimestamp = existing.RegistrationTimestamp
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return fail("read existing attempt registration", readErr)
	}
	if err := catalog.RegisterAttempt(attemptRegistration); err != nil {
		return fail("register service attempt", err)
	}
	process, err := recovery.CaptureProcessIdentity(os.Getpid())
	if err != nil {
		return fail("capture exact service owner process identity", err)
	}
	owner, err := catalog.InstallOwnerLease(governed.RunID(), attemptID, process)
	if err != nil {
		return fail("install active service owner", err)
	}
	return catalog, owner, nil
}

func retireRuntimeOwner(catalog *runtimecatalog.Catalog, owner runtimecatalog.ActiveOwnerLeaseV1) error {
	guard, err := catalog.AcquireOwnerLeaseGuard(owner.RunID)
	if err != nil {
		return errors.New("acquire owner shutdown guard")
	}
	defer guard.Close()
	if _, err := guard.MarkClosing(owner.LeaseID, 0); err != nil {
		return errors.New("close active service owner")
	}
	if _, err := guard.Retire(owner.LeaseID); err != nil {
		return errors.New("retire active service owner")
	}
	return nil
}

func serveCommand(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	serviceRoot := flags.String("service-root", "", "canonical EP-006 service root")
	listen := flags.String("listen", "127.0.0.1:8080", "loopback IP listener")
	tokenFile := flags.String("token-file", "", "protected bearer token file")
	principalID := flags.String("principal-id", "", "stable service principal identifier")
	cursorKeyFile := flags.String("cursor-key-file", "", "protected cursor key file")
	grantsFile := flags.String("authority-grants-file", "", "protected exact authority grant file")
	admissionProfileFile := flags.String("admission-profile-file", "", "optional protected run admission profile file")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *serviceRoot == "" || *tokenFile == "" || *principalID == "" || *cursorKeyFile == "" || *grantsFile == "" {
		fmt.Fprintln(stderr, "usage: abcp serve --service-root <path> --listen <loopback-ip:port> --token-file <path> --principal-id <id> --cursor-key-file <path> --authority-grants-file <path> [--admission-profile-file <protected-file>]")
		return 2
	}
	if err := serviceapi.ValidateLoopbackAddress(*listen); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	authenticator, err := serviceapi.LoadBearerAuthenticator(*tokenFile, *principalID)
	if err != nil {
		fmt.Fprintln(stderr, "load bearer authentication configuration")
		return 1
	}
	cursors, err := serviceapi.LoadCursorSigner(*cursorKeyFile)
	if err != nil {
		fmt.Fprintln(stderr, "load cursor signing configuration")
		return 1
	}
	authorityMatcher, err := serviceapi.LoadAuthorityMatcher(*grantsFile)
	if err != nil {
		fmt.Fprintln(stderr, "load authority grant configuration")
		return 1
	}
	catalog, err := runtimecatalog.Open(*serviceRoot)
	if err != nil {
		fmt.Fprintln(stderr, "open service runtime catalog safely")
		return 1
	}
	defer catalog.Close()
	var admissions *runadmission.Controller
	if *admissionProfileFile != "" {
		admissions, err = runadmission.NewController(runadmission.Config{
			ProfileFile: *admissionProfileFile, ServiceRoot: *serviceRoot, Catalog: catalog,
		})
		if err != nil {
			fmt.Fprintln(stderr, "load run admission configuration")
			return 1
		}
		defer admissions.Close()
	}
	localReadService, err := readmodel.New(catalog, cursors)
	if err != nil {
		fmt.Fprintln(stderr, "construct authoritative read service")
		return 1
	}
	readService, err := actioncontrol.NewGuardedReadModel(*serviceRoot, localReadService)
	if err != nil {
		fmt.Fprintln(stderr, "construct authoritative read service")
		return 1
	}
	defer readService.Close()
	timelineService, err := timeline.New(readService, catalog, cursors)
	if err != nil {
		fmt.Fprintln(stderr, "construct timeline service")
		return 1
	}
	journal, err := actioncontrol.Open(*serviceRoot)
	if err != nil {
		fmt.Fprintln(stderr, "open service action journal safely")
		return 1
	}
	defer journal.Close()
	actions, err := actionapi.NewController(actionapi.ControllerConfig{Catalog: catalog, ReadModel: readService, Journal: journal, Authority: authorityMatcher})
	if err != nil {
		fmt.Fprintln(stderr, "construct governed action controller")
		return 1
	}
	defer actions.Close()
	server, err := serviceapi.NewServer(serviceapi.ServerConfig{
		Authenticator: authenticator, Authority: authorityMatcher, Catalog: catalog, CursorSigner: cursors,
		RunProjections: readService, Events: readService, Timeline: timelineService, Evidence: timelineService, Actions: actions,
		RunAdmission: admissions,
	})
	if err != nil {
		fmt.Fprintln(stderr, "construct service API")
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := server.ListenAndServe(ctx, *listen); err != nil {
		fmt.Fprintln(stderr, "serve loopback API")
		return 1
	}
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
	fmt.Fprintln(writer, "usage: abcp <version|validate-transition|context-build|context-verify|governance-*-validate|governance-lease-issue|run|serve|recovery-inspect|recovery-resume>")
}

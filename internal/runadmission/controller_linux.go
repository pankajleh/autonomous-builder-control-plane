//go:build linux

package runadmission

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	contextcapsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/workflowauthoritypg"
)

type runBinding struct {
	repositoryIdentityDigest string
	authorityDigest          string
	ledgerPath               string
	evidenceRoot             string
}

func NewController(config Config) (*Controller, error) {
	if config.ProfileFile == "" || config.Catalog == nil || !canonicalAbsolute(config.ServiceRoot) {
		return nil, serviceapi.ErrAdmissionUnavailable
	}
	profileFile, err := filepath.Abs(config.ProfileFile)
	if err != nil || filepath.Clean(profileFile) != profileFile {
		return nil, serviceapi.ErrAdmissionUnavailable
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	executable := config.Executable
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return nil, serviceapi.ErrAdmissionUnavailable
		}
	}
	executable, err = filepath.Abs(executable)
	if err != nil || filepath.Clean(executable) != executable {
		return nil, serviceapi.ErrAdmissionUnavailable
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return nil, serviceapi.ErrAdmissionUnavailable
	}

	profiles, err := loadProfiles(profileFile)
	if err != nil {
		return nil, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	rootFD, err := openAbsoluteDirectory(config.ServiceRoot, false)
	if err != nil {
		return nil, serviceapi.ErrAdmissionUnavailable
	}
	defer syscall.Close(rootFD)
	admissionsFD, err := openChildDirectory(rootFD, "admissions", true)
	if err != nil {
		return nil, serviceapi.ErrAdmissionUnavailable
	}
	if err := validatePrivateDirectoryFD(admissionsFD); err != nil {
		syscall.Close(admissionsFD)
		return nil, serviceapi.ErrAdmissionUnavailable
	}
	launchesFD, err := openChildDirectory(admissionsFD, "launches", true)
	if err != nil {
		syscall.Close(admissionsFD)
		return nil, serviceapi.ErrAdmissionUnavailable
	}
	if err := validatePrivateDirectoryFD(launchesFD); err != nil {
		syscall.Close(launchesFD)
		syscall.Close(admissionsFD)
		return nil, serviceapi.ErrAdmissionUnavailable
	}
	if err := ensureLockFile(admissionsFD, ".lock"); err != nil || syscall.Fsync(admissionsFD) != nil {
		syscall.Close(launchesFD)
		syscall.Close(admissionsFD)
		return nil, serviceapi.ErrAdmissionUnavailable
	}
	controller := &Controller{
		serviceRoot: config.ServiceRoot, executable: executable, catalog: config.Catalog,
		clock: config.Clock, profiles: profiles, admissionsFD: admissionsFD, launchesFD: launchesFD,
	}
	controller.start = startProcess
	return controller, nil
}

func loadProfiles(path string) (map[string]loadedProfile, error) {
	data, err := readProtectedFile(path, MaxProfileFileBytes)
	if err != nil {
		return nil, err
	}
	var file ProfileFileV1
	if err := strictJSON(data, &file); err != nil || file.SchemaVersion != 1 || len(file.Profiles) == 0 || len(file.Profiles) > MaxProfiles {
		return nil, errors.New("invalid admission profile configuration")
	}
	profiles := make(map[string]loadedProfile, len(file.Profiles))
	for _, configuration := range file.Profiles {
		if runtimecatalog.ValidateIdentifier(configuration.ProfileID) != nil || len(configuration.ProfileID) > 128 {
			return nil, errors.New("invalid admission profile identifier")
		}
		if _, duplicate := profiles[configuration.ProfileID]; duplicate {
			return nil, errors.New("duplicate admission profile identifier")
		}
		profile, err := loadProfile(configuration)
		if err != nil {
			return nil, err
		}
		profiles[configuration.ProfileID] = profile
	}
	return profiles, nil
}

func loadProfile(configuration ProfileV1) (loadedProfile, error) {
	if configuration.RepositoryIdentity == "" || len(configuration.RepositoryIdentity) > contextcapsule.MaxStringBytes ||
		configuration.RepositoryIdentity != strings.TrimSpace(configuration.RepositoryIdentity) || strings.ContainsRune(configuration.RepositoryIdentity, 0) {
		return loadedProfile{}, errors.New("invalid repository identity")
	}
	for _, path := range []string{configuration.RepositoryPath, configuration.ManifestTemplatePath, configuration.LedgerRoot, configuration.EvidenceRoot, configuration.CgroupRoot, configuration.WorkflowAuthorityConfigPath} {
		if !canonicalAbsolute(path) {
			return loadedProfile{}, errors.New("profile paths must be canonical absolute paths")
		}
	}
	repositoryFD, err := openAbsoluteDirectory(configuration.RepositoryPath, false)
	if err != nil {
		return loadedProfile{}, err
	}
	defer syscall.Close(repositoryFD)
	root, err := gitOutput(configuration.RepositoryPath, "rev-parse", "--show-toplevel")
	if err != nil || root != configuration.RepositoryPath {
		return loadedProfile{}, errors.New("repository path is not the Git root")
	}
	if !canonicalRelativeDirectory(configuration.InputDirectory) {
		return loadedProfile{}, errors.New("input directory must be repository-relative")
	}
	probe := configuration.InputDirectory + "/.abcp-admission-ignore-probe"
	if _, err := gitOutput(configuration.RepositoryPath, "check-ignore", "-q", "--no-index", "--", probe); err != nil {
		return loadedProfile{}, errors.New("admission input directory is not Git-ignored")
	}
	inputFD, err := openRelativeDirectory(repositoryFD, configuration.InputDirectory, true)
	if err != nil {
		return loadedProfile{}, err
	}
	if err := validateRepositoryInputDirectoryFD(inputFD); err != nil {
		syscall.Close(inputFD)
		return loadedProfile{}, err
	}
	syscall.Close(inputFD)
	for _, root := range []struct {
		path    string
		create  bool
		private bool
	}{{configuration.LedgerRoot, true, true}, {configuration.EvidenceRoot, true, true}, {configuration.CgroupRoot, false, false}} {
		fd, err := openAbsoluteDirectory(root.path, root.create)
		if err != nil {
			return loadedProfile{}, err
		}
		if root.private {
			err = validatePrivateDirectoryFD(fd)
		}
		syscall.Close(fd)
		if err != nil {
			return loadedProfile{}, err
		}
	}
	if pathsOverlap(configuration.LedgerRoot, configuration.EvidenceRoot) {
		return loadedProfile{}, errors.New("ledger and evidence roots overlap")
	}
	if err := workflowauthoritypg.ValidateConfigFile(configuration.WorkflowAuthorityConfigPath); err != nil {
		return loadedProfile{}, errors.New("workflow authority configuration is invalid")
	}
	templateData, err := readProtectedFile(configuration.ManifestTemplatePath, MaxManifestTemplateBytes)
	if err != nil {
		return loadedProfile{}, err
	}
	var manifest authority.Manifest
	if err := strictJSON(templateData, &manifest); err != nil {
		return loadedProfile{}, errors.New("invalid admission manifest template")
	}
	if !manifest.Worktree.Enabled || manifest.Governance != nil || manifest.Ralphex.ExecutionState != nil {
		return loadedProfile{}, errors.New("manifest template is not a bounded V2 implementation template")
	}
	manifest.Repository.Path = configuration.RepositoryPath
	manifest.Repository.Identity = configuration.RepositoryIdentity
	validation := manifest
	validation.RunID = "admission-profile-validation"
	validation.Repository.StartSHA, err = gitOutput(configuration.RepositoryPath, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return loadedProfile{}, errors.New("repository HEAD is unavailable")
	}
	validationPlan := filepath.Join(configuration.RepositoryPath, ".git", "HEAD")
	validationPlanData, err := os.ReadFile(validationPlan)
	if err != nil {
		return loadedProfile{}, errors.New("repository HEAD binding is unavailable")
	}
	validation.Plan = authority.PlanManifest{Path: validationPlan, SHA256: digest(validationPlanData)}
	validation.ContextCapsule = nil
	validation.Worktree.Branch = "abcp/admission-profile-validation"
	governed, err := authority.New(validation)
	if err != nil {
		return loadedProfile{}, errors.New("invalid private admission policy")
	}
	manifest = governed.Manifest()
	manifest.RunID = ""
	manifest.Repository.StartSHA = ""
	manifest.Plan = authority.PlanManifest{}
	manifest.ContextCapsule = nil
	manifest.Worktree.Branch = ""
	return loadedProfile{
		configuration: configuration, template: manifest,
		inputPath:  filepath.Join(configuration.RepositoryPath, filepath.FromSlash(configuration.InputDirectory)),
		ledgerRoot: configuration.LedgerRoot, evidenceRoot: configuration.EvidenceRoot, cgroupRoot: configuration.CgroupRoot,
		workflowAuthorityConfigPath: configuration.WorkflowAuthorityConfigPath,
	}, nil
}

func (c *Controller) admitRun(ctx context.Context, principal serviceapi.Principal, request serviceapi.RunAdmissionRequestV1) (serviceapi.RunAdmissionResponseV1, error) {
	if c == nil || c.admissionsFD < 0 || c.launchesFD < 0 || c.start == nil || serviceapi.ValidateRunAdmissionRequestV1(request) != nil ||
		serviceapi.ValidatePrincipalID(principal.PrincipalID) != nil {
		return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrAdmissionUnavailable
	}
	profile, ok := c.profiles[request.ProfileID]
	if !ok {
		return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrUnknownAdmissionProfile
	}
	digest := RequestDigest(request)
	runID := DeriveRunID(principal.PrincipalID, request.RequestID)
	response := serviceapi.RunAdmissionResponseV1{RunID: runID, RunURL: "/v1/runs/" + runID}
	receipt := AdmissionReceiptV1{
		Kind: "AdmissionReceiptV1", SchemaVersion: 1, Principal: principal, RequestID: request.RequestID,
		CanonicalRequestSHA256: digest, RunID: runID, ProfileID: request.ProfileID,
		CreatedAt: c.clock().UTC().Format(time.RFC3339Nano),
	}
	if err := c.createOrVerifyReceipt(ctx, receipt); err != nil {
		return serviceapi.RunAdmissionResponseV1{}, err
	}
	if registered, err := c.registeredReplay(profile, principal, request, runID); err != nil {
		return serviceapi.RunAdmissionResponseV1{}, err
	} else if registered {
		return response, nil
	}
	lock, acquired, err := acquireLaunchLock(c.launchesFD, runID)
	if err != nil {
		return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrAdmissionUnavailable
	}
	if !acquired {
		if registered, readErr := c.registeredReplay(profile, principal, request, runID); readErr != nil {
			return serviceapi.RunAdmissionResponseV1{}, readErr
		} else if registered {
			return response, nil
		}
		return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrReconciliationRequired
	}
	started := false
	defer func() {
		if !started {
			lock.Close()
		}
	}()
	if registered, err := c.registeredReplay(profile, principal, request, runID); err != nil {
		return serviceapi.RunAdmissionResponseV1{}, err
	} else if registered {
		return response, nil
	}
	head, err := gitOutput(profile.configuration.RepositoryPath, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrAdmissionUnavailable
	}
	if head != request.RepositoryBaseSHA {
		return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrRepositoryBaseMismatch
	}
	manifestPath, binding, err := materialize(profile, principal, request, runID)
	if err != nil {
		return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	if registered, err := c.runRegistered(runID, binding); err != nil {
		return serviceapi.RunAdmissionResponseV1{}, err
	} else if registered {
		return response, nil
	}
	args := []string{
		"run", "--manifest", manifestPath, "--ledger", binding.ledgerPath,
		"--evidence-root", profile.evidenceRoot, "--cgroup-root", profile.cgroupRoot,
		"--service-root", c.serviceRoot,
		"--workflow-authority-config-file", profile.workflowAuthorityConfigPath,
	}
	if err := c.start(c.executable, args, lock); err != nil {
		return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrAdmissionUnavailable
	}
	started = true
	if err := lock.Close(); err != nil {
		return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrAdmissionUnavailable
	}
	return response, nil
}

func (c *Controller) registeredReplay(profile loadedProfile, principal serviceapi.Principal, request serviceapi.RunAdmissionRequestV1, runID string) (bool, error) {
	registration, err := c.catalog.ReadRun(runID)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, serviceapi.ErrAdmissionUnavailable
	}
	binding, err := existingRunBinding(profile, principal, request, runID)
	if err != nil || !registrationMatches(registration, runID, binding) {
		return false, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	return true, nil
}

func (c *Controller) runRegistered(runID string, expected runBinding) (bool, error) {
	registration, err := c.catalog.ReadRun(runID)
	if err == nil {
		if !registrationMatches(registration, runID, expected) {
			return false, serviceapi.ErrUnsafeAdmissionMaterialization
		}
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, serviceapi.ErrAdmissionUnavailable
}

func registrationMatches(registration runtimecatalog.RunRegistrationV1, runID string, expected runBinding) bool {
	return registration.RunID == runID && registration.RepositoryIdentityDigest == expected.repositoryIdentityDigest &&
		registration.AuthorityDigest == expected.authorityDigest && registration.CanonicalLedgerPath == expected.ledgerPath &&
		registration.CanonicalEvidenceRoot == expected.evidenceRoot
}

func (c *Controller) createOrVerifyReceipt(ctx context.Context, expected AdmissionReceiptV1) error {
	lockFD, err := syscall.Openat(c.admissionsFD, ".lock", syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return serviceapi.ErrAdmissionUnavailable
	}
	defer syscall.Close(lockFD)
	if err := boundedFlock(ctx, lockFD, ReceiptLockTimeout); err != nil {
		return serviceapi.ErrAdmissionUnavailable
	}
	defer syscall.Flock(lockFD, syscall.LOCK_UN)
	name := receiptKey(expected.Principal.PrincipalID, expected.RequestID) + ".json"
	data, found, err := readRegularAt(c.admissionsFD, name, MaxReceiptBytes)
	if err != nil {
		return serviceapi.ErrUnsafeAdmissionMaterialization
	}
	if found {
		var observed AdmissionReceiptV1
		if strictJSON(data, &observed) != nil || validateReceipt(observed, name) != nil {
			return serviceapi.ErrUnsafeAdmissionMaterialization
		}
		canonical, _ := json.Marshal(observed)
		if !bytes.Equal(data, canonical) {
			return serviceapi.ErrUnsafeAdmissionMaterialization
		}
		if observed.Principal != expected.Principal || observed.RequestID != expected.RequestID ||
			observed.CanonicalRequestSHA256 != expected.CanonicalRequestSHA256 || observed.RunID != expected.RunID || observed.ProfileID != expected.ProfileID {
			return serviceapi.ErrRequestIDConflict
		}
		return nil
	}
	data, err = json.Marshal(expected)
	if err != nil || len(data) > MaxReceiptBytes {
		return serviceapi.ErrUnsafeAdmissionMaterialization
	}
	if err := writeNewAtomicAt(c.admissionsFD, name, data); err != nil {
		return serviceapi.ErrAdmissionUnavailable
	}
	return nil
}

func validateReceipt(receipt AdmissionReceiptV1, name string) error {
	if receipt.Kind != "AdmissionReceiptV1" || receipt.SchemaVersion != 1 ||
		serviceapi.ValidatePrincipalID(receipt.Principal.PrincipalID) != nil || serviceapi.ValidatePrincipalID(receipt.RequestID) != nil ||
		runtimecatalog.ValidateIdentifier(receipt.RunID) != nil || runtimecatalog.ValidateIdentifier(receipt.ProfileID) != nil ||
		receipt.RunID != DeriveRunID(receipt.Principal.PrincipalID, receipt.RequestID) ||
		name != receiptKey(receipt.Principal.PrincipalID, receipt.RequestID)+".json" || !lowerSHA256(receipt.CanonicalRequestSHA256) {
		return errors.New("invalid admission receipt")
	}
	switch receipt.Principal.PrincipalType {
	case serviceapi.PrincipalService, serviceapi.PrincipalUser, serviceapi.PrincipalOperator, serviceapi.PrincipalTest:
	default:
		return errors.New("invalid admission principal")
	}
	if receipt.Principal.AuthnMethod == "" || len(receipt.Principal.AuthnMethod) > 128 || strings.IndexFunc(receipt.Principal.AuthnMethod, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
		return errors.New("invalid admission authentication method")
	}
	created, err := time.Parse(time.RFC3339Nano, receipt.CreatedAt)
	if err != nil || created.UTC().Format(time.RFC3339Nano) != receipt.CreatedAt {
		return errors.New("invalid admission creation time")
	}
	return nil
}

func materialize(profile loadedProfile, principal serviceapi.Principal, request serviceapi.RunAdmissionRequestV1, runID string) (string, runBinding, error) {
	for _, root := range []string{profile.ledgerRoot, profile.evidenceRoot} {
		fd, err := openAbsoluteDirectory(root, false)
		if err != nil {
			return "", runBinding{}, err
		}
		err = validatePrivateDirectoryFD(fd)
		syscall.Close(fd)
		if err != nil {
			return "", runBinding{}, err
		}
	}
	repositoryFD, err := openAbsoluteDirectory(profile.configuration.RepositoryPath, false)
	if err != nil {
		return "", runBinding{}, err
	}
	defer syscall.Close(repositoryFD)
	inputFD, err := openRelativeDirectory(repositoryFD, profile.configuration.InputDirectory, false)
	if err != nil {
		return "", runBinding{}, err
	}
	defer syscall.Close(inputFD)
	runFD, err := openChildDirectory(inputFD, runID, true)
	if err != nil {
		return "", runBinding{}, err
	}
	defer syscall.Close(runFD)
	if err := validatePrivateDirectoryFD(runFD); err != nil {
		return "", runBinding{}, err
	}
	runDirectory := filepath.Join(profile.inputPath, runID)
	planPath := filepath.Join(runDirectory, "plan.md")
	planRelative := profile.configuration.InputDirectory + "/" + runID + "/plan.md"
	plan := admissionPlan(principal, request)
	if err := createOrVerifyAt(runFD, "plan.md", plan); err != nil {
		return "", runBinding{}, err
	}
	spec := admissionCapsuleSpec(profile, request, planRelative)
	_, capsuleData, err := contextcapsule.Build(profile.configuration.RepositoryPath, spec)
	if err != nil {
		return "", runBinding{}, err
	}
	if err := createOrVerifyAt(runFD, "context-capsule.json", capsuleData); err != nil {
		return "", runBinding{}, err
	}
	capsulePath := filepath.Join(runDirectory, "context-capsule.json")
	manifestData, err := derivedManifestData(profile, request, runID, planPath, capsulePath, plan, capsuleData)
	if err != nil || len(manifestData) > MaxManifestTemplateBytes {
		return "", runBinding{}, errors.New("derived manifest exceeds bounds")
	}
	if err := createOrVerifyAt(runFD, "manifest.json", manifestData); err != nil {
		return "", runBinding{}, err
	}
	return filepath.Join(runDirectory, "manifest.json"), bindingFor(profile, runID, manifestData), nil
}

func existingRunBinding(profile loadedProfile, principal serviceapi.Principal, request serviceapi.RunAdmissionRequestV1, runID string) (runBinding, error) {
	for _, root := range []string{profile.ledgerRoot, profile.evidenceRoot} {
		fd, err := openAbsoluteDirectory(root, false)
		if err != nil {
			return runBinding{}, err
		}
		err = validatePrivateDirectoryFD(fd)
		syscall.Close(fd)
		if err != nil {
			return runBinding{}, err
		}
	}
	repositoryFD, err := openAbsoluteDirectory(profile.configuration.RepositoryPath, false)
	if err != nil {
		return runBinding{}, err
	}
	defer syscall.Close(repositoryFD)
	inputFD, err := openRelativeDirectory(repositoryFD, profile.configuration.InputDirectory, false)
	if err != nil {
		return runBinding{}, err
	}
	defer syscall.Close(inputFD)
	runFD, err := openChildDirectory(inputFD, runID, false)
	if err != nil {
		return runBinding{}, err
	}
	defer syscall.Close(runFD)
	if err := validatePrivateDirectoryFD(runFD); err != nil {
		return runBinding{}, err
	}
	runDirectory := filepath.Join(profile.inputPath, runID)
	planPath := filepath.Join(runDirectory, "plan.md")
	planRelative := profile.configuration.InputDirectory + "/" + runID + "/plan.md"
	plan := admissionPlan(principal, request)
	observedPlan, found, err := readRegularAt(runFD, "plan.md", MaxManifestTemplateBytes)
	if err != nil || !found || !bytes.Equal(observedPlan, plan) {
		return runBinding{}, errors.New("existing admission plan is invalid")
	}
	capsuleData, found, err := readRegularAt(runFD, "context-capsule.json", MaxManifestTemplateBytes)
	if err != nil || !found {
		return runBinding{}, errors.New("existing admission context capsule is invalid")
	}
	capsule, err := contextcapsule.Parse(capsuleData)
	if err != nil {
		return runBinding{}, err
	}
	spec := admissionCapsuleSpec(profile, request, planRelative)
	expectedCapsule := contextcapsule.Capsule{
		PolicyVersion: spec.PolicyVersion, Project: spec.Project, Plan: spec.Plan, RoadmapPhase: spec.RoadmapPhase,
		ExecutionPack: spec.ExecutionPack, Task: spec.Task, OperationContext: spec.OperationContext, PhaseAuthority: spec.PhaseAuthority,
		Repository: spec.Repository, BaseSHA: spec.BaseSHA, Invariants: spec.Invariants, NonGoals: spec.NonGoals,
		PredecessorOutcomes: spec.PredecessorOutcomes,
		Sources:             []contextcapsule.Source{{Path: planRelative, SHA256: digest(plan)}},
		CapsuleSHA256:       capsule.CapsuleSHA256,
	}
	if !reflect.DeepEqual(capsule, expectedCapsule) {
		return runBinding{}, errors.New("existing admission context capsule differs")
	}
	capsulePath := filepath.Join(runDirectory, "context-capsule.json")
	manifestData, err := derivedManifestData(profile, request, runID, planPath, capsulePath, plan, capsuleData)
	if err != nil {
		return runBinding{}, err
	}
	observedManifest, found, err := readRegularAt(runFD, "manifest.json", MaxManifestTemplateBytes)
	if err != nil || !found || !bytes.Equal(observedManifest, manifestData) {
		return runBinding{}, errors.New("existing admission manifest is invalid")
	}
	return bindingFor(profile, runID, manifestData), nil
}

func admissionPlan(principal serviceapi.Principal, request serviceapi.RunAdmissionRequestV1) []byte {
	return []byte(fmt.Sprintf("# Product run admission\n\n- Request ID: `%s`\n- Product authorization ID: `%s`\n- Product task ID: `%s`\n- Product version ID: `%s`\n- Product manifest SHA-256: `%s`\n- Repository base SHA: `%s`\n- Authenticated principal: `%s` (`%s`)\n- Delegated actor: `%s` (`%s`)\n\n## Task\n\n%s\n",
		request.RequestID, request.ProductAuthorizationID, request.ProductTaskID, request.ProductVersionID,
		request.ProductManifestSHA256, request.RepositoryBaseSHA, principal.PrincipalID, principal.PrincipalType,
		request.DelegatedActor.SubjectID, request.DelegatedActor.SubjectType, request.TaskMarkdown))
}

func admissionCapsuleSpec(profile loadedProfile, request serviceapi.RunAdmissionRequestV1, planRelative string) contextcapsule.Spec {
	return contextcapsule.Spec{
		PolicyVersion: contextcapsule.PolicyVersionV2,
		Project:       request.ProductAuthorizationID, Plan: request.ProductTaskID,
		RoadmapPhase: "product-run-admission", ExecutionPack: request.ProductVersionID, Task: request.RequestID,
		OperationContext: &contextcapsule.OperationContext{
			Kind:             contextcapsule.OperationImplementation,
			OwnedScope:       []string{"Product task " + request.ProductTaskID},
			BlockingCriteria: []string{"Current owned-scope Critical or Major findings."},
		},
		Repository: profile.configuration.RepositoryIdentity, BaseSHA: request.RepositoryBaseSHA,
		Invariants:          []string{"Preserve the admitted product and repository binding."},
		NonGoals:            []string{"Do not alter controller-owned execution policy."},
		PredecessorOutcomes: []contextcapsule.Outcome{}, Sources: []string{planRelative},
	}
}

func derivedManifestData(profile loadedProfile, request serviceapi.RunAdmissionRequestV1, runID, planPath, capsulePath string, plan, capsuleData []byte) ([]byte, error) {
	manifest, err := cloneManifest(profile.template)
	if err != nil {
		return nil, err
	}
	manifest.RunID = runID
	manifest.Repository.StartSHA = request.RepositoryBaseSHA
	manifest.Plan = authority.PlanManifest{Path: planPath, SHA256: digest(plan)}
	manifest.ContextCapsule = &authority.ContextCapsuleManifest{Path: capsulePath, SHA256: digest(capsuleData)}
	manifest.Worktree.Branch = "abcp/" + runID
	return json.Marshal(manifest)
}

func bindingFor(profile loadedProfile, runID string, manifestData []byte) runBinding {
	return runBinding{
		repositoryIdentityDigest: runtimecatalog.RepositoryIdentityDigest(profile.configuration.RepositoryIdentity),
		authorityDigest:          digest(manifestData),
		ledgerPath:               filepath.Join(profile.ledgerRoot, runID, "events.jsonl"),
		evidenceRoot:             filepath.Join(profile.evidenceRoot, runID),
	}
}

func cloneManifest(input authority.Manifest) (authority.Manifest, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return authority.Manifest{}, err
	}
	var result authority.Manifest
	if err := json.Unmarshal(data, &result); err != nil {
		return authority.Manifest{}, err
	}
	return result, nil
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func lowerSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func gitOutput(repository string, args ...string) (string, error) {
	command := exec.Command("git", append([]string{"-C", repository}, args...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func startProcess(executable string, args []string, lock *os.File) error {
	command := exec.Command(executable, args...)
	command.ExtraFiles = []*os.File{lock}
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}

func closeFD(fd int) error { return syscall.Close(fd) }

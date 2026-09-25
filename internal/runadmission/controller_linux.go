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
	governancev3 "github.com/pankajleh/autonomous-builder-control-plane/internal/governance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
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

// admissionAuthority is the controller-internal common seam shared by product
// and development admissions. Public request types remain separate and strict;
// only their already-validated authority is projected into this shape.
type admissionAuthority struct {
	kind                   string
	requestID              string
	profileID              string
	repositoryBaseSHA      string
	canonicalRequestSHA256 string
	plan                   func(serviceapi.Principal, string, string) []byte
	legacyPlan             func(serviceapi.Principal, string, string) []byte
	capsuleSpec            func(string, string) contextcapsule.Spec
}

func productAdmissionAuthority(request serviceapi.RunAdmissionRequestV1) admissionAuthority {
	return admissionAuthority{
		requestID: request.RequestID, profileID: request.ProfileID, repositoryBaseSHA: request.RepositoryBaseSHA,
		canonicalRequestSHA256: RequestDigest(request),
		plan: func(principal serviceapi.Principal, repositoryIdentity, runID string) []byte {
			return decorateAdmissionPlan(admissionPlan(principal, request), repositoryIdentity, runID)
		},
		legacyPlan: func(principal serviceapi.Principal, repositoryIdentity, runID string) []byte {
			return legacyDecorateAdmissionPlan(admissionPlan(principal, request), repositoryIdentity, runID)
		},
		capsuleSpec: func(repositoryIdentity, planRelative string) contextcapsule.Spec {
			return admissionCapsuleSpecForIdentity(repositoryIdentity, request, planRelative)
		},
	}
}

func developmentAdmissionAuthority(request serviceapi.DevelopmentRunAdmissionRequestV1) admissionAuthority {
	return admissionAuthority{
		kind: admissionKindDevelopment, requestID: request.RequestID, profileID: request.ProfileID,
		repositoryBaseSHA: request.RepositoryBaseSHA, canonicalRequestSHA256: DevelopmentRequestDigest(request),
		plan: func(principal serviceapi.Principal, repositoryIdentity, runID string) []byte {
			return decorateAdmissionPlan(developmentAdmissionPlan(principal, request), repositoryIdentity, runID)
		},
		legacyPlan: func(principal serviceapi.Principal, repositoryIdentity, runID string) []byte {
			return legacyDecorateAdmissionPlan(developmentAdmissionPlan(principal, request), repositoryIdentity, runID)
		},
		capsuleSpec: func(repositoryIdentity, planRelative string) contextcapsule.Spec {
			return developmentAdmissionCapsuleSpecForIdentity(repositoryIdentity, request, planRelative)
		},
	}
}

func (a admissionAuthority) runID(principalID string) string {
	if a.kind == admissionKindDevelopment {
		return DeriveDevelopmentRunID(principalID, a.requestID)
	}
	return DeriveRunID(principalID, a.requestID)
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
	repositoryController, err := governancev3.OpenControllerV1(configuration.RepositoryPath)
	if err != nil || repositoryController.RepositoryIdentity() != configuration.RepositoryIdentity {
		return loadedProfile{}, errors.New("repository identity is not the controller-derived canonical origin")
	}
	if !canonicalRelativeDirectory(configuration.InputDirectory) {
		return loadedProfile{}, errors.New("input directory must be repository-relative")
	}
	probe := configuration.InputDirectory + "/.abcp-admission-ignore-probe"
	ignored, err := gitPathIgnored(configuration.RepositoryPath, probe)
	if err != nil || !ignored {
		return loadedProfile{}, errors.New("admission input directory is not Git-ignored")
	}
	handoffProbe := ralphex.ExecutionPlanHandoffPrefixV1 + strings.Repeat("0", sha256.Size*2) + "/plan.md"
	ignored, err = gitPathIgnored(configuration.RepositoryPath, handoffProbe)
	if err != nil || ignored {
		return loadedProfile{}, errors.New("Ralphex execution-plan handoff namespace must not be Git-ignored")
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
	workflowConfiguration, err := readProtectedFile(configuration.WorkflowAuthorityConfigPath, workflowauthoritypg.MaxConfigBytes)
	if err != nil {
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
	profileBindingData, err := json.Marshal(struct {
		Configuration                 ProfileV1 `json:"configuration"`
		ManifestTemplateSHA256        string    `json:"manifest_template_sha256"`
		WorkflowAuthorityConfigSHA256 string    `json:"workflow_authority_config_sha256"`
	}{
		Configuration: configuration, ManifestTemplateSHA256: digest(templateData),
		WorkflowAuthorityConfigSHA256: digest(workflowConfiguration),
	})
	if err != nil {
		return loadedProfile{}, errors.New("profile binding is invalid")
	}
	return loadedProfile{
		configuration: configuration, template: manifest,
		inputPath:  filepath.Join(configuration.RepositoryPath, filepath.FromSlash(configuration.InputDirectory)),
		ledgerRoot: configuration.LedgerRoot, evidenceRoot: configuration.EvidenceRoot, cgroupRoot: configuration.CgroupRoot,
		workflowAuthorityConfigPath:   configuration.WorkflowAuthorityConfigPath,
		workflowAuthorityConfigSHA256: digest(workflowConfiguration), profileBindingSHA256: digest(profileBindingData),
	}, nil
}

func (c *Controller) admitRun(ctx context.Context, principal serviceapi.Principal, request serviceapi.RunAdmissionRequestV1) (serviceapi.RunAdmissionResponseV1, error) {
	if c == nil || c.admissionsFD < 0 || c.launchesFD < 0 || c.start == nil || serviceapi.ValidateRunAdmissionRequestV1(request) != nil ||
		serviceapi.ValidatePrincipalID(principal.PrincipalID) != nil {
		return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrAdmissionUnavailable
	}
	return c.admitAuthority(ctx, principal, productAdmissionAuthority(request))
}

func (c *Controller) admitDevelopmentRun(ctx context.Context, principal serviceapi.Principal, request serviceapi.DevelopmentRunAdmissionRequestV1) (serviceapi.RunAdmissionResponseV1, error) {
	if c == nil || c.admissionsFD < 0 || c.launchesFD < 0 || c.start == nil ||
		serviceapi.ValidateDevelopmentRunAdmissionRequestV1(request) != nil || serviceapi.ValidatePrincipalID(principal.PrincipalID) != nil {
		return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrAdmissionUnavailable
	}
	if request.ProfileID != DevelopmentProfileID && request.ProfileID != DevelopmentProfileIDV2 {
		return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrUnknownAdmissionProfile
	}
	return c.admitAuthority(ctx, principal, developmentAdmissionAuthority(request))
}

func (c *Controller) admitAuthority(ctx context.Context, principal serviceapi.Principal, admission admissionAuthority) (serviceapi.RunAdmissionResponseV1, error) {
	runID := admission.runID(principal.PrincipalID)
	response := serviceapi.RunAdmissionResponseV1{RunID: runID, RunURL: "/v1/runs/" + runID}
	receipt := AdmissionReceiptV1{
		Kind: "AdmissionReceiptV1", SchemaVersion: 1, AdmissionKind: admission.kind,
		Principal: principal, RequestID: admission.requestID,
		CanonicalRequestSHA256: admission.canonicalRequestSHA256, RunID: runID, ProfileID: admission.profileID,
		CreatedAt: c.clock().UTC().Format(time.RFC3339Nano),
	}
	observedReceipt, foundReceipt, err := c.readAdmissionReceipt(receipt)
	if err != nil {
		return serviceapi.RunAdmissionResponseV1{}, err
	}
	if foundReceipt {
		receipt = observedReceipt
	} else {
		profile, ok := c.profiles[admission.profileID]
		if !ok {
			return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrUnknownAdmissionProfile
		}
		receipt.ProfileBindingSHA256 = profile.profileBindingSHA256
		receipt, err = c.createOrVerifyReceipt(ctx, receipt)
		if err != nil {
			return serviceapi.RunAdmissionResponseV1{}, err
		}
	}
	if registered, err := c.registeredReplay(receipt, principal, admission); err != nil {
		return serviceapi.RunAdmissionResponseV1{}, err
	} else if registered {
		return response, nil
	}
	binding, bindingFound, err := c.readAdmissionBinding(receipt)
	if err != nil {
		return serviceapi.RunAdmissionResponseV1{}, err
	}
	lock, acquired, err := acquireLaunchLock(c.launchesFD, runID)
	if err != nil {
		return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrAdmissionUnavailable
	}
	if !acquired {
		if registered, readErr := c.registeredReplay(receipt, principal, admission); readErr != nil {
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
	if registered, err := c.registeredReplay(receipt, principal, admission); err != nil {
		return serviceapi.RunAdmissionResponseV1{}, err
	} else if registered {
		return response, nil
	}
	// Another request may have frozen the binding before this caller acquired
	// the launch lock. Prefer that durable binding over mutable profile state.
	if !bindingFound {
		binding, bindingFound, err = c.readAdmissionBinding(receipt)
		if err != nil {
			return serviceapi.RunAdmissionResponseV1{}, err
		}
	}
	if !bindingFound {
		profile, ok := c.profiles[admission.profileID]
		if !ok || profile.profileBindingSHA256 != receipt.ProfileBindingSHA256 {
			return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrReconciliationRequired
		}
		head, headErr := gitOutput(profile.configuration.RepositoryPath, "rev-parse", "--verify", "HEAD^{commit}")
		if headErr != nil {
			return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrAdmissionUnavailable
		}
		if head != admission.repositoryBaseSHA {
			return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrRepositoryBaseMismatch
		}
		manifestPath, materialized, materializeErr := materialize(profile, principal, admission, runID)
		if materializeErr != nil {
			return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrUnsafeAdmissionMaterialization
		}
		binding = admissionBinding(receipt, profile, manifestPath, materialized)
		if err := c.createOrVerifyAdmissionBinding(binding); err != nil {
			return serviceapi.RunAdmissionResponseV1{}, err
		}
	}
	if err := validateAdmissionBindingMaterial(binding, principal, admission); err != nil {
		return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	if registered, err := c.runRegistered(runID, binding.runBinding()); err != nil {
		return serviceapi.RunAdmissionResponseV1{}, err
	} else if registered {
		return response, nil
	}
	launchAttempted, err := c.readAdmissionLaunchIntent(binding)
	if err != nil {
		return serviceapi.RunAdmissionResponseV1{}, err
	}
	if launchAttempted {
		return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrReconciliationRequired
	}
	if err := c.createAdmissionLaunchIntent(binding); err != nil {
		return serviceapi.RunAdmissionResponseV1{}, err
	}
	args := []string{
		"run", "--manifest", binding.ManifestPath, "--ledger", binding.CanonicalLedgerPath,
		"--evidence-root", binding.EvidenceRoot, "--cgroup-root", binding.CgroupRoot,
		"--service-root", c.serviceRoot,
		"--workflow-authority-config-file", binding.WorkflowAuthorityConfigPath,
	}
	if err := c.start(c.executable, args, lock); err != nil {
		if clearErr := c.clearAdmissionLaunchIntent(binding); clearErr != nil {
			return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrReconciliationRequired
		}
		return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrAdmissionUnavailable
	}
	started = true
	if err := lock.Close(); err != nil {
		return serviceapi.RunAdmissionResponseV1{}, serviceapi.ErrAdmissionUnavailable
	}
	return response, nil
}

func (c *Controller) registeredReplay(receipt AdmissionReceiptV1, principal serviceapi.Principal, admission admissionAuthority) (bool, error) {
	registration, err := c.catalog.ReadRun(receipt.RunID)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, serviceapi.ErrAdmissionUnavailable
	}
	binding, found, err := c.readAdmissionBinding(receipt)
	if err != nil || !found || !registrationMatches(registration, receipt.RunID, binding.runBinding()) ||
		validateAdmissionBindingMaterial(binding, principal, admission) != nil {
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

func admissionBinding(receipt AdmissionReceiptV1, profile loadedProfile, manifestPath string, binding runBinding) AdmissionBindingV1 {
	return AdmissionBindingV1{
		Kind: "AdmissionBindingV1", SchemaVersion: 1, AdmissionKind: receipt.AdmissionKind,
		PrincipalID: receipt.Principal.PrincipalID, RequestID: receipt.RequestID,
		CanonicalRequestSHA256: receipt.CanonicalRequestSHA256, RunID: receipt.RunID, ProfileID: receipt.ProfileID,
		ProfileBindingSHA256: receipt.ProfileBindingSHA256,
		RepositoryPath:       profile.configuration.RepositoryPath, RepositoryIdentity: profile.configuration.RepositoryIdentity,
		RepositoryIdentityDigest: binding.repositoryIdentityDigest, AuthorityDigest: binding.authorityDigest,
		InputDirectory:     profile.configuration.InputDirectory,
		PlanPath:           filepath.Join(filepath.Dir(manifestPath), "plan.md"),
		ContextCapsulePath: filepath.Join(filepath.Dir(manifestPath), "context-capsule.json"), ManifestPath: manifestPath,
		LedgerRoot: profile.ledgerRoot, CanonicalLedgerPath: binding.ledgerPath,
		EvidenceRoot: profile.evidenceRoot, CanonicalEvidenceRoot: binding.evidenceRoot,
		CgroupRoot: profile.cgroupRoot, WorkflowAuthorityConfigPath: profile.workflowAuthorityConfigPath,
		WorkflowAuthorityConfigSHA256: profile.workflowAuthorityConfigSHA256,
	}
}

func (b AdmissionBindingV1) runBinding() runBinding {
	return runBinding{
		repositoryIdentityDigest: b.RepositoryIdentityDigest, authorityDigest: b.AuthorityDigest,
		ledgerPath: b.CanonicalLedgerPath, evidenceRoot: b.CanonicalEvidenceRoot,
	}
}

func bindingName(admissionKind, principalID, requestID string) string {
	return receiptKeyFor(admissionKind, principalID, requestID) + ".binding.json"
}

func validateAdmissionBinding(binding AdmissionBindingV1, receipt AdmissionReceiptV1) error {
	if binding.Kind != "AdmissionBindingV1" || binding.SchemaVersion != 1 ||
		binding.AdmissionKind != receipt.AdmissionKind ||
		binding.PrincipalID != receipt.Principal.PrincipalID || binding.RequestID != receipt.RequestID ||
		binding.CanonicalRequestSHA256 != receipt.CanonicalRequestSHA256 || binding.RunID != receipt.RunID || binding.ProfileID != receipt.ProfileID ||
		binding.ProfileBindingSHA256 != receipt.ProfileBindingSHA256 || !lowerSHA256(binding.ProfileBindingSHA256) ||
		!lowerSHA256(binding.RepositoryIdentityDigest) || !lowerSHA256(binding.AuthorityDigest) ||
		binding.RepositoryIdentity == "" || runtimecatalog.RepositoryIdentityDigest(binding.RepositoryIdentity) != binding.RepositoryIdentityDigest ||
		!canonicalAbsolute(binding.RepositoryPath) || !canonicalRelativeDirectory(binding.InputDirectory) ||
		!canonicalAbsolute(binding.PlanPath) || !canonicalAbsolute(binding.ContextCapsulePath) || !canonicalAbsolute(binding.ManifestPath) ||
		!canonicalAbsolute(binding.LedgerRoot) || !canonicalAbsolute(binding.CanonicalLedgerPath) ||
		!canonicalAbsolute(binding.EvidenceRoot) || !canonicalAbsolute(binding.CanonicalEvidenceRoot) ||
		!canonicalAbsolute(binding.CgroupRoot) || !canonicalAbsolute(binding.WorkflowAuthorityConfigPath) ||
		!lowerSHA256(binding.WorkflowAuthorityConfigSHA256) ||
		binding.CanonicalLedgerPath != filepath.Join(binding.LedgerRoot, binding.RunID, "events.jsonl") ||
		binding.CanonicalEvidenceRoot != filepath.Join(binding.EvidenceRoot, binding.RunID) ||
		binding.PlanPath != filepath.Join(binding.RepositoryPath, filepath.FromSlash(binding.InputDirectory), binding.RunID, "plan.md") ||
		binding.ContextCapsulePath != filepath.Join(binding.RepositoryPath, filepath.FromSlash(binding.InputDirectory), binding.RunID, "context-capsule.json") ||
		binding.ManifestPath != filepath.Join(binding.RepositoryPath, filepath.FromSlash(binding.InputDirectory), binding.RunID, "manifest.json") {
		return errors.New("invalid admission binding")
	}
	return nil
}

func (c *Controller) readAdmissionBinding(receipt AdmissionReceiptV1) (AdmissionBindingV1, bool, error) {
	name := bindingName(receipt.AdmissionKind, receipt.Principal.PrincipalID, receipt.RequestID)
	data, found, err := readRegularAt(c.admissionsFD, name, MaxBindingBytes)
	if err != nil {
		return AdmissionBindingV1{}, false, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	if !found {
		return AdmissionBindingV1{}, false, nil
	}
	var binding AdmissionBindingV1
	if strictJSON(data, &binding) != nil || validateAdmissionBinding(binding, receipt) != nil {
		return AdmissionBindingV1{}, false, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	canonical, _ := json.Marshal(binding)
	if !bytes.Equal(data, canonical) {
		return AdmissionBindingV1{}, false, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	return binding, true, nil
}

func (c *Controller) createOrVerifyAdmissionBinding(expected AdmissionBindingV1) error {
	receipt := AdmissionReceiptV1{
		AdmissionKind: expected.AdmissionKind,
		Principal:     serviceapi.Principal{PrincipalID: expected.PrincipalID}, RequestID: expected.RequestID,
		CanonicalRequestSHA256: expected.CanonicalRequestSHA256, RunID: expected.RunID, ProfileID: expected.ProfileID,
		ProfileBindingSHA256: expected.ProfileBindingSHA256,
	}
	if validateAdmissionBinding(expected, receipt) != nil {
		return serviceapi.ErrUnsafeAdmissionMaterialization
	}
	data, err := json.Marshal(expected)
	if err != nil || len(data) > MaxBindingBytes {
		return serviceapi.ErrUnsafeAdmissionMaterialization
	}
	name := bindingName(expected.AdmissionKind, expected.PrincipalID, expected.RequestID)
	observed, found, err := readRegularAt(c.admissionsFD, name, MaxBindingBytes)
	if err != nil {
		return serviceapi.ErrUnsafeAdmissionMaterialization
	}
	if found {
		if !bytes.Equal(observed, data) {
			return serviceapi.ErrUnsafeAdmissionMaterialization
		}
		return nil
	}
	if err := writeNewAtomicAt(c.admissionsFD, name, data); err != nil {
		return serviceapi.ErrAdmissionUnavailable
	}
	return nil
}

func launchIntentName(binding AdmissionBindingV1) string {
	return receiptKeyFor(binding.AdmissionKind, binding.PrincipalID, binding.RequestID) + ".launch.json"
}

func admissionLaunchIntent(binding AdmissionBindingV1) AdmissionLaunchIntentV1 {
	data, _ := json.Marshal(binding)
	return AdmissionLaunchIntentV1{
		Kind: "AdmissionLaunchIntentV1", SchemaVersion: 1, AdmissionKind: binding.AdmissionKind,
		PrincipalID: binding.PrincipalID, RequestID: binding.RequestID, RunID: binding.RunID,
		AdmissionBindingSHA256: digest(data),
	}
}

func (c *Controller) readAdmissionLaunchIntent(binding AdmissionBindingV1) (bool, error) {
	expected := admissionLaunchIntent(binding)
	data, found, err := readRegularAt(c.admissionsFD, launchIntentName(binding), MaxLaunchIntentBytes)
	if err != nil {
		return false, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	if !found {
		return false, nil
	}
	var observed AdmissionLaunchIntentV1
	if strictJSON(data, &observed) != nil || observed != expected {
		return false, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	canonical, _ := json.Marshal(observed)
	if !bytes.Equal(data, canonical) {
		return false, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	return true, nil
}

func (c *Controller) createAdmissionLaunchIntent(binding AdmissionBindingV1) error {
	intent := admissionLaunchIntent(binding)
	data, err := json.Marshal(intent)
	if err != nil || len(data) > MaxLaunchIntentBytes {
		return serviceapi.ErrUnsafeAdmissionMaterialization
	}
	if err := writeNewAtomicAt(c.admissionsFD, launchIntentName(binding), data); err != nil {
		return serviceapi.ErrAdmissionUnavailable
	}
	found, err := c.readAdmissionLaunchIntent(binding)
	if err != nil {
		return err
	}
	if !found {
		return serviceapi.ErrAdmissionUnavailable
	}
	return nil
}

func (c *Controller) clearAdmissionLaunchIntent(binding AdmissionBindingV1) error {
	found, err := c.readAdmissionLaunchIntent(binding)
	if err != nil || !found {
		return serviceapi.ErrUnsafeAdmissionMaterialization
	}
	if err := syscall.Unlinkat(c.admissionsFD, launchIntentName(binding)); err != nil {
		return serviceapi.ErrAdmissionUnavailable
	}
	if err := syscall.Fsync(c.admissionsFD); err != nil {
		return serviceapi.ErrAdmissionUnavailable
	}
	return nil
}

func validateAdmissionBindingMaterial(binding AdmissionBindingV1, principal serviceapi.Principal, admission admissionAuthority) error {
	for _, root := range []string{binding.LedgerRoot, binding.EvidenceRoot} {
		fd, err := openAbsoluteDirectory(root, false)
		if err != nil {
			return err
		}
		err = validatePrivateDirectoryFD(fd)
		syscall.Close(fd)
		if err != nil {
			return err
		}
	}
	repositoryFD, err := openAbsoluteDirectory(binding.RepositoryPath, false)
	if err != nil {
		return err
	}
	defer syscall.Close(repositoryFD)
	inputFD, err := openRelativeDirectory(repositoryFD, binding.InputDirectory, false)
	if err != nil {
		return err
	}
	defer syscall.Close(inputFD)
	runFD, err := openChildDirectory(inputFD, binding.RunID, false)
	if err != nil {
		return err
	}
	defer syscall.Close(runFD)
	err = validatePrivateDirectoryFD(runFD)
	if err != nil {
		return err
	}
	plan, found, err := readRegularAt(runFD, "plan.md", MaxManifestTemplateBytes)
	currentPlan := admission.plan(principal, binding.RepositoryIdentity, binding.RunID)
	legacyPlan := admission.legacyPlan(principal, binding.RepositoryIdentity, binding.RunID)
	if err != nil || !found || (!bytes.Equal(plan, currentPlan) && !bytes.Equal(plan, legacyPlan)) {
		return errors.New("admission plan binding is invalid")
	}
	capsuleData, found, err := readRegularAt(runFD, "context-capsule.json", MaxManifestTemplateBytes)
	if err != nil || !found {
		return errors.New("admission context capsule binding is invalid")
	}
	capsule, err := contextcapsule.Parse(capsuleData)
	if err != nil {
		return errors.New("admission context capsule binding is invalid")
	}
	planRelative := binding.InputDirectory + "/" + binding.RunID + "/plan.md"
	spec := admission.capsuleSpec(binding.RepositoryIdentity, planRelative)
	expectedCapsule := contextcapsule.Capsule{
		PolicyVersion: spec.PolicyVersion, Project: spec.Project, Plan: spec.Plan, RoadmapPhase: spec.RoadmapPhase,
		ExecutionPack: spec.ExecutionPack, Task: spec.Task, OperationContext: spec.OperationContext, PhaseAuthority: spec.PhaseAuthority,
		Repository: spec.Repository, BaseSHA: spec.BaseSHA, Invariants: spec.Invariants, NonGoals: spec.NonGoals,
		PredecessorOutcomes: spec.PredecessorOutcomes,
		Sources:             []contextcapsule.Source{{Path: planRelative, SHA256: digest(plan)}}, CapsuleSHA256: capsule.CapsuleSHA256,
	}
	if !reflect.DeepEqual(capsule, expectedCapsule) {
		return errors.New("admission context capsule binding differs")
	}
	manifestData, found, err := readRegularAt(runFD, "manifest.json", MaxManifestTemplateBytes)
	if err != nil || !found || digest(manifestData) != binding.AuthorityDigest {
		return errors.New("admission manifest binding is invalid")
	}
	var manifest authority.Manifest
	if strictJSON(manifestData, &manifest) != nil || manifest.RunID != binding.RunID ||
		manifest.Repository.Path != binding.RepositoryPath || manifest.Repository.Identity != binding.RepositoryIdentity ||
		manifest.Repository.StartSHA != admission.repositoryBaseSHA || manifest.Plan.Path != binding.PlanPath ||
		manifest.Plan.SHA256 != digest(plan) || manifest.ContextCapsule == nil ||
		manifest.ContextCapsule.Path != binding.ContextCapsulePath || manifest.ContextCapsule.SHA256 != digest(capsuleData) {
		return errors.New("admission manifest content is invalid")
	}
	workflowConfiguration, err := readProtectedFile(binding.WorkflowAuthorityConfigPath, workflowauthoritypg.MaxConfigBytes)
	if err != nil || digest(workflowConfiguration) != binding.WorkflowAuthorityConfigSHA256 ||
		workflowauthoritypg.ValidateConfigFile(binding.WorkflowAuthorityConfigPath) != nil {
		return errors.New("workflow authority configuration binding is invalid")
	}
	cgroupFD, err := openAbsoluteDirectory(binding.CgroupRoot, false)
	if err != nil {
		return err
	}
	return syscall.Close(cgroupFD)
}

func (c *Controller) readAdmissionReceipt(expected AdmissionReceiptV1) (AdmissionReceiptV1, bool, error) {
	name := receiptKeyFor(expected.AdmissionKind, expected.Principal.PrincipalID, expected.RequestID) + ".json"
	data, found, err := readRegularAt(c.admissionsFD, name, MaxReceiptBytes)
	if err != nil {
		return AdmissionReceiptV1{}, false, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	if !found {
		return AdmissionReceiptV1{}, false, nil
	}
	var observed AdmissionReceiptV1
	if strictJSON(data, &observed) != nil || validateReceipt(observed, name) != nil {
		return AdmissionReceiptV1{}, false, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	canonical, _ := json.Marshal(observed)
	if !bytes.Equal(data, canonical) {
		return AdmissionReceiptV1{}, false, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	if observed.AdmissionKind != expected.AdmissionKind || observed.Principal != expected.Principal || observed.RequestID != expected.RequestID ||
		observed.CanonicalRequestSHA256 != expected.CanonicalRequestSHA256 || observed.RunID != expected.RunID || observed.ProfileID != expected.ProfileID {
		return AdmissionReceiptV1{}, false, serviceapi.ErrRequestIDConflict
	}
	return observed, true, nil
}

func (c *Controller) createOrVerifyReceipt(ctx context.Context, expected AdmissionReceiptV1) (AdmissionReceiptV1, error) {
	lockFD, err := syscall.Openat(c.admissionsFD, ".lock", syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return AdmissionReceiptV1{}, serviceapi.ErrAdmissionUnavailable
	}
	defer syscall.Close(lockFD)
	if err := boundedFlock(ctx, lockFD, ReceiptLockTimeout); err != nil {
		return AdmissionReceiptV1{}, serviceapi.ErrAdmissionUnavailable
	}
	defer syscall.Flock(lockFD, syscall.LOCK_UN)
	observed, found, err := c.readAdmissionReceipt(expected)
	if err != nil {
		return AdmissionReceiptV1{}, err
	}
	if found {
		return observed, nil
	}
	name := receiptKeyFor(expected.AdmissionKind, expected.Principal.PrincipalID, expected.RequestID) + ".json"
	data, err := json.Marshal(expected)
	if err != nil || len(data) > MaxReceiptBytes {
		return AdmissionReceiptV1{}, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	if err := writeNewAtomicAt(c.admissionsFD, name, data); err != nil {
		return AdmissionReceiptV1{}, serviceapi.ErrAdmissionUnavailable
	}
	return expected, nil
}

func validateReceipt(receipt AdmissionReceiptV1, name string) error {
	expectedRunID := DeriveRunID(receipt.Principal.PrincipalID, receipt.RequestID)
	if receipt.AdmissionKind == admissionKindDevelopment {
		expectedRunID = DeriveDevelopmentRunID(receipt.Principal.PrincipalID, receipt.RequestID)
		if receipt.ProfileID != DevelopmentProfileID && receipt.ProfileID != DevelopmentProfileIDV2 {
			return errors.New("invalid development admission profile")
		}
	} else if receipt.AdmissionKind != "" {
		return errors.New("invalid admission receipt kind")
	}
	if receipt.Kind != "AdmissionReceiptV1" || receipt.SchemaVersion != 1 ||
		serviceapi.ValidatePrincipalID(receipt.Principal.PrincipalID) != nil || serviceapi.ValidatePrincipalID(receipt.RequestID) != nil ||
		runtimecatalog.ValidateIdentifier(receipt.RunID) != nil || runtimecatalog.ValidateIdentifier(receipt.ProfileID) != nil ||
		receipt.RunID != expectedRunID ||
		name != receiptKeyFor(receipt.AdmissionKind, receipt.Principal.PrincipalID, receipt.RequestID)+".json" || !lowerSHA256(receipt.CanonicalRequestSHA256) ||
		!lowerSHA256(receipt.ProfileBindingSHA256) {
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

func materialize(profile loadedProfile, principal serviceapi.Principal, admission admissionAuthority, runID string) (string, runBinding, error) {
	runIDSum := sha256.Sum256([]byte(runID))
	handoffPath := ralphex.ExecutionPlanHandoffPrefixV1 + hex.EncodeToString(runIDSum[:]) + "/plan.md"
	ignored, err := gitPathIgnored(profile.configuration.RepositoryPath, handoffPath)
	if err != nil || ignored {
		return "", runBinding{}, errors.New("Ralphex execution-plan handoff path is Git-ignored")
	}
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
	currentPlan := admission.plan(principal, profile.configuration.RepositoryIdentity, runID)
	legacyPlan := admission.legacyPlan(principal, profile.configuration.RepositoryIdentity, runID)
	plan, err := createOrVerifyAdmissionPlanAt(runFD, currentPlan, legacyPlan)
	if err != nil {
		return "", runBinding{}, err
	}
	spec := admission.capsuleSpec(profile.configuration.RepositoryIdentity, planRelative)
	_, capsuleData, err := contextcapsule.Build(profile.configuration.RepositoryPath, spec)
	if err != nil {
		return "", runBinding{}, err
	}
	if err := createOrVerifyAt(runFD, "context-capsule.json", capsuleData); err != nil {
		return "", runBinding{}, err
	}
	capsulePath := filepath.Join(runDirectory, "context-capsule.json")
	manifestData, err := derivedManifestData(profile, admission.repositoryBaseSHA, runID, planPath, capsulePath, plan, capsuleData)
	if err != nil || len(manifestData) > MaxManifestTemplateBytes {
		return "", runBinding{}, errors.New("derived manifest exceeds bounds")
	}
	if err := createOrVerifyAt(runFD, "manifest.json", manifestData); err != nil {
		return "", runBinding{}, err
	}
	return filepath.Join(runDirectory, "manifest.json"), bindingFor(profile, runID, manifestData), nil
}

func admissionPlan(principal serviceapi.Principal, request serviceapi.RunAdmissionRequestV1) []byte {
	fence := markdownFence(request.TaskMarkdown)
	taskMarkdown := request.TaskMarkdown
	if !strings.HasSuffix(taskMarkdown, "\n") {
		taskMarkdown += "\n"
	}
	return []byte(fmt.Sprintf("# Product run admission\n\n- Request ID: `%s`\n- Product authorization ID: `%s`\n- Product task ID: `%s`\n- Product version ID: `%s`\n- Product manifest SHA-256: `%s`\n- Repository base SHA: `%s`\n- Authenticated principal: `%s` (`%s`)\n- Delegated actor: `%s` (`%s`)\n\n### Task 1: Implement the authorized product task\n\n- [ ] Implement every requirement in the complete authorized product task below.\n\n#### Complete authorized product task\n\n%smarkdown\n%s%s\n",
		request.RequestID, request.ProductAuthorizationID, request.ProductTaskID, request.ProductVersionID,
		request.ProductManifestSHA256, request.RepositoryBaseSHA, principal.PrincipalID, principal.PrincipalType,
		request.DelegatedActor.SubjectID, request.DelegatedActor.SubjectType, fence, taskMarkdown, fence))
}

func developmentAdmissionPlan(principal serviceapi.Principal, request serviceapi.DevelopmentRunAdmissionRequestV1) []byte {
	fence := markdownFence(request.TaskMarkdown)
	taskMarkdown := request.TaskMarkdown
	if !strings.HasSuffix(taskMarkdown, "\n") {
		taskMarkdown += "\n"
	}
	return []byte(fmt.Sprintf("# Development run admission\n\n- Request ID: `%s`\n- Development capsule ID: `%s`\n- Development slice ID: `%s`\n- Development capsule SHA-256: `%s`\n- Repository base SHA: `%s`\n- Authenticated principal: `%s` (`%s`)\n- Delegated actor: `%s` (`%s`)\n\n### Task 1: Implement the frozen development slice\n\n- [ ] Implement every requirement in the complete frozen development capsule below.\n\n#### Complete frozen development capsule\n\n%smarkdown\n%s%s\n",
		request.RequestID, request.DevelopmentCapsuleID, request.DevelopmentSliceID,
		request.DevelopmentCapsuleSHA256, request.RepositoryBaseSHA, principal.PrincipalID, principal.PrincipalType,
		request.DelegatedActor.SubjectID, request.DelegatedActor.SubjectType, fence, taskMarkdown, fence))
}

func decorateAdmissionPlan(plan []byte, repositoryIdentity, runID string) []byte {
	return decorateAdmissionPlanWithID(plan, repositoryIdentity, runID, runID)
}

func legacyDecorateAdmissionPlan(plan []byte, repositoryIdentity, runID string) []byte {
	return decorateAdmissionPlanWithID(plan, repositoryIdentity, runID, strings.TrimPrefix(runID, "admission-"))
}

func decorateAdmissionPlanWithID(plan []byte, repositoryIdentity, runID, admissionID string) []byte {
	text := string(plan)
	title, project := admissionDisplayMetadata(text)
	if title == "" {
		title = "Governed task " + shortRunID(runID)
	}
	var lines []string
	lines = append(lines, "Display-Title: "+title)
	if project != "" {
		lines = append(lines, "Project: "+project)
	}
	if repositoryIdentity != "" {
		lines = append(lines, "Repository: "+repositoryIdentity)
	}
	if admissionID != "" {
		lines = append(lines, "Admission-ID: "+admissionID)
	}
	insert := strings.Join(lines, "\n") + "\n"
	if newline := strings.IndexByte(text, '\n'); newline >= 0 {
		return []byte(text[:newline+1] + insert + text[newline+1:])
	}
	return []byte(text + "\n" + insert)
}

func createOrVerifyAdmissionPlanAt(runFD int, current, legacy []byte) ([]byte, error) {
	data, found, err := readRegularAt(runFD, "plan.md", MaxManifestTemplateBytes)
	if err != nil {
		return nil, err
	}
	if found {
		if bytes.Equal(data, current) || bytes.Equal(data, legacy) {
			return data, nil
		}
		return nil, errors.New("existing admission material differs")
	}
	if err := writeNewAtomicAt(runFD, "plan.md", current); err != nil {
		return nil, err
	}
	data, found, err = readRegularAt(runFD, "plan.md", MaxManifestTemplateBytes)
	if err != nil || !found || !bytes.Equal(data, current) {
		return nil, errors.New("admission material verification failed")
	}
	return data, nil
}

func admissionDisplayMetadata(text string) (string, string) {
	var title, project string
	lines := strings.Split(text, "\n")
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if project == "" && strings.HasPrefix(line, "project_id:") {
			project = quotedScalar(line)
		}
		if title == "" && (strings.HasPrefix(line, "display_title:") || strings.HasPrefix(line, "task_title:") || strings.HasPrefix(line, "objective:")) {
			title = quotedScalar(line)
		}
		if title == "" && line == "## Objective" {
			for j := i + 1; j < len(lines); j++ {
				if v := strings.TrimSpace(lines[j]); v != "" {
					title = v
					break
				}
			}
		}
	}
	if project == "" && strings.HasPrefix(text, "# Development run admission") {
		project = "repo-c-development"
	}
	return strings.TrimSpace(title), strings.TrimSpace(project)
}

func quotedScalar(line string) string {
	_, value, ok := strings.Cut(line, ":")
	if !ok {
		return ""
	}
	value = strings.TrimSpace(value)
	return strings.Trim(value, "\"'")
}

func shortRunID(runID string) string {
	id := strings.TrimPrefix(runID, "admission-")
	if len(id) > 8 {
		id = id[:8] + "…"
	}
	return id
}

func markdownFence(value string) string {
	backticks := longestRun(value, '`') + 1
	tildes := longestRun(value, '~') + 1
	if backticks < 3 {
		backticks = 3
	}
	if tildes < 3 {
		tildes = 3
	}
	if tildes < backticks {
		return strings.Repeat("~", tildes)
	}
	return strings.Repeat("`", backticks)
}

func longestRun(value string, marker byte) int {
	longest, current := 0, 0
	for index := 0; index < len(value); index++ {
		if value[index] == marker {
			current++
			if current > longest {
				longest = current
			}
			continue
		}
		current = 0
	}
	return longest
}

func admissionCapsuleSpec(profile loadedProfile, request serviceapi.RunAdmissionRequestV1, planRelative string) contextcapsule.Spec {
	return admissionCapsuleSpecForIdentity(profile.configuration.RepositoryIdentity, request, planRelative)
}

func admissionCapsuleSpecForIdentity(repositoryIdentity string, request serviceapi.RunAdmissionRequestV1, planRelative string) contextcapsule.Spec {
	return contextcapsule.Spec{
		PolicyVersion: contextcapsule.PolicyVersionV2,
		Project:       request.ProductAuthorizationID, Plan: request.ProductTaskID,
		RoadmapPhase: "product-run-admission", ExecutionPack: request.ProductVersionID, Task: request.RequestID,
		OperationContext: &contextcapsule.OperationContext{
			Kind:             contextcapsule.OperationImplementation,
			OwnedScope:       []string{"Product task " + request.ProductTaskID},
			BlockingCriteria: []string{"Current owned-scope Critical or Major findings."},
		},
		Repository: repositoryIdentity, BaseSHA: request.RepositoryBaseSHA,
		Invariants:          []string{"Preserve the admitted product and repository binding."},
		NonGoals:            []string{"Do not alter controller-owned execution policy."},
		PredecessorOutcomes: []contextcapsule.Outcome{}, Sources: []string{planRelative},
	}
}

func developmentAdmissionCapsuleSpecForIdentity(repositoryIdentity string, request serviceapi.DevelopmentRunAdmissionRequestV1, planRelative string) contextcapsule.Spec {
	return contextcapsule.Spec{
		PolicyVersion: contextcapsule.PolicyVersionV2,
		Project:       "repo-c-development", Plan: request.DevelopmentSliceID,
		RoadmapPhase: "repo-c-development", ExecutionPack: request.DevelopmentCapsuleID, Task: request.RequestID,
		OperationContext: &contextcapsule.OperationContext{
			Kind:             contextcapsule.OperationImplementation,
			OwnedScope:       []string{"Development slice " + request.DevelopmentSliceID},
			BlockingCriteria: []string{"Current owned-scope Critical or Major findings."},
		},
		Repository: repositoryIdentity, BaseSHA: request.RepositoryBaseSHA,
		Invariants:          []string{"Preserve the admitted development capsule and repository binding."},
		NonGoals:            []string{"Do not alter controller-owned execution policy."},
		PredecessorOutcomes: []contextcapsule.Outcome{}, Sources: []string{planRelative},
	}
}

func derivedManifestData(profile loadedProfile, repositoryBaseSHA, runID, planPath, capsulePath string, plan, capsuleData []byte) ([]byte, error) {
	manifest, err := cloneManifest(profile.template)
	if err != nil {
		return nil, err
	}
	manifest.RunID = runID
	manifest.Repository.StartSHA = repositoryBaseSHA
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

func gitPathIgnored(repository, path string) (bool, error) {
	_, err := gitOutput(repository, "check-ignore", "-q", "--no-index", "--", path)
	if err == nil {
		return true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false, nil
	}
	return false, err
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

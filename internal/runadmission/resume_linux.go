package runadmission

import (
	"context"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// bindingForRunID locates the single durable admission binding whose RunID
// matches, by enumerating the protected admissions directory. A missing or
// duplicated match fails closed: resume must re-launch the exact original run,
// never a best-effort guess.
func (c *Controller) bindingForRunID(runID string) (AdmissionBindingV1, error) {
	if runtimecatalog.ValidateIdentifier(runID) != nil {
		return AdmissionBindingV1{}, serviceapi.ErrActionStatusNotFound
	}
	names, err := readDirAt(c.admissionsFD)
	if err != nil {
		return AdmissionBindingV1{}, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	var found AdmissionBindingV1
	matched := false
	for _, name := range names {
		if !strings.HasSuffix(name, ".binding.json") {
			continue
		}
		data, present, err := readRegularAt(c.admissionsFD, name, MaxBindingBytes)
		if err != nil {
			return AdmissionBindingV1{}, serviceapi.ErrUnsafeAdmissionMaterialization
		}
		if !present {
			continue
		}
		var binding AdmissionBindingV1
		if strictJSON(data, &binding) != nil {
			return AdmissionBindingV1{}, serviceapi.ErrUnsafeAdmissionMaterialization
		}
		if binding.RunID != runID {
			continue
		}
		if matched {
			return AdmissionBindingV1{}, serviceapi.ErrUnsafeAdmissionMaterialization
		}
		found, matched = binding, true
	}
	if !matched {
		return AdmissionBindingV1{}, serviceapi.ErrActionStatusNotFound
	}
	if err := validateAdmissionBinding(found, AdmissionReceiptV1{
		AdmissionKind:          found.AdmissionKind,
		Principal:              serviceapi.Principal{PrincipalID: found.PrincipalID},
		RequestID:              found.RequestID,
		CanonicalRequestSHA256: found.CanonicalRequestSHA256,
		RunID:                  found.RunID,
		ProfileID:              found.ProfileID,
		ProfileBindingSHA256:   found.ProfileBindingSHA256,
	}); err != nil {
		return AdmissionBindingV1{}, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	return found, nil
}

// ResumeRun re-launches the exact run subprocess with --resume after a human
// decision was durably recorded. It re-derives the launch from the durable
// admission binding, so it never synthesizes authority the original admission
// did not already carry.
//
// Resume is intentionally separate from first admission: the first-admission
// launch intent stays as durable evidence of the original launch, and resume is
// guarded only by the non-blocking launch lock plus the resume subprocess's own
// idempotency (it fails closed if the run is no longer paused). This allows the
// legitimate sequence pause -> resume -> re-implement -> pause -> resume.
func (c *Controller) ResumeRun(ctx context.Context, runID string) error {
	if c == nil || c.start == nil {
		return serviceapi.ErrAdmissionUnavailable
	}
	binding, err := c.bindingForRunID(runID)
	if err != nil {
		return err
	}
	lock, acquired, err := acquireLaunchLock(c.launchesFD, runID)
	if err != nil {
		return serviceapi.ErrAdmissionUnavailable
	}
	if !acquired {
		// A resume launch is already in flight for this run.
		lock.Close()
		return nil
	}
	args := []string{
		"run", "--resume", "--manifest", binding.ManifestPath, "--ledger", binding.CanonicalLedgerPath,
		"--evidence-root", binding.EvidenceRoot, "--cgroup-root", binding.CgroupRoot,
		"--service-root", c.serviceRoot,
		"--workflow-authority-config-file", binding.WorkflowAuthorityConfigPath,
	}
	if err := c.start(c.executable, args, lock); err != nil {
		lock.Close()
		return serviceapi.ErrAdmissionUnavailable
	}
	if err := lock.Close(); err != nil {
		return serviceapi.ErrAdmissionUnavailable
	}
	return nil
}

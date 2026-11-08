package runadmission

import (
	"context"
	"encoding/json"
	"errors"
	"syscall"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	runctl "github.com/pankajleh/autonomous-builder-control-plane/internal/run"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

type resumeOutcome int

const (
	resumeSkipped  resumeOutcome = iota // not this run, not now
	resumeLaunched                      // its resume was launched
	resumeWaiting                       // a passing cause (the wall, the workflow database): the pass stops here
)

// Pass is one pass over the registered runs: it launches the resumes due, at most ResumesPerPass, and answers how many.
func (r *InterruptedResumer) Pass(ctx context.Context) int {
	if r == nil || r.admissions == nil || r.runs == nil || ctx == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	resumes := &resumeRecords{admissionsFD: r.admissions.admissionsFD, fd: -1}
	defer resumes.close()
	launched, after := 0, ""
	for count := 0; count < maxResumeRuns && launched < ResumesPerPass && ctx.Err() == nil; {
		runs, more, err := r.runs.ListRuns(after, 100)
		if err != nil {
			r.logf("abcp resume pass stopped step=list-runs")
			return launched
		}
		for _, registration := range runs {
			count++
			after = registration.RunID
			if launched >= ResumesPerPass || ctx.Err() != nil {
				break
			}
			switch r.consider(ctx, registration.RunID, resumes) {
			case resumeLaunched:
				launched++
			case resumeWaiting:
				return launched
			}
		}
		if !more || len(runs) == 0 {
			break
		}
	}
	return launched
}

// consider launches one run's resume when it is due.
func (r *InterruptedResumer) consider(ctx context.Context, runID string, resumes *resumeRecords) resumeOutcome {
	if r.settled[runID] {
		return resumeSkipped
	}
	facts, err := r.runs.InterruptedRun(ctx, runID)
	if err != nil {
		return resumeSkipped
	}
	if facts.Interruption.Settled() {
		r.settled[runID] = true
		return resumeSkipped
	}
	if !facts.Interruption.Resumable() || facts.OwnerLive {
		return resumeSkipped
	}
	c := r.admissions
	if c.clock().Sub(facts.Interruption.LastEvent) > ResumeWithin {
		r.settled[runID] = true
		r.logf("abcp resume skipped run=%s state=%s reason=interrupted_over_%s_ago", runID, facts.Interruption.State, ResumeWithin)
		return resumeSkipped
	}
	if found, err := resumes.found(runID); err != nil {
		r.logf("abcp resume pass stopped step=resume-records")
		return resumeWaiting
	} else if found {
		// Launched once already: never again, whatever the run did with it.
		r.settled[runID] = true
		return resumeSkipped
	}
	binding, err := c.bindingForRunID(runID)
	if errors.Is(err, serviceapi.ErrActionStatusNotFound) {
		// A run not admitted here has no launch to repeat.
		r.settled[runID] = true
		return resumeSkipped
	}
	if err != nil {
		return resumeSkipped
	}
	if allowed, err := resumeAllowed(binding); err != nil {
		return resumeSkipped
	} else if !allowed {
		r.settled[runID] = true
		return resumeSkipped
	}
	lock, acquired, err := acquireLaunchLock(c.launchesFD, runID)
	if err != nil || !acquired {
		// Its owner, or a launch of it, holds the lock.
		return resumeSkipped
	}
	if err := c.requireWall(ctx); err != nil {
		lock.Close()
		r.logf("abcp resume waits run=%s cause=build_wall_unavailable", runID)
		return resumeWaiting
	}
	if c.workflowProbe != nil {
		if err := c.workflowProbe(ctx, binding.WorkflowAuthorityConfigPath); err != nil {
			lock.Close()
			r.logf("abcp resume waits run=%s cause=workflow_database_unavailable", runID)
			return resumeWaiting
		}
	}
	record := InterruptedResumeV1{
		Kind: InterruptedResumeKind, SchemaVersion: 1, RunID: runID, State: string(facts.Interruption.State),
		Attempts: facts.Interruption.Attempts, LaunchedAt: c.clock().UTC().Format(time.RFC3339Nano),
	}
	if err := resumes.write(record); err != nil {
		lock.Close()
		r.logf("abcp resume pass stopped step=resume-record run=%s", runID)
		return resumeWaiting
	}
	args := c.runArgs([]string{
		"run", "--resume-interrupted", "--manifest", binding.ManifestPath, "--ledger", binding.CanonicalLedgerPath,
		"--evidence-root", binding.EvidenceRoot, "--cgroup-root", binding.CgroupRoot,
		"--service-root", c.serviceRoot,
		"--workflow-authority-config-file", binding.WorkflowAuthorityConfigPath,
	})
	if err := c.start(c.executable, args, lock); err != nil {
		lock.Close()
		// Nothing was launched, so the record goes and a later pass may launch it.
		if removeErr := resumes.remove(runID); removeErr != nil {
			r.settled[runID] = true
		}
		r.logf("abcp resume launch failed run=%s", runID)
		return resumeSkipped
	}
	lock.Close()
	r.settled[runID] = true
	r.logf("abcp resume launched run=%s state=%s attempt=%d", runID, facts.Interruption.State, facts.Interruption.Attempts+1)
	return resumeLaunched
}

// resumeAllowed reads the run's own frozen manifest (the one its binding digests) and answers whether its profile
// allows a resume after an interruption.
func resumeAllowed(binding AdmissionBindingV1) (bool, error) {
	data, err := readProtectedFile(binding.ManifestPath, MaxManifestTemplateBytes)
	if err != nil {
		return false, err
	}
	if digest(data) != binding.AuthorityDigest {
		return false, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	var manifest authority.Manifest
	if err := strictJSON(data, &manifest); err != nil {
		return false, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	return runctl.ResumesAfterInterruption(manifest), nil
}

// resumeRecords is admissions/resumes, opened for one pass when a run first needs it.
type resumeRecords struct {
	admissionsFD int
	fd           int
}

func (s *resumeRecords) open() error {
	if s.fd >= 0 {
		return nil
	}
	fd, err := openChildDirectory(s.admissionsFD, resumesDirectory, true)
	if err != nil {
		return err
	}
	if err := validatePrivateDirectoryFD(fd); err != nil {
		syscall.Close(fd)
		return err
	}
	s.fd = fd
	return nil
}

func (s *resumeRecords) found(runID string) (bool, error) {
	if err := s.open(); err != nil {
		return false, err
	}
	data, found, err := readRegularAt(s.fd, runID+".json", MaxInterruptedResumeBytes)
	if err != nil || !found {
		return false, err
	}
	var record InterruptedResumeV1
	if strictJSON(data, &record) != nil || record.Kind != InterruptedResumeKind || record.RunID != runID {
		return false, serviceapi.ErrUnsafeAdmissionMaterialization
	}
	return true, nil
}

func (s *resumeRecords) write(record InterruptedResumeV1) error {
	if err := s.open(); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil || len(data) > MaxInterruptedResumeBytes {
		return serviceapi.ErrUnsafeAdmissionMaterialization
	}
	return writeNewAtomicAt(s.fd, record.RunID+".json", data)
}

func (s *resumeRecords) remove(runID string) error {
	if err := s.open(); err != nil {
		return err
	}
	if err := syscall.Unlinkat(s.fd, runID+".json"); err != nil {
		return err
	}
	return syscall.Fsync(s.fd)
}

func (s *resumeRecords) close() {
	if s.fd >= 0 {
		syscall.Close(s.fd)
		s.fd = -1
	}
}

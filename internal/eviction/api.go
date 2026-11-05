package eviction

import (
	"context"
	"errors"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// API serves product-requested worktree releases through the service API.
type API struct{ Sweeper *Sweeper }

var (
	_ serviceapi.WorktreeReleaser = API{}
	_ serviceapi.RunForgetter     = API{}
)

func (a API) ReleaseRunWorktree(ctx context.Context, _ serviceapi.Principal, run string, request serviceapi.WorktreeReleaseRequestV1) (serviceapi.WorktreeReleaseV1, error) {
	result, err := a.Sweeper.Release(ctx, run, request.Reason)
	if err != nil {
		return serviceapi.WorktreeReleaseV1{}, apiError(err)
	}
	v := serviceapi.WorktreeReleaseV1{SchemaVersion: "WorktreeReleaseV1", RunID: run, Status: result.Status}
	if result.Status == Released || result.Status == AlreadyReleased {
		v.Reason, v.ReleasedAt = result.Record.Reason, result.Record.EvictedAt
	}
	return v, nil
}

func (a API) ReleaseRepositoryWorktrees(ctx context.Context, _ serviceapi.Principal, request serviceapi.RepositoryWorktreeReleaseRequestV1) (serviceapi.RepositoryWorktreeReleaseV1, error) {
	result, err := a.Sweeper.ReleaseRepository(ctx, request.RepositoryIdentity, request.Reason)
	if err != nil {
		return serviceapi.RepositoryWorktreeReleaseV1{}, apiError(err)
	}
	return serviceapi.RepositoryWorktreeReleaseV1{SchemaVersion: "RepositoryWorktreeReleaseV1", RepositoryIdentity: request.RepositoryIdentity, Reason: request.Reason,
		Released: result.Released, AlreadyReleased: result.AlreadyReleased, Unfinished: result.Unfinished, Failed: result.Failed}, nil
}

// ForgetRun forgets a finished run on product request (Sweeper.Forget).
func (a API) ForgetRun(ctx context.Context, _ serviceapi.Principal, run string, request serviceapi.RunForgetRequestV1) (serviceapi.RunForgetV1, error) {
	result, err := a.Sweeper.Forget(ctx, run, request.Reason, request.RequestID)
	if err != nil {
		return serviceapi.RunForgetV1{}, apiError(err)
	}
	return result.Record.V1(result.Status), nil
}

// RunForgotten reports whether the run's forget is sealed.
func (a API) RunForgotten(run string) (bool, error) {
	if a.Sweeper == nil || a.Sweeper.Forgets == nil {
		return false, nil
	}
	return a.Sweeper.Forgets.Sealed(run)
}

func apiError(err error) error {
	switch {
	case errors.Is(err, ErrRunNotFinished):
		return serviceapi.ErrRunNotFinished
	case errors.Is(err, ErrRunOnHold):
		return serviceapi.ErrRunOnHold
	case errors.Is(err, ErrRunForgotten):
		return serviceapi.ErrRunForgotten
	case errors.Is(err, ErrForgetConflict):
		return serviceapi.ErrRequestIDConflict
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	}
	return serviceapi.ErrInternalDurableSubstrate
}

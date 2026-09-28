package eviction

import (
	"context"
	"errors"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// API serves product-requested worktree releases through the service API.
type API struct{ Sweeper *Sweeper }

var _ serviceapi.WorktreeReleaser = API{}

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

func apiError(err error) error {
	switch {
	case errors.Is(err, ErrRunNotFinished):
		return serviceapi.ErrRunNotFinished
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	}
	return serviceapi.ErrInternalDurableSubstrate
}

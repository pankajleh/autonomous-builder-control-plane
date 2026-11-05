package run

import (
	"context"
	"io"
)

// LockRepository takes repository's execution lock, the one a run holds while it sets up its worktree and while it
// removes its execution plan copy, and answers its release. It waits until ctx ends. The nightly git gc runs under it
// (Repo C design note APP_DELETION.md §4), so no run sets up while gc repacks.
func LockRepository(ctx context.Context, repository string) (io.Closer, error) {
	lease, err := acquireRepositoryExecutionLease(ctx, repository)
	if err != nil {
		return nil, err
	}
	return lease, nil
}

//go:build linux

package activity

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestResolverDiagnosticsNameWorktreeStepWithoutChangingClass(t *testing.T) {
	for _, scenario := range []struct {
		name, want string
		prepare    func(*bindingFixture) context.Context
	}{
		{"canceled git", "worktree/list/git-canceled", func(*bindingFixture) context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}},
		{"expired git deadline", "worktree/list/git-deadline", func(*bindingFixture) context.Context {
			ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			t.Cleanup(cancel)
			return ctx
		}},
		{"detached worktree", "worktree/branch-missing", func(f *bindingFixture) context.Context {
			command(t, f.worktree, "checkout", "--detach")
			return context.Background()
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newBindingFixture(t)
			_, err := (Resolver{f.root, f.catalog}).Resolve(scenario.prepare(f), f.run)
			if !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrIntegrity) {
				t.Fatal("binding resolution changed its unavailable class", err)
			}
			if got := diagnosticStep(err); got != scenario.want {
				t.Fatalf("diagnostic step = %q, want %q", got, scenario.want)
			}
		})
	}
}

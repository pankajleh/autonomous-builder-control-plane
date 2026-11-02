package runadmission

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	runctl "github.com/pankajleh/autonomous-builder-control-plane/internal/run"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// defaultWallProbeEvery is how long a passed wall probe holds before an admission probes again.
const defaultWallProbeEvery = 10 * time.Minute

// wallFailedProbeEvery is how often a failed probe is tried again, so a wall put right is seen without a restart.
const wallFailedProbeEvery = time.Minute

// wallState is the build wall's probe (Repo C design note BUILD_ISOLATION.md §5.1 step 7): run at start and before
// the first build after it, then again once a passed probe is older than every.
type wallState struct {
	mu         sync.Mutex
	probe      func(context.Context) error
	every      time.Duration
	clock      func() time.Time
	err        error
	at         time.Time
	firstBuild bool
}

func newWallState(wall *runctl.Wall, canaries []string, every time.Duration, clock func() time.Time) *wallState {
	if every <= 0 {
		every = defaultWallProbeEvery
	}
	state := &wallState{every: every, clock: clock, firstBuild: true, err: errors.New("the build wall is not probed yet")}
	state.probe = func(ctx context.Context) error {
		if err := sameWallFile(wall); err != nil {
			return err
		}
		return wall.Probe(ctx, canaries)
	}
	return state
}

// sameWallFile answers an error when the wall file on disk is no longer the one abcp serve loaded and probes: each run
// loads it again, and would otherwise start in a wall no probe has seen.
func sameWallFile(wall *runctl.Wall) error {
	current, err := runctl.BuildWallFileSHA256(wall.Path())
	if err != nil {
		return err
	}
	if current != wall.SHA256() {
		return fmt.Errorf("%w: the wall file changed since abcp serve loaded it (restart abcp serve to probe the new one)", runctl.ErrBuildWallUnavailable)
	}
	return nil
}

// check answers the last probe's answer, probing again first when forced, when a passed probe is older than every, or
// when a failed one is older than a minute.
func (s *wallState) check(ctx context.Context, force bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	age := s.clock().Sub(s.at)
	if force || s.at.IsZero() || (s.err == nil && age >= s.every) || (s.err != nil && age >= wallFailedProbeEvery) {
		s.err = s.probe(ctx)
		s.at = s.clock()
	}
	return s.err
}

// fail records err as the probe's answer now.
func (s *wallState) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err, s.at = err, s.clock()
}

// last answers the last probe's answer without probing.
func (s *wallState) last() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// BuildWallStatus answers why the build wall cannot start, nil when its probe passed; abcp serve logs it at start.
func (c *Controller) BuildWallStatus(ctx context.Context) error {
	if c == nil || c.wall == nil || c.wallState == nil {
		return errors.New("no build wall file is configured")
	}
	return c.wallState.check(ctx, false)
}

// BuildIsolation answers "bwrap-v1" while the last probe passed (GET /v1/capabilities, GET /v1/engines).
func (c *Controller) BuildIsolation() string {
	if c == nil || c.wall == nil || c.wallState == nil || c.wallState.last() != nil {
		return ""
	}
	return runctl.BuildIsolationKindV1
}

// requireWall refuses a build while the wall cannot start; the first build after the start probes again.
func (c *Controller) requireWall(ctx context.Context) error {
	if c.wall == nil || c.wallState == nil {
		return serviceapi.ErrBuildWallUnavailable
	}
	c.wallState.mu.Lock()
	first := c.wallState.firstBuild
	c.wallState.firstBuild = false
	c.wallState.mu.Unlock()
	if err := c.wallState.check(ctx, first); err != nil {
		return fmt.Errorf("%w: %v", serviceapi.ErrBuildWallUnavailable, err)
	}
	// Every build and resume checks the file is still the one probed, as its run will; a changed file fails the
	// probe's answer until abcp serve is restarted.
	if err := sameWallFile(c.wall); err != nil {
		c.wallState.fail(err)
		return fmt.Errorf("%w: %v", serviceapi.ErrBuildWallUnavailable, err)
	}
	return nil
}

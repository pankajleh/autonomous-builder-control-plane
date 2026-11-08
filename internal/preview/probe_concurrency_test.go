package preview

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// eachBounded calls every index once, never more than its limit at once, and returns only after the last call.
func TestEachBoundedCallsEveryIndexOnceWithinItsLimit(t *testing.T) {
	for _, tc := range []struct{ n, limit, want int }{{0, 4, 0}, {1, 4, 1}, {3, 4, 3}, {15, 4, 4}, {5, 1, 1}, {5, 0, 1}} {
		t.Run(fmt.Sprintf("n=%d,limit=%d", tc.n, tc.limit), func(t *testing.T) {
			var mu sync.Mutex
			calls := make([]int, tc.n)
			var running, most atomic.Int32
			var done atomic.Int32
			eachBounded(tc.n, tc.limit, func(i int) {
				now := running.Add(1)
				for {
					m := most.Load()
					if now <= m || most.CompareAndSwap(m, now) {
						break
					}
				}
				time.Sleep(10 * time.Millisecond)
				mu.Lock()
				calls[i]++
				mu.Unlock()
				running.Add(-1)
				done.Add(1)
			})
			if int(done.Load()) != tc.n {
				t.Fatalf("returned after %d of %d calls", done.Load(), tc.n)
			}
			for i, c := range calls {
				if c != 1 {
					t.Fatalf("index %d called %d times", i, c)
				}
			}
			if int(most.Load()) != tc.want {
				t.Fatalf("at most %d at once, want %d", most.Load(), tc.want)
			}
		})
	}
}

// The startup probes run side by side, at most probeConcurrency at once, and every profile is still probed: one whose
// probe fails is left unapproved and does not hold up or approve another.
func TestStartupProbesRunSideBySideAndEachProfileIsStillProved(t *testing.T) {
	d := newDocker(t.TempDir())
	profiles := map[string]PreviewProfileV1{}
	for i := 0; i < 15; i++ {
		p := testProfile()
		p.ProfileID = fmt.Sprintf("web-v%d", i+1)
		profiles[p.ProfileID] = p
	}
	var running, most, probes atomic.Int32
	d.run = func(ctx context.Context, args ...string) (string, error) {
		// Each probe begins by asking whether the host supports previews; refusing it ends the probe unproved,
		// after a pause long enough for the probes running together to overlap.
		if len(args) > 0 && args[0] == "info" {
			probes.Add(1)
			now := running.Add(1)
			for {
				m := most.Load()
				if now <= m || most.CompareAndSwap(m, now) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			running.Add(-1)
			return "", errors.New("no docker here")
		}
		return "", nil
	}
	started := time.Now()
	d.proveProfiles(context.Background(), profiles)
	elapsed := time.Since(started)
	if int(probes.Load()) != len(profiles) {
		t.Fatalf("probed %d of %d profiles", probes.Load(), len(profiles))
	}
	if int(most.Load()) != probeConcurrency {
		t.Fatalf("at most %d probes at once, want %d", most.Load(), probeConcurrency)
	}
	// One at a time this takes 15 x 50 ms; four at a time, four rounds of 50 ms.
	if elapsed >= 15*50*time.Millisecond {
		t.Fatalf("probes took %s, as long as one at a time", elapsed)
	}
	for id, p := range profiles {
		if d.Available(p) {
			t.Fatalf("profile %s approved after a failed probe", id)
		}
	}
}

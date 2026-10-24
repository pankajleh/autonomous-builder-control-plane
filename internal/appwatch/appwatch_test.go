package appwatch

import (
	"context"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var shop = App{Kind: KindHosted, HostingKey: "82daed69-2b7e-4d0a-afd5-b3fe79738de1"}
var crm = App{Kind: KindSite, Target: "srv-9a0accb29e34", Site: "crm"}

func states(w *Watch) []string {
	var out []string
	for _, e := range w.Events(0, 100).Events {
		out = append(out, e.State+" "+e.Kind)
	}
	return out
}

// BF-07e: an app unwell for ten minutes gets one "down" event, and one "up" event once it answers again; a busy app
// keeps what is known of it; an app its owner stopped is forgotten without an event.
func TestAnAppDownForTenMinutesIsToldOnceAndAgainWhenBack(t *testing.T) {
	c := &clock{now: time.Date(2026, 10, 24, 16, 0, 0, 0, time.UTC)}
	w, err := Open(t.TempDir(), c.Now)
	if err != nil {
		t.Fatal(err)
	}
	look := func(kind string, o ...Observation) {
		t.Helper()
		if err := w.Record(kind, o); err != nil {
			t.Fatal(err)
		}
	}
	look(KindHosted, Observation{shop, Healthy})
	look(KindHosted, Observation{shop, Unwell})
	c.Add(9 * time.Minute)
	look(KindHosted, Observation{shop, Unwell})
	if len(states(w)) != 0 {
		t.Fatal("told before ten minutes", states(w))
	}
	c.Add(time.Minute)
	look(KindHosted, Observation{shop, Busy})
	look(KindHosted, Observation{shop, Unwell})
	look(KindHosted, Observation{shop, Unwell})
	v := w.Events(0, 100)
	if len(v.Events) != 1 || v.Events[0].State != "down" || v.Events[0].HostingKey != shop.HostingKey || v.Events[0].Since != "2026-10-24T16:00:00Z" || v.Events[0].At != "2026-10-24T16:10:00Z" || v.LastSeq != 1 {
		t.Fatalf("down = %+v", v)
	}
	c.Add(time.Hour)
	look(KindHosted, Observation{shop, Unwell})
	look(KindHosted, Observation{shop, Healthy})
	look(KindHosted, Observation{shop, Healthy})
	if got := states(w); len(got) != 2 || got[1] != "up hosted" {
		t.Fatal("events", got)
	}
	// Unwell again briefly, then stopped by its owner: forgotten, nothing said.
	look(KindHosted, Observation{shop, Unwell})
	c.Add(5 * time.Minute)
	look(KindHosted)
	c.Add(10 * time.Minute)
	look(KindHosted, Observation{shop, Unwell})
	if len(states(w)) != 2 {
		t.Fatal("a stop was not a fresh start", states(w))
	}
}

// BF-07e: each kind is its own sweep, so a sweep of servers never forgets a share link; a down app forgotten (taken off)
// sends no "up"; what is known survives a restart of ABCP.
func TestEachKindIsItsOwnSweepAndTheWatchSurvivesARestart(t *testing.T) {
	c := &clock{now: time.Date(2026, 10, 24, 16, 0, 0, 0, time.UTC)}
	dir := t.TempDir()
	w, _ := Open(dir, c.Now)
	_ = w.Record(KindHosted, []Observation{{shop, Unwell}})
	_ = w.Record(KindSite, []Observation{{crm, Unwell}})
	c.Add(10 * time.Minute)
	_ = w.Record(KindSite, []Observation{{crm, Unwell}})
	_ = w.Record(KindHosted, []Observation{{shop, Unwell}})
	if got := states(w); len(got) != 2 || got[0] != "down site" || got[1] != "down hosted" {
		t.Fatal("events", got)
	}
	again, err := Open(dir, c.Now)
	if err != nil {
		t.Fatal(err)
	}
	_ = again.Record(KindSite, []Observation{{crm, Healthy}})
	_ = again.Record(KindHosted, nil)
	_ = again.Record(KindHosted, []Observation{{shop, Healthy}})
	got := states(again)
	if len(got) != 3 || got[2] != "up site" {
		t.Fatal("after a restart", got)
	}
	if e := again.Events(0, 100).Events[2]; e.Target != crm.Target || e.Site != crm.Site || e.Seq != 3 {
		t.Fatalf("up = %+v", e)
	}
}

// BF-07e: Repo C reads the events after its cursor, a page at a time; at most 500 are kept, and a cursor older than the
// oldest kept says some were dropped.
func TestEventsArePagedAfterACursorAndBounded(t *testing.T) {
	c := &clock{now: time.Date(2026, 10, 24, 16, 0, 0, 0, time.UTC)}
	w, _ := Open(t.TempDir(), c.Now)
	for i := 0; i < 260; i++ {
		_ = w.Record(KindHosted, []Observation{{shop, Unwell}})
		c.Add(DownAfter)
		_ = w.Record(KindHosted, []Observation{{shop, Unwell}})
		_ = w.Record(KindHosted, []Observation{{shop, Healthy}})
	}
	v := w.Events(0, 100)
	if v.LastSeq != 520 || len(v.Events) != 100 || v.Events[0].Seq != 21 || !v.Truncated {
		t.Fatalf("first page: last %d, %d events from %d, truncated %v", v.LastSeq, len(v.Events), v.Events[0].Seq, v.Truncated)
	}
	v = w.Events(500, 100)
	if len(v.Events) != 20 || v.Events[0].Seq != 501 || v.Truncated {
		t.Fatalf("last page: %d events from %d, truncated %v", len(v.Events), v.Events[0].Seq, v.Truncated)
	}
	if v := w.Events(520, 100); len(v.Events) != 0 || v.LastSeq != 520 || v.Events == nil {
		t.Fatalf("nothing new = %+v", v)
	}
}

// BF-07e: Run looks at each source on its own schedule, and a source that could not look changes nothing.
func TestRunRecordsWhatEachSourceSees(t *testing.T) {
	c := &clock{now: time.Date(2026, 10, 24, 16, 0, 0, 0, time.UTC)}
	w, _ := Open(t.TempDir(), c.Now)
	_ = w.Record(KindSite, []Observation{{crm, Unwell}})
	ctx, cancel := context.WithCancel(context.Background())
	looked := make(chan string, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.Run(ctx, Source{Kind: KindHosted, Every: time.Hour, Look: func(context.Context) ([]Observation, bool) {
			looked <- "hosted"
			return []Observation{{shop, Healthy}}, true
		}}, Source{Kind: KindSite, Every: time.Hour, Look: func(context.Context) ([]Observation, bool) {
			looked <- "site"
			return nil, false
		}})
	}()
	<-looked
	<-looked
	cancel()
	<-done
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f.Apps[shop.id()] == nil || w.f.Apps[crm.id()] == nil || w.f.Apps[crm.id()].UnwellSince.IsZero() {
		t.Fatal("apps", w.f.Apps)
	}
}

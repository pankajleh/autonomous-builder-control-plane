// Package appwatch tells Repo C when an app ABCP runs stops answering and when it answers again (Repo C design note
// BF07_WHEN_A_RUNNING_APP_GOES_WRONG §4.5, 7e): a share link's app, from its hosted state, and an app on a server, from a
// check of its address. An app unwell for DownAfter gets one "down" event; the same app answering again gets one "up"
// event. An app its owner paused, stopped or took off is forgotten without an event. Repo C reads the events in order
// with a cursor; nothing is pushed.
package appwatch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// DownAfter is how long an app must be unwell before its owner is told.
const DownAfter = 10 * time.Minute

// eventLimit bounds the events kept for Repo C to read.
const eventLimit = 500

// Kinds of app.
const (
	KindHosted = "hosted"
	KindSite   = "site"
)

// App names one app: a hosting key, or a target and a site on it.
type App struct {
	Kind, HostingKey, Target, Site string
}

func (a App) id() string {
	if a.Kind == KindHosted {
		return KindHosted + " " + a.HostingKey
	}
	return KindSite + " " + a.Target + " " + a.Site
}

// What one look at an app found.
const (
	Healthy = "healthy"
	Unwell  = "unwell"
	// Busy is an app being started, updated or taken off by its owner: what is known of it stays as it was.
	Busy = "busy"
)

// Observation is one look at an app its owner keeps running.
type Observation struct {
	App   App
	State string
}

type appState struct {
	App         App       `json:"app"`
	UnwellSince time.Time `json:"unwell_since"`
	Down        bool      `json:"down"`
}

type file struct {
	Seq    uint64                        `json:"seq"`
	Apps   map[string]*appState          `json:"apps"`
	Events []serviceapi.AppHealthEventV1 `json:"events"`
}

// Watch keeps each app's state and the events in one private file, written whole on each change.
type Watch struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
	f    file
}

// Open reads the watch's file in dir, or starts empty when there is none.
func Open(dir string, now func() time.Time) (*Watch, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	w := &Watch{path: filepath.Join(dir, "watch.json"), now: now, f: file{Apps: map[string]*appState{}}}
	data, err := os.ReadFile(w.path)
	if errors.Is(err, os.ErrNotExist) {
		return w, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &w.f); err != nil {
		return nil, err
	}
	if w.f.Apps == nil {
		w.f.Apps = map[string]*appState{}
	}
	return w, nil
}

// Record takes one sweep of kind's apps: every app of that kind its owner keeps running, as observed. Apps of that kind
// not in it are forgotten without an event. A source that could not look at its apps does not call Record.
func (w *Watch) Record(kind string, observations []Observation) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now().UTC()
	seen := map[string]bool{}
	changed := false
	for _, o := range observations {
		if o.App.Kind != kind {
			continue
		}
		id := o.App.id()
		seen[id] = true
		s := w.f.Apps[id]
		if s == nil {
			s = &appState{App: o.App}
			w.f.Apps[id] = s
			changed = true
		}
		switch o.State {
		case Healthy:
			if !s.UnwellSince.IsZero() {
				s.UnwellSince, changed = time.Time{}, true
			}
			if s.Down {
				s.Down, changed = false, true
				w.event(o.App, "up", now, now)
			}
		case Unwell:
			if s.UnwellSince.IsZero() {
				s.UnwellSince, changed = now, true
			}
			if !s.Down && now.Sub(s.UnwellSince) >= DownAfter {
				s.Down, changed = true, true
				w.event(o.App, "down", s.UnwellSince, now)
			}
		}
	}
	for id, s := range w.f.Apps {
		if s.App.Kind == kind && !seen[id] {
			delete(w.f.Apps, id)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return w.save()
}

// event appends one event. Call with w.mu held.
func (w *Watch) event(app App, state string, since, at time.Time) {
	w.f.Seq++
	w.f.Events = append(w.f.Events, serviceapi.AppHealthEventV1{Seq: w.f.Seq, At: at.Format(time.RFC3339), Kind: app.Kind,
		HostingKey: app.HostingKey, Target: app.Target, Site: app.Site, State: state, Since: since.Format(time.RFC3339)})
	if len(w.f.Events) > eventLimit {
		w.f.Events = w.f.Events[len(w.f.Events)-eventLimit:]
	}
}

// save writes the file whole, synced, before it replaces the old one. Call with w.mu held.
func (w *Watch) save() error {
	data, err := json.Marshal(w.f)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(w.path), ".watch-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), w.path)
}

// Events answers at most limit events after seq, oldest first, and the newest seq there is.
func (w *Watch) Events(after uint64, limit int) serviceapi.AppHealthEventsV1 {
	w.mu.Lock()
	defer w.mu.Unlock()
	v := serviceapi.AppHealthEventsV1{SchemaVersion: serviceapi.AppHealthEventsSchemaV1, Events: []serviceapi.AppHealthEventV1{}, LastSeq: w.f.Seq}
	i := sort.Search(len(w.f.Events), func(i int) bool { return w.f.Events[i].Seq > after })
	for ; i < len(w.f.Events) && len(v.Events) < limit; i++ {
		v.Events = append(v.Events, w.f.Events[i])
	}
	v.Truncated = len(w.f.Events) > 0 && after+1 < w.f.Events[0].Seq
	return v
}

// Source looks at one kind of app every so often. Look answers false when it could not look, and nothing is changed then.
type Source struct {
	Kind  string
	Every time.Duration
	Look  func(context.Context) ([]Observation, bool)
}

// Run looks at each source's apps on its own schedule until ctx ends.
func (w *Watch) Run(ctx context.Context, sources ...Source) {
	var wg sync.WaitGroup
	for _, s := range sources {
		wg.Add(1)
		go func(s Source) {
			defer wg.Done()
			ticker := time.NewTicker(s.Every)
			defer ticker.Stop()
			for {
				if observations, ok := s.Look(ctx); ok && ctx.Err() == nil {
					_ = w.Record(s.Kind, observations)
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}(s)
	}
	wg.Wait()
}

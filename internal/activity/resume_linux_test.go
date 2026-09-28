//go:build linux

package activity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

type providerFrame struct {
	id   uint64
	text string
}

// ralphexSidecar mimics the pinned sidecar's resume contract, including its
// defect: a Last-Event-ID equal to the newest event is treated as unknown and
// the whole session is replayed with a live-only ID 0 frame first.
type ralphexSidecar struct {
	mu          sync.Mutex
	history     []string
	headers     []string
	fullReplays int
	respond     func(lastEventID string) []providerFrame
}

func (p *ralphexSidecar) frames(lastEventID string) []providerFrame {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.headers = append(p.headers, lastEventID)
	if p.respond != nil {
		return p.respond(lastEventID)
	}
	from := uint64(1)
	var frames []providerFrame
	if lastEventID != "" {
		lei, _ := strconv.ParseUint(lastEventID, 10, 64)
		if lei == uint64(len(p.history)) {
			p.fullReplays++
			frames = append(frames, providerFrame{0, "starting task execution phase"})
		} else {
			from = lei + 1
		}
	}
	for id := from; id <= uint64(len(p.history)); id++ {
		frames = append(frames, providerFrame{id, p.history[id-1]})
	}
	return frames
}

func (p *ralphexSidecar) server(t *testing.T, selected *session, onSessions func()) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions" {
			if onSessions != nil {
				onSessions()
			}
			json.NewEncoder(w).Encode([]session{*selected})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range p.frames(r.Header.Get("Last-Event-ID")) {
			payload, _ := json.Marshal(ProviderEvent{Type: "output", Phase: "task", Text: frame.text, Timestamp: stamp(testTime)})
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", frame.id, payload)
		}
	}))
}

func unknownEvents(t *testing.T, s *Service, run, registration string) []Event {
	t.Helper()
	events, _, _, err := s.store.read(run, registration, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var unknown []Event
	for _, event := range events {
		if event.Status == "UNKNOWN" {
			unknown = append(unknown, event)
		}
	}
	return unknown
}

func TestCaughtUpResumeNeverTriggersSidecarFullReplay(t *testing.T) {
	f := newBindingFixture(t)
	selected := providerSession(t, f)
	s := &Service{store: newStore(t), resolver: Resolver{f.root, f.catalog}, ctx: context.Background(), now: func() time.Time { return testTime }}
	registration := jsonDigest(f.catalog.runs[f.run])
	provider := &ralphexSidecar{history: []string{"one", "two", "three"}}
	server := provider.server(t, &selected, nil)
	defer server.Close()
	sc := testSidecar(server)
	last := uint64(math.MaxUint64)

	if err := s.collectBatch(f.scope, registration, sc, &last); err != nil || last != 3 {
		t.Fatal("initial replay", err, last)
	}
	// A caught-up, quiet batch re-sends only the committed newest event.
	if err := s.collectBatch(f.scope, registration, sc, &last); err != nil || last != 3 {
		t.Fatal("caught-up resume", err, last)
	}
	provider.mu.Lock()
	provider.history = append(provider.history, "four", "five")
	provider.mu.Unlock()
	if err := s.collectBatch(f.scope, registration, sc, &last); err != nil || last != 5 {
		t.Fatal("resume after growth", err, last)
	}

	if provider.fullReplays != 0 {
		t.Fatal("collector resumed from the newest event and triggered a full replay")
	}
	if want := []string{"", "2", "2"}; fmt.Sprint(provider.headers) != fmt.Sprint(want) {
		t.Fatalf("resume headers = %q, want %q", provider.headers, want)
	}
	events, _, _, err := s.store.read(f.run, registration, 0, 100)
	if err != nil || len(events) != 5 || events[4].Detail != "five" {
		t.Fatal("overlap was re-appended or new detail lost", events, err)
	}
	if unknown := unknownEvents(t, s, f.run, registration); len(unknown) != 0 {
		t.Fatal("healthy resume recorded an UNKNOWN marker", unknown)
	}
}

func TestResumeOverlapFailsClosed(t *testing.T) {
	for _, scenario := range []struct {
		name, step string
		frames     []providerFrame
	}{
		{"uncommitted live-only frame", "resume-overlap", []providerFrame{{0, "starting task execution phase"}, {3, "three"}, {4, "four"}}},
		{"changed committed frame", "resume-overlap", []providerFrame{{3, "rewritten"}, {4, "four"}}},
		{"repeated frame", "ordering", []providerFrame{{4, "four"}, {4, "four"}}},
		{"reordered frames", "ordering", []providerFrame{{5, "five"}, {4, "four"}}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newBindingFixture(t)
			selected := providerSession(t, f)
			s := &Service{store: newStore(t), resolver: Resolver{f.root, f.catalog}, ctx: context.Background(), now: func() time.Time { return testTime }}
			registration := jsonDigest(f.catalog.runs[f.run])
			provider := &ralphexSidecar{history: []string{"one", "two", "three"}}
			server := provider.server(t, &selected, nil)
			defer server.Close()
			sc := testSidecar(server)
			last := uint64(math.MaxUint64)
			if err := s.collectBatch(f.scope, registration, sc, &last); err != nil || last != 3 {
				t.Fatal("initial replay", err, last)
			}
			before, _, _, _ := s.store.read(f.run, registration, 0, 100)

			provider.respond = func(string) []providerFrame { return scenario.frames }
			err := s.collectBatch(f.scope, registration, sc, &last)
			if !errors.Is(err, ErrIntegrity) || diagnosticStep(err) != scenario.step {
				t.Fatalf("got %v at step %q, want integrity at %q", err, diagnosticStep(err), scenario.step)
			}
			after, _, _, _ := s.store.read(f.run, registration, 0, 100)
			if len(after) != len(before) || last != 3 {
				t.Fatal("rejected overlap changed events or resume position", after, last)
			}
		})
	}
}

func TestSilentGapCountsOnlyGrowthBeforeTheRead(t *testing.T) {
	for _, scenario := range []struct {
		name    string
		growAt  int // /api/sessions call that appends provider bytes
		wantGap bool
	}{
		{"growth after the bounded read", 4, false},
		{"growth before the bounded read", 3, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newBindingFixture(t)
			selected := providerSession(t, f)
			s := &Service{store: newStore(t), resolver: Resolver{f.root, f.catalog}, ctx: context.Background(), now: func() time.Time { return testTime }}
			registration := jsonDigest(f.catalog.runs[f.run])
			progress := filepath.Join(selected.DirPath, "progress-plan.txt")
			calls := 0
			provider := &ralphexSidecar{history: []string{"one"}}
			// Each batch reads sessions once before and once after the bounded
			// provider read; the second batch's reads are calls 3 and 4.
			server := provider.server(t, &selected, func() {
				calls++
				if calls == scenario.growAt {
					file, err := os.OpenFile(progress, os.O_APPEND|os.O_WRONLY, 0)
					if err == nil {
						file.WriteString("[26-09-26 01:00:01] appended line\n")
						file.Close()
					}
				}
			})
			defer server.Close()
			sc := testSidecar(server)
			last := uint64(math.MaxUint64)
			if err := s.collectBatch(f.scope, registration, sc, &last); err != nil || last != 1 {
				t.Fatal("initial replay", err, last)
			}
			if err := s.collectBatch(f.scope, registration, sc, &last); err != nil {
				t.Fatal("quiet batch", err)
			}
			unknown := unknownEvents(t, s, f.run, registration)
			if gap := len(unknown) == 1 && unknown[0].Title == "Implementation detail replay gap"; gap != scenario.wantGap || len(unknown) > 1 {
				t.Fatalf("silent gap = %v (%+v), want %v", gap, unknown, scenario.wantGap)
			}
		})
	}
}

func TestQueuedRunWaitingForTheRepositoryIsNeverMarked(t *testing.T) {
	// Runs of one repository build one at a time. A queued run has no worktree
	// until it reaches implementation, however long the build ahead takes.
	queued := []ledger.Event{
		{EventID: "created", EventType: string(domain.StateRunCreated), Timestamp: testTime},
		{EventID: "validated", EventType: "STATE_TRANSITION", StateFrom: domain.StateRunCreated, StateTo: domain.StateAuthorityValidated, Timestamp: testTime},
	}
	f := newBindingFixture(t)
	command(t, f.repo, "worktree", "remove", "--force", f.worktree)
	command(t, f.repo, "branch", "-D", "abcp/"+f.run)
	registration := jsonDigest(f.catalog.runs[f.run])
	now := testTime
	snapshots := &fakeSnapshots{events: queued}
	s := &Service{store: newStore(t), resolver: Resolver{f.root, f.catalog}, snapshots: snapshots, ctx: context.Background(), now: func() time.Time { return now }}
	for _, waited := range []time.Duration{5 * time.Second, worktreeTransitionWindow, 45 * time.Minute} {
		now = testTime.Add(waited)
		if _, err := s.refresh(context.Background(), f.run, registration); err != nil {
			t.Fatal(err)
		}
		if unknown := unknownEvents(t, s, f.run, registration); len(unknown) != 0 {
			t.Fatalf("queued run marked after %v: %+v", waited, unknown)
		}
	}
	// Once implementation has begun, a missing worktree is bounded as before.
	snapshots.mu.Lock()
	snapshots.events = append(snapshots.events, ledger.Event{EventID: "implementing", EventType: "STATE_TRANSITION",
		StateFrom: domain.StateExecutionStarting, StateTo: domain.StateImplementing, Timestamp: now})
	snapshots.mu.Unlock()
	now = now.Add(worktreeTransitionWindow + time.Second)
	if _, err := s.refresh(context.Background(), f.run, registration); err != nil {
		t.Fatal(err)
	}
	if len(unknownEvents(t, s, f.run, registration)) != 1 {
		t.Fatal("a worktree still missing well after implementation began was not marked")
	}
}

func TestMissingWorktreeIsPendingOnlyAtStartup(t *testing.T) {
	admitted := []ledger.Event{{EventID: "implementing", StateTo: domain.StateImplementing, Timestamp: testTime}}
	for _, scenario := range []struct {
		name        string
		elapsed     time.Duration
		proof       bool
		otherFault  bool
		wantPending bool
	}{
		{"worktree not yet created", 5 * time.Second, false, false, true},
		{"worktree still absent after the window", worktreeTransitionWindow, false, false, false},
		{"worktree lost after provider proof starts a bounded transition", 5 * time.Second, true, false, true},
		{"other binding failure at startup", 5 * time.Second, false, true, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			f := newBindingFixture(t)
			command(t, f.repo, "worktree", "remove", "--force", f.worktree)
			command(t, f.repo, "branch", "-D", "abcp/"+f.run)
			if scenario.otherFault {
				os.WriteFile(f.manifest.Plan.Path, []byte("# Work\nchanged plan\n"), 0600)
			}
			registration := jsonDigest(f.catalog.runs[f.run])
			now := testTime.Add(scenario.elapsed)
			s := &Service{store: newStore(t), resolver: Resolver{f.root, f.catalog}, snapshots: &fakeSnapshots{events: admitted}, ctx: context.Background(), now: func() time.Time { return now }}
			if scenario.proof {
				if err := s.store.saveProof(f.run, registration, providerProof{Session: "sha256-session", Generation: "generation", FileIdentity: "file", Size: 1, PrefixDigest: "prefix"}); err != nil {
					t.Fatal(err)
				}
			}
			scope, err := s.refresh(context.Background(), f.run, registration)
			if err != nil || scope.RunID != "" {
				t.Fatal("unresolved binding produced a scope or error", scope, err)
			}
			unknown := unknownEvents(t, s, f.run, registration)
			if pending := len(unknown) == 0; pending != scenario.wantPending {
				t.Fatalf("pending = %v with markers %+v, want %v", pending, unknown, scenario.wantPending)
			}
		})
	}
}

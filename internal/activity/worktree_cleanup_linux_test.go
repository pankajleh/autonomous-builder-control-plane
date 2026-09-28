//go:build linux

package activity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

func worktreeService(t *testing.T, f *bindingFixture, now *time.Time, states ...domain.State) (*Service, string) {
	t.Helper()
	facts := make([]ledger.Event, 0, len(states))
	for i, state := range states {
		facts = append(facts, ledger.Event{EventID: fmt.Sprint("fact-", i), StateTo: state, Timestamp: testTime.Add(time.Duration(i) * time.Minute)})
	}
	s := &Service{store: newStore(t), resolver: Resolver{f.root, f.catalog}, snapshots: &fakeSnapshots{events: facts}, ctx: context.Background(), now: func() time.Time { return *now }}
	registration := jsonDigest(f.catalog.runs[f.run])
	if err := s.store.saveProof(f.run, registration, providerProof{Session: "sha256-session", Generation: "generation", FileIdentity: "file", Size: 1, PrefixDigest: "prefix"}); err != nil {
		t.Fatal(err)
	}
	return s, registration
}

func refreshMarkers(t *testing.T, s *Service, run, registration string) int {
	t.Helper()
	if _, err := s.refresh(context.Background(), run, registration); err != nil {
		t.Fatal("refresh", err)
	}
	return len(unknownEvents(t, s, run, registration))
}

func TestWorktreeRemovalAfterImplementationIsExpectedCleanup(t *testing.T) {
	for _, state := range []domain.State{domain.StateImplementationCompleted, domain.StateBranchAcceptancePending} {
		t.Run(string(state), func(t *testing.T) {
			f := newBindingFixture(t)
			command(t, f.repo, "worktree", "remove", "--force", f.worktree)
			now := testTime.Add(time.Hour)
			s, registration := worktreeService(t, f, &now, domain.StateImplementing, state)
			for i := 0; i < 3; i++ {
				if markers := refreshMarkers(t, s, f.run, registration); markers != 0 {
					t.Fatal("provider worktree cleanup after implementation recorded a marker")
				}
				now = now.Add(worktreeTransitionWindow)
			}
		})
	}
}

func TestWorktreeRemovalDuringImplementationIsBoundedTransition(t *testing.T) {
	f := newBindingFixture(t)
	now := testTime.Add(time.Hour)
	s, registration := worktreeService(t, f, &now, domain.StateImplementing)
	command(t, f.repo, "worktree", "remove", "--force", f.worktree)
	if refreshMarkers(t, s, f.run, registration) != 0 {
		t.Fatal("first observation of an absent worktree was marked")
	}
	now = now.Add(worktreeTransitionWindow - time.Second)
	if refreshMarkers(t, s, f.run, registration) != 0 {
		t.Fatal("absent worktree was marked inside the transition window")
	}
	// Restoring the worktree resets the grace; a later absence starts afresh.
	command(t, f.repo, "worktree", "add", f.worktree, "abcp/"+f.run)
	if refreshMarkers(t, s, f.run, registration) != 0 {
		t.Fatal("restored worktree was marked")
	}
	command(t, f.repo, "worktree", "remove", "--force", f.worktree)
	now = now.Add(time.Second)
	if refreshMarkers(t, s, f.run, registration) != 0 {
		t.Fatal("new absence did not restart the transition window")
	}
	now = now.Add(worktreeTransitionWindow)
	if refreshMarkers(t, s, f.run, registration) != 1 {
		t.Fatal("worktree absent beyond the transition window during implementation was not marked")
	}
}

func TestWorktreeRemovedDuringBatchDefersToRefresh(t *testing.T) {
	f := newBindingFixture(t)
	selected := providerSession(t, f)
	s := &Service{store: newStore(t), resolver: Resolver{f.root, f.catalog}, ctx: context.Background(), now: func() time.Time { return testTime }}
	registration := jsonDigest(f.catalog.runs[f.run])
	stored, err := s.store.Append(f.run, registration, provider(t, f.run, "existing"))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions" {
			json.NewEncoder(w).Encode([]session{selected})
			return
		}
		command(t, f.repo, "worktree", "remove", "--force", f.worktree)
		w.Header().Set("Content-Type", "text/event-stream")
		payload, _ := json.Marshal(ProviderEvent{Type: "task_end", Phase: "task", Text: "done", Timestamp: stamp(testTime)})
		fmt.Fprintf(w, "id: 1\ndata: %s\n\n", payload)
	}))
	defer server.Close()
	last := uint64(0)
	err = s.collectBatch(f.scope, registration, testSidecar(server), &last)
	if !errors.Is(err, errWorktreeMissing) || errors.Is(err, ErrIntegrity) || diagnosticStep(err) != "revalidate-binding/worktree/branch-missing" {
		t.Fatalf("worktree removal during a batch: %v at %q", err, diagnosticStep(err))
	}
	events, _, _, err := s.store.read(f.run, registration, 0, 100)
	if err != nil || len(events) != 1 || events[0] != stored || last != 0 {
		t.Fatal("discarded batch changed events or resume position", events, last, err)
	}
	if proof, err := s.store.proof(f.run, registration); err != nil || proof != nil {
		t.Fatal("discarded batch changed durable proof", proof, err)
	}
}

func TestControllerEvictedWorktreeIsExpectedCleanupInAnyLaterState(t *testing.T) {
	for _, sealed := range []bool{true, false} {
		t.Run(fmt.Sprint("sealed=", sealed), func(t *testing.T) {
			f := newBindingFixture(t)
			command(t, f.repo, "worktree", "remove", "--force", f.worktree)
			now := testTime.Add(time.Hour)
			// MERGED follows acceptance and is not an activity terminal state.
			s, registration := worktreeService(t, f, &now, domain.StateBranchAccepted, domain.StateIntegrationPending, domain.StateMerged)
			s.SetEvicted(func(run string) bool { return sealed && run == f.run })
			markers := 0
			for i := 0; i < 3; i++ {
				markers = refreshMarkers(t, s, f.run, registration)
				now = now.Add(worktreeTransitionWindow)
			}
			if want := map[bool]int{true: 0, false: 1}[sealed]; markers != want {
				t.Fatalf("markers = %d, want %d", markers, want)
			}
		})
	}
}

func TestCheckpointsRecordTheFinalHeadBeforeEviction(t *testing.T) {
	f := newBindingFixture(t)
	now := testTime.Add(time.Hour)
	s, _ := worktreeService(t, f, &now, domain.StateBranchAccepted)
	os.WriteFile(filepath.Join(f.worktree, "source"), []byte("final\n"), 0600)
	command(t, f.worktree, "add", "source")
	command(t, f.worktree, "commit", "-m", "final")
	shas, err := s.Checkpoints(context.Background(), f.run)
	if err != nil || len(shas) != 1 || shas[0] != command(t, f.worktree, "rev-parse", "HEAD") {
		t.Fatalf("checkpoints = %v, %v", shas, err)
	}
	if streaming, last := s.Engagement(f.run); streaming || !last.IsZero() {
		t.Fatal("collecting checkpoints for eviction counted as use of the run")
	}
}

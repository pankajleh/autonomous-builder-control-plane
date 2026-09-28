//go:build linux

package activity

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

// A controller-retained worktree keeps the binding resolvable after the
// provider finishes, so trailing provider detail (for example the final review
// signals) is still collected after acceptance, without any controller marker.
func TestRetainedWorktreeCollectsTrailingDetailAfterAcceptance(t *testing.T) {
	f := newBindingFixture(t)
	selected := providerSession(t, f)
	var facts []ledger.Event
	for i, state := range []domain.State{domain.StateImplementing, domain.StateImplementationCompleted, domain.StateBranchAcceptancePending, domain.StateBranchAccepted} {
		facts = append(facts, ledger.Event{EventID: fmt.Sprint("fact-", i), StateTo: state, Timestamp: testTime.Add(time.Duration(i) * time.Minute)})
	}
	s := &Service{store: newStore(t), resolver: Resolver{f.root, f.catalog}, snapshots: &fakeSnapshots{events: facts}, ctx: context.Background(), now: func() time.Time { return testTime.Add(time.Hour) }}
	registration := jsonDigest(f.catalog.runs[f.run])
	provider := &ralphexSidecar{history: []string{"one", "two", "three"}}
	server := provider.server(t, &selected, nil)
	defer server.Close()
	sc := testSidecar(server)
	last := uint64(math.MaxUint64)

	scope, err := s.refresh(context.Background(), f.run, registration)
	if err != nil || scope.RunID != f.run {
		t.Fatal("accepted run with a retained worktree did not resolve its binding", scope, err)
	}
	if err = s.collectBatch(scope, registration, sc, &last); err != nil || last != 3 {
		t.Fatal("collection after acceptance", err, last)
	}
	provider.mu.Lock()
	provider.history = append(provider.history, "review done", "session finished")
	provider.mu.Unlock()
	if err = s.collectBatch(scope, registration, sc, &last); err != nil || last != 5 {
		t.Fatal("trailing detail was not collected", err, last)
	}
	events, _, _, err := s.store.read(f.run, registration, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	trailing := 0
	for _, event := range events {
		if event.Detail == "review done" || event.Detail == "session finished" {
			trailing++
		}
	}
	if trailing != 2 {
		t.Fatalf("trailing provider detail events = %d, want 2", trailing)
	}
	if unknown := unknownEvents(t, s, f.run, registration); len(unknown) != 0 {
		t.Fatal("retained worktree recorded a controller marker", unknown)
	}
}

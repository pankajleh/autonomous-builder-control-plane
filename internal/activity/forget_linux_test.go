//go:build linux

package activity

import (
	"errors"
	"os"
	"testing"
)

// A forgotten run's activity log and anchor go (Repo C APP_DELETION.md §4); another run's stay, the store reopens, and
// the forgotten run's log is never made again.
func TestForgetRemovesOnlyThatRunsActivityLog(t *testing.T) {
	s := newStore(t)
	if _, err := s.Append("a", "registration", provider(t, "a", "1")); err != nil {
		t.Fatal(err)
	}
	b, err := s.Append("b", "registration", provider(t, "b", "1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Forget("a"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{s.path("a"), s.path("a") + ".anchor.json"} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s is still there: %v", path, err)
		}
	}
	if err := s.Forget("a"); err != nil {
		t.Fatalf("forgetting again: %v", err)
	}
	if _, err := s.Append("a", "registration", provider(t, "a", "2")); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("a forgotten run's log was made again: %v", err)
	}
	s.Close()
	reopened, err := OpenStore(s.root)
	if err != nil {
		t.Fatalf("the store did not reopen after a forget: %v", err)
	}
	defer reopened.Close()
	events, _, _, err := reopened.read("b", "registration", 0, 100)
	if err != nil || len(events) != 1 || events[0].Ordinal != b.Ordinal {
		t.Fatalf("another run's activity changed: %+v, %v", events, err)
	}
	if _, _, _, err := reopened.read("a", "registration", 0, 100); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("a forgotten run's activity read as %v", err)
	}
}

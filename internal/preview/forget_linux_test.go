//go:build linux

package preview

import (
	"context"
	"testing"
)

// A forgotten run's live previews end, their containers stop and their sources go (Repo C APP_DELETION.md §4); the
// records stay, and forgetting again does nothing more.
func TestForgetPreviewsEndsTheRunsPreviewsAndRemovesTheirSources(t *testing.T) {
	rt := &memoryRuntime{available: true, healthy: true}
	s, m := fixtureService(t, rt)
	created, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", createCommand("create-1"))
	if err != nil {
		t.Fatal(err)
	}
	awaitPreview(t, s, created.PreviewID, "READY", "HEALTHY")
	if err := s.ForgetPreviews(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	if s.Live("run-1") {
		t.Fatal("a forgotten run's preview is still live")
	}
	v, err := s.ReadPreview(context.Background(), principal(), "run-1", created.PreviewID)
	if err != nil || v.Status != "STOPPED" || v.RouteHandle != "" {
		t.Fatalf("preview = %+v, %v", v, err)
	}
	s.mu.Lock()
	running := len(s.workers)
	s.mu.Unlock()
	rt.mu.Lock()
	stopped := append([]string{}, rt.stopped...)
	rt.mu.Unlock()
	m.mu.Lock()
	removed := append([]string{}, m.removed...)
	m.mu.Unlock()
	if running != 0 || !contains(stopped, created.PreviewID) || !contains(removed, created.PreviewID) {
		t.Fatalf("workers=%d stopped=%v removed=%v", running, stopped, removed)
	}
	if err := s.ForgetPreviews(context.Background(), "run-1"); err != nil {
		t.Fatalf("forgetting again: %v", err)
	}
	if err := s.ForgetPreviews(context.Background(), "run-2"); err != nil {
		t.Fatalf("a run with no previews: %v", err)
	}
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

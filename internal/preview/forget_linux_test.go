//go:build linux

package preview

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
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

// gatedResolver resolves a source only once release is closed, telling resolving when it is asked.
type gatedResolver struct {
	source    Source
	resolving chan struct{}
	release   chan struct{}
	once      *sync.Once
}

func (r gatedResolver) Resolve(ctx context.Context, _ string, _ string) (Source, error) {
	r.once.Do(func() { close(r.resolving) })
	select {
	case <-r.release:
	case <-ctx.Done():
		return Source{}, ctx.Err()
	}
	return r.source, nil
}

// A create that passed the route's check before the forget was sealed, and resolves its source while the run's previews
// are forgotten, is refused: no preview of a forgotten run is made after ForgetPreviews looked.
func TestAPreviewAskedForWhileTheRunIsForgottenIsRefused(t *testing.T) {
	rt := &memoryRuntime{available: true, healthy: true}
	s, _ := fixtureService(t, rt)
	static := s.resolver.(staticResolver)
	gate := gatedResolver{source: static.source, resolving: make(chan struct{}), release: make(chan struct{}), once: &sync.Once{}}
	s.resolver = gate
	type outcome struct {
		v   serviceapi.PreviewV1
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		v, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", createCommand("create-1"))
		done <- outcome{v, err}
	}()
	<-gate.resolving
	if err := s.ForgetPreviews(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	close(gate.release)
	got := <-done
	if !errors.Is(got.err, serviceapi.ErrRunForgotten) {
		t.Fatalf("a create racing the forget = %+v, %v", got.v, got.err)
	}
	if s.Live("run-1") {
		t.Fatal("a forgotten run has a live preview")
	}
	if list, err := s.ListPreviews(context.Background(), principal(), "run-1"); err != nil || len(list.Previews) != 0 {
		t.Fatalf("previews = %+v, %v", list, err)
	}
	// A later create of the run is refused too.
	s.resolver = static
	if _, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", createCommand("create-2")); !errors.Is(err, serviceapi.ErrRunForgotten) {
		t.Fatalf("a later create = %v", err)
	}
}

// A preview whose containers a forget could not stop stays pending: the next forget stops it again.
func TestAForgetStopsAgainWhatItCouldNotStop(t *testing.T) {
	rt := &memoryRuntime{available: true, healthy: true}
	s, _ := fixtureService(t, rt)
	created, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", createCommand("create-1"))
	if err != nil {
		t.Fatal(err)
	}
	awaitPreview(t, s, created.PreviewID, "READY", "HEALTHY")
	rt.mu.Lock()
	rt.stopErr = errors.New("docker is busy")
	rt.mu.Unlock()
	if err := s.ForgetPreviews(context.Background(), "run-1"); err == nil {
		t.Fatal("a forget whose stop failed succeeded")
	}
	rt.mu.Lock()
	rt.stopErr = nil
	before := len(rt.stopped)
	rt.mu.Unlock()
	if err := s.ForgetPreviews(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	again := append([]string{}, rt.stopped[before:]...)
	rt.mu.Unlock()
	if !contains(again, created.PreviewID) {
		t.Fatalf("the retry did not stop the preview again: %v", again)
	}
	// Once stopped, a further call stops nothing more.
	rt.mu.Lock()
	before = len(rt.stopped)
	rt.mu.Unlock()
	if err := s.ForgetPreviews(context.Background(), "run-1"); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	more := len(rt.stopped) - before
	rt.mu.Unlock()
	if more != 0 {
		t.Fatalf("a forget after the stop stopped %d more", more)
	}
}

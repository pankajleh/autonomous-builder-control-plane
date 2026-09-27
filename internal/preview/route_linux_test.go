//go:build linux

package preview

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

type routeRuntime struct {
	memoryRuntime
	id, handle, target string
	calls              int
	err                error
	duringLookup       func()
}

func (r *routeRuntime) ResolveRoute(ctx context.Context, id, handle string) (string, error) {
	r.calls++
	if r.duringLookup != nil {
		r.duringLookup()
	}
	if ctx.Err() != nil || id != r.id || handle != r.handle {
		return "", ErrUnavailable
	}
	return r.target, r.err
}

// Seed a valid journal without a background worker so lifecycle and clock
// boundary denials are deterministic, including expiry during lookup.
func routeService(t *testing.T) (*Service, *routeRuntime, serviceapi.PreviewV1, *time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	rt := &routeRuntime{memoryRuntime: memoryRuntime{available: true, healthy: true}, handle: strings.Repeat("d", 64), target: "http://127.0.0.1:23456"}
	s, _ := serviceAt(t, filepath.Join(t.TempDir(), "previews"), &rt.memoryRuntime, func() time.Time { return now })
	s.runtime = rt
	p, c := testProfile(), createCommand("route-create")
	r := createReceipt(principal(), testAuthorityDigest, "run-1", c)
	v := serviceapi.PreviewV1{
		SchemaVersion: "PreviewV1", PreviewID: jsonDigest([]string{r.Key, r.Digest}), Revision: 1, RunID: "run-1",
		CheckpointActivityID: c.CheckpointActivityID, SourceSHA: strings.Repeat("c", 40), ProductAuthorizationID: "auth-1", ProductTaskID: "task-1", ProductVersionID: "version-1",
		ProfileID: p.ProfileID, ProfileDigest: p.Digest(), Status: "REQUESTED", Health: "UNKNOWN", CreatedAt: stamp(now), ExpiresAt: stamp(now.Add(time.Minute)),
		ValidationID: strings.Repeat("e", 64), EvidenceID: strings.Repeat("f", 64), RequestID: c.RequestID, RequestDigest: r.Digest,
	}
	r.Result = v
	if err := s.store.append(v, &r); err != nil {
		t.Fatal(err)
	}
	if !s.transition(v.PreviewID, "VALIDATING", "UNKNOWN", "") || !s.transition(v.PreviewID, "STARTING", "UNKNOWN", "") || !s.transition(v.PreviewID, "READY", "HEALTHY", rt.handle) {
		t.Fatal("seed lifecycle")
	}
	rt.id = v.PreviewID
	return s, rt, s.store.records[v.PreviewID], &now
}

func TestResolvePreviewRouteExactCurrentHandleAndDegradedHealth(t *testing.T) {
	s, rt, v, _ := routeService(t)
	for _, health := range []string{"HEALTHY", "DEGRADED"} {
		if !s.transition(v.PreviewID, "READY", health, v.RouteHandle) {
			t.Fatal("health transition")
		}
		got, err := s.ResolvePreviewRoute(context.Background(), principal(), v.RunID, v.PreviewID)
		want := serviceapi.PreviewRouteV1{SchemaVersion: "PreviewRouteV1", RunID: v.RunID, PreviewID: v.PreviewID, RouteHandle: v.RouteHandle, ExpiresAt: v.ExpiresAt, TargetURL: rt.target}
		if err != nil || got != want {
			t.Fatal("exact route", got, err)
		}
	}
	// A runtime replacement cannot resolve the stale journal handle.
	rt.handle = strings.Repeat("e", 64)
	if got, err := s.ResolvePreviewRoute(context.Background(), principal(), v.RunID, v.PreviewID); !errors.Is(err, serviceapi.ErrPreviewUnavailable) || got != (serviceapi.PreviewRouteV1{}) {
		t.Fatal("stale handle resolved", got, err)
	}
	if !s.transition(v.PreviewID, "READY", "HEALTHY", rt.handle) {
		t.Fatal("current handle transition")
	}
	got, err := s.ResolvePreviewRoute(context.Background(), principal(), v.RunID, v.PreviewID)
	if err != nil || got.RouteHandle != rt.handle || got.TargetURL != rt.target {
		t.Fatal("current handle not resolved", got, err)
	}
	// Read-only resolution never changes the existing capability truth.
	if !s.Available() {
		t.Fatal("resolution changed capability")
	}
}

func TestResolvePreviewRouteDenials(t *testing.T) {
	for _, scenario := range []string{
		"unknown", "cross-run", "cross-owner", "non-service", "REQUESTED", "VALIDATING", "STARTING", "FAILED", "STOPPED", "EXPIRED", "expired-now", "expired-during-lookup",
		"stale-handle", "missing-handle", "unavailable-runtime", "runtime-error", "empty-target", "non-loopback", "dns-target", "closed", "unavailable-service", "broken-store", "canceled", "service-canceled", "missing-profile", "changed-profile",
	} {
		t.Run(scenario, func(t *testing.T) {
			s, rt, v, now := routeService(t)
			p, run, id := principal(), v.RunID, v.PreviewID
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wantErr, wantCalls := serviceapi.ErrPreviewUnavailable, 0
			switch scenario {
			case "unknown":
				id = strings.Repeat("0", 64)
				wantErr = serviceapi.ErrDependencyNotFound
			case "cross-run":
				run = "run-2"
				wantErr = serviceapi.ErrDependencyNotFound
			case "cross-owner":
				p.PrincipalID = "other-gateway"
				wantErr = serviceapi.ErrDependencyNotFound
			case "non-service":
				p.PrincipalType = serviceapi.PrincipalUser
				wantErr = serviceapi.ErrDependencyNotFound
			case "REQUESTED", "VALIDATING", "STARTING":
				v.Status, v.Health, v.RouteHandle = scenario, "UNKNOWN", ""
				s.store.records[id] = v
			case "FAILED", "STOPPED", "EXPIRED":
				s.transition(id, scenario, "UNKNOWN", "")
			case "expired-now":
				*now = parseTime(v.ExpiresAt)
			case "expired-during-lookup":
				rt.duringLookup = func() { *now = parseTime(v.ExpiresAt) }
				wantCalls = 1
			case "stale-handle":
				rt.handle = strings.Repeat("e", 64)
				wantCalls = 1
			case "missing-handle":
				v.RouteHandle = ""
				s.store.records[id] = v
			case "unavailable-runtime":
				rt.available = false
			case "runtime-error":
				rt.err = errors.New("/private/container")
				wantCalls = 1
			case "empty-target":
				rt.target = ""
				wantCalls = 1
			case "non-loopback":
				rt.target = "http://10.88.0.2:8080"
				wantCalls = 1
			case "dns-target":
				rt.target = "http://localhost:23456"
				wantCalls = 1
			case "closed":
				s.Close()
			case "unavailable-service":
				s.unavailable = true
			case "broken-store":
				s.store.broken = true
			case "canceled":
				cancel()
			case "service-canceled":
				s.cancel()
			case "missing-profile":
				delete(s.profiles, v.ProfileID)
			case "changed-profile":
				p := s.profiles[v.ProfileID]
				p.TTLSeconds++
				s.profiles[v.ProfileID] = p
			}
			got, err := s.ResolvePreviewRoute(ctx, p, run, id)
			if !errors.Is(err, wantErr) || got != (serviceapi.PreviewRouteV1{}) || rt.calls != wantCalls {
				t.Fatalf("got %+v, %v, calls %d; want %v, calls %d", got, err, rt.calls, wantErr, wantCalls)
			}
			if strings.HasPrefix(scenario, "expired-") {
				current := s.store.records[id]
				if current.Status != "EXPIRED" || current.RouteHandle != "" {
					t.Fatal("expired route retained", current)
				}
			}
		})
	}
}

func TestReadyPreviewResolvesOwnedDockerPresentation(t *testing.T) {
	s, _, v, _ := routeService(t)
	f := newDockerFixture(t, testProfile())
	f.id = v.PreviewID
	f.d.approved[f.p.Digest()] = true
	handle, err := f.d.Start(context.Background(), f.id, filepath.Join(f.d.root, "sources", f.id), f.p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.d.Stop(context.Background(), f.id); err != nil {
			t.Error(err)
		}
	})
	g := f.d.groups[f.id]
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "exact preview") }))
	defer target.Close()
	connectPresentation(t, g, target.URL)
	s.runtime = f.d
	if !s.transition(v.PreviewID, "READY", "HEALTHY", handle) {
		t.Fatal("current route transition")
	}
	got, err := s.ResolvePreviewRoute(context.Background(), principal(), v.RunID, v.PreviewID)
	if err != nil || got.TargetURL != g.url || got.RouteHandle != handle || got.ExpiresAt != v.ExpiresAt || got.RunID != v.RunID || got.PreviewID != v.PreviewID || !strings.HasPrefix(got.TargetURL, "http://127.0.0.1:") || strings.Contains(got.TargetURL, g.endpoint) {
		t.Fatal("presentation mismatch", got, err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Get(got.TargetURL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(body) != "exact preview" {
		t.Fatal("wrong presentation", string(body), err)
	}
	stop := serviceapi.PreviewStopRequestV1{SchemaVersion: 1, RequestID: "route-stop", ExpectedRunID: v.RunID, ExpectedPreviewID: v.PreviewID, DelegatedActor: createCommand("unused").DelegatedActor}
	if _, err := s.StopPreview(context.Background(), principal(), testAuthorityDigest, v.RunID, v.PreviewID, stop); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ResolvePreviewRoute(context.Background(), principal(), v.RunID, v.PreviewID); err == nil || got.TargetURL != "" {
		t.Fatal("stopped route resolved", got, err)
	}
}

func TestDockerRouteLookupFailsClosed(t *testing.T) {
	for _, scenario := range []string{"unknown", "other-preview", "stale-handle", "empty-handle", "canceled", "stopped", "unroutable", "closed-listener", "non-loopback"} {
		t.Run(scenario, func(t *testing.T) {
			f := newDockerFixture(t, testProfile())
			f.d.approved[f.p.Digest()] = true
			handle, err := f.d.Start(context.Background(), f.id, filepath.Join(f.d.root, "sources", f.id), f.p)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := f.d.Stop(context.Background(), f.id); err != nil {
					t.Error(err)
				}
			})
			g := f.d.groups[f.id]
			id := f.id
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "unknown":
				id = strings.Repeat("0", 64)
			case "other-preview":
				id = strings.Repeat("1", 64)
				f.d.groups[id] = &presentation{handle: strings.Repeat("2", 64), url: "http://127.0.0.1:12345", server: &http.Server{}, transport: &http.Transport{}}
			case "stale-handle":
				handle = strings.Repeat("e", 64)
			case "empty-handle":
				handle = ""
			case "canceled":
				cancel()
			case "stopped":
				if err := f.d.Stop(ctx, id); err != nil {
					t.Fatal(err)
				}
			case "unroutable":
				g.url = ""
			case "closed-listener":
				if err := g.server.Close(); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(time.Second)
				for !g.closed.Load() && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if !g.closed.Load() {
					t.Fatal("listener did not close")
				}
			case "non-loopback":
				g.url = "http://10.88.0.2:8080"
			}
			if target, err := f.d.ResolveRoute(ctx, id, handle); !errors.Is(err, ErrUnavailable) || target != "" {
				t.Fatal("unsafe route resolved", target, err)
			}
		})
	}
}

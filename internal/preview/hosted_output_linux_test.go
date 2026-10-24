//go:build linux

package preview

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/appwatch"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func hostedOutputFixture(t *testing.T) (*dockerFixture, PreviewProfileV1, string, string) {
	t.Helper()
	p := testProfile()
	p.ProfileID, p.Hosted = "node-hosted-v1", true
	f := newDockerFixture(t, p)
	f.d.approved[p.Digest()] = true
	setup := filepath.Join(t.TempDir(), "setup.env")
	if err := os.WriteFile(setup, []byte("BOOTSTRAP_TOKEN="+strings.Repeat("0a", 16)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return f, p, filepath.Join(f.d.root, "sources", f.id), setup
}

// BF-07c2: a hosted app's own server runs attached, the same exec less --detach, so its error lines reach the key's
// output; an end of its own is heard with its exit code, and a stop is not taken for one.
func TestAHostedAppsOwnServerRunsAttachedSoItsErrorsAreKept(t *testing.T) {
	f, p, path, setup := hostedOutputFixture(t)
	f.attachOutput = "listening on 8080\nError: connect ECONNREFUSED for ops@example.com\n"
	output := newAppOutput(time.Now)
	if _, err := f.d.StartHosted(context.Background(), f.id, path, p, "app-1", hostedKeyDigest("app-1"), "", setup, output); err != nil {
		t.Fatal(err)
	}
	for _, args := range f.commands {
		if args[0] == "exec" && args[len(args)-1] == "/source/server.js" {
			t.Fatal("the app's server started detached as well", args)
		}
	}
	eventually(t, "the app's server was not started attached", func() bool { f.attachMu.Lock(); defer f.attachMu.Unlock(); return len(f.attached) > 0 })
	f.attachMu.Lock()
	attached := f.attached
	f.attachMu.Unlock()
	if len(attached) != 1 || attached[0][0] != "exec" || slices.Contains(attached[0], "--detach") || attached[0][len(attached[0])-1] != "/source/server.js" || !slices.Contains(attached[0], f.d.container(f.id, 0)) || !slices.Contains(attached[0], setup) {
		t.Fatal("attached start", attached)
	}
	eventually(t, "the error line was not kept", func() bool { return len(output.read().Lines) == 1 })
	if line := output.read().Lines[0]; !strings.Contains(line, "ECONNREFUSED") || strings.Contains(line, "ops@example.com") {
		t.Fatal("kept line", line)
	}
	f.attachEnd <- 1
	eventually(t, "the end was not heard", func() bool { v := output.read(); return v.ExitCode != nil && *v.ExitCode == 1 })
	if err := f.d.Stop(context.Background(), f.id); err != nil {
		t.Fatal(err)
	}

	stopped, p, path, setup := hostedOutputFixture(t)
	quiet := newAppOutput(time.Now)
	if _, err := stopped.d.StartHosted(context.Background(), stopped.id, path, p, "app-1", hostedKeyDigest("app-1"), "", setup, quiet); err != nil {
		t.Fatal(err)
	}
	if err := stopped.d.Stop(context.Background(), stopped.id); err != nil {
		t.Fatal(err)
	}
	stopped.attachMu.Lock()
	stops := stopped.attachStops
	stopped.attachMu.Unlock()
	if stops != 1 || quiet.read().ExitCode != nil {
		t.Fatal("a stop ended the attached server", stops, "or was taken for an end of its own", quiet.read().ExitCode)
	}

	preview, p, path, _ := hostedOutputFixture(t)
	p.Hosted = false
	preview.d.approved[p.Digest()] = true
	if _, err := preview.d.Start(context.Background(), preview.id, path, p); err != nil {
		t.Fatal(err)
	}
	if len(preview.attached) != 0 {
		t.Fatal("a preview's server ran attached", preview.attached)
	}
	if err := preview.d.Stop(context.Background(), preview.id); err != nil {
		t.Fatal(err)
	}
}

// BF-07c2: a start that fails after the app's server began ends it, so nothing runs on unseen.
func TestAFailedHostedStartEndsItsAttachedServer(t *testing.T) {
	f, p, path, setup := hostedOutputFixture(t)
	f.reject = func(_ context.Context, args []string) error {
		if args[0] == "inspect" {
			return ErrUnavailable
		}
		return nil
	}
	output := newAppOutput(time.Now)
	if _, err := f.d.StartHosted(context.Background(), f.id, path, p, "app-1", hostedKeyDigest("app-1"), "", setup, output); err == nil {
		t.Fatal("a start whose server could not be inspected succeeded")
	}
	f.attachMu.Lock()
	defer f.attachMu.Unlock()
	if len(f.attached) != 1 || f.attachStops != 1 {
		t.Fatal("attached", f.attached, "stops", f.attachStops)
	}
	if output.read().ExitCode != nil {
		t.Fatal("a failed start was taken for an end of its own")
	}
}

// BF-07c2: the key's errors come from the output the runtime was given, last across generations, count the restarts,
// and are read only by the key's principal once the key exists.
func TestAHostedKeysErrorsAreReadFromItsOutputAcrossGenerations(t *testing.T) {
	h, rt, _, clock, _ := newHostedFixture(t)
	ctx := context.Background()
	if _, err := h.HostedErrors(ctx, principal(), "app-1"); !errors.Is(err, serviceapi.ErrDependencyNotFound) {
		t.Fatal("errors of a key that does not exist", err)
	}
	if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "app-1", startCommand("start-1")); err != nil {
		t.Fatal(err)
	}
	awaitHosted(t, h, "app-1", "READY")
	rt.mu.Lock()
	first := rt.outputs[0]
	rt.mu.Unlock()
	if first == nil {
		t.Fatal("the runtime was not given the key's output")
	}
	first.Write([]byte("Error: one\n"))
	v, err := h.HostedErrors(ctx, principal(), "app-1")
	if err != nil || serviceapi.ValidateHostedErrorsV1(v, "app-1") != nil || !v.Running || v.Restarts != 0 || len(v.Lines) != 1 || v.Since != "2026-09-30T12:00:00Z" {
		t.Fatalf("errors = %+v, %v", v, err)
	}
	// The app stops answering; the supervisor starts the next generation with the same output.
	rt.mu.Lock()
	rt.healthy = false
	rt.mu.Unlock()
	eventually(t, "not degraded", func() bool { v, _ := h.ReadHosted(ctx, principal(), "app-1"); return v.Health == "DEGRADED" })
	clock.Add(3 * time.Minute)
	eventually(t, "no second generation", func() bool { rt.mu.Lock(); defer rt.mu.Unlock(); return len(rt.outputs) == 2 })
	rt.mu.Lock()
	second := rt.outputs[1]
	rt.healthy = true
	rt.mu.Unlock()
	if second != first {
		t.Fatal("a new generation got a new output")
	}
	second.Ended(1)
	v, err = h.HostedErrors(ctx, principal(), "app-1")
	if err != nil || v.Restarts != 1 || len(v.Lines) != 1 || v.ExitCode == nil || *v.ExitCode != 1 {
		t.Fatalf("errors after a restart = %+v, %v", v, err)
	}
	other := serviceapi.Principal{PrincipalID: "other", PrincipalType: serviceapi.PrincipalService, AuthnMethod: "bearer-token-v1"}
	if _, err := h.HostedErrors(ctx, other, "app-1"); !errors.Is(err, serviceapi.ErrDependencyNotFound) {
		t.Fatal("another principal read the errors", err)
	}
	if _, err := h.StopHosted(ctx, principal(), testAuthorityDigest, "app-1", stopCommand("stop-1")); err != nil {
		t.Fatal(err)
	}
	if v, err := h.HostedErrors(ctx, principal(), "app-1"); err != nil || v.Running || len(v.Lines) != 1 {
		t.Fatalf("errors after a stop = %+v, %v", v, err)
	}
}

// BF-07e: the health sweep sees every key its owner keeps running, healthy only while ready and healthy, and never a
// stopped key.
func TestTheHealthSweepSeesEveryRunningKeyAndNoStoppedOne(t *testing.T) {
	h, rt, _, _, _ := newHostedFixture(t)
	ctx := context.Background()
	if got, ok := h.HealthObservations(ctx); !ok || len(got) != 0 {
		t.Fatal("no keys", got, ok)
	}
	if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "app-1", startCommand("start-1")); err != nil {
		t.Fatal(err)
	}
	awaitHosted(t, h, "app-1", "READY")
	got, ok := h.HealthObservations(ctx)
	if !ok || len(got) != 1 || got[0].App.Kind != appwatch.KindHosted || got[0].App.HostingKey != "app-1" || got[0].State != appwatch.Healthy {
		t.Fatal("ready", got)
	}
	rt.mu.Lock()
	rt.healthy = false
	rt.mu.Unlock()
	eventually(t, "not degraded", func() bool { v, _ := h.ReadHosted(ctx, principal(), "app-1"); return v.Health == "DEGRADED" })
	if got, _ := h.HealthObservations(ctx); len(got) != 1 || got[0].State != appwatch.Unwell {
		t.Fatal("degraded", got)
	}
	if _, err := h.StopHosted(ctx, principal(), testAuthorityDigest, "app-1", stopCommand("stop-1")); err != nil {
		t.Fatal(err)
	}
	if got, ok := h.HealthObservations(ctx); !ok || len(got) != 0 {
		t.Fatal("stopped", got)
	}
	h.Close()
	if _, ok := h.HealthObservations(ctx); ok {
		t.Fatal("a closed hosting answered a sweep")
	}
}

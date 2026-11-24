package preview

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// hostedLines keeps what hosting logs, for the self-heal tests (Repo C's 2026-11-24 outage: a host out of memory for
// five minutes left every hosted app FAILED and hosting unavailable until the controller was restarted).
type hostedLines struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *hostedLines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *hostedLines) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func awaitLine(t *testing.T, l *hostedLines, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(l.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("no log line with %q in:\n%s", want, l.String())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func unavailableNow(h *Hosting) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.unavailable
}

// A clean-up the host refused (a stall) makes hosting unavailable; once the host answers again, heal repeats the
// clean-up, hosting is available again by itself, and the key that is to run starts again: no controller restart.
func TestHostedUnavailableAfterAStallHealsByItself(t *testing.T) {
	h, rt, m, _, _ := newHostedFixture(t)
	lines := &hostedLines{}
	h.SetLog(lines)
	hostedUnhealthyRestart = 0
	ctx := context.Background()
	if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "app-1", startCommand("start-1")); err != nil {
		t.Fatal(err)
	}
	awaitHosted(t, h, "app-1", "READY")
	stall := errors.New("host stalled")
	rt.set(func(r *hostedMemory) { r.healthy, r.stopErr = false, stall })
	awaitHosted(t, h, "app-1", "FAILED")
	awaitLine(t, lines, "unavailable=true reason=cleanup_failed")
	awaitLine(t, lines, "status=FAILED restarts_in_last_hour=3")
	if !unavailableNow(h) {
		t.Fatal("hosting stayed available while clean-ups failed")
	}
	if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "app-2", startCommand("start-2")); err == nil {
		t.Fatal("a new start was accepted while hosting was unavailable")
	}
	rt.set(func(r *hostedMemory) { r.healthy, r.stopErr = true, nil })
	v := awaitHosted(t, h, "app-1", "READY")
	if v.Desired != "RUNNING" || v.Generation <= 1+hostedRestartLimit {
		t.Fatalf("after healing = %+v", v)
	}
	if unavailableNow(h) {
		t.Fatal("hosting is still unavailable")
	}
	awaitLine(t, lines, "unavailable=false")
	awaitLine(t, lines, "restart attempt reason=available_again")
	m.mu.Lock()
	removed := len(m.removed)
	m.mu.Unlock()
	if removed == 0 {
		t.Fatal("the failed clean-ups were never repeated")
	}
	h.mu.Lock()
	left := len(h.pendingCleanup)
	h.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d clean-ups still pending", left)
	}
	if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "app-2", startCommand("start-3")); err != nil {
		t.Fatalf("a new start after healing = %v", err)
	}
	awaitHosted(t, h, "app-2", "READY")
}

// A key that failed is tried again by itself, with pauses, and runs again once its app is healthy again.
func TestHostedFailedKeyIsTriedAgainByItself(t *testing.T) {
	h, rt, _, _, _ := newHostedFixture(t)
	lines := &hostedLines{}
	h.SetLog(lines)
	hostedUnhealthyRestart, hostedFailedRetryFirst, hostedFailedRetryMost = 0, 5*time.Millisecond, 20*time.Millisecond
	ctx := context.Background()
	if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "app-1", startCommand("start-1")); err != nil {
		t.Fatal(err)
	}
	awaitHosted(t, h, "app-1", "READY")
	rt.set(func(r *hostedMemory) { r.healthy = false })
	awaitHosted(t, h, "app-1", "FAILED")
	rt.set(func(r *hostedMemory) { r.healthy = true })
	v := awaitHosted(t, h, "app-1", "READY")
	if v.Desired != "RUNNING" || v.Generation <= 1+hostedRestartLimit {
		t.Fatalf("tried again = %+v", v)
	}
	awaitLine(t, lines, "restart attempt reason=retry_after_failed try=1")
	awaitLine(t, lines, "recovered")
	awaitLine(t, lines, "status=READY health=HEALTHY")
	if unavailableNow(h) {
		t.Fatal("hosting went unavailable")
	}
}

// After its window, a FAILED key stays FAILED until it is started again, as it always did.
func TestHostedFailedKeyStopsTryingAfterItsWindow(t *testing.T) {
	h, rt, _, clock, _ := newHostedFixture(t)
	lines := &hostedLines{}
	h.SetLog(lines)
	hostedUnhealthyRestart, hostedFailedRetryFirst, hostedFailedRetryMost = 0, 20*time.Millisecond, 20*time.Millisecond
	ctx := context.Background()
	if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "app-1", startCommand("start-1")); err != nil {
		t.Fatal(err)
	}
	awaitHosted(t, h, "app-1", "READY")
	rt.set(func(r *hostedMemory) { r.healthy = false })
	awaitHosted(t, h, "app-1", "FAILED")
	clock.Add(hostedFailedRetryWindow + time.Minute)
	awaitLine(t, lines, "stopped trying by itself")
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		alive := supervising(h.keys[hostedKeyDigest("app-1")])
		h.mu.Unlock()
		if !alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the key's supervisor is still trying after its window")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if v := awaitHosted(t, h, "app-1", "FAILED"); v.Desired != "RUNNING" {
		t.Fatalf("after its window = %+v", v)
	}
	rt.set(func(r *hostedMemory) { r.healthy = true })
	if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "app-1", startCommand("start-2")); err != nil {
		t.Fatalf("start after FAILED = %v", err)
	}
	awaitHosted(t, h, "app-1", "READY")
}

// The preview service's own unavailability at the start stays until a restart: heal leaves it alone.
func TestHostedInheritedUnavailabilityStaysUntilARestart(t *testing.T) {
	h, _, _, _, _ := newHostedFixture(t)
	h.mu.Lock()
	h.unavailable, h.inherited = true, true
	h.mu.Unlock()
	if h.tryHeal(context.Background()) {
		t.Fatal("heal cleared the preview service's own unavailability")
	}
	if !unavailableNow(h) {
		t.Fatal("hosting became available")
	}
}

// The log names a key by its digest's first characters, never its hosting key, settings or data.
func TestHostedLogLinesNameNoKeyOrSetting(t *testing.T) {
	h, rt, _, _, _ := newHostedFixture(t)
	lines := &hostedLines{}
	h.SetLog(lines)
	hostedUnhealthyRestart = 0
	ctx := context.Background()
	if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "customer-app-key", startCommand("start-1")); err != nil {
		t.Fatal(err)
	}
	awaitHosted(t, h, "customer-app-key", "READY")
	rt.set(func(r *hostedMemory) { r.healthy = false })
	awaitHosted(t, h, "customer-app-key", "FAILED")
	out := lines.String()
	if strings.Contains(out, "customer-app-key") {
		t.Fatalf("a log line names the hosting key:\n%s", out)
	}
	if !strings.Contains(out, "key="+hostedKeyDigest("customer-app-key")[:12]) {
		t.Fatalf("no line names the key's digest:\n%s", out)
	}
	for _, want := range []string{"status=READY health=HEALTHY", "ended reason=unhealthy_too_long", "restart attempt=1 reason=generation_ended", "status=FAILED"} {
		if !strings.Contains(out, want) {
			t.Fatalf("no %q in:\n%s", want, out)
		}
	}
}

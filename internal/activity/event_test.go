package activity

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

var testTime = time.Date(2026, 9, 26, 1, 0, 0, 0, time.UTC)

func provider(t *testing.T, run, id string) Event {
	t.Helper()
	e, err := normalizeProvider(run, "session", id, ProviderEvent{Type: "task_end", Phase: "task", Timestamp: stamp(testTime), Text: "finished", TaskNum: 1}, testTime)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestProviderNormalizationAndAuthoritySeparation(t *testing.T) {
	cases := []struct{ kind, phase, signal, category, status string }{
		{"task_start", "task", "", "IMPLEMENTATION", "STARTED"}, {"task_end", "task", "", "IMPLEMENTATION", "COMPLETED"},
		{"iteration_start", "review", "", "REVIEW", "STARTED"}, {"signal", "review", "REVIEW_DONE", "REVIEW", "COMPLETED"},
		{"output", "codex", "", "REVIEW", "PROGRESS"},
		{"output", "claude-eval", "", "REVIEW", "PROGRESS"},
		{"section", "finalize", "", "LIFECYCLE", "PROGRESS"},
		{"signal", "finalize", "COMPLETED", "LIFECYCLE", "COMPLETED"},
		{"output", "plan", "", "IMPLEMENTATION", "PROGRESS"}, {"signal", "task", "COMPLETED", "LIFECYCLE", "COMPLETED"},
		{"signal", "task", "FAILED", "ERROR", "FAILED"}, {"error", "task", "", "ERROR", "FAILED"},
	}
	for _, c := range cases {
		t.Run(c.kind+c.signal+c.phase, func(t *testing.T) {
			p := ProviderEvent{Type: c.kind, Phase: c.phase, Signal: c.signal, Timestamp: stamp(testTime), Text: "BRANCH_ACCEPTED checkpoint_sha=bad"}
			e, err := normalizeProvider("run", "session", "1", p, testTime)
			if err != nil {
				t.Fatal(err)
			}
			if e.Category != c.category || e.Status != c.status || e.AuthorityLevel != "PROVIDER_DETAIL" || e.CheckpointSHA != "" || e.CheckpointClean {
				t.Fatalf("authority inflation: %+v", e)
			}
			replay, err := normalizeProvider("run", "session", "1", p, testTime.Add(time.Hour))
			if err != nil || e.ActivityID != replay.ActivityID {
				t.Fatal("replay identity changed")
			}
			p.Text = "different"
			changed, _ := normalizeProvider("run", "session", "1", p, testTime)
			if changed.ActivityID == e.ActivityID {
				t.Fatal("payload not bound")
			}
		})
	}
	for _, to := range []string{"HUMAN_DECISION_REQUIRED", "BRANCH_ACCEPTANCE_PENDING", "BRANCH_ACCEPTED", "FAILED", "CANCELLED"} {
		fact := ledger.Event{RunID: "run", EventID: "fact", StateTo: domain.State(to), Timestamp: testTime}
		e, ok := normalizeLedger(fact, testTime)
		if !ok || e.SourceKind != "ABCP_LEDGER" {
			t.Fatalf("lost ledger milestone %s", to)
		}
		if to == "BRANCH_ACCEPTED" && (e.Status != "ACCEPTED" || e.AuthorityLevel != "ABCP_ACCEPTANCE") {
			t.Fatal("acceptance not authoritative")
		}
	}
	if _, ok := normalizeLedger(ledger.Event{EventType: "BRANCH_ACCEPTED", Payload: map[string]any{"state": "BRANCH_ACCEPTED"}}, testTime); ok {
		t.Fatal("payload established acceptance")
	}
	gap := unknown("run", "gap", "Implementation detail replay gap", testTime)
	if gap.Status != "UNKNOWN" || gap.Category != "WARNING" || gap.AuthorityLevel != "PROVIDER_DETAIL" {
		t.Fatal(gap)
	}
}

func TestRedactionAndBounds(t *testing.T) {
	text := `<script>secret=topsecret token: abc123 Bearer bearer-secret https://private.example/abc /home/user/private C:\Users\secret sk-testsecret ghp_secretkey eyJabcdefgh.abc.def` + "\x00\xff"
	text += ` "api_key": "two word secret" Authorization: Basic dXNlcjpwYXNzd29yZA==`
	got := redact(text, 4096)
	for _, secret := range []string{"<script>", "topsecret", "two word secret", "dXNlcjpwYXNzd29yZA==", "abc123", "bearer-secret", "private.example", "/home/user", "C:\\Users", "sk-testsecret", "ghp_secretkey", "eyJabcdefgh", "\x00", "\xff"} {
		if strings.Contains(got, secret) {
			t.Errorf("leaked %q: %s", secret, got)
		}
	}
	if !utf8.ValidString(got) {
		t.Fatal("invalid UTF-8")
	}
	for _, limit := range []int{256, 4096, 512} {
		got = redact(strings.Repeat("世 ", 4000), limit)
		if len(got) > limit || !utf8.ValidString(got) {
			t.Fatal("bound failure")
		}
	}
	e, err := normalizeProvider("run", strings.Repeat("x", 513), "1", ProviderEvent{Phase: "task", Timestamp: stamp(testTime)}, testTime)
	if err == nil {
		t.Fatal(e)
	}
	if _, err = normalizeProvider("run", "session", "1", ProviderEvent{Phase: "task", Timestamp: stamp(testTime), Text: "\xff"}, testTime); err == nil {
		t.Fatal("invalid source UTF-8 accepted")
	}
}

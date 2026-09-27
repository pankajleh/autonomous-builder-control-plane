package activity

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestDiagnosticStepsPreserveFailClosedClasses(t *testing.T) {
	git := atStep("git-deadline", ErrUnavailable)
	worktree := annotate("worktree", annotate("head", git, ErrIntegrity), ErrUnavailable)
	batch := annotate("revalidate-binding", worktree, ErrIntegrity)

	if !errors.Is(worktree, ErrUnavailable) || errors.Is(worktree, ErrIntegrity) {
		t.Fatal("binding resolution changed its unavailable class", worktree)
	}
	if !errors.Is(batch, ErrIntegrity) || errors.Is(batch, ErrUnavailable) {
		t.Fatal("batch revalidation changed its integrity class", batch)
	}
	if got := diagnosticStep(batch); got != "revalidate-binding/worktree/head/git-deadline" {
		t.Fatalf("diagnostic step = %q", got)
	}
	if diagnosticStep(ErrIntegrity) != "" || atStep("unused", nil) != nil {
		t.Fatal("unannotated errors gained diagnostic steps")
	}
	for err, want := range map[error]string{
		nil:                             "none",
		batch:                           "integrity",
		git:                             "unavailable",
		atStep("store", ErrExhausted):   "exhausted",
		context.Canceled:                "interrupted",
		errors.New("unclassified test"): "other",
	} {
		if got := diagnosticClass(err); got != want {
			t.Fatalf("class(%v) = %q, want %q", err, got, want)
		}
	}
}

func TestDiagnoseEmitsOnlyControllerTokens(t *testing.T) {
	var out bytes.Buffer
	s := &Service{now: func() time.Time { return testTime }, diagnostics: &out}
	s.diagnose("admission-test", "provider-integrity", annotate("revalidate-binding", atStep("worktree", ErrUnavailable), ErrIntegrity))
	want := "abcp activity marker at=" + stamp(testTime) + " run=admission-test marker=provider-integrity step=revalidate-binding/worktree class=integrity\n"
	if out.String() != want {
		t.Fatalf("diagnostic line = %q, want %q", out.String(), want)
	}

	out.Reset()
	s.diagnose("admission-test\nforged token=secret", "provider-replay-gap", nil)
	want = "abcp activity marker at=" + stamp(testTime) + " run=invalid marker=provider-replay-gap step=unspecified class=none\n"
	if out.String() != want {
		t.Fatalf("unsafe run identifier reached diagnostics: %q", out.String())
	}

	// Services constructed without a diagnostic sink stay silent.
	(&Service{now: time.Now}).diagnose("admission-test", "provider-integrity", ErrIntegrity)
}

package prlifecycle

import (
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
)

func TestPhysicalResourceKeyIgnoresRevisionIdentity(t *testing.T) {
	repository, _ := githublifecycle.NewRepository("Octo", "Control")
	base, _ := githublifecycle.NewBranch("main")
	head, _ := githublifecycle.NewBranch("feature/topic")
	first, err := NewPRResourceKey(repository, base, head)
	if err != nil || !first.valid() {
		t.Fatalf("resource key: %v", err)
	}
	second, _ := NewPRResourceKey(repository, base, head)
	if first != second {
		t.Fatal("same physical resource produced different keys")
	}
	otherHead, _ := githublifecycle.NewBranch("feature/other")
	third, _ := NewPRResourceKey(repository, base, otherHead)
	if first == third {
		t.Fatal("distinct head resource produced same key")
	}
}

func TestTrackActorRequiresCanonicalStableNumericUserID(t *testing.T) {
	for _, subject := range []string{"github-user-id:0", "github-user-id:01", "github-user-id:+1", "login:octocat", "github-user-id:9223372036854775808"} {
		actor, _ := githublifecycle.NewUserIdentity(subject)
		if _, err := trackActorID(actor); err == nil {
			t.Fatalf("accepted unsafe subject %q", subject)
		}
	}
	actor, _ := githublifecycle.NewUserIdentity("github-user-id:42")
	if id, err := trackActorID(actor); err != nil || id != 42 {
		t.Fatalf("canonical actor rejected: id=%d err=%v", id, err)
	}
	app, _ := githublifecycle.NewAppInstallationIdentity("github-user-id:42", 9)
	if _, err := trackActorID(app); err == nil {
		t.Fatal("app installation accepted by user-only track")
	}
}

func TestTerminalBudgetAndProductionLimitsAreFixed(t *testing.T) {
	budget, err := NewTerminalBudget([]byte(`{"authority":1}`), []byte(`{"attempt":1}`), "title", "")
	if err != nil || budget.WorstCaseBytes > MaxTerminalBytes {
		t.Fatalf("valid terminal budget rejected: %#v %v", budget, err)
	}
	if _, err := NewTerminalBudget(make([]byte, (32<<10)+1), []byte("x"), "title", ""); err == nil {
		t.Fatal("oversized authority accepted")
	}
	loose := githublifecycle.DefaultLimits()
	loose.MaxTextBytes++
	if limitsAllowed(loose) {
		t.Fatal("looser-than-production profile accepted")
	}
	strict := githublifecycle.DefaultLimits()
	strict.MaxTextBytes--
	if !limitsAllowed(strict) {
		t.Fatal("component-wise stricter test profile rejected")
	}
	if err := validateDocument("title", "line\nline", 4096); err == nil || !strings.Contains(err.Error(), "single-line") {
		t.Fatal("control-bearing PR body accepted")
	}
}

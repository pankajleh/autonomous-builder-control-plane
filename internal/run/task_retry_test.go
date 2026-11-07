package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runfailure"
)

// A Ralphex stand-in that keeps Ralphex's rule for a builder that signals FAILED (pkg/processor/phase/task.go): it is
// sent back while fewer than task_retry_count retries were used (read from the isolated configuration, else 1), and the
// task attempt fails after that. Here the check fails once, the builder gives up three times, and its fourth session
// repairs the check.
const retryingRalphex = `#!/bin/sh
retries=1
if [ -f "$2/config" ]; then
  configured=$(sed -n 's/^task_retry_count = //p' "$2/config")
  [ -n "$configured" ] && retries=$configured
fi
printf '[26-11-07 10:00:00] validation started: unit\n'
printf '[26-11-07 10:00:05] deterministic validation failed; returning evidence to model for one repair turn: validation unit exit 1: not ok 1 - checkout total\n'
gave_up=0
while [ $gave_up -lt 3 ]; do
  printf '<<<RALPHEX:TASK_FAILED>>>\n'
  if [ $gave_up -ge $retries ]; then
    printf 'error: runner: task phase: task execution failed after retry (FAILED signal received)\n' >&2
    exit 1
  fi
  printf '[26-11-07 10:01:00] task failed, retrying...\n'
  gave_up=$((gave_up+1))
done
printf '[26-11-07 10:05:00] validation started: unit\n[26-11-07 10:05:05] validation completed successfully: unit (5s)\n'
printf 'candidate\n' > candidate.txt
git add candidate.txt || exit 90
git commit -qm 'candidate implementation' || exit 91
`

func TestTheProfilesTaskRetryCountSendsTheBuilderBackBeforeTheAttemptFails(t *testing.T) {
	t.Run("Ralphex's own default: one retry", func(t *testing.T) {
		fixture := newRunFixtureWithScript(t, retryingRalphex, authority.WorktreePolicy{}, commandPath(t, "true"))
		result := fixture.execute(t)
		if result.State != domain.StateFailed {
			t.Fatalf("state = %s", result.State)
		}
		events := readEvents(t, fixture.ledgerPath)
		detail := recordedFailure(t, events[len(events)-1])
		if detail.Cause != runfailure.CauseValidationFailed || detail.Check != "unit" || detail.RepairTurns != 1 || detail.TaskRetries != 1 || detail.TaskRetryLimit != 1 {
			t.Fatalf("detail = %+v", detail)
		}
		for _, event := range events {
			if _, ok := event.Payload["task_retry_count"]; ok {
				t.Fatalf("a run without the field recorded one: %+v", event.Payload)
			}
		}
	})
	t.Run("three retries", func(t *testing.T) {
		fixture := newRunFixtureEdited(t, retryingRalphex, authority.WorktreePolicy{}, func(manifest *authority.Manifest) {
			manifest.Ralphex.TaskRetryCount = 3
		}, commandPath(t, "true"))
		result := fixture.execute(t)
		if !result.Accepted() {
			t.Fatalf("state = %s, %q", result.State, result.FailureReason)
		}
		events := readEvents(t, fixture.ledgerPath)
		found := false
		for _, event := range events {
			if event.StateTo == domain.StateExecutionStarting {
				found = event.Payload["task_retry_count"] == float64(3)
			}
		}
		if !found {
			t.Fatal("the start evidence does not record the task retry count")
		}
	})
	t.Run("two retries are not enough here, and the detail says so", func(t *testing.T) {
		fixture := newRunFixtureEdited(t, retryingRalphex, authority.WorktreePolicy{}, func(manifest *authority.Manifest) {
			manifest.Ralphex.TaskRetryCount = 2
		}, commandPath(t, "true"))
		if result := fixture.execute(t); result.State != domain.StateFailed {
			t.Fatalf("state = %s", result.State)
		}
		events := readEvents(t, fixture.ledgerPath)
		detail := recordedFailure(t, events[len(events)-1])
		if detail.TaskRetries != 2 || detail.TaskRetryLimit != 2 || detail.Cause != runfailure.CauseValidationFailed {
			t.Fatalf("detail = %+v", detail)
		}
	})
}

// The count reaches Ralphex as one line of its isolated configuration, beside the open-model engines' effort line;
// without it, the configuration folder holds no configuration file, as before.
func TestTheTaskRetryCountIsOneLineOfRalphexsIsolatedConfiguration(t *testing.T) {
	seen := filepath.Join(t.TempDir(), "config")
	script := `#!/bin/sh
if [ -f "$2/config" ]; then cp "$2/config" ` + seen + ` || exit 92; fi
printf 'candidate\n' > candidate.txt
git add candidate.txt || exit 90
git commit -qm 'candidate implementation' || exit 91
`
	plain := newRunFixtureWithScript(t, script, authority.WorktreePolicy{}, commandPath(t, "true"))
	if result := plain.execute(t); !result.Accepted() {
		t.Fatalf("state = %s", result.State)
	}
	if _, err := os.Stat(seen); !os.IsNotExist(err) {
		t.Fatalf("a run without the field wrote a configuration: %v", err)
	}
	configured := newRunFixtureEdited(t, script, authority.WorktreePolicy{}, func(manifest *authority.Manifest) {
		manifest.Ralphex.TaskRetryCount = 3
	}, commandPath(t, "true"))
	if result := configured.execute(t); !result.Accepted() {
		t.Fatalf("state = %s", result.State)
	}
	data, err := os.ReadFile(seen)
	if err != nil || string(data) != "task_retry_count = 3\n" {
		t.Fatalf("configuration = %q, %v", data, err)
	}
	if err := appendRalphexConfig(filepath.Dir(seen), "codex_reasoning_effort ="); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(seen); !strings.HasSuffix(string(data), "task_retry_count = 3\ncodex_reasoning_effort =\n") {
		t.Fatalf("appended configuration = %q", data)
	}
}

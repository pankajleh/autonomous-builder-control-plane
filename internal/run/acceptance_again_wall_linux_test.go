//go:build linux

package run

import (
	"context"
	"os"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
)

// R5 in a real wall: the walled run's branch comes back into the product repository, its final acceptance fails once
// and passes on a second fresh checkout of the same branch there, and the run is accepted as flaky.
func TestAWalledRunsFlakyAcceptanceIsRunAgainOnTheProductBranch(t *testing.T) {
	bwrap := requireBwrap(t)
	fixture, wall, _, _ := walledFixture(t, bwrap, func(wallDirs, string) string {
		return fakeWalledRalphex(`printf '[26-11-07 10:00:00] validation started: unit\n[26-11-07 10:00:01] validation completed successfully: unit (1s)\n'`)
	})
	check, counter := flakyAcceptance(t, "1")
	manifest := fixture.authority.Manifest()
	manifest.AcceptanceReruns = 1
	manifest.Ralphex.LongRunningSubprocessMode = "orchestrator"
	manifest.Ralphex.Validation = []authority.RalphexValidationCommand{{ID: "unit", Argv: []string{commandPath(t, "true")}, Timeout: "1m"}}
	manifest.Acceptance = []authority.AcceptanceCommand{{Name: "node-app-acceptance", Class: "unit", Required: true, Timeout: "5s", Argv: []string{check}}}
	fixture.authority = fixture.admit(t, manifest)
	runner := fixture.runner(t)
	if err := runner.SetBuildWall(wall); err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background())
	if err != nil || !result.Accepted() {
		stderr, _ := os.ReadFile(result.Ralphex.StderrRef.URI)
		t.Fatalf("walled run = %s %q, %v\nstderr:\n%s", result.State, result.FailureReason, err, stderr)
	}
	if got := starts(t, counter); got != "2" {
		t.Fatalf("the acceptance ran %s times, want 2", got)
	}
	accepted := lastEventTo(t, readEvents(t, fixture.ledgerPath), domain.StateBranchAccepted)
	if accepted.Payload["acceptance_flaky"] != "node-app-acceptance" {
		t.Fatalf("BRANCH_ACCEPTED = %v", accepted.Payload)
	}
}

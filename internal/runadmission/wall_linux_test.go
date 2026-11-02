//go:build linux

package runadmission

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	runctl "github.com/pankajleh/autonomous-builder-control-plane/internal/run"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// testWall is a wall file whose bubblewrap only answers its version; admission tests replace its probe (passWall).
func testWall(t *testing.T) (*runctl.Wall, string) {
	t.Helper()
	root := t.TempDir()
	bwrap := filepath.Join(root, "bwrap")
	if err := os.WriteFile(bwrap, []byte("#!/bin/sh\necho 'bubblewrap 0.9.0'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	reads := filepath.Join(root, "vendor", "versions")
	brands := filepath.Join(root, "vendor", "brands")
	for _, dir := range []string{home, reads, brands} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(root, "build-wall.json")
	writeProtectedJSON(t, file, runctl.BuildWallFileV1{
		SchemaVersion: 1, Bwrap: bwrap, BuildsRoot: filepath.Join(root, "builds"), Home: home,
		System: []string{"/usr"}, ReadRoots: []runctl.ReadRootV1{{Path: reads}, {Path: brands, Shared: true}},
	})
	wall, err := runctl.LoadBuildWall(file)
	if err != nil {
		t.Fatal(err)
	}
	return wall, reads
}

// passWall makes the controller's wall probe pass (or answer err) without starting bubblewrap.
func passWall(controller *Controller, err error) {
	controller.wallState.mu.Lock()
	controller.wallState.probe = func(context.Context) error { return err }
	controller.wallState.err, controller.wallState.at, controller.wallState.firstBuild = err, time.Time{}, true
	controller.wallState.mu.Unlock()
}

// withTestWall is a controller configuration with a passing test wall.
func withTestWall(t *testing.T, config Config) (*Controller, error) {
	t.Helper()
	wall, _ := testWall(t)
	config.Wall = wall
	controller, err := NewController(config)
	if err == nil {
		passWall(controller, nil)
	}
	return controller, err
}

func TestNoBuildIsAdmittedOrResumedWithoutAWorkingWall(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	passWall(fixture.controller, errors.New("bwrap: setting up uid map: Permission denied"))
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); !errors.Is(err, serviceapi.ErrBuildWallUnavailable) {
		t.Fatalf("admission with a failing wall = %v, want build_wall_unavailable", err)
	}
	if len(fixture.starts) != 0 {
		t.Fatalf("a run was launched without its wall: %v", fixture.starts)
	}
	if _, err := os.Stat(filepath.Join(fixture.input, DeriveRunID(fixture.principal.PrincipalID, fixture.request.RequestID), "manifest.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a manifest was written without a wall: %v", err)
	}
	engines, err := fixture.controller.Engines(context.Background())
	if err != nil || engines.BuildIsolation != "" || len(engines.Engines) == 0 {
		t.Fatalf("engines without a wall = %+v, %v", engines, err)
	}
	for _, engine := range engines.Engines {
		if engine.Ready || engine.Reason != "build_wall_unavailable" {
			t.Fatalf("engine %s without a wall = ready %t reason %q", engine.ProfileID, engine.Ready, engine.Reason)
		}
	}
	if fixture.controller.BuildIsolation() != "" {
		t.Fatal("capabilities say builds are walled while the wall fails")
	}
	if err := fixture.controller.ResumeRun(context.Background(), DeriveRunID(fixture.principal.PrincipalID, "unknown")); err == nil {
		t.Fatal("a resume was accepted")
	}

	// A controller with no wall file at all refuses the same way.
	bare := &Controller{admissionsFD: fixture.controller.admissionsFD, launchesFD: fixture.controller.launchesFD, start: fixture.controller.start,
		profiles: fixture.controller.profiles, catalog: fixture.catalog, clock: time.Now}
	if _, err := bare.AdmitRun(context.Background(), fixture.principal, fixture.request); !errors.Is(err, serviceapi.ErrBuildWallUnavailable) {
		t.Fatalf("admission without a wall file = %v", err)
	}

	// Once the wall starts again, the build is admitted, its manifest names its run folder, and its run is told the wall file.
	passWall(fixture.controller, nil)
	response, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	manifest := readRunManifest(t, fixture, response.RunID)
	if manifest.Isolation == nil || manifest.Isolation.RunFolder != fixture.controller.wall.RunFolder(response.RunID) || len(manifest.Isolation.ReadPaths) != 0 {
		t.Fatalf("manifest isolation = %+v", manifest.Isolation)
	}
	if !containsArg(fixture.starts[0], "--build-wall-file") || !containsArg(fixture.starts[0], fixture.controller.wall.Path()) ||
		!containsArg(fixture.starts[0], "--build-wall-sha256") || !containsArg(fixture.starts[0], fixture.controller.wall.SHA256()) {
		t.Fatalf("launch argv lacks the wall file and its probed digest: %v", fixture.starts[0])
	}
	engines, _ = fixture.controller.Engines(context.Background())
	if engines.BuildIsolation != runctl.BuildIsolationKindV1 || fixture.controller.BuildIsolation() != runctl.BuildIsolationKindV1 {
		t.Fatalf("engines and capabilities with a working wall = %q %q", engines.BuildIsolation, fixture.controller.BuildIsolation())
	}
}

func TestAWallFileChangedSinceItWasProbedHoldsEveryBuild(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	wall := fixture.controller.wall
	data, err := os.ReadFile(wall.Path())
	if err != nil {
		t.Fatal(err)
	}
	// The file each run loads is changed after abcp serve loaded and probed it.
	if err := os.WriteFile(wall.Path(), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); !errors.Is(err, serviceapi.ErrBuildWallUnavailable) || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("admission with a changed wall file = %v, want build_wall_unavailable", err)
	}
	if len(fixture.starts) != 0 {
		t.Fatalf("a run was launched with a wall file no probe saw: %v", fixture.starts)
	}
	if fixture.controller.BuildIsolation() != "" {
		t.Fatal("capabilities say builds are walled while the wall file is not the one probed")
	}
	// The probe itself refuses the changed file, so the answer holds until abcp serve is restarted.
	state := newWallState(wall, nil, time.Hour, time.Now)
	if err := state.check(context.Background(), true); !errors.Is(err, runctl.ErrBuildWallUnavailable) || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("probe of a changed wall file = %v", err)
	}
	// Put back, the next probe passes again.
	if err := os.WriteFile(wall.Path(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	passWall(fixture.controller, nil)
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); err != nil {
		t.Fatal(err)
	}
}

func TestTheFirstBuildAfterTheStartProbesTheWallAgain(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	probes := 0
	fixture.controller.wallState.mu.Lock()
	fixture.controller.wallState.probe = func(context.Context) error { probes++; return nil }
	fixture.controller.wallState.err, fixture.controller.wallState.at = nil, time.Now()
	fixture.controller.wallState.clock = time.Now
	fixture.controller.wallState.mu.Unlock()
	request := fixture.request
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request); err != nil {
		t.Fatal(err)
	}
	request.RequestID = "repo-c-second"
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request); err != nil {
		t.Fatal(err)
	}
	if probes != 1 {
		t.Fatalf("probes = %d, want one before the first build and none for the second", probes)
	}
}

func TestReadPathsAreRealFoldersUnderTheWallsRootsAndPinnedInTheManifest(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	wall, reads := testWall(t)
	fixture.controller.wall = wall
	// Repo C names every app by its canonical UUID.
	const app, other = "3b8f2c1e-5d4a-4e7b-9c2f-0a1b2c3d4e5f", "9d0e1f2a-3b4c-4d5e-8f60-718293a4b5c6"
	fixture.request.ProductTaskID = app
	version := filepath.Join(reads, app, "commit-1", "files")
	otherApp := filepath.Join(reads, other, "commit-3", "files")
	every := filepath.Join(reads, "tasks", "9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a")
	for _, dir := range []string{version, otherApp, every} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	link := filepath.Join(reads, "link")
	if err := os.Symlink(version, link); err != nil {
		t.Fatal(err)
	}
	for name, paths := range map[string][]string{
		"outside the roots": {outside},
		"a link":            {link},
		"missing":           {filepath.Join(reads, app, "nothing")},
		"the root itself":   {reads},
		// The attack (the request is app's): another app's folder, a folder holding every app's.
		"another apps folder":      {otherApp},
		"another apps whole":       {filepath.Join(reads, other)},
		"every apps tasks":         {filepath.Join(reads, "tasks")},
		"its own and another apps": {version, otherApp},
	} {
		request := fixture.request
		request.RequestID = "repo-c-" + strings.ReplaceAll(name, " ", "-")
		request.ReadPaths = paths
		if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request); !errors.Is(err, serviceapi.ErrReadPathNotAccepted) {
			t.Fatalf("read path %s = %v, want read_path_not_accepted", name, err)
		}
	}
	// A task ID that is no canonical UUID names no app's folders: "tasks" would make every app's (imports/tasks) one.
	for _, task := range []string{"tasks", strings.ToUpper(app)} {
		request := fixture.request
		request.RequestID = "repo-c-not-a-uuid-" + strings.ToLower(task[:5])
		request.ProductTaskID = task
		request.ReadPaths = []string{filepath.Join(reads, "tasks")}
		if task != "tasks" {
			request.ReadPaths = []string{filepath.Join(reads, app)}
		}
		if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request); !errors.Is(err, serviceapi.ErrReadPathNotAccepted) {
			t.Fatalf("read paths for the task %q = %v, want read_path_not_accepted", task, err)
		}
	}
	request := fixture.request
	request.ReadPaths = []string{version}
	response, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request)
	if err != nil {
		t.Fatal(err)
	}
	manifest := readRunManifest(t, fixture, response.RunID)
	if manifest.Isolation == nil || len(manifest.Isolation.ReadPaths) != 1 || manifest.Isolation.ReadPaths[0] != version || manifest.Isolation.ProductTaskID != app {
		t.Fatalf("manifest read paths = %+v", manifest.Isolation)
	}
	if RequestDigest(request) == RequestDigest(fixture.request) {
		t.Fatal("read paths are not part of the request's digest")
	}
	withoutPaths := fixture.request
	withoutPaths.ReadPaths = nil
	if RequestDigest(withoutPaths) != RequestDigest(fixture.request) {
		t.Fatal("a request without read paths changed its digest")
	}
	var _ = authority.IsolationManifest{}
}

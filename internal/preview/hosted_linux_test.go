//go:build linux

package preview

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

type hostedMemory struct {
	mu                  sync.Mutex
	available, healthy  bool
	startErr, healthErr error
	started, stopped    []string
	keys                []string
	dump                string
	loaded              []string
	removed             []string
}

func (r *hostedMemory) Available(PreviewProfileV1) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.available
}
func (r *hostedMemory) StartHosted(_ context.Context, id, _ string, p PreviewProfileV1, keyDigest string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !p.Hosted {
		return "", ErrUnavailable
	}
	r.started = append(r.started, id)
	r.keys = append(r.keys, keyDigest)
	return strings.Repeat("d", 64), r.startErr
}
func (r *hostedMemory) Healthy(context.Context, string, PreviewProfileV1) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.healthy, r.healthErr
}
func (r *hostedMemory) ResolveRoute(_ context.Context, _, handle string) (string, error) {
	if handle != strings.Repeat("d", 64) {
		return "", ErrUnavailable
	}
	return "http://127.0.0.1:4321", nil
}
func (r *hostedMemory) Stop(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = append(r.stopped, id)
	return nil
}
func (r *hostedMemory) Dump(_ context.Context, _ string, _ PreviewProfileV1, w io.Writer) error {
	r.mu.Lock()
	data := r.dump
	r.mu.Unlock()
	_, err := io.WriteString(w, data)
	return err
}
func (r *hostedMemory) Load(_ context.Context, _ string, _ PreviewProfileV1, rd io.Reader) error {
	data, err := io.ReadAll(rd)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loaded = append(r.loaded, string(data))
	return err
}
func (r *hostedMemory) RemoveVolume(_ context.Context, keyDigest string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removed = append(r.removed, keyDigest)
	return nil
}
func (r *hostedMemory) set(f func(*hostedMemory)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f(r)
}

func hostedProfile() PreviewProfileV1 {
	p := testProfile()
	p.ProfileID, p.Hosted = "node-pg-hosted-v1", true
	db := ServiceProfileV1{Name: "db", Image: "local/db@sha256:" + strings.Repeat("f", 64), User: "999:999", StartArgv: []string{"/usr/local/bin/autobuild-postgres"}, Port: 5432, DataVolume: true,
		BackupArgv: []string{"/usr/lib/postgresql/17/bin/pg_dump", "-Fc", "-h", "/scratch", "-U", "app", "app"}, RestoreArgv: []string{"/usr/lib/postgresql/17/bin/pg_restore", "--clean", "--if-exists", "-h", "/scratch", "-U", "app", "-d", "app"}}
	p.Services = append(p.Services, db)
	return p
}

type hostedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *hostedClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *hostedClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func fastHosted(t *testing.T) {
	t.Helper()
	saved := []time.Duration{hostedStartTimeout, hostedUnhealthyRestart, hostedHealthEvery, hostedRestartBackoff, hostedNightlyEvery}
	hostedStartTimeout, hostedUnhealthyRestart, hostedHealthEvery, hostedRestartBackoff, hostedNightlyEvery = 200*time.Millisecond, 2*time.Minute, 2*time.Millisecond, time.Millisecond, time.Hour
	t.Cleanup(func() {
		hostedStartTimeout, hostedUnhealthyRestart, hostedHealthEvery, hostedRestartBackoff, hostedNightlyEvery = saved[0], saved[1], saved[2], saved[3], saved[4]
	})
}

func hostingAt(t *testing.T, root string, rt *hostedMemory, clock *hostedClock) (*Hosting, *memoryCheckout) {
	t.Helper()
	web, hosted := testProfile(), hostedProfile()
	m := &memoryCheckout{}
	source := Source{RunID: "run-1", CheckpointActivityID: strings.Repeat("b", 64), SHA: strings.Repeat("c", 40), Repository: "/private/repo", RepositoryIdentityDigest: hosted.RepositoryIdentityDigest, ProductAuthorizationID: "auth-1", ProductTaskID: "task-1", ProductVersionID: "version-1", ValidationID: strings.Repeat("e", 64)}
	h, err := newHosting(context.Background(), root, map[string]PreviewProfileV1{web.ProfileID: web, hosted.ProfileID: hosted}, staticResolver{source: source}, m, rt, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h, m
}

func newHostedFixture(t *testing.T) (*Hosting, *hostedMemory, *memoryCheckout, *hostedClock, string) {
	t.Helper()
	fastHosted(t)
	root := filepath.Join(t.TempDir(), "hosted")
	rt := &hostedMemory{available: true, healthy: true, dump: "dump-1"}
	clock := &hostedClock{now: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)}
	h, m := hostingAt(t, root, rt, clock)
	return h, rt, m, clock, root
}

func startCommand(id string) serviceapi.HostedStartRequestV1 {
	return serviceapi.HostedStartRequestV1{SchemaVersion: 1, RequestID: id, RunID: "run-1", CheckpointActivityID: strings.Repeat("b", 64), ProfileID: "node-pg-hosted-v1", DelegatedActor: serviceapi.DelegatedActorV1{SubjectID: "alice", SubjectType: serviceapi.PrincipalUser}}
}
func stopCommand(id string) serviceapi.HostedCommandRequestV1 {
	return serviceapi.HostedCommandRequestV1{SchemaVersion: 1, RequestID: id, DelegatedActor: serviceapi.DelegatedActorV1{SubjectID: "alice", SubjectType: serviceapi.PrincipalUser}}
}
func backupCommand(id, reason string) serviceapi.HostedBackupRequestV1 {
	return serviceapi.HostedBackupRequestV1{SchemaVersion: 1, RequestID: id, Reason: reason, DelegatedActor: serviceapi.DelegatedActorV1{SubjectID: "alice", SubjectType: serviceapi.PrincipalUser}}
}

func awaitHosted(t *testing.T, h *Hosting, key, status string) serviceapi.HostedV1 {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		v, err := h.ReadHosted(context.Background(), principal(), key)
		if err != nil {
			t.Fatal(err)
		}
		if v.Status == status {
			return v
		}
		if time.Now().After(deadline) {
			t.Fatalf("hosted %s status %s generation %d; wanted %s", key, v.Status, v.Generation, status)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestHostedStartReadyRouteReplayAndStop(t *testing.T) {
	h, rt, m, _, _ := newHostedFixture(t)
	ctx := context.Background()
	v, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "app-1", startCommand("start-1"))
	if err != nil || v.Status != "STARTING" || v.Desired != "RUNNING" || v.Generation != 1 || v.Data != "PRESENT" || v.SourceSHA != strings.Repeat("c", 40) {
		t.Fatalf("start = %+v, %v", v, err)
	}
	ready := awaitHosted(t, h, "app-1", "READY")
	if ready.Health != "HEALTHY" || ready.RouteHandle != strings.Repeat("d", 64) {
		t.Fatalf("ready = %+v", ready)
	}
	if rt.keys[0] != hostedKeyDigest("app-1") {
		t.Fatal("the runtime did not get the key's digest for its data volume")
	}
	route, err := h.ResolveHostedRoute(ctx, principal(), "app-1")
	if err != nil || serviceapi.ValidateHostedRouteV1(route, "app-1") != nil || route.TargetURL != "http://127.0.0.1:4321" {
		t.Fatalf("route = %+v, %v", route, err)
	}
	if again, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "app-1", startCommand("start-1")); err != nil || again != v {
		t.Fatalf("replayed start = %+v, %v", again, err)
	}
	changed := startCommand("start-1")
	changed.ProfileID = "web-v1"
	if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "app-1", changed); !errors.Is(err, serviceapi.ErrRequestIDConflict) {
		t.Fatalf("reused request ID = %v", err)
	}
	if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "app-1", startCommand("start-2")); !errors.Is(err, serviceapi.ErrHostedConflict) {
		t.Fatalf("second start while running = %v", err)
	}
	stopped, err := h.StopHosted(ctx, principal(), testAuthorityDigest, "app-1", stopCommand("stop-1"))
	if err != nil || stopped.Status != "STOPPED" || stopped.Desired != "STOPPED" || stopped.RouteHandle != "" || stopped.Data != "PRESENT" {
		t.Fatalf("stop = %+v, %v", stopped, err)
	}
	instance := hostedInstance(hostedKeyDigest("app-1"), 1)
	if len(rt.stopped) != 1 || rt.stopped[0] != instance || len(m.removed) != 1 || m.removed[0] != instance {
		t.Fatalf("stop left runtime objects: stopped %v removed %v", rt.stopped, m.removed)
	}
	if _, err := h.ResolveHostedRoute(ctx, principal(), "app-1"); !errors.Is(err, serviceapi.ErrPreviewUnavailable) {
		t.Fatalf("route after stop = %v", err)
	}
	if again, err := h.StopHosted(ctx, principal(), testAuthorityDigest, "app-1", stopCommand("stop-1")); err != nil || again != stopped {
		t.Fatalf("replayed stop = %+v, %v", again, err)
	}
	restarted, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "app-1", startCommand("start-3"))
	if err != nil || restarted.Generation != 2 {
		t.Fatalf("start after stop = %+v, %v", restarted, err)
	}
}

func TestHostedKeysBelongToTheirPrincipal(t *testing.T) {
	h, _, _, _, _ := newHostedFixture(t)
	ctx := context.Background()
	if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "app-1", startCommand("start-1")); err != nil {
		t.Fatal(err)
	}
	other := serviceapi.Principal{PrincipalID: "other", PrincipalType: serviceapi.PrincipalService, AuthnMethod: "bearer-token-v1"}
	if _, err := h.ReadHosted(ctx, other, "app-1"); !errors.Is(err, serviceapi.ErrDependencyNotFound) {
		t.Fatalf("another principal read the key: %v", err)
	}
	if _, err := h.StopHosted(ctx, other, testAuthorityDigest, "app-1", stopCommand("stop-x")); !errors.Is(err, serviceapi.ErrDependencyNotFound) {
		t.Fatalf("another principal stopped the key: %v", err)
	}
	if _, err := h.ReadHosted(ctx, principal(), "missing"); !errors.Is(err, serviceapi.ErrDependencyNotFound) {
		t.Fatalf("missing key = %v", err)
	}
}

func TestHostedProfilesAreSeparateFromPreviewProfiles(t *testing.T) {
	h, _, _, _, _ := newHostedFixture(t)
	c := startCommand("start-1")
	c.ProfileID = "web-v1"
	if _, err := h.StartHosted(context.Background(), principal(), testAuthorityDigest, "app-1", c); !errors.Is(err, serviceapi.ErrPreviewProfile) {
		t.Fatalf("a preview profile was hosted: %v", err)
	}
	if _, err := h.ReadHosted(context.Background(), principal(), "app-1"); !errors.Is(err, serviceapi.ErrDependencyNotFound) {
		t.Fatalf("a refused start left a key: %v", err)
	}
	rt := &memoryRuntime{available: true, healthy: true}
	s, _ := fixtureService(t, rt)
	s.profiles["node-pg-hosted-v1"] = hostedProfile()
	preview := createCommand("preview-1")
	preview.ProfileID = "node-pg-hosted-v1"
	if _, err := s.CreatePreview(context.Background(), principal(), testAuthorityDigest, "run-1", preview); !errors.Is(err, serviceapi.ErrPreviewProfile) {
		t.Fatalf("a hosted profile was previewed: %v", err)
	}
}

func TestHostedCapacityCountsRunningKeys(t *testing.T) {
	h, _, _, _, _ := newHostedFixture(t)
	ctx := context.Background()
	for i := 0; i < MaxHostedRunning; i++ {
		key := "app-" + string(rune('a'+i))
		if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, key, startCommand("start-"+key)); err != nil {
			t.Fatal(key, err)
		}
	}
	if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "app-z", startCommand("start-z")); !errors.Is(err, serviceapi.ErrHostedCapacity) {
		t.Fatalf("start beyond capacity = %v", err)
	}
	if _, err := h.StopHosted(ctx, principal(), testAuthorityDigest, "app-a", stopCommand("stop-a")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "app-z", startCommand("start-z2")); err != nil {
		t.Fatalf("start after a stop freed capacity = %v", err)
	}
}

func TestHostedKeysRunAgainAfterAControllerRestart(t *testing.T) {
	h, rt, _, clock, root := newHostedFixture(t)
	ctx := context.Background()
	for _, key := range []string{"app-1", "app-2"} {
		if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, key, startCommand("start-"+key)); err != nil {
			t.Fatal(err)
		}
		awaitHosted(t, h, key, "READY")
	}
	if _, err := h.StopHosted(ctx, principal(), testAuthorityDigest, "app-2", stopCommand("stop-2")); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	stopped := len(rt.stopped)
	if stopped != 2 {
		t.Fatalf("closing stopped %d instances, want 2", stopped)
	}
	again, _ := hostingAt(t, root, rt, clock)
	v := awaitHosted(t, again, "app-1", "READY")
	if v.Generation != 2 || v.Desired != "RUNNING" {
		t.Fatalf("after restart = %+v", v)
	}
	if rt.started[len(rt.started)-1] != hostedInstance(hostedKeyDigest("app-1"), 2) || rt.keys[len(rt.keys)-1] != hostedKeyDigest("app-1") {
		t.Fatal("the restarted generation did not reuse the key's data volume")
	}
	if v, err := again.ReadHosted(ctx, principal(), "app-2"); err != nil || v.Status != "STOPPED" || v.Generation != 1 {
		t.Fatalf("a stopped key after restart = %+v, %v", v, err)
	}
	if _, err := again.StartHosted(ctx, principal(), testAuthorityDigest, "app-1", startCommand("start-app-1")); err != nil {
		t.Fatalf("a receipt did not survive the restart: %v", err)
	}
}

func TestHostedUnhealthyInstanceRestartsUntilItsLimit(t *testing.T) {
	h, rt, _, _, _ := newHostedFixture(t)
	hostedUnhealthyRestart = 0
	ctx := context.Background()
	if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "app-1", startCommand("start-1")); err != nil {
		t.Fatal(err)
	}
	awaitHosted(t, h, "app-1", "READY")
	rt.set(func(r *hostedMemory) { r.healthy = false })
	v := awaitHosted(t, h, "app-1", "FAILED")
	if v.Generation != 1+hostedRestartLimit || v.Desired != "RUNNING" || v.RouteHandle != "" {
		t.Fatalf("failed = %+v", v)
	}
	rt.set(func(r *hostedMemory) { r.healthy = true })
	if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "app-1", startCommand("start-2")); err != nil {
		t.Fatalf("start after FAILED = %v", err)
	}
	if v := awaitHosted(t, h, "app-1", "READY"); v.Generation != 2+hostedRestartLimit {
		t.Fatalf("started again = %+v", v)
	}
}

func TestHostedBackupRestoreAndPurge(t *testing.T) {
	h, rt, _, _, root := newHostedFixture(t)
	ctx := context.Background()
	if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "app-1", startCommand("start-1")); err != nil {
		t.Fatal(err)
	}
	awaitHosted(t, h, "app-1", "READY")
	b, err := h.BackupHosted(ctx, principal(), testAuthorityDigest, "app-1", backupCommand("backup-1", "pre-update"))
	if err != nil || serviceapi.ValidateHostedBackupV1(b, "app-1") != nil || b.Reason != "pre-update" || b.Bytes != int64(len("dump-1")) || b.Generation != 1 {
		t.Fatalf("backup = %+v, %v", b, err)
	}
	file := filepath.Join(root, hostedKeyDigest("app-1"), "backups", b.BackupID+".dump")
	if data, err := os.ReadFile(file); err != nil || string(data) != "dump-1" {
		t.Fatalf("backup file = %q, %v", data, err)
	}
	if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("backup file mode = %v, %v", info, err)
	}
	if again, err := h.BackupHosted(ctx, principal(), testAuthorityDigest, "app-1", backupCommand("backup-1", "pre-update")); err != nil || again != b {
		t.Fatalf("replayed backup = %+v, %v", again, err)
	}
	list, err := h.ListHostedBackups(ctx, principal(), "app-1")
	if err != nil || len(list.Backups) != 1 || list.Backups[0] != b {
		t.Fatalf("backups = %+v, %v", list, err)
	}
	if v, _ := h.ReadHosted(ctx, principal(), "app-1"); v.LastBackup == nil || *v.LastBackup != b {
		t.Fatalf("last backup = %+v", v.LastBackup)
	}
	restore := serviceapi.HostedRestoreRequestV1{SchemaVersion: 1, RequestID: "restore-1", BackupID: b.BackupID, DelegatedActor: serviceapi.DelegatedActorV1{SubjectID: "alice", SubjectType: serviceapi.PrincipalUser}}
	if _, err := h.RestoreHosted(ctx, principal(), testAuthorityDigest, "app-1", restore); err != nil || len(rt.loaded) != 1 || rt.loaded[0] != "dump-1" {
		t.Fatalf("restore = %v, loaded %v", err, rt.loaded)
	}
	unknown := restore
	unknown.RequestID, unknown.BackupID = "restore-2", strings.Repeat("9", 64)
	if _, err := h.RestoreHosted(ctx, principal(), testAuthorityDigest, "app-1", unknown); !errors.Is(err, serviceapi.ErrDependencyNotFound) {
		t.Fatalf("restore of an unknown backup = %v", err)
	}
	if _, err := h.PurgeHosted(ctx, principal(), testAuthorityDigest, "app-1", stopCommand("purge-1")); !errors.Is(err, serviceapi.ErrHostedConflict) {
		t.Fatalf("purge while running = %v", err)
	}
	if _, err := h.StopHosted(ctx, principal(), testAuthorityDigest, "app-1", stopCommand("stop-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.BackupHosted(ctx, principal(), testAuthorityDigest, "app-1", backupCommand("backup-2", "manual")); !errors.Is(err, serviceapi.ErrHostedConflict) {
		t.Fatalf("backup while stopped = %v", err)
	}
	purged, err := h.PurgeHosted(ctx, principal(), testAuthorityDigest, "app-1", stopCommand("purge-2"))
	if err != nil || purged.Data != "PURGED" || purged.LastBackup != nil || len(rt.removed) != 1 || rt.removed[0] != hostedKeyDigest("app-1") {
		t.Fatalf("purge = %+v, %v, removed %v", purged, err, rt.removed)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("purge kept the backup file: %v", err)
	}
	if v, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "app-1", startCommand("start-2")); err != nil || v.Data != "PRESENT" {
		t.Fatalf("start after purge = %+v, %v", v, err)
	}
}

func TestHostedNightlyBackupsArePrunedAfterSevenDays(t *testing.T) {
	h, rt, _, clock, root := newHostedFixture(t)
	ctx := context.Background()
	if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "app-1", startCommand("start-1")); err != nil {
		t.Fatal(err)
	}
	awaitHosted(t, h, "app-1", "READY")
	manual, err := h.BackupHosted(ctx, principal(), testAuthorityDigest, "app-1", backupCommand("backup-1", "manual"))
	if err != nil {
		t.Fatal(err)
	}
	k := h.keys[hostedKeyDigest("app-1")]
	h.nightlyBackup(k)
	if list, _ := h.ListHostedBackups(ctx, principal(), "app-1"); len(list.Backups) != 1 {
		t.Fatalf("a nightly backup was taken before a day passed: %+v", list)
	}
	clock.Add(25 * time.Hour)
	rt.set(func(r *hostedMemory) { r.dump = "dump-2" })
	h.nightlyBackup(k)
	list, _ := h.ListHostedBackups(ctx, principal(), "app-1")
	if len(list.Backups) != 2 || list.Backups[1].Reason != "nightly" || list.Backups[1].Bytes != int64(len("dump-2")) {
		t.Fatalf("nightly = %+v", list)
	}
	clock.Add(7 * 24 * time.Hour)
	h.nightlyBackup(k)
	list, _ = h.ListHostedBackups(ctx, principal(), "app-1")
	if len(list.Backups) != 1 || list.Backups[0].Reason != "nightly" || list.Backups[0].BackupID == manual.BackupID {
		t.Fatalf("after 8 days = %+v", list)
	}
	entries, err := os.ReadDir(filepath.Join(root, hostedKeyDigest("app-1"), "backups"))
	if err != nil || len(entries) != 1 || entries[0].Name() != list.Backups[0].BackupID+".dump" {
		t.Fatalf("backup files = %v, %v", entries, err)
	}
}

func TestHostedStateFilesArePrivateAndRefuseTampering(t *testing.T) {
	h, rt, _, clock, root := newHostedFixture(t)
	if _, err := h.StartHosted(context.Background(), principal(), testAuthorityDigest, "app-1", startCommand("start-1")); err != nil {
		t.Fatal(err)
	}
	awaitHosted(t, h, "app-1", "READY")
	h.Close()
	path := filepath.Join(root, hostedKeyDigest("app-1"), "state.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("state file mode = %v, %v", info, err)
	}
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, bytes.Replace(data, []byte(`"Key":"app-1"`), []byte(`"Key":"app-2"`), 1), 0600); err != nil {
		t.Fatal(err)
	}
	web, hosted := testProfile(), hostedProfile()
	if _, err := newHosting(context.Background(), root, map[string]PreviewProfileV1{web.ProfileID: web, hosted.ProfileID: hosted}, staticResolver{}, &memoryCheckout{}, rt, clock.Now); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("a state file under another key's digest was loaded: %v", err)
	}
}

func TestHostedDirectoryWithoutStateIsNoKey(t *testing.T) {
	fastHosted(t)
	root := filepath.Join(t.TempDir(), "hosted")
	if err := os.MkdirAll(filepath.Join(root, hostedKeyDigest("app-1")), 0700); err != nil {
		t.Fatal(err)
	}
	h, _ := hostingAt(t, root, &hostedMemory{available: true, healthy: true}, &hostedClock{now: time.Now()})
	if _, err := h.ReadHosted(context.Background(), principal(), "app-1"); !errors.Is(err, serviceapi.ErrDependencyNotFound) {
		t.Fatalf("a directory without state = %v", err)
	}
	if _, err := h.StartHosted(context.Background(), principal(), testAuthorityDigest, "app-1", startCommand("start-1")); err != nil {
		t.Fatalf("start over an empty directory = %v", err)
	}
}

func TestHostedProfileValidation(t *testing.T) {
	if err := hostedProfile().Validate(); err != nil {
		t.Fatal("hosted profile", err)
	}
	cases := map[string]func(*PreviewProfileV1){
		"volume-not-hosted":   func(p *PreviewProfileV1) { p.Hosted = false },
		"two-volumes":         func(p *PreviewProfileV1) { p.Services[0].DataVolume = true },
		"backup-alone":        func(p *PreviewProfileV1) { p.Services[1].RestoreArgv = nil },
		"backup-without-data": func(p *PreviewProfileV1) { p.Services[1].DataVolume = false },
		"backup-shell":        func(p *PreviewProfileV1) { p.Services[1].BackupArgv = []string{"/bin/sh", "-c", "pg_dump"} },
		"restore-busybox":     func(p *PreviewProfileV1) { p.Services[1].RestoreArgv = []string{"/bin/busybox", "tee"} },
		"restore-injection":   func(p *PreviewProfileV1) { p.Services[1].RestoreArgv[1] = "$(id)" },
	}
	for name, mutate := range cases {
		p := cloneProfile(hostedProfile())
		mutate(&p)
		if p.Validate() == nil {
			t.Error(name, "was accepted")
		}
	}
	plain := testProfile()
	before := plain.Digest()
	plain.Hosted = false
	if plain.Digest() != before {
		t.Fatal("the new optional fields changed an existing profile's digest")
	}
}

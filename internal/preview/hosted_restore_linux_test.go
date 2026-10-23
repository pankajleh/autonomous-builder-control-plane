package preview

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// Repo C BF-10c (design note BF10_GOING_BACK_SAFELY.md §9): a restore backs up the data as it is now first, and a
// backup's file is handed out as it is.

func restoreCommand(id, backupID string) serviceapi.HostedRestoreRequestV1 {
	return serviceapi.HostedRestoreRequestV1{SchemaVersion: 1, RequestID: id, BackupID: backupID, DelegatedActor: serviceapi.DelegatedActorV1{SubjectID: "alice", SubjectType: serviceapi.PrincipalUser}}
}

func readyWithBackup(t *testing.T) (*Hosting, *hostedMemory, serviceapi.HostedBackupV1) {
	t.Helper()
	h, rt, _, _, _ := newHostedFixture(t)
	if _, err := h.StartHosted(context.Background(), principal(), testAuthorityDigest, "app-1", startCommand("start-1")); err != nil {
		t.Fatal(err)
	}
	awaitHosted(t, h, "app-1", "READY")
	b, err := h.BackupHosted(context.Background(), principal(), testAuthorityDigest, "app-1", backupCommand("backup-1", "manual"))
	if err != nil {
		t.Fatal(err)
	}
	return h, rt, b
}

func readBackup(t *testing.T, h *Hosting, backupID string) string {
	t.Helper()
	f, size, err := h.OpenHostedBackup(context.Background(), principal(), "app-1", backupID)
	if err != nil {
		t.Fatalf("open %s: %v", backupID, err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil || int64(len(data)) != size {
		t.Fatalf("read %d of %d bytes: %v", len(data), size, err)
	}
	return string(data)
}

func TestARestoreBacksUpTheDataAsItIsNowFirst(t *testing.T) {
	h, rt, b := readyWithBackup(t)
	ctx := context.Background()
	rt.set(func(r *hostedMemory) { r.dump = "data-now" })
	v, err := h.RestoreHosted(ctx, principal(), testAuthorityDigest, "app-1", restoreCommand("restore-1", b.BackupID))
	if err != nil || len(rt.loaded) != 1 || rt.loaded[0] != "dump-1" {
		t.Fatalf("restore = %v, loaded %v", err, rt.loaded)
	}
	list, _ := h.ListHostedBackups(ctx, principal(), "app-1")
	if len(list.Backups) != 2 || list.Backups[0] != b {
		t.Fatalf("backups = %+v", list)
	}
	before := list.Backups[1]
	if serviceapi.ValidateHostedBackupV1(before, "app-1") != nil || before.Reason != "pre-restore" || before.Bytes != int64(len("data-now")) || v.LastBackup == nil || *v.LastBackup != before {
		t.Fatalf("backup before the restore = %+v, last %+v", before, v.LastBackup)
	}
	if got := readBackup(t, h, before.BackupID); got != "data-now" {
		t.Fatalf("the backup before the restore holds %q", got)
	}
	if again, err := h.RestoreHosted(ctx, principal(), testAuthorityDigest, "app-1", restoreCommand("restore-1", b.BackupID)); err != nil || again.UpdatedAt != v.UpdatedAt || again.LastBackup == nil || *again.LastBackup != before || len(rt.loaded) != 1 {
		t.Fatalf("a replayed restore ran again: %v, loaded %v", err, rt.loaded)
	}
	if list, _ := h.ListHostedBackups(ctx, principal(), "app-1"); len(list.Backups) != 2 {
		t.Fatalf("a replayed restore backed up again: %+v", list)
	}
	if _, err := h.RestoreHosted(ctx, principal(), testAuthorityDigest, "app-1", restoreCommand("restore-2", before.BackupID)); err != nil || rt.loaded[1] != "data-now" {
		t.Fatalf("restoring the backup before the restore = %v, loaded %v", err, rt.loaded)
	}
}

func TestWithoutABackupFirstNothingIsRestored(t *testing.T) {
	h, rt, b := readyWithBackup(t)
	rt.set(func(r *hostedMemory) { r.dump = "" })
	if _, err := h.RestoreHosted(context.Background(), principal(), testAuthorityDigest, "app-1", restoreCommand("restore-1", b.BackupID)); !errors.Is(err, serviceapi.ErrPreviewUnavailable) || len(rt.loaded) != 0 {
		t.Fatalf("restore without a backup first = %v, loaded %v", err, rt.loaded)
	}
	if list, _ := h.ListHostedBackups(context.Background(), principal(), "app-1"); len(list.Backups) != 1 {
		t.Fatalf("backups = %+v", list)
	}
}

func TestTheBackupFirstIsKeptWhenTheRestoreFails(t *testing.T) {
	h, rt, b := readyWithBackup(t)
	rt.set(func(r *hostedMemory) { r.dump = "data-now"; r.loadErr = errors.New("load failed") })
	if _, err := h.RestoreHosted(context.Background(), principal(), testAuthorityDigest, "app-1", restoreCommand("restore-1", b.BackupID)); !errors.Is(err, serviceapi.ErrPreviewUnavailable) {
		t.Fatalf("failed restore = %v", err)
	}
	list, _ := h.ListHostedBackups(context.Background(), principal(), "app-1")
	if len(list.Backups) != 2 || list.Backups[1].Reason != "pre-restore" || readBackup(t, h, list.Backups[1].BackupID) != "data-now" {
		t.Fatalf("the backup before a failed restore was lost: %+v", list)
	}
}

func TestABackupFileIsHandedOutAsItIsAndOnlyForItsKey(t *testing.T) {
	h, _, b := readyWithBackup(t)
	ctx := context.Background()
	if got := readBackup(t, h, b.BackupID); got != "dump-1" {
		t.Fatalf("backup file = %q", got)
	}
	for _, tc := range []struct {
		name, key, id string
		p             serviceapi.Principal
	}{
		{"unknown backup", "app-1", strings.Repeat("9", 64), principal()},
		{"another key", "app-2", b.BackupID, principal()},
		{"another principal", "app-1", b.BackupID, serviceapi.Principal{PrincipalID: "other", PrincipalType: serviceapi.PrincipalService, AuthnMethod: "bearer-token-v1"}},
	} {
		if f, _, err := h.OpenHostedBackup(ctx, tc.p, tc.key, tc.id); !errors.Is(err, serviceapi.ErrDependencyNotFound) || f != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
}

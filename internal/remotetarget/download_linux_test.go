package remotetarget

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// A backup is read as it is on the server, with the size kept for it, and only
// a listed one whose file still has that size (BF-10c2).
func TestABackupIsReadAsItIsOnTheServer(t *testing.T) {
	f := newDeployFixture(t)
	f.deploy(f.putNode("one"), "crm", testHost, "req-crm-first")
	if _, err := f.m.BackUp(context.Background(), f.key, "crm", serviceapi.BackupCreateV1{RequestID: "req-backup-1"}); err != nil {
		t.Fatal(err)
	}
	f.m.Wait()
	backups, err := f.m.Backups(context.Background(), f.key, "crm")
	if err != nil || len(backups.Backups) != 1 {
		t.Fatalf("backups = %+v %v", backups, err)
	}
	b := backups.Backups[0]
	file := filepath.Join(f.remote.home, "autobuild/backups/crm", b.BackupID+".dump")
	want, err := os.ReadFile(file)
	if err != nil || int64(len(want)) != b.Bytes {
		t.Fatalf("the backup file = %d bytes %v, kept %d", len(want), err, b.Bytes)
	}
	body, size, err := f.m.OpenBackup(context.Background(), f.key, "crm", b.BackupID)
	if err != nil || size != b.Bytes {
		t.Fatalf("open = %d %v", size, err)
	}
	got, err := io.ReadAll(body)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("read %q %v, want %q", got, err, want)
	}
	if err := body.Close(); err != nil || body.Close() != nil {
		t.Fatal("closing twice", err)
	}

	// Closing before the end ends the read.
	if err := os.WriteFile(file, bytes.Repeat([]byte("x"), int(b.Bytes)), 0o600); err != nil {
		t.Fatal(err)
	}
	body, _, err = f.m.OpenBackup(context.Background(), f.key, "crm", b.BackupID)
	if err != nil {
		t.Fatal(err)
	}
	one := make([]byte, 1)
	if _, err := io.ReadFull(body, one); err != nil || one[0] != 'x' {
		t.Fatalf("first byte %q %v", one, err)
	}
	_ = body.Close()

	for _, c := range []struct {
		site, backup string
		want         error
	}{
		{"crm", "bk-20200101T000000Z-0000", serviceapi.ErrBackupUnknown},
		{"crm", "../../etc/passwd", serviceapi.ErrBackupUnknown},
		{"../crm", b.BackupID, serviceapi.ErrBackupUnknown},
		{"blog", b.BackupID, serviceapi.ErrSiteUnknown},
	} {
		if _, _, err := f.m.OpenBackup(context.Background(), f.key, c.site, c.backup); !errors.Is(err, c.want) {
			t.Fatalf("%s/%s = %v, want %v", c.site, c.backup, err, c.want)
		}
	}
	if _, _, err := f.m.OpenBackup(context.Background(), "srv-ffffffffffff", "crm", b.BackupID); !errors.Is(err, serviceapi.ErrTargetUnknown) {
		t.Fatalf("an unknown target = %v", err)
	}

	// A file whose size is not the one kept, or that is gone, is not handed out.
	if err := os.WriteFile(file, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.m.OpenBackup(context.Background(), f.key, "crm", b.BackupID); !errors.Is(err, serviceapi.ErrBackupUnknown) {
		t.Fatalf("a changed file = %v", err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.m.OpenBackup(context.Background(), f.key, "crm", b.BackupID); !errors.Is(err, serviceapi.ErrBackupUnknown) {
		t.Fatalf("a missing file = %v", err)
	}

	// A target not confirmed is not signed in to.
	r, _ := f.m.read(f.key)
	r.State = statePending
	if err := f.m.write(r); err != nil {
		t.Fatal(err)
	}
	before := len(f.remote.scripts)
	if _, _, err := f.m.OpenBackup(context.Background(), f.key, "crm", b.BackupID); !errors.Is(err, serviceapi.ErrTargetNotReady) || len(f.remote.scripts) != before {
		t.Fatalf("an unconfirmed target = %v", err)
	}
	for _, s := range f.remote.scripts {
		if strings.Contains(s, "exec cat") && !strings.Contains(s, "/autobuild/backups/crm/"+b.BackupID+".dump") {
			t.Fatalf("read another file: %s", s)
		}
	}
}

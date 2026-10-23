package remotetarget

import (
	"context"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// OpenBackup opens one listed backup of a Node app as it is on its target, for
// the app's owner to download (BF-10c2, Repo C design note
// BF10_GOING_BACK_SAFELY.md §9). The size kept for it is checked on the server
// first; then the file is read as it comes, never held whole or parsed.
// Closing it ends the read and the session. It runs beside operations, since
// it changes nothing.
func (m *Manager) OpenBackup(ctx context.Context, key, site, backup string) (io.ReadCloser, int64, error) {
	if !siteNamePattern.MatchString(site) || !backupIDPattern.MatchString(backup) {
		return nil, 0, serviceapi.ErrBackupUnknown
	}
	r, size, err := m.listedBackup(key, site, backup)
	if err != nil {
		return nil, 0, err
	}
	session, outcome := m.open(ctx, r)
	if session == nil {
		return nil, 0, errStep(outcome)
	}
	vars := []string{"N", site, "ID", backup}
	out, err := run(ctx, session, fill(backupSizeScript, vars...), nil)
	if err != nil {
		_ = session.Close()
		return nil, 0, err
	}
	if strings.TrimSpace(out) != strconv.FormatInt(size, 10) {
		_ = session.Close()
		return nil, 0, serviceapi.ErrBackupUnknown
	}
	streamCtx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = pw.CloseWithError(session.Stream(streamCtx, fill(backupReadScript, vars...), pw))
	}()
	return &backupReader{PipeReader: pr, end: func() {
		cancel()
		_ = pr.Close()
		<-done
		_ = session.Close()
	}}, size, nil
}

// listedBackup is the target and the size kept for a backup it lists.
func (m *Manager) listedBackup(key, site, backup string) (record, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read(key)
	if err != nil {
		return record{}, 0, err
	}
	d, ok := findDatabase(r, site)
	if !ok {
		return record{}, 0, serviceapi.ErrSiteUnknown
	}
	var size int64
	for _, b := range d.Backups {
		if b.BackupID == backup {
			size = b.Bytes
		}
	}
	if size <= 0 {
		return record{}, 0, serviceapi.ErrBackupUnknown
	}
	if r.State != stateConfirmed {
		return record{}, 0, serviceapi.ErrTargetNotReady
	}
	return r, size, nil
}

// backupReader is a backup read as it comes; closing it, once, ends the read.
type backupReader struct {
	*io.PipeReader
	end  func()
	once sync.Once
}

func (b *backupReader) Close() error {
	b.once.Do(b.end)
	return nil
}

const (
	// backupSizeScript prints a backup's size on the server, or missing.
	backupSizeScript = `set -eu
f="$HOME/autobuild/backups/@N@/@ID@.dump"
if [ -f "$f" ]; then wc -c < "$f"; else echo missing; fi
`
	// backupReadScript writes a backup's file to its output as it is.
	backupReadScript = `set -eu
exec cat "$HOME/autobuild/backups/@N@/@ID@.dump"
`
)

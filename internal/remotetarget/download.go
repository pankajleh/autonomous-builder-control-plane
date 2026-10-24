package remotetarget

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/pgcsv"
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
	r, b, err := m.listedBackup(key, site, backup)
	if err != nil {
		return nil, 0, err
	}
	size := b.Bytes
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

// OpenSpreadsheets opens one listed backup of a Node app as spreadsheets (BF-06b, Repo C design note
// BF06_YOUR_APPS_DATA.md §4.2): a zip with one CSV file per table. pg_restore, in the server's database container, writes
// the backup's data as text, read as it comes; pgcsv turns it into the zip. No database is restored and nothing changes on
// the server. Closing it ends the read and the session; a read that ends in an error is an unfinished zip.
func (m *Manager) OpenSpreadsheets(ctx context.Context, key, site, backup string) (io.ReadCloser, error) {
	if !siteNamePattern.MatchString(site) || !backupIDPattern.MatchString(backup) {
		return nil, serviceapi.ErrBackupUnknown
	}
	r, b, err := m.listedBackup(key, site, backup)
	if err != nil {
		return nil, err
	}
	taken, err := time.Parse(time.RFC3339, b.TakenAt)
	if err != nil {
		return nil, serviceapi.ErrBackupUnknown
	}
	session, outcome := m.open(ctx, r)
	if session == nil {
		return nil, errStep(outcome)
	}
	vars := []string{"N", site, "ID", backup}
	out, err := run(ctx, session, fill(backupSizeScript, vars...), nil)
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	if strings.TrimSpace(out) != strconv.FormatInt(b.Bytes, 10) {
		_ = session.Close()
		return nil, serviceapi.ErrBackupUnknown
	}
	streamCtx, cancel := context.WithCancel(ctx)
	data, dataW := io.Pipe()
	zr, zw := io.Pipe()
	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		_ = dataW.CloseWithError(session.Stream(streamCtx, fill(backupDataScript, vars...), dataW))
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		_, err := pgcsv.Convert(data, zw, taken)
		_ = data.CloseWithError(errors.New("spreadsheets ended"))
		_ = zw.CloseWithError(err)
	}()
	return &backupReader{PipeReader: zr, end: func() {
		cancel()
		_ = zr.Close()
		_ = data.Close()
		<-done
		<-done
		_ = session.Close()
	}}, nil
}

// listedBackup is the target and a backup it lists, with the size kept for it.
func (m *Manager) listedBackup(key, site, backup string) (record, backupRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read(key)
	if err != nil {
		return record{}, backupRecord{}, err
	}
	d, ok := findDatabase(r, site)
	if !ok {
		return record{}, backupRecord{}, serviceapi.ErrSiteUnknown
	}
	b, ok := findBackup(d, backup)
	if !ok || b.Bytes <= 0 {
		return record{}, backupRecord{}, serviceapi.ErrBackupUnknown
	}
	if r.State != stateConfirmed {
		return record{}, backupRecord{}, serviceapi.ErrTargetNotReady
	}
	return r, b, nil
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
	// backupDataScript writes a backup's data as text, with the database container's own pg_restore and no database.
	backupDataScript = `set -eu
` + dockerHost + `
exec docker exec -i autobuild-postgres /usr/lib/postgresql/17/bin/pg_restore --data-only --no-owner --no-privileges -f - < "$HOME/autobuild/backups/@N@/@ID@.dump"
`
)

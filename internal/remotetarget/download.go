package remotetarget

import (
	"archive/tar"
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
// it changes nothing. A WordPress website's backup is two files, read as one
// tar (openWebsiteBackup).
func (m *Manager) OpenBackup(ctx context.Context, key, site, backup string) (io.ReadCloser, int64, error) {
	if !siteNamePattern.MatchString(site) || !backupIDPattern.MatchString(backup) {
		return nil, 0, serviceapi.ErrBackupUnknown
	}
	r, b, err := m.listedBackup(key, site, backup)
	if err != nil {
		return nil, 0, err
	}
	if wpData(r, site) {
		return m.openWebsiteBackup(ctx, r, site, b)
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
	if wpData(r, site) {
		// A WordPress website's dump is MariaDB's SQL, which pgcsv does not read.
		return nil, serviceapi.ErrTargetInvalid
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

// The files in a WordPress website's backup as it is downloaded.
const (
	websiteDumpName    = "database.sql"
	websiteUploadsName = "uploads.tar"
)

// websiteBackupSize is the size of a WordPress website's backup as one tar:
// a ustar header and the file in whole blocks for each of the two files, and
// the two empty blocks that end it.
func websiteBackupSize(dump, uploads int64) int64 {
	blocks := func(n int64) int64 { return 512 + (n+511)/512*512 }
	return blocks(dump) + blocks(uploads) + 1024
}

// openWebsiteBackup opens one listed backup of a WordPress website (Repo C
// design note WORDPRESS_WEBSITES.md §13.1, "Download gives both"): one tar of
// its database's dump (database.sql) and its uploads' tar (uploads.tar), each
// as it is on the server. Both sizes kept for it are checked on the server
// first, so the tar's size is known before it is read; then each file is read
// as it comes into the tar, never held whole or parsed. A file that is not
// the size kept for it ends the read in an error.
func (m *Manager) openWebsiteBackup(ctx context.Context, r record, site string, b backupRecord) (io.ReadCloser, int64, error) {
	taken, err := time.Parse(time.RFC3339, b.TakenAt)
	if err != nil || b.UploadsBytes == nil || *b.UploadsBytes <= 0 {
		return nil, 0, serviceapi.ErrBackupUnknown
	}
	dump, uploads := b.Bytes, *b.UploadsBytes
	session, outcome := m.open(ctx, r)
	if session == nil {
		return nil, 0, errStep(outcome)
	}
	out, err := run(ctx, session, fill(websiteBackupSizesScript, "N", site, "ID", b.BackupID), nil)
	if err != nil {
		_ = session.Close()
		return nil, 0, err
	}
	if strings.Join(strings.Fields(out), " ") != strconv.FormatInt(dump, 10)+" "+strconv.FormatInt(uploads, 10) {
		_ = session.Close()
		return nil, 0, serviceapi.ErrBackupUnknown
	}
	streamCtx, cancel := context.WithCancel(ctx)
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		tw := tar.NewWriter(pw)
		write := func(name, file string, size int64) error {
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size, Typeflag: tar.TypeReg, ModTime: taken,
				Format: tar.FormatUSTAR}); err != nil {
				return err
			}
			return session.Stream(streamCtx, fill(backupFileReadScript, "N", site, "FILE", file), tw)
		}
		err := write(websiteDumpName, b.BackupID+".dump", dump)
		if err == nil {
			err = write(websiteUploadsName, b.BackupID+".uploads.tar", uploads)
		}
		if err == nil {
			err = tw.Close()
		}
		_ = pw.CloseWithError(err)
	}()
	return &backupReader{PipeReader: pr, end: func() {
		cancel()
		_ = pr.Close()
		<-done
		_ = session.Close()
	}}, websiteBackupSize(dump, uploads), nil
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
	// websiteBackupSizesScript prints a WordPress website's backup's two
	// sizes on the server, or missing for each that is not there.
	websiteBackupSizesScript = `set -eu
B="$HOME/autobuild/backups/@N@"
for f in "$B/@ID@.dump" "$B/@ID@.uploads.tar"; do if [ -f "$f" ]; then wc -c < "$f"; else echo missing; fi; done
`
	// backupFileReadScript writes one of a backup's files to its output as it is.
	backupFileReadScript = `set -eu
exec cat "$HOME/autobuild/backups/@N@/@FILE@"
`
	// backupDataScript writes a backup's data as text, with the database container's own pg_restore and no database.
	backupDataScript = `set -eu
` + dockerHost + `
exec docker exec -i autobuild-postgres /usr/lib/postgresql/17/bin/pg_restore --data-only --no-owner --no-privileges -f - < "$HOME/autobuild/backups/@N@/@ID@.dump"
`
)

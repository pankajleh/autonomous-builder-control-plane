package remotetarget

import (
	"context"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// A Node app updated to a release that starts on one of the app's backups
// (Repo C design note BF10_GOING_BACK_SAFELY.md §10.2, BF-10d2b: going back to
// an earlier version with its data as it was). The backup is one taken while
// that same release ran, so the release starts on data it wrote itself. The
// plan names it; applying it stops the app first, so nothing written after the
// backup of today's data is lost, backs the data up, restores the chosen backup
// into restore_app_<site> as the app's login, checks its rows and swaps it in
// in one transaction, keeping today's database as previous_app_<site>. Then
// the release starts as an update does. A failure before the swap changes
// nothing and the app that ran is started again; when the release never
// answers, today's database is swapped back and the release it replaced is
// started again. Once the site checks, previous_app_<site> is dropped.

// startFromSteps are what updating a Node app onto a backup does.
func startFromSteps(files, frontdoor string) []string {
	return []string{files, "get_images", "start_database", "make_database", "stop_app", "back_up", "restore_data",
		"switch_site", "start_app", "wait_health", "write_site", frontdoor, "check", "clean_up"}
}

// startFromBackup is the backup an update may start its release on: listed for
// the app, its rows counted (to be checked), and taken while that release ran.
func startFromBackup(d databaseRecord, id, release string) (backupRecord, error) {
	b, ok := findBackup(d, id)
	if !ok || b.Tables == nil {
		return backupRecord{}, serviceapi.ErrBackupUnknown
	}
	if b.Release != release {
		return backupRecord{}, serviceapi.ErrTargetInvalid
	}
	return b, nil
}

// startFromStale is true when an update onto a backup no longer fits its
// target: the app is stopped or its data gone, or the backup no longer kept.
func startFromStale(r record, plan serviceapi.PlanV1) bool {
	site, _ := findSite(r, plan.Site.Name)
	d, kept := findDatabase(r, plan.Site.Name)
	if !kept || site.State == siteStopped {
		return true
	}
	_, err := startFromBackup(d, plan.StartFrom.BackupID, plan.Release.Digest)
	return err != nil
}

// restoreData restores the chosen backup into a copy, checks its rows and
// swaps it in, keeping the database it replaces until the app answers. A
// failure leaves the app's database as it was.
func (m *Manager) restoreData(ctx context.Context, session remote, key, site, id string) (string, error) {
	db := databaseName(site)
	m.mu.Lock()
	r, err := m.read(key)
	m.mu.Unlock()
	if err != nil {
		return "", errStep("the backup could not be read")
	}
	d, _ := findDatabase(r, site)
	b, ok := findBackup(d, id)
	if !ok {
		return "", errStep("the backup is no longer kept")
	}
	restored, err := m.restoreCopy(ctx, session, key, site, b, "restore_", true)
	if err != nil {
		return "", err
	}
	switched, err := run(ctx, session, fill(switchDatabaseScript, "DB", db), nil)
	if err != nil {
		// The swap is one transaction: the database is as it was.
		_, _ = run(ctx, session, fill(dropDatabaseScript, "COPY", "restore_"+db), nil)
		return "", err
	}
	return restored + "; " + strings.TrimSpace(switched), nil
}

// unStartFrom stops the app and swaps back the database a restored backup
// replaced. It answers what it did.
func (m *Manager) unStartFrom(ctx context.Context, session remote, site string) string {
	if _, err := run(ctx, session, fill(unRestoreScript, "N", site, "DB", databaseName(site)), nil); err != nil {
		return "the data it replaced could not be put back"
	}
	return "the data it replaced is put back"
}

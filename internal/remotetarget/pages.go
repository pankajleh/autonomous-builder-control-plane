package remotetarget

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// A WordPress website's standard pages (Repo C design note STANDARD_PAGES.md
// §14, SP1b): Repo C sends the app's page files, ABCP backs the site up as it
// does before an update, puts each file in the site's container and has the
// kit's plugin set the page (`wp autobuild-site page set <name less .json>
// <file> --format=json`, Repo C tools/wp-site-kit SERVER.md), and answers what
// each file came to. ABCP never reads the files: they are named files of at
// most 256 KiB each, 1 MiB in all (serviceapi.DecodePageFiles); only the
// kit's one-line answer is read. A page the person edited in WordPress is kept
// by the kit, and a file the kit refuses is answered refused: neither fails
// the operation. The files stay in ABCP's memory while the operation runs and
// in the container's memory-backed /scratch until its clean_up step; neither
// a record nor a step keeps them, only their digest.

const (
	// pagesKitVersion is the first version of the kit's plugin with `page
	// set` (Repo C plugin 1.2.0). A site whose server image is older is not
	// touched: the operation fails at check_app, before any backup, saying
	// the live site needs updating first.
	pagesKitVersion = "1.2.0"
	// pagesFolder is where the files go in the site's container: its
	// memory-backed /scratch, the container user's own.
	pagesFolder = "/scratch/autobuild-pages"
)

var kitVersionPattern = regexp.MustCompile(`^([0-9]{1,4})\.([0-9]{1,4})\.([0-9]{1,4})$`)

// The kit's answers (its `page set --format=json`): what it did with the
// page, and with its footer link.
var (
	pageOutcomes  = map[string]bool{"created": true, "updated": true, "unchanged": true, "kept": true}
	footerResults = map[string]bool{"none": true, "there": true, "added": true, "full": true}
)

// pagesSteps are a pages operation's steps.
func pagesSteps() []string {
	return []string{"check_app", "back_up", "send_pages", "set_pages", "clean_up"}
}

// SetSitePages puts page files on a WordPress website as an operation: the
// site's kit is checked, the site backed up (reason before_update), each file
// put in its container and set with the kit's command, and the files removed
// from the container again. The same request answers the same operation; the
// same request with other files is refused.
func (m *Manager) SetSitePages(_ context.Context, key, site string, request serviceapi.SitePagesRequestV1) (serviceapi.OperationV1, error) {
	if !siteNamePattern.MatchString(site) || !requestIDPattern.MatchString(request.RequestID) {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
	}
	files, err := serviceapi.DecodePageFiles(request.Files)
	if err != nil || len(files) == 0 {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
	}
	digest := serviceapi.PageFilesDigest(files)
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read(key)
	if err != nil {
		return serviceapi.OperationV1{}, err
	}
	if existing, ok := m.operationByRequest(key, request.RequestID); ok {
		if existing.Operation.Kind != "pages" || existing.Operation.Site != site || existing.PagesDigest != digest {
			return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
		}
		return existing.Operation, nil
	}
	s, ok := findSite(r, site)
	if !ok || !served(s) {
		return serviceapi.OperationV1{}, serviceapi.ErrSiteUnknown
	}
	if siteKind(s) != kindWP {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
	}
	if s.State == siteStopped {
		return serviceapi.OperationV1{}, serviceapi.ErrSiteStopped
	}
	if r.State != stateConfirmed {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetNotReady
	}
	if m.busy[key] {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetBusy
	}
	op := serviceapi.OperationV1{
		SchemaVersion: serviceapi.OperationSchemaV1, OperationID: newID("op-"), Target: key, Kind: "pages", Site: site,
		RequestID: request.RequestID, State: opQueued, StartedAt: m.stamp(),
	}
	for _, name := range pagesSteps() {
		op.Steps = append(op.Steps, serviceapi.OperationStepV1{Name: name, State: "pending"})
	}
	rec := operationRecord{SchemaVersion: operationSchema, Operation: op, Host: s.Host, Release: s.Release, Sandbox: s.Sandbox, PagesDigest: digest}
	if err := m.writeJSON(filepath.Join(m.dir, "operations"), op.OperationID, rec); err != nil {
		return serviceapi.OperationV1{}, err
	}
	if m.pageFiles == nil {
		m.pageFiles = map[string][]serviceapi.PageFile{}
	}
	m.pageFiles[op.OperationID] = files
	m.busy[key] = true
	m.wg.Add(1)
	go m.run(rec)
	return op, nil
}

// runPages carries a pages operation out. A site whose kit is too old, a
// failed backup or files that could not be put in the container fail it
// before any page is set; once set_pages runs, each file's outcome is its
// own.
func (m *Manager) runPages(ctx context.Context, rec *operationRecord, r record, session remote) string {
	site := rec.Operation.Site
	m.mu.Lock()
	files := m.pageFiles[rec.Operation.OperationID]
	delete(m.pageFiles, rec.Operation.OperationID)
	m.mu.Unlock()
	if len(files) == 0 || serviceapi.PageFilesDigest(files) != rec.PagesDigest {
		m.markFirstPending(rec, "failed", "the page files are not kept")
		return "the page files are not kept"
	}
	steps := []struct {
		name string
		work func() (string, error)
	}{
		{"check_app", func() (string, error) {
			out, err := run(ctx, session, fill(wpKitVersionScript, "N", site), nil)
			if err != nil {
				return "", errStep("the site's WordPress did not answer: " + stepDetail(err))
			}
			version := lastLine(out)
			if !atLeast(version, pagesKitVersion) {
				if !kitVersionPattern.MatchString(version) {
					version = "unknown"
				}
				return "", errStep("the site's kit is " + version + ", older than " + pagesKitVersion + ": update the live site first")
			}
			return "kit " + version, nil
		}},
		{"back_up", func() (string, error) {
			_, detail, err := m.backUp(ctx, session, r.Key, site, reasonBeforeUpdate, rec.Release, "")
			return detail, err
		}},
		{"send_pages", func() (string, error) {
			if _, err := run(ctx, session, fill(wpPagesFolderScript, "N", site), nil); err != nil {
				return "", err
			}
			for _, f := range files {
				if _, err := run(ctx, session, fill(wpPageSendScript, "N", site, "F", f.Name), bytes.NewReader(f.Content)); err != nil {
					return "", errStep(f.Name + ": " + stepDetail(err))
				}
			}
			return fmt.Sprintf("%d file(s)", len(files)), nil
		}},
		{"set_pages", func() (string, error) {
			results := make([]serviceapi.SitePageV1, 0, len(files))
			on, kept, refused := 0, 0, 0
			for _, f := range files {
				page := m.setPage(ctx, session, site, f.Name)
				switch page.Outcome {
				case "kept":
					kept++
				case "refused":
					refused++
				default:
					on++
				}
				results = append(results, page)
			}
			m.mu.Lock()
			rec.Operation.Pages = results
			m.mu.Unlock()
			return fmt.Sprintf("%d set, %d kept, %d refused", on, kept, refused), nil
		}},
		{"clean_up", func() (string, error) {
			// The pages are set: a folder that could not be removed goes with the container's memory when it restarts.
			if _, err := run(ctx, session, fill(wpPagesCleanScript, "N", site), nil); err != nil {
				return "the files stay in the container's memory until it restarts", nil
			}
			return "removed", nil
		}},
	}
	for _, s := range steps {
		if err := m.step(rec, s.name, s.work); err != nil {
			return stepDetail(err)
		}
	}
	return ""
}

// setPage sets one page file with the kit's command and reads its answer.
func (m *Manager) setPage(ctx context.Context, session remote, site, name string) serviceapi.SitePageV1 {
	slug := strings.TrimSuffix(name, ".json")
	page := serviceapi.SitePageV1{File: name, Outcome: "refused"}
	out, err := run(ctx, session, fill(wpPageSetScript, "N", site, "SLUG", slug, "F", name), nil)
	if err != nil {
		page.Detail = stepDetail(err)
		return page
	}
	var said struct {
		Slug   string `json:"slug"`
		Page   string `json:"page"`
		Why    string `json:"why"`
		Footer string `json:"footer"`
	}
	if json.Unmarshal([]byte(lastLine(out)), &said) != nil || said.Slug != slug || !pageOutcomes[said.Page] || !footerResults[said.Footer] {
		page.Detail = "the site's answer could not be read"
		return page
	}
	page.Outcome, page.Footer = said.Page, said.Footer
	// Why a page was kept, as the kit words it, bounded and printable.
	if said.Page == "kept" && strings.TrimSpace(said.Why) != "" {
		page.Why = stepDetail(errStep(said.Why))
	}
	return page
}

// atLeast reports a version a.b.c no older than want.
func atLeast(version, want string) bool {
	got, wanted := kitVersionPattern.FindStringSubmatch(version), kitVersionPattern.FindStringSubmatch(want)
	if got == nil || wanted == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		a, _ := strconv.Atoi(got[i])
		b, _ := strconv.Atoi(wanted[i])
		if a != b {
			return a > b
		}
	}
	return true
}

// The pages scripts. @N@ is a site name and @F@ a page file's name, @SLUG@
// that name less .json, each checked against its pattern before it is put in
// (siteNamePattern; serviceapi.DecodePageFiles: [a-z][a-z0-9-]{0,59}.json).
// Every command in the site's container runs as its user, never through a
// shell; a file's bytes reach it on standard input.
const (
	// wpKitVersionScript prints the version of the kit's plugin the site runs.
	wpKitVersionScript = `set -eu
` + dockerHost + `
exec docker exec autobuild-app-@N@ php /usr/local/bin/wp --path=/opt/wordpress plugin get autobuild-site --field=version
`
	// wpPagesFolderScript makes the container's folder for the files afresh.
	wpPagesFolderScript = `set -eu
` + dockerHost + `
docker exec autobuild-app-@N@ /bin/busybox rm -rf ` + pagesFolder + `
docker exec autobuild-app-@N@ /bin/busybox mkdir -m 700 ` + pagesFolder + `
echo made
`
	// wpPageSendScript writes one file, read on its input, into that folder.
	wpPageSendScript = `set -eu
` + dockerHost + `
docker exec -i autobuild-app-@N@ /bin/busybox tee ` + pagesFolder + `/@F@ >/dev/null
echo sent
`
	// wpPageSetScript sets one page with the kit's command: its answer is one
	// line of JSON, or its error on standard error.
	wpPageSetScript = `set -eu
` + dockerHost + `
exec docker exec autobuild-app-@N@ php /usr/local/bin/wp --path=/opt/wordpress autobuild-site page set @SLUG@ ` + pagesFolder + `/@F@ --format=json
`
	// wpPagesCleanScript removes the folder again.
	wpPagesCleanScript = `set -eu
` + dockerHost + `
docker exec autobuild-app-@N@ /bin/busybox rm -rf ` + pagesFolder + `
echo removed
`
)

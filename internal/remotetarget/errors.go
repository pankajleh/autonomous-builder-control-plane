package remotetarget

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/applog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// SiteErrors reads a Node app's recent errors on its target when asked (BF-07c,
// Repo C design note BF07_WHEN_A_RUNNING_APP_GOES_WRONG §4.3): the app
// container's state and restarts, and the error lines of its own output from
// the last day. The server keeps at most the last siteErrorScan lines of that
// output and sends only those that read as errors; here they are cut to
// serviceapi.SiteErrorLines lines and serviceapi.SiteErrorBytes, and anything
// that looks like a secret, a sign-in token, an email address or a phone
// number is replaced before the answer leaves ABCP. It runs beside operations,
// since it changes nothing.
func (m *Manager) SiteErrors(ctx context.Context, key, site string) (serviceapi.SiteErrorsV1, error) {
	if !siteNamePattern.MatchString(site) {
		return serviceapi.SiteErrorsV1{}, serviceapi.ErrSiteUnknown
	}
	m.mu.Lock()
	r, err := m.read(key)
	if err == nil {
		if _, ok := findDatabase(r, site); !ok {
			err = serviceapi.ErrSiteUnknown
		} else if r.State != stateConfirmed {
			err = serviceapi.ErrTargetNotReady
		}
	}
	m.mu.Unlock()
	if err != nil {
		return serviceapi.SiteErrorsV1{}, err
	}
	session, outcome := m.open(ctx, r)
	if session == nil {
		return serviceapi.SiteErrorsV1{}, errStep(outcome)
	}
	defer session.Close()
	out, err := run(ctx, session, fill(siteErrorsScript, "N", site, "SCAN", strconv.Itoa(siteErrorScan)), nil)
	if err != nil {
		return serviceapi.SiteErrorsV1{}, err
	}
	read := parseSiteErrors(out)
	read.SchemaVersion, read.Target, read.Site = serviceapi.SiteErrorsSchemaV1, key, site
	read.ReadAt = time.Now().UTC().Format(time.RFC3339)
	return read, nil
}

// siteErrorScan is how many of the app's last output lines the server looks
// through for errors.
const siteErrorScan = 2000

// parseSiteErrors reads siteErrorsScript's answer: one state line, a marker,
// then the error lines, newest last.
func parseSiteErrors(out string) serviceapi.SiteErrorsV1 {
	read := serviceapi.SiteErrorsV1{Lines: []string{}}
	head, body, _ := strings.Cut(out, "\n"+siteErrorsMarker+"\n")
	fields := strings.Fields(lastLine(head))
	if len(fields) == 4 && fields[0] != "none" {
		read.Running = fields[0] == "running"
		if n, err := strconv.Atoi(fields[1]); err == nil && n >= 0 {
			read.Restarts = n
		}
		if code, err := strconv.Atoi(fields[2]); err == nil && !read.Running {
			read.ExitCode = &code
		}
		read.OutOfMemory = fields[3] == "true"
	}
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		line = applog.Printable(line)
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) > siteErrorLineBytes {
			line = line[:siteErrorLineBytes]
		}
		lines = append(lines, applog.Redact(line))
	}
	if len(lines) > serviceapi.SiteErrorLines {
		lines, read.Truncated = lines[len(lines)-serviceapi.SiteErrorLines:], true
	}
	total := 0
	for i := len(lines) - 1; i >= 0; i-- {
		total += len(lines[i]) + 1
		if total > serviceapi.SiteErrorBytes {
			lines, read.Truncated = lines[i+1:], true
			break
		}
	}
	read.Lines = append(read.Lines, lines...)
	return read
}

// siteErrorLineBytes bounds one line.
const siteErrorLineBytes = 400

const (
	siteErrorsMarker = "--- errors ---"
	// siteErrorsScript prints the app container's state, restart count, last
	// exit code and whether it ran out of memory ("none" when there is no such
	// container, and then no lines), then the error lines among the last
	// @SCAN@ lines of its own output from the last day. Its output is bounded
	// by Docker's log options (BF-07c starts apps with them) and by the scan.
	siteErrorsScript = `set -eu
` + dockerHost + `
s=""
if s=$(docker inspect -f '{{.State.Status}} {{.RestartCount}} {{.State.ExitCode}} {{.State.OOMKilled}}' autobuild-app-@N@ 2>/dev/null); then echo "$s"; else s=""; echo "none 0 0 false"; fi
echo "` + siteErrorsMarker + `"
if [ -n "$s" ]; then
  docker logs --since 24h --tail @SCAN@ autobuild-app-@N@ 2>&1 | grep -aiE 'error|exception|fatal|unhandled|uncaught|refused|denied|failed|crash|panic|ECONN|EADDR|ENOENT|EACCES|timed? ?out|traceback|^[[:space:]]+at ' | tail -n 200 | cut -c1-600 || true
fi
`
)

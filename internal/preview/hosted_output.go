package preview

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/applog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// HostedOutput takes a hosted app's own server output as it runs, and hears the exit code when that server ends on its
// own (Repo C design note BF07_WHEN_A_RUNNING_APP_GOES_WRONG §13, 7c2).
type HostedOutput interface {
	io.Writer
	Ended(code int)
}

// Bounds of what one key keeps.
const (
	// appOutputDay is how far back a key's errors, ends and restarts are kept.
	appOutputDay = 24 * time.Hour
	// appOutputLineLimit is the longest line looked at; the rest of a longer line is dropped.
	appOutputLineLimit = 4 << 10
	// appOutputEvents bounds the ends and restarts kept.
	appOutputEvents = 64
)

// appOutput keeps one hosting key's recent errors in memory, across the key's generations: the lines of its server's
// output that read as errors (applog.ErrorLine), cut to serviceapi.HostedErrorLineBytes and with what looks private
// replaced (applog.Redact) as they arrive, at most serviceapi.SiteErrorLines of them from the last day; when and how
// its server ended on its own; and when the controller started it again. Nothing else of the output is kept.
type appOutput struct {
	mu       sync.Mutex
	now      func() time.Time
	since    time.Time
	partial  []byte
	skipping bool // the rest of a line longer than appOutputLineLimit
	lines    []appOutputLine
	// dropped is when the newest line that no longer fits was kept.
	dropped  time.Time
	ends     []appOutputEnd
	restarts []time.Time
}

type appOutputLine struct {
	at   time.Time
	text string
}

type appOutputEnd struct {
	at   time.Time
	code int
}

func newAppOutput(now func() time.Time) *appOutput {
	return &appOutput{now: now, since: now()}
}

// Write keeps the error lines among p's complete lines; a line's start waits for its end in the next write.
func (o *appOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		chunk := p
		if i >= 0 {
			chunk = p[:i]
		}
		if !o.skipping {
			if room := appOutputLineLimit - len(o.partial); len(chunk) > room {
				o.partial = append(o.partial, chunk[:room]...)
				o.keep()
				o.partial, o.skipping = o.partial[:0], true
			} else {
				o.partial = append(o.partial, chunk...)
			}
		}
		if i < 0 {
			break
		}
		if !o.skipping {
			o.keep()
		}
		o.partial, o.skipping = o.partial[:0], false
		p = p[i+1:]
	}
	return n, nil
}

// keep looks at the line in o.partial. Call with o.mu held.
func (o *appOutput) keep() {
	line := applog.Printable(string(o.partial))
	if strings.TrimSpace(line) == "" || !applog.ErrorLine(line) {
		return
	}
	if len(line) > serviceapi.HostedErrorLineBytes {
		line = line[:serviceapi.HostedErrorLineBytes]
	}
	now := o.now()
	o.prune(now)
	o.lines = append(o.lines, appOutputLine{at: now, text: applog.Redact(line)})
	if len(o.lines) > serviceapi.SiteErrorLines {
		o.lines, o.dropped = o.lines[len(o.lines)-serviceapi.SiteErrorLines:], now
	}
}

// Ended records that the app's server ended on its own with code.
func (o *appOutput) Ended(code int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	o.prune(now)
	o.ends = append(o.ends, appOutputEnd{at: now, code: code})
	if len(o.ends) > appOutputEvents {
		o.ends = o.ends[len(o.ends)-appOutputEvents:]
	}
}

// restarted records that the controller started the app again after its generation ended.
func (o *appOutput) restarted() {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	o.prune(now)
	o.restarts = append(o.restarts, now)
	if len(o.restarts) > appOutputEvents {
		o.restarts = o.restarts[len(o.restarts)-appOutputEvents:]
	}
}

// prune forgets what is older than a day. Call with o.mu held.
func (o *appOutput) prune(now time.Time) {
	old := func(at time.Time) bool { return now.Sub(at) >= appOutputDay }
	i := 0
	for i < len(o.lines) && old(o.lines[i].at) {
		i++
	}
	o.lines = o.lines[i:]
	i = 0
	for i < len(o.ends) && old(o.ends[i].at) {
		i++
	}
	o.ends = o.ends[i:]
	i = 0
	for i < len(o.restarts) && old(o.restarts[i]) {
		i++
	}
	o.restarts = o.restarts[i:]
}

// read answers what is kept, as serviceapi.HostedErrorsV1 less its key, schema and running: the newest lines that fit
// serviceapi.SiteErrorBytes, the restarts and the last end's code.
func (o *appOutput) read() serviceapi.HostedErrorsV1 {
	o.mu.Lock()
	defer o.mu.Unlock()
	now := o.now()
	o.prune(now)
	v := serviceapi.HostedErrorsV1{ReadAt: stamp(now), Since: stamp(o.since), Restarts: len(o.restarts), Lines: []string{}}
	v.Truncated = !o.dropped.IsZero() && now.Sub(o.dropped) < appOutputDay
	first, total := len(o.lines), 0
	for first > 0 {
		total += len(o.lines[first-1].text) + 1
		if total > serviceapi.SiteErrorBytes {
			v.Truncated = true
			break
		}
		first--
	}
	for _, line := range o.lines[first:] {
		v.Lines = append(v.Lines, line.text)
	}
	if len(o.ends) > 0 {
		code := o.ends[len(o.ends)-1].code
		v.ExitCode = &code
	}
	return v
}

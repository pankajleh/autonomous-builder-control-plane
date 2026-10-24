package preview

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

type outputClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *outputClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *outputClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// BF-07c2: a hosted app's output keeps only its error lines, whole, cut and with private details replaced as they
// arrive, however the writes split them.
func TestAHostedAppsOutputKeepsOnlyItsErrorLinesWithPrivateDetailsReplaced(t *testing.T) {
	clock := &outputClock{now: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
	o := newAppOutput(clock.Now)
	for _, part := range []string{"listening on 3000\nError: connect ECONN", "REFUSED 10.0.0.5:5432 for ops@example.com\n", "    at TCPConnectWrap.afterConnect (node:net:1555:16)\n",
		"GET / 200\n\x1b[31mTypeError: x is not a function\x1b[0m\n", "Error: " + strings.Repeat("x", 5000) + "\nok after a long line\n", "fatal: no newline yet"} {
		if n, err := o.Write([]byte(part)); err != nil || n != len(part) {
			t.Fatal("write", n, err)
		}
	}
	v := o.read()
	if len(v.Lines) != 4 || v.Truncated || v.Restarts != 0 || v.ExitCode != nil || v.Since != "2026-10-24T12:00:00Z" || v.ReadAt != "2026-10-24T12:00:00Z" {
		t.Fatalf("read = %+v", v)
	}
	if !strings.HasPrefix(v.Lines[0], "Error: connect ECONNREFUSED") || strings.Contains(v.Lines[0], "ops@example.com") {
		t.Fatal("the split line was not kept whole and redacted:", v.Lines[0])
	}
	if !strings.HasPrefix(v.Lines[1], "    at TCPConnectWrap") || strings.Contains(v.Lines[2], "\x1b") || !strings.Contains(v.Lines[2], "TypeError: x is not a function") {
		t.Fatal("stack frame or control characters", v.Lines)
	}
	if len(v.Lines[3]) != serviceapi.HostedErrorLineBytes || !strings.HasPrefix(v.Lines[3], "Error: xxx") {
		t.Fatal("the long line was not cut", len(v.Lines[3]))
	}
	o.Write([]byte("\n"))
	if v := o.read(); len(v.Lines) != 5 || v.Lines[4] != "fatal: no newline yet" {
		t.Fatal("a line's end in a later write", v.Lines)
	}
}

// BF-07c2: what a key keeps is bounded: the newest serviceapi.SiteErrorLines lines that fit serviceapi.SiteErrorBytes,
// from the last day only, with truncated saying more were seen; ends and restarts from the last day.
func TestAHostedAppsOutputIsBoundedToTheLastDay(t *testing.T) {
	clock := &outputClock{now: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
	o := newAppOutput(clock.Now)
	for i := 0; i < serviceapi.SiteErrorLines+5; i++ {
		o.Write([]byte("Error: number " + strings.Repeat("y", i%3) + "\n"))
	}
	v := o.read()
	if len(v.Lines) != serviceapi.SiteErrorLines || !v.Truncated {
		t.Fatalf("lines %d truncated %v", len(v.Lines), v.Truncated)
	}
	o.Ended(1)
	o.restarted()
	clock.Add(23 * time.Hour)
	o.Ended(137)
	o.restarted()
	o.restarted()
	if v := o.read(); v.Restarts != 3 || v.ExitCode == nil || *v.ExitCode != 137 || len(v.Lines) != serviceapi.SiteErrorLines {
		t.Fatalf("within the day = %+v", v)
	}
	clock.Add(2 * time.Hour)
	if v := o.read(); v.Restarts != 2 || v.ExitCode == nil || *v.ExitCode != 137 || len(v.Lines) != 0 || v.Truncated {
		t.Fatalf("after the day = %+v", v)
	}
	clock.Add(24 * time.Hour)
	if v := o.read(); v.Restarts != 0 || v.ExitCode != nil || v.Since != "2026-10-24T12:00:00Z" {
		t.Fatalf("a day later = %+v", v)
	}
	for i := 0; i < serviceapi.SiteErrorLines; i++ {
		o.Write([]byte("Error: " + strings.Repeat("z", serviceapi.HostedErrorLineBytes) + "\n"))
	}
	v = o.read()
	total := 0
	for _, line := range v.Lines {
		total += len(line) + 1
	}
	if total > serviceapi.SiteErrorBytes || len(v.Lines) == serviceapi.SiteErrorLines || !v.Truncated {
		t.Fatalf("bytes %d lines %d truncated %v", total, len(v.Lines), v.Truncated)
	}
	if serviceapi.ValidateHostedErrorsV1(withKey(v), "app-1") != nil {
		t.Fatal("a full read is not a valid answer")
	}
}

func withKey(v serviceapi.HostedErrorsV1) serviceapi.HostedErrorsV1 {
	v.SchemaVersion, v.HostingKey = "HostedErrorsV1", "app-1"
	return v
}

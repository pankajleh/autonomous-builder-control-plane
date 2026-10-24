package remotetarget

import (
	"fmt"
	"strings"
	"testing"
)

// What the server answers is read into the app's state and its error lines:
// the exit code only once it stopped, nothing for an app with no container,
// and at most serviceapi.SiteErrorLines lines and serviceapi.SiteErrorBytes,
// the newest kept (BF-07c).
func TestSiteErrorsAreReadNewestLastAndBounded(t *testing.T) {
	read := parseSiteErrors("running 0 0 false\n" + siteErrorsMarker + "\n")
	if !read.Running || read.Restarts != 0 || read.ExitCode != nil || read.OutOfMemory || len(read.Lines) != 0 || read.Truncated || read.Lines == nil {
		t.Fatalf("a running app = %+v", read)
	}
	read = parseSiteErrors("restarting 7 137 true\n" + siteErrorsMarker + "\nError: one\n\n\x1b[31mError: two\x1b[0m\tthen\n")
	if read.Running || read.Restarts != 7 || read.ExitCode == nil || *read.ExitCode != 137 || !read.OutOfMemory {
		t.Fatalf("a restarting app = %+v", read)
	}
	if strings.Join(read.Lines, "|") != "Error: one|[31mError: two[0m then" {
		t.Fatalf("lines = %q", read.Lines)
	}
	read = parseSiteErrors("none 0 0 false\n" + siteErrorsMarker + "\n")
	if read.Running || read.Restarts != 0 || read.ExitCode != nil || len(read.Lines) != 0 {
		t.Fatalf("no container = %+v", read)
	}
	if read = parseSiteErrors("garbage"); read.Running || read.ExitCode != nil || len(read.Lines) != 0 {
		t.Fatalf("an answer without a marker = %+v", read)
	}

	var many strings.Builder
	for i := 1; i <= 80; i++ {
		fmt.Fprintf(&many, "Error number %d\n", i)
	}
	read = parseSiteErrors("running 1 0 false\n" + siteErrorsMarker + "\n" + many.String())
	if len(read.Lines) != 50 || read.Lines[0] != "Error number 31" || read.Lines[49] != "Error number 80" || !read.Truncated {
		t.Fatalf("80 lines kept as %d from %q truncated=%v", len(read.Lines), read.Lines[0], read.Truncated)
	}

	long := strings.Repeat("x", 1000)
	many.Reset()
	for i := 0; i < 50; i++ {
		many.WriteString(long + "\n")
	}
	read = parseSiteErrors("running 1 0 false\n" + siteErrorsMarker + "\n" + many.String())
	total := 0
	for _, line := range read.Lines {
		if len(line) != siteErrorLineBytes {
			t.Fatalf("a long line is %d bytes", len(line))
		}
		total += len(line) + 1
	}
	if total > 16*1024 || len(read.Lines) != 40 || !read.Truncated {
		t.Fatalf("long lines kept %d lines, %d bytes, truncated=%v", len(read.Lines), total, read.Truncated)
	}
}

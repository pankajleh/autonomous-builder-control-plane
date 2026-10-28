package stackjob

import (
	"bytes"
	"encoding/xml"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// tail keeps the last bytes written to it, for a step's output.
type tail struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func newTail(limit int) *tail { return &tail{limit: limit} }

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data = append(t.data, p...)
	if len(t.data) > 2*t.limit {
		t.data = append([]byte(nil), t.data[len(t.data)-t.limit:]...)
	}
	return len(p), nil
}

// Lines is the last n lines kept, at most limit bytes, valid UTF-8, with anything that looks like a secret masked.
func (t *tail) Lines(n int) string {
	t.mu.Lock()
	data := append([]byte(nil), t.data...)
	t.mu.Unlock()
	if len(data) > t.limit {
		data = data[len(data)-t.limit:]
	}
	text := strings.ToValidUTF8(strings.ReplaceAll(string(data), "\r\n", "\n"), "?")
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return Mask(strings.Join(lines, "\n"))
}

// secrets are shapes of keys, tokens and passwords that a project's output may print.
var secrets = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`),
	regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,})\b`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`),
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
	regexp.MustCompile(`(?i)\b([a-z0-9_.-]*(?:password|passwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key)[a-z0-9_.-]*)(\s*[:=]\s*)("[^"\n]*"|'[^'\n]*'|[^\s"',;]+)`),
	regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^\s:/@]+:[^\s/@]+@`),
}

// Mask hides what looks like a secret.
func Mask(text string) string {
	for i, re := range secrets {
		switch i {
		case len(secrets) - 2:
			text = re.ReplaceAllString(text, "$1$2[hidden]")
		case len(secrets) - 1:
			text = re.ReplaceAllString(text, "$1[hidden]@")
		default:
			text = re.ReplaceAllString(text, "[hidden]")
		}
	}
	return text
}

const (
	maxReports      = 200
	maxReportBytes  = 4 << 20
	maxReportsBytes = 8 << 20
)

// reportFolders are where the tools write JUnit reports, relative to a module.
var reportFolders = []string{"target/surefire-reports", "target/failsafe-reports", "build/test-results"}

// skippedInWalk are folders never searched for reports.
var skippedInWalk = map[string]bool{"node_modules": true, ".venv": true, ".git": true, ".gradle": true, ".m2": true}

// Counts reads the JUnit reports under dir (Surefire's, Gradle's) and the extra files named (pytest's), within limits,
// and answers the tests' counts, or false when there were none.
func Counts(dir string, extra ...string) (passed, failed, skipped int, ok bool) {
	files := []string{}
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || len(files) >= maxReports {
			return filepath.SkipDir
		}
		if d.IsDir() {
			if skippedInWalk[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.HasPrefix(d.Name(), "TEST-") || !strings.HasSuffix(d.Name(), ".xml") {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		for _, folder := range reportFolders {
			if strings.HasPrefix(rel, folder+"/") || strings.Contains(rel, "/"+folder+"/") {
				files = append(files, path)
				break
			}
		}
		return nil
	})
	files = append(files, extra...)
	read := 0
	for _, file := range files {
		data, readable := readReport(file, maxReportsBytes-read)
		if !readable {
			continue
		}
		read += len(data)
		if t, f, s, good := suiteCounts(data); good {
			passed, failed, skipped, ok = passed+t-f-s, failed+f, skipped+s, true
		}
	}
	if passed < 0 {
		passed = 0
	}
	return passed, failed, skipped, ok
}

// suiteCounts reads one report: a testsuite, or testsuites holding them, and answers tests, failed (failures and
// errors) and skipped. Go's XML reader expands no entities beyond XML's own, so a report cannot grow as it is read.
func suiteCounts(data []byte) (tests, failed, skipped int, ok bool) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	rootSuites := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			return tests, failed, skipped, ok
		}
		if err != nil {
			return 0, 0, 0, false
		}
		switch t := token.(type) {
		case xml.StartElement:
			depth++
			if depth == 1 && t.Name.Local == "testsuites" {
				rootSuites = true
				continue
			}
			if t.Name.Local == "testsuite" && (depth == 1 || (depth == 2 && rootSuites)) {
				a := map[string]int{}
				for _, attr := range t.Attr {
					if n, err := strconv.Atoi(attr.Value); err == nil && n >= 0 && n < 10_000_000 {
						a[attr.Name.Local] = n
					}
				}
				tests += a["tests"]
				failed += a["failures"] + a["errors"]
				skipped += a["skipped"] + a["disabled"]
				ok = true
			}
		case xml.EndElement:
			depth--
		}
	}
}

// readReport reads one report that is a regular file of at most maxReportBytes, and at most left bytes, opened without
// following a link or waiting on a pipe.
func readReport(path string, left int) ([]byte, bool) {
	f, err := os.OpenFile(path, os.O_RDONLY|noFollow|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxReportBytes || int(info.Size()) > left {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, maxReportBytes+1))
	if err != nil || len(data) > maxReportBytes {
		return nil, false
	}
	return data, true
}

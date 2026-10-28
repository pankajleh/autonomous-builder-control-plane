package stackjob

import (
	"bytes"
	"encoding/json"
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

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
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

// ansiCodes are a terminal's colours, which a runner may print even without a terminal.
var ansiCodes = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

// The summaries four Node test runners print at their end (Repo C design note FIX_AND_TEST §5): Jest's "Tests: 1 failed,
// 40 passed, 41 total", Vitest's "Tests  1 failed | 40 passed (41)", Node's own "# pass 40" (TAP, as without a terminal)
// or "ℹ pass 40", and Mocha's "40 passing".
var (
	jestSummary   = regexp.MustCompile(`^Tests:\s+((?:\d+ [a-z]+, )*\d+ total)$`)
	vitestSummary = regexp.MustCompile(`^Tests\s+(\d+ [a-z]+(?: \| \d+ [a-z]+)*) \(\d+\)$`)
	nodeLine      = regexp.MustCompile(`^(?:#|ℹ) (pass|fail|cancelled|skipped|todo) (\d+)$`)
	mochaLine     = regexp.MustCompile(`^(\d+) (passing|failing|pending)(?: \(\S+\))?$`)
	countWord     = regexp.MustCompile(`(\d+) ([a-z]+)`)
)

// RunnerCounts reads the tests' counts from what a Node test runner printed at its end, for a project that writes no JUnit
// report. Output that matches none of the four runners, or more than one, gives none: nothing is guessed.
func RunnerCounts(output string) (passed, failed, skipped int, ok bool) {
	type counts struct{ passed, failed, skipped int }
	found := map[string]*counts{}
	add := func(c *counts, word string, n int) {
		switch word {
		case "passed", "pass", "passing":
			c.passed = n
		case "failed", "fail", "failing":
			c.failed = n
		case "cancelled":
			c.failed += n
		case "skipped", "pending", "todo":
			c.skipped += n
		}
	}
	runner := func(name string, fresh bool) *counts {
		if fresh || found[name] == nil {
			found[name] = &counts{}
		}
		return found[name]
	}
	number := func(s string) (int, bool) {
		n, err := strconv.Atoi(s)
		return n, err == nil && n >= 0 && n < 10_000_000
	}
	inNode, inMocha := false, false
	for _, line := range strings.Split(ansiCodes.ReplaceAllString(output, ""), "\n") {
		line = strings.TrimSpace(line)
		if m := jestSummary.FindStringSubmatch(line); m != nil {
			c := runner("jest", true)
			for _, w := range countWord.FindAllStringSubmatch(m[1], -1) {
				if n, good := number(w[1]); good {
					add(c, w[2], n)
				}
			}
			continue
		}
		if m := vitestSummary.FindStringSubmatch(line); m != nil {
			c := runner("vitest", true)
			for _, w := range countWord.FindAllStringSubmatch(m[1], -1) {
				if n, good := number(w[1]); good {
					add(c, w[2], n)
				}
			}
			continue
		}
		if m := nodeLine.FindStringSubmatch(line); m != nil {
			if n, good := number(m[2]); good {
				// A summary's first line starts it afresh: a second run of the runner is counted, not added to the first.
				c := runner("node", m[1] == "pass" || !inNode)
				inNode = true
				add(c, m[1], n)
			}
			continue
		}
		if m := mochaLine.FindStringSubmatch(line); m != nil {
			if n, good := number(m[1]); good {
				c := runner("mocha", m[2] == "passing" || !inMocha)
				inMocha = true
				add(c, m[2], n)
			}
			continue
		}
	}
	if len(found) != 1 {
		return 0, 0, 0, false
	}
	for _, c := range found {
		return c.passed, c.failed, c.skipped, true
	}
	return 0, 0, 0, false
}

// coverageTool is a coverage report's tool and its version, as the images write it: "coverage.py 7.16.2".
var coverageTool = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9.]{0,30} [0-9][0-9A-Za-z.+-]{0,30}$`)

// ReadCoverage reads what the image's coverage report answered for part n (coverageResult): the project's lines the tests
// ran, its lines in all and the tool; nil when the run could not be measured.
func ReadCoverage(work string, n int) *serviceapi.StackCoverageV1 {
	data, readable := readReport(filepath.Join(work, ".abcp", filepath.Base(coverageResult(n))), 64<<10)
	if !readable {
		return nil
	}
	var c struct {
		Tool    string `json:"tool"`
		Covered int    `json:"covered"`
		Total   int    `json:"total"`
	}
	if json.Unmarshal(data, &c) != nil || c.Total <= 0 || c.Total > 100_000_000 || c.Covered < 0 || c.Covered > c.Total || !coverageTool.MatchString(c.Tool) {
		return nil
	}
	return &serviceapi.StackCoverageV1{Covered: c.Covered, Total: c.Total, Tool: c.Tool}
}

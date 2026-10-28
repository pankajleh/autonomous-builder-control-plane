package stackjob

import (
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"unicode"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/strictjson"
)

// A scan's result (Repo C design note SECURITY_AND_COMPLIANCE §11.1): the image's scanner writes it under /work/.abcp,
// and ABCP reads it once the part's container has ended. It comes from tools run in the customer's own container, so it
// is checked whole, bounded and masked, and kept as data about their code, never followed as instructions.

const (
	scanSchema      = 1
	maxScanBytes    = 1 << 20
	maxFindings     = 2000
	maxLicences     = 200
	maxLicenceCount = 1_000_000
	maxFindingLine  = 10_000_000
	maxMore         = 1_000_000
	maxAboutTools   = 12
	maxAboutData    = 6
)

// scanReport is where part n's scanner writes its result, under /work but outside the part.
func scanReport(n int) string { return "/work/.abcp/scan-" + strconv.Itoa(n) + ".json" }

// scanFile is the result as the scanner writes it.
type scanFile struct {
	Schema   int               `json:"schema"`
	Findings []scanFinding     `json:"findings"`
	More     int               `json:"more"`
	Licences map[string]int    `json:"licences,omitempty"`
	Checks   map[string]string `json:"checks,omitempty"`
	About    *scanAbout        `json:"about,omitempty"`
}

// scanAbout says what the scan used, for the person's report: each tool's version, and the date of each source of
// vulnerability data (OSV's ecosystems, as the build host last fetched them).
type scanAbout struct {
	Tools map[string]string `json:"tools,omitempty"`
	Data  map[string]string `json:"data,omitempty"`
}

type scanFinding struct {
	Kind     string   `json:"kind"`
	Tool     string   `json:"tool"`
	Rule     string   `json:"rule,omitempty"`
	Severity string   `json:"severity"`
	File     string   `json:"file,omitempty"`
	Line     int      `json:"line,omitempty"`
	Package  string   `json:"package,omitempty"`
	Version  string   `json:"version,omitempty"`
	Fixed    string   `json:"fixed,omitempty"`
	Licence  string   `json:"licence,omitempty"`
	IDs      []string `json:"ids,omitempty"`
	Title    string   `json:"title,omitempty"`
}

// ScanResult is a part's checked result.
type ScanResult struct {
	Findings []serviceapi.StackFindingV1
	More     int
	Licences map[string]int
	Checks   map[string]string
	About    *serviceapi.StackScanAboutV1
}

var (
	errScan       = errors.New("the scan's result does not check")
	findingKinds  = map[string]bool{"dependency": true, "secret": true, "code": true, "licence": true}
	severities    = map[string]bool{"critical": true, "high": true, "medium": true, "low": true, "info": true, "unknown": true}
	scanChecks    = map[string]bool{"dependencies": true, "secrets": true, "code": true, "licences": true}
	checkStates   = map[string]bool{"done": true, "failed": true, "skipped": true}
	findingTool   = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)
	advisoryID    = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,60}$`)
	toolVersion   = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,40}$`)
	dataSource    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}$`)
	findingPathOK = regexp.MustCompile(`^[^\x00-\x1f\x7f\\]+$`)
)

// printable is text of at most max runes with no control characters.
func printable(text string, max int) bool {
	if len([]rune(text)) > max {
		return false
	}
	for _, r := range text {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// relativeFile is a path inside the code: relative, clean, never above it.
func relativeFile(file string) bool {
	if file == "" {
		return true
	}
	if !printable(file, 300) || !findingPathOK.MatchString(file) || strings.HasPrefix(file, "/") || path.Clean(file) != file {
		return false
	}
	for _, part := range strings.Split(file, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

func (f scanFinding) valid() bool {
	if !findingKinds[f.Kind] || !findingTool.MatchString(f.Tool) || !severities[f.Severity] || f.Line < 0 || f.Line > maxFindingLine ||
		!printable(f.Rule, 120) || !relativeFile(f.File) || !printable(f.Package, 200) || !printable(f.Version, 100) ||
		!printable(f.Fixed, 100) || !printable(f.Licence, 100) || !printable(f.Title, 300) || len(f.IDs) > 10 {
		return false
	}
	for _, id := range f.IDs {
		if !advisoryID.MatchString(id) {
			return false
		}
	}
	return true
}

// ReadScan reads part n's result from work (the job's folder on this host): a regular file reached through real
// folders only, opened without following a link, at most maxScanBytes, of schema 1, every field bounded and every kind,
// severity and check from its list, and every text masked as a step's output is (with the job's registry tokens).
func ReadScan(work string, n int, secrets []string) (ScanResult, error) {
	name := filepath.Base(scanReport(n))
	if !realFile(work, ".abcp", name) {
		return ScanResult{}, errScan
	}
	f, err := os.OpenFile(filepath.Join(work, ".abcp", name), os.O_RDONLY|noFollow|syscall.O_NONBLOCK, 0)
	if err != nil {
		return ScanResult{}, errScan
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxScanBytes {
		return ScanResult{}, errScan
	}
	data, err := io.ReadAll(io.LimitReader(f, maxScanBytes+1))
	if err != nil || len(data) > maxScanBytes {
		return ScanResult{}, errScan
	}
	var file scanFile
	if strictjson.Decode(data, &file) != nil || file.Schema != scanSchema || file.Findings == nil || len(file.Findings) > maxFindings ||
		file.More < 0 || file.More > maxMore || len(file.Licences) > maxLicences || len(file.Checks) > len(scanChecks) {
		return ScanResult{}, errScan
	}
	mask := func(text string) string { return maskRegistries(Mask(text), secrets) }
	result := ScanResult{Findings: make([]serviceapi.StackFindingV1, 0, len(file.Findings)), More: file.More}
	for _, f := range file.Findings {
		if !f.valid() {
			return ScanResult{}, errScan
		}
		ids := []string(nil)
		for _, id := range f.IDs {
			ids = append(ids, mask(id))
		}
		result.Findings = append(result.Findings, serviceapi.StackFindingV1{Kind: f.Kind, Tool: f.Tool, Rule: mask(f.Rule), Severity: f.Severity,
			File: mask(f.File), Line: f.Line, Package: mask(f.Package), Version: mask(f.Version), Fixed: mask(f.Fixed), Licence: mask(f.Licence),
			IDs: ids, Title: mask(f.Title)})
	}
	if len(file.Licences) > 0 {
		result.Licences = map[string]int{}
		for name, count := range file.Licences {
			if name == "" || !printable(name, 100) || count < 0 || count > maxLicenceCount {
				return ScanResult{}, errScan
			}
			result.Licences[mask(name)] += count
		}
	}
	if len(file.Checks) > 0 {
		result.Checks = map[string]string{}
		for check, state := range file.Checks {
			if !scanChecks[check] || !checkStates[state] {
				return ScanResult{}, errScan
			}
			result.Checks[check] = state
		}
	}
	if file.About != nil {
		about, ok := file.About.checked(mask)
		if !ok {
			return ScanResult{}, errScan
		}
		result.About = about
	}
	return result, nil
}

// checked is what the scan used, every name and version from its pattern and every date printable; nil when it names
// nothing.
func (a scanAbout) checked(mask func(string) string) (*serviceapi.StackScanAboutV1, bool) {
	if len(a.Tools) > maxAboutTools || len(a.Data) > maxAboutData {
		return nil, false
	}
	about := serviceapi.StackScanAboutV1{}
	for tool, version := range a.Tools {
		if !findingTool.MatchString(tool) || !toolVersion.MatchString(version) {
			return nil, false
		}
		if about.Tools == nil {
			about.Tools = map[string]string{}
		}
		about.Tools[tool] = version
	}
	for source, date := range a.Data {
		if !dataSource.MatchString(source) || date == "" || !printable(date, 60) {
			return nil, false
		}
		if about.Data == nil {
			about.Data = map[string]string{}
		}
		about.Data[source] = mask(date)
	}
	if about.Tools == nil && about.Data == nil {
		return nil, true
	}
	return &about, true
}

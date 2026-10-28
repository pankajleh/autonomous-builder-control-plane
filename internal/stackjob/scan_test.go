package stackjob

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// A scan job (Repo C SECURITY_AND_COMPLIANCE §11.1): install, Java's build, no tests, then the image's scanner, whose
// result is read back checked and masked.

const goodScan = `{"schema":1,"findings":[
 {"kind":"dependency","tool":"osv-scanner","rule":"GHSA-p6mc-m468-83gw","severity":"high","file":"requirements.txt","package":"lodash","version":"4.17.20","fixed":"4.17.21","ids":["GHSA-p6mc-m468-83gw","CVE-2020-8203"],"title":"Prototype pollution"},
 {"kind":"secret","tool":"betterleaks","rule":"aws-access-token","severity":"high","file":"config/app.js","line":12,"title":"An AWS key: AKIAABCDEFGHIJKLMNOP"},
 {"kind":"code","tool":"bandit","rule":"B602","severity":"medium","file":"app.py","line":3,"title":"subprocess call with shell=True"},
 {"kind":"licence","tool":"licences","severity":"info","package":"readline","version":"8.2","licence":"GPL-3.0-only"}
],"more":3,"licences":{"MIT":40,"GPL-3.0-only":1,"UNKNOWN":2},"checks":{"dependencies":"done","secrets":"done","code":"done","licences":"done"}}`

func writeScan(t *testing.T, work string, n int, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(work, ".abcp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".abcp", "scan-"+string(rune('0'+n))+".json"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAScanResultIsReadCheckedAndMasked(t *testing.T) {
	work := t.TempDir()
	writeScan(t, work, 0, goodScan)
	result, err := ReadScan(work, 0, []string{"registry-token-123456"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Findings) != 4 || result.More != 3 || result.Licences["MIT"] != 40 || result.Checks["secrets"] != "done" {
		t.Fatalf("result = %+v", result)
	}
	if got := result.Findings[1].Title; strings.Contains(got, "AKIAABCDEFGHIJKLMNOP") || !strings.Contains(got, "[hidden]") {
		t.Fatalf("a key in a title is not masked: %q", got)
	}
	if f := result.Findings[0]; f.Package != "lodash" || f.Fixed != "4.17.21" || len(f.IDs) != 2 || f.File != "requirements.txt" {
		t.Fatalf("dependency = %+v", f)
	}
	empty := t.TempDir()
	writeScan(t, empty, 1, `{"schema":1,"findings":[],"more":0}`)
	if r, err := ReadScan(empty, 1, nil); err != nil || len(r.Findings) != 0 || r.Licences != nil || r.Checks != nil {
		t.Fatalf("an empty scan = %+v %v", r, err)
	}
}

func TestAScanResultThatDoesNotCheckIsRefusedWhole(t *testing.T) {
	finding := func(f string) string { return `{"schema":1,"findings":[` + f + `],"more":0}` }
	ok := `{"kind":"code","tool":"bandit","severity":"low","file":"a.py","line":1}`
	many := strings.TrimSuffix(strings.Repeat(ok+",", 2001), ",")
	for name, text := range map[string]string{
		"not JSON":        `{"schema":1,"findings":[`,
		"two values":      finding(ok) + finding(ok),
		"schema 2":        `{"schema":2,"findings":[],"more":0}`,
		"no findings":     `{"schema":1,"more":0}`,
		"unknown field":   `{"schema":1,"findings":[],"more":0,"value":"x"}`,
		"finding field":   finding(`{"kind":"secret","tool":"betterleaks","severity":"high","file":"a.js","value":"AKIA..."}`),
		"duplicate field": `{"schema":1,"findings":[],"more":0,"more":1}`,
		"kind":            finding(`{"kind":"virus","tool":"bandit","severity":"low"}`),
		"severity":        finding(`{"kind":"code","tool":"bandit","severity":"urgent"}`),
		"tool":            finding(`{"kind":"code","tool":"Bandit!","severity":"low"}`),
		"line":            finding(`{"kind":"code","tool":"bandit","severity":"low","line":-1}`),
		"dot dot":         finding(`{"kind":"code","tool":"bandit","severity":"low","file":"../../etc/passwd"}`),
		"absolute":        finding(`{"kind":"code","tool":"bandit","severity":"low","file":"/etc/passwd"}`),
		"not clean":       finding(`{"kind":"code","tool":"bandit","severity":"low","file":"a/./b.py"}`),
		"control":         finding(`{"kind":"code","tool":"bandit","severity":"low","title":"a\u001b[31mb"}`),
		"long title":      finding(`{"kind":"code","tool":"bandit","severity":"low","title":"` + strings.Repeat("x", 301) + `"}`),
		"ids":             finding(`{"kind":"dependency","tool":"osv-scanner","severity":"low","ids":["GHSA <script>"]}`),
		"too many":        finding(many),
		"more":            `{"schema":1,"findings":[],"more":-1}`,
		"licence count":   `{"schema":1,"findings":[],"more":0,"licences":{"MIT":-1}}`,
		"licence name":    `{"schema":1,"findings":[],"more":0,"licences":{"":1}}`,
		"check key":       `{"schema":1,"findings":[],"more":0,"checks":{"tests":"done"}}`,
		"check state":     `{"schema":1,"findings":[],"more":0,"checks":{"code":"maybe"}}`,
	} {
		work := t.TempDir()
		writeScan(t, work, 0, text)
		if _, err := ReadScan(work, 0, nil); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// Too big, missing, a link, and a folder of links above it.
	big := t.TempDir()
	writeScan(t, big, 0, `{"schema":1,"findings":[],"more":0,"licences":{"`+strings.Repeat("x", maxScanBytes)+`":1}}`)
	if _, err := ReadScan(big, 0, nil); err == nil {
		t.Error("a file past 1 MiB accepted")
	}
	if _, err := ReadScan(t.TempDir(), 0, nil); err == nil {
		t.Error("a missing file accepted")
	}
	outside := t.TempDir()
	writeScan(t, outside, 0, goodScan)
	linked := t.TempDir()
	os.MkdirAll(filepath.Join(linked, ".abcp"), 0o755)
	if err := os.Symlink(filepath.Join(outside, ".abcp", "scan-0.json"), filepath.Join(linked, ".abcp", "scan-0.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadScan(linked, 0, nil); err == nil {
		t.Error("a link accepted")
	}
	folder := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, ".abcp"), filepath.Join(folder, ".abcp")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadScan(folder, 0, nil); err == nil {
		t.Error("a linked .abcp accepted")
	}
}

func TestAScansStepsInstallBuildJavaOnlyRunNoTestsAndScan(t *testing.T) {
	for _, p := range []Part{{Stack: StackPython, Tool: "uv"}, {Stack: StackNode, Tool: "npm", Build: true, Test: true}, {Stack: StackJava, Tool: "maven", Dir: "api"}} {
		steps := ScanSteps(p, 2)
		if len(steps) != 4 || steps[0].Name != "install" || steps[1].Name != "build" || steps[2].Name != "test" || steps[3].Name != "scan" {
			t.Fatalf("%s steps = %+v", p.Stack, steps)
		}
		check := Steps(p, 2)
		if strings.Join(steps[0].Argv, " ") != strings.Join(check[0].Argv, " ") || steps[0].Skip != check[0].Skip {
			t.Errorf("%s install differs from a check's", p.Stack)
		}
		if (p.Stack == StackJava) != (steps[1].Skip == "") || (p.Stack != StackJava && steps[1].Skip != "not needed to scan") {
			t.Errorf("%s build = %+v", p.Stack, steps[1])
		}
		if steps[2].Skip != "a scan runs no tests" {
			t.Errorf("%s test = %+v", p.Stack, steps[2])
		}
		want := "/opt/autobuild/scan --part " + workDir(p) + " --index 2 --stack " + p.Stack + " --out /work/.abcp/scan-2.json"
		if got := strings.Join(steps[3].Argv, " "); got != want || steps[3].Timeout != scanTimeout || steps[3].Dir != workDir(p) {
			t.Errorf("%s scan = %q, want %q", p.Stack, got, want)
		}
	}
}

func scanFixture(t *testing.T, box *fakeSandbox) (*fixture, string) {
	t.Helper()
	f := newFixture(t, box)
	osv := t.TempDir()
	if err := f.m.EnableScans(osv); err != nil {
		t.Fatal(err)
	}
	return f, osv
}

func (f *fixture) scan(t *testing.T, request, source string, files map[string]string) serviceapi.StackJobV1 {
	t.Helper()
	if files != nil {
		write(t, filepath.Join(f.sources, source), files)
	}
	job, err := f.m.StartStackJob(context.Background(), serviceapi.StackJobCreateV1{RequestID: request, Source: source, CacheKey: key, Scan: true})
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func TestAScanJobRunsTheScannerWithTheDataReadOnlyAndKeepsItsFindings(t *testing.T) {
	box := &fakeSandbox{proveOK: true}
	box.exec = func(_ context.Context, work, _ string, argv []string, out io.Writer) (int, error) {
		if argv[0] == "/opt/autobuild/scan" {
			writeScan(t, work, 0, goodScan)
		}
		io.WriteString(out, "done\n")
		return 0, nil
	}
	f, osv := scanFixture(t, box)
	job := f.wait(t, f.scan(t, "req-1", "proj-1", map[string]string{"requirements.txt": "lodash\n", "app.py": "print(1)\n"}).ID)
	if job.State != Passed || !job.Scan || len(job.Parts) != 1 {
		t.Fatalf("job = %+v", job)
	}
	part := job.Parts[0]
	if len(part.Findings) != 4 || part.FindingsMore != 3 || part.Licences["UNKNOWN"] != 2 || part.Checks["code"] != "done" {
		t.Fatalf("part = %+v", part)
	}
	names := []string{}
	for _, s := range part.Steps {
		names = append(names, s.Name+":"+s.State)
	}
	if got := strings.Join(names, " "); got != "install:PASSED build:SKIPPED test:SKIPPED scan:PASSED" {
		t.Fatalf("steps = %s", got)
	}
	if len(box.specs) != 1 || box.specs[0].OSVData != osv {
		t.Fatalf("the scan's spec does not bind the data: %+v", box.specs)
	}
	if len(box.calls) != 2 || box.calls[1].argv[0] != "/opt/autobuild/scan" {
		t.Fatalf("calls = %+v", box.calls)
	}
	// A check under the same project binds nothing, and a scan's body is not a check's.
	check := f.wait(t, f.start(t, "req-2", "proj-1", nil).ID)
	if check.Scan || len(box.specs) != 2 || box.specs[1].OSVData != "" || len(check.Parts[0].Findings) != 0 {
		t.Fatalf("check = %+v, spec %+v", check, box.specs[1])
	}
	if _, err := f.m.StartStackJob(context.Background(), serviceapi.StackJobCreateV1{RequestID: "req-1", Source: "proj-1", CacheKey: key}); !errors.Is(err, serviceapi.ErrStackJobConflict) {
		t.Fatalf("a check under a scan's request = %v", err)
	}
	if again, err := f.m.StartStackJob(context.Background(), serviceapi.StackJobCreateV1{RequestID: "req-1", Source: "proj-1", CacheKey: key, Scan: true}); err != nil || again.ID != job.ID {
		t.Fatalf("replay = %v %v", again.ID, err)
	}
}

func TestAScanGoesOnPastAFailedInstallAndFailsOnlyOnItsOwnResult(t *testing.T) {
	box := &fakeSandbox{proveOK: true}
	result := ""
	exit := 0
	box.exec = func(_ context.Context, work, _ string, argv []string, out io.Writer) (int, error) {
		switch argv[0] {
		case "/opt/autobuild/scan":
			if result != "" {
				writeScan(t, work, 0, result)
			}
			return exit, nil
		default:
			io.WriteString(out, "npm ERR! 404\n")
			return 1, nil
		}
	}
	f, _ := scanFixture(t, box)
	files := map[string]string{"package.json": `{"scripts":{"build":"vite build","test":"vitest"}}`, "package-lock.json": "{}"}
	result = goodScan
	job := f.wait(t, f.scan(t, "req-1", "web", files).ID)
	if job.State != Passed || job.Parts[0].Steps[0].State != Failed || job.Parts[0].Steps[0].Note == "" || job.Parts[0].Steps[3].State != Passed || len(job.Parts[0].Findings) != 4 {
		t.Fatalf("a scan past a failed install = %+v", job)
	}
	for name, setup := range map[string]func(){
		"no result":      func() { result, exit = "", 0 },
		"a bad result":   func() { result, exit = `{"schema":1}`, 0 },
		"scanner failed": func() { result, exit = goodScan, 2 },
	} {
		setup()
		job := f.wait(t, f.scan(t, "req-"+strings.ReplaceAll(name, " ", "-"), "web", nil).ID)
		if job.State != Failed || job.Failure != FailScan || job.Parts[0].Steps[3].State != Failed || len(job.Parts[0].Findings) != 0 {
			t.Errorf("%s = %+v", name, job)
		}
	}
}

func TestScansAreRefusedWithoutTheirDataAndTheirFolderMustBeReal(t *testing.T) {
	f := newFixture(t, &fakeSandbox{proveOK: true})
	write(t, filepath.Join(f.sources, "proj-1"), map[string]string{"requirements.txt": "x\n"})
	if _, err := f.m.StartStackJob(context.Background(), serviceapi.StackJobCreateV1{RequestID: "req-1", Source: "proj-1", CacheKey: key, Scan: true}); !errors.Is(err, serviceapi.ErrStackScansUnavailable) {
		t.Fatalf("a scan without the data = %v", err)
	}
	dir := t.TempDir()
	if err := os.Symlink(dir, dir+"-link"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "relative", dir + "-link", dir + "/missing", dir + "/"} {
		if f.m.EnableScans(bad) == nil {
			t.Errorf("%q enabled scans", bad)
		}
	}
	if _, err := f.m.StartStackJob(context.Background(), serviceapi.StackJobCreateV1{RequestID: "req-2", Source: "proj-1", CacheKey: key}); err != nil {
		t.Fatalf("a check without the data = %v", err)
	}
}

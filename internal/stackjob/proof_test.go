package stackjob

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/preview"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// Repo C design note FIX_AND_TEST: a fix's tests run on the code before it, coverage, and Node's counts.

func TestRunnerCountsReadFourNodeRunnersAndNothingElse(t *testing.T) {
	for _, c := range []struct {
		name, output         string
		passed, failed, skip int
		ok                   bool
	}{
		{"jest", "PASS src/a.test.js\nTest Suites: 1 failed, 2 passed, 3 total\nTests:       1 failed, 2 skipped, 40 passed, 43 total\nSnapshots:   0 total\n", 40, 1, 2, true},
		{"jest in colour", "\x1b[1mTests:\x1b[22m       \x1b[1m\x1b[32m5 passed\x1b[39m\x1b[22m, 5 total\n", 5, 0, 0, true},
		{"vitest", " Test Files  1 failed | 3 passed (4)\n      Tests  2 failed | 40 passed | 1 skipped (43)\n   Start at  10:00:00\n", 40, 2, 1, true},
		{"node, without a terminal", "ok 1 - adds prices\n1..2\n# tests 2\n# suites 0\n# pass 2\n# fail 0\n# cancelled 0\n# skipped 0\n# todo 1\n# duration_ms 50\n", 2, 0, 1, true},
		{"node, with a terminal", "✔ adds prices (1ms)\nℹ tests 3\nℹ pass 2\nℹ fail 1\nℹ cancelled 0\nℹ skipped 0\nℹ todo 0\n", 2, 1, 0, true},
		{"node run twice counts the last", "# pass 9\n# fail 9\n> second\n# pass 3\n# fail 0\n", 3, 0, 0, true},
		{"mocha", "  sums\n    ✓ adds\n\n  40 passing (12ms)\n  1 pending\n  2 failing\n", 40, 2, 1, true},
		{"two runners give none", "Tests:       2 passed, 2 total\n# pass 3\n# fail 0\n", 0, 0, 0, false},
		{"nothing to read", "built in 2s\nall good\n", 0, 0, 0, false},
		{"a test's own words are not a summary", "    Tests: are fun, 3 total of them\n", 0, 0, 0, false},
		{"rspec", "..F*.\n\nFinished in 0.4 seconds (files took 1.2 seconds to load)\n12 examples, 1 failure, 2 pending\n", 9, 1, 2, true},
		{"rspec, all passed", "\x1b[32m3 examples, 0 failures\x1b[0m\n", 3, 0, 0, true},
		{"minitest", "Finished in 0.01s, 200 runs/s.\n\n12 runs, 30 assertions, 1 failures, 1 errors, 2 skips\n", 8, 2, 2, true},
		{"minitest that does not add up", "2 runs, 3 assertions, 2 failures, 1 errors, 0 skips\n", 0, 0, 0, false},
		{"go's lines are not read here", "--- PASS: TestTotal (0.00s)\nok  x 0.1s\n", 0, 0, 0, false},
	} {
		passed, failed, skipped, ok := RunnerCounts(c.output)
		if ok != c.ok || passed != c.passed || failed != c.failed || skipped != c.skip {
			t.Errorf("%s: %d passed, %d failed, %d skipped, %v", c.name, passed, failed, skipped, ok)
		}
	}
}

func TestGosCountsAreEveryTestsLineInAllItsOutput(t *testing.T) {
	g := &goTally{}
	io.WriteString(g, "=== RUN   TestTotal\n--- PASS: TestTotal (0.00s)\n=== RUN   TestSplit\n    --- PASS: TestSplit/even (0.00s)\n    --- FA")
	io.WriteString(g, "IL: TestSplit/odd (0.01s)\n--- FAIL: TestSplit (0.01s)\n\x1b[33m--- SKIP: TestSlow (0.00s)\x1b[0m\n")
	io.WriteString(g, "    total_test.go:9: --- PASS: printed by a test, not a test's line\n"+strings.Repeat("x", goLineBytes+10)+"--- PASS: TestLong\n")
	io.WriteString(g, "FAIL\nFAIL\tx\t0.02s\n--- PASS: TestLast (0.00s)")
	if p, f, s, ok := g.Counts(); !ok || p != 3 || f != 2 || s != 1 {
		t.Fatalf("go counts = %d %d %d %v", p, f, s, ok)
	}
	if _, _, _, ok := (&goTally{}).Counts(); ok {
		t.Fatal("counts without a test")
	}
}

func TestCoverageIsReadOnlyWhenTheReportAnsweredSensibly(t *testing.T) {
	work := t.TempDir()
	os.Mkdir(filepath.Join(work, ".abcp"), 0o755)
	if ReadCoverage(work, 0) != nil {
		t.Fatal("coverage without a report")
	}
	for body, want := range map[string]bool{
		`{"tool": "coverage.py 7.16.2", "covered": 41, "total": 120}` + "\n": true,
		`{"tool": "JaCoCo 0.8.15", "covered": 3, "total": 3}`:                true,
		`{"tool": "c8 12.0.0", "covered": 4, "total": 3}`:                    false,
		`{"tool": "c8 12.0.0", "covered": 0, "total": 0}`:                    false,
		`{"tool": "<b>c8</b>", "covered": 1, "total": 3}`:                    false,
		`not measured`: false,
	} {
		os.WriteFile(filepath.Join(work, ".abcp", "coverage-0.json"), []byte(body), 0o644)
		got := ReadCoverage(work, 0)
		if (got != nil) != want {
			t.Errorf("%s: %+v", body, got)
		}
	}
	os.WriteFile(filepath.Join(work, ".abcp", "coverage-0.json"), []byte(`{"tool": "coverage.py 7.16.2", "covered": 41, "total": 120}`), 0o644)
	if got := ReadCoverage(work, 0); got == nil || *got != (serviceapi.StackCoverageV1{Covered: 41, Total: 120, Tool: "coverage.py 7.16.2"}) {
		t.Fatalf("coverage = %+v", got)
	}
}

func TestAMeasuredTestStepWrapsTheTestsAndKeepsTheirExitCode(t *testing.T) {
	for _, c := range []struct {
		part Part
		want []string
	}{
		{Part{Stack: StackPython, Tool: "pip"}, []string{"PYTHONPATH=/opt/autobuild/coverage/python .venv/bin/python -m coverage run --data-file=/work/.abcp/coverage-0.data",
			"--omit='*/.venv/*", "-m pytest -q -p no:cacheprovider --junitxml=/work/.abcp/pytest-0.xml", "-m unittest discover", "/opt/autobuild/coverage/report python"}},
		{Part{Stack: StackJava, Tool: "maven"}, []string{`JAVA_TOOL_OPTIONS="$JAVA_TOOL_OPTIONS -javaagent:/opt/autobuild/coverage/jacoco/jacocoagent.jar=destfile=/work/.abcp/jacoco-0.exec,append=true,excludes=`,
			`" mvn -B -ntp test;`, "/opt/autobuild/coverage/report java /work/.abcp/jacoco-0.exec /work/.abcp/jacoco-0.xml"}},
		{Part{Stack: StackNode, Tool: "npm", Test: true}, []string{"NODE_V8_COVERAGE=/work/.abcp/v8-0 npm run test;", "/opt/autobuild/coverage/report node /work/.abcp/v8-0 /work/.abcp/c8-0"}},
		{Part{Stack: StackGo, Tool: "go", Test: true}, []string{"go test -v -coverprofile=/work/.abcp/go-cover-0.out -coverpkg=./... ./...;",
			"/opt/autobuild/coverage/report go /work/.abcp/go-cover-0.out /work/.abcp/go-cover-0.json", "fi; go test -v ./..."}},
		{Part{Stack: StackRuby, Tool: "bundler", Runner: "rspec", Test: true}, []string{"mkdir -p /work/.abcp/ruby-0 && AUTOBUILD_COVERAGE=/work/.abcp/ruby-0 RUBYOPT=-r/opt/autobuild/coverage/ruby/start.rb bundle exec rspec;",
			"/opt/autobuild/coverage/report ruby /work/.abcp/ruby-0 /work/.abcp/ruby-0.json", "fi; bundle exec rspec"}},
		{Part{Stack: StackPHP, Tool: "composer", Runner: "phpunit", Test: true}, []string{"vendor/bin/phpunit --log-junit /work/.abcp/phpunit-0.xml --coverage-clover /work/.abcp/clover-0.xml;",
			"/opt/autobuild/coverage/report php /work/.abcp/clover-0.xml /work/.abcp/clover-0.json", "fi; vendor/bin/phpunit --log-junit /work/.abcp/phpunit-0.xml"}},
		{Part{Stack: StackDotnet, Tool: "dotnet", Test: true, Target: "A.sln"}, []string{"dotnet test --no-build A.sln --logger trx --collect 'XPlat Code Coverage' --test-adapter-path /opt/autobuild/coverage/coverlet --results-directory /work/.abcp/dotnet-0;",
			"/opt/autobuild/coverage/report dotnet /work/.abcp/dotnet-0 /work/.abcp/dotnet-0.json", "fi; rm -rf /work/.abcp/dotnet-0 && exec dotnet test --no-build A.sln --logger trx"}},
	} {
		steps := Steps(c.part, 0)
		plain := steps[len(steps)-1]
		m := Measured(c.part, 0, plain)
		if len(m.Argv) != 3 || m.Argv[0] != "/bin/sh" || m.Show != plain.Show || m.JUnit != plain.JUnit || m.NoTests != plain.NoTests {
			t.Fatalf("%s: %+v", c.part.Stack, m)
		}
		script := m.Argv[2]
		// What a run before left goes first; an image without the tools runs the tests as they are; the tests' code is kept.
		if !strings.HasPrefix(script, "rm -rf ") || !strings.Contains(script, "if [ -x /opt/autobuild/coverage/report ]; then ") ||
			!strings.Contains(script, "s=$?; ") || !strings.Contains(script, " > /work/.abcp/coverage-0.json 2>/dev/null; exit $s; fi; ") {
			t.Errorf("%s: %s", c.part.Stack, script)
		}
		for _, w := range c.want {
			if !strings.Contains(script, w) {
				t.Errorf("%s: no %q in %s", c.part.Stack, w, script)
			}
		}
	}
	// Steps with nothing to measure are left as they are.
	node := Part{Stack: StackNode, Tool: "npm"}
	if s := Steps(node, 0)[2]; Measured(node, 0, s).Skip == "" || Measured(node, 0, Steps(node, 0)[0]).Argv[0] != "npm" {
		t.Fatal("a skipped test step or an install was measured")
	}
}

func TestTestFilesAreKnownByEachStacksConventions(t *testing.T) {
	for _, c := range []struct {
		part Part
		path string
		want bool
	}{
		{Part{Stack: StackPython}, "tests/test_total.py", true},
		{Part{Stack: StackPython}, "app/total_test.py", true},
		{Part{Stack: StackPython}, "conftest.py", true},
		{Part{Stack: StackPython}, "tests/data/case.json", true},
		{Part{Stack: StackPython}, "app/total.py", false},
		{Part{Stack: StackPython}, "../tests/test_x.py", false},
		{Part{Stack: StackJava}, "src/test/java/a/TotalTest.java", true},
		{Part{Stack: StackJava}, "core/src/test/resources/x.txt", true},
		{Part{Stack: StackJava}, "src/main/java/a/Total.java", false},
		{Part{Stack: StackNode}, "src/total.test.ts", true},
		{Part{Stack: StackNode}, "src/__tests__/total.js", true},
		{Part{Stack: StackNode}, "src/total.spec.mjs", true},
		{Part{Stack: StackNode}, "src/total.js", false},
		{Part{Stack: StackNode, Dir: "web"}, "web/test/total.js", true},
		{Part{Stack: StackNode, Dir: "web"}, "api/test/total.js", false},
		{Part{Stack: StackGo}, "internal/total/total_test.go", true},
		{Part{Stack: StackGo}, "internal/total/testdata/case.json", true},
		{Part{Stack: StackGo}, "internal/total/total.go", false},
		{Part{Stack: StackRuby}, "spec/models/basket_spec.rb", true},
		{Part{Stack: StackRuby}, "test/basket_test.rb", true},
		{Part{Stack: StackRuby}, "spec/fixtures/basket.yml", true},
		{Part{Stack: StackRuby}, "lib/basket.rb", false},
		{Part{Stack: StackRuby}, "app/models/latest.rb", false},
		{Part{Stack: StackPHP}, "tests/Unit/BasketTest.php", true},
		{Part{Stack: StackPHP}, "src/BasketTest.php", true},
		{Part{Stack: StackPHP}, "app/Models/Basket.php", false},
		{Part{Stack: StackDotnet}, "tests/Shop.Tests/BasketTests.cs", true},
		{Part{Stack: StackDotnet}, "src/Shop.UnitTests/Fixtures/data.json", true},
		{Part{Stack: StackDotnet}, "src/Shop/BasketTest.fs", true},
		{Part{Stack: StackDotnet}, "src/Shop/Basket.cs", false},
		{Part{Stack: StackDotnet}, "src/Contest/Program.cs", false},
	} {
		if got := testFile(c.part, c.path); got != c.want {
			t.Errorf("%s %s = %v", c.part.Stack, c.path, got)
		}
	}
}

func TestWhatTheNewTestsShowedOnTheCodeBefore(t *testing.T) {
	step := func(state string, failed int) serviceapi.StackStepV1 {
		return serviceapi.StackStepV1{Name: "test", State: state, Tests: &serviceapi.StackTestsV1{Passed: 3, Failed: failed}}
	}
	for _, c := range []struct {
		base, before serviceapi.StackStepV1
		want         string
	}{
		{step(Passed, 0), step(Failed, 1), ProofFailedBefore},
		{step(StepSkipped, 0), step(Failed, 1), ProofFailedBefore},
		{step(Failed, 1), step(Failed, 2), ProofFailedBefore},
		{step(Failed, 1), step(Failed, 1), ProofNotRun},
		{step(Passed, 0), step(Passed, 0), ProofPassedBefore},
		{step(Passed, 0), step(StepNotRun, 0), ProofNotRun},
	} {
		if got, _ := proofState(c.base, c.before); got != c.want {
			t.Errorf("%s then %s = %s", c.base.State, c.before.State, got)
		}
	}
}

func TestAFixsNewTestIsRunOnTheCodeBeforeIt(t *testing.T) {
	box := &fakeSandbox{proveOK: true}
	var proofWork string
	box.exec = func(_ context.Context, work, dir string, argv []string, out io.Writer) (int, error) {
		line := strings.Join(argv, " ")
		if !strings.Contains(line, "pytest") {
			io.WriteString(out, "ok\n")
			return 0, nil
		}
		code, _ := os.ReadFile(filepath.Join(work, "sample.py"))
		_, err := os.Stat(filepath.Join(work, "tests", "test_empty.py"))
		report := `<testsuite tests="1" failures="0" errors="0" skipped="0"/>`
		exit := 0
		switch {
		case err == nil && strings.Contains(string(code), "else None"):
			// The new test on the code before the fix: it fails.
			proofWork = work
			report, exit = `<testsuite tests="2" failures="1" errors="0" skipped="0"/>`, 1
		case err == nil:
			report = `<testsuite tests="2" failures="0" errors="0" skipped="0"/>`
		}
		os.WriteFile(filepath.Join(work, ".abcp", "pytest-0.xml"), []byte(report), 0o644)
		return exit, nil
	}
	engine := &fakeEngine{act: func(run EngineRun, n int) (EngineResult, error) {
		if n == 1 {
			os.WriteFile(filepath.Join(run.Work, "sample.py"), []byte("def total(xs):\n    return sum(xs)\n"), 0o644)
			os.WriteFile(filepath.Join(run.Work, "tests", "test_empty.py"), []byte("from sample import total\n\ndef test_empty():\n    assert total([]) == 0\n"), 0o644)
		}
		return EngineResult{Summary: "fixed"}, nil
	}}
	f := newChangeFixture(t, box, engine)
	job := f.wait(t, f.start(t, "proof-1", "fix", pythonProject).ID)
	if job.State != ChangeHandedBack || job.Proof == nil {
		t.Fatalf("job = %+v", job)
	}
	if p := job.Proof; p.State != ProofFailedBefore || strings.Join(p.Files, ",") != "tests/test_empty.py" || p.Tests == nil || p.Tests.Failed != 1 || p.Tests.Passed != 1 {
		t.Fatalf("proof = %+v", p)
	}
	if filepath.Base(proofWork) != "proof" || !strings.Contains(engine.runs[0].Prompt, "run on the code as it was before your change") {
		t.Fatalf("proof ran in %q", proofWork)
	}
	if _, err := os.Stat(proofWork); !os.IsNotExist(err) {
		t.Fatal("the code before the change was kept")
	}
	// The checks after still read the changed code, and the fix is handed back however the proof went.
	if job.After[0].Steps[2].Tests.Passed != 2 || job.After[0].Steps[2].Coverage != nil {
		t.Fatalf("after = %+v", job.After[0].Steps[2])
	}
}

func TestATestJobsCoverageIsMeasuredBeforeAndAfter(t *testing.T) {
	box := &fakeSandbox{proveOK: true}
	after := false
	box.exec = func(_ context.Context, work, dir string, argv []string, out io.Writer) (int, error) {
		line := strings.Join(argv, " ")
		if strings.Contains(line, "coverage run") {
			tests, covered := "1", "40"
			if after {
				tests, covered = "3", "63"
			}
			os.WriteFile(filepath.Join(work, ".abcp", "pytest-0.xml"), []byte(`<testsuite tests="`+tests+`" failures="0" errors="0" skipped="0"/>`), 0o644)
			os.WriteFile(filepath.Join(work, ".abcp", "coverage-0.json"), []byte(`{"tool": "coverage.py 7.16.2", "covered": `+covered+`, "total": 100}`), 0o644)
		}
		io.WriteString(out, "ok\n")
		return 0, nil
	}
	engine := &fakeEngine{act: func(run EngineRun, n int) (EngineResult, error) {
		if n == 1 {
			os.WriteFile(filepath.Join(run.Work, "tests", "test_more.py"), []byte("def test_more():\n    assert True\n"), 0o644)
		}
		after = true
		return EngineResult{Summary: "added tests"}, nil
	}}
	f := newChangeFixture(t, box, engine)
	job := f.wait(t, f.start(t, "cover-1", "tests", pythonProject).ID)
	if job.State != ChangeHandedBack || job.Proof != nil {
		t.Fatalf("job = %+v", job)
	}
	before, later := job.Before[0].Steps[2].Coverage, job.After[0].Steps[2].Coverage
	if before == nil || later == nil || before.Covered != 40 || later.Covered != 63 || later.Total != 100 || later.Tool != "coverage.py 7.16.2" ||
		!strings.Contains(engine.runs[0].Prompt, "line coverage is measured") {
		t.Fatalf("coverage = %+v then %+v", before, later)
	}
}

func TestANodeProjectsCountsComeFromItsRunnersSummary(t *testing.T) {
	box := &fakeSandbox{proveOK: true}
	box.exec = func(_ context.Context, _, _ string, argv []string, out io.Writer) (int, error) {
		if strings.Join(argv, " ") == "npm run test" {
			io.WriteString(out, "> node --test\nok 1 - adds\n# tests 3\n# pass 3\n# fail 0\n# skipped 0\n")
		}
		return 0, nil
	}
	f := newFixture(t, box)
	job := f.wait(t, f.start(t, "node-1", "node-1", map[string]string{"package.json": `{"name": "x", "scripts": {"test": "node --test"}}`}).ID)
	if job.State != Passed || len(job.Parts) != 1 {
		t.Fatalf("job = %+v", job)
	}
	if tests := job.Parts[0].Steps[2].Tests; tests == nil || *tests != (serviceapi.StackTestsV1{Passed: 3}) || job.Parts[0].Steps[2].Coverage != nil {
		t.Fatalf("tests = %+v", tests)
	}
}

func TestGoAndRubyProjectsCountTheirTests(t *testing.T) {
	box := &fakeSandbox{proveOK: true}
	box.exec = func(_ context.Context, _, _ string, argv []string, out io.Writer) (int, error) {
		switch strings.Join(argv, " ") {
		case "go test -v ./...":
			// More tests than the lines a step keeps: each is still counted.
			for i := 0; i < 300; i++ {
				fmt.Fprintf(out, "=== RUN   Test%d\n--- PASS: Test%d (0.00s)\n", i, i)
			}
			io.WriteString(out, "--- FAIL: TestLast (0.00s)\nFAIL\n")
			return 1, nil
		case "bundle exec rspec":
			io.WriteString(out, "...\n\nFinished in 0.1 seconds\n3 examples, 0 failures\n")
		}
		return 0, nil
	}
	f := newFixture(t, box)
	job := f.wait(t, f.start(t, "go-1", "go-1", map[string]string{"go.mod": "module x\n\ngo 1.22\n", "x_test.go": "package x\n"}).ID)
	if job.State != Failed || job.Failure != FailTests || job.Parts[0].Stack != StackGo || job.Parts[0].Version != "1.27" {
		t.Fatalf("go job = %+v", job)
	}
	if tests := job.Parts[0].Steps[2].Tests; tests == nil || *tests != (serviceapi.StackTestsV1{Passed: 300, Failed: 1}) {
		t.Fatalf("go tests = %+v", tests)
	}
	job = f.wait(t, f.start(t, "rb-1", "rb-1", map[string]string{"Gemfile": "gem 'rspec'\n", "spec/x_spec.rb": ""}).ID)
	if job.State != Passed || job.Parts[0].Stack != StackRuby || job.Parts[0].Steps[1].State != StepSkipped {
		t.Fatalf("ruby job = %+v", job)
	}
	if tests := job.Parts[0].Steps[2].Tests; tests == nil || *tests != (serviceapi.StackTestsV1{Passed: 3}) {
		t.Fatalf("ruby tests = %+v", tests)
	}
	specs := map[string]bool{}
	for _, s := range box.specs {
		specs[strings.Join(s.Egress, ",")] = true
	}
	if !specs[preview.EgressGo] || !specs[preview.EgressRubyGems] {
		t.Fatalf("sandboxes' registries = %v", specs)
	}
}

func TestDotnetCountsComeFromItsTRXLogs(t *testing.T) {
	dir := t.TempDir()
	if _, _, _, ok := TRXCounts(filepath.Join(dir, "none")); ok {
		t.Fatal("counts without logs")
	}
	trx := func(name, counters string) {
		os.WriteFile(filepath.Join(dir, name), []byte(`<?xml version="1.0" encoding="utf-8"?><TestRun xmlns="http://microsoft.com/schemas/VisualStudio/TeamTest/2010">`+
			`<ResultSummary outcome="Failed"><Counters `+counters+` /></ResultSummary></TestRun>`), 0o644)
	}
	trx("a.trx", `total="5" executed="4" passed="3" failed="1" error="0" timeout="0" aborted="0" inconclusive="0" notExecuted="1"`)
	trx("b.trx", `total="2" executed="2" passed="1" failed="0" error="1" notExecuted="0"`)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte(`<Counters passed="99"/>`), 0o644)
	if p, f, sk, ok := TRXCounts(dir); !ok || p != 4 || f != 2 || sk != 1 {
		t.Fatalf("trx counts = %d %d %d %v", p, f, sk, ok)
	}
}

func TestPHPAndDotnetProjectsCountTheirTests(t *testing.T) {
	box := &fakeSandbox{proveOK: true}
	box.exec = func(_ context.Context, work, _ string, argv []string, _ io.Writer) (int, error) {
		cmd := strings.Join(argv, " ")
		switch {
		case strings.HasPrefix(cmd, "vendor/bin/phpunit"):
			os.WriteFile(filepath.Join(work, ".abcp", "phpunit-0.xml"), []byte(`<testsuites><testsuite name="all" tests="3" failures="1" errors="0" skipped="0">`+
				`<testsuite name="unit" tests="3" failures="1"/></testsuite></testsuites>`), 0o644)
			return 1, nil
		case strings.Contains(cmd, "dotnet test"):
			os.MkdirAll(filepath.Join(work, ".abcp", "dotnet-0"), 0o755)
			os.WriteFile(filepath.Join(work, ".abcp", "dotnet-0", "run.trx"), []byte(`<TestRun><ResultSummary><Counters passed="2" failed="0"/></ResultSummary></TestRun>`), 0o644)
		}
		return 0, nil
	}
	f := newFixture(t, box)
	job := f.wait(t, f.start(t, "php-1", "php-1", map[string]string{"composer.json": `{"require-dev":{"phpunit/phpunit":"^11"}}`}).ID)
	if job.State != Failed || job.Failure != FailTests || job.Parts[0].Stack != StackPHP || job.Parts[0].Version != "8.4" {
		t.Fatalf("php job = %+v", job)
	}
	if tests := job.Parts[0].Steps[2].Tests; tests == nil || *tests != (serviceapi.StackTestsV1{Passed: 2, Failed: 1}) {
		t.Fatalf("php tests = %+v", tests)
	}
	job = f.wait(t, f.start(t, "dn-1", "dn-1", map[string]string{"Shop.Tests.csproj": `<TargetFramework>net8.0</TargetFramework><PackageReference Include="xunit" Version="2.9.3" />`}).ID)
	if job.State != Passed || job.Parts[0].Stack != StackDotnet || job.Parts[0].Version != "8.0" {
		t.Fatalf("dotnet job = %+v", job)
	}
	if tests := job.Parts[0].Steps[2].Tests; tests == nil || *tests != (serviceapi.StackTestsV1{Passed: 2}) {
		t.Fatalf("dotnet tests = %+v", tests)
	}
}

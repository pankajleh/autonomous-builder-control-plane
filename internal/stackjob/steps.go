package stackjob

import (
	"net/netip"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// Step is one step of a part: install, build or test. A step with Skip set is not run, and says why.
type Step struct {
	Name    string
	Dir     string
	Argv    []string
	Show    string
	Skip    string
	Timeout time.Duration
	// JUnit counts the JUnit reports the tools wrote after the step.
	JUnit bool
	// NoTests is the exit code that means the tools found no tests (pytest's and unittest's 5).
	NoTests int
}

const (
	installTimeout = 15 * time.Minute
	buildTimeout   = 15 * time.Minute
	testTimeout    = 20 * time.Minute
)

// workDir is the part's folder in the container.
func workDir(p Part) string {
	if p.Dir == "" {
		return "/work"
	}
	return path.Join("/work", p.Dir)
}

// pytestReport is where pytest writes its JUnit report for part n, under /work but outside the part.
func pytestReport(n int) string { return "/work/.abcp/pytest-" + strconv.Itoa(n) + ".xml" }

// Steps are a part's install, build and test, from what Detect found. Every command is fixed text with names Detect
// chose from a short list; nothing in the customer's code becomes part of a command.
func Steps(p Part, n int) []Step {
	dir := workDir(p)
	step := func(name string, timeout time.Duration, argv ...string) Step {
		return Step{Name: name, Dir: dir, Argv: argv, Show: strings.Join(argv, " "), Timeout: timeout}
	}
	shell := func(name string, timeout time.Duration, show, script string) Step {
		return Step{Name: name, Dir: dir, Argv: []string{"/bin/sh", "-c", script}, Show: show, Timeout: timeout}
	}
	switch p.Stack {
	case StackPython:
		var install Step
		switch p.Tool {
		case "uv":
			install = step("install", installTimeout, "uv", "sync", "--frozen")
		case "poetry":
			install = step("install", installTimeout, "poetry", "install", "--no-interaction")
		default:
			args := "install --no-input"
			if len(p.Requirements) == 0 {
				args += " ."
			}
			for _, r := range p.Requirements {
				args += " -r " + r
			}
			install = shell("install", installTimeout, "python -m venv .venv && pip "+args, "python -m venv .venv && .venv/bin/python -m pip "+args)
		}
		report := pytestReport(n)
		test := shell("test", testTimeout, "pytest (or unittest when the project has no pytest)",
			`if .venv/bin/python -c "import pytest" 2>/dev/null; then exec .venv/bin/python -m pytest -q -p no:cacheprovider --junitxml=`+report+
				`; else exec .venv/bin/python -m unittest discover; fi`)
		test.JUnit, test.NoTests = true, 5
		return []Step{install, step("build", buildTimeout, ".venv/bin/python", "-m", "compileall", "-q", "-x", `(^|/)\.venv/`, "."), test}
	case StackJava:
		var tool []string
		switch {
		case p.Tool == "maven" && p.Wrapper:
			tool = []string{"/bin/sh", "mvnw", "-B", "-ntp"}
		case p.Tool == "maven":
			tool = []string{"mvn", "-B", "-ntp"}
		case p.Wrapper:
			tool = []string{"/bin/sh", "gradlew", "--no-daemon", "--console=plain"}
		default:
			tool = []string{"gradle", "--no-daemon", "--console=plain"}
		}
		with := func(args ...string) []string { return append(append([]string(nil), tool...), args...) }
		fetches := "Maven"
		build, test := with("-DskipTests", "package"), with("test")
		if p.Tool == "gradle" {
			fetches = "Gradle"
			build, test = with("assemble"), with("test")
		}
		install := Step{Name: "install", Dir: dir, Skip: fetches + " fetches what the code needs while it builds"}
		t := step("test", testTimeout, test...)
		t.JUnit = true
		return []Step{install, step("build", buildTimeout, build...), t}
	case StackNode:
		var install Step
		run := []string{"npm", "run"}
		switch p.Tool {
		case "pnpm":
			install = step("install", installTimeout, "corepack", "pnpm", "install", "--frozen-lockfile")
			run = []string{"corepack", "pnpm", "run"}
		case "yarn":
			install = step("install", installTimeout, "corepack", "yarn", "install", "--frozen-lockfile")
			run = []string{"corepack", "yarn", "run"}
		case "yarn-berry":
			install = step("install", installTimeout, "corepack", "yarn", "install", "--immutable")
			run = []string{"corepack", "yarn", "run"}
		case "npm":
			install = step("install", installTimeout, "npm", "ci", "--no-audit", "--no-fund")
		default:
			install = step("install", installTimeout, "npm", "install", "--no-audit", "--no-fund")
		}
		build := Step{Name: "build", Dir: dir, Skip: "the project has no build script"}
		if p.Build {
			build = step("build", buildTimeout, append(append([]string(nil), run...), "build")...)
		}
		test := Step{Name: "test", Dir: dir, Skip: "the project has no test script"}
		if p.Test {
			test = step("test", testTimeout, append(append([]string(nil), run...), "test")...)
			test.JUnit = true
		}
		return []Step{install, build, test}
	case StackGo:
		test := Step{Name: "test", Dir: dir, Skip: "the project has no tests"}
		if p.Test {
			// Verbose, so the runner's own lines say each test's outcome (RunnerCounts).
			test = step("test", testTimeout, "go", "test", "-v", "./...")
			test.JUnit = true
		}
		return []Step{step("install", installTimeout, "go", "mod", "download"), step("build", buildTimeout, "go", "build", "./..."), test}
	case StackRuby:
		build := Step{Name: "build", Dir: dir, Skip: "a Ruby project has nothing to build"}
		test := Step{Name: "test", Dir: dir, Skip: "the project has no tests"}
		switch p.Runner {
		case "rspec":
			test = step("test", testTimeout, "bundle", "exec", "rspec")
			test.JUnit = true
		case "rake":
			test = step("test", testTimeout, "bundle", "exec", "rake", "test")
			test.JUnit = true
		}
		return []Step{step("install", installTimeout, "bundle", "install"), build, test}
	}
	return nil
}

// scanTimeout bounds a scan job's scan step (Repo C SECURITY_AND_COMPLIANCE §11.1).
const scanTimeout = 10 * time.Minute

// ScanSteps are a scan job's steps for a part: its install as a check's; its build for Java only, whose classes
// SpotBugs reads; no tests; then the image's own scanner, which writes its result to scanReport(n). Fixed text again: the
// stack and the part's folder are what Detect chose.
func ScanSteps(p Part, n int) []Step {
	check := Steps(p, n)
	if len(check) != 3 {
		return nil
	}
	dir := workDir(p)
	build := check[1]
	if p.Stack != StackJava {
		build = Step{Name: "build", Dir: dir, Skip: "not needed to scan"}
	}
	test := Step{Name: "test", Dir: dir, Skip: "a scan runs no tests"}
	argv := []string{"/opt/autobuild/scan", "--part", dir, "--index", strconv.Itoa(n), "--stack", p.Stack, "--out", scanReport(n)}
	scan := Step{Name: "scan", Dir: dir, Argv: argv, Show: "the security scan: dependencies, secrets, code and licences", Timeout: scanTimeout}
	return []Step{check[0], build, test, scan}
}

// allowedEnvironment are the image settings a stack profile may pass to every step, beside PATH.
var allowedEnvironment = map[string]bool{"JAVA_HOME": true, "MAVEN_HOME": true, "GRADLE_HOME": true, "LANG": true}

// Env is a step's whole environment: nothing from the controller, the image's PATH and the profile's few settings, the
// cache's home, and the proxy for every tool of the stack.
func Env(p Part, profile ProfileV1, proxy netip.AddrPort) []string {
	px := "http://" + proxy.String()
	env := []string{"PATH=" + profile.Path, "HOME=/cache/home", "XDG_CACHE_HOME=/cache/home/.cache", "TMPDIR=/tmp", "CI=1", "LANG=C.UTF-8",
		"HTTPS_PROXY=" + px, "HTTP_PROXY=" + px, "https_proxy=" + px, "http_proxy=" + px}
	keys := make([]string, 0, len(profile.Environment))
	for k := range profile.Environment {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+profile.Environment[k])
	}
	switch p.Stack {
	case StackPython:
		env = append(env, "PIP_DISABLE_PIP_VERSION_CHECK=1", "PIP_NO_INPUT=1", "POETRY_VIRTUALENVS_IN_PROJECT=true", "POETRY_NO_INTERACTION=1",
			"UV_PYTHON_DOWNLOADS=never", "UV_LINK_MODE=copy", "UV_PROJECT_ENVIRONMENT=.venv")
	case StackJava:
		gw := proxy.Addr().String()
		port := proxy.Port()
		// Java takes its home from the user database, not HOME (the runner's user is not in the image's, or is another's
		// there), so it is named: Maven's local repository and the settings written into the cache are then found.
		env = append(env, "GRADLE_USER_HOME=/cache/home/.gradle", "JAVA_TOOL_OPTIONS=-Duser.home=/cache/home -Dhttps.proxyHost="+gw+" -Dhttps.proxyPort="+strconv.Itoa(int(port))+
			" -Dhttp.proxyHost="+gw+" -Dhttp.proxyPort="+strconv.Itoa(int(port)))
	case StackNode:
		env = append(env, "npm_config_https_proxy="+px, "npm_config_proxy="+px, "npm_config_update_notifier=false", "YARN_HTTPS_PROXY="+px,
			"YARN_HTTP_PROXY="+px, "YARN_ENABLE_TELEMETRY=0", "COREPACK_ENABLE_DOWNLOAD_PROMPT=0", "COREPACK_HOME=/cache/home/.cache/corepack",
			"NODE_USE_ENV_PROXY=1")
	case StackGo:
		// The module proxy alone (no direct fetches from code hosts), the cache's module and build caches, and never another
		// toolchain downloaded.
		env = append(env, "GOPATH=/cache/home/go", "GOCACHE=/cache/home/.cache/go-build", "GOPROXY=https://proxy.golang.org", "GOTOOLCHAIN=local",
			"GOFLAGS=-modcacherw", "GOTELEMETRY=off")
	case StackRuby:
		// Bundler installs into the cache, never the project, and its own settings stay there too.
		env = append(env, "BUNDLE_PATH=/cache/home/bundle", "BUNDLE_APP_CONFIG=/cache/home/.bundle", "GEM_HOME=/cache/home/gems", "BUNDLE_JOBS=4")
	}
	return env
}

// mavenSettings and gradleProperties tell Maven's and Gradle's own downloads where the proxy is; written into the
// cache's home before a Java part's steps. Maven's also carry the job's own registries (§15.9), and are written again
// without them when the part ends.
func mavenSettings(proxy netip.AddrPort, registries []serviceapi.RegistryV1) string {
	host, port := proxy.Addr().String(), strconv.Itoa(int(proxy.Port()))
	servers, mirrors, profiles := mavenRegistries(registries)
	return `<settings>` + servers + mirrors + `<proxies>` +
		`<proxy><id>abcp-https</id><active>true</active><protocol>https</protocol><host>` + host + `</host><port>` + port + `</port></proxy>` +
		`<proxy><id>abcp-http</id><active>true</active><protocol>http</protocol><host>` + host + `</host><port>` + port + `</port></proxy>` +
		`</proxies>` + profiles + `</settings>` + "\n"
}

func gradleProperties(proxy netip.AddrPort) string {
	host, port := proxy.Addr().String(), strconv.Itoa(int(proxy.Port()))
	return "systemProp.https.proxyHost=" + host + "\nsystemProp.https.proxyPort=" + port +
		"\nsystemProp.http.proxyHost=" + host + "\nsystemProp.http.proxyPort=" + port + "\norg.gradle.daemon=false\n"
}

// coverageTools is where a stack image keeps its coverage tools and their report (Repo C design note FIX_AND_TEST §4,
// tools/stack-images/coverage); an image without it is never measured.
const coverageTools = "/opt/autobuild/coverage"

// coverageResult is where a measured test step leaves the report's answer for part n, under /work but outside the part.
func coverageResult(n int) string { return "/work/.abcp/coverage-" + strconv.Itoa(n) + ".json" }

// coverageOmit leaves a Python project's tests and environment out of what coverage.py counts.
const coverageOmit = "*/.venv/*,*/tests/*,*/test/*,*/test_*.py,*/*_test.py,*/conftest.py"

// jacocoExcludes are the build tools' own classes, which JaCoCo's agent then leaves alone; the report reads only the
// project's own compiled classes whatever ran.
const jacocoExcludes = "org.apache.maven.*:org.codehaus.plexus.*:org.eclipse.sisu.*:org.eclipse.aether.*:org.gradle.*:worker.org.gradle.*"

// Measured is test step t run so the project's coverage is measured (FIX_AND_TEST §4): the same tests with the stack's
// coverage tool around them, then the image's report, whose answer is left at coverageResult(n). What a run before left
// is removed first, so the checks after a change never read the baseline's. The step's exit code is still the tests' own,
// and an image without the tools runs the tests as they are. Fixed text again: the paths are ABCP's and the tool's words
// are Detect's.
func Measured(p Part, n int, t Step) Step {
	if t.Skip != "" || t.Name != "test" {
		return t
	}
	id := strconv.Itoa(n)
	report, result := coverageTools+"/report", coverageResult(n)
	var data, out, run string
	switch p.Stack {
	case StackPython:
		data, out = "/work/.abcp/coverage-"+id+".data", "/work/.abcp/coverage-"+id+".out.json"
		cover := "PYTHONPATH=" + coverageTools + "/python .venv/bin/python -m coverage run --data-file=" + data + " --source=. --omit='" + coverageOmit + "' -m"
		run = `if .venv/bin/python -c "import pytest" 2>/dev/null; then ` + cover + ` pytest -q -p no:cacheprovider --junitxml=` + pytestReport(n) +
			`; else ` + cover + ` unittest discover; fi`
		report += " python"
	case StackJava:
		data, out = "/work/.abcp/jacoco-"+id+".exec", "/work/.abcp/jacoco-"+id+".xml"
		run = `JAVA_TOOL_OPTIONS="$JAVA_TOOL_OPTIONS -javaagent:` + coverageTools + `/jacoco/jacocoagent.jar=destfile=` + data + `,append=true,excludes=` +
			jacocoExcludes + `" ` + strings.Join(t.Argv, " ")
		report += " java"
	case StackNode:
		data, out = "/work/.abcp/v8-"+id, "/work/.abcp/c8-"+id
		run = "NODE_V8_COVERAGE=" + data + " " + strings.Join(t.Argv, " ")
		report += " node"
	case StackGo:
		// Go measures itself: every package of the module counted, whichever package's tests ran it (Repo C MORE_STACKS §5).
		data, out = "/work/.abcp/go-cover-"+id+".out", "/work/.abcp/go-cover-"+id+".json"
		run = "go test -v -coverprofile=" + data + " -coverpkg=./... ./..."
		report += " go"
	case StackRuby:
		// Ruby's own Coverage, started by the image's file in every Ruby process the tests run; each writes into data.
		data, out = "/work/.abcp/ruby-"+id, "/work/.abcp/ruby-"+id+".json"
		run = "mkdir -p " + data + " && AUTOBUILD_COVERAGE=" + data + " RUBYOPT=-r" + coverageTools + "/ruby/start.rb " + strings.Join(t.Argv, " ")
		report += " ruby"
	default:
		return t
	}
	plain := strings.Join(t.Argv, " ")
	if p.Stack == StackPython {
		plain = t.Argv[2]
	}
	script := "rm -rf " + data + " " + out + " " + result + "; if [ -x " + coverageTools + "/report ]; then " + run + "; s=$?; " + report + " " + data + " " +
		out + " > " + result + " 2>/dev/null; exit $s; fi; " + plain
	m := t
	m.Argv = []string{"/bin/sh", "-c", script}
	return m
}

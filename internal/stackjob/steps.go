package stackjob

import (
	"net/netip"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
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
	}
	return nil
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
		env = append(env, "GRADLE_USER_HOME=/cache/home/.gradle", "JAVA_TOOL_OPTIONS=-Dhttps.proxyHost="+gw+" -Dhttps.proxyPort="+strconv.Itoa(int(port))+
			" -Dhttp.proxyHost="+gw+" -Dhttp.proxyPort="+strconv.Itoa(int(port)))
	case StackNode:
		env = append(env, "npm_config_https_proxy="+px, "npm_config_proxy="+px, "npm_config_update_notifier=false", "YARN_HTTPS_PROXY="+px,
			"YARN_HTTP_PROXY="+px, "YARN_ENABLE_TELEMETRY=0", "COREPACK_ENABLE_DOWNLOAD_PROMPT=0", "COREPACK_HOME=/cache/home/.cache/corepack",
			"NODE_USE_ENV_PROXY=1")
	}
	return env
}

// mavenSettings and gradleProperties tell Maven's and Gradle's own downloads where the proxy is; written into the
// cache's home before a Java part's steps.
func mavenSettings(proxy netip.AddrPort) string {
	host, port := proxy.Addr().String(), strconv.Itoa(int(proxy.Port()))
	return `<settings><proxies>` +
		`<proxy><id>abcp-https</id><active>true</active><protocol>https</protocol><host>` + host + `</host><port>` + port + `</port></proxy>` +
		`<proxy><id>abcp-http</id><active>true</active><protocol>http</protocol><host>` + host + `</host><port>` + port + `</port></proxy>` +
		`</proxies></settings>` + "\n"
}

func gradleProperties(proxy netip.AddrPort) string {
	host, port := proxy.Addr().String(), strconv.Itoa(int(proxy.Port()))
	return "systemProp.https.proxyHost=" + host + "\nsystemProp.https.proxyPort=" + port +
		"\nsystemProp.http.proxyHost=" + host + "\nsystemProp.http.proxyPort=" + port + "\norg.gradle.daemon=false\n"
}

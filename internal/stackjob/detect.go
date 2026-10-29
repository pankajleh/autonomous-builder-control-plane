// Package stackjob runs stack jobs (docs/plans/stack-jobs.md; Repo C design note IMPROVE_EXISTING_PRODUCTS §13): a
// customer's code installed, built and tested in the stack sandbox, one part at a time, with each step's evidence.
package stackjob

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	StackPython      = "python"
	StackJava        = "java"
	StackNode        = "node"
	StackGo          = "go"
	StackRuby        = "ruby"
	StackUnsupported = "unsupported"
)

// Defaults are the versions a part runs on when nothing in it says otherwise.
var defaults = map[string]string{StackPython: "3.12", StackJava: "21", StackNode: "22", StackGo: "1.27", StackRuby: "3.4"}

// Part is one part of the code: its folder under the job's work ("" for the top), its stack, the version and tool
// chosen, and what the steps need to know.
type Part struct {
	Dir          string
	Stack        string
	Found        string
	Version      string
	VersionNote  string
	Tool         string
	Wrapper      bool
	Requirements []string
	Build, Test  bool
	// Runner is a Ruby part's test runner: "rspec" or "rake" (Repo C MORE_STACKS §4).
	Runner string
}

// Available is, for each stack, the versions the stack profiles serve.
type Available map[string][]string

// maxManifest is the most of a manifest read: the parts that matter are near the top.
const maxManifest = 1 << 20

func readSmall(path string) string {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxManifest {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

func exists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

// unsupportedMarkers name stacks found but not run yet.
var unsupportedMarkers = []struct{ file, stack string }{
	{"composer.json", "php"}, {"Cargo.toml", "rust"},
}

// stackOf answers the stack of one folder: Java, then Python, then Ruby and Go, then Node, then a stack not run yet, or
// "". Ruby and Go come before Node because a Rails project, or a Go service with a front end, has a package.json for it
// beside its own manifest: the server's stack is the project's (Repo C MORE_STACKS §3).
func stackOf(dir string) (stack, found string) {
	switch {
	case exists(filepath.Join(dir, "pom.xml")) || exists(filepath.Join(dir, "build.gradle")) || exists(filepath.Join(dir, "build.gradle.kts")):
		return StackJava, ""
	case exists(filepath.Join(dir, "pyproject.toml")) || exists(filepath.Join(dir, "setup.py")) || len(requirementFiles(dir)) > 0:
		return StackPython, ""
	case rubyProject(dir):
		return StackRuby, ""
	case exists(filepath.Join(dir, "go.mod")):
		return StackGo, ""
	case exists(filepath.Join(dir, "package.json")):
		return StackNode, ""
	}
	for _, m := range unsupportedMarkers {
		if exists(filepath.Join(dir, m.file)) {
			return StackUnsupported, m.stack
		}
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.csproj")); len(matches) > 0 {
		return StackUnsupported, "dotnet"
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "*.sln")); len(matches) > 0 {
		return StackUnsupported, "dotnet"
	}
	return "", ""
}

// skippedFolders are never a part.
var skippedFolders = map[string]bool{"node_modules": true, "vendor": true, "dist": true, "build": true, "target": true, "venv": true}

// Detect finds the parts of the code in work: the top when it has a manifest, else up to three folders one level down
// that do, in name order.
func Detect(work string, available Available) []Part {
	if stack, found := stackOf(work); stack != "" {
		return []Part{part(work, "", stack, found, available)}
	}
	entries, err := os.ReadDir(work)
	if err != nil {
		return nil
	}
	names := []string{}
	for _, e := range entries {
		if e.Type().IsDir() && !strings.HasPrefix(e.Name(), ".") && !skippedFolders[e.Name()] && folderName.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	parts := []Part{}
	for _, name := range names {
		if len(parts) == 3 {
			break
		}
		if stack, found := stackOf(filepath.Join(work, name)); stack != "" {
			parts = append(parts, part(filepath.Join(work, name), name, stack, found, available))
		}
	}
	return parts
}

var folderName = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)

func part(dir, rel, stack, found string, available Available) Part {
	p := Part{Dir: rel, Stack: stack, Found: found}
	switch stack {
	case StackPython:
		python(dir, &p, available)
	case StackJava:
		java(dir, &p, available)
	case StackNode:
		node(dir, &p, available)
	case StackGo:
		goPart(dir, &p, available)
	case StackRuby:
		rubyPart(dir, &p, available)
	}
	return p
}

// requirementFiles are the requirement files installed, in this order.
func requirementFiles(dir string) []string {
	out := []string{}
	for _, name := range []string{"requirements.txt", "requirements-dev.txt", "requirements-test.txt", "dev-requirements.txt", "test-requirements.txt"} {
		if exists(filepath.Join(dir, name)) {
			out = append(out, name)
		}
	}
	return out
}

var requiresPython = regexp.MustCompile(`(?m)^\s*requires-python\s*=\s*["']([^"'\n]{1,100})["']`)

func python(dir string, p *Part, available Available) {
	pyproject := readSmall(filepath.Join(dir, "pyproject.toml"))
	switch {
	case exists(filepath.Join(dir, "uv.lock")):
		p.Tool = "uv"
	case exists(filepath.Join(dir, "poetry.lock")) || strings.Contains(pyproject, "[tool.poetry]"):
		p.Tool = "poetry"
	default:
		p.Tool = "pip"
		p.Requirements = requirementFiles(dir)
	}
	asked, allowed := "", func(string) bool { return true }
	if v := strings.TrimSpace(firstLine(readSmall(filepath.Join(dir, ".python-version")))); v != "" {
		asked = majorMinor(v)
	} else if m := requiresPython.FindStringSubmatch(pyproject); m != nil {
		allowed = specifierAllows(m[1])
	}
	p.Version, p.VersionNote = choose(StackPython, asked, allowed, available)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// majorMinor is "3.11" of "3.11.4", "python3.11" or "3.11"; "" when it is no version.
func majorMinor(v string) string {
	m := regexp.MustCompile(`(\d+)\.(\d+)`).FindStringSubmatch(v)
	if m == nil {
		return ""
	}
	return m[1] + "." + m[2]
}

// specifierAllows reads a Python version specifier (">=3.10,<3.13") well enough to say which minor versions it allows.
func specifierAllows(spec string) func(string) bool {
	type clause struct{ op, v string }
	clauses := []clause{}
	for _, raw := range strings.Split(spec, ",") {
		m := regexp.MustCompile(`^\s*(>=|<=|==|~=|>|<|!=)\s*(\d+(?:\.\d+)?)`).FindStringSubmatch(raw)
		if m != nil {
			clauses = append(clauses, clause{m[1], m[2]})
		}
	}
	return func(v string) bool {
		for _, c := range clauses {
			cmp := compareVersions(v, c.v)
			switch c.op {
			case ">=", "~=":
				if cmp < 0 {
					return false
				}
			case ">":
				if cmp <= 0 {
					return false
				}
			case "<":
				if cmp >= 0 {
					return false
				}
			case "<=":
				if cmp > 0 {
					return false
				}
			case "==":
				if cmp != 0 {
					return false
				}
			case "!=":
				if cmp == 0 {
					return false
				}
			}
		}
		return true
	}
}

// compareVersions compares dotted numbers by their first two parts.
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 2; i++ {
		x, y := 0, 0
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// choose picks the version a part runs on from those available. Asked nothing, it is the default when allowed, else the
// lowest allowed version; asked a version, it is that one, else the nearest newer allowed one, else the newest allowed,
// and the note says so. With nothing allowed it is the newest, said too.
func choose(stack, asked string, allowed func(string) bool, available Available) (string, string) {
	versions := append([]string(nil), available[stack]...)
	sort.Slice(versions, func(i, j int) bool { return compareVersions(versions[i], versions[j]) < 0 })
	if len(versions) == 0 {
		return "", ""
	}
	newest := versions[len(versions)-1]
	if asked == "" {
		def := defaults[stack]
		for _, v := range versions {
			if v == def && allowed(v) {
				return v, ""
			}
		}
		for _, v := range versions {
			if allowed(v) {
				return v, ""
			}
		}
		return newest, "no version it allows is available, ran on " + newest
	}
	for _, v := range versions {
		if v == asked && allowed(v) {
			return v, ""
		}
	}
	for _, v := range versions {
		if compareVersions(v, asked) >= 0 && allowed(v) {
			return v, "asked for " + asked + ", ran on " + v
		}
	}
	for i := len(versions) - 1; i >= 0; i-- {
		if allowed(versions[i]) {
			return versions[i], "asked for " + asked + ", ran on " + versions[i]
		}
	}
	return newest, "asked for " + asked + ", ran on " + newest
}

var (
	pomRelease    = regexp.MustCompile(`<maven\.compiler\.release>\s*(\d+(?:\.\d+)?)\s*</`)
	pomSource     = regexp.MustCompile(`<maven\.compiler\.source>\s*(\d+(?:\.\d+)?)\s*</`)
	pomJava       = regexp.MustCompile(`<java\.version>\s*(\d+(?:\.\d+)?)\s*</`)
	pomPlugin     = regexp.MustCompile(`<release>\s*(\d+)\s*</release>`)
	gradleToolkit = regexp.MustCompile(`JavaLanguageVersion\.of\(\s*(\d+)\s*\)`)
	gradleCompat  = regexp.MustCompile(`(?:sourceCompatibility|targetCompatibility)\s*=\s*(?:JavaVersion\.VERSION_(?:1_)?(\d+)|['"]?(\d+(?:\.\d+)?)['"]?)`)
)

// javaMajor is 8 of "1.8" and 17 of "17".
func javaMajor(v string) string {
	if strings.HasPrefix(v, "1.") {
		return strings.TrimPrefix(v, "1.")
	}
	major, _, _ := strings.Cut(v, ".")
	return major
}

func java(dir string, p *Part, available Available) {
	asked := strings.TrimSpace(firstLine(readSmall(filepath.Join(dir, ".java-version"))))
	if asked != "" {
		asked = javaMajor(asked)
	}
	if exists(filepath.Join(dir, "pom.xml")) {
		p.Tool = "maven"
		p.Wrapper = exists(filepath.Join(dir, "mvnw")) && exists(filepath.Join(dir, ".mvn", "wrapper", "maven-wrapper.properties"))
		pom := readSmall(filepath.Join(dir, "pom.xml"))
		for _, re := range []*regexp.Regexp{pomRelease, pomSource, pomJava, pomPlugin} {
			if asked == "" {
				if m := re.FindStringSubmatch(pom); m != nil {
					asked = javaMajor(m[1])
				}
			}
		}
	} else {
		p.Tool = "gradle"
		p.Wrapper = exists(filepath.Join(dir, "gradlew")) && exists(filepath.Join(dir, "gradle", "wrapper", "gradle-wrapper.jar")) &&
			exists(filepath.Join(dir, "gradle", "wrapper", "gradle-wrapper.properties"))
		build := readSmall(filepath.Join(dir, "build.gradle")) + "\n" + readSmall(filepath.Join(dir, "build.gradle.kts"))
		if asked == "" {
			if m := gradleToolkit.FindStringSubmatch(build); m != nil {
				asked = m[1]
			} else if m := gradleCompat.FindStringSubmatch(build); m != nil {
				asked = javaMajor(m[1] + m[2])
			}
		}
	}
	if _, err := strconv.Atoi(asked); err != nil {
		asked = ""
	}
	p.Version, p.VersionNote = choose(StackJava, asked, func(string) bool { return true }, available)
	if p.VersionNote != "" && asked != "" && compareVersions(p.Version, asked) > 0 {
		// A newer Java compiles for an older one: say what it was built for, not that it was wrong.
		p.VersionNote = "built with Java " + p.Version + " for Java " + asked
	}
}

func node(dir string, p *Part, available Available) {
	var manifest struct {
		PackageManager string         `json:"packageManager"`
		Engines        map[string]any `json:"engines"`
		Scripts        map[string]any `json:"scripts"`
	}
	_ = json.Unmarshal([]byte(readSmall(filepath.Join(dir, "package.json"))), &manifest)
	pm := strings.ToLower(manifest.PackageManager)
	switch {
	case exists(filepath.Join(dir, "pnpm-lock.yaml")) || strings.HasPrefix(pm, "pnpm@"):
		p.Tool = "pnpm"
	case exists(filepath.Join(dir, "yarn.lock")) || strings.HasPrefix(pm, "yarn@"):
		p.Tool = "yarn"
		if exists(filepath.Join(dir, ".yarnrc.yml")) || (strings.HasPrefix(pm, "yarn@") && !strings.HasPrefix(pm, "yarn@1.")) {
			p.Tool = "yarn-berry"
		}
	case exists(filepath.Join(dir, "package-lock.json")) || exists(filepath.Join(dir, "npm-shrinkwrap.json")):
		p.Tool = "npm"
	default:
		p.Tool = "npm-no-lockfile"
	}
	script := func(name string) bool {
		s, ok := manifest.Scripts[name].(string)
		return ok && strings.TrimSpace(s) != "" && !strings.Contains(s, "no test specified")
	}
	p.Build, p.Test = script("build"), script("test")
	asked := ""
	for _, file := range []string{".nvmrc", ".node-version"} {
		if asked == "" {
			asked = nodeMajor(firstLine(readSmall(filepath.Join(dir, file))))
		}
	}
	if engine, ok := manifest.Engines["node"].(string); ok && asked == "" {
		asked = nodeMajor(engine)
	}
	p.Version, p.VersionNote = choose(StackNode, asked, func(string) bool { return true }, available)
}

// nodeMajor is the first major version a Node version or range names: 20 of "v20.11.0", ">=20" or "^20.1"; "" for an
// alias such as "lts/*".
func nodeMajor(v string) string {
	m := regexp.MustCompile(`(\d+)`).FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return ""
	}
	return m[1]
}

// gemLine is a gem a Gemfile asks for.
var gemLine = regexp.MustCompile(`(?m)^\s*gem\s+["']([A-Za-z0-9._-]{1,100})["']`)

// mobileTools are the gems a mobile app's Gemfile lists for its build tools; a Gemfile of these alone is not a Ruby project.
var mobileTools = map[string]bool{"fastlane": true, "cocoapods": true, "xcpretty": true, "xcodeproj": true, "danger": true, "slather": true}

// rubyProject reports whether a folder is a Ruby project: a Gemfile asking for at least one gem beyond a mobile app's tools.
func rubyProject(dir string) bool {
	gemfile := readSmall(filepath.Join(dir, "Gemfile"))
	for _, m := range gemLine.FindAllStringSubmatch(gemfile, -1) {
		if !mobileTools[m[1]] {
			return true
		}
	}
	return false
}

// goDirective is go.mod's go line: the oldest Go the module builds with.
var goDirective = regexp.MustCompile(`(?m)^go\s+(\d+\.\d+)(?:\.\d+)?\s*$`)

func goPart(dir string, p *Part, available Available) {
	p.Tool, p.Build = "go", true
	p.Test = hasTestFile(dir, func(name string) bool { return strings.HasSuffix(name, "_test.go") })
	allowed := func(string) bool { return true }
	if m := goDirective.FindStringSubmatch(readSmall(filepath.Join(dir, "go.mod"))); m != nil {
		least := m[1]
		allowed = func(v string) bool { return compareVersions(v, least) >= 0 }
	}
	p.Version, p.VersionNote = choose(StackGo, "", allowed, available)
}

var (
	gemfileRuby  = regexp.MustCompile(`(?m)^\s*ruby\s+["'](?:[~>=<\s]*)(\d+\.\d+)`)
	lockfileRuby = regexp.MustCompile(`(?m)^RUBY VERSION\s*\n\s+ruby\s+(\d+\.\d+)`)
	lockedRSpec  = regexp.MustCompile(`(?m)^    rspec-core \(`)
)

func rubyPart(dir string, p *Part, available Available) {
	p.Tool = "bundler"
	lock := readSmall(filepath.Join(dir, "Gemfile.lock"))
	switch {
	case isDir(filepath.Join(dir, "spec")) && (lockedRSpec.MatchString(lock) || strings.Contains(readSmall(filepath.Join(dir, "Gemfile")), "rspec")):
		p.Runner = "rspec"
	case exists(filepath.Join(dir, "Rakefile")) && (isDir(filepath.Join(dir, "test")) || isDir(filepath.Join(dir, "tests"))):
		p.Runner = "rake"
	}
	p.Test = p.Runner != ""
	asked := majorMinor(strings.TrimPrefix(strings.TrimSpace(firstLine(readSmall(filepath.Join(dir, ".ruby-version")))), "ruby-"))
	if asked == "" {
		if m := gemfileRuby.FindStringSubmatch(readSmall(filepath.Join(dir, "Gemfile"))); m != nil {
			asked = m[1]
		} else if m := lockfileRuby.FindStringSubmatch(lock); m != nil {
			asked = m[1]
		}
	}
	p.Version, p.VersionNote = choose(StackRuby, asked, func(string) bool { return true }, available)
}

func isDir(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir()
}

// hasTestFile reports whether a folder holds a test file within four levels, its dependencies and build output left out,
// looking at most at 5,000 entries.
func hasTestFile(dir string, test func(string) bool) bool {
	seen := 0
	var walk func(string, int) bool
	walk = func(d string, depth int) bool {
		entries, err := os.ReadDir(d)
		if err != nil {
			return false
		}
		for _, e := range entries {
			if seen++; seen > 5000 {
				return false
			}
			if e.Type().IsDir() {
				if depth < 4 && !strings.HasPrefix(e.Name(), ".") && !skippedFolders[e.Name()] && walk(filepath.Join(d, e.Name()), depth+1) {
					return true
				}
			} else if e.Type().IsRegular() && test(e.Name()) {
				return true
			}
		}
		return false
	}
	return walk(dir, 0)
}

package stackjob

import (
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var available = Available{StackPython: {"3.10", "3.11", "3.12", "3.13"}, StackJava: {"17", "21"}, StackNode: {"20", "22", "24"}}

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func detectOne(t *testing.T, files map[string]string) Part {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, files)
	parts := Detect(dir, available)
	if len(parts) != 1 {
		t.Fatalf("parts = %+v", parts)
	}
	return parts[0]
}

func TestPythonPartsPickTheirToolAndVersion(t *testing.T) {
	for _, c := range []struct {
		files               map[string]string
		tool, version, note string
		requirements        []string
	}{
		{map[string]string{"requirements.txt": "flask\n"}, "pip", "3.12", "", []string{"requirements.txt"}},
		{map[string]string{"requirements.txt": "", "requirements-dev.txt": "pytest\n"}, "pip", "3.12", "", []string{"requirements.txt", "requirements-dev.txt"}},
		{map[string]string{"pyproject.toml": "[project]\nname='x'\nrequires-python = \">=3.8\"\n"}, "pip", "3.12", "", nil},
		{map[string]string{"pyproject.toml": "[project]\nrequires-python = \">=3.8,<3.12\"\n"}, "pip", "3.10", "", nil},
		{map[string]string{"pyproject.toml": "[project]\nrequires-python = '>=3.13'\n"}, "pip", "3.13", "", nil},
		{map[string]string{"pyproject.toml": "[project]\nrequires-python = \">=3.14\"\n"}, "pip", "3.13", "no version it allows is available, ran on 3.13", nil},
		{map[string]string{"pyproject.toml": "[tool.poetry]\nname='x'\n", "poetry.lock": ""}, "poetry", "3.12", "", nil},
		{map[string]string{"pyproject.toml": "[project]\n", "uv.lock": "", ".python-version": "3.11.4\n"}, "uv", "3.11", "", nil},
		{map[string]string{"setup.py": "", ".python-version": "3.9\n"}, "pip", "3.10", "asked for 3.9, ran on 3.10", nil},
	} {
		p := detectOne(t, c.files)
		if p.Stack != StackPython || p.Tool != c.tool || p.Version != c.version || p.VersionNote != c.note || !slices.Equal(p.Requirements, c.requirements) {
			t.Errorf("%v: %+v", c.files, p)
		}
	}
}

func TestJavaPartsPickMavenOrGradleTheirWrapperAndAJavaThatBuildsForThem(t *testing.T) {
	p := detectOne(t, map[string]string{"pom.xml": "<project><properties><maven.compiler.release>17</maven.compiler.release></properties></project>",
		"mvnw": "", ".mvn/wrapper/maven-wrapper.properties": ""})
	if p.Stack != StackJava || p.Tool != "maven" || !p.Wrapper || p.Version != "17" || p.VersionNote != "" {
		t.Fatalf("maven 17 with wrapper: %+v", p)
	}
	p = detectOne(t, map[string]string{"pom.xml": "<project><properties><java.version>1.8</java.version></properties></project>", "mvnw": ""})
	if p.Wrapper || p.Version != "17" || p.VersionNote != "built with Java 17 for Java 8" {
		t.Fatalf("maven 8, a wrapper script without its properties: %+v", p)
	}
	p = detectOne(t, map[string]string{"build.gradle.kts": "java { toolchain { languageVersion.set(JavaLanguageVersion.of(21)) } }", "gradlew": "",
		"gradle/wrapper/gradle-wrapper.jar": "", "gradle/wrapper/gradle-wrapper.properties": ""})
	if p.Tool != "gradle" || !p.Wrapper || p.Version != "21" {
		t.Fatalf("gradle 21 with wrapper: %+v", p)
	}
	p = detectOne(t, map[string]string{"build.gradle": "sourceCompatibility = JavaVersion.VERSION_11\n", "gradlew": ""})
	if p.Wrapper || p.Version != "17" || p.VersionNote != "built with Java 17 for Java 11" {
		t.Fatalf("gradle 11 without the wrapper's jar: %+v", p)
	}
	if p := detectOne(t, map[string]string{"pom.xml": "<project/>"}); p.Version != "21" || p.VersionNote != "" {
		t.Fatalf("no version said: %+v", p)
	}
	// Java wins over the Python and Node files beside it.
	if p := detectOne(t, map[string]string{"pom.xml": "<project/>", "package.json": "{}", "requirements.txt": ""}); p.Stack != StackJava {
		t.Fatalf("several manifests: %+v", p)
	}
}

func TestNodePartsPickTheirLockfilesToolScriptsAndVersion(t *testing.T) {
	for _, c := range []struct {
		files               map[string]string
		tool, version, note string
		build, test         bool
	}{
		{map[string]string{"package.json": `{"scripts":{"build":"next build","test":"jest"}}`, "package-lock.json": "{}"}, "npm", "22", "", true, true},
		{map[string]string{"package.json": `{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`}, "npm-no-lockfile", "22", "", false, false},
		{map[string]string{"package.json": `{"engines":{"node":">=18"}}`, "pnpm-lock.yaml": ""}, "pnpm", "20", "asked for 18, ran on 20", false, false},
		{map[string]string{"package.json": `{}`, "yarn.lock": "", ".nvmrc": "v24.1.0\n"}, "yarn", "24", "", false, false},
		{map[string]string{"package.json": `{"packageManager":"yarn@4.5.0"}`, "yarn.lock": ""}, "yarn-berry", "22", "", false, false},
		{map[string]string{"package.json": `{}`, ".nvmrc": "lts/*"}, "npm-no-lockfile", "22", "", false, false},
		{map[string]string{"package.json": `{}`, ".node-version": "26"}, "npm-no-lockfile", "24", "asked for 26, ran on 24", false, false},
		{map[string]string{"package.json": `not json`}, "npm-no-lockfile", "22", "", false, false},
	} {
		p := detectOne(t, c.files)
		if p.Stack != StackNode || p.Tool != c.tool || p.Version != c.version || p.VersionNote != c.note || p.Build != c.build || p.Test != c.test {
			t.Errorf("%v: %+v", c.files, p)
		}
	}
}

func TestPartsAreTheTopOrUpToThreeFoldersAndAStackNotRunYetIsNamed(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{"api/pom.xml": "<project/>", "web/package.json": "{}", "docs/readme.md": "", "node_modules/x/package.json": "{}",
		".hidden/package.json": "{}", "worker/requirements.txt": "", "zz/package.json": "{}"})
	parts := Detect(dir, available)
	got := []string{}
	for _, p := range parts {
		got = append(got, p.Dir+":"+p.Stack)
	}
	if strings.Join(got, ",") != "api:java,web:node,worker:python" {
		t.Fatalf("parts = %v", got)
	}
	if p := detectOne(t, map[string]string{"go.mod": "module x\n"}); p.Stack != StackUnsupported || p.Found != "go" {
		t.Fatalf("go: %+v", p)
	}
	if p := detectOne(t, map[string]string{"App.csproj": "<Project/>"}); p.Stack != StackUnsupported || p.Found != "dotnet" {
		t.Fatalf("dotnet: %+v", p)
	}
	if parts := Detect(t.TempDir(), available); len(parts) != 0 {
		t.Fatalf("empty: %+v", parts)
	}
	// A manifest that is a link is not read as one.
	link := t.TempDir()
	write(t, link, map[string]string{"real/pom.xml": "<project/>"})
	if err := os.Symlink(filepath.Join(link, "real", "pom.xml"), filepath.Join(link, "pom.xml")); err != nil {
		t.Fatal(err)
	}
	if parts := Detect(link, available); len(parts) != 1 || parts[0].Dir != "real" {
		t.Fatalf("linked manifest: %+v", parts)
	}
}

func TestStepsAreFixedCommandsForEachTool(t *testing.T) {
	shows := func(p Part) string {
		out := []string{}
		for _, s := range Steps(p, 0) {
			if s.Skip != "" {
				out = append(out, s.Name+": skipped")
				continue
			}
			out = append(out, s.Name+": "+s.Show+" @"+s.Dir)
		}
		return strings.Join(out, " | ")
	}
	for _, c := range []struct {
		part Part
		want string
	}{
		{Part{Stack: StackPython, Tool: "pip", Requirements: []string{"requirements.txt", "requirements-dev.txt"}},
			"install: python -m venv .venv && pip install --no-input -r requirements.txt -r requirements-dev.txt @/work | build: .venv/bin/python -m compileall -q -x (^|/)\\.venv/ . @/work | test: pytest (or unittest when the project has no pytest) @/work"},
		{Part{Stack: StackPython, Tool: "pip"}, "install: python -m venv .venv && pip install --no-input . @/work"},
		{Part{Stack: StackPython, Tool: "uv", Dir: "api"}, "install: uv sync --frozen @/work/api"},
		{Part{Stack: StackJava, Tool: "maven", Wrapper: true}, "install: skipped | build: /bin/sh mvnw -B -ntp -DskipTests package @/work | test: /bin/sh mvnw -B -ntp test @/work"},
		{Part{Stack: StackJava, Tool: "gradle"}, "install: skipped | build: gradle --no-daemon --console=plain assemble @/work | test: gradle --no-daemon --console=plain test @/work"},
		{Part{Stack: StackNode, Tool: "pnpm", Build: true, Test: true}, "install: corepack pnpm install --frozen-lockfile @/work | build: corepack pnpm run build @/work | test: corepack pnpm run test @/work"},
		{Part{Stack: StackNode, Tool: "npm"}, "install: npm ci --no-audit --no-fund @/work | build: skipped | test: skipped"},
		{Part{Stack: StackNode, Tool: "yarn-berry", Test: true}, "install: corepack yarn install --immutable @/work | build: skipped | test: corepack yarn run test @/work"},
	} {
		if got := shows(c.part); !strings.HasPrefix(got, c.want) {
			t.Errorf("%+v:\n got  %s\n want %s", c.part, got, c.want)
		}
	}
	test := Steps(Part{Stack: StackPython, Tool: "pip"}, 1)[2]
	if !test.JUnit || test.NoTests != 5 || !strings.Contains(test.Argv[2], "--junitxml=/work/.abcp/pytest-1.xml") {
		t.Fatalf("python test step: %+v", test)
	}
	if Steps(Part{Stack: StackUnsupported, Found: "go"}, 0) != nil {
		t.Fatal("a stack not run yet has steps")
	}
}

func TestEnvironmentIsMadeFromNothingButThePathTheProfilesSettingsAndTheProxy(t *testing.T) {
	proxy := netip.MustParseAddrPort("10.213.4.17:3129")
	profile := ProfileV1{Path: "/opt/java/openjdk/bin:/usr/bin:/bin", Environment: map[string]string{"JAVA_HOME": "/opt/java/openjdk"}}
	env := strings.Join(Env(Part{Stack: StackJava}, profile, proxy), "\n")
	for _, want := range []string{"PATH=/opt/java/openjdk/bin:/usr/bin:/bin", "HOME=/cache/home", "CI=1", "HTTPS_PROXY=http://10.213.4.17:3129",
		"JAVA_HOME=/opt/java/openjdk", "JAVA_TOOL_OPTIONS=-Duser.home=/cache/home -Dhttps.proxyHost=10.213.4.17 -Dhttps.proxyPort=3129 -Dhttp.proxyHost=10.213.4.17 -Dhttp.proxyPort=3129",
		"GRADLE_USER_HOME=/cache/home/.gradle"} {
		if !strings.Contains(env, want+"\n") && !strings.HasSuffix(env, want) {
			t.Errorf("java env lacks %q:\n%s", want, env)
		}
	}
	if node := strings.Join(Env(Part{Stack: StackNode}, ProfileV1{Path: "/usr/local/bin:/usr/bin:/bin"}, proxy), "\n"); !strings.Contains(node, "npm_config_https_proxy=http://10.213.4.17:3129") ||
		!strings.Contains(node, "COREPACK_HOME=/cache/home/.cache/corepack") || strings.Contains(node, "JAVA_TOOL_OPTIONS") {
		t.Fatalf("node env:\n%s", node)
	}
	if py := strings.Join(Env(Part{Stack: StackPython}, ProfileV1{Path: "/usr/local/bin:/usr/bin:/bin"}, proxy), "\n"); !strings.Contains(py, "UV_PYTHON_DOWNLOADS=never") ||
		!strings.Contains(py, "POETRY_VIRTUALENVS_IN_PROJECT=true") {
		t.Fatalf("python env:\n%s", py)
	}
	if s := mavenSettings(proxy, nil); !strings.Contains(s, "<host>10.213.4.17</host><port>3129</port>") || !strings.Contains(gradleProperties(proxy), "systemProp.https.proxyPort=3129") {
		t.Fatalf("java proxy files:\n%s\n%s", s, gradleProperties(proxy))
	}
}

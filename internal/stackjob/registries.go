package stackjob

import (
	"encoding/base64"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// A job's private registries (Repo C design note IMPROVE_EXISTING_PRODUCTS §15.9, F5b): their names join the part's own
// egress, the tools' settings are written into the cache's home before the part and removed after it (the cache is the
// project's and is kept between jobs; the token is not), and every token is masked wherever an output is kept.

// registryHosts answers the names a part's egress lets through for its job's registries.
func registryHosts(rs []serviceapi.RegistryV1) []string {
	hosts := []string{}
	for _, r := range rs {
		if h := r.Host(); h != "" && !contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// slash answers a registry's address ending in one slash, as npm and Yarn compare them.
func slash(u string) string { return strings.TrimRight(u, "/") + "/" }

// npmrc is npm's (and pnpm's and Yarn 1's) settings for a job's npm registries: the default registry for "all", a
// scope's for "own", and each registry's token for its own address alone.
func npmrc(rs []serviceapi.RegistryV1) string {
	var b strings.Builder
	for _, r := range rs {
		if r.Kind != "npm" {
			continue
		}
		u := slash(r.URL)
		if r.Use == "all" {
			b.WriteString("registry=" + u + "\n")
		} else {
			b.WriteString(r.Scope + ":registry=" + u + "\n")
		}
		b.WriteString("//" + strings.TrimPrefix(u, "https://") + ":_authToken=" + r.Token + "\n")
	}
	return b.String()
}

// yarnrc is Yarn 2 and later's settings for the same registries.
func yarnrc(rs []serviceapi.RegistryV1) string {
	var top, scopes, auth strings.Builder
	for _, r := range rs {
		if r.Kind != "npm" {
			continue
		}
		u := strings.TrimRight(r.URL, "/")
		if r.Use == "all" {
			top.WriteString("npmRegistryServer: " + quoteYAML(u) + "\n")
		} else {
			scopes.WriteString("  " + quoteYAML(strings.TrimPrefix(r.Scope, "@")) + ":\n    npmRegistryServer: " + quoteYAML(u) + "\n    npmAuthToken: " +
				quoteYAML(r.Token) + "\n")
		}
		auth.WriteString("  " + quoteYAML(strings.TrimPrefix(u, "https:")) + ":\n    npmAuthToken: " + quoteYAML(r.Token) + "\n    npmAlwaysAuth: true\n")
	}
	out := top.String()
	if scopes.Len() > 0 {
		out += "npmScopes:\n" + scopes.String()
	}
	if auth.Len() > 0 {
		out += "npmRegistries:\n" + auth.String()
	}
	return out
}

func quoteYAML(s string) string { return `"` + s + `"` }

// withCredentials answers a registry's address with its user name and token in it, as pip and uv take them.
func withCredentials(r serviceapi.RegistryV1) string {
	u, err := url.Parse(r.URL)
	if err != nil {
		return r.URL
	}
	u.User = url.UserPassword(r.Username, r.Token)
	return u.String()
}

// pipConf is pip's settings for a job's Python registries: the first one for "all" as the index, the rest as extra ones.
func pipConf(rs []serviceapi.RegistryV1) string {
	index, extra := "", []string{}
	for _, r := range rs {
		if r.Kind != "python" {
			continue
		}
		if r.Use == "all" && index == "" {
			index = withCredentials(r)
		} else {
			extra = append(extra, withCredentials(r))
		}
	}
	if index == "" && len(extra) == 0 {
		return ""
	}
	out := "[global]\n"
	if index != "" {
		out += "index-url = " + index + "\n"
	}
	if len(extra) > 0 {
		out += "extra-index-url =\n"
		for _, e := range extra {
			out += "    " + e + "\n"
		}
	}
	return out
}

// uvToml is uv's settings for the same registries: the "all" one as the default index, the others before it.
func uvToml(rs []serviceapi.RegistryV1) string {
	var b strings.Builder
	defaulted := false
	for _, r := range rs {
		if r.Kind != "python" {
			continue
		}
		b.WriteString("[[index]]\nurl = " + quoteTOML(withCredentials(r)) + "\n")
		if r.Use == "all" && !defaulted {
			b.WriteString("default = true\n")
			defaulted = true
		}
		b.WriteString("\n")
	}
	return b.String()
}

func quoteTOML(s string) string { return `"` + s + `"` }

// xmlText escapes s for an XML text node; registries' fields are checked to carry none of these, this is a second guard.
func xmlText(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
}

// mavenRegistries is what Maven's settings carry for a job's Maven registries: a server each, a mirror of everything for
// the first "all" one, and a profile, active by default, adding the others as repositories.
func mavenRegistries(rs []serviceapi.RegistryV1) (servers, mirrors, profiles string) {
	var sv, mr, repos strings.Builder
	mirrored := false
	for i, r := range rs {
		if r.Kind != "maven" {
			continue
		}
		id := "abcp-registry-" + string(rune('1'+i))
		sv.WriteString("<server><id>" + id + "</id><username>" + xmlText(r.Username) + "</username><password>" + xmlText(r.Token) + "</password></server>")
		if r.Use == "all" && !mirrored {
			mr.WriteString("<mirror><id>" + id + "</id><mirrorOf>*</mirrorOf><url>" + xmlText(r.URL) + "</url></mirror>")
			mirrored = true
			continue
		}
		repos.WriteString("<repository><id>" + id + "</id><url>" + xmlText(r.URL) + "</url></repository>")
	}
	if sv.Len() > 0 {
		servers = "<servers>" + sv.String() + "</servers>"
	}
	if mr.Len() > 0 {
		mirrors = "<mirrors>" + mr.String() + "</mirrors>"
	}
	if repos.Len() > 0 {
		r := repos.String()
		profiles = "<profiles><profile><id>abcp-registries</id><repositories>" + r + "</repositories><pluginRepositories>" +
			strings.ReplaceAll(strings.ReplaceAll(r, "<repository>", "<pluginRepository>"), "</repository>", "</pluginRepository>") +
			"</pluginRepositories></profile></profiles><activeProfiles><activeProfile>abcp-registries</activeProfile></activeProfiles>"
	}
	return servers, mirrors, profiles
}

// gradleInit is an init script adding a job's Maven registries to Gradle's plugin and dependency repositories, and to
// every project's unless the build refuses repositories declared in projects.
func gradleInit(rs []serviceapi.RegistryV1) string {
	var repos strings.Builder
	for _, r := range rs {
		if r.Kind != "maven" {
			continue
		}
		repos.WriteString("    handler.maven { url = uri('" + r.URL + "'); credentials { username = '" + r.Username + "'; password = '" + r.Token + "' } }\n")
	}
	if repos.Len() == 0 {
		return ""
	}
	return "// Written by ABCP for this job alone; removed when the part ends.\n" +
		"def abcpRegistries = { handler ->\n" + repos.String() + "}\n" +
		"def abcpProjectRepositories = true\n" +
		"gradle.settingsEvaluated { settings ->\n" +
		"  abcpRegistries(settings.pluginManagement.repositories)\n" +
		"  try {\n" +
		"    abcpRegistries(settings.dependencyResolutionManagement.repositories)\n" +
		"    abcpProjectRepositories = settings.dependencyResolutionManagement.repositoriesMode.get().toString() != 'FAIL_ON_PROJECT_REPOS'\n" +
		"  } catch (Throwable ignored) {}\n" +
		"}\n" +
		"gradle.beforeProject { project -> if (abcpProjectRepositories) abcpRegistries(project.repositories) }\n"
}

// registryFile is one settings file a job's registries need: where it goes under the cache and what it says.
type registryFile struct {
	dir  []string
	name string
	data string
}

// registryFiles answers the settings files a job's registries need, whatever stack each part is: npm's and Yarn's for
// npm registries, pip's and uv's for Python ones, Gradle's init script for Maven ones (Maven's settings are homeSettings'
// own, written with the proxy before each Java part).
func registryFiles(rs []serviceapi.RegistryV1) []registryFile {
	files := []registryFile{}
	add := func(data string, name string, dir ...string) {
		if data != "" {
			files = append(files, registryFile{dir: append([]string{"home"}, dir...), name: name, data: data})
		}
	}
	add(npmrc(rs), ".npmrc")
	add(yarnrc(rs), ".yarnrc.yml")
	add(pipConf(rs), "pip.conf", ".config", "pip")
	add(uvToml(rs), "uv.toml", ".config", "uv")
	add(gradleInit(rs), "abcp-registries.gradle", ".gradle", "init.d")
	for _, r := range rs {
		if r.Kind == "maven" {
			// Not written here, only removed when the job ends: homeSettings writes it with the registries in it.
			files = append(files, registryFile{dir: []string{"home", ".m2"}, name: "settings.xml"})
			break
		}
	}
	return files
}

// writeRegistrySettings writes a job's registry settings into the cache's home through real folders only, and answers
// what removes them when the job ends, again through real folders only, since the job's code may have left links
// where they were. With no registries it writes nothing.
func writeRegistrySettings(cache string, rs []serviceapi.RegistryV1) (func(), error) {
	files := registryFiles(rs)
	remove := func() {
		for _, f := range files {
			if dir, err := realDirs(cache, f.dir...); err == nil {
				path := filepath.Join(dir, f.name)
				if info, err := os.Lstat(path); err == nil && info.IsDir() {
					_ = os.RemoveAll(path)
				} else {
					_ = os.Remove(path)
				}
			}
		}
	}
	for _, f := range files {
		if f.data == "" {
			continue
		}
		dir, err := realDirs(cache, f.dir...)
		if err == nil {
			err = writeReal(dir, f.name, []byte(f.data))
		}
		if err != nil {
			remove()
			return func() {}, err
		}
	}
	return remove, nil
}

// RegistryMask is the text that stands for a registry token wherever an output is kept.
const RegistryMask = "[registry token]"

// registrySecrets answers each token in the forms an output may carry it: as given, URL-encoded, and in basic
// authentication's base64 (with and without its user name).
func registrySecrets(rs []serviceapi.RegistryV1) []string {
	out := []string{}
	for _, r := range rs {
		forms := []string{r.Token, url.QueryEscape(r.Token), url.PathEscape(r.Token), escapeAll(r.Token), base64.StdEncoding.EncodeToString([]byte(r.Token))}
		if r.Username != "" {
			userinfo := url.UserPassword(r.Username, r.Token).String()
			forms = append(forms, base64.StdEncoding.EncodeToString([]byte(r.Username+":"+r.Token)), userinfo, strings.TrimPrefix(userinfo, url.User(r.Username).String()+":"))
		}
		for _, f := range forms {
			if f != "" && !contains(out, f) {
				out = append(out, f)
			}
		}
	}
	return out
}

// escapeAll percent-encodes every byte of s but the unreserved ones, the strictest form a tool may print a token in.
func escapeAll(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.IndexByte("-._~", c) >= 0 {
			b.WriteByte(c)
		} else {
			b.WriteString("%" + strings.ToUpper(strconv.FormatInt(int64(c)>>4, 16)+strconv.FormatInt(int64(c)&15, 16)))
		}
	}
	return b.String()
}

// maskRegistries replaces every form of every token in text with RegistryMask, the longest first so a form holding
// another is masked whole.
func maskRegistries(text string, secrets []string) string {
	if len(secrets) == 0 {
		return text
	}
	ordered := append([]string(nil), secrets...)
	for i := range ordered {
		for j := i + 1; j < len(ordered); j++ {
			if len(ordered[j]) > len(ordered[i]) {
				ordered[i], ordered[j] = ordered[j], ordered[i]
			}
		}
	}
	for _, s := range ordered {
		text = strings.ReplaceAll(text, s, RegistryMask)
	}
	return text
}

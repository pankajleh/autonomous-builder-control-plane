package serviceapi

import (
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Private registries (Repo C design note IMPROVE_EXISTING_PRODUCTS §15.9, F5b): a project's own npm, Maven or Python
// registry, sent with one stack or change job alone. ABCP lets the job's sandbox reach its name and writes the tool's
// settings for that job; the token is kept in memory with the job, never in its record, evidence, answers or logs.

// RegistryV1 is one registry. Use is "all" (it mirrors the public registry; every package comes through it) or "own"
// (the company's packages only; the public registry stays). Scope names an npm registry's packages ("@acme") and is
// required for npm's "own"; nothing else takes it. Username is the basic authentication user Maven and Python send.
type RegistryV1 struct {
	Kind     string `json:"kind"`
	Use      string `json:"use"`
	Scope    string `json:"scope,omitempty"`
	URL      string `json:"url"`
	Username string `json:"username,omitempty"`
	Token    string `json:"token"`
}

// MaxRegistries is how many registries one job takes.
const MaxRegistries = 3

var (
	registryHost  = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)
	registryScope = regexp.MustCompile(`^@[a-z0-9][a-z0-9._-]{0,213}$`)
	registryUser  = regexp.MustCompile(`^[A-Za-z0-9._@+-]{1,128}$`)
	registryPath  = regexp.MustCompile(`^(?:/[A-Za-z0-9._~%+-]+)*/?$`)
	registryToken = regexp.MustCompile(`^[\x21-\x7e]+$`)
)

// Valid reports a registry ABCP may give a job: https on 443, a name and never an address, a plain path, no user,
// password, query or fragment in the address, and a token of printable characters that can't break a settings file.
func (r RegistryV1) Valid() bool {
	if r.Kind != "npm" && r.Kind != "maven" && r.Kind != "python" || r.Use != "all" && r.Use != "own" {
		return false
	}
	if r.Kind == "npm" && r.Use == "own" {
		if !registryScope.MatchString(r.Scope) {
			return false
		}
	} else if r.Scope != "" {
		return false
	}
	if r.Kind == "npm" && r.Username != "" || r.Kind != "npm" && !registryUser.MatchString(r.Username) {
		return false
	}
	if len(r.Token) < 8 || len(r.Token) > 4096 || !registryToken.MatchString(r.Token) || strings.ContainsAny(r.Token, `"'\<>&$`+"`") {
		return false
	}
	return r.Host() != ""
}

// Host answers the registry's host name, or "" for an address ABCP refuses.
func (r RegistryV1) Host() string {
	if len(r.URL) > 300 || !utf8.ValidString(r.URL) {
		return ""
	}
	u, err := url.Parse(r.URL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" ||
		strings.Contains(r.URL, "#") || u.Port() != "" && u.Port() != "443" || !registryPath.MatchString(u.EscapedPath()) || strings.Contains(u.Path, "..") {
		return ""
	}
	host := u.Hostname()
	if _, err := netip.ParseAddr(host); err == nil || host != strings.ToLower(host) || len(host) > 253 || !registryHost.MatchString(host) {
		return ""
	}
	return host
}

// validRegistries reports a job's registries: at most MaxRegistries, each valid, no address twice.
func validRegistries(rs []RegistryV1) bool {
	if len(rs) > MaxRegistries {
		return false
	}
	seen := map[string]bool{}
	for _, r := range rs {
		if !r.Valid() || seen[r.URL] {
			return false
		}
		seen[r.URL] = true
	}
	return true
}

// RegistriesDigestPart is what a job's body digest keeps of its registries: everything but the tokens.
func RegistriesDigestPart(rs []RegistryV1) []string {
	out := []string{}
	for _, r := range rs {
		out = append(out, r.Kind, r.Use, r.Scope, r.URL, r.Username)
	}
	return out
}

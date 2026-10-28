package serviceapi

import (
	"strings"
	"testing"
)

func TestARegistryIsHTTPSOnANameWithAPlainPathAndASafeToken(t *testing.T) {
	ok := RegistryV1{Kind: "npm", Use: "own", Scope: "@acme", URL: "https://acme-123456789012.d.codeartifact.ap-south-1.amazonaws.com/npm/acme/", Token: "eyJ2ZXIiOjEsImlzdSI6MTcz.abc-_def"}
	if !ok.Valid() || ok.Host() != "acme-123456789012.d.codeartifact.ap-south-1.amazonaws.com" {
		t.Fatalf("valid registry refused: %q", ok.Host())
	}
	maven := RegistryV1{Kind: "maven", Use: "all", URL: "https://nexus.acme.example:443/repository/maven-public", Username: "build.bot", Token: "Zm9vYmFyYmF6+/="}
	python := RegistryV1{Kind: "python", Use: "own", URL: "https://pypi.acme.example/simple", Username: "aws", Token: "abcdefgh"}
	if !maven.Valid() || !python.Valid() {
		t.Fatal("maven or python refused")
	}
	change := func(r RegistryV1, edit func(*RegistryV1)) RegistryV1 { edit(&r); return r }
	for name, r := range map[string]RegistryV1{
		"http":            change(ok, func(r *RegistryV1) { r.URL = "http://npm.acme.example/" }),
		"an address":      change(ok, func(r *RegistryV1) { r.URL = "https://10.0.0.5/npm/" }),
		"IPv6":            change(ok, func(r *RegistryV1) { r.URL = "https://[::1]/npm/" }),
		"another port":    change(ok, func(r *RegistryV1) { r.URL = "https://npm.acme.example:8443/" }),
		"a user":          change(ok, func(r *RegistryV1) { r.URL = "https://me:pw@npm.acme.example/" }),
		"a query":         change(ok, func(r *RegistryV1) { r.URL = "https://npm.acme.example/?x=1" }),
		"a fragment":      change(ok, func(r *RegistryV1) { r.URL = "https://npm.acme.example/#x" }),
		"dot dot":         change(ok, func(r *RegistryV1) { r.URL = "https://npm.acme.example/a/../b" }),
		"upper case":      change(ok, func(r *RegistryV1) { r.URL = "https://NPM.acme.example/" }),
		"no dot":          change(ok, func(r *RegistryV1) { r.URL = "https://localhost/" }),
		"a quote in path": change(ok, func(r *RegistryV1) { r.URL = "https://npm.acme.example/a'b" }),
		"too long":        change(ok, func(r *RegistryV1) { r.URL = "https://npm.acme.example/" + strings.Repeat("a", 300) }),
		"no scope":        change(ok, func(r *RegistryV1) { r.Scope = "" }),
		"bad scope":       change(ok, func(r *RegistryV1) { r.Scope = "acme" }),
		"npm user":        change(ok, func(r *RegistryV1) { r.Username = "me" }),
		"scope for all":   change(maven, func(r *RegistryV1) { r.Scope = "@acme" }),
		"no maven user":   change(maven, func(r *RegistryV1) { r.Username = "" }),
		"short token":     change(ok, func(r *RegistryV1) { r.Token = "abc" }),
		"space in token":  change(ok, func(r *RegistryV1) { r.Token = "abc defghij" }),
		"quote in token":  change(ok, func(r *RegistryV1) { r.Token = "abcdefgh'" }),
		"dollar in token": change(ok, func(r *RegistryV1) { r.Token = "abcdefgh$x" }),
		"newline":         change(ok, func(r *RegistryV1) { r.Token = "abcdefgh\nregistry=https://evil.example/" }),
		"another kind":    change(ok, func(r *RegistryV1) { r.Kind = "nuget" }),
		"another use":     change(ok, func(r *RegistryV1) { r.Use = "some" }),
	} {
		if r.Valid() {
			t.Errorf("%s: accepted", name)
		}
	}
	job := StackJobCreateV1{RequestID: "req-12345678", Source: "src-" + strings.Repeat("a", 32), CacheKey: strings.Repeat("d", 64)}
	if !job.Valid() {
		t.Fatal("the base request is refused")
	}
	job.Registries = []RegistryV1{ok, maven, python}
	if !job.Valid() {
		t.Fatal("three registries refused")
	}
	job.Registries = []RegistryV1{ok, maven, python, change(python, func(r *RegistryV1) { r.URL = "https://pypi2.acme.example/simple" })}
	if job.Valid() {
		t.Fatal("four registries accepted")
	}
	job.Registries = []RegistryV1{python, python}
	if job.Valid() {
		t.Fatal("the same address twice accepted")
	}
	if got := strings.Join(RegistriesDigestPart([]RegistryV1{maven}), "|"); got != "maven|all||https://nexus.acme.example:443/repository/maven-public|build.bot" {
		t.Fatalf("digest part = %q", got)
	}
}

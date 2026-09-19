//go:build linux

package serviceapi

import (
	"encoding/base64"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestProtectedStartupConfigurationAndAuthentication(t *testing.T) {
	directory := t.TempDir()
	token := strings.Repeat("secret-token-", 4)
	tokenPath := writeProtectedTestFile(t, directory, "token", token+"\n")
	authenticator, err := LoadBearerAuthenticator(tokenPath, "service-principal")
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/v1/capabilities", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	principal, err := authenticator.Authenticate(request)
	if err != nil || principal.PrincipalID != "service-principal" || principal.PrincipalType != PrincipalService {
		t.Fatalf("principal = %+v, err=%v", principal, err)
	}
	request.Header.Set("Authorization", "Bearer wrong")
	if _, err := authenticator.Authenticate(request); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("wrong token = %v", err)
	}

	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	keyPath := writeProtectedTestFile(t, directory, "cursor", `{"key_id":"key-v1","key_base64":"`+base64.StdEncoding.EncodeToString(key)+`"}`)
	if _, err := LoadCursorSigner(keyPath); err != nil {
		t.Fatal(err)
	}
	grantsPath := writeProtectedTestFile(t, directory, "grants", `{"principals":[{"principal_id":"service-principal","required_authorities":["repo:approve"],"may_assert_delegated_actor":true}]}`)
	matcher, err := LoadAuthorityMatcher(grantsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := matcher.Match(principal, "repo:approve"); err != nil || !matcher.MayAssertDelegatedActor(principal) {
		t.Fatalf("configured grant was denied: %v", err)
	}
	if len(matcher.Digest()) != 64 {
		t.Fatalf("authority grant digest = %q", matcher.Digest())
	}
	if err := matcher.Match(principal, "repo:other"); !errors.Is(err, ErrAuthorityDenied) {
		t.Fatalf("unknown authority = %v", err)
	}
}

func TestProtectedConfigurationRejectsModesLinksBoundsAndSchemas(t *testing.T) {
	directory := t.TempDir()
	modePath := filepath.Join(directory, "mode")
	if err := os.WriteFile(modePath, []byte(strings.Repeat("x", 32)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBearerAuthenticator(modePath, "principal"); err == nil {
		t.Fatal("insecure token mode accepted")
	}
	if os.Geteuid() == 0 {
		ownerPath := filepath.Join(directory, "owner")
		if err := os.WriteFile(ownerPath, []byte(strings.Repeat("x", 32)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(ownerPath, 1, -1); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadBearerAuthenticator(ownerPath, "principal"); err == nil {
			t.Fatal("foreign-owned token accepted")
		}
	}
	good := writeProtectedTestFile(t, directory, "good", strings.Repeat("x", 32))
	hard := filepath.Join(directory, "hard")
	if err := os.Link(good, hard); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBearerAuthenticator(good, "principal"); err == nil {
		t.Fatal("hard-linked token accepted")
	}
	if err := os.Remove(hard); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(directory, "symlink")
	if err := os.Symlink(good, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBearerAuthenticator(symlink, "principal"); err == nil {
		t.Fatal("symlinked token accepted")
	}
	special := filepath.Join(directory, "special")
	if err := syscall.Mkfifo(special, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBearerAuthenticator(special, "principal"); err == nil {
		t.Fatal("special-file token accepted")
	}
	oversize := writeProtectedTestFile(t, directory, "oversize", strings.Repeat("x", MaxTokenFileBytes+1))
	if _, err := LoadBearerAuthenticator(oversize, "principal"); err == nil {
		t.Fatal("oversized token file accepted")
	}
	duplicateKey := writeProtectedTestFile(t, directory, "duplicate-key", `{"key_id":"one","key_id":"two","key_base64":"ignored"}`)
	if _, err := LoadCursorSigner(duplicateKey); err == nil {
		t.Fatal("duplicate cursor key field accepted")
	}
	unknownGrant := writeProtectedTestFile(t, directory, "unknown-grant", `{"principals":[],"unknown":true}`)
	if _, err := LoadAuthorityMatcher(unknownGrant); err == nil {
		t.Fatal("unknown authority grant field accepted")
	}
	wildcardGrant := writeProtectedTestFile(t, directory, "wildcard-grant", `{"principals":[{"principal_id":"principal","required_authorities":["repo:*"],"may_assert_delegated_actor":false}]}`)
	if _, err := LoadAuthorityMatcher(wildcardGrant); err == nil {
		t.Fatal("wildcard authority accepted")
	}
	if _, err := LoadBearerAuthenticator(filepath.Join(directory, "missing"), "principal"); err == nil {
		t.Fatal("missing token configuration accepted")
	}
}

func TestFinalReopenedConfigurationDescriptorRequiresAllProtectedAttributes(t *testing.T) {
	path := writeProtectedTestFile(t, t.TempDir(), "config", "protected")
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)
	var initial syscall.Stat_t
	if err := syscall.Fstat(fd, &initial); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*syscall.Stat_t){
		"type":  func(stat *syscall.Stat_t) { stat.Mode = stat.Mode&^syscall.S_IFMT | syscall.S_IFIFO },
		"owner": func(stat *syscall.Stat_t) { stat.Uid++ },
		"mode":  func(stat *syscall.Stat_t) { stat.Mode = stat.Mode&^0o7777 | 0o640 },
		"links": func(stat *syscall.Stat_t) { stat.Nlink = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			current := initial
			mutate(&current)
			if validReopenedConfigStat(current, initial) {
				t.Fatal("unsafe final reopened descriptor accepted")
			}
		})
	}
}

func writeProtectedTestFile(t *testing.T, directory, name, contents string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

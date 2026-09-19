// Package serviceapi defines the frozen EP-006 v1 transport, authentication,
// cursor, DTO, and later-track dependency contracts.
package serviceapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/strictjson"
)

const (
	MaxTokenFileBytes = 4 << 10
	MaxCursorKeyBytes = 8 << 10
	MaxGrantFileBytes = 64 << 10
)

var (
	ErrUnauthenticated = errors.New("authentication required")
	ErrAuthorityDenied = errors.New("authority denied")
	ErrUnsafeConfig    = errors.New("unsafe service configuration")
)

var principalPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type PrincipalType string

const (
	PrincipalService  PrincipalType = "service"
	PrincipalUser     PrincipalType = "user"
	PrincipalOperator PrincipalType = "operator"
	PrincipalTest     PrincipalType = "test"
)

type Principal struct {
	PrincipalID   string        `json:"principal_id"`
	PrincipalType PrincipalType `json:"principal_type"`
	AuthnMethod   string        `json:"authn_method"`
}

func ValidatePrincipalID(value string) error {
	if !principalPattern.MatchString(value) {
		return errors.New("invalid principal identifier")
	}
	return nil
}

func validatePrincipal(principal Principal) error {
	if err := ValidatePrincipalID(principal.PrincipalID); err != nil {
		return err
	}
	switch principal.PrincipalType {
	case PrincipalService, PrincipalUser, PrincipalOperator, PrincipalTest:
	default:
		return errors.New("invalid principal type")
	}
	if principal.AuthnMethod == "" || len(principal.AuthnMethod) > 128 || strings.IndexFunc(principal.AuthnMethod, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
		return errors.New("invalid authentication method")
	}
	return nil
}

type Authenticator interface {
	Authenticate(*http.Request) (Principal, error)
}

type BearerAuthenticator struct {
	token     []byte
	principal Principal
}

func LoadBearerAuthenticator(tokenFile, principalID string) (*BearerAuthenticator, error) {
	if err := ValidatePrincipalID(principalID); err != nil {
		return nil, err
	}
	data, err := readProtectedFile(tokenFile, MaxTokenFileBytes)
	if err != nil {
		return nil, ErrUnsafeConfig
	}
	if len(data) > 0 && data[len(data)-1] == '\n' {
		data = data[:len(data)-1]
	}
	if len(data) < 32 || len(data) > 512 {
		return nil, errors.New("bearer token length is outside bounds")
	}
	for _, value := range data {
		if value == 0 || value == ' ' || value == '\t' || value == '\r' || value == '\n' || value == '\v' || value == '\f' {
			return nil, errors.New("bearer token contains forbidden bytes")
		}
	}
	return &BearerAuthenticator{token: append([]byte(nil), data...), principal: Principal{PrincipalID: principalID, PrincipalType: PrincipalService, AuthnMethod: "bearer-token-v1"}}, nil
}

func (a *BearerAuthenticator) Authenticate(request *http.Request) (Principal, error) {
	if a == nil || request == nil {
		return Principal{}, ErrUnauthenticated
	}
	header := request.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) || len(header) == len(prefix) {
		return Principal{}, ErrUnauthenticated
	}
	presented := []byte(header[len(prefix):])
	var configuredPadded [512]byte
	var presentedPadded [512]byte
	copy(configuredPadded[:], a.token)
	copy(presentedPadded[:], presented)
	match := subtle.ConstantTimeCompare(configuredPadded[:], presentedPadded[:]) & subtle.ConstantTimeEq(int32(len(a.token)), int32(len(presented)))
	if match != 1 {
		return Principal{}, ErrUnauthenticated
	}
	return a.principal, nil
}

type CursorKeyFileV1 struct {
	KeyID     string `json:"key_id"`
	KeyBase64 string `json:"key_base64"`
}

func LoadCursorSigner(path string) (*CursorSigner, error) {
	data, err := readProtectedFile(path, MaxCursorKeyBytes)
	if err != nil {
		return nil, ErrUnsafeConfig
	}
	var file CursorKeyFileV1
	if err := decodeStrictJSON(data, &file); err != nil {
		return nil, errors.New("invalid cursor key configuration")
	}
	if err := ValidatePrincipalID(file.KeyID); err != nil {
		return nil, errors.New("invalid cursor key identifier")
	}
	key, err := base64.StdEncoding.Strict().DecodeString(file.KeyBase64)
	if err != nil || len(key) < 32 || len(key) > 64 {
		return nil, errors.New("invalid cursor key material")
	}
	return NewCursorSigner(file.KeyID, key)
}

type AuthorityGrantFileV1 struct {
	Principals []AuthorityGrantV1 `json:"principals"`
}

type AuthorityGrantV1 struct {
	PrincipalID             string   `json:"principal_id"`
	RequiredAuthorities     []string `json:"required_authorities"`
	MayAssertDelegatedActor bool     `json:"may_assert_delegated_actor"`
}

type AuthorityMatcher struct {
	grants map[string]authorityGrant
	digest string
}

type authorityGrant struct {
	authorities map[string]struct{}
	mayDelegate bool
}

func LoadAuthorityMatcher(path string) (*AuthorityMatcher, error) {
	data, err := readProtectedFile(path, MaxGrantFileBytes)
	if err != nil {
		return nil, ErrUnsafeConfig
	}
	var file AuthorityGrantFileV1
	if err := decodeStrictJSON(data, &file); err != nil {
		return nil, errors.New("invalid authority grant configuration")
	}
	if file.Principals == nil || len(file.Principals) > 16 {
		return nil, errors.New("authority grant principal count is outside bounds")
	}
	digest := sha256.Sum256(data)
	matcher := &AuthorityMatcher{grants: make(map[string]authorityGrant, len(file.Principals)), digest: hex.EncodeToString(digest[:])}
	for _, principal := range file.Principals {
		if err := ValidatePrincipalID(principal.PrincipalID); err != nil {
			return nil, errors.New("invalid authority grant principal")
		}
		if _, duplicate := matcher.grants[principal.PrincipalID]; duplicate {
			return nil, errors.New("duplicate authority grant principal")
		}
		if principal.RequiredAuthorities == nil || len(principal.RequiredAuthorities) > 256 {
			return nil, errors.New("authority grant count is outside bounds")
		}
		grant := authorityGrant{authorities: make(map[string]struct{}, len(principal.RequiredAuthorities)), mayDelegate: principal.MayAssertDelegatedActor}
		for _, authority := range principal.RequiredAuthorities {
			if !validAuthority(authority) {
				return nil, errors.New("invalid required authority")
			}
			if _, duplicate := grant.authorities[authority]; duplicate {
				return nil, errors.New("duplicate required authority")
			}
			grant.authorities[authority] = struct{}{}
		}
		matcher.grants[principal.PrincipalID] = grant
	}
	return matcher, nil
}

func (m *AuthorityMatcher) Digest() string {
	if m == nil {
		return ""
	}
	return m.digest
}

func validAuthority(value string) bool {
	return value != "" && len(value) <= 256 && !strings.ContainsAny(value, "*?[]\x00\r\n")
}

func (m *AuthorityMatcher) Match(principal Principal, requiredAuthority string) error {
	if !m.valid() || validatePrincipal(principal) != nil || !validAuthority(requiredAuthority) {
		return ErrAuthorityDenied
	}
	grant, ok := m.grants[principal.PrincipalID]
	if !ok {
		return ErrAuthorityDenied
	}
	if _, ok := grant.authorities[requiredAuthority]; !ok {
		return ErrAuthorityDenied
	}
	return nil
}

func (m *AuthorityMatcher) MayAssertDelegatedActor(principal Principal) bool {
	if !m.valid() || validatePrincipal(principal) != nil {
		return false
	}
	grant, ok := m.grants[principal.PrincipalID]
	return ok && grant.mayDelegate
}

func (m *AuthorityMatcher) valid() bool {
	if m == nil || m.grants == nil || len(m.digest) != 64 {
		return false
	}
	_, err := hex.DecodeString(m.digest)
	return err == nil && strings.ToLower(m.digest) == m.digest
}

func decodeStrictJSON(data []byte, target any) error {
	return strictjson.Decode(data, target)
}

func rejectDuplicateJSONFields(data []byte) error {
	var value json.RawMessage
	return strictjson.Decode(data, &value)
}

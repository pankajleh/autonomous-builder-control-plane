package serviceapi

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
)

const (
	MaxCursorTokenBytes = 4 << 10
	MaxCursorLifetime   = 15 * time.Minute
	CursorKindCatalog   = "catalog"
	CursorKindLedger    = "ledger"
)

var (
	ErrInvalidCursor      = errors.New("invalid cursor")
	ErrCursorEpochChanged = errors.New("cursor epoch changed")
)

// CursorEnvelopeV1 is a strict-canonical, stateless signed cursor. MAC covers
// every other field exactly as emitted by encoding/json for this struct.
type CursorEnvelopeV1 struct {
	SchemaVersion  string          `json:"schema_version"`
	KeyID          string          `json:"key_id"`
	Kind           string          `json:"kind"`
	FilterIdentity string          `json:"filter_identity"`
	IssuedAt       string          `json:"issued_at"`
	ExpiresAt      string          `json:"expires_at"`
	Payload        json.RawMessage `json:"payload"`
	MAC            string          `json:"mac"`
}

type cursorUnsignedV1 struct {
	SchemaVersion  string          `json:"schema_version"`
	KeyID          string          `json:"key_id"`
	Kind           string          `json:"kind"`
	FilterIdentity string          `json:"filter_identity"`
	IssuedAt       string          `json:"issued_at"`
	ExpiresAt      string          `json:"expires_at"`
	Payload        json.RawMessage `json:"payload"`
}

type CatalogCursorPayloadV1 struct {
	LastRunID            string `json:"last_run_id"`
	CatalogSchemaVersion string `json:"catalog_schema_version"`
	Filters              string `json:"filters"`
}

type CursorSigner struct {
	keyID string
	key   []byte
	now   func() time.Time
}

func NewCursorSigner(keyID string, key []byte) (*CursorSigner, error) {
	return NewCursorSignerWithClock(keyID, key, time.Now)
}

func NewCursorSignerWithClock(keyID string, key []byte, now func() time.Time) (*CursorSigner, error) {
	if ValidatePrincipalID(keyID) != nil || len(key) < 32 || len(key) > 64 || now == nil {
		return nil, errors.New("invalid cursor signer configuration")
	}
	return &CursorSigner{keyID: keyID, key: append([]byte(nil), key...), now: now}, nil
}

func (s *CursorSigner) KeyID() string {
	if s == nil {
		return ""
	}
	return s.keyID
}

func (s *CursorSigner) valid() bool {
	return s != nil && ValidatePrincipalID(s.keyID) == nil && len(s.key) >= 32 && len(s.key) <= 64 && s.now != nil
}

func (s *CursorSigner) Sign(kind, filterIdentity string, payload any, issuedAt, expiresAt time.Time) (string, error) {
	if !s.valid() || (kind != CursorKindCatalog && kind != CursorKindLedger) || len(filterIdentity) > 1024 {
		return "", ErrInvalidCursor
	}
	issuedAt = issuedAt.UTC()
	expiresAt = expiresAt.UTC()
	current := s.now().UTC()
	if issuedAt.After(current) || expiresAt.Before(issuedAt) || expiresAt.Sub(issuedAt) > MaxCursorLifetime || expiresAt.After(current.Add(MaxCursorLifetime)) {
		return "", ErrInvalidCursor
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil || len(payloadBytes) == 0 || len(payloadBytes) > 2048 {
		return "", ErrInvalidCursor
	}
	unsigned := cursorUnsignedV1{
		SchemaVersion: "CursorEnvelopeV1", KeyID: s.keyID, Kind: kind,
		FilterIdentity: filterIdentity, IssuedAt: issuedAt.Format(time.RFC3339Nano),
		ExpiresAt: expiresAt.Format(time.RFC3339Nano), Payload: payloadBytes,
	}
	unsignedBytes, _ := json.Marshal(unsigned)
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write(unsignedBytes)
	envelope := CursorEnvelopeV1{
		SchemaVersion: unsigned.SchemaVersion, KeyID: unsigned.KeyID, Kind: unsigned.Kind,
		FilterIdentity: unsigned.FilterIdentity, IssuedAt: unsigned.IssuedAt,
		ExpiresAt: unsigned.ExpiresAt, Payload: unsigned.Payload,
		MAC: base64.RawURLEncoding.EncodeToString(mac.Sum(nil)),
	}
	encoded, _ := json.Marshal(envelope)
	token := base64.RawURLEncoding.EncodeToString(encoded)
	if len(token) > MaxCursorTokenBytes {
		return "", ErrInvalidCursor
	}
	return token, nil
}

func (s *CursorSigner) Verify(token, expectedKind, expectedFilterIdentity string, target any) error {
	if !s.valid() || token == "" || len(token) > MaxCursorTokenBytes || (expectedKind != CursorKindCatalog && expectedKind != CursorKindLedger) {
		return ErrInvalidCursor
	}
	encoded, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(encoded) == 0 || len(encoded) > MaxCursorTokenBytes || rejectDuplicateJSONFields(encoded) != nil {
		return ErrInvalidCursor
	}
	var envelope CursorEnvelopeV1
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return ErrInvalidCursor
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ErrInvalidCursor
	}
	canonical, _ := json.Marshal(envelope)
	if !bytes.Equal(canonical, encoded) || envelope.SchemaVersion != "CursorEnvelopeV1" {
		return ErrInvalidCursor
	}
	if envelope.KeyID != s.keyID {
		return ErrCursorEpochChanged
	}
	if envelope.Kind != expectedKind || envelope.FilterIdentity != expectedFilterIdentity {
		return ErrInvalidCursor
	}
	issued, err := time.Parse(time.RFC3339Nano, envelope.IssuedAt)
	if err != nil || envelope.IssuedAt != issued.UTC().Format(time.RFC3339Nano) || issued.After(s.now().UTC()) {
		return ErrInvalidCursor
	}
	expires, err := time.Parse(time.RFC3339Nano, envelope.ExpiresAt)
	if err != nil || envelope.ExpiresAt != expires.UTC().Format(time.RFC3339Nano) || expires.Before(issued) || expires.Sub(issued) > MaxCursorLifetime || !s.now().UTC().Before(expires) {
		return ErrInvalidCursor
	}
	unsigned := cursorUnsignedV1{
		SchemaVersion: envelope.SchemaVersion, KeyID: envelope.KeyID, Kind: envelope.Kind,
		FilterIdentity: envelope.FilterIdentity, IssuedAt: envelope.IssuedAt,
		ExpiresAt: envelope.ExpiresAt, Payload: envelope.Payload,
	}
	unsignedBytes, _ := json.Marshal(unsigned)
	presentedMAC, err := base64.RawURLEncoding.Strict().DecodeString(envelope.MAC)
	if err != nil {
		return ErrInvalidCursor
	}
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write(unsignedBytes)
	if !hmac.Equal(presentedMAC, mac.Sum(nil)) {
		return ErrInvalidCursor
	}
	if err := decodeStrictJSON(envelope.Payload, target); err != nil {
		return ErrInvalidCursor
	}
	return nil
}

func (s *CursorSigner) SignCatalog(lastRunID, filters string, issuedAt time.Time) (string, error) {
	if lastRunID != "" && runtimecatalog.ValidateIdentifier(lastRunID) != nil {
		return "", ErrInvalidCursor
	}
	payload := CatalogCursorPayloadV1{LastRunID: lastRunID, CatalogSchemaVersion: runtimecatalog.CatalogSchemaV1, Filters: filters}
	return s.Sign(CursorKindCatalog, filters, payload, issuedAt, issuedAt.Add(MaxCursorLifetime))
}

func (s *CursorSigner) VerifyCatalog(token, filters string) (CatalogCursorPayloadV1, error) {
	var payload CatalogCursorPayloadV1
	if err := s.Verify(token, CursorKindCatalog, filters, &payload); err != nil {
		return CatalogCursorPayloadV1{}, err
	}
	if payload.CatalogSchemaVersion != runtimecatalog.CatalogSchemaV1 || payload.Filters != filters || (payload.LastRunID != "" && runtimecatalog.ValidateIdentifier(payload.LastRunID) != nil) {
		return CatalogCursorPayloadV1{}, ErrInvalidCursor
	}
	return payload, nil
}

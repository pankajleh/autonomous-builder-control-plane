package serviceapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestCatalogCursorIntegrityEpochKindExpiryAndBounds(t *testing.T) {
	now := time.Date(2026, 9, 13, 4, 5, 6, 0, time.UTC)
	clock := now
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	signer, err := NewCursorSignerWithClock("key-v1", key, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	token, err := signer.SignCatalog("run-a", "runs-v1:none", now)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := signer.VerifyCatalog(token, "runs-v1:none")
	if err != nil || payload.LastRunID != "run-a" {
		t.Fatalf("payload = %+v, err=%v", payload, err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatal(err)
	}
	var envelope CursorEnvelopeV1
	if err := json.Unmarshal(decoded, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Payload[0] ^= 1
	tampered, _ := json.Marshal(envelope)
	if _, err := signer.VerifyCatalog(base64.RawURLEncoding.EncodeToString(tampered), "runs-v1:none"); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("tampered cursor = %v", err)
	}
	other, _ := NewCursorSignerWithClock("key-v2", key, func() time.Time { return clock })
	if _, err := other.VerifyCatalog(token, "runs-v1:none"); !errors.Is(err, ErrCursorEpochChanged) {
		t.Fatalf("changed epoch = %v", err)
	}
	var catalogPayload CatalogCursorPayloadV1
	if err := signer.Verify(token, CursorKindLedger, "runs-v1:none", &catalogPayload); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("wrong route kind = %v", err)
	}
	clock = now.Add(MaxCursorLifetime)
	if _, err := signer.VerifyCatalog(token, "runs-v1:none"); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("expired cursor = %v", err)
	}
	if _, err := signer.VerifyCatalog(string(make([]byte, MaxCursorTokenBytes+1)), "runs-v1:none"); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("oversized cursor = %v", err)
	}
}

func TestCursorRejectsNonCanonicalEnvelopeAndExcessLifetime(t *testing.T) {
	now := time.Now().UTC()
	key := make([]byte, 32)
	signer, err := NewCursorSignerWithClock("key", key, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer.Sign(CursorKindCatalog, "f", CatalogCursorPayloadV1{}, now, now.Add(MaxCursorLifetime+time.Nanosecond)); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("excess lifetime = %v", err)
	}
	noncanonical := base64.RawURLEncoding.EncodeToString([]byte(`{ "schema_version":"CursorEnvelopeV1" }`))
	if err := signer.Verify(noncanonical, CursorKindCatalog, "", &CatalogCursorPayloadV1{}); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("noncanonical cursor = %v", err)
	}
}

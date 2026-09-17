package postgres

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

var (
	authorityDomainPatternV1 = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	postgresIdentifierV1     = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
	lowerHex64V1             = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func validateAuthorityDomainV1(value string) error {
	if !authorityDomainPatternV1.MatchString(value) {
		return fmt.Errorf("authority domain is invalid")
	}
	return nil
}

func validateDBIdentityV1(value string) error {
	if !lowerHex64V1.MatchString(value) {
		return errors.New("database identity must be 64 lowercase hexadecimal characters")
	}
	return nil
}

func validatePostgresIdentifierV1(value string) error {
	if !postgresIdentifierV1.MatchString(value) {
		return errors.New("PostgreSQL role or database name is not a strict unquoted identifier")
	}
	return nil
}

func validateCanonicalJSONV1(data []byte, maximum int) error {
	if len(data) == 0 || len(data) > maximum || !json.Valid(data) {
		return errors.New("canonical JSON is empty, oversized, or invalid")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil || !bytes.Equal(compact.Bytes(), data) {
		return errors.New("JSON bytes contain non-canonical whitespace")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := validateJSONValueV1(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("canonical JSON has trailing input")
	}
	return nil
}

func validateJSONValueV1(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode canonical JSON: %w", err)
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("canonical JSON object key is not a string")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("canonical JSON object key %q is duplicated", key)
				}
				seen[key] = struct{}{}
				if err := validateJSONValueV1(decoder); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("canonical JSON object is unterminated")
			}
		case '[':
			for decoder.More() {
				if err := validateJSONValueV1(decoder); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("canonical JSON array is unterminated")
			}
		default:
			return errors.New("canonical JSON has an unexpected delimiter")
		}
	case json.Number:
		if strings.ContainsAny(string(value), ".eE") || strings.HasPrefix(string(value), "-") {
			return errors.New("canonical JSON number is not an unsigned integer")
		}
	}
	return nil
}

func sha256HexV1(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func requireDigestV1(data []byte, digest string) error {
	if err := validateDBIdentityV1(digest); err != nil || sha256HexV1(data) != digest {
		return errors.New("canonical bytes disagree with their SHA-256")
	}
	return nil
}

func canonicalSecondV1(value time.Time) time.Time {
	return value.UTC().Truncate(time.Second)
}

func quoteIdentifierV1(value string) (string, error) {
	if err := validatePostgresIdentifierV1(value); err != nil {
		return "", err
	}
	return `"` + value + `"`, nil
}

func quoteLiteralV1(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

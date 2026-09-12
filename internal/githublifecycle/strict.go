package githublifecycle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
)

func validSHA256(value string) bool {
	return len(value) == sha256.Size*2 && isLowerHex(value)
}

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

// strictDecode accepts exactly one canonical JSON value. Re-marshalling is
// intentional: it rejects whitespace, reordered and duplicate object keys,
// aliases, defaulted fields, and every encoding our constructors would not
// themselves produce.
func strictDecode(data []byte, target any) error {
	if len(data) == 0 {
		return errors.New("canonical JSON is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("canonical JSON contains more than one value")
		}
		return err
	}
	return nil
}

func requireCanonical(data, rebuilt []byte) error {
	if !bytes.Equal(data, rebuilt) {
		return errors.New("JSON is not the strict canonical encoding")
	}
	return nil
}

package governance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// ParseCanonicalBounded applies the strict canonical JSON rules with an
// explicit authority-record bound. It is used by V4 records whose frozen
// wire has a bound other than the ordinary one MiB limit.
func ParseCanonicalBounded(data []byte, maximum int, target any) error {
	if maximum < 1 || len(data) == 0 || len(data) > maximum {
		return fmt.Errorf("canonical JSON input is empty or exceeds %d bytes", maximum)
	}
	if err := validateNoDuplicateJSONFields(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("canonical JSON contains trailing data")
	}
	canonical, err := json.Marshal(target)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, canonical) {
		return errors.New("JSON is not strict canonical encoding")
	}
	return nil
}

// validateNoDuplicateJSONFields performs the check encoding/json deliberately
// does not perform. Duplicate keys are forbidden at every object depth even
// when both occurrences would decode to the same Go value.
func validateNoDuplicateJSONFields(data []byte) error {
	if !utf8.Valid(data) {
		return errors.New("canonical JSON is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanCanonicalJSONValue(decoder); err != nil {
		return err
	}
	if token, err := decoder.Token(); err != io.EOF {
		if err != nil {
			return err
		}
		return fmt.Errorf("canonical JSON contains trailing token %v", token)
	}
	return nil
}

func scanCanonicalJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			nameToken, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := nameToken.(string)
			if !ok {
				return errors.New("canonical JSON object name is not a string")
			}
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("canonical JSON contains duplicate field %q", name)
			}
			seen[name] = struct{}{}
			if err := scanCanonicalJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.Join(errors.New("canonical JSON object is not terminated"), err)
		}
	case '[':
		for decoder.More() {
			if err := scanCanonicalJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.Join(errors.New("canonical JSON array is not terminated"), err)
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
	return nil
}

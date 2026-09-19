// Package strictjson decodes bounded controller and service JSON without the
// case-folding and invalid-UTF-8 acceptance of encoding/json's struct matcher.
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

var rawMessageType = reflect.TypeOf(json.RawMessage{})

// Decode accepts one JSON value, rejects duplicate/unknown/case-aliased
// fields, and validates every input byte as UTF-8 before decoding target.
func Decode(data []byte, target any) error {
	targetType := reflect.TypeOf(target)
	if len(data) == 0 || !utf8.Valid(data) || targetType == nil || targetType.Kind() != reflect.Pointer || targetType.Elem().Kind() == reflect.Invalid {
		return errors.New("invalid strict JSON input")
	}
	if err := rejectDuplicateFields(data); err != nil {
		return err
	}
	var raw json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	if err := requireEOF(decoder); err != nil {
		return err
	}
	if err := validateShape(raw, targetType.Elem()); err != nil {
		return err
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return requireEOF(decoder)
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateShape(raw json.RawMessage, target reflect.Type) error {
	for target.Kind() == reflect.Pointer {
		if bytes.Equal(raw, []byte("null")) {
			return nil
		}
		target = target.Elem()
	}
	if target == rawMessageType || target.Kind() == reflect.Interface {
		return nil
	}
	switch target.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return err
		}
		fields := jsonFields(target)
		for name, value := range object {
			field, ok := fields[name]
			if !ok {
				return fmt.Errorf("unknown JSON field %q", name)
			}
			if err := validateShape(value, field); err != nil {
				return fmt.Errorf("JSON field %q: %w", name, err)
			}
		}
	case reflect.Slice, reflect.Array:
		if target == rawMessageType {
			return nil
		}
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for _, value := range values {
			if err := validateShape(value, target.Elem()); err != nil {
				return err
			}
		}
	case reflect.Map:
		if target.Key().Kind() != reflect.String {
			return nil
		}
		var values map[string]json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for _, value := range values {
			if err := validateShape(value, target.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func jsonFields(target reflect.Type) map[string]reflect.Type {
	result := make(map[string]reflect.Type)
	for index := 0; index < target.NumField(); index++ {
		field := target.Field(index)
		if field.PkgPath != "" {
			continue
		}
		tag := field.Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name == "-" {
			continue
		}
		if field.Anonymous && name == "" {
			embedded := field.Type
			for embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}
			if embedded.Kind() == reflect.Struct {
				for embeddedName, embeddedType := range jsonFields(embedded) {
					result[embeddedName] = embeddedType
				}
				continue
			}
		}
		if name == "" {
			name = field.Name
		}
		result[name] = field.Type
	}
	return result
}

func rejectDuplicateFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var parse func() error
	parse = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("invalid JSON object key")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate JSON field %q", key)
				}
				seen[key] = struct{}{}
				if err := parse(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := parse(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("invalid JSON delimiter")
		}
	}
	if err := parse(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

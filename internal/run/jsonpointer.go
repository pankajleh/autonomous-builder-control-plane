package run

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"strconv"
	"strings"
)

// A JSON replacement (Repo C design note PREVIEW_EDITOR.md §12.2): the value at one RFC 6901 pointer in a JSON file
// changes from one value to another, the rest of the file byte for byte as it was. A site-kit website shows the same
// gallery item word for word on two pages, so a text replacement can't say which one; a pointer can.

var (
	errPointerInvalid  = errors.New("the pointer is not valid")
	errPointerNotFound = errors.New("the pointer names no value")
	errPointerTwice    = errors.New("the pointer names more than one value")
	errPointerDiffers  = errors.New("the value there is not the one expected")
	errJSONInvalid     = errors.New("the file is not valid JSON")
)

// parseJSONPointer splits an RFC 6901 pointer into its reference tokens: "/a/b~1c/0" is [a b/c 0]. The whole document
// ("") is refused: a replacement changes one value inside a file, never the file.
func parseJSONPointer(pointer string) ([]string, error) {
	if pointer == "" || pointer[0] != '/' {
		return nil, errPointerInvalid
	}
	parts := strings.Split(pointer[1:], "/")
	for i, part := range parts {
		for j := 0; j < len(part); j++ {
			if part[j] == '~' && (j+1 == len(part) || (part[j+1] != '0' && part[j+1] != '1')) {
				return nil, errPointerInvalid
			}
		}
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
	}
	return parts, nil
}

// jsonPointerSpan answers where, in content, the value the pointer names starts and ends. The pointer must name exactly
// one value: an object with the same key twice on the path is refused, as is a pointer that names nothing.
func jsonPointerSpan(content []byte, pointer string) (int, int, error) {
	target, err := parseJSONPointer(pointer)
	if err != nil {
		return 0, 0, err
	}
	if !json.Valid(content) {
		return 0, 0, errJSONInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(content))
	dec.UseNumber()
	var spans [][2]int
	if err := walkJSON(dec, nil, target, &spans); err != nil {
		return 0, 0, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return 0, 0, errJSONInvalid
	}
	switch len(spans) {
	case 0:
		return 0, 0, errPointerNotFound
	case 1:
		return spans[0][0], spans[0][1], nil
	default:
		return 0, 0, errPointerTwice
	}
}

// walkJSON reads one value at path: the target's own value is recorded (its bytes as the file has them), a value on the
// way to it is entered, and any other value is skipped whole.
func walkJSON(dec *json.Decoder, path, target []string, spans *[][2]int) error {
	for i := range path {
		if path[i] != target[i] {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return errJSONInvalid
			}
			return nil
		}
	}
	if len(path) == len(target) {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return errJSONInvalid
		}
		end := int(dec.InputOffset())
		*spans = append(*spans, [2]int{end - len(raw), end})
		return nil
	}
	token, err := dec.Token()
	if err != nil {
		return errJSONInvalid
	}
	switch token {
	case json.Delim('{'):
		for dec.More() {
			key, err := dec.Token()
			name, ok := key.(string)
			if err != nil || !ok {
				return errJSONInvalid
			}
			if err := walkJSON(dec, append(append([]string(nil), path...), name), target, spans); err != nil {
				return err
			}
		}
	case json.Delim('['):
		for index := 0; dec.More(); index++ {
			if err := walkJSON(dec, append(append([]string(nil), path...), strconv.Itoa(index)), target, spans); err != nil {
				return err
			}
		}
	default:
		return nil // a scalar on the way: the pointer goes further than the document does
	}
	if _, err := dec.Token(); err != nil { // the closing } or ]
		return errJSONInvalid
	}
	return nil
}

// replaceJSONValue changes the value the pointer names from from to to (each one JSON value), the rest of content byte
// for byte as it was. The value there must equal from (as JSON, whatever its spacing); to is written compact.
func replaceJSONValue(content []byte, pointer string, from, to json.RawMessage) ([]byte, error) {
	start, end, err := jsonPointerSpan(content, pointer)
	if err != nil {
		return nil, err
	}
	same, err := jsonEqual(content[start:end], from)
	if err != nil {
		return nil, err
	}
	if !same {
		return nil, errPointerDiffers
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, to); err != nil {
		return nil, fmt.Errorf("the new value: %w", errJSONInvalid)
	}
	updated := make([]byte, 0, len(content)-(end-start)+compact.Len())
	updated = append(updated, content[:start]...)
	updated = append(updated, compact.Bytes()...)
	updated = append(updated, content[end:]...)
	if !json.Valid(updated) {
		return nil, errJSONInvalid
	}
	return updated, nil
}

func jsonEqual(a, b []byte) (bool, error) {
	decode := func(raw []byte) (any, error) {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, errJSONInvalid
		}
		return numbersByValue(v), nil
	}
	va, err := decode(a)
	if err != nil {
		return false, err
	}
	vb, err := decode(b)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(va, vb), nil
}

// numbersByValue makes 1, 1.0 and 1e0 one number, exactly (no float rounding), so two spellings of the same value
// compare equal.
func numbersByValue(v any) any {
	switch t := v.(type) {
	case json.Number:
		if r, ok := new(big.Rat).SetString(t.String()); ok {
			return r.RatString()
		}
		return t.String()
	case map[string]any:
		for k, x := range t {
			t[k] = numbersByValue(x)
		}
	case []any:
		for i, x := range t {
			t[i] = numbersByValue(x)
		}
	}
	return v
}

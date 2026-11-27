package run

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
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

// Several JSON replacements in one file (a template's copy's writes, Repo C design note TEMPLATE_LIBRARY_ROUND_2 §10.5):
// app.config.json takes one per setting, and a preset file is written whole, at the pointer "".

var errPointersNest = errors.New("two pointers in the file nest")

// jsonWrite is one value's replacement in a file: the value the pointer names changes from from to to.
type jsonWrite struct {
	pointer  string
	from, to json.RawMessage
}

// parseJSONPointerOrRoot is parseJSONPointer with "" too: the whole document (RFC 6901), no reference token.
func parseJSONPointerOrRoot(pointer string) ([]string, error) {
	if pointer == "" {
		return []string{}, nil
	}
	return parseJSONPointer(pointer)
}

// replaceJSONValues makes each write in content, at pointers that do not nest (authority.PointersNest), "" being the
// whole document. The value each pointer names must equal its from (as JSON, whatever its spacing); each to is written
// compact in its place, as replaceJSONValue writes one, and every other byte of content stays as it was: the spacing
// and the line end around a whole document's value included.
func replaceJSONValues(content []byte, writes []jsonWrite) ([]byte, error) {
	targets := make([][]string, len(writes))
	for i, write := range writes {
		for _, other := range writes[:i] {
			if authority.PointersNest(other.pointer, write.pointer) {
				return nil, fmt.Errorf("%q and %q: %w", other.pointer, write.pointer, errPointersNest)
			}
		}
		tokens, err := parseJSONPointerOrRoot(write.pointer)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", write.pointer, err)
		}
		targets[i] = tokens
	}
	if !json.Valid(content) {
		return nil, errJSONInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(content))
	dec.UseNumber()
	spans := make([][][2]int, len(writes))
	if err := walkJSONTargets(dec, nil, targets, spans); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errJSONInvalid
	}
	type placed struct {
		start, end int
		value      []byte
	}
	values := make([]placed, 0, len(writes))
	for i, write := range writes {
		switch len(spans[i]) {
		case 0:
			return nil, fmt.Errorf("%q: %w", write.pointer, errPointerNotFound)
		case 1:
		default:
			return nil, fmt.Errorf("%q: %w", write.pointer, errPointerTwice)
		}
		start, end := spans[i][0][0], spans[i][0][1]
		same, err := jsonEqual(content[start:end], write.from)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", write.pointer, err)
		}
		if !same {
			return nil, fmt.Errorf("%q: %w", write.pointer, errPointerDiffers)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, write.to); err != nil {
			return nil, fmt.Errorf("%q: the new value: %w", write.pointer, errJSONInvalid)
		}
		values = append(values, placed{start: start, end: end, value: compact.Bytes()})
	}
	sort.Slice(values, func(i, j int) bool { return values[i].start < values[j].start })
	updated := make([]byte, 0, len(content))
	at := 0
	for _, value := range values {
		if value.start < at {
			return nil, errPointersNest // values that do not nest never overlap
		}
		updated = append(updated, content[at:value.start]...)
		updated = append(updated, value.value...)
		at = value.end
	}
	updated = append(updated, content[at:]...)
	if !json.Valid(updated) {
		return nil, errJSONInvalid
	}
	return updated, nil
}

// walkJSONTargets is walkJSON for several targets at once: the value at a target is recorded in that target's spans
// (its bytes as the file has them), a value on the way to any target is entered, and any other value is skipped whole.
func walkJSONTargets(dec *json.Decoder, path []string, targets [][]string, spans [][][2]int) error {
	onTheWay := false
	for i, target := range targets {
		if len(path) > len(target) || !tokensHavePrefix(target, path) {
			continue
		}
		if len(path) == len(target) {
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return errJSONInvalid
			}
			end := int(dec.InputOffset())
			spans[i] = append(spans[i], [2]int{end - len(raw), end})
			return nil
		}
		onTheWay = true
	}
	if !onTheWay {
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return errJSONInvalid
		}
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
			if err := walkJSONTargets(dec, append(append([]string(nil), path...), name), targets, spans); err != nil {
				return err
			}
		}
	case json.Delim('['):
		for index := 0; dec.More(); index++ {
			if err := walkJSONTargets(dec, append(append([]string(nil), path...), strconv.Itoa(index)), targets, spans); err != nil {
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

func tokensHavePrefix(tokens, prefix []string) bool {
	for i := range prefix {
		if tokens[i] != prefix[i] {
			return false
		}
	}
	return true
}

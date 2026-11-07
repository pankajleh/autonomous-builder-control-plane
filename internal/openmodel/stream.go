package openmodel

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
)

// The relay's fixes to a streamed answer of POST <base>/responses (compat
// stop_at_first_call and custom_tool_as_function), for gpt-oss-120b on
// Together (Repo C OPEN_MODEL_ENGINES, the diagnosis of 2026-11-07, proved
// on the server by a stand-in relay: Codex wrote hello.txt, then made two
// real apply_patch edits).
//
// How Together streams such an answer: an output_item.added for each item
// as the model starts it, the call's arguments as deltas, and every item's
// .done held until the very end, where they no longer match the deltas
// (names, arguments and call ids shifted between calls). So the relay
// builds a call's end from its added item and its deltas, never from
// Together's .done. Every event it does not change goes to Codex byte for
// byte as it came, as soon as it came.

// streamFix is what the relay does to one streamed answer.
type streamFix struct {
	// stop ends the answer at its first call (stop_at_first_call).
	stop bool
	// custom are the custom tools the request offered as functions
	// (custom_tool_as_function), whose calls go back as custom_tool_call.
	custom []string
	ids    ResponseIDs

	items    map[int]*streamItem
	order    []int
	call     *streamItem // the answer's first call, with stop
	response object      // id, object, created_at and model of the answer
	usage    json.RawMessage
	next     int // the sequence number after the last one seen
	// through passes every later event as it comes: an error came while
	// the first call's arguments were not complete JSON, or the provider's
	// own response.completed has gone.
	through bool
	ended   bool

	// What the log says.
	cut       string // what ended the answer at its call: next_item, error, end
	converted int    // calls turned back into custom_tool_call
	visible   int    // bytes of text, arguments and input the model streamed
	completed bool   // a response.completed (the provider's or the relay's) went to Codex
}

// streamItem is one output item of the answer.
type streamItem struct {
	index int
	added object // the item as output_item.added gave it
	// convert: a function_call of a custom tool, held and sent back as a
	// custom_tool_call once it is complete.
	convert bool
	text    strings.Builder // output_text deltas
	args    strings.Builder // function_call_arguments deltas
	input   strings.Builder // custom_tool_call_input deltas
	// doneArgs and final are what the provider's arguments.done and
	// output_item.done gave, used only when no delta came.
	doneArgs *string
	final    object
	// sent is the item as its output_item.done went to Codex.
	sent json.RawMessage
}

func newStreamFix(stop bool, custom []string, ids ResponseIDs) *streamFix {
	if ids == nil {
		ids = randomID
	}
	return &streamFix{stop: stop, custom: custom, ids: ids, items: map[int]*streamItem{}}
}

// active reports whether the fix can change anything in an answer.
func (f *streamFix) active() bool { return f != nil && (f.stop || len(f.custom) > 0) }

// sseEvent is one event of the stream: its bytes as they came (its lines
// and the blank line after them), and its data's type and fields.
type sseEvent struct {
	raw  []byte
	kind string
	data object // nil when the data is not a JSON object
}

func parseEvent(raw []byte) sseEvent {
	event := sseEvent{raw: raw}
	var name string
	var data []string
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\r\n"), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if value, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(value, " "))
		} else if value, ok := strings.CutPrefix(line, "event:"); ok {
			name = strings.TrimSpace(value)
		}
	}
	if len(data) > 0 {
		if fields, err := parseObject([]byte(strings.Join(data, "\n"))); err == nil {
			event.data = fields
		}
	}
	event.kind = event.data.text("type")
	if event.kind == "" {
		event.kind = name
	}
	return event
}

// errEventTooLarge: one event of the provider's stream passed MaxRelayBody.
var errEventTooLarge = errors.New("an event larger than the relay takes")

// readEvent answers the next event's bytes, the last one even without its
// blank line, or the reader's error (io.EOF at the end).
func readEvent(reader *bufio.Reader) ([]byte, error) {
	var block []byte
	for {
		line, err := reader.ReadBytes('\n')
		block = append(block, line...)
		if len(block) > MaxRelayBody {
			return nil, errEventTooLarge
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(block) > 0 {
				return block, nil
			}
			return nil, err
		}
		if len(bytes.TrimRight(line, "\r\n")) == 0 {
			return block, nil
		}
	}
}

// copyStream reads the provider's stream event by event and writes to w what
// the fix answers for each, flushing after each write, until the stream ends
// or the fix ends the answer (the caller then closes the provider's stream).
// It answers a write's error as errClientGone, or the reader's error.
func (f *streamFix) copyStream(w io.Writer, flush func(), upstream io.Reader) error {
	reader := bufio.NewReaderSize(upstream, 32<<10)
	write := func(events [][]byte) error {
		for _, event := range events {
			if _, err := w.Write(event); err != nil {
				return errClientGone
			}
		}
		if len(events) > 0 && flush != nil {
			flush()
		}
		return nil
	}
	for !f.ended {
		raw, err := readEvent(reader)
		if err != nil {
			// The stream ended, or broke: a first call complete by then ends
			// the answer as an error would.
			if writeErr := write(f.end()); writeErr != nil {
				return writeErr
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := write(f.event(parseEvent(raw))); err != nil {
			return err
		}
	}
	return nil
}

var errClientGone = errors.New("the client went away")

// event answers what Codex gets for one event of the provider's.
func (f *streamFix) event(e sseEvent) [][]byte {
	if number, ok := intField(e.data, "sequence_number"); ok && number >= f.next {
		f.next = number + 1
	}
	if raw, ok := e.data.get("response"); ok {
		f.noteResponse(raw)
	}
	if delta := e.data.text("delta"); delta != "" {
		f.visible += len(delta)
	}
	if f.through {
		return [][]byte{e.raw}
	}
	index, _ := intField(e.data, "output_index")
	entry := f.items[index]
	pass := [][]byte{e.raw}
	switch e.kind {
	case "response.output_item.added":
		if f.call != nil && index != f.call.index {
			return f.cutAtCall("next_item")
		}
		raw, _ := e.data.get("item")
		added, err := parseObject(raw)
		if err != nil || entry != nil {
			return pass
		}
		entry = &streamItem{index: index, added: added}
		f.items[index] = entry
		f.order = append(f.order, index)
		kind := added.text("type")
		entry.convert = kind == "function_call" && slices.Contains(f.custom, added.text("name"))
		if f.stop && (kind == "function_call" || kind == "custom_tool_call") {
			f.call = entry
		}
		if entry.convert {
			return nil
		}
		return pass
	case "response.output_text.delta":
		if entry != nil {
			entry.text.WriteString(e.data.text("delta"))
		}
		return pass
	case "response.function_call_arguments.delta":
		if entry == nil {
			return pass
		}
		entry.args.WriteString(e.data.text("delta"))
		if entry.convert {
			return nil
		}
		return pass
	case "response.custom_tool_call_input.delta":
		if entry != nil {
			entry.input.WriteString(e.data.text("delta"))
		}
		return pass
	case "response.function_call_arguments.done":
		if entry == nil || (!entry.convert && entry != f.call) {
			return pass
		}
		// The relay sends this call's own when the call ends.
		arguments := e.data.text("arguments")
		entry.doneArgs = &arguments
		return nil
	case "response.output_item.done":
		if entry == nil || entry.sent != nil {
			return pass
		}
		raw, _ := e.data.get("item")
		if !entry.convert && entry != f.call {
			entry.sent = raw
			return pass
		}
		entry.final, _ = parseObject(raw)
		return f.endItem(entry)
	case "error", "response.failed", "response.incomplete":
		if f.call == nil {
			return pass
		}
		if !f.call.complete() {
			// An error before the call was complete goes to Codex as it
			// came, so Codex's own retries apply.
			f.through = true
			return pass
		}
		return f.cutAtCall("error")
	case "response.completed":
		// The provider ended the answer itself: every item still held ends
		// first, and the answer's output is the items as Codex got them.
		out := [][]byte{}
		for _, at := range f.order {
			if held := f.items[at]; held.sent == nil && (held.convert || held == f.call) {
				out = append(out, f.endItem(held)...)
			}
		}
		// Anything after it goes as it comes.
		f.completed, f.through = true, true
		if f.call == nil && f.converted == 0 {
			return append(out, e.raw)
		}
		raw, _ := e.data.get("response")
		response, err := parseObject(raw)
		if err != nil {
			return append(out, e.raw)
		}
		data := e.data.set("response", response.set("output", f.output()).encode())
		return append(out, sseBytes("response.completed", data))
	}
	return pass
}

// end answers what Codex gets when the provider's stream ends or breaks
// before the answer did: the first call, complete, ends it.
func (f *streamFix) end() [][]byte {
	if f.ended || f.through || f.call == nil || !f.call.complete() {
		return nil
	}
	return f.cutAtCall("end")
}

// cutAtCall ends the answer at its first call: each item up to it that has
// not ended ends, then a response.completed with them as its output and the
// usage when the provider gave one.
func (f *streamFix) cutAtCall(why string) [][]byte {
	out := [][]byte{}
	for _, at := range f.order {
		if entry := f.items[at]; entry.sent == nil {
			out = append(out, f.endItem(entry)...)
		}
	}
	response := object{}
	for _, key := range []string{"id", "object", "created_at"} {
		if value, ok := f.response.get(key); ok {
			response = append(response, member{key: key, value: value})
		}
	}
	if _, ok := response.get("id"); !ok {
		response = append(object{{key: "id", value: mustJSON(f.ids("resp_"))}}, response...)
	}
	response = response.set("object", mustJSON("response"))
	response = append(response, member{key: "status", value: mustJSON("completed")})
	if value, ok := f.response.get("model"); ok {
		response = append(response, member{key: "model", value: value})
	}
	response = append(response, member{key: "output", value: f.output()})
	if f.usage != nil {
		response = append(response, member{key: "usage", value: f.usage})
	}
	out = append(out, f.synthesized("response.completed", object{{key: "response", value: response.encode()}}))
	f.cut, f.completed, f.ended = why, true, true
	return out
}

// endItem answers the events that end an item the relay held or the
// provider had not ended: a call's arguments.done and its output_item.done,
// built from its added item and its deltas; a custom tool's call made as a
// function as the custom_tool_call Codex applies, added and done; any
// other item as it was added, a message with its streamed text.
func (f *streamFix) endItem(entry *streamItem) [][]byte {
	at := mustJSON(entry.index)
	item := slices.Clone(entry.added).set("status", mustJSON("completed"))
	out := [][]byte{}
	switch kind := entry.added.text("type"); {
	case entry.convert:
		name := entry.added.text("name")
		custom := object{}
		if id, ok := entry.added.get("id"); ok {
			custom = append(custom, member{key: "id", value: id})
		}
		custom = append(custom, member{key: "type", value: mustJSON("custom_tool_call")}, member{key: "status", value: mustJSON("completed")})
		if callID, ok := entry.added.get("call_id"); ok {
			custom = append(custom, member{key: "call_id", value: callID})
		}
		custom = append(custom, member{key: "name", value: mustJSON(name)}, member{key: "input", value: mustJSON(customInput(name, entry.arguments()))})
		item = custom
		f.converted++
		out = append(out, f.synthesized("response.output_item.added", object{{key: "output_index", value: at}, {key: "item", value: item.encode()}}))
	case kind == "function_call":
		arguments := mustJSON(entry.arguments())
		item = item.set("arguments", arguments)
		done := object{}
		if id, ok := entry.added.get("id"); ok {
			done = append(done, member{key: "item_id", value: id})
		}
		done = append(done, member{key: "output_index", value: at}, member{key: "arguments", value: arguments})
		out = append(out, f.synthesized("response.function_call_arguments.done", done))
	case kind == "custom_tool_call":
		input := entry.input.String()
		if input == "" {
			input = entry.final.text("input")
		}
		if input == "" {
			input = entry.added.text("input")
		}
		item = item.set("input", mustJSON(input))
	case kind == "message":
		if text := entry.text.String(); text != "" {
			var content []json.RawMessage
			if raw, ok := entry.added.get("content"); !ok || json.Unmarshal(raw, &content) != nil || len(content) == 0 {
				part := object{{key: "type", value: mustJSON("output_text")}, {key: "text", value: mustJSON(text)}, {key: "annotations", value: json.RawMessage("[]")}}
				item = item.set("content", json.RawMessage("["+string(part.encode())+"]"))
			}
		}
	default:
		// A reasoning item, or one the relay does not know, as it was added.
		item = entry.added
	}
	entry.sent = item.encode()
	return append(out, f.synthesized("response.output_item.done", object{{key: "output_index", value: at}, {key: "item", value: entry.sent}}))
}

// output answers the items that have gone to Codex, in the answer's order.
func (f *streamFix) output() json.RawMessage {
	items := []item{}
	for _, at := range f.order {
		if sent := f.items[at].sent; sent != nil {
			items = append(items, item{raw: sent})
		}
	}
	return encodeItems(items)
}

// synthesized is an event of the relay's own, numbered after the last one
// the provider sent.
func (f *streamFix) synthesized(kind string, fields object) []byte {
	data := append(object{{key: "type", value: mustJSON(kind)}, {key: "sequence_number", value: mustJSON(f.next)}}, fields...)
	f.next++
	return sseBytes(kind, data)
}

func sseBytes(kind string, data object) []byte {
	return []byte("event: " + kind + "\ndata: " + string(data.encode()) + "\n\n")
}

// noteResponse keeps what the answer's response object says of itself, and
// its usage when it gives one.
func (f *streamFix) noteResponse(raw json.RawMessage) {
	response, err := parseObject(raw)
	if err != nil {
		return
	}
	for _, key := range []string{"id", "object", "created_at", "model"} {
		if _, ok := f.response.get(key); ok {
			continue
		}
		if value, ok := response.get(key); ok && present(value) {
			f.response = append(f.response, member{key: key, value: value})
		}
	}
	if usage, ok := response.get("usage"); ok && present(usage) {
		f.usage = usage
	}
}

// arguments answers a call's arguments: its deltas, or else what the
// provider's arguments.done, output_item.done or added item gave.
func (i *streamItem) arguments() string {
	if i.args.Len() > 0 {
		return i.args.String()
	}
	if i.doneArgs != nil && *i.doneArgs != "" {
		return *i.doneArgs
	}
	if arguments := i.final.text("arguments"); arguments != "" {
		return arguments
	}
	return i.added.text("arguments")
}

// complete reports whether a call can go to Codex as it is: a function
// call's arguments are one JSON value (Together's deltas are, by the time
// its next item starts); a freeform input always is.
func (i *streamItem) complete() bool {
	if i.added.text("type") == "custom_tool_call" {
		return true
	}
	return json.Valid([]byte(i.arguments()))
}

// patchStart and patchEnd bound an apply_patch input.
const (
	patchStart = "*** Begin Patch"
	patchEnd   = "*** End Patch"
)

// customInput answers the input of a custom tool's call that the model made
// as a function: its arguments' "input". gpt-oss-120b also wrote apply_patch
// as {"patch": …}, {"command": …} and {"command": ["apply_patch", …]}
// (Codex's base prompt shows that last one), so for these the input is the
// "patch" or "command" string, or the list's item that holds the patch (else
// its last string). Arguments of no such shape go as they came, so the tool
// answers the model with its own error. For apply_patch the input is cut to
// what lies from *** Begin Patch to *** End Patch, when it holds both.
func customInput(name, arguments string) string {
	input := arguments
	var value any
	if json.Unmarshal([]byte(arguments), &value) == nil {
		switch shaped := value.(type) {
		case string:
			input = shaped
		case []any:
			if text, ok := listInput(shaped); ok {
				input = text
			}
		case map[string]any:
			for _, key := range []string{"input", "patch", "command"} {
				if text, ok := shaped[key].(string); ok {
					input = text
					break
				}
				if list, ok := shaped[key].([]any); ok {
					if text, ok := listInput(list); ok {
						input = text
						break
					}
				}
			}
		}
	}
	if name != "apply_patch" {
		return input
	}
	start := strings.Index(input, patchStart)
	end := strings.LastIndex(input, patchEnd)
	if start < 0 || end < start {
		return input
	}
	return input[start : end+len(patchEnd)]
}

// listInput answers the item of a list that holds a patch, or else its last
// string.
func listInput(list []any) (string, bool) {
	last, found := "", false
	for _, entry := range list {
		if text, ok := entry.(string); ok {
			if strings.Contains(text, patchStart) {
				return text, true
			}
			last, found = text, true
		}
	}
	return last, found
}

// intField answers a key's value when it is a JSON integer.
func intField(o object, key string) (int, bool) {
	raw, ok := o.get(key)
	if !ok {
		return 0, false
	}
	number, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	return number, err == nil
}

// estimatedTokens is a rough count of tokens in so many bytes of text (about
// four bytes a token), for a line whose usage the provider never sent.
func estimatedTokens(bytes int) int { return (bytes + 3) / 4 }

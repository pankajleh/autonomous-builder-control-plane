package openmodel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// The relay's fixes, by the name a model's compat list gives them. Each one
// is a workaround for a request shape Together AI's Responses API refused
// from Codex 0.163.0 in round 1 of the trial (Repo C design note
// OPEN_MODEL_ENGINES §9.1), proved by a probe that sent the exact request
// before the fix (HTTP 400) and after it (HTTP 200). A fix changes only what
// its name says; every other byte of the request goes on as Codex sent it.
const (
	// CompatDropNullReasoningContent removes `"content": null` from the
	// reasoning items of a request's input. Together answers its own
	// reasoning item, and refuses it back with that key ("data did not match
	// any variant of untagged enum ResponseInput").
	CompatDropNullReasoningContent = "drop_null_reasoning_content"
	// CompatMessageBeforeCalls puts, within one assistant turn (consecutive
	// reasoning, call and assistant-message items), the reasoning first, then
	// the assistant message(s), then the call(s). Codex replays a turn in the
	// order it streamed (call, then message); Together then splits the turn
	// and refuses it ("invalid_tool_messages: incomplete parallel tool-call
	// group"). Nothing is added or removed.
	CompatMessageBeforeCalls = "message_before_calls"
	// CompatOutputAfterCall puts each call's output straight after its call.
	// Together makes every call a turn of its own, so Codex's replay of two
	// parallel calls (call A, call B, output A, output B) is refused the same
	// way. Nothing is added or removed; a request it cannot place exactly is
	// left as it is.
	CompatOutputAfterCall = "output_after_call"
	// CompatDropNamespaceTools leaves out tools of type "namespace" (Codex's
	// multi_agent_v1), which some of Together's endpoints refuse ("unknown
	// variant `namespace`").
	CompatDropNamespaceTools = "drop_namespace_tools"
	// CompatChatBridge sends the request to the provider's
	// /chat/completions instead, with each earlier assistant turn's
	// reasoning as reasoning_content, and answers Codex with the Responses
	// events it reads (chatbridge.go). For a model whose template refuses an
	// earlier turn without its reasoning, which Together's /responses never
	// sends back.
	CompatChatBridge = "chat_completions_bridge"
	// CompatDropMediumEffort leaves out an empty `"reasoning": {}` (which
	// Together fills in as "medium") or a "medium" effort, for a model that
	// refuses medium ("reasoning_effort must be low, high, xhigh, max …").
	CompatDropMediumEffort = "drop_medium_effort"
)

// CompatFixes are the fixes in the order the relay applies them.
var CompatFixes = []string{
	CompatDropNullReasoningContent, CompatMessageBeforeCalls, CompatOutputAfterCall,
	CompatDropMediumEffort, CompatDropNamespaceTools, CompatChatBridge,
}

// validCompat accepts distinct known fixes; the chat bridge keeps a turn's
// calls together itself, so the two reorderings never go with it (they
// would split the turn and its reasoning).
func validCompat(compat []string) error {
	seen := map[string]bool{}
	for _, name := range compat {
		if !slices.Contains(CompatFixes, name) {
			return fmt.Errorf("compat names an unknown fix %q (known: %s)", name, strings.Join(CompatFixes, ", "))
		}
		if seen[name] {
			return fmt.Errorf("compat repeats %q", name)
		}
		seen[name] = true
	}
	if seen[CompatChatBridge] && (seen[CompatMessageBeforeCalls] || seen[CompatOutputAfterCall]) {
		return errors.New("compat " + CompatChatBridge + " keeps a turn's calls together itself: it is refused with " + CompatMessageBeforeCalls + " or " + CompatOutputAfterCall)
	}
	return nil
}

// Applied counts what the fixes changed in one request, by fix.
type Applied map[string]int

// String lists the fixes that changed something, in the relay's order, as
// name:count; "none" when nothing changed.
func (a Applied) String() string {
	parts := []string{}
	for _, name := range CompatFixes {
		if count := a[name]; count > 0 {
			parts = append(parts, name+":"+strconv.Itoa(count))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ",")
}

// member is one key of a JSON object, its value exactly as it was sent.
type member struct {
	key   string
	value json.RawMessage
}

// object is a JSON object that keeps its keys' order and its values' bytes,
// so a fix rewrites only what it changes.
type object []member

// parseObject reads a JSON object; it refuses anything else, a repeated key
// or trailing data.
func parseObject(data []byte) (object, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("not a JSON object")
	}
	out := object{}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return nil, errors.New("a JSON object with a repeated or odd key")
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		out = append(out, member{key: key, value: value})
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("an unterminated JSON object")
	}
	if _, err := decoder.Token(); err == nil {
		return nil, errors.New("data after the JSON object")
	}
	return out, nil
}

func (o object) get(key string) (json.RawMessage, bool) {
	for _, m := range o {
		if m.key == key {
			return m.value, true
		}
	}
	return nil, false
}

func (o object) set(key string, value json.RawMessage) object {
	for index, m := range o {
		if m.key == key {
			o[index].value = value
			return o
		}
	}
	return append(o, member{key: key, value: value})
}

func (o object) without(key string) object {
	return slices.DeleteFunc(slices.Clone(o), func(m member) bool { return m.key == key })
}

// text answers the value of key when it is a JSON string.
func (o object) text(key string) string {
	raw, ok := o.get(key)
	if !ok {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

func (o object) encode() json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for index, m := range o {
		if index > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(m.key)
		b.Write(key)
		b.WriteByte(':')
		b.Write(m.value)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// item is one entry of a request's input with what the fixes look at.
type item struct {
	raw    json.RawMessage
	fields object // nil when the entry is not an object
	kind   string
	callID string
}

const (
	kindReasoning        = "reasoning"
	kindAssistantMessage = "assistant_message"
	kindCall             = "call"
	kindCallOutput       = "call_output"
)

func readItem(raw json.RawMessage) item {
	fields, err := parseObject(raw)
	if err != nil {
		return item{raw: raw}
	}
	entry := item{raw: raw, fields: fields, callID: fields.text("call_id")}
	switch kind := fields.text("type"); kind {
	case "reasoning":
		entry.kind = kindReasoning
	case "message":
		if fields.text("role") == "assistant" {
			entry.kind = kindAssistantMessage
		}
	// Codex's freeform apply_patch replays as custom_tool_call items, which
	// Together turns into tool calls as it does function calls.
	case "function_call", "custom_tool_call":
		entry.kind = kindCall
	case "function_call_output", "custom_tool_call_output":
		entry.kind = kindCallOutput
	}
	return entry
}

// inTurn reports whether an item belongs to an assistant turn.
func (i item) inTurn() bool {
	return i.kind == kindReasoning || i.kind == kindAssistantMessage || i.kind == kindCall
}

// Rewrite applies a model's fixes to a POST <base>/responses body, other than
// the chat bridge (chatbridge.go). It answers the body to send: the very
// bytes it was given when no fix changed anything. A body that is not a
// JSON object is answered as an error; the relay refuses it.
func Rewrite(body []byte, compat []string) ([]byte, Applied, error) {
	request, err := parseObject(body)
	if err != nil {
		return nil, nil, err
	}
	applied := Applied{}
	has := func(name string) bool { return slices.Contains(compat, name) }
	if raw, ok := request.get("input"); ok && (has(CompatDropNullReasoningContent) || has(CompatMessageBeforeCalls) || has(CompatOutputAfterCall)) {
		var entries []json.RawMessage
		if json.Unmarshal(raw, &entries) == nil {
			items := make([]item, len(entries))
			for index, entry := range entries {
				items[index] = readItem(entry)
			}
			changed := false
			if has(CompatDropNullReasoningContent) {
				if count := dropNullReasoningContent(items); count > 0 {
					applied[CompatDropNullReasoningContent], changed = count, true
				}
			}
			if has(CompatMessageBeforeCalls) {
				var count int
				if items, count = messageBeforeCalls(items); count > 0 {
					applied[CompatMessageBeforeCalls], changed = count, true
				}
			}
			if has(CompatOutputAfterCall) {
				var count int
				if items, count = outputAfterCall(items); count > 0 {
					applied[CompatOutputAfterCall], changed = count, true
				}
			}
			if changed {
				request = request.set("input", encodeItems(items))
			}
		}
	}
	if has(CompatDropMediumEffort) {
		var count int
		if request, count = dropMediumEffort(request); count > 0 {
			applied[CompatDropMediumEffort] = count
		}
	}
	if has(CompatDropNamespaceTools) {
		var count int
		if request, count = dropNamespaceTools(request); count > 0 {
			applied[CompatDropNamespaceTools] = count
		}
	}
	if len(applied) == 0 {
		return body, applied, nil
	}
	return request.encode(), applied, nil
}

func encodeItems(items []item) json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('[')
	for index, entry := range items {
		if index > 0 {
			b.WriteByte(',')
		}
		b.Write(entry.raw)
	}
	b.WriteByte(']')
	return b.Bytes()
}

// dropNullReasoningContent removes "content": null from reasoning items.
func dropNullReasoningContent(items []item) int {
	count := 0
	for index, entry := range items {
		if entry.kind != kindReasoning {
			continue
		}
		if raw, ok := entry.fields.get("content"); ok && string(bytes.TrimSpace(raw)) == "null" {
			entry.fields = entry.fields.without("content")
			entry.raw = entry.fields.encode()
			items[index] = entry
			count++
		}
	}
	return count
}

// messageBeforeCalls reorders each assistant turn in which a message follows
// a call: reasoning, then messages, then calls, each in its own order.
func messageBeforeCalls(items []item) ([]item, int) {
	out := make([]item, 0, len(items))
	moved := 0
	for start := 0; start < len(items); {
		if !items[start].inTurn() {
			out = append(out, items[start])
			start++
			continue
		}
		end := start
		for end < len(items) && items[end].inTurn() {
			end++
		}
		turn := items[start:end]
		firstCall := slices.IndexFunc(turn, func(i item) bool { return i.kind == kindCall })
		late := firstCall >= 0 && slices.ContainsFunc(turn[firstCall:], func(i item) bool { return i.kind == kindAssistantMessage })
		if late {
			for _, kind := range []string{kindReasoning, kindAssistantMessage, kindCall} {
				for _, entry := range turn {
					if entry.kind == kind {
						out = append(out, entry)
					}
				}
			}
			moved++
		} else {
			out = append(out, turn...)
		}
		start = end
	}
	return out, moved
}

// outputAfterCall moves each call's output (the last one for its call_id)
// straight after the call. When that cannot be done exactly (an output before
// its call, a repeated call_id), the items are left as they are.
func outputAfterCall(items []item) ([]item, int) {
	outputs := map[string]int{}
	for index, entry := range items {
		if entry.kind == kindCallOutput && entry.callID != "" {
			outputs[entry.callID] = index
		}
	}
	placed := map[string]bool{}
	out := make([]item, 0, len(items))
	moved := 0
	for index, entry := range items {
		if entry.kind == kindCallOutput && placed[entry.callID] {
			continue
		}
		out = append(out, entry)
		if entry.kind != kindCall || entry.callID == "" || placed[entry.callID] {
			continue
		}
		if at, ok := outputs[entry.callID]; ok {
			if at != index+1 {
				moved++
			}
			out = append(out, items[at])
			placed[entry.callID] = true
		}
	}
	if len(out) != len(items) {
		return items, 0
	}
	return out, moved
}

// dropMediumEffort leaves out an empty reasoning object or a "medium"
// effort, and the reasoning object when nothing is left in it.
func dropMediumEffort(request object) (object, int) {
	raw, ok := request.get("reasoning")
	if !ok {
		return request, 0
	}
	reasoning, err := parseObject(raw)
	if err != nil {
		return request, 0
	}
	if len(reasoning) != 0 && reasoning.text("effort") != "medium" {
		return request, 0
	}
	reasoning = reasoning.without("effort")
	if len(reasoning) == 0 {
		return request.without("reasoning"), 1
	}
	return request.set("reasoning", reasoning.encode()), 1
}

// dropNamespaceTools leaves out tools of type "namespace".
func dropNamespaceTools(request object) (object, int) {
	raw, ok := request.get("tools")
	if !ok {
		return request, 0
	}
	var tools []json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return request, 0
	}
	kept := make([]item, 0, len(tools))
	for _, tool := range tools {
		if fields, err := parseObject(tool); err == nil && fields.text("type") == "namespace" {
			continue
		}
		kept = append(kept, item{raw: tool})
	}
	if len(kept) == len(tools) {
		return request, 0
	}
	return request.set("tools", encodeItems(kept)), len(tools) - len(kept)
}

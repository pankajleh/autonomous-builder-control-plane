package openmodel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

// The streams of these tests are shaped as Together streamed gpt-oss-120b's
// answers in the 2026-11-07 diagnosis (the server's /tmp/gptoss-diag,
// loop2 and loop3 resp-*-try1.txt): created and in progress, a reasoning
// item added and done at once (content null), each call added with empty
// arguments and its arguments as deltas, every later .done held to the end
// and no longer matching the deltas, and Together's parse error as an event
// with a data line only.

// together streams one recorded answer, event by event.
type togetherAnswer struct {
	events []string
	seq    int
}

const (
	gptOssResponse = "resp_01a1b3e7-8669-7650-bdb4-4134831ef5a0"
	parseError     = "Parse error: Failed to process token 200003: Unexpected token 200003 while expecting start token 200006"
	helloPatch     = "*** Begin Patch\n*** Add File: hello.txt\n+hi\n*** End Patch"
)

func newTogetherAnswer() *togetherAnswer {
	a := &togetherAnswer{}
	a.add("response.created", map[string]any{"response": map[string]any{"id": gptOssResponse, "object": "response", "created_at": 1794019657,
		"status": "in_progress", "model": "openai/gpt-oss-120b", "output": []any{}}})
	a.add("response.in_progress", map[string]any{"response": map[string]any{"id": gptOssResponse, "object": "response", "status": "in_progress"}})
	return a
}

func (a *togetherAnswer) add(kind string, fields map[string]any) *togetherAnswer {
	fields["type"], fields["sequence_number"] = kind, a.seq
	a.seq++
	data, _ := json.Marshal(fields)
	a.events = append(a.events, fmt.Sprintf("event: %s\ndata: %s\n\n", kind, data))
	return a
}

// parseError is Together's error after HTTP 200: a data line, no event line.
func (a *togetherAnswer) parseError() *togetherAnswer {
	data, _ := json.Marshal(map[string]any{"type": "error", "code": "processing_error", "message": parseError, "param": nil, "sequence_number": a.seq})
	a.seq++
	a.events = append(a.events, "data: "+string(data)+"\n\n")
	return a
}

func (a *togetherAnswer) reasoning(index int) *togetherAnswer {
	item := map[string]any{"id": "rs_01a1b3e786f077e1", "type": "reasoning", "summary": []any{}, "content": nil, "encrypted_content": "", "status": nil}
	a.add("response.output_item.added", map[string]any{"output_index": index, "item": item})
	return a.add("response.output_item.done", map[string]any{"output_index": index, "item": item})
}

// call adds a function call and its arguments, split into deltas.
func (a *togetherAnswer) call(index int, name, callID, arguments string) *togetherAnswer {
	id := "fc_" + callID
	a.add("response.output_item.added", map[string]any{"output_index": index, "item": map[string]any{"id": id, "type": "function_call", "name": name,
		"call_id": callID, "arguments": "", "status": "in_progress"}})
	a.add("response.function_call_arguments.delta", map[string]any{"output_index": index, "item_id": id, "delta": ""})
	for rest := arguments; rest != ""; {
		size := min(7, len(rest))
		a.add("response.function_call_arguments.delta", map[string]any{"output_index": index, "item_id": id, "delta": rest[:size]})
		rest = rest[size:]
	}
	return a
}

// message adds an assistant message and its text as deltas, without its
// .done (Together holds it to the end).
func (a *togetherAnswer) message(index int, text string) *togetherAnswer {
	id := fmt.Sprintf("msg_%d", index)
	a.add("response.output_item.added", map[string]any{"output_index": index, "item": map[string]any{"id": id, "type": "message", "role": "assistant", "content": []any{}}})
	a.add("response.content_part.added", map[string]any{"output_index": index, "item_id": id, "content_index": 0, "part": map[string]any{"type": "output_text", "text": ""}})
	return a.add("response.output_text.delta", map[string]any{"output_index": index, "item_id": id, "content_index": 0, "delta": text})
}

func (a *togetherAnswer) completed(output []any, usage map[string]any) *togetherAnswer {
	return a.add("response.completed", map[string]any{"response": map[string]any{"id": gptOssResponse, "object": "response", "created_at": 1794019657,
		"status": "completed", "model": "openai/gpt-oss-120b", "output": output, "usage": usage}})
}

func (a *togetherAnswer) String() string { return strings.Join(a.events, "") }

// fixed runs one answer through a stream fix as the relay does.
func fixed(t *testing.T, fix *streamFix, answer string) string {
	t.Helper()
	var out bytes.Buffer
	flushes := 0
	if err := fix.copyStream(&out, func() { flushes++ }, strings.NewReader(answer)); err != nil {
		t.Fatal(err)
	}
	if out.Len() > 0 && flushes == 0 {
		t.Error("the fixed stream was never flushed")
	}
	return out.String()
}

// codexEvents reads a stream as Codex does: each event's data.
func codexEvents(t *testing.T, stream string) []map[string]any {
	t.Helper()
	events := []map[string]any{}
	for _, block := range strings.Split(strings.TrimSuffix(stream, "\n\n"), "\n\n") {
		event := parseEvent([]byte(block))
		if event.data == nil {
			t.Fatalf("an event Codex cannot read: %q", block)
		}
		var data map[string]any
		if err := json.Unmarshal(event.data.encode(), &data); err != nil {
			t.Fatal(err)
		}
		if name, _, _ := strings.Cut(strings.TrimPrefix(block, "event: "), "\n"); strings.HasPrefix(block, "event: ") && name != data["type"] {
			t.Errorf("event %s carries data of type %v", name, data["type"])
		}
		events = append(events, data)
	}
	return events
}

// completedOutput answers the last event, which must be response.completed,
// and its output items as type:name.
func completedOutput(t *testing.T, events []map[string]any) (map[string]any, []string) {
	t.Helper()
	last := events[len(events)-1]
	if last["type"] != "response.completed" {
		t.Fatalf("the answer ends with %v", last["type"])
	}
	response := last["response"].(map[string]any)
	kinds := []string{}
	for _, entry := range response["output"].([]any) {
		item := entry.(map[string]any)
		kinds = append(kinds, strings.TrimSuffix(fmt.Sprint(item["type"], ":", item["name"]), ":<nil>"))
	}
	return response, kinds
}

// TestAParseErrorAfterACompleteCallEndsTheAnswerAtTheCall: loop2's first
// answer. The call's arguments are complete JSON when Together's parser
// fails; Codex gets every event before the error byte for byte, then the
// call's arguments.done and output_item.done built from its deltas, and a
// response.completed with the reasoning and the call, without usage (none
// came). The error does not reach Codex.
func TestAParseErrorAfterACompleteCallEndsTheAnswerAtTheCall(t *testing.T) {
	arguments := "{\n  \"cmd\": \"printf \\\"hi\\\" > hello.txt\",\n  \"justification\": \"Create hello.txt\"\n}"
	answer := newTogetherAnswer().reasoning(0).call(1, "exec_command", "call_1", arguments)
	before := answer.String()
	answer.parseError()
	fix := newStreamFix(true, nil, nil)
	got := fixed(t, fix, answer.String())
	if !strings.HasPrefix(got, before) || strings.Contains(got, "Parse error") {
		t.Fatalf("the events before the error changed, or the error went on:\n%s", got)
	}
	events := codexEvents(t, strings.TrimPrefix(got, before))
	if len(events) != 3 || events[0]["type"] != "response.function_call_arguments.done" || events[0]["arguments"] != arguments ||
		events[0]["item_id"] != "fc_call_1" || events[0]["output_index"] != float64(1) || events[1]["type"] != "response.output_item.done" {
		t.Fatalf("the call ended as %v", events)
	}
	item := events[1]["item"].(map[string]any)
	if item["arguments"] != arguments || item["status"] != "completed" || item["call_id"] != "call_1" || item["name"] != "exec_command" || item["id"] != "fc_call_1" {
		t.Errorf("the call's output_item.done carries %v", item)
	}
	response, kinds := completedOutput(t, events)
	if strings.Join(kinds, " ") != "reasoning function_call:exec_command" || response["id"] != gptOssResponse || response["status"] != "completed" ||
		response["model"] != "openai/gpt-oss-120b" || response["created_at"] != float64(1794019657) || response["usage"] != nil {
		t.Errorf("the relay's response.completed = %v", response)
	}
	if events[0]["sequence_number"] != float64(answer.seq) || events[2]["sequence_number"] != float64(answer.seq+2) {
		t.Errorf("the relay's events are numbered %v to %v after Together's last %d (its error)", events[0]["sequence_number"], events[2]["sequence_number"], answer.seq-1)
	}
	if fix.cut != "error" || fix.usage != nil || fix.visible != len(arguments) {
		t.Errorf("cut %q, usage %s, visible %d", fix.cut, fix.usage, fix.visible)
	}
}

// TestInventedItemsAfterACallAreLeftOut: loop3's second answer, the
// runaway: five calls and a "done" in one answer, then Together's .done
// items (names and arguments shifted) and a completed with usage. Codex
// gets the first call alone, built from its deltas; the relay stops reading
// at the second call's output_item.added.
func TestInventedItemsAfterACallAreLeftOut(t *testing.T) {
	first := `{"cmd":["bash","-lc","ls -R"]}`
	answer := newTogetherAnswer().reasoning(0).call(1, "exec_command", "call_1", first)
	before := answer.String()
	answer.call(2, "apply_patch", "call_2", `{"input":"`+strings.ReplaceAll(helloPatch, "\n", `\n`)+`"}`).
		call(3, "exec_command", "call_3", `{"cmd":["bash","-lc","cat notes.txt"]}`).message(4, "done")
	shifted := map[string]any{"id": "fc_call_1", "type": "function_call", "name": "exec_command", "call_id": "call_other", "arguments": `{"cmd":["bash","-lc","ls -R ."]}`, "status": "completed"}
	answer.add("response.function_call_arguments.done", map[string]any{"output_index": 1, "item_id": "fc_call_1", "arguments": shifted["arguments"]}).
		add("response.output_item.done", map[string]any{"output_index": 1, "item": shifted}).
		completed([]any{shifted}, map[string]any{"input_tokens": 5833, "output_tokens": 5920, "total_tokens": 11753})
	fix := newStreamFix(true, nil, nil)
	got := fixed(t, fix, answer.String())
	if !strings.HasPrefix(got, before) {
		t.Fatalf("the events before the second call changed:\n%s", got)
	}
	for _, invented := range []string{"call_2", "call_3", "apply_patch", `"delta":"done"`, "ls -R .", "call_other", "5920"} {
		if strings.Contains(got, invented) {
			t.Errorf("Codex got %q, from after the first call:\n%s", invented, got)
		}
	}
	events := codexEvents(t, strings.TrimPrefix(got, before))
	if len(events) != 3 || events[1]["item"].(map[string]any)["arguments"] != first {
		t.Fatalf("the call ended as %v", events)
	}
	if _, kinds := completedOutput(t, events); strings.Join(kinds, " ") != "reasoning function_call:exec_command" {
		t.Errorf("the answer's output = %v", kinds)
	}
	if fix.cut != "next_item" || fix.usage != nil {
		t.Errorf("cut %q, usage %s", fix.cut, fix.usage)
	}
}

// TestAPreambleBeforeTheCallGoesToCodexWithItsText: a message the model
// wrote before its call goes as it came, and since Together holds its .done
// to the end, the relay ends it with its streamed text before the call.
func TestAPreambleBeforeTheCallGoesToCodexWithItsText(t *testing.T) {
	answer := newTogetherAnswer().reasoning(0).message(1, "Reading the plan.").call(2, "exec_command", "call_1", `{"cmd":"cat plan.md"}`)
	before := answer.String()
	answer.parseError()
	got := fixed(t, newStreamFix(true, nil, nil), answer.String())
	events := codexEvents(t, strings.TrimPrefix(got, before))
	if !strings.HasPrefix(got, before) || len(events) != 4 || events[0]["type"] != "response.output_item.done" || events[0]["output_index"] != float64(1) {
		t.Fatalf("the preamble ended as %v", events)
	}
	message := events[0]["item"].(map[string]any)
	content, _ := json.Marshal(message["content"])
	if message["status"] != "completed" || string(content) != `[{"annotations":[],"text":"Reading the plan.","type":"output_text"}]` {
		t.Errorf("the preamble's output_item.done carries %v", message)
	}
	if _, kinds := completedOutput(t, events); strings.Join(kinds, " ") != "reasoning message function_call:exec_command" {
		t.Errorf("the answer's output = %v", kinds)
	}
}

// TestAnErrorBeforeACallIsCompleteGoesToCodexAsItCame: an error with no
// call, or with a call whose arguments are not yet one JSON value, reaches
// Codex unchanged (so its own retries apply), and so does all that follows.
func TestAnErrorBeforeACallIsCompleteGoesToCodexAsItCame(t *testing.T) {
	for name, answer := range map[string]*togetherAnswer{
		"no call":               newTogetherAnswer().reasoning(0).parseError(),
		"a call's half":         newTogetherAnswer().reasoning(0).call(1, "exec_command", "call_1", `{"cmd":"cat pla`).parseError(),
		"an apply_patch's half": newTogetherAnswer().reasoning(0).call(1, "apply_patch", "call_1", `{"input":"*** Begin Patch\n*** Add`).parseError().add("response.output_item.added", map[string]any{"output_index": 2, "item": map[string]any{"type": "message"}}),
	} {
		fix := newStreamFix(true, []string{"apply_patch"}, nil)
		got := fixed(t, fix, answer.String())
		if name != "an apply_patch's half" && got != answer.String() {
			t.Errorf("%s: the stream changed:\n%s", name, got)
		}
		if !strings.HasSuffix(got, answer.events[len(answer.events)-1]) || strings.Contains(got, "response.completed") || fix.cut != "" || fix.usage != nil {
			t.Errorf("%s: the error did not reach Codex as it came (cut %q):\n%s", name, fix.cut, got)
		}
	}
}

// TestACallTheProviderEndsItselfKeepsItsUsage: a provider that stops at the
// call (its .done, then completed with usage) is not cut: Codex gets the
// call built from its deltas, and the provider's completed with its usage
// and the call as its output.
func TestACallTheProviderEndsItselfKeepsItsUsage(t *testing.T) {
	arguments := `{"cmd":"cat plan.md"}`
	answer := newTogetherAnswer().reasoning(0).call(1, "exec_command", "call_1", arguments)
	before := answer.String()
	item := map[string]any{"id": "fc_call_1", "type": "function_call", "name": "exec_command", "call_id": "call_1", "arguments": arguments, "status": "completed"}
	answer.add("response.function_call_arguments.done", map[string]any{"output_index": 1, "item_id": "fc_call_1", "arguments": arguments}).
		add("response.output_item.done", map[string]any{"output_index": 1, "item": item}).
		completed([]any{map[string]any{"type": "reasoning"}, item}, map[string]any{"input_tokens": 5881, "output_tokens": 40, "total_tokens": 5921})
	fix := newStreamFix(true, nil, nil)
	got := fixed(t, fix, answer.String())
	events := codexEvents(t, strings.TrimPrefix(got, before))
	if !strings.HasPrefix(got, before) || len(events) != 3 || events[1]["item"].(map[string]any)["arguments"] != arguments {
		t.Fatalf("the call ended as %v", events)
	}
	response, kinds := completedOutput(t, events)
	usage, _ := json.Marshal(response["usage"])
	if strings.Join(kinds, " ") != "reasoning function_call:exec_command" || string(usage) != `{"input_tokens":5881,"output_tokens":40,"total_tokens":5921}` {
		t.Errorf("the provider's completed reached Codex as %v", response)
	}
	if fix.cut != "" || fix.usage == nil {
		t.Errorf("cut %q, usage %s", fix.cut, fix.usage)
	}
}

// TestAFunctionApplyPatchGoesBackAsCodexsCustomCall: each shape gpt-oss-120b
// gave its apply_patch call (diagnosis §1, §3) reaches Codex as the
// custom_tool_call it applies, with the patch as its input; the function's
// own events never reach Codex.
func TestAFunctionApplyPatchGoesBackAsCodexsCustomCall(t *testing.T) {
	encoded := func(value any) string {
		data, _ := json.Marshal(value)
		return string(data)
	}
	for name, arguments := range map[string]string{
		"input":           encoded(map[string]any{"input": helloPatch}),
		"patch":           encoded(map[string]any{"patch": helloPatch}),
		"command":         encoded(map[string]any{"command": helloPatch}),
		"command list":    encoded(map[string]any{"command": []any{"apply_patch", helloPatch}}),
		"pretty, a space": "{\n  \"input\": " + encoded(helloPatch) + "\n}",
	} {
		answer := newTogetherAnswer().reasoning(0)
		before := answer.String()
		answer.call(1, "apply_patch", "call_p", arguments).message(2, "done")
		fix := newStreamFix(true, []string{"apply_patch"}, nil)
		got := fixed(t, fix, answer.String())
		if !strings.HasPrefix(got, before) || strings.Contains(got, `"function_call`) {
			t.Fatalf("%s: the function's events reached Codex:\n%s", name, got)
		}
		events := codexEvents(t, strings.TrimPrefix(got, before))
		if len(events) != 3 || events[0]["type"] != "response.output_item.added" || events[1]["type"] != "response.output_item.done" {
			t.Fatalf("%s: the call reached Codex as %v", name, events)
		}
		for _, event := range events[:2] {
			item := event["item"].(map[string]any)
			if item["type"] != "custom_tool_call" || item["name"] != "apply_patch" || item["input"] != helloPatch || item["call_id"] != "call_p" ||
				item["id"] != "fc_call_p" || item["status"] != "completed" || item["arguments"] != nil {
				t.Errorf("%s: Codex got %v", name, item)
			}
		}
		if _, kinds := completedOutput(t, events); strings.Join(kinds, " ") != "reasoning custom_tool_call:apply_patch" {
			t.Errorf("%s: the answer's output = %v", name, kinds)
		}
		if fix.converted != 1 || fix.cut != "next_item" {
			t.Errorf("%s: converted %d, cut %q", name, fix.converted, fix.cut)
		}
	}
	// Another function call goes as it came.
	answer := newTogetherAnswer().reasoning(0).call(1, "exec_command", "call_1", `{"cmd":"ls"}`)
	before := answer.String()
	answer.parseError()
	if got := fixed(t, newStreamFix(true, []string{"apply_patch"}, nil), answer.String()); !strings.HasPrefix(got, before) || strings.Contains(got, "custom_tool_call") {
		t.Errorf("exec_command did not go as it came:\n%s", got)
	}
}

// TestTheCustomCallsInputFromEachShape: the input taken from each shape of
// arguments, the patch cut out of what surrounds it, and arguments of no
// known shape left as they came for apply_patch to refuse.
func TestTheCustomCallsInputFromEachShape(t *testing.T) {
	for arguments, want := range map[string]string{
		`{"input":"*** Begin Patch\n*** End Patch"}`:                                        "*** Begin Patch\n*** End Patch",
		`{"patch":"*** Begin Patch\n*** End Patch\n"}`:                                      "*** Begin Patch\n*** End Patch",
		`{"command":"apply_patch <<'EOF'\n*** Begin Patch\n*** End Patch\nEOF"}`:            "*** Begin Patch\n*** End Patch",
		`{"command":["apply_patch","*** Begin Patch\n*** End Patch"]}`:                      "*** Begin Patch\n*** End Patch",
		`{"command":["bash","-lc","apply_patch"]}`:                                          "apply_patch",
		`{"input":7,"patch":"*** Begin Patch\n*** End Patch"}`:                              "*** Begin Patch\n*** End Patch",
		`"*** Begin Patch\n*** End Patch"`:                                                  "*** Begin Patch\n*** End Patch",
		`{"cmd":"cat notes.txt"}`:                                                           `{"cmd":"cat notes.txt"}`,
		`*** Begin Patch` + "\n" + `*** Add File: a` + "\n" + `+x` + "\n" + `*** End Patch`: "*** Begin Patch\n*** Add File: a\n+x\n*** End Patch",
	} {
		if got := customInput("apply_patch", arguments); got != want {
			t.Errorf("%s gave %q, want %q", arguments, got, want)
		}
	}
	if got := customInput("other_tool", `{"input":"say *** End Patch then *** Begin Patch"}`); got != "say *** End Patch then *** Begin Patch" {
		t.Errorf("another custom tool's input = %q", got)
	}
}

// TestACustomCallWithoutTheStopFixEndsAtItsOwnDone: custom_tool_as_function
// alone (a provider that stops at the call) holds the function's events
// until its output_item.done, sends Codex the custom_tool_call then, and the
// provider's completed with it in its output.
func TestACustomCallWithoutTheStopFixEndsAtItsOwnDone(t *testing.T) {
	arguments := `{"input":"` + strings.ReplaceAll(helloPatch, "\n", `\n`) + `"}`
	item := map[string]any{"id": "fc_call_p", "type": "function_call", "name": "apply_patch", "call_id": "call_p", "arguments": arguments, "status": "completed"}
	answer := newTogetherAnswer().reasoning(0)
	before := answer.String()
	answer.call(1, "apply_patch", "call_p", arguments).
		add("response.function_call_arguments.done", map[string]any{"output_index": 1, "item_id": "fc_call_p", "arguments": arguments}).
		add("response.output_item.done", map[string]any{"output_index": 1, "item": item}).
		completed([]any{map[string]any{"type": "reasoning"}, item}, map[string]any{"input_tokens": 100, "output_tokens": 30, "total_tokens": 130})
	fix := newStreamFix(false, []string{"apply_patch"}, nil)
	got := fixed(t, fix, answer.String())
	events := codexEvents(t, strings.TrimPrefix(got, before))
	if !strings.HasPrefix(got, before) || len(events) != 3 || strings.Contains(got, "function_call") ||
		events[1]["item"].(map[string]any)["input"] != helloPatch {
		t.Fatalf("the call reached Codex as %v", events)
	}
	response, kinds := completedOutput(t, events)
	if strings.Join(kinds, " ") != "reasoning custom_tool_call:apply_patch" || response["usage"] == nil || fix.cut != "" {
		t.Errorf("the provider's completed = %v (cut %q)", response, fix.cut)
	}
	// With no custom call in it, the same kind of answer goes byte for byte.
	plain := newTogetherAnswer().reasoning(0).call(1, "exec_command", "call_1", `{"cmd":"ls"}`).completed([]any{}, map[string]any{"input_tokens": 1})
	if got := fixed(t, newStreamFix(false, []string{"apply_patch"}, nil), plain.String()); got != plain.String() {
		t.Errorf("an answer without a custom call changed:\n%s", got)
	}
}

// TestTheStreamFixesLeaveOtherAnswersAsTheyCame: without either fix nothing
// is active; a stream with neither a call nor a custom tool's call, read
// through both, is byte for byte the provider's, events split over reads,
// CRLF lines and a last event without its blank line included.
func TestTheStreamFixesLeaveOtherAnswersAsTheyCame(t *testing.T) {
	if newStreamFix(false, nil, nil).active() || (*streamFix)(nil).active() || !newStreamFix(true, nil, nil).active() || !newStreamFix(false, []string{"apply_patch"}, nil).active() {
		t.Fatal("active() is wrong")
	}
	answer := newTogetherAnswer().message(0, "Hello.").completed([]any{}, map[string]any{"input_tokens": 3})
	stream := answer.String() + ": keep-alive\r\n\r\nevent: response.done\r\ndata: {\"type\":\"x\"}\r\n\r\ndata: [DONE]"
	for _, fix := range []*streamFix{newStreamFix(true, []string{"apply_patch"}, nil), newStreamFix(false, []string{"apply_patch"}, nil)} {
		var out bytes.Buffer
		if err := fix.copyStream(&out, nil, &trickle{data: []byte(stream)}); err != nil || out.String() != stream {
			t.Errorf("the answer changed: %v\n%q", err, out.String())
		}
	}
}

// trickle reads a few bytes at a time.
type trickle struct{ data []byte }

func (t *trickle) Read(p []byte) (int, error) {
	if len(t.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p[:min(len(p), 5)], t.data)
	t.data = t.data[n:]
	return n, nil
}

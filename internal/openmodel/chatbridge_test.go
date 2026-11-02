package openmodel

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestTheChatBridgeSendsEachTurnsReasoningBack: Beam's probe (probe-beam.log,
// 7 items, the last [message, reasoning, function_call,
// function_call_output]) is refused on /responses at messages[5]; bridged, it
// becomes one Chat Completions request whose assistant turn carries its
// reasoning, which Together takes (probe-beam-shapes.log c:rc).
func TestTheChatBridgeSendsEachTurnsReasoningBack(t *testing.T) {
	beam := "reflection/Beam-1.0-501B-A23B"
	request := codexRequest(beam, withInput(codexOpening(), thought("rs_1", "We need to inspect the repo."), call("call_1"), output("call_1")),
		func(r map[string]any) { r["max_output_tokens"] = 4096; r["temperature"] = 0.2 })
	if refusal := togetherRefusal(request); !strings.Contains(refusal, "The assistant turn at messages[5] is missing its reasoning") {
		t.Fatalf("Beam on /responses: %q", refusal)
	}
	chat, err := ChatRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"model":"reflection/Beam-1.0-501B-A23B","messages":[` +
		`{"role":"system","content":"You are a coding agent running in the Codex CLI."},` +
		`{"role":"system","content":"<skills_instructions>none</skills_instructions>\n\n<permissions instructions>danger-full-access</permissions instructions>"},` +
		`{"role":"user","content":"<environment_context><cwd>/work</cwd></environment_context>"},` +
		`{"role":"user","content":"Follow the plan in docs/plans/unit-converter.md."},` +
		`{"role":"assistant","content":"","reasoning_content":"We need to inspect the repo.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":\"cat plan.md\"}"}}]},` +
		`{"role":"tool","content":"Process exited with code 0\nOutput:\n# Plan","tool_call_id":"call_1"}],"stream":false,` +
		`"tools":[{"type":"function","function":{"name":"exec_command","description":"exec_command","parameters":{"properties":{},"type":"object"}}},` +
		`{"type":"function","function":{"name":"write_stdin","description":"write_stdin","parameters":{"properties":{},"type":"object"}}},` +
		`{"type":"function","function":{"name":"request_user_input","description":"request_user_input","parameters":{"properties":{},"type":"object"}}},` +
		`{"type":"function","function":{"name":"view_image","description":"view_image","parameters":{"properties":{},"type":"object"}}},` +
		`{"type":"function","function":{"name":"get_goal","description":"get_goal","parameters":{"properties":{},"type":"object"}}},` +
		`{"type":"function","function":{"name":"create_goal","description":"create_goal","parameters":{"properties":{},"type":"object"}}},` +
		`{"type":"function","function":{"name":"update_goal","description":"update_goal","parameters":{"properties":{},"type":"object"}}}],` +
		`"tool_choice":"auto","parallel_tool_calls":true,"max_tokens":4096,"temperature":0.2}`
	if string(chat) != want {
		t.Fatalf("the chat request =\n%s\nwant\n%s", chat, want)
	}
	if refusal := chatRefusal(chat); refusal != "" {
		t.Fatalf("Together's stand-in refused the bridged request: %s", refusal)
	}
	// probe-beam-shapes.log c:none: the same turn without its reasoning is refused.
	if refusal := chatRefusal([]byte(strings.Replace(string(chat), `"reasoning_content":"We need to inspect the repo.",`, "", 1))); !strings.Contains(refusal, "missing its reasoning") {
		t.Fatalf("a turn without reasoning: %q", refusal)
	}
}

func TestTheChatBridgeFoldsCodexsItemsIntoTurns(t *testing.T) {
	request := codexRequest("m", withInput(codexOpening(),
		thought("rs_1", "First thought."), said("msg_a", "Looking."), call("call_a"), thought("rs_2", "Second thought."), call("call_b"),
		output("call_a"), output("call_b"),
		map[string]any{"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": "A later note."}}},
		map[string]any{"type": "function_call_output", "call_id": "call_c", "output": []any{map[string]any{"type": "input_text", "text": "part one"}, map[string]any{"type": "input_image", "image_url": "x"}}},
		map[string]any{"type": "custom_tool_call", "call_id": "p", "name": "apply_patch", "input": "x"},
		map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "Summarised."}}, "content": nil},
		map[string]any{"type": "function_call", "call_id": "call_d", "name": "exec_command"},
	), func(r map[string]any) {
		r["instructions"] = "Be brief."
		r["tool_choice"] = map[string]any{"type": "function", "name": "exec_command"}
		delete(r, "parallel_tool_calls")
	})
	chat, err := ChatRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Messages []chatMessage    `json:"messages"`
		Tools    []map[string]any `json:"tools"`
		Choice   any              `json:"tool_choice"`
		Parallel *bool            `json:"parallel_tool_calls"`
	}
	json.Unmarshal(chat, &got)
	lines := []string{}
	for _, message := range got.Messages {
		calls := []string{}
		for _, c := range message.ToolCalls {
			calls = append(calls, c.ID+"("+c.Function.Arguments+")")
		}
		lines = append(lines, fmt.Sprintf("%s|%s|%s|%s|%s", message.Role, *message.Content, message.ReasoningContent, strings.Join(calls, ","), message.ToolCallID))
	}
	want := []string{
		"system|Be brief.|||",
		"system|You are a coding agent running in the Codex CLI.|||",
		"system|<skills_instructions>none</skills_instructions>\n\n<permissions instructions>danger-full-access</permissions instructions>|||",
		"user|<environment_context><cwd>/work</cwd></environment_context>|||",
		"user|Follow the plan in docs/plans/unit-converter.md.|||",
		`assistant|Looking.|First thought.` + "\n\n" + `Second thought.|call_a({"cmd":"cat plan.md"}),call_b({"cmd":"cat plan.md"})|`,
		"tool|Process exited with code 0\nOutput:\n# Plan|||call_a",
		"tool|Process exited with code 0\nOutput:\n# Plan|||call_b",
		"user|A later note.|||",
		"tool|part one|||call_c",
		"assistant||Summarised.|call_d({})|",
	}
	if strings.Join(lines, "\n---\n") != strings.Join(want, "\n---\n") {
		t.Fatalf("messages =\n%s\nwant\n%s", strings.Join(lines, "\n---\n"), strings.Join(want, "\n---\n"))
	}
	if len(got.Tools) != 7 || got.Parallel != nil || got.Choice != "auto" {
		t.Errorf("tools %d, parallel %v, choice %v: only function tools, and a choice that is not a name becomes auto", len(got.Tools), got.Parallel, got.Choice)
	}
}

// TestTheChatBridgeAnswersInTheResponsesEventsCodexReads: a chat answer with
// reasoning, text and two tool calls becomes the Responses stream round 1's
// Beam build ran on: created, in progress, each item added and done (the
// message with its text as one delta), completed with the usage.
func TestTheChatBridgeAnswersInTheResponsesEventsCodexReads(t *testing.T) {
	chat := []byte(`{"id":"chat-9","choices":[{"message":{"role":"assistant","content":"Running both.","reasoning_content":"Need the plan and the tests.",` +
		`"tool_calls":[{"id":"call_1","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":\"cat plan.md\"}"}},` +
		`{"type":"function","function":{"name":"exec_command","arguments":""}}]}}],` +
		`"usage":{"prompt_tokens":5580,"completion_tokens":50,"total_tokens":5630,"prompt_tokens_details":{"cached_tokens":5376},"completion_tokens_details":{"reasoning_tokens":12}}}`)
	counter := 0
	ids := func(prefix string) string { counter++; return fmt.Sprintf("%sid%d", prefix, counter) }
	response, err := ResponseFromChat(chat, json.RawMessage(`"reflection/Beam-1.0-501B-A23B"`), time.Unix(1762000000, 0), ids)
	if err != nil {
		t.Fatal(err)
	}
	events := string(ResponseEvents(response))
	blocks := strings.Split(strings.TrimSuffix(events, "\n\n"), "\n\n")
	kinds := []string{}
	for index, block := range blocks {
		kind, data, ok := strings.Cut(block, "\ndata: ")
		if !ok || !strings.HasPrefix(kind, "event: ") {
			t.Fatalf("event %d is not an SSE event: %q", index, block)
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(data), &decoded); err != nil || decoded["type"] != strings.TrimPrefix(kind, "event: ") || decoded["sequence_number"] != float64(index) {
			t.Fatalf("event %d = %s (%v)", index, data, err)
		}
		kinds = append(kinds, strings.TrimPrefix(kind, "event: "))
	}
	want := "response.created response.in_progress " +
		"response.output_item.added response.output_item.done " +
		"response.output_item.added response.output_text.delta response.output_text.done response.output_item.done " +
		"response.output_item.added response.output_item.done response.output_item.added response.output_item.done response.completed"
	if strings.Join(kinds, " ") != want {
		t.Fatalf("events = %v", kinds)
	}
	_, completed, _ := strings.Cut(blocks[len(blocks)-1], "\ndata: ")
	wantCompleted := `{"type":"response.completed","sequence_number":12,"response":{"created_at":1762000000,"id":"resp_chat-9","model":"reflection/Beam-1.0-501B-A23B","object":"response",` +
		`"output":[{"content":[{"text":"Need the plan and the tests.","type":"reasoning_text"}],"id":"rs_id1","summary":[],"type":"reasoning"},` +
		`{"content":[{"annotations":[],"text":"Running both.","type":"output_text"}],"id":"msg_id2","role":"assistant","status":"completed","type":"message"},` +
		`{"arguments":"{\"cmd\":\"cat plan.md\"}","call_id":"call_1","id":"fc_id3","name":"exec_command","status":"completed","type":"function_call"},` +
		`{"arguments":"{}","call_id":"call_id4","id":"fc_id5","name":"exec_command","status":"completed","type":"function_call"}],` +
		`"status":"completed","usage":{"input_tokens":5580,"input_tokens_details":{"cached_tokens":5376},"output_tokens":50,"output_tokens_details":{"reasoning_tokens":12},"total_tokens":5630}}}`
	if completed != wantCompleted {
		t.Fatalf("completed =\n%s\nwant\n%s", completed, wantCompleted)
	}
	if !strings.Contains(events, `"type":"response.output_text.delta","sequence_number":5,"item_id":"msg_id2","output_index":1,"content_index":0,"delta":"Running both."`) {
		t.Errorf("the message's delta is missing: %s", events)
	}
	if !strings.Contains(events, `"type":"response.created","sequence_number":0,"response":{"created_at":1762000000,"id":"resp_chat-9","model":"reflection/Beam-1.0-501B-A23B","object":"response","output":[],"status":"in_progress"}`) {
		t.Errorf("the stream does not open in progress: %s", events)
	}

	// No id, no usage, a reasoning field instead: still an answer.
	plain, err := ResponseFromChat([]byte(`{"choices":[{"message":{"content":"ok","reasoning":"Thought."}}]}`), nil, time.Unix(1, 0), ids)
	if err != nil || plain["id"] != "resp_id6" || len(plain["output"].([]map[string]any)) != 2 ||
		fmt.Sprint(plain["usage"]) != "map[input_tokens:0 input_tokens_details:map[cached_tokens:0] output_tokens:0 output_tokens_details:map[reasoning_tokens:0] total_tokens:0]" {
		t.Errorf("a bare answer = %v, %v", plain, err)
	}
}

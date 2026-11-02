package openmodel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// The probe cases of round 1 (2026-11-01): the request shapes Together AI's
// Responses API refused from Codex 0.163.0, as the server's probe logs record
// them (runtime/open-models/probe-*.log: the item types, which case answered
// 400 with what, and which answered 200), rebuilt here without the prompts.

// codexOpening is how Codex 0.163.0 opens every request's input
// (capture.log): two developer messages (the base prompt; skills and
// permissions), the environment and the prompt.
func codexOpening() []any {
	return []any{
		map[string]any{"type": "message", "id": "msg_1", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": "You are a coding agent running in the Codex CLI."}}},
		map[string]any{"type": "message", "id": "msg_2", "role": "developer", "content": []any{
			map[string]any{"type": "input_text", "text": "<skills_instructions>none</skills_instructions>"},
			map[string]any{"type": "input_text", "text": "<permissions instructions>danger-full-access</permissions instructions>"}}},
		map[string]any{"type": "message", "id": "msg_3", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "<environment_context><cwd>/work</cwd></environment_context>"}}},
		map[string]any{"type": "message", "id": "msg_4", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Follow the plan in docs/plans/unit-converter.md."}}},
	}
}

// reasoningItem is a reasoning item as Codex replays Together's: its content
// null, its text in encrypted_content.
func reasoningItem(id string) map[string]any {
	return map[string]any{"type": "reasoning", "id": id, "summary": []any{}, "content": nil, "encrypted_content": "opaque-" + id}
}

// thought is a reasoning item with its text, as a chat-bridged answer gives it.
func thought(id, text string) map[string]any {
	return map[string]any{"type": "reasoning", "id": id, "summary": []any{}, "content": []any{map[string]any{"type": "reasoning_text", "text": text}}}
}

func call(id string) map[string]any {
	return map[string]any{"type": "function_call", "id": "fc_" + id, "call_id": id, "name": "exec_command", "arguments": `{"cmd":"cat plan.md"}`, "status": "completed"}
}

func output(id string) map[string]any {
	return map[string]any{"type": "function_call_output", "call_id": id, "output": "Process exited with code 0\nOutput:\n# Plan"}
}

func said(id, text string) map[string]any {
	return map[string]any{"type": "message", "id": id, "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}}
}

// codexTools are the tools Codex 0.163.0 offers these models (capture.log):
// the namespace is tools[4].
func codexTools() []any {
	function := func(name string) map[string]any {
		return map[string]any{"type": "function", "name": name, "description": name, "strict": false, "parameters": map[string]any{"type": "object", "properties": map[string]any{}}}
	}
	return []any{function("exec_command"), function("write_stdin"), function("request_user_input"), function("view_image"),
		map[string]any{"type": "namespace", "name": "multi_agent_v1", "tools": []any{function("spawn_agent")}},
		function("get_goal"), function("create_goal"), function("update_goal")}
}

// codexRequest is a Responses request as Codex 0.163.0 sends it.
func codexRequest(model string, input []any, edit func(map[string]any)) []byte {
	request := map[string]any{
		"model": model, "input": input, "tools": codexTools(), "tool_choice": "auto", "parallel_tool_calls": true,
		"reasoning": map[string]any{}, "store": false, "stream": true, "include": []any{"reasoning.encrypted_content"},
		"prompt_cache_key": "01a19875-0000-7000-8000-000000000000",
	}
	if edit != nil {
		edit(request)
	}
	data, err := json.Marshal(request)
	if err != nil {
		panic(err)
	}
	return data
}

// togetherRefusal answers what Together answered a Responses request in
// round 1, with the probes' exact words, or "" for an HTTP 200.
func togetherRefusal(body []byte) string {
	var request struct {
		Model     string           `json:"model"`
		Input     []map[string]any `json:"input"`
		Tools     []map[string]any `json:"tools"`
		Reasoning *map[string]any  `json:"reasoning"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return "json_parse_error: Invalid JSON data"
	}
	for _, entry := range request.Input {
		if content, has := entry["content"]; has && content == nil && entry["type"] == "reasoning" {
			return "json_parse_error: Invalid JSON data: data did not match any variant of untagged enum ResponseInput"
		}
	}
	for index, tool := range request.Tools {
		if tool["type"] == "namespace" && request.Model == "Prism-ML/Ternary-Bonsai-27B" {
			return fmt.Sprintf("json_data_error: Invalid JSON data: Failed to deserialize the JSON body into the target type: tools[%d].type: unknown variant `namespace`, expected one of `function`, `web_search_preview`, `code_interpreter`, `mcp`", index)
		}
	}
	if request.Model == "deepseek-ai/DeepSeek-V4.1-Flash" && request.Reasoning != nil {
		if effort, _ := (*request.Reasoning)["effort"].(string); effort == "" || effort == "medium" {
			return `process_messages_failed: Failed to apply chat template: DeepSeek V4.1 reasoning_effort must be low, high, xhigh, max, or an integer within [1, 100]; got "medium"`
		}
	}
	// Together's conversion to its chat template: an assistant message and
	// each call become messages of their own; a call must be answered by its
	// output before any other message.
	type turn struct{ role, callID string }
	var messages []turn
	for _, entry := range request.Input {
		switch entry["type"] {
		case "function_call", "custom_tool_call":
			messages = append(messages, turn{"assistant-call", fmt.Sprint(entry["call_id"])})
		case "function_call_output", "custom_tool_call_output":
			messages = append(messages, turn{"tool", fmt.Sprint(entry["call_id"])})
		case "reasoning":
			messages = append(messages, turn{role: "reasoning"})
		default:
			messages = append(messages, turn{role: fmt.Sprint(entry["role"])})
		}
	}
	for index, message := range messages {
		if message.role != "assistant-call" {
			continue
		}
		if index+1 >= len(messages) || messages[index+1].role != "tool" || messages[index+1].callID != message.callID {
			return "invalid_tool_messages: incomplete parallel tool-call group: tool call '" + message.callID + "' has no tool response; all tool calls in a parallel group must be answered before continuing"
		}
	}
	if request.Model == "reflection/Beam-1.0-501B-A23B" {
		// Together's /responses never attaches a reasoning item to the turn
		// that made the call; Beam's template refuses that turn.
		for index, message := range messages {
			if message.role == "assistant-call" {
				return fmt.Sprintf("process_messages_failed: Failed to apply chat template: Failed to render template: invalid operation: The assistant turn at messages[%d] is missing its reasoning.", index)
			}
		}
	}
	return ""
}

// chatRefusal answers what Together answered Beam's Chat Completions request
// (probe-beam-shapes.log: c:none 400, c:rc 200), or "".
func chatRefusal(body []byte) string {
	var request struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return "json_parse_error: Invalid JSON data"
	}
	for index, message := range request.Messages {
		if message["role"] == "assistant" && message["tool_calls"] != nil {
			if text, _ := message["reasoning_content"].(string); text == "" {
				return fmt.Sprintf("process_messages_failed: Failed to apply chat template: Failed to render template: invalid operation: The assistant turn at messages[%d] is missing its reasoning.", index)
			}
		}
	}
	return ""
}

// fakeTogether answers as Together did: 400 with the probes' words, or a
// short Responses stream (or a chat answer) when the request would have
// passed. It records each request's path, body and headers.
type fakeTogether struct {
	requests []recorded
	answer   func(w http.ResponseWriter, r *http.Request, body []byte) bool
}

type recorded struct {
	path   string
	body   []byte
	header http.Header
}

func (f *fakeTogether) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body := make([]byte, 0)
	buffer := make([]byte, 32<<10)
	for {
		n, err := r.Body.Read(buffer)
		body = append(body, buffer[:n]...)
		if err != nil {
			break
		}
	}
	f.requests = append(f.requests, recorded{path: r.URL.Path, body: body, header: r.Header.Clone()})
	if f.answer != nil && f.answer(w, r, body) {
		return
	}
	refusal := ""
	switch {
	case strings.HasSuffix(r.URL.Path, "/responses"):
		refusal = togetherRefusal(body)
	case strings.HasSuffix(r.URL.Path, "/chat/completions"):
		refusal = chatRefusal(body)
	}
	if refusal != "" {
		code, message, _ := strings.Cut(refusal, ": ")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": message, "type": "invalid_request_error"}})
		return
	}
	if strings.HasSuffix(r.URL.Path, "/chat/completions") {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chat-1","choices":[{"message":{"role":"assistant","content":"done","reasoning_content":"Checked."}}],"usage":{"prompt_tokens":125,"completion_tokens":3,"total_tokens":128}}`)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\"}}\n\n"+
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[]}}\n\n")
}

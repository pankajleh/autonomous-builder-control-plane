package openmodel

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// The chat bridge (compat chat_completions_bridge). Together's /responses
// turns each reasoning item Codex replays into a message of its own, never
// the reasoning of the turn that made the call, and Beam 1.0's template then
// refuses every earlier assistant turn ("missing its reasoning … Send it back
// in 'reasoning_content'"); its /chat/completions takes reasoning_content on
// the assistant turn (round 1's probe-beam-shapes: c:rc HTTP 200). So for
// such a model the relay turns Codex's Responses request into one Chat
// Completions request (not streamed), and the answer into the Responses
// events Codex reads. This is the bridge that carried round 1's accepted
// Beam build (the trial relay's chat_bridge.py, version 4 to 6).

// ChatRequest answers the Chat Completions request for a Responses request.
func ChatRequest(responses []byte) ([]byte, error) {
	var request struct {
		Model             json.RawMessage   `json:"model"`
		Input             json.RawMessage   `json:"input"`
		Instructions      *string           `json:"instructions"`
		Tools             []json.RawMessage `json:"tools"`
		ToolChoice        json.RawMessage   `json:"tool_choice"`
		ParallelToolCalls *bool             `json:"parallel_tool_calls"`
		MaxOutputTokens   json.RawMessage   `json:"max_output_tokens"`
		Temperature       json.RawMessage   `json:"temperature"`
		TopP              json.RawMessage   `json:"top_p"`
	}
	decoder := json.NewDecoder(bytes.NewReader(responses))
	decoder.UseNumber()
	if err := decoder.Decode(&request); err != nil {
		return nil, err
	}
	messages, err := chatMessages(request.Input)
	if err != nil {
		return nil, err
	}
	if request.Model == nil {
		request.Model = json.RawMessage("null")
	}
	if request.Instructions != nil && *request.Instructions != "" {
		messages = append([]chatMessage{{Role: "system", Content: request.Instructions}}, messages...)
	}
	out := object{{key: "model", value: request.Model}, {key: "messages", value: mustJSON(messages)}, {key: "stream", value: json.RawMessage("false")}}
	var tools []json.RawMessage
	for _, raw := range request.Tools {
		var tool struct {
			Type        string          `json:"type"`
			Name        json.RawMessage `json:"name"`
			Description json.RawMessage `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		}
		if json.Unmarshal(raw, &tool) != nil || tool.Type != "function" {
			continue
		}
		function := object{}
		for _, field := range []member{{"name", tool.Name}, {"description", tool.Description}, {"parameters", tool.Parameters}} {
			if field.value != nil {
				function = append(function, field)
			}
		}
		tools = append(tools, object{{key: "type", value: json.RawMessage(`"function"`)}, {key: "function", value: function.encode()}}.encode())
	}
	if len(tools) > 0 {
		out = append(out, member{key: "tools", value: encodeItems(rawItems(tools))})
		choice := json.RawMessage(`"auto"`)
		var named string
		if json.Unmarshal(request.ToolChoice, &named) == nil {
			choice = request.ToolChoice
		}
		out = append(out, member{key: "tool_choice", value: choice})
		if request.ParallelToolCalls != nil {
			out = append(out, member{key: "parallel_tool_calls", value: mustJSON(*request.ParallelToolCalls)})
		}
	}
	if present(request.MaxOutputTokens) && string(request.MaxOutputTokens) != "0" {
		out = append(out, member{key: "max_tokens", value: request.MaxOutputTokens})
	}
	for _, field := range []member{{"temperature", request.Temperature}, {"top_p", request.TopP}} {
		if present(field.value) {
			out = append(out, field)
		}
	}
	return out.encode(), nil
}

// chatMessage is one Chat Completions message. Content is a pointer so an
// assistant turn with calls and no text still carries "content": "".
type chatMessage struct {
	Role             string     `json:"role"`
	Content          *string    `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []chatCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

type chatCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// chatMessages folds Codex's input items into messages: consecutive
// reasoning, call and assistant-message items are one assistant turn, whose
// reasoning goes back as reasoning_content; a call's output is a tool
// message; a developer or system message is a system message before the
// conversation starts and a user message after.
func chatMessages(input json.RawMessage) ([]chatMessage, error) {
	var single string
	if json.Unmarshal(input, &single) == nil {
		return []chatMessage{{Role: "user", Content: &single}}, nil
	}
	var entries []json.RawMessage
	if len(input) != 0 && json.Unmarshal(input, &entries) != nil {
		return nil, errors.New("the request's input is neither text nor a list")
	}
	messages := []chatMessage{}
	var turn *chatMessage
	var reasoning []string
	flush := func() {
		if turn != nil {
			turn.ReasoningContent = strings.Join(reasoning, "\n\n")
			messages = append(messages, *turn)
		}
		turn, reasoning = nil, nil
	}
	open := func() {
		if turn == nil {
			empty := ""
			turn = &chatMessage{Role: "assistant", Content: &empty}
		}
	}
	for _, raw := range entries {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			flush()
			messages = append(messages, chatMessage{Role: "user", Content: &text})
			continue
		}
		var entry struct {
			Type      string          `json:"type"`
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			Summary   json.RawMessage `json:"summary"`
			CallID    string          `json:"call_id"`
			Name      string          `json:"name"`
			Arguments string          `json:"arguments"`
			Output    json.RawMessage `json:"output"`
		}
		if json.Unmarshal(raw, &entry) != nil {
			continue
		}
		kind := entry.Type
		if kind == "" && entry.Role != "" {
			kind = "message"
		}
		switch {
		case kind == "message" && entry.Role == "assistant":
			open()
			if said := partsText(entry.Content, "output_text", "text", "input_text"); said != "" {
				if *turn.Content == "" {
					*turn.Content = said
				} else {
					joined := strings.TrimSpace(*turn.Content + "\n\n" + said)
					turn.Content = &joined
				}
			}
		case kind == "reasoning":
			open()
			thought := partsText(entry.Content, "reasoning_text")
			if thought == "" {
				thought = partsText(entry.Summary, "summary_text")
			}
			if thought != "" {
				reasoning = append(reasoning, thought)
			}
		case kind == "function_call":
			open()
			call := chatCall{ID: entry.CallID, Type: "function"}
			call.Function.Name, call.Function.Arguments = entry.Name, entry.Arguments
			if call.Function.Arguments == "" {
				call.Function.Arguments = "{}"
			}
			turn.ToolCalls = append(turn.ToolCalls, call)
		case kind == "function_call_output":
			flush()
			output := outputText(entry.Output)
			messages = append(messages, chatMessage{Role: "tool", ToolCallID: entry.CallID, Content: &output})
		case kind == "message":
			flush()
			role := entry.Role
			if role == "developer" || role == "system" {
				role = "system"
				for _, earlier := range messages {
					if earlier.Role != "system" {
						role = "user"
						break
					}
				}
			}
			said := partsText(entry.Content, "input_text", "text", "output_text")
			messages = append(messages, chatMessage{Role: role, Content: &said})
		}
		// Other items (none seen from Codex for a bridged model) are left out.
	}
	flush()
	return messages, nil
}

// partsText answers a content value's text: the string itself, or the text
// of its parts of those types, joined by blank lines.
func partsText(raw json.RawMessage, kinds ...string) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	out := []string{}
	for _, part := range parts {
		if part.Text != nil && containsString(kinds, part.Type) {
			out = append(out, *part.Text)
		}
	}
	return strings.Join(out, "\n\n")
}

// outputText answers a call's output as text: a string as it is, a list of
// parts by their text, anything else as its JSON.
func outputText(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	switch {
	case !present(trimmed):
		return "null"
	case trimmed[0] == '"':
		var text string
		json.Unmarshal(trimmed, &text)
		return text
	case trimmed[0] == '[':
		return partsText(trimmed, "input_text", "output_text", "text")
	}
	return string(trimmed)
}

// ResponseIDs makes the ids of a bridged answer's items; tests replace it.
type ResponseIDs func(prefix string) string

func randomID(prefix string) string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(value)
}

// ResponseFromChat answers the Responses object for a Chat Completions
// answer: the reasoning, the message and each tool call as items, and the
// usage with its cached and reasoning tokens. It is always "completed", as
// Together's own /responses answers.
func ResponseFromChat(chat []byte, model json.RawMessage, now time.Time, ids ResponseIDs) (map[string]any, error) {
	if ids == nil {
		ids = randomID
	}
	var answer struct {
		ID      json.RawMessage `json:"id"`
		Choices []struct {
			Message struct {
				Content          *string `json:"content"`
				ReasoningContent *string `json:"reasoning_content"`
				Reasoning        *string `json:"reasoning"`
				ToolCalls        []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens        json.Number `json:"prompt_tokens"`
			CompletionTokens    json.Number `json:"completion_tokens"`
			TotalTokens         json.Number `json:"total_tokens"`
			CachedTokens        json.Number `json:"cached_tokens"`
			PromptTokensDetails struct {
				CachedTokens json.Number `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionTokensDetails struct {
				ReasoningTokens json.Number `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	decoder := json.NewDecoder(bytes.NewReader(chat))
	decoder.UseNumber()
	if err := decoder.Decode(&answer); err != nil {
		return nil, err
	}
	id := ""
	if json.Unmarshal(answer.ID, &id) != nil || id == "" {
		if trimmed := strings.TrimSpace(string(answer.ID)); trimmed != "" && trimmed != "null" {
			id = trimmed
		} else {
			id = ids("")
		}
	}
	items := []map[string]any{}
	if len(answer.Choices) > 0 {
		message := answer.Choices[0].Message
		thought := message.ReasoningContent
		if thought == nil || *thought == "" {
			thought = message.Reasoning
		}
		if thought != nil && *thought != "" {
			items = append(items, map[string]any{"type": "reasoning", "id": ids("rs_"), "summary": []any{},
				"content": []any{map[string]any{"type": "reasoning_text", "text": *thought}}})
		}
		if message.Content != nil && *message.Content != "" {
			items = append(items, map[string]any{"type": "message", "id": ids("msg_"), "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": *message.Content, "annotations": []any{}}}})
		}
		for _, call := range message.ToolCalls {
			callID, arguments := call.ID, call.Function.Arguments
			if callID == "" {
				callID = ids("call_")
			}
			if arguments == "" {
				arguments = "{}"
			}
			items = append(items, map[string]any{"type": "function_call", "id": ids("fc_"), "call_id": callID,
				"name": call.Function.Name, "arguments": arguments, "status": "completed"})
		}
	}
	number := func(values ...json.Number) json.Number {
		for _, value := range values {
			if value != "" && value != "0" {
				return value
			}
		}
		return "0"
	}
	usage := answer.Usage
	total := usage.TotalTokens
	if total == "" {
		prompt, _ := number(usage.PromptTokens).Int64()
		completion, _ := number(usage.CompletionTokens).Int64()
		total = json.Number(jsonInt(prompt + completion))
	}
	if model == nil {
		model = json.RawMessage("null")
	}
	return map[string]any{
		"id": "resp_" + id, "object": "response", "created_at": now.Unix(), "model": model, "status": "completed",
		"output": items,
		"usage": map[string]any{
			"input_tokens":          number(usage.PromptTokens),
			"input_tokens_details":  map[string]any{"cached_tokens": number(usage.PromptTokensDetails.CachedTokens, usage.CachedTokens)},
			"output_tokens":         number(usage.CompletionTokens),
			"output_tokens_details": map[string]any{"reasoning_tokens": number(usage.CompletionTokensDetails.ReasoningTokens)},
			"total_tokens":          total,
		},
	}, nil
}

// ResponseEvents answers the Responses stream Codex reads for a bridged
// answer: created and in progress, then each item added (a message with its
// text as one delta) and done, then completed with the whole response.
func ResponseEvents(response map[string]any) []byte {
	var b bytes.Buffer
	sequence := 0
	event := func(kind string, fields object) {
		data := append(object{{key: "type", value: mustJSON(kind)}, {key: "sequence_number", value: mustJSON(sequence)}}, fields...)
		b.WriteString("event: " + kind + "\ndata: ")
		b.Write(data.encode())
		b.WriteString("\n\n")
		sequence++
	}
	started := map[string]any{"id": response["id"], "object": "response", "created_at": response["created_at"],
		"model": response["model"], "status": "in_progress", "output": []any{}}
	event("response.created", object{{key: "response", value: mustJSON(started)}})
	event("response.in_progress", object{{key: "response", value: mustJSON(started)}})
	items, _ := response["output"].([]map[string]any)
	for index, entry := range items {
		at := mustJSON(index)
		event("response.output_item.added", object{{key: "output_index", value: at}, {key: "item", value: mustJSON(entry)}})
		if entry["type"] == "message" {
			text := entry["content"].([]any)[0].(map[string]any)["text"]
			id := mustJSON(entry["id"])
			event("response.output_text.delta", object{{key: "item_id", value: id}, {key: "output_index", value: at}, {key: "content_index", value: mustJSON(0)}, {key: "delta", value: mustJSON(text)}})
			event("response.output_text.done", object{{key: "item_id", value: id}, {key: "output_index", value: at}, {key: "content_index", value: mustJSON(0)}, {key: "text", value: mustJSON(text)}})
		}
		event("response.output_item.done", object{{key: "output_index", value: at}, {key: "item", value: mustJSON(entry)}})
	}
	event("response.completed", object{{key: "response", value: mustJSON(response)}})
	return b.Bytes()
}

func mustJSON(value any) json.RawMessage {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		panic(err)
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

func rawItems(raws []json.RawMessage) []item {
	out := make([]item, len(raws))
	for index, raw := range raws {
		out[index] = item{raw: raw}
	}
	return out
}

func present(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) != 0 && string(trimmed) != "null"
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func jsonInt(value int64) string { return string(mustJSON(value)) }

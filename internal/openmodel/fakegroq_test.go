package openmodel

import "encoding/json"

// groqRefusal answers what Groq's Responses API answered gpt-oss-120b's
// requests on 2026-11-07, with Groq's words, or "" for an HTTP 200 (the Groq
// probes on the build host: Codex 0.163.0's own requests as ABCP's relay sent
// them, cut to a few tools so that each fitted the free plan). Groq reads
// the body in order: a tool that is not a function (Codex's custom
// apply_patch, its multi_agent_v1 namespace) makes the whole body invalid to
// it, with no field named; then client_metadata is a field it does not know;
// then include is one it does not support. store false, prompt_cache_key,
// parallel_tool_calls, the reasoning effort and Codex's replayed items (a
// reasoning item with its reasoning_text content and a null
// encrypted_content, calls and their outputs) it takes.
func groqRefusal(body []byte) string {
	var request map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil {
		return "invalid JSON body"
	}
	var tools []map[string]any
	if raw, ok := request["tools"]; ok && json.Unmarshal(raw, &tools) != nil {
		return "invalid JSON body"
	}
	for _, tool := range tools {
		if tool["type"] != "function" {
			return "invalid JSON body"
		}
	}
	if _, ok := request["client_metadata"]; ok {
		return "unknown field `client_metadata` in request body"
	}
	if _, ok := request["include"]; ok {
		return "Field 'include' is not supported"
	}
	return ""
}

// codexClientMetadata is the shape of the client_metadata Codex 0.163.0 sends
// with every request (the 2026-11-07 capture), its values shortened.
func codexClientMetadata() map[string]any {
	return map[string]any{"thread_id": "01a1b4be-0000-7000-8000-000000000000", "turn_id": "01a1b4be-0000-7000-8000-000000000001",
		"session_id": "01a1b4be-0000-7000-8000-000000000000", "x-codex-turn-metadata": `{"sandbox":"none","model":"openai/gpt-oss-120b"}`}
}

// groqCompat is gpt-oss-120b's compat list on Groq.
var groqCompat = []string{CompatDropNamespaceTools, CompatDropClientMetadata, CompatDropInclude, CompatCustomToolAsFunction}

//go:build linux

package openmodel

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestCodexBuildsThroughTheRelayWithItsCatalogEntry runs the real Codex CLI
// (ABCP_TEST_CODEX names its binary; 0.163.0 is the host's) through a build's
// relay to a fake provider, with gpt-oss-120b's manifest entry: the relay's
// three turn fixes and a catalog entry with apply_patch. It checks what Codex
// 0.163.0 makes of the catalog (it ignores keys it does not know, so only
// what it sends shows that an entry took): apply_patch offered as Codex's
// freeform tool, no tool_search, the base prompt Codex sends on fallback
// metadata unchanged, tool output cut at about 40,000 bytes instead of
// 10,000; and that Codex's replay of a turn reaches the provider fixed and an
// apply_patch call edits the file. Nothing reaches any real provider.
func TestCodexBuildsThroughTheRelayWithItsCatalogEntry(t *testing.T) {
	binary := os.Getenv("ABCP_TEST_CODEX")
	if binary == "" {
		t.Skip("ABCP_TEST_CODEX names no Codex binary")
	}
	var mu sync.Mutex
	var bodies [][]byte
	var authorized []bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `{"object":"list","data":[{"id":"openai/gpt-oss-120b","object":"model"}]}`)
			return
		}
		body := readAll(t, r)
		mu.Lock()
		bodies = append(bodies, body)
		authorized = append(authorized, r.Header.Get("Authorization") == "Bearer "+testKey)
		mu.Unlock()
		var request struct {
			Input []map[string]any `json:"input"`
		}
		json.Unmarshal(body, &request)
		kinds := map[string]int{}
		for _, entry := range request.Input {
			kinds[fmt.Sprint(entry["type"])]++
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch {
		case kinds["function_call_output"] == 0:
			// A turn the way Together streams one: reasoning (its content
			// null), two parallel calls, then the assistant's message.
			writeEvents(w, "resp_1",
				map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{}, "content": nil},
				map[string]any{"type": "function_call", "id": "fc_a", "call_id": "call_a", "name": "exec_command", "arguments": `{"cmd":"seq 1 40000"}`, "status": "completed"},
				map[string]any{"type": "function_call", "id": "fc_b", "call_id": "call_b", "name": "exec_command", "arguments": `{"cmd":"echo b"}`, "status": "completed"},
				map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Reading.", "annotations": []any{}}}})
		case kinds["custom_tool_call_output"] == 0:
			writeEvents(w, "resp_2", map[string]any{"type": "custom_tool_call", "id": "ctc_1", "call_id": "call_p", "name": "apply_patch",
				"input": "*** Begin Patch\n*** Add File: hello.txt\n+hello from apply_patch\n*** End Patch\n", "status": "completed"})
		default:
			writeEvents(w, "resp_3", map[string]any{"type": "message", "id": "msg_3", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "done", "annotations": []any{}}}})
		}
	}))
	defer server.Close()

	provider := testProvider(t)
	provider.ID, provider.BaseURL, provider.Relay = "together", server.URL+"/v1", true
	provider.Models[0].Compat = []string{CompatDropNullReasoningContent, CompatMessageBeforeCalls, CompatOutputAfterCall}
	provider.Models[0].CodexCatalog = &CodexCatalog{ApplyPatch: true}
	if err := provider.Validate(); err != nil {
		t.Fatal(err)
	}
	start, err := Begin(context.Background(), provider, "gpt-oss-120b", StartOptions{Run: "codex-check"})
	if err != nil {
		t.Fatal(err)
	}
	defer start.Release()
	work := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "exec", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox",
		"-c", "features.multi_agent=true", "-c", "model_reasoning_effort=high", "Read the plan, then write hello.txt.")
	command.Dir = work
	command.Env = start.Environment([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()})
	command.Stdin = nil
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("codex: %v\n%s", err, output)
	}
	if written, err := os.ReadFile(filepath.Join(work, "hello.txt")); err != nil || string(written) != "hello from apply_patch\n" {
		t.Fatalf("apply_patch did not write the file: %q, %v\n%s", written, err, output)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 3 || slices.Contains(authorized, false) {
		t.Fatalf("the provider saw %d requests, authorized %v", len(bodies), authorized)
	}

	var first struct {
		Tools []map[string]any `json:"tools"`
		Input []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
	}
	json.Unmarshal(bodies[0], &first)
	tools := []string{}
	for _, tool := range first.Tools {
		tools = append(tools, fmt.Sprint(tool["type"], ":", tool["name"]))
	}
	if !slices.Contains(tools, "custom:apply_patch") || slices.ContainsFunc(tools, func(t string) bool { return strings.HasPrefix(t, "tool_search") }) ||
		!slices.Contains(tools, "namespace:multi_agent_v1") || !slices.Contains(tools, "function:exec_command") {
		t.Errorf("the tools Codex offers = %v", tools)
	}
	if len(first.Input) == 0 || len(first.Input[0].Content) == 0 || first.Input[0].Content[0].Text != codexBaseInstructions {
		t.Error("the base prompt is not the one Codex sends on fallback metadata")
	}

	// Codex's replay of the first turn, as the provider got it from the relay.
	var second struct {
		Input []map[string]any `json:"input"`
	}
	json.Unmarshal(bodies[1], &second)
	order := []string{}
	for _, entry := range second.Input {
		switch entry["type"] {
		case "reasoning":
			if _, has := entry["content"]; has && entry["content"] == nil {
				t.Error("the relay passed a reasoning item with content null")
			}
			order = append(order, "reasoning")
		case "message":
			if entry["role"] == "assistant" {
				order = append(order, "message")
			}
		case "function_call":
			order = append(order, "call "+fmt.Sprint(entry["call_id"]))
		case "function_call_output":
			order = append(order, "output "+fmt.Sprint(entry["call_id"]))
			if entry["call_id"] == "call_a" {
				if text := fmt.Sprint(entry["output"]); len(text) < 30000 || len(text) > 45000 {
					t.Errorf("seq's output reached the model at %d bytes", len(text))
				}
			}
		}
	}
	if strings.Join(order, ", ") != "reasoning, message, call call_a, output call_a, call call_b, output call_b" {
		t.Errorf("the replayed turn reached the provider as %v", order)
	}
	logged, err := relayLogLines(filepath.Join(start.Home, RelayLogFile), 3)
	if err != nil || strings.Count(string(logged), "\n") < 3 || strings.Contains(string(logged), testKey) ||
		!strings.Contains(string(logged), "run=codex-check together/gpt-oss-120b POST /v1/responses model=openai/gpt-oss-120b status=200 fixes=drop_null_reasoning_content:1,message_before_calls:1,output_after_call:2") {
		t.Errorf("the relay's log = %s, %v", logged, err)
	}
	t.Logf("codex through the relay:\n%s\nrelay log:\n%s", output, logged)
}

// relayLogLines reads the relay's log once it holds n lines (the relay logs a
// request once its answer is out), or after two seconds.
func relayLogLines(path string, n int) ([]byte, error) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if (err == nil && strings.Count(string(data), "\n") >= n) || time.Now().After(deadline) {
			return data, err
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func readAll(t *testing.T, r *http.Request) []byte {
	t.Helper()
	data := make([]byte, 0, r.ContentLength)
	buffer := make([]byte, 32<<10)
	for {
		n, err := r.Body.Read(buffer)
		data = append(data, buffer[:n]...)
		if err != nil {
			return data
		}
	}
}

// writeEvents answers one Responses stream with these output items.
func writeEvents(w http.ResponseWriter, id string, items ...map[string]any) {
	write := func(event map[string]any) {
		data, _ := json.Marshal(event)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], data)
	}
	write(map[string]any{"type": "response.created", "response": map[string]any{"id": id, "object": "response", "status": "in_progress", "output": []any{}}})
	for index, entry := range items {
		write(map[string]any{"type": "response.output_item.added", "output_index": index, "item": entry})
		write(map[string]any{"type": "response.output_item.done", "output_index": index, "item": entry})
	}
	write(map[string]any{"type": "response.completed", "response": map[string]any{"id": id, "object": "response", "status": "completed", "output": items,
		"usage": map[string]any{"input_tokens": 10, "input_tokens_details": map[string]any{"cached_tokens": 0}, "output_tokens": 5,
			"output_tokens_details": map[string]any{"reasoning_tokens": 0}, "total_tokens": 15}}})
}

// TestCodexBuildsThroughTheChatBridge runs the real Codex CLI (as above)
// through a build's relay with compat chat_completions_bridge to a fake
// provider's /chat/completions: Codex reads the bridged answer (reasoning and
// a tool call), runs the call, and its replay reaches the provider with the
// turn's reasoning as reasoning_content.
func TestCodexBuildsThroughTheChatBridge(t *testing.T) {
	binary := os.Getenv("ABCP_TEST_CODEX")
	if binary == "" {
		t.Skip("ABCP_TEST_CODEX names no Codex binary")
	}
	var mu sync.Mutex
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+testKey || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, `{"error":{"message":"unexpected"}}`, http.StatusNotFound)
			return
		}
		var body map[string]any
		json.Unmarshal(readAll(t, r), &body)
		mu.Lock()
		bodies = append(bodies, body)
		turn := len(bodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if turn == 1 {
			fmt.Fprint(w, `{"id":"chat-1","choices":[{"message":{"role":"assistant","content":"","reasoning_content":"I should look first.",
				"tool_calls":[{"id":"call_1","type":"function","function":{"name":"exec_command","arguments":"{\"cmd\":\"echo bridged\"}"}}]}}],
				"usage":{"prompt_tokens":120,"completion_tokens":20,"total_tokens":140,"prompt_tokens_details":{"cached_tokens":100}}}`)
			return
		}
		fmt.Fprint(w, `{"id":"chat-2","choices":[{"message":{"role":"assistant","content":"all done","reasoning_content":"It printed."}}],
			"usage":{"prompt_tokens":150,"completion_tokens":5,"total_tokens":155}}`)
	}))
	defer server.Close()

	provider := testProvider(t)
	provider.ID, provider.BaseURL, provider.Relay = "together", server.URL+"/v1", true
	provider.Models[1].Compat = []string{CompatChatBridge}
	provider.Models[1].CodexCatalog = &CodexCatalog{}
	start, err := Begin(context.Background(), provider, "devstral", StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer start.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "exec", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox", "Run echo, then say done.")
	command.Dir = t.TempDir()
	command.Env = start.Environment([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()})
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "all done") || !strings.Contains(string(output), "bridged") {
		t.Fatalf("codex through the bridge: %v\n%s", err, output)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("the provider saw %d chat requests", len(bodies))
	}
	messages, _ := bodies[1]["messages"].([]any)
	var assistant, tool map[string]any
	for _, entry := range messages {
		message := entry.(map[string]any)
		switch message["role"] {
		case "assistant":
			assistant = message
		case "tool":
			tool = message
		}
	}
	if assistant == nil || assistant["reasoning_content"] != "I should look first." || tool == nil || tool["tool_call_id"] != "call_1" ||
		!strings.Contains(fmt.Sprint(tool["content"]), "bridged") || bodies[1]["stream"] != false {
		t.Errorf("Codex's replay reached the provider as %v", messages)
	}
	if calls, _ := assistant["tool_calls"].([]any); len(calls) != 1 {
		t.Errorf("the replayed turn's calls = %v", assistant["tool_calls"])
	}
	logged, _ := relayLogLines(filepath.Join(start.Home, RelayLogFile), 2)
	if strings.Count(string(logged), "fixes=chat_completions_bridge:1") != 2 || strings.Contains(string(logged), testKey) {
		t.Errorf("the relay's log = %s", logged)
	}
}

//go:build linux

package run

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

	"github.com/pankajleh/autonomous-builder-control-plane/internal/openmodel"
)

// TestCodexBuildsThroughTheRelayInsideTheWall runs the real Codex CLI (ABCP_TEST_CODEX names its binary, as for
// internal/openmodel's TestCodexBuildsThrough tests) inside a build's wall, through the build's relay to a fake
// provider. The relay stays where it is, in abcp run on the host's loopback; phase 1 keeps the host's network, so
// Codex inside the wall reaches 127.0.0.1:<port> as before. The build's Codex home is its own (config.toml and the
// model's catalog written there), its session lands in <the provider's folder>/runs/<run>, and the relay's log stays in
// the model's slot home outside the wall. Nothing reaches any real provider.
func TestCodexBuildsThroughTheRelayInsideTheWall(t *testing.T) {
	binary := os.Getenv("ABCP_TEST_CODEX")
	if binary == "" {
		t.Skip("ABCP_TEST_CODEX names no Codex binary")
	}
	bwrap := requireBwrap(t)
	if resolved, err := filepath.EvalSymlinks(binary); err == nil {
		binary = resolved
	}
	const key = "tgp_test_key_value_never_shown_7Rk2"
	var mu sync.Mutex
	var bodies [][]byte
	var authorized []bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `{"object":"list","data":[{"id":"openai/gpt-oss-120b","object":"model"}]}`)
			return
		}
		body := make([]byte, 0, r.ContentLength)
		buffer := make([]byte, 32<<10)
		for {
			n, err := r.Body.Read(buffer)
			body = append(body, buffer[:n]...)
			if err != nil {
				break
			}
		}
		mu.Lock()
		bodies = append(bodies, body)
		authorized = append(authorized, r.Header.Get("Authorization") == "Bearer "+key)
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
			writeResponseEvents(w, "resp_1",
				map[string]any{"type": "reasoning", "id": "rs_1", "summary": []any{}, "content": nil},
				map[string]any{"type": "function_call", "id": "fc_a", "call_id": "call_a", "name": "exec_command", "arguments": `{"cmd":"ls -A /home; test -e /var/run/docker.sock && echo docker-visible; echo home=$HOME"}`, "status": "completed"},
				map[string]any{"type": "message", "id": "msg_1", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Looking.", "annotations": []any{}}}})
		case kinds["custom_tool_call_output"] == 0:
			writeResponseEvents(w, "resp_2", map[string]any{"type": "custom_tool_call", "id": "ctc_1", "call_id": "call_p", "name": "apply_patch",
				"input": "*** Begin Patch\n*** Add File: hello.txt\n+hello from inside the wall\n*** End Patch\n", "status": "completed"})
		default:
			writeResponseEvents(w, "resp_3", map[string]any{"type": "message", "id": "msg_3", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": "done", "annotations": []any{}}}})
		}
	}))
	defer server.Close()

	wall, dirs, err := writeTestWall(t, bwrap, nil)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(dirs.root, "runtime", "together-api-key")
	writeTestFile(t, keyFile, []byte(key+"\n"), 0o600)
	provider := openmodel.Provider{
		ID: "together", BaseURL: server.URL + "/v1", KeyFile: keyFile, WireAPI: "responses", Relay: true,
		CodexHome: filepath.Join(dirs.home, ".codex", "lanes", "together"), QueueTimeout: "1s",
		Models: []openmodel.Model{{Name: "gpt-oss-120b", ID: "openai/gpt-oss-120b", Origin: "US", ContextWindow: 131072,
			Efforts: []string{"low", "medium", "high"}, DefaultEffort: "high",
			Compat:       []string{openmodel.CompatDropNullReasoningContent, openmodel.CompatMessageBeforeCalls, openmodel.CompatOutputAfterCall},
			CodexCatalog: &openmodel.CodexCatalog{ApplyPatch: true}}},
	}
	if err := provider.Validate(); err != nil {
		t.Fatal(err)
	}
	start, err := openmodel.Begin(context.Background(), provider, "gpt-oss-120b", openmodel.StartOptions{Run: "codex-wall-check"})
	if err != nil {
		t.Fatal(err)
	}
	defer start.Release()
	slotHome := start.Home

	const runID = "admission-codex-wall"
	folder := wall.RunFolder(runID)
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	w := &walledRun{wall: wall, runID: runID, folder: folder, repository: filepath.Join(folder, "repo"), env: map[string]string{},
		home: filepath.Join(folder, "home"), tmp: filepath.Join(folder, "tmp"), engineHome: filepath.Join(folder, "engine-home")}
	work := filepath.Join(w.folder, "work")
	for _, dir := range []string{w.home, w.tmp, w.engineHome, work} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	w.evidence = map[string]any{}
	if err := w.prepareEngine("codex", nil, start); err != nil {
		t.Fatal(err)
	}
	if start.Home != w.engineHome {
		t.Fatalf("CODEX_HOME = %s, want the build's own %s", start.Home, w.engineHome)
	}
	config, err := os.ReadFile(filepath.Join(w.engineHome, "config.toml"))
	if err != nil || strings.Contains(string(config), slotHome) || !strings.Contains(string(config), start.Relay.URL()) ||
		!strings.Contains(string(config), filepath.Join(w.engineHome, openmodel.CatalogFile)) {
		t.Fatalf("the build's Codex settings = %s, %v", config, err)
	}
	args, err := wall.Args(WallSpec{RunFolder: w.folder, Home: w.home, Tmp: w.tmp, Chdir: work, ReadOnly: []string{binary}, Mounts: w.mounts})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	argv := append(args, binary, "exec", "--skip-git-repo-check", "--dangerously-bypass-approvals-and-sandbox",
		"-c", "model_reasoning_effort=high", "Look around, then write hello.txt.")
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Env = w.environment(start.Environment([]string{"PATH=/usr/bin:/bin", "HOME=" + dirs.home}))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("codex in the wall: %v\n%s", err, output)
	}
	if written, err := os.ReadFile(filepath.Join(work, "hello.txt")); err != nil || string(written) != "hello from inside the wall\n" {
		t.Fatalf("apply_patch did not write the file in the wall: %q, %v\n%s", written, err, output)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 3 || slices.Contains(authorized, false) {
		t.Fatalf("the provider saw %d requests through the relay, authorized %v\n%s", len(bodies), authorized, output)
	}
	// What the agent's command saw inside: only the run's own home, no Docker socket.
	var second struct {
		Input []map[string]any `json:"input"`
	}
	json.Unmarshal(bodies[1], &second)
	seen := ""
	for _, entry := range second.Input {
		if entry["type"] == "function_call_output" {
			seen += fmt.Sprint(entry["output"])
		}
	}
	if strings.Contains(seen, "docker-visible") || !strings.Contains(seen, "home="+dirs.home) {
		t.Errorf("the agent's command saw %q", seen)
	}
	// The session is where the meter reads (<lane folder>/runs/<run>/YYYY/MM/DD/rollout-*.jsonl, six levels at most).
	var sessions []string
	filepath.WalkDir(filepath.Join(provider.CodexHome, "runs", runID), func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && strings.HasPrefix(entry.Name(), "rollout-") {
			rel, _ := filepath.Rel(provider.CodexHome, path)
			if strings.Count(rel, string(filepath.Separator)) < 6 {
				sessions = append(sessions, rel)
			}
		}
		return nil
	})
	if len(sessions) == 0 {
		t.Errorf("no Codex session under %s/runs/%s", provider.CodexHome, runID)
	}
	if logged, err := os.ReadFile(filepath.Join(slotHome, openmodel.RelayLogFile)); err != nil || !strings.Contains(string(logged), "run=codex-wall-check together/gpt-oss-120b POST /v1/responses") || strings.Contains(string(logged), key) {
		t.Errorf("the relay's log in the slot home = %s, %v", logged, err)
	}
	t.Logf("codex inside the wall:\n%s\nsessions: %v", output, sessions)
}

// writeResponseEvents answers one Responses stream with these output items.
func writeResponseEvents(w http.ResponseWriter, id string, items ...map[string]any) {
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

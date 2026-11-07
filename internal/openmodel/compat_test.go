package openmodel

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// cleanReasoning is a reasoning item as the turn probes sent it (relay v1
// had already removed its null content).
func cleanReasoning(id string) map[string]any {
	item := reasoningItem(id)
	delete(item, "content")
	return item
}

func withInput(opening []any, items ...any) []any { return append(opening, items...) }

// TestEachFixTurnsARefusalFromTheProbesIntoAnAnswer sends each probe's
// request to a stand-in for Together that refuses what Together refused, with
// the probe logs' words: as Codex sent it (400), then with the model's fix
// (200).
func TestEachFixTurnsARefusalFromTheProbesIntoAnAnswer(t *testing.T) {
	type probe struct {
		fix, model, log string
		request         []byte
		refused         string
	}
	nemotron, muse := "nvidia/NVIDIA-Nemotron-3.5-Super-VL", "meta-models/Muse-Spark-Open-816B-A42B"
	// probe-nemotron2.log: 23 items, the last turn two parallel calls, its
	// message after them, then both outputs.
	middle := []any{}
	for _, id := range []string{"call_1", "call_2", "call_3", "call_4"} {
		middle = append(middle, cleanReasoning("rs_"+id), call(id), output(id))
	}
	middle = append(middle, call("call_5"), output("call_5"))
	parallel := withInput(append(codexOpening(), middle...), call("call_a"), call("call_b"), said("msg_9", "Checking both."), output("call_a"), output("call_b"))
	if len(parallel) != 23 {
		t.Fatalf("the nemotron2 probe has %d items", len(parallel))
	}
	probes := []probe{
		{CompatDropNullReasoningContent, "openai/gpt-oss-120b", "round 1 #0 (relay v0)",
			codexRequest("openai/gpt-oss-120b", withInput(codexOpening(), reasoningItem("rs_1"), call("call_1"), output("call_1")), nil),
			"data did not match any variant of untagged enum ResponseInput"},
		{CompatMessageBeforeCalls, nemotron, "probe-nemotron.log asis",
			codexRequest(nemotron, withInput(codexOpening(), cleanReasoning("rs_1"), call("call_01a198a2045777d2bdbef57f"), said("msg_5", "Reading the plan."), output("call_01a198a2045777d2bdbef57f")), nil),
			"incomplete parallel tool-call group: tool call 'call_01a198a2045777d2bdbef57f' has no tool response"},
		{CompatMessageBeforeCalls, nemotron, "probe-nemotron.log noparallel",
			codexRequest(nemotron, withInput(codexOpening(), cleanReasoning("rs_1"), call("call_1"), said("msg_5", "Reading the plan."), output("call_1")),
				func(r map[string]any) { r["parallel_tool_calls"] = false }),
			"incomplete parallel tool-call group"},
		{CompatMessageBeforeCalls, muse, "probe-muse.log asis",
			codexRequest(muse, withInput(codexOpening(), cleanReasoning("rs_1"), call("call_1"), output("call_1"), call("call_2"), output("call_2"),
				cleanReasoning("rs_3"), call("call_01a198a62416755bb76dfe3d"), said("msg_6", "Now the tests."), output("call_01a198a62416755bb76dfe3d")), nil),
			"tool call 'call_01a198a62416755bb76dfe3d' has no tool response"},
		{CompatDropNamespaceTools, "Prism-ML/Ternary-Bonsai-27B", "probe-bonsai.log asis",
			codexRequest("Prism-ML/Ternary-Bonsai-27B", codexOpening(), nil),
			"tools[4].type: unknown variant `namespace`"},
		{CompatDropMediumEffort, "deepseek-ai/DeepSeek-V4.1-Flash", "probe-deepseek2.log effort_empty",
			codexRequest("deepseek-ai/DeepSeek-V4.1-Flash", codexOpening(), nil),
			`reasoning_effort must be low, high, xhigh, max, or an integer within [1, 100]; got "medium"`},
		{CompatDropMediumEffort, "deepseek-ai/DeepSeek-V4.1-Flash", "probe-deepseek.log effort_medium",
			codexRequest("deepseek-ai/DeepSeek-V4.1-Flash", codexOpening(), func(r map[string]any) { r["reasoning"] = map[string]any{"effort": "medium"} }),
			`got "medium"`},
	}
	for _, p := range probes {
		if refusal := togetherRefusal(p.request); !strings.Contains(refusal, p.refused) {
			t.Fatalf("%s: as Codex sent it, Together's stand-in answered %q, the probe %q", p.log, refusal, p.refused)
		}
		fixed, applied, err := Rewrite(p.request, []string{p.fix})
		if err != nil || applied[p.fix] == 0 {
			t.Fatalf("%s: %s changed nothing (%v, %v)", p.log, p.fix, applied, err)
		}
		if refusal := togetherRefusal(fixed); refusal != "" {
			t.Errorf("%s: with %s, Together's stand-in still answered %q", p.log, p.fix, refusal)
		}
	}

	// probe-nemotron2.log: the reorder alone is still refused (400), the
	// reorder and each output after its call is taken (200).
	request := codexRequest(nemotron, parallel, nil)
	reordered, _, _ := Rewrite(request, []string{CompatMessageBeforeCalls})
	if refusal := togetherRefusal(reordered); !strings.Contains(refusal, "tool call 'call_a' has no tool response") {
		t.Fatalf("reorder alone: %q", refusal)
	}
	both, applied, _ := Rewrite(request, []string{CompatMessageBeforeCalls, CompatOutputAfterCall})
	if refusal := togetherRefusal(both); refusal != "" || applied.String() != "message_before_calls:1,output_after_call:2" {
		t.Fatalf("reorder and output after call: %q, %s", refusal, applied)
	}
	var got struct {
		Input []map[string]any `json:"input"`
	}
	json.Unmarshal(both, &got)
	order := []string{}
	for _, entry := range got.Input[len(got.Input)-5:] {
		order = append(order, entry["type"].(string)+":"+firstOf(entry["call_id"], entry["id"]))
	}
	if strings.Join(order, " ") != "message:msg_9 function_call:call_a function_call_output:call_a function_call:call_b function_call_output:call_b" {
		t.Fatalf("the last turn went as %v", order)
	}
	if len(got.Input) != 23 {
		t.Fatal("a fix added or removed an item")
	}
}

func firstOf(values ...any) string {
	for _, value := range values {
		if text, ok := value.(string); ok && text != "" {
			return text
		}
	}
	return ""
}

// TestAFixChangesOnlyWhatItsNameSays: without a fix (or with fixes that find
// nothing to do) the request goes on byte for byte; with one, every other key,
// its order and every number stay exactly as Codex sent them.
func TestAFixChangesOnlyWhatItsNameSays(t *testing.T) {
	body := []byte(`{"model":"m","max_output_tokens":12345678901234567890,"temperature":1e-7,"input":[` +
		`{"type":"reasoning","id":"rs_1","summary":[],"content":null,"encrypted_content":"x"},` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"a \u00e9 \"quoted\""}]}],` +
		`"reasoning":{"effort":"medium","summary":"auto"},"tools":[{"type":"namespace","name":"multi_agent_v1"},{"type":"function","name":"exec_command"}],"stream":true}`)
	for _, compat := range [][]string{nil, {CompatMessageBeforeCalls, CompatOutputAfterCall}} {
		same, applied, err := Rewrite(body, compat)
		if err != nil || !bytes.Equal(same, body) || applied.String() != "none" {
			t.Fatalf("%v changed the request: %s, %v", compat, same, err)
		}
	}
	cases := map[string]string{
		CompatDropNullReasoningContent: `{"model":"m","max_output_tokens":12345678901234567890,"temperature":1e-7,"input":[` +
			`{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"x"},` +
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"a \u00e9 \"quoted\""}]}],` +
			`"reasoning":{"effort":"medium","summary":"auto"},"tools":[{"type":"namespace","name":"multi_agent_v1"},{"type":"function","name":"exec_command"}],"stream":true}`,
		CompatDropMediumEffort: `{"model":"m","max_output_tokens":12345678901234567890,"temperature":1e-7,"input":[` +
			`{"type":"reasoning","id":"rs_1","summary":[],"content":null,"encrypted_content":"x"},` +
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"a \u00e9 \"quoted\""}]}],` +
			`"reasoning":{"summary":"auto"},"tools":[{"type":"namespace","name":"multi_agent_v1"},{"type":"function","name":"exec_command"}],"stream":true}`,
		CompatDropNamespaceTools: `{"model":"m","max_output_tokens":12345678901234567890,"temperature":1e-7,"input":[` +
			`{"type":"reasoning","id":"rs_1","summary":[],"content":null,"encrypted_content":"x"},` +
			`{"type":"message","role":"user","content":[{"type":"input_text","text":"a \u00e9 \"quoted\""}]}],` +
			`"reasoning":{"effort":"medium","summary":"auto"},"tools":[{"type":"function","name":"exec_command"}],"stream":true}`,
	}
	for fix, want := range cases {
		got, applied, err := Rewrite(body, []string{fix})
		if err != nil || string(got) != want || applied[fix] != 1 {
			t.Errorf("%s:\n got %s\nwant %s (%v, %v)", fix, got, want, applied, err)
		}
	}
	if _, _, err := Rewrite([]byte(`{"model":"m","model":"n"}`), nil); err == nil {
		t.Error("a repeated key was taken")
	}
	if _, _, err := Rewrite([]byte(`[1]`), nil); err == nil {
		t.Error("a request that is not an object was taken")
	}
}

func TestTheEffortFixLeavesOutOnlyAnEmptyOrMediumLevel(t *testing.T) {
	for reasoning, want := range map[string]string{
		`{}`:                                `{"model":"m"}`,
		`{"effort":"medium"}`:               `{"model":"m"}`,
		`{"effort":"medium","summary":"x"}`: `{"model":"m","reasoning":{"summary":"x"}}`,
		`{"effort":"high"}`:                 `{"model":"m","reasoning":{"effort":"high"}}`,
		`null`:                              `{"model":"m","reasoning":null}`,
	} {
		got, _, err := Rewrite([]byte(`{"model":"m","reasoning":`+reasoning+`}`), []string{CompatDropMediumEffort})
		if err != nil || string(got) != want {
			t.Errorf("reasoning %s became %s, want %s", reasoning, got, want)
		}
	}
}

func TestTheTurnFixesMoveNothingTheyCannotPlaceExactly(t *testing.T) {
	ids := func(items []item) []string {
		out := []string{}
		for _, entry := range items {
			out = append(out, entry.kind+":"+entry.callID)
		}
		return out
	}
	read := func(entries ...any) []item {
		out := []item{}
		for _, entry := range entries {
			data, _ := json.Marshal(entry)
			out = append(out, readItem(data))
		}
		return out
	}
	// An output before its call, or one whose call is not there: left as it is.
	for _, items := range [][]item{
		read(output("a"), call("a")),
		read(output("x"), call("a"), said("m", "x")),
	} {
		if got, moved := outputAfterCall(items); moved != 0 || !reflect.DeepEqual(ids(got), ids(items)) {
			t.Errorf("%v became %v", ids(items), ids(got))
		}
	}
	// A turn whose message comes before its calls is not touched; Codex's
	// freeform apply_patch calls count as calls.
	patch := map[string]any{"type": "custom_tool_call", "call_id": "p", "name": "apply_patch", "input": "*** Begin Patch\n*** End Patch\n"}
	patchOutput := map[string]any{"type": "custom_tool_call_output", "call_id": "p", "output": "Success."}
	items := read(said("m1", "first"), call("a"), output("a"))
	if got, moved := messageBeforeCalls(items); moved != 0 || !reflect.DeepEqual(ids(got), ids(items)) {
		t.Errorf("a turn already in order became %v", ids(got))
	}
	got, moved := messageBeforeCalls(read(cleanReasoning("r"), patch, call("a"), said("m1", "x"), patchOutput, output("a")))
	if moved != 1 || strings.Join(ids(got), " ") != "reasoning: assistant_message: call:p call:a call_output:p call_output:a" {
		t.Errorf("an apply_patch turn became %v", ids(got))
	}
	got, moved = outputAfterCall(got)
	if moved != 2 || strings.Join(ids(got), " ") != "reasoning: assistant_message: call:p call_output:p call:a call_output:a" {
		t.Errorf("an apply_patch turn's outputs became %v (%d)", ids(got), moved)
	}
}

func TestACompatListNamesKnownFixesOnlyOnTheRelay(t *testing.T) {
	relayed := func(edit func(*Provider)) error {
		base := groq()
		provider := base.Clone()
		provider.Relay = true
		edit(provider)
		return provider.Validate()
	}
	if err := relayed(func(p *Provider) {
		p.Models[0].Compat = []string{CompatDropNullReasoningContent, CompatMessageBeforeCalls, CompatOutputAfterCall, CompatDropMediumEffort, CompatDropNamespaceTools}
		p.Models[1].Compat = []string{CompatChatBridge}
		p.Models[0].CodexCatalog = &CodexCatalog{ApplyPatch: true, ToolOutputTokens: 20000}
		p.Models[1].CodexCatalog = &CodexCatalog{}
	}); err != nil {
		t.Fatalf("a valid relay was refused: %v", err)
	}
	for name, edit := range map[string]func(*Provider){
		"an unknown fix":             func(p *Provider) { p.Models[0].Compat = []string{"reorder"} },
		"a repeated fix":             func(p *Provider) { p.Models[0].Compat = []string{CompatOutputAfterCall, CompatOutputAfterCall} },
		"compat without the relay":   func(p *Provider) { p.Relay = false; p.Models[0].Compat = []string{CompatOutputAfterCall} },
		"the bridge and a reorder":   func(p *Provider) { p.Models[1].Compat = []string{CompatChatBridge, CompatMessageBeforeCalls} },
		"the bridge and the outputs": func(p *Provider) { p.Models[1].Compat = []string{CompatOutputAfterCall, CompatChatBridge} },
		"apply_patch on the bridge": func(p *Provider) {
			p.Models[1].Compat, p.Models[1].CodexCatalog = []string{CompatChatBridge}, &CodexCatalog{ApplyPatch: true}
		},
		"a tiny tool output":           func(p *Provider) { p.Models[0].CodexCatalog = &CodexCatalog{ToolOutputTokens: 999} },
		"a tool output past a quarter": func(p *Provider) { p.Models[0].CodexCatalog = &CodexCatalog{ToolOutputTokens: 40000} },
		"the relay on chat":            func(p *Provider) { p.WireAPI = "chat" },
	} {
		if err := relayed(edit); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	unknown := relayed(func(p *Provider) { p.Models[0].Compat = []string{"reorder"} })
	if !strings.Contains(unknown.Error(), `unknown fix "reorder"`) || !strings.Contains(unknown.Error(), CompatChatBridge) {
		t.Errorf("the refusal of an unknown fix does not name the known ones: %v", unknown)
	}
}

// customPatchTool is Codex 0.163.0's freeform apply_patch as its request
// offers it (cap1 of the 2026-11-07 diagnosis), its grammar shortened.
func customPatchTool() map[string]any {
	return map[string]any{"type": "custom", "name": "apply_patch",
		"description": "The `apply_patch` tool can be used to edit files. This is a FREEFORM tool, so do not wrap the patch in JSON.",
		"format":      map[string]any{"type": "grammar", "syntax": "lark", "definition": "start: begin_patch hunk+ end_patch"}}
}

// TestTheCustomToolGoesToTheProviderAsAFunction: with custom_tool_as_function
// the custom apply_patch is offered as a function taking one string, input,
// in its place among the tools; Codex's earlier custom call and its output
// are replayed as a function call and its output, the arguments the input as
// {"input": …}; a tool_choice naming it names the function. Every other key
// and tool goes as it came, and the relay knows which names to turn back.
func TestTheCustomToolGoesToTheProviderAsAFunction(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: notes.txt\n+one\n*** End Patch\n"
	customCall := map[string]any{"type": "custom_tool_call", "id": "fc_p", "status": "completed", "call_id": "call_p", "name": "apply_patch", "input": patch}
	customOutput := map[string]any{"type": "custom_tool_call_output", "call_id": "call_p", "output": "Success. Updated the following files:\nA notes.txt\n"}
	request := codexRequest("openai/gpt-oss-120b", withInput(codexOpening(), cleanReasoning("rs_1"), customCall, customOutput), func(r map[string]any) {
		r["tools"] = append(codexTools()[:3:3], append([]any{customPatchTool()}, codexTools()[3:]...)...)
		r["tool_choice"] = map[string]any{"type": "custom", "name": "apply_patch"}
	})
	fixed, applied, custom, err := rewrite(request, []string{CompatCustomToolAsFunction})
	if err != nil || applied.String() != "custom_tool_as_function:4" || strings.Join(custom, ",") != "apply_patch" {
		t.Fatalf("applied %s, custom %v, %v", applied, custom, err)
	}
	var got struct {
		Tools      []map[string]any `json:"tools"`
		Input      []map[string]any `json:"input"`
		ToolChoice map[string]any   `json:"tool_choice"`
	}
	if err := json.Unmarshal(fixed, &got); err != nil {
		t.Fatal(err)
	}
	tool := got.Tools[3]
	parameters, _ := json.Marshal(tool["parameters"])
	if len(got.Tools) != 9 || tool["type"] != "function" || tool["name"] != "apply_patch" || tool["strict"] != false ||
		tool["description"] != applyPatchDescription || tool["format"] != nil ||
		string(parameters) != `{"additionalProperties":false,"properties":{"input":{"description":"The entire patch in the apply_patch format.","type":"string"}},"required":["input"],"type":"object"}` {
		t.Errorf("apply_patch went to the provider as %v", tool)
	}
	if got.Tools[2]["name"] != "request_user_input" || got.Tools[4]["name"] != "view_image" || got.Tools[5]["type"] != "namespace" {
		t.Errorf("the other tools moved: %v", got.Tools)
	}
	replayed, _ := json.Marshal(got.Input[len(got.Input)-2:])
	if string(replayed) != `[{"arguments":"{\"input\":\"*** Begin Patch\\n*** Add File: notes.txt\\n+one\\n*** End Patch\\n\"}","call_id":"call_p","name":"apply_patch","type":"function_call"},`+
		`{"call_id":"call_p","output":"Success. Updated the following files:\nA notes.txt\n","type":"function_call_output"}]` {
		t.Errorf("the custom call was replayed as %s", replayed)
	}
	if got.ToolChoice["type"] != "function" || got.ToolChoice["name"] != "apply_patch" {
		t.Errorf("tool_choice = %v", got.ToolChoice)
	}
	if refusal := togetherRefusal(fixed); refusal != "" {
		t.Errorf("Together's stand-in refuses the fixed request: %s", refusal)
	}
	// With the turn fixes too, the replayed call still counts as a call.
	_, applied, _, _ = rewrite(request, []string{CompatDropNullReasoningContent, CompatMessageBeforeCalls, CompatOutputAfterCall, CompatCustomToolAsFunction})
	if applied.String() != "custom_tool_as_function:4" {
		t.Errorf("with the turn fixes: %s", applied)
	}

	// A custom tool whose name a function tool has too stays as it is; a
	// request without a custom tool or item goes byte for byte.
	clash := codexRequest("openai/gpt-oss-120b", codexOpening(), func(r map[string]any) {
		r["tools"] = []any{customPatchTool(), map[string]any{"type": "function", "name": "apply_patch", "parameters": map[string]any{}}}
	})
	if same, applied, custom, _ := rewrite(clash, []string{CompatCustomToolAsFunction}); !bytes.Equal(same, clash) || len(applied) != 0 || custom != nil {
		t.Errorf("a clashing custom tool was changed: %s %v", applied, custom)
	}
	plain := codexRequest("openai/gpt-oss-120b", withInput(codexOpening(), call("call_1"), output("call_1")), nil)
	if same, applied, custom, _ := rewrite(plain, []string{CompatCustomToolAsFunction, CompatStopAtFirstCall}); !bytes.Equal(same, plain) || len(applied) != 0 || custom != nil {
		t.Errorf("a request without a custom tool changed: %s %v", applied, custom)
	}
}

// TestModelsWithoutTheNewFixesKeepTheirCustomTool: every other model's
// fixes leave the custom tool and Codex's custom items exactly as they came.
func TestModelsWithoutTheNewFixesKeepTheirCustomTool(t *testing.T) {
	customCall := map[string]any{"type": "custom_tool_call", "call_id": "call_p", "name": "apply_patch", "input": "*** Begin Patch\n*** End Patch\n"}
	customOutput := map[string]any{"type": "custom_tool_call_output", "call_id": "call_p", "output": "Success."}
	request := codexRequest("openai/gpt-oss-120b", withInput(codexOpening(), customCall, customOutput), func(r map[string]any) {
		r["tools"] = append(codexTools(), customPatchTool())
	})
	for _, compat := range [][]string{nil, {CompatDropNullReasoningContent, CompatMessageBeforeCalls, CompatOutputAfterCall}, {CompatStopAtFirstCall}} {
		same, applied, custom, err := rewrite(request, compat)
		if err != nil || !bytes.Equal(same, request) || applied.String() != "none" || custom != nil {
			t.Errorf("%v changed the request: %s %v %v", compat, applied, custom, err)
		}
	}
}

func TestTheStreamFixesGoWithTheRelayButNotTheBridge(t *testing.T) {
	relayed := func(compat ...string) error {
		base := groq()
		provider := base.Clone()
		provider.Relay = true
		provider.Models[0].Compat = compat
		return provider.Validate()
	}
	if err := relayed(CompatDropNullReasoningContent, CompatMessageBeforeCalls, CompatOutputAfterCall, CompatStopAtFirstCall, CompatCustomToolAsFunction); err != nil {
		t.Errorf("gpt-oss-120b's list was refused: %v", err)
	}
	if err := relayed(CompatCustomToolAsFunction); err != nil {
		t.Errorf("custom_tool_as_function alone was refused: %v", err)
	}
	for _, compat := range [][]string{{CompatChatBridge, CompatStopAtFirstCall}, {CompatCustomToolAsFunction, CompatChatBridge}, {CompatStopAtFirstCall, CompatStopAtFirstCall}} {
		if err := relayed(compat...); err == nil {
			t.Errorf("%v was accepted", compat)
		}
	}
}

// TestGroqsRefusalsTurnIntoAnAnswerWithItsFixes walks Codex 0.163.0's request
// for gpt-oss-120b through what Groq answered on 2026-11-07 (groqRefusal):
// as sent, the custom apply_patch and the namespace make the body invalid;
// with those offered as Groq takes them, client_metadata is unknown; without
// it, include is not supported; without that too, Groq answers. The replay of
// a later turn (Groq's reasoning item with its text, a call, its output) goes
// through with the same list, and nothing else in the request changes.
func TestGroqsRefusalsTurnIntoAnAnswerWithItsFixes(t *testing.T) {
	codex := func(input []any) []byte {
		return codexRequest("openai/gpt-oss-120b", input, func(r map[string]any) {
			r["tools"] = append(codexTools(), customPatchTool())
			r["reasoning"] = map[string]any{"effort": "high"}
			r["client_metadata"] = codexClientMetadata()
		})
	}
	request := codex(codexOpening())
	steps := []struct {
		compat  []string
		refused string
	}{
		{nil, "invalid JSON body"},
		{[]string{CompatDropNamespaceTools}, "invalid JSON body"},
		{[]string{CompatDropNamespaceTools, CompatCustomToolAsFunction}, "unknown field `client_metadata` in request body"},
		{[]string{CompatDropNamespaceTools, CompatCustomToolAsFunction, CompatDropClientMetadata}, "Field 'include' is not supported"},
		{groqCompat, ""},
	}
	for _, step := range steps {
		fixed, _, err := Rewrite(request, step.compat)
		if err != nil || groqRefusal(fixed) != step.refused {
			t.Fatalf("with %v Groq's stand-in answered %q, Groq %q (%v)", step.compat, groqRefusal(fixed), step.refused, err)
		}
	}
	fixed, applied, err := Rewrite(request, groqCompat)
	if err != nil || applied.String() != "drop_namespace_tools:1,drop_client_metadata:1,drop_include:1,custom_tool_as_function:1" {
		t.Fatalf("applied %s, %v", applied, err)
	}
	var got map[string]json.RawMessage
	json.Unmarshal(fixed, &got)
	for _, key := range []string{"model", "input", "tool_choice", "parallel_tool_calls", "reasoning", "store", "stream", "prompt_cache_key"} {
		var before map[string]json.RawMessage
		json.Unmarshal(request, &before)
		if !bytes.Equal(got[key], before[key]) {
			t.Errorf("%s changed: %s, was %s", key, got[key], before[key])
		}
	}
	reasoning := map[string]any{"type": "reasoning", "id": "resp_01m6t3a7raef1v2cacgz92sf8h", "summary": []any{},
		"content": []any{map[string]any{"type": "reasoning_text", "text": "We need to call function."}}, "encrypted_content": nil}
	replay := codex(withInput(codexOpening(), reasoning, call("fc_1"), output("fc_1")))
	if again, applied, err := Rewrite(replay, groqCompat); err != nil || groqRefusal(again) != "" || applied[CompatDropNullReasoningContent] != 0 ||
		!bytes.Contains(again, []byte(`"content":[{"text":"We need to call function.","type":"reasoning_text"}]`)) {
		t.Errorf("Groq's stand-in refused the replay (%q) or its reasoning changed: %s, %v", groqRefusal(again), applied, err)
	}
}

// TestTheFieldFixesLeaveOutTheirKeyAndNothingElse: drop_client_metadata and
// drop_include each leave out one top-level key; the other keys, their order
// and every number go on byte for byte; a request without the key is sent
// exactly as it came.
func TestTheFieldFixesLeaveOutTheirKeyAndNothingElse(t *testing.T) {
	body := []byte(`{"model":"m","include":["reasoning.encrypted_content"],"max_output_tokens":12345678901234567890,` +
		`"client_metadata":{"turn_id":"t","x-codex-turn-metadata":"{\"a\":1e-7}"},"input":[{"type":"message","role":"user","content":"a \u00e9"}],"stream":true}`)
	for fix, want := range map[string]string{
		CompatDropClientMetadata: `{"model":"m","include":["reasoning.encrypted_content"],"max_output_tokens":12345678901234567890,` +
			`"input":[{"type":"message","role":"user","content":"a \u00e9"}],"stream":true}`,
		CompatDropInclude: `{"model":"m","max_output_tokens":12345678901234567890,` +
			`"client_metadata":{"turn_id":"t","x-codex-turn-metadata":"{\"a\":1e-7}"},"input":[{"type":"message","role":"user","content":"a \u00e9"}],"stream":true}`,
	} {
		got, applied, err := Rewrite(body, []string{fix})
		if err != nil || string(got) != want || applied.String() != fix+":1" {
			t.Errorf("%s:\n got %s\nwant %s (%s, %v)", fix, got, want, applied, err)
		}
	}
	both, applied, _ := Rewrite(body, []string{CompatDropClientMetadata, CompatDropInclude})
	if string(both) != `{"model":"m","max_output_tokens":12345678901234567890,"input":[{"type":"message","role":"user","content":"a \u00e9"}],"stream":true}` ||
		applied.String() != "drop_client_metadata:1,drop_include:1" {
		t.Errorf("both: %s (%s)", both, applied)
	}
	plain := []byte(`{"model":"m","stream":true,"n":1e-7}`)
	if same, applied, err := Rewrite(plain, []string{CompatDropClientMetadata, CompatDropInclude}); err != nil || !bytes.Equal(same, plain) || applied.String() != "none" {
		t.Errorf("a request without the keys changed: %s %s %v", same, applied, err)
	}
	relayed := groq()
	provider := relayed.Clone()
	provider.Relay, provider.Models[0].Compat = true, append([]string{}, groqCompat...)
	provider.Models[0].CodexCatalog = &CodexCatalog{ApplyPatch: true}
	if err := provider.Validate(); err != nil {
		t.Errorf("gpt-oss-120b's list on Groq was refused: %v", err)
	}
}

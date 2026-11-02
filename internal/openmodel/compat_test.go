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

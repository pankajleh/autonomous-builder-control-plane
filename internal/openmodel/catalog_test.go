package openmodel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestTheCatalogEntryHoldsWhatCodexRequires: Codex 0.163.0 refuses an entry
// without these keys (it ignores keys it does not know); apply_patch is
// offered only where the manifest says so; the base prompt is Codex's own.
func TestTheCatalogEntryHoldsWhatCodexRequires(t *testing.T) {
	if sum := sha256.Sum256([]byte(codexBaseInstructions)); hex.EncodeToString(sum[:]) != CodexBaseInstructionsSHA256 ||
		!strings.HasPrefix(codexBaseInstructions, "You are a coding agent running in the Codex CLI") || len(codexBaseInstructions) != 17119 {
		t.Fatal("codex_base_instructions.md is not the prompt Codex 0.163.0 sends on fallback metadata")
	}
	provider := groq()
	if CodexCatalogJSON(provider, provider.Models[0]) != nil {
		t.Fatal("a model without codex_catalog got a catalog")
	}
	provider.Models[0].CodexCatalog = &CodexCatalog{ApplyPatch: true}
	provider.Models[1].CodexCatalog = &CodexCatalog{ToolOutputTokens: 20000}
	read := func(m Model) map[string]any {
		data := CodexCatalogJSON(provider, m)
		var catalog struct {
			Models []map[string]any `json:"models"`
		}
		if err := json.Unmarshal(data, &catalog); err != nil || len(catalog.Models) != 1 || !strings.HasSuffix(string(data), "}\n") {
			t.Fatalf("catalog %s: %v", data, err)
		}
		return catalog.Models[0]
	}
	gptOSS := read(provider.Models[0])
	for _, key := range []string{"slug", "display_name", "supported_reasoning_levels", "shell_type", "visibility", "supported_in_api", "priority",
		"support_verbosity", "truncation_policy", "experimental_supported_tools", "base_instructions"} {
		if _, ok := gptOSS[key]; !ok {
			t.Errorf("the entry lacks %s, which Codex requires", key)
		}
	}
	want := map[string]any{
		"slug": "openai/gpt-oss-120b", "display_name": "openai/gpt-oss-120b", "description": "groq/gpt-oss-120b (ABCP open-model engine)",
		"default_reasoning_level": "high", "shell_type": "default", "visibility": "list", "supported_in_api": true, "priority": float64(0),
		"supports_reasoning_summaries": false, "default_reasoning_summary": "none", "support_verbosity": false,
		"apply_patch_tool_type": "freeform", "truncation_policy": map[string]any{"mode": "tokens", "limit": float64(10000)},
		"supports_search_tool": false, "context_window": float64(131072), "max_context_window": float64(131072),
		"auto_compact_token_limit": float64(104857), "experimental_supported_tools": []any{},
		"supported_reasoning_levels": []any{
			map[string]any{"effort": "low", "description": "low reasoning"}, map[string]any{"effort": "medium", "description": "medium reasoning"},
			map[string]any{"effort": "high", "description": "high reasoning"}},
		"base_instructions": codexBaseInstructions,
	}
	if !reflect.DeepEqual(gptOSS, want) {
		for key := range want {
			if !reflect.DeepEqual(gptOSS[key], want[key]) {
				t.Errorf("%s = %v, want %v", key, gptOSS[key], want[key])
			}
		}
		for key := range gptOSS {
			if _, ok := want[key]; !ok {
				t.Errorf("unexpected key %s", key)
			}
		}
	}
	devstral := read(provider.Models[1])
	if _, offered := devstral["apply_patch_tool_type"]; offered || devstral["default_reasoning_level"] != nil ||
		len(devstral["supported_reasoning_levels"].([]any)) != 0 || devstral["truncation_policy"].(map[string]any)["limit"] != float64(20000) {
		t.Errorf("a model without apply_patch or levels: %v", devstral)
	}
}

package openmodel

import (
	_ "embed"
	"encoding/json"
)

// Codex's model catalog (model_catalog_json). Codex 0.163.0 has no metadata
// for a provider's model ids, so it runs them on fallback metadata ("Model
// metadata … not found. Defaulting to fallback metadata"): a base prompt that
// tells the model to edit files with an apply_patch tool Codex then does not
// offer (round 1's gpt-oss-120b wrote no file in 38 minutes, every edit
// answered "unsupported call: apply_patch"), tool output cut at 10,000 bytes,
// and the spawn_agent tool listing OpenAI's models as the ones a sub-agent may
// pick.
//
// Codex reads model_catalog_json, a top-level config.toml key, as the path of
// a JSON file {"models": [entry, …]} that takes the place of its bundled
// catalog. Codex 0.163.0 refuses an entry without supported_reasoning_levels,
// shell_type, visibility, supported_in_api, priority, support_verbosity,
// truncation_policy and experimental_supported_tools, or without
// base_instructions (or model_messages.instructions_template); it ignores
// keys it does not know. apply_patch_tool_type takes only "freeform";
// truncation_policy's mode is "bytes" or "tokens".
//
// The entry ABCP writes keeps every request Codex sends as it was on fallback
// metadata (checked against Codex 0.163.0 with Ralphex's own flags: the same
// prompt, tools, reasoning, include and parallel calls) apart from three
// things: apply_patch is offered where the manifest says so, tool output is
// cut at ToolOutputTokens tokens (10,000 tokens is about 40,000 bytes, Codex's
// own models' limit), and spawn_agent lists the build's model alone.
// supports_search_tool stays false: true would add a tool_search tool, which
// providers refuse, and fold the multi-agent tools behind it.

// codexBaseInstructions is the base prompt Codex 0.163.0 sends a model it has
// no metadata for, byte for byte as it sends it (captured from its request;
// Codex is openai/codex, Apache License 2.0). The catalog entry must give one,
// and giving Codex its own keeps the prompt unchanged.
//
//go:embed codex_base_instructions.md
var codexBaseInstructions string

// CodexBaseInstructionsSHA256 is the SHA-256 of codexBaseInstructions, so an
// edit to the file shows in the tests.
const CodexBaseInstructionsSHA256 = "3b08633fa672906666659d764864dfda1d7af5b5111ea5817c8f46e5de4e1a8d"

// Tool output limits a catalog entry may set, in tokens.
const (
	DefaultToolOutputTokens = 10000
	MinToolOutputTokens     = 1000
	MaxToolOutputTokens     = 100000
)

// CatalogFile is the name of the catalog file in a model's Codex home.
const CatalogFile = "model-catalog.json"

// CodexCatalogJSON answers the model's catalog file, or nil when its manifest
// entry gives no codex_catalog.
func CodexCatalogJSON(p Provider, m Model) []byte {
	if m.CodexCatalog == nil {
		return nil
	}
	tokens := m.CodexCatalog.toolOutputTokens()
	levels := make([]map[string]string, 0, len(m.Efforts))
	for _, effort := range m.Efforts {
		levels = append(levels, map[string]string{"effort": effort, "description": effort + " reasoning"})
	}
	var defaultLevel any
	if m.DefaultEffort != "" {
		defaultLevel = m.DefaultEffort
	}
	entry := object{
		{key: "slug", value: mustJSON(m.ID)},
		{key: "display_name", value: mustJSON(m.ID)},
		{key: "description", value: mustJSON(p.ID + "/" + m.Name + " (ABCP open-model engine)")},
		{key: "default_reasoning_level", value: mustJSON(defaultLevel)},
		{key: "supported_reasoning_levels", value: mustJSON(levels)},
		{key: "shell_type", value: mustJSON("default")},
		{key: "visibility", value: mustJSON("list")},
		{key: "supported_in_api", value: mustJSON(true)},
		{key: "priority", value: mustJSON(0)},
		{key: "base_instructions", value: mustJSON(codexBaseInstructions)},
		{key: "supports_reasoning_summaries", value: mustJSON(false)},
		{key: "default_reasoning_summary", value: mustJSON("none")},
		{key: "support_verbosity", value: mustJSON(false)},
	}
	if m.CodexCatalog.ApplyPatch {
		entry = append(entry, member{key: "apply_patch_tool_type", value: mustJSON("freeform")})
	}
	entry = append(entry,
		member{key: "truncation_policy", value: mustJSON(map[string]any{"mode": "tokens", "limit": tokens})},
		member{key: "supports_search_tool", value: mustJSON(false)},
		member{key: "context_window", value: mustJSON(m.ContextWindow)},
		member{key: "max_context_window", value: mustJSON(m.ContextWindow)},
		member{key: "auto_compact_token_limit", value: mustJSON(m.CompactAt())},
		member{key: "experimental_supported_tools", value: mustJSON([]string{})},
	)
	catalog := object{{key: "models", value: mustJSON([]json.RawMessage{entry.encode()})}}
	return append(catalog.encode(), '\n')
}

func (c *CodexCatalog) toolOutputTokens() int {
	if c.ToolOutputTokens == 0 {
		return DefaultToolOutputTokens
	}
	return c.ToolOutputTokens
}

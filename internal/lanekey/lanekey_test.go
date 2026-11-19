package lanekey

import (
	"errors"
	"strings"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

func TestAKeyHasItsProvidersShapeOrIsRefusedWithoutBeingShown(t *testing.T) {
	for _, test := range []struct {
		provider, key string
		ok            bool
	}{
		{"together", strings.Repeat("0123456789abcdef", 4), true},
		{"together", "tgp_v1_" + strings.Repeat("Ab3-_", 8) + "xyz", true},
		{"together", strings.Repeat("a", 32), true},
		{"together", strings.Repeat("a", 128), true},
		{"together", strings.Repeat("a", 31), false},
		{"together", strings.Repeat("a", 129), false},
		{"together", "tgp_v1_" + strings.Repeat("a", 30) + " b", false},
		{"together", "tgp_v1_" + strings.Repeat("a", 30) + "\n", false},
		{"together", "TOGETHER_API_KEY=" + strings.Repeat("a", 40), false},
		{"together", "tgp_v1_" + strings.Repeat("a", 30) + "é", false},
		{"together", "tgp_v1_" + strings.Repeat("a", 30) + `"`, false},
		{"together", "", false},
		{"groq", "gsk_" + strings.Repeat("Ab9", 17) + "x", true},
		{"groq", "gsk_" + strings.Repeat("a", 35), false},
		{"groq", "gsk_" + strings.Repeat("a", 30) + "+/", false},
		// The engine lanes' vendors: one .env value of 20 to 512 letters, digits, '_' or '-'; never an Anthropic setup token.
		{"anthropic", "sk-ant-api03-" + strings.Repeat("Ab9_-", 19) + "AA", true},
		{"anthropic", strings.Repeat("a", 20), true},
		{"anthropic", strings.Repeat("a", 512), true},
		{"anthropic", strings.Repeat("a", 19), false},
		{"anthropic", strings.Repeat("a", 513), false},
		{"anthropic", "sk-ant-oat01-" + strings.Repeat("a", 40), false},
		{"anthropic", "sk-ant-api03-" + strings.Repeat("a", 20) + "=x", false},
		{"anthropic", "sk-ant-api03-" + strings.Repeat("a", 20) + "\nCLAUDE_CODE_USE_BEDROCK=1", false},
		{"openai", "sk-proj-" + strings.Repeat("Ab9_-", 31) + "A", true},
		{"openai", "sk-proj-" + strings.Repeat("a", 20) + " ", false},
		{"openai", "sk-proj-" + strings.Repeat("a", 20) + "#x", false},
		// Another provider's key has the builds' own rule: 16 to 512 printable characters without spaces, never NAME=value.
		{"other", "sk-" + strings.Repeat("a", 13), true},
		{"other", "key+/=" + strings.Repeat("a", 20), true},
		{"other", strings.Repeat("a", 512), true},
		{"other", strings.Repeat("a", 15), false},
		{"other", strings.Repeat("a", 513), false},
		{"other", "OTHER_API_KEY=" + strings.Repeat("a", 20), false},
		{"other", strings.Repeat("a", 20) + "\t", false},
	} {
		err := CheckShape(test.provider, test.key)
		if (err == nil) != test.ok {
			t.Errorf("%s key %.20q… = %v", test.provider, test.key, err)
		}
		if err != nil && (!errors.Is(err, serviceapi.ErrLaneKeyInvalid) || (len(test.key) > 3 && strings.Contains(err.Error(), test.key))) {
			t.Errorf("%s key %.20q…: the error is %q", test.provider, test.key, err)
		}
	}
}

func TestAFingerprintIsTheFirstEightHexOfTheKeysSHA256(t *testing.T) {
	// sha256("abc") = ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad
	if got := Fingerprint("abc"); got != "ba7816bf" {
		t.Fatalf("fingerprint = %q", got)
	}
}

func TestAnEngineLanesKeyLineIsTheOnlyLineThatChanges(t *testing.T) {
	file := "# a comment naming ANTHROPIC_API_KEY=not-this-one\r\nCLAUDE_CONFIG_DIR=/home\r\nANTHROPIC_API_KEY=old\r\nANTHROPIC_MODEL=m\r\n"
	if got := string(withSetting([]byte(file), "ANTHROPIC_API_KEY", "new")); got != strings.Replace(file, "=old", "=new", 1) {
		t.Fatalf("replaced = %q", got)
	}
	if got := string(withoutSetting([]byte(file), "ANTHROPIC_API_KEY")); got != strings.Replace(file, "ANTHROPIC_API_KEY=old\r\n", "", 1) {
		t.Fatalf("removed = %q", got)
	}
	for data, want := range map[string]string{
		"":                      "OPENAI_API_KEY=new\n",
		"CODEX_HOME=/home":      "CODEX_HOME=/home\nOPENAI_API_KEY=new\n",
		"CODEX_HOME=/home\n":    "CODEX_HOME=/home\nOPENAI_API_KEY=new\n",
		"OPENAI_API_KEY=old":    "OPENAI_API_KEY=new",
		"OPENAI_API_KEY_2=x\n":  "OPENAI_API_KEY_2=x\nOPENAI_API_KEY=new\n",
		"#OPENAI_API_KEY=old\n": "#OPENAI_API_KEY=old\nOPENAI_API_KEY=new\n",
	} {
		if got := string(withSetting([]byte(data), "OPENAI_API_KEY", "new")); got != want {
			t.Errorf("%q with the key = %q", data, got)
		}
	}
	for executor, cases := range map[string]map[string]string{
		"claude": {"ANTHROPIC_API_KEY": "", "CLAUDE_CODE_USE_BEDROCK": "bedrock", "CLAUDE_CODE_OAUTH_TOKEN": "setup-token"},
		"codex":  {"OPENAI_API_KEY": "", "OPENAI_BASE_URL": "another-address"},
	} {
		for setting, want := range cases {
			if got := paidOtherwise(executor, map[string]string{setting: "https://example.com/v1"}); got != want {
				t.Errorf("%s with %s = %q", executor, setting, got)
			}
		}
	}
	if got := paidOtherwise("codex", map[string]string{"OPENAI_BASE_URL": "https://api.openai.com/v1"}); got != "" {
		t.Errorf("Codex at OpenAI's address = %q", got)
	}
}

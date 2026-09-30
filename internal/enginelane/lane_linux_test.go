//go:build linux

package enginelane

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const secret = "sk-lane-value-never-shown-9QZ"

func write(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex-api.env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestALaneGivesItsEnginesSettingsAndTheSHA256OfItsFile(t *testing.T) {
	content := "# Codex on the OpenAI API\n\nCODEX_HOME=/home/devagent/.codex-api\r\nOPENAI_API_KEY=" + secret + "\n"
	path := write(t, content, 0o600)
	lane, err := Load("codex-api", path, "codex")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	if lane.SHA256 != hex.EncodeToString(sum[:]) || lane.Name != "codex-api" {
		t.Fatalf("lane %s %s", lane.Name, lane.SHA256)
	}
	if !reflect.DeepEqual(lane.Keys(), []string{"CODEX_HOME", "OPENAI_API_KEY"}) || lane.Settings["CODEX_HOME"] != "/home/devagent/.codex-api" {
		t.Fatalf("settings %v", lane.Keys())
	}
	// The lane's settings replace the inherited ones of the same names; the rest is kept in its order.
	got := lane.Environment([]string{"HOME=/home/devagent", "OPENAI_API_KEY=subscription", "CODEX_HOME=/home/devagent/.codex", "ABCP_CONTEXT_CAPSULE_PATH=/c.json"})
	want := []string{"HOME=/home/devagent", "ABCP_CONTEXT_CAPSULE_PATH=/c.json", "CODEX_HOME=/home/devagent/.codex-api", "OPENAI_API_KEY=" + secret}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environment %v", got)
	}
	read, err := Load("codex-api", write(t, "OPENAI_API_KEY="+secret+"\n", 0o400), "codex")
	if err != nil || len(read.Settings) != 1 {
		t.Fatalf("a read-only file is private too: %v", err)
	}
}

func TestAClaudeLaneTakesBedrocksSettings(t *testing.T) {
	path := write(t, "CLAUDE_CONFIG_DIR=/home/devagent/.claude-bedrock\nCLAUDE_CODE_USE_BEDROCK=1\nAWS_REGION=ap-south-1\nAWS_ACCESS_KEY_ID=AKIAEXAMPLE\n"+
		"AWS_SECRET_ACCESS_KEY="+secret+"\nANTHROPIC_MODEL=apac.anthropic.example-model-v1:0\n", 0o600)
	lane, err := Load("claude-bedrock", path, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(lane.Settings) != 6 || lane.Settings["CLAUDE_CODE_USE_BEDROCK"] != "1" {
		t.Fatalf("settings %v", lane.Keys())
	}
	if _, err := Load("claude-bedrock", path, "codex"); err == nil || !strings.Contains(err.Error(), "CLAUDE_CONFIG_DIR is not an engine lane setting") {
		t.Fatalf("a Claude lane given to Codex: %v", err)
	}
}

func TestALaneFileThatIsNotTheOperatorsPrivateFileOrGivesAnythingElseIsRefused(t *testing.T) {
	good := "OPENAI_API_KEY=" + secret + "\n"
	for name, content := range map[string]string{
		"not KEY=VALUE":          "OPENAI_API_KEY " + secret + "\n",
		"lower-case key":         "openai_api_key=" + secret + "\n",
		"not the engine's":       "AWS_SECRET_ACCESS_KEY=" + secret + "\n",
		"inherited by any build": "HOME=/tmp\n",
		"given twice":            good + good,
		"empty value":            "OPENAI_API_KEY=\n",
		"nothing":                "# only a comment\n\n",
		"NUL":                    "OPENAI_API_KEY=" + secret + "\x00\n",
		"too large":              good + "# " + strings.Repeat("x", MaxBytes) + "\n",
	} {
		_, err := Load("codex-api", write(t, content, 0o600), "codex")
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: the error names the value: %v", name, err)
		}
	}
	if _, err := Load("codex-api", write(t, good, 0o640), "codex"); err == nil || !strings.Contains(err.Error(), "mode 0600") {
		t.Errorf("group-readable: %v", err)
	}
	if _, err := Load("codex-api", write(t, good, 0o604), "codex"); err == nil {
		t.Error("world-readable file accepted")
	}

	target := write(t, good, 0o600)
	link := filepath.Join(t.TempDir(), "link.env")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("codex-api", link, "codex"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("a linked file: %v", err)
	}
	folder := t.TempDir()
	if err := os.Symlink(filepath.Dir(target), filepath.Join(folder, "lanes")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("codex-api", filepath.Join(folder, "lanes", "codex-api.env"), "codex"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("a linked folder: %v", err)
	}
	if _, err := Load("codex-api", filepath.Dir(target), "codex"); err == nil {
		t.Error("a folder accepted")
	}
	if _, err := Load("codex-api", filepath.Join(t.TempDir(), "missing.env"), "codex"); err == nil {
		t.Error("a missing file accepted")
	}

	for _, bad := range [][3]string{
		{"Codex API", target, "codex"}, {"", target, "codex"}, {"-api", target, "codex"}, {strings.Repeat("a", 41), target, "codex"},
		{"codex-api", "codex-api.env", "codex"}, {"codex-api", target + "/../codex-api.env", "codex"}, {"codex-api", strings.TrimSuffix(target, ".env"), "codex"},
		{"codex-api", target, "gemini"},
	} {
		if _, err := Load(bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("accepted %q %q %q", bad[0], bad[1], bad[2])
		}
	}
}

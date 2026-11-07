//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaneKeysReadsTheProfilesLanesAndPrintsNoKey(t *testing.T) {
	root := t.TempDir()
	secrets := filepath.Join(root, "secrets")
	if err := os.Mkdir(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(path string, data []byte) string {
		t.Helper()
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	key := "tgp_v1_" + strings.Repeat("Q", 43)
	keyFile := write(filepath.Join(secrets, "together-api-key"), []byte(key+"\n"))
	provider := map[string]any{
		"id": "together", "base_url": "https://api.together.xyz/v1", "key_file": keyFile, "wire_api": "responses",
		"codex_home": filepath.Join(root, "codex"), "models": []map[string]any{{"name": "m", "id": "org/m", "origin": "US", "context_window": 131072}},
	}
	together, _ := json.Marshal(map[string]any{"executor": map[string]any{"executor": "codex", "task_model": "m", "provider": provider}})
	account, _ := json.Marshal(map[string]any{"executor": map[string]any{"executor": "codex", "lane": "codex-account", "lane_file": filepath.Join(root, "codex-account.env")}})
	profiles, _ := json.Marshal(map[string]any{"schema_version": 1, "profiles": []map[string]string{
		{"profile_id": "local-p02-together", "manifest_template_path": write(filepath.Join(root, "together.json"), together)},
		{"profile_id": "local-p02-codex-account", "manifest_template_path": write(filepath.Join(root, "account.json"), account)},
	}})
	profileFile := write(filepath.Join(root, "profiles.json"), profiles)
	var stdout, stderr bytes.Buffer
	if code := runCLI([]string{"lane-keys", "--admission-profile-file", profileFile}, &stdout, &stderr); code != 0 ||
		stdout.String() != "lane together kind=key state=set profiles=local-p02-together\nlane keys ready lanes=1 other-lanes=codex-account\n" {
		t.Fatalf("lane-keys = %d %q %q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), key) {
		t.Fatal("lane-keys printed the key")
	}
	if err := os.Chmod(profileFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runCLI([]string{"lane-keys", "--admission-profile-file", profileFile}, &stdout, &stderr); code != 1 {
		t.Fatalf("a profile file open to others = %d", code)
	}
	if code := runCLI([]string{"lane-keys"}, &stdout, &stderr); code != 2 {
		t.Fatalf("no profile file = %d", code)
	}
}

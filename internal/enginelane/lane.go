// Package enginelane reads an engine lane: one way of paying for a build
// engine (Repo C design note A6), such as Codex through the OpenAI API or
// Claude Code through Amazon Bedrock. A build profile's manifest names its lane
// and the lane's file; the file holds only the settings that engine accepts for
// it, and those settings replace the inherited ones for that build only.
//
// The file is the operator's: a regular file owned by the controller's user,
// readable by that user alone, never a symbolic link. It is read when the
// profile is loaded and again when each build starts, and each build records
// its SHA-256, never its values.
package enginelane

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// MaxBytes bounds a lane file.
const MaxBytes = 16 << 10

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
var keyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)

// allowed lists, per executor, the only settings a lane may give. Everything
// else in a lane file refuses it.
var allowed = map[string]map[string]bool{
	"codex": set("CODEX_HOME", "OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_ORG_ID", "OPENAI_PROJECT_ID"),
	// Claude Code on Anthropic's API (ANTHROPIC_API_KEY, the owner's choice for the Claude lane) or on Amazon Bedrock.
	"claude": set("CLAUDE_CONFIG_DIR", "ANTHROPIC_API_KEY", "CLAUDE_CODE_USE_BEDROCK", "AWS_REGION", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
		"AWS_SESSION_TOKEN", "ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL"),
}

func set(keys ...string) map[string]bool {
	out := make(map[string]bool, len(keys))
	for _, key := range keys {
		out[key] = true
	}
	return out
}

// Lane is a loaded lane: its settings and the SHA-256 of its file.
type Lane struct {
	Name     string
	Settings map[string]string
	SHA256   string
}

// Keys answers the lane's setting names, sorted, for evidence.
func (l Lane) Keys() []string {
	keys := make([]string, 0, len(l.Settings))
	for key := range l.Settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ValidName reports whether name is a lane name.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// ValidPath reports whether path can name a lane file: absolute, clean and
// ending in ".env".
func ValidPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && strings.HasSuffix(path, ".env") && len(path) <= 4096
}

// Load reads the lane file at path for executor.
func Load(name, path, executor string) (Lane, error) {
	if !ValidName(name) {
		return Lane{}, errors.New("engine lane name is invalid")
	}
	if !ValidPath(path) {
		return Lane{}, errors.New("engine lane file path must be absolute, clean and end in .env")
	}
	keys, ok := allowed[executor]
	if !ok {
		return Lane{}, fmt.Errorf("executor %q has no engine lanes", executor)
	}
	data, err := readPrivate(path)
	if err != nil {
		return Lane{}, fmt.Errorf("engine lane %s: %w", name, err)
	}
	settings, err := parse(data, keys)
	if err != nil {
		return Lane{}, fmt.Errorf("engine lane %s: %w", name, err)
	}
	sum := sha256.Sum256(data)
	return Lane{Name: name, Settings: settings, SHA256: hex.EncodeToString(sum[:])}, nil
}

// parse reads KEY=VALUE lines; blank lines and lines starting with # are
// skipped. A key must be on the executor's list and given once; a value is one
// line without NUL. Errors name the line, never its value.
func parse(data []byte, keys map[string]bool) (map[string]string, error) {
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, errors.New("file holds a NUL byte")
	}
	settings := map[string]string{}
	for index, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(raw, "\r")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found || !keyPattern.MatchString(key) {
			return nil, fmt.Errorf("line %d is not KEY=VALUE", index+1)
		}
		if !keys[key] {
			return nil, fmt.Errorf("line %d: %s is not an engine lane setting for this executor", index+1, key)
		}
		if _, seen := settings[key]; seen {
			return nil, fmt.Errorf("line %d: %s is given twice", index+1, key)
		}
		if value == "" {
			return nil, fmt.Errorf("line %d: %s is empty", index+1, key)
		}
		settings[key] = value
	}
	if len(settings) == 0 {
		return nil, errors.New("file gives no settings")
	}
	return settings, nil
}

// Environment answers base with the lane's settings in place of any inherited
// ones of the same names. The order of base is kept; the lane's settings follow
// in name order.
func (l Lane) Environment(base []string) []string {
	out := make([]string, 0, len(base)+len(l.Settings))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := l.Settings[key]; !replaced {
			out = append(out, entry)
		}
	}
	for _, key := range l.Keys() {
		out = append(out, key+"="+l.Settings[key])
	}
	return out
}

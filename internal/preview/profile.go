// Package preview presents exact governed candidate bytes without writing run state.
package preview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/strictjson"
)

var (
	ErrUnavailable = errors.New("preview NOT_AVAILABLE")
	ErrIntegrity   = errors.New("preview integrity failure")
	ErrIneligible  = errors.New("checkpoint is not eligible for preview")
)
var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
var sha256Pattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var gitSHA = regexp.MustCompile(`^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)
var imagePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9./:_-]{0,200}@sha256:[a-f0-9]{64}$`)
var serviceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
var userPattern = regexp.MustCompile(`^[1-9][0-9]{0,5}:[1-9][0-9]{0,5}$`)

// PreviewProfileV1 is protected operator configuration. No field is accepted
// from a preview command. All service write budgets count against memory.
type PreviewProfileV1 struct {
	SchemaVersion            int                `json:"schema_version"`
	ProfileID                string             `json:"profile_id"`
	RepositoryIdentityDigest string             `json:"repository_identity_digest"`
	Services                 []ServiceProfileV1 `json:"services"`
	HealthPath               string             `json:"health_path"`
	HealthTimeoutSeconds     int                `json:"health_timeout_seconds"`
	TTLSeconds               int                `json:"ttl_seconds"`
	CPUQuota                 int                `json:"cpu_quota"`
	MemoryBytes              int64              `json:"memory_bytes"`
	PidsLimit                int                `json:"pids_limit"`
	TmpfsBytes               int64              `json:"tmpfs_bytes"`
	NetworkPolicy            string             `json:"network_policy"`
}
type ServiceProfileV1 struct {
	Name        string            `json:"name"`
	Image       string            `json:"image"`
	User        string            `json:"user"`
	PrepareArgv []string          `json:"prepare_argv"`
	StartArgv   []string          `json:"start_argv"`
	Environment map[string]string `json:"environment"`
	MountSource bool              `json:"mount_source"`
	Port        int               `json:"port"`
	Presented   bool              `json:"presented"`
}
type ProfileFileV1 struct {
	SchemaVersion int                `json:"schema_version"`
	Profiles      []PreviewProfileV1 `json:"profiles"`
}

func digest(data []byte) string           { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func jsonDigest(v any) string             { b, _ := json.Marshal(v); return digest(b) }
func (p PreviewProfileV1) Digest() string { return jsonDigest(p) }

// This small semantic allowlist deliberately excludes arbitrary values, URLs,
// credentials, runtime injection variables and inherited image environments.
func validEnvironment(env map[string]string) bool {
	if len(env) > 4 {
		return false
	}
	for k, v := range env {
		switch k {
		case "NODE_ENV":
			if v != "development" && v != "production" && v != "test" {
				return false
			}
		case "LANG", "LC_ALL":
			if v != "C" && v != "C.UTF-8" {
				return false
			}
		case "TZ":
			if v != "UTC" {
				return false
			}
		default:
			return false
		}
	}
	return true
}
func validArgv(argv []string, optional bool) bool {
	if len(argv) == 0 {
		return optional
	}
	if len(argv) > 32 || !filepath.IsAbs(argv[0]) || filepath.Clean(argv[0]) != argv[0] {
		return false
	}
	// Shells and inline interpreters are not a configuration escape hatch.
	switch filepath.Base(argv[0]) {
	case "sh", "bash", "dash", "ash", "zsh", "fish", "ksh", "env", "sudo", "su", "docker", "podman":
		return false
	}
	n := 0
	for _, v := range argv {
		n += len(v)
		if len(v) > 1024 || strings.ContainsAny(v, "\x00\r\n`$;|&<>") || v == "-c" || v == "-e" || v == "--eval" || strings.Contains(v, "docker.sock") {
			return false
		}
	}
	return n <= 8192
}
func (p PreviewProfileV1) Validate() error {
	if p.SchemaVersion != 1 || !identifier.MatchString(p.ProfileID) || !sha256Pattern.MatchString(p.RepositoryIdentityDigest) || len(p.Services) < 1 || len(p.Services) > 4 || p.NetworkPolicy != "INTERNAL_ONLY" {
		return ErrUnavailable
	}
	if p.TTLSeconds < 1 || p.TTLSeconds > 3600 || p.HealthTimeoutSeconds < 1 || p.HealthTimeoutSeconds > 30 || p.HealthTimeoutSeconds > p.TTLSeconds || p.CPUQuota < 1000 || p.CPUQuota > 200000 || p.MemoryBytes < 16<<20 || p.MemoryBytes > 2<<30 || p.PidsLimit < 1 || p.PidsLimit > 256 || p.TmpfsBytes < 1<<20 || p.TmpfsBytes > 256<<20 || p.TmpfsBytes > p.MemoryBytes/2 {
		return ErrUnavailable
	}
	if len(p.HealthPath) > 256 || !strings.HasPrefix(p.HealthPath, "/") || strings.HasPrefix(p.HealthPath, "//") || strings.ContainsAny(p.HealthPath, "\x00\r\n?#\\") {
		return ErrUnavailable
	}
	seen := map[string]bool{}
	presented := 0
	for _, s := range p.Services {
		if !serviceName.MatchString(s.Name) || seen[s.Name] || !imagePattern.MatchString(s.Image) || !userPattern.MatchString(s.User) || !validArgv(s.PrepareArgv, true) || !validArgv(s.StartArgv, false) || !validEnvironment(s.Environment) || s.Port < 1024 || s.Port > 65535 {
			return ErrUnavailable
		}
		// busybox applets can otherwise smuggle a shell as argv[1].
		if filepath.Base(s.StartArgv[0]) == "busybox" || (len(s.PrepareArgv) > 0 && filepath.Base(s.PrepareArgv[0]) == "busybox") {
			return ErrUnavailable
		}
		seen[s.Name] = true
		if s.Presented {
			presented++
		}
	}
	if presented != 1 {
		return ErrUnavailable
	}
	return nil
}
func LoadProfiles(path string) (map[string]PreviewProfileV1, error) {
	if path == "" {
		return map[string]PreviewProfileV1{}, nil
	}
	b, err := readFile(path, 256<<10, true)
	if err != nil {
		return nil, ErrUnavailable
	}
	var file ProfileFileV1
	if strictjson.Decode(b, &file) != nil || file.SchemaVersion != 1 || len(file.Profiles) == 0 || len(file.Profiles) > 32 {
		return nil, ErrUnavailable
	}
	out := map[string]PreviewProfileV1{}
	for _, p := range file.Profiles {
		if p.Validate() != nil {
			return nil, ErrUnavailable
		}
		if _, ok := out[p.ProfileID]; ok {
			return nil, ErrUnavailable
		}
		out[p.ProfileID] = p
	}
	return out, nil
}
func environmentValues(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []string{}
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}

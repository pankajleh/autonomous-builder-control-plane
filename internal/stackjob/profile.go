package stackjob

import (
	"errors"
	"os"
	"regexp"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/preview"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/strictjson"
)

// ProfilesV1 is the operator's stack profile file (--stack-profiles-file).
type ProfilesV1 struct {
	SchemaVersion int         `json:"schema_version"`
	Profiles      []ProfileV1 `json:"profiles"`
}

// ProfileV1 is one image and what it serves: a stack's versions, its PATH and few settings, its limits and its
// registries. Each stack and version is served by one profile. A legacy profile serves versions out of support, for a
// migration's checks before alone (Repo C LEGACY_MIGRATION §5, M4): never for a stack job, a fix, tests, a scan, an
// agent's commands or a migration's checks after.
type ProfileV1 struct {
	ProfileID      string            `json:"profile_id"`
	Stack          string            `json:"stack"`
	Versions       []string          `json:"versions"`
	Image          string            `json:"image"`
	Path           string            `json:"path"`
	Environment    map[string]string `json:"environment,omitempty"`
	CPUQuota       int               `json:"cpu_quota"`
	MemoryBytes    int64             `json:"memory_bytes"`
	PidsLimit      int               `json:"pids_limit"`
	TmpfsBytes     int64             `json:"tmpfs_bytes"`
	WorkBytes      int64             `json:"work_bytes"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	Egress         []string          `json:"egress"`
	Legacy         bool              `json:"legacy,omitempty"`
}

// servedStacks are the stacks a profile may serve.
var servedStacks = map[string]bool{StackPython: true, StackJava: true, StackNode: true, StackGo: true, StackRuby: true, StackPHP: true,
	StackDotnet: true}

var (
	profileID     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	versionPat    = regexp.MustCompile(`^\d{1,2}(?:\.\d{1,2})?$`)
	pathPat       = regexp.MustCompile(`^/[A-Za-z0-9._/-]*(?::/[A-Za-z0-9._/-]*){0,15}$`)
	settingValue  = regexp.MustCompile(`^[A-Za-z0-9._/:-]{1,256}$`)
	errBadProfile = errors.New("stack profile invalid")
)

// Spec is the sandbox the profile runs a part in.
func (p ProfileV1) Spec() preview.StackSpec {
	return preview.StackSpec{Image: p.Image, CPUQuota: p.CPUQuota, MemoryBytes: p.MemoryBytes, PidsLimit: p.PidsLimit, TmpfsBytes: p.TmpfsBytes,
		Lifetime: time.Duration(p.TimeoutSeconds)*time.Second + 2*time.Minute, Egress: p.Egress}
}

// Validate checks one profile.
func (p ProfileV1) Validate() error {
	if !profileID.MatchString(p.ProfileID) || !servedStacks[p.Stack] || len(p.Versions) == 0 ||
		len(p.Versions) > 8 || !pathPat.MatchString(p.Path) || len(p.Environment) > 8 || p.WorkBytes < 64<<20 || p.WorkBytes > 32<<30 ||
		p.TimeoutSeconds < 60 || p.TimeoutSeconds > 3600 || !p.Spec().Valid() {
		return errBadProfile
	}
	for _, v := range p.Versions {
		if !versionPat.MatchString(v) {
			return errBadProfile
		}
	}
	for k, v := range p.Environment {
		if !allowedEnvironment[k] || !settingValue.MatchString(v) {
			return errBadProfile
		}
	}
	return nil
}

// LoadProfiles reads and checks the profile file: schema 1, at most 32 profiles, each valid, ids unique, and each stack
// and version served once.
func LoadProfiles(path string) (ProfilesV1, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return ProfilesV1{}, errBadProfile
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ProfilesV1{}, errBadProfile
	}
	var file ProfilesV1
	if strictjson.Decode(data, &file) != nil {
		return ProfilesV1{}, errBadProfile
	}
	return file, file.Validate()
}

// Validate checks the whole file.
func (f ProfilesV1) Validate() error {
	if f.SchemaVersion != 1 || len(f.Profiles) == 0 || len(f.Profiles) > 32 {
		return errBadProfile
	}
	ids, served := map[string]bool{}, map[string]bool{}
	for _, p := range f.Profiles {
		if p.Validate() != nil || ids[p.ProfileID] {
			return errBadProfile
		}
		ids[p.ProfileID] = true
		for _, v := range p.Versions {
			if served[p.Stack+" "+v] {
				return errBadProfile
			}
			served[p.Stack+" "+v] = true
		}
	}
	return nil
}

// Available is each stack's versions, legacy ones left out.
func (f ProfilesV1) Available() Available {
	out := Available{}
	for _, p := range f.Profiles {
		if !p.Legacy {
			out[p.Stack] = append(out[p.Stack], p.Versions...)
		}
	}
	return out
}

// WithLegacy is each stack's versions, legacy ones too: what an assessment's or a migration's checks before may run on.
func (f ProfilesV1) WithLegacy() Available {
	out := Available{}
	for _, p := range f.Profiles {
		out[p.Stack] = append(out[p.Stack], p.Versions...)
	}
	return out
}

// For is the profile serving a stack's version.
func (f ProfilesV1) For(stack, version string) (ProfileV1, bool) {
	for _, p := range f.Profiles {
		if p.Stack != stack {
			continue
		}
		for _, v := range p.Versions {
			if v == version {
				return p, true
			}
		}
	}
	return ProfileV1{}, false
}

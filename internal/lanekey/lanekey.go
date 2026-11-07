// Package lanekey keeps the key lanes' key files (Repo C design note BUILD_ENGINE_RULES.md §9, slice 5;
// docs/plans/lane-keys.md), so a key entered on Repo C's AI and API keys page reaches builds.
//
// A key lane is an open-model provider (Repo C design note OPEN_MODEL_ENGINES) that a build profile's manifest
// template names in executor.provider: Together, Groq. Its key file is the template's executor.provider.key_file,
// which openmodel.Begin reads when each build on it starts. The lanes are the ones the admission profile file names
// when ABCP starts; an engine lane file (executor.lane: an API-key .env lane or an administrator's account lane) is
// never a key lane, and a key is never written for one.
//
// Setting a key writes the lane's key file whole: a new file beside it (0600, the controller's user), written and
// synced, renamed over it and the folder synced, so a build starting meanwhile reads the old key or the new one, never
// part of either. A build already running keeps the key it started with: it holds it in memory, never re-reads the
// file. Removing a key removes the file, so the lane's engines read lane_no_key in GET /v1/engines.
//
// The key is never logged, answered, or put in a ledger event or an evidence item: answers and audit lines carry the
// lane and the first 8 hex of the key's SHA-256. ABCP never calls the provider here; it checks the key's shape only.
package lanekey

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/enginelane"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/openmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runadmission"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/strictjson"
)

// Lane is one key lane: the provider's id, the key file its profiles' builds read, and those profiles.
type Lane struct {
	Name     string
	KeyFile  string
	Profiles []string
}

// Lanes are the lanes the admission profile file names: the key lanes, and the names of the engine lanes (files and
// accounts), which never take a key.
type Lanes struct {
	Key   []Lane
	Other []string
}

// Load reads the admission profile file and each profile's manifest template, as abcp serve's run admission reads
// them (private files of the controller's user, no link in their paths, strict JSON), and answers the lanes they name.
// It reads no key file. Two profiles that name one provider must name the same key file, a key file belongs to one
// lane, and no name is both a key lane and an engine lane.
func Load(profileFile string) (Lanes, error) {
	data, err := enginelane.ReadPrivate(profileFile, runadmission.MaxProfileFileBytes)
	if err != nil {
		return Lanes{}, fmt.Errorf("the admission profile file cannot be read: %w", err)
	}
	var file runadmission.ProfileFileV1
	if err := strictjson.Decode(data, &file); err != nil || file.SchemaVersion != 1 || len(file.Profiles) == 0 || len(file.Profiles) > runadmission.MaxProfiles {
		return Lanes{}, errors.New("the admission profile file is not valid")
	}
	keys := map[string]*Lane{}
	others := map[string]bool{}
	files := map[string]string{}
	for _, profile := range file.Profiles {
		if !filepath.IsAbs(profile.ManifestTemplatePath) || filepath.Clean(profile.ManifestTemplatePath) != profile.ManifestTemplatePath {
			return Lanes{}, fmt.Errorf("profile %q: its manifest template path is not absolute and clean", profile.ProfileID)
		}
		template, err := enginelane.ReadPrivate(profile.ManifestTemplatePath, runadmission.MaxManifestTemplateBytes)
		if err != nil {
			return Lanes{}, fmt.Errorf("profile %q: its manifest template cannot be read: %w", profile.ProfileID, err)
		}
		var manifest authority.Manifest
		if err := strictjson.Decode(template, &manifest); err != nil {
			return Lanes{}, fmt.Errorf("profile %q: its manifest template is not valid", profile.ProfileID)
		}
		executor := manifest.Executor
		switch {
		case executor.Provider != nil:
			provider := *executor.Provider
			if err := provider.Validate(); err != nil {
				return Lanes{}, fmt.Errorf("profile %q: %w", profile.ProfileID, err)
			}
			lane := keys[provider.ID]
			if lane == nil {
				if owner, taken := files[provider.KeyFile]; taken && owner != provider.ID {
					return Lanes{}, fmt.Errorf("lanes %q and %q name one key file", owner, provider.ID)
				}
				files[provider.KeyFile] = provider.ID
				lane = &Lane{Name: provider.ID, KeyFile: provider.KeyFile}
				keys[provider.ID] = lane
			}
			if lane.KeyFile != provider.KeyFile {
				return Lanes{}, fmt.Errorf("lane %q: its profiles name two key files", provider.ID)
			}
			lane.Profiles = append(lane.Profiles, profile.ProfileID)
		case executor.Lane != "":
			others[executor.Lane] = true
		}
	}
	out := Lanes{}
	for name, lane := range keys {
		if others[name] {
			return Lanes{}, fmt.Errorf("lane %q is both a provider and an engine lane", name)
		}
		sort.Strings(lane.Profiles)
		out.Key = append(out.Key, *lane)
	}
	sort.Slice(out.Key, func(i, j int) bool { return out.Key[i].Name < out.Key[j].Name })
	for name := range others {
		out.Other = append(out.Other, name)
	}
	sort.Strings(out.Other)
	return out, nil
}

// Manager keeps the key lanes' key files and answers the lane routes (serviceapi.LaneKeyController).
type Manager struct {
	lanes map[string]Lane
	names []string
	other map[string]bool
	log   io.Writer
	now   func() time.Time
	// mu serializes writes and removals, so two requests for one lane never race on its file.
	mu sync.Mutex
}

// New answers a manager of those lanes. Audit lines (the lane, the key's fingerprint, never the key) go to log.
func New(lanes Lanes, log io.Writer, now func() time.Time) (*Manager, error) {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = io.Discard
	}
	m := &Manager{lanes: map[string]Lane{}, other: map[string]bool{}, log: log, now: now}
	for _, lane := range lanes.Key {
		if _, twice := m.lanes[lane.Name]; twice || !filepath.IsAbs(lane.KeyFile) || filepath.Clean(lane.KeyFile) != lane.KeyFile {
			return nil, fmt.Errorf("lane %q is given twice or its key file is not an absolute, clean path", lane.Name)
		}
		lane.Profiles = slices.Clone(lane.Profiles)
		m.lanes[lane.Name] = lane
		m.names = append(m.names, lane.Name)
	}
	sort.Strings(m.names)
	for _, name := range lanes.Other {
		if _, key := m.lanes[name]; key {
			return nil, fmt.Errorf("lane %q is both a provider and an engine lane", name)
		}
		m.other[name] = true
	}
	return m, nil
}

// Names answers the key lanes' names, sorted.
func (m *Manager) Names() []string { return slices.Clone(m.names) }

// Lanes answers each key lane's state and key fingerprint, reading each key file under the builds' own rules.
func (m *Manager) Lanes(context.Context) (serviceapi.LanesV1, error) {
	out := serviceapi.LanesV1{SchemaVersion: serviceapi.LanesSchemaV1, Lanes: make([]serviceapi.LaneV1, 0, len(m.names))}
	for _, name := range m.names {
		out.Lanes = append(out.Lanes, m.state(m.lanes[name]))
	}
	return out, nil
}

// state reads a lane's key file as a build would (openmodel.ReadKey).
func (m *Manager) state(lane Lane) serviceapi.LaneV1 {
	answer := serviceapi.LaneV1{Lane: lane.Name, Kind: serviceapi.LaneKindKey, Profiles: slices.Clone(lane.Profiles)}
	key, err := openmodel.ReadKey(lane.KeyFile)
	switch {
	case errors.Is(err, openmodel.ErrKeyMissing):
		answer.State = serviceapi.LaneKeyMissing
	case err != nil:
		answer.State = serviceapi.LaneKeyUnreadable
	default:
		answer.State, answer.Fingerprint = serviceapi.LaneKeySet, Fingerprint(key.Value())
	}
	return answer
}

func (m *Manager) lane(name string) (Lane, error) {
	if lane, ok := m.lanes[name]; ok {
		return lane, nil
	}
	if m.other[name] {
		return Lane{}, serviceapi.ErrLaneNotKey
	}
	return Lane{}, serviceapi.ErrLaneUnknown
}

// SetKey writes the lane's key file with the key, after checking its shape for the provider, and reads it back as the
// next build will. The answer and the audit line carry the key's fingerprint, never the key.
func (m *Manager) SetKey(_ context.Context, name string, request serviceapi.LaneKeyRequestV1) (serviceapi.LaneV1, error) {
	lane, err := m.lane(name)
	if err != nil {
		return serviceapi.LaneV1{}, err
	}
	if err := CheckShape(lane.Name, request.Key); err != nil {
		m.audit("refused", lane.Name, "reason=shape")
		return serviceapi.LaneV1{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := writeKeyFile(lane.KeyFile, []byte(request.Key+"\n")); err != nil {
		m.audit("not set", lane.Name, "reason=write")
		return serviceapi.LaneV1{}, fmt.Errorf("%w: %s", serviceapi.ErrLaneKeyUnavailable, err.Error())
	}
	answer := m.state(lane)
	if answer.State != serviceapi.LaneKeySet || answer.Fingerprint != Fingerprint(request.Key) {
		m.audit("not set", lane.Name, "reason=read-back")
		return serviceapi.LaneV1{}, fmt.Errorf("%w: the key file does not read back as the key", serviceapi.ErrLaneKeyUnavailable)
	}
	m.audit("set", lane.Name, "fingerprint="+answer.Fingerprint)
	return answer, nil
}

// RemoveKey removes the lane's key file; a lane without one answers missing as well.
func (m *Manager) RemoveKey(_ context.Context, name string) (serviceapi.LaneV1, error) {
	lane, err := m.lane(name)
	if err != nil {
		return serviceapi.LaneV1{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	before := m.state(lane)
	removed, err := removeKeyFile(lane.KeyFile)
	if err != nil {
		m.audit("not removed", lane.Name, "reason=remove")
		return serviceapi.LaneV1{}, fmt.Errorf("%w: %s", serviceapi.ErrLaneKeyUnavailable, err.Error())
	}
	answer := m.state(lane)
	if answer.State != serviceapi.LaneKeyMissing {
		return serviceapi.LaneV1{}, fmt.Errorf("%w: the key file is still there", serviceapi.ErrLaneKeyUnavailable)
	}
	switch {
	case !removed:
		m.audit("removed", lane.Name, "was=missing")
	case before.Fingerprint != "":
		m.audit("removed", lane.Name, "fingerprint="+before.Fingerprint)
	default:
		m.audit("removed", lane.Name, "was="+before.State)
	}
	return answer, nil
}

var auditMu sync.Mutex

// audit writes one line naming the lane and what happened: a fingerprint or a reason, never the key.
func (m *Manager) audit(what, lane, detail string) {
	auditMu.Lock()
	defer auditMu.Unlock()
	fmt.Fprintf(m.log, "abcp lane key %s at=%s lane=%s %s\n", what, m.now().UTC().Format(time.RFC3339), lane, detail)
}

// Fingerprint answers the first 8 hex of the key's SHA-256: enough to tell keys apart on a page, nothing to find the
// key with.
func Fingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:8]
}

// shape is what a provider's key looks like: its length and its characters.
type shape struct {
	min, max int
	allowed  func(byte) bool
	words    string
}

func keyCharacter(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

func printable(c byte) bool { return c >= 0x21 && c <= 0x7e }

// shapes are the providers' keys' shapes: Together's (64 hex, or tgp_v1_ and 43 more) and Groq's (gsk_ and 52 more),
// with room either way. Any other provider's key has openmodel.ReadKey's shape: 16 to 512 printable characters
// without spaces, never NAME=value.
var shapes = map[string]shape{
	"together": {min: 32, max: 128, allowed: keyCharacter, words: "32 to 128 letters, digits, '_' or '-'"},
	"groq":     {min: 40, max: 128, allowed: keyCharacter, words: "40 to 128 letters, digits, '_' or '-'"},
}

var defaultShape = shape{min: 16, max: 512, allowed: printable, words: "16 to 512 printable characters without spaces"}

// CheckShape reports whether key has the provider's shape. Its error names the rule, never the key.
func CheckShape(provider, key string) error {
	rule, known := shapes[provider]
	if !known {
		rule = defaultShape
	}
	bad := len(key) < rule.min || len(key) > rule.max || !known && envName(key)
	for index := 0; !bad && index < len(key); index++ {
		bad = !rule.allowed(key[index])
	}
	if bad {
		return fmt.Errorf("%w: a %s key is %s", serviceapi.ErrLaneKeyInvalid, provider, rule.words)
	}
	return nil
}

// envName reports whether key starts NAME=, a lane file's line: openmodel.ReadKey refuses it.
func envName(key string) bool {
	name, _, found := strings.Cut(key, "=")
	if !found || name == "" || name[0] < 'A' || name[0] > 'Z' {
		return false
	}
	for index := 1; index < len(name); index++ {
		c := name[index]
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

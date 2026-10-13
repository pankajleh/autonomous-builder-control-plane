// Package accountlane keeps the account lanes: engine lanes the operator set
// up for an administrator's own Codex or Claude account (Repo C design note
// AI_KEYS_AND_MODELS §9.5, step 4c; docs/plans/account-lanes.md).
//
// The operator makes each lane once: its lane file and its own private home
// (CODEX_HOME for Codex, CLAUDE_CONFIG_DIR for Claude), and the build profiles
// that name it. ABCP then only signs a lane in and out:
//   - a Codex lane by `codex login --device-auth` in the lane's home, whose
//     link and one-time code the administrator opens on their own device;
//     Codex keeps the sign-in in the home's auth.json;
//   - a Claude lane by a token made with `claude setup-token`, which ABCP
//     writes into the lane file as CLAUDE_CODE_OAUTH_TOKEN (mode 0600).
//
// No credential leaves ABCP: answers carry only the state, when it was signed
// in, and a pending device sign-in's link and code.
package accountlane

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/enginelane"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// FileV1 is the operator's account lanes file (abcp serve --account-lanes-file).
type FileV1 struct {
	SchemaVersion int      `json:"schema_version"`
	Lanes         []LaneV1 `json:"lanes"`
}

// LaneV1 is one account lane: its name, its engine, its lane file and its home.
type LaneV1 struct {
	Lane     string `json:"lane"`
	Executor string `json:"executor"`
	LaneFile string `json:"lane_file"`
	Home     string `json:"home"`
}

// MaxLanes bounds the account lanes file.
const MaxLanes = 8

// homeSetting names the setting that points each engine at its own home.
var homeSetting = map[string]string{"codex": "CODEX_HOME", "claude": "CLAUDE_CONFIG_DIR"}

// tokenSetting is the Claude setting a setup token goes in.
const tokenSetting = "CLAUDE_CODE_OAUTH_TOKEN"

// claudeTokenPattern is what `claude setup-token` makes.
var claudeTokenPattern = regexp.MustCompile(`^sk-ant-oat[0-9]{2}-[A-Za-z0-9_-]{20,500}$`)

// DeviceLogin is a running Codex device sign-in.
type DeviceLogin struct {
	VerificationURL string
	UserCode        string
	ExpiresAt       time.Time
	// Done is closed when the sign-in process has ended, either way.
	Done <-chan struct{}
	// Stop ends the process, if it still runs.
	Stop func()
}

// StartDeviceLogin starts `codex login --device-auth` with CODEX_HOME set to
// home and answers once the link and code are known.
type StartDeviceLogin func(ctx context.Context, home string) (DeviceLogin, error)

// Manager signs the account lanes in and out.
type Manager struct {
	lanes []LaneV1
	start StartDeviceLogin
	clock func() time.Time

	mu      sync.Mutex
	pending map[string]*DeviceLogin
	ctx     context.Context
	cancel  context.CancelFunc
}

// New checks every lane and answers the manager. A lane whose file or home is
// not the operator's private one, or whose file gives anything but its home
// (and, for Claude, its token), is refused.
func New(file FileV1, start StartDeviceLogin, clock func() time.Time) (*Manager, error) {
	if file.SchemaVersion != 1 || len(file.Lanes) == 0 || len(file.Lanes) > MaxLanes {
		return nil, errors.New("invalid account lanes file")
	}
	if start == nil || clock == nil {
		return nil, errors.New("account lanes need a device sign-in and a clock")
	}
	seen := map[string]bool{}
	for _, lane := range file.Lanes {
		if !enginelane.ValidName(lane.Lane) || homeSetting[lane.Executor] == "" || !enginelane.ValidPath(lane.LaneFile) ||
			!filepath.IsAbs(lane.Home) || filepath.Clean(lane.Home) != lane.Home {
			return nil, fmt.Errorf("account lane %q is invalid", lane.Lane)
		}
		for _, key := range []string{"lane:" + lane.Lane, "file:" + lane.LaneFile, "home:" + lane.Home} {
			if seen[key] {
				return nil, fmt.Errorf("account lane %q repeats a name, file or home", lane.Lane)
			}
			seen[key] = true
		}
		if err := privateDirectory(lane.Home); err != nil {
			return nil, fmt.Errorf("account lane %s home: %w", lane.Lane, err)
		}
		if _, err := settingsOf(lane); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{lanes: append([]LaneV1(nil), file.Lanes...), start: start, clock: clock, pending: map[string]*DeviceLogin{}, ctx: ctx, cancel: cancel}, nil
}

// Close stops every pending device sign-in.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.cancel()
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, login := range m.pending {
		login.Stop()
		delete(m.pending, name)
	}
}

// settingsOf loads a lane's file and checks it gives exactly its home, and for
// Claude perhaps its token.
func settingsOf(lane LaneV1) (map[string]string, error) {
	loaded, err := enginelane.Load(lane.Lane, lane.LaneFile, lane.Executor)
	if err != nil {
		return nil, fmt.Errorf("account lane %s: %w", lane.Lane, err)
	}
	if loaded.Settings[homeSetting[lane.Executor]] != lane.Home {
		return nil, fmt.Errorf("account lane %s: its file must point %s at its home", lane.Lane, homeSetting[lane.Executor])
	}
	for key := range loaded.Settings {
		if key != homeSetting[lane.Executor] && !(lane.Executor == "claude" && key == tokenSetting) {
			return nil, fmt.Errorf("account lane %s: its file may give only %s", lane.Lane, homeSetting[lane.Executor])
		}
	}
	return loaded.Settings, nil
}

func (m *Manager) lane(name string) (LaneV1, error) {
	for _, lane := range m.lanes {
		if lane.Lane == name {
			return lane, nil
		}
	}
	return LaneV1{}, serviceapi.ErrAccountLaneUnknown
}

// AccountLanes answers every lane's state.
func (m *Manager) AccountLanes(context.Context) (serviceapi.AccountLanesV1, error) {
	out := serviceapi.AccountLanesV1{SchemaVersion: serviceapi.AccountLanesSchemaV1, Lanes: make([]serviceapi.AccountLaneV1, 0, len(m.lanes))}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, lane := range m.lanes {
		state, err := m.stateLocked(lane)
		if err != nil {
			return serviceapi.AccountLanesV1{}, serviceapi.ErrAccountLaneUnavailable
		}
		out.Lanes = append(out.Lanes, state)
	}
	return out, nil
}

// stateLocked answers a lane's state; m.mu is held.
func (m *Manager) stateLocked(lane LaneV1) (serviceapi.AccountLaneV1, error) {
	out := serviceapi.AccountLaneV1{Lane: lane.Lane, Executor: lane.Executor, State: "signed_out"}
	switch lane.Executor {
	case "codex":
		if info, err := os.Lstat(filepath.Join(lane.Home, "auth.json")); err == nil && info.Mode().IsRegular() {
			out.State, out.Since = "signed_in", info.ModTime().UTC().Format(time.RFC3339)
			return out, nil
		}
		if login := m.pending[lane.Lane]; login != nil {
			select {
			case <-login.Done:
				delete(m.pending, lane.Lane)
			default:
				if m.clock().Before(login.ExpiresAt) {
					out.State = "signing_in"
					out.Device = &serviceapi.AccountLaneDeviceV1{VerificationURL: login.VerificationURL, UserCode: login.UserCode,
						ExpiresAt: login.ExpiresAt.UTC().Format(time.RFC3339)}
				}
			}
		}
	case "claude":
		settings, err := settingsOf(lane)
		if err != nil {
			return out, err
		}
		if settings[tokenSetting] != "" {
			info, err := os.Lstat(lane.LaneFile)
			if err != nil {
				return out, err
			}
			out.State, out.Since = "signed_in", info.ModTime().UTC().Format(time.RFC3339)
		}
	}
	return out, nil
}

// SignIn signs a lane in: a Codex lane starts (or shows again) its device
// sign-in; a Claude lane keeps the setup token given, replacing any before it.
func (m *Manager) SignIn(ctx context.Context, name string, request serviceapi.AccountLaneSignInV1) (serviceapi.AccountLaneV1, error) {
	lane, err := m.lane(name)
	if err != nil {
		return serviceapi.AccountLaneV1{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch lane.Executor {
	case "codex":
		if request.Token != "" {
			return serviceapi.AccountLaneV1{}, serviceapi.ErrAccountLaneInvalid
		}
		state, err := m.stateLocked(lane)
		if err != nil {
			return serviceapi.AccountLaneV1{}, serviceapi.ErrAccountLaneUnavailable
		}
		switch state.State {
		case "signed_in":
			return serviceapi.AccountLaneV1{}, serviceapi.ErrAccountLaneSignedIn
		case "signing_in":
			return state, nil
		}
		if old := m.pending[lane.Lane]; old != nil {
			old.Stop()
			delete(m.pending, lane.Lane)
		}
		login, err := m.start(m.ctx, lane.Home)
		if err != nil {
			return serviceapi.AccountLaneV1{}, serviceapi.ErrAccountLaneUnavailable
		}
		m.pending[lane.Lane] = &login
		return m.stateLocked(lane)
	case "claude":
		if !claudeTokenPattern.MatchString(request.Token) {
			return serviceapi.AccountLaneV1{}, serviceapi.ErrAccountLaneInvalid
		}
		if err := writeLaneFile(lane.LaneFile, map[string]string{homeSetting["claude"]: lane.Home, tokenSetting: request.Token}); err != nil {
			return serviceapi.AccountLaneV1{}, serviceapi.ErrAccountLaneUnavailable
		}
		state, err := m.stateLocked(lane)
		if err != nil {
			return serviceapi.AccountLaneV1{}, serviceapi.ErrAccountLaneUnavailable
		}
		return state, nil
	}
	return serviceapi.AccountLaneV1{}, serviceapi.ErrAccountLaneUnknown
}

// SignOut signs a lane out: a Codex lane's pending sign-in stops and its
// auth.json goes; a Claude lane's file keeps only its home.
func (m *Manager) SignOut(ctx context.Context, name string) (serviceapi.AccountLaneV1, error) {
	lane, err := m.lane(name)
	if err != nil {
		return serviceapi.AccountLaneV1{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch lane.Executor {
	case "codex":
		if login := m.pending[lane.Lane]; login != nil {
			login.Stop()
			delete(m.pending, lane.Lane)
		}
		if err := os.Remove(filepath.Join(lane.Home, "auth.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return serviceapi.AccountLaneV1{}, serviceapi.ErrAccountLaneUnavailable
		}
	case "claude":
		if err := writeLaneFile(lane.LaneFile, map[string]string{homeSetting["claude"]: lane.Home}); err != nil {
			return serviceapi.AccountLaneV1{}, serviceapi.ErrAccountLaneUnavailable
		}
	}
	state, err := m.stateLocked(lane)
	if err != nil {
		return serviceapi.AccountLaneV1{}, serviceapi.ErrAccountLaneUnavailable
	}
	return state, nil
}

// writeLaneFile replaces a lane file whole: a new private file beside it,
// written and synced, then renamed over it, so a build starting meanwhile reads
// the old file or the new one, never part of either.
func writeLaneFile(path string, settings map[string]string) error {
	keys := make([]string, 0, len(settings))
	for key, value := range settings {
		if strings.ContainsAny(value, "\r\n\x00") || value == "" {
			return errors.New("invalid lane setting")
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var body strings.Builder
	body.WriteString("# An account lane, written by ABCP (docs/plans/account-lanes.md).\n")
	for _, key := range keys {
		body.WriteString(key + "=" + settings[key] + "\n")
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(body.String()); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

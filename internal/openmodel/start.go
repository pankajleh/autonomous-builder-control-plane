package openmodel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// UnavailableError says the provider could not take the build before its
// start deadline: every slot stayed taken, or the provider kept answering 429,
// 5xx or nothing. Nothing has started. Only this error lets a manifest's
// fallback run instead; a missing or refused key and an unknown model fail the
// build, so a misconfigured engine is never hidden behind another one.
type UnavailableError struct {
	Provider, Model, Reason, Detail string
}

// Unavailable reasons.
const (
	ReasonBusy        = "busy"
	ReasonUnavailable = "unavailable"
)

func (e *UnavailableError) Error() string {
	return "OPEN_MODEL_ENGINE_UNAVAILABLE: " + e.Provider + "/" + e.Model + ": " + e.Detail
}

// StartOptions are a build start's seams; zero values are the live ones.
type StartOptions struct {
	Client   *http.Client
	Backoff  Backoff
	SlotPoll time.Duration
	Now      func() time.Time
	Sleep    func(context.Context, time.Duration) error
	// Run names the build in the relay's log lines.
	Run string
	// RelayTransport is how the relay reaches the provider (RelayOptions).
	RelayTransport http.RoundTripper
}

// Start is a build that may run on its provider: its slot is held until
// Release.
type Start struct {
	Provider      Provider
	Model         Model
	Home          string
	ConfigSHA256  string
	KeyFileSHA256 string
	Preflight     *Check
	// Slot is the model's build slot the build holds (1 to its cap).
	Slot int
	// CatalogSHA256 is the SHA-256 of the model's Codex catalog file, empty
	// without one.
	CatalogSHA256 string
	// Relay is the build's loopback relay, nil unless the provider asks for
	// it; it serves until Release.
	Relay    *Relay
	relayLog *relayLog
	// logHome is the model's slot home, where the relay's log is kept; Home
	// moves to the build's own folder when the build is walled (UseHome).
	logHome string
	key     Key
	release func() error
}

// String names the build's engine; a Start never prints its key.
func (s *Start) String() string {
	if s == nil {
		return "open-model start <nil>"
	}
	return "open-model start " + s.Provider.ID + "/" + s.Model.Name
}

// GoString is String for %#v.
func (s *Start) GoString() string { return s.String() }

// Begin readies one build on the provider's model: it reads the key, takes one
// of the model's build slots (waiting up to the provider's queue timeout),
// asks the provider's /models when the provider asks for a preflight (within
// the same deadline), and writes the model's Codex home. On any error the slot
// is released and nothing has started.
func Begin(ctx context.Context, p Provider, modelName string, options StartOptions) (*Start, error) {
	model, ok := p.Model(modelName)
	if !ok {
		return nil, fmt.Errorf("OPEN_MODEL_ENGINE_MODEL: %s offers no model %q", p.ID, modelName)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	key, err := ReadKey(p.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("OPEN_MODEL_ENGINE_KEY: %s: %w", p.ID, err)
	}
	deadline := now().Add(p.StartTimeout())
	release, slot, err := AcquireNumberedSlot(ctx, SlotDir(p, model), model.Slots(), deadline, options.SlotPoll)
	if errors.Is(err, ErrNoSlot) {
		return nil, &UnavailableError{Provider: p.ID, Model: model.Name, Reason: ReasonBusy,
			Detail: fmt.Sprintf("no build slot within %s (%d at a time)", p.StartTimeout(), model.Slots())}
	}
	if err != nil {
		return nil, fmt.Errorf("OPEN_MODEL_ENGINE_SLOT: %s/%s: %w", p.ID, model.Name, err)
	}
	start := &Start{Provider: p, Model: model, KeyFileSHA256: key.SHA256(), Slot: slot, release: release}
	fail := func(err error) (*Start, error) {
		return nil, errors.Join(err, start.Release())
	}
	if p.Preflight {
		client := options.Client
		if client == nil {
			client = NewClient(10 * time.Second)
		}
		check := Preflight(ctx, client, p.BaseURL, key, model.ID, deadline, options.Backoff, now, options.Sleep)
		start.Preflight = &check
		switch check.Status {
		case CheckOK:
		case CheckKeyRefused:
			return fail(fmt.Errorf("OPEN_MODEL_ENGINE_KEY_REFUSED: %s answered HTTP %d to its key", p.ID, check.HTTPStatus))
		case CheckModelMissing:
			return fail(fmt.Errorf("OPEN_MODEL_ENGINE_MODEL_MISSING: %s does not list %s", p.ID, model.ID))
		default:
			detail := fmt.Sprintf("the provider did not answer after %d tries", check.Attempts)
			if check.HTTPStatus != 0 {
				detail = fmt.Sprintf("the provider answered HTTP %d after %d tries", check.HTTPStatus, check.Attempts)
			}
			return fail(&UnavailableError{Provider: p.ID, Model: model.Name, Reason: ReasonUnavailable, Detail: detail})
		}
	}
	if err := ctx.Err(); err != nil {
		return fail(context.Cause(ctx))
	}
	home, catalog := homeOf(p, model, slot)
	var files []HomeFile
	if catalog != nil {
		files = append(files, HomeFile{Name: CatalogFile, Data: catalog})
		sum := sha256.Sum256(catalog)
		start.CatalogSHA256 = hex.EncodeToString(sum[:])
	}
	if err := WriteHomeFiles(p.CodexHome, home, files...); err != nil {
		return fail(fmt.Errorf("OPEN_MODEL_ENGINE_HOME: %s/%s: %w", p.ID, model.Name, err))
	}
	baseURL := p.BaseURL
	if p.Relay {
		if start.relayLog, err = openRelayLog(home); err != nil {
			return fail(fmt.Errorf("OPEN_MODEL_ENGINE_RELAY: %s/%s: %w", p.ID, model.Name, err))
		}
		start.Relay, err = StartRelay(p, model, RelayOptions{Transport: options.RelayTransport, Log: start.relayLog, Run: options.Run, Now: now})
		if err != nil {
			return fail(fmt.Errorf("OPEN_MODEL_ENGINE_RELAY: %s/%s: %w", p.ID, model.Name, err))
		}
		baseURL = start.Relay.URL()
	}
	config := configOf(p, model, home, baseURL, catalog)
	if err := WriteHomeFiles(p.CodexHome, home, HomeFile{Name: "config.toml", Data: config}); err != nil {
		return fail(fmt.Errorf("OPEN_MODEL_ENGINE_HOME: %s/%s: %w", p.ID, model.Name, err))
	}
	sum := sha256.Sum256(config)
	start.Home, start.ConfigSHA256, start.key, start.logHome = home, hex.EncodeToString(sum[:]), key, home
	return start, nil
}

// baseURL is the address Codex is given: the build's relay, or the provider's.
func (s *Start) baseURL() string {
	if s.Relay != nil {
		return s.Relay.URL()
	}
	return s.Provider.BaseURL
}

// relayIdentityAddress stands for the relay's address in the configuration a
// relay build's home is named after: the port changes with every build, the
// home must not.
const relayIdentityAddress = "http://127.0.0.1:0"

// homeOf answers a build's Codex home and the model's catalog file (nil
// without one). A build that talks to the provider directly and has no
// catalog keeps the home of its configuration (HomeFor). Otherwise the home
// is named after the configuration with the relay's address and the
// catalog's path left out, and the catalog itself; and a relay build's home is
// its slot's (<model>-<12 hex>-<slot>), since its config.toml names its own
// relay's port: builds that overlap hold different slots, so they never
// rewrite each other's file.
func homeOf(p Provider, m Model, slot int) (string, []byte) {
	catalog := CodexCatalogJSON(p, m)
	if !p.Relay && catalog == nil {
		return HomeFor(p, m, CodexConfig(p, m)), nil
	}
	baseURL, catalogPath := p.BaseURL, ""
	if p.Relay {
		upstream, _ := url.Parse(p.BaseURL)
		baseURL = relayIdentityAddress + strings.TrimSuffix(upstream.Path, "/")
	}
	if catalog != nil {
		catalogPath = "$CODEX_HOME/" + CatalogFile
	}
	identity := append(codexConfig(p, m, baseURL, catalogPath), catalog...)
	home := HomeFor(p, m, identity)
	if p.Relay {
		home += "-" + strconv.Itoa(slot)
	}
	return home, catalog
}

// configOf answers the build's config.toml in that home, with Codex's
// base_url at baseURL and the catalog's path when there is one.
func configOf(p Provider, m Model, home, baseURL string, catalog []byte) []byte {
	catalogPath := ""
	if catalog != nil {
		catalogPath = filepath.Join(home, CatalogFile)
	}
	return codexConfig(p, m, baseURL, catalogPath)
}

// Environment answers base for Ralphex and Codex on the provider: without the
// inherited OpenAI and Codex settings, with the model's CODEX_HOME and the
// provider's key variable.
func (s *Start) Environment(base []string) []string {
	return Environment(base, map[string]string{"CODEX_HOME": s.Home, s.Provider.EnvKey(): s.key.Value()})
}

// Release stops the build's relay and gives its slot back; once is enough,
// more are harmless.
func (s *Start) Release() error {
	if s == nil {
		return nil
	}
	var err error
	if s.Relay != nil {
		err = s.Relay.Close()
		s.Relay = nil
	}
	if s.relayLog != nil {
		err = errors.Join(err, s.relayLog.Close())
		s.relayLog = nil
	}
	if s.release != nil {
		release := s.release
		s.release = nil
		err = errors.Join(err, release())
	}
	return err
}

// Evidence answers what the run's start records: the provider, model and
// configuration it ran on, never the key.
func (s *Start) Evidence() map[string]any {
	evidence := map[string]any{
		"engine_used":           "provider",
		"provider":              s.Provider.ID,
		"base_url":              s.Provider.BaseURL,
		"wire_api":              s.Provider.WireAPI,
		"model":                 s.Model.Name,
		"model_id":              s.Model.ID,
		"origin":                s.Model.Origin,
		"context_window":        s.Model.ContextWindow,
		"default_effort":        s.Model.DefaultEffort,
		"codex_home":            s.Home,
		"codex_config_sha256":   s.ConfigSHA256,
		"key_env":               s.Provider.EnvKey(),
		"key_file_sha256":       s.KeyFileSHA256,
		"max_concurrent_builds": s.Model.Slots(),
	}
	if s.Preflight != nil {
		evidence["preflight"] = map[string]any{"status": s.Preflight.Status, "attempts": s.Preflight.Attempts}
	}
	if s.Slot != 0 {
		evidence["slot"] = s.Slot
	}
	if s.CatalogSHA256 != "" {
		evidence["codex_catalog"] = map[string]any{
			"sha256": s.CatalogSHA256, "apply_patch": s.Model.CodexCatalog.ApplyPatch, "tool_output_tokens": s.Model.CodexCatalog.toolOutputTokens(),
		}
	}
	if s.Relay != nil {
		evidence["relay"] = map[string]any{
			"address": s.Relay.Address(), "upstream": s.Provider.BaseURL, "compat": append([]string{}, s.Model.Compat...),
			"log": filepath.Join(s.logHomeOrHome(), RelayLogFile),
		}
	}
	return evidence
}

func (s *Start) logHomeOrHome() string {
	if s.logHome != "" {
		return s.logHome
	}
	return s.Home
}

// FallbackEvidence answers what the run's start records when it runs on the
// manifest's fallback instead: the provider it was meant for and why not.
func FallbackEvidence(p Provider, modelName string, fallback Fallback, cause *UnavailableError) map[string]any {
	return map[string]any{
		"engine_used":     "fallback",
		"provider":        p.ID,
		"model":           modelName,
		"fallback_reason": cause.Reason,
		"fallback_detail": cause.Detail,
		"fallback": map[string]any{
			"access": "sign-in", "task_model": fallback.TaskModel, "task_effort": fallback.TaskEffort,
			"review_model": fallback.ReviewModel, "review_effort": fallback.ReviewEffort,
		},
	}
}

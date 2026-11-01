package openmodel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
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
	key           Key
	release       func() error
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
	release, err := AcquireSlot(ctx, SlotDir(p, model), model.Slots(), deadline, options.SlotPoll)
	if errors.Is(err, ErrNoSlot) {
		return nil, &UnavailableError{Provider: p.ID, Model: model.Name, Reason: ReasonBusy,
			Detail: fmt.Sprintf("no build slot within %s (%d at a time)", p.StartTimeout(), model.Slots())}
	}
	if err != nil {
		return nil, fmt.Errorf("OPEN_MODEL_ENGINE_SLOT: %s/%s: %w", p.ID, model.Name, err)
	}
	start := &Start{Provider: p, Model: model, KeyFileSHA256: key.SHA256(), release: release}
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
	config := CodexConfig(p, model)
	home := HomeFor(p, model, config)
	if err := WriteHome(p.CodexHome, home, config); err != nil {
		return fail(fmt.Errorf("OPEN_MODEL_ENGINE_HOME: %s/%s: %w", p.ID, model.Name, err))
	}
	sum := sha256.Sum256(config)
	start.Home, start.ConfigSHA256, start.key = home, hex.EncodeToString(sum[:]), key
	return start, nil
}

// Environment answers base for Ralphex and Codex on the provider: without the
// inherited OpenAI and Codex settings, with the model's CODEX_HOME and the
// provider's key variable.
func (s *Start) Environment(base []string) []string {
	return Environment(base, map[string]string{"CODEX_HOME": s.Home, s.Provider.EnvKey(): s.key.Value()})
}

// Release gives the build slot back; once is enough, more are harmless.
func (s *Start) Release() error {
	if s == nil || s.release == nil {
		return nil
	}
	release := s.release
	s.release = nil
	return release()
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
	return evidence
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

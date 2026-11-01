// Package openmodel describes an open-model build engine (Repo C design note
// OPEN_MODEL_ENGINES): Codex driven by Ralphex as before, but talking to an
// OpenAI-compatible provider such as Together AI or Groq instead of OpenAI.
//
// A build profile's manifest names the provider in executor.provider: its
// base URL, the operator's key file, the wire API Codex speaks to it, the
// folder its Codex homes live in, and the models a build may run, each under a
// short lower-case name. Nothing here is on unless a manifest names a
// provider.
//
// When a build starts, the controller reads the key file (owner-only, never a
// link), waits for one of the model's build slots, optionally asks the
// provider's /models whether the key and the model answer, and writes the
// Codex configuration for that provider and model into a private Codex home
// beside the provider's other homes. The key reaches Codex only as the one
// environment variable its configuration names; it is never written to a file,
// a log, the run's events or its workspace.
package openmodel

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// WireResponses is the only wire API Codex speaks to a provider: Codex
// refuses wire_api = "chat" since it removed Chat Completions support
// (openai/codex discussion 7782).
const WireResponses = "responses"

// Policies for a provider that cannot take a build when it is due to start.
const (
	// UnavailableWait queues the build until its start deadline, then fails it
	// before anything ran. It is the default.
	UnavailableWait = "wait"
	// UnavailableFallback starts the build on the manifest's fallback (the
	// controller's own Codex sign-in) before anything ran, and records it.
	UnavailableFallback = "fallback"
)

// Defaults, used where a provider leaves a setting out.
const (
	DefaultQueueTimeout        = 20 * time.Minute
	DefaultRequestMaxRetries   = 8
	DefaultStreamMaxRetries    = 8
	DefaultMaxConcurrentBuilds = 1
	// MaxModels bounds a provider's model list, as MaxExecutorChoices bounds a
	// profile's.
	MaxModels = 16
	// MaxKeyBytes bounds a key file.
	MaxKeyBytes = 1024
)

var (
	providerIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,23}$`)
	// modelNamePattern is the name a manifest, a profile's choices and Repo C
	// use: the run executor model pattern (serviceapi) and Repo C's choice
	// pattern alike.
	modelNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	// modelIDPattern is the provider's own id: letters of either case, digits
	// and . _ / @ + -, never ':' (Ralphex splits model:effort on it) nor a
	// quote (it is written into TOML).
	modelIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@+-]{0,127}$`)
	originPattern  = regexp.MustCompile(`^[A-Z]{2}$`)
	// Efforts are the reasoning levels Codex may send a provider. "max" is
	// Claude's alone.
	Efforts = []string{"low", "medium", "high", "xhigh"}
)

// Provider is a manifest's executor.provider.
type Provider struct {
	// ID names the provider (together, groq): the engine's lane in GET
	// /v1/engines, its Codex provider id (abcp-<id>) and its key variable
	// (<ID>_API_KEY).
	ID string `json:"id"`
	// BaseURL is the provider's OpenAI-compatible API root, without a trailing
	// slash: https, or http on a loopback address only.
	BaseURL string `json:"base_url"`
	// KeyFile is the operator's key file: one line, owner-only, never a link.
	KeyFile string `json:"key_file"`
	// WireAPI is the protocol Codex speaks to the provider: "responses".
	WireAPI string `json:"wire_api"`
	// CodexHome is the folder the provider's Codex homes live in, one per
	// model and configuration: <codex_home>/<model>-<digest>/ holds the
	// config.toml the controller writes and the sessions Codex logs, which the
	// usage collector reads; <codex_home>/slots/ holds the build slots.
	CodexHome string `json:"codex_home"`
	// Models are the models a build may run, by name.
	Models []Model `json:"models"`
	// Preflight asks the provider's /models before each build whether the key
	// and the model answer, honouring Retry-After with exponential backoff
	// until the build's start deadline.
	Preflight bool `json:"preflight,omitempty"`
	// CheckModels lets GET /v1/engines ask the provider's /models too (cached
	// for a few minutes); it never changes "ready".
	CheckModels bool `json:"check_models,omitempty"`
	// QueueTimeout bounds how long a build waits for a slot and a provider
	// that answers before it starts; DefaultQueueTimeout when empty.
	QueueTimeout string `json:"queue_timeout,omitempty"`
	// OnUnavailable is UnavailableWait (the default) or UnavailableFallback.
	OnUnavailable string `json:"on_unavailable,omitempty"`
	// Fallback is what a build runs on instead when the provider cannot take
	// it before it starts; required with UnavailableFallback, refused without.
	Fallback *Fallback `json:"fallback,omitempty"`
	// RequestMaxRetries and StreamMaxRetries are Codex's own retries of a
	// refused request or a dropped stream, each honouring Retry-After with
	// exponential backoff; the defaults when zero.
	RequestMaxRetries int `json:"request_max_retries,omitempty"`
	StreamMaxRetries  int `json:"stream_max_retries,omitempty"`
}

// Model is one model a provider build may run.
type Model struct {
	// Name is what manifests, profiles' choices and Repo C call it.
	Name string `json:"name"`
	// ID is the provider's own id, as its /models lists it.
	ID string `json:"id"`
	// Origin is the country of the company that made the model (ISO 3166
	// alpha-2), so the engines answer can say it (Repo C decides what may run
	// for whom).
	Origin string `json:"origin"`
	// ContextWindow is the model's context in tokens on this provider; Codex
	// is told it, so it compacts before the provider refuses.
	ContextWindow int `json:"context_window"`
	// AutoCompactTokenLimit is when Codex compacts; 80% of the window when 0.
	AutoCompactTokenLimit int `json:"auto_compact_token_limit,omitempty"`
	// Efforts are the reasoning levels a build may pick for it; empty for a
	// model that takes none.
	Efforts []string `json:"efforts,omitempty"`
	// DefaultEffort is the level a build runs it at when its manifest names
	// none: one of Efforts, or empty to send none and leave it to the
	// provider.
	DefaultEffort string `json:"default_effort,omitempty"`
	// MaxConcurrentBuilds is how many builds may run on it at once, across
	// every profile that shares the provider's CodexHome; 1 when 0.
	MaxConcurrentBuilds int `json:"max_concurrent_builds,omitempty"`
}

// Fallback is the controller's own Codex sign-in, as the Codex profiles run it.
type Fallback struct {
	TaskModel    string `json:"task_model"`
	TaskEffort   string `json:"task_effort"`
	ReviewModel  string `json:"review_model,omitempty"`
	ReviewEffort string `json:"review_effort"`
}

// Clone answers a deep copy.
func (p *Provider) Clone() *Provider {
	if p == nil {
		return nil
	}
	clone := *p
	clone.Models = make([]Model, len(p.Models))
	for index, model := range p.Models {
		model.Efforts = slices.Clone(model.Efforts)
		clone.Models[index] = model
	}
	if p.Fallback != nil {
		fallback := *p.Fallback
		clone.Fallback = &fallback
	}
	return &clone
}

// ValidModelName reports whether name can name a provider model.
func ValidModelName(name string) bool { return modelNamePattern.MatchString(name) }

// Validate checks a provider as a manifest gives it. It reads nothing.
func (p Provider) Validate() error {
	if !providerIDPattern.MatchString(p.ID) {
		return errors.New("executor.provider.id must be a short lower-case name")
	}
	if err := validBaseURL(p.BaseURL); err != nil {
		return fmt.Errorf("executor.provider.base_url %w", err)
	}
	if !cleanAbsolute(p.KeyFile) {
		return errors.New("executor.provider.key_file must be an absolute, clean path")
	}
	switch p.WireAPI {
	case WireResponses:
	case "chat":
		return errors.New(`executor.provider.wire_api "chat" is refused: Codex speaks only the Responses API to a provider (it removed Chat Completions); use a provider's /v1/responses, or a Responses bridge on a loopback address`)
	default:
		return errors.New(`executor.provider.wire_api must be "responses"`)
	}
	if !cleanAbsolute(p.CodexHome) || p.CodexHome == "/" {
		return errors.New("executor.provider.codex_home must be an absolute, clean folder")
	}
	if len(p.Models) == 0 || len(p.Models) > MaxModels {
		return fmt.Errorf("executor.provider.models must list 1 to %d models", MaxModels)
	}
	names, ids := map[string]bool{}, map[string]bool{}
	for index, model := range p.Models {
		if err := model.validate(); err != nil {
			return fmt.Errorf("executor.provider.models[%d] %w", index, err)
		}
		if names[model.Name] || ids[model.ID] {
			return fmt.Errorf("executor.provider.models[%d] repeats a name or an id", index)
		}
		names[model.Name], ids[model.ID] = true, true
	}
	if _, err := p.queueTimeout(); err != nil {
		return err
	}
	switch p.OnUnavailable {
	case "", UnavailableWait:
		if p.Fallback != nil {
			return errors.New(`executor.provider.fallback is used only with on_unavailable "fallback"`)
		}
	case UnavailableFallback:
		if p.Fallback == nil {
			return errors.New(`executor.provider.on_unavailable "fallback" needs executor.provider.fallback`)
		}
		if err := p.Fallback.validate(); err != nil {
			return err
		}
	default:
		return errors.New(`executor.provider.on_unavailable must be "wait" or "fallback"`)
	}
	for field, value := range map[string]int{"request_max_retries": p.RequestMaxRetries, "stream_max_retries": p.StreamMaxRetries} {
		if value < 0 || value > 50 {
			return fmt.Errorf("executor.provider.%s must be 0 to 50", field)
		}
	}
	return nil
}

func (m Model) validate() error {
	if !modelNamePattern.MatchString(m.Name) {
		return errors.New("name must be a lower-case model name")
	}
	if !modelIDPattern.MatchString(m.ID) {
		return errors.New("id must be the provider's model id (no ':' or quotes)")
	}
	if !originPattern.MatchString(m.Origin) {
		return errors.New("origin must be a two-letter country code")
	}
	if m.ContextWindow < 8192 || m.ContextWindow > 4_000_000 {
		return errors.New("context_window must be 8192 to 4000000 tokens")
	}
	if m.AutoCompactTokenLimit < 0 || (m.AutoCompactTokenLimit != 0 && (m.AutoCompactTokenLimit < 4096 || m.AutoCompactTokenLimit >= m.ContextWindow)) {
		return errors.New("auto_compact_token_limit must be at least 4096 and below context_window")
	}
	if len(m.Efforts) > len(Efforts) {
		return errors.New("efforts repeats a level")
	}
	seen := map[string]bool{}
	for _, effort := range m.Efforts {
		if !slices.Contains(Efforts, effort) || seen[effort] {
			return errors.New("efforts must be distinct levels of low, medium, high and xhigh")
		}
		seen[effort] = true
	}
	if m.DefaultEffort != "" && !seen[m.DefaultEffort] {
		return errors.New("default_effort must be one of efforts")
	}
	if m.MaxConcurrentBuilds < 0 || m.MaxConcurrentBuilds > 8 {
		return errors.New("max_concurrent_builds must be 1 to 8")
	}
	return nil
}

func (f Fallback) validate() error {
	if !modelNamePattern.MatchString(f.TaskModel) || (f.ReviewModel != "" && !modelNamePattern.MatchString(f.ReviewModel)) {
		return errors.New("executor.provider.fallback must name the controller's Codex models by name")
	}
	if f.TaskEffort != "xhigh" || f.ReviewEffort != "xhigh" {
		return errors.New("executor.provider.fallback runs Codex on the controller's sign-in, at xhigh only")
	}
	return nil
}

// Model answers the model of that name.
func (p Provider) Model(name string) (Model, bool) {
	for _, model := range p.Models {
		if model.Name == name {
			return model, true
		}
	}
	return Model{}, false
}

// Accepts reports whether a manifest may name that level for the model: one
// it lists, or none, which runs it at its DefaultEffort.
func (m Model) Accepts(effort string) bool {
	return effort == "" || slices.Contains(m.Efforts, effort)
}

// Effort answers the level a build sends for the model: the manifest's, else
// the model's default; empty sends none.
func (m Model) Effort(manifest string) string {
	if manifest != "" {
		return manifest
	}
	return m.DefaultEffort
}

// Slots answers how many builds may run on the model at once.
func (m Model) Slots() int {
	if m.MaxConcurrentBuilds == 0 {
		return DefaultMaxConcurrentBuilds
	}
	return m.MaxConcurrentBuilds
}

// CompactAt answers the token count at which Codex compacts.
func (m Model) CompactAt() int {
	if m.AutoCompactTokenLimit != 0 {
		return m.AutoCompactTokenLimit
	}
	return m.ContextWindow * 8 / 10
}

// EnvKey is the one variable that carries the provider's key to Codex. It
// holds "KEY", so Codex's default shell policy keeps it from the commands the
// model runs, and the generated configuration excludes it by name as well.
func (p Provider) EnvKey() string {
	return strings.ToUpper(strings.ReplaceAll(p.ID, "-", "_")) + "_API_KEY"
}

// CodexProviderID is the id the generated configuration gives the provider.
func (p Provider) CodexProviderID() string { return "abcp-" + p.ID }

// StartTimeout answers how long a build may wait to start: queue_timeout, or
// DefaultQueueTimeout.
func (p Provider) StartTimeout() time.Duration {
	timeout, _ := p.queueTimeout()
	return timeout
}

func (p Provider) queueTimeout() (time.Duration, error) {
	if p.QueueTimeout == "" {
		return DefaultQueueTimeout, nil
	}
	timeout, err := time.ParseDuration(p.QueueTimeout)
	if err != nil || timeout < time.Second || timeout > 2*time.Hour {
		return 0, errors.New("executor.provider.queue_timeout must be a duration from 1s to 2h")
	}
	return timeout, nil
}

func (p Provider) requestMaxRetries() int {
	if p.RequestMaxRetries == 0 {
		return DefaultRequestMaxRetries
	}
	return p.RequestMaxRetries
}

func (p Provider) streamMaxRetries() int {
	if p.StreamMaxRetries == 0 {
		return DefaultStreamMaxRetries
	}
	return p.StreamMaxRetries
}

// CodexConfig answers the config.toml the controller writes into the model's
// Codex home. Ralphex adds its own -c overrides on top (the model, the
// reasoning level, multi-agent review and the stream timeout); this file
// gives the provider, the model's context, and keeps the key from the
// commands Codex runs. Every value in it has been validated, so it is quoted
// as a TOML basic string without escapes.
func CodexConfig(p Provider, m Model) []byte {
	provider := p.CodexProviderID()
	var b strings.Builder
	b.WriteString("# Written by ABCP for the open-model engine " + p.ID + "/" + m.Name + " (Repo C design note OPEN_MODEL_ENGINES).\n")
	b.WriteString("# ABCP rewrites this file before each build on it; edit the profile's manifest instead.\n")
	b.WriteString("model = " + quote(m.ID) + "\n")
	b.WriteString("model_provider = " + quote(provider) + "\n")
	b.WriteString("model_context_window = " + strconv.Itoa(m.ContextWindow) + "\n")
	b.WriteString("model_auto_compact_token_limit = " + strconv.Itoa(m.CompactAt()) + "\n")
	b.WriteString("model_reasoning_summary = \"none\"\n")
	b.WriteString("web_search = \"disabled\"\n")
	b.WriteString("\n[model_providers." + provider + "]\n")
	b.WriteString("name = " + quote(p.ID+" (ABCP open-model engine)") + "\n")
	b.WriteString("base_url = " + quote(p.BaseURL) + "\n")
	b.WriteString("env_key = " + quote(p.EnvKey()) + "\n")
	b.WriteString("wire_api = " + quote(p.WireAPI) + "\n")
	b.WriteString("requires_openai_auth = false\n")
	b.WriteString("supports_websockets = false\n")
	b.WriteString("request_max_retries = " + strconv.Itoa(p.requestMaxRetries()) + "\n")
	b.WriteString("stream_max_retries = " + strconv.Itoa(p.streamMaxRetries()) + "\n")
	b.WriteString("\n[model_providers." + provider + ".capabilities]\n")
	b.WriteString("remote_compaction = \"unsupported\"\n")
	b.WriteString("\n[shell_environment_policy]\n")
	b.WriteString("ignore_default_excludes = false\n")
	b.WriteString("exclude = [" + quote(p.EnvKey()) + "]\n")
	return []byte(b.String())
}

// HomeFor answers the model's Codex home for that configuration:
// <codex_home>/<model>-<first 12 hex of the configuration's SHA-256>. A
// changed configuration gets a home of its own, so builds that overlap never
// rewrite each other's file, and the usage collector reads every home under
// codex_home (sessions sit six levels below it).
func HomeFor(p Provider, m Model, config []byte) string {
	sum := sha256.Sum256(config)
	return filepath.Join(p.CodexHome, m.Name+"-"+hex.EncodeToString(sum[:])[:12])
}

// SlotDir answers the folder of the model's build slots.
func SlotDir(p Provider, m Model) string { return filepath.Join(p.CodexHome, "slots", m.Name) }

// Environment answers base without the inherited OpenAI and Codex settings,
// which an open-model build must not carry, and with settings appended in name
// order.
func Environment(base []string, settings map[string]string) []string {
	dropped := map[string]bool{
		"CODEX_HOME": true, "OPENAI_API_KEY": true, "OPENAI_BASE_URL": true, "OPENAI_ORG_ID": true, "OPENAI_PROJECT_ID": true,
		"AZURE_OPENAI_API_KEY": true, "AZURE_OPENAI_ENDPOINT": true,
	}
	out := make([]string, 0, len(base)+len(settings))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := settings[key]; !replaced && !dropped[key] {
			out = append(out, entry)
		}
	}
	keys := make([]string, 0, len(settings))
	for key := range settings {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		out = append(out, key+"="+settings[key])
	}
	return out
}

func quote(value string) string { return `"` + value + `"` }

func cleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && len(path) <= 4096 && !strings.ContainsAny(path, "\x00\"\\\n\r")
}

// validBaseURL accepts https://host[:port][/path] without credentials, query,
// fragment or a trailing slash, and http only on a loopback address.
func validBaseURL(raw string) error {
	if raw == "" || len(raw) > 512 || strings.ContainsAny(raw, "\"\\ \t\r\n") {
		return errors.New("must be an https URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return errors.New("must be an https URL without credentials, query or fragment")
	}
	if strings.HasSuffix(raw, "/") {
		return errors.New("must not end in a slash")
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if Loopback(parsed.Hostname()) {
			return nil
		}
		return errors.New("must use https unless it is a loopback address")
	}
	return errors.New("must be an https URL")
}

// Loopback reports whether host is a loopback address or localhost.
func Loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

package serviceapi

import (
	"context"
	"net/http"
)

// EngineV1 is one admission profile's build engine as Repo C's AI and API keys
// page shows it (Repo C design note AI_KEYS_AND_MODELS §9.5): the executor, the
// task and review model and reasoning its manifest names, its engine lane if
// it has one, and whether its credentials are in place. No setting's value is
// part of it, only a lane's model name.
type EngineV1 struct {
	ProfileID    string `json:"profile_id"`
	Executor     string `json:"executor"`
	TaskModel    string `json:"task_model,omitempty"`
	TaskEffort   string `json:"task_effort,omitempty"`
	ReviewModel  string `json:"review_model,omitempty"`
	ReviewEffort string `json:"review_effort,omitempty"`
	// Lane names the profile's engine lane; LaneModel is the model its file
	// names, when it names one. Both are empty for a profile on the
	// controller's own sign-in.
	Lane      string `json:"lane,omitempty"`
	LaneModel string `json:"lane_model,omitempty"`
	// Access is "sign-in" (the controller's own Codex or Claude sign-in) or
	// "lane". Ready says its credentials are in place; it does not say the
	// vendor accepts them. Reason says why not: "lane_unreadable",
	// "lane_no_key" or "not_signed_in".
	Access string `json:"access"`
	Ready  bool   `json:"ready"`
	Reason string `json:"reason,omitempty"`
	// Choices are the models and reasoning levels a run admission may pick for
	// this profile's task (step 4b); omitted when it offers none.
	Choices *EngineChoicesV1 `json:"choices,omitempty"`
	// Provider describes an open-model engine (Repo C design note
	// OPEN_MODEL_ENGINES): Codex on an OpenAI-compatible provider, whose id is
	// also the profile's lane. Omitted for every other profile.
	Provider *EngineProviderV1 `json:"provider,omitempty"`
}

// EngineProviderV1 is an open-model engine's provider, as the profile's
// manifest names it. It carries no key and no file path.
type EngineProviderV1 struct {
	ID            string                  `json:"id"`
	BaseURL       string                  `json:"base_url"`
	WireAPI       string                  `json:"wire_api"`
	OnUnavailable string                  `json:"on_unavailable"`
	Models        []EngineProviderModelV1 `json:"models"`
	// Check is the provider's own word on the key and the task's model, from
	// its /models (cached for minutes), when the manifest asks for it. It
	// never changes Ready, which says only that the key is in place.
	Check *EngineProviderCheckV1 `json:"check,omitempty"`
}

// EngineProviderModelV1 is one model an open-model engine may run.
type EngineProviderModelV1 struct {
	Name                string   `json:"name"`
	ID                  string   `json:"id"`
	Origin              string   `json:"origin"`
	ContextWindow       int      `json:"context_window"`
	Efforts             []string `json:"efforts,omitempty"`
	DefaultEffort       string   `json:"default_effort,omitempty"`
	MaxConcurrentBuilds int      `json:"max_concurrent_builds"`
}

// EngineProviderCheckV1 is one answer of a provider's /models: "ok",
// "key_refused", "model_missing" or "unavailable".
type EngineProviderCheckV1 struct {
	Status     string `json:"status"`
	HTTPStatus int    `json:"http_status,omitempty"`
	CheckedAt  string `json:"checked_at,omitempty"`
}

// EngineChoicesV1 is a profile's allowlist, as its configuration lists it.
type EngineChoicesV1 struct {
	Models  []string `json:"models,omitempty"`
	Efforts []string `json:"efforts,omitempty"`
}

// EnginesV1 lists every admission profile's engine, in profile order.
type EnginesV1 struct {
	SchemaVersion string     `json:"schema_version"`
	Engines       []EngineV1 `json:"engines"`
}

// EngineCatalog answers the engines; it reads lane files and sign-in files'
// presence on each call, so a changed key or sign-in shows at once.
type EngineCatalog interface {
	Engines(context.Context) (EnginesV1, error)
}

func (s *Server) engines(writer http.ResponseWriter, request *http.Request, requestID string) {
	if request.Method != http.MethodGet || !requestBodyEmpty(request) || request.URL.RawQuery != "" {
		s.writeError(writer, http.StatusBadRequest, ErrorV1{Code: "invalid_request", Message: "invalid engines request", RequestID: requestID})
		return
	}
	if s.reserved.Engines == nil {
		s.writeError(writer, http.StatusNotImplemented, ErrorV1{Code: "unsupported_capability", Message: "engines are not available", RequestID: requestID})
		return
	}
	response, err := s.reserved.Engines.Engines(request.Context())
	if err != nil {
		s.writeError(writer, http.StatusServiceUnavailable, ErrorV1{Code: "engines_unavailable", Message: "engines are unavailable", RequestID: requestID, Retryable: true})
		return
	}
	s.writeJSON(writer, http.StatusOK, response)
}

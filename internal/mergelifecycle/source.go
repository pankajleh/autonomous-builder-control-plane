package mergelifecycle

import (
	"context"
	"errors"
	"sync"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

// StaticAuthoritySource is a controller configuration snapshot suitable for
// network-free composition and deterministic tests. Construction copies all
// slice-backed policy and evidence fields; Resolve never trusts request data
// beyond selecting an existing run identity.
type StaticAuthoritySource struct {
	mu    sync.RWMutex
	byRun map[string]GovernedAuthority
}

func NewStaticAuthoritySource(values ...GovernedAuthority) (*StaticAuthoritySource, error) {
	result := &StaticAuthoritySource{byRun: make(map[string]GovernedAuthority, len(values))}
	for _, value := range values {
		if value.Phase3Authority.RunID() == "" || value.Phase3Authority.SHA256() == "" {
			return nil, errors.New("static merge authority is incomplete")
		}
		runID := value.Phase3Authority.RunID()
		if _, exists := result.byRun[runID]; exists {
			return nil, errors.New("static merge authority run is duplicated")
		}
		result.byRun[runID] = cloneGovernedAuthority(value)
	}
	if len(result.byRun) == 0 {
		return nil, errors.New("at least one static merge authority is required")
	}
	return result, nil
}

func (s *StaticAuthoritySource) Resolve(ctx context.Context, runID string) (GovernedAuthority, error) {
	if ctx == nil {
		return GovernedAuthority{}, errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return GovernedAuthority{}, err
	}
	s.mu.RLock()
	value, ok := s.byRun[runID]
	s.mu.RUnlock()
	if !ok {
		return GovernedAuthority{}, errors.New("run has no controller merge authority")
	}
	return cloneGovernedAuthority(value), nil
}

func cloneGovernedAuthority(value GovernedAuthority) GovernedAuthority {
	value.AcceptedSources = append([]githublifecycle.AcceptedSourceCandidateV1(nil), value.AcceptedSources...)
	for index := range value.AcceptedSources {
		value.AcceptedSources[index].AcceptanceEvidence = append([]ledger.EvidenceRef(nil), value.AcceptedSources[index].AcceptanceEvidence...)
	}
	value.Policy.RequiredChecks = append([]githublifecycle.TrustedCheckIdentityV1(nil), value.Policy.RequiredChecks...)
	for index := range value.Policy.RequiredChecks {
		if value.Policy.RequiredChecks[index].App != nil {
			app := *value.Policy.RequiredChecks[index].App
			value.Policy.RequiredChecks[index].App = &app
		}
	}
	value.Policy.EligibleReviewers = append([]githublifecycle.StableIdentityV1(nil), value.Policy.EligibleReviewers...)
	value.Policy.RequiredReviewers = append([]githublifecycle.StableIdentityV1(nil), value.Policy.RequiredReviewers...)
	value.EvidenceClosure = append([]ledger.EvidenceRef(nil), value.EvidenceClosure...)
	return value
}

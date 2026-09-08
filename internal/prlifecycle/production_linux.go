//go:build linux

package prlifecycle

import (
	"errors"
	"os"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

// The production admission identity is captured once at process startup. It
// cannot be supplied by a plan, run, request, plugin, or ControllerConfig.
var productionAdmissionRoot = os.Getenv("ABCP_PR_ADMISSION_ROOT")

type ProductionControllerConfig struct {
	GitHub              *GitHubAdapter
	Artifacts           ArtifactWriter
	AuthoritativeLedger *ledger.JSONLLedger
	Now                 func() time.Time
}

// NewProductionController is the only exported production construction path.
// Test injection remains behind unexported constructors in this package.
func NewProductionController(config ProductionControllerConfig) (*Controller, error) {
	if productionAdmissionRoot == "" {
		return nil, errors.New("controller host admission root was not configured at process startup")
	}
	store, err := newPRWriteAdmissionStore(productionAdmissionRoot)
	if err != nil {
		return nil, err
	}
	recorder, err := NewMaterialLedgerRecorder(config.AuthoritativeLedger)
	if err != nil {
		return nil, err
	}
	return newController(ControllerConfig{Store: store, GitHub: config.GitHub, Artifacts: config.Artifacts, Ledger: recorder, Now: config.Now})
}

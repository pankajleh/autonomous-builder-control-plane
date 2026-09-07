//go:build !linux

package prlifecycle

import (
	"context"
	"errors"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

var errUnsupportedLockSemantics = errors.New("PR lifecycle requires verified Linux flock and no-follow semantics")

type PRWriteAdmissionStore struct{}

func NewPRWriteAdmissionStore(string) (*PRWriteAdmissionStore, error) {
	return nil, errUnsupportedLockSemantics
}
func (*PRWriteAdmissionStore) Root() string { return "" }

type MaterialLedgerRecorder struct{}

func NewMaterialLedgerRecorder(string, *ledger.JSONLLedger) (*MaterialLedgerRecorder, error) {
	return nil, errUnsupportedLockSemantics
}
func (*MaterialLedgerRecorder) Record(ledger.Event, []byte) error { return errUnsupportedLockSemantics }

type ArtifactWriter interface {
	RunID() string
	RunDir() string
	WriteBytes(name, kind string, data []byte) (ledger.EvidenceRef, error)
}

type ControllerConfig struct {
	Store     *PRWriteAdmissionStore
	GitHub    *GitHubAdapter
	Artifacts ArtifactWriter
	Ledger    *MaterialLedgerRecorder
	Now       func() time.Time
}
type Controller struct{}

func NewController(ControllerConfig) (*Controller, error) { return nil, errUnsupportedLockSemantics }

type Request struct {
	RunID     string
	Authority githublifecycle.Authority
	Title     string
	Body      string
}

func (*Controller) Upsert(context.Context, Request) (PRLifecycleResultV1, error) {
	return PRLifecycleResultV1{}, errUnsupportedLockSemantics
}

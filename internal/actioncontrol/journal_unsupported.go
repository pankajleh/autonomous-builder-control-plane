//go:build !linux

package actioncontrol

import (
	"context"
	"time"
)

type Journal struct{}
type DecisionEffectLease struct{}

func Open(string) (*Journal, error)                            { return nil, ErrUnsupported }
func OpenJournal(root string) (*Journal, error)                { return Open(root) }
func OpenWithClock(string, func() time.Time) (*Journal, error) { return nil, ErrUnsupported }
func (*Journal) Close() error                                  { return nil }
func (*Journal) CreateReceipt(context.Context, ReceiptInput, func(uint64) (AdmissionBinding, error)) (ActionReceiptV1, bool, error) {
	return ActionReceiptV1{}, false, ErrUnsupported
}
func (*Journal) CreateClaim(context.Context, string, string, string, string) (ActionClaimV1, bool, error) {
	return ActionClaimV1{}, false, ErrUnsupported
}
func (*Journal) AppendOutcome(context.Context, string, string, OutcomeStatus, []string, string) (ActionOutcomeV1, error) {
	return ActionOutcomeV1{}, ErrUnsupported
}
func (*Journal) Read(context.Context, string, string) (Operation, error) {
	return Operation{}, ErrUnsupported
}
func (*Journal) ReadByRequest(context.Context, string, string, string) (Operation, error) {
	return Operation{}, ErrUnsupported
}
func (*Journal) ReplayReceipt(context.Context, ReceiptReplayInput) (Operation, error) {
	return Operation{}, ErrUnsupported
}
func (*Journal) AcquireDecisionEffect(context.Context, string, string) (*DecisionEffectLease, Operation, error) {
	return nil, Operation{}, ErrUnsupported
}
func (*DecisionEffectLease) Operation(context.Context) (Operation, error) {
	return Operation{}, ErrUnsupported
}
func (*DecisionEffectLease) Revalidate() error { return ErrUnsupported }
func (*DecisionEffectLease) Close() error      { return nil }
func (*Journal) ReadByDecisionRequest(context.Context, string, string) (Operation, error) {
	return Operation{}, ErrUnsupported
}
func (*Journal) OperationsForLease(context.Context, string, string, *uint64) ([]Operation, error) {
	return nil, ErrUnsupported
}
func (*Journal) acquire(context.Context, string) (*journalGuard, error) { return nil, ErrUnsupported }

type journalGuard struct{}

func (*journalGuard) close() error                 { return nil }
func (*journalGuard) scan() (*journalState, error) { return nil, ErrUnsupported }

type journalState struct{ sequence uint64 }

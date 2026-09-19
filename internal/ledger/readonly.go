package ledger

import "errors"

// ReadOnlyJSONLLedger is the restricted service-side view of an existing
// authoritative ledger. It deliberately exposes no append or lock methods.
type ReadOnlyJSONLLedger struct {
	ledger *JSONLLedger
}

// OpenExistingReadOnlyJSONLLedger opens and pins an existing parent and
// ledger without creating or modifying any filesystem object.
func OpenExistingReadOnlyJSONLLedger(path string) (*ReadOnlyJSONLLedger, error) {
	value, err := openJSONLLedger(path, false, true, nil, nil)
	if err != nil {
		return nil, err
	}
	return &ReadOnlyJSONLLedger{ledger: value}, nil
}

// OpenRegisteredReadOnlyJSONLLedger opens a restricted reader only when the
// named parent and file match immutable catalog authority.
func OpenRegisteredReadOnlyJSONLLedger(path string, generation PhysicalGeneration) (*ReadOnlyJSONLLedger, error) {
	value, err := openJSONLLedger(path, false, true, nil, &generation)
	if err != nil {
		return nil, errors.Join(ErrRegisteredGenerationChanged, err)
	}
	return &ReadOnlyJSONLLedger{ledger: value}, nil
}

// Snapshot returns one bounded image and the pinned physical identity.
func (l *ReadOnlyJSONLLedger) Snapshot() ([]byte, string, error) {
	if l == nil || l.ledger == nil {
		return nil, "", errors.New("read-only ledger is required")
	}
	return l.ledger.Snapshot()
}

// PhysicalIdentity returns the verified pinned physical identity.
func (l *ReadOnlyJSONLLedger) PhysicalIdentity() (string, error) {
	if l == nil || l.ledger == nil {
		return "", errors.New("read-only ledger is required")
	}
	return l.ledger.PhysicalIdentity()
}

// PhysicalGeneration returns the verified pinned parent/file generation.
func (l *ReadOnlyJSONLLedger) PhysicalGeneration() (PhysicalGeneration, error) {
	if l == nil || l.ledger == nil {
		return PhysicalGeneration{}, errors.New("read-only ledger is required")
	}
	return l.ledger.PhysicalGeneration()
}

// Close is idempotent and permanently closes this view.
func (l *ReadOnlyJSONLLedger) Close() error {
	if l == nil || l.ledger == nil {
		return nil
	}
	return l.ledger.Close()
}

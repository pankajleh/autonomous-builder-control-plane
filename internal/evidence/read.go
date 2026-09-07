package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

// ReadVerifiedLocal securely reads one bounded local artifact beneath the
// controller-owned evidence root and verifies its exact SHA256 digest.
func ReadVerifiedLocal(root string, ref ledger.EvidenceRef, maximumBytes int64) ([]byte, error) {
	if maximumBytes < 1 {
		return nil, errors.New("positive evidence artifact size bound is required")
	}
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, errors.New("evidence root must be an absolute clean path")
	}
	if ref.URI == "" || !filepath.IsAbs(ref.URI) || filepath.Clean(ref.URI) != ref.URI || ref.Kind == "" {
		return nil, errors.New("evidence reference must contain an absolute clean path and kind")
	}
	decoded, err := hex.DecodeString(ref.SHA256)
	if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != ref.SHA256 {
		return nil, errors.New("evidence reference must contain a complete lowercase SHA256 digest")
	}
	relative, err := filepath.Rel(root, ref.URI)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, errors.New("evidence artifact is outside the controller-owned evidence root")
	}
	data, err := readBoundedLocal(root, ref.URI, maximumBytes)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != ref.SHA256 {
		return nil, fmt.Errorf("evidence SHA256 mismatch")
	}
	return data, nil
}

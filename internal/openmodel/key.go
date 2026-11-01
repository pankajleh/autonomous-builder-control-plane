package openmodel

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/enginelane"
)

// Key is a provider key read from its file. It prints as a placeholder, so a
// key that reaches a log line or an error by mistake shows nothing.
type Key struct {
	value string
	sum   string
}

// envLine is a NAME=value line: a lane file's shape, not a key file's.
var envLine = regexp.MustCompile(`^[A-Z][A-Z0-9_]*=`)

// ErrKeyMissing says the key file is not there yet: the engine is not ready.
var ErrKeyMissing = errors.New("the provider's key file is not there")

// String hides the key.
func (Key) String() string { return "[provider key]" }

// GoString hides the key from %#v.
func (Key) GoString() string { return "openmodel.Key{[provider key]}" }

// SHA256 answers the SHA-256 of the key file, as engine lanes record theirs:
// a changed key shows as a changed digest; the key cannot be read from it.
func (k Key) SHA256() string { return k.sum }

// Value answers the key itself, only for the one environment variable Codex
// reads.
func (k Key) Value() string { return k.value }

// ReadKey reads a key file under the engine lanes' rules (a regular file owned
// by the controller's user, mode 0600 or 0400, no link anywhere in its path)
// holding one line: the key alone, 16 to 512 printable characters without
// spaces.
// Errors never carry the file's content.
func ReadKey(path string) (Key, error) {
	data, err := enginelane.ReadPrivate(path, MaxKeyBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Key{}, ErrKeyMissing
		}
		return Key{}, fmt.Errorf("the provider's key file cannot be used: %w", err)
	}
	value := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if len(value) < 16 || len(value) > 512 {
		return Key{}, errors.New("the provider's key file must hold one line: the key")
	}
	if envLine.MatchString(value) {
		return Key{}, errors.New("the provider's key file must hold the key alone, not NAME=value")
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x21 || value[index] > 0x7e {
			return Key{}, errors.New("the provider's key file must hold one line: the key")
		}
	}
	sum := sha256.Sum256(data)
	return Key{value: value, sum: hex.EncodeToString(sum[:])}, nil
}

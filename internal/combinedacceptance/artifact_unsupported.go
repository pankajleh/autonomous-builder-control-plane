//go:build !linux

package combinedacceptance

import (
	"errors"
	"os"
)

func openArtifactNoSymlinks(string) (*os.File, error) {
	return nil, errors.New("secure local evidence artifact reads are unsupported on this platform")
}

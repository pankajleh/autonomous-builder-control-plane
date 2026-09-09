//go:build !linux

package ledger

import (
	"errors"
	"os"
	"time"
)

func lockLedgerFile(*os.File, time.Duration) error {
	return errors.New("authoritative ledger file serialization is unsupported on this platform")
}

func unlockLedgerFile(*os.File) error { return nil }

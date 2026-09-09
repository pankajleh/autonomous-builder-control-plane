//go:build linux

package ledger

import (
	"errors"
	"os"
	"syscall"
	"time"
)

func lockLedgerFile(file *os.File, wait time.Duration) error {
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EINTR) {
			return err
		}
		if wait <= 0 || !time.Now().Before(deadline) {
			return errors.New("authoritative ledger file lock is busy")
		}
		time.Sleep(time.Millisecond)
	}
}

func unlockLedgerFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

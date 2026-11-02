//go:build linux

package openmodel

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// WriteHome makes the model's Codex home (and the provider's folder above it)
// private folders of the controller's user, created 0700 when missing, and
// writes config.toml there (0600) unless it already holds exactly config. A
// link, a folder another user owns or others may open, or a config.toml that
// is not a regular file stops the build.
func WriteHome(root, home string, config []byte) error {
	return WriteHomeFiles(root, home, HomeFile{Name: "config.toml", Data: config})
}

// HomeFile is one file the controller writes into a model's Codex home.
type HomeFile struct {
	Name string
	Data []byte
}

// WriteHomeFiles is WriteHome for several files, each written as config.toml
// is, in the order given (config.toml last, so it never names a catalog that
// is not there yet).
func WriteHomeFiles(root, home string, files ...HomeFile) error {
	if filepath.Dir(home) != root {
		return errors.New("the model's Codex home is not directly in the provider's folder")
	}
	for _, dir := range []string{root, home} {
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("create the Codex home: %w", err)
		}
		if err := privateDirectory(dir); err != nil {
			return err
		}
	}
	for _, file := range files {
		if file.Name == "" || filepath.Base(file.Name) != file.Name || file.Name[0] == '.' {
			return errors.New("a Codex home file must be a plain name")
		}
		if err := writeHomeFile(home, file.Name, file.Data); err != nil {
			return err
		}
	}
	return nil
}

func writeHomeFile(home, name string, data []byte) error {
	path := filepath.Join(home, name)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("the Codex home's %s is not a regular file", name)
		}
		if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) && info.Mode().Perm()&0o077 == 0 {
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect the Codex home's %s: %w", name, err)
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	temporary := filepath.Join(home, "."+name+"."+hex.EncodeToString(suffix))
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("write the Codex home's %s: %w", name, err)
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		os.Remove(temporary)
		return fmt.Errorf("write the Codex home's %s: %w", name, err)
	}
	if err := os.Rename(temporary, path); err != nil {
		os.Remove(temporary)
		return fmt.Errorf("write the Codex home's %s: %w", name, err)
	}
	return nil
}

// RelayLogFile is the relay's log in a model's Codex home.
const RelayLogFile = "relay.log"

// MaxRelayLogBytes is when the relay's log moves to relay.log.1 (the one
// before it is dropped).
const MaxRelayLogBytes = 4 << 20

// relayLog appends the relay's lines to <home>/relay.log (0600, never through
// a link), moving it to relay.log.1 once it passes MaxRelayLogBytes.
type relayLog struct {
	mu   sync.Mutex
	path string
	file *os.File
	size int64
}

func openRelayLog(home string) (*relayLog, error) {
	log := &relayLog{path: filepath.Join(home, RelayLogFile)}
	if err := log.open(); err != nil {
		return nil, err
	}
	return log, nil
}

func (l *relayLog) open() error {
	file, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("open the relay's log: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		file.Close()
		return errors.New("the relay's log is not a private regular file")
	}
	l.file, l.size = file, info.Size()
	return nil
}

func (l *relayLog) Write(line []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return 0, os.ErrClosed
	}
	if l.size+int64(len(line)) > MaxRelayLogBytes {
		l.file.Close()
		l.file = nil
		if err := os.Rename(l.path, l.path+".1"); err != nil {
			return 0, err
		}
		if err := l.open(); err != nil {
			return 0, err
		}
	}
	n, err := l.file.Write(line)
	l.size += int64(n)
	return n, err
}

func (l *relayLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}

// privateDirectory requires a folder, not a link, owned by this user and
// closed to everyone else.
func privateDirectory(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect the Codex home: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is a link or not a folder", dir)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("%s is not owned by the controller's user", dir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s is open to others: it must be mode 0700", dir)
	}
	return nil
}

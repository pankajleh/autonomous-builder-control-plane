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
	"syscall"
)

// WriteHome makes the model's Codex home (and the provider's folder above it)
// private folders of the controller's user, created 0700 when missing, and
// writes config.toml there (0600) unless it already holds exactly config. A
// link, a folder another user owns or others may open, or a config.toml that
// is not a regular file stops the build.
func WriteHome(root, home string, config []byte) error {
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
	path := filepath.Join(home, "config.toml")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("the Codex home's config.toml is not a regular file")
		}
		if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, config) && info.Mode().Perm()&0o077 == 0 {
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect the Codex home's config.toml: %w", err)
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		return err
	}
	temporary := filepath.Join(home, ".config.toml."+hex.EncodeToString(suffix))
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("write the Codex home's config.toml: %w", err)
	}
	_, writeErr := file.Write(config)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		os.Remove(temporary)
		return fmt.Errorf("write the Codex home's config.toml: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		os.Remove(temporary)
		return fmt.Errorf("write the Codex home's config.toml: %w", err)
	}
	return nil
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

//go:build linux

package lanekey

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Seams for the tests of a write that fails part way; the live ones otherwise.
var (
	syncFile   = syscall.Fsync
	renameFile = syscall.Renameat
)

// openFolder opens the key file's folder one component at a time without following a link, and answers it only when
// it is a folder of the controller's user that no one else may write in.
func openFolder(path string) (int, error) {
	fd, err := syscall.Open(string(filepath.Separator), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return -1, errors.New("the root folder cannot be opened")
	}
	if path != string(filepath.Separator) {
		for _, component := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
			next, openErr := syscall.Openat(fd, component, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
			syscall.Close(fd)
			if openErr != nil {
				return -1, errors.New("the key file's folder is not there, or a folder above it is a symbolic link")
			}
			fd = next
		}
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		syscall.Close(fd)
		return -1, errors.New("the key file's folder cannot be read")
	}
	if int(stat.Uid) != os.Getuid() || stat.Mode&0o022 != 0 {
		syscall.Close(fd)
		return -1, errors.New("the key file's folder must be the controller user's, writable by that user alone")
	}
	return fd, nil
}

// entry says what is at name in the folder: nothing, a regular file or a symbolic link (which this write replaces;
// no build reads a link), or something else, which is refused.
func entry(folder int, name string) (exists bool, err error) {
	fd, openErr := syscall.Openat(folder, name, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	switch {
	case openErr == syscall.ENOENT:
		return false, nil
	case openErr == syscall.ELOOP:
		return true, nil
	case openErr != nil:
		return true, errors.New("the key file cannot be opened")
	}
	defer syscall.Close(fd)
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		return true, errors.New("the key file cannot be read")
	}
	if stat.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return true, errors.New("the key file's name holds a folder or another kind of file")
	}
	return true, nil
}

// writeKeyFile replaces path whole with data: a new file beside it (mode 0600, the controller's user, a name no one
// else may take), written and synced, then renamed over path and the folder synced. On any failure the new file is
// removed and path is as it was.
func writeKeyFile(path string, data []byte) (err error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return errors.New("the key file's path is not absolute and clean")
	}
	folder, err := openFolder(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer syscall.Close(folder)
	name := filepath.Base(path)
	if _, err := entry(folder, name); err != nil {
		return err
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return errors.New("no name for the new key file")
	}
	temporary := "." + name + ".abcp-" + hex.EncodeToString(random[:]) + ".tmp"
	fd, err := syscall.Openat(folder, temporary, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return errors.New("the new key file cannot be made")
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = syscall.Unlinkat(folder, temporary)
		}
	}()
	file := os.NewFile(uintptr(fd), "lane-key")
	defer file.Close()
	var stat syscall.Stat_t
	if err := syscall.Fchmod(fd, 0o600); err != nil || syscall.Fstat(fd, &stat) != nil || int(stat.Uid) != os.Getuid() || stat.Mode&0o7777 != 0o600 {
		return errors.New("the new key file is not the controller user's, mode 0600")
	}
	if _, err := file.Write(data); err != nil {
		return errors.New("the new key file cannot be written")
	}
	if err := syncFile(fd); err != nil {
		return errors.New("the new key file cannot be synced")
	}
	if err := renameFile(folder, temporary, folder, name); err != nil {
		return errors.New("the new key file cannot be renamed over the key file")
	}
	renamed = true
	if err := syscall.Fsync(folder); err != nil {
		return errors.New("the key file's folder cannot be synced")
	}
	return nil
}

// removeKeyFile removes path, a regular file or a symbolic link, and syncs its folder. It answers whether there was
// one.
func removeKeyFile(path string) (bool, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) {
		return false, errors.New("the key file's path is not absolute and clean")
	}
	folder, err := openFolder(filepath.Dir(path))
	if err != nil {
		return false, err
	}
	defer syscall.Close(folder)
	name := filepath.Base(path)
	exists, err := entry(folder, name)
	if err != nil || !exists {
		return false, err
	}
	if err := syscall.Unlinkat(folder, name); err != nil && err != syscall.ENOENT {
		return false, errors.New("the key file cannot be removed")
	}
	if err := syscall.Fsync(folder); err != nil {
		return true, errors.New("the key file's folder cannot be synced")
	}
	return true, nil
}

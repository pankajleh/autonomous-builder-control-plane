package stackjob

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	maxSourceFiles = 20_000
	maxSourceBytes = 300 << 20
)

var errSource = errors.New("stack job source refused")

// noFollow opens a file only if it is not a link, so nothing outside the source is read through one.
const noFollow = syscall.O_NOFOLLOW

// copySource copies the source folder into dst, which must not exist: regular files and folders only (links, devices
// and the like are left out, and the source itself may not be a link), never .git, within the file and byte limits.
// The source is only read.
func copySource(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil || !info.IsDir() {
		return errSource
	}
	if err := os.Mkdir(dst, 0o700); err != nil {
		return errSource
	}
	files, bytes := 0, int64(0)
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return errSource
		}
		rel, err := filepath.Rel(src, path)
		if err != nil || strings.HasPrefix(rel, "..") {
			return errSource
		}
		if rel == "." {
			return nil
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.Mkdir(target, 0o755)
		case d.Type().IsRegular():
			info, err := d.Info()
			if err != nil {
				return errSource
			}
			files++
			bytes += info.Size()
			if files > maxSourceFiles || bytes > maxSourceBytes {
				return errSource
			}
			// Keep the owner's execute bit, so a project's own scripts still run; nothing else of the mode.
			mode := os.FileMode(0o644)
			if info.Mode()&0o100 != 0 {
				mode = 0o755
			}
			return copyFile(path, target, mode)
		default:
			return nil
		}
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.OpenFile(src, os.O_RDONLY|noFollow, 0)
	if err != nil {
		return errSource
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return errSource
	}
	_, err = io.Copy(out, in)
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return errSource
	}
	return nil
}

// folderSize is the bytes of regular files under dir, stopping once past limit.
func folderSize(dir string, limit int64) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || total > limit {
			return filepath.SkipAll
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

// realDirs makes root/rel... real folders, checking each from root down: anything else at a name, a link above all, is
// removed first, so nothing a job left is followed. root itself is the controller's own.
func realDirs(root string, rel ...string) (string, error) {
	path := root
	for _, part := range rel {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		switch {
		case err == nil && info.IsDir():
			continue
		case err == nil:
			if os.Remove(path) != nil {
				return "", errSource
			}
		case !os.IsNotExist(err):
			return "", errSource
		}
		if os.Mkdir(path, 0o755) != nil {
			return "", errSource
		}
	}
	return path, nil
}

// writeReal writes name in dir (a real folder) afresh: whatever a job left there, a link included, is removed, never
// followed.
func writeReal(dir, name string, data []byte) error {
	path := filepath.Join(dir, name)
	if info, err := os.Lstat(path); err == nil {
		if info.IsDir() {
			err = os.RemoveAll(path)
		} else {
			err = os.Remove(path)
		}
		if err != nil {
			return errSource
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|noFollow, 0o644)
	if err != nil {
		return errSource
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return errSource
	}
	return nil
}

// realFile reports whether root/rel... is a regular file reached through real folders only.
func realFile(root string, rel ...string) bool {
	path := root
	for i, part := range rel {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if err != nil || (i < len(rel)-1 && !info.IsDir()) || (i == len(rel)-1 && !info.Mode().IsRegular()) {
			return false
		}
	}
	return len(rel) > 0
}

package activity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/strictjson"
)

const maxRuntimeEntries = 1024

type runtimeOwner struct {
	Kind, Namespace, Directory, Checksum string
}

// A private namespace and an exclusive namespace lock serialize runtime
// creation with reconciliation. Unmarked paths are never adopted or deleted.
func sidecarRuntimeNamespace(root string) (*os.File, *os.File, error) {
	parent, err := privateDirectory(root, false)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	parent.Close()
	dir, err := privateDirectory(filepath.Join(root, "activity-sidecars"), true)
	if err != nil {
		return nil, nil, ErrUnavailable
	}
	lock, err := openRegular(filepath.Join(dir.Name(), ".lock"), os.O_RDWR|os.O_CREATE, true)
	if err != nil {
		dir.Close()
		return nil, nil, ErrUnavailable
	}
	if err = lockFile(lock); err != nil {
		lock.Close()
		dir.Close()
		return nil, nil, err
	}
	return dir, lock, nil
}

func runtimeNamespaceID(dir *os.File) (string, error) {
	info, err := dir.Stat()
	if err != nil || physicalID(info) == "" {
		return "", ErrIntegrity
	}
	return identity(dir.Name(), physicalID(info)), nil
}

func reconcileSidecarRuntime(ctx context.Context, root string) error {
	ctx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()
	dir, lock, err := sidecarRuntimeNamespace(root)
	if err != nil {
		return err
	}
	defer dir.Close()
	defer lock.Close()
	return cleanSidecarRuntimes(ctx, dir)
}

func cleanSidecarRuntimes(ctx context.Context, dir *os.File) error {
	entries, err := dir.ReadDir(maxRuntimeEntries + 1)
	if err != nil && err != io.EOF || len(entries) > maxRuntimeEntries {
		return ErrExhausted
	}
	namespace, err := runtimeNamespaceID(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return ErrUnavailable
		}
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "sidecar-") {
			continue
		}
		path := filepath.Join(dir.Name(), entry.Name())
		runtimeDir, err := privateDirectory(path, false)
		if err != nil {
			continue
		}
		original, statErr := runtimeDir.Stat()
		runtimeDir.Close()
		if statErr != nil {
			continue
		}
		lease, err := openRegular(filepath.Join(path, "owner.json"), os.O_RDWR, true)
		if err != nil {
			continue
		}
		if lockFile(lease) != nil {
			lease.Close()
			continue // Either the controller or its child still owns this runtime.
		}
		data, readErr := io.ReadAll(io.LimitReader(lease, 1025))
		var owner runtimeOwner
		valid := readErr == nil && len(data) <= 1024 && strictjson.Decode(data, &owner) == nil
		checksum := owner.Checksum
		owner.Checksum = ""
		valid = valid && owner.Kind == "ActivitySidecarRuntimeV1" && owner.Namespace == namespace && owner.Directory == entry.Name() && checksum == jsonDigest(owner)
		current, statErr := os.Lstat(path)
		if valid && statErr == nil && os.SameFile(original, current) {
			// Bound the entire traversal, not just the number of runtime roots.
			err = removeBoundedRuntime(ctx, path)
		}
		lease.Close()
		if err != nil {
			return err
		}
	}
	return dir.Sync()
}

func removeBoundedRuntime(ctx context.Context, path string) error {
	paths := make([]string, 0)
	var collect func(string, int) error
	collect = func(name string, depth int) error {
		if ctx.Err() != nil || len(paths) >= 4096 || depth > 16 {
			return ErrExhausted
		}
		paths = append(paths, name)
		info, err := os.Lstat(name)
		if err != nil || !info.IsDir() {
			return err // Symlinks are removed as entries, never traversed.
		}
		dir, err := openDirectory(name)
		if err != nil {
			return err
		}
		entries, err := dir.ReadDir(4097 - len(paths))
		dir.Close()
		if err != nil && err != io.EOF || len(entries)+len(paths) > 4096 {
			return ErrExhausted
		}
		for _, entry := range entries {
			if err = collect(filepath.Join(name, entry.Name()), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := collect(path, 0); err != nil {
		return err
	}
	for i := len(paths) - 1; i >= 0; i-- {
		if ctx.Err() != nil {
			return ErrUnavailable
		}
		if err := os.Remove(paths[i]); err != nil {
			return err
		}
	}
	return nil
}

func newSidecarRuntime(ctx context.Context, root string) (string, *os.File, error) {
	dir, lock, err := sidecarRuntimeNamespace(root)
	if err != nil {
		return "", nil, err
	}
	defer dir.Close()
	defer lock.Close()
	if err = cleanSidecarRuntimes(ctx, dir); err != nil {
		return "", nil, err
	}
	namespace, err := runtimeNamespaceID(dir)
	if err != nil {
		return "", nil, err
	}
	path, err := os.MkdirTemp(dir.Name(), "sidecar-")
	if err != nil {
		return "", nil, err
	}
	lease, err := openRegular(filepath.Join(path, "owner.json"), os.O_RDWR|os.O_CREATE|os.O_EXCL, true)
	if err != nil {
		os.RemoveAll(path)
		return "", nil, err
	}
	owner := runtimeOwner{Kind: "ActivitySidecarRuntimeV1", Namespace: namespace, Directory: filepath.Base(path)}
	owner.Checksum = jsonDigest(owner)
	data, _ := json.Marshal(owner)
	if err = lockFile(lease); err == nil {
		_, err = lease.Write(data)
	}
	runtimeDir, openErr := privateDirectory(path, false)
	if openErr == nil {
		openErr = errors.Join(runtimeDir.Sync(), runtimeDir.Close())
	}
	if errors.Join(err, lease.Sync(), openErr, dir.Sync()) != nil {
		lease.Close()
		os.RemoveAll(path)
		return "", nil, ErrIntegrity
	}
	return path, lease, nil
}

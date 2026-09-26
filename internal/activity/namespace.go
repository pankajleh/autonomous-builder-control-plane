package activity

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/strictjson"
)

// Keep this witness beside the activity directory, in the protected service
// root. Loss of all activity files must not reset the bare SSE ordinal space.
// The witness also holds a lifetime lease that survives activity directory loss.
type namespaceWitness struct {
	Kind, Namespace, Checksum string
}

func openStoreNamespace(root string) (*os.File, *os.File, bool, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == "/" {
		return nil, nil, false, ErrIntegrity
	}
	parent, err := privateDirectory(filepath.Dir(root), false)
	if err != nil {
		return nil, nil, false, ErrUnavailable
	}
	defer parent.Close()
	path := root + ".namespace.json"
	witness, err := openRegular(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, true)
	fresh := err == nil
	if errors.Is(err, os.ErrExist) {
		witness, err = openRegular(path, os.O_RDWR, true)
	}
	if err != nil {
		return nil, nil, false, ErrIntegrity
	}
	fail := func() (*os.File, *os.File, bool, error) {
		witness.Close()
		return nil, nil, false, ErrIntegrity
	}
	if err = lockFile(witness); err != nil {
		witness.Close()
		return nil, nil, false, err
	}
	// Persist creation even if initialization is interrupted. An incomplete
	// witness fails closed rather than authorizing another initialization.
	if err = parent.Sync(); err != nil {
		return fail()
	}
	dir, err := privateDirectory(root, fresh)
	if err != nil {
		return fail()
	}
	valid := false
	defer func() {
		if !valid {
			dir.Close()
		}
	}()
	generation := generationFileID(dir)
	if generation == "" {
		return fail()
	}
	expected := namespaceWitness{Kind: "ActivityNamespaceV1", Namespace: identity(root, generation)}
	expected.Checksum = jsonDigest(expected)
	if fresh {
		entries, readErr := dir.ReadDir(1)
		// Existing storage without its witness cannot be safely adopted.
		if readErr != nil && readErr != io.EOF || len(entries) != 0 {
			return fail()
		}
		data, _ := json.Marshal(expected)
		_, err = witness.Write(data)
		if errors.Join(err, witness.Sync(), parent.Sync()) != nil {
			return fail()
		}
	} else {
		data, readErr := io.ReadAll(io.LimitReader(witness, 1025))
		var recorded namespaceWitness
		if readErr != nil || len(data) > 1024 || strictjson.Decode(data, &recorded) != nil || recorded != expected {
			return fail()
		}
	}
	valid = true
	return dir, witness, fresh, nil
}

func (s *Store) checkNamespace() error {
	witness, err := openRegular(s.root+".namespace.json", os.O_RDONLY, true)
	if err != nil {
		return ErrIntegrity
	}
	defer witness.Close()
	info, err := witness.Stat()
	if err != nil || !os.SameFile(info, s.namespaceInfo) || info.Size() != s.namespaceInfo.Size() || info.ModTime() != s.namespaceInfo.ModTime() {
		return ErrIntegrity
	}
	return nil
}

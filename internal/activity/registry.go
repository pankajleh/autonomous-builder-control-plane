package activity

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/strictjson"
)

const maxRegistryBytes = 8 << 20

// This bounded namespace witness contains no activity or authoritative run
// state. It records only that a run's generation has ever been initialized.
// Its digest is committed in the independent global ordinal allocator, making
// deletion, truncation, or rollback of the registry fail closed on restart.
type initializationRegistry struct {
	Namespace string                    `json:"namespace"`
	Runs      map[string]initializedRun `json:"runs"`
}

type initializedRun struct {
	Generation   string `json:"generation"`
	Registration string `json:"registration"`
}

func sequenceChecksum(state sequenceState) string {
	state.Checksum = ""
	return jsonDigest(state)
}

func (s *Store) loadRegistry() error {
	path := filepath.Join(s.root, "initializations.json")
	data, err := readFile(path, maxRegistryBytes, true)
	info, statErr := s.dir.Stat()
	if err != nil || statErr != nil || digest(data) != s.registryHash || strictjson.Decode(data, &s.registry) != nil || s.registry.Namespace != identity(s.root, physicalID(info)) || s.registry.Runs == nil || len(s.registry.Runs) > runtimecatalog.MaxRuns {
		return ErrIntegrity
	}
	for run, record := range s.registry.Runs {
		if runtimecatalog.ValidateIdentifier(run) != nil || len(record.Generation) != 64 || len(record.Registration) != 64 {
			return ErrIntegrity
		}
	}
	f, err := openRegular(path, os.O_RDONLY, true)
	if err != nil {
		return ErrIntegrity
	}
	s.registryInfo, err = f.Stat()
	f.Close()
	return err
}

func (s *Store) checkRegistry() error {
	f, err := openRegular(filepath.Join(s.root, "initializations.json"), os.O_RDONLY, true)
	if err != nil {
		return ErrIntegrity
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || s.registryInfo == nil || !os.SameFile(info, s.registryInfo) || info.Size() != s.registryInfo.Size() || info.ModTime() != s.registryInfo.ModTime() {
		return ErrIntegrity
	}
	return nil
}

func (s *Store) saveRegistry() error {
	if s.registryInfo != nil {
		if err := s.checkRegistry(); err != nil {
			return err
		}
	}
	data, err := json.Marshal(s.registry)
	if err != nil || len(data) > maxRegistryBytes || len(s.registry.Runs) > runtimecatalog.MaxRuns {
		return ErrExhausted
	}
	path := filepath.Join(s.root, "initializations.json")
	f, err := openRegular(path+".next", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, true)
	if err != nil {
		return ErrIntegrity
	}
	_, err = f.Write(data)
	if errors.Join(err, f.Sync(), f.Close()) != nil || os.Rename(path+".next", path) != nil || s.dir.Sync() != nil {
		return ErrIntegrity
	}
	s.registryHash = digest(data)
	if err = s.saveSequence(s.next); err != nil {
		return err
	}
	return s.loadRegistry()
}

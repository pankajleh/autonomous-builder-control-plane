package remotetarget

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/strictjson"
)

// A release is one app version's files as a tar, named by the tar's SHA-256
// (Repo C design note BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md §11, BF-14.2a, and
// kind node from BF-14.2b). It
// carries autobuild-release.json, the manifest, which names every other file
// with its SHA-256 and size. ABCP keeps it under <dir>/releases and never
// changes it; what reaches a server is checked against the manifest there.

const (
	// MaxReleases bounds the releases one ABCP keeps.
	MaxReleases     = 30
	maxReleaseFiles = 2000
	maxFileBytes    = 64 << 20
	maxFilesBytes   = 128 << 20
	manifestName    = "autobuild-release.json"
	manifestSchema  = "ReleaseManifestV1"
	releaseSchema   = 1
)

var (
	digestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	appPattern      = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	versionPattern  = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)
	filePathPattern = regexp.MustCompile(`^[A-Za-z0-9._~@+(), -]{1,255}(/[A-Za-z0-9._~@+(), -]{1,255})*$`)
)

type manifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type manifest struct {
	SchemaVersion string         `json:"schema_version"`
	App           string         `json:"app"`
	Version       string         `json:"version"`
	Kind          string         `json:"kind"`
	Files         []manifestFile `json:"files"`
}

// releaseRecord is a kept release, in <dir>/releases/<digest>.json beside its tar.
type releaseRecord struct {
	SchemaVersion int                  `json:"schema_version"`
	Release       serviceapi.ReleaseV1 `json:"release"`
	Files         []manifestFile       `json:"files"`
}

func (m *Manager) releaseDir() string { return filepath.Join(m.dir, "releases") }

// safeFilePath is a relative name with no empty, "." or ".." part, no link
// and nothing a shell or a checksum line could read differently.
func safeFilePath(name string) bool {
	if len(name) > 1024 || !filePathPattern.MatchString(name) || name == manifestName {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "." || part == ".." || strings.TrimSpace(part) != part {
			return false
		}
	}
	return true
}

// PutRelease keeps a release once its digest, its manifest and every file in
// it are checked. A release kept already is answered as it is.
func (m *Manager) PutRelease(_ context.Context, digest string, body io.Reader) (serviceapi.ReleaseV1, bool, error) {
	if !digestPattern.MatchString(digest) {
		return serviceapi.ReleaseV1{}, false, serviceapi.ErrReleaseInvalid
	}
	m.releaseMu.Lock()
	defer m.releaseMu.Unlock()
	if kept, err := m.readRelease(digest); err == nil {
		return kept.Release, false, nil
	} else if !errors.Is(err, serviceapi.ErrReleaseUnknown) {
		return serviceapi.ReleaseV1{}, false, err
	}
	if err := privateDirectoryCreate(m.releaseDir()); err != nil {
		return serviceapi.ReleaseV1{}, false, serviceapi.ErrTargetUnavailable
	}
	entries, err := os.ReadDir(m.releaseDir())
	if err != nil {
		return serviceapi.ReleaseV1{}, false, serviceapi.ErrTargetUnavailable
	}
	count := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			count++
		}
	}
	if count >= MaxReleases {
		return serviceapi.ReleaseV1{}, false, serviceapi.ErrReleaseLimit
	}
	tmp, err := os.CreateTemp(m.releaseDir(), ".release-*")
	if err != nil {
		return serviceapi.ReleaseV1{}, false, serviceapi.ErrTargetUnavailable
	}
	name := tmp.Name()
	defer os.Remove(name)
	defer tmp.Close()
	if err := tmp.Chmod(0o600); err != nil {
		return serviceapi.ReleaseV1{}, false, serviceapi.ErrTargetUnavailable
	}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(body, serviceapi.MaxReleaseBytes+1))
	if err != nil || n == 0 || n > serviceapi.MaxReleaseBytes || hex.EncodeToString(hash.Sum(nil)) != digest {
		return serviceapi.ReleaseV1{}, false, serviceapi.ErrReleaseInvalid
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return serviceapi.ReleaseV1{}, false, serviceapi.ErrTargetUnavailable
	}
	man, err := readReleaseTar(tmp)
	if err != nil {
		return serviceapi.ReleaseV1{}, false, serviceapi.ErrReleaseInvalid
	}
	if err := tmp.Sync(); err != nil {
		return serviceapi.ReleaseV1{}, false, serviceapi.ErrTargetUnavailable
	}
	var total int64
	for _, f := range man.Files {
		total += f.Size
	}
	rec := releaseRecord{SchemaVersion: releaseSchema, Files: man.Files, Release: serviceapi.ReleaseV1{
		SchemaVersion: serviceapi.ReleaseSchemaV1, Digest: digest, App: man.App, Version: man.Version, Kind: man.Kind,
		Files: len(man.Files), Bytes: total, ReceivedAt: m.now().UTC().Format("2006-01-02T15:04:05Z"),
	}}
	if err := os.Rename(name, filepath.Join(m.releaseDir(), digest+".tar")); err != nil {
		return serviceapi.ReleaseV1{}, false, serviceapi.ErrTargetUnavailable
	}
	data, _ := json.MarshalIndent(rec, "", "  ")
	if err := writePrivate(filepath.Join(m.releaseDir(), digest+".json"), append(data, '\n')); err != nil {
		_ = os.Remove(filepath.Join(m.releaseDir(), digest+".tar"))
		return serviceapi.ReleaseV1{}, false, serviceapi.ErrTargetUnavailable
	}
	return rec.Release, true, nil
}

// Release answers a kept release.
func (m *Manager) Release(_ context.Context, digest string) (serviceapi.ReleaseV1, error) {
	rec, err := m.readRelease(digest)
	if err != nil {
		return serviceapi.ReleaseV1{}, err
	}
	return rec.Release, nil
}

func (m *Manager) readRelease(digest string) (releaseRecord, error) {
	if !digestPattern.MatchString(digest) {
		return releaseRecord{}, serviceapi.ErrReleaseInvalid
	}
	data, err := os.ReadFile(filepath.Join(m.releaseDir(), digest+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return releaseRecord{}, serviceapi.ErrReleaseUnknown
	}
	if err != nil {
		return releaseRecord{}, serviceapi.ErrTargetUnavailable
	}
	var rec releaseRecord
	if strictjson.Decode(data, &rec) != nil || rec.SchemaVersion != releaseSchema || rec.Release.Digest != digest {
		return releaseRecord{}, serviceapi.ErrTargetUnavailable
	}
	if _, err := os.Stat(filepath.Join(m.releaseDir(), digest+".tar")); err != nil {
		return releaseRecord{}, serviceapi.ErrReleaseUnknown
	}
	return rec, nil
}

// readReleaseTar reads a release's tar: only regular files and folders, every
// name safe, the manifest's list exactly the files with their sizes and hashes.
func readReleaseTar(r io.Reader) (manifest, error) {
	invalid := errors.New("invalid release")
	reader := tar.NewReader(r)
	seen := map[string]manifestFile{}
	var manifestData []byte
	var total int64
	for {
		h, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return manifest{}, invalid
		}
		name := strings.TrimPrefix(h.Name, "./")
		switch h.Typeflag {
		case tar.TypeDir:
			if name = strings.TrimSuffix(name, "/"); name != "" && name != "." && !safeFilePath(name) {
				return manifest{}, invalid
			}
			continue
		case tar.TypeReg:
		default:
			return manifest{}, invalid
		}
		if h.Size < 0 || h.Size > maxFileBytes {
			return manifest{}, invalid
		}
		if name == manifestName {
			if manifestData != nil || h.Size > 1<<20 {
				return manifest{}, invalid
			}
			if manifestData, err = io.ReadAll(reader); err != nil {
				return manifest{}, invalid
			}
			continue
		}
		if !safeFilePath(name) {
			return manifest{}, invalid
		}
		if _, dup := seen[name]; dup || len(seen) >= maxReleaseFiles {
			return manifest{}, invalid
		}
		total += h.Size
		if total > maxFilesBytes {
			return manifest{}, invalid
		}
		hash := sha256.New()
		n, err := io.Copy(hash, reader)
		if err != nil || n != h.Size {
			return manifest{}, invalid
		}
		seen[name] = manifestFile{Path: name, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: n}
	}
	if manifestData == nil {
		return manifest{}, invalid
	}
	var man manifest
	if strictjson.Decode(manifestData, &man) != nil || man.SchemaVersion != manifestSchema || !appPattern.MatchString(man.App) ||
		!versionPattern.MatchString(man.Version) || (man.Kind != kindStatic && man.Kind != kindNode) || len(man.Files) != len(seen) || len(man.Files) == 0 {
		return manifest{}, invalid
	}
	if !sort.SliceIsSorted(man.Files, func(i, j int) bool { return man.Files[i].Path < man.Files[j].Path }) {
		return manifest{}, invalid
	}
	index := false
	for _, f := range man.Files {
		if seen[f.Path] != f {
			return manifest{}, invalid
		}
		index = index || f.Path == "index.html"
	}
	// A static site opens from index.html; a Node app follows Node format 1.
	if (man.Kind == kindStatic && !index) || (man.Kind == kindNode && !validNodeFiles(man.Files)) {
		return manifest{}, invalid
	}
	return man, nil
}

// checksumLines is what `sha256sum -c` reads to check a release on a server.
func checksumLines(files []manifestFile) []byte {
	var b bytes.Buffer
	for _, f := range files {
		b.WriteString(f.SHA256 + "  " + f.Path + "\n")
	}
	return b.Bytes()
}

// privateDirectoryCreate makes a folder open to the controller's user alone, or checks one.
func privateDirectoryCreate(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return privateDirectory(dir)
}

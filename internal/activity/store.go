package activity

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/strictjson"
)

// Ordinals are allocated durably across all runs, before each append. This
// makes a bare SSE ordinal unambiguous across runs and recreated generations.
// Allocation gaps after a crash are harmless; allocated values are never reused.
type Store struct {
	mu            sync.Mutex
	root          string
	dir, lock     *os.File
	namespace     *os.File
	namespaceInfo os.FileInfo
	next          uint64
	sequenceInfo  os.FileInfo
	registryInfo  os.FileInfo
	registry      initializationRegistry
	registryHash  string
	cache         map[string]*runLog
	closed        bool
}

type providerProof struct {
	Session      string `json:"session"` // digest, never the provider's path-shaped ID
	Generation   string `json:"generation"`
	FileIdentity string `json:"file_identity"`
	Size         int64  `json:"size"`
	PrefixDigest string `json:"prefix_digest"`
}

// The separately committed anchor detects complete-record truncation as well
// as replacement/deletion across process restarts. Fully written records after
// an old anchor can be recovered if a crash interrupted anchor publication.
type logAnchor struct {
	Generation string `json:"generation"`
	Physical   string `json:"physical"`
	Bytes      int64  `json:"bytes"`
	Tail       string `json:"tail"`
	Ordinal    uint64 `json:"ordinal"`
}

type sequenceState struct {
	Next     uint64 `json:"next"`
	Registry string `json:"registry"`
	Checksum string `json:"checksum"`
}

type logHeader struct{ Run, Generation, Registration string }
type logRecord struct {
	Event    *Event         `json:"event,omitempty"`
	Provider *providerProof `json:"provider,omitempty"`
	Checksum string         `json:"checksum"`
}

type runLog struct {
	header          logHeader
	events          []Event
	identities      map[string]int
	providerSources map[string]string
	provider        *providerProof
	tail            string
	bytes           int64
	info            os.FileInfo
	physical        string
	lastOrdinal     uint64
	err             error
}

func OpenStore(root string) (*Store, error) {
	dir, namespace, fresh, err := openStoreNamespace(root)
	if err != nil {
		return nil, err
	}
	lock, err := openRegular(filepath.Join(root, ".lock"), os.O_RDWR|os.O_CREATE, true)
	if err != nil {
		namespace.Close()
		dir.Close()
		return nil, ErrUnavailable
	}
	if err = lockFile(lock); err != nil {
		namespace.Close()
		lock.Close()
		dir.Close()
		return nil, err
	}
	s := &Store{root: root, dir: dir, lock: lock, namespace: namespace, cache: map[string]*runLog{}}
	s.namespaceInfo, err = namespace.Stat()
	if err != nil {
		s.Close()
		return nil, ErrIntegrity
	}
	data, err := readFile(filepath.Join(root, "sequence"), 256, true)
	var sequence sequenceState
	if errors.Is(err, os.ErrNotExist) {
		if !fresh {
			s.Close()
			return nil, ErrIntegrity
		}
		entries, e := dir.ReadDir(2*runtimecatalog.MaxRuns + 11)
		if e != nil && e != io.EOF || len(entries) > 2*runtimecatalog.MaxRuns+10 {
			s.Close()
			return nil, ErrIntegrity
		}
		for _, entry := range entries {
			if entry.Name() != ".lock" {
				s.Close()
				return nil, ErrIntegrity
			}
		}
		info, e := dir.Stat()
		if e != nil {
			s.Close()
			return nil, ErrIntegrity
		}
		s.registry = initializationRegistry{Namespace: identity(root, physicalID(info)), Runs: map[string]initializedRun{}}
		if err = s.saveRegistry(); err != nil {
			s.Close()
			return nil, err
		}
	} else if err != nil || strictjson.Decode(data, &sequence) != nil || sequence.Next > 1<<53-1 || len(sequence.Registry) != 64 || sequence.Checksum != sequenceChecksum(sequence) {
		s.Close()
		return nil, ErrIntegrity
	}
	if data != nil {
		s.next = sequence.Next
		s.registryHash = sequence.Registry
		if err = s.loadRegistry(); err != nil {
			s.Close()
			return nil, err
		}
	}
	seq, err := openRegular(filepath.Join(root, "sequence"), os.O_RDONLY, true)
	if err != nil {
		s.Close()
		return nil, ErrIntegrity
	}
	s.sequenceInfo, err = seq.Stat()
	seq.Close()
	if err != nil {
		s.Close()
		return nil, ErrIntegrity
	}
	// Check the committed high water marks before allocating any new run's
	// ordinal, so a rolled-back sequence cannot alias another run on restart.
	scan, scanErr := openDirectory(root)
	if scanErr != nil {
		s.Close()
		return nil, ErrIntegrity
	}
	entries, scanErr := scan.ReadDir(2*runtimecatalog.MaxRuns + 11)
	scan.Close()
	if scanErr != nil && scanErr != io.EOF || len(entries) > 2*runtimecatalog.MaxRuns+10 {
		s.Close()
		return nil, ErrIntegrity
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".anchor.json") {
			anchor, anchorErr := s.readAnchor(filepath.Join(root, entry.Name()))
			if anchorErr != nil || anchor.Ordinal > s.next {
				s.Close()
				return nil, ErrIntegrity
			}
		}
	}
	return s, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return errors.Join(s.lock.Close(), s.dir.Close(), s.namespace.Close())
}

func (s *Store) saveSequence(next uint64) error {
	if s.sequenceInfo != nil {
		f, err := openRegular(filepath.Join(s.root, "sequence"), os.O_RDONLY, true)
		if err != nil {
			return ErrIntegrity
		}
		info, err := f.Stat()
		f.Close()
		if err != nil || !os.SameFile(info, s.sequenceInfo) || info.ModTime() != s.sequenceInfo.ModTime() || info.Size() != s.sequenceInfo.Size() {
			return ErrIntegrity
		}
	}
	path := filepath.Join(s.root, "sequence.next")
	f, err := openRegular(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, true)
	if err != nil {
		return ErrIntegrity
	}
	state := sequenceState{Next: next, Registry: s.registryHash}
	state.Checksum = sequenceChecksum(state)
	data, _ := json.Marshal(state)
	_, err = f.Write(data)
	err = errors.Join(err, f.Sync(), f.Close())
	if err != nil {
		return ErrIntegrity
	}
	if os.Rename(path, filepath.Join(s.root, "sequence")) != nil || s.dir.Sync() != nil {
		return ErrIntegrity
	}
	s.next = next
	seq, err := openRegular(filepath.Join(s.root, "sequence"), os.O_RDONLY, true)
	if err != nil {
		return ErrIntegrity
	}
	s.sequenceInfo, err = seq.Stat()
	seq.Close()
	if err != nil {
		return ErrIntegrity
	}
	return nil
}

func (s *Store) path(run string) string { return filepath.Join(s.root, digest([]byte(run))+".jsonl") }
func (s *Store) load(run, registration string) (*runLog, error) {
	if s.closed || runtimecatalog.ValidateIdentifier(run) != nil {
		return nil, ErrUnavailable
	}
	if err := s.checkNamespace(); err != nil {
		return nil, err
	}
	dir, err := privateDirectory(s.root, false)
	if err != nil {
		return nil, ErrIntegrity
	}
	info, err := dir.Stat()
	dir.Close()
	original, e := s.dir.Stat()
	if err != nil || e != nil || !os.SameFile(info, original) {
		return nil, ErrIntegrity
	}
	if err = s.checkRegistry(); err != nil {
		return nil, err
	}
	initialized, known := s.registry.Runs[run]
	if known && initialized.Registration != digest([]byte(registration)) {
		return nil, ErrIntegrity
	}
	path := s.path(run)
	anchor, anchorErr := s.readAnchor(path + ".anchor.json")
	f, err := openRegular(path, os.O_RDONLY, true)
	if errors.Is(err, os.ErrNotExist) {
		if _, exists := s.cache[run]; exists || known || !errors.Is(anchorErr, os.ErrNotExist) {
			return nil, ErrIntegrity
		}
		var seed [32]byte
		if _, err = rand.Read(seed[:]); err != nil {
			return nil, err
		}
		h := logHeader{run, hex.EncodeToString(seed[:]), registration}
		if len(s.registry.Runs) >= runtimecatalog.MaxRuns {
			return nil, ErrExhausted
		}
		initialized = initializedRun{Generation: h.Generation, Registration: digest([]byte(registration))}
		s.registry.Runs[run] = initialized
		// Persist the independent initialization witness BEFORE creating a log.
		// An interrupted initialization fails closed; it cannot erase a generation.
		if err = s.saveRegistry(); err != nil {
			return nil, err
		}
		known = true
		data, _ := json.Marshal(h)
		data = append(data, '\n')
		f, err = openRegular(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, true)
		if err != nil {
			return nil, ErrIntegrity
		}
		_, err = f.Write(data)
		if err == nil {
			err = f.Sync()
		}
		if err == nil {
			anchor = logAnchor{Generation: h.Generation, Physical: generationFileID(f), Bytes: int64(len(data)), Tail: digest(data[:len(data)-1])}
			err = s.saveAnchor(path+".anchor.json", anchor)
			anchorErr = err
		}
		err = errors.Join(err, f.Close(), s.dir.Sync())
		if err != nil {
			return nil, ErrIntegrity
		}
		f, err = openRegular(path, os.O_RDONLY, true)
	}
	if err != nil {
		return nil, ErrIntegrity
	}
	defer f.Close()
	if !known || anchor.Generation != initialized.Generation {
		return nil, ErrIntegrity
	}
	info, err = f.Stat()
	if err != nil || info.Size() > MaxBytes || anchorErr != nil || anchor.Bytes > info.Size() || anchor.Bytes <= 0 || anchor.Physical == "" || anchor.Physical != generationFileID(f) {
		return nil, ErrIntegrity
	}
	if prior := s.cache[run]; prior != nil {
		if prior.err != nil {
			return nil, prior.err
		}
		if !os.SameFile(prior.info, info) || prior.bytes != info.Size() || prior.info.ModTime() != info.ModTime() {
			prior.err = ErrIntegrity
			return nil, ErrIntegrity
		}
		if prior.header.Registration != registration || anchor.Generation != prior.header.Generation || anchor.Bytes != prior.bytes || anchor.Tail != prior.tail {
			return nil, ErrIntegrity
		}
		return prior, nil
	}
	log := &runLog{identities: map[string]int{}, providerSources: map[string]string{}, info: info, bytes: info.Size(), physical: anchor.Physical}
	scanner := bufio.NewScanner(io.LimitReader(f, MaxBytes+1))
	scanner.Buffer(make([]byte, 4096), 32<<10)
	if !scanner.Scan() || strictjson.Decode(scanner.Bytes(), &log.header) != nil || log.header.Run != run || log.header.Registration != registration || len(log.header.Generation) != 64 || anchor.Generation != log.header.Generation {
		return nil, ErrIntegrity
	}
	log.tail = digest(scanner.Bytes())
	consumed := int64(len(scanner.Bytes()) + 1)
	anchored := consumed == anchor.Bytes && log.tail == anchor.Tail && anchor.Ordinal == 0
	var last uint64
	for scanner.Scan() {
		line := scanner.Bytes()
		consumed += int64(len(line) + 1)
		var rec logRecord
		if strictjson.Decode(line, &rec) != nil || (rec.Event == nil) == (rec.Provider == nil) {
			return nil, ErrIntegrity
		}
		check := rec.Checksum
		rec.Checksum = ""
		if check != identity(log.tail, jsonDigest(rec)) {
			return nil, ErrIntegrity
		}
		log.tail = check
		if consumed == anchor.Bytes {
			ordinal := last
			if rec.Event != nil {
				ordinal = rec.Event.Ordinal
			}
			anchored = check == anchor.Tail && ordinal == anchor.Ordinal
		}
		if rec.Provider != nil {
			copy := *rec.Provider
			log.provider = &copy
			continue
		}
		event := *rec.Event
		if validateEvent(event) != nil || event.RunID != run || event.Ordinal <= last || event.Ordinal > s.next || len(log.events) >= MaxEvents {
			return nil, ErrIntegrity
		}
		if _, duplicate := log.identities[event.ActivityID]; duplicate {
			return nil, ErrIntegrity
		}
		if err = log.checkProviderEvent(event); err != nil {
			return nil, err
		}
		if key := providerSourceKey(event); key != "" {
			log.providerSources[key] = event.SourceDigest
		}
		last = event.Ordinal
		log.identities[event.ActivityID] = len(log.events)
		log.events = append(log.events, event)
	}
	if scanner.Err() != nil || consumed != info.Size() || !anchored {
		return nil, ErrIntegrity
	}
	log.lastOrdinal = last
	if consumed > anchor.Bytes {
		if err = s.saveAnchor(path+".anchor.json", logAnchor{Generation: log.header.Generation, Physical: log.physical, Bytes: consumed, Tail: log.tail, Ordinal: last}); err != nil {
			return nil, err
		}
	}
	// Four cached runs bound retained activity memory; the others are recovered
	// from disk on demand. All cache use is under this store's mutex.
	if len(s.cache) >= 4 {
		for key := range s.cache {
			delete(s.cache, key)
			break
		}
	}
	s.cache[run] = log
	return log, nil
}

func validateEvent(e Event) error {
	if e.SchemaVersion != "ActivityEventV1" || len(e.ActivityID) != 64 || len(e.SourceDigest) != 64 || e.Title == "" || len(e.Title) > 256 || len(e.Detail) > 4096 || len(e.SourceSessionID) > MaxSourceID || len(e.SourceEventID) > MaxSourceID {
		return ErrIntegrity
	}
	if e.SourceKind == "RALPHEX_PROGRESS" && (e.AuthorityLevel != "PROVIDER_DETAIL" || e.Status == "ACCEPTED" || e.CheckpointSHA != "" || e.CheckpointClean) {
		return ErrIntegrity
	}
	if e.Status == "ACCEPTED" && (e.SourceKind != "ABCP_LEDGER" || e.AuthorityLevel != "ABCP_ACCEPTANCE" || e.Category != "ACCEPTANCE") {
		return ErrIntegrity
	}
	if e.CheckpointSHA != "" && (e.SourceKind != "ABCP_CHECKPOINT_OBSERVER" || !e.CheckpointClean || !gitSHA.MatchString(e.CheckpointSHA)) {
		return ErrIntegrity
	}
	return nil
}

func (s *Store) write(run string, log *runLog, rec logRecord) error {
	rec.Checksum = identity(log.tail, jsonDigest(rec))
	data, _ := json.Marshal(rec)
	data = append(data, '\n')
	if log.bytes+int64(len(data)) > MaxBytes {
		log.err = ErrExhausted
		return log.err
	}
	f, err := openRegular(s.path(run), os.O_WRONLY|os.O_APPEND, true)
	if err != nil {
		log.err = ErrIntegrity
		return log.err
	}
	info, err := f.Stat()
	if err != nil || !os.SameFile(info, log.info) || info.Size() != log.bytes {
		f.Close()
		log.err = ErrIntegrity
		return log.err
	}
	_, err = f.Write(data)
	err = errors.Join(err, f.Sync())
	info, statErr := f.Stat()
	last := log.lastOrdinal
	if rec.Event != nil {
		last = rec.Event.Ordinal
	}
	if err == nil && statErr == nil {
		err = s.saveAnchor(s.path(run)+".anchor.json", logAnchor{Generation: log.header.Generation, Physical: log.physical, Bytes: info.Size(), Tail: rec.Checksum, Ordinal: last})
	}
	err = errors.Join(err, statErr, f.Close())
	if err != nil {
		log.err = ErrIntegrity
		return log.err
	}
	log.tail, log.info, log.bytes, log.lastOrdinal = rec.Checksum, info, info.Size(), last
	return nil
}

func (s *Store) Append(run, registration string, event Event) (Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	log, err := s.load(run, registration)
	if err != nil {
		return Event{}, err
	}
	if event.RunID != run || validateEvent(event) != nil {
		return Event{}, ErrIntegrity
	}
	if err = log.checkProviderEvent(event); err != nil {
		return Event{}, err
	}
	if index, ok := log.identities[event.ActivityID]; ok {
		return log.events[index], nil
	}
	if len(log.events) >= MaxEvents || s.next >= 1<<53-1 {
		log.err = ErrExhausted
		return Event{}, log.err
	}
	if err = s.saveSequence(s.next + 1); err != nil {
		log.err = err
		return Event{}, err
	}
	event.Ordinal = s.next
	if err = s.write(run, log, logRecord{Event: &event}); err != nil {
		return Event{}, err
	}
	log.identities[event.ActivityID] = len(log.events)
	log.events = append(log.events, event)
	if key := providerSourceKey(event); key != "" {
		log.providerSources[key] = event.SourceDigest
	}
	return event, nil
}

func providerSourceKey(event Event) string {
	if event.SourceKind != "RALPHEX_PROGRESS" || event.SourceSessionID == "" || event.SourceEventID == "" {
		return ""
	}
	return identity(event.SourceSessionID, event.SourceEventID)
}

func (log *runLog) checkProviderEvent(event Event) error {
	if key := providerSourceKey(event); key != "" {
		if previous, exists := log.providerSources[key]; exists && previous != event.SourceDigest {
			return ErrIntegrity
		}
	}
	return nil
}

// A provider restart can change the association between replay ordinals and
// payloads. Check the entire batch before committing any of it, including when
// an earlier collection began partway through the available replay window.
func (s *Store) checkProviderReplay(run, registration string, events []Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	log, err := s.load(run, registration)
	if err != nil {
		return err
	}
	for _, event := range events {
		if err = log.checkProviderEvent(event); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) read(run, registration string, after uint64, limit int) ([]Event, string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	log, err := s.load(run, registration)
	if err != nil {
		return nil, "", false, err
	}
	index := 0
	if after != 0 {
		index = sort.Search(len(log.events), func(i int) bool { return log.events[i].Ordinal >= after })
		if index == len(log.events) || log.events[index].Ordinal != after {
			return nil, "", false, ErrIntegrity
		}
		index++
	}
	end := index + limit
	if end > len(log.events) {
		end = len(log.events)
	}
	result := make([]Event, end-index)
	copy(result, log.events[index:end])
	return result, log.header.Generation, end < len(log.events), nil
}

func (s *Store) proof(run, registration string) (*providerProof, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	log, err := s.load(run, registration)
	if err != nil {
		return nil, err
	}
	if log.provider == nil {
		return nil, nil
	}
	p := *log.provider
	return &p, nil
}
func (s *Store) saveProof(run, registration string, p providerProof) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	log, err := s.load(run, registration)
	if err != nil {
		return err
	}
	if log.provider != nil && *log.provider == p {
		return nil
	}
	if log.provider != nil && (log.provider.Session != p.Session || log.provider.Generation != p.Generation || log.provider.FileIdentity != p.FileIdentity || log.provider.Size > p.Size) {
		return ErrIntegrity
	}
	if err = s.write(run, log, logRecord{Provider: &p}); err != nil {
		return err
	}
	log.provider = &p
	return nil
}

func (s *Store) readAnchor(path string) (logAnchor, error) {
	data, err := readFile(path, 1024, true)
	if err != nil {
		return logAnchor{}, err
	}
	var anchor logAnchor
	if strictjson.Decode(data, &anchor) != nil || len(anchor.Generation) != 64 || len(anchor.Tail) != 64 || anchor.Bytes < 1 || anchor.Bytes > MaxBytes || len(anchor.Physical) != 64 {
		return logAnchor{}, ErrIntegrity
	}
	return anchor, nil
}
func (s *Store) saveAnchor(path string, anchor logAnchor) error {
	if anchor.Physical == "" {
		return ErrIntegrity
	}
	data, _ := json.Marshal(anchor)
	temporary := path + ".next"
	f, err := openRegular(temporary, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, true)
	if err != nil {
		return ErrIntegrity
	}
	_, err = f.Write(data)
	err = errors.Join(err, f.Sync(), f.Close())
	if err != nil {
		return ErrIntegrity
	}
	if os.Rename(temporary, path) != nil || s.dir.Sync() != nil {
		return ErrIntegrity
	}
	return nil
}

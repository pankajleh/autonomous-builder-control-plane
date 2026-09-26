package preview

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/strictjson"
)

// One process owns the journal for its lifetime. Each durable frame preserves
// prior states and optionally admits an immutable idempotency receipt. A hash
// chain and independently fsynced head reject torn writes and tail loss.
type receipt struct {
	Key, Digest string
	Result      serviceapi.PreviewV1
}
type frame struct {
	Sequence uint64
	Previous string
	Preview  serviceapi.PreviewV1
	Receipt  *receipt `json:",omitempty"`
}
type store struct {
	root      string
	lock, log *os.File
	records   map[string]serviceapi.PreviewV1
	receipts  map[string]receipt
	revisions map[string]uint64
	sequence  uint64
	head      string
	size      int64
	broken    bool
}

func openStore(root string) (*store, error) {
	d, err := privateDirectory(root, true)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	lock, err := openRegular(filepath.Join(root, "owner.lock"), os.O_RDWR|os.O_CREATE, true)
	if err != nil {
		return nil, err
	}
	if err = lockFile(lock); err != nil {
		lock.Close()
		return nil, err
	}
	return &store{root: root, lock: lock, records: map[string]serviceapi.PreviewV1{}, receipts: map[string]receipt{}, revisions: map[string]uint64{}, broken: true}, nil
}

// Loading history is separate from exclusive namespace ownership so restart
// cleanup remains possible even when a crash damaged the journal or anchor.
func (s *store) load() error {
	d, err := privateDirectory(s.root, false)
	if err != nil {
		return ErrIntegrity
	}
	defer d.Close()
	s.log, err = openRegular(filepath.Join(s.root, "history.jsonl"), os.O_RDWR|os.O_CREATE|os.O_APPEND, true)
	if err != nil {
		return ErrIntegrity
	}
	info, err := s.log.Stat()
	if err != nil || info.Size() > 64<<20 {
		return ErrIntegrity
	}
	s.size = info.Size()
	if s.size > 0 {
		var last [1]byte
		if _, err = s.log.ReadAt(last[:], s.size-1); err != nil || last[0] != '\n' {
			return ErrIntegrity
		}
	}
	scanner := bufio.NewScanner(s.log)
	scanner.Buffer(make([]byte, 4096), 32<<10)
	for scanner.Scan() {
		var f frame
		if strictjson.Decode(scanner.Bytes(), &f) != nil || f.Sequence != s.sequence+1 || f.Previous != s.head || s.validate(f) != nil {
			return ErrIntegrity
		}
		s.apply(f)
		s.head = digest(scanner.Bytes())
		s.sequence = f.Sequence
	}
	if scanner.Err() != nil {
		return ErrIntegrity
	}
	anchor, err := readFile(filepath.Join(s.root, "head"), 128, true)
	if s.sequence == 0 {
		if err == nil && len(anchor) != 0 {
			return ErrIntegrity
		}
		if err != nil && !os.IsNotExist(err) {
			return ErrIntegrity
		}
	} else if err != nil || string(anchor) != s.head {
		return ErrIntegrity
	}
	if _, err = s.log.Seek(0, io.SeekEnd); err != nil {
		return ErrIntegrity
	}
	if d.Sync() != nil {
		return ErrIntegrity
	}
	s.broken = false
	return nil
}
func terminal(status string) bool {
	return status == "FAILED" || status == "EXPIRED" || status == "STOPPED"
}
func immutable(v serviceapi.PreviewV1) serviceapi.PreviewV1 {
	v.Status = ""
	v.Health = ""
	v.StoppedAt = ""
	v.EvidenceID = ""
	v.RouteHandle = ""
	return v
}
func (s *store) validate(f frame) error {
	v := f.Preview
	if serviceapi.ValidatePreviewV1(v) != nil {
		return ErrIntegrity
	}
	old, ok := s.records[v.PreviewID]
	if !ok {
		if len(s.records) >= 1000 || v.Revision != s.revisions[v.RunID]+1 || v.Status != "REQUESTED" || f.Receipt == nil {
			return ErrIntegrity
		}
	} else {
		if immutable(old) != immutable(v) {
			return ErrIntegrity
		}
		allowed := old == v || !terminal(old.Status) && (terminal(v.Status) || old.Status == "REQUESTED" && v.Status == "VALIDATING" || old.Status == "VALIDATING" && v.Status == "STARTING" || old.Status == "STARTING" && v.Status == "READY" || old.Status == "READY" && v.Status == "READY")
		if !allowed {
			return ErrIntegrity
		}
	}
	if f.Receipt != nil {
		r := f.Receipt
		if !sha256Pattern.MatchString(r.Key) || !sha256Pattern.MatchString(r.Digest) || r.Result != v {
			return ErrIntegrity
		}
		if _, exists := s.receipts[r.Key]; exists {
			return ErrIntegrity
		}
	}
	return nil
}
func (s *store) apply(f frame) {
	v := f.Preview
	s.records[v.PreviewID] = v
	if v.Revision > s.revisions[v.RunID] {
		s.revisions[v.RunID] = v.Revision
	}
	if f.Receipt != nil {
		s.receipts[f.Receipt.Key] = *f.Receipt
	}
}
func (s *store) append(v serviceapi.PreviewV1, r *receipt) error {
	if s.broken || s.sequence >= 100000 || s.size >= (64<<20)-(32<<10) {
		s.broken = true
		return ErrUnavailable
	}
	f := frame{Sequence: s.sequence + 1, Previous: s.head, Preview: v, Receipt: r}
	if s.validate(f) != nil {
		return ErrIntegrity
	}
	data, err := json.Marshal(f)
	if err != nil || len(data) > 32<<10 {
		return ErrIntegrity
	}
	s.broken = true
	n, err := s.log.Write(append(data, '\n'))
	if err != nil || n != len(data)+1 || s.log.Sync() != nil {
		return ErrIntegrity
	}
	hash := digest(data)
	// A crash before the anchor rename makes recovery fail closed; no ambiguous
	// command can be silently admitted twice.
	tmp := filepath.Join(s.root, "head.next")
	file, err := openRegular(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, true)
	if err != nil {
		return ErrIntegrity
	}
	_, err = file.WriteString(hash)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err != nil || syncErr != nil || closeErr != nil {
		return ErrIntegrity
	}
	if os.Rename(tmp, filepath.Join(s.root, "head")) != nil {
		return ErrIntegrity
	}
	d, err := privateDirectory(s.root, false)
	if err != nil {
		return ErrIntegrity
	}
	err = d.Sync()
	d.Close()
	if err != nil {
		return ErrIntegrity
	}
	s.apply(f)
	s.sequence = f.Sequence
	s.head = hash
	s.size += int64(n)
	s.broken = false
	return nil
}
func (s *store) close() error {
	if s == nil {
		return nil
	}
	var err error
	if s.log != nil {
		err = s.log.Close()
	}
	if s.lock != nil {
		if e := s.lock.Close(); e != nil {
			err = e
		}
	}
	return err
}

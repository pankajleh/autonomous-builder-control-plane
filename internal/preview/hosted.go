package preview

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/strictjson"
)

// Hosted instances (B9.2.1, docs/plans/completed/b921-hosted-instances.md): one app per hosting key, running one
// version at a time on the governed preview runtime, with the key's own data volume, no time limit, and restarts by
// the controller. Hosted instances use no preview slots.
const (
	// MaxHostedRunning is the owner's pilot limit of keys running at once.
	MaxHostedRunning = 5
	// hostedRestartLimit automatic restarts in an hour leave the key FAILED until it is started again.
	hostedRestartLimit = 3
	hostedNightly      = 24 * time.Hour
	hostedBackupKeep   = 7 * 24 * time.Hour
	hostedBackupLimit  = 32
	hostedReceiptLimit = 64
	// hostedBackupMaxBytes bounds one backup file.
	hostedBackupMaxBytes = 2 << 30
)

// Timings, variables only so tests can shorten them.
var (
	// hostedStartTimeout bounds a first health answer: a new database initialises on a real disk.
	hostedStartTimeout = 90 * time.Second
	// hostedUnhealthyRestart restarts an instance whose health reads have failed this long.
	hostedUnhealthyRestart = 2 * time.Minute
	hostedHealthEvery      = 5 * time.Second
	hostedRestartBackoff   = 2 * time.Second
	hostedNightlyEvery     = 10 * time.Minute
)

// HostedRuntime is the part of the governed runtime a hosted instance uses. The Docker runtime provides it.
type HostedRuntime interface {
	Available(PreviewProfileV1) bool
	StartHosted(ctx context.Context, id, path string, p PreviewProfileV1, keyDigest string) (string, error)
	Healthy(ctx context.Context, id string, p PreviewProfileV1) (bool, error)
	ResolveRoute(ctx context.Context, id, handle string) (string, error)
	Stop(ctx context.Context, id string) error
	Dump(ctx context.Context, id string, p PreviewProfileV1, w io.Writer) error
	Load(ctx context.Context, id string, p PreviewProfileV1, r io.Reader) error
	RemoveVolume(ctx context.Context, keyDigest string) error
}

type hostedVersion struct {
	RunID, CheckpointActivityID, SourceSHA, ProfileID, ProfileDigest string
	ProductAuthorizationID, ProductTaskID, ProductVersionID          string
	ValidationID, RepositoryIdentityDigest                           string
}

type hostedReceipt struct {
	Key, Digest string
	Result      json.RawMessage
}

// hostedState is one key's private state file, written whole and synced before it replaces the old one.
type hostedState struct {
	SchemaVersion           int
	Key                     string
	Owner                   ownerIdentity
	Desired, Status, Health string
	Generation              uint64
	InstanceID, RouteHandle string
	Version                 hostedVersion
	CreatedAt, UpdatedAt    string
	Restarts                []string
	Backups                 []serviceapi.HostedBackupV1
	LastNightly             string
	Data                    string
	Receipts                []hostedReceipt
}

type hostedKey struct {
	op     sync.Mutex // one command at a time per key
	digest string
	state  hostedState // guarded by Hosting.mu
	cancel context.CancelFunc
	done   chan struct{}
}

type Hosting struct {
	mu          sync.Mutex
	root        string
	profiles    map[string]PreviewProfileV1
	resolver    SourceResolver
	checkout    Materializer
	runtime     HostedRuntime
	now         func() time.Time
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	keys        map[string]*hostedKey
	closed      bool
	unavailable bool
}

// NewHosting starts the hosted instances of a preview service's runtime. It runs after the preview service has
// removed every runtime object left from before, and starts again every key whose desired state is running.
func NewHosting(parent context.Context, previews *Service) (*Hosting, error) {
	if previews == nil || previews.store == nil {
		return nil, ErrUnavailable
	}
	runtime, ok := previews.runtime.(HostedRuntime)
	if !ok {
		return nil, ErrUnavailable
	}
	h, err := newHosting(parent, filepath.Join(previews.store.root, "hosted"), previews.profiles, previews.resolver, previews.checkout, runtime, previews.now)
	if err == nil && previews.unavailable {
		h.unavailable = true
	}
	return h, err
}

func newHosting(parent context.Context, root string, profiles map[string]PreviewProfileV1, resolver SourceResolver, checkout Materializer, runtime HostedRuntime, now func() time.Time) (*Hosting, error) {
	if parent == nil || resolver == nil || checkout == nil || runtime == nil || now == nil {
		return nil, ErrUnavailable
	}
	d, err := privateDirectory(root, true)
	if err != nil {
		return nil, ErrUnavailable
	}
	entries, err := d.ReadDir(1001)
	d.Close()
	if err != nil && err != io.EOF || len(entries) > 1000 {
		return nil, ErrIntegrity
	}
	ctx, cancel := context.WithCancel(parent)
	h := &Hosting{root: root, profiles: profiles, resolver: resolver, checkout: checkout, runtime: runtime, now: now, ctx: ctx, cancel: cancel, keys: map[string]*hostedKey{}}
	for _, e := range entries {
		if !sha256Pattern.MatchString(e.Name()) {
			cancel()
			return nil, ErrIntegrity
		}
		// A crash between creating a key's directory and its first state file leaves no key.
		if _, err := os.Lstat(filepath.Join(root, e.Name(), "state.json")); os.IsNotExist(err) {
			continue
		}
		st, err := h.load(e.Name())
		if err != nil {
			cancel()
			return nil, err
		}
		h.keys[e.Name()] = &hostedKey{digest: e.Name(), state: st}
	}
	for _, k := range h.keys {
		st := k.state
		st.RouteHandle, st.Health, st.Restarts = "", "UNKNOWN", nil
		if st.Desired == "RUNNING" {
			st.Status, st.Generation = "STARTING", st.Generation+1
			st.InstanceID = hostedInstance(k.digest, st.Generation)
		} else {
			st.Status = "STOPPED"
		}
		st.UpdatedAt = stamp(now())
		if err := h.save(k, st); err != nil {
			cancel()
			return nil, err
		}
		if st.Desired == "RUNNING" {
			h.launch(k)
		}
	}
	h.wg.Add(1)
	go h.nightly()
	return h, nil
}

func hostedKeyDigest(key string) string { return jsonDigest([]string{"hosted-key-v1", key}) }
func hostedInstance(keyDigest string, generation uint64) string {
	return jsonDigest([]any{"hosted-instance-v1", keyDigest, generation})
}

func (h *Hosting) load(digest string) (hostedState, error) {
	b, err := readFile(filepath.Join(h.root, digest, "state.json"), 4<<20, true)
	if err != nil {
		return hostedState{}, ErrIntegrity
	}
	var st hostedState
	if strictjson.Decode(b, &st) != nil || st.SchemaVersion != 1 || hostedKeyDigest(st.Key) != digest || !st.Owner.valid() || serviceapi.ValidateHostedV1(view(st), st.Key) != nil || len(st.Receipts) > hostedReceiptLimit || len(st.Backups) > hostedBackupLimit {
		return hostedState{}, ErrIntegrity
	}
	return st, nil
}

// save writes the key's state whole: a synced temporary file renamed into place, then the directory synced.
func (h *Hosting) save(k *hostedKey, st hostedState) error {
	if serviceapi.ValidateHostedV1(view(st), st.Key) != nil {
		return ErrIntegrity
	}
	data, err := json.Marshal(st)
	if err != nil || len(data) > 4<<20 {
		return ErrIntegrity
	}
	dir := filepath.Join(h.root, k.digest)
	d, err := privateDirectory(dir, true)
	if err != nil {
		h.unavailable = true
		return ErrUnavailable
	}
	defer d.Close()
	tmp := filepath.Join(dir, "state.next")
	f, err := openRegular(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, true)
	if err != nil {
		h.unavailable = true
		return ErrUnavailable
	}
	_, err = f.Write(data)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err != nil || syncErr != nil || closeErr != nil || os.Rename(tmp, filepath.Join(dir, "state.json")) != nil || d.Sync() != nil {
		h.unavailable = true
		return ErrUnavailable
	}
	k.state = st
	return nil
}

// view is the API form of a key's state.
func view(st hostedState) serviceapi.HostedV1 {
	v := serviceapi.HostedV1{SchemaVersion: "HostedV1", HostingKey: st.Key, Desired: st.Desired, Status: st.Status, Health: st.Health, Generation: st.Generation,
		RunID: st.Version.RunID, CheckpointActivityID: st.Version.CheckpointActivityID, SourceSHA: st.Version.SourceSHA, ProfileID: st.Version.ProfileID, ProfileDigest: st.Version.ProfileDigest,
		ProductAuthorizationID: st.Version.ProductAuthorizationID, ProductTaskID: st.Version.ProductTaskID, ProductVersionID: st.Version.ProductVersionID,
		RouteHandle: st.RouteHandle, Data: st.Data, CreatedAt: st.CreatedAt, UpdatedAt: st.UpdatedAt}
	if n := len(st.Backups); n > 0 {
		last := st.Backups[n-1]
		v.LastBackup = &last
	}
	return v
}

// command is the common admission of one command: the key's owner, and the receipt for its request ID.
type hostedCommand struct {
	receipt, digest string
}

func hostedReceiptKey(o ownerIdentity, key, requestID string) string {
	return jsonDigest(struct {
		Namespace string
		Principal ownerIdentity
		Key       string
		RequestID string
	}{"hosted-command-v1", o, key, requestID})
}
func newHostedCommand(p serviceapi.Principal, authorityDigest, key, operation, requestID string, actor serviceapi.DelegatedActorV1, body any) hostedCommand {
	o := owner(p)
	return hostedCommand{hostedReceiptKey(o, key, requestID), jsonDigest([]any{o, authorityDigest, operation, key, requestID, actor, jsonDigest(body)})}
}

// replay returns the stored result of a repeated request, or a conflict when the request ID was used differently.
func replay(st hostedState, c hostedCommand, into any) (bool, error) {
	for _, r := range st.Receipts {
		if r.Key == c.receipt {
			if r.Digest != c.digest {
				return true, serviceapi.ErrRequestIDConflict
			}
			if json.Unmarshal(r.Result, into) != nil {
				return true, serviceapi.ErrInternalDurableSubstrate
			}
			return true, nil
		}
	}
	return false, nil
}
func withReceipt(st hostedState, c hostedCommand, result any) hostedState {
	data, _ := json.Marshal(result)
	st.Receipts = append(append([]hostedReceipt(nil), st.Receipts...), hostedReceipt{c.receipt, c.digest, data})
	if len(st.Receipts) > hostedReceiptLimit {
		st.Receipts = st.Receipts[len(st.Receipts)-hostedReceiptLimit:]
	}
	return st
}

// key returns the key's entry for its owner. A missing key, or another principal's, is not found.
func (h *Hosting) key(p serviceapi.Principal, key string, create bool) (*hostedKey, error) {
	if serviceapi.ValidateHostingKey(key) != nil || !owner(p).valid() {
		return nil, serviceapi.ErrDependencyNotFound
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.ctx.Err() != nil {
		return nil, serviceapi.ErrPreviewUnavailable
	}
	digest := hostedKeyDigest(key)
	k := h.keys[digest]
	if k == nil {
		if !create {
			return nil, serviceapi.ErrDependencyNotFound
		}
		if len(h.keys) >= 1000 {
			return nil, serviceapi.ErrPreviewUnavailable
		}
		k = &hostedKey{digest: digest}
		h.keys[digest] = k
		return k, nil
	}
	if k.state.Key != "" && k.state.Owner != owner(p) {
		return nil, serviceapi.ErrDependencyNotFound
	}
	return k, nil
}

// forget drops a key entry that was created for a start that did not succeed.
func (h *Hosting) forget(k *hostedKey) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if k.state.Key == "" {
		delete(h.keys, k.digest)
	}
}

func (h *Hosting) ReadHosted(_ context.Context, p serviceapi.Principal, key string) (serviceapi.HostedV1, error) {
	k, err := h.key(p, key, false)
	if err != nil {
		return serviceapi.HostedV1{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if k.state.Key == "" {
		return serviceapi.HostedV1{}, serviceapi.ErrDependencyNotFound
	}
	return view(k.state), nil
}

func (h *Hosting) StartHosted(ctx context.Context, p serviceapi.Principal, authorityDigest, key string, c serviceapi.HostedStartRequestV1) (serviceapi.HostedV1, error) {
	empty := serviceapi.HostedV1{}
	if serviceapi.ValidateHostedStartRequestV1(c) != nil || !sha256Pattern.MatchString(authorityDigest) {
		return empty, serviceapi.ErrPreviewIneligible
	}
	k, err := h.key(p, key, true)
	if err != nil {
		return empty, err
	}
	k.op.Lock()
	defer k.op.Unlock()
	defer h.forget(k)
	cmd := newHostedCommand(p, authorityDigest, key, "start", c.RequestID, c.DelegatedActor, c)
	h.mu.Lock()
	st := k.state
	var result serviceapi.HostedV1
	if ok, err := replay(st, cmd, &result); ok {
		h.mu.Unlock()
		return result, err
	}
	profile, ok := h.profiles[c.ProfileID]
	if !ok || !profile.Hosted {
		h.mu.Unlock()
		return empty, serviceapi.ErrPreviewProfile
	}
	if h.unavailable || !h.runtime.Available(profile) {
		h.mu.Unlock()
		return empty, serviceapi.ErrPreviewUnavailable
	}
	// A key that failed too often is started again as a new version would be; one that runs must be stopped first.
	if st.Desired == "RUNNING" && st.Status != "FAILED" {
		h.mu.Unlock()
		return empty, serviceapi.ErrHostedConflict
	}
	if h.running(k) >= MaxHostedRunning {
		h.mu.Unlock()
		return empty, serviceapi.ErrHostedCapacity
	}
	h.mu.Unlock()
	h.halt(k)
	source, err := h.resolver.Resolve(ctx, c.RunID, c.CheckpointActivityID)
	if err != nil || source.RunID != c.RunID || source.CheckpointActivityID != c.CheckpointActivityID || source.RepositoryIdentityDigest != profile.RepositoryIdentityDigest {
		return empty, serviceapi.ErrPreviewIneligible
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || h.ctx.Err() != nil {
		return empty, serviceapi.ErrPreviewUnavailable
	}
	if h.running(k) >= MaxHostedRunning {
		return empty, serviceapi.ErrHostedCapacity
	}
	st = k.state
	now := stamp(h.now())
	if st.Key == "" {
		st = hostedState{SchemaVersion: 1, Key: key, Owner: owner(p), CreatedAt: now}
	}
	st.Desired, st.Status, st.Health, st.RouteHandle, st.Restarts = "RUNNING", "STARTING", "UNKNOWN", "", nil
	st.Generation++
	st.InstanceID = hostedInstance(k.digest, st.Generation)
	st.Version = hostedVersion{RunID: source.RunID, CheckpointActivityID: source.CheckpointActivityID, SourceSHA: source.SHA, ProfileID: c.ProfileID, ProfileDigest: profile.Digest(),
		ProductAuthorizationID: source.ProductAuthorizationID, ProductTaskID: source.ProductTaskID, ProductVersionID: source.ProductVersionID,
		ValidationID: source.ValidationID, RepositoryIdentityDigest: source.RepositoryIdentityDigest}
	st.Data, st.UpdatedAt = "PRESENT", now
	result = view(st)
	if err = h.save(k, withReceipt(st, cmd, result)); err != nil {
		return empty, err
	}
	h.launch(k)
	return result, nil
}

func (h *Hosting) StopHosted(_ context.Context, p serviceapi.Principal, authorityDigest, key string, c serviceapi.HostedCommandRequestV1) (serviceapi.HostedV1, error) {
	return h.command(p, authorityDigest, key, "stop", c.RequestID, c.DelegatedActor, c, func(k *hostedKey, st hostedState) (hostedState, error) {
		if st.Desired == "STOPPED" && st.Status == "STOPPED" {
			return st, nil
		}
		h.halt(k)
		h.mu.Lock()
		st = k.state
		h.mu.Unlock()
		st.Desired, st.Status, st.Health, st.RouteHandle = "STOPPED", "STOPPED", "UNKNOWN", ""
		return st, nil
	})
}

func (h *Hosting) PurgeHosted(ctx context.Context, p serviceapi.Principal, authorityDigest, key string, c serviceapi.HostedCommandRequestV1) (serviceapi.HostedV1, error) {
	return h.command(p, authorityDigest, key, "purge", c.RequestID, c.DelegatedActor, c, func(k *hostedKey, st hostedState) (hostedState, error) {
		if st.Status != "STOPPED" {
			return st, serviceapi.ErrHostedConflict
		}
		if err := h.runtime.RemoveVolume(ctx, k.digest); err != nil {
			return st, serviceapi.ErrPreviewUnavailable
		}
		if os.RemoveAll(filepath.Join(h.root, k.digest, "backups")) != nil {
			return st, serviceapi.ErrPreviewUnavailable
		}
		st.Data, st.Backups, st.LastNightly = "PURGED", nil, ""
		return st, nil
	})
}

func (h *Hosting) RestoreHosted(ctx context.Context, p serviceapi.Principal, authorityDigest, key string, c serviceapi.HostedRestoreRequestV1) (serviceapi.HostedV1, error) {
	return h.command(p, authorityDigest, key, "restore", c.RequestID, c.DelegatedActor, c, func(k *hostedKey, st hostedState) (hostedState, error) {
		profile, ok := h.profiles[st.Version.ProfileID]
		if st.Status != "READY" || !ok || profile.Digest() != st.Version.ProfileDigest {
			return st, serviceapi.ErrHostedConflict
		}
		found := false
		for _, b := range st.Backups {
			found = found || b.BackupID == c.BackupID
		}
		if !found {
			return st, serviceapi.ErrDependencyNotFound
		}
		f, err := openRegular(h.backupPath(k.digest, c.BackupID), os.O_RDONLY, true)
		if err != nil {
			return st, serviceapi.ErrPreviewUnavailable
		}
		defer f.Close()
		if err = h.runtime.Load(ctx, st.InstanceID, profile, f); err != nil {
			return st, serviceapi.ErrPreviewUnavailable
		}
		return st, nil
	})
}

// command runs one state-changing command on an existing key, idempotently by its request ID.
func (h *Hosting) command(p serviceapi.Principal, authorityDigest, key, operation, requestID string, actor serviceapi.DelegatedActorV1, body any, apply func(*hostedKey, hostedState) (hostedState, error)) (serviceapi.HostedV1, error) {
	empty := serviceapi.HostedV1{}
	if !sha256Pattern.MatchString(authorityDigest) {
		return empty, serviceapi.ErrPreviewIneligible
	}
	k, err := h.key(p, key, false)
	if err != nil {
		return empty, err
	}
	k.op.Lock()
	defer k.op.Unlock()
	cmd := newHostedCommand(p, authorityDigest, key, operation, requestID, actor, body)
	h.mu.Lock()
	st := k.state
	var result serviceapi.HostedV1
	if ok, err := replay(st, cmd, &result); ok {
		h.mu.Unlock()
		return result, err
	}
	if st.Key == "" || h.unavailable {
		h.mu.Unlock()
		return empty, serviceapi.ErrDependencyNotFound
	}
	h.mu.Unlock()
	st, err = apply(k, st)
	if err != nil {
		return empty, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st.UpdatedAt = stamp(h.now())
	result = view(st)
	if err = h.save(k, withReceipt(st, cmd, result)); err != nil {
		return empty, err
	}
	return result, nil
}

func (h *Hosting) ResolveHostedRoute(ctx context.Context, p serviceapi.Principal, key string) (serviceapi.HostedRouteV1, error) {
	k, err := h.key(p, key, false)
	if err != nil {
		return serviceapi.HostedRouteV1{}, err
	}
	h.mu.Lock()
	st := k.state
	h.mu.Unlock()
	if st.Status != "READY" {
		return serviceapi.HostedRouteV1{}, serviceapi.ErrPreviewUnavailable
	}
	target, err := h.runtime.ResolveRoute(ctx, st.InstanceID, st.RouteHandle)
	if err != nil {
		return serviceapi.HostedRouteV1{}, serviceapi.ErrPreviewUnavailable
	}
	return serviceapi.HostedRouteV1{SchemaVersion: "HostedRouteV1", HostingKey: key, RouteHandle: st.RouteHandle, TargetURL: target}, nil
}

func (h *Hosting) ListHostedBackups(_ context.Context, p serviceapi.Principal, key string) (serviceapi.HostedBackupListV1, error) {
	k, err := h.key(p, key, false)
	if err != nil {
		return serviceapi.HostedBackupListV1{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if k.state.Key == "" {
		return serviceapi.HostedBackupListV1{}, serviceapi.ErrDependencyNotFound
	}
	return serviceapi.HostedBackupListV1{SchemaVersion: "HostedBackupListV1", HostingKey: key, Backups: append([]serviceapi.HostedBackupV1{}, k.state.Backups...)}, nil
}

func (h *Hosting) BackupHosted(ctx context.Context, p serviceapi.Principal, authorityDigest, key string, c serviceapi.HostedBackupRequestV1) (serviceapi.HostedBackupV1, error) {
	empty := serviceapi.HostedBackupV1{}
	if !sha256Pattern.MatchString(authorityDigest) {
		return empty, serviceapi.ErrPreviewIneligible
	}
	k, err := h.key(p, key, false)
	if err != nil {
		return empty, err
	}
	k.op.Lock()
	defer k.op.Unlock()
	cmd := newHostedCommand(p, authorityDigest, key, "backup", c.RequestID, c.DelegatedActor, c)
	h.mu.Lock()
	st := k.state
	var result serviceapi.HostedBackupV1
	if ok, err := replay(st, cmd, &result); ok {
		h.mu.Unlock()
		return result, err
	}
	h.mu.Unlock()
	if st.Key == "" {
		return empty, serviceapi.ErrDependencyNotFound
	}
	result, err = h.backup(ctx, k, st, c.Reason, jsonDigest([]string{"hosted-backup-v1", k.digest, cmd.receipt}))
	if err != nil {
		return empty, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st = k.state
	st.Backups = append(append([]serviceapi.HostedBackupV1(nil), st.Backups...), result)
	st = pruneBackups(st, h.now())
	st.UpdatedAt = stamp(h.now())
	if err = h.save(k, withReceipt(st, cmd, result)); err != nil {
		return empty, err
	}
	h.removeUnlisted(k.digest, st.Backups)
	return result, nil
}

func (h *Hosting) backupPath(keyDigest, backupID string) string {
	return filepath.Join(h.root, keyDigest, "backups", backupID+".dump")
}

type limitedFile struct {
	f       *os.File
	written int64
}

func (w *limitedFile) Write(p []byte) (int, error) {
	if w.written+int64(len(p)) > hostedBackupMaxBytes {
		return 0, ErrUnavailable
	}
	n, err := w.f.Write(p)
	w.written += int64(n)
	return n, err
}

// backup dumps the ready instance's data into a new private file. The key's command lock is held.
func (h *Hosting) backup(ctx context.Context, k *hostedKey, st hostedState, reason, id string) (serviceapi.HostedBackupV1, error) {
	empty := serviceapi.HostedBackupV1{}
	profile, ok := h.profiles[st.Version.ProfileID]
	if st.Status != "READY" || !ok || profile.Digest() != st.Version.ProfileDigest || profile.dataService() < 0 {
		return empty, serviceapi.ErrHostedConflict
	}
	dir := filepath.Join(h.root, k.digest, "backups")
	d, err := privateDirectory(dir, true)
	if err != nil {
		return empty, serviceapi.ErrPreviewUnavailable
	}
	defer d.Close()
	tmp := filepath.Join(dir, id+".next")
	f, err := openRegular(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, true)
	if err != nil {
		return empty, serviceapi.ErrPreviewUnavailable
	}
	w := &limitedFile{f: f}
	err = h.runtime.Dump(ctx, st.InstanceID, profile, w)
	syncErr := f.Sync()
	closeErr := f.Close()
	if err != nil || syncErr != nil || closeErr != nil || w.written == 0 || os.Rename(tmp, h.backupPath(k.digest, id)) != nil || d.Sync() != nil {
		_ = os.Remove(tmp)
		return empty, serviceapi.ErrPreviewUnavailable
	}
	return serviceapi.HostedBackupV1{SchemaVersion: "HostedBackupV1", HostingKey: st.Key, BackupID: id, Reason: reason, TakenAt: stamp(h.now()), Bytes: w.written, Generation: st.Generation, SourceSHA: st.Version.SourceSHA}, nil
}

// pruneBackups keeps backups for 7 days, and at most hostedBackupLimit, but always the newest.
func pruneBackups(st hostedState, now time.Time) hostedState {
	kept := []serviceapi.HostedBackupV1{}
	for i, b := range st.Backups {
		if i == len(st.Backups)-1 || now.Sub(parseTime(b.TakenAt)) < hostedBackupKeep {
			kept = append(kept, b)
		}
	}
	if len(kept) > hostedBackupLimit {
		kept = kept[len(kept)-hostedBackupLimit:]
	}
	st.Backups = kept
	return st
}

// removeUnlisted deletes backup files no longer listed in the key's state.
func (h *Hosting) removeUnlisted(keyDigest string, listed []serviceapi.HostedBackupV1) {
	names := map[string]bool{}
	for _, b := range listed {
		names[b.BackupID+".dump"] = true
	}
	d, err := privateDirectory(filepath.Join(h.root, keyDigest, "backups"), false)
	if err != nil {
		return
	}
	defer d.Close()
	entries, err := d.ReadDir(1000)
	if err != nil && err != io.EOF {
		return
	}
	for _, e := range entries {
		if !names[e.Name()] {
			_ = os.Remove(filepath.Join(h.root, keyDigest, "backups", e.Name()))
		}
	}
}

// running counts the other keys that are to run and have not failed. Call with h.mu held.
func (h *Hosting) running(except *hostedKey) int {
	n := 0
	for _, k := range h.keys {
		if k != except && k.state.Desired == "RUNNING" && k.state.Status != "FAILED" {
			n++
		}
	}
	return n
}

// launch starts the key's supervisor. Call with h.mu held and the key's state STARTING.
func (h *Hosting) launch(k *hostedKey) {
	ctx, cancel := context.WithCancel(h.ctx)
	done := make(chan struct{})
	k.cancel, k.done = cancel, done
	h.wg.Add(1)
	go h.supervise(ctx, k, done)
}

// halt stops the key's supervisor and waits for it to remove its runtime objects.
func (h *Hosting) halt(k *hostedKey) {
	h.mu.Lock()
	cancel, done := k.cancel, k.done
	k.cancel, k.done = nil, nil
	h.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// supervise runs the key's instance, one generation at a time, until it is stopped or has failed too often.
func (h *Hosting) supervise(ctx context.Context, k *hostedKey, done chan struct{}) {
	defer h.wg.Done()
	defer close(done)
	for {
		h.mu.Lock()
		st := k.state
		h.mu.Unlock()
		h.runGeneration(ctx, k, st)
		wait(ctx, hostedRestartBackoff)
		if ctx.Err() != nil {
			return
		}
		h.mu.Lock()
		st = k.state
		now := h.now()
		recent := []string{}
		for _, at := range st.Restarts {
			if now.Sub(parseTime(at)) < time.Hour {
				recent = append(recent, at)
			}
		}
		st.RouteHandle, st.Health = "", "UNKNOWN"
		if len(recent) >= hostedRestartLimit {
			st.Status, st.Restarts, st.UpdatedAt = "FAILED", recent, stamp(now)
			_ = h.save(k, st)
			h.mu.Unlock()
			return
		}
		st.Restarts = append(recent, stamp(now))
		st.Status, st.Generation, st.UpdatedAt = "STARTING", st.Generation+1, stamp(now)
		st.InstanceID = hostedInstance(k.digest, st.Generation)
		err := h.save(k, st)
		h.mu.Unlock()
		if err != nil {
			return
		}
	}
}

// runGeneration starts one generation, keeps its health current, and removes its runtime objects when it ends.
func (h *Hosting) runGeneration(ctx context.Context, k *hostedKey, st hostedState) {
	id := st.InstanceID
	defer func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := h.runtime.Stop(cleanCtx, id)
		cancel()
		if err == nil {
			err = h.checkout.Remove(id)
		}
		if err != nil {
			h.mu.Lock()
			h.unavailable = true
			h.mu.Unlock()
		}
	}()
	update := func(status, health, route string) bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		if ctx.Err() != nil || k.state.InstanceID != id {
			return false
		}
		next := k.state
		if next.Status == status && next.Health == health && next.RouteHandle == route {
			return true
		}
		next.Status, next.Health, next.RouteHandle, next.UpdatedAt = status, health, route, stamp(h.now())
		return h.save(k, next) == nil
	}
	profile, ok := h.profiles[st.Version.ProfileID]
	if !ok || !profile.Hosted || profile.Digest() != st.Version.ProfileDigest {
		return
	}
	source, err := h.resolver.Resolve(ctx, st.Version.RunID, st.Version.CheckpointActivityID)
	if err != nil || source.SHA != st.Version.SourceSHA || source.RepositoryIdentityDigest != st.Version.RepositoryIdentityDigest || source.ValidationID != st.Version.ValidationID {
		return
	}
	path, err := h.checkout.Materialize(ctx, id, source)
	if err != nil {
		return
	}
	route, err := h.runtime.StartHosted(ctx, id, path, profile, k.digest)
	if err != nil || !sha256Pattern.MatchString(route) {
		return
	}
	startCtx, cancel := context.WithTimeout(ctx, hostedStartTimeout)
	healthy := false
	for startCtx.Err() == nil && !healthy {
		ok, err := h.runtime.Healthy(startCtx, id, profile)
		if err != nil {
			break
		}
		healthy = ok
		if !ok {
			wait(startCtx, hostedHealthEvery/20)
		}
	}
	cancel()
	if !healthy || !update("READY", "HEALTHY", route) {
		return
	}
	var failingSince time.Time
	for ctx.Err() == nil {
		wait(ctx, hostedHealthEvery)
		if ctx.Err() != nil {
			return
		}
		ok, err := h.runtime.Healthy(ctx, id, profile)
		if err != nil {
			return
		}
		health := "HEALTHY"
		if !ok {
			health = "DEGRADED"
			if failingSince.IsZero() {
				failingSince = h.now()
			}
			if h.now().Sub(failingSince) >= hostedUnhealthyRestart {
				return
			}
		} else {
			failingSince = time.Time{}
		}
		if !update("READY", health, route) {
			return
		}
	}
}

func wait(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

// nightly takes each ready key's daily backup and prunes old ones.
func (h *Hosting) nightly() {
	defer h.wg.Done()
	for h.ctx.Err() == nil {
		h.mu.Lock()
		keys := make([]*hostedKey, 0, len(h.keys))
		for _, k := range h.keys {
			keys = append(keys, k)
		}
		h.mu.Unlock()
		sort.Slice(keys, func(i, j int) bool { return keys[i].digest < keys[j].digest })
		for _, k := range keys {
			h.nightlyBackup(k)
		}
		wait(h.ctx, hostedNightlyEvery)
	}
}

func (h *Hosting) nightlyBackup(k *hostedKey) {
	if !k.op.TryLock() {
		return
	}
	defer k.op.Unlock()
	h.mu.Lock()
	st := k.state
	h.mu.Unlock()
	since := st.LastNightly
	if since == "" {
		since = st.CreatedAt
	}
	if st.Status != "READY" || h.now().Sub(parseTime(since)) < hostedNightly {
		return
	}
	now := h.now()
	b, err := h.backup(h.ctx, k, st, "nightly", jsonDigest([]string{"hosted-backup-v1", k.digest, "nightly", stamp(now)}))
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	st = k.state
	st.Backups = append(append([]serviceapi.HostedBackupV1(nil), st.Backups...), b)
	st = pruneBackups(st, now)
	st.LastNightly, st.UpdatedAt = stamp(now), stamp(now)
	if h.save(k, st) == nil {
		h.removeUnlisted(k.digest, st.Backups)
	}
}

// Close stops every hosted instance and removes its runtime objects. Desired states are kept, so the next start
// runs them again.
func (h *Hosting) Close() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	h.cancel()
	h.mu.Unlock()
	h.wg.Wait()
	return nil
}

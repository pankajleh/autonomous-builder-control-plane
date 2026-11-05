package eviction

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/activity"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/readmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/strictjson"
)

// Forgetting a run (Repo C design note APP_DELETION.md §4): when an app or an account is deleted, the product asks
// ABCP to forget each of the app's finished runs. ABCP seals a record of everything it will remove, then removes the
// run's previews' sources, worktree, refs, inputs, progress, folder, eviction record, evidence bodies, engine session
// logs and activity, and appends RUN_FORGOTTEN to the run's ledger. It keeps the ledger (ids, states, times, hashes),
// the registration, the admission receipts and the record, whose artifact hash manifest is the retention policy's
// minimal record of the evidence (docs/architecture/RUN_EVIDENCE_RETENTION_POLICY.md). The objects the removed refs
// reached go at the nightly git gc (internal/housekeeping).

// Forget outcomes.
const (
	Forgotten        = "FORGOTTEN"         // this request sealed the forget (or replays it)
	AlreadyForgotten = "ALREADY_FORGOTTEN" // another request sealed it
)

var (
	// ErrRunOnHold refuses to forget a run an operator holds: <service-root>/holds/<run> exists.
	ErrRunOnHold = errors.New("run is on hold")
	// ErrInvalidForget rejects a forget the product may not ask for.
	ErrInvalidForget = errors.New("invalid run forget request")
	// ErrForgetConflict refuses a request whose id sealed the run's forget with another reason: a conflicting replay.
	ErrForgetConflict = errors.New("run forget request id conflicts")
)

const (
	// forgetFinishTimeout bounds the removal once a forget is sealed. It runs apart from the request (which the server
	// ends after 30 s), so a request that goes away never interrupts Git half way through a ref or a worktree.
	forgetFinishTimeout  = 15 * time.Minute
	maxForgetRecordBytes = 4 << 20
	maxForgetArtifacts   = 20000
	maxProgressEntries   = 100000
	maxProgressHeader    = 64 << 10
	// RunForgottenEvent is the ledger event a finished forget appends.
	RunForgottenEvent = "RUN_FORGOTTEN"
)

// ForgottenArtifact is one evidence body a forget removes, as the artifact hash manifest keeps it.
type ForgottenArtifact struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// ForgetRecordV1 is <service-root>/forgotten/<run>.json. It is written before anything is removed and never changes
// after, so an interrupted forget is finished by the next call and every call answers it. It holds ids, the run's own
// paths, hashes and a time; never the person's words.
type ForgetRecordV1 struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schema_version"`
	RunID         string `json:"run_id"`
	Reason        string `json:"reason"`
	RequestID     string `json:"request_id"`
	ForgottenAt   string `json:"forgotten_at"`
	// Repository is the product repository the refs are in.
	Repository string `json:"repository"`
	// Refs is every ref of the run to remove, each only while it still names its SHA.
	Refs []serviceapi.ForgottenRefV1 `json:"refs"`
	// Worktree is the run's worktree in Repository, "" when it has none there: a run admitted before builds were
	// walled keeps it there, and so does an exact edit, which never runs in the wall whatever its manifest names.
	Worktree string `json:"worktree"`
	// Inputs is the run's admitted input folder (its plan, capsule and manifest).
	Inputs string `json:"inputs"`
	// RunFolder is the run folder its manifest names (admission names one for every run since builds were walled);
	// "" for a run admitted before. A forget removes both it and Worktree.
	RunFolder string `json:"run_folder"`
	// Evidence is the run's evidence folder: its bodies go, the folder stays.
	Evidence string `json:"evidence"`
	// Sessions are the engines' session folders the run's ledger ties to it (<sessions>/runs/<run>).
	Sessions []string `json:"sessions"`
	// Artifacts is the artifact hash manifest of the evidence bodies removed.
	Artifacts []ForgottenArtifact `json:"artifacts"`
}

func cleanAbsolute(path string) bool { return filepath.IsAbs(path) && filepath.Clean(path) == path }

// sessionFolderOf reports whether path is the run's own session folder: <sessions root>/runs/<run> (run.runSessions).
func sessionFolderOf(run, path string) bool {
	return cleanAbsolute(path) && filepath.Base(path) == run && filepath.Base(filepath.Dir(path)) == "runs"
}

func (r ForgetRecordV1) validate() error {
	if r.Kind != "RunForgetV1" || r.SchemaVersion != 1 || !runtimecatalog.ValidIdentifier(r.RunID) ||
		serviceapi.ValidateRunForgetRequestV1(serviceapi.RunForgetRequestV1{SchemaVersion: 1, Reason: r.Reason, RequestID: r.RequestID}) != nil ||
		!cleanAbsolute(r.Repository) || r.Refs == nil || r.Sessions == nil || r.Artifacts == nil || len(r.Artifacts) > maxForgetArtifacts {
		return ErrIntegrity
	}
	if _, err := time.Parse(time.RFC3339Nano, r.ForgottenAt); err != nil {
		return ErrIntegrity
	}
	for _, ref := range r.Refs {
		if !serviceapi.ForgottenRefOf(r.RunID, ref.Ref) || !gitSHA.MatchString(ref.SHA) {
			return ErrIntegrity
		}
	}
	if r.Worktree != "" && (!cleanAbsolute(r.Worktree) || !inside(r.Repository, r.Worktree) || filepath.Base(r.Worktree) != r.RunID) {
		return ErrIntegrity
	}
	if !cleanAbsolute(r.Inputs) || !inside(r.Repository, r.Inputs) || filepath.Base(r.Inputs) != r.RunID {
		return ErrIntegrity
	}
	if r.RunFolder != "" && (!cleanAbsolute(r.RunFolder) || filepath.Base(r.RunFolder) != r.RunID) {
		return ErrIntegrity
	}
	if !cleanAbsolute(r.Evidence) || filepath.Base(r.Evidence) != r.RunID {
		return ErrIntegrity
	}
	for _, path := range r.Sessions {
		if !sessionFolderOf(r.RunID, path) {
			return ErrIntegrity
		}
	}
	for _, a := range r.Artifacts {
		if a.Name == "" || a.Name == "." || a.Name == ".." || strings.ContainsAny(a.Name, "/\x00") || len(a.SHA256) != 64 || a.Bytes < 0 {
			return ErrIntegrity
		}
	}
	return nil
}

// V1 is the record as the service API answers it, with status.
func (r ForgetRecordV1) V1(status string) serviceapi.RunForgetV1 {
	return serviceapi.RunForgetV1{SchemaVersion: "RunForgetV1", RunID: r.RunID, Status: status, Reason: r.Reason, RequestID: r.RequestID,
		ForgottenAt: r.ForgottenAt, RefsRemoved: append([]serviceapi.ForgottenRefV1{}, r.Refs...)}
}

// ForgetStore keeps one record per forgotten run under <service-root>/forgotten, and reads operators' holds from
// <service-root>/holds: a run is on hold while holds/<run> exists, whatever it holds (its reason, for people).
type ForgetStore struct{ dir, holds string }

func OpenForgetStore(serviceRoot string) (*ForgetStore, error) {
	dir, err := openRecordDirectory(serviceRoot, "forgotten")
	if err != nil {
		return nil, err
	}
	return &ForgetStore{dir: dir, holds: filepath.Join(serviceRoot, "holds")}, nil
}

func (s *ForgetStore) path(run string) (string, error) {
	if s == nil || !runtimecatalog.ValidIdentifier(run) {
		return "", ErrIntegrity
	}
	return filepath.Join(s.dir, run+".json"), nil
}

// Read returns the run's forget record, or ok=false when it was never forgotten.
func (s *ForgetStore) Read(run string) (ForgetRecordV1, bool, error) {
	path, err := s.path(run)
	if err != nil {
		return ForgetRecordV1{}, false, err
	}
	data, ok, err := readRecordFile(path, maxForgetRecordBytes)
	if err != nil || !ok {
		return ForgetRecordV1{}, false, err
	}
	var record ForgetRecordV1
	if strictjson.Decode(data, &record) != nil || record.RunID != run || record.validate() != nil {
		return ForgetRecordV1{}, false, ErrIntegrity
	}
	return record, true, nil
}

// Sealed reports whether the run's forget is sealed, from the record's presence alone: reads ask it on every request.
// A record is written whole by a rename, so one that is there is sealed.
func (s *ForgetStore) Sealed(run string) (bool, error) {
	path, err := s.path(run)
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return false, ErrIntegrity
	}
	return true, nil
}

func (s *ForgetStore) write(record ForgetRecordV1) error {
	if err := record.validate(); err != nil {
		return err
	}
	path, err := s.path(record.RunID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil || len(data)+1 > maxForgetRecordBytes {
		return ErrIntegrity
	}
	return writeRecordFile(s.dir, path, append(data, '\n'))
}

// Held reports whether an operator holds the run (holds/<run> exists). Anything but a clear answer is an error.
func (s *ForgetStore) Held(run string) (bool, error) {
	if s == nil || !runtimecatalog.ValidIdentifier(run) {
		return false, ErrIntegrity
	}
	_, err := os.Lstat(filepath.Join(s.holds, run))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ForgetResult is a forget's answer: its status and its sealed record.
type ForgetResult struct {
	Status string
	Record ForgetRecordV1
}

// Forget forgets a finished run, not on hold, because the product deleted its app or account. The first call seals
// the record and removes everything it names; a call that finds the record finishes what an interruption left and
// answers the same record: FORGOTTEN to the request that sealed it, ALREADY_FORGOTTEN to any other. A run not finished
// is refused (ErrRunNotFinished), as one on hold is (ErrRunOnHold); a hold placed after the record is sealed does not
// stop the forget it promised.
func (s *Sweeper) Forget(ctx context.Context, run, reason, requestID string) (ForgetResult, error) {
	if serviceapi.ValidateRunForgetRequestV1(serviceapi.RunForgetRequestV1{SchemaVersion: 1, Reason: reason, RequestID: requestID}) != nil {
		return ForgetResult{}, ErrInvalidForget
	}
	if s.Forgets == nil || s.Ledgers == nil {
		return ForgetResult{}, ErrIntegrity
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, sealed, err := s.Forgets.Read(run)
	if err != nil {
		return ForgetResult{}, err
	}
	if sealed && record.RequestID == requestID && record.Reason != reason {
		return ForgetResult{}, ErrForgetConflict
	}
	if !sealed {
		if record, err = s.sealForget(ctx, run, reason, requestID); err != nil {
			return ForgetResult{}, err
		}
	}
	// Sealed, the removal is the record's to finish, not the request's: it runs to its own deadline whether or not the
	// request is still there, and a call that comes after it answers the same record.
	finish, cancel := context.WithTimeout(context.WithoutCancel(ctx), forgetFinishTimeout)
	defer cancel()
	if err := s.finishForget(finish, record); err != nil {
		s.logf("abcp run forget unfinished run=%s class=%s", run, class(err))
		return ForgetResult{}, err
	}
	status := AlreadyForgotten
	if record.RequestID == requestID {
		status = Forgotten
	}
	return ForgetResult{Status: status, Record: record}, nil
}

// forgetSealed reports whether the run's forget is sealed, so the sweep and releases leave it alone (they would pin
// refs the record does not name). A record folder that cannot be read is an error, never an answer.
func (s *Sweeper) forgetSealed(run string) (bool, error) {
	if s.Forgets == nil {
		return false, nil
	}
	return s.Forgets.Sealed(run)
}

func (s *Sweeper) sealForget(ctx context.Context, run, reason, requestID string) (ForgetRecordV1, error) {
	scope, err := s.Binder.ResolveBinding(ctx, run)
	if err != nil {
		return ForgetRecordV1{}, err
	}
	snapshot, err := s.States.Snapshot(ctx, run)
	if err != nil {
		return ForgetRecordV1{}, err
	}
	if terminal, _ := finishedIn(snapshot); !terminal {
		return ForgetRecordV1{}, ErrRunNotFinished
	}
	if held, err := s.Forgets.Held(run); err != nil {
		return ForgetRecordV1{}, err
	} else if held {
		return ForgetRecordV1{}, ErrRunOnHold
	}
	registration, err := s.Runs.ReadRun(run)
	if err != nil {
		return ForgetRecordV1{}, err
	}
	refs, err := runRefs(ctx, scope.Repository, run)
	if err != nil {
		return ForgetRecordV1{}, err
	}
	// The run's worktree in the product repository is looked for whatever run folder its manifest names: an exact
	// edit is given one, but never runs in the wall, and keeps its worktree there (run.checkoutExactEdit).
	worktree, err := productWorktree(ctx, scope)
	if err != nil {
		return ForgetRecordV1{}, err
	}
	artifacts, err := evidenceManifest(registration.CanonicalEvidenceRoot, run)
	if err != nil {
		return ForgetRecordV1{}, err
	}
	record := ForgetRecordV1{Kind: "RunForgetV1", SchemaVersion: 1, RunID: run, Reason: reason, RequestID: requestID,
		ForgottenAt: s.Now().UTC().Format(time.RFC3339Nano), Repository: scope.Repository, Refs: refs, Worktree: worktree,
		Inputs: scope.Inputs, RunFolder: scope.RunFolder, Evidence: registration.CanonicalEvidenceRoot,
		Sessions: sessionFolders(snapshot, run), Artifacts: artifacts}
	if err := s.Forgets.write(record); err != nil {
		return ForgetRecordV1{}, err
	}
	s.logf("abcp run forget sealed at=%s run=%s reason=%s refs=%d artifacts=%d sessions=%d", record.ForgottenAt, run, reason, len(refs), len(artifacts), len(record.Sessions))
	return record, nil
}

// finishForget removes everything the record names. Each step does nothing when its part is gone already, so it can
// run again after an interruption at any step.
func (s *Sweeper) finishForget(ctx context.Context, record ForgetRecordV1) error {
	run := record.RunID
	steps := []struct {
		name string
		do   func() error
	}{
		{"previews", func() error { return s.Previews.ForgetPreviews(ctx, run) }},
		{"worktree", func() error { return removeForgottenWorktree(ctx, record) }},
		{"refs", func() error { return removeForgottenRefs(ctx, record) }},
		{"inputs", func() error { return removeForgottenInputs(record) }},
		{"progress", func() error { return removeForgottenProgress(record) }},
		{"eviction-record", func() error { return s.Store.Remove(run) }},
		{"evidence", func() error { return removeEvidenceBodies(record.Evidence) }},
		{"sessions", func() error {
			for _, path := range record.Sessions {
				if err := removeRunFolder(path); err != nil {
					return err
				}
			}
			return nil
		}},
		{"activity", func() error { return s.Activity.ForgetActivity(run) }},
		{"ledger", func() error { return s.appendForgotten(ctx, record) }},
	}
	for _, step := range steps {
		if err := step.do(); err != nil {
			s.logf("abcp run forget step failed run=%s step=%s class=%s", run, step.name, class(err))
			return err
		}
	}
	s.logf("abcp run forgotten run=%s reason=%s refs=%d artifacts=%d", run, record.Reason, len(record.Refs), len(record.Artifacts))
	return nil
}

// runRefs lists the run's refs in the repository with their SHAs: refs/heads/abcp/<run> and refs/abcp/<kind>/<run>/….
func runRefs(ctx context.Context, repository, run string) ([]serviceapi.ForgottenRefV1, error) {
	all, err := listRunRefs(ctx, repository, run)
	if err != nil {
		return nil, err
	}
	refs := make([]serviceapi.ForgottenRefV1, 0, len(all))
	for ref, sha := range all {
		refs = append(refs, serviceapi.ForgottenRefV1{Ref: ref, SHA: sha})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Ref < refs[j].Ref })
	return refs, nil
}

func listRunRefs(ctx context.Context, repository, run string) (map[string]string, error) {
	out, err := git(ctx, repository, "for-each-ref", "--format=%(refname)%00%(objectname)", "refs/heads/abcp/"+run, "refs/abcp/")
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		ref, sha, ok := strings.Cut(line, "\x00")
		if !ok || !gitSHA.MatchString(sha) {
			return nil, ErrIntegrity
		}
		if serviceapi.ForgottenRefOf(run, ref) {
			refs[ref] = sha
		}
	}
	return refs, nil
}

// removeForgottenRefs deletes each ref the record names, with its reflog, only while it names the record's SHA. A ref
// gone already is skipped; one that names another SHA stops the forget (someone moved it after the record was sealed).
func removeForgottenRefs(ctx context.Context, record ForgetRecordV1) error {
	if len(record.Refs) == 0 {
		return nil
	}
	current, err := listRunRefs(ctx, record.Repository, record.RunID)
	if err != nil {
		return err
	}
	for _, ref := range record.Refs {
		sha, present := current[ref.Ref]
		if !present {
			continue
		}
		if sha != ref.SHA {
			return ErrIntegrity
		}
		if _, err := git(ctx, record.Repository, "update-ref", "--no-deref", "-d", ref.Ref, ref.SHA); err != nil {
			return err
		}
	}
	return nil
}

// productWorktree is the run's worktree in the product repository, "" when it has none there. Git says where the run's
// branch is checked out; a worktree off its branch, or one Git no longer lists, is still the run's at Ralphex's path
// for it.
func productWorktree(ctx context.Context, scope activity.Scope) (string, error) {
	product := scope
	product.RunFolder, product.RunRepository = "", ""
	path, err := activity.WorktreePath(ctx, product)
	if err != nil || path != "" {
		return path, err
	}
	conventional := conventionalWorktree(scope.Repository, scope.RunID)
	if _, err := os.Lstat(conventional); err == nil {
		return conventional, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return "", nil
}

// conventionalWorktree is where Ralphex, and an exact edit, keep the run's worktree in repository.
func conventionalWorktree(repository, run string) string {
	return filepath.Join(repository, ".ralphex", "worktrees", "abcp", run)
}

// removeForgottenWorktree removes the run's folder and its worktree in the product repository (Git first, then
// whatever Git left, never through a link), and prunes the repository's worktree list. It fails while Git still lists
// a worktree of the run: its index would keep the run's objects through gc, and its branch could not be deleted.
func removeForgottenWorktree(ctx context.Context, record ForgetRecordV1) error {
	if record.RunFolder != "" {
		if err := removeRunFolder(record.RunFolder); err != nil {
			return err
		}
	}
	if record.Worktree != "" {
		if _, err := os.Lstat(record.Worktree); err == nil {
			// A locked worktree needs the second --force; a failure leaves the folder to the removal below.
			_, _ = git(ctx, record.Repository, "worktree", "remove", "--force", "--force", "--", record.Worktree)
		}
		if err := removeRunFolder(record.Worktree); err != nil {
			return err
		}
	}
	if _, err := git(ctx, record.Repository, "worktree", "prune"); err != nil {
		return err
	}
	listed, err := listedWorktrees(ctx, record)
	if err != nil || len(listed) == 0 {
		return err
	}
	// Git does not prune a locked worktree whose folder is gone: unlock it, then prune again.
	for _, path := range listed {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			_, _ = git(ctx, record.Repository, "worktree", "unlock", "--", path)
		}
	}
	if _, err := git(ctx, record.Repository, "worktree", "prune"); err != nil {
		return err
	}
	if listed, err = listedWorktrees(ctx, record); err != nil {
		return err
	}
	if len(listed) != 0 {
		return ErrIntegrity
	}
	return nil
}

// listedWorktrees are the worktrees of the product repository that are the run's: at the record's worktree, at the
// run's conventional place, or holding its branch.
func listedWorktrees(ctx context.Context, record ForgetRecordV1) ([]string, error) {
	out, err := git(ctx, record.Repository, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	conventional := conventionalWorktree(record.Repository, record.RunID)
	var listed []string
	path := ""
	for _, field := range strings.Split(out, "\x00") {
		switch {
		case strings.HasPrefix(field, "worktree "):
			path = strings.TrimPrefix(field, "worktree ")
			if path == conventional || (record.Worktree != "" && path == record.Worktree) {
				listed = append(listed, path)
			}
		case field == "branch refs/heads/abcp/"+record.RunID && path != "":
			if len(listed) == 0 || listed[len(listed)-1] != path {
				listed = append(listed, path)
			}
		}
	}
	return listed, nil
}

// removeForgottenInputs removes the run's admitted inputs and the plan copy Ralphex was given in the product repository
// (a run admitted before builds were walled; normally gone when Ralphex exits, and named by the run's digest alone).
func removeForgottenInputs(record ForgetRecordV1) error {
	if err := removeRunFolder(record.Inputs); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(record.RunID))
	return removeRunFolder(filepath.Join(record.Repository, ralphex.ExecutionPlanHandoffPrefixV1+hex.EncodeToString(sum[:])))
}

// removeForgottenProgress removes the progress logs of a run admitted before builds were walled: they are in the
// product repository's .ralphex/progress (with Ralphex's archives of them), shared by its runs, so a log is the run's
// when its header names the run's branch (Ralphex's "Branch: " line, before the header's "---" line). A walled run's
// progress is in its folder, gone with it.
func removeForgottenProgress(record ForgetRecordV1) error {
	if record.RunFolder != "" {
		return nil
	}
	root := filepath.Join(record.Repository, ".ralphex", "progress")
	if _, err := os.Lstat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	want := "Branch: abcp/" + record.RunID
	entries := 0
	var mine []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entries++; entries > maxProgressEntries {
			return ErrIntegrity
		}
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".txt") {
			named, err := progressNamesBranch(path, want)
			if err != nil {
				return err
			}
			if named {
				mine = append(mine, path)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, path := range mine {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func progressNamesBranch(path, want string) (bool, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, maxProgressHeader))
	scanner.Buffer(make([]byte, 0, 4096), maxProgressHeader)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if strings.HasPrefix(line, "---") {
			return false, nil
		}
		if line == want {
			return true, nil
		}
	}
	return false, nil
}

// evidenceManifest hashes every evidence body of the run, the manifest the record keeps once they are gone.
func evidenceManifest(dir, run string) ([]ForgottenArtifact, error) {
	artifacts := []ForgottenArtifact{}
	if !cleanAbsolute(dir) || filepath.Base(dir) != run {
		return nil, ErrIntegrity
	}
	entries, err := evidenceEntries(dir)
	if err != nil || entries == nil {
		return artifacts, err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		file, err := os.OpenFile(filepath.Join(dir, entry.Name()), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			return nil, err
		}
		hash := sha256.New()
		n, err := io.Copy(hash, file)
		file.Close()
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, ForgottenArtifact{Name: entry.Name(), SHA256: hex.EncodeToString(hash.Sum(nil)), Bytes: n})
	}
	return artifacts, nil
}

// evidenceEntries lists the run's evidence folder, nil when there is none. It must be a real folder, never a link.
func evidenceEntries(dir string) ([]fs.DirEntry, error) {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, ErrIntegrity
	}
	if resolved, err := filepath.EvalSymlinks(dir); err != nil || resolved != dir {
		return nil, ErrIntegrity
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	if len(entries) > maxForgetArtifacts {
		return nil, ErrIntegrity
	}
	return entries, nil
}

// removeEvidenceBodies removes everything in the run's evidence folder, never through a link; the folder stays.
func removeEvidenceBodies(dir string) error {
	entries, err := evidenceEntries(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	if entries != nil {
		return syncDirectory(dir)
	}
	return nil
}

// sessionFolders are the engine session folders the run's ledger ties to it: a walled run's start records its
// isolation, whose "sessions" is <the engine's sessions>/runs/<run> (run.prepareEngine). A value of any other shape is
// not the run's and is left alone.
func sessionFolders(snapshot readmodel.Snapshot, run string) []string {
	seen := map[string]bool{}
	folders := []string{}
	for _, event := range snapshot.Events {
		isolation, ok := event.Payload["isolation"].(map[string]any)
		if !ok {
			continue
		}
		path, ok := isolation["sessions"].(string)
		if !ok || !sessionFolderOf(run, path) || seen[path] {
			continue
		}
		seen[path] = true
		folders = append(folders, path)
	}
	sort.Strings(folders)
	return folders
}

// forgottenEvent is RUN_FORGOTTEN for the record: the same event every time, so appending it again is a no-op. Its
// payload is a fixed reason word, the request's UUID and counts.
func forgottenEvent(record ForgetRecordV1) (ledger.Event, error) {
	at, err := time.Parse(time.RFC3339Nano, record.ForgottenAt)
	if err != nil {
		return ledger.Event{}, ErrIntegrity
	}
	sum := sha256.Sum256([]byte(RunForgottenEvent + "\x00" + record.RunID + "\x00" + record.RequestID))
	event := ledger.Event{SchemaVersion: ledger.CurrentSchemaVersion, EventID: hex.EncodeToString(sum[:16]), Timestamp: at.UTC(),
		RunID: record.RunID, EventType: RunForgottenEvent, Actor: "control-plane", Source: "run-forget",
		Payload: map[string]any{"reason": record.Reason, "request_id": record.RequestID, "refs_removed": len(record.Refs), "artifacts_removed": len(record.Artifacts)}}
	return event, event.Validate()
}

func (s *Sweeper) appendForgotten(ctx context.Context, record ForgetRecordV1) error {
	event, err := forgottenEvent(record)
	if err != nil {
		return err
	}
	return s.Ledgers.WithExistingWritableLedgerAndSnapshot(ctx, record.RunID, func(writer *ledger.JSONLLedger, _ func() (readmodel.Snapshot, error)) error {
		return writer.AppendOrVerify(event)
	})
}

// Package housekeeping runs ABCP's nightly clean-up (Repo C design note APP_DELETION.md §4 and decision 11): a git gc
// of each product repository named, so the objects only a forgotten run's refs reached go once their grace has passed,
// and the removal of finished builds' engine session logs older than 30 days from the session folders named (a forget
// removes a forgotten run's at once).
package housekeeping

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/gitexec"
)

const (
	// PruneGrace is git gc's --prune: an unreachable object younger than 14 days stays, so a running build's new
	// objects are safe, and a forgotten run's objects go within 14 days of its refs (APP_DELETION.md §3.4).
	PruneGrace = "14.days.ago"
	// SessionLogsMaxAge is how long an engine's session log no run's records tie to it is kept (decision 11).
	SessionLogsMaxAge = 30 * 24 * time.Hour
	// DefaultAt is when the nightly clean-up runs, in UTC: 04:47 in India, after the code backup (21:43 UTC) and
	// before the day's builds.
	DefaultAt = "23:17"

	gcTimeout         = 2 * time.Hour
	gitWaitDelay      = 10 * time.Second
	lockWait          = 30 * time.Minute
	maxSessionEntries = 2_000_000
)

var errInvalid = errors.New("housekeeping: invalid setting")

// Locker takes a repository's execution lock and answers its release (run.LockRepository).
type Locker func(ctx context.Context, repository string) (io.Closer, error)

// SessionPolicy says what of a session folder the 30-day rule reaches.
type SessionPolicy int

const (
	// BuildsOnly is the rule for a folder people use too (the server's own ~/.codex/sessions, the projects of the
	// Claude config folder builds on the server's own sign-in use): only builds' logs in it age.
	BuildsOnly SessionPolicy = iota
	// LaneFolder is the rule for a build lane's own folder, which only ABCP's builds and change jobs write: everything in
	// it ages but a build's that has not finished and memory/.
	LaneFolder
)

func (p SessionPolicy) String() string {
	if p == LaneFolder {
		return "lane"
	}
	return "builds"
}

// SessionSweep counts one session folder's sweep: the files removed, and the entries it could not read or remove,
// skipped (the first one's error is the sweep's).
type SessionSweep struct{ Removed, Skipped int }

// Nightly runs the clean-up once a day at At (time after midnight, UTC).
type Nightly struct {
	At           time.Duration
	Repositories []string
	// SessionLogs are folders people use too (BuildsOnly); LaneSessionLogs are build lanes' own (LaneFolder).
	SessionLogs     []string
	LaneSessionLogs []string
	Lock            Locker
	// Finished reports whether a run is finished; a run it cannot tell is not. Only a finished run's session logs
	// are aged.
	Finished func(run string) bool
	Now      func() time.Time
	Log      io.Writer
}

// ParseAt reads a time of day "HH:MM" (UTC) as the time after midnight.
func ParseAt(value string) (time.Duration, error) {
	hours, minutes, ok := strings.Cut(value, ":")
	h, herr := strconv.Atoi(hours)
	m, merr := strconv.Atoi(minutes)
	if !ok || len(hours) != 2 || len(minutes) != 2 || herr != nil || merr != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, fmt.Errorf("%w: a time of day is HH:MM (UTC)", errInvalid)
	}
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute, nil
}

// ValidateRepository accepts a product repository to gc: an absolute, clean path.
func ValidateRepository(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("%w: a repository is an absolute, clean path", errInvalid)
	}
	return nil
}

// ValidateSessionLogs accepts an engine's session folder: an absolute, clean path named sessions (Codex's) or projects
// (Claude Code's), so a mistyped engine home, with its sign-in and settings, is never named. Even so, only the builds'
// logs in it are aged (RemoveOldSessionLogs).
func ValidateSessionLogs(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || (filepath.Base(path) != "sessions" && filepath.Base(path) != "projects") {
		return fmt.Errorf("%w: a session folder is an absolute, clean path named sessions or projects", errInvalid)
	}
	return nil
}

// Enabled reports whether there is anything to do.
func (n *Nightly) Enabled() bool {
	return len(n.Repositories) > 0 || len(n.SessionLogs) > 0 || len(n.LaneSessionLogs) > 0
}

// Run runs Once at At every day until ctx ends.
func (n *Nightly) Run(ctx context.Context) {
	for {
		timer := time.NewTimer(untilNext(n.Now(), n.At))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		n.Once(ctx)
	}
}

// untilNext is how long from now until the next at after midnight UTC.
func untilNext(now time.Time, at time.Duration) time.Duration {
	now = now.UTC()
	next := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Add(at)
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next.Sub(now)
}

// Once gcs each repository and sweeps each session folder, one log line each: counts, never a name in them.
func (n *Nightly) Once(ctx context.Context) {
	for _, repository := range n.Repositories {
		started := n.Now()
		err := GC(ctx, repository, n.Lock)
		n.logf("abcp nightly git gc repository=%q ok=%t took=%s%s", repository, err == nil, n.Now().Sub(started).Round(time.Second), failure(err))
	}
	for _, folders := range []struct {
		roots  []string
		policy SessionPolicy
	}{{n.SessionLogs, BuildsOnly}, {n.LaneSessionLogs, LaneFolder}} {
		for _, root := range folders.roots {
			swept, err := RemoveOldSessionLogs(root, n.Now(), SessionLogsMaxAge, folders.policy, n.Finished)
			n.logf("abcp nightly session logs folder=%q policy=%s ok=%t removed=%d skipped=%d%s", root, folders.policy, err == nil, swept.Removed, swept.Skipped, failure(err))
		}
	}
}

func failure(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return " class=interrupted"
	}
	return " class=failed"
}

func (n *Nightly) logf(format string, args ...any) {
	if n.Log != nil {
		fmt.Fprintf(n.Log, format+"\n", args...)
	}
}

// GC runs `git gc --prune=14.days.ago` on repository under nice and ionice (idle class) and under its execution lock,
// waiting up to 30 minutes for a run's setup to give the lock back.
func GC(ctx context.Context, repository string, lock Locker) error {
	if err := ValidateRepository(repository); err != nil {
		return err
	}
	if lock == nil {
		return errInvalid
	}
	lockCtx, cancel := context.WithTimeout(ctx, lockWait)
	held, err := lock(lockCtx, repository)
	cancel()
	if err != nil {
		return err
	}
	defer held.Close()
	gcCtx, cancel := context.WithTimeout(ctx, gcTimeout)
	defer cancel()
	// ionice -t runs git even where the idle class cannot be set; nice still applies. Both exec git, so an interrupted
	// gc (ABCP stopping, or the 2 hours over) is git, asked to stop with SIGTERM, which removes its lock files (gc.pid,
	// packed-refs.lock), and killed only if it has not within gitWaitDelay.
	cmd := exec.CommandContext(gcCtx, "nice", "-n", "19", "ionice", "-c", "3", "-t", "git", "-C", repository, "gc", "--quiet", "--prune="+PruneGrace)
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = gitWaitDelay
	cmd.Env = gitexec.Environment()
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Run()
}

// RemoveOldSessionLogs applies the 30-day rule (maxAge) in root, an engine's session folder, as policy says
// (APP_DELETION.md decision 11). In every folder it reaches:
//
//   - runs/<run>/, a walled build's sessions (bound into its wall), of a run finished says is finished: a running
//     build's folder is never walked, and a forgotten run's is removed by its forget;
//   - a Claude Code project folder named after a build's working folder (…-worktrees-abcp-admission-<id>…, a build
//     from before builds were walled) of a finished run;
//   - a Codex log in a dated folder (YYYY/MM/DD/rollout-….jsonl) whose first record says codex exec ran it in a
//     finished build's working folder (…/worktrees/abcp/admission-<id>, the folder Repo C's meter ties it by).
//
// BuildsOnly reaches nothing else: a person's own sessions stay. LaneFolder reaches everything else in the folder too
// (a change job's sessions, a lane's own), but a log or folder of a build that has not finished. A memory/ folder is
// never touched. In what it reaches, each file (or link, never followed) last changed before now-maxAge goes, then each
// folder left empty that was last changed before then. The walk opens each folder from its parent's descriptor without
// following links and removes only names in the folder it holds open, so a build that swaps a folder for a link while it
// is walked can never lead it outside. An entry it cannot read or remove is skipped and counted, and the walk goes on;
// the first such error is answered with the counts. A root that is not there has nothing to remove.
func RemoveOldSessionLogs(root string, now time.Time, maxAge time.Duration, policy SessionPolicy, finished func(run string) bool) (SessionSweep, error) {
	if err := ValidateSessionLogs(root); err != nil || maxAge <= 0 || (policy != BuildsOnly && policy != LaneFolder) {
		return SessionSweep{}, errInvalid
	}
	if finished == nil {
		finished = func(string) bool { return false }
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return SessionSweep{}, nil
	}
	if err != nil {
		return SessionSweep{}, err
	}
	if !info.IsDir() {
		return SessionSweep{}, errInvalid
	}
	if resolved, err := filepath.EvalSymlinks(root); err != nil || resolved != root {
		return SessionSweep{}, errInvalid
	}
	return removeOldSessionLogs(root, now.Add(-maxAge), policy, finished)
}

var (
	// buildProject is a Claude Code project folder named after a build's working folder: the folder's path with every
	// character but a letter or a digit made "-".
	buildProject = regexp.MustCompile(`-worktrees-abcp-(admission-[0-9a-f]{64})(?:-|$)`)
	// buildFolder is a build's working folder, as a Codex log's first record names it.
	buildFolder = regexp.MustCompile(`/worktrees/abcp/(admission-[0-9a-f]{64})(?:/|$)`)
	digits      = regexp.MustCompile(`^[0-9]+$`)
)

// codexLogRun is the build run whose working folder a Codex log's first record (session_meta) names, "" for none. Only
// codex exec's own logs (originator codex_exec, as Ralphex runs it) are a build's: a person's interactive session started
// in a build's folder (codex_cli_rs) is not.
func codexLogRun(first []byte) string {
	var record struct {
		Type    string `json:"type"`
		Payload struct {
			Cwd        string `json:"cwd"`
			Originator string `json:"originator"`
		} `json:"payload"`
	}
	if json.Unmarshal(first, &record) != nil || record.Type != "session_meta" || record.Payload.Originator != "codex_exec" {
		return ""
	}
	if m := buildFolder.FindStringSubmatch(record.Payload.Cwd); m != nil {
		return m[1]
	}
	return ""
}

// datedFolder reports whether name is a Codex dated folder's name at depth (1: YYYY, 2: MM, 3: DD).
func datedFolder(name string, depth int) bool {
	return digits.MatchString(name) && len(name) == map[int]int{1: 4, 2: 2, 3: 2}[depth]
}

//go:build linux

package cilifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
)

type durableFixture struct {
	controller  *Controller
	github      *githubFixture
	authority   githublifecycle.Authority
	attemptRoot string
	artifacts   *evidence.Store
	ledger      *ledger.JSONLLedger
	server      *httptest.Server
}

func newDurableFixture(t *testing.T, mode fixtureMode) *durableFixture {
	t.Helper()
	authority := testCollectionAuthority(t)
	github := &githubFixture{t: t, authority: authority, mode: mode}
	server := httptest.NewServer(github)
	t.Cleanup(server.Close)
	clock := newIncrementingClock()
	adapter, err := newGitHubAdapter(server.Client().Transport, sealedTestAuth, server.URL, productionLimits(), clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	attemptRoot := newAttemptRoot(t)
	artifacts, err := evidence.NewStore(filepath.Join(t.TempDir(), "evidence"), "run-ci")
	if err != nil {
		t.Fatal(err)
	}
	ledgerRoot := t.TempDir()
	if err := os.Chmod(ledgerRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	material, err := ledger.NewJSONLLedger(filepath.Join(ledgerRoot, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	controller, err := NewController(ControllerConfig{
		AttemptRoot: attemptRoot, GitHub: adapter, Artifacts: artifacts, AuthoritativeLedger: material,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	return &durableFixture{controller, github, authority, attemptRoot, artifacts, material, server}
}

func newAttemptRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "capacity.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func durableRequest(fixture *durableFixture, attempt string) CollectRequest {
	return CollectRequest{RunID: "run-ci", AttemptID: attempt, Authority: fixture.authority}
}

func TestControllerPublishesLedgerOutcomeAndReplaysWithoutNetwork(t *testing.T) {
	fixture := newDurableFixture(t, modeSuccess)
	request := durableRequest(fixture, "publish-replay")
	first, err := fixture.controller.Collect(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Input().Outcome != OutcomeStable || fixture.github.requests.Load() != 10 {
		t.Fatalf("first collection outcome=%s requests=%d", first.Input().Outcome, fixture.github.requests.Load())
	}
	identity, requestErr := validateCollectRequest(request)
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	reservationPath := filepath.Join(fixture.attemptRoot, attemptReservationFilename(identity.attemptKey))
	reservationBytes, err := os.ReadFile(reservationPath)
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := parseAttemptReservation(reservationBytes)
	if err != nil || reservation.AttemptID != request.AttemptID {
		t.Fatalf("reservation=%#v error=%v", reservation, err)
	}
	bundlePath := filepath.Join(fixture.artifacts.RunDir(), attemptBundleName(identity.attemptKey))
	if data, err := os.ReadFile(bundlePath); err != nil || !bytes.Equal(data, first.CanonicalJSON()) {
		t.Fatalf("published bundle mismatch: %v", err)
	}
	line, err := os.ReadFile(fixture.ledger.Path())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(line, []byte{'\n'}) != 1 {
		t.Fatalf("ledger lines=%d", bytes.Count(line, []byte{'\n'}))
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSuffix(line, []byte{'\n'}), &raw); err != nil {
		t.Fatal(err)
	}
	if _, present := raw["state_from"]; present {
		t.Fatal("CI outcome event contains a transition source")
	}
	if _, present := raw["state_to"]; present {
		t.Fatal("CI outcome event contains a transition target")
	}
	observed, found, err := fixture.controller.material.find(outcomeEventID(identity.attemptKey))
	if err != nil || !found {
		t.Fatalf("outcome event missing: %v", err)
	}
	if observed.event.EvidenceRefs[0].SHA256 != first.SHA256() || observed.event.Timestamp.UnixNano() != first.Input().AttemptEndedUnixNano {
		t.Fatalf("outcome event not bound to bundle: %#v", observed.event)
	}
	second, err := fixture.controller.Collect(context.Background(), request)
	if err != nil || second.SHA256() != first.SHA256() || fixture.github.requests.Load() != 10 {
		t.Fatalf("replay bundle=%s error=%v requests=%d", second.SHA256(), err, fixture.github.requests.Load())
	}
}

func TestControllerReplaysCompletedNonStableOutcome(t *testing.T) {
	fixture := newDurableFixture(t, modeRerequest)
	request := durableRequest(fixture, "unstable-replay")
	first, firstErr := fixture.controller.Collect(context.Background(), request)
	var typed *CollectionError
	if !errors.As(firstErr, &typed) || typed.Outcome != OutcomeUnstable || len(first.CanonicalJSON()) == 0 {
		t.Fatalf("first result=%s error=%#v", first.CanonicalJSON(), firstErr)
	}
	requests := fixture.github.requests.Load()
	second, secondErr := fixture.controller.Collect(context.Background(), request)
	if !errors.As(secondErr, &typed) || typed.Outcome != OutcomeUnstable || second.SHA256() != first.SHA256() || fixture.github.requests.Load() != requests {
		t.Fatalf("replay result=%s error=%#v requests=%d", second.SHA256(), secondErr, fixture.github.requests.Load())
	}
}

func TestUnsupportedPrincipalIsRejectedBeforeDurableOrNetworkWork(t *testing.T) {
	fixture := newDurableFixture(t, modeSuccess)
	actors := []githublifecycle.ActingIdentity{}
	app, err := githublifecycle.NewAppInstallationIdentity("github-app:builder", 90210)
	if err != nil {
		t.Fatal(err)
	}
	actors = append(actors, app)
	malformed, err := githublifecycle.NewUserIdentity("github-user-id:042")
	if err != nil {
		t.Fatal(err)
	}
	actors = append(actors, malformed)
	for index, actor := range actors {
		input := fixture.authority.Input()
		input.Actor = actor
		authority, err := githublifecycle.NewAuthority(input)
		if err != nil {
			t.Fatal(err)
		}
		bundle, collectErr := fixture.controller.Collect(context.Background(), CollectRequest{
			RunID: "run-ci", AttemptID: "unsupported-" + strconv.Itoa(index), Authority: authority,
		})
		var typed *CollectionError
		if !errors.As(collectErr, &typed) || typed.Outcome != OutcomeUnsupportedPrincipal || len(bundle.CanonicalJSON()) != 0 {
			t.Fatalf("unsupported principal result=%s error=%#v", bundle.CanonicalJSON(), collectErr)
		}
	}
	if fixture.github.requests.Load() != 0 {
		t.Fatalf("unsupported principals reached GitHub: %d", fixture.github.requests.Load())
	}
	entries, err := os.ReadDir(fixture.attemptRoot)
	if err != nil || len(entries) != 1 || entries[0].Name() != "capacity.lock" {
		t.Fatalf("unsupported principals reached allocator: entries=%v error=%v", entries, err)
	}
	artifacts, err := os.ReadDir(fixture.artifacts.RunDir())
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("unsupported principals reached evidence: entries=%v error=%v", artifacts, err)
	}
	if _, err := os.Lstat(fixture.ledger.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsupported principals reached ledger: %v", err)
	}
}

func TestBundleWithoutEventFinishesSameAttemptWithoutNetwork(t *testing.T) {
	fixture := newDurableFixture(t, modeSuccess)
	request := durableRequest(fixture, "crash-after-bundle")
	fixture.controller.material.appendFault = func(stage string) error {
		if stage == "before_append" {
			return errors.New("injected pre-append crash")
		}
		return nil
	}
	first, firstErr := fixture.controller.Collect(context.Background(), request)
	if len(first.CanonicalJSON()) != 0 || firstErr == nil {
		t.Fatalf("incomplete attempt became authoritative: bundle=%s error=%v", first.CanonicalJSON(), firstErr)
	}
	requests := fixture.github.requests.Load()
	fixture.controller.material.appendFault = nil
	recovered, err := fixture.controller.Collect(context.Background(), request)
	if err != nil || recovered.Input().Outcome != OutcomeStable || fixture.github.requests.Load() != requests {
		t.Fatalf("same-attempt recovery bundle=%s error=%v requests=%d", recovered.CanonicalJSON(), err, fixture.github.requests.Load())
	}
	entries, err := os.ReadDir(fixture.attemptRoot)
	if err != nil || len(entries) != 2 {
		t.Fatalf("recovery consumed another reservation: entries=%d error=%v", len(entries), err)
	}
}

func TestZeroAndPartialReservationsRecoverInPlace(t *testing.T) {
	for _, size := range []int{0, 17} {
		t.Run(fmtTestName("bytes", size), func(t *testing.T) {
			fixture := newDurableFixture(t, modeEmpty)
			request := durableRequest(fixture, "repair-reservation")
			identity, requestErr := validateCollectRequest(request)
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			expected, err := newAttemptReservation(request, identity).canonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(fixture.attemptRoot, attemptReservationFilename(identity.attemptKey))
			if err := os.WriteFile(path, expected[:size], 0o600); err != nil {
				t.Fatal(err)
			}
			bundle, err := fixture.controller.Collect(context.Background(), request)
			if err != nil || bundle.Input().Outcome != OutcomeStable {
				t.Fatalf("reservation recovery bundle=%s error=%v", bundle.CanonicalJSON(), err)
			}
			actual, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(actual, expected) {
				t.Fatalf("reservation was not repaired exactly: %v", err)
			}
		})
	}
}

func fmtTestName(prefix string, value int) string {
	return prefix + "-" + strconv.Itoa(value)
}

func TestAttemptAllocatorCapacityAndReservationLock(t *testing.T) {
	authority := testCollectionAuthority(t)
	root := newAttemptRoot(t)
	store, err := newAttemptStore(root, attemptLimits{maxPerRun: 1, maxGlobal: 2, maxBytes: 2 * MaxCompletedAttemptBytes})
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	requestA := CollectRequest{RunID: "run-a", AttemptID: "one", Authority: authority}
	identityA, _ := validateCollectRequest(requestA)
	lease, err := store.acquire(newAttemptReservation(requestA, identityA))
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(root, attemptReservationFilename(identityA.attemptKey))
	other, err := os.OpenFile(lockPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Fatal("reservation file was not the held attempt lock")
	}
	_ = other.Close()
	if _, err := store.acquire(newAttemptReservation(requestA, identityA)); err == nil {
		t.Fatal("same-process same-attempt concurrency was accepted")
	}
	_ = lease.close()

	requestSameRun := CollectRequest{RunID: "run-a", AttemptID: "two", Authority: authority}
	identitySameRun, _ := validateCollectRequest(requestSameRun)
	if _, err := store.acquire(newAttemptReservation(requestSameRun, identitySameRun)); err == nil {
		t.Fatal("per-run attempt capacity was not enforced")
	}
	requestB := CollectRequest{RunID: "run-b", AttemptID: "one", Authority: authority}
	identityB, _ := validateCollectRequest(requestB)
	leaseB, err := store.acquire(newAttemptReservation(requestB, identityB))
	if err != nil {
		t.Fatal(err)
	}
	_ = leaseB.close()
	requestC := CollectRequest{RunID: "run-c", AttemptID: "one", Authority: authority}
	identityC, _ := validateCollectRequest(requestC)
	if _, err := store.acquire(newAttemptReservation(requestC, identityC)); err == nil {
		t.Fatal("global attempt capacity was not enforced")
	}

	byteRoot := newAttemptRoot(t)
	byteStore, err := newAttemptStore(byteRoot, attemptLimits{maxPerRun: 2, maxGlobal: 2, maxBytes: MaxCompletedAttemptBytes})
	if err != nil {
		t.Fatal(err)
	}
	defer byteStore.close()
	first, err := byteStore.acquire(newAttemptReservation(requestA, identityA))
	if err != nil {
		t.Fatal(err)
	}
	_ = first.close()
	if _, err := byteStore.acquire(newAttemptReservation(requestB, identityB)); err == nil {
		t.Fatal("reserved-byte capacity was not enforced")
	}
}

func TestAttemptReservationFlockAcrossProcesses(t *testing.T) {
	if os.Getenv("ABCP_CI_FLOCK_HELPER") == "1" {
		file, err := os.OpenFile(os.Getenv("ABCP_CI_FLOCK_PATH"), os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			t.Fatal("child process acquired a held attempt reservation")
		}
		return
	}
	authority := testCollectionAuthority(t)
	root := newAttemptRoot(t)
	store, err := newAttemptStore(root, productionAttemptLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	request := CollectRequest{RunID: "run", AttemptID: "multiprocess", Authority: authority}
	identity, _ := validateCollectRequest(request)
	lease, err := store.acquire(newAttemptReservation(request, identity))
	if err != nil {
		t.Fatal(err)
	}
	defer lease.close()
	command := exec.Command(os.Args[0], "-test.run=^TestAttemptReservationFlockAcrossProcesses$")
	command.Env = append(os.Environ(), "ABCP_CI_FLOCK_HELPER=1", "ABCP_CI_FLOCK_PATH="+filepath.Join(root, attemptReservationFilename(identity.attemptKey)))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("flock helper: %v: %s", err, output)
	}
}

func TestConcurrentSameAndDifferentAttempts(t *testing.T) {
	t.Run("same-attempt", func(t *testing.T) {
		fixture := newDurableFixture(t, modeSuccess)
		request := durableRequest(fixture, "same-concurrent")
		started := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		original := fixture.server.Config.Handler
		fixture.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/user" {
				once.Do(func() { close(started) })
				<-release
			}
			original.ServeHTTP(w, r)
		})
		result := make(chan error, 1)
		go func() {
			_, err := fixture.controller.Collect(context.Background(), request)
			result <- err
		}()
		<-started
		if bundle, err := fixture.controller.Collect(context.Background(), request); err == nil || len(bundle.CanonicalJSON()) != 0 {
			t.Fatalf("contending same attempt was not busy: bundle=%s error=%v", bundle.CanonicalJSON(), err)
		}
		close(release)
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		if fixture.github.requests.Load() != 10 {
			t.Fatalf("same attempt duplicated network collection: %d", fixture.github.requests.Load())
		}
	})

	t.Run("different-attempts", func(t *testing.T) {
		fixture := newDurableFixture(t, modeSuccess)
		requests := []CollectRequest{durableRequest(fixture, "parallel-a"), durableRequest(fixture, "parallel-b")}
		errorsSeen := make(chan error, len(requests))
		var group sync.WaitGroup
		for _, request := range requests {
			request := request
			group.Add(1)
			go func() {
				defer group.Done()
				_, err := fixture.controller.Collect(context.Background(), request)
				errorsSeen <- err
			}()
		}
		group.Wait()
		close(errorsSeen)
		for err := range errorsSeen {
			if err != nil {
				t.Fatal(err)
			}
		}
		if fixture.github.requests.Load() != 20 {
			t.Fatalf("different attempts did not collect independently: %d", fixture.github.requests.Load())
		}
	})
}

func TestAttemptAllocatorRejectsUnsafeObjects(t *testing.T) {
	tests := map[string]func(*testing.T, string){
		"unknown": func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, "foreign"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"symlink": func(t *testing.T, root string) {
			if err := os.Symlink("capacity.lock", filepath.Join(root, "ci-"+strings.Repeat("a", 64)+".reservation")); err != nil {
				t.Fatal(err)
			}
		},
		"fifo": func(t *testing.T, root string) {
			if err := syscall.Mkfifo(filepath.Join(root, "ci-"+strings.Repeat("b", 64)+".reservation"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			root := newAttemptRoot(t)
			store, err := newAttemptStore(root, productionAttemptLimits())
			if err != nil {
				t.Fatal(err)
			}
			defer store.close()
			mutate(t, root)
			authority := testCollectionAuthority(t)
			request := CollectRequest{RunID: "run", AttemptID: "unsafe", Authority: authority}
			identity, _ := validateCollectRequest(request)
			if _, err := store.acquire(newAttemptReservation(request, identity)); err == nil {
				t.Fatal("unsafe allocator object was accepted")
			}
		})
	}

	t.Run("capacity-permissions", func(t *testing.T) {
		root := newAttemptRoot(t)
		if err := os.Chmod(filepath.Join(root, "capacity.lock"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := newAttemptStore(root, productionAttemptLimits()); err == nil {
			t.Fatal("unsafe capacity permissions were accepted")
		}
	})
}

func TestReplayRejectsTamperMissingArtifactAndConflictingEvent(t *testing.T) {
	for _, mode := range []string{"tamper", "missing", "symlink", "fifo", "conflicting-event"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newDurableFixture(t, modeSuccess)
			request := durableRequest(fixture, "replay-integrity")
			bundle, err := fixture.controller.Collect(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			identity, _ := validateCollectRequest(request)
			path := filepath.Join(fixture.artifacts.RunDir(), attemptBundleName(identity.attemptKey))
			switch mode {
			case "tamper":
				if err := os.WriteFile(path, append(bundle.CanonicalJSON(), ' '), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(fixture.attemptRoot, "capacity.lock"), path); err != nil {
					t.Fatal(err)
				}
			case "fifo":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			case "conflicting-event":
				observed, found, err := fixture.controller.material.find(outcomeEventID(identity.attemptKey))
				if err != nil || !found {
					t.Fatal(err)
				}
				conflict := observed.event
				conflict.Actor = "other-controller"
				if err := fixture.ledger.Append(conflict); err != nil {
					t.Fatal(err)
				}
			}
			requests := fixture.github.requests.Load()
			replayed, replayErr := fixture.controller.Collect(context.Background(), request)
			if replayErr == nil || len(replayed.CanonicalJSON()) != 0 || fixture.github.requests.Load() != requests {
				t.Fatalf("replay integrity failure escaped: bundle=%s error=%v requests=%d", replayed.CanonicalJSON(), replayErr, fixture.github.requests.Load())
			}
		})
	}
}

type writeThenErrorStore struct{ *evidence.Store }

func (s writeThenErrorStore) WriteBytes(name, kind string, data []byte) (ledger.EvidenceRef, error) {
	ref, err := s.Store.WriteBytes(name, kind, data)
	if err != nil {
		return ref, err
	}
	return ref, errors.New("injected ambiguous artifact publication")
}

func TestPublicationAndLedgerAmbiguityAreVerified(t *testing.T) {
	t.Run("artifact", func(t *testing.T) {
		fixture := newDurableFixture(t, modeEmpty)
		fixture.controller.artifacts.store = writeThenErrorStore{fixture.artifacts}
		bundle, err := fixture.controller.Collect(context.Background(), durableRequest(fixture, "artifact-ambiguity"))
		if err != nil || bundle.Input().Outcome != OutcomeStable {
			t.Fatalf("create-or-verify failed: bundle=%s error=%v", bundle.CanonicalJSON(), err)
		}
	})

	for _, stage := range []string{"after_append", "after_fsync"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newDurableFixture(t, modeEmpty)
			fixture.controller.material.appendFault = func(observed string) error {
				if observed == stage {
					return errors.New("injected ambiguous ledger result")
				}
				return nil
			}
			bundle, err := fixture.controller.Collect(context.Background(), durableRequest(fixture, "ledger-"+stage))
			if err != nil || bundle.Input().Outcome != OutcomeStable {
				t.Fatalf("ambiguous ledger result was not rescanned: bundle=%s error=%v", bundle.CanonicalJSON(), err)
			}
		})
	}
}

func TestReservationFsyncFailureIsSameAttemptRecoverable(t *testing.T) {
	authority := testCollectionAuthority(t)
	root := newAttemptRoot(t)
	store, err := newAttemptStore(root, productionAttemptLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	request := CollectRequest{RunID: "run", AttemptID: "reservation-fsync", Authority: authority}
	identity, _ := validateCollectRequest(request)
	store.syncFile = func(*os.File) error { return errors.New("injected reservation fsync") }
	if _, err := store.acquire(newAttemptReservation(request, identity)); err == nil {
		t.Fatal("injected reservation fsync failure was ignored")
	}
	store.syncFile = func(file *os.File) error { return file.Sync() }
	lease, err := store.acquire(newAttemptReservation(request, identity))
	if err != nil {
		t.Fatalf("same reservation did not recover: %v", err)
	}
	if lease.created || lease.needsRepair {
		t.Fatalf("same-attempt recovery consumed a new slot: %#v", lease)
	}
	_ = lease.close()
}

//go:build linux

package cilifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"time"

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

func TestPrepositionedMaterialNeverBecomesHistoricalProof(t *testing.T) {
	for _, material := range []string{"bundle", "event"} {
		t.Run(material, func(t *testing.T) {
			fixture := newDurableFixture(t, modeSuccess)
			request := durableRequest(fixture, "prepositioned-"+material)
			identity, requestErr := validateCollectRequest(request)
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			bundle, err := fixture.controller.github.collectRemote(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			fixture.github.requests.Store(0)
			ref, err := fixture.artifacts.WriteBytes(attemptBundleName(identity.attemptKey), EvidenceBundleKindV1, bundle.CanonicalJSON())
			if err != nil {
				t.Fatal(err)
			}
			if material == "event" {
				event, _, err := deterministicOutcomeEvent(bundle, ref)
				if err != nil {
					t.Fatal(err)
				}
				if err := fixture.ledger.Append(event); err != nil {
					t.Fatal(err)
				}
			}

			for retry := 0; retry < 3; retry++ {
				got, collectErr := fixture.controller.Collect(context.Background(), request)
				if collectErr == nil || len(got.CanonicalJSON()) != 0 {
					t.Fatalf("retry %d promoted prepositioned %s: bundle=%s error=%v", retry, material, got.CanonicalJSON(), collectErr)
				}
			}
			if fixture.github.requests.Load() != 0 {
				t.Fatalf("prepositioned %s reached GitHub: %d requests", material, fixture.github.requests.Load())
			}
			reservationPath := filepath.Join(fixture.attemptRoot, attemptReservationFilename(identity.attemptKey))
			reservationBytes, err := os.ReadFile(reservationPath)
			if err != nil || len(reservationBytes) == 0 || reservationBytes[0] != '!' {
				t.Fatalf("prepositioned %s did not leave a durable conflict marker: bytes=%q error=%v", material, reservationBytes, err)
			}
		})
	}
}

func TestPrepositionedMaterialInspectionFailureCannotAuthorizeRetry(t *testing.T) {
	for _, stage := range []string{"bundle-file", "bundle-directory", "event-file", "event-directory"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newDurableFixture(t, modeSuccess)
			request := durableRequest(fixture, "prepositioned-inspection-"+stage)
			identity, requestErr := validateCollectRequest(request)
			if requestErr != nil {
				t.Fatal(requestErr)
			}
			bundle, err := fixture.controller.github.collectRemote(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			fixture.github.requests.Store(0)
			ref, err := fixture.artifacts.WriteBytes(attemptBundleName(identity.attemptKey), EvidenceBundleKindV1, bundle.CanonicalJSON())
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(stage, "event-") {
				event, _, err := deterministicOutcomeEvent(bundle, ref)
				if err != nil {
					t.Fatal(err)
				}
				if err := fixture.ledger.Append(event); err != nil {
					t.Fatal(err)
				}
			}

			injected := errors.New("injected pre-positioned material inspection failure")
			switch stage {
			case "bundle-file":
				fixture.controller.artifacts.syncFile = func(*os.File) error { return injected }
			case "bundle-directory":
				fixture.controller.artifacts.syncDir = func(*os.File) error { return injected }
			case "event-file":
				fixture.controller.material.syncFile = func(*os.File) error { return injected }
			case "event-directory":
				fixture.controller.material.syncDir = func(*os.File) error { return injected }
			}
			got, collectErr := fixture.controller.Collect(context.Background(), request)
			if collectErr == nil || len(got.CanonicalJSON()) != 0 {
				t.Fatalf("%s inspection failure became authoritative: bundle=%s error=%v", stage, got.CanonicalJSON(), collectErr)
			}

			fixture.controller.artifacts.syncFile = func(file *os.File) error { return file.Sync() }
			fixture.controller.artifacts.syncDir = func(file *os.File) error { return file.Sync() }
			fixture.controller.material.syncFile = func(file *os.File) error { return file.Sync() }
			fixture.controller.material.syncDir = func(file *os.File) error { return file.Sync() }
			for retry := 0; retry < 2; retry++ {
				got, collectErr = fixture.controller.Collect(context.Background(), request)
				if collectErr == nil || len(got.CanonicalJSON()) != 0 {
					t.Fatalf("retry %d promoted %s material: bundle=%s error=%v", retry, stage, got.CanonicalJSON(), collectErr)
				}
			}
			if fixture.github.requests.Load() != 0 {
				t.Fatalf("%s inspection failure reached GitHub: %d requests", stage, fixture.github.requests.Load())
			}
			reservationPath := filepath.Join(fixture.attemptRoot, attemptReservationFilename(identity.attemptKey))
			reservationBytes, err := os.ReadFile(reservationPath)
			if err != nil || len(reservationBytes) == 0 || reservationBytes[0] != '!' {
				t.Fatalf("%s did not remain durably fail-closed: bytes=%q error=%v", stage, reservationBytes, err)
			}
		})
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
	if err := lease.repair(); err != nil {
		t.Fatal(err)
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
		request := durableRequest(fixture, "artifact-ambiguity")
		bundle, err := fixture.controller.Collect(context.Background(), request)
		if err == nil || len(bundle.CanonicalJSON()) != 0 {
			t.Fatalf("artifact write error was converted to success: bundle=%s error=%v", bundle.CanonicalJSON(), err)
		}
		requests := fixture.github.requests.Load()
		bundle, err = fixture.controller.Collect(context.Background(), request)
		if err != nil || bundle.Input().Outcome != OutcomeStable || fixture.github.requests.Load() != requests {
			t.Fatalf("durable artifact was not recovered on retry: bundle=%s error=%v requests=%d", bundle.CanonicalJSON(), err, fixture.github.requests.Load())
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
			request := durableRequest(fixture, "ledger-"+stage)
			bundle, err := fixture.controller.Collect(context.Background(), request)
			if stage == "after_append" {
				if err == nil || len(bundle.CanonicalJSON()) != 0 {
					t.Fatalf("append error was converted to success: bundle=%s error=%v", bundle.CanonicalJSON(), err)
				}
				fixture.controller.material.appendFault = nil
				bundle, err = fixture.controller.Collect(context.Background(), request)
			}
			if err != nil || bundle.Input().Outcome != OutcomeStable {
				t.Fatalf("durable ledger result was not recovered: bundle=%s error=%v", bundle.CanonicalJSON(), err)
			}
		})
	}
}

func TestArtifactFinalSyncFailuresAreNotMaskedByReadback(t *testing.T) {
	for _, stage := range []string{"file", "directory"} {
		t.Run(stage, func(t *testing.T) {
			fixture := newDurableFixture(t, modeEmpty)
			request := durableRequest(fixture, "artifact-sync-"+stage)
			if stage == "file" {
				fixture.controller.artifacts.syncFile = func(*os.File) error { return errors.New("injected final file sync") }
			} else {
				fixture.controller.artifacts.syncDir = func(*os.File) error { return errors.New("injected final directory sync") }
			}
			bundle, err := fixture.controller.Collect(context.Background(), request)
			if err == nil || len(bundle.CanonicalJSON()) != 0 {
				t.Fatalf("%s sync failure was converted to success: bundle=%s error=%v", stage, bundle.CanonicalJSON(), err)
			}
			if _, err := os.Lstat(fixture.ledger.Path()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s sync failure reached the ledger: %v", stage, err)
			}
			requests := fixture.github.requests.Load()
			fixture.controller.artifacts.syncFile = func(file *os.File) error { return file.Sync() }
			fixture.controller.artifacts.syncDir = func(file *os.File) error { return file.Sync() }
			bundle, err = fixture.controller.Collect(context.Background(), request)
			if err != nil || bundle.Input().Outcome != OutcomeStable || fixture.github.requests.Load() != requests {
				t.Fatalf("%s sync retry did not recover: bundle=%s error=%v requests=%d", stage, bundle.CanonicalJSON(), err, fixture.github.requests.Load())
			}
		})
	}
}

func TestReservationPersistenceFailureMustBeRetried(t *testing.T) {
	for _, stage := range []string{"file", "directory"} {
		t.Run(stage, func(t *testing.T) {
			authority := testCollectionAuthority(t)
			root := newAttemptRoot(t)
			store, err := newAttemptStore(root, productionAttemptLimits())
			if err != nil {
				t.Fatal(err)
			}
			defer store.close()
			request := CollectRequest{RunID: "run", AttemptID: "reservation-" + stage, Authority: authority}
			identity, _ := validateCollectRequest(request)
			injected := errors.New("injected reservation " + stage + " sync")
			if stage == "file" {
				store.syncFile = func(*os.File) error { return injected }
			} else {
				store.syncDir = func(*os.File) error { return injected }
			}
			if _, err := store.acquire(newAttemptReservation(request, identity)); err == nil {
				t.Fatalf("initial %s sync failure was ignored", stage)
			}
			if _, err := store.acquire(newAttemptReservation(request, identity)); err == nil {
				t.Fatalf("existing reservation skipped retrying %s sync", stage)
			}
			store.syncFile = func(file *os.File) error { return file.Sync() }
			store.syncDir = func(file *os.File) error { return file.Sync() }
			lease, err := store.acquire(newAttemptReservation(request, identity))
			if err != nil {
				t.Fatalf("same reservation did not recover after %s sync: %v", stage, err)
			}
			if lease.created || !lease.needsRepair {
				t.Fatalf("same-attempt recovery consumed a new slot: %#v", lease)
			}
			if err := lease.repair(); err != nil {
				t.Fatalf("same-attempt guard did not complete after %s sync: %v", stage, err)
			}
			_ = lease.close()
		})
	}
}

func newTestMaterialLedger(t *testing.T) (*materialLedger, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "events.jsonl")
	supplied, err := ledger.NewJSONLLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	material, err := newMaterialLedger(supplied)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = material.close() })
	return material, path
}

func testMaterialRecord(t *testing.T, index int) (ledger.Event, []byte) {
	t.Helper()
	event := ledger.Event{
		SchemaVersion: ledger.CurrentSchemaVersion,
		EventID:       fmt.Sprintf("%064x", index+1),
		Timestamp:     time.Unix(int64(index+1), 0).UTC(),
		RunID:         "material-test",
		AttemptID:     fmt.Sprintf("attempt-%d", index),
		EventType:     ciEvidenceOutcomeEventType,
		Actor:         "controller",
		Source:        "cilifecycle",
	}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	canonical, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return event, canonical
}

func TestMaterialLedgerRejectsProjectedBoundsBeforeAppend(t *testing.T) {
	t.Run("bytes", func(t *testing.T) {
		material, path := newTestMaterialLedger(t)
		event, canonical := testMaterialRecord(t, 0)
		material.maxBytes = int64(len(canonical))
		if err := material.record(event, canonical); err == nil {
			t.Fatal("ledger accepted an event whose newline crossed the byte bound")
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) != 0 {
			t.Fatalf("byte-bound rejection mutated ledger: bytes=%q error=%v", data, err)
		}
	})

	t.Run("lines", func(t *testing.T) {
		material, path := newTestMaterialLedger(t)
		material.maxLines = 1
		first, firstCanonical := testMaterialRecord(t, 1)
		if err := material.record(first, firstCanonical); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		second, secondCanonical := testMaterialRecord(t, 2)
		if err := material.record(second, secondCanonical); err == nil {
			t.Fatal("ledger accepted an event beyond the line bound")
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(after, before) {
			t.Fatalf("line-bound rejection mutated ledger: before=%q after=%q error=%v", before, after, err)
		}
	})
}

func TestMaterialLedgerRollsBackAppendFailures(t *testing.T) {
	for _, mode := range []string{"append-error", "partial-write"} {
		t.Run(mode, func(t *testing.T) {
			material, path := newTestMaterialLedger(t)
			event, canonical := testMaterialRecord(t, 10)
			injected := errors.New("injected ledger write failure")
			if mode == "append-error" {
				material.writeLine = func(*os.File, []byte) (int, error) { return 0, injected }
			} else {
				material.writeLine = func(file *os.File, data []byte) (int, error) {
					n, err := file.Write(data[:len(data)/2])
					return n, errors.Join(injected, err)
				}
			}
			if err := material.record(event, canonical); err == nil {
				t.Fatalf("%s was converted to success", mode)
			}
			data, err := os.ReadFile(path)
			if err != nil || len(data) != 0 {
				t.Fatalf("%s left a poisoned tail: bytes=%q error=%v", mode, data, err)
			}
			material.writeLine = func(file *os.File, data []byte) (int, error) { return file.Write(data) }
			if err := material.record(event, canonical); err != nil {
				t.Fatalf("record did not recover after %s rollback: %v", mode, err)
			}
		})
	}
}

func TestMaterialLedgerRollbackSerializesOrdinaryAppend(t *testing.T) {
	material, path := newTestMaterialLedger(t)
	ordinaryLedger, err := ledger.NewJSONLLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	ciEvent, ciCanonical := testMaterialRecord(t, 15)
	ordinaryEvent, err := ledger.NewEvent("ordinary-run", "ORDINARY_EVENT", "controller", "unit")
	if err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected ambiguous partial CI append")
	partialWritten := make(chan struct{})
	releaseCI := make(chan struct{})
	material.writeLine = func(file *os.File, data []byte) (int, error) {
		n, writeErr := file.Write(data[:len(data)/2])
		close(partialWritten)
		<-releaseCI
		return n, errors.Join(injected, writeErr)
	}
	ciDone := make(chan error, 1)
	go func() { ciDone <- material.record(ciEvent, ciCanonical) }()
	<-partialWritten

	appendStarted := make(chan struct{})
	appendDone := make(chan error, 1)
	go func() {
		close(appendStarted)
		appendDone <- ordinaryLedger.Append(ordinaryEvent)
	}()
	<-appendStarted

	var earlyAppendErr error
	appendCompletedEarly := false
	select {
	case earlyAppendErr = <-appendDone:
		appendCompletedEarly = true
	case <-time.After(250 * time.Millisecond):
	}
	close(releaseCI)
	ciErr := <-ciDone
	if ciErr == nil || !errors.Is(ciErr, injected) {
		t.Fatalf("partial CI append was not rolled back as a failure: %v", ciErr)
	}
	if appendCompletedEarly {
		t.Fatalf("ordinary append escaped the CI ledger transaction: %v", earlyAppendErr)
	}
	select {
	case err = <-appendDone:
		if err != nil {
			t.Fatalf("serialized ordinary append failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ordinary append did not resume after CI rollback")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(data, []byte{'\n'}) != 1 || data[len(data)-1] != '\n' {
		t.Fatalf("ledger is not one bounded canonical line after rollback: %q", data)
	}
	line := bytes.TrimSuffix(data, []byte{'\n'})
	var observed ledger.Event
	if err := json.Unmarshal(line, &observed); err != nil {
		t.Fatalf("ordinary event is not valid JSON: %v", err)
	}
	reencoded, err := json.Marshal(observed)
	if err != nil || !bytes.Equal(reencoded, line) || observed.EventID != ordinaryEvent.EventID {
		t.Fatalf("ordinary event was lost or made noncanonical: event=%#v error=%v", observed, err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	_, foundCI, snapshot, scanErr := scanMaterialLedger(file, ciEvent.EventID, material.maxBytes, material.maxLines)
	if scanErr != nil || foundCI || snapshot.lines != 1 || snapshot.bytes != int64(len(data)) || snapshot.bytes > material.maxBytes {
		t.Fatalf("ledger did not remain canonical and bounded: found_ci=%t snapshot=%+v error=%v", foundCI, snapshot, scanErr)
	}
}

func TestMaterialLedgerFsyncFailureRequiresSuccessfulRetry(t *testing.T) {
	material, path := newTestMaterialLedger(t)
	event, canonical := testMaterialRecord(t, 20)
	material.syncFile = func(*os.File) error { return errors.New("injected ledger fsync failure") }
	if err := material.record(event, canonical); err == nil {
		t.Fatal("ledger fsync failure was converted to success by readback")
	}
	data, err := os.ReadFile(path)
	if err != nil || bytes.Count(data, []byte{'\n'}) != 1 {
		t.Fatalf("complete unsynced line was not retained for retry: bytes=%q error=%v", data, err)
	}
	material.syncFile = func(file *os.File) error { return file.Sync() }
	if err := material.record(event, canonical); err != nil {
		t.Fatalf("existing event did not cross a successful fsync boundary: %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil || bytes.Count(data, []byte{'\n'}) != 1 {
		t.Fatalf("fsync retry duplicated the event: bytes=%q error=%v", data, err)
	}
}

func TestControllerReplayRequiresSuccessfulLedgerFsync(t *testing.T) {
	fixture := newDurableFixture(t, modeEmpty)
	request := durableRequest(fixture, "controller-ledger-fsync-retry")
	injected := errors.New("injected controller ledger fsync failure")
	syncAttempts := 0
	fixture.controller.material.syncFile = func(file *os.File) error {
		syncAttempts++
		if syncAttempts <= 2 {
			return injected
		}
		return file.Sync()
	}

	first, firstErr := fixture.controller.Collect(context.Background(), request)
	if firstErr == nil || len(first.CanonicalJSON()) != 0 {
		t.Fatalf("initial fsync failure became authoritative: bundle=%s error=%v", first.CanonicalJSON(), firstErr)
	}
	ledgerBytes, err := os.ReadFile(fixture.ledger.Path())
	if err != nil || bytes.Count(ledgerBytes, []byte{'\n'}) != 1 {
		t.Fatalf("initial fsync failure did not retain one retryable line: bytes=%q error=%v", ledgerBytes, err)
	}
	requests := fixture.github.requests.Load()

	second, secondErr := fixture.controller.Collect(context.Background(), request)
	if secondErr == nil || len(second.CanonicalJSON()) != 0 {
		t.Fatalf("retry replayed without successful ledger fsync: bundle=%s error=%v", second.CanonicalJSON(), secondErr)
	}
	if fixture.github.requests.Load() != requests {
		t.Fatalf("ledger fsync retry contacted GitHub: requests=%d, want %d", fixture.github.requests.Load(), requests)
	}

	third, thirdErr := fixture.controller.Collect(context.Background(), request)
	if thirdErr != nil || third.Input().Outcome != OutcomeStable {
		t.Fatalf("successful ledger stabilization did not authorize replay: bundle=%s error=%v", third.CanonicalJSON(), thirdErr)
	}
	if fixture.github.requests.Load() != requests || syncAttempts < 3 {
		t.Fatalf("replay requests=%d/%d sync attempts=%d", fixture.github.requests.Load(), requests, syncAttempts)
	}
	ledgerBytes, err = os.ReadFile(fixture.ledger.Path())
	if err != nil || bytes.Count(ledgerBytes, []byte{'\n'}) != 1 {
		t.Fatalf("ledger stabilization duplicated the event: bytes=%q error=%v", ledgerBytes, err)
	}
}

func TestCILifecycleCrossCompilesForNonLinux(t *testing.T) {
	output := filepath.Join(t.TempDir(), "cilifecycle-darwin.test")
	command := exec.Command("go", "test", "-c", "-o", output, ".")
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=darwin", "GOARCH=amd64")
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("non-Linux cross-compilation failed: %v: %s", err, combined)
	}
}

func TestMaterialLedgerSerializesConcurrentControllers(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "events.jsonl")
	supplied, err := ledger.NewJSONLLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	const workers = 24
	materials := make([]*materialLedger, workers)
	for index := range materials {
		materials[index], err = newMaterialLedger(supplied)
		if err != nil {
			t.Fatal(err)
		}
		defer materials[index].close()
	}
	start := make(chan struct{})
	errorsSeen := make(chan error, workers)
	var group sync.WaitGroup
	for index := range materials {
		event, canonical := testMaterialRecord(t, 100+index)
		group.Add(1)
		go func(material *materialLedger, event ledger.Event, canonical []byte) {
			defer group.Done()
			<-start
			errorsSeen <- material.record(event, canonical)
		}(materials[index], event, canonical)
	}
	close(start)
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || bytes.Count(data, []byte{'\n'}) != workers {
		t.Fatalf("concurrent ledger lost or interleaved records: lines=%d error=%v", bytes.Count(data, []byte{'\n'}), err)
	}
}

func TestAttemptInventoryReadIsBounded(t *testing.T) {
	root := newAttemptRoot(t)
	limits := attemptLimits{maxPerRun: 2, maxGlobal: 2, maxBytes: 2 * MaxCompletedAttemptBytes}
	store, err := newAttemptStore(root, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	for index := 0; index < 1000; index++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("foreign-%04d", index)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	requested := 0
	store.readDir = func(directory *os.File, count int) ([]os.DirEntry, error) {
		requested = count
		return directory.ReadDir(count)
	}
	if _, err := store.inventory(); err == nil {
		t.Fatal("contaminated attempt root was accepted")
	}
	if requested != limits.maxGlobal+2 {
		t.Fatalf("inventory requested %d entries, want capacity plus limit and one sentinel (%d)", requested, limits.maxGlobal+2)
	}
}

func TestProcessAttemptLocksAreRemovedAfterUniqueAttempts(t *testing.T) {
	baseline := processAttemptLockCount()
	authority := testCollectionAuthority(t)
	root := newAttemptRoot(t)
	store, err := newAttemptStore(root, attemptLimits{maxPerRun: 1, maxGlobal: 1, maxBytes: MaxCompletedAttemptBytes})
	if err != nil {
		t.Fatal(err)
	}
	defer store.close()
	firstRequest := CollectRequest{RunID: "run", AttemptID: "retained", Authority: authority}
	firstIdentity, _ := validateCollectRequest(firstRequest)
	lease, err := store.acquire(newAttemptReservation(firstRequest, firstIdentity))
	if err != nil {
		t.Fatal(err)
	}
	if got := processAttemptLockCount(); got != baseline+1 {
		t.Fatalf("active lock registry size=%d, want %d", got, baseline+1)
	}
	if _, err := store.acquire(newAttemptReservation(firstRequest, firstIdentity)); err == nil {
		t.Fatal("same-attempt contention was accepted")
	}
	if got := processAttemptLockCount(); got != baseline+1 {
		t.Fatalf("failed contention leaked a lock entry: got %d, want %d", got, baseline+1)
	}
	_ = lease.close()
	if got := processAttemptLockCount(); got != baseline {
		t.Fatalf("closed active attempt retained a lock entry: got %d, want %d", got, baseline)
	}
	for index := 0; index < 512; index++ {
		request := CollectRequest{RunID: fmt.Sprintf("run-%d", index), AttemptID: fmt.Sprintf("unique-%d", index), Authority: authority}
		identity, _ := validateCollectRequest(request)
		if _, err := store.acquire(newAttemptReservation(request, identity)); err == nil {
			t.Fatalf("capacity-exhausted unique attempt %d was accepted", index)
		}
		if got := processAttemptLockCount(); got != baseline {
			t.Fatalf("unique attempt %d leaked process lock: got %d, want %d", index, got, baseline)
		}
	}
}

//go:build linux

package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/domain"
)

func TestRunTransitionNamespaceGenerationIsNonReissuable(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		name := "loss"
		if replacement {
			name = "replacement"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.jsonl")
			value, err := NewJSONLLedger(path)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := value.AcquireRunTransition("run-generation")
			if err != nil {
				t.Fatal(err)
			}
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
			authority := append([]byte(nil), value.runLocks.authorityData...)
			authorityName := value.runLocks.authorityName
			if err := value.Close(); err != nil {
				t.Fatal(err)
			}

			directory := path + ".run-locks"
			if err := os.Rename(directory, directory+"-original"); err != nil {
				t.Fatal(err)
			}
			if replacement {
				if err := os.Mkdir(directory, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			fresh, err := OpenExistingJSONLLedger(path)
			if err != nil {
				t.Fatal(err)
			}
			defer fresh.Close()
			if lease, err := fresh.AcquireRunTransition("run-generation"); !errors.Is(err, errRunTransitionIntegrity) {
				if lease != nil {
					_ = lease.Close()
				}
				t.Fatalf("fresh process accepted retired namespace generation: %v", err)
			}
			observed, found, err := getRunTransitionXattr(int(fresh.parent.Fd()), authorityName)
			if err != nil || !found || string(observed) != string(authority) {
				t.Fatalf("ledger-parent authority changed: found=%v err=%v", found, err)
			}
			if replacement {
				entries, err := os.ReadDir(directory)
				if err != nil || len(entries) != 0 {
					t.Fatalf("replacement namespace gained authority objects: entries=%d err=%v", len(entries), err)
				}
			} else if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("lost namespace was recreated: %v", err)
			}
		})
	}
}

func TestRunTransitionLeaseRejectsLockReplacementBeforeAppendAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	value, err := NewJSONLLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	defer value.Close()
	runID := "run-lock-replacement"
	lease, err := value.AcquireRunTransition(runID)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(runID))
	lockPath := filepath.Join(path+".run-locks", hex.EncodeToString(digest[:])+".lock")
	if err := os.Rename(lockPath, lockPath+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}

	peer, err := OpenExistingJSONLLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if peerLease, err := peer.AcquireRunTransition(runID); !errors.Is(err, errRunTransitionIntegrity) {
		if peerLease != nil {
			_ = peerLease.Close()
		}
		t.Fatalf("replacement lock acquired a second transition authority: %v", err)
	}
	event := transitionEvent(t, runID, "attempt-replaced", "event-replaced", domain.StateFailed)
	if err := value.AppendOrVerifyLeased(event, lease); !errors.Is(err, errRunTransitionIntegrity) {
		t.Fatalf("append used a replaced run-transition lock: %v", err)
	}
	if err := lease.Close(); !errors.Is(err, errRunTransitionIntegrity) {
		t.Fatalf("release did not revalidate replaced run-transition lock: %v", err)
	}
	data, _, err := value.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), event.EventID) {
		t.Fatal("event was appended after transition-lock replacement")
	}
}

func TestRunTransitionReplacementRacesAreCrossProcess(t *testing.T) {
	attacks := []struct {
		name      string
		contender string
		replace   func(*testing.T, string, string)
	}{
		{name: "runner-vs-watcher-directory", contender: "watcher", replace: replaceRunTransitionDirectory},
		{name: "runner-vs-decision-lock", contender: "decision", replace: replaceRunTransitionLock},
	}
	for _, attack := range attacks {
		t.Run(attack.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.jsonl")
			runID := "run-cross-process"
			creator, err := NewJSONLLedger(path)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := creator.AcquireRunTransition(runID)
			if err != nil {
				t.Fatal(err)
			}
			if err := lease.Close(); err != nil {
				t.Fatal(err)
			}
			if err := creator.Close(); err != nil {
				t.Fatal(err)
			}

			state := t.TempDir()
			marker := filepath.Join(state, "runner-held")
			release := filepath.Join(state, "release-runner")
			holder := startRunTransitionHelper(t, "holder", "runner", path, runID, marker, release)
			waitForRunTransitionMarker(t, marker)
			attack.replace(t, path, runID)
			runRunTransitionContender(t, attack.contender, path, runID)
			if err := os.WriteFile(release, []byte("release\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := waitRunTransitionHelper(holder, 10*time.Second); err != nil {
				t.Fatalf("runner helper did not fail closed after replacement: %v\n%s", err, holder.output.String())
			}
		})
	}
}

func TestRunTransitionLeaseHelperProcess(t *testing.T) {
	mode := os.Getenv("ABCP_RUN_TRANSITION_HELPER")
	if mode == "" {
		return
	}
	path := os.Getenv("ABCP_RUN_TRANSITION_LEDGER")
	runID := os.Getenv("ABCP_RUN_TRANSITION_RUN_ID")
	role := os.Getenv("ABCP_RUN_TRANSITION_ROLE")
	value, err := OpenExistingJSONLLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	defer value.Close()
	lease, err := value.AcquireRunTransition(runID)
	if mode == "contender" {
		if lease != nil {
			_ = lease.Close()
			t.Fatalf("%s acquired a second transition authority", role)
		}
		if !errors.Is(err, errRunTransitionIntegrity) {
			t.Fatalf("%s replacement result = %v", role, err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	marker := os.Getenv("ABCP_RUN_TRANSITION_MARKER")
	if err := os.WriteFile(marker, []byte(role+"\n"), 0o600); err != nil {
		_ = lease.Close()
		t.Fatal(err)
	}
	release := os.Getenv("ABCP_RUN_TRANSITION_RELEASE")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(release); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			_ = lease.Close()
			t.Fatal(err)
		}
		if !time.Now().Before(deadline) {
			_ = lease.Close()
			t.Fatal("timed out waiting for replacement race release")
		}
		time.Sleep(5 * time.Millisecond)
	}
	event := transitionEvent(t, runID, "attempt-"+role, "event-"+role, domain.StateFailed)
	appendErr := value.AppendOrVerifyLeased(event, lease)
	closeErr := lease.Close()
	if !errors.Is(appendErr, errRunTransitionIntegrity) || !errors.Is(closeErr, errRunTransitionIntegrity) {
		t.Fatalf("%s retained authority after replacement: append=%v close=%v", role, appendErr, closeErr)
	}
}

type runTransitionHelper struct {
	command  *exec.Cmd
	output   strings.Builder
	done     chan error
	finished chan struct{}
}

func startRunTransitionHelper(t *testing.T, mode, role, path, runID, marker, release string) *runTransitionHelper {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestRunTransitionLeaseHelperProcess$")
	helper := &runTransitionHelper{command: command, done: make(chan error, 1), finished: make(chan struct{})}
	command.Stdout = &helper.output
	command.Stderr = &helper.output
	command.Env = append(os.Environ(),
		"ABCP_RUN_TRANSITION_HELPER="+mode,
		"ABCP_RUN_TRANSITION_ROLE="+role,
		"ABCP_RUN_TRANSITION_LEDGER="+path,
		"ABCP_RUN_TRANSITION_RUN_ID="+runID,
		"ABCP_RUN_TRANSITION_MARKER="+marker,
		"ABCP_RUN_TRANSITION_RELEASE="+release,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		helper.done <- command.Wait()
		close(helper.finished)
	}()
	t.Cleanup(func() {
		select {
		case <-helper.finished:
		default:
			_ = command.Process.Kill()
			<-helper.finished
		}
	})
	return helper
}

func runRunTransitionContender(t *testing.T, role, path, runID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunTransitionLeaseHelperProcess$")
	command.Env = append(os.Environ(),
		"ABCP_RUN_TRANSITION_HELPER=contender",
		"ABCP_RUN_TRANSITION_ROLE="+role,
		"ABCP_RUN_TRANSITION_LEDGER="+path,
		"ABCP_RUN_TRANSITION_RUN_ID="+runID,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s contender helper failed: %v\n%s", role, err, output)
	}
}

func waitRunTransitionHelper(helper *runTransitionHelper, timeout time.Duration) error {
	select {
	case err := <-helper.done:
		return err
	case <-time.After(timeout):
		_ = helper.command.Process.Kill()
		return errors.New("helper timed out")
	}
}

func waitForRunTransitionMarker(t *testing.T, marker string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("run-transition helper did not acquire its lease")
}

func replaceRunTransitionDirectory(t *testing.T, path, _ string) {
	t.Helper()
	directory := path + ".run-locks"
	if err := os.Rename(directory, directory+"-retired"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
}

func replaceRunTransitionLock(t *testing.T, path, runID string) {
	t.Helper()
	digest := sha256.Sum256([]byte(runID))
	lockPath := filepath.Join(path+".run-locks", hex.EncodeToString(digest[:])+".lock")
	if err := os.Rename(lockPath, lockPath+"-retired"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
}

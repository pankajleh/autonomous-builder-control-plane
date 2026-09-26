//go:build linux

package activity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

type sidecarProcess struct {
	PID          int
	URL, Runtime string
}

func TestSidecarAbruptControllerDeathAndStartupReconciliation(t *testing.T) {
	f := newBindingFixture(t)
	selected := providerSession(t, f)
	writeJSON(t, filepath.Join(f.repo, ".ralphex", "sessions.json"), []session{selected})
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	pinned := filepath.Join(t.TempDir(), "provider")
	if err = os.WriteFile(pinned, data, 0700); err != nil {
		t.Fatal(err)
	}
	scope := f.scope
	scope.Ralphex.BinaryPath, scope.Ralphex.BinarySHA256 = pinned, digest(data)
	payload, _ := json.Marshal(scope)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	parent := exec.CommandContext(ctx, executable, "--activity-controller", f.root)
	parent.Stdin = bytes.NewReader(payload)
	parent.Stderr = os.Stderr
	stdout, err := parent.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = parent.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { parent.Process.Kill(); parent.Wait() }()
	var child sidecarProcess
	if err = json.NewDecoder(stdout).Decode(&child); err != nil {
		t.Fatal("controller did not start child", err)
	}
	defer func() {
		if p, err := os.FindProcess(child.PID); err == nil {
			p.Kill()
		}
	}()
	// A second controller's startup must leave this live-owned directory alone.
	if err = reconcileSidecarRuntime(ctx, f.root); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(child.Runtime); err != nil {
		t.Fatal("live-owned runtime removed", err)
	}
	if err = parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = parent.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for {
		stat, err := os.ReadFile("/proc/" + strconv.Itoa(child.PID) + "/stat")
		// An orphan zombie has exited and has no listener/watcher descriptors.
		ended := os.IsNotExist(err) || err == nil && strings.HasPrefix(string(stat)[strings.LastIndex(string(stat), ")")+1:], " Z")
		connection, dialErr := net.DialTimeout("tcp4", strings.TrimPrefix(child.URL, "http://"), 100*time.Millisecond)
		if dialErr == nil {
			connection.Close()
		}
		// The leader may become a zombie while other threads finish group exit.
		// Wait for both process termination and closure of the actual listener.
		if ended && dialErr != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("sidecar survived abrupt controller death")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err = os.Stat(child.Runtime); err != nil {
		t.Fatal("fixture did not leave stale runtime state", err)
	}
	signer, err := serviceapi.NewCursorSigner("restart-key", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(ctx, f.root, f.catalog, &fakeSnapshots{}, signer)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if _, err = os.Stat(child.Runtime); !os.IsNotExist(err) {
		t.Fatal("startup did not reconcile stale runtime", err)
	}
	// The next watcher can start and shut down normally in the same namespace.
	sc, err := startSidecar(ctx, scope, f.root)
	if err != nil {
		t.Fatal("restart watcher", err)
	}
	sc.close()
}

func TestRuntimeReconciliationPreservesUnrelatedPathsAndLiveLeases(t *testing.T) {
	root := privateRuntimeRoot(t)
	live, lease, err := newSidecarRuntime(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	namespace := filepath.Dir(live)
	unrelated := filepath.Join(namespace, "sidecar-unrelated")
	if err = os.Mkdir(unrelated, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(unrelated, "important")
	if err = os.WriteFile(marker, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err = os.Symlink(outside, filepath.Join(namespace, "sidecar-link")); err != nil {
		t.Fatal(err)
	}
	if err = reconcileSidecarRuntime(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{live, marker, outside, filepath.Join(namespace, "sidecar-link")} {
		if _, err = os.Lstat(path); err != nil {
			t.Fatal("unowned or live path removed", path, err)
		}
	}
	lease.Close()
	if err = reconcileSidecarRuntime(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(live); !os.IsNotExist(err) {
		t.Fatal("released runtime not reconciled", err)
	}
}

func TestRuntimeReconciliationRejectsInvalidMarkersAndBoundsTraversal(t *testing.T) {
	for _, mode := range []string{"corrupt", "wrong-namespace", "wrong-directory", "marker-symlink", "deep", "wide", "too-many-runtimes"} {
		t.Run(mode, func(t *testing.T) {
			root := privateRuntimeRoot(t)
			path, lease, err := newSidecarRuntime(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			lease.Close()
			marker := filepath.Join(path, "owner.json")
			switch mode {
			case "corrupt":
				os.WriteFile(marker, []byte("{}"), 0600)
			case "wrong-namespace", "wrong-directory":
				data, _ := os.ReadFile(marker)
				var owner runtimeOwner
				json.Unmarshal(data, &owner)
				if mode == "wrong-namespace" {
					owner.Namespace = strings.Repeat("a", 64)
				} else {
					owner.Directory = "other-directory"
				}
				owner.Checksum = ""
				owner.Checksum = jsonDigest(owner)
				writeJSON(t, marker, owner)
			case "marker-symlink":
				os.Rename(marker, marker+".old")
				os.Symlink(marker+".old", marker)
			case "deep":
				os.MkdirAll(filepath.Join(path, strings.Repeat("nested/", 18)), 0700)
			case "wide":
				for i := 0; i < 4097; i++ {
					if err := os.WriteFile(filepath.Join(path, fmt.Sprint(i)), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "too-many-runtimes":
				for i := 0; i < maxRuntimeEntries; i++ {
					if err := os.Mkdir(filepath.Join(filepath.Dir(path), fmt.Sprint("unrelated-", i)), 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			err = reconcileSidecarRuntime(context.Background(), root)
			if mode == "deep" || mode == "wide" || mode == "too-many-runtimes" {
				if !errors.Is(err, ErrExhausted) {
					t.Fatal("unbounded reconciliation", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(path); err != nil {
				t.Fatal("unproven runtime removed", err)
			}
		})
	}
}

func privateRuntimeRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "service")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRuntimeRequiresProtectedServiceRoot(t *testing.T) {
	root := privateRuntimeRoot(t)
	if err := os.Chmod(root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, lease, err := newSidecarRuntime(context.Background(), root); err == nil {
		lease.Close()
		t.Fatal("unprotected service root accepted")
	}
}

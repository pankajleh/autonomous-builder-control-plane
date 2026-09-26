//go:build linux

package activity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
)

// The test executable doubles as a hash-pinned machine-contract fixture. This
// exercises real exec, attestation, listener ownership, and process cleanup.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--abcp-governance-capability-v1" {
		p := ralphex.CapabilityProbeV1{Kind: "RalphexCapabilityProbeV1", SourceSHA: strings.Repeat("b", 40), MaxIterationsFlag: true, SessionTimeoutFlag: true, IdleTimeoutFlag: true, IsolatedConfig: true, InternalReviewBudgetV1: true, OrchestratorSubprocessWaitV1: true, HumanSessionIdentityV1: true}
		data, _ := json.Marshal(p)
		fmt.Println(string(data))
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == "--serve" {
		if len(os.Args) != 8 || os.Args[2] != "--host" || os.Args[3] != "127.0.0.1" || os.Args[4] != "--port" || os.Args[6] != "--watch" || os.Getenv("ABCP_ACTIVITY_TEST_SECRET") != "" {
			os.Exit(3)
		}
		root := os.Args[7]
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/sessions":
				data, err := os.ReadFile(filepath.Join(root, ".ralphex", "sessions.json"))
				if err != nil {
					w.WriteHeader(500)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write(data)
			case "/events":
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "id: 1\ndata: {\"type\":\"task_end\",\"phase\":\"task\",\"timestamp\":\"2026-09-26T01:00:00Z\",\"text\":\"finished\"}\n\n")
			default:
				w.WriteHeader(404)
			}
		})
		if http.ListenAndServe("127.0.0.1:"+os.Args[5], handler) != nil {
			os.Exit(4)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestPinnedWatchOnlySidecarLifecycle(t *testing.T) {
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
	executable = filepath.Join(t.TempDir(), "pinned-provider")
	if err = os.WriteFile(executable, data, 0700); err != nil {
		t.Fatal(err)
	}
	scope := f.scope
	scope.Ralphex.BinaryPath = executable
	scope.Ralphex.BinarySHA256 = digest(data)
	t.Setenv("ABCP_ACTIVITY_TEST_SECRET", "must-not-inherit")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	check, checkErr := openRegular(executable, os.O_RDONLY, false)
	if checkErr != nil {
		t.Fatal("open executable", checkErr)
	}
	check.Close()
	if checkErr = ralphex.VerifyGovernedExecutionCapabilityV1(executable, scope.Ralphex.BinarySHA256, scope.Ralphex.SourceSHA); checkErr != nil {
		t.Fatal("verify executable", checkErr)
	}
	sc, err := startSidecar(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	list, err := sc.sessions(ctx)
	if err != nil || len(list) != 1 || list[0].AdmissionID != f.run {
		sc.close()
		t.Fatal("machine sessions", err)
	}
	if !strings.HasPrefix(sc.url, "http://127.0.0.1:") {
		t.Fatal("sidecar not loopback")
	}
	sc.close()
	select {
	case <-sc.done:
	default:
		t.Fatal("child not reaped")
	}
	for _, field := range []string{"hash", "source", "path"} {
		t.Run(field, func(t *testing.T) {
			bad := scope
			switch field {
			case "hash":
				bad.Ralphex.BinarySHA256 = strings.Repeat("0", 64)
			case "source":
				bad.Ralphex.SourceSHA = strings.Repeat("0", 40)
			case "path":
				bad.Ralphex.BinaryPath = filepath.Join(t.TempDir(), "binary")
				os.Symlink(executable, bad.Ralphex.BinaryPath)
			}
			start := time.Now()
			if unexpected, err := startSidecar(ctx, bad); err == nil {
				unexpected.close()
				t.Fatal("unverified sidecar started")
			}
			if time.Since(start) > 6*time.Second {
				t.Fatal("startup deadline exceeded")
			}
		})
	}
}

func TestSidecarSharedByRepositoryAndExecutablePin(t *testing.T) {
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
	scope.Ralphex.BinaryPath = pinned
	scope.Ralphex.BinarySHA256 = digest(data)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := &Service{ctx: ctx, sidecars: map[string]*sharedSidecar{}}
	first, key := service.acquireSidecar(scope)
	if first.err != nil {
		service.releaseSidecar(key)
		t.Fatal(first.err)
	}
	other := scope
	other.RunID = "other-run"
	other.Ralphex.Timeout = "10m"
	second, otherKey := service.acquireSidecar(other)
	if second != first || key != otherKey {
		t.Fatal("same scope started two sidecars")
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			shared, k := service.acquireSidecar(other)
			if shared != first || k != key {
				t.Error("shared sidecar changed")
			}
			service.releaseSidecar(k)
		}()
	}
	wg.Wait()
	service.releaseSidecar(key)
	select {
	case <-first.sc.done:
		t.Fatal("sidecar closed with remaining consumer")
	default:
	}
	service.releaseSidecar(otherKey)
	select {
	case <-first.sc.done:
	default:
		t.Fatal("sidecar leaked")
	}
	if len(service.sidecars) != 0 {
		t.Fatal("closed sidecar remained attachable")
	}
}

func TestSidecarAttestationRespectsCallerDeadline(t *testing.T) {
	// A slow (but hash-pinned) capability probe cannot extend the caller's
	// startup deadline. It owns its duplicate descriptor until it exits.
	binary := filepath.Join(t.TempDir(), "slow-provider")
	script := []byte("#!/bin/sh\nsleep 0.1\nexit 1\n")
	if err := os.WriteFile(binary, script, 0700); err != nil {
		t.Fatal(err)
	}
	scope := Scope{Ralphex: authority.RalphexManifest{BinaryPath: binary, BinarySHA256: digest(script), SourceSHA: strings.Repeat("b", 40)}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	started := time.Now()
	if sc, err := startSidecar(ctx, scope); err == nil {
		sc.close()
		t.Fatal("unattested provider started")
	}
	if time.Since(started) > time.Second {
		t.Fatal("startup ignored caller deadline")
	}
}

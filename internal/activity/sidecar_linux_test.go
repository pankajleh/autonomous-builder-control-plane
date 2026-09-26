//go:build linux

package activity

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
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
	if len(os.Args) == 3 && os.Args[1] == "--activity-controller" {
		var scope Scope
		if json.NewDecoder(os.Stdin).Decode(&scope) != nil {
			os.Exit(10)
		}
		sc, err := startSidecar(context.Background(), scope, os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(11)
		}
		json.NewEncoder(os.Stdout).Encode(sidecarProcess{PID: sc.command.Process.Pid, URL: sc.url, Runtime: sc.runtimeDir})
		select {} // Test parent sends SIGKILL: no deferred cleanup can run.
	}
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
		// Model the provider's config.Load startup: defaults are installed under
		// HOME (or cwd when absent), and cwd/.ralphex supplies local overrides.
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		configDir := filepath.Join(home, ".config", "ralphex")
		if os.MkdirAll(configDir, 0700) != nil || os.WriteFile(filepath.Join(configDir, "config"), []byte("defaults"), 0600) != nil {
			os.Exit(5)
		}
		if _, err := os.Stat(".ralphex/config"); err == nil {
			os.Exit(6)
		}
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
	if err := os.WriteFile(filepath.Join(f.repo, ".ralphex", "config"), []byte("untrusted project overrides"), 0600); err != nil {
		t.Fatal(err)
	}
	before := command(t, f.repo, "status", "--porcelain", "--untracked-files=all")
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
	sc, err := startSidecar(ctx, scope, f.root)
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
	if _, err := os.Stat(filepath.Join(sc.runtimeDir, ".config", "ralphex", "config")); err != nil {
		t.Fatal("provider did not initialize isolated configuration", err)
	}
	if after := command(t, f.repo, "status", "--porcelain", "--untracked-files=all"); after != before {
		t.Fatalf("watch-only startup changed repository: before %q, after %q", before, after)
	}
	if _, err := os.Stat(filepath.Join(f.repo, ".config")); !os.IsNotExist(err) {
		t.Fatal("configuration written into repository", err)
	}
	sc.close()
	if _, err := os.Stat(sc.runtimeDir); !os.IsNotExist(err) {
		t.Fatal("sidecar runtime directory leaked", err)
	}
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
			if unexpected, err := startSidecar(ctx, bad, f.root); err == nil {
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
	service := &Service{ctx: ctx, resolver: Resolver{Root: f.root}, sidecars: map[string]*sharedSidecar{}}
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
	if sc, err := startSidecar(ctx, scope, t.TempDir()); err == nil {
		sc.close()
		t.Fatal("unattested provider started")
	}
	if time.Since(started) > time.Second {
		t.Fatal("startup ignored caller deadline")
	}
}

type providerRoundTripFunc func(*http.Request) (*http.Response, error)

func (f providerRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProviderBatchRejectsSidecarListenerTakeover(t *testing.T) {
	for _, timing := range []string{"before-batch", "during-events", "before-persistence"} {
		t.Run(timing, func(t *testing.T) {
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
			f.manifest.Ralphex.BinaryPath, f.manifest.Ralphex.BinarySHA256 = pinned, digest(data)
			f.saveManifest(t)
			f.scope, err = (Resolver{f.root, f.catalog}).Resolve(context.Background(), f.run)
			if err != nil {
				t.Fatal(err)
			}
			sc, err := startSidecar(context.Background(), f.scope, f.root)
			if err != nil {
				t.Fatal(err)
			}
			defer sc.close()
			var replacement *httptest.Server
			defer func() {
				if replacement != nil {
					replacement.Close()
				}
			}()
			takeover := func() {
				t.Helper()
				if replacement != nil {
					return
				}
				if err := sc.command.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				<-sc.done
				listener, err := net.Listen("tcp4", strings.TrimPrefix(sc.url, "http://"))
				if err != nil {
					t.Fatal("replace exited sidecar listener", err)
				}
				replacement = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/sessions" {
						json.NewEncoder(w).Encode([]session{selected})
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "id: 0\ndata: {\"type\":\"task_end\",\"phase\":\"task\",\"timestamp\":\"2026-09-26T01:00:00Z\",\"text\":\"fabricated\"}\n\n")
				}))
				replacement.Listener.Close()
				replacement.Listener = listener
				replacement.Start()
			}
			s := &Service{store: newStore(t), resolver: Resolver{f.root, f.catalog}, ctx: context.Background(), now: func() time.Time { return testTime }}
			switch timing {
			case "before-batch":
				takeover()
			case "during-events":
				transport := sc.client.Transport
				sc.client.Transport = providerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					if r.URL.Path == "/events" {
						takeover()
					}
					return transport.RoundTrip(r)
				})
			case "before-persistence":
				// Normalization runs after the last metadata and progress check.
				s.now = func() time.Time { takeover(); return testTime }
			}
			registration := jsonDigest(f.catalog.runs[f.run])
			last := uint64(math.MaxUint64)
			if err := s.collectBatch(f.scope, registration, sc, &last); err == nil {
				t.Error("batch accepted after pinned sidecar exited")
			}
			events, _, _, err := s.store.read(f.run, registration, 0, 100)
			if err != nil || len(events) != 0 || last != math.MaxUint64 {
				t.Error("rejected batch changed durable events or resume position", events, last, err)
			}
			if proof, err := s.store.proof(f.run, registration); err != nil || proof != nil {
				t.Error("rejected batch persisted progress proof", proof, err)
			}
		})
	}
}

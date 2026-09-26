//go:build linux

package activity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func providerSession(t *testing.T, f *bindingFixture) session {
	t.Helper()
	dir := filepath.Join(f.repo, ".ralphex", "progress")
	os.MkdirAll(dir, 0700)
	path := filepath.Join(dir, "progress-plan.txt")
	os.WriteFile(path, []byte("immutable progress prefix\n"), 0600)
	return session{ID: sessionID(path), DirPath: dir, AdmissionID: f.run, Repository: f.scope.RepositoryIdentity, Project: f.scope.Project, Branch: f.scope.Branch, StartTime: stamp(testTime)}
}

func TestSessionCorrelationFailsClosed(t *testing.T) {
	f := newBindingFixture(t)
	valid := providerSession(t, f)
	if got, err := correlate(f.scope, []session{valid}); err != nil || got.ID != valid.ID {
		t.Fatal(err)
	}
	withoutProject := valid
	withoutProject.Project = ""
	if _, err := correlate(f.scope, []session{withoutProject}); err != nil {
		t.Fatal(err)
	}
	if _, err := correlate(f.scope, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatal("zero matches", err)
	}
	if _, err := correlate(f.scope, []session{valid, valid}); !errors.Is(err, ErrIntegrity) {
		t.Fatal("ambiguous", err)
	}
	for _, field := range []string{"admission", "repository", "project", "branch", "dir", "id", "time"} {
		t.Run(field, func(t *testing.T) {
			bad := valid
			switch field {
			case "admission":
				bad.AdmissionID = "test"
			case "repository":
				bad.Repository = "other"
			case "project":
				bad.Project = "other"
			case "branch":
				bad.Branch = "main"
			case "dir":
				bad.DirPath = f.worktree
			case "id":
				bad.ID = strings.Repeat("x", 513)
			case "time":
				bad.StartTime = ""
			}
			if _, err := correlate(f.scope, []session{bad}); err == nil {
				t.Fatal("mismatch accepted")
			}
		})
	}
	// An unrelated admitted run is not a match and cannot supply scope.
	unrelated := valid
	unrelated.AdmissionID = "other"
	unrelated.Branch = "abcp/other"
	if _, err := correlate(f.scope, []session{unrelated, valid}); err != nil {
		t.Fatal("unrelated session interfered", err)
	}
	dir := valid.DirPath
	os.Rename(dir, dir+"-real")
	os.Symlink(dir+"-real", dir)
	if _, err := correlate(f.scope, []session{valid}); err == nil {
		t.Fatal("symlink dir accepted")
	}
}

func TestProgressGenerationAndAppendContinuity(t *testing.T) {
	for _, mode := range []string{"append", "truncate", "replace", "rewrite", "metadata"} {
		t.Run(mode, func(t *testing.T) {
			f := newBindingFixture(t)
			selected := providerSession(t, f)
			p, err := progressProof(f.scope, selected, nil)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(selected.DirPath, "progress-plan.txt")
			switch mode {
			case "append":
				file, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
				file.WriteString("next event\n")
				file.Close()
			case "truncate":
				os.WriteFile(path, []byte("short"), 0600)
			case "replace":
				os.Remove(path)
				os.WriteFile(path, []byte("immutable progress prefix\n"), 0600)
			case "rewrite":
				os.WriteFile(path, []byte("rewritten progress prefix\n"), 0600)
			case "metadata":
				selected.StartTime = stamp(testTime.AddDate(0, 0, 1))
			}
			_, err = progressProof(f.scope, selected, &p)
			if mode == "append" && err != nil {
				t.Fatal("append rejected", err)
			}
			if mode != "append" && err == nil {
				t.Fatal("generation change accepted")
			}
		})
	}
}

func TestProviderReplayGapDedupeAndLastEventID(t *testing.T) {
	f := newBindingFixture(t)
	selected := providerSession(t, f)
	s := &Service{store: newStore(t), resolver: Resolver{f.root, f.catalog}, ctx: context.Background(), now: func() time.Time { return testTime }}
	registration := jsonDigest(f.catalog.runs[f.run])
	ids := []uint64{0, 1, 2}
	lastHeader := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions" {
			json.NewEncoder(w).Encode([]session{selected})
			return
		}
		if r.URL.Path != "/events" || r.URL.Query().Get("session") != selected.ID {
			t.Error("wrong machine contract")
			w.WriteHeader(400)
			return
		}
		lastHeader = r.Header.Get("Last-Event-ID")
		w.Header().Set("Content-Type", "text/event-stream")
		for _, id := range ids {
			payload, _ := json.Marshal(ProviderEvent{Type: "task_end", Phase: "task", Timestamp: stamp(testTime), Text: "done", TaskNum: int(id)})
			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", id, payload)
		}
	}))
	defer server.Close()
	sc := &sidecar{url: server.URL, client: providerClient()}
	last := uint64(math.MaxUint64)
	if err := s.collectBatch(f.scope, registration, sc, &last); err != nil {
		t.Fatal(err)
	}
	if last != 2 || lastHeader != "" {
		t.Fatal(last, lastHeader)
	}
	last = math.MaxUint64
	if err := s.collectBatch(f.scope, registration, sc, &last); err != nil {
		t.Fatal("replay", err)
	}
	events, _, _, _ := s.store.read(f.run, registration, 0, 100)
	if len(events) != 3 {
		t.Fatal("duplicate replay", len(events))
	}
	ids = []uint64{5}
	if err := s.collectBatch(f.scope, registration, sc, &last); err != nil {
		t.Fatal(err)
	}
	if lastHeader != "2" || last != 5 {
		t.Fatal("resume", lastHeader, last)
	}
	events, _, _, _ = s.store.read(f.run, registration, 0, 100)
	if len(events) != 5 || events[3].Status != "UNKNOWN" || !strings.Contains(events[3].Title, "gap") || events[4].TaskNumber != 5 {
		t.Fatalf("gap guessed missing events: %+v", events)
	}
	for _, event := range events {
		if event.CheckpointSHA != "" || event.CheckpointClean {
			t.Fatal("task_end sampled SHA")
		}
	}
	selected.Project = "other"
	ids = []uint64{6}
	if err := s.collectBatch(f.scope, registration, sc, &last); !errors.Is(err, ErrIntegrity) {
		t.Fatal("changed session accepted", err)
	}
	after, _, _, _ := s.store.read(f.run, registration, 0, 100)
	if len(after) != len(events) {
		t.Fatal("failed batch persisted")
	}
}

func TestSSEParserBoundsAndPartialFrames(t *testing.T) {
	payload := `{"type":"task_start","phase":"task","text":"hi","timestamp":"2026-09-26T01:00:00Z"}`
	good := "id: 1\ndata: " + payload + "\n\n"
	parsed, err := parseSSE(strings.NewReader(": comment\n\n" + good + "id: 2\ndata: {"))
	if err != nil || len(parsed) != 1 {
		t.Fatal("partial frame was not discarded", err)
	}
	for _, bad := range []string{"id: nope\ndata: " + payload + "\n\n", "id: 01\ndata: " + payload + "\n\n", "id: 1\nid: 2\ndata: " + payload + "\n\n", "data: " + payload + "\n\n", "id: 1\ndata: " + strings.Repeat("x", 65537) + "\n\n"} {
		if _, err := parseSSE(strings.NewReader(bad)); err == nil {
			t.Fatal("invalid SSE accepted")
		}
	}
	var batch strings.Builder
	for i := 1; i <= 501; i++ {
		batch.WriteString("id: " + strconv.Itoa(i) + "\ndata: " + payload + "\n\n")
	}
	parsed, err = parseSSE(strings.NewReader(batch.String()))
	if err != nil || len(parsed) != 500 {
		t.Fatal("batch unbounded", err)
	}
}

func TestReplayPreservesEventsAcrossProviderAndStoreRestarts(t *testing.T) {
	f := newBindingFixture(t)
	selected := providerSession(t, f)
	root := filepath.Join(t.TempDir(), "activity")
	registration := jsonDigest(f.catalog.runs[f.run])
	var original []Event
	for lifetime := 0; lifetime < 2; lifetime++ {
		store, err := OpenStore(root)
		if err != nil {
			t.Fatal(err)
		}
		stampAt := testTime.Add(time.Duration(lifetime) * time.Hour)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/sessions" {
				json.NewEncoder(w).Encode([]session{selected})
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			// Live sections use observation time; historical sections use a
			// following line's time. Plain output also gets a new time on replay.
			for id, kind := range []string{"output", "task_start", "section", "task_end", "iteration_start"} {
				p := ProviderEvent{Type: kind, Phase: "review", Text: kind, Timestamp: stamp(stampAt)}
				data, _ := json.Marshal(p)
				fmt.Fprintf(w, "id: %d\ndata: %s\n\n", id, data)
			}
		}))
		s := &Service{store: store, resolver: Resolver{f.root, f.catalog}, ctx: context.Background(), now: func() time.Time { return stampAt }}
		sc := &sidecar{url: server.URL, client: providerClient()}
		last := uint64(math.MaxUint64)
		err = s.collectBatch(f.scope, registration, sc, &last)
		server.Close()
		if err != nil {
			store.Close()
			t.Fatal(err)
		}
		events, _, _, err := store.read(f.run, registration, 0, 100)
		store.Close()
		if err != nil || len(events) != 5 || last != 4 {
			t.Fatal("replay changed durable event count", len(events), last, err)
		}
		if lifetime == 0 {
			original = events
		} else if !reflect.DeepEqual(events, original) {
			t.Fatal("replay changed identities, ordinals or first-observed timestamps")
		}
	}
}

func TestReplayRejectsChangedSourceOrdinalAssociation(t *testing.T) {
	for _, start := range []int{0, 2} {
		t.Run(strconv.Itoa(start), func(t *testing.T) {
			f := newBindingFixture(t)
			selected := providerSession(t, f)
			registration := jsonDigest(f.catalog.runs[f.run])
			live := []ProviderEvent{
				{Type: "output", Phase: "task", Text: "seed", Timestamp: stamp(testTime)},
				{Type: "section", Phase: "review", Section: "review iteration 1", Timestamp: stamp(testTime)},
				{Type: "output", Phase: "review", Text: "plain review output", Timestamp: stamp(testTime)},
				{Type: "output", Phase: "review", Text: "timestamped output", Timestamp: stamp(testTime)},
			}
			// The native completed-session loader emits plain output before its
			// pending section; the active tailer emits the section first.
			completed := []ProviderEvent{live[0], live[2], live[1], live[3]}
			store := newStore(t)
			root := store.root
			var original []Event
			for lifetime, payloads := range [][]ProviderEvent{live, completed} {
				if lifetime > 0 {
					store.Close()
					var err error
					store, err = OpenStore(root)
					if err != nil {
						t.Fatal(err)
					}
					defer store.Close()
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/sessions" {
						json.NewEncoder(w).Encode([]session{selected})
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					for id, payload := range payloads {
						if lifetime == 0 && id < start {
							continue
						}
						data, _ := json.Marshal(payload)
						fmt.Fprintf(w, "id: %d\ndata: %s\n\n", id, data)
					}
				}))
				s := &Service{store: store, resolver: Resolver{f.root, f.catalog}, ctx: context.Background(), now: func() time.Time { return testTime }}
				last := uint64(math.MaxUint64)
				err := s.collectBatch(f.scope, registration, &sidecar{url: server.URL, client: providerClient()}, &last)
				server.Close()
				if lifetime == 0 && err != nil {
					t.Fatal(err)
				}
				if lifetime > 0 && !errors.Is(err, ErrIntegrity) {
					t.Fatal("changed replay association was accepted", err)
				}
				events, _, _, err := store.read(f.run, registration, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				if lifetime == 0 {
					original = events
				} else if !reflect.DeepEqual(events, original) || last != math.MaxUint64 {
					t.Fatal("conflicting replay changed durable events or resume position")
				}
			}
		})
	}
}

func TestProviderBatchTimeoutPreservesCompleteFramesAndResumes(t *testing.T) {
	f := newBindingFixture(t)
	selected := providerSession(t, f)
	s := &Service{store: newStore(t), resolver: Resolver{f.root, f.catalog}, ctx: context.Background(), now: func() time.Time { return testTime }}
	registration := jsonDigest(f.catalog.runs[f.run])
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions" {
			json.NewEncoder(w).Encode([]session{selected})
			return
		}
		request := requests.Add(1)
		expected, id := "", 0
		if request > 1 {
			expected, id = "0", 1
		}
		if r.Header.Get("Last-Event-ID") != expected {
			t.Errorf("resume header: got %q, want %q", r.Header.Get("Last-Event-ID"), expected)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		payload, _ := json.Marshal(ProviderEvent{Type: "output", Phase: "task", Text: fmt.Sprint(id), Timestamp: stamp(testTime)})
		fmt.Fprintf(w, "id: %d\ndata: %s\n\nid: %d\ndata: {", id, payload, id+1)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := providerClient()
	client.Timeout = 100 * time.Millisecond
	sc := &sidecar{url: server.URL, client: client}
	last := uint64(math.MaxUint64)
	for i := uint64(0); i < 2; i++ {
		if err := s.collectBatch(f.scope, registration, sc, &last); err != nil || last != i {
			t.Fatal("timeout lost complete frame", err, last)
		}
	}
	events, _, _, err := s.store.read(f.run, registration, 0, 100)
	if err != nil || len(events) != 2 || events[0].Detail != "0" || events[1].Detail != "1" {
		t.Fatal("partial frame persisted or resume duplicated data", events, err)
	}
}

func TestProviderBatchCancellationClosesLiveRequest(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sc := &sidecar{url: server.URL, client: providerClient()}
	done := make(chan error, 1)
	go func() { _, err := sc.batch(ctx, "session", math.MaxUint64); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled request succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("provider read ignored cancellation")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("provider connection remained open")
	}
}

func TestProviderBatchRejectsTrustChangesDuringRequest(t *testing.T) {
	for _, change := range []string{"binding", "session", "progress-replaced", "progress-rewritten"} {
		t.Run(change, func(t *testing.T) {
			f := newBindingFixture(t)
			selected := providerSession(t, f)
			registration := jsonDigest(f.catalog.runs[f.run])
			s := &Service{store: newStore(t), resolver: Resolver{f.root, f.catalog}, ctx: context.Background(), now: func() time.Time { return testTime }}
			stored, err := s.store.Append(f.run, registration, provider(t, f.run, "existing"))
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/sessions" {
					json.NewEncoder(w).Encode([]session{selected})
					return
				}
				var err error
				switch change {
				case "binding":
					err = os.WriteFile(f.bindingPath, []byte("{}"), 0600)
				case "session":
					selected.Project = "changed"
				case "progress-replaced":
					path := filepath.Join(selected.DirPath, "progress-plan.txt")
					if err = os.Rename(path, path+".old"); err == nil {
						err = os.WriteFile(path, []byte("immutable progress prefix\n"), 0600)
					}
				case "progress-rewritten":
					err = os.WriteFile(filepath.Join(selected.DirPath, "progress-plan.txt"), []byte("rewritten progress prefix\n"), 0600)
				}
				if err != nil {
					t.Error("mutate trust fixture", err)
					w.WriteHeader(500)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				payload, _ := json.Marshal(ProviderEvent{Type: "task_end", Phase: "task", Text: "done", Timestamp: stamp(testTime)})
				fmt.Fprintf(w, "id: 1\ndata: %s\n\n", payload)
			}))
			defer server.Close()
			last := uint64(0)
			sc := &sidecar{url: server.URL, client: providerClient()}
			if err := s.collectBatch(f.scope, registration, sc, &last); !errors.Is(err, ErrIntegrity) {
				t.Fatal("changed trust accepted", err)
			}
			events, _, _, err := s.store.read(f.run, registration, 0, 100)
			if err != nil || len(events) != 1 || events[0] != stored || last != 0 {
				t.Fatal("rejected batch changed events or resume position", events, last, err)
			}
			if proof, err := s.store.proof(f.run, registration); err != nil || proof != nil {
				t.Fatal("rejected batch changed durable proof", proof, err)
			}
		})
	}
}

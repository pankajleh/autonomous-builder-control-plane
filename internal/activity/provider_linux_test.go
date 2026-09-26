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
	"strconv"
	"strings"
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

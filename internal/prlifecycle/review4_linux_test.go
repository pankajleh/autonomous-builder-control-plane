//go:build linux

package prlifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type review4ReplayFixture struct {
	controller  *Controller
	github      *review2Fixture
	request     Request
	key         PRResourceKeyV1
	terminalSHA string
}

func makeReview4ReplayFixture(t *testing.T, shape string) review4ReplayFixture {
	t.Helper()
	authority := lifecycleAuthority(t)
	github := newReview2Fixture(authority)
	runID := "review4-" + shape
	controller, _, _, server := makeCorrectionController(t, authority, github, runID, time.Now, nil)
	t.Cleanup(server.Close)
	request := Request{RunID: runID, Authority: authority, Title: "first", Body: "body"}
	result, err := controller.Upsert(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if shape == "lower-barrier" {
		controller.github.writeConstructionHook = func() error { return errors.New("abandon after revision") }
		_, err = controller.Upsert(context.Background(), Request{RunID: runID, Authority: authority, Title: "second", Body: "body"})
		controller.github.writeConstructionHook = nil
		if err == nil {
			t.Fatal("write-construction fault did not leave a later zero-write ordinal")
		}
	}
	return review4ReplayFixture{
		controller:  controller,
		github:      github,
		request:     request,
		key:         review3Key(t, controller, request),
		terminalSHA: result.TerminalSHA256(),
	}
}

func rewriteRevision(t *testing.T, fixture review4ReplayFixture, mutate func(*revisionRecord)) {
	t.Helper()
	path := filepath.Join(fixture.controller.store.Root(), recordPrefix(fixture.key, 1)+"revision.json")
	data, err := os.ReadFile(path)
	var revision revisionRecord
	if err != nil || strictJSON(data, &revision) != nil {
		t.Fatalf("read revision for mutation: %v", err)
	}
	mutate(&revision)
	data, err = json.Marshal(revision)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func canonicalAlteredAuthority(t *testing.T, raw json.RawMessage) json.RawMessage {
	t.Helper()
	var mirror authorityMirror
	if err := strictJSON(raw, &mirror); err != nil {
		t.Fatal(err)
	}
	mirror.Actor.Subject = "github-user-id:43"
	altered, err := json.Marshal(mirror)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCanonicalAuthority(altered, digestBytes(altered)); err != nil {
		t.Fatalf("altered authority is not independently canonical: %v", err)
	}
	return altered
}

func review4RemoteCounts(fixture review4ReplayFixture) (int, int) {
	fixture.github.mu.Lock()
	defer fixture.github.mu.Unlock()
	return fixture.github.requests, fixture.github.writes
}

func review4TerminalCount(t *testing.T, fixture review4ReplayFixture) int {
	t.Helper()
	terminals, err := filepath.Glob(filepath.Join(fixture.controller.store.Root(), "*-terminal.json"))
	if err != nil {
		t.Fatal(err)
	}
	return len(terminals)
}

func requireReview4IntegrityWithoutEffects(t *testing.T, fixture review4ReplayFixture, request Request) {
	t.Helper()
	requestsBefore, writesBefore := review4RemoteCounts(fixture)
	terminalsBefore := review4TerminalCount(t, fixture)
	result, err := fixture.controller.Upsert(context.Background(), request)
	requireLifecycleCode(t, err, CodeIntegrityFailure)
	requestsAfter, writesAfter := review4RemoteCounts(fixture)
	terminalsAfter := review4TerminalCount(t, fixture)
	if requestsAfter != requestsBefore || writesAfter != writesBefore {
		t.Fatalf("revision integrity failure contacted GitHub: requests=%d/%d writes=%d/%d", requestsBefore, requestsAfter, writesBefore, writesAfter)
	}
	if terminalsAfter != terminalsBefore {
		t.Fatalf("revision integrity failure published a terminal: %d/%d", terminalsBefore, terminalsAfter)
	}
	if result.TerminalSHA256() == fixture.terminalSHA {
		t.Fatal("revision integrity failure returned the prior terminal as success")
	}
}

func TestRevisionRequestDigestCannotSelectWrongTerminal(t *testing.T) {
	for _, shape := range []string{"current-max", "lower-barrier"} {
		t.Run(shape, func(t *testing.T) {
			fixture := makeReview4ReplayFixture(t, shape)
			different := Request{RunID: fixture.request.RunID, Authority: fixture.request.Authority, Title: "different", Body: "request"}
			authority, err := different.Authority.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			requestSHA, err := canonicalDigest(struct {
				Authority json.RawMessage `json:"authority"`
				Title     string          `json:"title"`
				Body      string          `json:"body"`
			}{authority, different.Title, different.Body})
			if err != nil {
				t.Fatal(err)
			}
			rewriteRevision(t, fixture, func(revision *revisionRecord) {
				revision.RequestSHA256 = requestSHA
			})
			requireReview4IntegrityWithoutEffects(t, fixture, different)
		})
	}
}

func TestRevisionDuplicatedIdentityTamperingFailsClosed(t *testing.T) {
	mutations := map[string]func(*testing.T, *revisionRecord){
		"source-digest": func(_ *testing.T, revision *revisionRecord) {
			revision.SourceSHA256 = strings.Repeat("0", 64)
		},
		"derived-digest": func(_ *testing.T, revision *revisionRecord) {
			revision.AuthoritySHA256 = strings.Repeat("0", 64)
		},
		"document-digest": func(_ *testing.T, revision *revisionRecord) {
			revision.DocumentSHA256 = strings.Repeat("0", 64)
		},
		"source-authority": func(t *testing.T, revision *revisionRecord) {
			revision.SourceAuthority = canonicalAlteredAuthority(t, revision.SourceAuthority)
		},
		"derived-authority": func(t *testing.T, revision *revisionRecord) {
			revision.Authority = canonicalAlteredAuthority(t, revision.Authority)
		},
	}
	for _, shape := range []string{"current-max", "lower-barrier"} {
		for name, mutate := range mutations {
			t.Run(shape+"/"+name, func(t *testing.T) {
				fixture := makeReview4ReplayFixture(t, shape)
				rewriteRevision(t, fixture, func(revision *revisionRecord) {
					mutate(t, revision)
				})
				requireReview4IntegrityWithoutEffects(t, fixture, fixture.request)
			})
		}
	}
}

func TestRevisionIdentityPositiveReplayControls(t *testing.T) {
	for _, shape := range []string{"current-max", "lower-barrier"} {
		t.Run(shape, func(t *testing.T) {
			fixture := makeReview4ReplayFixture(t, shape)
			requestsBefore, writesBefore := review4RemoteCounts(fixture)
			terminalsBefore := review4TerminalCount(t, fixture)
			result, err := fixture.controller.Upsert(context.Background(), fixture.request)
			if err != nil {
				t.Fatal(err)
			}
			requestsAfter, writesAfter := review4RemoteCounts(fixture)
			if result.TerminalSHA256() != fixture.terminalSHA {
				t.Fatal("untampered replay did not return the identical terminal")
			}
			if requestsAfter != requestsBefore || writesAfter != writesBefore {
				t.Fatalf("untampered replay contacted GitHub: requests=%d/%d writes=%d/%d", requestsBefore, requestsAfter, writesBefore, writesAfter)
			}
			if terminalsAfter := review4TerminalCount(t, fixture); terminalsAfter != terminalsBefore {
				t.Fatalf("untampered replay changed terminal count: %d/%d", terminalsBefore, terminalsAfter)
			}
		})
	}
}

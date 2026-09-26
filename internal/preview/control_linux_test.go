//go:build linux

package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

type previewAuthenticator struct{ principal serviceapi.Principal }

func (a previewAuthenticator) Authenticate(*http.Request) (serviceapi.Principal, error) {
	return a.principal, nil
}

type previewCatalog struct{ catalogFixture }

func (c previewCatalog) ListRuns(string, int) ([]runtimecatalog.RunRegistrationV1, bool, error) {
	return []runtimecatalog.RunRegistrationV1{c.reg}, false, nil
}

func previewHandler(t *testing.T, s *Service, p serviceapi.Principal, authority *serviceapi.AuthorityMatcher) http.Handler {
	t.Helper()
	signer, err := serviceapi.NewCursorSignerWithClock("preview-test", make([]byte, 32), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	server, err := serviceapi.NewServer(serviceapi.ServerConfig{
		Preview: s, Authenticator: previewAuthenticator{p}, Authority: authority,
		Catalog: &previewCatalog{catalogFixture{runtimecatalog.RunRegistrationV1{RunID: "run-1"}}}, CursorSigner: signer,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server.Handler()
}

func previewRequest(t *testing.T, h http.Handler, method, path string, body any, status int) []byte {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(data)))
	if w.Code != status {
		t.Fatalf("%s %s: %d, want %d: %s", method, path, w.Code, status, w.Body.String())
	}
	return w.Body.Bytes()
}

func previewSequence(s *Service) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.sequence
}

func TestHTTPPreviewOwnershipAndAuthorityReceiptsSurviveRestart(t *testing.T) {
	ctx := context.Background()
	p, other := principal(), principal()
	other.PrincipalID = "other-gateway"
	file := filepath.Join(t.TempDir(), "grants.json")
	writeJSON(t, file, serviceapi.AuthorityGrantFileV1{Principals: []serviceapi.AuthorityGrantV1{
		{PrincipalID: p.PrincipalID, RequiredAuthorities: []string{"preview.control"}, MayAssertDelegatedActor: true},
		{PrincipalID: other.PrincipalID, RequiredAuthorities: []string{"preview.control"}, MayAssertDelegatedActor: true},
	}})
	grants, err := serviceapi.LoadAuthorityMatcher(file)
	if err != nil {
		t.Fatal(err)
	}
	// Even a byte-level authority-file change must not inherit admitted receipts.
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := serviceapi.LoadAuthorityMatcher(file)
	if err != nil || changed.Digest() == grants.Digest() {
		t.Fatal("changed authority fixture", err)
	}
	s, _ := fixtureService(t, &memoryRuntime{available: true, healthy: true})
	ownerHTTP := previewHandler(t, s, p, grants)
	otherHTTP := previewHandler(t, s, other, grants)
	base := "/v1/runs/run-1/previews"
	c := createCommand("shared-request-id")
	created := previewRequest(t, ownerHTTP, http.MethodPost, base, c, 202)
	var v serviceapi.PreviewV1
	if err = json.Unmarshal(created, &v); err != nil {
		t.Fatal(err)
	}
	awaitPreview(t, s, v.PreviewID, "READY", "")
	detail := base + "/" + v.PreviewID
	stop := serviceapi.PreviewStopRequestV1{SchemaVersion: 1, RequestID: "stop-owner", ExpectedRunID: "run-1", ExpectedPreviewID: v.PreviewID, DelegatedActor: c.DelegatedActor}

	before := previewSequence(s)
	if body := previewRequest(t, otherHTTP, http.MethodGet, base, nil, 200); string(body) != "{\"schema_version\":\"PreviewListV1\",\"run_id\":\"run-1\",\"previews\":[]}\n" {
		t.Fatal("cross-principal list disclosed metadata", string(body))
	}
	for _, route := range []string{detail, base + "/" + strings.Repeat("f", 64)} {
		previewRequest(t, otherHTTP, http.MethodGet, route, nil, 404)
	}
	previewRequest(t, otherHTTP, http.MethodPost, detail+"/stop", stop, 404)
	if previewSequence(s) != before {
		t.Fatal("cross-principal request mutated history")
	}
	if got, err := s.ReadPreview(ctx, p, "run-1", v.PreviewID); err != nil || got.Status != "READY" {
		t.Fatal("cross-principal stop ended runtime", got, err)
	}
	// The same request ID belongs to a separate receipt namespace for each principal.
	otherCreated := previewRequest(t, otherHTTP, http.MethodPost, base, c, 202)
	var otherPreview serviceapi.PreviewV1
	if json.Unmarshal(otherCreated, &otherPreview) != nil || otherPreview.PreviewID == v.PreviewID || otherPreview.Revision != v.Revision+1 {
		t.Fatal("principal inherited create receipt")
	}
	stopped := previewRequest(t, ownerHTTP, http.MethodPost, detail+"/stop", stop, 202)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, _ := serviceAt(t, s.store.root, &memoryRuntime{available: true, healthy: true}, time.Now)
	ownerHTTP = previewHandler(t, recovered, p, grants)
	otherHTTP = previewHandler(t, recovered, other, grants)
	changedHTTP := previewHandler(t, recovered, p, changed)
	before = previewSequence(recovered)
	if replay := previewRequest(t, ownerHTTP, http.MethodPost, base, c, 202); !bytes.Equal(replay, created) {
		t.Fatal("create replay changed after restart")
	}
	if replay := previewRequest(t, otherHTTP, http.MethodPost, base, c, 202); !bytes.Equal(replay, otherCreated) {
		t.Fatal("other principal lost its receipt")
	}
	if replay := previewRequest(t, ownerHTTP, http.MethodPost, detail+"/stop", stop, 202); !bytes.Equal(replay, stopped) {
		t.Fatal("stop replay changed after restart")
	}
	previewRequest(t, changedHTTP, http.MethodPost, base, c, 409)
	previewRequest(t, changedHTTP, http.MethodPost, detail+"/stop", stop, 409)
	previewRequest(t, otherHTTP, http.MethodPost, detail+"/stop", stop, 404)
	previewRequest(t, otherHTTP, http.MethodGet, detail, nil, 404)
	previewRequest(t, ownerHTTP, http.MethodGet, base+"/"+otherPreview.PreviewID, nil, 404)
	if previewSequence(recovered) != before {
		t.Fatal("replay, conflict or denied request mutated history")
	}
	for _, pair := range []struct {
		h  http.Handler
		id string
	}{{ownerHTTP, v.PreviewID}, {otherHTTP, otherPreview.PreviewID}} {
		body := previewRequest(t, pair.h, http.MethodGet, base, nil, 200)
		var list serviceapi.PreviewListV1
		if json.Unmarshal(body, &list) != nil || len(list.Previews) != 1 || list.Previews[0].PreviewID != pair.id {
			t.Fatal("recovered list did not preserve ownership", string(body))
		}
		for _, private := range []string{p.PrincipalID, other.PrincipalID, grants.Digest(), "principal_id", "authority_digest", "Owner", "Binding"} {
			if bytes.Contains(body, []byte(private)) {
				t.Fatal("internal admission metadata escaped DTO", private)
			}
		}
	}
}

func TestPreviewReceiptConflictsHaveNoAdditionalMutation(t *testing.T) {
	ctx := context.Background()
	s, _ := fixtureService(t, &memoryRuntime{available: true, healthy: true})
	c := createCommand("create")
	v, err := s.CreatePreview(ctx, principal(), testAuthorityDigest, "run-1", c)
	if err != nil {
		t.Fatal(err)
	}
	awaitPreview(t, s, v.PreviewID, "READY", "")
	stop := serviceapi.PreviewStopRequestV1{SchemaVersion: 1, RequestID: "stop", ExpectedRunID: "run-1", ExpectedPreviewID: v.PreviewID, DelegatedActor: c.DelegatedActor}
	if _, err = s.StopPreview(ctx, principal(), testAuthorityDigest, "run-1", v.PreviewID, stop); err != nil {
		t.Fatal(err)
	}
	awaitCleanup(t, s)
	before := previewSequence(s)
	for _, change := range []func(*serviceapi.PreviewRequestV1){
		func(c *serviceapi.PreviewRequestV1) { c.ProfileID = "other" },
		func(c *serviceapi.PreviewRequestV1) { c.CheckpointActivityID = strings.Repeat("c", 64) },
		func(c *serviceapi.PreviewRequestV1) { c.ExpectedRunID = "run-2" },
		func(c *serviceapi.PreviewRequestV1) { c.DelegatedActor.SubjectID = "bob" },
		func(c *serviceapi.PreviewRequestV1) { c.DelegatedActor.SubjectType = serviceapi.PrincipalOperator },
		func(c *serviceapi.PreviewRequestV1) { c.RequestID = stop.RequestID },
	} {
		changed := c
		change(&changed)
		if _, err = s.CreatePreview(ctx, principal(), testAuthorityDigest, changed.ExpectedRunID, changed); !errors.Is(err, serviceapi.ErrRequestIDConflict) {
			t.Fatal("changed create inherited receipt", changed, err)
		}
	}
	for _, change := range []func(*serviceapi.PreviewStopRequestV1){
		func(c *serviceapi.PreviewStopRequestV1) { c.ExpectedPreviewID = strings.Repeat("c", 64) },
		func(c *serviceapi.PreviewStopRequestV1) { c.ExpectedRunID = "run-2" },
		func(c *serviceapi.PreviewStopRequestV1) { c.DelegatedActor.SubjectID = "bob" },
		func(c *serviceapi.PreviewStopRequestV1) { c.DelegatedActor.SubjectType = serviceapi.PrincipalOperator },
		func(c *serviceapi.PreviewStopRequestV1) { c.RequestID = "create" },
	} {
		changed := stop
		change(&changed)
		if _, err = s.StopPreview(ctx, principal(), testAuthorityDigest, changed.ExpectedRunID, changed.ExpectedPreviewID, changed); !errors.Is(err, serviceapi.ErrRequestIDConflict) {
			t.Fatal("changed stop inherited receipt", changed, err)
		}
	}
	for _, authority := range []string{"", "not-a-digest", strings.Repeat("b", 64)} {
		if _, err = s.CreatePreview(ctx, principal(), authority, "run-1", c); err == nil {
			t.Fatal("missing or changed authority admitted replay")
		}
		if _, err = s.StopPreview(ctx, principal(), authority, "run-1", v.PreviewID, stop); err == nil {
			t.Fatal("missing or changed authority admitted stop replay")
		}
	}
	for _, kind := range []serviceapi.PrincipalType{serviceapi.PrincipalUser, serviceapi.PrincipalOperator, serviceapi.PrincipalTest} {
		p := principal()
		p.PrincipalType = kind
		if list, err := s.ListPreviews(ctx, p, "run-1"); err != nil || len(list.Previews) != 0 {
			t.Fatal("wrong principal type inherited visibility")
		}
		if _, err = s.ReadPreview(ctx, p, "run-1", v.PreviewID); !errors.Is(err, serviceapi.ErrDependencyNotFound) {
			t.Fatal("wrong principal type inherited detail")
		}
		if _, err = s.CreatePreview(ctx, p, testAuthorityDigest, "run-1", c); err == nil {
			t.Fatal("wrong principal type inherited receipt")
		}
		if _, err = s.StopPreview(ctx, p, testAuthorityDigest, "run-1", v.PreviewID, stop); err == nil {
			t.Fatal("wrong principal type inherited stop")
		}
	}
	if previewSequence(s) != before {
		t.Fatal("rejected command mutated history")
	}
}

func TestJournalRejectsUnboundAuthorityAndOwnerRebinding(t *testing.T) {
	s, _ := fixtureService(t, &memoryRuntime{available: true, healthy: true})
	ctx := context.Background()
	v, err := s.CreatePreview(ctx, principal(), testAuthorityDigest, "run-1", createCommand("journal-owner"))
	if err != nil {
		t.Fatal(err)
	}
	awaitPreview(t, s, v.PreviewID, "READY", "")
	stop := serviceapi.PreviewStopRequestV1{SchemaVersion: 1, RequestID: "journal-stop", ExpectedRunID: "run-1", ExpectedPreviewID: v.PreviewID, DelegatedActor: createCommand("").DelegatedActor}
	if _, err = s.StopPreview(ctx, principal(), testAuthorityDigest, "run-1", v.PreviewID, stop); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.store.root, "history.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"legacy-unowned", "owner-rebound", "missing-authority", "receipt-principal", "receipt-actor", "receipt-body", "receipt-result", "receipt-operation"} {
		t.Run(scenario, func(t *testing.T) {
			lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
			var output bytes.Buffer
			head := ""
			for i, line := range lines {
				var f frame
				if json.Unmarshal(line, &f) != nil {
					t.Fatal("bad fixture")
				}
				if scenario == "legacy-unowned" {
					f.Owner = ownerIdentity{}
					if f.Receipt != nil {
						f.Receipt.Binding = commandBinding{}
					}
				} else if i == len(lines)-1 {
					r := f.Receipt
					if r == nil {
						t.Fatal("expected final stop receipt")
					}
					switch scenario {
					case "owner-rebound":
						f.Owner.PrincipalID = "other-gateway"
						r.Binding.Principal = f.Owner
					case "missing-authority":
						r.Binding.AuthorityDigest = ""
					case "receipt-principal":
						r.Binding.Principal.PrincipalID = "other-gateway"
					case "receipt-actor":
						r.Binding.DelegatedActor.SubjectType = serviceapi.PrincipalService
					case "receipt-body":
						r.Binding.BodyDigest = strings.Repeat("d", 64)
					case "receipt-result":
						r.Result.PreviewID = strings.Repeat("e", 64)
					case "receipt-operation":
						r.Binding.Operation = "create"
					}
					// Recompute binding and chain hashes: semantic validation must
					// reject these frames even with an internally consistent chain.
					r.Key = receiptIdentity(r.Binding.Principal, r.Binding.RequestID)
					r.Digest = jsonDigest(r.Binding)
				}
				f.Previous = head
				encoded, err := json.Marshal(f)
				if err != nil {
					t.Fatal(err)
				}
				head = digest(encoded)
				output.Write(encoded)
				output.WriteByte('\n')
			}
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "history.jsonl"), output.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "head"), []byte(head), 0600); err != nil {
				t.Fatal(err)
			}
			if loaded, err := openLoadedStore(root); err == nil {
				loaded.close()
				t.Fatal("journal accepted missing or rebound admission metadata")
			}
			preserved, err := os.ReadFile(filepath.Join(root, "history.jsonl"))
			if err != nil || !bytes.Equal(preserved, output.Bytes()) {
				t.Fatal("rejected history was modified", err)
			}
		})
	}
}

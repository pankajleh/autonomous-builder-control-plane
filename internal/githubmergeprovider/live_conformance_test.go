package githubmergeprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/githublifecycle"
)

// TestTask3ControlledLiveConformance is acceptance-only. It cannot run from
// ordinary implementation gates: both an explicit destructive-test phrase
// and a complete disposable-ref fixture are required.
func TestTask3ControlledLiveConformance(t *testing.T) {
	if os.Getenv("GITHUBMERGEPROVIDER_LIVE_ACCEPTANCE") != "I_ACCEPT_DISPOSABLE_TWO_REF_MUTATION" {
		t.Skip("controlled live conformance requires explicit acceptance authority")
	}
	var fixture struct {
		Owner        string `json:"owner"`
		Repository   string `json:"repository"`
		RepositoryID string `json:"repository_id"`
		BaseRef      string `json:"base_ref"`
		HeadRef      string `json:"head_ref"`
		BaseBefore   string `json:"base_before"`
		BaseAfter    string `json:"base_after"`
		HeadOID      string `json:"head_oid"`
		Disposable   bool   `json:"disposable"`
	}
	if err := json.Unmarshal([]byte(os.Getenv("GITHUBMERGEPROVIDER_LIVE_FIXTURE")), &fixture); err != nil || !fixture.Disposable ||
		!strings.HasPrefix(fixture.BaseRef, "refs/heads/abcp-live-disposable/") ||
		!strings.HasPrefix(fixture.HeadRef, "refs/heads/abcp-live-disposable/") || fixture.BaseRef == fixture.HeadRef {
		t.Fatal("live fixture must explicitly identify two distinct disposable refs")
	}
	for _, oid := range []string{fixture.BaseBefore, fixture.BaseAfter, fixture.HeadOID} {
		if _, err := githublifecycle.NewGitSHA(oid); err != nil {
			t.Fatal("live fixture contains an invalid complete OID")
		}
	}
	token := os.Getenv("GITHUBMERGEPROVIDER_LIVE_TOKEN")
	if token == "" {
		t.Fatal("live acceptance credential is absent")
	}
	client := &http.Client{
		Transport:     productionTransport(githublifecycle.DefaultLimits()),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       30 * time.Second,
	}
	doGraphQL := func(baseBefore, headBefore string) error {
		body, _ := json.Marshal(struct {
			Query     string `json:"query"`
			Variables struct {
				Input struct {
					ClientMutationID string `json:"clientMutationId"`
					RefUpdates       []struct {
						AfterOID  string `json:"afterOid"`
						BeforeOID string `json:"beforeOid"`
						Force     bool   `json:"force"`
						Name      string `json:"name"`
					} `json:"refUpdates"`
					RepositoryID string `json:"repositoryId"`
				} `json:"input"`
			} `json:"variables"`
		}{Query: githublifecycle.GitHubUpdateRefsDocumentV1})
		var requestBody map[string]any
		_ = json.Unmarshal(body, &requestBody)
		requestBody["variables"] = map[string]any{"input": map[string]any{
			"clientMutationId": "abcp-live-conformance", "repositoryId": fixture.RepositoryID,
			"refUpdates": []map[string]any{
				{"name": fixture.BaseRef, "beforeOid": baseBefore, "afterOid": fixture.BaseAfter, "force": false},
				{"name": fixture.HeadRef, "beforeOid": headBefore, "afterOid": fixture.HeadOID, "force": false},
			},
		}}
		body, _ = json.Marshal(requestBody)
		request := mustValue(http.NewRequestWithContext(context.Background(), http.MethodPost, apiOrigin+"/graphql", bytes.NewReader(body)))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-GitHub-Api-Version", apiVersion)
		response, err := client.Do(request)
		if err != nil {
			return errors.New("live updateRefs transport failed")
		}
		defer response.Body.Close()
		responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil || response.StatusCode != 200 {
			return errors.New("live updateRefs returned an invalid response")
		}
		var envelope struct {
			Data struct {
				UpdateRefs *struct {
					ClientMutationID string `json:"clientMutationId"`
				} `json:"updateRefs"`
			} `json:"data"`
			Errors []json.RawMessage `json:"errors"`
		}
		if err := json.Unmarshal(responseBody, &envelope); err != nil {
			return errors.New("live updateRefs response schema is invalid")
		}
		if len(envelope.Errors) != 0 {
			return fmt.Errorf("atomic rejection")
		}
		if envelope.Data.UpdateRefs == nil || envelope.Data.UpdateRefs.ClientMutationID != "abcp-live-conformance" {
			return errors.New("live updateRefs response did not echo its mutation identity")
		}
		return nil
	}
	readRef := func(ref string) string {
		path := repoPath(mustValue(githublifecycle.NewRepository(fixture.Owner, fixture.Repository))) + "/git/ref/" + strings.TrimPrefix(ref, "refs/")
		request := mustValue(http.NewRequestWithContext(context.Background(), http.MethodGet, apiOrigin+path, nil))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("X-GitHub-Api-Version", apiVersion)
		response := mustValue(client.Do(request))
		defer response.Body.Close()
		var value gitRefResponse
		if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&value) != nil {
			t.Fatal("live ref observation failed")
		}
		return value.Object.SHA
	}
	if err := doGraphQL(fixture.BaseBefore, fixture.HeadOID); err != nil {
		t.Fatalf("accepted two-ref conformance mutation failed: %v", err)
	}
	if readRef(fixture.BaseRef) != fixture.BaseAfter || readRef(fixture.HeadRef) != fixture.HeadOID {
		t.Fatal("accepted two-ref mutation did not produce the exact base/head result")
	}
	wrong := strings.Repeat("f", len(fixture.BaseAfter))
	if wrong == fixture.BaseAfter || wrong == fixture.HeadOID {
		wrong = strings.Repeat("e", len(fixture.BaseAfter))
	}
	if err := doGraphQL(wrong, fixture.HeadOID); err == nil {
		t.Fatal("wrong-base atomic boundary unexpectedly succeeded")
	}
	if readRef(fixture.BaseRef) != fixture.BaseAfter || readRef(fixture.HeadRef) != fixture.HeadOID {
		t.Fatal("wrong-base rejection modified a disposable ref")
	}
	if err := doGraphQL(fixture.BaseAfter, wrong); err == nil {
		t.Fatal("wrong-head atomic boundary unexpectedly succeeded")
	}
	if readRef(fixture.BaseRef) != fixture.BaseAfter || readRef(fixture.HeadRef) != fixture.HeadOID {
		t.Fatal("wrong-head rejection modified a disposable ref")
	}
}

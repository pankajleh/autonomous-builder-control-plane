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

type liveAtomicityStep struct {
	Name       string
	BaseBefore string
	BaseAfter  string
	HeadBefore string
	HeadAfter  string
	MustReject bool
}

func controlledLiveAtomicitySteps(baseBefore, baseAfter, headOID, wrong string) []liveAtomicityStep {
	return []liveAtomicityStep{
		{Name: "wrong-base", BaseBefore: wrong, BaseAfter: baseAfter, HeadBefore: headOID, HeadAfter: headOID, MustReject: true},
		{Name: "wrong-head", BaseBefore: baseBefore, BaseAfter: baseAfter, HeadBefore: wrong, HeadAfter: headOID, MustReject: true},
		{Name: "accepted", BaseBefore: baseBefore, BaseAfter: baseAfter, HeadBefore: headOID, HeadAfter: headOID},
	}
}

func TestTask3ClosureM04LiveAtomicityOrdering(t *testing.T) {
	steps := controlledLiveAtomicitySteps("base-before", "base-after", "head", "wrong")
	if len(steps) != 3 || steps[0].Name != "wrong-base" || !steps[0].MustReject || steps[1].Name != "wrong-head" || !steps[1].MustReject ||
		steps[1].BaseBefore != "base-before" || steps[1].BaseAfter != "base-after" || steps[2].Name != "accepted" || steps[2].MustReject {
		t.Fatalf("live atomicity ordering is not negative, negative, positive with a material wrong-head base update: %+v", steps)
	}
}

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
	doGraphQL := func(baseBefore, baseAfter, headBefore, headAfter string) error {
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
				{"name": fixture.BaseRef, "beforeOid": baseBefore, "afterOid": baseAfter, "force": false},
				{"name": fixture.HeadRef, "beforeOid": headBefore, "afterOid": headAfter, "force": false},
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
	if readRef(fixture.BaseRef) != fixture.BaseBefore || readRef(fixture.HeadRef) != fixture.HeadOID {
		t.Fatal("disposable refs are not at the declared initial boundary")
	}
	defer func() {
		base := readRef(fixture.BaseRef)
		head := readRef(fixture.HeadRef)
		if base != fixture.BaseBefore || head != fixture.HeadOID {
			if err := doGraphQL(base, fixture.BaseBefore, head, fixture.HeadOID); err != nil {
				t.Errorf("mandatory disposable-ref cleanup failed: %v", err)
				return
			}
		}
		if readRef(fixture.BaseRef) != fixture.BaseBefore || readRef(fixture.HeadRef) != fixture.HeadOID {
			t.Error("mandatory disposable-ref cleanup did not restore the fixture")
		}
	}()
	wrong := strings.Repeat("f", len(fixture.BaseAfter))
	if wrong == fixture.BaseAfter || wrong == fixture.HeadOID {
		wrong = strings.Repeat("e", len(fixture.BaseAfter))
	}
	for _, step := range controlledLiveAtomicitySteps(fixture.BaseBefore, fixture.BaseAfter, fixture.HeadOID, wrong) {
		err := doGraphQL(step.BaseBefore, step.BaseAfter, step.HeadBefore, step.HeadAfter)
		if step.MustReject {
			if err == nil {
				t.Fatalf("%s atomic boundary unexpectedly succeeded", step.Name)
			}
			if readRef(fixture.BaseRef) != fixture.BaseBefore || readRef(fixture.HeadRef) != fixture.HeadOID {
				t.Fatalf("%s rejection modified a disposable ref", step.Name)
			}
			continue
		}
		if err != nil {
			t.Fatalf("accepted two-ref conformance mutation failed: %v", err)
		}
		if readRef(fixture.BaseRef) != fixture.BaseAfter || readRef(fixture.HeadRef) != fixture.HeadOID {
			t.Fatal("accepted two-ref mutation did not produce the exact base/head result")
		}
	}
}

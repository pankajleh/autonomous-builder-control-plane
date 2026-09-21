//go:build linux

package runadmission

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

type admissionTestCatalog struct {
	mu      sync.Mutex
	records map[string]runtimecatalog.RunRegistrationV1
	err     error
}

func (c *admissionTestCatalog) ReadRun(runID string) (runtimecatalog.RunRegistrationV1, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return runtimecatalog.RunRegistrationV1{}, c.err
	}
	if record, ok := c.records[runID]; ok {
		return record, nil
	}
	return runtimecatalog.RunRegistrationV1{}, os.ErrNotExist
}

type admissionFixture struct {
	controller *Controller
	catalog    *admissionTestCatalog
	request    serviceapi.RunAdmissionRequestV1
	principal  serviceapi.Principal
	repository string
	service    string
	input      string
	locks      []*os.File
	starts     [][]string
	mu         sync.Mutex
}

func TestDeterministicRunIdentityAndSemanticDigest(t *testing.T) {
	request := testAdmissionRequest(strings.Repeat("a", 40))
	firstID := DeriveRunID("service-1", request.RequestID)
	if firstID != DeriveRunID("service-1", request.RequestID) || firstID == DeriveRunID("service-2", request.RequestID) || runtimecatalog.ValidateIdentifier(firstID) != nil {
		t.Fatalf("derived run identity is not stable and catalog-safe: %q", firstID)
	}
	firstDigest := RequestDigest(request)
	if firstDigest != RequestDigest(request) || !lowerSHA256(firstDigest) {
		t.Fatalf("request digest is not deterministic: %q", firstDigest)
	}
	request.TaskMarkdown += " changed"
	if firstDigest == RequestDigest(request) {
		t.Fatal("semantic request change did not change digest")
	}
}

func TestAdmissionPlanHasExactlyOneExecutableTaskAndPreservesAuthorizedMarkdown(t *testing.T) {
	request := testAdmissionRequest(strings.Repeat("a", 40))
	request.TaskMarkdown = "### Task 7: Repo C task\n\n- [ ] preserve this requirement\n\n````markdown\n### Task 8: nested example\n- [ ] preserve this too\n````"
	principal := serviceapi.Principal{PrincipalID: "service-1", PrincipalType: serviceapi.PrincipalService, AuthnMethod: "bearer"}
	plan := admissionPlan(principal, request)
	if err := ralphex.ValidateSingleIncompleteTaskV1(plan); err != nil {
		t.Fatalf("admission plan is not exactly one executable task: %v\n%s", err, plan)
	}
	if !bytes.Contains(plan, []byte(request.TaskMarkdown)) {
		t.Fatalf("admission plan did not preserve authorized task markdown byte-for-byte:\n%s", plan)
	}
	if strings.Count(string(plan), "### Task 1: Implement the authorized product task") != 1 ||
		strings.Count(string(plan), "- [ ] Implement every requirement in the complete authorized product task below.") != 1 {
		t.Fatalf("admission plan does not expose one actionable Task 1 section:\n%s", plan)
	}
}

func TestAdmissionMaterializesV2InputsAndReplaysOrConflicts(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	defer fixture.closeLocks()
	response, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	expectedRunID := DeriveRunID(fixture.principal.PrincipalID, fixture.request.RequestID)
	if response.RunID != expectedRunID || response.RunURL != "/v1/runs/"+expectedRunID {
		t.Fatalf("admission response = %+v", response)
	}
	fixture.mu.Lock()
	starts := append([][]string(nil), fixture.starts...)
	fixture.mu.Unlock()
	if len(starts) != 1 || starts[0][0] != fixture.controller.executable || !reflect.DeepEqual(starts[0][1:], []string{
		"run", "--manifest", filepath.Join(fixture.input, expectedRunID, "manifest.json"),
		"--ledger", filepath.Join(filepath.Dir(filepath.Dir(fixture.input)), "ledger", expectedRunID, "events.jsonl"),
		"--evidence-root", filepath.Join(filepath.Dir(filepath.Dir(fixture.input)), "evidence"),
		"--cgroup-root", filepath.Join(filepath.Dir(filepath.Dir(fixture.input)), "cgroup"),
		"--service-root", fixture.service,
		"--workflow-authority-config-file", filepath.Join(filepath.Dir(filepath.Dir(fixture.input)), "workflow-authority.json"),
	}) {
		t.Fatalf("launch argv = %#v", starts)
	}
	plan, err := os.ReadFile(filepath.Join(fixture.input, expectedRunID, "plan.md"))
	if err != nil || !strings.Contains(string(plan), fixture.request.ProductManifestSHA256) || !strings.Contains(string(plan), fixture.request.TaskMarkdown) || strings.Contains(string(plan), "--manifest") {
		t.Fatalf("materialized product plan = %q, err=%v", plan, err)
	}
	var manifest authority.Manifest
	manifestData, err := os.ReadFile(filepath.Join(fixture.input, expectedRunID, "manifest.json"))
	if err != nil || json.Unmarshal(manifestData, &manifest) != nil {
		t.Fatalf("read derived manifest: %v", err)
	}
	if manifest.RunID != expectedRunID || manifest.Repository.StartSHA != fixture.request.RepositoryBaseSHA ||
		manifest.Worktree.Branch != "abcp/"+expectedRunID || manifest.ContextCapsule == nil {
		t.Fatalf("derived manifest bindings = %+v", manifest)
	}
	pinnedRalphex := fixture.controller.profiles[fixture.request.ProfileID].template.Ralphex
	if !reflect.DeepEqual(manifest.Ralphex, pinnedRalphex) || manifest.Ralphex.BinaryPath == testExecutable(t) || manifest.Ralphex.SourceSHA == "" {
		t.Fatalf("derived manifest did not preserve controller-owned Ralphex identity: got %+v, want %+v", manifest.Ralphex, pinnedRalphex)
	}
	receiptPath := filepath.Join(fixture.service, "admissions", receiptKey(fixture.principal.PrincipalID, fixture.request.RequestID)+".json")
	receiptData, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	var receipt AdmissionReceiptV1
	if strictJSON(receiptData, &receipt) != nil || receipt.CanonicalRequestSHA256 != RequestDigest(fixture.request) || receipt.RunID != expectedRunID {
		t.Fatalf("admission receipt = %s", receiptData)
	}
	canonical, _ := json.Marshal(receipt)
	if !reflect.DeepEqual(receiptData, canonical) {
		t.Fatalf("receipt is not strict canonical JSON: %q", receiptData)
	}

	registerFixtureRun(t, fixture, expectedRunID)
	replayed, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request)
	if err != nil || replayed != response {
		t.Fatalf("same-request replay = %+v, %v", replayed, err)
	}
	fixture.mu.Lock()
	startCount := len(fixture.starts)
	fixture.mu.Unlock()
	if startCount != 1 {
		t.Fatalf("replay launched %d processes", startCount)
	}
	conflict := fixture.request
	conflict.TaskMarkdown = "different product task"
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, conflict); !errors.Is(err, serviceapi.ErrRequestIDConflict) {
		t.Fatalf("conflicting replay = %v", err)
	}
}

func TestAdmissionMaterializationIsDeterministicAfterAmbiguousLaunch(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	response, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	runDirectory := filepath.Join(fixture.input, response.RunID)
	before := make(map[string][]byte)
	for _, name := range []string{"plan.md", "context-capsule.json", "manifest.json"} {
		before[name], err = os.ReadFile(filepath.Join(runDirectory, name))
		if err != nil {
			t.Fatal(err)
		}
	}
	fixture.mu.Lock()
	firstLock := fixture.locks[0]
	fixture.mu.Unlock()
	if err := firstLock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); !errors.Is(err, serviceapi.ErrReconciliationRequired) {
		t.Fatalf("ambiguous launch replay = %v", err)
	}
	for name, expected := range before {
		observed, err := os.ReadFile(filepath.Join(runDirectory, name))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(observed, expected) {
			t.Fatalf("%s changed across deterministic materialization", name)
		}
	}
}

func TestAdmissionConcurrentDuplicateSuppressionAndExitedLauncherReconciliation(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	defer fixture.closeLocks()
	const callers = 16
	var wait sync.WaitGroup
	errorsSeen := make(chan error, callers)
	responses := make(chan serviceapi.RunAdmissionResponseV1, callers)
	for index := 0; index < callers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request)
			errorsSeen <- err
			responses <- response
		}()
	}
	wait.Wait()
	close(errorsSeen)
	close(responses)
	successes, reconciliations := 0, 0
	for err := range errorsSeen {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, serviceapi.ErrReconciliationRequired):
			reconciliations++
		default:
			t.Fatalf("concurrent admission: %v", err)
		}
	}
	if successes != 1 || reconciliations != callers-1 {
		t.Fatalf("concurrent outcomes: success=%d reconciliation=%d", successes, reconciliations)
	}
	want := DeriveRunID(fixture.principal.PrincipalID, fixture.request.RequestID)
	for response := range responses {
		if response.RunID != "" && response.RunID != want {
			t.Fatalf("concurrent response = %+v", response)
		}
	}
	fixture.mu.Lock()
	if len(fixture.starts) != 1 || len(fixture.locks) != 1 {
		fixture.mu.Unlock()
		t.Fatalf("concurrent launches = %d, locks=%d", len(fixture.starts), len(fixture.locks))
	}
	deadLock := fixture.locks[0]
	fixture.mu.Unlock()
	if err := deadLock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); !errors.Is(err, serviceapi.ErrReconciliationRequired) {
		t.Fatalf("replay after exited pre-registration launcher = %v", err)
	}
	fixture.mu.Lock()
	if len(fixture.starts) != 1 {
		fixture.mu.Unlock()
		t.Fatalf("exited launcher replay starts = %d", len(fixture.starts))
	}
	fixture.mu.Unlock()
	registerFixtureRun(t, fixture, want)
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); err != nil {
		t.Fatalf("registered replay: %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.starts) != 1 {
		t.Fatalf("registered run was relaunched: %d", len(fixture.starts))
	}
}

func TestAdmissionProfileRequiresIgnoredSymlinkFreeInput(t *testing.T) {
	for _, test := range []struct {
		name       string
		gitignore  bool
		symlinkDir bool
	}{
		{name: "not ignored"},
		{name: "symlink", gitignore: true, symlinkDir: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, repository, head := makeRepository(t, test.gitignore)
			input := filepath.Join(repository, ".abcp-input")
			if test.symlinkDir {
				outside := filepath.Join(root, "outside")
				if err := os.Mkdir(outside, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, input); err != nil {
					t.Fatal(err)
				}
			}
			config, catalog := writeAdmissionConfiguration(t, root, repository, head)
			service := filepath.Join(root, "service")
			if err := os.Mkdir(service, 0o700); err != nil {
				t.Fatal(err)
			}
			controller, err := NewController(Config{ProfileFile: config, ServiceRoot: service, Executable: testExecutable(t), Catalog: catalog})
			if err == nil {
				controller.Close()
				t.Fatal("unsafe admission input was accepted")
			}
		})
	}
}

func TestAdmissionProfileParsingRejectsUnsafeProtectedFiles(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string) string
	}{
		{
			name: "unknown JSON field",
			mutate: func(t *testing.T, path string) string {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				data = bytes.Replace(data, []byte(`{"schema_version":1`), []byte(`{"schema_version":1,"unknown":true`), 1)
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "duplicate JSON field",
			mutate: func(t *testing.T, path string) string {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				data = bytes.Replace(data, []byte(`{"schema_version":1`), []byte(`{"schema_version":1,"schema_version":1`), 1)
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "case-alias JSON field",
			mutate: func(t *testing.T, path string) string {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				data = bytes.Replace(data, []byte(`"schema_version"`), []byte(`"Schema_Version"`), 1)
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "invalid UTF-8",
			mutate: func(t *testing.T, path string) string {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				data = bytes.Replace(data, []byte("example/product"), []byte{'e', 'x', 'a', 'm', 'p', 'l', 'e', '/', 0xff}, 1)
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "bad mode",
			mutate: func(t *testing.T, path string) string {
				if err := os.Chmod(path, 0o640); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "symlink",
			mutate: func(t *testing.T, path string) string {
				link := path + ".link"
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
		},
		{
			name: "multiple hard links",
			mutate: func(t *testing.T, path string) string {
				if err := os.Link(path, path+".hardlink"); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, repository, head := makeRepository(t, true)
			profilePath, catalog := writeAdmissionConfiguration(t, root, repository, head)
			profilePath = test.mutate(t, profilePath)
			service := filepath.Join(root, "service")
			if err := os.Mkdir(service, 0o700); err != nil {
				t.Fatal(err)
			}
			controller, err := NewController(Config{ProfileFile: profilePath, ServiceRoot: service, Executable: testExecutable(t), Catalog: catalog})
			if err == nil {
				controller.Close()
				t.Fatal("unsafe admission profile was accepted")
			}
		})
	}
}

func TestAdmissionProfileRequiresCanonicalProtectedWorkflowAuthorityConfig(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string, string)
	}{
		{
			name: "relative path",
			mutate: func(t *testing.T, profilePath, _ string) {
				var profiles ProfileFileV1
				data, err := os.ReadFile(profilePath)
				if err != nil || json.Unmarshal(data, &profiles) != nil {
					t.Fatalf("read profiles: %v", err)
				}
				profiles.Profiles[0].WorkflowAuthorityConfigPath = "workflow-authority.json"
				writeProtectedJSON(t, profilePath, profiles)
			},
		},
		{
			name: "bad mode",
			mutate: func(t *testing.T, _, authorityPath string) {
				if err := os.Chmod(authorityPath, 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink",
			mutate: func(t *testing.T, profilePath, authorityPath string) {
				link := authorityPath + ".link"
				if err := os.Symlink(authorityPath, link); err != nil {
					t.Fatal(err)
				}
				var profiles ProfileFileV1
				data, err := os.ReadFile(profilePath)
				if err != nil || json.Unmarshal(data, &profiles) != nil {
					t.Fatalf("read profiles: %v", err)
				}
				profiles.Profiles[0].WorkflowAuthorityConfigPath = link
				writeProtectedJSON(t, profilePath, profiles)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, repository, head := makeRepository(t, true)
			profilePath, catalog := writeAdmissionConfiguration(t, root, repository, head)
			authorityPath := filepath.Join(root, "workflow-authority.json")
			test.mutate(t, profilePath, authorityPath)
			service := filepath.Join(root, "service")
			if err := os.Mkdir(service, 0o700); err != nil {
				t.Fatal(err)
			}
			controller, err := NewController(Config{ProfileFile: profilePath, ServiceRoot: service, Executable: testExecutable(t), Catalog: catalog})
			if err == nil {
				controller.Close()
				t.Fatal("unsafe workflow-authority config was accepted")
			}
		})
	}
}

func TestAdmissionProfileRejectsRepositoryIdentityThatIsNotCanonicalOrigin(t *testing.T) {
	root, repository, head := makeRepository(t, true)
	profilePath, catalog := writeAdmissionConfiguration(t, root, repository, head)
	git(t, repository, "remote", "add", "upstream", "https://example.test/example/other.git")
	var profiles ProfileFileV1
	data, err := os.ReadFile(profilePath)
	if err != nil || json.Unmarshal(data, &profiles) != nil {
		t.Fatalf("read profile: %v", err)
	}
	profiles.Profiles[0].RepositoryIdentity = "example/other"
	writeProtectedJSON(t, profilePath, profiles)
	var template authority.Manifest
	data, err = os.ReadFile(profiles.Profiles[0].ManifestTemplatePath)
	if err != nil || json.Unmarshal(data, &template) != nil {
		t.Fatalf("read template: %v", err)
	}
	template.Repository.Remotes["upstream"] = "https://example.test/example/other.git"
	writeProtectedJSON(t, profiles.Profiles[0].ManifestTemplatePath, template)
	service := filepath.Join(root, "service")
	if err := os.Mkdir(service, 0o700); err != nil {
		t.Fatal(err)
	}
	controller, err := NewController(Config{ProfileFile: profilePath, ServiceRoot: service, Executable: testExecutable(t), Catalog: catalog})
	if err == nil {
		controller.Close()
		t.Fatal("non-origin repository identity was accepted")
	}
}

func TestAdmissionCreateOrVerifyRejectsMaterialDrift(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	response, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	lock := fixture.locks[0]
	fixture.mu.Unlock()
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(fixture.input, response.RunID, "plan.md")
	if err := os.WriteFile(planPath, []byte("drift"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); !errors.Is(err, serviceapi.ErrUnsafeAdmissionMaterialization) {
		t.Fatalf("drifted material retry = %v", err)
	}
}

func TestAdmissionRegisteredReplayRejectsFrozenMaterialDrift(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	response, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	registerFixtureRun(t, fixture, response.RunID)
	if err := os.WriteFile(filepath.Join(fixture.input, response.RunID, "plan.md"), []byte("drift"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); !errors.Is(err, serviceapi.ErrUnsafeAdmissionMaterialization) {
		t.Fatalf("registered replay with drifted material = %v", err)
	}
}

func TestAdmissionPersistsReceiptBeforeBaseMismatch(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	request := fixture.request
	request.RepositoryBaseSHA = strings.Repeat("b", 40)
	if request.RepositoryBaseSHA == fixture.request.RepositoryBaseSHA {
		request.RepositoryBaseSHA = strings.Repeat("c", 40)
	}
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request); !errors.Is(err, serviceapi.ErrRepositoryBaseMismatch) {
		t.Fatalf("base mismatch = %v", err)
	}
	receiptPath := filepath.Join(fixture.service, "admissions", receiptKey(fixture.principal.PrincipalID, request.RequestID)+".json")
	if _, err := os.Stat(receiptPath); err != nil {
		t.Fatalf("base mismatch was not durably receipted first: %v", err)
	}
	runID := DeriveRunID(fixture.principal.PrincipalID, request.RequestID)
	if _, err := os.Stat(filepath.Join(fixture.input, runID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("base mismatch materialized run inputs: %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.starts) != 0 {
		t.Fatalf("base mismatch launched %d processes", len(fixture.starts))
	}
}

func TestAdmissionUnboundReceiptDoesNotAdoptChangedPrivateProfile(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	request := fixture.request
	request.RepositoryBaseSHA = strings.Repeat("b", 40)
	if request.RepositoryBaseSHA == fixture.request.RepositoryBaseSHA {
		request.RepositoryBaseSHA = strings.Repeat("c", 40)
	}
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, request); !errors.Is(err, serviceapi.ErrRepositoryBaseMismatch) {
		t.Fatalf("base mismatch = %v", err)
	}
	root := filepath.Dir(filepath.Dir(fixture.input))
	templatePath := filepath.Join(root, "manifest-template.json")
	data, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatal(err)
	}
	var template authority.Manifest
	if err := json.Unmarshal(data, &template); err != nil {
		t.Fatal(err)
	}
	template.Acceptance[0].Name = "changed-after-receipt"
	writeProtectedJSON(t, templatePath, template)
	if err := fixture.controller.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewController(Config{
		ProfileFile: filepath.Join(root, "admission-profiles.json"), ServiceRoot: fixture.service,
		Executable: testExecutable(t), Catalog: fixture.catalog,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if _, err := restarted.AdmitRun(context.Background(), fixture.principal, request); !errors.Is(err, serviceapi.ErrReconciliationRequired) {
		t.Fatalf("changed-profile unbound replay = %v", err)
	}
}

func TestAdmissionFailsClosedOnDurableCatalogAndReceiptFailures(t *testing.T) {
	t.Run("catalog read", func(t *testing.T) {
		fixture := newAdmissionFixture(t, true)
		fixture.catalog.mu.Lock()
		fixture.catalog.err = errors.New("catalog unavailable")
		fixture.catalog.mu.Unlock()
		if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); !errors.Is(err, serviceapi.ErrAdmissionUnavailable) {
			t.Fatalf("catalog read failure = %v", err)
		}
	})
	t.Run("corrupt receipt", func(t *testing.T) {
		fixture := newAdmissionFixture(t, true)
		receiptPath := filepath.Join(fixture.service, "admissions", receiptKey(fixture.principal.PrincipalID, fixture.request.RequestID)+".json")
		if err := os.WriteFile(receiptPath, []byte(`{"kind":"tampered"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); !errors.Is(err, serviceapi.ErrUnsafeAdmissionMaterialization) {
			t.Fatalf("corrupt receipt = %v", err)
		}
	})
}

func TestAdmissionLaunchFailurePreservesOneDurableIdentity(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	starts := 0
	fixture.controller.start = func(string, []string, *os.File) error {
		starts++
		return errors.New("launch failed")
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); !errors.Is(err, serviceapi.ErrAdmissionUnavailable) {
			t.Fatalf("launch attempt %d = %v", attempt+1, err)
		}
	}
	if starts != 2 {
		t.Fatalf("launch attempts = %d", starts)
	}
	receiptPath := filepath.Join(fixture.service, "admissions", receiptKey(fixture.principal.PrincipalID, fixture.request.RequestID)+".json")
	data, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	var receipt AdmissionReceiptV1
	if err := strictJSON(data, &receipt); err != nil || receipt.RunID != DeriveRunID(fixture.principal.PrincipalID, fixture.request.RequestID) {
		t.Fatalf("durable launch-failure receipt = %+v, %v", receipt, err)
	}
	conflict := fixture.request
	conflict.TaskMarkdown = "conflicting reuse"
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, conflict); !errors.Is(err, serviceapi.ErrRequestIDConflict) {
		t.Fatalf("conflicting reuse after launch failure = %v", err)
	}
}

func TestAdmissionHeldLaunchRequiresRegisteredExactBinding(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	defer fixture.closeLocks()
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); !errors.Is(err, serviceapi.ErrReconciliationRequired) {
		t.Fatalf("ambiguous replay = %v", err)
	}
}

func TestAdmissionRejectsConflictingRegisteredRunBinding(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	runID := DeriveRunID(fixture.principal.PrincipalID, fixture.request.RequestID)
	fixture.catalog.mu.Lock()
	fixture.catalog.records[runID] = runtimecatalog.RunRegistrationV1{
		RunID: runID, RepositoryIdentityDigest: strings.Repeat("f", 64), AuthorityDigest: strings.Repeat("e", 64),
		CanonicalLedgerPath:   filepath.Join(filepath.Dir(filepath.Dir(fixture.input)), "foreign-ledger"),
		CanonicalEvidenceRoot: filepath.Join(filepath.Dir(filepath.Dir(fixture.input)), "foreign-evidence"),
	}
	fixture.catalog.mu.Unlock()
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); !errors.Is(err, serviceapi.ErrUnsafeAdmissionMaterialization) {
		t.Fatalf("conflicting registered binding = %v", err)
	}
}

func TestAdmissionRegisteredExactReplaySurvivesRepositoryHeadAdvance(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	defer fixture.closeLocks()
	response, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	registerFixtureRun(t, fixture, response.RunID)
	if err := os.WriteFile(filepath.Join(fixture.repository, "advance.txt"), []byte("advance\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, fixture.repository, "add", "advance.txt")
	git(t, fixture.repository, "commit", "-m", "advance")
	replayed, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request)
	if err != nil || replayed != response {
		t.Fatalf("registered exact replay after HEAD advance = %+v, %v", replayed, err)
	}
}

func TestAdmissionRegisteredExactReplaySurvivesPrivateProfileChange(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	defer fixture.closeLocks()
	response, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	registerFixtureRun(t, fixture, response.RunID)

	root := filepath.Dir(filepath.Dir(fixture.input))
	templatePath := filepath.Join(root, "manifest-template.json")
	data, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatal(err)
	}
	var template authority.Manifest
	if err := json.Unmarshal(data, &template); err != nil {
		t.Fatal(err)
	}
	template.Acceptance[0].Name = "changed-after-admission"
	writeProtectedJSON(t, templatePath, template)
	if err := fixture.controller.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewController(Config{
		ProfileFile: filepath.Join(root, "admission-profiles.json"), ServiceRoot: fixture.service,
		Executable: testExecutable(t), Catalog: fixture.catalog,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()

	replayed, err := restarted.AdmitRun(context.Background(), fixture.principal, fixture.request)
	if err != nil || replayed != response {
		t.Fatalf("registered exact replay after private profile change = %+v, %v", replayed, err)
	}
}

func TestAdmissionRegisteredExactReplaySurvivesPrivateProfileRemoval(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	defer fixture.closeLocks()
	response, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	registerFixtureRun(t, fixture, response.RunID)
	root := filepath.Dir(filepath.Dir(fixture.input))
	profilePath := filepath.Join(root, "admission-profiles.json")
	data, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	var profiles ProfileFileV1
	if err := json.Unmarshal(data, &profiles); err != nil {
		t.Fatal(err)
	}
	profiles.Profiles[0].ProfileID = "replacement"
	writeProtectedJSON(t, profilePath, profiles)
	if err := fixture.controller.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewController(Config{ProfileFile: profilePath, ServiceRoot: fixture.service, Executable: testExecutable(t), Catalog: fixture.catalog})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	replayed, err := restarted.AdmitRun(context.Background(), fixture.principal, fixture.request)
	if err != nil || replayed != response {
		t.Fatalf("registered exact replay after private profile removal = %+v, %v", replayed, err)
	}
}

func TestAdmissionBoundLaunchRequiresReconciliationAfterPrivateProfileChange(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	_, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	fixture.mu.Lock()
	lock := fixture.locks[0]
	fixture.mu.Unlock()
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(filepath.Dir(fixture.input))
	templatePath := filepath.Join(root, "manifest-template.json")
	data, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatal(err)
	}
	var template authority.Manifest
	if err := json.Unmarshal(data, &template); err != nil {
		t.Fatal(err)
	}
	template.Acceptance[0].Name = "changed-after-bound-launch"
	writeProtectedJSON(t, templatePath, template)
	if err := fixture.controller.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewController(Config{
		ProfileFile: filepath.Join(root, "admission-profiles.json"), ServiceRoot: fixture.service,
		Executable: testExecutable(t), Catalog: fixture.catalog,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	starts := 0
	restarted.start = func(string, []string, *os.File) error {
		starts++
		return nil
	}
	_, err = restarted.AdmitRun(context.Background(), fixture.principal, fixture.request)
	if !errors.Is(err, serviceapi.ErrReconciliationRequired) || starts != 0 {
		t.Fatalf("bound ambiguous replay after private profile change = %v, starts=%d", err, starts)
	}
}

func TestAdmissionRejectsModeWidenedPrivateRoots(t *testing.T) {
	for _, rootName := range []string{"ledger", "evidence"} {
		t.Run(rootName, func(t *testing.T) {
			root, repository, head := makeRepository(t, true)
			profilePath, catalog := writeAdmissionConfiguration(t, root, repository, head)
			if err := os.Chmod(filepath.Join(root, rootName), 0o750); err != nil {
				t.Fatal(err)
			}
			service := filepath.Join(root, "service")
			if err := os.Mkdir(service, 0o700); err != nil {
				t.Fatal(err)
			}
			controller, err := NewController(Config{ProfileFile: profilePath, ServiceRoot: service, Executable: testExecutable(t), Catalog: catalog})
			if err == nil {
				controller.Close()
				t.Fatal("mode-widened private root was accepted")
			}
		})
	}
}

func TestAdmissionRevalidatesPrivateRootsBeforeMaterialization(t *testing.T) {
	fixture := newAdmissionFixture(t, true)
	root := filepath.Dir(filepath.Dir(fixture.input))
	if err := os.Chmod(filepath.Join(root, "ledger"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); !errors.Is(err, serviceapi.ErrUnsafeAdmissionMaterialization) {
		t.Fatalf("mode-widened private root at admission = %v", err)
	}
}

func TestAdmissionManifestTemplateRequiresStrictJSON(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{name: "case alias", mutate: func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"policy_version"`), []byte(`"Policy_Version"`), 1)
		}},
		{name: "invalid UTF-8", mutate: func(data []byte) []byte {
			return bytes.Replace(data, []byte("product-v1"), []byte{'p', 'r', 'o', 'd', 'u', 'c', 't', '-', 0xff}, 1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, repository, head := makeRepository(t, true)
			profilePath, catalog := writeAdmissionConfiguration(t, root, repository, head)
			var profiles ProfileFileV1
			profileData, err := os.ReadFile(profilePath)
			if err != nil || json.Unmarshal(profileData, &profiles) != nil {
				t.Fatalf("read profile: %v", err)
			}
			templatePath := profiles.Profiles[0].ManifestTemplatePath
			templateData, err := os.ReadFile(templatePath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(templatePath, test.mutate(templateData), 0o600); err != nil {
				t.Fatal(err)
			}
			service := filepath.Join(root, "service")
			if err := os.Mkdir(service, 0o700); err != nil {
				t.Fatal(err)
			}
			controller, err := NewController(Config{ProfileFile: profilePath, ServiceRoot: service, Executable: testExecutable(t), Catalog: catalog})
			if err == nil {
				controller.Close()
				t.Fatal("unsafe admission manifest template was accepted")
			}
		})
	}
}

func newAdmissionFixture(t *testing.T, ignored bool) *admissionFixture {
	t.Helper()
	root, repository, head := makeRepository(t, ignored)
	config, catalog := writeAdmissionConfiguration(t, root, repository, head)
	service := filepath.Join(root, "service")
	if err := os.Mkdir(service, 0o700); err != nil {
		t.Fatal(err)
	}
	controller, err := NewController(Config{
		ProfileFile: config, ServiceRoot: service, Executable: testExecutable(t), Catalog: catalog,
		Clock: func() time.Time { return time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &admissionFixture{
		controller: controller, catalog: catalog, request: testAdmissionRequest(head),
		principal:  serviceapi.Principal{PrincipalID: "repo-c-service", PrincipalType: serviceapi.PrincipalService, AuthnMethod: "test-v1"},
		repository: repository, service: service, input: filepath.Join(repository, ".abcp-input"),
	}
	controller.start = func(executable string, args []string, lock *os.File) error {
		duplicate, err := syscall.Dup(int(lock.Fd()))
		if err != nil {
			return err
		}
		fixture.mu.Lock()
		fixture.starts = append(fixture.starts, append([]string{executable}, args...))
		fixture.locks = append(fixture.locks, os.NewFile(uintptr(duplicate), "test-child-launch-lock"))
		fixture.mu.Unlock()
		return nil
	}
	t.Cleanup(func() {
		fixture.closeLocks()
		controller.Close()
	})
	return fixture
}

func (f *admissionFixture) closeLocks() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, lock := range f.locks {
		_ = lock.Close()
	}
	f.locks = nil
}

func makeRepository(t *testing.T, ignored bool) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	repository := filepath.Join(root, "repository")
	git(t, "", "init", "-b", "main", repository)
	git(t, repository, "config", "user.email", "admission@example.test")
	git(t, repository, "config", "user.name", "Admission Test")
	git(t, repository, "remote", "add", "origin", "https://example.test/example/product.git")
	contents := "tracked\n"
	if ignored {
		contents = ".abcp-input/\n"
	}
	if err := os.WriteFile(filepath.Join(repository, ".gitignore"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, repository, "add", ".gitignore")
	git(t, repository, "commit", "-m", "initial")
	return root, repository, git(t, repository, "rev-parse", "HEAD")
}

func writeAdmissionConfiguration(t *testing.T, root, repository, _ string) (string, *admissionTestCatalog) {
	t.Helper()
	truePath := testExecutable(t)
	runtimePath := filepath.Join(root, "approved-ralphex")
	if err := os.WriteFile(runtimePath, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	binaryDigest := sha256.Sum256(binary)
	template := authority.Manifest{
		Repository: authority.RepositoryManifest{Remotes: map[string]string{"origin": "https://example.test/example/product.git"}, DefaultBranch: "main"},
		Ralphex: authority.RalphexManifest{
			BinaryPath: runtimePath, BinarySHA256: hex.EncodeToString(binaryDigest[:]), SourceSHA: strings.Repeat("c", 40),
			Mode: ralphex.ModeFull, Timeout: "5m", WaitOnLimit: "0s",
		},
		Executor: authority.ExecutorPolicy{Executor: "codex"}, Worktree: authority.WorktreePolicy{Enabled: true},
		Acceptance:    []authority.AcceptanceCommand{{Name: "test", Required: true, Timeout: "5m", Argv: []string{truePath}}},
		PolicyVersion: "product-v1",
	}
	templatePath := filepath.Join(root, "manifest-template.json")
	writeProtectedJSON(t, templatePath, template)
	workflowAuthorityPath := filepath.Join(root, "workflow-authority.json")
	writeProtectedJSON(t, workflowAuthorityPath, map[string]any{
		"schema_version":          1,
		"connection_string":       "postgres://admission.test/abcp",
		"authority_domain_sha256": strings.Repeat("b", 64),
	})
	for _, directory := range []string{"ledger", "evidence", "cgroup"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	profile := ProfileFileV1{SchemaVersion: 1, Profiles: []ProfileV1{{
		ProfileID: "default", RepositoryPath: repository, RepositoryIdentity: "example/product",
		ManifestTemplatePath: templatePath, InputDirectory: ".abcp-input",
		LedgerRoot: filepath.Join(root, "ledger"), EvidenceRoot: filepath.Join(root, "evidence"), CgroupRoot: filepath.Join(root, "cgroup"),
		WorkflowAuthorityConfigPath: workflowAuthorityPath,
	}}}
	profilePath := filepath.Join(root, "admission-profiles.json")
	writeProtectedJSON(t, profilePath, profile)
	return profilePath, &admissionTestCatalog{records: make(map[string]runtimecatalog.RunRegistrationV1)}
}

func registerFixtureRun(t *testing.T, fixture *admissionFixture, runID string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixture.input, runID, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest authority.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(filepath.Dir(fixture.input))
	fixture.catalog.mu.Lock()
	fixture.catalog.records[runID] = runtimecatalog.RunRegistrationV1{
		RunID: runID, RepositoryIdentityDigest: runtimecatalog.RepositoryIdentityDigest("example/product"), AuthorityDigest: digest(data),
		CanonicalLedgerPath: filepath.Join(root, "ledger", runID, "events.jsonl"), CanonicalEvidenceRoot: filepath.Join(root, "evidence", runID),
	}
	fixture.catalog.mu.Unlock()
}

func testAdmissionRequest(baseSHA string) serviceapi.RunAdmissionRequestV1 {
	return serviceapi.RunAdmissionRequestV1{
		SchemaVersion: 1, RequestID: "request-1", ProfileID: "default",
		ProductAuthorizationID: "authorization-1", ProductTaskID: "task-1", ProductVersionID: "version-1",
		ProductManifestSHA256: strings.Repeat("a", 64), RepositoryBaseSHA: baseSHA,
		TaskMarkdown:   "Implement the bounded product task.",
		DelegatedActor: serviceapi.DelegatedActorV1{SubjectID: "operator-1", SubjectType: serviceapi.PrincipalOperator},
	}
}

func writeProtectedJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func testExecutable(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func git(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	if directory != "" {
		command.Dir = directory
	}
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

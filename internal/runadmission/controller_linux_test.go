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
	mu         sync.Mutex
	registered map[string]bool
	err        error
}

func (c *admissionTestCatalog) ReadRun(runID string) (runtimecatalog.RunRegistrationV1, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.err != nil {
		return runtimecatalog.RunRegistrationV1{}, c.err
	}
	if c.registered[runID] {
		return runtimecatalog.RunRegistrationV1{RunID: runID}, nil
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

func TestAdmissionMaterializationIsDeterministicAcrossRelaunch(t *testing.T) {
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
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); err != nil {
		t.Fatalf("relaunch admission: %v", err)
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

func TestAdmissionConcurrentDuplicateSuppressionAndDeadLauncherRetry(t *testing.T) {
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
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent admission: %v", err)
		}
	}
	want := DeriveRunID(fixture.principal.PrincipalID, fixture.request.RequestID)
	for response := range responses {
		if response.RunID != want {
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
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); err != nil {
		t.Fatalf("retry after dead pre-registration launcher: %v", err)
	}
	fixture.mu.Lock()
	if len(fixture.starts) != 2 {
		fixture.mu.Unlock()
		t.Fatalf("dead launcher retry starts = %d", len(fixture.starts))
	}
	secondLock := fixture.locks[1]
	fixture.mu.Unlock()
	if err := secondLock.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.catalog.mu.Lock()
	fixture.catalog.registered[want] = true
	fixture.catalog.mu.Unlock()
	if _, err := fixture.controller.AdmitRun(context.Background(), fixture.principal, fixture.request); err != nil {
		t.Fatalf("registered replay: %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.starts) != 2 {
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
	binary, err := os.ReadFile(truePath)
	if err != nil {
		t.Fatal(err)
	}
	binaryDigest := sha256.Sum256(binary)
	template := authority.Manifest{
		Repository: authority.RepositoryManifest{Remotes: map[string]string{"origin": "https://example.test/example/product.git"}, DefaultBranch: "main"},
		Ralphex:    authority.RalphexManifest{BinaryPath: truePath, BinarySHA256: hex.EncodeToString(binaryDigest[:]), Mode: ralphex.ModeFull, Timeout: "5m", WaitOnLimit: "0s"},
		Executor:   authority.ExecutorPolicy{Executor: "codex"}, Worktree: authority.WorktreePolicy{Enabled: true},
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
	return profilePath, &admissionTestCatalog{registered: make(map[string]bool)}
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

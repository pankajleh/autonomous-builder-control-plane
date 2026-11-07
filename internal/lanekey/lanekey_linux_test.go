//go:build linux

package lanekey

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/evidence"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ledger"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/openmodel"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// Two keys of Together's shape that exist only in these tests.
const (
	firstKey  = "tgp_v1_Rmlyc3RUZXN0S2V5T25seUZvckFCQ1BMYW5lS2V5czAx"
	secondKey = "tgp_v1_U2Vjb25kVGVzdEtleU9ubHlGb3JBQkNQTGFuZUtleXMy"
)

var testTime = time.Date(2026, 11, 7, 18, 0, 0, 0, time.UTC)

// fixture is a host in small: a private secrets folder, a private configuration folder with an admission profile file
// and the manifest templates it names (two on Together, one on an API-key lane file, one on an account lane, one on
// the controller's own sign-in), and the Together provider those two templates name.
type fixture struct {
	root, secrets, config, profiles string
	together                        openmodel.Provider
}

func privateFolder(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func privateFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func togetherProvider(root, keyFile string) openmodel.Provider {
	return openmodel.Provider{
		ID: "together", BaseURL: "https://api.together.xyz/v1", KeyFile: keyFile, WireAPI: openmodel.WireResponses,
		CodexHome: filepath.Join(root, "codex", "lanes", "together"), QueueTimeout: "2s",
		Models: []openmodel.Model{{Name: "gpt-oss-120b", ID: "openai/gpt-oss-120b", Origin: "US", ContextWindow: 131072, MaxConcurrentBuilds: 2}},
	}
}

func template(t *testing.T, executor map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"executor": executor})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{root: root, secrets: privateFolder(t, filepath.Join(root, "secrets")), config: privateFolder(t, filepath.Join(root, "abcp-config"))}
	f.together = togetherProvider(root, filepath.Join(f.secrets, "together-api-key"))
	templates := map[string][]byte{
		"local-p02-together":      template(t, map[string]any{"executor": "codex", "task_model": "gpt-oss-120b", "provider": f.together}),
		"local-p02-node-together": template(t, map[string]any{"executor": "codex", "task_model": "gpt-oss-120b", "provider": f.together}),
		"local-p02-codex-api":     template(t, map[string]any{"executor": "codex", "lane": "codex-api", "lane_file": filepath.Join(f.config, "lanes", "codex-api.env")}),
		"local-p02-codex-account": template(t, map[string]any{"executor": "codex", "lane": "codex-account", "lane_file": filepath.Join(f.config, "lanes", "codex-account.env")}),
		"local-p02":               template(t, map[string]any{"executor": "codex", "task_effort": "xhigh", "review_effort": "xhigh"}),
	}
	f.profiles = f.writeProfiles(t, templates)
	return f
}

func (f *fixture) writeProfiles(t *testing.T, templates map[string][]byte) string {
	t.Helper()
	var profiles []map[string]string
	for _, id := range []string{"local-p02", "local-p02-together", "local-p02-codex-api", "local-p02-node-together", "local-p02-codex-account", "local-p02-groq"} {
		data, ok := templates[id]
		if !ok {
			continue
		}
		path := filepath.Join(f.config, "manifest-template-"+id+".json")
		privateFile(t, path, data)
		profiles = append(profiles, map[string]string{"profile_id": id, "manifest_template_path": path})
	}
	data, err := json.Marshal(map[string]any{"schema_version": 1, "profiles": profiles})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.config, "profiles.json")
	privateFile(t, path, data)
	return path
}

func (f *fixture) manager(t *testing.T, log *bytes.Buffer) *Manager {
	t.Helper()
	lanes, err := Load(f.profiles)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(lanes, log, func() time.Time { return testTime })
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

// scan fails the test when any file under root, or any of the texts, holds a key.
func scan(t *testing.T, root string, texts ...string) {
	t.Helper()
	files := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files++
		texts = append(texts, path+"\x00"+string(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range texts {
		for _, key := range []string{firstKey, secondKey} {
			if strings.Contains(text, key) {
				name, _, _ := strings.Cut(text, "\x00")
				t.Errorf("a key is in %.200q", name)
			}
		}
	}
	if files == 0 {
		t.Fatal("no file was scanned")
	}
}

func TestTheProfilesNameTheProvidersKeyLanesOnly(t *testing.T) {
	f := newFixture(t)
	lanes, err := Load(f.profiles)
	if err != nil {
		t.Fatal(err)
	}
	if len(lanes.Key) != 1 || lanes.Key[0].Name != "together" || lanes.Key[0].KeyFile != f.together.KeyFile ||
		strings.Join(lanes.Key[0].Profiles, ",") != "local-p02-node-together,local-p02-together" || strings.Join(lanes.Other, ",") != "codex-account,codex-api" {
		t.Fatalf("lanes = %+v", lanes)
	}
	if _, err := os.Stat(f.together.KeyFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("loading made a key file")
	}
	// Each of these profiles beside a Together one and an API-key lane's refuses the whole file.
	for name, executor := range map[string]func(g *fixture) map[string]any{
		"one provider, two key files": func(g *fixture) map[string]any {
			return map[string]any{"executor": "codex", "task_model": "gpt-oss-120b", "provider": togetherProvider(g.root, filepath.Join(g.secrets, "another-together-key"))}
		},
		"one key file, two providers": func(g *fixture) map[string]any {
			provider := togetherProvider(g.root, g.together.KeyFile)
			provider.ID = "groq"
			return map[string]any{"executor": "codex", "task_model": "gpt-oss-120b", "provider": provider}
		},
		"a provider named as an engine lane": func(g *fixture) map[string]any {
			provider := togetherProvider(g.root, filepath.Join(g.secrets, "codex-api-key"))
			provider.ID = "codex-api"
			return map[string]any{"executor": "codex", "task_model": "gpt-oss-120b", "provider": provider}
		},
		"a provider that does not hold": func(g *fixture) map[string]any {
			provider := togetherProvider(g.root, filepath.Join(g.secrets, "groq-api-key"))
			provider.ID, provider.WireAPI = "groq", "chat"
			return map[string]any{"executor": "codex", "task_model": "gpt-oss-120b", "provider": provider}
		},
		"a field no manifest has": func(*fixture) map[string]any { return map[string]any{"executor": "codex", "provider_key": "x"} },
	} {
		t.Run(name, func(t *testing.T) {
			g := newFixture(t)
			g.profiles = g.writeProfiles(t, map[string][]byte{
				"local-p02-together":  template(t, map[string]any{"executor": "codex", "task_model": "gpt-oss-120b", "provider": g.together}),
				"local-p02-codex-api": template(t, map[string]any{"executor": "codex", "lane": "codex-api", "lane_file": filepath.Join(g.config, "lanes", "codex-api.env")}),
				"local-p02-groq":      template(t, executor(g)),
			})
			lanes, err := Load(g.profiles)
			if err == nil {
				t.Fatalf("loaded %+v", lanes)
			}
			t.Log(err)
		})
	}
	// A Groq profile beside them is a lane of its own.
	g := newFixture(t)
	groq := togetherProvider(g.root, filepath.Join(g.secrets, "groq-api-key"))
	groq.ID, groq.BaseURL = "groq", "https://api.groq.com/openai/v1"
	g.profiles = g.writeProfiles(t, map[string][]byte{
		"local-p02-together": template(t, map[string]any{"executor": "codex", "task_model": "gpt-oss-120b", "provider": g.together}),
		"local-p02-groq":     template(t, map[string]any{"executor": "codex", "task_model": "gpt-oss-120b", "provider": groq}),
	})
	if lanes, err := Load(g.profiles); err != nil || len(lanes.Key) != 2 || lanes.Key[0].Name != "groq" || lanes.Key[0].KeyFile != groq.KeyFile || lanes.Key[1].Name != "together" || len(lanes.Other) != 0 {
		t.Fatalf("with Groq = %+v, %v", lanes, err)
	}
	// Files that are not the controller's private files are not read.
	for _, path := range []string{f.profiles, filepath.Join(f.config, "manifest-template-local-p02-together.json")} {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(f.profiles); err == nil {
			t.Errorf("%s open to others was read", filepath.Base(path))
		}
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(f.root, "profiles-link.json")
	if err := os.Symlink(f.profiles, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(link); err == nil {
		t.Error("a linked profile file was read")
	}
}

func TestASetKeyIsTheLanesPrivateFileAndTheNextBuildStartReadsIt(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	f := newFixture(t)
	log := &bytes.Buffer{}
	m := f.manager(t, log)
	ctx := context.Background()
	var answers []string
	record := func(value any) {
		data, _ := json.Marshal(value)
		answers = append(answers, string(data))
	}
	lanes, _ := m.Lanes(ctx)
	record(lanes)
	if len(lanes.Lanes) != 1 || lanes.Lanes[0].State != serviceapi.LaneKeyMissing || lanes.Lanes[0].Fingerprint != "" {
		t.Fatalf("before = %+v", lanes)
	}
	if _, err := openmodel.Begin(ctx, f.together, "gpt-oss-120b", openmodel.StartOptions{SlotPoll: 10 * time.Millisecond}); err == nil || !strings.Contains(err.Error(), "OPEN_MODEL_ENGINE_KEY") {
		t.Fatalf("a build started without a key: %v", err)
	}

	set, err := m.SetKey(ctx, "together", serviceapi.LaneKeyRequestV1{Key: firstKey})
	record(set)
	if err != nil || set.State != serviceapi.LaneKeySet || set.Fingerprint != Fingerprint(firstKey) || len(set.Fingerprint) != 8 || set.Lane != "together" || set.Kind != "key" {
		t.Fatalf("set = %+v, %v", set, err)
	}
	info, err := os.Lstat(f.together.KeyFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || int(info.Sys().(*syscall.Stat_t).Uid) != os.Getuid() {
		t.Fatalf("the key file is %v, %v", info.Mode(), err)
	}
	if data, _ := os.ReadFile(f.together.KeyFile); string(data) != firstKey+"\n" {
		t.Fatal("the key file does not hold the key alone, on one line")
	}
	if entries, _ := os.ReadDir(f.secrets); len(entries) != 1 {
		t.Fatalf("the folder holds more than the key file: %v", entries)
	}

	// The next build's start reads the key; a running build keeps the key it started with.
	running, err := openmodel.Begin(ctx, f.together, "gpt-oss-120b", openmodel.StartOptions{SlotPoll: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer running.Release()
	if !environmentHolds(running, firstKey) || running.KeyFileSHA256 != fileSum(firstKey) {
		t.Fatal("the build did not start on the key set")
	}
	set, err = m.SetKey(ctx, "together", serviceapi.LaneKeyRequestV1{Key: secondKey})
	record(set)
	if err != nil || set.Fingerprint != Fingerprint(secondKey) {
		t.Fatalf("set again = %+v, %v", set, err)
	}
	next, err := openmodel.Begin(ctx, f.together, "gpt-oss-120b", openmodel.StartOptions{SlotPoll: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Release()
	if !environmentHolds(next, secondKey) || next.KeyFileSHA256 != fileSum(secondKey) {
		t.Fatal("the next build did not start on the new key")
	}
	if !environmentHolds(running, firstKey) || environmentHolds(running, secondKey) {
		t.Fatal("the running build's key changed")
	}
	lanes, _ = m.Lanes(ctx)
	record(lanes)
	if lanes.Lanes[0].State != serviceapi.LaneKeySet || lanes.Lanes[0].Fingerprint != Fingerprint(secondKey) {
		t.Fatalf("after = %+v", lanes)
	}

	// What the two starts record goes in the run's ledger and evidence, as abcp run records them (EXECUTION_STARTING's
	// open_model): digests, never the key.
	journal, err := ledger.NewJSONLLedger(filepath.Join(f.root, "ledger", "run-1", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	store, err := evidence.NewStore(filepath.Join(f.root, "evidence"), "run-1")
	if err != nil {
		t.Fatal(err)
	}
	for index, start := range []*openmodel.Start{running, next} {
		event, err := ledger.NewEvent("run-1", "OPEN_MODEL_STARTED", "abcp", "test")
		if err != nil {
			t.Fatal(err)
		}
		event.Payload = map[string]any{"open_model": start.Evidence()}
		if err := journal.Append(event); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(start.Evidence())
		if _, err := store.WriteBytes(fmt.Sprintf("open-model-%d.json", index), "open-model", data); err != nil {
			t.Fatal(err)
		}
		record(fmt.Sprint(start, start.Evidence()))
	}

	removed, err := m.RemoveKey(ctx, "together")
	record(removed)
	if err != nil || removed.State != serviceapi.LaneKeyMissing || removed.Fingerprint != "" {
		t.Fatalf("remove = %+v, %v", removed, err)
	}
	if _, err := os.Lstat(f.together.KeyFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the key file is still there")
	}
	if _, err := openmodel.Begin(ctx, f.together, "gpt-oss-120b", openmodel.StartOptions{SlotPoll: 10 * time.Millisecond}); err == nil ||
		!strings.Contains(err.Error(), "OPEN_MODEL_ENGINE_KEY") || strings.Contains(err.Error(), secondKey) {
		t.Fatalf("a build started on a removed key: %v", err)
	}
	if again, err := m.RemoveKey(ctx, "together"); err != nil || again.State != serviceapi.LaneKeyMissing {
		t.Fatalf("remove again = %+v, %v", again, err)
	}
	want := []string{
		"abcp lane key set at=2026-11-07T18:00:00Z lane=together fingerprint=" + Fingerprint(firstKey),
		"abcp lane key set at=2026-11-07T18:00:00Z lane=together fingerprint=" + Fingerprint(secondKey),
		"abcp lane key removed at=2026-11-07T18:00:00Z lane=together fingerprint=" + Fingerprint(secondKey),
		"abcp lane key removed at=2026-11-07T18:00:00Z lane=together was=missing",
	}
	if log.String() != strings.Join(want, "\n")+"\n" {
		t.Fatalf("audit lines:\n%s", log.String())
	}
	// Every file the test wrote (the Codex homes, slots, ledger, evidence), every answer and every log line.
	scan(t, f.root, append(answers, log.String())...)
}

func environmentHolds(start *openmodel.Start, key string) bool {
	for _, entry := range start.Environment([]string{"PATH=/usr/bin"}) {
		if entry == "TOGETHER_API_KEY="+key {
			return true
		}
	}
	return false
}

func fileSum(key string) string {
	sum := sha256.Sum256([]byte(key + "\n"))
	return hex.EncodeToString(sum[:])
}

func TestAKeyFileIsReplacedWholeOrNotAtAll(t *testing.T) {
	f := newFixture(t)
	m := f.manager(t, &bytes.Buffer{})
	ctx := context.Background()
	if _, err := m.SetKey(ctx, "together", serviceapi.LaneKeyRequestV1{Key: firstKey}); err != nil {
		t.Fatal(err)
	}
	// A build starting while keys change reads the old key or the new one, whole, every time.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	failures := make(chan string, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for reads := 0; ; reads++ {
			select {
			case <-stop:
				if reads == 0 {
					failures <- "nothing was read"
				}
				return
			default:
			}
			key, err := openmodel.ReadKey(f.together.KeyFile)
			if err != nil || (key.Value() != firstKey && key.Value() != secondKey) {
				select {
				case failures <- fmt.Sprintf("read %d: %v", reads, err):
				default:
				}
				return
			}
		}
	}()
	for index := 0; index < 200; index++ {
		key := firstKey
		if index%2 == 0 {
			key = secondKey
		}
		if _, err := m.SetKey(ctx, "together", serviceapi.LaneKeyRequestV1{Key: key}); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	select {
	case failure := <-failures:
		t.Fatal(failure)
	default:
	}
	// A write that fails before its rename leaves the old key and no new file.
	defer func() { syncFile, renameFile = syscall.Fsync, syscall.Renameat }()
	for name, fail := range map[string]func(){
		"sync":   func() { syncFile = func(int) error { return syscall.EIO } },
		"rename": func() { renameFile = func(int, string, int, string) error { return syscall.EIO } },
	} {
		fail()
		answer, err := m.SetKey(ctx, "together", serviceapi.LaneKeyRequestV1{Key: secondKey})
		syncFile, renameFile = syscall.Fsync, syscall.Renameat
		if !errors.Is(err, serviceapi.ErrLaneKeyUnavailable) || answer.State != "" || strings.Contains(err.Error(), secondKey) {
			t.Fatalf("%s failing = %+v, %v", name, answer, err)
		}
		if key, err := openmodel.ReadKey(f.together.KeyFile); err != nil || key.Value() != firstKey {
			t.Fatalf("%s failing: the old key is not there whole: %v", name, err)
		}
		if entries, _ := os.ReadDir(f.secrets); len(entries) != 1 {
			t.Fatalf("%s failing left a file: %v", name, entries)
		}
	}
}

func TestOnlyAProfilesProviderLaneTakesAKey(t *testing.T) {
	f := newFixture(t)
	log := &bytes.Buffer{}
	m := f.manager(t, log)
	ctx := context.Background()
	privateFolder(t, filepath.Join(f.config, "lanes"))
	accountFile := filepath.Join(f.config, "lanes", "codex-account.env")
	privateFile(t, accountFile, []byte("CODEX_HOME=/somewhere\n"))
	for lane, want := range map[string]error{
		"groq": serviceapi.ErrLaneUnknown, "codex-account": serviceapi.ErrLaneNotKey, "codex-api": serviceapi.ErrLaneNotKey, "local-p02": serviceapi.ErrLaneUnknown,
	} {
		if _, err := m.SetKey(ctx, lane, serviceapi.LaneKeyRequestV1{Key: firstKey}); !errors.Is(err, want) {
			t.Errorf("set %s = %v", lane, err)
		}
		if _, err := m.RemoveKey(ctx, lane); !errors.Is(err, want) {
			t.Errorf("remove %s = %v", lane, err)
		}
	}
	if data, _ := os.ReadFile(accountFile); string(data) != "CODEX_HOME=/somewhere\n" {
		t.Fatal("the account lane's file changed")
	}
	if _, err := m.SetKey(ctx, "together", serviceapi.LaneKeyRequestV1{Key: "too-short"}); !errors.Is(err, serviceapi.ErrLaneKeyInvalid) {
		t.Fatalf("a key of another shape = %v", err)
	}
	if entries, _ := os.ReadDir(f.secrets); len(entries) != 0 || log.String() != "abcp lane key refused at=2026-11-07T18:00:00Z lane=together reason=shape\n" {
		t.Fatalf("a refused key was written or not audited: %v %q", entries, log.String())
	}
}

func TestAKeyIsWrittenOnlyInTheControllersOwnFolder(t *testing.T) {
	ctx := context.Background()
	t.Run("a folder others may write in", func(t *testing.T) {
		f := newFixture(t)
		m := f.manager(t, &bytes.Buffer{})
		if err := os.Chmod(f.secrets, 0o777); err != nil {
			t.Fatal(err)
		}
		if _, err := m.SetKey(ctx, "together", serviceapi.LaneKeyRequestV1{Key: firstKey}); !errors.Is(err, serviceapi.ErrLaneKeyUnavailable) {
			t.Fatalf("set = %v", err)
		}
		if entries, _ := os.ReadDir(f.secrets); len(entries) != 0 {
			t.Fatalf("written: %v", entries)
		}
	})
	t.Run("a linked folder", func(t *testing.T) {
		f := newFixture(t)
		m := f.manager(t, &bytes.Buffer{})
		elsewhere := privateFolder(t, filepath.Join(f.root, "elsewhere"))
		if err := os.Remove(f.secrets); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(elsewhere, f.secrets); err != nil {
			t.Fatal(err)
		}
		if _, err := m.SetKey(ctx, "together", serviceapi.LaneKeyRequestV1{Key: firstKey}); !errors.Is(err, serviceapi.ErrLaneKeyUnavailable) {
			t.Fatalf("set = %v", err)
		}
		if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
			t.Fatalf("written through the link: %v", entries)
		}
	})
	t.Run("no folder", func(t *testing.T) {
		f := newFixture(t)
		m := f.manager(t, &bytes.Buffer{})
		if err := os.Remove(f.secrets); err != nil {
			t.Fatal(err)
		}
		if _, err := m.SetKey(ctx, "together", serviceapi.LaneKeyRequestV1{Key: firstKey}); !errors.Is(err, serviceapi.ErrLaneKeyUnavailable) {
			t.Fatalf("set = %v", err)
		}
		if _, err := os.Stat(f.secrets); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("a folder was made")
		}
	})
	t.Run("a folder in the key file's name", func(t *testing.T) {
		f := newFixture(t)
		m := f.manager(t, &bytes.Buffer{})
		privateFolder(t, f.together.KeyFile)
		if _, err := m.SetKey(ctx, "together", serviceapi.LaneKeyRequestV1{Key: firstKey}); !errors.Is(err, serviceapi.ErrLaneKeyUnavailable) {
			t.Fatalf("set = %v", err)
		}
		if _, err := m.RemoveKey(ctx, "together"); !errors.Is(err, serviceapi.ErrLaneKeyUnavailable) {
			t.Fatalf("remove = %v", err)
		}
	})
	t.Run("a key file no build reads is replaced by one it does", func(t *testing.T) {
		f := newFixture(t)
		m := f.manager(t, &bytes.Buffer{})
		privateFile(t, f.together.KeyFile, []byte(secondKey+"\n"))
		if err := os.Chmod(f.together.KeyFile, 0o644); err != nil {
			t.Fatal(err)
		}
		if lanes, _ := m.Lanes(ctx); lanes.Lanes[0].State != serviceapi.LaneKeyUnreadable || lanes.Lanes[0].Fingerprint != "" {
			t.Fatalf("a file open to others = %+v", lanes)
		}
		if set, err := m.SetKey(ctx, "together", serviceapi.LaneKeyRequestV1{Key: firstKey}); err != nil || set.State != serviceapi.LaneKeySet {
			t.Fatalf("set = %+v, %v", set, err)
		}
		if info, _ := os.Stat(f.together.KeyFile); info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v", info.Mode())
		}
		// A link in its name is replaced, and what it pointed at is left alone.
		target := filepath.Join(f.root, "target")
		privateFile(t, target, []byte("untouched\n"))
		if err := os.Remove(f.together.KeyFile); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, f.together.KeyFile); err != nil {
			t.Fatal(err)
		}
		if lanes, _ := m.Lanes(ctx); lanes.Lanes[0].State != serviceapi.LaneKeyUnreadable {
			t.Fatalf("a link = %+v", lanes)
		}
		if set, err := m.SetKey(ctx, "together", serviceapi.LaneKeyRequestV1{Key: firstKey}); err != nil || set.State != serviceapi.LaneKeySet {
			t.Fatalf("set over a link = %+v, %v", set, err)
		}
		if data, _ := os.ReadFile(target); string(data) != "untouched\n" {
			t.Fatal("the link's target changed")
		}
		if info, _ := os.Lstat(f.together.KeyFile); !info.Mode().IsRegular() {
			t.Fatal("the link is still there")
		}
	})
}

type noCatalog struct{}

func (noCatalog) ListRuns(string, int) ([]runtimecatalog.RunRegistrationV1, bool, error) {
	return nil, false, nil
}

func (noCatalog) ReadRun(string) (runtimecatalog.RunRegistrationV1, error) {
	return runtimecatalog.RunRegistrationV1{}, serviceapi.ErrDependencyNotFound
}

// liveServer is abcp serve's service API in small: its bearer token and grant files, a request log, and the lanes.
func liveServer(t *testing.T, f *fixture, grants []string, manager *Manager, log *bytes.Buffer) (http.Handler, string) {
	t.Helper()
	token := strings.Repeat("t0ken", 8)
	tokenFile := filepath.Join(f.config, "token")
	privateFile(t, tokenFile, []byte(token+"\n"))
	authenticator, err := serviceapi.LoadBearerAuthenticator(tokenFile, "repo-c-service")
	if err != nil {
		t.Fatal(err)
	}
	grantData, _ := json.Marshal(map[string]any{"principals": []map[string]any{{"principal_id": "repo-c-service", "required_authorities": grants, "may_assert_delegated_actor": true}}})
	grantsFile := filepath.Join(f.config, "grants.json")
	privateFile(t, grantsFile, grantData)
	matcher, err := serviceapi.LoadAuthorityMatcher(grantsFile)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := serviceapi.NewCursorSigner("key-v1", bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	server, err := serviceapi.NewServer(serviceapi.ServerConfig{
		Authenticator: authenticator, Authority: matcher, Catalog: noCatalog{}, CursorSigner: signer, RequestLog: log, LaneKeys: manager,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server.Handler(), token
}

func TestTheLaneRoutesWriteTheKeyFileAndNoAnswerOrLogHoldsTheKey(t *testing.T) {
	f := newFixture(t)
	log := &bytes.Buffer{}
	handler, token := liveServer(t, f, []string{"accounts.control", "lanes.control"}, f.manager(t, log), log)
	var answers []string
	send := func(method, path, credential, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		if credential != "" {
			request.Header.Set("Authorization", "Bearer "+credential)
		}
		request.Header.Set("X-Request-ID", "req-lane-keys-test")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		answers = append(answers, fmt.Sprint(response.Code, response.Header(), response.Body.String()))
		return response
	}
	if r := send(http.MethodGet, "/v1/lanes", token, ""); r.Code != 200 || r.Body.String() != `{"schema_version":"LanesV1","lanes":[{"lane":"together","kind":"key","state":"missing","profiles":["local-p02-node-together","local-p02-together"]}]}`+"\n" {
		t.Fatalf("lanes = %d %s", r.Code, r.Body.String())
	}
	if r := send(http.MethodPut, "/v1/lanes/together/key", token, `{"key":"`+firstKey+`"}`); r.Code != 200 ||
		r.Body.String() != `{"lane":"together","kind":"key","state":"set","fingerprint":"`+Fingerprint(firstKey)+`","profiles":["local-p02-node-together","local-p02-together"]}`+"\n" {
		t.Fatalf("set = %d %s", r.Code, r.Body.String())
	}
	if key, err := openmodel.ReadKey(f.together.KeyFile); err != nil || key.Value() != firstKey {
		t.Fatalf("the build's read = %v", err)
	}
	for _, refused := range []struct {
		method, path, credential, body string
		status                         int
		code                           string
	}{
		{http.MethodPut, "/v1/lanes/together/key", "", `{"key":"` + secondKey + `"}`, 401, "unauthenticated"},
		{http.MethodPut, "/v1/lanes/together/key", strings.Repeat("x", 40), `{"key":"` + secondKey + `"}`, 401, "unauthenticated"},
		{http.MethodPut, "/v1/lanes/groq/key", token, `{"key":"` + secondKey + `"}`, 404, "lane_unknown"},
		{http.MethodPut, "/v1/lanes/codex-account/key", token, `{"key":"` + secondKey + `"}`, 409, "lane_not_key"},
		{http.MethodPut, "/v1/lanes/codex-api/key", token, `{"key":"` + secondKey + `"}`, 409, "lane_not_key"},
		{http.MethodPut, "/v1/lanes/together/key", token, `{"key":"` + secondKey + ` "}`, 400, "lane_key_invalid"},
		{http.MethodPut, "/v1/lanes/together/key", token, `{"key":"` + secondKey + `","note":1}`, 400, "invalid_request"},
		{http.MethodPut, "/v1/lanes/together/key", token, `{"key":"` + strings.Repeat(secondKey, 90) + `"}`, 413, "request_too_large"},
		{http.MethodDelete, "/v1/lanes/codex-account/key", token, "", 409, "lane_not_key"},
	} {
		if r := send(refused.method, refused.path, refused.credential, refused.body); r.Code != refused.status || !strings.Contains(r.Body.String(), `"code":"`+refused.code+`"`) {
			t.Errorf("%s %s = %d %s", refused.method, refused.path, r.Code, r.Body.String())
		}
	}
	if key, err := openmodel.ReadKey(f.together.KeyFile); err != nil || key.Value() != firstKey {
		t.Fatalf("a refused request changed the key: %v", err)
	}
	if r := send(http.MethodDelete, "/v1/lanes/together/key", token, ""); r.Code != 200 || !strings.Contains(r.Body.String(), `"state":"missing"`) || strings.Contains(r.Body.String(), "fingerprint") {
		t.Fatalf("remove = %d %s", r.Code, r.Body.String())
	}
	if _, err := os.Lstat(f.together.KeyFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the key file is still there")
	}
	if strings.Count(log.String(), "abcp request ") != 12 || !strings.Contains(log.String(), "abcp lane key set at=2026-11-07T18:00:00Z lane=together fingerprint="+Fingerprint(firstKey)+"\n") ||
		!strings.Contains(log.String(), "abcp lane key refused at=2026-11-07T18:00:00Z lane=together reason=shape\n") ||
		!strings.Contains(log.String(), "abcp lane key removed at=2026-11-07T18:00:00Z lane=together fingerprint="+Fingerprint(firstKey)+"\n") {
		t.Fatalf("log:\n%s", log.String())
	}
	scan(t, f.root, append(answers, log.String())...)

	// Without lanes.control the routes refuse, and nothing is written.
	g := newFixture(t)
	handler, token = liveServer(t, g, []string{"accounts.control"}, g.manager(t, &bytes.Buffer{}), &bytes.Buffer{})
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		path, body := "/v1/lanes/together/key", `{"key":"`+firstKey+`"}`
		if method == http.MethodGet {
			path, body = "/v1/lanes", ""
		} else if method == http.MethodDelete {
			body = ""
		}
		if r := send(method, path, token, body); r.Code != 403 || !strings.Contains(r.Body.String(), `"code":"authority_denied"`) {
			t.Errorf("ungranted %s = %d %s", method, r.Code, r.Body.String())
		}
	}
	if entries, _ := os.ReadDir(g.secrets); len(entries) != 0 {
		t.Fatalf("an ungranted request wrote: %v", entries)
	}
}

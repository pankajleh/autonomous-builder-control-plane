//go:build linux

package remotetarget

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

const (
	testApp     = "0b7f0f6a-1c2d-4e5f-8a9b-0c1d2e3f4a5b"
	testVersion = "1111111111111111111111111111111111111111"
	testHost    = "13-232-1-2.sslip.io"
)

// release builds a release's tar the way Repo C does, and answers it with its digest.
func release(t *testing.T, files map[string]string, change func(*manifest, *tar.Writer)) ([]byte, string) {
	t.Helper()
	man := manifest{SchemaVersion: manifestSchema, App: testApp, Version: testVersion, Kind: "static"}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sortStrings(names)
	for _, name := range names {
		sum := sha256.Sum256([]byte(files[name]))
		man.Files = append(man.Files, manifestFile{Path: name, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(files[name]))})
	}
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	if change != nil {
		change(&man, w)
	}
	data, _ := json.Marshal(man)
	add := func(name string, body []byte) {
		_ = w.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg, ModTime: time.Unix(0, 0)})
		_, _ = w.Write(body)
	}
	add(manifestName, data)
	for _, name := range names {
		add(name, []byte(files[name]))
	}
	_ = w.Close()
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// localRemote runs each script with sh in a folder standing for the deploy
// login's home, with a fake docker on its PATH that records what it was asked.
type localRemote struct {
	home    string
	bin     string
	mu      sync.Mutex
	scripts []string
	hold    chan struct{}
}

func newLocalRemote(t *testing.T) *localRemote {
	t.Helper()
	l := &localRemote{home: t.TempDir(), bin: t.TempDir()}
	// The fake keeps containers, networks, volumes and images as files under
	// $HOME/.docker, and the shared Postgres's roles and databases too, so a
	// script sees what earlier scripts did; a database's file holds its rows
	// as the backup and restore scripts count them, which a dump carries after
	// its first line. A Node app answers /health unless $HOME/app.fails-once
	// was there when it started; $HOME/restore.loses-rows makes a restore
	// lose every row.
	fake := `#!/bin/sh
echo "$@" >> "$HOME/docker.log"
S="$HOME/.docker"
mkdir -p "$S/nets"
last() { for a; do l=$a; done; echo "$l"; }
case "$1" in
  inspect)
    n=$(last "$@")
    case "$*" in
      *NetworkSettings*)
        [ -f "$S/c-$n" ] || exit 1
        printf '{'; for f in "$S/nets/$n"/*; do [ -e "$f" ] && printf '"%s":{},' "${f##*/}"; done; echo '}' ;;
      *) if [ -f "$S/c-$n" ]; then echo true; else echo false; fi ;;
    esac ;;
  run)
    n=""; net=""; prev=""
    for a; do [ "$prev" = "--name" ] && n=$a; [ "$prev" = "--network" ] && net=$a; prev=$a; done
    touch "$S/c-$n"; mkdir -p "$S/nets/$n"
    if [ -n "$net" ]; then touch "$S/nets/$n/$net"; fi
    case "$n" in autobuild-app-*) if [ -f "$HOME/app.fails-once" ]; then touch "$S/c-$n.unhealthy"; rm -f "$HOME/app.fails-once"; fi ;; esac ;;
  rm) n=$(last "$@"); rm -rf "$S/c-$n" "$S/c-$n.unhealthy" "$S/nets/$n" ;;
  exec)
    shift; if [ "$1" = "-i" ]; then shift; fi
    n=$1; shift
    [ -f "$S/c-$n" ] || exit 1
    db=""; prev=""; for a; do [ "$prev" = "-d" ] && db=$a; prev=$a; done
    rows() { if [ -s "$S/db-$1" ]; then cat "$S/db-$1"; else echo '{}'; fi; }
    mkdir -p "$S/scratch"
    case "$*" in
      *pg_isready*) exit 0 ;;
      "/bin/sh -c "*) shift 2; sh -c "$(printf '%s' "$1" | sed "s|/scratch|$S/scratch|g")" ;;
      *pg_restore*)
        [ -f "$S/db-$db" ] || exit 1
        tail -n +2 > "$S/db-$db"
        if [ -f "$HOME/restore.loses-rows" ]; then echo '{"public.members":0}' > "$S/db-$db"; fi ;;
      *"FROM pg_roles"*) r=$(echo "$*" | sed -n "s/.*rolname = '\([a-z0-9_]*\)'.*/\1/p"); if [ -f "$S/role-$r" ]; then echo 1; fi ;;
      *"FROM pg_database"*) d=$(echo "$*" | sed -n "s/.*datname = '\([a-z0-9_]*\)'.*/\1/p"); if [ -f "$S/db-$d" ]; then echo 1; fi ;;
      *"CREATE DATABASE"*) d=$(echo "$*" | sed -n 's/.*CREATE DATABASE \([a-z0-9_]*\) .*/\1/p'); touch "$S/db-$d" ;;
      *"DROP DATABASE"*) d=$(echo "$*" | sed -n 's/.*DROP DATABASE IF EXISTS \([a-z0-9_]*\) .*/\1/p'); rm -f "$S/db-$d" ;;
      *psql*)
        sql=$(cat); echo "$sql" >> "$S/psql.stdin"
        r=$(echo "$sql" | sed -n 's/^\(CREATE\|ALTER\) ROLE \([a-z0-9_]*\) .*/\2/p'); if [ -n "$r" ]; then touch "$S/role-$r"; fi
        case "$sql" in
          *pg_export_snapshot*)
            c=$(echo "$sql" | sed -n 's|^\\o /scratch/\(.*\)$|\1|p')
            rows "$db" > "$S/scratch/$c"
            printf 'PGDMP fake dump of %s\n' "$db"; rows "$db" ;;
          *json_object_agg*) rows "$db" ;;
        esac
        echo "$sql" | while IFS= read -r line; do
          case "$line" in
            "DROP DATABASE "*) rm -f "$S/db-$(echo "$line" | sed -n 's/^DROP DATABASE \(IF EXISTS \)\{0,1\}\([a-z0-9_]*\).*/\2/p')" ;;
            "ALTER DATABASE "*)
              from=$(echo "$line" | sed -n 's/^ALTER DATABASE \([a-z0-9_]*\) RENAME TO .*/\1/p')
              to=$(echo "$line" | sed -n 's/^ALTER DATABASE [a-z0-9_]* RENAME TO \([a-z0-9_]*\);$/\1/p')
              [ -f "$S/db-$from" ] || exit 1
              mv "$S/db-$from" "$S/db-$to" ;;
          esac
        done || exit 1 ;;
      *wget*) if [ -f "$S/c-$n.unhealthy" ]; then exit 1; fi ;;
    esac ;;
  network)
    case "$2" in
      inspect) if [ -f "$S/net-$3" ]; then exit 0; else exit 1; fi ;;
      create) n=$(last "$@"); touch "$S/net-$n"; case "$*" in *autobuild.site=*) touch "$S/site-net-$n" ;; esac ;;
      connect|disconnect)
        verb=$2; shift 2; while [ $# -gt 2 ]; do shift; done
        net=$1; c=$2
        if [ "$verb" = disconnect ]; then rm -f "$S/nets/$c/$net"; exit 0; fi
        [ -f "$S/net-$net" ] && [ -f "$S/c-$c" ] || exit 1
        touch "$S/nets/$c/$net" ;;
      rm) rm -f "$S/net-$3" "$S/site-net-$3" ;;
      ls) for f in "$S"/site-net-*; do [ -e "$f" ] && echo "${f##*/site-net-}"; done ;;
    esac ;;
  volume)
    case "$2" in
      inspect) if [ -f "$S/vol-$3" ]; then exit 0; else exit 1; fi ;;
      create) touch "$S/vol-$(last "$@")" ;;
    esac ;;
  image) if [ -f "$S/img-$(echo "$3" | cksum | cut -d' ' -f1)" ]; then exit 0; else exit 1; fi ;;
  pull) touch "$S/img-$(last "$@" | cksum | cut -d' ' -f1)" ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(l.bin, "docker"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	return l
}

func (l *localRemote) Run(ctx context.Context, script string, stdin io.Reader) (string, error) {
	l.mu.Lock()
	l.scripts = append(l.scripts, script)
	hold := l.hold
	l.mu.Unlock()
	if hold != nil {
		<-hold
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Env = []string{"HOME=" + l.home, "PATH=" + l.bin + ":" + os.Getenv("PATH")}
	cmd.Stdin = stdin
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return out.String(), errStep(lastLine(errOut.String()))
	}
	return out.String(), nil
}

func (l *localRemote) Close() error { return nil }

func (l *localRemote) docker() string {
	data, _ := os.ReadFile(filepath.Join(l.home, "docker.log"))
	return string(data)
}

// serve answers like the front door: the Caddyfile's block for the host names
// the site whose link it serves index.html from, or the Node app it proxies,
// which answers when it runs, is healthy and the front door is on its network.
func (l *localRemote) serve(host, path string) (string, error) {
	data, err := os.ReadFile(filepath.Join(l.home, "autobuild/frontdoor/config/Caddyfile"))
	if err != nil {
		return "", errStep("unreachable")
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if line == host+" {" && i+1 < len(lines) {
			if app, ok := strings.CutPrefix(strings.TrimSpace(lines[i+1]), "reverse_proxy autobuild-app-"); ok {
				app = strings.TrimSuffix(app, ":8080")
				state := filepath.Join(l.home, ".docker")
				_, running := os.Stat(filepath.Join(state, "c-autobuild-app-"+app))
				_, unhealthy := os.Stat(filepath.Join(state, "c-autobuild-app-"+app+".unhealthy"))
				_, joined := os.Stat(filepath.Join(state, "nets/autobuild-frontdoor/autobuild-net-"+app))
				if path != "/health" || running != nil || unhealthy == nil || joined != nil {
					return "", errStep("status")
				}
				return "healthy", nil
			}
			site := strings.TrimPrefix(strings.TrimSpace(lines[i+1]), "root * /srv/sites/")
			body, err := os.ReadFile(filepath.Join(l.home, "autobuild/sites", site, "index.html"))
			if err != nil {
				return "", errStep("status")
			}
			sum := sha256.Sum256(body)
			return hex.EncodeToString(sum[:]), nil
		}
	}
	return "", errStep("certificate")
}

type deployFixture struct {
	t      *testing.T
	m      *Manager
	remote *localRemote
	now    time.Time
	key    string
}

func newDeployFixture(t *testing.T) *deployFixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "targets")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	f := &deployFixture{t: t, remote: newLocalRemote(t), now: time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC), key: "srv-0123456789ab"}
	m, err := New(dir, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	m.resolve = func(_ context.Context, host string) ([]net.IP, error) {
		if host == testHost {
			return []net.IP{net.ParseIP("13.232.1.2")}, nil
		}
		return []net.IP{net.ParseIP("13.232.9.9")}, nil
	}
	m.remote = func(context.Context, record) (remote, string) { return f.remote, "" }
	m.fetch = func(_ context.Context, ip net.IP, host, path string) (string, error) {
		if !ip.Equal(net.ParseIP("13.232.1.2")) {
			return "", errStep("unreachable")
		}
		return f.remote.serve(host, path)
	}
	m.checkEvery, m.checkFor, m.healthWait = time.Millisecond, 50*time.Millisecond, 2
	f.m = m
	if _, err := m.CreateTarget(context.Background(), f.key, serviceapi.TargetCreateV1{Address: "13.232.1.2"}); err != nil {
		t.Fatal(err)
	}
	r, _ := m.read(f.key)
	r.State, r.HostKey, r.Fingerprint = stateConfirmed, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOk7mQ2bZr9xW3vT8yP1sL4nE6hG0jK5cA2dF7uR9wB+", "SHA256:x"
	if err := m.write(r); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *deployFixture) put(files map[string]string) string {
	f.t.Helper()
	data, digest := release(f.t, files, nil)
	if _, created, err := f.m.PutRelease(context.Background(), digest, bytes.NewReader(data)); err != nil || !created {
		f.t.Fatalf("put = %v %v", created, err)
	}
	return digest
}

func (f *deployFixture) deploy(digest, site, host, request string) (serviceapi.PlanV1, serviceapi.OperationV1) {
	f.t.Helper()
	plan, err := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Release: digest, Site: serviceapi.PlanSiteV1{Name: site, Host: host}})
	if err != nil {
		f.t.Fatalf("plan: %v", err)
	}
	op, err := f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: plan.PlanID, PlanHash: plan.Hash, RequestID: request})
	if err != nil {
		f.t.Fatalf("apply: %v", err)
	}
	f.m.Wait()
	done, err := f.m.Operation(context.Background(), f.key, op.OperationID)
	if err != nil {
		f.t.Fatal(err)
	}
	return plan, done
}

func TestAReleaseIsKeptOnlyWhenEveryFileMatchesItsManifest(t *testing.T) {
	f := newDeployFixture(t)
	files := map[string]string{"index.html": "<h1>Hi</h1>", "css/site.css": "body{}", "img/logo one.svg": "<svg/>"}
	data, digest := release(t, files, nil)
	got, created, err := f.m.PutRelease(context.Background(), digest, bytes.NewReader(data))
	if err != nil || !created || got.Files != 3 || got.Kind != "static" || got.App != testApp || got.Version != testVersion || got.Bytes != 23 {
		t.Fatalf("put = %+v %v %v", got, created, err)
	}
	if _, created, err := f.m.PutRelease(context.Background(), digest, bytes.NewReader(data)); err != nil || created {
		t.Fatalf("the same release again = %v %v", created, err)
	}
	if info, err := os.Stat(filepath.Join(f.m.dir, "releases")); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("releases folder: %v %v", info, err)
	}
	if _, err := f.m.Release(context.Background(), strings.Repeat("0", 64)); !errors.Is(err, serviceapi.ErrReleaseUnknown) {
		t.Fatalf("unknown release = %v", err)
	}
	bad := map[string]func() ([]byte, string){
		"another digest": func() ([]byte, string) { d, _ := release(t, files, nil); return d, strings.Repeat("0", 64) },
		"a link": func() ([]byte, string) {
			return release(t, files, func(_ *manifest, w *tar.Writer) {
				_ = w.WriteHeader(&tar.Header{Name: "evil", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink})
			})
		},
		"a name climbing out": func() ([]byte, string) {
			return release(t, files, func(m *manifest, w *tar.Writer) {
				_ = w.WriteHeader(&tar.Header{Name: "../x", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
				_, _ = w.Write([]byte("x"))
			})
		},
		"an absolute name": func() ([]byte, string) {
			return release(t, files, func(m *manifest, w *tar.Writer) {
				_ = w.WriteHeader(&tar.Header{Name: "/x", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
				_, _ = w.Write([]byte("x"))
			})
		},
		"a file the manifest leaves out": func() ([]byte, string) {
			return release(t, files, func(m *manifest, w *tar.Writer) {
				_ = w.WriteHeader(&tar.Header{Name: "extra.js", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
				_, _ = w.Write([]byte("x"))
			})
		},
		"a wrong hash": func() ([]byte, string) {
			return release(t, files, func(m *manifest, _ *tar.Writer) { m.Files[0].SHA256 = strings.Repeat("0", 64) })
		},
		"a newline in a name": func() ([]byte, string) { return release(t, map[string]string{"index.html": "x", "a\nb.js": "y"}, nil) },
		"no index.html":       func() ([]byte, string) { return release(t, map[string]string{"home.html": "x"}, nil) },
		"another kind": func() ([]byte, string) {
			return release(t, files, func(m *manifest, _ *tar.Writer) { m.Kind = "node" })
		},
	}
	for name, make := range bad {
		data, digest := make()
		if _, _, err := f.m.PutRelease(context.Background(), digest, bytes.NewReader(data)); !errors.Is(err, serviceapi.ErrReleaseInvalid) {
			t.Errorf("%s = %v", name, err)
		}
	}
	if _, _, err := f.m.PutRelease(context.Background(), strings.Repeat("0", 64), strings.NewReader("not a tar")); !errors.Is(err, serviceapi.ErrReleaseInvalid) {
		t.Fatalf("not a tar = %v", err)
	}
}

func TestAPlannedSiteIsSentCheckedSwitchedServedAndReplaced(t *testing.T) {
	f := newDeployFixture(t)
	first := f.put(map[string]string{"index.html": "<h1>One</h1>", "app.js": "1"})
	plan, op := f.deploy(first, "shop", testHost, "req-first-000")
	if strings.Join(plan.Steps, ",") != "send_files,switch_site,write_site,start_frontdoor,check" || plan.Replaces != "" || plan.Hash != planHash(plan) {
		t.Fatalf("plan = %+v", plan)
	}
	if op.State != opSucceeded {
		t.Fatalf("operation = %+v", op)
	}
	home := f.remote.home
	link, err := os.Readlink(filepath.Join(home, "autobuild/sites/shop"))
	if err != nil || link != "../releases/"+first {
		t.Fatalf("link = %q %v", link, err)
	}
	if body, _ := os.ReadFile(filepath.Join(home, "autobuild/releases", first, "app.js")); string(body) != "1" {
		t.Fatalf("files not there: %q", body)
	}
	if _, err := os.Stat(filepath.Join(home, "autobuild/releases", first, manifestName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the manifest is not served")
	}
	caddy, _ := os.ReadFile(filepath.Join(home, "autobuild/frontdoor/config/Caddyfile"))
	if !strings.Contains(string(caddy), testHost+" {\n\troot * /srv/sites/shop\n\tfile_server") {
		t.Fatalf("Caddyfile = %s", caddy)
	}
	docker := f.remote.docker()
	for _, want := range []string{"pull -q " + FrontDoorImage, "run -d --name autobuild-frontdoor --restart unless-stopped --read-only --cap-drop ALL --cap-add NET_BIND_SERVICE",
		"--security-opt no-new-privileges", "-p 80:80 -p 443:443", "/srv/sites:ro", "/srv/releases:ro"} {
		if !strings.Contains(docker, want) {
			t.Fatalf("docker did not get %q:\n%s", want, docker)
		}
	}
	sites, _ := f.m.Sites(context.Background(), f.key)
	if len(sites.Sites) != 1 || sites.Sites[0].State != siteLive || sites.Sites[0].Release != first || sites.Sites[0].LastCheck == nil || !sites.Sites[0].LastCheck.OK {
		t.Fatalf("sites = %+v", sites)
	}

	second := f.put(map[string]string{"index.html": "<h1>Two</h1>"})
	plan, op = f.deploy(second, "shop", testHost, "req-second-00")
	if strings.Join(plan.Steps, ",") != "send_files,switch_site,write_site,reload_frontdoor,check" || plan.Replaces != first || op.State != opSucceeded {
		t.Fatalf("second plan %+v op %+v", plan, op)
	}
	if link, _ := os.Readlink(filepath.Join(home, "autobuild/sites/shop")); link != "../releases/"+second {
		t.Fatalf("link = %q", link)
	}
	if _, err := os.Stat(filepath.Join(home, "autobuild/releases", first)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the replaced release stays on the server")
	}
	if !strings.Contains(f.remote.docker(), "exec autobuild-frontdoor caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile") {
		t.Fatalf("no reload: %s", f.remote.docker())
	}
	r, _ := f.m.read(f.key)
	if strings.Join(r.Releases, ",") != second {
		t.Fatalf("releases on the server = %v", r.Releases)
	}
	// The same release again needs no files sent.
	plan, op = f.deploy(second, "shop", testHost, "req-third-000")
	if plan.Steps[0] != "reuse_files" || op.State != opSucceeded || op.Steps[0].Detail != "already there" {
		t.Fatalf("again plan %+v op %+v", plan, op)
	}
}

func TestAPlanIsAppliedOnceBeforeItExpiresAndOnlyAsSealed(t *testing.T) {
	f := newDeployFixture(t)
	digest := f.put(map[string]string{"index.html": "x"})
	plan, err := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Release: digest, Site: serviceapi.PlanSiteV1{Name: "shop", Host: testHost}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: plan.PlanID, PlanHash: strings.Repeat("0", 64), RequestID: "req-12345678"}); !errors.Is(err, serviceapi.ErrPlanStale) {
		t.Fatalf("another hash = %v", err)
	}
	f.now = f.now.Add(31 * time.Minute)
	if _, err := f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: plan.PlanID, PlanHash: plan.Hash, RequestID: "req-12345678"}); !errors.Is(err, serviceapi.ErrPlanStale) {
		t.Fatalf("expired = %v", err)
	}
	f.now = f.now.Add(-31 * time.Minute)
	op, err := f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: plan.PlanID, PlanHash: plan.Hash, RequestID: "req-12345678"})
	if err != nil {
		t.Fatal(err)
	}
	f.m.Wait()
	again, err := f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: plan.PlanID, PlanHash: plan.Hash, RequestID: "req-12345678"})
	if err != nil || again.OperationID != op.OperationID {
		t.Fatalf("the same request answers the same operation: %v %v", again.OperationID, err)
	}
	if _, err := f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: plan.PlanID, PlanHash: plan.Hash, RequestID: "req-87654321"}); !errors.Is(err, serviceapi.ErrPlanStale) {
		t.Fatalf("applied twice = %v", err)
	}
	for name, create := range map[string]serviceapi.PlanCreateV1{
		"a name with a shell in it": {Release: digest, Site: serviceapi.PlanSiteV1{Name: "shop;reboot", Host: testHost}},
		"a host with a shell in it": {Release: digest, Site: serviceapi.PlanSiteV1{Name: "shop", Host: "$(id).example.com"}},
		"an address for a host":     {Release: digest, Site: serviceapi.PlanSiteV1{Name: "shop", Host: "13.232.1.2"}},
		"a one-word host":           {Release: digest, Site: serviceapi.PlanSiteV1{Name: "shop", Host: "localhost"}},
		"another site on its host":  {Release: digest, Site: serviceapi.PlanSiteV1{Name: "blog", Host: testHost}},
	} {
		if _, err := f.m.CreatePlan(context.Background(), f.key, create); !errors.Is(err, serviceapi.ErrTargetInvalid) {
			t.Errorf("%s = %v", name, err)
		}
	}
	if _, err := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Release: strings.Repeat("0", 64), Site: serviceapi.PlanSiteV1{Name: "shop", Host: testHost}}); !errors.Is(err, serviceapi.ErrReleaseUnknown) {
		t.Fatalf("unknown release = %v", err)
	}
	r, _ := f.m.read(f.key)
	r.State = statePending
	_ = f.m.write(r)
	if _, err := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Release: digest, Site: serviceapi.PlanSiteV1{Name: "shop", Host: testHost}}); !errors.Is(err, serviceapi.ErrTargetNotReady) {
		t.Fatalf("an unconfirmed target = %v", err)
	}
}

func TestOneOperationAtATimeAndAFailedCheckFailsTheSite(t *testing.T) {
	f := newDeployFixture(t)
	digest := f.put(map[string]string{"index.html": "x"})
	f.remote.hold = make(chan struct{})
	plan, _ := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Release: digest, Site: serviceapi.PlanSiteV1{Name: "shop", Host: testHost}})
	if _, err := f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: plan.PlanID, PlanHash: plan.Hash, RequestID: "req-12345678"}); err != nil {
		t.Fatal(err)
	}
	other, _ := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Release: digest, Site: serviceapi.PlanSiteV1{Name: "blog", Host: "13-232-1-2.nip.example"}})
	if _, err := f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: other.PlanID, PlanHash: other.Hash, RequestID: "req-87654321"}); !errors.Is(err, serviceapi.ErrTargetBusy) {
		t.Fatalf("a second operation = %v", err)
	}
	close(f.remote.hold)
	f.m.Wait()
	f.remote.hold = nil

	// A name that does not point at the server fails the check, and the site with it.
	plan, err := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Release: digest, Site: serviceapi.PlanSiteV1{Name: "blog", Host: "13-232-1-2.nip.example"}})
	if err != nil {
		t.Fatal(err)
	}
	op, _ := f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: plan.PlanID, PlanHash: plan.Hash, RequestID: "req-blog-0001"})
	f.m.Wait()
	op, _ = f.m.Operation(context.Background(), f.key, op.OperationID)
	last := op.Steps[len(op.Steps)-1]
	if op.State != opFailed || last.Name != "check" || last.State != "failed" || last.Detail != "dns" {
		t.Fatalf("operation = %+v", op)
	}
	sites, _ := f.m.Sites(context.Background(), f.key)
	for _, s := range sites.Sites {
		if s.Name == "blog" && (s.State != siteFailed || s.LastCheck == nil || s.LastCheck.OK) {
			t.Fatalf("blog = %+v", s)
		}
	}
	// A site whose update failed keeps its block while another site is deployed.
	third := f.put(map[string]string{"index.html": "third"})
	if _, op := f.deploy(third, "shop", testHost, "req-third-001"); op.State != opSucceeded {
		t.Fatalf("shop = %+v", op)
	}
	caddy, _ := os.ReadFile(filepath.Join(f.remote.home, "autobuild/frontdoor/config/Caddyfile"))
	if !strings.Contains(string(caddy), "13-232-1-2.nip.example {") || !strings.Contains(string(caddy), testHost+" {") {
		t.Fatalf("Caddyfile = %s", caddy)
	}
}

func TestRemovingTheLastSiteStopsTheFrontDoorAndLeavesNothingBehind(t *testing.T) {
	f := newDeployFixture(t)
	digest := f.put(map[string]string{"index.html": "x"})
	if _, op := f.deploy(digest, "shop", testHost, "req-first-000"); op.State != opSucceeded {
		t.Fatalf("deploy = %+v", op)
	}
	op, err := f.m.RemoveSite(context.Background(), f.key, "shop", serviceapi.SiteRemoveV1{RequestID: "req-remove-01"})
	if err != nil {
		t.Fatal(err)
	}
	f.m.Wait()
	op, _ = f.m.Operation(context.Background(), f.key, op.OperationID)
	if op.State != opSucceeded {
		t.Fatalf("remove = %+v", op)
	}
	home := f.remote.home
	if _, err := os.Lstat(filepath.Join(home, "autobuild/sites/shop")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the link stays")
	}
	if _, err := os.Stat(filepath.Join(home, "autobuild/releases", digest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the release stays")
	}
	if caddy, _ := os.ReadFile(filepath.Join(home, "autobuild/frontdoor/config/Caddyfile")); strings.Contains(string(caddy), testHost) {
		t.Fatalf("Caddyfile = %s", caddy)
	}
	if !strings.Contains(f.remote.docker(), "rm -f autobuild-frontdoor") {
		t.Fatal("the front door keeps running")
	}
	if sites, _ := f.m.Sites(context.Background(), f.key); len(sites.Sites) != 0 {
		t.Fatalf("sites = %+v", sites)
	}
	if _, err := f.m.RemoveSite(context.Background(), f.key, "shop", serviceapi.SiteRemoveV1{RequestID: "req-remove-02"}); !errors.Is(err, serviceapi.ErrSiteUnknown) {
		t.Fatalf("remove again = %v", err)
	}
	for _, script := range f.remote.scripts {
		if regexp.MustCompile(`@[A-Z]+@`).MatchString(script) || strings.Contains(script, "sudo") || strings.Contains(script, "rm -rf /") {
			t.Fatalf("a script = %s", script)
		}
	}
}

func TestAnOperationAPreviousABCPLeftRunningIsUnknown(t *testing.T) {
	f := newDeployFixture(t)
	digest := f.put(map[string]string{"index.html": "x"})
	f.remote.hold = make(chan struct{})
	plan, _ := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Release: digest, Site: serviceapi.PlanSiteV1{Name: "shop", Host: testHost}})
	op, err := f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: plan.PlanID, PlanHash: plan.Hash, RequestID: "req-12345678"})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		f.remote.mu.Lock()
		started := len(f.remote.scripts) > 0
		f.remote.mu.Unlock()
		if started || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	// A new ABCP on the same folder, as after a restart mid-operation.
	again, err := New(f.m.dir, func() time.Time { return f.now })
	if err != nil {
		t.Fatal(err)
	}
	got, _ := again.Operation(context.Background(), f.key, op.OperationID)
	sites, _ := again.Sites(context.Background(), f.key)
	close(f.remote.hold)
	f.m.Wait()
	if got.State != opUnknown || len(sites.Sites) != 1 || sites.Sites[0].State != siteUnknown {
		t.Fatalf("after a restart: %+v %+v", got, sites)
	}
}

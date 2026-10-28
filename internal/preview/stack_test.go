package preview

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func stackSpec() StackSpec {
	return StackSpec{Image: "ghcr.io/pankajleh/autobuild-stack-python@sha256:" + strings.Repeat("c", 64), CPUQuota: 200000, MemoryBytes: 4 << 30,
		PidsLimit: 1024, TmpfsBytes: 256 << 20, Lifetime: 32 * time.Minute, Egress: []string{EgressPyPI}}
}

func TestAStackSpecIsPinnedBoundedAndReachesOnlyRegistries(t *testing.T) {
	if !stackSpec().Valid() {
		t.Fatal("good spec refused")
	}
	for name, change := range map[string]func(*StackSpec){
		"tag":       func(s *StackSpec) { s.Image = "python:3.12" },
		"cpu":       func(s *StackSpec) { s.CPUQuota = 1 },
		"memory":    func(s *StackSpec) { s.MemoryBytes = 64 << 30 },
		"pids":      func(s *StackSpec) { s.PidsLimit = 0 },
		"lifetime":  func(s *StackSpec) { s.Lifetime = 5 * time.Hour },
		"no egress": func(s *StackSpec) { s.Egress = nil },
		"shopify":   func(s *StackSpec) { s.Egress = []string{EgressShopifyStores} },
		"relay":     func(s *StackSpec) { s.Egress = []string{EgressAutobuildRelay} },
		"twice":     func(s *StackSpec) { s.Egress = []string{EgressNpm, EgressNpm} },
	} {
		s := stackSpec()
		change(&s)
		if s.Valid() {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestAStackContainerHasThePreviewWallsAndOnlyItsTwoFolders(t *testing.T) {
	d := newDocker("/srv/abcp/previews")
	id := strings.Repeat("a", 64)
	args := strings.Join(d.stackCreateArgs(id, stackSpec(), "1000:1000", "/srv/stack/work/j", "/srv/stack/caches/k"), " ")
	for _, want := range []string{"--pull=never", "--network " + d.network(id), "--user 1000:1000", "--cap-drop=ALL", "--security-opt=no-new-privileges",
		"--read-only", "--cpu-quota=200000", "--memory=4294967296", "--memory-swap=4294967296", "--pids-limit=1024", "--ipc=none", "--log-driver=none",
		"--tmpfs /tmp:rw,nosuid,nodev,size=268435456,mode=1777", "--mount type=bind,src=/srv/stack/work/j,dst=/work,bind-propagation=rprivate",
		"--mount type=bind,src=/srv/stack/caches/k,dst=/cache,bind-propagation=rprivate", "--runtime=runsc", "--label abcp.stack.id=" + id,
		"--label " + d.label(), "-i PATH=/usr/bin:/bin /bin/sleep 1920"} {
		if !strings.Contains(args, want) {
			t.Errorf("create lacks %q:\n%s", want, args)
		}
	}
	for _, never := range []string{"--privileged", "--network host", "docker.sock", "-p ", "--publish", "--cap-add"} {
		if strings.Contains(args, never) {
			t.Errorf("create has %q", never)
		}
	}
}

func TestOpeningAStackRefusesBadInputsBeforeDocker(t *testing.T) {
	d := newDocker("/srv/abcp/previews")
	ran := 0
	d.run = func(context.Context, ...string) (string, error) { ran++; return "", ErrUnavailable }
	ctx := context.Background()
	good := strings.Repeat("a", 64)
	for name, call := range map[string]func() error{
		"id":        func() error { _, err := d.openStack(ctx, "x", stackSpec(), "/srv/w", "/srv/c"); return err },
		"spec":      func() error { _, err := d.openStack(ctx, good, StackSpec{}, "/srv/w", "/srv/c"); return err },
		"relative":  func() error { _, err := d.openStack(ctx, good, stackSpec(), "srv/w", "/srv/c"); return err },
		"comma":     func() error { _, err := d.openStack(ctx, good, stackSpec(), "/srv/w,dst=/etc", "/srv/c"); return err },
		"dot dot":   func() error { _, err := d.openStack(ctx, good, stackSpec(), "/srv/../etc", "/srv/c"); return err },
		"not clean": func() error { _, err := d.openStack(ctx, good, stackSpec(), "/srv/w/", "/srv/c"); return err },
	} {
		if call() == nil {
			t.Errorf("%s opened", name)
		}
	}
	if ran != 0 {
		t.Fatalf("docker ran %d times for refused inputs", ran)
	}
	if os.Getuid() > 0 {
		if user, ok := stackUser(); !ok || user != strconv.Itoa(os.Getuid())+":"+strconv.Itoa(os.Getgid()) {
			t.Fatalf("user = %q", user)
		}
	}
}

func TestAStepRunsWithExactlyItsEnvironmentUnderWork(t *testing.T) {
	d := newDocker("/srv/abcp/previews")
	var got []string
	d.attach = func(_ context.Context, w io.Writer, args ...string) (int, error) {
		got = args
		io.WriteString(w, "ok\n")
		return 3, nil
	}
	x := &StackSession{d: d, id: strings.Repeat("b", 64)}
	var out strings.Builder
	code, err := x.Exec(context.Background(), "/work/api", []string{"PATH=/usr/bin:/bin", "CI=1"}, []string{"npm", "test"}, &out)
	if err != nil || code != 3 || out.String() != "ok\n" {
		t.Fatalf("exec = %d %v %q", code, err, out.String())
	}
	if strings.Join(got, " ") != "exec --workdir /work/api "+d.container(x.id, 0)+" /usr/bin/env -i PATH=/usr/bin:/bin CI=1 npm test" {
		t.Fatalf("args = %v", got)
	}
	for name, call := range map[string]func() error{
		"outside work": func() error {
			_, err := x.Exec(context.Background(), "/etc", nil, []string{"ls"}, io.Discard)
			return err
		},
		"dot dot": func() error {
			_, err := x.Exec(context.Background(), "/work/..", nil, []string{"ls"}, io.Discard)
			return err
		},
		"env": func() error {
			_, err := x.Exec(context.Background(), "/work", []string{"BAD KEY=1"}, []string{"ls"}, io.Discard)
			return err
		},
		"env newline": func() error {
			_, err := x.Exec(context.Background(), "/work", []string{"A=1\nB=2"}, []string{"ls"}, io.Discard)
			return err
		},
		"no command": func() error { _, err := x.Exec(context.Background(), "/work", nil, nil, io.Discard); return err },
	} {
		if call() == nil {
			t.Errorf("%s ran", name)
		}
	}
	x.closed.Store(true)
	if _, err := x.Exec(context.Background(), "/work", nil, []string{"ls"}, io.Discard); err == nil {
		t.Fatal("a closed part ran a step")
	}
}

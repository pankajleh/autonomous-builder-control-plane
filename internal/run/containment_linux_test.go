//go:build linux

package run

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCgroupV2EvidenceFailsClosedOnMembershipAndTeardown(t *testing.T) {
	root := t.TempDir()
	scope := filepath.Join(root, "abcp-run")
	if err := os.Mkdir(scope, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCgroupTestFile(t, filepath.Join(root, "cgroup.controllers"), "cpu memory\n")
	writeCgroupTestFile(t, filepath.Join(scope, "cgroup.procs"), "101\n102\n")
	writeCgroupTestFile(t, filepath.Join(scope, "cgroup.events"), "populated 1\n")
	writeCgroupTestFile(t, filepath.Join(scope, "cgroup.kill"), "")
	if err := ValidateCgroupV2ScopeV1(root, scope); err != nil {
		t.Fatal(err)
	}
	if err := VerifyCgroupMembershipV1(scope, []int{101, 102}); err != nil {
		t.Fatal(err)
	}
	if err := VerifyCgroupMembershipV1(scope, []int{103}); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("expected escaped descendant rejection, got %v", err)
	}
	if err := VerifyCgroupEmptyV1(scope); err == nil || !strings.Contains(err.Error(), "still contains") {
		t.Fatalf("expected non-empty teardown rejection, got %v", err)
	}
	writeCgroupTestFile(t, filepath.Join(scope, "cgroup.procs"), "")
	if err := VerifyCgroupEmptyV1(scope); err != nil {
		t.Fatal(err)
	}
}

func TestCgroupV2ScopeRejectsRootAndSymlink(t *testing.T) {
	root := t.TempDir()
	writeCgroupTestFile(t, filepath.Join(root, "cgroup.controllers"), "cpu\n")
	if err := ValidateCgroupV2ScopeV1(root, root); err == nil {
		t.Fatal("accepted broad cgroup root as an invocation scope")
	}
	scope := filepath.Join(root, "scope")
	if err := os.Mkdir(scope, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCgroupTestFile(t, filepath.Join(scope, "real.procs"), "")
	if err := os.Symlink(filepath.Join(scope, "real.procs"), filepath.Join(scope, "cgroup.procs")); err != nil {
		t.Fatal(err)
	}
	writeCgroupTestFile(t, filepath.Join(scope, "cgroup.events"), "")
	writeCgroupTestFile(t, filepath.Join(scope, "cgroup.kill"), "")
	if err := ValidateCgroupV2ScopeV1(root, scope); err == nil || !strings.Contains(err.Error(), "non-symlink") {
		t.Fatalf("expected symlink control rejection, got %v", err)
	}
}

func writeCgroupTestFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

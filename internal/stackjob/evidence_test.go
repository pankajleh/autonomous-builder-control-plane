package stackjob

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestTestsAreCountedFromTheToolsJUnitReportsWithinLimits(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, map[string]string{
		"target/surefire-reports/TEST-a.AppTest.xml":        `<?xml version="1.0"?><testsuite name="a" tests="5" failures="1" errors="1" skipped="1"><testcase/></testsuite>`,
		"core/target/surefire-reports/TEST-b.CoreTest.xml":  `<testsuite tests="3" failures="0" errors="0" skipped="0"/>`,
		"build/test-results/test/TEST-c.xml":                `<testsuite tests="2" skipped="0" failures="0" errors="0"/>`,
		"node_modules/x/target/surefire-reports/TEST-x.xml": `<testsuite tests="100" failures="100"/>`,
		"target/surefire-reports/not-a-report.xml":          `<testsuite tests="100"/>`,
		"target/surefire-reports/TEST-broken.xml":           `<testsuite tests="4"`,
		"elsewhere/TEST-z.xml":                              `<testsuite tests="9"/>`,
		".abcp/pytest-0.xml":                                `<testsuites><testsuite name="pytest" errors="0" failures="1" skipped="2" tests="10"><testsuite tests="99"/></testsuite></testsuites>`,
	})
	passed, failed, skipped, ok := Counts(dir, filepath.Join(dir, ".abcp", "pytest-0.xml"))
	// Surefire 5 (2 failed, 1 skipped) + 3 + Gradle 2 + pytest 10 (1 failed, 2 skipped); the rest are not reports.
	if !ok || passed != 2+3+2+7 || failed != 3 || skipped != 3 {
		t.Fatalf("counts = %d passed, %d failed, %d skipped, %v", passed, failed, skipped, ok)
	}
	if _, _, _, ok := Counts(t.TempDir()); ok {
		t.Fatal("counts without reports")
	}
	// An entity cannot grow a report: Go's reader refuses the undeclared one, and the report counts for nothing.
	bomb := t.TempDir()
	write(t, bomb, map[string]string{"target/surefire-reports/TEST-bomb.xml": `<?xml version="1.0"?><!DOCTYPE t [<!ENTITY a "aaaaaaaaaa"><!ENTITY b "&a;&a;&a;&a;&a;&a;&a;&a;&a;&a;">]><testsuite tests="&b;"/>`})
	if p, f, s, _ := Counts(bomb); p != 0 || f != 0 || s != 0 {
		t.Fatalf("entity report counted: %d %d %d", p, f, s)
	}
	big := t.TempDir()
	write(t, big, map[string]string{"target/surefire-reports/TEST-big.xml": `<testsuite tests="1"/>` + strings.Repeat(" ", maxReportBytes)})
	if _, _, _, ok := Counts(big); ok {
		t.Fatal("a report over its limit was read")
	}
}

func TestOutputKeepsItsLastLinesAndHidesSecrets(t *testing.T) {
	out := newTail(1 << 10)
	for i := 0; i < 500; i++ {
		out.Write([]byte("line " + strings.Repeat("x", 10) + "\n"))
	}
	out.Write([]byte("DATABASE_PASSWORD=hunter2 and token: abc123def\nAWS AKIAABCDEFGHIJKLMNOP ghp_" + strings.Repeat("a", 36) + "\n"))
	out.Write([]byte("https://user:s3cret@registry.example.com/x sk-" + strings.Repeat("b", 30) + "\n-----BEGIN RSA PRIVATE KEY-----\nMIIE\n-----END RSA PRIVATE KEY-----\n"))
	text := out.Lines(8)
	// The last eight lines, the private key's three of them masked as one.
	if strings.Count(text, "line ") != 2 || strings.Count(text, "\n") != 5 {
		t.Fatalf("lines kept:\n%s", text)
	}
	for _, secret := range []string{"hunter2", "abc123def", "AKIAABCDEFGHIJKLMNOP", "ghp_aaaa", "s3cret", "sk-bbbb", "MIIE"} {
		if strings.Contains(text, secret) {
			t.Errorf("%q shown:\n%s", secret, text)
		}
	}
	if !strings.Contains(text, "DATABASE_PASSWORD=[hidden]") || !strings.Contains(text, "https://[hidden]@registry.example.com/x") {
		t.Fatalf("masked text:\n%s", text)
	}
	if Mask("BUILD SUCCESS in 4s, 12 tests") != "BUILD SUCCESS in 4s, 12 tests" {
		t.Fatal("plain output changed")
	}
}

func TestTheCopyTakesOnlyFilesAndFoldersAndNeverGitOrALink(t *testing.T) {
	src := t.TempDir()
	write(t, src, map[string]string{"pom.xml": "<project/>", "src/App.java": "class App {}", ".git/config": "[remote] url = https://x:token@github.com", "mvnw": "#!/bin/sh"})
	os.Chmod(filepath.Join(src, "mvnw"), 0o755)
	outside := t.TempDir()
	write(t, outside, map[string]string{"secret.txt": "not to be read"})
	os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(src, "linked.txt"))
	os.Symlink(outside, filepath.Join(src, "linked-folder"))
	syscall.Mkfifo(filepath.Join(src, "fifo"), 0o600)
	dst := filepath.Join(t.TempDir(), "work")
	if err := copySource(src, dst); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{".git", "linked.txt", "linked-folder", "fifo"} {
		if _, err := os.Lstat(filepath.Join(dst, gone)); err == nil {
			t.Errorf("%s copied", gone)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(dst, "src", "App.java")); string(data) != "class App {}" {
		t.Fatal("a file was not copied")
	}
	if info, _ := os.Stat(filepath.Join(dst, "mvnw")); info.Mode().Perm() != 0o755 {
		t.Fatalf("mvnw mode %v", info.Mode())
	}
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(src, link)
	if copySource(link, filepath.Join(t.TempDir(), "w")) == nil {
		t.Fatal("a linked source was copied")
	}
	if copySource(src, dst) == nil {
		t.Fatal("copied over an existing folder")
	}
	if folderSize(dst, 1<<20) != int64(len("<project/>")+len("class App {}")+len("#!/bin/sh")) {
		t.Fatalf("size = %d", folderSize(dst, 1<<20))
	}
}

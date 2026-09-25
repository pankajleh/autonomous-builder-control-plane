package runadmission

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// readDirAt must enumerate the protected admissions directory, which is what
// bindingForRunID relies on to locate a durable binding by run id.
func TestReadDirAtEnumeratesAdmissions(t *testing.T) {
	root := t.TempDir()
	admissions := filepath.Join(root, "admissions")
	if err := os.Mkdir(admissions, 0o700); err != nil {
		t.Fatal(err)
	}
	rootFD, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(rootFD)
	fd, err := openChildDirectory(rootFD, "admissions", false)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)

	for _, name := range []string{"abc.json", "def.binding.json", "ghi.launch.json", "jkl.lock"} {
		if err := os.WriteFile(filepath.Join(admissions, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	names, err := readDirAt(fd)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, name := range names {
		got[name] = true
	}
	for _, want := range []string{"abc.json", "def.binding.json", "ghi.launch.json", "jkl.lock"} {
		if !got[want] {
			t.Fatalf("readDirAt did not return %q; got %v", want, names)
		}
	}
}

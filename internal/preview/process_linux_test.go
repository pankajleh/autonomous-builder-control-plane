//go:build linux

package preview

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestExecuteProcessHelper(t *testing.T) {
	switch os.Getenv("ABCP_PREVIEW_PROCESS_HELPER") {
	case "parent", "exited-parent":
		child := exec.Command(os.Args[0], "-test.run=^TestExecuteProcessHelper$")
		child.Env = []string{"ABCP_PREVIEW_PROCESS_HELPER=child", "ABCP_PREVIEW_PROCESS_READY=" + os.Getenv("ABCP_PREVIEW_PROCESS_READY")}
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		if os.Getenv("ABCP_PREVIEW_PROCESS_HELPER") == "exited-parent" {
			os.Exit(0)
		}
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "child":
		if err := os.WriteFile(os.Getenv("ABCP_PREVIEW_PROCESS_READY"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(2)
		}
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
}
func TestExecuteTerminatesDescendantsAndBoundsInheritedPipes(t *testing.T) {
	for _, mode := range []string{"parent", "exited-parent"} {
		t.Run(mode, func(t *testing.T) {
			ready := filepath.Join(t.TempDir(), "ready")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := execute(ctx, os.Args[0], []string{"ABCP_PREVIEW_PROCESS_HELPER=" + mode, "ABCP_PREVIEW_PROCESS_READY=" + ready}, "-test.run=^TestExecuteProcessHelper$")
				done <- err
			}()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("descendant did not start")
				}
				time.Sleep(5 * time.Millisecond)
			}
			started := time.Now()
			if mode == "parent" {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("cancellation reported success")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("descendant output pipe outlived cancellation")
			}
			if time.Since(started) >= 2*time.Second {
				t.Fatal("cancellation exceeded pipe bound")
			}
			data, err := os.ReadFile(ready)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(data))
			if err != nil || pid <= 0 {
				t.Fatal("invalid descendant pid", err)
			}
			deadline = time.Now().Add(time.Second)
			for {
				stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
				if os.IsNotExist(err) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				tail := string(stat)[strings.LastIndex(string(stat), ")")+1:]
				if strings.HasPrefix(tail, " Z ") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("descendant remains running")
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}

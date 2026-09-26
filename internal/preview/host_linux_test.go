//go:build linux

package preview

import (
	"context"
	"os"
	"testing"
	"time"
)

// Opt-in acceptance uses only a pre-existing digest-pinned local image. The
// normal suite never depends on Docker and never pulls an image. Unsupported
// hosts are a supported outcome only with capability false and clean teardown.
func TestLocalDockerHostIsolation(t *testing.T) {
	image := os.Getenv("ABCP_PREVIEW_DOCKER_SMOKE_IMAGE")
	if image == "" {
		t.Skip("set ABCP_PREVIEW_DOCKER_SMOKE_IMAGE to a local pinned offline probe image")
	}
	p := testProfile()
	p.Services[0].Image = image
	p.Services[0].User = "65534:65534"
	p.Services[0].Environment = nil
	p.Services[0].StartArgv = []string{"/usr/sbin/httpd", "-f", "-p", "8080", "-h", "/scratch"}
	if p.Validate() != nil {
		t.Fatal("invalid protected smoke profile")
	}
	d := newDocker(t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := d.Reconcile(ctx); err != nil {
		t.Fatal("Docker host is unavailable for opt-in smoke")
	}
	defer func() {
		if err := d.Reconcile(context.Background()); err != nil {
			t.Error("orphan cleanup failed")
		}
	}()
	proved := d.prove(ctx, p)
	if proved != d.Available(p) {
		t.Fatal("capability disagrees with isolation proof")
	}
	if proved {
		t.Log("INTERNAL_ONLY egress block and explicit host-loopback presentation proved")
	} else {
		t.Log("preview_runtime=false: host/profile isolation or presentation unavailable")
	}
	d.mu.Lock()
	groups := len(d.groups)
	d.mu.Unlock()
	if groups != 0 {
		t.Fatal("smoke route remains live")
	}
}

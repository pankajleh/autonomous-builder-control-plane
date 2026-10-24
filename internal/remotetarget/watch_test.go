package remotetarget

import (
	"context"
	"errors"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/appwatch"
)

// BF-07e: a sweep looks at each site its owner keeps running on a confirmed target, once, from the target's own
// address: a Node app at /health, a static site at /. Busy sites are not looked at, and paused or never-deployed sites
// and the sites of a target not confirmed are left out.
func TestASweepLooksAtEachRunningSiteOnceFromItsTargetsAddress(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m, err := New(dir, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	m.allow = func(net.IP) bool { return true }
	var mu sync.Mutex
	asked := []string{}
	m.fetch = func(_ context.Context, ip net.IP, host, path string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, ip.String()+" "+host+path)
		if strings.HasPrefix(host, "down.") {
			return "", errors.New("status")
		}
		return "sha", nil
	}
	site := func(name, kind, state, release, operation string) siteRecord {
		return siteRecord{Name: name, Host: name + ".example.com", Kind: kind, State: state, Release: release, Operation: operation}
	}
	for _, r := range []record{
		{SchemaVersion: 1, Key: "srv-host1", Address: "203.0.113.7", State: stateConfirmed, CreatedAt: "2026-10-24T10:00:00Z", Sites: []siteRecord{
			site("crm", kindNode, siteLive, "r1", ""), site("down", kindNode, siteFailed, "r1", ""), site("page", "", siteLive, "r2", ""),
			site("paused", kindNode, siteStopped, "r1", ""), site("busy", kindNode, siteLive, "r1", "op-0123456789abcdef"),
			site("new", kindNode, siteDeploying, "", ""), site("going", "", siteRemoving, "r2", "")}},
		{SchemaVersion: 1, Key: "srv-pending", Address: "203.0.113.8", State: "pending", CreatedAt: "2026-10-24T11:00:00Z", Sites: []siteRecord{site("other", kindNode, siteLive, "r1", "")}},
	} {
		if err := m.write(r); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := m.HealthObservations(context.Background())
	if !ok {
		t.Fatal("the sweep could not look")
	}
	said := []string{}
	for _, o := range got {
		if o.App.Kind != appwatch.KindSite || o.App.Target != "srv-host1" {
			t.Fatal("observation", o)
		}
		said = append(said, o.App.Site+" "+o.State)
	}
	sort.Strings(said)
	if strings.Join(said, ", ") != "busy busy, crm healthy, down unwell, going busy, page healthy" {
		t.Fatal("observations:", said)
	}
	sort.Strings(asked)
	if strings.Join(asked, ", ") != "203.0.113.7 crm.example.com/health, 203.0.113.7 down.example.com/health, 203.0.113.7 page.example.com/" {
		t.Fatal("asked:", asked)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := m.HealthObservations(ctx); ok {
		t.Fatal("a sweep cut short was taken as complete")
	}
}

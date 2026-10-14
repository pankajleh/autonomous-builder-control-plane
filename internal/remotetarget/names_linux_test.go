//go:build linux

package remotetarget

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

func TestANameIsLookedUpOnceAndSaysWhetherItPointsAtTheTarget(t *testing.T) {
	f := newDeployFixture(t)
	answers := map[string][]net.IP{
		testHost:              {net.ParseIP("13.232.1.2")},
		"shop.example.com":    {net.ParseIP("13.232.9.9")},
		"both.example.com":    {net.ParseIP("2001:db8::7"), net.ParseIP("13.232.1.2"), net.ParseIP("13.232.1.2")},
		"nothing.example.com": nil,
	}
	f.m.resolve = func(_ context.Context, host string) ([]net.IP, error) {
		if ips, ok := answers[host]; ok && ips != nil {
			return ips, nil
		}
		if host == "13.232.1.2" {
			return []net.IP{net.ParseIP(host)}, nil
		}
		return nil, errors.New("no such host")
	}
	f.m.remote = func(context.Context, record) (remote, string) {
		t.Fatal("looking a name up opens no session")
		return nil, ""
	}
	for _, c := range []struct {
		host      string
		points    bool
		addresses string
	}{
		{testHost, true, "13.232.1.2"},
		{"shop.example.com", false, "13.232.9.9"},
		{"both.example.com", true, "13.232.1.2,2001:db8::7"},
		{"nothing.example.com", false, ""},
	} {
		name, err := f.m.Name(context.Background(), f.key, c.host)
		if err != nil || name.SchemaVersion != serviceapi.NameSchemaV1 || name.Target != f.key || name.Host != c.host || name.Address != "13.232.1.2" ||
			name.Points != c.points || strings.Join(name.Addresses, ",") != c.addresses || name.CheckedAt != "2026-10-14T01:00:00Z" {
			t.Fatalf("%s = %+v %v", c.host, name, err)
		}
	}
	for _, bad := range []string{"13.232.1.2", "localhost", "Shop.example.com", "a b.example", "$(id).example"} {
		if _, err := f.m.Name(context.Background(), f.key, bad); !errors.Is(err, serviceapi.ErrTargetInvalid) {
			t.Fatalf("%q = %v", bad, err)
		}
	}
	if _, err := f.m.Name(context.Background(), "srv-000000000000", testHost); !errors.Is(err, serviceapi.ErrTargetUnknown) {
		t.Fatalf("an unknown target = %v", err)
	}
	// Nothing is recorded.
	if sites, _ := f.m.Sites(context.Background(), f.key); len(sites.Sites) != 0 {
		t.Fatalf("sites = %+v", sites)
	}
}

func TestASiteIsCheckedAgainWithoutASessionAndBecomesLiveOnceItsNamePoints(t *testing.T) {
	f := newDeployFixture(t)
	digest := f.put(map[string]string{"index.html": "blog"})
	// The name does not point at the server yet: every step runs, the check fails, the site with it.
	if _, op := f.deploy(digest, "blog", "blog.example.com", "req-blog-0001"); op.State != opFailed || op.Steps[len(op.Steps)-1].Detail != "dns" {
		t.Fatalf("deploy = %+v", op)
	}
	if f.siteState("blog") != siteFailed {
		t.Fatalf("blog = %s", f.siteState("blog"))
	}
	sessions := 0
	opened := f.m.remote
	f.m.remote = func(ctx context.Context, r record) (remote, string) { sessions++; return opened(ctx, r) }

	// Checked again while the name still points elsewhere: it stays failed, with the new last check.
	f.now = f.now.Add(time.Minute)
	op := f.finished(f.m.CheckSite(context.Background(), f.key, "blog", serviceapi.OperationRequestV1{RequestID: "req-check-0001"}))
	if op.State != opFailed || op.Kind != "check_site" || len(op.Steps) != 1 || op.Steps[0].Name != "check" || op.Steps[0].Detail != "dns" {
		t.Fatalf("check = %+v", op)
	}
	if f.siteState("blog") != siteFailed {
		t.Fatalf("still elsewhere: %s", f.siteState("blog"))
	}

	// The record is added: the same site answers, and becomes live.
	resolve := f.m.resolve
	f.m.resolve = func(ctx context.Context, host string) ([]net.IP, error) {
		if host == "blog.example.com" {
			return []net.IP{net.ParseIP("13.232.1.2")}, nil
		}
		return resolve(ctx, host)
	}
	f.now = f.now.Add(time.Minute)
	op = f.finished(f.m.CheckSite(context.Background(), f.key, "blog", serviceapi.OperationRequestV1{RequestID: "req-check-0002"}))
	if op.State != opSucceeded || op.Steps[0].State != "done" || op.Steps[0].Detail != "answers with this release" {
		t.Fatalf("check = %+v", op)
	}
	sites, _ := f.m.Sites(context.Background(), f.key)
	if len(sites.Sites) != 1 || sites.Sites[0].State != siteLive || sites.Sites[0].Release != digest || sites.Sites[0].LastCheck == nil ||
		!sites.Sites[0].LastCheck.OK || sites.Sites[0].LastCheck.At != "2026-10-14T01:02:00Z" {
		t.Fatalf("sites = %+v", sites)
	}
	if sessions != 0 {
		t.Fatalf("a check opened %d sessions", sessions)
	}
	if again, err := f.m.CheckSite(context.Background(), f.key, "blog", serviceapi.OperationRequestV1{RequestID: "req-check-0002"}); err != nil ||
		again.OperationID != op.OperationID {
		t.Fatalf("the same request is one check: %+v %v", again, err)
	}
	// A live site checked again stays live.
	if op := f.finished(f.m.CheckSite(context.Background(), f.key, "blog", serviceapi.OperationRequestV1{RequestID: "req-check-0003"})); op.State != opSucceeded ||
		f.siteState("blog") != siteLive {
		t.Fatalf("live = %+v %s", op, f.siteState("blog"))
	}

	for _, c := range []struct {
		site, request string
		want          error
	}{
		{"shop", "req-check-0004", serviceapi.ErrSiteUnknown},
		{"Blog", "req-check-0005", serviceapi.ErrTargetInvalid},
		{"blog", "x", serviceapi.ErrTargetInvalid},
	} {
		if _, err := f.m.CheckSite(context.Background(), f.key, c.site, serviceapi.OperationRequestV1{RequestID: c.request}); !errors.Is(err, c.want) {
			t.Fatalf("%s %s = %v", c.site, c.request, err)
		}
	}
	// A request id already used for another operation is refused.
	if _, err := f.m.CheckSite(context.Background(), f.key, "blog", serviceapi.OperationRequestV1{RequestID: "req-blog-0001"}); err == nil {
		t.Fatal("a check reusing a deploy's request id")
	}
}

func TestAStoppedOrUnknownSiteIsNotCheckedAgain(t *testing.T) {
	f := newDeployFixture(t)
	f.deploy(f.putNode("one"), "crm", testHost, "req-crm-first")
	f.finished(f.m.StopSite(context.Background(), f.key, "crm", serviceapi.OperationRequestV1{RequestID: "req-stop-001"}))
	if _, err := f.m.CheckSite(context.Background(), f.key, "crm", serviceapi.OperationRequestV1{RequestID: "req-check-001"}); !errors.Is(err, serviceapi.ErrSiteStopped) {
		t.Fatalf("a stopped app = %v", err)
	}
	f.finished(f.m.StartSite(context.Background(), f.key, "crm", serviceapi.OperationRequestV1{RequestID: "req-start-001"}))
	// A started Node app is checked on /health.
	if op := f.finished(f.m.CheckSite(context.Background(), f.key, "crm", serviceapi.OperationRequestV1{RequestID: "req-check-002"})); op.State != opSucceeded ||
		op.Steps[0].Detail != "answers /health" {
		t.Fatalf("node = %+v", op)
	}
	r, _ := f.m.read(f.key)
	s, _ := findSite(r, "crm")
	s.State = siteUnknown
	f.m.setSite(&r, s)
	if err := f.m.write(r); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.CheckSite(context.Background(), f.key, "crm", serviceapi.OperationRequestV1{RequestID: "req-check-003"}); !errors.Is(err, serviceapi.ErrTargetInvalid) {
		t.Fatalf("an unknown site = %v", err)
	}
}

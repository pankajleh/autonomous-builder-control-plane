package preview

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEgressAllowsOnlyOneShopifyStoreLabelOnPort443(t *testing.T) {
	rules := []string{EgressShopifyStores}
	for host, want := range map[string]bool{
		"chai-corner.myshopify.com": true, "SHOP1.MyShopify.COM": true, "a.myshopify.com": true,
		"myshopify.com": false, "a.b.myshopify.com": false, "-a.myshopify.com": false, "a-.myshopify.com": false,
		"a.myshopify.com.evil.test": false, "amyshopify.com": false, "shop.myshopify.co": false, "example.com": false,
		"a_b.myshopify.com": false, "1.2.3.4": false, "::1": false, strings.Repeat("a", 64) + ".myshopify.com": false,
	} {
		if got := egressAllowed(rules, host, "443"); got != want {
			t.Errorf("%q: allowed %v, want %v", host, got, want)
		}
	}
	for _, port := range []string{"80", "8443", "", "0443"} {
		if egressAllowed(rules, "shop.myshopify.com", port) {
			t.Errorf("port %q allowed", port)
		}
	}
	if egressAllowed(nil, "shop.myshopify.com", "443") || egressAllowed([]string{"*.example.com:443"}, "shop.myshopify.com", "443") {
		t.Fatal("a name was allowed without its rule")
	}
}

func TestEgressConnectsOnlyToPublicAddresses(t *testing.T) {
	for address, want := range map[string]bool{
		"23.227.38.65": true, "2620:127:f00f::1": true, "8.8.8.8": true, "::ffff:23.227.38.65": true,
		"10.213.0.1": false, "10.0.0.1": false, "172.16.0.1": false, "192.168.1.1": false, "127.0.0.1": false, "169.254.169.254": false,
		"100.64.0.1": false, "198.18.0.1": false, "192.0.0.1": false, "0.0.0.0": false, "224.0.0.1": false, "255.255.255.255": false,
		"::1": false, "fe80::1": false, "fd00::1": false, "2001:db8::1": false, "64:ff9b::a00:1": false, "::ffff:10.0.0.1": false,
	} {
		if got := publicAddress(netip.MustParseAddr(address)); got != want {
			t.Errorf("%s: public %v, want %v", address, got, want)
		}
	}
}

// egressFixture is a proxy on loopback whose names resolve as the test says and whose tunnels all reach one local
// echo server, recording the address each would have reached.
type egressFixture struct {
	proxy    *egressProxy
	mu       sync.Mutex
	dialed   []string
	lookedUp []string
}

func newEgressFixture(t *testing.T, answers map[string][]string) *egressFixture {
	t.Helper()
	echo, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { echo.Close() })
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &egressFixture{proxy: newEgressProxy(listener, []string{EgressShopifyStores})}
	f.proxy.lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		f.mu.Lock()
		f.lookedUp = append(f.lookedUp, host)
		f.mu.Unlock()
		out := []netip.Addr{}
		for _, a := range answers[host] {
			if a == "error" {
				return nil, errors.New("no such host")
			}
			out = append(out, netip.MustParseAddr(a))
		}
		return out, nil
	}
	f.proxy.dial = func(ctx context.Context, address string) (net.Conn, error) {
		f.mu.Lock()
		f.dialed = append(f.dialed, address)
		f.mu.Unlock()
		if strings.HasPrefix(address, "203.0.113.") {
			return nil, errors.New("connection refused")
		}
		var d net.Dialer
		return d.DialContext(ctx, "tcp4", echo.Addr().String())
	}
	f.proxy.start()
	t.Cleanup(func() { f.proxy.Close() })
	return f
}

func (f *egressFixture) connect(t *testing.T, head string) (net.Conn, string) {
	t.Helper()
	c, err := net.Dial("tcp4", f.proxy.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, head); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(c)
	status, _ := reader.ReadString('\n')
	for {
		line, err := reader.ReadString('\n')
		if err != nil || line == "\r\n" {
			break
		}
	}
	return &bufferedConn{Conn: c, reader: reader}, strings.TrimSpace(status)
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.reader.Read(p) }

func connectLine(target string) string {
	return "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n\r\n"
}

func TestEgressProxyTunnelsToAnAllowedStoreOnly(t *testing.T) {
	f := newEgressFixture(t, map[string][]string{
		"chai.myshopify.com":    {"23.227.38.65"},
		"mixed.myshopify.com":   {"10.0.0.7", "169.254.169.254", "23.227.38.66"},
		"private.myshopify.com": {"127.0.0.1", "10.213.0.1", "100.64.0.1"},
		"gone.myshopify.com":    {"error"},
		"down.myshopify.com":    {"203.0.113.9"},
		"retry.myshopify.com":   {"203.0.113.10", "23.227.38.67"},
	})
	c, status := f.connect(t, connectLine("chai.myshopify.com:443"))
	if status != "HTTP/1.1 200 Connection Established" {
		t.Fatalf("status %q", status)
	}
	io.WriteString(c, "client hello")
	got := make([]byte, len("client hello"))
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "client hello" {
		t.Fatalf("tunnel carried %q, %v", got, err)
	}
	for target, want := range map[string]string{
		"MIXED.MyShopify.com:443": "HTTP/1.1 200 Connection Established", "retry.myshopify.com:443": "HTTP/1.1 200 Connection Established",
		"private.myshopify.com:443": "HTTP/1.1 502 Bad Gateway", "gone.myshopify.com:443": "HTTP/1.1 502 Bad Gateway",
		"down.myshopify.com:443": "HTTP/1.1 502 Bad Gateway", "example.com:443": "HTTP/1.1 403 Forbidden",
		"chai.myshopify.com:80": "HTTP/1.1 403 Forbidden", "a.b.myshopify.com:443": "HTTP/1.1 403 Forbidden",
		"23.227.38.65:443": "HTTP/1.1 403 Forbidden", "[::1]:443": "HTTP/1.1 403 Forbidden", "chai.myshopify.com": "HTTP/1.1 403 Forbidden",
	} {
		if _, status := f.connect(t, connectLine(target)); status != want {
			t.Errorf("%s: %q, want %q", target, status, want)
		}
	}
	for head, want := range map[string]string{
		"GET http://chai.myshopify.com/ HTTP/1.1\r\nHost: chai.myshopify.com\r\n\r\n":             "HTTP/1.1 405 Method Not Allowed",
		"CONNECT chai.myshopify.com:443 HTTP/1.1\r\nX: " + strings.Repeat("a", 9000) + "\r\n\r\n": "HTTP/1.1 400 Bad Request",
		"not http at all\r\n\r\n": "HTTP/1.1 400 Bad Request",
	} {
		if _, status := f.connect(t, head); status != want {
			t.Errorf("%.40q: %q, want %q", head, status, want)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// Only allowed names were looked up, in lower case, and only public addresses from their answers were dialled.
	for _, host := range f.lookedUp {
		if !strings.HasSuffix(host, ".myshopify.com") || strings.ToLower(host) != host || strings.Count(host, ".") != 2 {
			t.Errorf("looked up %q", host)
		}
	}
	want := map[string]bool{"23.227.38.65:443": true, "23.227.38.66:443": true, "203.0.113.9:443": true, "203.0.113.10:443": true, "23.227.38.67:443": true}
	for _, address := range f.dialed {
		if !want[address] {
			t.Errorf("dialled %s", address)
		}
	}
	if len(f.dialed) != 5 {
		t.Errorf("dialled %v", f.dialed)
	}
	if counts := f.proxy.Counts(); counts.Opened != 3 || counts.Refused != 12 {
		t.Errorf("counts %+v", counts)
	}
}

func TestEgressProxySendsWhatFollowsTheRequestAndCloseEndsTunnels(t *testing.T) {
	f := newEgressFixture(t, map[string][]string{"chai.myshopify.com": {"23.227.38.65"}})
	// The app's TLS hello often arrives in the same packet as its CONNECT.
	c, status := f.connect(t, connectLine("chai.myshopify.com:443")+"early bytes")
	if status != "HTTP/1.1 200 Connection Established" {
		t.Fatalf("status %q", status)
	}
	got := make([]byte, len("early bytes"))
	if _, err := io.ReadFull(c, got); err != nil || string(got) != "early bytes" {
		t.Fatalf("got %q, %v", got, err)
	}
	closed := make(chan error, 1)
	go func() { closed <- f.proxy.Close() }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close waited for an open tunnel")
	}
	if n, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatalf("the tunnel stayed open after Close (%d bytes)", n)
	}
}

func TestEgressProxyRefusesBeyondItsConcurrentTunnels(t *testing.T) {
	f := newEgressFixture(t, map[string][]string{"chai.myshopify.com": {"23.227.38.65"}})
	for i := 0; i < egressMaxConcurrent; i++ {
		if _, status := f.connect(t, connectLine("chai.myshopify.com:443")); status != "HTTP/1.1 200 Connection Established" {
			t.Fatalf("tunnel %d: %q", i, status)
		}
	}
	c, err := net.Dial("tcp4", f.proxy.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	io.WriteString(c, connectLine("chai.myshopify.com:443"))
	if n, err := c.Read(make([]byte, 64)); err == nil {
		t.Fatalf("a tunnel beyond the limit was answered (%d bytes)", n)
	}
	if counts := f.proxy.Counts(); counts.Opened != egressMaxConcurrent || counts.Refused != 1 {
		t.Errorf("counts %+v", counts)
	}
}

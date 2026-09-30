package preview

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The egress proxy (Repo C design note A5 §8) is a hosted group's only way out: an HTTP CONNECT proxy on the group
// network's gateway, port EgressPort, which the owner's host rule lets the preview range reach. It accepts a tunnel only
// to a name the profile's Egress allows, resolves that name itself and connects only to a public address, never to one
// the app names. It sees names, never content: TLS stays end to end between the app and the store. It logs nothing
// itself; it counts what it accepted and refused.

const (
	egressHeadLimit     = 8 << 10
	egressHeadTimeout   = 10 * time.Second
	egressDialTimeout   = 10 * time.Second
	egressMaxConcurrent = 32
	egressMaxLifetime   = 30 * time.Minute
)

// shopifyStore is one DNS label under myshopify.com, as Shopify names stores.
var shopifyStore = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.myshopify\.com$`)

// egressAllowed reports whether rules allow a tunnel to host:port. The only rule v1 knows is EgressShopifyStores.
func egressAllowed(rules []string, host, port string) bool {
	host = strings.ToLower(host)
	if _, err := netip.ParseAddr(host); err != nil && port == "443" {
		for _, rule := range rules {
			if rule == EgressShopifyStores && shopifyStore.MatchString(host) {
				return true
			}
		}
	}
	return false
}

// nonPublic are ranges that IsGlobalUnicast does not rule out but no store lives in.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), // shared address space (carrier NAT)
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("2001:db8::/32"), // documentation
	netip.MustParsePrefix("64:ff9b::/96"),  // NAT64, which could reach private IPv4 addresses
}

// publicAddress reports an address the proxy may connect to: global unicast and not private, loopback, link-local,
// or one of the ranges above.
func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, prefix := range nonPublic {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

// EgressCounts is what one proxy has done: tunnels opened, and requests refused.
type EgressCounts struct{ Opened, Refused int64 }

type egressProxy struct {
	listener net.Listener
	rules    []string
	lookup   func(ctx context.Context, host string) ([]netip.Addr, error)
	dial     func(ctx context.Context, address string) (net.Conn, error)
	slots    chan struct{}
	opened   atomic.Int64
	refused  atomic.Int64
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
}

// newEgressProxy serves rules on listener until Close.
func newEgressProxy(listener net.Listener, rules []string) *egressProxy {
	resolver := &net.Resolver{}
	dialer := &net.Dialer{Timeout: egressDialTimeout}
	e := &egressProxy{
		listener: listener, rules: append([]string(nil), rules...), slots: make(chan struct{}, egressMaxConcurrent), conns: map[net.Conn]struct{}{},
		lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return resolver.LookupNetIP(ctx, "ip", host)
		},
		dial: func(ctx context.Context, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, "tcp", address)
		},
	}
	e.ctx, e.cancel = context.WithCancel(context.Background())
	return e
}

func (e *egressProxy) start() {
	e.wg.Add(1)
	go e.serve()
}

func (e *egressProxy) Counts() EgressCounts {
	return EgressCounts{Opened: e.opened.Load(), Refused: e.refused.Load()}
}

// Close stops listening and ends every tunnel.
func (e *egressProxy) Close() error {
	e.cancel()
	err := e.listener.Close()
	e.mu.Lock()
	for c := range e.conns {
		c.Close()
	}
	e.mu.Unlock()
	e.wg.Wait()
	return err
}

func (e *egressProxy) track(c net.Conn, add bool) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !add {
		delete(e.conns, c)
		return true
	}
	if e.ctx.Err() != nil {
		return false
	}
	e.conns[c] = struct{}{}
	return true
}

func (e *egressProxy) serve() {
	defer e.wg.Done()
	for {
		c, err := e.listener.Accept()
		if err != nil {
			if e.ctx.Err() != nil {
				return
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		select {
		case e.slots <- struct{}{}:
		default:
			e.refused.Add(1)
			c.Close()
			continue
		}
		if !e.track(c, true) {
			<-e.slots
			c.Close()
			return
		}
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			defer func() { <-e.slots }()
			defer e.track(c, false)
			defer c.Close()
			e.handle(c)
		}()
	}
}

func (e *egressProxy) refuse(c net.Conn, status int) {
	e.refused.Add(1)
	io.WriteString(c, "HTTP/1.1 "+strconv.Itoa(status)+" "+http.StatusText(status)+"\r\nConnection: close\r\nContent-Length: 0\r\n\r\n")
}

func (e *egressProxy) handle(c net.Conn) {
	c.SetDeadline(time.Now().Add(egressHeadTimeout))
	reader := bufio.NewReaderSize(io.LimitReader(c, egressHeadLimit), 4096)
	request, err := http.ReadRequest(reader)
	if err != nil {
		e.refuse(c, http.StatusBadRequest)
		return
	}
	if request.Method != http.MethodConnect {
		e.refuse(c, http.StatusMethodNotAllowed)
		return
	}
	host, port, err := net.SplitHostPort(request.Host)
	if err != nil || !egressAllowed(e.rules, host, port) {
		e.refuse(c, http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(e.ctx, egressDialTimeout)
	defer cancel()
	// The name is resolved once, here, and the tunnel goes to an address from that answer: never one the app gives.
	addresses, err := e.lookup(ctx, strings.ToLower(host))
	var upstream net.Conn
	for _, ip := range addresses {
		if err != nil || !publicAddress(ip) {
			continue
		}
		if conn, dialErr := e.dial(ctx, netip.AddrPortFrom(ip.Unmap(), 443).String()); dialErr == nil {
			upstream = conn
			break
		}
	}
	if upstream == nil {
		e.refuse(c, http.StatusBadGateway)
		return
	}
	defer upstream.Close()
	if !e.track(upstream, true) {
		return
	}
	defer e.track(upstream, false)
	e.opened.Add(1)
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	// Whatever the app sent after the request line (its TLS hello) goes first; then both ways until either side ends.
	lifetime := time.Now().Add(egressMaxLifetime)
	c.SetDeadline(lifetime)
	upstream.SetDeadline(lifetime)
	if buffered := reader.Buffered(); buffered > 0 {
		head, _ := reader.Peek(buffered)
		if _, err := upstream.Write(head); err != nil {
			return
		}
	}
	done := make(chan struct{}, 2)
	go func() { io.Copy(upstream, c); upstream.Close(); done <- struct{}{} }()
	go func() { io.Copy(c, upstream); c.Close(); done <- struct{}{} }()
	<-done
	<-done
}

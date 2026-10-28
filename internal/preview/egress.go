package preview

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
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

// The app relay (Repo C design note BF08_NO_PAGE_OPEN §14, BF-08b): with EgressAutobuildRelay, the proxy also takes a
// plain GET or POST for a path under /__autobuild/ and sends it to the app relay ABCP is started with, saying which group
// asks. The listener is the group's own, on its own network's gateway, so the caller is always that group; the app holds
// no secret, and every header that could claim another identity is dropped.
const (
	relayBodyLimit   = 64 << 10
	relayAnswerLimit = 256 << 10
	relayTimeout     = 35 * time.Second
)

// relayPath is a path the relay passes on: under /__autobuild/, plain characters only.
var relayPath = regexp.MustCompile(`^/__autobuild/[A-Za-z0-9_-]+(?:/[A-Za-z0-9_-]+){0,7}$`)

// relayToken is the token the app relay knows ABCP by.
var relayToken = regexp.MustCompile(`^[A-Za-z0-9_-]{32,128}$`)

// relayCaller is who a group's app says is asking: a preview by its ID, a hosted app by its hosting key (BF-08b2).
var relayCaller = regexp.MustCompile(`^(?:preview [a-f0-9]{64}|hosted [A-Za-z0-9][A-Za-z0-9._-]{0,127})$`)

// relayContentType is a content type passed on either way.
var relayContentType = regexp.MustCompile(`^[A-Za-z0-9!#$&^_.+-]+/[A-Za-z0-9!#$&^_.+-]+(?:; ?[A-Za-z0-9_-]+=[A-Za-z0-9_.-]+)?$`)

// AppRelay is where the proxy sends an app's relay requests: an http origin on loopback (the preview gateway), and the
// token that gateway knows ABCP by.
type AppRelay struct{ URL, Token string }

// Valid reports whether the relay is a plain loopback origin with a token.
func (r AppRelay) Valid() bool {
	u, err := url.Parse(r.URL)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.Port() == "" {
		return false
	}
	ip, err := netip.ParseAddr(u.Hostname())
	return err == nil && ip.IsLoopback() && relayToken.MatchString(r.Token)
}

// shopifyStore is one DNS label under myshopify.com, as Shopify names stores.
var shopifyStore = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.myshopify\.com$`)

// Package registries a stack job may fetch from (docs/plans/stack-jobs.md; Repo C IMPROVE_EXISTING_PRODUCTS §13.3), each
// an exact list of names on port 443. Only stack profiles name them; validEgress refuses them on a preview profile.
const (
	EgressPyPI         = "pypi"
	EgressMavenCentral = "maven-central"
	EgressGradle       = "gradle"
	EgressNpm          = "npm"
)

var registryHosts = map[string][]string{
	EgressPyPI:         {"pypi.org", "files.pythonhosted.org"},
	EgressMavenCentral: {"repo.maven.apache.org", "repo1.maven.org"},
	EgressGradle:       {"services.gradle.org", "downloads.gradle.org", "plugins.gradle.org", "plugins-artifacts.gradle.org"},
	EgressNpm:          {"registry.npmjs.org", "registry.yarnpkg.com", "repo.yarnpkg.com"},
}

// RegistryRule reports whether rule is one of the package registries' entries.
func RegistryRule(rule string) bool {
	_, ok := registryHosts[rule]
	return ok
}

// RegistryHosts are the names rule lets through, or none.
func RegistryHosts(rule string) []string { return slices.Clone(registryHosts[rule]) }

// egressAllowed reports whether rules allow a tunnel to host:port: EgressShopifyStores, and the package registries.
func egressAllowed(rules []string, host, port string) bool {
	host = strings.ToLower(host)
	if _, err := netip.ParseAddr(host); err != nil && port == "443" {
		for _, rule := range rules {
			if rule == EgressShopifyStores && shopifyStore.MatchString(host) {
				return true
			}
			if slices.Contains(registryHosts[rule], host) {
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

// EgressCounts is what one proxy has done: tunnels opened, requests refused, and requests passed to the app relay.
type EgressCounts struct{ Opened, Refused, Relayed int64 }

type egressProxy struct {
	listener net.Listener
	rules    []string
	lookup   func(ctx context.Context, host string) ([]netip.Addr, error)
	dial     func(ctx context.Context, address string) (net.Conn, error)
	slots    chan struct{}
	opened   atomic.Int64
	refused  atomic.Int64
	relayed  atomic.Int64
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.Mutex
	conns    map[net.Conn]struct{}

	// hosts are a stack job's own registries' names (§15.9), let through on 443 as the registry rules' names are.
	hosts []string

	// relay and caller are set by relayTo for a group whose profile names EgressAutobuildRelay.
	relay  *AppRelay
	caller string
	client *http.Client
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

// relayTo sends the group's relay requests to relay as caller ("preview <id>"); before start only.
func (e *egressProxy) relayTo(relay AppRelay, caller string) {
	e.relay, e.caller = &relay, caller
	e.client = &http.Client{
		Timeout: relayTimeout,
		// Never the controller's own proxy settings, never a kept connection, never a redirect.
		Transport:     &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: relayTimeout, MaxResponseHeaderBytes: 16 << 10},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (e *egressProxy) start() {
	e.wg.Add(1)
	go e.serve()
}

func (e *egressProxy) Counts() EgressCounts {
	return EgressCounts{Opened: e.opened.Load(), Refused: e.refused.Load(), Relayed: e.relayed.Load()}
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
		if e.relay != nil && slices.Contains(e.rules, EgressAutobuildRelay) {
			e.forward(c, reader, request)
			return
		}
		e.refuse(c, http.StatusMethodNotAllowed)
		return
	}
	host, port, err := net.SplitHostPort(request.Host)
	if err != nil || !egressAllowed(e.rules, host, port) && !(port == "443" && slices.Contains(e.hosts, strings.ToLower(host))) {
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

// forward passes one plain request for a path under /__autobuild/ to the app relay and writes its answer back. Only the
// method, the path, the body and its content type go on; the relay token and the caller are the proxy's own.
func (e *egressProxy) forward(c net.Conn, reader *bufio.Reader, request *http.Request) {
	switch {
	case request.Method != http.MethodGet && request.Method != http.MethodPost:
		e.refuse(c, http.StatusMethodNotAllowed)
		return
	case request.URL.IsAbs() || request.URL.RawQuery != "" || request.URL.Fragment != "" || !relayPath.MatchString(request.URL.Path):
		e.refuse(c, http.StatusNotFound)
		return
	case len(request.TransferEncoding) > 0 || request.Method == http.MethodPost && request.ContentLength < 0:
		e.refuse(c, http.StatusLengthRequired)
		return
	case request.ContentLength > relayBodyLimit:
		e.refuse(c, http.StatusRequestEntityTooLarge)
		return
	}
	c.SetDeadline(time.Now().Add(relayTimeout + 5*time.Second))
	var body []byte
	if request.ContentLength > 0 {
		// What the head's reader already holds comes first; the rest is read from the connection itself, past the
		// head's limit, up to the declared length.
		body = make([]byte, request.ContentLength)
		held := min(reader.Buffered(), len(body))
		head, _ := reader.Peek(held)
		copy(body, head)
		if _, err := io.ReadFull(c, body[held:]); err != nil {
			e.refuse(c, http.StatusBadRequest)
			return
		}
	}
	ctx, cancel := context.WithTimeout(e.ctx, relayTimeout)
	defer cancel()
	out, err := http.NewRequestWithContext(ctx, request.Method, e.relay.URL+request.URL.Path, bytes.NewReader(body))
	if err != nil {
		e.refuse(c, http.StatusBadRequest)
		return
	}
	if kind := request.Header.Get("Content-Type"); relayContentType.MatchString(kind) {
		out.Header.Set("Content-Type", kind)
	}
	out.Header.Set("Accept", "application/json")
	out.Header.Set("X-Autobuild-Relay-Token", e.relay.Token)
	out.Header.Set("X-Autobuild-Caller", e.caller)
	answer, err := e.client.Do(out)
	if err != nil {
		e.refuse(c, http.StatusBadGateway)
		return
	}
	defer answer.Body.Close()
	text, err := io.ReadAll(io.LimitReader(answer.Body, relayAnswerLimit+1))
	if err != nil || len(text) > relayAnswerLimit || answer.StatusCode < 200 || answer.StatusCode > 599 {
		e.refuse(c, http.StatusBadGateway)
		return
	}
	kind := answer.Header.Get("Content-Type")
	if !relayContentType.MatchString(kind) {
		kind = "application/octet-stream"
	}
	e.relayed.Add(1)
	fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Type: %s\r\nContent-Length: %d\r\nCache-Control: no-store\r\nConnection: close\r\n\r\n", answer.StatusCode, http.StatusText(answer.StatusCode), kind, len(text))
	c.Write(text)
}

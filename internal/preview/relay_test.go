package preview

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testRelayToken = "relay-token-0123456789abcdefghijklmnop"

func TestProfilesMayNameTheAppRelayOnce(t *testing.T) {
	preview := testProfile()
	preview.Egress = []string{EgressAutobuildRelay}
	if err := preview.Validate(); err != nil {
		t.Fatal("a preview naming the app relay was refused:", err)
	}
	// A hosted profile names it too (BF-08b2), with Shopify stores or alone.
	hosted := cloneProfile(preview)
	hosted.Hosted = true
	if !validEgress(hosted) {
		t.Fatal("a hosted profile naming the app relay was refused")
	}
	hosted.Egress = []string{EgressShopifyStores, EgressAutobuildRelay}
	if !validEgress(hosted) {
		t.Fatal("a hosted profile naming Shopify stores and the app relay was refused")
	}
	cases := map[string]func(*PreviewProfileV1){
		"twice":              func(p *PreviewProfileV1) { p.Egress = []string{EgressAutobuildRelay, EgressAutobuildRelay} },
		"shopify-on-preview": func(p *PreviewProfileV1) { p.Egress = []string{EgressAutobuildRelay, EgressShopifyStores} },
		"unknown":            func(p *PreviewProfileV1) { p.Egress = []string{"autobuild-relay:443"} },
	}
	for name, change := range cases {
		p := cloneProfile(preview)
		change(&p)
		if validEgress(p) {
			t.Error("accepted:", name)
		}
	}
}

func TestTheRelayCallerIsAPreviewOrAHostedApp(t *testing.T) {
	for _, ok := range []string{"preview " + strings.Repeat("ab", 32), "hosted 82daed69-2b7e-4d0a-afd5-b3fe79738de1", "hosted app-1"} {
		if !relayCaller.MatchString(ok) {
			t.Error("refused:", ok)
		}
	}
	for _, bad := range []string{"", "preview abc", "hosted ", "hosted -x", "hosted a b", "site team-kudos", "preview " + strings.Repeat("AB", 32), "hosted a\r\nX: y"} {
		if relayCaller.MatchString(bad) {
			t.Error("accepted:", bad)
		}
	}
}

func TestTheAppRelayIsALoopbackOriginWithAToken(t *testing.T) {
	if !(AppRelay{URL: "http://127.0.0.1:18093", Token: testRelayToken}).Valid() || !(AppRelay{URL: "http://[::1]:8080", Token: testRelayToken}).Valid() {
		t.Fatal("a loopback relay was refused")
	}
	for _, r := range []AppRelay{
		{URL: "https://127.0.0.1:18093", Token: testRelayToken}, {URL: "http://10.0.0.5:18093", Token: testRelayToken},
		{URL: "http://localhost:18093", Token: testRelayToken}, {URL: "http://127.0.0.1", Token: testRelayToken},
		{URL: "http://127.0.0.1:18093/x", Token: testRelayToken}, {URL: "http://u@127.0.0.1:18093", Token: testRelayToken},
		{URL: "http://127.0.0.1:18093", Token: "short"}, {URL: "http://127.0.0.1:18093", Token: strings.Repeat("a", 31) + " "},
	} {
		if r.Valid() {
			t.Errorf("accepted %+v", r)
		}
	}
}

// relayFixture is a group's proxy with the app relay rule, whose relay is a test server recording what reached it.
type relayFixture struct {
	proxy *egressProxy
	mu    sync.Mutex
	seen  []*http.Request
	body  []string
}

func newRelayFixture(t *testing.T, rules []string, answer func(w http.ResponseWriter, r *http.Request)) *relayFixture {
	t.Helper()
	f := &relayFixture{}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		text, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.seen, f.body = append(f.seen, r), append(f.body, string(text))
		f.mu.Unlock()
		answer(w, r)
	}))
	t.Cleanup(relay.Close)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.proxy = newEgressProxy(listener, rules)
	f.proxy.relayTo(AppRelay{URL: relay.URL, Token: testRelayToken}, "preview "+strings.Repeat("ab", 32))
	f.proxy.start()
	t.Cleanup(func() { f.proxy.Close() })
	return f
}

// ask sends raw to the proxy and returns the answer's status line and body.
func (f *relayFixture) ask(t *testing.T, raw string) (string, string) {
	t.Helper()
	c, err := net.Dial("tcp4", f.proxy.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(c, raw); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(c)
	answer, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer answer.Body.Close()
	body, _ := io.ReadAll(answer.Body)
	return answer.Status, string(body)
}

func TestTheProxyPassesAnAppsRelayRequestOnWithItsOwnIdentityAlone(t *testing.T) {
	f := newRelayFixture(t, []string{EgressAutobuildRelay}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "x=1")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"id":"plink_1","status":"paid"}`)
	})
	status, body := f.ask(t, "GET /__autobuild/connect/razorpay/payment-link/plink_1 HTTP/1.1\r\nHost: 10.213.0.1:3129\r\n"+
		"X-Autobuild-Caller: preview "+strings.Repeat("cd", 32)+"\r\nX-Autobuild-Relay-Token: forged\r\nX-Autobuild-Site: forged\r\n"+
		"Authorization: Bearer app\r\nCookie: a=b\r\nForwarded: for=1.2.3.4\r\n\r\n")
	if status != "200 OK" || body != `{"id":"plink_1","status":"paid"}` {
		t.Fatalf("answer %q %q", status, body)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.seen) != 1 {
		t.Fatalf("%d requests reached the relay", len(f.seen))
	}
	r := f.seen[0]
	if r.Method != http.MethodGet || r.URL.Path != "/__autobuild/connect/razorpay/payment-link/plink_1" {
		t.Fatalf("relay saw %s %s", r.Method, r.URL)
	}
	if r.Header.Get("X-Autobuild-Relay-Token") != testRelayToken || r.Header.Get("X-Autobuild-Caller") != "preview "+strings.Repeat("ab", 32) {
		t.Fatalf("identity %q %q", r.Header.Get("X-Autobuild-Relay-Token"), r.Header.Get("X-Autobuild-Caller"))
	}
	for _, name := range []string{"X-Autobuild-Site", "Authorization", "Cookie", "Forwarded"} {
		if r.Header.Get(name) != "" {
			t.Errorf("%s reached the relay", name)
		}
	}
	if f.proxy.Counts().Relayed != 1 || f.proxy.Counts().Opened != 0 {
		t.Fatalf("counts %+v", f.proxy.Counts())
	}
}

func TestTheProxyPassesABodyPastItsHeadLimit(t *testing.T) {
	f := newRelayFixture(t, []string{EgressAutobuildRelay}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	})
	payload := `{"text":"` + strings.Repeat("x", 20000) + `"}`
	status, _ := f.ask(t, "POST /__autobuild/ai HTTP/1.1\r\nHost: 10.213.0.1:3129\r\nContent-Type: application/json\r\nContent-Length: "+
		strconv.Itoa(len(payload))+"\r\n\r\n"+payload)
	if status != "200 OK" {
		t.Fatal(status)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.body) != 1 || f.body[0] != payload || f.seen[0].Header.Get("Content-Type") != "application/json" {
		t.Fatalf("relay got %d bytes", len(f.body[0]))
	}
}

func TestTheProxyRefusesWhatIsNotARelayRequest(t *testing.T) {
	f := newRelayFixture(t, []string{EgressAutobuildRelay}, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	for raw, want := range map[string]string{
		"GET / HTTP/1.1\r\nHost: x\r\n\r\n":                                                       "404 Not Found",
		"GET /api/orders HTTP/1.1\r\nHost: x\r\n\r\n":                                             "404 Not Found",
		"GET /__autobuild/../api HTTP/1.1\r\nHost: x\r\n\r\n":                                     "404 Not Found",
		"GET /__autobuild//ai HTTP/1.1\r\nHost: x\r\n\r\n":                                        "404 Not Found",
		"GET /__autobuild/ai?x=1 HTTP/1.1\r\nHost: x\r\n\r\n":                                     "404 Not Found",
		"GET http://example.com/__autobuild/ai HTTP/1.1\r\nHost: example.com\r\n\r\n":             "404 Not Found",
		"DELETE /__autobuild/ai HTTP/1.1\r\nHost: x\r\n\r\n":                                      "405 Method Not Allowed",
		"POST /__autobuild/ai HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n": "411 Length Required",
		"POST /__autobuild/ai HTTP/1.1\r\nHost: x\r\nContent-Length: 70000\r\n\r\n":               "413 Request Entity Too Large",
		connectLine("example.com:443"):                                                            "403 Forbidden",
	} {
		if status, _ := f.ask(t, raw); status != want {
			t.Errorf("%q: %s, want %s", strings.SplitN(raw, "\r\n", 2)[0], status, want)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.seen) != 0 {
		t.Fatalf("%d refused requests reached the relay", len(f.seen))
	}
}

func TestWithoutTheRuleTheProxyRelaysNothing(t *testing.T) {
	f := newRelayFixture(t, []string{EgressShopifyStores}, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	if status, _ := f.ask(t, "GET /__autobuild/ai HTTP/1.1\r\nHost: x\r\n\r\n"); status != "405 Method Not Allowed" {
		t.Fatal(status)
	}
}

func TestARelayThatFailsOrAnswersTooMuchIsABadGateway(t *testing.T) {
	big := newRelayFixture(t, []string{EgressAutobuildRelay}, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("y", relayAnswerLimit+1))
	})
	if status, _ := big.ask(t, "GET /__autobuild/ai HTTP/1.1\r\nHost: x\r\n\r\n"); status != "502 Bad Gateway" {
		t.Fatal(status)
	}
	refused := newRelayFixture(t, []string{EgressAutobuildRelay}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"not_found"}`)
	})
	if status, body := refused.ask(t, "GET /__autobuild/ai HTTP/1.1\r\nHost: x\r\n\r\n"); status != "404 Not Found" || body != `{"error":"not_found"}` {
		t.Fatal(status, body)
	}
}

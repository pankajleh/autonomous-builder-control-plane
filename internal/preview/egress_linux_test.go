//go:build linux

package preview

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// shopifyProfile is a hosted profile whose one service takes the Shopify settings and reaches stores through the proxy.
func shopifyProfile() PreviewProfileV1 {
	p := testProfile()
	p.ProfileID, p.Hosted, p.Egress = "shopify-app-hosted-v1", true, []string{EgressShopifyStores}
	p.Services[0].Settings = []string{"SHOPIFY_API_KEY", "SHOPIFY_API_SECRET", "SHOPIFY_APP_URL", "SCOPES"}
	return p
}

var shopifySettings = map[string]string{"SHOPIFY_API_KEY": "client-id-1", "SHOPIFY_API_SECRET": "never-shown-secret-1",
	"SHOPIFY_APP_URL": "https://shop-app.deployflix.app", "SCOPES": "read_products,write_products"}

func TestProfilesTakeEgressAndSettingsOnlyWhenHostedAndOnlyFromTheirLists(t *testing.T) {
	if err := shopifyProfile().Validate(); err != nil {
		t.Fatal("the Shopify profile was refused:", err)
	}
	cases := map[string]func(*PreviewProfileV1){
		"egress-on-a-preview":   func(p *PreviewProfileV1) { p.Hosted = false; p.Services[0].Settings = nil },
		"settings-on-a-preview": func(p *PreviewProfileV1) { p.Hosted = false; p.Egress = nil },
		"another-egress":        func(p *PreviewProfileV1) { p.Egress = []string{"*.example.com:443"} },
		"a-wider-egress":        func(p *PreviewProfileV1) { p.Egress = []string{"*:443"} },
		"two-egress-entries":    func(p *PreviewProfileV1) { p.Egress = []string{EgressShopifyStores, EgressShopifyStores} },
		"another-port":          func(p *PreviewProfileV1) { p.Egress = []string{"*.myshopify.com:80"} },
		"an-unknown-setting":    func(p *PreviewProfileV1) { p.Services[0].Settings = append(p.Services[0].Settings, "DATABASE_URL") },
		"a-runtime-setting":     func(p *PreviewProfileV1) { p.Services[0].Settings = []string{"LD_PRELOAD"} },
		"a-setting-twice":       func(p *PreviewProfileV1) { p.Services[0].Settings = []string{"SCOPES", "SCOPES"} },
		"two-services-with-settings": func(p *PreviewProfileV1) {
			db := p.Services[0]
			db.Name, db.Presented, db.Settings = "db", false, []string{"SCOPES"}
			p.Services = append(p.Services, db)
		},
	}
	for name, change := range cases {
		p := cloneProfile(shopifyProfile())
		change(&p)
		if p.Validate() == nil {
			t.Error("accepted:", name)
		}
	}
	// Profiles without them keep their digests: neither field appears in their JSON.
	raw, _ := json.Marshal(hostedProfile())
	if strings.Contains(string(raw), "egress") || strings.Contains(string(raw), "settings") {
		t.Fatal("a profile without egress or settings changed its JSON:", string(raw))
	}
}

func TestAHostedStartCarriesSettingsAsValuesTheFileCanHoldUnchanged(t *testing.T) {
	start := func(settings map[string]string) serviceapi.HostedStartRequestV1 {
		c := startCommand("start-1")
		c.Settings = settings
		return c
	}
	if serviceapi.ValidateHostedStartRequestV1(start(shopifySettings)) != nil || serviceapi.ValidateHostedStartRequestV1(start(nil)) != nil {
		t.Fatal("a good start was refused")
	}
	nine := map[string]string{}
	for _, name := range []string{"A", "B", "C", "D", "E", "F", "G", "H", "I"} {
		nine[name] = "v"
	}
	for name, settings := range map[string]map[string]string{
		"nine-settings": nine, "a-lower-case-name": {"scopes": "x"}, "a-name-with-a-dash": {"SHOPIFY-KEY": "x"}, "an-empty-value": {"SCOPES": ""},
		"a-new-line": {"SCOPES": "a\nOTHER=b"}, "a-carriage-return": {"SCOPES": "a\rb"}, "a-nul": {"SCOPES": "a\x00b"}, "a-leading-space": {"SCOPES": " a"},
		"a-tab": {"SCOPES": "a\tb"}, "not-utf8": {"SCOPES": "a\xffb"}, "too-long": {"SCOPES": strings.Repeat("a", 1025)},
	} {
		if serviceapi.ValidateHostedStartRequestV1(start(settings)) == nil {
			t.Error("accepted:", name)
		}
	}
}

func shopifyHosting(t *testing.T) (*Hosting, *hostedMemory, string) {
	t.Helper()
	fastHosted(t)
	root := filepath.Join(t.TempDir(), "hosted")
	rt := &hostedMemory{available: true, healthy: true, dump: "dump-1"}
	clock := &hostedClock{now: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)}
	web, hosted, shopify := testProfile(), hostedProfile(), shopifyProfile()
	source := Source{RunID: "run-1", CheckpointActivityID: strings.Repeat("b", 64), SHA: strings.Repeat("c", 40), Repository: "/private/repo", RepositoryIdentityDigest: shopify.RepositoryIdentityDigest, ProductAuthorizationID: "auth-1", ProductTaskID: "task-1", ProductVersionID: "version-1", ValidationID: strings.Repeat("e", 64)}
	h, err := newHosting(context.Background(), root, map[string]PreviewProfileV1{web.ProfileID: web, hosted.ProfileID: hosted, shopify.ProfileID: shopify}, staticResolver{source: source}, &memoryCheckout{}, rt, clock.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	return h, rt, root
}

func shopifyStart(id string, settings map[string]string) serviceapi.HostedStartRequestV1 {
	c := startCommand(id)
	c.ProfileID, c.Settings = "shopify-app-hosted-v1", settings
	return c
}

func TestHostedSettingsLiveOnlyInTheKeysPrivateFile(t *testing.T) {
	h, rt, root := shopifyHosting(t)
	ctx := context.Background()
	// Exactly the profile's settings: none missing, none extra; and none at all for a profile that takes none.
	missing := map[string]string{"SHOPIFY_API_KEY": "k"}
	extra := map[string]string{"SHOPIFY_API_KEY": "k", "SHOPIFY_API_SECRET": "s", "SHOPIFY_APP_URL": "u", "SCOPES": "x", "OTHER": "y"}
	for name, settings := range map[string]map[string]string{"none": nil, "missing": missing, "extra": extra} {
		if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "shop-1", shopifyStart("start-"+name, settings)); !errors.Is(err, serviceapi.ErrPreviewIneligible) {
			t.Errorf("%s: %v", name, err)
		}
	}
	nodeWithSettings := startCommand("start-node")
	nodeWithSettings.Settings = map[string]string{"SCOPES": "x"}
	if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "node-1", nodeWithSettings); !errors.Is(err, serviceapi.ErrPreviewIneligible) {
		t.Errorf("settings for a profile that takes none: %v", err)
	}
	if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "shop-1", shopifyStart("start-1", shopifySettings)); err != nil {
		t.Fatal(err)
	}
	awaitHosted(t, h, "shop-1", "READY")
	digest := hostedKeyDigest("shop-1")
	file := filepath.Join(root, digest, "settings.env")
	info, err := os.Lstat(file)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatal("the settings file", info, err)
	}
	data, _ := os.ReadFile(file)
	if string(data) != "SCOPES=read_products,write_products\nSHOPIFY_API_KEY=client-id-1\nSHOPIFY_API_SECRET=never-shown-secret-1\nSHOPIFY_APP_URL=https://shop-app.deployflix.app\n" {
		t.Fatalf("settings file %q", data)
	}
	rt.mu.Lock()
	if len(rt.settings) != 1 || rt.settings[0] != file {
		t.Fatal("the runtime was not given the settings file", rt.settings)
	}
	rt.mu.Unlock()
	// Nothing else the key keeps, nor anything a reader sees, holds a value.
	filepath.Walk(filepath.Join(root, digest), func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() && path != file {
			if b, _ := os.ReadFile(path); strings.Contains(string(b), "never-shown-secret-1") {
				t.Errorf("%s holds a setting's value", path)
			}
		}
		return nil
	})
	v, _ := h.ReadHosted(ctx, principal(), "shop-1")
	if raw, _ := json.Marshal(v); strings.Contains(string(raw), "never-shown") || strings.Contains(string(raw), "client-id-1") {
		t.Fatal("the hosted view shows a setting")
	}
	// The same request again is a replay; the receipt knows the settings' names only.
	if _, err := h.StartHosted(ctx, principal(), testAuthorityDigest, "shop-1", shopifyStart("start-1", shopifySettings)); err != nil {
		t.Fatal("replay:", err)
	}
	// After a controller restart the instance is started again with the same file.
	h.Close()
	rt2 := &hostedMemory{available: true, healthy: true, dump: "dump-1"}
	web, hosted, shopify := testProfile(), hostedProfile(), shopifyProfile()
	source := Source{RunID: "run-1", CheckpointActivityID: strings.Repeat("b", 64), SHA: strings.Repeat("c", 40), Repository: "/private/repo", RepositoryIdentityDigest: shopify.RepositoryIdentityDigest, ProductAuthorizationID: "auth-1", ProductTaskID: "task-1", ProductVersionID: "version-1", ValidationID: strings.Repeat("e", 64)}
	again, err := newHosting(ctx, root, map[string]PreviewProfileV1{web.ProfileID: web, hosted.ProfileID: hosted, shopify.ProfileID: shopify}, staticResolver{source: source}, &memoryCheckout{}, rt2, (&hostedClock{now: time.Date(2026, 9, 30, 13, 0, 0, 0, time.UTC)}).Now)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	awaitHosted(t, again, "shop-1", "READY")
	rt2.mu.Lock()
	if len(rt2.settings) != 1 || rt2.settings[0] != file {
		t.Fatal("after a restart the runtime was not given the settings file", rt2.settings)
	}
	rt2.mu.Unlock()
	// A purge removes the settings with the data.
	if _, err := again.StopHosted(ctx, principal(), testAuthorityDigest, "shop-1", stopCommand("stop-1")); err != nil {
		t.Fatal(err)
	}
	awaitHosted(t, again, "shop-1", "STOPPED")
	if _, err := again.PurgeHosted(ctx, principal(), testAuthorityDigest, "shop-1", stopCommand("purge-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(file); !os.IsNotExist(err) {
		t.Fatal("a purge kept the settings file", err)
	}
}

func TestTheServiceThatTakesSettingsGetsThemFromTheirFileAndEveryServiceLearnsTheProxy(t *testing.T) {
	gateway := previewSubnet(strings.Repeat("c", 64), 0).Addr().Next()
	s := shopifyProfile().Services[0]
	plain := serviceStart("app-0", s, []string{"/usr/local/bin/node", "/opt/autobuild/start.mjs"}, gateway, "")
	if strings.Join(plain, " ") != "exec --detach app-0 /usr/bin/env -i PATH=/usr/local/bin:/usr/bin:/bin HOME=/scratch TMPDIR=/scratch NODE_ENV=development HTTPS_PROXY=http://"+gateway.String()+":3129 NODE_USE_ENV_PROXY=1 /usr/local/bin/node /opt/autobuild/start.mjs" {
		t.Fatal("plain start", plain)
	}
	withSettings := serviceStart("app-0", s, []string{"/usr/local/bin/node", "/opt/autobuild/start.mjs"}, gateway, "/private/hosted/k/settings.env")
	if strings.Join(withSettings, " ") != "exec --detach --env-file /private/hosted/k/settings.env app-0 /usr/bin/env -u HOSTNAME PATH=/usr/local/bin:/usr/bin:/bin HOME=/scratch TMPDIR=/scratch NODE_ENV=development HTTPS_PROXY=http://"+gateway.String()+":3129 NODE_USE_ENV_PROXY=1 /usr/local/bin/node /opt/autobuild/start.mjs" {
		t.Fatal("start with settings", withSettings)
	}
	// A group without a proxy tells its services nothing about one.
	if none := strings.Join(serviceStart("app-0", testProfile().Services[0], []string{"/x"}, netip.Addr{}, ""), " "); strings.Contains(none, "PROXY") {
		t.Fatal("start without a proxy", none)
	}
}

func TestAnEgressGroupRunsItsProxyWithTheSettingsFileUntilItStops(t *testing.T) {
	p := shopifyProfile()
	f := newDockerFixture(t, p)
	f.d.approved[p.Digest()] = true
	settings := filepath.Join(t.TempDir(), "settings.env")
	if err := os.WriteFile(settings, []byte("SCOPES=x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.d.root, "sources", f.id)
	// Only a private settings file, and only for a start that names one.
	if _, err := f.d.StartHosted(context.Background(), f.id, path, p, strings.Repeat("7", 64), settings); err == nil {
		t.Fatal("a settings file others can read was accepted")
	}
	if _, err := f.d.StartHosted(context.Background(), f.id, path, p, strings.Repeat("7", 64), ""); err == nil {
		t.Fatal("a start without the settings the profile takes was accepted")
	}
	os.Chmod(settings, 0600)
	route, err := f.d.StartHosted(context.Background(), f.id, path, p, strings.Repeat("7", 64), settings)
	if err != nil || !sha256Pattern.MatchString(route) {
		t.Fatal(route, err)
	}
	f.d.mu.Lock()
	g := f.d.groups[f.id]
	f.d.mu.Unlock()
	if g == nil || g.egress == nil {
		t.Fatal("the group has no egress proxy")
	}
	started := false
	for _, args := range f.commands {
		joined := strings.Join(args, " ")
		if args[0] == "exec" && slices.Contains(args, "--detach") {
			started = true
			if !strings.Contains(joined, "--env-file "+settings+" ") || !strings.Contains(joined, "HTTPS_PROXY=http://"+g.gateway.String()+":3129") || strings.Contains(joined, "SCOPES=x") {
				t.Fatal("start", joined)
			}
		}
	}
	if !started {
		t.Fatal("no service was started")
	}
	address := g.egress.listener.Addr().String()
	if err := f.d.Stop(context.Background(), f.id); err != nil {
		t.Fatal(err)
	}
	if c, err := net.DialTimeout("tcp", address, time.Second); err == nil {
		c.Close()
		t.Fatal("the proxy still listens after Stop")
	}
	// A start that fails after the proxy opened closes it again.
	q := newDockerFixture(t, p)
	q.d.approved[p.Digest()] = true
	q.reject = func(_ context.Context, args []string) error {
		if args[0] == "exec" {
			return errors.New("exec failed")
		}
		return nil
	}
	listen := q.d.listen
	var addr string
	q.d.listen = func(network, address string) (l net.Listener, err error) {
		l, err = listen(network, address)
		if err == nil {
			addr = l.Addr().String()
		}
		return l, err
	}
	if _, err := q.d.StartHosted(context.Background(), q.id, filepath.Join(q.d.root, "sources", q.id), p, strings.Repeat("7", 64), settings); err == nil {
		t.Fatal("the failed start succeeded")
	}
	if addr == "" {
		t.Fatal("the proxy was never opened")
	}
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Fatal("a failed start left its proxy listening")
	}
}

func TestTheProbeProvesAnEgressProfileOnlyWhenTheProxyIsReachableAndRefusesOtherNames(t *testing.T) {
	for _, rule := range []bool{false, true} {
		p := shopifyProfile()
		f := newDockerFixture(t, p)
		f.id = jsonDigest([]string{f.d.namespace, p.Digest(), "isolation-probe"})
		f.subnet = previewSubnet(f.id, 0)
		f.egressRule = rule
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
		f.external = func() { connectPresentation(t, f.d.groups[f.id], target.URL) }
		proved := f.d.prove(context.Background(), p)
		target.Close()
		if proved != rule {
			t.Fatalf("with the owner's rule %v, proved %v", rule, proved)
		}
		asked := false
		for _, args := range f.commands {
			asked = asked || args[0] == "exec" && strings.Contains(strings.Join(args, " "), "CONNECT example.com:443") && strings.HasSuffix(strings.Join(args, " "), " 3129")
		}
		if !asked {
			t.Fatal("the probe did not try the proxy from the service")
		}
	}
}

//go:build linux

package preview

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPreviewNetworksAreOne28OfThePreviewRange(t *testing.T) {
	id := strings.Repeat("c", 64)
	seen := map[netip.Prefix]bool{}
	for attempt := 0; attempt < 4096; attempt++ {
		subnet := previewSubnet(id, attempt)
		if subnet.Bits() != 28 || subnet.Masked() != subnet || !previewSubnets.Contains(subnet.Addr()) || seen[subnet] {
			t.Fatal("attempt", attempt, subnet)
		}
		seen[subnet] = true
	}
	if previewSubnet(id, 4096) != previewSubnet(id, 0) {
		t.Fatal("the candidates do not wrap around the range")
	}
	if previewSubnet(strings.Repeat("0", 64), 0).String() != "10.213.0.0/28" || previewSubnet(strings.Repeat("f", 64), 0).String() != "10.213.255.240/28" {
		t.Fatal("the first candidate is not given by the id", previewSubnet(strings.Repeat("0", 64), 0), previewSubnet(strings.Repeat("f", 64), 0))
	}
}

func TestAGroupsNetworkIsCreatedInThePreviewRangeAndTheNextSubnetIsTriedWhenOneIsTaken(t *testing.T) {
	p := testProfile()
	f := newDockerFixture(t, p)
	f.d.approved[p.Digest()] = true
	taken := previewSubnet(f.id, 0).String()
	var tried []string
	f.reject = func(_ context.Context, args []string) error {
		if args[0] == "network" && args[1] == "create" {
			i := slices.Index(args, "--subnet")
			tried = append(tried, args[i+1])
			if args[i+1] == taken {
				return errors.New("Pool overlaps with other one on this address space")
			}
		}
		return nil
	}
	if _, err := f.d.Start(context.Background(), f.id, filepath.Join(f.d.root, "sources", f.id), p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.d.Stop(context.Background(), f.id) })
	if len(tried) != 2 || tried[0] != taken || tried[1] != previewSubnet(f.id, 1).String() || f.subnet != previewSubnet(f.id, 1) {
		t.Fatal("subnets tried", tried, "created", f.subnet)
	}

	g := newDockerFixture(t, p)
	g.d.approved[p.Digest()] = true
	creates := 0
	g.reject = func(_ context.Context, args []string) error {
		if args[0] == "network" && args[1] == "create" {
			creates++
			return errors.New("Pool overlaps with other one on this address space")
		}
		return nil
	}
	if _, err := g.d.Start(context.Background(), g.id, filepath.Join(g.d.root, "sources", g.id), p); err == nil || creates != 64 {
		t.Fatal("a full range must refuse the start after 64 tries", err, creates)
	}
}

func TestANetworkOrAnAddressOutsideThePreviewRangeFailsTheInspection(t *testing.T) {
	d := newDocker("/private/previews")
	id := strings.Repeat("c", 64)
	network := func(configs ...map[string]string) string {
		raw, _ := json.Marshal([]any{map[string]any{"Internal": true, "Driver": "bridge", "Labels": map[string]string{"abcp.preview.owner": d.namespace},
			"IPAM": map[string]any{"Config": configs}}})
		return string(raw)
	}
	good := map[string]string{"Subnet": "10.213.4.32/28", "Gateway": "10.213.4.33"}
	cases := map[string]string{
		"docker-default-pool":    network(map[string]string{"Subnet": "172.16.5.0/24", "Gateway": "172.16.5.1"}),
		"a-wider-subnet":         network(map[string]string{"Subnet": "10.213.4.0/24", "Gateway": "10.213.4.1"}),
		"not-on-a-28-boundary":   network(map[string]string{"Subnet": "10.213.4.40/28", "Gateway": "10.213.4.41"}),
		"gateway-elsewhere":      network(map[string]string{"Subnet": "10.213.4.32/28", "Gateway": "172.16.5.1"}),
		"no-address-range":       network(),
		"two-address-ranges":     network(good, map[string]string{"Subnet": "10.213.4.48/28", "Gateway": "10.213.4.49"}),
		"no-longer-internal":     strings.Replace(network(good), `"Internal":true`, `"Internal":false`, 1),
		"another-owners-network": strings.Replace(network(good), d.namespace, strings.Repeat("0", 64), 1),
	}
	d.run = func(context.Context, ...string) (string, error) { return network(good), nil }
	if gateway, ok := d.networkGateway(context.Background(), id); !ok || gateway.String() != "10.213.4.33" {
		t.Fatal("a network in the preview range was refused", gateway, ok)
	}
	for name, raw := range cases {
		d.run = func(context.Context, ...string) (string, error) { return raw, nil }
		if _, ok := d.networkGateway(context.Background(), id); ok {
			t.Fatal("accepted:", name)
		}
	}

	p := testProfile()
	v := dockerInspection(d, id, p, p.Services[0])
	v["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)[d.network(id)] = map[string]string{"IPAddress": "172.16.5.3"}
	raw, _ := json.Marshal([]any{v})
	d.run = func(context.Context, ...string) (string, error) { return string(raw), nil }
	if _, err := d.endpoint(context.Background(), id, p, p.Services[0], 0, ""); err == nil {
		t.Fatal("a service outside the preview range passed the inspection")
	}
	q := gvisorProfile()
	if extraHostsAllowed(q, q.Services[0], []string{"db:172.16.5.3"}) || !extraHostsAllowed(q, q.Services[0], []string{"db:10.213.4.35"}) {
		t.Fatal("hosts entries must point into the preview range")
	}
}

func TestTheProbeRefusesAProfileWhoseServicesReachTheHost(t *testing.T) {
	for _, reach := range []bool{false, true} {
		p := testProfile()
		f := newDockerFixture(t, p)
		f.id = jsonDigest([]string{f.d.namespace, p.Digest(), "isolation-probe"})
		f.subnet = previewSubnet(f.id, 0)
		f.reachHost = reach
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }))
		f.external = func() { connectPresentation(t, f.d.groups[f.id], target.URL) }
		proved := f.d.prove(context.Background(), p)
		target.Close()
		if proved == reach {
			t.Fatalf("host reachable %v, proved %v", reach, proved)
		}
		asked := false
		for _, args := range f.commands {
			asked = asked || args[0] == "exec" && strings.Contains(strings.Join(args, " "), "wget -T 2 -O /dev/null http://127.0.0.1:")
		}
		if !asked {
			t.Fatal("the probe did not try the host from the service")
		}
	}
}

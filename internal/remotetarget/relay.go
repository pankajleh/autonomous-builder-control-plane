package remotetarget

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// A site's relay credential: 32 random bytes, base64url.
var relayCredentialPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// A host relays may be sent to: a DNS name, lower case, not an address.
var relayHostPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.){1,9}[a-z]{2,63}$`)

// SetRelayHost says where sites' front-door blocks send Autobuild's paths
// (BF-08a); empty turns it off. Front doors change at their next write.
func (m *Manager) SetRelayHost(host string) error {
	if host != "" && (len(host) < 4 || len(host) > 253 || !relayHostPattern.MatchString(host)) {
		return serviceapi.ErrTargetInvalid
	}
	m.mu.Lock()
	m.relayHost = host
	m.mu.Unlock()
	return nil
}

// SetSiteRelay keeps a site's relay credential and rewrites its target's front
// door as an operation (BF-08a). Giving the same credential again answers the
// operation that first gave it.
func (m *Manager) SetSiteRelay(_ context.Context, key, site string, relay serviceapi.SiteRelayV1) (serviceapi.OperationV1, error) {
	if !siteNamePattern.MatchString(site) || !relayCredentialPattern.MatchString(relay.Credential) {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
	}
	sum := sha256.Sum256([]byte(relay.Credential))
	requestID := "relay-" + site + "-" + hex.EncodeToString(sum[:8])
	m.mu.Lock()
	defer m.mu.Unlock()
	r, existing, err := m.lifecycleTarget(key, "relay", site, requestID)
	if err != nil || existing != nil {
		return deref(existing), err
	}
	s, ok := findSite(r, site)
	if !ok || !served(s) {
		return serviceapi.OperationV1{}, serviceapi.ErrSiteUnknown
	}
	s.Relay = relay.Credential
	m.setSite(&r, s)
	if err := m.write(r); err != nil {
		return serviceapi.OperationV1{}, err
	}
	rec, err := m.startLifecycle(&r, "relay", site, requestID, []string{"write_site", "reload_frontdoor"}, "")
	if err != nil {
		return serviceapi.OperationV1{}, err
	}
	go m.run(rec)
	return rec.Operation, nil
}

// runRelay writes the target's front door again, with every site's block as
// its record says, and reloads it.
func (m *Manager) runRelay(ctx context.Context, rec *operationRecord, r record, session remote) string {
	steps := []struct {
		name string
		work func() (string, error)
	}{
		{"write_site", func() (string, error) {
			return run(ctx, session, fill(writeSiteScript, "NETS", m.nodeNetworks(r.Key)), strings.NewReader(m.caddyfile(r.Key, "", "")))
		}},
		{"reload_frontdoor", func() (string, error) {
			return run(ctx, session, fill(frontDoorScript, "NETS", m.nodeNetworks(r.Key)), nil)
		}},
	}
	for _, s := range steps {
		if err := m.step(rec, s.name, s.work); err != nil {
			return stepDetail(err)
		}
	}
	return ""
}

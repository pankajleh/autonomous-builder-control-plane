package remotetarget

import (
	"context"
	"net"
	"sort"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// A name of your own (Repo C design note BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md
// §11, BF-14.3a): whether a host points at a target, looked up once and
// nothing recorded, and a site's check run once more without SSH, so a site
// that failed only because its DNS record was missing can become live
// without a new plan. ABCP never writes anyone's DNS.

const (
	// nameTimeout bounds one lookup of a name.
	nameTimeout = 5 * time.Second
	// maxNameAddresses bounds the addresses a name answers with.
	maxNameAddresses = 8
)

// Name looks host up once and says whether one of its addresses is the
// target's.
func (m *Manager) Name(ctx context.Context, key, host string) (serviceapi.NameV1, error) {
	if !validHost(host) {
		return serviceapi.NameV1{}, serviceapi.ErrTargetInvalid
	}
	m.mu.Lock()
	r, err := m.read(key)
	m.mu.Unlock()
	if err != nil {
		return serviceapi.NameV1{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, nameTimeout)
	defer cancel()
	ips, err := m.addresses(ctx, r.Address)
	if err != nil {
		return serviceapi.NameV1{}, err
	}
	name := serviceapi.NameV1{SchemaVersion: serviceapi.NameSchemaV1, Target: key, Host: host, Address: ips[0].String(),
		Addresses: []string{}, CheckedAt: m.stamp()}
	// A name that does not resolve has no addresses; that is an answer, not an error.
	resolved, _ := m.resolve(ctx, host)
	seen := map[string]bool{}
	for _, ip := range resolved {
		name.Points = name.Points || ip.Equal(ips[0])
		if text := ip.String(); !seen[text] {
			seen[text] = true
			name.Addresses = append(name.Addresses, text)
		}
	}
	sort.Slice(name.Addresses, func(i, j int) bool {
		a, b := net.ParseIP(name.Addresses[i]), net.ParseIP(name.Addresses[j])
		if (a.To4() == nil) != (b.To4() == nil) {
			return a.To4() != nil
		}
		return name.Addresses[i] < name.Addresses[j]
	})
	if len(name.Addresses) > maxNameAddresses {
		name.Addresses = name.Addresses[:maxNameAddresses]
	}
	return name, nil
}

// CheckSite runs a site's check once more: its name must point at the target
// and it must answer over HTTPS with its release. A failed site whose release
// is in place becomes live when it answers; otherwise it stays as it was, with
// the new last check.
func (m *Manager) CheckSite(_ context.Context, key, site string, request serviceapi.OperationRequestV1) (serviceapi.OperationV1, error) {
	if !siteNamePattern.MatchString(site) || !requestIDPattern.MatchString(request.RequestID) {
		return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, existing, err := m.lifecycleTarget(key, "check_site", site, request.RequestID)
	if err != nil || existing != nil {
		return deref(existing), err
	}
	s, ok := findSite(r, site)
	if !ok || !served(s) {
		return serviceapi.OperationV1{}, serviceapi.ErrSiteUnknown
	}
	switch s.State {
	case siteLive, siteFailed:
	case siteStopped:
		return serviceapi.OperationV1{}, serviceapi.ErrSiteStopped
	default:
		// A site left unknown is the drift check's to reconcile.
		return serviceapi.OperationV1{}, serviceapi.ErrTargetInvalid
	}
	rec, err := m.startLifecycle(&r, "check_site", site, request.RequestID, []string{"check"}, "")
	if err != nil {
		return serviceapi.OperationV1{}, err
	}
	go m.run(rec)
	return rec.Operation, nil
}

// runCheckSite needs no session: the check reads the site over HTTPS.
func (m *Manager) runCheckSite(ctx context.Context, rec *operationRecord, r record) string {
	release, err := m.readRelease(rec.Release)
	if err != nil {
		m.markFirstPending(rec, "failed", "release_missing")
		return "release_missing"
	}
	if err := m.step(rec, "check", func() (string, error) { return m.checkSite(ctx, r, rec.Host, release) }); err != nil {
		return stepDetail(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, err := m.read(r.Key); err == nil {
		if s, ok := findSite(cur, rec.Operation.Site); ok && s.State == siteFailed && s.Release == rec.Release {
			s.State, s.UpdatedAt = siteLive, m.stamp()
			m.setSite(&cur, s)
			_ = m.write(cur)
		}
	}
	return ""
}

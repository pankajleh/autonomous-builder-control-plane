package remotetarget

import (
	"context"
	"sync"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/appwatch"
)

// watchConcurrency and watchTimeout bound one sweep of the targets' sites.
const (
	watchConcurrency = 4
	watchTimeout     = 20 * time.Second
)

// HealthObservations looks at every site its owner keeps running on a confirmed target (Repo C BF-07e): a Node app must
// answer /health and a static site / with 200 over HTTPS from the target's own address, with a valid certificate for its
// host. A site being deployed, removed or operated on is busy; a paused site, a site never deployed and the sites of a
// target that is not confirmed are not looked at. One request per site, four at a time. False when the targets cannot
// be read.
func (m *Manager) HealthObservations(ctx context.Context) ([]appwatch.Observation, bool) {
	m.mu.Lock()
	records, err := m.all()
	m.mu.Unlock()
	if err != nil {
		return nil, false
	}
	type look struct {
		app           appwatch.App
		address, host string
		path          string
	}
	out := []appwatch.Observation{}
	var looks []look
	for _, r := range records {
		if r.State != stateConfirmed {
			continue
		}
		for _, s := range r.Sites {
			app := appwatch.App{Kind: appwatch.KindSite, Target: r.Key, Site: s.Name}
			switch {
			case s.State == siteStopped || s.Release == "":
				continue
			case s.Operation != "" || s.State == siteDeploying || s.State == siteRemoving || s.State == siteUnknown:
				out = append(out, appwatch.Observation{App: app, State: appwatch.Busy})
			default:
				path := "/"
				if s.Kind == kindNode {
					path = "/health"
				}
				looks = append(looks, look{app: app, address: r.Address, host: s.Host, path: path})
			}
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, watchConcurrency)
	for _, l := range looks {
		wg.Add(1)
		slots <- struct{}{}
		go func(l look) {
			defer wg.Done()
			defer func() { <-slots }()
			state := appwatch.Unwell
			if m.answers(ctx, l.address, l.host, l.path) {
				state = appwatch.Healthy
			}
			mu.Lock()
			out = append(out, appwatch.Observation{App: l.app, State: state})
			mu.Unlock()
		}(l)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, false
	}
	return out, true
}

// answers is true when host answers path with 200 from the target's address.
func (m *Manager) answers(ctx context.Context, address, host, path string) bool {
	ctx, cancel := context.WithTimeout(ctx, watchTimeout)
	defer cancel()
	ips, err := m.addresses(ctx, address)
	if err != nil || len(ips) == 0 {
		return false
	}
	_, err = m.fetch(ctx, ips[0], host, path)
	return err == nil
}

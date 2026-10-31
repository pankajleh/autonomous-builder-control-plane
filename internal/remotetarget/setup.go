package remotetarget

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// The first administrator of an app on a server (Repo C design note
// BF14_PROVIDER_NEUTRAL_DEPLOYMENT.md §11, BF-14.2c). An app built with the
// template library's accounts block has no administrator until someone makes
// the first one with the app's private setup token, which the app reads from
// BOOTSTRAP_TOKEN; without one, nobody can sign in. Each Node app on a target
// gets its own token: ABCP makes it the first time the app starts, keeps it
// with the app's database record, and sends it on every start on the start
// script's input, which keeps it in setup.env beside database.env. It goes
// with the app's data. The administrator reads it through ABCP to open the
// app's first-admin form; the app makes a first administrator only while it
// has none.

var setupTokenPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

// setupToken answers the app's setup token, making it the first time.
func (m *Manager) setupToken(key, site string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read(key)
	if err != nil {
		return "", err
	}
	d, ok := findDatabase(r, site)
	if !ok {
		d = databaseRecord{Site: site, Name: databaseName(site), CreatedAt: m.stamp()}
	}
	if setupTokenPattern.MatchString(d.SetupToken) {
		return d.SetupToken, nil
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	d.SetupToken = hex.EncodeToString(b)
	setDatabase(&r, d)
	if err := m.write(r); err != nil {
		return "", err
	}
	return d.SetupToken, nil
}

// startApp starts a site's app with its database and its setup token, and,
// while relays are on, the address of the front door's inside listener
// (BF-08b2), where its own server asks Autobuild. A WordPress website starts
// with its address and its administrator's instead (wpsites.go).
func (m *Manager) startApp(ctx context.Context, session remote, key, site, sandbox string) (string, error) {
	m.mu.Lock()
	r, err := m.read(key)
	m.mu.Unlock()
	if err == nil && wpData(r, site) {
		return m.startWebsite(ctx, session, key, site, sandbox)
	}
	token, err := m.setupToken(key, site)
	if err != nil {
		return "", errStep("no setup token could be kept")
	}
	m.mu.Lock()
	relay := ""
	if m.relayHost != "" {
		relay = "-e AUTOBUILD_RELAY=" + insideRelay + " "
	}
	m.mu.Unlock()
	return run(ctx, session, fill(startAppScript, "N", site, "RUNTIME", runtimeFlag(sandbox), "RELAY", relay), strings.NewReader(token+"\n"))
}

// SiteSetup answers a Node app's setup token and the host its first-admin
// form is on. An app that has not started since ABCP keeps setup tokens has
// none yet: starting it again makes one.
func (m *Manager) SiteSetup(_ context.Context, key, site string) (serviceapi.SiteSetupV1, error) {
	if !siteNamePattern.MatchString(site) {
		return serviceapi.SiteSetupV1{}, serviceapi.ErrTargetInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.read(key)
	if err != nil {
		return serviceapi.SiteSetupV1{}, err
	}
	s, ok := findSite(r, site)
	if !ok || !served(s) {
		return serviceapi.SiteSetupV1{}, serviceapi.ErrSiteUnknown
	}
	if siteKind(s) != kindNode {
		return serviceapi.SiteSetupV1{}, serviceapi.ErrTargetInvalid
	}
	d, ok := findDatabase(r, site)
	if !ok || !setupTokenPattern.MatchString(d.SetupToken) {
		return serviceapi.SiteSetupV1{}, serviceapi.ErrSetupPending
	}
	return serviceapi.SiteSetupV1{SchemaVersion: serviceapi.SiteSetupSchemaV1, Target: key, Site: site, Host: s.Host, Token: d.SetupToken}, nil
}

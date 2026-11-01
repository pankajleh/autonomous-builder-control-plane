package remotetarget

import (
	"context"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/strictjson"
)

// Each bought server's backups kept in its own legal area (Repo C design note HOSTING_REGIONS_AND_DATA_LAWS.md §9,
// decision 3 A, R2; docs/plans/server-backups-areas.md). The server backups' settings file (--server-backups-file)
// names, for each legal area that has one, the bucket its servers' copies go to, and maps every provider's region to its
// legal area, so ABCP needs nothing of Repo C's. A bought server names its provider and region when it is created, and
// is refused (ErrTargetBackupsArea) unless its area has a bucket; each of its backups is then copied there alone, through
// the one-object upload address signed with the hosted backups' key (offsite.go). A server whose area loses its bucket
// later keeps its backups on itself and says why; its copies are never sent to another area. The file is read afresh
// at each backup, so a new area needs no restart, and it is used only while private (mode 0600) and valid.

// areaBucket is where one legal area's copies go: a bucket in an AWS region of that same area, under a prefix the hosted
// backups' key may write.
type areaBucket struct {
	Bucket string `json:"bucket"`
	Region string `json:"region"`
	Prefix string `json:"prefix"`
}

// areaSettings is the server backups' settings file.
type areaSettings struct {
	// Areas are the legal areas that have a bucket, by code ("IN", "UK", "EEA").
	Areas map[string]areaBucket `json:"areas"`
	// Regions maps each provider's region to its legal area: {"aws": {"eu-west-2": "UK"}, "digitalocean": {"lon1": "UK"}}.
	Regions map[string]map[string]string `json:"regions"`
	// Unplaced is the legal area of a bought server created before R2, which names no region: every one was sold in
	// India. Without it such a server's backups stay on it.
	Unplaced string `json:"unplaced,omitempty"`
}

// areasMaxBytes bounds the settings file: every region of every provider is a few kilobytes.
const areasMaxBytes = 32 << 10

// Why a bought server's backup is not copied: stable words, recorded on the backup and said in the step.
const (
	whyNoAreas      = "the backups' areas are not set"
	whyNoRegion     = "its region is not known"
	whyNoLegalArea  = "its region has no legal area"
	whyNoAreaBucket = "no bucket in its legal area"
)

var (
	areaPattern     = regexp.MustCompile(`^[A-Z]{2,3}$`)
	providerPattern = regexp.MustCompile(`^[a-z]{1,16}$`)
	placePattern    = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	// prefixPattern keeps every copy under hosted/, the only prefix the hosted backups' key may write.
	prefixPattern = regexp.MustCompile(`^hosted/([a-z0-9-]+/)*$`)
)

// SetAreasFile names the server backups' private settings file (mode 0600); empty, a bought server is refused and its
// backups stay on it.
func (m *Manager) SetAreasFile(path string) {
	m.mu.Lock()
	m.areasFile = path
	m.mu.Unlock()
}

// AreasWithBuckets names the legal areas whose bought servers' backups have a bucket now, for the controller's start-up
// line ("IN,UK"), or "none" when the settings file is not given, not private or not valid. Read afresh, as at each backup.
func (m *Manager) AreasWithBuckets() string {
	s, ok := m.readAreas()
	if !ok || len(s.Areas) == 0 {
		return "none"
	}
	areas := make([]string, 0, len(s.Areas))
	for area := range s.Areas {
		areas = append(areas, area)
	}
	sort.Strings(areas)
	return strings.Join(areas, ",")
}

// readAreas reads the settings file afresh: a regular private file, bounded, strict and consistent, or nothing.
func (m *Manager) readAreas() (areaSettings, bool) {
	m.mu.Lock()
	path := m.areasFile
	m.mu.Unlock()
	if path == "" {
		return areaSettings{}, false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > areasMaxBytes {
		return areaSettings{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return areaSettings{}, false
	}
	var s areaSettings
	if strictjson.Decode(data, &s) != nil || !s.valid() {
		return areaSettings{}, false
	}
	return s, true
}

// valid checks every name, and that each area's bucket is in an AWS region of that same area: a file that would send
// one area's copies into another is not used at all.
func (s areaSettings) valid() bool {
	if len(s.Regions) == 0 || s.Unplaced != "" && !areaPattern.MatchString(s.Unplaced) {
		return false
	}
	for provider, places := range s.Regions {
		if !providerPattern.MatchString(provider) || len(places) == 0 {
			return false
		}
		for place, area := range places {
			if !placePattern.MatchString(place) || !areaPattern.MatchString(area) {
				return false
			}
		}
	}
	for area, b := range s.Areas {
		if !areaPattern.MatchString(area) || !bucketPattern.MatchString(b.Bucket) || strings.Contains(b.Bucket, "..") ||
			!regionPattern.MatchString(b.Region) || !prefixPattern.MatchString(b.Prefix) || s.Regions["aws"][b.Region] != area {
			return false
		}
	}
	return true
}

// bucketFor answers the bucket of a bought server's legal area and that area, or why its backups cannot be copied.
func (m *Manager) bucketFor(provider, region string) (areaBucket, string, string) {
	s, ok := m.readAreas()
	if !ok {
		return areaBucket{}, "", whyNoAreas
	}
	area := s.Unplaced
	if provider != "" || region != "" {
		area = s.Regions[provider][region]
		if area == "" {
			return areaBucket{}, "", whyNoLegalArea
		}
	} else if area == "" {
		return areaBucket{}, "", whyNoRegion
	}
	b, ok := s.Areas[area]
	if !ok {
		return areaBucket{}, area, whyNoAreaBucket + " (" + area + ")"
	}
	return b, area, ""
}

// checkBoughtArea refuses a bought server that does not name its provider and region, or whose legal area has no
// bucket; a server that is not bought names neither.
func (m *Manager) checkBoughtArea(create serviceapi.TargetCreateV1) error {
	if !create.BackupsOffServer {
		if create.Provider != "" || create.Region != "" {
			return serviceapi.ErrTargetInvalid
		}
		return nil
	}
	if !providerPattern.MatchString(create.Provider) || !placePattern.MatchString(create.Region) {
		return serviceapi.ErrTargetInvalid
	}
	if _, _, why := m.bucketFor(create.Provider, create.Region); why != "" {
		return serviceapi.ErrTargetBackupsArea
	}
	return nil
}

// copier answers how a new backup of a target's site and earlier ones are copied off it, or nil when none is: a bought
// server's through its legal area's bucket alone (or a refusal that says why, never another area's bucket), and any
// other target's by its own role when offsite.json names a bucket for it (host 1).
func (m *Manager) copier(ctx context.Context, session remote, key, site string) func(backup string) string {
	m.mu.Lock()
	r, err := m.read(key)
	m.mu.Unlock()
	if err != nil {
		return nil
	}
	if !r.BackupsOffServer {
		if t, ok := m.offsiteFor(key); ok {
			return func(backup string) string { return m.copyOffsite(ctx, session, key, site, backup, t) }
		}
		return nil
	}
	s, ok := m.uploadsFor(key)
	if !ok {
		// Without the hosted backups' key nothing is tried, as before.
		return nil
	}
	b, _, why := m.bucketFor(r.Provider, r.Region)
	if why != "" {
		return func(backup string) string {
			return m.noteOffsite(key, site, backup, serviceapi.BackupOffsiteV1{At: m.stamp(), Detail: why})
		}
	}
	s.Bucket, s.Region, s.Prefix = b.Bucket, b.Region, b.Prefix
	return func(backup string) string { return m.copyUploaded(ctx, session, key, site, backup, s) }
}

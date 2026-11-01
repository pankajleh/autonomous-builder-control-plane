//go:build linux

package remotetarget

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// R2 (Repo C HOSTING_REGIONS_AND_DATA_LAWS.md, decision 3 A): each bought server's backups are kept in its own legal
// area's bucket, and never sent to another area.

const (
	indiaBucket = "autobuild-bf14-backups-123456789012"
	ukBucket    = "autobuild-bf14-backups-uk-123456789012"
	areasIN     = `"IN":{"bucket":"` + indiaBucket + `","region":"ap-south-1","prefix":"hosted/servers/"}`
	areasUK     = `"UK":{"bucket":"` + ukBucket + `","region":"eu-west-2","prefix":"hosted/servers/"}`
	areaRegions = `"regions":{"aws":{"ap-south-1":"IN","eu-west-2":"UK","eu-central-1":"EEA"},"digitalocean":{"blr1":"IN","lon1":"UK","fra1":"EEA"}}`
)

// bucketCall is one request a fake bucket answered: its method and the region its signature was scoped to.
type bucketCall struct{ method, region string }

// fakeBuckets are one fake S3 per bucket, each checking every signature as fakeAWS does, and what each was asked.
type fakeBuckets struct {
	mu    sync.Mutex
	s3    map[string]*fakeAWS
	urls  map[string]string
	calls map[string][]bucketCall
}

func (b *fakeBuckets) asked(bucket string) []bucketCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]bucketCall(nil), b.calls[bucket]...)
}

func (b *fakeBuckets) total() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, c := range b.calls {
		n += len(c)
	}
	return n
}

// buckets gives the fixture's target the hosted backups' key and a fake S3 for each named bucket.
func (f *deployFixture) buckets(t *testing.T, names ...string) *fakeBuckets {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is needed to run the copy as a server does")
	}
	b := &fakeBuckets{s3: map[string]*fakeAWS{}, urls: map[string]string{}, calls: map[string][]bucketCall{}}
	for _, name := range names {
		name := name
		aws := &fakeAWS{t: t, objects: map[string][]byte{}}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scope := r.URL.Query().Get("X-Amz-Credential")
			if scope == "" {
				scope, _, _ = strings.Cut(strings.TrimPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential="), ",")
			}
			parts := strings.Split(scope, "/")
			region := ""
			if len(parts) == 5 {
				region = parts[2]
			}
			b.mu.Lock()
			b.calls[name] = append(b.calls[name], bucketCall{r.Method, region})
			b.mu.Unlock()
			aws.ServeHTTP(w, r)
		}))
		t.Cleanup(srv.Close)
		b.s3[name], b.urls[name] = aws, srv.URL
	}
	f.m.bucketEndpoint = func(bucket, _ string) string {
		if u, ok := b.urls[bucket]; ok {
			return u
		}
		return "http://127.0.0.1:1"
	}
	f.m.metadataURL = "http://127.0.0.1:1"
	settings := filepath.Join(t.TempDir(), "hosted-offsite.json")
	if err := os.WriteFile(settings, []byte(`{"bucket":"`+indiaBucket+`","region":"ap-south-1","access_key_id":"`+fakeUploadAccess+
		`","secret_access_key":"`+fakeUploadSecret+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.m.SetUploadsFile(settings)
	return b
}

// placed makes the fixture's target a server bought at provider, in region (as Repo C creates one from R2 on).
func (f *deployFixture) placed(t *testing.T, provider, region string) {
	t.Helper()
	r, err := f.m.read(f.key)
	if err != nil {
		t.Fatal(err)
	}
	r.BackupsOffServer, r.Provider, r.Region = true, provider, region
	if err := f.m.write(r); err != nil {
		t.Fatal(err)
	}
}

// A London server's backups go to the UK's bucket, signed for London, and nothing reaches India's. When the UK's
// bucket is taken out of the settings, its backups stay on the server and say why, and no bucket is asked anything;
// once it is back, the next backup copies itself and the earlier ones, at most three of them, there.
func TestABoughtServersBackupGoesToItsLegalAreasBucketAlone(t *testing.T) {
	f := newDeployFixture(t)
	f.deploy(f.putNode("one"), "crm", testHost, "req-crm-first")
	f.rows("app_crm", `{"public.members":3}`)
	b := f.buckets(t, indiaBucket, ukBucket)
	f.placed(t, "digitalocean", "lon1")
	settings := f.areas(t, `{"areas":{`+areasIN+`,`+areasUK+`},`+areaRegions+`,"unplaced":"IN"}`)

	taken := f.backup("crm", "req-backup-1")
	object := "/hosted/servers/" + f.key + "/crm/" + taken.BackupID + ".dump"
	dump, _ := os.ReadFile(filepath.Join(f.remote.home, "autobuild/backups/crm", taken.BackupID+".dump"))
	if len(dump) == 0 || string(b.s3[ukBucket].objects[object]) != string(dump) {
		t.Fatalf("the UK's bucket holds %q, the dump is %q", b.s3[ukBucket].objects[object], dump)
	}
	if got := f.backupOf("crm", taken.BackupID).Offsite; got == nil || !got.OK || got.Detail != "s3://"+ukBucket+object {
		t.Fatalf("offsite = %+v", got)
	}
	if len(b.asked(indiaBucket)) != 0 {
		t.Fatalf("India's bucket was asked %v", b.asked(indiaBucket))
	}
	if got := b.asked(ukBucket); len(got) != 2 || got[0] != (bucketCall{http.MethodPut, "eu-west-2"}) || got[1] != (bucketCall{http.MethodHead, "eu-west-2"}) {
		t.Fatalf("the UK's bucket was asked %v", got)
	}

	// The UK loses its bucket: nothing is copied, nothing is asked, and the backup says why.
	if err := os.WriteFile(settings, []byte(`{"areas":{`+areasIN+`},`+areaRegions+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	asked := b.total()
	var kept []string
	for i, request := range []string{"req-backup-2", "req-backup-3", "req-backup-4", "req-backup-5"} {
		f.now = f.now.Add(time.Duration(i+1) * time.Minute)
		backup := f.backup("crm", request)
		got := f.backupOf("crm", backup.BackupID).Offsite
		if got == nil || got.OK || got.Detail != "no bucket in its legal area (UK)" {
			t.Fatalf("without the UK's bucket = %+v", got)
		}
		kept = append(kept, backup.BackupID)
	}
	if b.total() != asked {
		t.Fatalf("a bucket was asked while the UK had none: %v %v", b.asked(indiaBucket), b.asked(ukBucket))
	}
	_, op := f.deploy(f.putNode("two"), "crm", testHost, "req-crm-second")
	if detail := stepOf(op, "back_up", "").Detail; op.State != opSucceeded || !strings.HasSuffix(detail, "; kept on the server only: no bucket in its legal area (UK)") {
		t.Fatalf("update = %+v", op)
	}
	if b.total() != asked || len(b.asked(indiaBucket)) != 0 {
		t.Fatal("an update's backup was copied to another area")
	}

	// The bucket is back: the next backup copies itself and the three latest not copied, and asks nothing more.
	if err := os.WriteFile(settings, []byte(`{"areas":{`+areasIN+`,`+areasUK+`},`+areaRegions+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	asked = b.total()
	f.now = f.now.Add(time.Hour)
	last := f.backup("crm", "req-backup-6")
	if got := f.backupOf("crm", last.BackupID).Offsite; got == nil || !got.OK {
		t.Fatalf("the newest after the bucket came back = %+v", got)
	}
	if b.total()-asked != 4*2 {
		t.Fatalf("one backup asked %d times, not 4 copies of 2 requests", b.total()-asked)
	}
	if got := f.backupOf("crm", kept[0]).Offsite; got == nil || got.OK {
		t.Fatalf("the fourth earlier one was copied too: %+v", got)
	}
	for _, c := range b.asked(ukBucket) {
		if c.region != "eu-west-2" {
			t.Fatalf("a request to the UK's bucket was signed for %s", c.region)
		}
	}
	if len(b.asked(indiaBucket)) != 0 {
		t.Fatal("India's bucket was asked")
	}
}

// An Indian server, and one bought before R2 (no region: every one was sold in India), keep India's bucket and its
// place, so their earlier copies stay where they were; without unplaced, one bought before R2 copies nothing.
func TestIndiasServersKeepTheirBucketAndOnesBoughtBeforeR2AreIndias(t *testing.T) {
	f := newDeployFixture(t)
	f.deploy(f.putNode("one"), "crm", testHost, "req-crm-first")
	f.rows("app_crm", `{"public.members":3}`)
	b := f.buckets(t, indiaBucket, ukBucket)
	f.placed(t, "aws", "ap-south-1")
	settings := f.areas(t, `{"areas":{`+areasIN+`,`+areasUK+`},`+areaRegions+`,"unplaced":"IN"}`)

	mumbai := f.backup("crm", "req-backup-1")
	object := "/hosted/servers/" + f.key + "/crm/" + mumbai.BackupID + ".dump"
	if got := f.backupOf("crm", mumbai.BackupID).Offsite; got == nil || !got.OK || got.Detail != "s3://"+indiaBucket+object || len(b.s3[indiaBucket].objects[object]) == 0 {
		t.Fatalf("an Indian server's copy = %+v", got)
	}
	for _, c := range b.asked(indiaBucket) {
		if c.region != "ap-south-1" {
			t.Fatalf("India's bucket was asked for %s", c.region)
		}
	}

	// Bought before R2: no provider, no region.
	f.placed(t, "", "")
	f.now = f.now.Add(time.Minute)
	before := f.backup("crm", "req-backup-2")
	if got := f.backupOf("crm", before.BackupID).Offsite; got == nil || !got.OK || got.Detail != "s3://"+indiaBucket+"/hosted/servers/"+f.key+"/crm/"+before.BackupID+".dump" {
		t.Fatalf("a server bought before R2 = %+v", got)
	}
	if err := os.WriteFile(settings, []byte(`{"areas":{`+areasIN+`},`+areaRegions+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	asked := b.total()
	f.now = f.now.Add(time.Minute)
	unknown := f.backup("crm", "req-backup-3")
	if got := f.backupOf("crm", unknown.BackupID).Offsite; got == nil || got.OK || got.Detail != "its region is not known" || b.total() != asked {
		t.Fatalf("without unplaced = %+v", got)
	}
	// The earlier copies are as they were: listed, copied, at India's bucket.
	for _, id := range []string{mumbai.BackupID, before.BackupID} {
		if got := f.backupOf("crm", id).Offsite; got == nil || !got.OK || !strings.HasPrefix(got.Detail, "s3://"+indiaBucket+"/hosted/servers/") {
			t.Fatalf("%s = %+v", id, got)
		}
	}
	if len(b.asked(ukBucket)) != 0 {
		t.Fatal("the UK's bucket was asked")
	}
}

// A bought server names where it is and is refused, before anything is made for it, unless its region's legal area
// has a bucket; a server that is not bought names nowhere.
func TestABoughtServerIsRefusedWhenItsLegalAreaHasNoBucket(t *testing.T) {
	f := newDeployFixture(t)
	ctx := context.Background()
	create := func(key string, c serviceapi.TargetCreateV1) error {
		c.Address = "13.232.1.2"
		_, err := f.m.CreateTarget(ctx, key, c)
		return err
	}
	bought := func(provider, region string) serviceapi.TargetCreateV1 {
		return serviceapi.TargetCreateV1{BackupsOffServer: true, Provider: provider, Region: region}
	}
	// No settings file at all: every bought server is refused.
	if err := create("srv-0000000000a1", bought("digitalocean", "blr1")); !errors.Is(err, serviceapi.ErrTargetBackupsArea) {
		t.Fatalf("without the settings = %v", err)
	}
	f.areas(t, `{"areas":{`+areasIN+`},`+areaRegions+`}`)
	for _, c := range []struct {
		name   string
		create serviceapi.TargetCreateV1
		want   error
	}{
		{"London, no UK bucket", bought("aws", "eu-west-2"), serviceapi.ErrTargetBackupsArea},
		{"DigitalOcean London", bought("digitalocean", "lon1"), serviceapi.ErrTargetBackupsArea},
		{"a region no area has", bought("aws", "xx-nowhere-1"), serviceapi.ErrTargetBackupsArea},
		{"a provider no area has", bought("hostinger", "mum2"), serviceapi.ErrTargetBackupsArea},
		{"bought, nowhere named", serviceapi.TargetCreateV1{BackupsOffServer: true}, serviceapi.ErrTargetInvalid},
		{"bought, no region", bought("aws", ""), serviceapi.ErrTargetInvalid},
		{"a region not a region's name", bought("aws", "eu west 2"), serviceapi.ErrTargetInvalid},
		{"a provider not a provider's name", bought("AWS", "ap-south-1"), serviceapi.ErrTargetInvalid},
		{"own server with a region", serviceapi.TargetCreateV1{Provider: "aws", Region: "ap-south-1"}, serviceapi.ErrTargetInvalid},
	} {
		if err := create("srv-0000000000a2", c.create); !errors.Is(err, c.want) {
			t.Errorf("%s = %v, want %v", c.name, err, c.want)
		}
	}
	if list, _ := f.m.Targets(ctx); len(list.Targets) != 1 {
		t.Fatalf("a refused server was kept: %+v", list.Targets)
	}
	for _, c := range []struct{ key, provider, region string }{{"srv-0000000000b1", "aws", "ap-south-1"}, {"srv-0000000000b2", "digitalocean", "blr1"}} {
		target, err := f.m.CreateTarget(ctx, c.key, serviceapi.TargetCreateV1{Address: "13.232.1.2", BackupsOffServer: true, Provider: c.provider, Region: c.region})
		if err != nil || target.Provider != c.provider || target.Region != c.region || !target.BackupsOffServer {
			t.Fatalf("%s/%s = %+v %v", c.provider, c.region, target, err)
		}
	}
	// A person's own server names nowhere and needs no settings.
	f.m.SetAreasFile("")
	if target, err := f.m.CreateTarget(ctx, "srv-0000000000c1", serviceapi.TargetCreateV1{Address: "13.232.1.2"}); err != nil || target.Provider != "" || target.Region != "" {
		t.Fatalf("an own server = %+v %v", target, err)
	}
}

// The settings are used only while private, bounded and consistent: a bucket in another area's region, a prefix the key
// may not write or an unknown field makes the whole file unused, so nothing is copied rather than copied astray.
func TestServerBackupsSettingsAreUsedOnlyWhilePrivateAndConsistent(t *testing.T) {
	f := newDeployFixture(t)
	if got := f.m.AreasWithBuckets(); got != "none" {
		t.Fatalf("without a file = %s", got)
	}
	path := f.areas(t, `{"areas":{`+areasIN+`,`+areasUK+`},`+areaRegions+`,"unplaced":"IN"}`)
	if got := f.m.AreasWithBuckets(); got != "IN,UK" {
		t.Fatalf("areas = %s", got)
	}
	if b, area, why := f.m.bucketFor("digitalocean", "lon1"); why != "" || area != "UK" || b.Bucket != ukBucket || b.Region != "eu-west-2" {
		t.Fatalf("lon1 = %+v %s %s", b, area, why)
	}
	if _, area, why := f.m.bucketFor("digitalocean", "fra1"); area != "EEA" || why != "no bucket in its legal area (EEA)" {
		t.Fatalf("fra1 = %s %s", area, why)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, why := f.m.bucketFor("digitalocean", "lon1"); why != whyNoAreas || f.m.AreasWithBuckets() != "none" {
		t.Fatalf("a file others may read was used: %s", why)
	}
	for name, settings := range map[string]string{
		"the UK's bucket in Mumbai": `{"areas":{"UK":{"bucket":"` + ukBucket + `","region":"ap-south-1","prefix":"hosted/servers/"}},` + areaRegions + `}`,
		"a bucket in no region":     `{"areas":{"UK":{"bucket":"` + ukBucket + `","region":"eu-west-9","prefix":"hosted/servers/"}},` + areaRegions + `}`,
		"a prefix outside hosted/":  `{"areas":{"UK":{"bucket":"` + ukBucket + `","region":"eu-west-2","prefix":"servers/"}},` + areaRegions + `}`,
		"a prefix climbing out":     `{"areas":{"UK":{"bucket":"` + ukBucket + `","region":"eu-west-2","prefix":"hosted/../"}},` + areaRegions + `}`,
		"a bucket not a bucket":     `{"areas":{"UK":{"bucket":"Bad..Name","region":"eu-west-2","prefix":"hosted/servers/"}},` + areaRegions + `}`,
		"an unknown field":          `{"areas":{` + areasUK + `},` + areaRegions + `,"default":"IN"}`,
		"no regions":                `{"areas":{` + areasUK + `}}`,
		"an area not an area":       `{"areas":{"uk":{"bucket":"` + ukBucket + `","region":"eu-west-2","prefix":"hosted/servers/"}},` + areaRegions + `}`,
		"unplaced not an area":      `{"areas":{` + areasUK + `},` + areaRegions + `,"unplaced":"india"}`,
		"a key twice":               `{"areas":{` + areasUK + `,` + areasUK + `},` + areaRegions + `}`,
	} {
		if err := os.WriteFile(path, []byte(settings), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, why := f.m.bucketFor("digitalocean", "lon1"); why != whyNoAreas {
			t.Errorf("%s was used: %s", name, why)
		}
	}
	if err := os.WriteFile(path, []byte(`{"areas":{`+areasIN+`},`+areaRegions+`,"pad":"`+strings.Repeat("x", areasMaxBytes)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, why := f.m.bucketFor("aws", "ap-south-1"); why != whyNoAreas {
		t.Fatalf("an oversized file was used: %s", why)
	}
}

// The file docs/plans/server-backups.example.json is the one the host writes (with the account's id): valid, with a bucket
// for each legal area of the first regions sold, each in its own area, and every region of Repo C's list in its area.
func TestTheExampleSettingsKeepEachRegionsBackupsInItsArea(t *testing.T) {
	f := newDeployFixture(t)
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "plans", "server-backups.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.areas(t, string(data))
	if got := f.m.AreasWithBuckets(); got != "AU,BR,CA,EEA,IN,JP,KR,SG,UK,US" {
		t.Fatalf("areas = %s", got)
	}
	for _, c := range []struct{ provider, region, area, bucketIn string }{
		{"aws", "ap-south-1", "IN", "ap-south-1"}, {"digitalocean", "blr1", "IN", "ap-south-1"}, {"hostinger", "mum2", "IN", "ap-south-1"},
		{"aws", "eu-west-2", "UK", "eu-west-2"}, {"digitalocean", "lon1", "UK", "eu-west-2"},
		{"aws", "eu-west-1", "EEA", "eu-central-1"}, {"aws", "eu-west-3", "EEA", "eu-central-1"}, {"aws", "eu-north-1", "EEA", "eu-central-1"},
		{"digitalocean", "ams3", "EEA", "eu-central-1"}, {"digitalocean", "fra1", "EEA", "eu-central-1"},
		{"aws", "us-west-2", "US", "us-east-1"}, {"digitalocean", "sfo3", "US", "us-east-1"}, {"digitalocean", "atl1", "US", "us-east-1"},
		{"digitalocean", "tor1", "CA", "ca-central-1"}, {"aws", "sa-east-1", "BR", "sa-east-1"},
		{"aws", "ap-northeast-3", "JP", "ap-northeast-1"}, {"aws", "ap-northeast-2", "KR", "ap-northeast-2"},
		{"digitalocean", "sgp1", "SG", "ap-southeast-1"}, {"digitalocean", "syd1", "AU", "ap-southeast-2"},
	} {
		b, area, why := f.m.bucketFor(c.provider, c.region)
		if why != "" || area != c.area || b.Region != c.bucketIn || b.Prefix != "hosted/servers/" {
			t.Errorf("%s %s = %+v %s %s", c.provider, c.region, b, area, why)
		}
	}
	// The regions turned on later have their area and no bucket yet: refused, never sent elsewhere.
	for _, c := range []struct{ region, area string }{{"eu-central-2", "CH"}, {"ap-south-2", "IN"}, {"me-central-1", "AE"}, {"ap-southeast-4", "AU"}} {
		_, area, why := f.m.bucketFor("aws", c.region)
		if area != c.area || (c.area != "IN" && c.area != "AU" && why != "no bucket in its legal area ("+c.area+")") {
			t.Errorf("%s = %s %s", c.region, area, why)
		}
	}
	if b, _, _ := f.m.bucketFor("aws", "ap-south-1"); b.Bucket != indiaBucket {
		t.Fatalf("India's bucket = %s", b.Bucket)
	}
}

// A restore on a bought server puts its own backup back from the server, as before, and the backup it takes first is
// copied to its legal area's bucket alone.
func TestARestoreOnABoughtServerCopiesItsFirstBackupToItsAreaOnly(t *testing.T) {
	f := newDeployFixture(t)
	f.deploy(f.putNode("one"), "crm", testHost, "req-crm-first")
	f.rows("app_crm", `{"public.members":3}`)
	b := f.buckets(t, indiaBucket, ukBucket)
	f.placed(t, "aws", "eu-west-2")
	f.areas(t, `{"areas":{`+areasIN+`,`+areasUK+`},`+areaRegions+`}`)
	taken := f.backup("crm", "req-backup-1")
	f.rows("app_crm", `{"public.members":5}`)

	plan, err := f.m.CreatePlan(context.Background(), f.key, serviceapi.PlanCreateV1{Restore: &serviceapi.PlanRestoreV1{Site: "crm", Backup: taken.BackupID}})
	if err != nil {
		t.Fatal(err)
	}
	op := f.finished(f.m.Apply(context.Background(), f.key, serviceapi.ApplyV1{PlanID: plan.PlanID, PlanHash: plan.Hash, RequestID: "req-restore-1"}))
	if op.State != opSucceeded || !strings.HasSuffix(stepOf(op, "back_up", "").Detail, "; copied off the server") || f.held("app_crm") != `{"public.members":3}` {
		t.Fatalf("restore = %+v, held %s", op, f.held("app_crm"))
	}
	list, _ := f.m.Backups(context.Background(), f.key, "crm")
	if len(list.Backups) != 2 || list.Backups[1].Reason != reasonBeforeRestore {
		t.Fatalf("backups = %+v", list.Backups)
	}
	for _, backup := range list.Backups {
		if backup.Offsite == nil || !backup.Offsite.OK || !strings.HasPrefix(backup.Offsite.Detail, "s3://"+ukBucket+"/hosted/servers/"+f.key+"/crm/") {
			t.Fatalf("%s = %+v", backup.BackupID, backup.Offsite)
		}
	}
	if len(b.asked(indiaBucket)) != 0 {
		t.Fatal("India's bucket was asked")
	}
}

// Pruning a bought server's backups removes them from the server as before and leaves their copies to the bucket's own
// 30-day rule: no bucket is asked to delete anything.
func TestPruningABoughtServersBackupsLeavesTheirCopiesToTheBucketsRule(t *testing.T) {
	f := newDeployFixture(t)
	f.deploy(f.putNode("one"), "crm", testHost, "req-crm-first")
	f.rows("app_crm", `{"public.members":3}`)
	b := f.buckets(t, ukBucket)
	f.placed(t, "digitalocean", "lon1")
	f.areas(t, `{"areas":{`+areasUK+`},`+areaRegions+`}`)
	old := f.backup("crm", "req-backup-1")
	f.now = f.now.Add(8 * 24 * time.Hour)
	f.backup("crm", "req-backup-2")
	if f.backupOf("crm", old.BackupID).BackupID != "" {
		t.Fatal("a backup older than 7 days is still listed")
	}
	if _, err := os.Stat(filepath.Join(f.remote.home, "autobuild/backups/crm", old.BackupID+".dump")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the pruned backup is still on the server: %v", err)
	}
	if _, ok := b.s3[ukBucket].objects["/hosted/servers/"+f.key+"/crm/"+old.BackupID+".dump"]; !ok {
		t.Fatal("the pruned backup's copy is gone")
	}
	for _, c := range b.asked(ukBucket) {
		if c.method != http.MethodPut && c.method != http.MethodHead {
			t.Fatalf("the bucket was asked to %s", c.method)
		}
	}
}

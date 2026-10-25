//go:build linux

package remotetarget

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// fakeAWS answers as EC2's instance metadata (IMDSv2) and as S3 does for one bucket: it checks every S3 request's
// Signature Version 4 with the role's secret, and keeps what was put.
type fakeAWS struct {
	t       *testing.T
	mu      sync.Mutex
	objects map[string][]byte
	refuse  bool
	tokens  int
}

const (
	fakeAccess = "ASIAFAKEACCESSKEY01"
	fakeSecret = "fake/secret/for+the/role"
	fakeToken  = "fake-session-token"
)

func (a *fakeAWS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPut && r.URL.Path == "/latest/api/token":
		if r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds") == "" {
			http.Error(w, "no ttl", http.StatusBadRequest)
			return
		}
		a.mu.Lock()
		a.tokens++
		a.mu.Unlock()
		_, _ = io.WriteString(w, "imds-token")
	case strings.HasPrefix(r.URL.Path, "/latest/meta-data/iam/security-credentials/"):
		if r.Header.Get("X-aws-ec2-metadata-token") != "imds-token" {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/latest/meta-data/iam/security-credentials/" {
			_, _ = io.WriteString(w, "autobuild-bf14-host\n")
			return
		}
		_, _ = io.WriteString(w, `{"Code":"Success","AccessKeyId":"`+fakeAccess+`","SecretAccessKey":"`+fakeSecret+`","Token":"`+fakeToken+`"}`)
	default:
		body, _ := io.ReadAll(r.Body)
		if !a.signed(r, body) && !a.presigned(r) && !a.signedByUploads(r) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "<Error><Code>SignatureDoesNotMatch</Code></Error>")
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			if a.refuse {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, "<Error><Code>AccessDenied</Code></Error>")
				return
			}
			a.objects[r.URL.Path] = body
		case http.MethodHead:
			data, ok := a.objects[r.URL.Path]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		}
	}
}

// signed checks a request's Signature Version 4 as S3 does, the payload hash against the body as well.
func (a *fakeAWS) signed(r *http.Request, body []byte) bool {
	auth := r.Header.Get("Authorization")
	payload := r.Header.Get("X-Amz-Content-Sha256")
	if sum := sha256.Sum256(body); payload != hex.EncodeToString(sum[:]) || r.Header.Get("X-Amz-Security-Token") != fakeToken {
		return false
	}
	var credential, signedNames, signature string
	for _, part := range strings.Split(strings.TrimPrefix(auth, "AWS4-HMAC-SHA256 "), ", ") {
		k, v, _ := strings.Cut(part, "=")
		switch k {
		case "Credential":
			credential = v
		case "SignedHeaders":
			signedNames = v
		case "Signature":
			signature = v
		}
	}
	scopeParts := strings.SplitN(credential, "/", 2)
	if len(scopeParts) != 2 || scopeParts[0] != fakeAccess || signedNames != "host;x-amz-content-sha256;x-amz-date;x-amz-security-token" {
		return false
	}
	scope := scopeParts[1]
	names := strings.Split(signedNames, ";")
	sort.Strings(names)
	var headers strings.Builder
	for _, n := range names {
		v := r.Header.Get(n)
		if n == "host" {
			v = r.Host
		}
		headers.WriteString(n + ":" + v + "\n")
	}
	canonical := strings.Join([]string{r.Method, r.URL.EscapedPath(), "", headers.String(), signedNames, payload}, "\n")
	sum := sha256.Sum256([]byte(canonical))
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", r.Header.Get("X-Amz-Date"), scope, hex.EncodeToString(sum[:])}, "\n")
	key := []byte("AWS4" + fakeSecret)
	for _, part := range strings.Split(scope, "/") {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(part))
		key = mac.Sum(nil)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(toSign))
	return hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(signature))
}

// The hosted backups' key (BF-10b), which signs bought servers' upload addresses (BF-14.4d d1).
const (
	fakeUploadAccess = "AKIAFAKEUPLOADKEY001"
	fakeUploadSecret = "fakeUploadSecretForTheHostedBackups/Key+"
)

// presigned checks an upload address as S3 does: a PUT signed in its query with the hosted backups' key, the host header
// alone signed, the payload unsigned, for at most 15 minutes.
func (a *fakeAWS) presigned(r *http.Request) bool {
	q := r.URL.Query()
	if r.Method != http.MethodPut || q.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" || q.Get("X-Amz-SignedHeaders") != "host" || q.Get("X-Amz-Expires") != "900" ||
		r.Header.Get("Authorization") != "" {
		return false
	}
	credential := strings.SplitN(q.Get("X-Amz-Credential"), "/", 2)
	if len(credential) != 2 || credential[0] != fakeUploadAccess {
		return false
	}
	signature := q.Get("X-Amz-Signature")
	q.Del("X-Amz-Signature")
	canonical := strings.Join([]string{r.Method, r.URL.EscapedPath(), q.Encode(), "host:" + r.Host + "\n", "host", "UNSIGNED-PAYLOAD"}, "\n")
	return hmac.Equal([]byte(sign4(fakeUploadSecret, q.Get("X-Amz-Date"), credential[1], canonical)), []byte(signature))
}

// signedByUploads checks a request signed in its headers with the hosted backups' key (no session token).
func (a *fakeAWS) signedByUploads(r *http.Request) bool {
	prefix := "AWS4-HMAC-SHA256 Credential=" + fakeUploadAccess + "/"
	auth := r.Header.Get("Authorization")
	if r.Method != http.MethodHead || !strings.HasPrefix(auth, prefix) {
		return false
	}
	scope, rest, _ := strings.Cut(strings.TrimPrefix(auth, prefix), ", ")
	if !strings.HasPrefix(rest, "SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=") {
		return false
	}
	payload := r.Header.Get("X-Amz-Content-Sha256")
	canonical := strings.Join([]string{r.Method, r.URL.EscapedPath(), "", "host:" + r.Host + "\nx-amz-content-sha256:" + payload + "\nx-amz-date:" +
		r.Header.Get("X-Amz-Date") + "\n", "host;x-amz-content-sha256;x-amz-date", payload}, "\n")
	return hmac.Equal([]byte(sign4(fakeUploadSecret, r.Header.Get("X-Amz-Date"), scope, canonical)),
		[]byte(strings.TrimPrefix(rest, "SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=")))
}

func sign4(secret, amz, scope, canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", amz, scope, hex.EncodeToString(sum[:])}, "\n")
	key := []byte("AWS4" + secret)
	for _, part := range strings.Split(scope, "/") {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(part))
		key = mac.Sum(nil)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(toSign))
	return hex.EncodeToString(mac.Sum(nil))
}

// uploads gives the fixture's target backups_off_server and the hosted backups' settings file, with S3 faked.
func (f *deployFixture) uploads(t *testing.T) (*fakeAWS, string) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is needed to run the copy as a server does")
	}
	aws := &fakeAWS{t: t, objects: map[string][]byte{}}
	srv := httptest.NewServer(aws)
	t.Cleanup(srv.Close)
	f.m.s3Endpoint = srv.URL
	f.m.metadataURL = "http://127.0.0.1:1"
	settings := filepath.Join(t.TempDir(), "hosted-offsite.json")
	if err := os.WriteFile(settings, []byte(`{"bucket":"autobuild-bf14-backups-123456789012","region":"ap-south-1","access_key_id":"`+fakeUploadAccess+
		`","secret_access_key":"`+fakeUploadSecret+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f.m.SetUploadsFile(settings)
	r, err := f.m.read(f.key)
	if err != nil {
		t.Fatal(err)
	}
	r.BackupsOffServer = true
	if err := f.m.write(r); err != nil {
		t.Fatal(err)
	}
	return aws, settings
}

func (f *deployFixture) offsite(t *testing.T) *fakeAWS {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is needed to run the copy as a server does")
	}
	aws := &fakeAWS{t: t, objects: map[string][]byte{}}
	srv := httptest.NewServer(aws)
	t.Cleanup(srv.Close)
	f.m.metadataURL, f.m.s3Endpoint = srv.URL, srv.URL
	if err := os.WriteFile(filepath.Join(f.m.dir, "offsite.json"),
		[]byte(`{"targets":{"`+f.key+`":{"bucket":"autobuild-bf14-backups-123456789012","region":"ap-south-1"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return aws
}

func (f *deployFixture) backupOf(site, id string) serviceapi.BackupV1 {
	list, _ := f.m.Backups(context.Background(), f.key, site)
	for _, b := range list.Backups {
		if b.BackupID == id {
			return b
		}
	}
	return serviceapi.BackupV1{}
}

// BF-14.2b-2: every backup taken on a target with a bucket is also copied off the server, with the server's own
// credentials, and a copy that fails leaves the backup and is tried again with the next one.
func TestABackupIsCopiedOffTheServerWithTheServersOwnRole(t *testing.T) {
	f := newDeployFixture(t)
	f.deploy(f.putNode("one"), "crm", testHost, "req-crm-first")
	f.rows("app_crm", `{"public.members":3}`)
	aws := f.offsite(t)

	taken := f.backup("crm", "req-backup-1")
	object := "/" + f.key + "/crm/" + taken.BackupID + ".dump"
	dump, _ := os.ReadFile(filepath.Join(f.remote.home, "autobuild/backups/crm", taken.BackupID+".dump"))
	if len(dump) == 0 || string(aws.objects[object]) != string(dump) {
		t.Fatalf("copied %q, dump %q", aws.objects[object], dump)
	}
	got := f.backupOf("crm", taken.BackupID)
	if got.Offsite == nil || !got.Offsite.OK || got.Offsite.Detail != "s3://autobuild-bf14-backups-123456789012"+object {
		t.Fatalf("offsite = %+v", got.Offsite)
	}
	for _, script := range f.remote.scripts {
		if strings.Contains(script, fakeSecret) || strings.Contains(script, fakeToken) {
			t.Fatal("a credential reached a script")
		}
	}

	// S3 refuses: the backup stands, says why, and the next backup copies both.
	aws.refuse = true
	refused := f.backup("crm", "req-backup-2")
	if got := f.backupOf("crm", refused.BackupID); got.Offsite == nil || got.Offsite.OK || got.Offsite.Detail != "S3 refused the copy (403 AccessDenied)" {
		t.Fatalf("refused = %+v", got.Offsite)
	}
	aws.refuse = false
	third := f.backup("crm", "req-backup-3")
	for _, id := range []string{refused.BackupID, third.BackupID} {
		if got := f.backupOf("crm", id); got.Offsite == nil || !got.Offsite.OK {
			t.Fatalf("%s after the retry = %+v", id, got.Offsite)
		}
	}

	// The step says so: an update's backup first, copied.
	_, op := f.deploy(f.putNode("two"), "crm", testHost, "req-crm-second")
	if detail := stepOf(op, "back_up", "").Detail; op.State != opSucceeded || !strings.HasSuffix(detail, "; copied off the server") {
		t.Fatalf("update = %+v", op)
	}
}

func TestABackupStaysOnTheServerWhenItCannotBeCopied(t *testing.T) {
	f := newDeployFixture(t)
	f.deploy(f.putNode("one"), "crm", testHost, "req-crm-first")
	f.rows("app_crm", `{"public.members":3}`)

	// No bucket for this target: nothing is tried.
	plain := f.backup("crm", "req-backup-1")
	if got := f.backupOf("crm", plain.BackupID); got.Offsite != nil {
		t.Fatalf("offsite without a bucket = %+v", got.Offsite)
	}
	f.offsite(t)
	// A server with no role: the backup stands and says why.
	f.m.metadataURL = "http://127.0.0.1:1"
	b := f.backup("crm", "req-backup-2")
	if got := f.backupOf("crm", b.BackupID); got.Offsite == nil || got.Offsite.OK || got.Offsite.Detail != "the server has no role to copy with (URLError)" {
		t.Fatalf("no role = %+v", got.Offsite)
	}
	// A bucket that is not a bucket's name is ignored.
	if err := os.WriteFile(filepath.Join(f.m.dir, "offsite.json"), []byte(`{"targets":{"`+f.key+`":{"bucket":"Bad..Name","region":"ap-south-1"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.m.offsiteFor(f.key); ok {
		t.Fatal("a bad bucket name was used")
	}
}

// From 2026-10-15 08:30 UTC, when the first offsite.json was written, the list of targets, a new target and every
// night's backups failed: the list read offsite.json as a target's record. It is the folder's settings, not a target.
func TestTheOffsiteSettingsAreNotATarget(t *testing.T) {
	f := newDeployFixture(t)
	if err := os.WriteFile(filepath.Join(f.m.dir, "offsite.json"),
		[]byte(`{"targets":{"`+f.key+`":{"bucket":"autobuild-bf14-backups-123456789012","region":"ap-south-1"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := f.m.Targets(context.Background())
	if err != nil || len(list.Targets) != 1 || list.Targets[0].Key != f.key {
		t.Fatalf("targets = %+v, %v", list, err)
	}
	if _, err := f.m.Target(context.Background(), "offsite"); !errors.Is(err, serviceapi.ErrTargetUnknown) {
		t.Fatalf("offsite as a target = %v", err)
	}
	if _, err := f.m.CreateTarget(context.Background(), "offsite", serviceapi.TargetCreateV1{Address: testHost}); !errors.Is(err, serviceapi.ErrTargetInvalid) {
		t.Fatalf("a target named offsite = %v", err)
	}
}

// BF-14.4d d1: a server bought for a person copies each backup off itself to an address that may put that one object, for
// 15 minutes, signed with the hosted backups' key; ABCP reads the copy back with the key. Neither the key nor the role is
// used on the server, and a copy that fails leaves the backup and is tried again with the next one.
func TestABoughtServersBackupIsUploadedToAnAddressForThatObjectOnly(t *testing.T) {
	f := newDeployFixture(t)
	f.deploy(f.putNode("one"), "crm", testHost, "req-crm-first")
	f.rows("app_crm", `{"public.members":3}`)
	aws, settings := f.uploads(t)

	taken := f.backup("crm", "req-backup-1")
	object := "/hosted/servers/" + f.key + "/crm/" + taken.BackupID + ".dump"
	dump, _ := os.ReadFile(filepath.Join(f.remote.home, "autobuild/backups/crm", taken.BackupID+".dump"))
	if len(dump) == 0 || string(aws.objects[object]) != string(dump) {
		t.Fatalf("uploaded %q, dump %q (objects %v)", aws.objects[object], dump, len(aws.objects))
	}
	got := f.backupOf("crm", taken.BackupID)
	if got.Offsite == nil || !got.Offsite.OK || got.Offsite.Detail != "s3://autobuild-bf14-backups-123456789012"+object {
		t.Fatalf("offsite = %+v", got.Offsite)
	}
	if aws.tokens != 0 {
		t.Fatal("the server asked for the instance role's credentials")
	}
	for _, script := range f.remote.scripts {
		if strings.Contains(script, fakeUploadSecret) || strings.Contains(script, "Authorization") {
			t.Fatal("the key reached a script")
		}
	}

	// S3 refuses: the backup stands, says why, and the next backup copies both.
	aws.refuse = true
	refused := f.backup("crm", "req-backup-2")
	if got := f.backupOf("crm", refused.BackupID); got.Offsite == nil || got.Offsite.OK || got.Offsite.Detail != "S3 refused the copy (403 AccessDenied)" {
		t.Fatalf("refused = %+v", got.Offsite)
	}
	aws.refuse = false
	third := f.backup("crm", "req-backup-3")
	for _, id := range []string{refused.BackupID, third.BackupID} {
		if got := f.backupOf("crm", id); got.Offsite == nil || !got.Offsite.OK {
			t.Fatalf("%s after the retry = %+v", id, got.Offsite)
		}
	}

	// A settings file others may read, or one without a valid key, copies nothing; nor does a target that did not ask.
	if err := os.Chmod(settings, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.m.uploadsFor(f.key); ok {
		t.Fatal("a settings file others may read was used")
	}
	if err := os.WriteFile(settings, []byte(`{"bucket":"autobuild-bf14-backups-123456789012","region":"ap-south-1","access_key_id":"nope","secret_access_key":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.m.uploadsFor(f.key); ok {
		t.Fatal("a settings file without a valid key was used")
	}
	r, _ := f.m.read(f.key)
	r.BackupsOffServer = false
	_ = f.m.write(r)
	plain := f.backup("crm", "req-backup-4")
	// A target asks for it when it is created, and its record and answer keep it.
	bought, err := f.m.CreateTarget(context.Background(), "srv-00000000000b", serviceapi.TargetCreateV1{Address: "13.232.1.2", BackupsOffServer: true})
	if r, _ := f.m.read("srv-00000000000b"); err != nil || !bought.BackupsOffServer || !r.BackupsOffServer {
		t.Fatalf("a bought server = %+v %v", bought, err)
	}
	if got := f.backupOf("crm", plain.BackupID); got.Offsite != nil {
		t.Fatalf("a target that did not ask = %+v", got.Offsite)
	}
}

// The address may put the object it names and nothing else: the key and the region scope it, it expires, and changing
// its object breaks its signature.
func TestAnUploadAddressNamesOneObjectAndExpires(t *testing.T) {
	s := uploadSettings{Bucket: "autobuild-bf14-backups-123456789012", Region: "ap-south-1", AccessKeyID: fakeUploadAccess, SecretAccessKey: fakeUploadSecret}
	now := time.Date(2026, 10, 25, 15, 0, 0, 0, time.UTC)
	address := presignPut("https://autobuild-bf14-backups-123456789012.s3.ap-south-1.amazonaws.com", "hosted/servers/srv-0123456789ab/crm/bk-20261025T150000Z-9f2d.dump", s, now, uploadExpiry)
	if !uploadURL.MatchString(address) || strings.Contains(address, fakeUploadSecret) {
		t.Fatalf("address = %s", address)
	}
	u, _ := url.Parse(address)
	q := u.Query()
	if q.Get("X-Amz-Credential") != fakeUploadAccess+"/20261025/ap-south-1/s3/aws4_request" || q.Get("X-Amz-Expires") != "900" || q.Get("X-Amz-Date") != "20261025T150000Z" {
		t.Fatalf("query = %v", q)
	}
	r := httptest.NewRequest(http.MethodPut, address, nil)
	if !(&fakeAWS{}).presigned(r) {
		t.Fatal("S3 would refuse the address")
	}
	other := httptest.NewRequest(http.MethodPut, strings.Replace(address, "/crm/", "/shop/", 1), nil)
	if (&fakeAWS{}).presigned(other) {
		t.Fatal("the address put another object")
	}
}

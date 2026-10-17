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
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

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
		if !a.signed(r, body) {
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

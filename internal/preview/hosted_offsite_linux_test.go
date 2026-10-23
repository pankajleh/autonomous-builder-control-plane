package preview

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// A made-up key in the settings' shape, put together at run time so no source line looks like a real one.
var (
	offsiteTestKey    = "AKIA" + strings.Repeat("T", 16)
	offsiteTestSecret = strings.Repeat("s", 40)
)

// fakeS3 keeps objects by path, refuses puts while refuse is set, and checks every request's Signature Version 4.
type fakeS3 struct {
	mu       sync.Mutex
	objects  map[string][]byte
	refuse   bool
	requests int
	unsigned int
}

func (s *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++
	if !s.signed(r, body) {
		s.unsigned++
		w.WriteHeader(http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodPut:
		if s.refuse {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		s.objects[r.URL.Path] = body
	case http.MethodHead:
		data, ok := s.objects[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	}
}

// signed checks a request as S3 does: the payload hash against the body, then the signature over the signed headers.
func (s *fakeS3) signed(r *http.Request, body []byte) bool {
	payload := r.Header.Get("X-Amz-Content-Sha256")
	if sum := sha256.Sum256(body); payload != hex.EncodeToString(sum[:]) {
		return false
	}
	var credential, names, signature string
	for _, part := range strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 "), ", ") {
		k, v, _ := strings.Cut(part, "=")
		switch k {
		case "Credential":
			credential = v
		case "SignedHeaders":
			names = v
		case "Signature":
			signature = v
		}
	}
	access, scope, ok := strings.Cut(credential, "/")
	if !ok || access != offsiteTestKey || names != "host;x-amz-content-sha256;x-amz-date" || !strings.HasSuffix(scope, "/ap-south-1/s3/aws4_request") {
		return false
	}
	headers := "host:" + r.Host + "\nx-amz-content-sha256:" + payload + "\nx-amz-date:" + r.Header.Get("X-Amz-Date") + "\n"
	canonical := strings.Join([]string{r.Method, r.URL.EscapedPath(), "", headers, names, payload}, "\n")
	sum := sha256.Sum256([]byte(canonical))
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", r.Header.Get("X-Amz-Date"), scope, hex.EncodeToString(sum[:])}, "\n")
	key := []byte("AWS4" + offsiteTestSecret)
	for _, part := range strings.Split(scope, "/") {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(part))
		key = mac.Sum(nil)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(toSign))
	return hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(signature))
}

func offsiteFixture(t *testing.T, mode os.FileMode) (*Hosting, *hostedMemory, *hostedClock, *fakeS3, string) {
	t.Helper()
	h, rt, _, clock, _ := newHostedFixture(t)
	s3 := &fakeS3{objects: map[string][]byte{}}
	server := httptest.NewServer(s3)
	t.Cleanup(server.Close)
	h.offsiteEndpoint = func(bucket, region string) string { return server.URL + "/" + bucket + "." + region }
	h.offsiteClient = server.Client()
	settings := filepath.Join(t.TempDir(), "hosted-offsite.json")
	data, _ := json.Marshal(hostedOffsiteSettings{Bucket: "autobuild-bf14-backups-123456789012", Region: "ap-south-1", AccessKeyID: offsiteTestKey, SecretAccessKey: offsiteTestSecret})
	if err := os.WriteFile(settings, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(settings, mode); err != nil {
		t.Fatal(err)
	}
	h.SetOffsiteFile(settings)
	if _, err := h.StartHosted(context.Background(), principal(), testAuthorityDigest, "app-1", startCommand("start-1")); err != nil {
		t.Fatal(err)
	}
	awaitHosted(t, h, "app-1", "READY")
	return h, rt, clock, s3, settings
}

func TestHostedBackupsAreCopiedOffTheHostAndReadBack(t *testing.T) {
	h, rt, _, s3, _ := offsiteFixture(t, 0600)
	ctx := context.Background()
	b, err := h.BackupHosted(ctx, principal(), testAuthorityDigest, "app-1", backupCommand("backup-1", "pre-update"))
	if err != nil || serviceapi.ValidateHostedBackupV1(b, "app-1") != nil {
		t.Fatalf("backup = %+v, %v", b, err)
	}
	object := "/autobuild-bf14-backups-123456789012.ap-south-1/hosted/app-1/" + b.BackupID + ".dump"
	if b.Offsite == nil || !b.Offsite.OK || b.Offsite.Detail != "s3://autobuild-bf14-backups-123456789012/hosted/app-1/"+b.BackupID+".dump" {
		t.Fatalf("copy = %+v", b.Offsite)
	}
	if string(s3.objects[object]) != "dump-1" || s3.unsigned != 0 || s3.requests != 2 {
		t.Fatalf("bucket = %q, unsigned %d, requests %d", s3.objects[object], s3.unsigned, s3.requests)
	}
	list, _ := h.ListHostedBackups(ctx, principal(), "app-1")
	if len(list.Backups) != 1 || list.Backups[0].Offsite == nil || !list.Backups[0].Offsite.OK {
		t.Fatalf("listed = %+v", list)
	}
	encoded, _ := json.Marshal(list)
	if strings.Contains(string(encoded), offsiteTestKey) || strings.Contains(string(encoded), offsiteTestSecret) {
		t.Fatal("the key is answered")
	}
	rt.set(func(r *hostedMemory) { r.dump = "dump-2" })
	if again, err := h.BackupHosted(ctx, principal(), testAuthorityDigest, "app-1", backupCommand("backup-1", "pre-update")); err != nil || again.Offsite == nil || *again.Offsite != *b.Offsite || s3.requests != 2 {
		t.Fatalf("a replayed backup copied again: %+v, %v, requests %d", again, err, s3.requests)
	}
}

func TestAFailedCopyKeepsTheBackupAndTheNextBackupTriesItAgain(t *testing.T) {
	h, rt, clock, s3, _ := offsiteFixture(t, 0600)
	ctx := context.Background()
	s3.refuse = true
	first, err := h.BackupHosted(ctx, principal(), testAuthorityDigest, "app-1", backupCommand("backup-1", "manual"))
	if err != nil || first.Offsite == nil || first.Offsite.OK || first.Offsite.Detail != "the bucket refused the copy (403)" {
		t.Fatalf("refused copy = %+v, %v", first, err)
	}
	s3.refuse = false
	clock.Add(25 * time.Hour)
	rt.set(func(r *hostedMemory) { r.dump = "dump-2" })
	h.nightlyBackup(h.keys[hostedKeyDigest("app-1")])
	list, _ := h.ListHostedBackups(ctx, principal(), "app-1")
	if len(list.Backups) != 2 || list.Backups[1].Reason != "nightly" {
		t.Fatalf("backups = %+v", list)
	}
	for _, b := range list.Backups {
		if b.Offsite == nil || !b.Offsite.OK {
			t.Fatalf("not copied: %+v", b)
		}
	}
	if string(s3.objects["/autobuild-bf14-backups-123456789012.ap-south-1/hosted/app-1/"+first.BackupID+".dump"]) != "dump-1" {
		t.Fatal("the earlier backup was not copied again")
	}
}

func TestWithoutPrivateSettingsNothingIsCopied(t *testing.T) {
	h, _, _, s3, settings := offsiteFixture(t, 0644)
	ctx := context.Background()
	b, err := h.BackupHosted(ctx, principal(), testAuthorityDigest, "app-1", backupCommand("backup-1", "manual"))
	if err != nil || b.Offsite != nil || s3.requests != 0 {
		t.Fatalf("a readable settings file copied: %+v, %v, requests %d", b, err, s3.requests)
	}
	if err := os.Chmod(settings, 0600); err != nil {
		t.Fatal(err)
	}
	h.SetOffsiteFile("")
	if b, err = h.BackupHosted(ctx, principal(), testAuthorityDigest, "app-1", backupCommand("backup-2", "manual")); err != nil || b.Offsite != nil || s3.requests != 0 {
		t.Fatalf("no settings file copied: %+v, %v", b, err)
	}
	h.SetOffsiteFile(settings)
	if b, err = h.BackupHosted(ctx, principal(), testAuthorityDigest, "app-1", backupCommand("backup-3", "manual")); err != nil || b.Offsite == nil || !b.Offsite.OK {
		t.Fatalf("turned on, read afresh: %+v, %v", b, err)
	}
}

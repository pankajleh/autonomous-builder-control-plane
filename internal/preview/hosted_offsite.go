package preview

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// Hosted backups copied off the host (Repo C BF-10b, design note BF10_GOING_BACK_SAFELY.md §8; docs/plans/hosted-offsite.md).
// When the file given by --hosted-offsite-file names a bucket and a key, each hosted backup is also put at
// s3://<bucket>/hosted/<hosting key>/<backup>.dump, signed with Signature Version 4 and the dump's SHA-256 as its payload
// hash, then read back (HEAD) and its size compared. The file is read afresh at each backup, so a change needs no
// restart, and its key is never logged or answered. A copy that fails never fails the backup: the backup stays on the
// host, and the next backup of the same key tries up to hostedOffsiteRetries earlier ones not copied yet.

// hostedOffsiteSettings is the settings file: where the copies go and the key that may write them (and nothing else).
type hostedOffsiteSettings struct {
	Bucket          string `json:"bucket"`
	Region          string `json:"region"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

const (
	// hostedOffsiteRetries bounds the earlier backups a new one tries to copy again.
	hostedOffsiteRetries = 3
	// hostedOffsiteMaxBytes bounds the settings file.
	hostedOffsiteMaxBytes = 4096
)

var (
	// hostedOffsiteTimeout bounds one copy and its read-back; a variable only so tests can shorten it.
	hostedOffsiteTimeout = 10 * time.Minute

	offsiteBucket = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	offsiteRegion = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]$`)
	offsiteKeyID  = regexp.MustCompile(`^(AKIA|ASIA)[A-Z0-9]{16}$`)
	offsiteSecret = regexp.MustCompile(`^[A-Za-z0-9/+=]{40}$`)
)

// SetOffsiteFile names the private settings file (mode 0600) that turns hosted backups' copies on; empty turns them off.
func (h *Hosting) SetOffsiteFile(path string) {
	h.mu.Lock()
	h.offsiteFile = path
	h.mu.Unlock()
}

// offsiteSettings reads the settings file afresh; without a valid one, nothing is copied.
func (h *Hosting) offsiteSettings() (hostedOffsiteSettings, bool) {
	h.mu.Lock()
	path := h.offsiteFile
	h.mu.Unlock()
	if path == "" {
		return hostedOffsiteSettings{}, false
	}
	data, err := readFile(path, hostedOffsiteMaxBytes, true)
	if err != nil {
		return hostedOffsiteSettings{}, false
	}
	var s hostedOffsiteSettings
	if json.Unmarshal(data, &s) != nil || !offsiteBucket.MatchString(s.Bucket) || strings.Contains(s.Bucket, "..") || !offsiteRegion.MatchString(s.Region) ||
		!offsiteKeyID.MatchString(s.AccessKeyID) || !offsiteSecret.MatchString(s.SecretAccessKey) {
		return hostedOffsiteSettings{}, false
	}
	return s, true
}

// withOffsite copies a new backup off the host, and up to hostedOffsiteRetries earlier ones not copied yet. It answers
// the new backup with its copy recorded, and the earlier ones' copies by backup id. Without settings it changes nothing.
// The key's command lock is held.
func (h *Hosting) withOffsite(ctx context.Context, k *hostedKey, earlier []serviceapi.HostedBackupV1, b serviceapi.HostedBackupV1) (serviceapi.HostedBackupV1, map[string]*serviceapi.HostedBackupOffsiteV1) {
	s, ok := h.offsiteSettings()
	if !ok {
		return b, nil
	}
	b.Offsite = h.copyOffsite(ctx, s, k.digest, b.HostingKey, b.BackupID)
	again := map[string]*serviceapi.HostedBackupOffsiteV1{}
	for i := len(earlier) - 1; i >= 0 && len(again) < hostedOffsiteRetries; i-- {
		e := earlier[i]
		if e.BackupID != b.BackupID && (e.Offsite == nil || !e.Offsite.OK) {
			again[e.BackupID] = h.copyOffsite(ctx, s, k.digest, e.HostingKey, e.BackupID)
		}
	}
	return b, again
}

// withCopies records earlier backups' new copies on the state's backups.
func withCopies(backups []serviceapi.HostedBackupV1, again map[string]*serviceapi.HostedBackupOffsiteV1) []serviceapi.HostedBackupV1 {
	for i, b := range backups {
		if c, ok := again[b.BackupID]; ok {
			backups[i].Offsite = c
		}
	}
	return backups
}

// copyOffsite puts one backup file and reads it back; it answers what happened, never the key or a response body.
func (h *Hosting) copyOffsite(parent context.Context, s hostedOffsiteSettings, keyDigest, key, backupID string) *serviceapi.HostedBackupOffsiteV1 {
	object := "hosted/" + key + "/" + backupID + ".dump"
	address := "s3://" + s.Bucket + "/" + object
	result := func(ok bool, detail string) *serviceapi.HostedBackupOffsiteV1 {
		return &serviceapi.HostedBackupOffsiteV1{At: stamp(h.now()), OK: ok, Detail: detail}
	}
	f, err := openRegular(h.backupPath(keyDigest, backupID), os.O_RDONLY, true)
	if err != nil {
		return result(false, "the backup file could not be read")
	}
	defer f.Close()
	sum := sha256.New()
	size, err := io.Copy(sum, f)
	if err != nil || size == 0 {
		return result(false, "the backup file could not be read")
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return result(false, "the backup file could not be read")
	}
	ctx, cancel := context.WithTimeout(parent, hostedOffsiteTimeout)
	defer cancel()
	url := h.offsiteEndpoint(s.Bucket, s.Region) + "/" + object
	put, err := http.NewRequestWithContext(ctx, http.MethodPut, url, io.NopCloser(f))
	if err != nil {
		return result(false, "the copy could not be made")
	}
	put.ContentLength = size
	signS3(put, s, hex.EncodeToString(sum.Sum(nil)), h.now())
	response, err := h.offsiteClient.Do(put)
	if err != nil {
		return result(false, "the bucket did not answer")
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return result(false, "the bucket refused the copy ("+strconv.Itoa(response.StatusCode)+")")
	}
	head, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return result(false, "the copy could not be read back")
	}
	empty := sha256.Sum256(nil)
	signS3(head, s, hex.EncodeToString(empty[:]), h.now())
	response, err = h.offsiteClient.Do(head)
	if err != nil {
		return result(false, "the copy could not be read back")
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength != size {
		return result(false, fmt.Sprintf("the copy read back differs (%d, %d bytes of %d)", response.StatusCode, response.ContentLength, size))
	}
	return result(true, address)
}

// signS3 signs a request to S3 with Signature Version 4 (host, x-amz-content-sha256 and x-amz-date signed).
func signS3(r *http.Request, s hostedOffsiteSettings, payloadHash string, now time.Time) {
	amz := now.UTC().Format("20060102T150405Z")
	day := amz[:8]
	r.Header.Set("X-Amz-Date", amz)
	r.Header.Set("X-Amz-Content-Sha256", payloadHash)
	canonical := strings.Join([]string{r.Method, r.URL.EscapedPath(), r.URL.RawQuery,
		"host:" + r.URL.Host + "\nx-amz-content-sha256:" + payloadHash + "\nx-amz-date:" + amz + "\n",
		"host;x-amz-content-sha256;x-amz-date", payloadHash}, "\n")
	scope := day + "/" + s.Region + "/s3/aws4_request"
	hashed := sha256.Sum256([]byte(canonical))
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", amz, scope, hex.EncodeToString(hashed[:])}, "\n")
	key := []byte("AWS4" + s.SecretAccessKey)
	for _, part := range []string{day, s.Region, "s3", "aws4_request"} {
		key = hmacSHA256(key, part)
	}
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.AccessKeyID+"/"+scope+
		", SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature="+hex.EncodeToString(hmacSHA256(key, toSign)))
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// s3Endpoint is S3's virtual-hosted address of a bucket.
func s3Endpoint(bucket, region string) string {
	return "https://" + bucket + ".s3." + region + ".amazonaws.com"
}

package remotetarget

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// Backups copied off the server (BF-14.2b-2, docs/plans/remote-offsite.md). When the targets folder's offsite.json
// names a bucket for a target, each backup taken there is also copied to s3://<bucket>/<target>/<site>/<backup>.dump
// by the server itself, with its own short-lived credentials from the instance role it was given (EC2's metadata,
// IMDSv2): no key is kept on the server or in ABCP. A copy that fails leaves the backup on the server and says why;
// the next backup of the same app tries the ones not yet copied again. A WordPress website's backup is two files, its
// dump and its uploads (<backup>.uploads.tar beside <backup>.dump), and is copied when both are.

// offsiteTarget is where one target's backups are copied.
type offsiteTarget struct {
	Bucket string `json:"bucket"`
	Region string `json:"region"`
}

var (
	bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	regionPattern = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]$`)
)

// offsiteRetries bounds the earlier backups a new one tries to copy again.
const offsiteRetries = 3

// offsiteKey names the folder's own settings, offsite.json beside the targets' records: never a target, so the list of
// targets passes it by and no target may take its name.
const offsiteKey = "offsite"

// offsiteFor reads where a target's backups are copied, read afresh each time so a change needs no restart.
func (m *Manager) offsiteFor(key string) (offsiteTarget, bool) {
	data, err := os.ReadFile(filepath.Join(m.dir, offsiteKey+".json"))
	if err != nil {
		return offsiteTarget{}, false
	}
	var file struct {
		Targets map[string]offsiteTarget `json:"targets"`
	}
	if json.Unmarshal(data, &file) != nil {
		return offsiteTarget{}, false
	}
	t, ok := file.Targets[key]
	if !ok || !bucketPattern.MatchString(t.Bucket) || strings.Contains(t.Bucket, "..") || !regionPattern.MatchString(t.Region) {
		return offsiteTarget{}, false
	}
	return t, true
}

// copyBackups copies a site's new backup off the server with copy, and up to offsiteRetries earlier ones not copied
// yet; it answers the new backup's words for the step.
func (m *Manager) copyBackups(key, site, id string, copy func(backup string) string) string {
	said := copy(id)
	m.mu.Lock()
	var again []string
	if r, err := m.read(key); err == nil {
		if d, ok := findDatabase(r, site); ok {
			for i := len(d.Backups) - 1; i >= 0 && len(again) < offsiteRetries; i-- {
				b := d.Backups[i]
				if b.BackupID != id && (b.Offsite == nil || !b.Offsite.OK) {
					again = append(again, b.BackupID)
				}
			}
		}
	}
	m.mu.Unlock()
	for _, earlier := range again {
		copy(earlier)
	}
	return said
}

// backupFiles are a backup's files on the server: its dump, and a WordPress website's uploads beside it.
func (m *Manager) backupFiles(key, site, id string) []string {
	m.mu.Lock()
	r, err := m.read(key)
	m.mu.Unlock()
	files := []string{id + ".dump"}
	if err == nil && wpData(r, site) {
		files = append(files, id+".uploads.tar")
	}
	return files
}

// copyOffsite copies one backup and records the outcome on it.
func (m *Manager) copyOffsite(ctx context.Context, session remote, key, site, id string, t offsiteTarget) string {
	if !keyPattern.MatchString(key) || !siteNamePattern.MatchString(site) || !backupIDPattern.MatchString(id) {
		return "kept on the server only"
	}
	s3 := m.s3Endpoint
	if s3 == "" {
		s3 = "https://" + t.Bucket + ".s3." + t.Region + ".amazonaws.com"
	}
	copied := serviceapi.BackupOffsiteV1{At: m.stamp(), OK: true}
	var addresses []string
	for _, file := range m.backupFiles(key, site, id) {
		object := key + "/" + site + "/" + file
		out, err := run(ctx, session, fill(offsiteScript, "N", site, "FILE", file, "BUCKET", t.Bucket, "REGION", t.Region, "KEY", object,
			"IMDS", m.metadataURL, "S3", s3), nil)
		if err != nil {
			copied.OK, copied.Detail = false, stepDetail(err)
			break
		} else if !strings.HasPrefix(lastLine(out), "copied ") {
			copied.OK, copied.Detail = false, "the copy said nothing"
			break
		}
		addresses = append(addresses, "s3://"+t.Bucket+"/"+object)
	}
	if copied.OK {
		copied.Detail = strings.Join(addresses, ", ")
	}
	return m.noteOffsite(key, site, id, copied)
}

// noteOffsite records a copy's outcome on its backup and answers it in words for the step.
func (m *Manager) noteOffsite(key, site, id string, copied serviceapi.BackupOffsiteV1) string {
	said := "copied off the server"
	if !copied.OK {
		said = "kept on the server only: " + copied.Detail
	}
	m.mu.Lock()
	if r, err := m.read(key); err == nil {
		if d, ok := findDatabase(r, site); ok {
			for i := range d.Backups {
				if d.Backups[i].BackupID == id {
					d.Backups[i].Offsite = &copied
				}
			}
			setDatabase(&r, d)
			_ = m.write(r)
		}
	}
	m.mu.Unlock()
	return said
}

// offsiteScript runs on the server: it reads the server's own credentials from its instance role (IMDSv2), signs an
// S3 PUT of one backup (Signature Version 4, the dump's SHA-256 as its payload hash) and reads the object back to
// compare its size. It prints no credential; a failure names the step and S3's error code only.
const offsiteScript = `set -eu
f="$HOME/autobuild/backups/@N@/@FILE@"
if [ ! -s "$f" ]; then echo "the backup is no longer on the server" >&2; exit 4; fi
if ! command -v python3 >/dev/null 2>&1; then echo "the server has no python3 to copy with" >&2; exit 4; fi
AB_FILE="$f" AB_BUCKET='@BUCKET@' AB_REGION='@REGION@' AB_KEY='@KEY@' AB_IMDS='@IMDS@' AB_S3='@S3@' python3 - <<'PY'
import datetime, hashlib, hmac, json, os, re, sys, urllib.error, urllib.parse, urllib.request
E = os.environ
def fail(why):
    print(why, file=sys.stderr)
    sys.exit(4)
imds = E["AB_IMDS"]
try:
    token = urllib.request.urlopen(urllib.request.Request(imds + "/latest/api/token", method="PUT",
        headers={"X-aws-ec2-metadata-token-ttl-seconds": "300"}), timeout=5).read().decode()
    ask = {"X-aws-ec2-metadata-token": token}
    role = urllib.request.urlopen(urllib.request.Request(imds + "/latest/meta-data/iam/security-credentials/", headers=ask), timeout=5).read().decode().split()[0]
    cred = json.loads(urllib.request.urlopen(urllib.request.Request(imds + "/latest/meta-data/iam/security-credentials/" + role, headers=ask), timeout=5).read())
    access, secret, session = cred["AccessKeyId"], cred["SecretAccessKey"], cred["Token"]
except Exception as e:
    fail("the server has no role to copy with (%s)" % type(e).__name__)
path, region, bucket, key, s3 = E["AB_FILE"], E["AB_REGION"], E["AB_BUCKET"], E["AB_KEY"], E["AB_S3"]
size = os.path.getsize(path)
digest = hashlib.sha256()
with open(path, "rb") as dump:
    for chunk in iter(lambda: dump.read(1 << 20), b""):
        digest.update(chunk)
host = urllib.parse.urlsplit(s3).netloc
uri = "/" + urllib.parse.quote(key, safe="/-_.~")
def signed(method, payload):
    now = datetime.datetime.now(datetime.timezone.utc)
    amz, day = now.strftime("%Y%m%dT%H%M%SZ"), now.strftime("%Y%m%d")
    headers = {"host": host, "x-amz-content-sha256": payload, "x-amz-date": amz, "x-amz-security-token": session}
    names = ";".join(sorted(headers))
    canonical = "\n".join([method, uri, "", "".join("%s:%s\n" % (n, headers[n]) for n in sorted(headers)), names, payload])
    scope = "%s/%s/s3/aws4_request" % (day, region)
    to_sign = "\n".join(["AWS4-HMAC-SHA256", amz, scope, hashlib.sha256(canonical.encode()).hexdigest()])
    k = ("AWS4" + secret).encode()
    for part in (day, region, "s3", "aws4_request"):
        k = hmac.new(k, part.encode(), hashlib.sha256).digest()
    out = {n: v for n, v in headers.items() if n != "host"}
    out["Authorization"] = "AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s" % (
        access, scope, names, hmac.new(k, to_sign.encode(), hashlib.sha256).hexdigest())
    return out
def call(method, body=None, payload=hashlib.sha256(b"").hexdigest(), length=None):
    headers = signed(method, payload)
    if length is not None:
        headers["Content-Length"] = str(length)
    try:
        with urllib.request.urlopen(urllib.request.Request(s3 + uri, data=body, method=method, headers=headers), timeout=120) as r:
            return r.status, r.headers.get("Content-Length")
    except urllib.error.HTTPError as e:
        code = re.search(r"<Code>([A-Za-z]+)</Code>", e.read(4000).decode("utf-8", "replace") if method != "HEAD" else "")
        return e.code, code.group(1) if code else ""
    except Exception as e:
        return 0, type(e).__name__
with open(path, "rb") as dump:
    status, said = call("PUT", dump, digest.hexdigest(), size)
if status != 200:
    fail(("S3 refused the copy (%s %s)" % (status, said)).replace(" )", ")"))
status, said = call("HEAD")
if status != 200 or said != str(size):
    fail("the copy could not be read back (%s)" % status)
print("copied %d bytes" % size)
PY
`

// Backups copied off a server Autobuild bought for a person (Repo C BF14_4_YOUR_OWN_CLOUD_ACCOUNT.md §17.1, BF-14.4d d1).
// A target created with backups_off_server has each backup also copied to
// s3://<bucket>/hosted/servers/<target>/<site>/<backup>.dump, in the hosted backups' bucket, with the key of their
// settings file (--hosted-offsite-file, BF-10b), which may write and read under hosted/ and nothing else. ABCP signs an
// upload address for that one object (Signature Version 4 in the query, uploadExpiry) and the server uploads the backup
// to it itself; ABCP then reads the copy's size back with the key. No key reaches the server, and the address can put
// that one object until it expires. A bought server never gets the instance role: the role may read every backup in the
// bucket, other people's included.

// uploadSettings is the hosted backups' settings file: the bucket and the key that may write under hosted/.
type uploadSettings struct {
	Bucket          string `json:"bucket"`
	Region          string `json:"region"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

const (
	// uploadExpiry bounds an upload address's life: long enough for a backup to upload from India to Mumbai's S3.
	uploadExpiry = 15 * time.Minute
	// uploadsMaxBytes bounds the settings file.
	uploadsMaxBytes = 4096
)

var (
	uploadKeyID  = regexp.MustCompile(`^(AKIA|ASIA)[A-Z0-9]{16}$`)
	uploadSecret = regexp.MustCompile(`^[A-Za-z0-9/+=]{40}$`)
	uploadURL    = regexp.MustCompile(`^https?://[A-Za-z0-9.:-]+/hosted/servers/[a-z0-9-]+/[a-z0-9-]+/[A-Za-z0-9-]+\.(dump|uploads\.tar)\?[A-Za-z0-9%&=._-]+$`)
)

// SetUploadsFile names the hosted backups' private settings file (mode 0600), whose key signs the upload addresses of the
// targets whose backups are copied off them; empty, none is copied.
func (m *Manager) SetUploadsFile(path string) {
	m.mu.Lock()
	m.uploadsFile = path
	m.mu.Unlock()
}

// uploadsFor answers the settings to copy a target's backups off it with, when the target asked for that and the settings
// file is valid; the file is read afresh each time, so a change needs no restart.
func (m *Manager) uploadsFor(key string) (uploadSettings, bool) {
	m.mu.Lock()
	path := m.uploadsFile
	r, err := m.read(key)
	m.mu.Unlock()
	if path == "" || err != nil || !r.BackupsOffServer {
		return uploadSettings{}, false
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > uploadsMaxBytes {
		return uploadSettings{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return uploadSettings{}, false
	}
	var s uploadSettings
	if json.Unmarshal(data, &s) != nil || !bucketPattern.MatchString(s.Bucket) || strings.Contains(s.Bucket, "..") ||
		!regionPattern.MatchString(s.Region) || !uploadKeyID.MatchString(s.AccessKeyID) || !uploadSecret.MatchString(s.SecretAccessKey) {
		return uploadSettings{}, false
	}
	return s, true
}

// copyUploaded copies one backup off the server through an upload address, reads the copy's size back, and records the
// outcome on the backup.
func (m *Manager) copyUploaded(ctx context.Context, session remote, key, site, id string, s uploadSettings) string {
	if !keyPattern.MatchString(key) || !siteNamePattern.MatchString(site) || !backupIDPattern.MatchString(id) {
		return "kept on the server only"
	}
	endpoint := m.s3Endpoint
	if endpoint == "" {
		endpoint = "https://" + s.Bucket + ".s3." + s.Region + ".amazonaws.com"
	}
	copied := serviceapi.BackupOffsiteV1{At: m.stamp()}
	var addresses []string
	for _, file := range m.backupFiles(key, site, id) {
		object := "hosted/servers/" + key + "/" + site + "/" + file
		address := presignPut(endpoint, object, s, m.now(), uploadExpiry)
		if !uploadURL.MatchString(address) {
			copied.Detail = "the upload address could not be made"
			return m.noteOffsite(key, site, id, copied)
		}
		out, err := run(ctx, session, fill(uploadScript, "N", site, "FILE", file, "URL", address), nil)
		size, said := strings.CutPrefix(lastLine(out), "uploaded ")
		switch {
		case err != nil:
			copied.Detail = stepDetail(err)
		case !said || size == "":
			copied.Detail = "the copy said nothing"
		default:
			if status, length := headObject(ctx, endpoint, object, s, m.now()); status != http.StatusOK || length != size {
				copied.Detail = fmt.Sprintf("the copy could not be read back (%d)", status)
			}
		}
		if copied.Detail != "" {
			return m.noteOffsite(key, site, id, copied)
		}
		addresses = append(addresses, "s3://"+s.Bucket+"/"+object)
	}
	copied.OK, copied.Detail = true, strings.Join(addresses, ", ")
	return m.noteOffsite(key, site, id, copied)
}

// presignPut is an address that may put one object until it expires (Signature Version 4 in the query, the payload
// unsigned, only the host header signed).
func presignPut(endpoint, object string, s uploadSettings, now time.Time, expiry time.Duration) string {
	u, err := url.Parse(endpoint + "/" + object)
	if err != nil {
		return ""
	}
	amz := now.UTC().Format("20060102T150405Z")
	scope := amz[:8] + "/" + s.Region + "/s3/aws4_request"
	query := url.Values{}
	query.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	query.Set("X-Amz-Credential", s.AccessKeyID+"/"+scope)
	query.Set("X-Amz-Date", amz)
	query.Set("X-Amz-Expires", strconv.Itoa(int(expiry/time.Second)))
	query.Set("X-Amz-SignedHeaders", "host")
	canonicalQuery := query.Encode()
	canonical := strings.Join([]string{http.MethodPut, u.EscapedPath(), canonicalQuery, "host:" + u.Host + "\n", "host", "UNSIGNED-PAYLOAD"}, "\n")
	return u.Scheme + "://" + u.Host + u.EscapedPath() + "?" + canonicalQuery + "&X-Amz-Signature=" + signature(s, amz, scope, canonical)
}

// headObject reads one object's size with the key (Signature Version 4 in the headers); a failure answers status 0.
func headObject(ctx context.Context, endpoint, object string, s uploadSettings, now time.Time) (int, string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint+"/"+object, nil)
	if err != nil {
		return 0, ""
	}
	empty := sha256.Sum256(nil)
	payload := hex.EncodeToString(empty[:])
	amz := now.UTC().Format("20060102T150405Z")
	scope := amz[:8] + "/" + s.Region + "/s3/aws4_request"
	r.Header.Set("X-Amz-Date", amz)
	r.Header.Set("X-Amz-Content-Sha256", payload)
	canonical := strings.Join([]string{r.Method, r.URL.EscapedPath(), "", "host:" + r.URL.Host + "\nx-amz-content-sha256:" + payload + "\nx-amz-date:" + amz + "\n",
		"host;x-amz-content-sha256;x-amz-date", payload}, "\n")
	r.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.AccessKeyID+"/"+scope+
		", SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature="+signature(s, amz, scope, canonical))
	answer, err := (&http.Client{Timeout: 30 * time.Second}).Do(r)
	if err != nil {
		return 0, ""
	}
	defer answer.Body.Close()
	return answer.StatusCode, answer.Header.Get("Content-Length")
}

// signature signs a canonical request with the key (Signature Version 4).
func signature(s uploadSettings, amz, scope, canonical string) string {
	hashed := sha256.Sum256([]byte(canonical))
	toSign := strings.Join([]string{"AWS4-HMAC-SHA256", amz, scope, hex.EncodeToString(hashed[:])}, "\n")
	key := []byte("AWS4" + s.SecretAccessKey)
	for _, part := range []string{amz[:8], s.Region, "s3", "aws4_request"} {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(part))
		key = mac.Sum(nil)
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(toSign))
	return hex.EncodeToString(mac.Sum(nil))
}

// uploadScript runs on the server: it puts one backup at its upload address and says its size. The address is the only
// thing it is given; it prints no address, and a failure names S3's status and error code only.
const uploadScript = `set -eu
f="$HOME/autobuild/backups/@N@/@FILE@"
if [ ! -s "$f" ]; then echo "the backup is no longer on the server" >&2; exit 4; fi
if ! command -v python3 >/dev/null 2>&1; then echo "the server has no python3 to copy with" >&2; exit 4; fi
AB_FILE="$f" AB_URL='@URL@' python3 - <<'PY'
import os, re, sys, urllib.error, urllib.request
path, address = os.environ["AB_FILE"], os.environ["AB_URL"]
size = os.path.getsize(path)
try:
    with open(path, "rb") as dump:
        with urllib.request.urlopen(urllib.request.Request(address, data=dump, method="PUT", headers={"Content-Length": str(size)}), timeout=600) as r:
            status = r.status
except urllib.error.HTTPError as e:
    code = re.search(r"<Code>([A-Za-z]+)</Code>", e.read(4000).decode("utf-8", "replace"))
    print(("S3 refused the copy (%s %s)" % (e.code, code.group(1) if code else "")).replace(" )", ")"), file=sys.stderr)
    sys.exit(4)
except Exception as e:
    print("the copy could not reach S3 (%s)" % type(e).__name__, file=sys.stderr)
    sys.exit(4)
if status != 200:
    print("S3 answered %s" % status, file=sys.stderr)
    sys.exit(4)
print("uploaded %d" % size)
PY
`

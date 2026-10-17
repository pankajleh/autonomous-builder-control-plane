package remotetarget

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

// Backups copied off the server (BF-14.2b-2, docs/plans/remote-offsite.md). When the targets folder's offsite.json
// names a bucket for a target, each backup taken there is also copied to s3://<bucket>/<target>/<site>/<backup>.dump
// by the server itself, with its own short-lived credentials from the instance role it was given (EC2's metadata,
// IMDSv2): no key is kept on the server or in ABCP. A copy that fails leaves the backup on the server and says why;
// the next backup of the same app tries the ones not yet copied again.

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

// copyBackups copies a site's new backup off the server, and up to offsiteRetries earlier ones not copied yet; it
// answers the new backup's words for the step.
func (m *Manager) copyBackups(ctx context.Context, session remote, key, site, id string, t offsiteTarget) string {
	said := m.copyOffsite(ctx, session, key, site, id, t)
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
		m.copyOffsite(ctx, session, key, site, earlier, t)
	}
	return said
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
	object := key + "/" + site + "/" + id + ".dump"
	out, err := run(ctx, session, fill(offsiteScript, "N", site, "ID", id, "BUCKET", t.Bucket, "REGION", t.Region, "KEY", object,
		"IMDS", m.metadataURL, "S3", s3), nil)
	copied := serviceapi.BackupOffsiteV1{At: m.stamp(), OK: err == nil, Detail: "s3://" + t.Bucket + "/" + object}
	said := "copied off the server"
	if err != nil {
		copied.Detail = stepDetail(err)
		said = "kept on the server only: " + copied.Detail
	} else if !strings.HasPrefix(lastLine(out), "copied ") {
		copied.OK, copied.Detail = false, "the copy said nothing"
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
f="$HOME/autobuild/backups/@N@/@ID@.dump"
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

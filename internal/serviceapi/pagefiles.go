package serviceapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Page files (Repo C design note STANDARD_PAGES.md §14, SP1b): an app's standard pages, which Repo C passes to ABCP as
// named files for a WordPress website's preview (read-only at /pages, preview.go) and for its live site on a target
// (`wp autobuild-site page set`, remotetarget). ABCP never reads what is in them: each is a file with a strict name,
// size and the shape of a JSON object, its bytes kept exactly as given (base64 on the wire). Nothing in Repo B knows
// Repo C's page-set format or folders.

// PageFileV1 is one page file: its name and its bytes in standard, padded base64.
type PageFileV1 struct {
	Name          string `json:"name"`
	ContentBase64 string `json:"content_base64"`
}

// PageFile is one page file's name and bytes, decoded and checked.
type PageFile struct {
	Name    string
	Content []byte
}

const (
	// MaxPageFiles, MaxPageFileBytes and MaxPageFilesBytes bound one set of page files: at most 20 files, each at most
	// 256 KiB and at most 1 MiB in all.
	MaxPageFiles      = 20
	MaxPageFileBytes  = 256 << 10
	MaxPageFilesBytes = 1 << 20
	// MaxPageFilesRequestBytes bounds a request that carries page files: their whole base64 and 64 KiB more for names
	// and the request's other fields. It is the one bound above MaxRequestBodyBytes besides a release's and an account
	// lane's question.
	MaxPageFilesRequestBytes = (MaxPageFilesBytes+2)/3*4 + 64<<10
)

// pageFileName is a page file's name: a lowercase name and .json, which is all a WordPress website's page needs.
var pageFileName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,59}\.json$`)

// ErrPageFilesInvalid is any page file outside the contract.
var ErrPageFilesInvalid = errors.New("page files invalid")

// DecodePageFiles checks a set of page files and answers their bytes: at most MaxPageFiles, named in strictly
// increasing order (so each name once and the set in one order), each canonical base64 of a JSON object in UTF-8 of at
// most MaxPageFileBytes, MaxPageFilesBytes in all. No files is an empty set.
func DecodePageFiles(files []PageFileV1) ([]PageFile, error) {
	if len(files) > MaxPageFiles {
		return nil, ErrPageFilesInvalid
	}
	out := make([]PageFile, 0, len(files))
	total := 0
	for i, f := range files {
		if !pageFileName.MatchString(f.Name) || (i > 0 && files[i-1].Name >= f.Name) {
			return nil, ErrPageFilesInvalid
		}
		if len(f.ContentBase64) > (MaxPageFileBytes+2)/3*4 {
			return nil, ErrPageFilesInvalid
		}
		content, err := base64.StdEncoding.Strict().DecodeString(f.ContentBase64)
		// The decoder skips line breaks; only the one canonical spelling of the bytes is taken.
		if err != nil || base64.StdEncoding.EncodeToString(content) != f.ContentBase64 || len(content) > MaxPageFileBytes || !jsonObject(content) {
			return nil, ErrPageFilesInvalid
		}
		total += len(content)
		if total > MaxPageFilesBytes {
			return nil, ErrPageFilesInvalid
		}
		out = append(out, PageFile{Name: f.Name, Content: content})
	}
	return out, nil
}

// jsonObject reports UTF-8 text that is one JSON object; nothing else about it is read.
func jsonObject(content []byte) bool {
	trimmed := bytes.TrimLeft(content, " \t\r\n")
	return utf8.Valid(content) && len(trimmed) > 0 && trimmed[0] == '{' && json.Valid(content)
}

// PageFilesDigest names a set of page files: the SHA-256 of one line per file in name order, its name, a space and its
// bytes' SHA-256 in hex. Repo C computes the same to check what a preview or a site took. No files is "".
func PageFilesDigest(files []PageFile) string {
	if len(files) == 0 {
		return ""
	}
	var lines strings.Builder
	for _, f := range files {
		sum := sha256.Sum256(f.Content)
		lines.WriteString(f.Name + " " + hex.EncodeToString(sum[:]) + "\n")
	}
	sum := sha256.Sum256([]byte(lines.String()))
	return hex.EncodeToString(sum[:])
}

// isPageFilesUpload is a request that may carry page files: a preview's create and a site's pages on a target. Each
// still reads its body within its own bound.
func isPageFilesUpload(r *http.Request) bool {
	if r.Method != http.MethodPost || r.URL == nil || r.URL.RawPath != "" {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/"), "/")
	switch {
	case len(parts) == 3 && parts[0] == "runs" && parts[2] == "previews":
		return true
	case len(parts) == 5 && parts[0] == "targets" && parts[2] == "sites" && parts[4] == "pages":
		return true
	}
	return false
}

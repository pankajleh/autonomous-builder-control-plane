package run

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The pages follow (Repo C design note PREVIEW_EDITOR.md §12.2). A site-kit website's content.json is the source of
// everything it shows, and the kit's tools/export-static.mjs writes its pages from it: each page's HTML, public/snapshot.js,
// public/offline.js and sitemap.xml. An exact edit only replaces text or a JSON value, so after a site-kit website's edit
// the export runs once and its output becomes one more commit. The export imports the website's own files (public/kit,
// public/blocks), app code, so it runs inside the build wall: read-only system and tools, only its own folder writable,
// no network, a time bound. It runs on a folder written from Git's objects, never a checkout, so no attribute or filter
// of the repository runs. Only the export's own outputs may change; anything else refuses the edit.

const (
	siteExportScript   = "tools/export-static.mjs"
	siteExportTimeout  = 60 * time.Second
	siteExportMaxFiles = 5000
	siteExportMaxBytes = 128 << 20
	siteExportMaxOut   = 4 << 10
)

// siteExportCommand runs the export in dir; tests replace it.
var siteExportCommand = walledSiteExport

// siteKitSite reports whether commit holds a site-kit website: app.json says "kind": "static" and names the SITE block,
// and the kit's export script is there.
func siteKitSite(ctx context.Context, repository, commit string) bool {
	data, err := gitBytes(ctx, repository, nil, []string{"GIT_LITERAL_PATHSPECS=1"}, "cat-file", "blob", commit+":app.json")
	if err != nil {
		return false
	}
	var app struct {
		Kind   string                     `json:"kind"`
		Blocks map[string]json.RawMessage `json:"blocks"`
	}
	if json.Unmarshal(data, &app) != nil || app.Kind != "static" || app.Blocks["SITE"] == nil {
		return false
	}
	_, err = gitBytes(ctx, repository, nil, nil, "cat-file", "-e", commit+":"+siteExportScript)
	return err == nil
}

// siteExportAllowed reports whether the export may write or remove path: its snapshot, its offline copy, the sitemap and
// the pages, which it writes at the top of the website.
func siteExportAllowed(path string) bool {
	switch path {
	case "public/snapshot.js", "public/offline.js", "sitemap.xml":
		return true
	}
	return !strings.Contains(path, "/") && strings.HasSuffix(path, ".html")
}

type siteTreeEntry struct{ mode, blob string }

// materializeTree writes commit's regular files into dir straight from Git's objects (no link, no submodule) and answers
// every entry of the tree by path.
func materializeTree(ctx context.Context, repository, commit, dir string) (map[string]siteTreeEntry, error) {
	listing, err := gitBytes(ctx, repository, nil, nil, "ls-tree", "-r", "-z", "--full-tree", commit)
	if err != nil {
		return nil, fmt.Errorf("list the version's files: %w", err)
	}
	entries := map[string]siteTreeEntry{}
	var wanted []string
	for _, line := range bytes.Split(bytes.TrimSuffix(listing, []byte{0}), []byte{0}) {
		meta, path, ok := strings.Cut(string(line), "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 {
			return nil, errors.New("the version's file list could not be read")
		}
		entries[path] = siteTreeEntry{mode: fields[0], blob: fields[2]}
		if fields[1] == "blob" && (fields[0] == "100644" || fields[0] == "100755") {
			wanted = append(wanted, path)
		}
	}
	if len(entries) > siteExportMaxFiles {
		return nil, fmt.Errorf("the website has more than %d files", siteExportMaxFiles)
	}
	var request bytes.Buffer
	for _, path := range wanted {
		request.WriteString(entries[path].blob + "\n")
	}
	batch, err := gitBytes(ctx, repository, request.Bytes(), nil, "cat-file", "--batch")
	if err != nil {
		return nil, fmt.Errorf("read the version's files: %w", err)
	}
	total := 0
	for _, path := range wanted {
		header, rest, ok := bytes.Cut(batch, []byte{'\n'})
		fields := strings.Fields(string(header))
		if !ok || len(fields) != 3 || fields[1] != "blob" {
			return nil, errors.New("the version's files could not be read")
		}
		size, err := strconv.Atoi(fields[2])
		if err != nil || size < 0 || size+1 > len(rest) {
			return nil, errors.New("the version's files could not be read")
		}
		if total += size; total > siteExportMaxBytes {
			return nil, fmt.Errorf("the website is larger than %d bytes", siteExportMaxBytes)
		}
		target := filepath.Join(dir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, err
		}
		perm := os.FileMode(0o644)
		if entries[path].mode == "100755" {
			perm = 0o755
		}
		if err := os.WriteFile(target, rest[:size], perm); err != nil {
			return nil, err
		}
		batch = rest[size+1:]
	}
	return entries, nil
}

// gitBlobID answers Git's name for content as a blob, in the repository's hash: SHA-1 for a 40-character name, SHA-256 for
// a 64-character one.
func gitBlobID(content []byte, nameLength int) string {
	header := fmt.Sprintf("blob %d\x00", len(content))
	if nameLength == 64 {
		sum := sha256.Sum256(append([]byte(header), content...))
		return hex.EncodeToString(sum[:])
	}
	sum := sha1.Sum(append([]byte(header), content...))
	return hex.EncodeToString(sum[:])
}

// exportChanges compares dir after the export with the tree it was written from: the files the export wrote or removed,
// refused when one of them is not the export's to write, or when anything but a regular file appeared.
func exportChanges(dir string, before map[string]siteTreeEntry) (map[string][]byte, []string, error) {
	changed := map[string][]byte{}
	seen := map[string]bool{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !entry.Type().IsRegular() {
			return fmt.Errorf("the export made %s, which is not a file", rel)
		}
		seen[rel] = true
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if old, ok := before[rel]; ok && old.blob == gitBlobID(content, len(old.blob)) {
			return nil
		}
		if !siteExportAllowed(rel) {
			return fmt.Errorf("the export changed %s, which is not one of its pages", rel)
		}
		changed[rel] = content
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	var removed []string
	for path, entry := range before {
		if seen[path] || entry.mode == "120000" || entry.mode == "160000" {
			continue
		}
		if !siteExportAllowed(path) {
			return nil, nil, fmt.Errorf("the export removed %s, which is not one of its pages", path)
		}
		removed = append(removed, path)
	}
	sort.Strings(removed)
	return changed, removed, nil
}

// walledSiteExport runs node tools/export-static.mjs on dir inside the wall: the read-only system and tools, dir the only
// writable folder, no network, at most siteExportTimeout.
func walledSiteExport(ctx context.Context, wall *Wall, dir string) error {
	if wall == nil {
		return errors.New("no build wall to run the export in")
	}
	node, err := wall.nodeBinary()
	if err != nil {
		return err
	}
	prefix, err := wall.ObserverArgs(wall.file.Tools, []string{dir}, false)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, siteExportTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, prefix[0], append(prefix[1:], node, filepath.Join(dir, filepath.FromSlash(siteExportScript)))...)
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "LANG=C.UTF-8"}
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil {
		text := output.String()
		if len(text) > siteExportMaxOut {
			text = text[len(text)-siteExportMaxOut:]
		}
		return fmt.Errorf("the export stopped: %v: %s", err, strings.TrimSpace(text))
	}
	return nil
}

// nodeBinary answers the one Node the wall's tools hold (<tool>/versions/node/<version>/bin/node, as nvm keeps it).
func (w *Wall) nodeBinary() (string, error) {
	var found []string
	for _, tool := range w.file.Tools {
		matches, _ := filepath.Glob(filepath.Join(tool, "versions", "node", "*", "bin", "node"))
		found = append(found, matches...)
	}
	if len(found) != 1 {
		return "", fmt.Errorf("the wall's tools hold %d Node versions, not one", len(found))
	}
	return found[0], nil
}

// exportWall answers the wall a site-kit website's export runs in after the exact edit on commit. An exact edit runs no
// build, so it starts before Run loads a build's wall from wallFile (2026-11-18: the first live try found no wall, and
// the edit was refused); the wall is loaded here as Run loads it, and only the one abcp serve probed. Nothing is loaded
// for a commit that is not a site-kit website, or when the runner was given its wall or no wall file.
func (r *Runner) exportWall(ctx context.Context, repository, commit string) (*Wall, error) {
	if r.wall != nil || r.wallFile == "" || !siteKitSite(ctx, repository, commit) {
		return r.wall, nil
	}
	wall, err := LoadBuildWall(r.wallFile)
	if err != nil {
		return nil, fmt.Errorf("%w: the pages: %v", ErrBuildWallUnavailable, err)
	}
	if wall.SHA256() != r.wallSHA256 {
		return nil, fmt.Errorf("%w: the pages: the wall file is not the one abcp serve probed (sha256 %s, probed %s)",
			ErrBuildWallUnavailable, wall.SHA256(), r.wallSHA256)
	}
	return wall, nil
}

// exportSitePages runs the export after a site-kit website's edit and commits what it wrote on the branch, one commit
// after headSHA. It answers the files regenerated and the branch's new head; nothing (and headSHA) when the commit is
// not a site-kit website or the pages already follow.
func exportSitePages(ctx context.Context, wall *Wall, repository, branch, headSHA string) ([]string, string, error) {
	if !siteKitSite(ctx, repository, headSHA) {
		return nil, headSHA, nil
	}
	dir, err := os.MkdirTemp("", "abcp-site-export-")
	if err != nil {
		return nil, "", fmt.Errorf("make the export's folder: %w", err)
	}
	defer os.RemoveAll(dir)
	before, err := materializeTree(ctx, repository, headSHA, dir)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrExactEditDoesNotApply, err)
	}
	if err := siteExportCommand(ctx, wall, dir); err != nil {
		return nil, "", fmt.Errorf("%w: the pages: %v", ErrExactEditDoesNotApply, err)
	}
	changed, removed, err := exportChanges(dir, before)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrExactEditDoesNotApply, err)
	}
	if len(changed) == 0 && len(removed) == 0 {
		return nil, headSHA, nil
	}
	index, err := os.MkdirTemp("", "abcp-site-export-index-")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(index)
	indexEnv := []string{"GIT_INDEX_FILE=" + filepath.Join(index, "index")}
	if _, err := gitBytes(ctx, repository, nil, indexEnv, "read-tree", headSHA); err != nil {
		return nil, "", fmt.Errorf("read the edit into the export's index: %w", err)
	}
	var entries bytes.Buffer
	var regenerated []string
	for path, content := range changed {
		blob, err := gitBytes(ctx, repository, content, nil, "hash-object", "-w", "--no-filters", "--stdin")
		if err != nil {
			return nil, "", fmt.Errorf("write %s: %w", path, err)
		}
		mode := "100644"
		if old, ok := before[path]; ok {
			mode = old.mode
		}
		fmt.Fprintf(&entries, "%s %s\t%s\x00", mode, strings.TrimSpace(string(blob)), path)
		regenerated = append(regenerated, path)
	}
	for _, path := range removed {
		fmt.Fprintf(&entries, "0 %s\t%s\x00", strings.Repeat("0", len(before[path].blob)), path)
		regenerated = append(regenerated, path)
	}
	sort.Strings(regenerated)
	if _, err := gitBytes(ctx, repository, entries.Bytes(), indexEnv, "update-index", "-z", "--index-info"); err != nil {
		return nil, "", fmt.Errorf("put the pages in the export's index: %w", err)
	}
	tree, err := gitBytes(ctx, repository, nil, indexEnv, "write-tree")
	if err != nil {
		return nil, "", fmt.Errorf("write the pages' tree: %w", err)
	}
	message := fmt.Sprintf("The pages regenerated from the edit (%d file(s))\n\n", len(regenerated))
	for _, path := range regenerated {
		message += "- " + path + "\n"
	}
	commit, err := gitBytes(ctx, repository, []byte(message), exactEditIdentity, "commit-tree", strings.TrimSpace(string(tree)), "-p", headSHA, "-F", "-")
	if err != nil {
		return nil, "", fmt.Errorf("write the pages' commit: %w", err)
	}
	newHead := strings.TrimSpace(string(commit))
	if _, err := gitBytes(ctx, repository, nil, nil, "update-ref", "-m", "abcp exact edit: pages", "refs/heads/"+branch, newHead, headSHA); err != nil {
		return nil, "", fmt.Errorf("move branch %q to the pages' commit: %w", branch, err)
	}
	// Checked as Git holds it: one commit after the edit, changing only the export's own files.
	diff, err := gitBytes(ctx, repository, nil, nil, "diff-tree", "-r", "-z", "--no-renames", "--name-only", "--no-commit-id", headSHA, newHead)
	if err != nil {
		return nil, "", fmt.Errorf("read the pages' commit: %w", err)
	}
	for _, path := range bytes.Split(bytes.TrimSuffix(diff, []byte{0}), []byte{0}) {
		if !siteExportAllowed(string(path)) {
			return nil, "", fmt.Errorf("the pages' commit changes %s", path)
		}
	}
	return regenerated, newHead, nil
}

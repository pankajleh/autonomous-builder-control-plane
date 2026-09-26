package preview

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/activity"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	capsule "github.com/pankajleh/autonomous-builder-control-plane/internal/context"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runadmission"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/strictjson"
)

type ActivityReader interface {
	ReadActivity(context.Context, string, serviceapi.PageRequestV1) (json.RawMessage, error)
}
type Source struct {
	RunID, CheckpointActivityID, SHA, Repository, RepositoryIdentityDigest, Branch, Base string
	ProductAuthorizationID, ProductTaskID, ProductVersionID                              string
	ValidationID                                                                         string
}
type SourceResolver interface {
	Resolve(context.Context, string, string) (Source, error)
}
type Resolver struct {
	Root     string
	Catalog  activity.Catalog
	Activity ActivityReader
}

func (r Resolver) Resolve(ctx context.Context, run, checkpoint string) (Source, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	fail := func() (Source, error) { return Source{}, ErrIneligible }
	if r.Catalog == nil || r.Activity == nil || !runtimecatalog.ValidIdentifier(run) || !sha256Pattern.MatchString(checkpoint) {
		return fail()
	}
	// BP-01 verifies the registration, manifest, plan, repository and governed
	// worktree without granting this extension any ledger or transition methods.
	scope, err := (activity.Resolver{Root: r.Root, Catalog: r.Catalog}).Resolve(ctx, run)
	if err != nil {
		return fail()
	}
	if !repositoryIdentity(ctx, scope.Repository, scope.RepositoryIdentity) {
		return fail()
	}
	reg, err := r.Catalog.ReadRun(run)
	if err != nil || reg.RunID != run || reg.AuthorityDigest != scope.AuthorityDigest {
		return fail()
	}
	dir, err := privateDirectory(filepath.Join(r.Root, "admissions"), false)
	if err != nil {
		return fail()
	}
	entries, err := dir.ReadDir(20001)
	dir.Close()
	if (err != nil && err != io.EOF) || len(entries) > 20000 {
		return fail()
	}
	var b runadmission.AdmissionBindingV1
	matches := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".binding.json") {
			continue
		}
		data, e := readFile(filepath.Join(r.Root, "admissions", entry.Name()), runadmission.MaxBindingBytes, true)
		var next runadmission.AdmissionBindingV1
		if e != nil || strictjson.Decode(data, &next) != nil {
			return fail()
		}
		if next.RunID == run {
			matches++
			b = next
			if entry.Name() != digest([]byte(b.PrincipalID+"\x00"+b.RequestID))+".binding.json" {
				return fail()
			}
		}
	}
	if matches != 1 || b.AdmissionKind != "" || runadmission.DeriveRunID(b.PrincipalID, b.RequestID) != run || !identifier.MatchString(b.PrincipalID) || !identifier.MatchString(b.RequestID) || b.Kind != "AdmissionBindingV1" || b.SchemaVersion != 1 || b.AuthorityDigest != reg.AuthorityDigest || b.RepositoryIdentityDigest != reg.RepositoryIdentityDigest || runtimecatalog.RepositoryIdentityDigest(b.RepositoryIdentity) != reg.RepositoryIdentityDigest || b.RepositoryPath != scope.Repository || b.CanonicalLedgerPath != reg.CanonicalLedgerPath || b.CanonicalEvidenceRoot != reg.CanonicalEvidenceRoot {
		return fail()
	}
	admitted := filepath.Join(b.RepositoryPath, b.InputDirectory, run)
	if b.ContextCapsulePath != filepath.Join(admitted, "context-capsule.json") || b.ManifestPath != filepath.Join(admitted, "manifest.json") || b.PlanPath != filepath.Join(admitted, "plan.md") {
		return fail()
	}
	data, err := readFile(b.ManifestPath, runadmission.MaxManifestTemplateBytes, false)
	var m authority.Manifest
	if err != nil || digest(data) != reg.AuthorityDigest || strictjson.Decode(data, &m) != nil || m.RunID != run || m.Repository.Path != scope.Repository || m.Repository.Identity != b.RepositoryIdentity || m.Repository.StartSHA != scope.Base || m.ContextCapsule == nil || m.ContextCapsule.Path != b.ContextCapsulePath {
		return fail()
	}
	data, err = readFile(b.ContextCapsulePath, capsule.MaxCapsuleBytes, false)
	if err != nil || digest(data) != m.ContextCapsule.SHA256 {
		return fail()
	}
	c, err := capsule.Parse(data)
	if err != nil || c.PolicyVersion != capsule.PolicyVersionV2 || c.RoadmapPhase != "product-run-admission" || c.Repository != b.RepositoryIdentity || c.BaseSHA != scope.Base || c.Task != b.RequestID || !identifier.MatchString(c.Project) || !identifier.MatchString(c.Plan) || !identifier.MatchString(c.ExecutionPack) || c.PhaseAuthority != nil || c.OperationContext == nil || c.OperationContext.Kind != capsule.OperationImplementation {
		return fail()
	}
	// Parse proves canonical field order. The payload hash is precisely the
	// canonical capsule with its final capsule_sha256 field omitted.
	suffix := []byte(`,"capsule_sha256":"` + c.CapsuleSHA256 + `"}`)
	if !sha256Pattern.MatchString(c.CapsuleSHA256) || !bytes.HasSuffix(data, suffix) || digest(append(append([]byte{}, data[:len(data)-len(suffix)]...), '}')) != c.CapsuleSHA256 {
		return fail()
	}
	plan, err := readFile(b.PlanPath, runadmission.MaxManifestTemplateBytes, false)
	rel, relErr := filepath.Rel(b.RepositoryPath, b.PlanPath)
	if err != nil || relErr != nil || m.Plan.Path != b.PlanPath || digest(plan) != m.Plan.SHA256 || len(c.Sources) != 1 || c.Sources[0].Path != filepath.ToSlash(rel) || c.Sources[0].SHA256 != m.Plan.SHA256 {
		return fail()
	}
	// Read every bounded page, including events after the requested checkpoint:
	// an integrity/ambiguity marker invalidates the source, even after a match.
	var selected activity.Event
	found := 0
	cursor := ""
	last := uint64(0)
	seen := map[string]bool{}
	for count := 0; ; count++ {
		if count >= activity.MaxEvents/activity.MaxPageSize+1 || ctx.Err() != nil {
			return fail()
		}
		raw, e := r.Activity.ReadActivity(ctx, run, serviceapi.PageRequestV1{PageSize: activity.MaxPageSize, Cursor: cursor})
		var page activity.Page
		if e != nil || strictjson.Decode(raw, &page) != nil || page.RunID != run || page.SchemaVersion != "ActivityPageV1" || len(page.Events) > activity.MaxPageSize {
			return fail()
		}
		for _, event := range page.Events {
			if event.RunID != run || event.Ordinal <= last || event.Category == "UNKNOWN" || event.Status == "UNKNOWN" && !providerWarning(event) {
				return fail()
			}
			last = event.Ordinal
			if event.ActivityID == checkpoint {
				selected = event
				found++
			}
		}
		if page.NextCursor == "" {
			break
		}
		if seen[page.NextCursor] || len(page.Events) == 0 {
			return fail()
		}
		seen[page.NextCursor] = true
		cursor = page.NextCursor
	}
	e := selected
	if found != 1 || e.SchemaVersion != "ActivityEventV1" || e.AuthorityLevel != "ABCP_STATE" || e.Category != "CHECKPOINT" || e.Status != "AVAILABLE" || e.SourceKind != "ABCP_CHECKPOINT_OBSERVER" || !e.CheckpointClean || !gitSHA.MatchString(e.CheckpointSHA) || e.CheckpointSHA == scope.Base || e.ActivityID != jsonDigest([]string{run, "checkpoint", e.CheckpointSHA}) || e.SourceDigest != jsonDigest([]string{run, e.CheckpointSHA}) {
		return fail()
	}
	if _, err = git(ctx, scope.Repository, "merge-base", "--is-ancestor", scope.Base, e.CheckpointSHA); err != nil {
		return fail()
	}
	if _, err = git(ctx, scope.Repository, "merge-base", "--is-ancestor", e.CheckpointSHA, "refs/heads/"+scope.Branch); err != nil {
		return fail()
	}
	again, err := r.Catalog.ReadRun(run)
	if err != nil || again != reg {
		return fail()
	}
	return Source{RunID: run, CheckpointActivityID: checkpoint, SHA: e.CheckpointSHA, Repository: scope.Repository, RepositoryIdentityDigest: reg.RepositoryIdentityDigest, Branch: scope.Branch, Base: scope.Base, ProductAuthorizationID: c.Project, ProductTaskID: c.Plan, ProductVersionID: c.ExecutionPack, ValidationID: jsonDigest([]string{jsonDigest(reg), jsonDigest(b), c.CapsuleSHA256, jsonDigest(e)})}, nil
}

// Ordinary provider warnings retain both hashed provider source identities.
// Controller integrity/ambiguity markers deliberately have neither identity.
func providerWarning(e activity.Event) bool {
	return e.SchemaVersion == "ActivityEventV1" && e.AuthorityLevel == "PROVIDER_DETAIL" && e.SourceKind == "RALPHEX_PROGRESS" && e.Category == "WARNING" &&
		strings.HasPrefix(e.SourceSessionID, "sha256-") && sha256Pattern.MatchString(strings.TrimPrefix(e.SourceSessionID, "sha256-")) &&
		strings.HasPrefix(e.SourceEventID, "sha256-") && sha256Pattern.MatchString(strings.TrimPrefix(e.SourceEventID, "sha256-"))
}

func repositoryIdentity(ctx context.Context, path, identity string) bool {
	root, err := git(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil || root != path {
		return false
	}
	remotes, err := git(ctx, path, "remote")
	if err != nil {
		return false
	}
	matched := false
	for _, name := range strings.Fields(remotes) {
		urls, err := git(ctx, path, "remote", "get-url", "--all", name)
		if err != nil {
			return false
		}
		for _, raw := range strings.Split(urls, "\n") {
			value := raw
			if parsed, err := url.Parse(raw); err == nil && parsed.Scheme != "" {
				value = parsed.Path
			} else if colon := strings.IndexByte(raw, ':'); colon >= 0 && !strings.Contains(raw[:colon], "/") {
				value = raw[colon+1:]
			}
			value = strings.TrimSuffix(strings.Trim(strings.ReplaceAll(value, "\\", "/"), "/"), ".git")
			if value == identity {
				matched = true
			}
		}
	}
	return matched
}

package preview

import "github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"

// Ownership and admission configuration are journal-only metadata. Authentication
// methods can rotate without changing the authenticated principal's identity.
type ownerIdentity struct {
	PrincipalID   string
	PrincipalType serviceapi.PrincipalType
}

func owner(p serviceapi.Principal) ownerIdentity {
	return ownerIdentity{p.PrincipalID, p.PrincipalType}
}

func (o ownerIdentity) valid() bool {
	return o.PrincipalType == serviceapi.PrincipalService && serviceapi.ValidatePrincipalID(o.PrincipalID) == nil
}

// The API supplies the authority digest after matching the fixed preview.control
// grant. Neither it nor the owner can be supplied in a preview command body.
type commandBinding struct {
	Principal       ownerIdentity
	AuthorityDigest string
	Operation       string
	RunID           string
	PreviewID       string // Target for stop; create's resulting identity is in Result.
	RequestID       string
	DelegatedActor  serviceapi.DelegatedActorV1
	BodyDigest      string
}

func receiptIdentity(p ownerIdentity, id string) string {
	return jsonDigest(struct {
		Namespace string
		Principal ownerIdentity
		RequestID string
	}{"preview-command-v2", p, id})
}

func createReceipt(p serviceapi.Principal, authorityDigest, run string, c serviceapi.PreviewRequestV1) receipt {
	return bindReceipt(commandBinding{owner(p), authorityDigest, "create", run, "", c.RequestID, c.DelegatedActor, jsonDigest(c)})
}

func stopReceipt(p serviceapi.Principal, authorityDigest, run, id string, c serviceapi.PreviewStopRequestV1) receipt {
	return bindReceipt(commandBinding{owner(p), authorityDigest, "stop", run, id, c.RequestID, c.DelegatedActor, jsonDigest(c)})
}

func bindReceipt(b commandBinding) receipt {
	return receipt{Key: receiptIdentity(b.Principal, b.RequestID), Digest: jsonDigest(b), Binding: b}
}

func (r receipt) valid(v serviceapi.PreviewV1, o ownerIdentity, creating bool) bool {
	b := r.Binding
	if b.Principal != o || !o.valid() || !sha256Pattern.MatchString(b.AuthorityDigest) ||
		b.RunID != v.RunID || r.Key != receiptIdentity(o, b.RequestID) || r.Digest != jsonDigest(b) || r.Result != v {
		return false
	}
	if creating {
		c := serviceapi.PreviewRequestV1{SchemaVersion: 1, RequestID: b.RequestID, ExpectedRunID: b.RunID, CheckpointActivityID: v.CheckpointActivityID, ProfileID: v.ProfileID, DelegatedActor: b.DelegatedActor}
		return b.Operation == "create" && b.PreviewID == "" && serviceapi.ValidatePreviewRequestV1(c, b.RunID) == nil &&
			b.BodyDigest == jsonDigest(c) && v.RequestID == b.RequestID && v.RequestDigest == r.Digest && v.PreviewID == jsonDigest([]string{r.Key, r.Digest})
	}
	c := serviceapi.PreviewStopRequestV1{SchemaVersion: 1, RequestID: b.RequestID, ExpectedRunID: b.RunID, ExpectedPreviewID: b.PreviewID, DelegatedActor: b.DelegatedActor}
	return b.Operation == "stop" && b.PreviewID == v.PreviewID && terminal(v.Status) &&
		serviceapi.ValidatePreviewStopRequestV1(c, b.RunID, b.PreviewID) == nil && b.BodyDigest == jsonDigest(c)
}

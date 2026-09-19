// Package runadmission implements the bounded EP-006 product-facing run
// admission bridge. Product requests select a public profile; all execution
// policy and filesystem authority remains controller-owned.
package runadmission

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/pankajleh/autonomous-builder-control-plane/internal/authority"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/runtimecatalog"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/serviceapi"
)

const (
	MaxProfileFileBytes      = 256 << 10
	MaxManifestTemplateBytes = 256 << 10
	MaxProfiles              = 32
	MaxReceiptBytes          = 16 << 10
	ReceiptLockTimeout       = 2 * time.Second
)

type CatalogReader interface {
	ReadRun(string) (runtimecatalog.RunRegistrationV1, error)
}

type Config struct {
	ProfileFile string
	ServiceRoot string
	Executable  string
	Catalog     CatalogReader
	Clock       func() time.Time
}

type ProfileFileV1 struct {
	SchemaVersion int         `json:"schema_version"`
	Profiles      []ProfileV1 `json:"profiles"`
}

type ProfileV1 struct {
	ProfileID                   string `json:"profile_id"`
	RepositoryPath              string `json:"repository_path"`
	RepositoryIdentity          string `json:"repository_identity"`
	ManifestTemplatePath        string `json:"manifest_template_path"`
	InputDirectory              string `json:"input_directory"`
	LedgerRoot                  string `json:"ledger_root"`
	EvidenceRoot                string `json:"evidence_root"`
	CgroupRoot                  string `json:"cgroup_root"`
	WorkflowAuthorityConfigPath string `json:"workflow_authority_config_path"`
}

type AdmissionReceiptV1 struct {
	Kind                   string               `json:"kind"`
	SchemaVersion          int                  `json:"schema_version"`
	Principal              serviceapi.Principal `json:"principal"`
	RequestID              string               `json:"request_id"`
	CanonicalRequestSHA256 string               `json:"canonical_request_sha256"`
	RunID                  string               `json:"run_id"`
	ProfileID              string               `json:"profile_id"`
	CreatedAt              string               `json:"created_at"`
}

type loadedProfile struct {
	configuration               ProfileV1
	template                    authority.Manifest
	inputPath                   string
	ledgerRoot                  string
	evidenceRoot                string
	cgroupRoot                  string
	workflowAuthorityConfigPath string
}

type Controller struct {
	serviceRoot  string
	executable   string
	catalog      CatalogReader
	clock        func() time.Time
	profiles     map[string]loadedProfile
	admissionsFD int
	launchesFD   int
	start        func(string, []string, *os.File) error
}

func RequestDigest(request serviceapi.RunAdmissionRequestV1) string {
	data, _ := json.Marshal(request)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func DeriveRunID(principalID, requestID string) string {
	digest := sha256.Sum256([]byte(principalID + "\x00" + requestID))
	return "admission-" + hex.EncodeToString(digest[:])
}

func receiptKey(principalID, requestID string) string {
	digest := sha256.Sum256([]byte(principalID + "\x00" + requestID))
	return hex.EncodeToString(digest[:])
}

func strictJSON(data []byte, target any) error {
	if err := rejectDuplicateFields(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func rejectDuplicateFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var parse func() error
	parse = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("invalid object key")
				}
				if _, duplicate := seen[key]; duplicate {
					return errors.New("duplicate JSON field")
				}
				seen[key] = struct{}{}
				if err := parse(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := parse(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return errors.New("invalid JSON delimiter")
		}
	}
	if err := parse(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func (c *Controller) Close() error {
	if c == nil || c.profiles == nil {
		return nil
	}
	var result error
	if c.launchesFD >= 0 {
		result = errors.Join(result, closeFD(c.launchesFD))
		c.launchesFD = -1
	}
	if c.admissionsFD >= 0 {
		result = errors.Join(result, closeFD(c.admissionsFD))
		c.admissionsFD = -1
	}
	return result
}

// Keep context in the interface even though an accepted launch deliberately
// outlives the HTTP request that admitted it.
func (c *Controller) AdmitRun(ctx context.Context, principal serviceapi.Principal, request serviceapi.RunAdmissionRequestV1) (serviceapi.RunAdmissionResponseV1, error) {
	return c.admitRun(ctx, principal, request)
}

// Package workflowauthoritypg provides the PostgreSQL CAS implementation of
// governance.WorkflowAuthorityBackendV1. Database schema installation is an
// explicit operator action; this package never creates or changes schema.
package workflowauthoritypg

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	governancev3 "github.com/pankajleh/autonomous-builder-control-plane/internal/governance"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/ralphex"
)

const (
	MaxConfigBytes   = 64 << 10
	MaxStateBytes    = 1 << 20
	openTimeout      = 5 * time.Second
	operationTimeout = 5 * time.Second
)

var (
	errConfiguration = errors.New("workflow authority configuration is invalid")
	errUnavailable   = errors.New("workflow authority storage is unavailable")
	errStateInvalid  = errors.New("workflow authority state is invalid or divergent")
)

type configurationV1 struct {
	SchemaVersion         int    `json:"schema_version"`
	ConnectionString      string `json:"connection_string"`
	AuthorityDomainSHA256 string `json:"authority_domain_sha256"`
}

type database interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Backend is a bounded PostgreSQL authority client. The connection string is
// held only by pgx configuration and is never returned or included in errors.
type Backend struct {
	pool            *pgxpool.Pool
	authorityDomain string
}

type storedRecord struct {
	controllerIdentity string
	authorityDomain    string
	repositoryIdentity string
	revision           uint64
	canonicalState     []byte
	stateSHA256        string
}

// ValidateConfigFile verifies only the protected strict-JSON configuration.
// It does not connect to PostgreSQL and never exposes connection details.
func ValidateConfigFile(path string) error {
	_, err := loadConfiguration(path)
	if err != nil {
		return errConfiguration
	}
	return nil
}

// Open reads one protected strict-JSON configuration file and verifies that
// the configured PostgreSQL authority is reachable. It never installs schema.
func Open(ctx context.Context, path string) (*Backend, error) {
	configuration, err := loadConfiguration(path)
	if err != nil {
		return nil, errConfiguration
	}
	poolConfiguration, err := pgxpool.ParseConfig(configuration.ConnectionString)
	if err != nil {
		return nil, errConfiguration
	}
	if poolConfiguration.ConnConfig.ConnectTimeout <= 0 || poolConfiguration.ConnConfig.ConnectTimeout > openTimeout {
		poolConfiguration.ConnConfig.ConnectTimeout = openTimeout
	}
	poolConfiguration.MinConns = 0
	poolConfiguration.MaxConns = 4
	bounded, cancel := boundedContext(ctx, openTimeout)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(bounded, poolConfiguration)
	if err != nil {
		return nil, errUnavailable
	}
	if err := pool.Ping(bounded); err != nil {
		pool.Close()
		return nil, errUnavailable
	}
	return &Backend{pool: pool, authorityDomain: configuration.AuthorityDomainSHA256}, nil
}

// Close releases PostgreSQL connections without exposing connection details.
func (b *Backend) Close() {
	if b != nil && b.pool != nil {
		b.pool.Close()
	}
}

// AuthorityDomainV1 returns the configured workflow-wide authority identity.
func (b *Backend) AuthorityDomainV1() (string, error) {
	if b == nil || b.pool == nil || !lowerSHA256(b.authorityDomain) {
		return "", errUnavailable
	}
	return b.authorityDomain, nil
}

// EnsureInitialized creates revision 1 once. A conflict is always read back
// and must already be a valid record for the exact controller, repository,
// and configured authority domain.
func (b *Backend) EnsureInitialized(ctx context.Context, controllerIdentity, repositoryIdentity string) error {
	if b == nil || b.pool == nil {
		return errUnavailable
	}
	record, err := initialRecord(controllerIdentity, b.authorityDomain, repositoryIdentity)
	if err != nil {
		return errStateInvalid
	}
	insertContext, cancelInsert := boundedContext(ctx, operationTimeout)
	_, _ = b.pool.Exec(insertContext, `
		INSERT INTO public.workflow_authority_v1
			(controller_identity, authority_domain, repository_identity, revision, canonical_state, state_sha256, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, clock_timestamp())
		ON CONFLICT (controller_identity) DO NOTHING`,
		record.controllerIdentity, record.authorityDomain, record.repositoryIdentity, int64(record.revision), record.canonicalState, record.stateSHA256)
	cancelInsert()

	// INSERT may have committed even when its client-visible result was a
	// timeout or disconnect. Use an independent bounded read-back so a spent
	// write context cannot turn that ambiguity into a divergent reinsert.
	reconcileContext, cancelReconcile := boundedContext(context.Background(), operationTimeout)
	defer cancelReconcile()
	observed, err := loadRecord(b.pool, reconcileContext, controllerIdentity, b.authorityDomain)
	if err != nil || observed.repositoryIdentity != repositoryIdentity {
		return errStateInvalid
	}
	return nil
}

// LoadWorkflowStateV1 loads and independently verifies the canonical bytes,
// stored digest, revision, controller, repository, and authority-domain bind.
func (b *Backend) LoadWorkflowStateV1(controllerIdentity string) ([]byte, uint64, error) {
	if b == nil || b.pool == nil || !lowerSHA256(controllerIdentity) {
		return nil, 0, errUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	record, err := loadRecord(b.pool, ctx, controllerIdentity, b.authorityDomain)
	if err != nil {
		return nil, 0, err
	}
	return append([]byte(nil), record.canonicalState...), record.revision, nil
}

// CompareAndSwapWorkflowStateV1 advances exactly expectedRevision. If the
// commit result is ambiguous, the authoritative row is read back and an exact
// desired-state match is accepted as the committed result.
func (b *Backend) CompareAndSwapWorkflowStateV1(controllerIdentity string, expectedRevision uint64, canonicalState []byte) (bool, error) {
	if b == nil || b.pool == nil || !lowerSHA256(controllerIdentity) || expectedRevision == 0 || expectedRevision >= math.MaxInt64 {
		return false, errStateInvalid
	}
	desired, err := desiredRecord(controllerIdentity, b.authorityDomain, expectedRevision, canonicalState)
	if err != nil {
		return false, errStateInvalid
	}
	writeContext, cancelWrite := context.WithTimeout(context.Background(), operationTimeout)
	tag, execErr := b.pool.Exec(writeContext, `
		UPDATE public.workflow_authority_v1
		SET revision = $4, canonical_state = $5, state_sha256 = $6, updated_at = clock_timestamp()
		WHERE controller_identity = $1
		  AND authority_domain = $2
		  AND repository_identity = $3
		  AND revision = $7`,
		desired.controllerIdentity, desired.authorityDomain, desired.repositoryIdentity, int64(desired.revision), desired.canonicalState, desired.stateSHA256, int64(expectedRevision))
	cancelWrite()
	if execErr == nil && tag.RowsAffected() == 1 {
		return true, nil
	}

	// Reconciliation must not inherit a canceled write context: a timeout can
	// be reported after PostgreSQL committed the exact desired row.
	reconcileContext, cancelReconcile := context.WithTimeout(context.Background(), operationTimeout)
	defer cancelReconcile()
	observed, loadErr := loadRecord(b.pool, reconcileContext, controllerIdentity, b.authorityDomain)
	if loadErr != nil {
		return false, errUnavailable
	}
	committed, reconcileErr := reconcileCAS(observed, desired)
	if reconcileErr != nil {
		return false, reconcileErr
	}
	if committed {
		return true, nil
	}
	if execErr != nil {
		return false, errUnavailable
	}
	return false, nil
}

func loadRecord(db database, ctx context.Context, controllerIdentity, authorityDomain string) (storedRecord, error) {
	if db == nil || !lowerSHA256(controllerIdentity) || !lowerSHA256(authorityDomain) {
		return storedRecord{}, errStateInvalid
	}
	var domain, repositoryIdentity, stateSHA256 string
	var revision int64
	var canonicalState []byte
	err := db.QueryRow(ctx, `
		SELECT authority_domain, repository_identity, revision, canonical_state, state_sha256
		FROM public.workflow_authority_v1
		WHERE controller_identity = $1 AND authority_domain = $2`, controllerIdentity, authorityDomain).
		Scan(&domain, &repositoryIdentity, &revision, &canonicalState, &stateSHA256)
	if err != nil {
		return storedRecord{}, errUnavailable
	}
	if revision <= 0 {
		return storedRecord{}, errStateInvalid
	}
	record := storedRecord{
		controllerIdentity: controllerIdentity, authorityDomain: domain, repositoryIdentity: repositoryIdentity,
		revision: uint64(revision), canonicalState: canonicalState, stateSHA256: stateSHA256,
	}
	if err := validateStoredRecord(record); err != nil {
		return storedRecord{}, err
	}
	return record, nil
}

func initialRecord(controllerIdentity, authorityDomain, repositoryIdentity string) (storedRecord, error) {
	if !lowerSHA256(controllerIdentity) || !lowerSHA256(authorityDomain) || !validRepositoryIdentity(repositoryIdentity) {
		return storedRecord{}, errStateInvalid
	}
	state := governancev3.ControllerStateV1{
		Kind: "GovernanceControllerStateV1", ControllerIdentity: controllerIdentity,
		RepositoryIdentity: repositoryIdentity, Revision: 1,
		IssuedV2Authorities: []governancev3.IssuedAuthorityV1{},
		ExecutionState:      ralphex.ExecutionStateV1{AggregateElapsed: "0s"},
		FindingEvidence:     []governancev3.FindingEvidenceV1{},
	}
	data, err := json.Marshal(state)
	if err != nil {
		return storedRecord{}, errStateInvalid
	}
	return storedRecord{
		controllerIdentity: controllerIdentity, authorityDomain: authorityDomain, repositoryIdentity: repositoryIdentity,
		revision: 1, canonicalState: data, stateSHA256: digest(data),
	}, nil
}

func desiredRecord(controllerIdentity, authorityDomain string, expectedRevision uint64, canonicalState []byte) (storedRecord, error) {
	if expectedRevision == 0 || expectedRevision >= math.MaxInt64 || len(canonicalState) == 0 || len(canonicalState) > MaxStateBytes {
		return storedRecord{}, errStateInvalid
	}
	var state governancev3.ControllerStateV1
	if err := governancev3.ParseCanonical(canonicalState, &state); err != nil || state.Kind != "GovernanceControllerStateV1" ||
		state.ControllerIdentity != controllerIdentity || state.Revision != expectedRevision+1 || !validRepositoryIdentity(state.RepositoryIdentity) ||
		state.IssuedV2Authorities == nil || state.FindingEvidence == nil || state.ExecutionState.AggregateElapsed == "" {
		return storedRecord{}, errStateInvalid
	}
	return storedRecord{
		controllerIdentity: controllerIdentity, authorityDomain: authorityDomain, repositoryIdentity: state.RepositoryIdentity,
		revision: state.Revision, canonicalState: append([]byte(nil), canonicalState...), stateSHA256: digest(canonicalState),
	}, nil
}

func validateStoredRecord(record storedRecord) error {
	if !lowerSHA256(record.controllerIdentity) || !lowerSHA256(record.authorityDomain) || !validRepositoryIdentity(record.repositoryIdentity) ||
		record.revision == 0 || record.revision > math.MaxInt64 || len(record.canonicalState) == 0 || len(record.canonicalState) > MaxStateBytes ||
		!lowerSHA256(record.stateSHA256) || record.stateSHA256 != digest(record.canonicalState) {
		return errStateInvalid
	}
	var state governancev3.ControllerStateV1
	if err := governancev3.ParseCanonical(record.canonicalState, &state); err != nil || state.Kind != "GovernanceControllerStateV1" ||
		state.ControllerIdentity != record.controllerIdentity || state.RepositoryIdentity != record.repositoryIdentity || state.Revision != record.revision ||
		state.IssuedV2Authorities == nil || state.FindingEvidence == nil || state.ExecutionState.AggregateElapsed == "" {
		return errStateInvalid
	}
	return nil
}

func reconcileCAS(observed, desired storedRecord) (bool, error) {
	if observed.controllerIdentity != desired.controllerIdentity || observed.authorityDomain != desired.authorityDomain || observed.repositoryIdentity != desired.repositoryIdentity {
		return false, errStateInvalid
	}
	return observed.revision == desired.revision && observed.stateSHA256 == desired.stateSHA256 && bytes.Equal(observed.canonicalState, desired.canonicalState), nil
}

func parseConfiguration(data []byte) (configurationV1, error) {
	var configuration configurationV1
	if len(data) == 0 || len(data) > MaxConfigBytes || rejectDuplicateFields(data) != nil {
		return configuration, errConfiguration
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&configuration); err != nil {
		return configurationV1{}, errConfiguration
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return configurationV1{}, errConfiguration
	}
	if configuration.SchemaVersion != 1 || configuration.ConnectionString == "" || len(configuration.ConnectionString) > 16<<10 ||
		strings.ContainsRune(configuration.ConnectionString, 0) || !lowerSHA256(configuration.AuthorityDomainSHA256) {
		return configurationV1{}, errConfiguration
	}
	return configuration, nil
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
					return errConfiguration
				}
				if _, duplicate := seen[key]; duplicate {
					return errConfiguration
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
			return errConfiguration
		}
	}
	if err := parse(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errConfiguration
	}
	return nil
}

func loadConfiguration(path string) (configurationV1, error) {
	data, err := readProtectedConfiguration(path, MaxConfigBytes)
	if err != nil {
		return configurationV1{}, errConfiguration
	}
	return parseConfiguration(data)
}

func boundedContext(parent context.Context, maximum time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, maximum)
}

func validRepositoryIdentity(value string) bool {
	return value != "" && len(value) <= 4096 && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "\x00\r\n")
}

func lowerSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Package postgres implements the durable V4 workflow-authority backend.
// PostgreSQL owns governance coordination; the append-only run ledger remains
// the operational run-state authority.
package postgres

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/governance"
)

const (
	connectTimeoutV1        = 5 * time.Second
	operationTimeoutV1      = 15 * time.Second
	maxConnectionsV1        = 4
	maxConnectionLifetimeV1 = 30 * time.Minute
	maxConnectionIdleTimeV1 = 5 * time.Minute
	healthCheckPeriodV1     = 30 * time.Second
)

type ConfigV1 struct {
	ConnectionString string
	AuthorityDomain  string
}

// PostgresWorkflowAuthorityBackendV1 is one authenticated authority-domain
// view over the abcp_v4 schema. It is safe for concurrent use.
type PostgresWorkflowAuthorityBackendV1 struct {
	pool            *pgxpool.Pool
	authorityDomain string
	runtimeRole     string
}

var _ governance.WorkflowAuthorityBackendV1 = (*PostgresWorkflowAuthorityBackendV1)(nil)

// OpenPostgresWorkflowAuthorityBackendV1 verifies transport, session role,
// phase-H hardening, forced RLS, policies, and literal function bodies before
// it exposes any state operation.
func OpenPostgresWorkflowAuthorityBackendV1(ctx context.Context, config ConfigV1) (*PostgresWorkflowAuthorityBackendV1, error) {
	if strings.TrimSpace(config.ConnectionString) == "" {
		return nil, errors.New("PostgreSQL connection string is required")
	}
	if err := validateAuthorityDomainV1(config.AuthorityDomain); err != nil {
		return nil, err
	}
	poolConfig, err := pgxpool.ParseConfig(config.ConnectionString)
	if err != nil {
		return nil, fmt.Errorf("parse PostgreSQL runtime connection: %w", err)
	}
	if err := validateTransportV1(poolConfig.ConnConfig.Host, poolConfig.ConnConfig.TLSConfig); err != nil {
		return nil, err
	}
	poolConfig.MaxConns = maxConnectionsV1
	poolConfig.MinConns = 0
	poolConfig.MaxConnLifetime = maxConnectionLifetimeV1
	poolConfig.MaxConnIdleTime = maxConnectionIdleTimeV1
	poolConfig.HealthCheckPeriod = healthCheckPeriodV1
	if poolConfig.ConnConfig.ConnectTimeout == 0 || poolConfig.ConnConfig.ConnectTimeout > connectTimeoutV1 {
		poolConfig.ConnConfig.ConnectTimeout = connectTimeoutV1
	}
	if poolConfig.ConnConfig.RuntimeParams == nil {
		poolConfig.ConnConfig.RuntimeParams = make(map[string]string)
	}
	for key, value := range map[string]string{
		"search_path": "pg_catalog,abcp_v4", "row_security": "on", "statement_timeout": "10s",
		"lock_timeout": "2s", "idle_in_transaction_session_timeout": "15s",
	} {
		poolConfig.ConnConfig.RuntimeParams[key] = value
	}
	poolConfig.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		return verifyRuntimeSessionV1(ctx, conn, config.AuthorityDomain)
	}

	openContext, cancel := boundedContextV1(ctx, connectTimeoutV1)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(openContext, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL runtime pool: %w", err)
	}
	backend := &PostgresWorkflowAuthorityBackendV1{pool: pool, authorityDomain: config.AuthorityDomain}
	if err := pool.QueryRow(openContext, `SELECT session_user::text`).Scan(&backend.runtimeRole); err != nil {
		pool.Close()
		return nil, fmt.Errorf("authenticate PostgreSQL runtime session: %w", err)
	}
	if err := VerifyFrozenCatalogV1(openContext, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("verify frozen abcp_v4 catalog: %w", err)
	}
	if err := VerifyFinalHardeningV1(openContext, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("verify PostgreSQL bootstrap hardening: %w", err)
	}
	return backend, nil
}

// NewPostgresWorkflowAuthorityBackendV1 is an alias retained for composition
// sites that use constructor naming rather than Open naming.
func NewPostgresWorkflowAuthorityBackendV1(ctx context.Context, config ConfigV1) (*PostgresWorkflowAuthorityBackendV1, error) {
	return OpenPostgresWorkflowAuthorityBackendV1(ctx, config)
}

func (b *PostgresWorkflowAuthorityBackendV1) Close() {
	if b != nil && b.pool != nil {
		b.pool.Close()
	}
}

func (b *PostgresWorkflowAuthorityBackendV1) AuthorityDomainV1() (string, error) {
	if b == nil || b.pool == nil || validateAuthorityDomainV1(b.authorityDomain) != nil {
		return "", errors.New("PostgreSQL workflow-authority backend is not open")
	}
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeoutV1)
	defer cancel()
	var count int
	err := b.pool.QueryRow(ctx, `SELECT count(*) FROM abcp_v4.abcp_authority_domain_v1 WHERE authority_domain=$1 AND runtime_role=session_user`, b.authorityDomain).Scan(&count)
	if err != nil || count != 1 {
		return "", errors.New("authenticated authority-domain mapping is unavailable or changed")
	}
	return b.authorityDomain, nil
}

func (b *PostgresWorkflowAuthorityBackendV1) LoadWorkflowStateV1(controllerIdentity string) ([]byte, uint64, error) {
	if err := b.validateOperationIdentityV1(controllerIdentity); err != nil {
		return nil, 0, err
	}
	ctx, cancel := boundedContextV1(context.Background(), operationTimeoutV1)
	defer cancel()
	return b.loadWorkflowStateContextV1(ctx, controllerIdentity)
}

func (b *PostgresWorkflowAuthorityBackendV1) loadWorkflowStateContextV1(ctx context.Context, controllerIdentity string) ([]byte, uint64, error) {
	var state []byte
	var revision int64
	var storedDigest string
	err := b.pool.QueryRow(ctx, `
		SELECT canonical_state, revision, btrim(state_sha256)
		FROM abcp_v4.abcp_workflow_authority_v1
		WHERE authority_domain=$1 AND controller_identity=$2`, b.authorityDomain, controllerIdentity).Scan(&state, &revision, &storedDigest)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, 0, errors.New("workflow-authority state is missing or uninitialized")
	}
	if err != nil {
		return nil, 0, fmt.Errorf("load workflow-authority state: %w", err)
	}
	if revision <= 0 || len(state) == 0 || len(state) > MaxCanonicalRequestBytesV1 || sha256HexV1(state) != storedDigest {
		return nil, 0, errors.New("workflow-authority row is corrupt or outside its frozen bound")
	}
	return append([]byte(nil), state...), uint64(revision), nil
}

func (b *PostgresWorkflowAuthorityBackendV1) CompareAndSwapWorkflowStateV1(controllerIdentity string, expectedRevision uint64, canonicalState []byte) (bool, error) {
	if err := b.validateOperationIdentityV1(controllerIdentity); err != nil {
		return false, err
	}
	if expectedRevision == 0 || expectedRevision == ^uint64(0) {
		return false, errors.New("expected workflow revision is invalid or exhausted")
	}
	if err := validateCanonicalJSONV1(canonicalState, MaxCanonicalRequestBytesV1); err != nil {
		return false, fmt.Errorf("workflow state: %w", err)
	}
	var identity struct {
		ControllerIdentity string `json:"controller_identity"`
		Revision           uint64 `json:"revision"`
	}
	if err := json.Unmarshal(canonicalState, &identity); err != nil || identity.ControllerIdentity != controllerIdentity || identity.Revision != expectedRevision+1 {
		return false, errors.New("workflow state identity or successor revision is invalid")
	}
	state := append([]byte(nil), canonicalState...)
	digest := sha256HexV1(state)
	ctx, cancel := boundedContextV1(context.Background(), operationTimeoutV1)
	defer cancel()
	tx, err := b.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: pgx.ReadWrite})
	if err != nil {
		return false, fmt.Errorf("begin workflow-authority CAS: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	command, err := tx.Exec(ctx, `
		UPDATE abcp_v4.abcp_workflow_authority_v1
		SET revision=$4, canonical_state=$5, state_sha256=$6, updated_at=clock_timestamp()
		WHERE authority_domain=$1 AND controller_identity=$2 AND revision=$3`,
		b.authorityDomain, controllerIdentity, int64(expectedRevision), int64(expectedRevision+1), state, digest)
	if err != nil {
		return false, fmt.Errorf("update workflow-authority CAS: %w", err)
	}
	if command.RowsAffected() == 0 {
		if err := tx.Rollback(ctx); err != nil {
			return false, fmt.Errorf("rollback losing workflow-authority CAS: %w", err)
		}
		return false, nil
	}
	if command.RowsAffected() != 1 {
		return false, errors.New("workflow-authority CAS affected a non-unique row set")
	}
	if err := tx.Commit(ctx); err != nil {
		return b.reconcileWorkflowCASV1(controllerIdentity, expectedRevision+1, state, digest, err)
	}
	return true, nil
}

func (b *PostgresWorkflowAuthorityBackendV1) reconcileWorkflowCASV1(controllerIdentity string, revision uint64, state []byte, digest string, commitErr error) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeoutV1)
	defer cancel()
	loaded, loadedRevision, err := b.loadWorkflowStateContextV1(ctx, controllerIdentity)
	if err == nil && loadedRevision == revision && bytes.Equal(loaded, state) && sha256HexV1(loaded) == digest {
		return true, nil
	}
	return false, fmt.Errorf("workflow-authority commit outcome is ambiguous and exact read-back did not prove application: %w", commitErr)
}

// PutArtifactBlobV1 performs content-addressed create-or-verify. A conflicting
// row at the same digest never gains authority.
func (b *PostgresWorkflowAuthorityBackendV1) PutArtifactBlobV1(ctx context.Context, canonicalBytes []byte) (string, error) {
	if b == nil || b.pool == nil || len(canonicalBytes) == 0 || len(canonicalBytes) > MaxArtifactBytesV1 {
		return "", errors.New("artifact backend or bytes are invalid")
	}
	digest := sha256HexV1(canonicalBytes)
	opctx, cancel := boundedContextV1(ctx, operationTimeoutV1)
	defer cancel()
	tx, err := b.pool.BeginTx(opctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(opctx, `INSERT INTO abcp_v4.abcp_artifact_blob_v1(authority_domain,artifact_sha256,canonical_bytes,byte_size,created_at) VALUES($1,$2,$3,$4,clock_timestamp()) ON CONFLICT DO NOTHING`, b.authorityDomain, digest, canonicalBytes, len(canonicalBytes)); err != nil {
		return "", err
	}
	var existing []byte
	var size int64
	if err := tx.QueryRow(opctx, `SELECT canonical_bytes,byte_size FROM abcp_v4.abcp_artifact_blob_v1 WHERE authority_domain=$1 AND artifact_sha256=$2`, b.authorityDomain, digest).Scan(&existing, &size); err != nil || size != int64(len(canonicalBytes)) || !bytes.Equal(existing, canonicalBytes) {
		return "", errors.New("artifact create-or-verify conflict")
	}
	if err := tx.Commit(opctx); err != nil {
		return "", fmt.Errorf("artifact commit outcome is ambiguous: %w", err)
	}
	return digest, nil
}

func (b *PostgresWorkflowAuthorityBackendV1) LoadArtifactBlobV1(ctx context.Context, digest string) ([]byte, error) {
	if b == nil || b.pool == nil || validateDBIdentityV1(digest) != nil {
		return nil, errors.New("artifact backend or digest is invalid")
	}
	opctx, cancel := boundedContextV1(ctx, operationTimeoutV1)
	defer cancel()
	var data []byte
	var size int64
	if err := b.pool.QueryRow(opctx, `SELECT canonical_bytes,byte_size FROM abcp_v4.abcp_artifact_blob_v1 WHERE authority_domain=$1 AND artifact_sha256=$2`, b.authorityDomain, digest).Scan(&data, &size); err != nil {
		return nil, err
	}
	if size != int64(len(data)) || len(data) == 0 || len(data) > MaxArtifactBytesV1 || sha256HexV1(data) != digest {
		return nil, errors.New("artifact row is corrupt")
	}
	return append([]byte(nil), data...), nil
}

// CallFrozenFunctionV1 invokes exactly one of the ten bytea functions. Both
// request and result are strict-canonical frozen wire records.
func (b *PostgresWorkflowAuthorityBackendV1) CallFrozenFunctionV1(ctx context.Context, function string, request, result any) error {
	if b == nil || b.pool == nil || result == nil {
		return errors.New("PostgreSQL backend and result destination are required")
	}
	data, contract, err := marshalOperationRequestV1(function, request)
	if err != nil {
		return err
	}
	var domainEnvelope struct {
		AuthorityDomain string `json:"authority_domain"`
	}
	_ = json.Unmarshal(data, &domainEnvelope)
	if domainEnvelope.AuthorityDomain != b.authorityDomain {
		return errors.New("operation request selects a different authority domain")
	}
	opctx, cancel := boundedContextV1(ctx, operationTimeoutV1)
	defer cancel()
	tx, err := b.pool.BeginTx(opctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var returned []byte
	query := `SELECT abcp_v4.` + function + `($1)`
	if err := tx.QueryRow(opctx, query, data).Scan(&returned); err != nil {
		return err
	}
	if err := unmarshalOperationResultV1(returned, contract, result); err != nil {
		return err
	}
	if err := tx.Commit(opctx); err != nil {
		return fmt.Errorf("%s commit outcome is ambiguous: %w", function, err)
	}
	return nil
}

func (b *PostgresWorkflowAuthorityBackendV1) validateOperationIdentityV1(controllerIdentity string) error {
	if b == nil || b.pool == nil {
		return errors.New("PostgreSQL workflow-authority backend is not open")
	}
	if err := validateDBIdentityV1(controllerIdentity); err != nil {
		return err
	}
	return nil
}

func validateTransportV1(host string, tlsConfig *tls.Config) error {
	if strings.HasPrefix(host, "/") || isLoopbackHostV1(host) {
		return nil
	}
	if tlsConfig == nil || tlsConfig.InsecureSkipVerify || strings.TrimSpace(tlsConfig.ServerName) == "" || tlsConfig.RootCAs == nil {
		return errors.New("non-loopback PostgreSQL requires verify-full TLS with a CA and hostname")
	}
	return nil
}

func isLoopbackHostV1(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func boundedContextV1(parent context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) <= limit {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, limit)
}

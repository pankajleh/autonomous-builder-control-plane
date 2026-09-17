package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/governance"
)

type BootstrapConfigV1 struct {
	ProvisionerConnectionString string                       `json:"provisioner_connection_string"`
	MigratorConnectionString    string                       `json:"migrator_connection_string"`
	MigratorPassword            string                       `json:"migrator_password"`
	DatabaseName                string                       `json:"database_name"`
	Domains                     []AuthorityDomainBootstrapV1 `json:"domains"`
	Workers                     []WorkerBootstrapV1          `json:"workers"`
	Artifacts                   []ArtifactBootstrapV1        `json:"artifacts"`
	Stages                      []StageBootstrapV1           `json:"stages"`
}

type AuthorityDomainBootstrapV1 struct {
	AuthorityDomain           string                            `json:"authority_domain"`
	RuntimeRole               string                            `json:"runtime_role"`
	RuntimePassword           string                            `json:"runtime_password"`
	RecoveryVerifierRole      string                            `json:"recovery_verifier_role"`
	RecoveryVerifierPassword  string                            `json:"recovery_verifier_password"`
	BootstrapProvenanceSHA256 string                            `json:"bootstrap_provenance_sha256"`
	Workflows                 []WorkflowBootstrapV1             `json:"workflows"`
	PredecessorDirectories    []PredecessorDirectoryBootstrapV1 `json:"predecessor_directories"`
}

type WorkflowBootstrapV1 struct {
	ControllerIdentity string    `json:"controller_identity"`
	Revision           uint64    `json:"revision"`
	CanonicalState     []byte    `json:"canonical_state"`
	UpdatedAt          time.Time `json:"updated_at"`
}

type PredecessorDirectoryBootstrapV1 struct {
	ControllerIdentity string `json:"controller_identity"`
	WriterEpoch        uint64 `json:"writer_epoch"`
	WriterState        string `json:"writer_state"`
	CanonicalDirectory []byte `json:"canonical_directory"`
	DirectorySHA256    string `json:"directory_sha256"`
	Revision           uint64 `json:"revision"`
}

type WorkerBootstrapV1 struct {
	WorkerIdentity            string    `json:"worker_identity"`
	CanonicalCapacity         []byte    `json:"canonical_capacity"`
	CapacitySHA256            string    `json:"capacity_sha256"`
	CapacityRevision          uint64    `json:"capacity_revision"`
	CanonicalKey              []byte    `json:"canonical_key"`
	CanonicalRegistration     []byte    `json:"canonical_registration"`
	HostIdentity              string    `json:"host_identity"`
	ObserverIdentity          string    `json:"observer_identity"`
	ObserverKeySHA256         string    `json:"observer_key_sha256"`
	RegistrationSHA256        string    `json:"registration_sha256"`
	RegistrationRevision      uint64    `json:"registration_revision"`
	WorkerCapacitySHA256      string    `json:"worker_capacity_sha256"`
	BootstrapProvenanceSHA256 string    `json:"bootstrap_provenance_sha256"`
	RegisteredAt              time.Time `json:"registered_at"`
}

type ArtifactBootstrapV1 struct {
	AuthorityDomain string    `json:"authority_domain"`
	CanonicalBytes  []byte    `json:"canonical_bytes"`
	CreatedAt       time.Time `json:"created_at"`
}

type StageBootstrapV1 struct {
	AuthorityDomain       string `json:"authority_domain"`
	LineageSHA256         string `json:"lineage_sha256"`
	Stage                 string `json:"stage"`
	SubjectSHA256         string `json:"subject_sha256"`
	SealState             string `json:"seal_state"`
	NextOccurrenceOrdinal uint64 `json:"next_occurrence_ordinal"`
	Revision              uint64 `json:"revision"`
}

// BootstrapV1 executes the frozen provisioner→migrator→hardener protocol.
// Each phase uses a distinct connection. A committed schema is never exposed
// as usable until a later fresh provisioner connection verifies phase H.
func BootstrapV1(ctx context.Context, config BootstrapConfigV1) error {
	if err := validateBootstrapConfigV1(config); err != nil {
		return err
	}
	phaseContext, cancel := boundedContextV1(ctx, operationTimeoutV1)
	defer cancel()
	provisioner, err := connectBootstrapV1(phaseContext, config.ProvisionerConnectionString)
	if err != nil {
		return fmt.Errorf("connect phase-P provisioner: %w", err)
	}
	var schemaExists bool
	if err := provisioner.QueryRow(phaseContext, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname='abcp_v4')`).Scan(&schemaExists); err != nil {
		provisioner.Close(context.Background())
		return fmt.Errorf("reconcile phase-M schema: %w", err)
	}
	if !schemaExists {
		if err := provisionV1(phaseContext, provisioner, config); err != nil {
			provisioner.Close(context.Background())
			return fmt.Errorf("PostgreSQL bootstrap phase P: %w", err)
		}
	} else if err := verifyProvisionedRoleTopologyV1(phaseContext, provisioner, config); err != nil {
		provisioner.Close(context.Background())
		return fmt.Errorf("reconcile provisioned role topology: %w", err)
	}
	provisioner.Close(context.Background())

	if !schemaExists {
		migrator, err := connectBootstrapV1(phaseContext, config.MigratorConnectionString)
		if err != nil {
			return fmt.Errorf("connect phase-M migrator: %w", err)
		}
		if err := migrateV1(phaseContext, migrator, config); err != nil {
			migrator.Close(context.Background())
			return fmt.Errorf("PostgreSQL bootstrap phase M: %w", err)
		}
		migrator.Close(context.Background())
	} else {
		// A committed M is never trusted merely because the schema exists. If
		// the migrator is still usable, replay reopens the exact M authority,
		// verifies every configured bootstrap row byte-for-byte, and only then
		// permits the separate provisioner to finalize H. A NOLOGIN migrator is
		// accepted here only when a fresh catalog check proves H already
		// completed atomically; configured rows are rechecked below through the
		// domain runtimes after final hardening verification.
		migrator, connectErr := connectBootstrapV1(phaseContext, config.MigratorConnectionString)
		if connectErr == nil {
			if err := verifyMigratorSetAuthorityV1(phaseContext, migrator); err != nil {
				migrator.Close(context.Background())
				return fmt.Errorf("reconcile committed phase-M authority: %w", err)
			}
			if err := verifyCommittedBootstrapRowsV1(phaseContext, migrator, config); err != nil {
				migrator.Close(context.Background())
				return fmt.Errorf("reconcile committed phase-M bootstrap rows: %w", err)
			}
			if err := VerifyFrozenCatalogV1(phaseContext, migrator); err != nil {
				migrator.Close(context.Background())
				return fmt.Errorf("reconcile committed phase-M catalog: %w", err)
			}
			migrator.Close(context.Background())
		} else {
			verifier, err := connectBootstrapV1(phaseContext, config.ProvisionerConnectionString)
			if err != nil {
				return fmt.Errorf("reconcile ambiguous phase-H verifier: %w", err)
			}
			if err := VerifyFinalHardeningV1(phaseContext, verifier); err != nil {
				verifier.Close(context.Background())
				return fmt.Errorf("migrator is unavailable before verified phase H: %w", errors.Join(connectErr, err))
			}
			verifier.Close(context.Background())
		}
	}

	hardener, err := connectBootstrapV1(phaseContext, config.ProvisionerConnectionString)
	if err != nil {
		return fmt.Errorf("connect phase-H provisioner: %w", err)
	}
	if err := hardenV1(phaseContext, hardener, config.DatabaseName); err != nil {
		hardener.Close(context.Background())
		return fmt.Errorf("PostgreSQL bootstrap phase H: %w", err)
	}
	hardener.Close(context.Background())

	verifier, err := connectBootstrapV1(phaseContext, config.ProvisionerConnectionString)
	if err != nil {
		return fmt.Errorf("connect fresh phase-H verifier: %w", err)
	}
	defer verifier.Close(context.Background())
	if err := VerifyFinalHardeningV1(phaseContext, verifier); err != nil {
		return fmt.Errorf("fresh phase-H verification: %w", err)
	}
	if err := verifyWorkerBootstrapRowsAfterHardeningV1(phaseContext, verifier, config); err != nil {
		return fmt.Errorf("fresh worker bootstrap-row verification: %w", err)
	}
	if err := VerifyFrozenCatalogV1(phaseContext, verifier); err != nil {
		return fmt.Errorf("fresh frozen-catalog verification: %w", err)
	}
	if err := verifyBootstrapRowsThroughRuntimeV1(phaseContext, config); err != nil {
		return fmt.Errorf("fresh bootstrap-row verification: %w", err)
	}
	return nil
}

func provisionV1(ctx context.Context, conn *pgx.Conn, config BootstrapConfigV1) error {
	var sessionUser, currentUser, database string
	var canLogin, superuser, createDB, createRole, replication, bypassRLS bool
	if err := conn.QueryRow(ctx, `SELECT session_user::text,current_user::text,current_database(),rolcanlogin,rolsuper,rolcreatedb,rolcreaterole,rolreplication,rolbypassrls FROM pg_catalog.pg_roles WHERE rolname=session_user`).Scan(&sessionUser, &currentUser, &database, &canLogin, &superuser, &createDB, &createRole, &replication, &bypassRLS); err != nil {
		return err
	}
	if database != config.DatabaseName || sessionUser != currentUser || isStaticRoleV1(sessionUser) || !canLogin || superuser || createDB || !createRole || replication || bypassRLS {
		return errors.New("bootstrap provisioner identity or attributes are invalid")
	}
	var grantable bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS(
		  SELECT 1 FROM pg_catalog.pg_database d,
		  LATERAL pg_catalog.aclexplode(coalesce(d.datacl,pg_catalog.acldefault('d',d.datdba))) a
		  WHERE d.datname=current_database() AND a.grantee=(SELECT oid FROM pg_catalog.pg_roles WHERE rolname=session_user)
		    AND a.privilege_type='CREATE' AND a.is_grantable)`).Scan(&grantable); err != nil || !grantable {
		return errors.New("bootstrap provisioner lacks database CREATE WITH GRANT OPTION")
	}

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := createOrVerifyRoleV1(ctx, tx, MigratorRoleV1, true, true, config.MigratorPassword); err != nil {
		return err
	}
	var staticRoleCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_catalog.pg_roles WHERE rolname=ANY($1)`, []string{SchemaOwnerRoleV1, RuntimeGroupRoleV1, RecoveryVerifierGroupRoleV1, DomainFunctionOwnerRoleV1, WorkerFunctionOwnerRoleV1}).Scan(&staticRoleCount); err != nil {
		return err
	}
	if staticRoleCount == 0 {
		if _, err := tx.Exec(ctx, frozenProvisionSQL); err != nil {
			return fmt.Errorf("execute frozen phase-P role SQL: %w", err)
		}
	}
	for _, role := range []string{SchemaOwnerRoleV1, RuntimeGroupRoleV1, RecoveryVerifierGroupRoleV1, DomainFunctionOwnerRoleV1, WorkerFunctionOwnerRoleV1} {
		if err := createOrVerifyRoleV1(ctx, tx, role, false, false, ""); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `GRANT abcp_v4_schema_owner,abcp_v4_domain_fn,abcp_v4_worker_fn TO abcp_v4_migrator WITH ADMIN OPTION`); err != nil {
		return err
	}
	databaseID, _ := quoteIdentifierV1(config.DatabaseName)
	if _, err := tx.Exec(ctx, `GRANT CREATE ON DATABASE `+databaseID+` TO abcp_v4_schema_owner`); err != nil {
		return err
	}
	for _, domain := range config.Domains {
		if err := createOrVerifyRoleV1(ctx, tx, domain.RuntimeRole, true, false, domain.RuntimePassword); err != nil {
			return err
		}
		if err := createOrVerifyRoleV1(ctx, tx, domain.RecoveryVerifierRole, true, false, domain.RecoveryVerifierPassword); err != nil {
			return err
		}
		runtimeID, _ := quoteIdentifierV1(domain.RuntimeRole)
		recoveryID, _ := quoteIdentifierV1(domain.RecoveryVerifierRole)
		if _, err := tx.Exec(ctx, `GRANT abcp_v4_runtime TO `+runtimeID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `GRANT abcp_v4_recovery_verifier TO `+recoveryID); err != nil {
			return err
		}
	}
	if err := verifyPhasePMembershipsV1(ctx, tx, config); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("phase-P commit outcome is ambiguous: %w", err)
	}
	return nil
}

func migrateV1(ctx context.Context, conn *pgx.Conn, config BootstrapConfigV1) error {
	var sessionUser, currentUser string
	var canLogin, superuser, createDB, createRole, replication, bypassRLS bool
	if err := conn.QueryRow(ctx, `SELECT session_user::text,current_user::text,rolcanlogin,rolsuper,rolcreatedb,rolcreaterole,rolreplication,rolbypassrls FROM pg_catalog.pg_roles WHERE rolname=session_user`).Scan(&sessionUser, &currentUser, &canLogin, &superuser, &createDB, &createRole, &replication, &bypassRLS); err != nil {
		return err
	}
	if sessionUser != MigratorRoleV1 || currentUser != sessionUser || !canLogin || superuser || createDB || !createRole || replication || bypassRLS {
		return errors.New("phase-M session is not the exact unhardened migrator")
	}
	if err := verifyMigratorSetAuthorityV1(ctx, conn); err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, frozenSchemaSQL); err != nil {
		return fmt.Errorf("execute frozen schema SQL: %w", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE abcp_v4_schema_owner`); err != nil {
		return err
	}
	if err := insertBootstrapRowsV1(ctx, tx, config); err != nil {
		return err
	}
	if err := verifyBootstrapRowsV1(ctx, tx, config, ""); err != nil {
		return fmt.Errorf("verify phase-M bootstrap rows: %w", err)
	}
	if _, err := tx.Exec(ctx, `RESET ROLE`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, frozenFunctionsSQL); err != nil {
		return fmt.Errorf("execute frozen functions/policies SQL: %w", err)
	}
	if err := VerifyFrozenCatalogV1(ctx, tx); err != nil {
		return fmt.Errorf("verify phase-M catalog before commit: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("phase-M commit outcome is ambiguous: %w", err)
	}
	return nil
}

func hardenV1(ctx context.Context, conn *pgx.Conn, databaseName string) error {
	var sessionUser string
	var createRole, superuser, bypassRLS bool
	if err := conn.QueryRow(ctx, `SELECT session_user::text,rolcreaterole,rolsuper,rolbypassrls FROM pg_catalog.pg_roles WHERE rolname=session_user`).Scan(&sessionUser, &createRole, &superuser, &bypassRLS); err != nil {
		return err
	}
	if isStaticRoleV1(sessionUser) || !createRole || superuser || bypassRLS {
		return errors.New("phase-H session is not the external bootstrap provisioner")
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	databaseID, _ := quoteIdentifierV1(databaseName)
	if _, err := tx.Exec(ctx, `REVOKE CREATE ON DATABASE `+databaseID+` FROM abcp_v4_schema_owner`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, frozenHardenSQL); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("phase-H commit outcome is ambiguous: %w", err)
	}
	return nil
}

func insertBootstrapRowsV1(ctx context.Context, tx pgx.Tx, config BootstrapConfigV1) error {
	for _, domain := range config.Domains {
		if _, err := tx.Exec(ctx, `INSERT INTO abcp_v4.abcp_authority_domain_v1(authority_domain,runtime_role,recovery_verifier_role,bootstrap_provenance_sha256) VALUES($1,$2,$3,$4)`, domain.AuthorityDomain, domain.RuntimeRole, domain.RecoveryVerifierRole, domain.BootstrapProvenanceSHA256); err != nil {
			return err
		}
		for _, workflow := range domain.Workflows {
			if _, err := tx.Exec(ctx, `INSERT INTO abcp_v4.abcp_workflow_authority_v1(authority_domain,controller_identity,revision,canonical_state,state_sha256,updated_at) VALUES($1,$2,$3,$4,$5,$6)`, domain.AuthorityDomain, workflow.ControllerIdentity, int64(workflow.Revision), workflow.CanonicalState, sha256HexV1(workflow.CanonicalState), canonicalSecondV1(workflow.UpdatedAt)); err != nil {
				return err
			}
		}
		for _, directory := range domain.PredecessorDirectories {
			if _, err := tx.Exec(ctx, `INSERT INTO abcp_v4.abcp_predecessor_directory_v1(authority_domain,controller_identity,writer_epoch,writer_state,canonical_directory,directory_sha256,revision) VALUES($1,$2,$3,$4,$5,$6,$7)`, domain.AuthorityDomain, directory.ControllerIdentity, int64(directory.WriterEpoch), directory.WriterState, directory.CanonicalDirectory, directory.DirectorySHA256, int64(directory.Revision)); err != nil {
				return err
			}
		}
	}
	for _, worker := range config.Workers {
		if _, err := tx.Exec(ctx, `INSERT INTO abcp_v4.abcp_worker_capacity_v1(worker_identity,canonical_capacity,capacity_sha256,revision) VALUES($1,$2,$3,$4)`, worker.WorkerIdentity, worker.CanonicalCapacity, worker.CapacitySHA256, int64(worker.CapacityRevision)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO abcp_v4.abcp_worker_observation_key_registration_v1(worker_identity,host_identity,observer_identity,observer_key_sha256,registration_sha256,registration_revision,worker_capacity_sha256,bootstrap_provenance_sha256,canonical_registration,canonical_key,registered_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, worker.WorkerIdentity, worker.HostIdentity, worker.ObserverIdentity, worker.ObserverKeySHA256, worker.RegistrationSHA256, int64(worker.RegistrationRevision), worker.WorkerCapacitySHA256, worker.BootstrapProvenanceSHA256, worker.CanonicalRegistration, worker.CanonicalKey, canonicalSecondV1(worker.RegisteredAt)); err != nil {
			return err
		}
	}
	for _, artifact := range config.Artifacts {
		if _, err := tx.Exec(ctx, `INSERT INTO abcp_v4.abcp_artifact_blob_v1(authority_domain,artifact_sha256,canonical_bytes,byte_size,created_at) VALUES($1,$2,$3,$4,$5)`, artifact.AuthorityDomain, sha256HexV1(artifact.CanonicalBytes), artifact.CanonicalBytes, len(artifact.CanonicalBytes), canonicalSecondV1(artifact.CreatedAt)); err != nil {
			return err
		}
	}
	for _, stage := range config.Stages {
		if _, err := tx.Exec(ctx, `INSERT INTO abcp_v4.abcp_stage_seal_v1(authority_domain,lineage_sha256,stage,subject_sha256,seal_state,next_occurrence_ordinal,revision) VALUES($1,$2,$3,$4,$5,$6,$7)`, stage.AuthorityDomain, stage.LineageSHA256, stage.Stage, stage.SubjectSHA256, stage.SealState, int64(stage.NextOccurrenceOrdinal), int64(stage.Revision)); err != nil {
			return err
		}
	}
	return nil
}

// verifyBootstrapRowsV1 proves that every configured create-or-verify row is
// still byte-identical to the phase-M input. When authorityDomain is nonempty
// it verifies only rows visible to that domain's hardened runtime role.
func verifyBootstrapRowsV1(ctx context.Context, query catalogQuerierV1, config BootstrapConfigV1, authorityDomain string) error {
	for _, domain := range config.Domains {
		if authorityDomain != "" && domain.AuthorityDomain != authorityDomain {
			continue
		}
		var runtimeRole, recoveryRole, provenance string
		if err := query.QueryRow(ctx, `SELECT runtime_role::text,recovery_verifier_role::text,btrim(bootstrap_provenance_sha256) FROM abcp_v4.abcp_authority_domain_v1 WHERE authority_domain=$1`, domain.AuthorityDomain).Scan(&runtimeRole, &recoveryRole, &provenance); err != nil {
			return fmt.Errorf("authority-domain bootstrap row %s: %w", domain.AuthorityDomain, err)
		}
		if runtimeRole != domain.RuntimeRole || recoveryRole != domain.RecoveryVerifierRole || provenance != domain.BootstrapProvenanceSHA256 {
			return fmt.Errorf("authority-domain bootstrap row %s differs from configuration", domain.AuthorityDomain)
		}
		for _, workflow := range domain.Workflows {
			var revision int64
			var canonical []byte
			var digest string
			var updated time.Time
			if err := query.QueryRow(ctx, `SELECT revision,canonical_state,btrim(state_sha256),updated_at FROM abcp_v4.abcp_workflow_authority_v1 WHERE authority_domain=$1 AND controller_identity=$2`, domain.AuthorityDomain, workflow.ControllerIdentity).Scan(&revision, &canonical, &digest, &updated); err != nil {
				return fmt.Errorf("workflow bootstrap row %s: %w", workflow.ControllerIdentity, err)
			}
			if revision != int64(workflow.Revision) || !bytes.Equal(canonical, workflow.CanonicalState) || digest != sha256HexV1(workflow.CanonicalState) || !updated.Equal(canonicalSecondV1(workflow.UpdatedAt)) {
				return fmt.Errorf("workflow bootstrap row %s differs from configuration", workflow.ControllerIdentity)
			}
		}
		for _, directory := range domain.PredecessorDirectories {
			var epoch, revision int64
			var state, digest string
			var canonical []byte
			if err := query.QueryRow(ctx, `SELECT writer_epoch,writer_state,canonical_directory,btrim(directory_sha256),revision FROM abcp_v4.abcp_predecessor_directory_v1 WHERE authority_domain=$1 AND controller_identity=$2`, domain.AuthorityDomain, directory.ControllerIdentity).Scan(&epoch, &state, &canonical, &digest, &revision); err != nil {
				return fmt.Errorf("predecessor-directory bootstrap row %s: %w", directory.ControllerIdentity, err)
			}
			if epoch != int64(directory.WriterEpoch) || state != directory.WriterState || !bytes.Equal(canonical, directory.CanonicalDirectory) || digest != directory.DirectorySHA256 || revision != int64(directory.Revision) {
				return fmt.Errorf("predecessor-directory bootstrap row %s differs from configuration", directory.ControllerIdentity)
			}
		}
	}
	if authorityDomain != "" {
		for _, artifact := range config.Artifacts {
			if artifact.AuthorityDomain != authorityDomain {
				continue
			}
			var canonical []byte
			var size int64
			var created time.Time
			digest := sha256HexV1(artifact.CanonicalBytes)
			if err := query.QueryRow(ctx, `SELECT canonical_bytes,byte_size,created_at FROM abcp_v4.abcp_artifact_blob_v1 WHERE authority_domain=$1 AND artifact_sha256=$2`, authorityDomain, digest).Scan(&canonical, &size, &created); err != nil {
				return fmt.Errorf("artifact bootstrap row %s: %w", digest, err)
			}
			if !bytes.Equal(canonical, artifact.CanonicalBytes) || size != int64(len(artifact.CanonicalBytes)) || !created.Equal(canonicalSecondV1(artifact.CreatedAt)) {
				return fmt.Errorf("artifact bootstrap row %s differs from configuration", digest)
			}
		}
		for _, stage := range config.Stages {
			if stage.AuthorityDomain != authorityDomain {
				continue
			}
			var state string
			var ordinal, revision int64
			if err := query.QueryRow(ctx, `SELECT seal_state,next_occurrence_ordinal,revision FROM abcp_v4.abcp_stage_seal_v1 WHERE authority_domain=$1 AND lineage_sha256=$2 AND stage=$3 AND subject_sha256=$4`, stage.AuthorityDomain, stage.LineageSHA256, stage.Stage, stage.SubjectSHA256).Scan(&state, &ordinal, &revision); err != nil {
				return fmt.Errorf("stage bootstrap row %s/%s: %w", stage.Stage, stage.SubjectSHA256, err)
			}
			if state != stage.SealState || ordinal != int64(stage.NextOccurrenceOrdinal) || revision != int64(stage.Revision) {
				return fmt.Errorf("stage bootstrap row %s/%s differs from configuration", stage.Stage, stage.SubjectSHA256)
			}
		}
		return nil
	}
	if err := verifyWorkerBootstrapRowsV1(ctx, query, config.Workers); err != nil {
		return err
	}
	for _, artifact := range config.Artifacts {
		if err := verifyBootstrapRowsV1(ctx, query, BootstrapConfigV1{Domains: config.Domains, Artifacts: []ArtifactBootstrapV1{artifact}}, artifact.AuthorityDomain); err != nil {
			return err
		}
	}
	for _, stage := range config.Stages {
		if err := verifyBootstrapRowsV1(ctx, query, BootstrapConfigV1{Domains: config.Domains, Stages: []StageBootstrapV1{stage}}, stage.AuthorityDomain); err != nil {
			return err
		}
	}
	return nil
}

func verifyWorkerBootstrapRowsV1(ctx context.Context, query catalogQuerierV1, workers []WorkerBootstrapV1) error {
	for _, worker := range workers {
		var capacity []byte
		var capacityDigest string
		var capacityRevision int64
		if err := query.QueryRow(ctx, `SELECT canonical_capacity,btrim(capacity_sha256),revision FROM abcp_v4.abcp_worker_capacity_v1 WHERE worker_identity=$1`, worker.WorkerIdentity).Scan(&capacity, &capacityDigest, &capacityRevision); err != nil {
			return fmt.Errorf("worker-capacity bootstrap row %s: %w", worker.WorkerIdentity, err)
		}
		if !bytes.Equal(capacity, worker.CanonicalCapacity) || capacityDigest != worker.CapacitySHA256 || capacityRevision != int64(worker.CapacityRevision) {
			return fmt.Errorf("worker-capacity bootstrap row %s differs from configuration", worker.WorkerIdentity)
		}
		var host, observer, keyDigest, registrationDigest, workerCapacityDigest, provenance string
		var registrationRevision int64
		var registration, key []byte
		var registered time.Time
		if err := query.QueryRow(ctx, `SELECT btrim(host_identity),btrim(observer_identity),btrim(observer_key_sha256),btrim(registration_sha256),registration_revision,btrim(worker_capacity_sha256),btrim(bootstrap_provenance_sha256),canonical_registration,canonical_key,registered_at FROM abcp_v4.abcp_worker_observation_key_registration_v1 WHERE worker_identity=$1`, worker.WorkerIdentity).Scan(&host, &observer, &keyDigest, &registrationDigest, &registrationRevision, &workerCapacityDigest, &provenance, &registration, &key, &registered); err != nil {
			return fmt.Errorf("worker-registration bootstrap row %s: %w", worker.WorkerIdentity, err)
		}
		if host != worker.HostIdentity || observer != worker.ObserverIdentity || keyDigest != worker.ObserverKeySHA256 || registrationDigest != worker.RegistrationSHA256 || registrationRevision != int64(worker.RegistrationRevision) || workerCapacityDigest != worker.WorkerCapacitySHA256 || provenance != worker.BootstrapProvenanceSHA256 || !bytes.Equal(registration, worker.CanonicalRegistration) || !bytes.Equal(key, worker.CanonicalKey) || !registered.Equal(canonicalSecondV1(worker.RegisteredAt)) {
			return fmt.Errorf("worker-registration bootstrap row %s differs from configuration", worker.WorkerIdentity)
		}
	}
	return nil
}

// verifyWorkerBootstrapRowsAfterHardeningV1 uses the frozen worker-function
// SELECT grants and forced-RLS policies. PostgreSQL 17 gives a role creator
// ADMIN but SET=false membership; the external provisioner enables SET only
// inside this always-rolled-back transaction, reads every configured worker
// field through abcp_v4_worker_fn, and leaves no post-H membership change.
func verifyWorkerBootstrapRowsAfterHardeningV1(ctx context.Context, conn *pgx.Conn, config BootstrapConfigV1) error {
	if len(config.Workers) == 0 {
		return nil
	}
	var sessionUser, currentUser string
	var createRole, superuser, bypassRLS bool
	if err := conn.QueryRow(ctx, `SELECT session_user::text,current_user::text,rolcreaterole,rolsuper,rolbypassrls FROM pg_catalog.pg_roles WHERE rolname=session_user`).Scan(&sessionUser, &currentUser, &createRole, &superuser, &bypassRLS); err != nil {
		return err
	}
	if sessionUser != currentUser || isStaticRoleV1(sessionUser) || !createRole || superuser || bypassRLS {
		return errors.New("post-H worker verifier is not the external bootstrap provisioner")
	}
	provisionerID, err := quoteIdentifierV1(sessionUser)
	if err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: pgx.ReadWrite})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `GRANT abcp_v4_worker_fn TO `+provisionerID+` WITH SET TRUE`); err != nil {
		return fmt.Errorf("enable rolled-back worker verifier SET authority: %w", err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE abcp_v4_worker_fn`); err != nil {
		return fmt.Errorf("enter worker verifier role: %w", err)
	}
	if err := verifyWorkerBootstrapRowsV1(ctx, tx, config.Workers); err != nil {
		return err
	}
	return tx.Rollback(ctx)
}

// verifyCommittedBootstrapRowsV1 uses the temporary owner SET authority from
// phase P inside an always-rolled-back transaction. FORCE RLS correctly
// prevents that NOLOGIN table owner from reading rows in normal operation;
// temporarily clearing FORCE inside this private reconciliation transaction
// is therefore necessary to compare configured rows, and rollback restores
// the byte-exact frozen catalog before phase H can begin.
func verifyCommittedBootstrapRowsV1(ctx context.Context, conn *pgx.Conn, config BootstrapConfigV1) error {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: pgx.ReadWrite})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE abcp_v4_schema_owner`); err != nil {
		return err
	}
	for _, table := range []string{
		"abcp_authority_domain_v1", "abcp_workflow_authority_v1", "abcp_predecessor_directory_v1",
		"abcp_worker_capacity_v1", "abcp_worker_observation_key_registration_v1", "abcp_artifact_blob_v1", "abcp_stage_seal_v1",
	} {
		if _, err := tx.Exec(ctx, `ALTER TABLE abcp_v4.`+table+` NO FORCE ROW LEVEL SECURITY`); err != nil {
			return err
		}
	}
	return verifyBootstrapRowsV1(ctx, tx, config, "")
}

func verifyBootstrapRowsThroughRuntimeV1(ctx context.Context, config BootstrapConfigV1) error {
	for _, domain := range config.Domains {
		connection, err := runtimeBootstrapConnectionV1(ctx, config.ProvisionerConnectionString, domain)
		if err != nil {
			return err
		}
		if err := verifyRuntimeSessionV1(ctx, connection, domain.AuthorityDomain); err != nil {
			connection.Close(context.Background())
			return err
		}
		if err := verifyBootstrapRowsV1(ctx, connection, config, domain.AuthorityDomain); err != nil {
			connection.Close(context.Background())
			return err
		}
		connection.Close(context.Background())
	}
	return nil
}

func runtimeBootstrapConnectionV1(ctx context.Context, source string, domain AuthorityDomainBootstrapV1) (*pgx.Conn, error) {
	config, err := pgx.ParseConfig(source)
	if err != nil {
		return nil, err
	}
	config.User = domain.RuntimeRole
	config.Password = domain.RuntimePassword
	if config.RuntimeParams == nil {
		config.RuntimeParams = make(map[string]string)
	}
	config.RuntimeParams["search_path"] = "pg_catalog,abcp_v4"
	config.RuntimeParams["row_security"] = "on"
	config.RuntimeParams["statement_timeout"] = "10s"
	config.RuntimeParams["lock_timeout"] = "2s"
	config.RuntimeParams["idle_in_transaction_session_timeout"] = "15s"
	return pgx.ConnectConfig(ctx, config)
}

func createOrVerifyRoleV1(ctx context.Context, tx pgx.Tx, role string, login, createRole bool, password string) error {
	identifier, err := quoteIdentifierV1(role)
	if err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE rolname=$1)`, role).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		loginClause := "NOLOGIN"
		if login {
			loginClause = "LOGIN"
		}
		createRoleClause := "NOCREATEROLE"
		if createRole {
			createRoleClause = "CREATEROLE"
		}
		statement := `CREATE ROLE ` + identifier + ` ` + loginClause + ` NOSUPERUSER NOCREATEDB ` + createRoleClause + ` NOREPLICATION NOBYPASSRLS`
		if password != "" {
			statement += ` PASSWORD ` + quoteLiteralV1(password)
		}
		if _, err := tx.Exec(ctx, statement); err != nil {
			return err
		}
	} else if password != "" {
		if _, err := tx.Exec(ctx, `ALTER ROLE `+identifier+` PASSWORD `+quoteLiteralV1(password)); err != nil {
			return err
		}
	}
	var actualLogin, superuser, actualCreateDB, actualCreateRole, replication, bypassRLS bool
	if err := tx.QueryRow(ctx, `SELECT rolcanlogin,rolsuper,rolcreatedb,rolcreaterole,rolreplication,rolbypassrls FROM pg_catalog.pg_roles WHERE rolname=$1`, role).Scan(&actualLogin, &superuser, &actualCreateDB, &actualCreateRole, &replication, &bypassRLS); err != nil {
		return err
	}
	if actualLogin != login || actualCreateRole != createRole || superuser || actualCreateDB || replication || bypassRLS {
		return fmt.Errorf("role %s has attributes outside the frozen bootstrap topology", role)
	}
	return nil
}

func verifyPhasePMembershipsV1(ctx context.Context, query catalogQuerierV1, config BootstrapConfigV1) error {
	if err := verifyProvisionedDomainRolesV1(ctx, query, config); err != nil {
		return err
	}
	return verifyMigratorSetAuthorityV1(ctx, query)
}

func verifyProvisionedDomainRolesV1(ctx context.Context, query catalogQuerierV1, config BootstrapConfigV1) error {
	for _, domain := range config.Domains {
		if err := verifyLoginRoleAttributesV1(ctx, query, domain.RuntimeRole); err != nil {
			return err
		}
		if err := verifyLoginRoleAttributesV1(ctx, query, domain.RecoveryVerifierRole); err != nil {
			return err
		}
		groups, err := directMembershipsV1(ctx, query, domain.RuntimeRole)
		if err != nil || len(groups) != 1 || groups[0] != RuntimeGroupRoleV1 {
			return fmt.Errorf("runtime role %s has incorrect membership", domain.RuntimeRole)
		}
		groups, err = directMembershipsV1(ctx, query, domain.RecoveryVerifierRole)
		if err != nil || len(groups) != 1 || groups[0] != RecoveryVerifierGroupRoleV1 {
			return fmt.Errorf("recovery role %s has incorrect membership", domain.RecoveryVerifierRole)
		}
	}
	return nil
}

func verifyProvisionedRoleTopologyV1(ctx context.Context, query catalogQuerierV1, config BootstrapConfigV1) error {
	if err := verifyProvisionedDomainRolesV1(ctx, query, config); err != nil {
		return err
	}
	for _, role := range []string{SchemaOwnerRoleV1, RuntimeGroupRoleV1, RecoveryVerifierGroupRoleV1, DomainFunctionOwnerRoleV1, WorkerFunctionOwnerRoleV1} {
		var login, superuser, createDB, createRole, replication, bypassRLS bool
		if err := query.QueryRow(ctx, `SELECT rolcanlogin,rolsuper,rolcreatedb,rolcreaterole,rolreplication,rolbypassrls FROM pg_catalog.pg_roles WHERE rolname=$1`, role).Scan(&login, &superuser, &createDB, &createRole, &replication, &bypassRLS); err != nil {
			return fmt.Errorf("verify static role %s: %w", role, err)
		}
		if login || superuser || createDB || createRole || replication || bypassRLS {
			return fmt.Errorf("static role %s has attributes outside the frozen topology", role)
		}
	}
	var login, superuser, createDB, createRole, replication, bypassRLS bool
	if err := query.QueryRow(ctx, `SELECT rolcanlogin,rolsuper,rolcreatedb,rolcreaterole,rolreplication,rolbypassrls FROM pg_catalog.pg_roles WHERE rolname=$1`, MigratorRoleV1).Scan(&login, &superuser, &createDB, &createRole, &replication, &bypassRLS); err != nil {
		return fmt.Errorf("verify migrator replay state: %w", err)
	}
	if login != createRole || superuser || createDB || replication || bypassRLS {
		return errors.New("migrator is neither exact phase-M authority nor exact phase-H hardened state")
	}
	return nil
}

func verifyLoginRoleAttributesV1(ctx context.Context, query catalogQuerierV1, role string) error {
	var login, superuser, createDB, createRole, replication, bypassRLS bool
	if err := query.QueryRow(ctx, `SELECT rolcanlogin,rolsuper,rolcreatedb,rolcreaterole,rolreplication,rolbypassrls FROM pg_catalog.pg_roles WHERE rolname=$1`, role).Scan(&login, &superuser, &createDB, &createRole, &replication, &bypassRLS); err != nil {
		return fmt.Errorf("verify login role %s: %w", role, err)
	}
	if !login || superuser || createDB || createRole || replication || bypassRLS {
		return fmt.Errorf("login role %s has attributes outside the frozen topology", role)
	}
	return nil
}

func verifyMigratorSetAuthorityV1(ctx context.Context, query catalogQuerierV1) error {
	rows, err := query.Query(ctx, `
		SELECT parent.rolname,m.admin_option,m.set_option
		FROM pg_catalog.pg_auth_members m
		JOIN pg_catalog.pg_roles parent ON parent.oid=m.roleid
		JOIN pg_catalog.pg_roles child ON child.oid=m.member
		WHERE child.rolname=$1 AND parent.rolname LIKE 'abcp_v4_%'
		ORDER BY parent.rolname`, MigratorRoleV1)
	if err != nil {
		return err
	}
	defer rows.Close()
	expected := []string{DomainFunctionOwnerRoleV1, SchemaOwnerRoleV1, WorkerFunctionOwnerRoleV1}
	var actual []string
	for rows.Next() {
		var role string
		var admin, set bool
		if err := rows.Scan(&role, &admin, &set); err != nil {
			return err
		}
		if !admin || !set {
			return fmt.Errorf("migrator membership %s lacks ADMIN and SET authority", role)
		}
		actual = append(actual, role)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !equalStringsV1(actual, expected) {
		return fmt.Errorf("migrator memberships are %v, want %v", actual, expected)
	}
	return nil
}

func connectBootstrapV1(ctx context.Context, connectionString string) (*pgx.Conn, error) {
	config, err := pgx.ParseConfig(connectionString)
	if err != nil {
		return nil, err
	}
	if err := validateTransportV1(config.Host, config.TLSConfig); err != nil {
		return nil, err
	}
	if config.ConnectTimeout == 0 || config.ConnectTimeout > connectTimeoutV1 {
		config.ConnectTimeout = connectTimeoutV1
	}
	if config.RuntimeParams == nil {
		config.RuntimeParams = make(map[string]string)
	}
	config.RuntimeParams["search_path"] = "pg_catalog"
	config.RuntimeParams["row_security"] = "on"
	config.RuntimeParams["statement_timeout"] = "10s"
	config.RuntimeParams["lock_timeout"] = "2s"
	config.RuntimeParams["idle_in_transaction_session_timeout"] = "15s"
	return pgx.ConnectConfig(ctx, config)
}

func validateBootstrapConfigV1(config BootstrapConfigV1) error {
	if strings.TrimSpace(config.ProvisionerConnectionString) == "" || strings.TrimSpace(config.MigratorConnectionString) == "" || config.MigratorPassword == "" || validatePostgresIdentifierV1(config.DatabaseName) != nil || len(config.Domains) == 0 {
		return errors.New("bootstrap connections, database, and at least one authority domain are required")
	}
	domains := make(map[string]struct{}, len(config.Domains))
	roles := make(map[string]struct{}, len(config.Domains)*2)
	for _, domain := range config.Domains {
		if err := validateAuthorityDomainV1(domain.AuthorityDomain); err != nil {
			return err
		}
		if _, duplicate := domains[domain.AuthorityDomain]; duplicate {
			return errors.New("bootstrap authority domains must be unique")
		}
		domains[domain.AuthorityDomain] = struct{}{}
		if validatePostgresIdentifierV1(domain.RuntimeRole) != nil || validatePostgresIdentifierV1(domain.RecoveryVerifierRole) != nil || domain.RuntimeRole == domain.RecoveryVerifierRole || isStaticRoleV1(domain.RuntimeRole) || isStaticRoleV1(domain.RecoveryVerifierRole) || domain.RuntimePassword == "" || domain.RecoveryVerifierPassword == "" || validateDBIdentityV1(domain.BootstrapProvenanceSHA256) != nil || len(domain.Workflows) == 0 {
			return fmt.Errorf("authority domain %s bootstrap role, provenance, or workflows are invalid", domain.AuthorityDomain)
		}
		for _, role := range []string{domain.RuntimeRole, domain.RecoveryVerifierRole} {
			if _, duplicate := roles[role]; duplicate {
				return errors.New("per-domain PostgreSQL login roles must be globally unique")
			}
			roles[role] = struct{}{}
		}
		controllers := map[string]struct{}{}
		for _, workflow := range domain.Workflows {
			if validateDBIdentityV1(workflow.ControllerIdentity) != nil || workflow.Revision == 0 || workflow.UpdatedAt.IsZero() || validateCanonicalJSONV1(workflow.CanonicalState, MaxCanonicalRequestBytesV1) != nil {
				return fmt.Errorf("authority domain %s has an invalid workflow bootstrap row", domain.AuthorityDomain)
			}
			var identity struct {
				ControllerIdentity string `json:"controller_identity"`
				Revision           uint64 `json:"revision"`
			}
			if json.Unmarshal(workflow.CanonicalState, &identity) != nil || identity.ControllerIdentity != workflow.ControllerIdentity || identity.Revision != workflow.Revision {
				return errors.New("workflow bootstrap bytes disagree with row identity/revision")
			}
			if _, duplicate := controllers[workflow.ControllerIdentity]; duplicate {
				return errors.New("workflow bootstrap controller identity is duplicated")
			}
			controllers[workflow.ControllerIdentity] = struct{}{}
		}
		for _, directory := range domain.PredecessorDirectories {
			if validateDBIdentityV1(directory.ControllerIdentity) != nil || directory.WriterEpoch == 0 || directory.Revision == 0 || (directory.WriterState != "OPEN" && directory.WriterState != "TOMBSTONED") || validateCanonicalJSONV1(directory.CanonicalDirectory, MaxCanonicalRequestBytesV1) != nil {
				return errors.New("predecessor-directory bootstrap row is invalid")
			}
			parsed, err := governance.ParsePredecessorAuthorityDirectoryV1(directory.CanonicalDirectory)
			if err != nil || parsed.AuthorityDomain != domain.AuthorityDomain || parsed.ControllerIdentity != directory.ControllerIdentity || parsed.WriterEpoch != directory.WriterEpoch || string(parsed.WriterState) != directory.WriterState || parsed.DirectoryRevision != directory.Revision || parsed.DirectorySHA256 != directory.DirectorySHA256 {
				return errors.New("predecessor-directory bootstrap bytes disagree with row identity/revision")
			}
		}
	}
	for _, worker := range config.Workers {
		if err := validateWorkerBootstrapV1(worker); err != nil {
			return err
		}
	}
	for _, artifact := range config.Artifacts {
		if _, ok := domains[artifact.AuthorityDomain]; !ok || len(artifact.CanonicalBytes) == 0 || len(artifact.CanonicalBytes) > MaxArtifactBytesV1 || artifact.CreatedAt.IsZero() {
			return errors.New("artifact bootstrap row is invalid")
		}
	}
	for _, stage := range config.Stages {
		if _, ok := domains[stage.AuthorityDomain]; !ok || validateDBIdentityV1(stage.LineageSHA256) != nil || validateDBIdentityV1(stage.SubjectSHA256) != nil || strings.TrimSpace(stage.Stage) == "" || (stage.SealState != "OPEN" && stage.SealState != "CLOSED") || stage.NextOccurrenceOrdinal == 0 || stage.Revision == 0 {
			return errors.New("stage bootstrap row is invalid")
		}
	}
	return nil
}

func validateWorkerBootstrapV1(worker WorkerBootstrapV1) error {
	for _, identity := range []string{worker.WorkerIdentity, worker.HostIdentity, worker.ObserverIdentity, worker.ObserverKeySHA256, worker.RegistrationSHA256, worker.WorkerCapacitySHA256, worker.BootstrapProvenanceSHA256, worker.CapacitySHA256} {
		if validateDBIdentityV1(identity) != nil {
			return errors.New("worker bootstrap identity or digest is invalid")
		}
	}
	if worker.CapacityRevision == 0 || worker.RegistrationRevision != 1 || worker.RegisteredAt.IsZero() || worker.WorkerCapacitySHA256 != worker.CapacitySHA256 || validateCanonicalJSONV1(worker.CanonicalCapacity, MaxCanonicalRequestBytesV1) != nil || validateCanonicalJSONV1(worker.CanonicalKey, MaxCanonicalRequestBytesV1) != nil || validateCanonicalJSONV1(worker.CanonicalRegistration, MaxCanonicalRequestBytesV1) != nil || terminalSelfDigestV1(worker.CanonicalCapacity, "capacity_sha256", worker.CapacitySHA256) != nil || terminalSelfDigestV1(worker.CanonicalKey, "key_sha256", worker.ObserverKeySHA256) != nil || terminalSelfDigestV1(worker.CanonicalRegistration, "registration_sha256", worker.RegistrationSHA256) != nil {
		return errors.New("worker bootstrap canonical records are invalid")
	}
	var key struct {
		WorkerIdentity       string `json:"worker_identity"`
		HostIdentity         string `json:"host_identity"`
		ObserverIdentity     string `json:"observer_identity"`
		KeyID                string `json:"key_id"`
		Algorithm            string `json:"algorithm"`
		PublicKeyHex         string `json:"public_key_hex"`
		RegistrationRevision uint64 `json:"registration_revision"`
	}
	var registration struct {
		WorkerIdentity            string `json:"worker_identity"`
		HostIdentity              string `json:"host_identity"`
		ObserverIdentity          string `json:"observer_identity"`
		ObserverKeySHA256         string `json:"observer_key_sha256"`
		WorkerCapacitySHA256      string `json:"worker_capacity_sha256"`
		BootstrapProvenanceSHA256 string `json:"bootstrap_provenance_sha256"`
		RegistrationRevision      uint64 `json:"registration_revision"`
	}
	if json.Unmarshal(worker.CanonicalKey, &key) != nil || json.Unmarshal(worker.CanonicalRegistration, &registration) != nil || key.WorkerIdentity != worker.WorkerIdentity || key.HostIdentity != worker.HostIdentity || key.ObserverIdentity != worker.ObserverIdentity || key.Algorithm != "ED25519" || key.RegistrationRevision != 1 || registration.WorkerIdentity != worker.WorkerIdentity || registration.HostIdentity != worker.HostIdentity || registration.ObserverIdentity != worker.ObserverIdentity || registration.ObserverKeySHA256 != worker.ObserverKeySHA256 || registration.WorkerCapacitySHA256 != worker.CapacitySHA256 || registration.BootstrapProvenanceSHA256 != worker.BootstrapProvenanceSHA256 || registration.RegistrationRevision != 1 {
		return errors.New("worker key/registration bindings are invalid")
	}
	publicKey, err := hex.DecodeString(key.PublicKeyHex)
	if err != nil || len(publicKey) != 32 || validateDBIdentityV1(key.KeyID) != nil {
		return errors.New("worker observation public key or key ID is invalid")
	}
	digest := sha256.New()
	digest.Write([]byte("ABCP-WORKER-OBSERVER-V1"))
	digest.Write([]byte{0})
	digest.Write([]byte(worker.WorkerIdentity))
	digest.Write([]byte{0})
	digest.Write([]byte(worker.HostIdentity))
	digest.Write([]byte{0})
	digest.Write([]byte(key.KeyID))
	if hex.EncodeToString(digest.Sum(nil)) != worker.ObserverIdentity {
		return errors.New("worker observer identity derivation disagrees")
	}
	return nil
}

func terminalSelfDigestV1(data []byte, field, digest string) error {
	suffix := `,"` + field + `":"` + digest + `"}`
	if !strings.HasSuffix(string(data), suffix) {
		return errors.New("terminal self digest is absent or not last")
	}
	preimage := append([]byte(nil), data[:len(data)-len(suffix)]...)
	preimage = append(preimage, '}')
	if sha256HexV1(preimage) != digest {
		return errors.New("terminal self digest disagrees")
	}
	return nil
}

func sortedDomainNamesV1(domains []AuthorityDomainBootstrapV1) []string {
	result := make([]string, len(domains))
	for index := range domains {
		result[index] = domains[index].AuthorityDomain
	}
	sort.Strings(result)
	return result
}

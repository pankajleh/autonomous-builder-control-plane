package postgres

import (
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
)

type BootstrapConfigV1 struct {
	ProvisionerConnectionString string
	MigratorConnectionString    string
	MigratorPassword            string
	DatabaseName                string
	Domains                     []AuthorityDomainBootstrapV1
	Workers                     []WorkerBootstrapV1
	Artifacts                   []ArtifactBootstrapV1
	Stages                      []StageBootstrapV1
}

type AuthorityDomainBootstrapV1 struct {
	AuthorityDomain           string
	RuntimeRole               string
	RuntimePassword           string
	RecoveryVerifierRole      string
	RecoveryVerifierPassword  string
	BootstrapProvenanceSHA256 string
	Workflows                 []WorkflowBootstrapV1
	PredecessorDirectories    []PredecessorDirectoryBootstrapV1
}

type WorkflowBootstrapV1 struct {
	ControllerIdentity string
	Revision           uint64
	CanonicalState     []byte
	UpdatedAt          time.Time
}

type PredecessorDirectoryBootstrapV1 struct {
	ControllerIdentity string
	WriterEpoch        uint64
	WriterState        string
	CanonicalDirectory []byte
	DirectorySHA256    string
	Revision           uint64
}

type WorkerBootstrapV1 struct {
	WorkerIdentity            string
	CanonicalCapacity         []byte
	CapacitySHA256            string
	CapacityRevision          uint64
	CanonicalKey              []byte
	CanonicalRegistration     []byte
	HostIdentity              string
	ObserverIdentity          string
	ObserverKeySHA256         string
	RegistrationSHA256        string
	RegistrationRevision      uint64
	WorkerCapacitySHA256      string
	BootstrapProvenanceSHA256 string
	RegisteredAt              time.Time
}

type ArtifactBootstrapV1 struct {
	AuthorityDomain string
	CanonicalBytes  []byte
	CreatedAt       time.Time
}

type StageBootstrapV1 struct {
	AuthorityDomain       string
	LineageSHA256         string
	Stage                 string
	SubjectSHA256         string
	SealState             string
	NextOccurrenceOrdinal uint64
	Revision              uint64
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
	} else if err := verifyProvisionedDomainRolesV1(phaseContext, provisioner, config); err != nil {
		provisioner.Close(context.Background())
		return fmt.Errorf("reconcile provisioned domain roles: %w", err)
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
	if err := VerifyFrozenCatalogV1(phaseContext, verifier); err != nil {
		return fmt.Errorf("fresh frozen-catalog verification: %w", err)
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
			if validateDBIdentityV1(directory.ControllerIdentity) != nil || directory.WriterEpoch == 0 || directory.Revision == 0 || (directory.WriterState != "OPEN" && directory.WriterState != "TOMBSTONED") || validateCanonicalJSONV1(directory.CanonicalDirectory, MaxCanonicalRequestBytesV1) != nil || requireDigestV1(directory.CanonicalDirectory, directory.DirectorySHA256) != nil {
				return errors.New("predecessor-directory bootstrap row is invalid")
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

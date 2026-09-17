package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/authoritybackend"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/governance"
)

const postgresImageV1 = "postgres@sha256:f3bd19c606e442c3d7bdfa8002e03fe260a1023351e0ea4598032022b68dd6e3"

type postgresHarnessV1 struct {
	hostPort            string
	adminPassword       string
	provisionerPassword string
	migratorPassword    string
	runtimePasswordA    string
	recoveryPasswordA   string
	runtimePasswordB    string
	recoveryPasswordB   string
	container           string
}

func testPostgres17BootstrapAndHardening(t *testing.T) {
	harness := newPostgresHarnessV1(t)
	config := harness.bootstrapConfigV1(false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	provisioner, err := connectBootstrapV1(ctx, config.ProvisionerConnectionString)
	if err != nil {
		t.Fatal(err)
	}
	if err := provisionV1(ctx, provisioner, config); err != nil {
		provisioner.Close(context.Background())
		t.Fatalf("phase P: %v", err)
	}
	var adminOption, inheritOption, setOption bool
	if _, err := provisioner.Exec(ctx, `CREATE ROLE abcp_creator_membership_vector NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`); err != nil {
		provisioner.Close(context.Background())
		t.Fatalf("create creator-membership vector: %v", err)
	}
	if err := provisioner.QueryRow(ctx, `
		SELECT m.admin_option,m.inherit_option,m.set_option
		FROM pg_catalog.pg_auth_members m
		JOIN pg_catalog.pg_roles parent ON parent.oid=m.roleid
		JOIN pg_catalog.pg_roles child ON child.oid=m.member
		WHERE parent.rolname='abcp_creator_membership_vector' AND child.rolname=session_user`).Scan(&adminOption, &inheritOption, &setOption); err != nil {
		provisioner.Close(context.Background())
		t.Fatalf("read PostgreSQL-17 creator membership: %v", err)
	}
	if !adminOption || inheritOption || setOption {
		provisioner.Close(context.Background())
		t.Fatalf("creator membership is ADMIN=%v INHERIT=%v SET=%v", adminOption, inheritOption, setOption)
	}
	_, err = provisioner.Exec(ctx, `GRANT abcp_creator_membership_vector TO abcp_bootstrap_provisioner WITH ADMIN OPTION`)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "0LP01" {
		provisioner.Close(context.Background())
		t.Fatalf("creator self-ADMIN vector returned %v, want SQLSTATE 0LP01", err)
	}
	provisioner.Close(context.Background())

	migrator, err := connectBootstrapV1(ctx, config.MigratorConnectionString)
	if err != nil {
		t.Fatal(err)
	}
	_, err = migrator.Exec(ctx, `ALTER ROLE abcp_v4_migrator NOLOGIN NOCREATEROLE`)
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		migrator.Close(context.Background())
		t.Fatalf("migrator self-hardening returned %v, want SQLSTATE 42501", err)
	}
	if err := migrateV1(ctx, migrator, config); err != nil {
		migrator.Close(context.Background())
		t.Fatalf("phase M: %v", err)
	}
	migrator.Close(context.Background())

	hardener, err := connectBootstrapV1(ctx, config.ProvisionerConnectionString)
	if err != nil {
		t.Fatal(err)
	}
	if err := hardenV1(ctx, hardener, config.DatabaseName); err != nil {
		hardener.Close(context.Background())
		t.Fatalf("phase H: %v", err)
	}
	hardener.Close(context.Background())
	verifier, err := connectBootstrapV1(ctx, config.ProvisionerConnectionString)
	if err != nil {
		t.Fatal(err)
	}
	defer verifier.Close(context.Background())
	if err := VerifyFinalHardeningV1(ctx, verifier); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFrozenCatalogV1(ctx, verifier); err != nil {
		t.Fatal(err)
	}
}

func testPostgresWorkflowAuthorityBackendCAS(t *testing.T) {
	harness := newPostgresHarnessV1(t)
	config := harness.bootstrapConfigV1(false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := BootstrapV1(ctx, config); err != nil {
		t.Fatal(err)
	}
	backend, err := OpenPostgresWorkflowAuthorityBackendV1(ctx, ConfigV1{ConnectionString: harness.dsnV1("abcp_runtime_a", harness.runtimePasswordA, "abcp_test"), AuthorityDomain: strings.Repeat("3", 64)})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	controller := strings.Repeat("4", 64)
	loaded, revision, err := backend.LoadWorkflowStateV1(controller)
	if err != nil || revision != 1 || string(loaded) != workflowStateV1(controller, 1, "") {
		t.Fatalf("initial load: revision=%d state=%s err=%v", revision, loaded, err)
	}
	next := []byte(workflowStateV1(controller, 2, "winner"))
	var wait sync.WaitGroup
	results := make(chan bool, 32)
	errorsCh := make(chan error, 32)
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			swapped, err := backend.CompareAndSwapWorkflowStateV1(controller, 1, next)
			if err != nil {
				errorsCh <- err
				return
			}
			results <- swapped
		}()
	}
	wait.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		t.Errorf("CAS error: %v", err)
	}
	winners := 0
	for swapped := range results {
		if swapped {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("CAS winners=%d, want 1", winners)
	}
	backend.Close()
	fresh, err := OpenPostgresWorkflowAuthorityBackendV1(ctx, ConfigV1{ConnectionString: harness.dsnV1("abcp_runtime_a", harness.runtimePasswordA, "abcp_test"), AuthorityDomain: strings.Repeat("3", 64)})
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	loaded, revision, err = fresh.LoadWorkflowStateV1(controller)
	if err != nil || revision != 2 || string(loaded) != string(next) {
		t.Fatalf("fresh load: revision=%d state=%s err=%v", revision, loaded, err)
	}
}

func testPostgresDomainIsolation(t *testing.T) {
	harness := newPostgresHarnessV1(t)
	config := harness.bootstrapConfigV1(true)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := BootstrapV1(ctx, config); err != nil {
		t.Fatal(err)
	}
	domainA, domainB := strings.Repeat("3", 64), strings.Repeat("b", 64)
	controllerA, controllerB := strings.Repeat("4", 64), strings.Repeat("c", 64)
	backendA, err := OpenPostgresWorkflowAuthorityBackendV1(ctx, ConfigV1{ConnectionString: harness.dsnV1("abcp_runtime_a", harness.runtimePasswordA, "abcp_test"), AuthorityDomain: domainA})
	if err != nil {
		t.Fatal(err)
	}
	defer backendA.Close()
	backendB, err := OpenPostgresWorkflowAuthorityBackendV1(ctx, ConfigV1{ConnectionString: harness.dsnV1("abcp_runtime_b", harness.runtimePasswordB, "abcp_test"), AuthorityDomain: domainB})
	if err != nil {
		t.Fatal(err)
	}
	defer backendB.Close()
	if _, _, err := backendA.LoadWorkflowStateV1(controllerB); err == nil {
		t.Fatal("domain A loaded domain B workflow")
	}
	if _, _, err := backendB.LoadWorkflowStateV1(controllerA); err == nil {
		t.Fatal("domain B loaded domain A workflow")
	}
	artifact := []byte(`{"kind":"ArtifactBlobV1","value":"same-content"}`)
	digestA, err := backendA.PutArtifactBlobV1(ctx, artifact)
	if err != nil {
		t.Fatal(err)
	}
	digestB, err := backendB.PutArtifactBlobV1(ctx, artifact)
	if err != nil || digestA != digestB {
		t.Fatalf("domain-local content address: %q %q %v", digestA, digestB, err)
	}
	if loaded, err := backendA.LoadArtifactBlobV1(ctx, digestA); err != nil || string(loaded) != string(artifact) {
		t.Fatalf("domain A artifact load: %s %v", loaded, err)
	}
	if _, err := backendA.pool.Exec(ctx, `UPDATE abcp_v4.abcp_authority_domain_v1 SET bootstrap_provenance_sha256=$1 WHERE authority_domain=$2`, strings.Repeat("d", 64), domainA); err == nil {
		t.Fatal("ordinary runtime directly updated authority-domain binding")
	}
	if _, err := backendA.pool.Exec(ctx, `SELECT * FROM abcp_v4.abcp_worker_observation_key_registration_v1`); err == nil {
		t.Fatal("ordinary runtime directly read observation-key registration")
	}
}

func testPostgresBootstrapPOnlyAndHReplay(t *testing.T) {
	harness := newPostgresHarnessV1(t)
	config := harness.bootstrapConfigV1(false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	provisioner, err := connectBootstrapV1(ctx, config.ProvisionerConnectionString)
	if err != nil {
		t.Fatal(err)
	}
	if err := provisionV1(ctx, provisioner, config); err != nil {
		provisioner.Close(context.Background())
		t.Fatal(err)
	}
	provisioner.Close(context.Background())
	if err := BootstrapV1(ctx, config); err != nil {
		t.Fatalf("P-only replay: %v", err)
	}
	if err := BootstrapV1(ctx, config); err != nil {
		t.Fatalf("already-hardened replay: %v", err)
	}
}

func testPostgresBootstrapCommittedMReplay(t *testing.T) {
	harness := newPostgresHarnessV1(t)
	config := harness.bootstrapConfigV1(false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	provisioner, err := connectBootstrapV1(ctx, config.ProvisionerConnectionString)
	if err != nil {
		t.Fatal(err)
	}
	if err := provisionV1(ctx, provisioner, config); err != nil {
		provisioner.Close(context.Background())
		t.Fatal(err)
	}
	provisioner.Close(context.Background())
	migrator, err := connectBootstrapV1(ctx, config.MigratorConnectionString)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateV1(ctx, migrator, config); err != nil {
		migrator.Close(context.Background())
		t.Fatal(err)
	}
	migrator.Close(context.Background())
	domain := config.Domains[0]
	workflow := domain.Workflows[0]
	admin, err := pgx.Connect(ctx, harness.dsnV1("postgres", harness.adminPassword, "abcp_test"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE abcp_v4.abcp_workflow_authority_v1 SET canonical_state=$3,state_sha256=$4 WHERE authority_domain=$1 AND controller_identity=$2`, domain.AuthorityDomain, workflow.ControllerIdentity, []byte(`{"controller_identity":"`+workflow.ControllerIdentity+`","revision":1,"drift":true}`), strings.Repeat("e", 64)); err != nil {
		admin.Close(context.Background())
		t.Fatal(err)
	}
	admin.Close(context.Background())
	if err := BootstrapV1(ctx, config); err == nil || !strings.Contains(err.Error(), "bootstrap rows") {
		t.Fatalf("committed-M drift was not rejected: %v", err)
	}
	admin, err = pgx.Connect(ctx, harness.dsnV1("postgres", harness.adminPassword, "abcp_test"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE abcp_v4.abcp_workflow_authority_v1 SET canonical_state=$3,state_sha256=$4 WHERE authority_domain=$1 AND controller_identity=$2`, domain.AuthorityDomain, workflow.ControllerIdentity, workflow.CanonicalState, sha256HexV1(workflow.CanonicalState)); err != nil {
		admin.Close(context.Background())
		t.Fatal(err)
	}
	admin.Close(context.Background())
	if err := BootstrapV1(ctx, config); err != nil {
		t.Fatalf("committed-M exact replay: %v", err)
	}
}

func testPostgresHardenedWorkerReplay(t *testing.T) {
	harness := newPostgresHarnessV1(t)
	config := harness.bootstrapConfigV1(false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := BootstrapV1(ctx, config); err != nil {
		t.Fatal(err)
	}
	if err := BootstrapV1(ctx, config); err != nil {
		t.Fatalf("exact already-hardened worker replay: %v", err)
	}
	admin, err := pgx.Connect(ctx, harness.dsnV1("postgres", harness.adminPassword, "abcp_test"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	worker := config.Workers[0]
	type driftCase struct {
		name, table, column string
		drift, exact        any
	}
	cases := []driftCase{
		{"capacity-bytes", "abcp_worker_capacity_v1", "canonical_capacity", []byte(`{}`), worker.CanonicalCapacity},
		{"capacity-digest", "abcp_worker_capacity_v1", "capacity_sha256", strings.Repeat("a", 64), worker.CapacitySHA256},
		{"capacity-revision", "abcp_worker_capacity_v1", "revision", int64(2), int64(worker.CapacityRevision)},
		{"registration-host", "abcp_worker_observation_key_registration_v1", "host_identity", strings.Repeat("a", 64), worker.HostIdentity},
		{"registration-observer", "abcp_worker_observation_key_registration_v1", "observer_identity", strings.Repeat("b", 64), worker.ObserverIdentity},
		{"registration-key-digest", "abcp_worker_observation_key_registration_v1", "observer_key_sha256", strings.Repeat("c", 64), worker.ObserverKeySHA256},
		{"registration-digest", "abcp_worker_observation_key_registration_v1", "registration_sha256", strings.Repeat("d", 64), worker.RegistrationSHA256},
		{"registration-capacity-digest", "abcp_worker_observation_key_registration_v1", "worker_capacity_sha256", strings.Repeat("e", 64), worker.WorkerCapacitySHA256},
		{"registration-provenance", "abcp_worker_observation_key_registration_v1", "bootstrap_provenance_sha256", strings.Repeat("f", 64), worker.BootstrapProvenanceSHA256},
		{"registration-bytes", "abcp_worker_observation_key_registration_v1", "canonical_registration", []byte(`{}`), worker.CanonicalRegistration},
		{"registration-key-bytes", "abcp_worker_observation_key_registration_v1", "canonical_key", []byte(`{}`), worker.CanonicalKey},
		{"registration-timestamp", "abcp_worker_observation_key_registration_v1", "registered_at", worker.RegisteredAt.Add(time.Second), worker.RegisteredAt},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			statement := `UPDATE abcp_v4.` + test.table + ` SET ` + test.column + `=$2 WHERE worker_identity=$1`
			if _, err := admin.Exec(ctx, statement, worker.WorkerIdentity, test.drift); err != nil {
				t.Fatal(err)
			}
			if err := BootstrapV1(ctx, config); err == nil || !strings.Contains(err.Error(), "worker bootstrap-row verification") {
				t.Fatalf("hardened worker drift was not rejected: %v", err)
			}
			if _, err := admin.Exec(ctx, statement, worker.WorkerIdentity, test.exact); err != nil {
				t.Fatal(err)
			}
		})
	}
	if _, err := admin.Exec(ctx, `UPDATE abcp_v4.abcp_worker_observation_key_registration_v1 SET registration_revision=2 WHERE worker_identity=$1`, worker.WorkerIdentity); err == nil {
		t.Fatal("registration revision drift escaped the immutable schema constraint")
	}
	var setOption bool
	if err := admin.QueryRow(ctx, `
		SELECT m.set_option
		FROM pg_catalog.pg_auth_members m
		JOIN pg_catalog.pg_roles parent ON parent.oid=m.roleid
		JOIN pg_catalog.pg_roles child ON child.oid=m.member
		WHERE parent.rolname='abcp_v4_worker_fn' AND child.rolname='abcp_bootstrap_provisioner'`).Scan(&setOption); err != nil {
		t.Fatal(err)
	}
	if setOption {
		t.Fatal("post-H worker verification left provisioner SET authority behind")
	}
	if err := BootstrapV1(ctx, config); err != nil {
		t.Fatalf("exact worker replay after drift restoration: %v", err)
	}
}

func testPostgresDurablePredecessorFence(t *testing.T) {
	harness := newPostgresHarnessV1(t)
	config := harness.bootstrapConfigV1(false)
	domain := &config.Domains[0]
	controllerID := domain.Workflows[0].ControllerIdentity
	identity := governance.WorkflowBackendIdentityV1{
		AuthorityDomain: domain.AuthorityDomain, ControllerIdentity: controllerID,
		ImplementationID: "postgres-v1",
	}
	composition := sha256.Sum256([]byte("ABCP-POSTGRES-WORKFLOW-COMPOSITION-V1\x00" + domain.AuthorityDomain + "\x00" + controllerID))
	identity.CompositionSHA256 = hex.EncodeToString(composition[:])
	identityDigest, err := identity.SHA256()
	if err != nil {
		t.Fatal(err)
	}
	directory := postgresFenceDirectoryV1(t, domain.AuthorityDomain, controllerID, identityDigest)
	directoryBytes, _ := directory.CanonicalJSON()
	domain.PredecessorDirectories = []PredecessorDirectoryBootstrapV1{{
		ControllerIdentity: controllerID, WriterEpoch: directory.WriterEpoch, WriterState: string(directory.WriterState),
		CanonicalDirectory: directoryBytes, DirectorySHA256: directory.DirectorySHA256, Revision: directory.DirectoryRevision,
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := BootstrapV1(ctx, config); err != nil {
		t.Fatal(err)
	}
	backendA, err := OpenPostgresWorkflowAuthorityBackendV1(ctx, ConfigV1{ConnectionString: harness.dsnV1("abcp_runtime_a", harness.runtimePasswordA, "abcp_test"), AuthorityDomain: domain.AuthorityDomain})
	if err != nil {
		t.Fatal(err)
	}
	defer backendA.Close()
	backendB, err := OpenPostgresWorkflowAuthorityBackendV1(ctx, ConfigV1{ConnectionString: harness.dsnV1("abcp_runtime_a", harness.runtimePasswordA, "abcp_test"), AuthorityDomain: domain.AuthorityDomain})
	if err != nil {
		t.Fatal(err)
	}
	defer backendB.Close()
	probes := make(map[string]authoritybackend.PredecessorDrainProbeV1)
	for _, binding := range directory.Bindings {
		digest := binding.BarrierStateSHA256
		probes[binding.BindingID] = func(governance.PredecessorStoreBindingV1) (string, error) { return digest, nil }
	}
	clock := func() time.Time { return time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC) }
	fenceA, err := NewPostgresPredecessorDirectoryFenceV1(backendA, PredecessorFenceConfigV1{ControllerIdentity: controllerID, OwnerInstanceSHA256: strings.Repeat("e", 64), LeaseDuration: time.Minute, Now: clock, Probes: probes})
	if err != nil {
		t.Fatal(err)
	}
	fenceB, err := NewPostgresPredecessorDirectoryFenceV1(backendB, PredecessorFenceConfigV1{ControllerIdentity: controllerID, OwnerInstanceSHA256: strings.Repeat("f", 64), LeaseDuration: time.Minute, Now: clock, Probes: probes})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewProductionFencedWorkflowAuthorityBackendV1(backendA, fenceA, "workflow", identity); err != nil {
		t.Fatalf("exact PostgreSQL production workflow composition: %v", err)
	}
	if _, err := NewProductionFencedWorkflowAuthorityBackendV1(backendB, fenceA, "workflow", identity); err == nil {
		t.Fatal("production workflow accepted a fence owned by a different PostgreSQL backend instance")
	}
	if _, err := NewProductionFencedWorkflowAuthorityBackendV1(backendA, fenceB, "workflow", identity); err == nil {
		t.Fatal("production workflow accepted a mismatched durable PostgreSQL fence instance")
	}
	var wait sync.WaitGroup
	leases := make(chan authoritybackend.PredecessorWriterLeaseHandleV1, 16)
	errorsCh := make(chan error, 16)
	for index := 0; index < 16; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			fence := fenceA
			if index%2 != 0 {
				fence = fenceB
			}
			lease, err := fence.AcquirePredecessorWriterV1([]string{"ledger-run-1"}, "maintenance")
			if err != nil {
				errorsCh <- err
				return
			}
			leases <- lease
		}(index)
	}
	wait.Wait()
	close(leases)
	close(errorsCh)
	for err := range errorsCh {
		t.Errorf("cross-host open: %v", err)
	}
	var opened []authoritybackend.PredecessorWriterLeaseHandleV1
	for lease := range leases {
		opened = append(opened, lease)
	}
	if len(opened) != 16 {
		t.Fatalf("opened %d durable leases, want 16", len(opened))
	}
	loaded, err := fenceB.DirectoryV1()
	if err != nil || len(loaded.ActiveWriterLeases) != 16 {
		t.Fatalf("fresh host sees %d active leases: %v", len(loaded.ActiveWriterLeases), err)
	}
	for _, lease := range opened {
		if err := lease.Release(); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err = fenceA.DirectoryV1()
	if err != nil || len(loaded.ActiveWriterLeases) != 0 {
		t.Fatalf("durable releases left %d leases: %v", len(loaded.ActiveWriterLeases), err)
	}
}

func postgresFenceDirectoryV1(t *testing.T, domain, controller, workflowIdentity string) governance.PredecessorAuthorityDirectoryV1 {
	t.Helper()
	binding := func(id string, kind governance.PredecessorBindingKind, digit string) governance.PredecessorStoreBindingV1 {
		value := governance.PredecessorStoreBindingV1{BindingID: id, BindingKind: kind, HostIdentity: strings.Repeat(digit, 64), InitialScanArtifactSHA256: strings.Repeat("8", 64), BarrierStateSHA256: strings.Repeat(digit, 64), RegisteredEpoch: 1}
		switch kind {
		case governance.PredecessorBindingWorkflowState:
			value.WorkflowBackendIdentitySHA256 = workflowIdentity
		case governance.PredecessorBindingLedger:
			value.RunID = "run-1"
			value.CanonicalPathSHA256 = strings.Repeat("9", 64)
			value.PhysicalIdentitySHA256 = strings.Repeat("a", 64)
		default:
			value.CanonicalPathSHA256 = strings.Repeat("b", 64)
			value.PhysicalIdentitySHA256 = strings.Repeat("c", 64)
		}
		return value
	}
	directory, err := governance.SealPredecessorAuthorityDirectoryV1(governance.PredecessorAuthorityDirectoryV1{
		Kind: "PredecessorAuthorityDirectoryV1", SchemaVersion: governance.PredecessorAuthorityDirectorySchemaV1,
		AuthorityDomain: domain, ControllerIdentity: controller, RepositoryIdentity: "repository",
		WriterEpoch: 1, WriterState: governance.PredecessorWriterOpen,
		Bindings: []governance.PredecessorStoreBindingV1{
			binding("ledger-run-1", governance.PredecessorBindingLedger, "1"),
			binding("merge-root", governance.PredecessorBindingMergeStateStore, "2"),
			binding("pr-root", governance.PredecessorBindingPRAdmission, "3"),
			binding("workflow", governance.PredecessorBindingWorkflowState, "4"),
		}, ActiveWriterLeases: []string{}, DirectoryRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

func newPostgresHarnessV1(t *testing.T) *postgresHarnessV1 {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is required for PostgreSQL integration tests")
	}
	temporary := t.TempDir()
	if err := os.Chmod(temporary, 0o700); err != nil {
		t.Fatal(err)
	}
	harness := &postgresHarnessV1{
		adminPassword: randomSecretV1(t), provisionerPassword: randomSecretV1(t), migratorPassword: randomSecretV1(t),
		runtimePasswordA: randomSecretV1(t), recoveryPasswordA: randomSecretV1(t), runtimePasswordB: randomSecretV1(t), recoveryPasswordB: randomSecretV1(t),
		container: "abcp-pg-v4-" + randomHexV1(t, 8),
	}
	secretPath := filepath.Join(temporary, "postgres_password")
	if err := os.WriteFile(secretPath, []byte(harness.adminPassword), 0o400); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("docker", "run", "-d", "--platform", "linux/amd64", "--name", harness.container,
		"-p", "127.0.0.1::5432", "-e", "POSTGRES_PASSWORD_FILE=/run/secrets/postgres_password",
		"-v", secretPath+":/run/secrets/postgres_password:ro", postgresImageV1)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("start pinned PostgreSQL container: %v: %s", err, output)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", harness.container).Run()
	})
	portOutput, err := exec.Command("docker", "port", harness.container, "5432/tcp").Output()
	if err != nil {
		t.Fatal(err)
	}
	harness.hostPort = strings.TrimSpace(string(portOutput))
	deadline := time.Now().Add(45 * time.Second)
	var admin *pgx.Conn
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		admin, err = pgx.Connect(ctx, harness.dsnV1("postgres", harness.adminPassword, "postgres"))
		cancel()
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("wait for PostgreSQL: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	provisionerPassword := quoteLiteralV1(harness.provisionerPassword)
	if _, err := admin.Exec(ctx, `CREATE ROLE abcp_bootstrap_provisioner LOGIN NOSUPERUSER NOCREATEDB CREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD `+provisionerPassword); err != nil {
		admin.Close(context.Background())
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `CREATE DATABASE abcp_test OWNER postgres`); err != nil {
		admin.Close(context.Background())
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `GRANT CREATE ON DATABASE abcp_test TO abcp_bootstrap_provisioner WITH GRANT OPTION`); err != nil {
		admin.Close(context.Background())
		t.Fatal(err)
	}
	admin.Close(context.Background())
	return harness
}

func (h *postgresHarnessV1) bootstrapConfigV1(twoDomains bool) BootstrapConfigV1 {
	domainA, controllerA := strings.Repeat("3", 64), strings.Repeat("4", 64)
	domains := []AuthorityDomainBootstrapV1{{
		AuthorityDomain: domainA, RuntimeRole: "abcp_runtime_a", RuntimePassword: h.runtimePasswordA,
		RecoveryVerifierRole: "abcp_recovery_a", RecoveryVerifierPassword: h.recoveryPasswordA,
		BootstrapProvenanceSHA256: strings.Repeat("5", 64),
		Workflows:                 []WorkflowBootstrapV1{{ControllerIdentity: controllerA, Revision: 1, CanonicalState: []byte(workflowStateV1(controllerA, 1, "")), UpdatedAt: time.Unix(946684800, 0).UTC()}},
	}}
	if twoDomains {
		domainB, controllerB := strings.Repeat("b", 64), strings.Repeat("c", 64)
		domains = append(domains, AuthorityDomainBootstrapV1{
			AuthorityDomain: domainB, RuntimeRole: "abcp_runtime_b", RuntimePassword: h.runtimePasswordB,
			RecoveryVerifierRole: "abcp_recovery_b", RecoveryVerifierPassword: h.recoveryPasswordB,
			BootstrapProvenanceSHA256: strings.Repeat("d", 64),
			Workflows:                 []WorkflowBootstrapV1{{ControllerIdentity: controllerB, Revision: 1, CanonicalState: []byte(workflowStateV1(controllerB, 1, "")), UpdatedAt: time.Unix(946684800, 0).UTC()}},
		})
	}
	return BootstrapConfigV1{
		ProvisionerConnectionString: h.dsnV1("abcp_bootstrap_provisioner", h.provisionerPassword, "abcp_test"),
		MigratorConnectionString:    h.dsnV1(MigratorRoleV1, h.migratorPassword, "abcp_test"), MigratorPassword: h.migratorPassword,
		DatabaseName: "abcp_test", Domains: domains, Workers: []WorkerBootstrapV1{postgresWorkerBootstrapV1()},
	}
}

func postgresWorkerBootstrapV1() WorkerBootstrapV1 {
	workerIdentity := strings.Repeat("6", 64)
	hostIdentity := strings.Repeat("7", 64)
	keyID := strings.Repeat("8", 64)
	observerPreimage := []byte("ABCP-WORKER-OBSERVER-V1\x00" + workerIdentity + "\x00" + hostIdentity + "\x00" + keyID)
	observerIdentity := sha256HexV1(observerPreimage)
	capacityPrefix := fmt.Sprintf(`{"kind":"WorkerCapacityV1","schema_version":"worker-capacity-v1","worker_identity":"%s","aggregate_memory_bytes":12884901888,"aggregate_pids":384,"aggregate_cpu_micros_per_period":600000,"aggregate_cpu_period_micros":100000,"nofile":4096,"combined_cache_image_daemon_bytes":4294967296,"container_tmpfs_bytes":2147483648,"container_shm_bytes":268435456,"combined_container_daemon_log_bytes":67108864,"container_image_bytes":2147483648`, workerIdentity)
	capacity, capacityDigest := sealPostgresBootstrapRecordV1(capacityPrefix, "capacity_sha256")
	keyPrefix := fmt.Sprintf(`{"kind":"WorkerObservationKeyV1","schema_version":"worker-observation-key-v1","worker_identity":"%s","host_identity":"%s","observer_identity":"%s","key_id":"%s","algorithm":"ED25519","public_key_hex":"d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a","valid_from":"2000-01-01T00:00:00Z","valid_through":"2100-01-01T00:00:00Z","registration_revision":1`, workerIdentity, hostIdentity, observerIdentity, keyID)
	key, keyDigest := sealPostgresBootstrapRecordV1(keyPrefix, "key_sha256")
	registeredAt := time.Date(2000, 1, 2, 0, 0, 0, 0, time.UTC)
	provenance := strings.Repeat("5", 64)
	registrationPrefix := fmt.Sprintf(`{"kind":"WorkerObservationKeyRegistrationV1","schema_version":"worker-observation-key-registration-v1","worker_identity":"%s","host_identity":"%s","observer_identity":"%s","observer_key_sha256":"%s","worker_capacity_sha256":"%s","bootstrap_provenance_sha256":"%s","registration_revision":1,"registered_at":"2000-01-02T00:00:00Z"`, workerIdentity, hostIdentity, observerIdentity, keyDigest, capacityDigest, provenance)
	registration, registrationDigest := sealPostgresBootstrapRecordV1(registrationPrefix, "registration_sha256")
	return WorkerBootstrapV1{
		WorkerIdentity: workerIdentity, CanonicalCapacity: capacity, CapacitySHA256: capacityDigest, CapacityRevision: 1,
		CanonicalKey: key, CanonicalRegistration: registration, HostIdentity: hostIdentity, ObserverIdentity: observerIdentity,
		ObserverKeySHA256: keyDigest, RegistrationSHA256: registrationDigest, RegistrationRevision: 1,
		WorkerCapacitySHA256: capacityDigest, BootstrapProvenanceSHA256: provenance, RegisteredAt: registeredAt,
	}
}

func sealPostgresBootstrapRecordV1(prefix, field string) ([]byte, string) {
	preimage := []byte(prefix + `}`)
	digest := sha256HexV1(preimage)
	return []byte(prefix + `,"` + field + `":"` + digest + `"}`), digest
}

func (h *postgresHarnessV1) dsnV1(user, password, database string) string {
	host, portText, _ := strings.Cut(h.hostPort, ":")
	port, _ := strconv.Atoi(portText)
	u := &url.URL{Scheme: "postgres", User: url.UserPassword(user, password), Host: fmt.Sprintf("%s:%d", host, port), Path: "/" + database}
	query := u.Query()
	query.Set("sslmode", "disable")
	u.RawQuery = query.Encode()
	return u.String()
}

func workflowStateV1(controller string, revision uint64, value string) string {
	if value == "" {
		return fmt.Sprintf(`{"controller_identity":"%s","revision":%d}`, controller, revision)
	}
	return fmt.Sprintf(`{"controller_identity":"%s","revision":%d,"value":"%s"}`, controller, revision, value)
}

func randomSecretV1(t *testing.T) string {
	t.Helper()
	return randomHexV1(t, 32)
}

func randomHexV1(t *testing.T, size int) string {
	t.Helper()
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(data)
}

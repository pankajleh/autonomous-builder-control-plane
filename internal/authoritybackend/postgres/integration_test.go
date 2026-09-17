package postgres

import (
	"context"
	"crypto/rand"
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
		DatabaseName: "abcp_test", Domains: domains,
	}
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

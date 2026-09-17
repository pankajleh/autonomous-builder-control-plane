package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

type catalogQuerierV1 interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

var frozenTableNamesV1 = []string{
	"abcp_artifact_blob_v1",
	"abcp_artifact_occurrence_v1",
	"abcp_artifact_publisher_v1",
	"abcp_authority_domain_v1",
	"abcp_predecessor_directory_v1",
	"abcp_publisher_abandon_authorization_v1",
	"abcp_stage_seal_v1",
	"abcp_worker_capacity_v1",
	"abcp_worker_observation_key_registration_v1",
	"abcp_worker_reservation_v1",
	"abcp_workflow_authority_v1",
}

func verifyRuntimeSessionV1(ctx context.Context, query catalogQuerierV1, authorityDomain string) error {
	var currentUser, sessionUser, searchPath, rowSecurity string
	if err := query.QueryRow(ctx, `SELECT current_user::text,session_user::text,current_setting('search_path'),current_setting('row_security')`).Scan(&currentUser, &sessionUser, &searchPath, &rowSecurity); err != nil {
		return fmt.Errorf("read runtime session identity: %w", err)
	}
	if currentUser != sessionUser || rowSecurity != "on" || !equalSearchPathV1(searchPath) {
		return errors.New("runtime session role, search_path, or row_security is unsafe")
	}
	if isStaticRoleV1(sessionUser) {
		return errors.New("runtime session is authenticated as a static owner/group/migrator role")
	}
	var canLogin, superuser, createDB, createRole, replication, bypassRLS bool
	if err := query.QueryRow(ctx, `SELECT rolcanlogin,rolsuper,rolcreatedb,rolcreaterole,rolreplication,rolbypassrls FROM pg_catalog.pg_roles WHERE rolname=session_user`).Scan(&canLogin, &superuser, &createDB, &createRole, &replication, &bypassRLS); err != nil {
		return fmt.Errorf("read runtime role attributes: %w", err)
	}
	if !canLogin || superuser || createDB || createRole || replication || bypassRLS {
		return errors.New("runtime login has unsafe role attributes")
	}
	groups, err := directMembershipsV1(ctx, query, sessionUser)
	if err != nil {
		return err
	}
	if len(groups) != 1 || groups[0] != RuntimeGroupRoleV1 {
		return errors.New("runtime login must have only abcp_v4_runtime membership")
	}
	runtimeParents, err := directMembershipsV1(ctx, query, RuntimeGroupRoleV1)
	if err != nil || len(runtimeParents) != 0 {
		return errors.New("runtime group has an owner/recovery SET ROLE path")
	}
	var recoveryRole string
	if err := query.QueryRow(ctx, `SELECT recovery_verifier_role::text FROM abcp_v4.abcp_authority_domain_v1 WHERE authority_domain=$1 AND runtime_role=session_user`, authorityDomain).Scan(&recoveryRole); err != nil {
		return errors.New("runtime login does not map to exactly one requested authority domain")
	}
	if err := verifyPerDomainLoginV1(ctx, query, recoveryRole, RecoveryVerifierGroupRoleV1); err != nil {
		return fmt.Errorf("recovery-verifier login: %w", err)
	}
	return nil
}

// VerifyFinalHardeningV1 proves the separate phase-H result. It is safe to
// call through an ordinary runtime connection because it only reads catalogs.
func VerifyFinalHardeningV1(ctx context.Context, query catalogQuerierV1) error {
	var canLogin, createRole, superuser, bypassRLS bool
	if err := query.QueryRow(ctx, `SELECT rolcanlogin,rolcreaterole,rolsuper,rolbypassrls FROM pg_catalog.pg_roles WHERE rolname=$1`, MigratorRoleV1).Scan(&canLogin, &createRole, &superuser, &bypassRLS); err != nil {
		return fmt.Errorf("read migrator hardening: %w", err)
	}
	if canLogin || createRole || superuser || bypassRLS {
		return errors.New("abcp_v4_migrator is not NOLOGIN NOCREATEROLE NOSUPERUSER NOBYPASSRLS")
	}
	memberships, err := directMembershipsV1(ctx, query, MigratorRoleV1)
	if err != nil || len(memberships) != 0 {
		return errors.New("abcp_v4_migrator retains role memberships")
	}
	var owns int
	if err := query.QueryRow(ctx, `
		SELECT
		 (SELECT count(*) FROM pg_catalog.pg_namespace n JOIN pg_catalog.pg_roles r ON r.oid=n.nspowner WHERE r.rolname=$1)+
		 (SELECT count(*) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_roles r ON r.oid=c.relowner WHERE r.rolname=$1)+
		 (SELECT count(*) FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_roles r ON r.oid=p.proowner WHERE r.rolname=$1)`, MigratorRoleV1).Scan(&owns); err != nil || owns != 0 {
		return errors.New("abcp_v4_migrator owns database objects")
	}
	var schemaOwnerCanCreate bool
	if err := query.QueryRow(ctx, `SELECT pg_catalog.has_database_privilege($1,current_database(),'CREATE')`, SchemaOwnerRoleV1).Scan(&schemaOwnerCanCreate); err != nil || schemaOwnerCanCreate {
		return errors.New("schema owner retains database CREATE after phase H")
	}
	var ownerSetPaths int
	if err := query.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_catalog.pg_auth_members m
		JOIN pg_catalog.pg_roles parent ON parent.oid=m.roleid
		WHERE parent.rolname=ANY($1) AND m.set_option`, []string{SchemaOwnerRoleV1, DomainFunctionOwnerRoleV1, WorkerFunctionOwnerRoleV1}).Scan(&ownerSetPaths); err != nil || ownerSetPaths != 0 {
		return errors.New("a login retains SET ROLE authority to a frozen owner after phase H")
	}
	return nil
}

// VerifyFrozenCatalogV1 checks the executable fixed schema before runtime use.
func VerifyFrozenCatalogV1(ctx context.Context, query catalogQuerierV1) error {
	if err := verifyStaticRoleTopologyV1(ctx, query); err != nil {
		return err
	}
	rows, err := query.Query(ctx, `
		SELECT c.relname,r.rolname,c.relrowsecurity,c.relforcerowsecurity
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
		JOIN pg_catalog.pg_roles r ON r.oid=c.relowner
		WHERE n.nspname='abcp_v4' AND c.relkind='r'
		ORDER BY c.relname`)
	if err != nil {
		return err
	}
	var tableNames []string
	for rows.Next() {
		var name, owner string
		var rls, force bool
		if err := rows.Scan(&name, &owner, &rls, &force); err != nil {
			rows.Close()
			return err
		}
		if owner != SchemaOwnerRoleV1 || !rls || !force {
			rows.Close()
			return fmt.Errorf("table %s lacks schema-owner or forced-RLS identity", name)
		}
		tableNames = append(tableNames, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	if !equalStringsV1(tableNames, frozenTableNamesV1) {
		return fmt.Errorf("abcp_v4 table set is %v, want %v", tableNames, frozenTableNamesV1)
	}

	policies, err := frozenPoliciesV1()
	if err != nil {
		return err
	}
	rows, err = query.Query(ctx, `SELECT tablename,policyname,permissive,roles,cmd,qual,with_check FROM pg_catalog.pg_policies WHERE schemaname='abcp_v4' ORDER BY policyname`)
	if err != nil {
		return err
	}
	seenPolicies := make(map[string]bool, len(policies))
	for rows.Next() {
		var table, name, permissive, command string
		var roles []string
		var using, withCheck *string
		if err := rows.Scan(&table, &name, &permissive, &roles, &command, &using, &withCheck); err != nil {
			rows.Close()
			return err
		}
		expected, ok := policies[name]
		actualUsing, actualCheck := normalizePolicyExpressionV1(derefStringV1(using)), normalizePolicyExpressionV1(derefStringV1(withCheck))
		expectedUsing, expectedCheck := normalizePolicyExpressionV1(expected.Using), normalizePolicyExpressionV1(expected.WithCheck)
		if !ok || seenPolicies[name] || table != expected.Table || permissive != "PERMISSIVE" || command != expected.Command || len(roles) != 1 || roles[0] != expected.Role || actualUsing != expectedUsing || actualCheck != expectedCheck {
			rows.Close()
			return fmt.Errorf("frozen policy %s has table/role/command/expression drift (table=%q/%q role=%v/%q command=%q/%q using=%q/%q check=%q/%q)", name, table, expected.Table, roles, expected.Role, command, expected.Command, actualUsing, expectedUsing, actualCheck, expectedCheck)
		}
		seenPolicies[name] = true
	}
	rows.Close()
	if len(seenPolicies) != len(policies) {
		return fmt.Errorf("abcp_v4 policy set differs: got %d, want %d", len(seenPolicies), len(policies))
	}

	bodies, err := FrozenFunctionBodiesV1()
	if err != nil {
		return err
	}
	rows, err = query.Query(ctx, `
		SELECT p.proname,p.prosrc,p.proconfig,p.prosecdef,r.rolname,
		       pg_catalog.has_function_privilege($1,p.oid,'EXECUTE'),
		       EXISTS(SELECT 1 FROM pg_catalog.aclexplode(coalesce(p.proacl,pg_catalog.acldefault('f',p.proowner))) a WHERE a.grantee=0 AND a.privilege_type='EXECUTE')
		FROM pg_catalog.pg_proc p
		JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace
		JOIN pg_catalog.pg_roles r ON r.oid=p.proowner
		WHERE n.nspname='abcp_v4'
		ORDER BY p.proname`, RuntimeGroupRoleV1)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(bodies))
	for rows.Next() {
		var name, body, owner string
		var settings []string
		var securityDefiner, runtimeExecute, publicExecute bool
		if err := rows.Scan(&name, &body, &settings, &securityDefiner, &owner, &runtimeExecute, &publicExecute); err != nil {
			rows.Close()
			return err
		}
		expectedBody, ok := bodies[name]
		if !ok || seen[name] || body != expectedBody || owner != functionOwnersV1[name] || !securityDefiner || !runtimeExecute || publicExecute || !validFunctionSettingsV1(settings) {
			rows.Close()
			return fmt.Errorf("frozen function %s has body/owner/config/grant drift", name)
		}
		seen[name] = true
	}
	rows.Close()
	if len(seen) != len(bodies) {
		return fmt.Errorf("abcp_v4 function count is %d, want %d", len(seen), len(bodies))
	}
	if err := verifyExactTableACLsV1(ctx, query); err != nil {
		return err
	}
	return verifyLeastPrivilegeV1(ctx, query)
}

func verifyExactTableACLsV1(ctx context.Context, query catalogQuerierV1) error {
	expected := map[string]bool{}
	add := func(role, table string, privileges ...string) {
		for _, privilege := range privileges {
			expected[role+"\x00"+table+"\x00"+privilege] = true
		}
	}
	add(RuntimeGroupRoleV1, "abcp_authority_domain_v1", "SELECT")
	add(RuntimeGroupRoleV1, "abcp_workflow_authority_v1", "SELECT", "UPDATE")
	add(RuntimeGroupRoleV1, "abcp_artifact_blob_v1", "SELECT", "INSERT")
	for _, table := range []string{"abcp_stage_seal_v1", "abcp_artifact_publisher_v1", "abcp_artifact_occurrence_v1", "abcp_predecessor_directory_v1"} {
		add(RuntimeGroupRoleV1, table, "SELECT")
	}
	for _, table := range []string{"abcp_authority_domain_v1", "abcp_workflow_authority_v1", "abcp_artifact_blob_v1", "abcp_stage_seal_v1", "abcp_artifact_publisher_v1", "abcp_artifact_occurrence_v1", "abcp_worker_capacity_v1", "abcp_worker_observation_key_registration_v1", "abcp_worker_reservation_v1", "abcp_publisher_abandon_authorization_v1"} {
		add(RecoveryVerifierGroupRoleV1, table, "SELECT")
	}
	add(RecoveryVerifierGroupRoleV1, "abcp_publisher_abandon_authorization_v1", "INSERT")
	add(DomainFunctionOwnerRoleV1, "abcp_authority_domain_v1", "SELECT")
	add(DomainFunctionOwnerRoleV1, "abcp_workflow_authority_v1", "SELECT", "UPDATE")
	add(DomainFunctionOwnerRoleV1, "abcp_artifact_blob_v1", "SELECT", "INSERT")
	for _, table := range []string{"abcp_stage_seal_v1", "abcp_artifact_publisher_v1", "abcp_artifact_occurrence_v1", "abcp_predecessor_directory_v1"} {
		add(DomainFunctionOwnerRoleV1, table, "SELECT", "INSERT", "UPDATE")
	}
	add(DomainFunctionOwnerRoleV1, "abcp_publisher_abandon_authorization_v1", "SELECT", "UPDATE")
	add(WorkerFunctionOwnerRoleV1, "abcp_authority_domain_v1", "SELECT")
	add(WorkerFunctionOwnerRoleV1, "abcp_worker_capacity_v1", "SELECT", "UPDATE")
	add(WorkerFunctionOwnerRoleV1, "abcp_worker_observation_key_registration_v1", "SELECT")
	add(WorkerFunctionOwnerRoleV1, "abcp_worker_reservation_v1", "SELECT", "INSERT", "UPDATE")

	rows, err := query.Query(ctx, `
		SELECT coalesce(r.rolname,'PUBLIC'),c.relname,a.privilege_type
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
		CROSS JOIN LATERAL pg_catalog.aclexplode(coalesce(c.relacl,'{}'::aclitem[])) a
		LEFT JOIN pg_catalog.pg_roles r ON r.oid=a.grantee
		WHERE n.nspname='abcp_v4' AND c.relkind='r'
		  AND (a.grantee=0 OR r.rolname=ANY($1))
		ORDER BY 1,2,3`, []string{RuntimeGroupRoleV1, RecoveryVerifierGroupRoleV1, DomainFunctionOwnerRoleV1, WorkerFunctionOwnerRoleV1})
	if err != nil {
		return err
	}
	actual := map[string]bool{}
	for rows.Next() {
		var role, table, privilege string
		if err := rows.Scan(&role, &table, &privilege); err != nil {
			rows.Close()
			return err
		}
		actual[role+"\x00"+table+"\x00"+privilege] = true
	}
	rows.Close()
	if len(actual) != len(expected) {
		return fmt.Errorf("abcp_v4 group table ACL count is %d, want %d", len(actual), len(expected))
	}
	for key := range expected {
		if !actual[key] {
			return fmt.Errorf("abcp_v4 group table ACL %q is missing", key)
		}
	}
	return nil
}

func normalizePolicyExpressionV1(value string) string {
	value = strings.ReplaceAll(value, "abcp_v4.", "")
	return strings.Map(func(character rune) rune {
		switch character {
		case ' ', '\t', '\r', '\n', '(', ')', '"':
			return -1
		default:
			return character
		}
	}, strings.ToLower(value))
}

func derefStringV1(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func verifyStaticRoleTopologyV1(ctx context.Context, query catalogQuerierV1) error {
	roles := []string{SchemaOwnerRoleV1, RuntimeGroupRoleV1, RecoveryVerifierGroupRoleV1, DomainFunctionOwnerRoleV1, WorkerFunctionOwnerRoleV1}
	for _, role := range roles {
		var login, superuser, createDB, createRole, replication, bypassRLS bool
		if err := query.QueryRow(ctx, `SELECT rolcanlogin,rolsuper,rolcreatedb,rolcreaterole,rolreplication,rolbypassrls FROM pg_catalog.pg_roles WHERE rolname=$1`, role).Scan(&login, &superuser, &createDB, &createRole, &replication, &bypassRLS); err != nil {
			return fmt.Errorf("read frozen role %s: %w", role, err)
		}
		if login || superuser || createDB || createRole || replication || bypassRLS {
			return fmt.Errorf("frozen role %s has unsafe attributes", role)
		}
		memberships, err := directMembershipsV1(ctx, query, role)
		if err != nil || len(memberships) != 0 {
			return fmt.Errorf("frozen role %s has an unexpected parent membership", role)
		}
	}
	return nil
}

func verifyPerDomainLoginV1(ctx context.Context, query catalogQuerierV1, role, expectedGroup string) error {
	if role == "" || isStaticRoleV1(role) {
		return errors.New("login role identity is absent or static")
	}
	var login, superuser, createDB, createRole, replication, bypassRLS bool
	if err := query.QueryRow(ctx, `SELECT rolcanlogin,rolsuper,rolcreatedb,rolcreaterole,rolreplication,rolbypassrls FROM pg_catalog.pg_roles WHERE rolname=$1`, role).Scan(&login, &superuser, &createDB, &createRole, &replication, &bypassRLS); err != nil {
		return err
	}
	if !login || superuser || createDB || createRole || replication || bypassRLS {
		return errors.New("login role has unsafe attributes")
	}
	memberships, err := directMembershipsV1(ctx, query, role)
	if err != nil || len(memberships) != 1 || memberships[0] != expectedGroup {
		return errors.New("login role has unexpected memberships")
	}
	return nil
}

func verifyLeastPrivilegeV1(ctx context.Context, query catalogQuerierV1) error {
	type privilege struct {
		role, table, operation string
		want                   bool
	}
	checks := []privilege{
		{RuntimeGroupRoleV1, "abcp_workflow_authority_v1", "SELECT", true},
		{RuntimeGroupRoleV1, "abcp_workflow_authority_v1", "UPDATE", true},
		{RuntimeGroupRoleV1, "abcp_artifact_blob_v1", "INSERT", true},
		{RuntimeGroupRoleV1, "abcp_stage_seal_v1", "INSERT", false},
		{RuntimeGroupRoleV1, "abcp_artifact_publisher_v1", "UPDATE", false},
		{RuntimeGroupRoleV1, "abcp_worker_observation_key_registration_v1", "SELECT", false},
		{RuntimeGroupRoleV1, "abcp_publisher_abandon_authorization_v1", "SELECT", false},
		{RecoveryVerifierGroupRoleV1, "abcp_workflow_authority_v1", "SELECT", true},
		{RecoveryVerifierGroupRoleV1, "abcp_predecessor_directory_v1", "SELECT", false},
		{RecoveryVerifierGroupRoleV1, "abcp_publisher_abandon_authorization_v1", "INSERT", true},
		{RecoveryVerifierGroupRoleV1, "abcp_workflow_authority_v1", "UPDATE", false},
	}
	for _, check := range checks {
		var got bool
		qualified := "abcp_v4." + check.table
		if err := query.QueryRow(ctx, `SELECT pg_catalog.has_table_privilege($1,c.oid,$3) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='abcp_v4' AND c.relname=$2`, check.role, check.table, check.operation).Scan(&got); err != nil || got != check.want {
			return fmt.Errorf("least-privilege drift for %s %s on %s: got %v, want %v (query error: %v)", check.role, check.operation, qualified, got, check.want, err)
		}
	}
	return nil
}

func directMembershipsV1(ctx context.Context, query catalogQuerierV1, member string) ([]string, error) {
	rows, err := query.Query(ctx, `
		SELECT parent.rolname
		FROM pg_catalog.pg_auth_members m
		JOIN pg_catalog.pg_roles parent ON parent.oid=m.roleid
		JOIN pg_catalog.pg_roles child ON child.oid=m.member
		WHERE child.rolname=$1
		ORDER BY parent.rolname`, member)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, err
		}
		result = append(result, role)
	}
	return result, rows.Err()
}

func frozenPolicyNamesV1() []string {
	const marker = "CREATE POLICY "
	remaining := frozenFunctionsSQL
	var names []string
	for {
		index := strings.Index(remaining, marker)
		if index < 0 {
			break
		}
		remaining = remaining[index+len(marker):]
		end := strings.IndexByte(remaining, ' ')
		if end <= 0 {
			break
		}
		names = append(names, remaining[:end])
		remaining = remaining[end:]
	}
	sort.Strings(names)
	return names
}

func validFunctionSettingsV1(settings []string) bool {
	if len(settings) != 2 {
		return false
	}
	values := make(map[string]string, 2)
	for _, setting := range settings {
		parts := strings.SplitN(setting, "=", 2)
		if len(parts) != 2 {
			return false
		}
		values[parts[0]] = parts[1]
	}
	path := strings.Split(values["search_path"], ",")
	return len(path) == 2 && strings.TrimSpace(path[0]) == "pg_catalog" && strings.TrimSpace(path[1]) == "abcp_v4" && values["row_security"] == "on"
}

func equalSearchPathV1(value string) bool {
	parts := strings.Split(value, ",")
	return len(parts) == 2 && strings.TrimSpace(parts[0]) == "pg_catalog" && strings.TrimSpace(parts[1]) == "abcp_v4"
}

func equalStringsV1(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func isStaticRoleV1(role string) bool {
	for _, candidate := range []string{MigratorRoleV1, SchemaOwnerRoleV1, RuntimeGroupRoleV1, RecoveryVerifierGroupRoleV1, DomainFunctionOwnerRoleV1, WorkerFunctionOwnerRoleV1} {
		if role == candidate {
			return true
		}
	}
	return false
}

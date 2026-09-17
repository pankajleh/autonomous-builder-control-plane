package postgres

import (
	_ "embed"
	"fmt"
	"sort"
	"strings"
)

const (
	SchemaNameV1                  = "abcp_v4"
	MigratorRoleV1                = "abcp_v4_migrator"
	SchemaOwnerRoleV1             = "abcp_v4_schema_owner"
	RuntimeGroupRoleV1            = "abcp_v4_runtime"
	RecoveryVerifierGroupRoleV1   = "abcp_v4_recovery_verifier"
	DomainFunctionOwnerRoleV1     = "abcp_v4_domain_fn"
	WorkerFunctionOwnerRoleV1     = "abcp_v4_worker_fn"
	MaxCanonicalRequestBytesV1    = 1 << 20
	MaxCutoverRequestBytesV1      = 4_259_848
	MaxCutoverJSONOverheadBytesV1 = 65_536
	MaxArtifactBytesV1            = 32 << 20
)

// These assets are copied byte-for-byte from the accepted A document. The
// function definitions deliberately remain SQL assets: catalog verification
// compares pg_proc.prosrc with the exact bytes between their $fn$ delimiters.

//go:embed provision.sql
var frozenProvisionSQL string

//go:embed schema.sql
var frozenSchemaSQL string

//go:embed functions.sql
var frozenFunctionsSQL string

//go:embed harden.sql
var frozenHardenSQL string

// FrozenSQLV1 returns the immutable static blocks used by phases P, M and H.
// The returned strings are copies of immutable Go strings and contain a final
// LF exactly as embedded.
func FrozenSQLV1() (provision, schema, functions, harden string) {
	return frozenProvisionSQL, frozenSchemaSQL, frozenFunctionsSQL, frozenHardenSQL
}

var functionOwnersV1 = map[string]string{
	"abcp_publisher_open_v1":             DomainFunctionOwnerRoleV1,
	"abcp_publisher_append_v1":           DomainFunctionOwnerRoleV1,
	"abcp_publisher_finalize_v1":         DomainFunctionOwnerRoleV1,
	"abcp_publisher_abandon_v1":          DomainFunctionOwnerRoleV1,
	"abcp_close_stage_v1":                DomainFunctionOwnerRoleV1,
	"abcp_predecessor_writer_open_v1":    DomainFunctionOwnerRoleV1,
	"abcp_predecessor_writer_release_v1": DomainFunctionOwnerRoleV1,
	"abcp_cutover_v1":                    DomainFunctionOwnerRoleV1,
	"abcp_worker_reserve_v1":             WorkerFunctionOwnerRoleV1,
	"abcp_worker_transition_v1":          WorkerFunctionOwnerRoleV1,
}

// FrozenFunctionBodiesV1 extracts the literal accepted function bodies. It
// fails closed if the embedded block is incomplete, duplicated, or malformed.
func FrozenFunctionBodiesV1() (map[string]string, error) {
	result := make(map[string]string, len(functionOwnersV1))
	const prefix = "CREATE FUNCTION abcp_v4."
	remaining := frozenFunctionsSQL
	for {
		start := strings.Index(remaining, prefix)
		if start < 0 {
			break
		}
		remaining = remaining[start+len(prefix):]
		nameEnd := strings.IndexByte(remaining, '(')
		if nameEnd <= 0 {
			return nil, fmt.Errorf("frozen function name is malformed")
		}
		name := remaining[:nameEnd]
		marker := " AS $fn$"
		bodyStart := strings.Index(remaining, marker)
		if bodyStart < 0 {
			return nil, fmt.Errorf("frozen function %s lacks its opening delimiter", name)
		}
		bodyStart += len(marker)
		bodyEnd := strings.Index(remaining[bodyStart:], "$fn$;")
		if bodyEnd < 0 {
			return nil, fmt.Errorf("frozen function %s lacks its closing delimiter", name)
		}
		body := remaining[bodyStart : bodyStart+bodyEnd]
		if _, duplicate := result[name]; duplicate {
			return nil, fmt.Errorf("frozen function %s is duplicated", name)
		}
		result[name] = body
		remaining = remaining[bodyStart+bodyEnd+len("$fn$;"):]
	}
	if len(result) != len(functionOwnersV1) {
		return nil, fmt.Errorf("frozen function count is %d, want %d", len(result), len(functionOwnersV1))
	}
	for name := range functionOwnersV1 {
		if _, ok := result[name]; !ok {
			return nil, fmt.Errorf("frozen function %s is missing", name)
		}
	}
	return result, nil
}

func frozenFunctionNamesV1() []string {
	names := make([]string, 0, len(functionOwnersV1))
	for name := range functionOwnersV1 {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

type frozenPolicyV1 struct {
	Name, Table, Command, Role string
	Using, WithCheck           string
}

func frozenPoliciesV1() (map[string]frozenPolicyV1, error) {
	result := make(map[string]frozenPolicyV1, 35)
	for _, line := range strings.Split(frozenFunctionsSQL, "\n") {
		if !strings.HasPrefix(line, "CREATE POLICY ") {
			continue
		}
		remaining := strings.TrimPrefix(line, "CREATE POLICY ")
		name, remaining, ok := strings.Cut(remaining, " ON abcp_v4.")
		if !ok {
			return nil, fmt.Errorf("frozen policy statement has invalid ON clause")
		}
		table, remaining, ok := strings.Cut(remaining, " FOR ")
		if !ok {
			return nil, fmt.Errorf("frozen policy %s has invalid FOR clause", name)
		}
		command, remaining, ok := strings.Cut(remaining, " TO ")
		if !ok {
			return nil, fmt.Errorf("frozen policy %s has invalid TO clause", name)
		}
		roleEnd := strings.IndexByte(remaining, ' ')
		if roleEnd < 0 {
			roleEnd = strings.IndexByte(remaining, ';')
		}
		if roleEnd <= 0 {
			return nil, fmt.Errorf("frozen policy %s has invalid role clause", name)
		}
		policy := frozenPolicyV1{Name: name, Table: table, Command: command, Role: remaining[:roleEnd]}
		remaining = remaining[roleEnd:]
		var err error
		policy.Using, remaining, err = takePolicyExpressionV1(remaining, "USING")
		if err != nil {
			return nil, fmt.Errorf("frozen policy %s: %w", name, err)
		}
		policy.WithCheck, remaining, err = takePolicyExpressionV1(remaining, "WITH CHECK")
		if err != nil {
			return nil, fmt.Errorf("frozen policy %s: %w", name, err)
		}
		if strings.TrimSpace(remaining) != ";" {
			return nil, fmt.Errorf("frozen policy %s has trailing SQL", name)
		}
		if _, duplicate := result[name]; duplicate {
			return nil, fmt.Errorf("frozen policy %s is duplicated", name)
		}
		result[name] = policy
	}
	if len(result) != 35 {
		return nil, fmt.Errorf("frozen policy count is %d, want 35", len(result))
	}
	return result, nil
}

func takePolicyExpressionV1(input, keyword string) (expression, remaining string, resultErr error) {
	trimmed := strings.TrimLeft(input, " ")
	prefix := keyword + " ("
	if !strings.HasPrefix(trimmed, prefix) {
		return "", input, nil
	}
	start := len(keyword) + 1
	depth := 0
	for index := start; index < len(trimmed); index++ {
		switch trimmed[index] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return trimmed[start : index+1], trimmed[index+1:], nil
			}
		}
	}
	return "", input, fmt.Errorf("%s expression is unterminated", keyword)
}

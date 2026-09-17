package postgres

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPostgresFrozenFunctionsAndCrossHostPublisherRecovery(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	designPath := filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "docs", "plans", "assurance-proof-obligation-governance-enforcement-assurance.md")
	design, err := os.ReadFile(designPath)
	if err != nil {
		t.Fatalf("read accepted A document: %v", err)
	}
	wantProvision := exactSQLBlock(t, string(design), "Phase P executes these exact static-role statements", 0)
	wantSchema := exactSQLBlock(t, string(design), "Phase M then begins the single migrator transaction", 0)
	wantFunctions := exactSQLBlock(t, string(design), "The following 35 policy statements are literal SQL", 0)
	wantHarden := exactSQLBlock(t, string(design), "After Phase M commits, Phase H executes", 0)
	provision, schema, functions, harden := FrozenSQLV1()
	for name, pair := range map[string][2]string{
		"provision": {provision, wantProvision}, "schema": {schema, wantSchema},
		"functions": {functions, wantFunctions}, "harden": {harden, wantHarden},
	} {
		if pair[0] != pair[1] {
			t.Fatalf("embedded %s SQL is not byte-identical to accepted A", name)
		}
	}
	all := provision + schema + functions + harden
	for token, count := range map[string]int{
		"CREATE TABLE ": 11, "CREATE FUNCTION ": 10, "CREATE POLICY ": 35,
		" ENABLE ROW LEVEL SECURITY;": 11, " FORCE ROW LEVEL SECURITY;": 11,
		"ALTER FUNCTION ": 10, "\nGRANT ": 17, "\nREVOKE ": 7,
	} {
		if got := strings.Count(all, token); got != count {
			t.Fatalf("frozen SQL has %d occurrences of %q, want %d", got, token, count)
		}
	}
	if got := len(frozenPolicyNamesV1()); got != 35 {
		t.Fatalf("frozen policy catalog has %d entries", got)
	}
	bodies, err := FrozenFunctionBodiesV1()
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 10 {
		t.Fatalf("frozen function body catalog has %d entries", len(bodies))
	}
	for name, body := range bodies {
		if !strings.HasPrefix(body, "\nDECLARE ") || !strings.HasSuffix(body, "END ") {
			t.Fatalf("function %s body excludes bytes between its literal delimiters", name)
		}
		if !strings.Contains(functions, "AS $fn$"+body+"$fn$;") {
			t.Fatalf("function %s body cannot be reconstructed byte-exactly", name)
		}
	}

	t.Run("postgres17-bootstrap-and-hardening", testPostgres17BootstrapAndHardening)
}

func TestPostgresCutoverRequestBound(t *testing.T) {
	field := make([]byte, MaxCanonicalRequestBytesV1)
	for index := range field {
		field[index] = 'a'
	}
	request := CutoverRequestV1{
		Kind: "CutoverRequestV1", SchemaVersion: "cutover-request-v1", AuthorityDomain: "a",
		RequestID: strings.Repeat("1", 64), ControllerIdentity: strings.Repeat("2", 64),
		ExpectedDirectoryRevision: 1, WriterEpoch: 1,
		TombstonedDirectoryB64: base64.StdEncoding.EncodeToString(field), TombstonedDirectorySHA256: strings.Repeat("3", 64),
		PredecessorStateB64: base64.StdEncoding.EncodeToString(field), PredecessorStateSHA256: strings.Repeat("4", 64),
		PredecessorStateRevision: 1, PredecessorArtifactSHA256: strings.Repeat("4", 64), CutoverSHA256: strings.Repeat("5", 64),
		SuccessorStateB64: base64.StdEncoding.EncodeToString(field), SuccessorStateSHA256: strings.Repeat("6", 64), SuccessorRevision: 2,
	}
	data, _, err := marshalOperationRequestV1("abcp_cutover_v1", request)
	if err != nil {
		t.Fatalf("three maximum decoded fields must fit: %v", err)
	}
	if len(data) > MaxCutoverRequestBytesV1 {
		t.Fatalf("accepted cutover request is %d bytes", len(data))
	}
	request.SuccessorStateB64 = base64.StdEncoding.EncodeToString(append(field, 'x'))
	if _, _, err := marshalOperationRequestV1("abcp_cutover_v1", request); err == nil {
		t.Fatal("decoded field above one MiB was accepted")
	}
	request.SuccessorStateB64 = base64.StdEncoding.EncodeToString(field)
	request.AuthorityDomain = strings.Repeat("a", 129)
	if _, _, err := marshalOperationRequestV1("abcp_cutover_v1", request); err == nil {
		t.Fatal("cutover JSON overhead/domain bound was not enforced")
	}
}

func TestPostgresWorkflowAuthorityBackendCAS(t *testing.T) {
	testPostgresWorkflowAuthorityBackendCAS(t)
}

func TestPostgresDomainIsolationArtifactStore(t *testing.T) {
	testPostgresDomainIsolation(t)
}

func exactSQLBlock(t *testing.T, document, anchor string, skip int) string {
	t.Helper()
	anchorIndex := strings.Index(document, anchor)
	if anchorIndex < 0 {
		t.Fatalf("accepted A anchor %q is missing", anchor)
	}
	remainder := document[anchorIndex+len(anchor):]
	for index := 0; index <= skip; index++ {
		start := strings.Index(remainder, "```sql\n")
		if start < 0 {
			t.Fatalf("SQL block after %q is missing", anchor)
		}
		remainder = remainder[start+len("```sql\n"):]
	}
	end := strings.Index(remainder, "```\n")
	if end < 0 {
		t.Fatalf("SQL block after %q is unterminated", anchor)
	}
	return remainder[:end]
}

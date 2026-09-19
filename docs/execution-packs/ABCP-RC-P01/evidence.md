# ABCP-RC-P01 Delivery Evidence

Status: **COMPLETE — HUMAN MERGE GATE**

## Authority identities

- Pack: `ABCP-RC-P01`
- Governing architecture SHA-256: `883be23eb566c7c11ed675bdfdadb39eb12e0bce5ec00f7f6e65b824171713ad`
- Execution-pack SHA-256: `5b1a7a4361c41b89eb6f6960fea202b891bdde9f46310c2715cbaab2b4f2d14d`
- Review-contract SHA-256: `ac47543fdb96483e285730fa227c0c5a5968c1ef11e16ede78cef065d4dc950d`
- Repo C authority commit: `b5de18b79765be089317d86786bf64956452ae10`
- Repo C product contract SHA-256: `ffc01430674db7fd342647f2f0697691f9f58a9510558f3d199cb3da43d10d9e`
- Repo C ABCP boundary SHA-256: `1560ce20c6b4066b342978daefde8fcf00752a11e0761781a508f88873320268`
- Repo C reconciliation SHA-256: `0fd5c11f59a27367bbbb3ed8e19856a22929d5ac221e4f49190b35b78c8ae81b`
- Repo C ProductAuthorization source SHA-256: `a5c09c4ea825b4c44d3bb41779a7fe51e54155dda812359235df0120845646ac`

All Repo C hashes above were recomputed from exact commit `b5de18b79765be089317d86786bf64956452ae10` and matched the frozen authority.

## Git identities

- Exact implementation base SHA: `4d45202f5b411d9c91caf5fef906d1eff9b26e4b`
- Feature branch: `feature/abcp-rc-p01-run-admission-20260919`
- Executable tested SHA: `3cdda8adc92756fa14e87af7d4e83cda2d95b943`
- Review-1 correction executable SHA: `3cdda8adc92756fa14e87af7d4e83cda2d95b943`
- Evidence-only successor SHA: `e895e8b7010176c8aa45d6f836912344b1be1865`
- Draft PR: `#22`, `https://github.com/pankajleh/autonomous-builder-control-plane/pull/22`
- PR base: `main`
- PR state at evidence preparation: open Draft, merge state `CLEAN`
- Merge: **NOT DONE — HUMAN GATE**

Commit `e895e8b7010176c8aa45d6f836912344b1be1865` is the first evidence-only successor to the executable. The follow-up evidence-only binding commit records that now-known identity and changes no executable behavior.

## Implementation summary

Changed production paths:

- `cmd/abcp/main.go`
- `internal/governance/governance.go`
- `internal/runadmission/controller.go`
- `internal/runadmission/controller_linux.go`
- `internal/serviceapi/config.go`
- `internal/serviceapi/server.go`
- `internal/strictjson/strictjson.go`
- `internal/workflowauthoritypg/backend.go`

Changed test paths:

- `internal/governance/governance_test.go`
- `internal/runadmission/controller_linux_test.go`
- `internal/serviceapi/server_test.go`
- `internal/strictjson/strictjson_test.go`
- `internal/workflowauthoritypg/backend_test.go`

No product-facing DTO field or schema was added or removed. The correction enforces exact case-sensitive and valid-UTF-8 JSON, machine-service delegated-actor grants, repository-derived workflow initialization, owner/mode-safe private roots, exact registered-run binding, and non-success reconciliation for ambiguous launch state. Exact registered replay remains stable after repository HEAD advances.

Repo C adapter/UI, human-decision continuation, retry/resume/recovery, provider selection, merge/publication authority, live infrastructure and all later-pack work remain excluded.

## Expected-red and regression evidence

Baseline focused gates at exact base `4d45202f5b411d9c91caf5fef906d1eff9b26e4b` all passed, establishing that the candidate's existing suite lacked the hardening coverage.

Observed expected-red commands and results before production correction:

- `go test -count=1 ./internal/serviceapi -run 'TestRunAdmission(StrictDecodeAndTypedErrors|RequiresMachineServiceDelegationGrant)'`: RED. Case-alias and invalid-UTF-8 requests returned `202`; an admission without a delegated-actor grant also returned `202`.
- `go test -count=1 ./internal/runadmission -run 'TestAdmission(ProfileParsingRejectsUnsafeProtectedFiles|HeldLaunchRequiresRegisteredExactBinding|RejectsConflictingRegisteredRunBinding|RejectsModeWidenedPrivateRoots)'`: RED. A case-alias profile, held-launch false success, conflicting registered binding and mode-widened ledger/evidence roots were accepted.
- `go test -count=1 ./internal/workflowauthoritypg -run TestParseConfigurationStrict`: RED. Invalid UTF-8 workflow configuration was accepted.
- `go test -count=1 ./internal/governance -run TestControllerExposesRepositoryDerivedIdentityForBackendInitialization`: RED at compile time because trusted repository-derived identity was not exposed for backend initialization.
- Exact registered replay after a repository HEAD advance: RED against the intermediate binding correction, then fixed before executable freeze so replay does not regress while validating the full durable binding.

ALREADY_SATISFIED at the exact base:

- the profile invalid-UTF-8 mutation used in the initial table was already rejected indirectly by repository/remote identity validation;
- duplicate and unknown JSON fields, protected config file mode/symlink/hard-link checks, ignored symlink-free input, deterministic request identity, conflicting request reuse, base mismatch and receipt-before-launch behavior already passed and were preserved;
- retry, resume and recovery capabilities remained false.

Final regressions additionally cover durable catalog failure, corrupt receipt state, launch failure with one preserved durable identity, exact and conflicting registered-run bindings, post-startup private-root mode widening, strict manifest/profile/workflow/public JSON, and denial of a granted non-service principal.

## Focused gates at executable SHA

- `go test -count=1 ./internal/runadmission`: PASS (`1.613s`)
- `go test -count=1 ./internal/serviceapi`: PASS (`0.033s`)
- `go test -count=1 ./internal/workflowauthoritypg`: PASS (`0.011s`)
- `go test -count=1 ./internal/governance`: PASS (`3.928s`)
- `go test -count=1 ./cmd/abcp`: PASS (`0.619s`)
- `go test -count=1 -race ./internal/runadmission ./internal/serviceapi ./internal/workflowauthoritypg`: PASS (`2.741s`, `1.102s`, `1.030s` respectively)
- PostgreSQL integration test: SKIPPED exactly as coded because `ABCP_WORKFLOW_AUTHORITY_PG_TEST_DSN` was unavailable. No live infrastructure was provisioned.

## Broad gates at executable SHA

- `go test -count=1 ./...`: PASS across all 33 listed packages (`internal/runtimecatalog` slowest at `92.953s`)
- `go test -count=1 -race ./...`: PASS across all 33 listed packages (`internal/runtimecatalog` slowest at `335.666s`)
- `go vet ./...`: PASS
- `GOOS=darwin GOARCH=amd64 go build ./cmd/abcp`: PASS
- `GOOS=windows GOARCH=amd64 go build ./cmd/abcp`: PASS
- `git diff --check`: PASS

Classified pre-existing/flaky exceptions: none.

## Contract and schema hashes at executable SHA

- `internal/serviceapi/dto.go` SHA-256: `1d52733b6dbfb49a9b961bd8f5ea364a00a34f674d28f171478a8b98671d21c5`
- `docs/architecture/ABCP-EP006-RC-INTEGRATION-ARCHITECTURE.md` SHA-256: `883be23eb566c7c11ed675bdfdadb39eb12e0bce5ec00f7f6e65b824171713ad`
- `docs/execution-packs/ABCP-RC-P01-RUN-ADMISSION.execution-pack.md` SHA-256: `5b1a7a4361c41b89eb6f6960fea202b891bdde9f46310c2715cbaab2b4f2d14d`
- `internal/workflowauthoritypg/schema.sql` SHA-256: `373aceffb999dfdb999254abd5c7ed909a947e992cd98d23643ff31207dc8ec0`
- `internal/strictjson/strictjson.go` SHA-256: `59f5d7da17bb7f864b0222eebcbb78133ca8094e0a127c29c918f7eb082a4481`

## Authorized review sequence

Review 1 result: accepted findings.

Accepted findings:

- Required failure-matrix coverage needed explicit durable catalog/receipt failure and launch-failure replay tests.
- Machine-service enforcement needed a regression proving that a non-service principal remains denied even when a delegation grant exists.

Review-1 corrections were test-only additions; all affected focused/race gates and every required broad gate were rerun against correction executable SHA `3cdda8adc92756fa14e87af7d4e83cda2d95b943`.

OUT_OF_SCOPE_REVIEW suggestions: none.

Review 2 authorized: yes.

Review 2 final result: `<<<RALPHEX:REVIEW_DONE>>>`.

Remaining accepted findings: none. No third review was performed.

## Final scope and delivery checks

- Allowed-path check: PASS; every executable and evidence path matches the exact pack regex.
- Executable changed files: 13; PASS against the active bounded wrapper.
- Executable diff: 752 insertions and 274 deletions; PASS against the active bounded wrapper.
- Executable worktree before evidence: clean.
- Executable branch push: PASS; remote head was `3cdda8adc92756fa14e87af7d4e83cda2d95b943` before the evidence successor.
- Draft PR targets `main`: PASS (`#22`, exact required title, Draft).
- Executable SHA named by evidence: PASS.
- Evidence-only successor explicitly distinguished: PASS (`e895e8b7010176c8aa45d6f836912344b1be1865`); it changes no executable behavior.
- Unresolved scope-crossing finding: none.
- Merge: **NOT DONE — HUMAN GATE**.

## Completion claim

`ABCP_RC_P01_RUN_ADMISSION_COMPLETE`

This evidence document does not authorize merge or live infrastructure mutation.

# ABCP-RC-P01 Delivery Evidence

Status: **BOUNDED PRG CORRECTIONS COMPLETE — HUMAN MERGE GATE**

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
- Executable tested SHA: `8a4ef3cd0a4a450cd924666682c850278fe9ace5`
- Review-1 correction executable SHA: `3cdda8adc92756fa14e87af7d4e83cda2d95b943`
- Bounded PRG correction executable SHA: `8a4ef3cd0a4a450cd924666682c850278fe9ace5`
- Prior evidence-only successor SHA: `e895e8b7010176c8aa45d6f836912344b1be1865`
- Bounded PRG evidence-only successor SHA: `6b08ac4eb09947421ddbba5884086cf28e25681a`
- Draft PR: `#22`, `https://github.com/pankajleh/autonomous-builder-control-plane/pull/22`
- PR base: `main`
- PR state at evidence preparation: open Draft, merge state `CLEAN`
- Merge: **NOT DONE — HUMAN GATE**

Commit `e895e8b7010176c8aa45d6f836912344b1be1865` is the first evidence-only successor to the executable. The follow-up evidence-only binding commit records that now-known identity and changes no executable behavior.

Commit `8a4ef3cd0a4a450cd924666682c850278fe9ace5` is the exact executable correction tested for the later user-authorized bounded PRG review. Evidence-only successor `6b08ac4eb09947421ddbba5884086cf28e25681a` records those results and changes no executable behavior. The follow-up identity-only commit binds that now-known evidence SHA and likewise changes no executable behavior.

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

- `cmd/abcp/main_test.go`
- `internal/governance/governance_test.go`
- `internal/runadmission/controller_linux_test.go`
- `internal/serviceapi/server_test.go`
- `internal/strictjson/strictjson_test.go`
- `internal/workflowauthoritypg/backend_test.go`

No product-facing DTO field or schema was added or removed. The original correction enforces exact case-sensitive and valid-UTF-8 JSON, machine-service delegated-actor grants, repository-derived workflow initialization, owner/mode-safe private roots, exact registered-run binding, and non-success reconciliation for ambiguous launch state. The bounded PRG correction additionally freezes the resolved private profile authority in the durable receipt, persists the exact private material/launch/catalog binding before launch, reconciles exact replay independently of later profile changes or removal while still verifying frozen material, rejects unbound receipt rebinding, and rejects configured repository identities that differ from the governance controller's canonical origin. Exact registered replay remains stable after repository HEAD advances.

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

Bounded PRG review regression evidence:

- `go test -count=1 ./internal/runadmission -run TestAdmissionRegisteredExactReplaySurvivesPrivateProfileChange -v`: RED before correction; exact replay returned `unsafe admission materialization` after a valid private profile change.
- An isolated focused reproduction with the original profile ID removed returned `unknown admission profile` for the exact registered replay; the kept regressions cover both changed and removed profiles.
- An isolated focused reproduction configured a non-origin repository identity that matched an alternate remote/template; `NewController` incorrectly accepted the profile before correction. `TestAdmissionProfileRejectsRepositoryIdentityThatIsNotCanonicalOrigin` now covers it.
- Command coverage showed the repository-derived `EnsureInitialized` wiring was unexecuted and reverting it left the prior getter-only regression green. `TestRunCommandInitializesWorkflowAuthorityWithControllerDerivedRepositoryIdentity` now exercises the composition path with a recording backend.
- Additional kept regressions cover changed-profile unbound receipts, bound relaunch after profile change, and registered replay rejection when frozen admission material drifts.

## Focused gates at corrected executable SHA

- `go test -count=1 ./internal/runadmission`: PASS (`3.197s`)
- `go test -count=1 ./internal/serviceapi`: PASS (`0.028s`)
- `go test -count=1 ./internal/workflowauthoritypg`: PASS (`0.013s`)
- `go test -count=1 ./internal/governance`: PASS (`4.404s`)
- `go test -count=1 ./cmd/abcp`: PASS (`0.647s`)
- `go test -count=1 -race ./internal/runadmission ./internal/serviceapi ./internal/workflowauthoritypg`: PASS (`4.693s`, `1.137s`, `1.069s` respectively)
- PostgreSQL integration test: SKIPPED exactly as coded because `ABCP_WORKFLOW_AUTHORITY_PG_TEST_DSN` was unavailable. No live infrastructure was provisioned.

## Broad gates at corrected executable SHA

- `go test -count=1 ./...`: PASS across all listed packages (`internal/runtimecatalog` slowest at `61.277s`)
- `go test -count=1 -race ./...`: PASS across all listed packages (`internal/runtimecatalog` slowest at `314.232s`)
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

## User-authorized bounded PRG review

This later bounded PRG review was explicitly requested by the user and is recorded separately from the already completed pack Review 1 / Review 2 sequence above; it is not represented as a third pack review.

Accepted findings and corrections:

- Exact replay incorrectly depended on the mutable current private profile. The corrected receipt freezes the selected private profile authority, a protected binding freezes the exact material/launch/catalog inputs before launch, registered replay uses that original binding, and unbound receipts cannot adopt changed private authority.
- Profile loading accepted a non-origin repository identity that the actual governance run path necessarily rejected. Profile loading now requires the same canonical-origin identity derived by `ControllerV1`.
- The repository-derived workflow-authority initialization correction lacked command-composition regression coverage. A recording backend now proves the run command passes the controller-derived repository identity rather than the manifest identity.

Correction executable SHA: `8a4ef3cd0a4a450cd924666682c850278fe9ace5`.

Required focused, focused-race, broad, broad-race, vet, cross-platform build, and diff-check gates all passed against that exact executable SHA. `ABCP_WORKFLOW_AUTHORITY_PG_TEST_DSN` was unavailable, so the PostgreSQL integration test skipped exactly as coded; no infrastructure was provisioned.

## Final scope and delivery checks

- Allowed-path check: PASS; every executable and evidence path matches the exact pack regex.
- Executable changed files: 14; PASS against the active bounded wrapper.
- Executable diff: 1,299 insertions and 321 deletions; PASS against the active bounded wrapper.
- Executable worktree before evidence: clean.
- Bounded correction delivery push: PASS; the remote branch includes executable correction `8a4ef3cd0a4a450cd924666682c850278fe9ace5`, evidence successor `6b08ac4eb09947421ddbba5884086cf28e25681a`, and the identity-only successor.
- Draft PR targets `main`: PASS (`#22`, exact required title, Draft).
- Executable SHA named by evidence: PASS (`8a4ef3cd0a4a450cd924666682c850278fe9ace5`).
- Evidence-only successor explicitly distinguished: PASS (`6b08ac4eb09947421ddbba5884086cf28e25681a`); it changes no executable behavior, and the follow-up identity-only commit only binds that SHA.
- Unresolved scope-crossing finding: none.
- Merge: **NOT DONE — HUMAN GATE**.

## Completion claim

`ABCP_RC_P01_RUN_ADMISSION_COMPLETE`

This evidence document does not authorize merge or live infrastructure mutation.

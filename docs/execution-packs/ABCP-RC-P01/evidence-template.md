# ABCP-RC-P01 Evidence Template

Status: **EVIDENCE TEMPLATE — FILL DURING BOUNDED RUN**

## Authority identities

- Pack: `ABCP-RC-P01`
- Governing architecture SHA-256: `<fill after authority commit>`
- Execution-pack SHA-256: `<fill after authority commit>`
- Review-contract SHA-256: `<fill after authority commit>`
- Repo C authority commit: `b5de18b79765be089317d86786bf64956452ae10`
- Repo C product contract SHA-256: `ffc01430674db7fd342647f2f0697691f9f58a9510558f3d199cb3da43d10d9e`
- Repo C ABCP boundary SHA-256: `1560ce20c6b4066b342978daefde8fcf00752a11e0761781a508f88873320268`
- Repo C reconciliation SHA-256: `0fd5c11f59a27367bbbb3ed8e19856a22929d5ac221e4f49190b35b78c8ae81b`
- Repo C ProductAuthorization source SHA-256: `a5c09c4ea825b4c44d3bb41779a7fe51e54155dda812359235df0120845646ac`

## Git identities

- Exact implementation base SHA: `<40-char SHA>`
- Feature branch: `<branch>`
- Executable tested SHA: `<40-char SHA>`
- Evidence-only successor SHA: `<40-char SHA>`
- Draft PR: `<number/url>`
- PR base: `main`
- Merge: **NOT DONE — HUMAN GATE**
## Implementation summary

- Changed production paths: `<list>`
- Changed test paths: `<list>`
- Contract/API changes: `<summary>`
- Explicit exclusions preserved: `<statement>`

## Expected-red / regression evidence

For each in-scope correction:
- defect/requirement: `<text>`
- pre-fix command: `<command>`
- pre-fix result: `<RED or ALREADY_SATISFIED>`
- regression test: `<test name/path>`

Do not claim an expected-red result that was not actually observed. Existing passing behavior is recorded as already satisfied.

## Focused gates

Record exact command, result, duration/counts where available:
- `go test -count=1 ./internal/runadmission`
- `go test -count=1 ./internal/serviceapi`
- `go test -count=1 ./internal/workflowauthoritypg`
- `go test -count=1 ./internal/governance`
- `go test -count=1 ./cmd/abcp`
- `go test -count=1 -race ./internal/runadmission ./internal/serviceapi ./internal/workflowauthoritypg`
- PostgreSQL integration test: `<PASS / SKIPPED because ABCP_WORKFLOW_AUTHORITY_PG_TEST_DSN unavailable>`
## Broad gates

- `go test -count=1 ./...`: `<result>`
- `go test -count=1 -race ./...`: `<result>`
- `go vet ./...`: `<result>`
- `GOOS=darwin GOARCH=amd64 go build ./cmd/abcp`: `<result>`
- `GOOS=windows GOARCH=amd64 go build ./cmd/abcp`: `<result>`
- `git diff --check`: `<result>`

Any classified pre-existing/flaky exception:
`<none or exact reproduction/classification>`

## Contract/API hashes

At the executable tested SHA:
- `internal/serviceapi/dto.go` SHA-256: `<hash>`
- `docs/architecture/ABCP-EP006-RC-INTEGRATION-ARCHITECTURE.md` SHA-256: `<hash>`
- `docs/execution-packs/ABCP-RC-P01-RUN-ADMISSION.execution-pack.md` SHA-256: `<hash>`
- relevant generated/schema hashes: `<hashes or N/A>`

## Review 1

- Review result: `<REVIEW_DONE / accepted findings>`
- Accepted findings: `<none or exact list>`
- OUT_OF_SCOPE_REVIEW suggestions: `<none or exact list>`

If Review 1 made executable corrections:
- correction executable SHA: `<40-char SHA>`
- regression tests added: `<list>`
- gates rerun: `<commands/results>`
## Review 2 — only if Review 1 corrected code

- Authorized: `<yes/no>`
- Final result: `<REVIEW_DONE / TASK_FAILED>`
- Remaining accepted findings: `<none or exact list>`

No third review is authorized.

## Final scope/evidence checks

- allowed-path check: `<PASS>`
- changed files <= wrapper ceiling: `<PASS>`
- changed lines <= wrapper ceiling: `<PASS>`
- worktree clean: `<PASS>`
- branch pushed: `<PASS>`
- Draft PR targets `main`: `<PASS>`
- executable SHA named by evidence: `<PASS>`
- evidence-only successor explicitly distinguished: `<PASS>`
- unresolved scope-crossing finding: `<none>`

## Completion claim

Emit only if all pack criteria are satisfied:

`ABCP_RC_P01_RUN_ADMISSION_COMPLETE`

This evidence document does not authorize merge or live infrastructure mutation.

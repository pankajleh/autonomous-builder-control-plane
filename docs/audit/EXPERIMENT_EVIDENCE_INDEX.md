# Experiment Evidence Index

This index records the behavior-lab evidence used to make the architecture decision. Evidence directories live outside product repositories on the audited Ubuntu host.

## Canonical tool authority

```text
Ralphex source:
  /home/devagent/ralphex-behavior-lab/tools/ralphex
  SHA: 319e30618352a1b43e4be1b8a894c6c05e6d5fa8

Ralphex binary:
  /home/devagent/ralphex-behavior-lab/tools/ralphex/.bin/ralphex
  SHA256: 9ad47b083aaaf34eed4b35ca82f3c92d1a3b7641921be0ea606cbd1cd1c3adac
```

## Experiment runs

### EXP-00 — baseline autonomous success

- Result: PASS
- Run: `EXP-00-20260905-131640`
- Target: `/home/devagent/ralphex-behavior-lab/targets/exp00-success`
- Evidence: `/home/devagent/ralphex-behavior-lab/evidence/EXP-00-20260905-131640`

### EXP-01 — SMTP success notification

- Result: PASS
- Run: `EXP-01-20260905-134621`

### EXP-02 — terminal failure

- Result: PASS
- Run: `EXP-02-20260905-144813`
- Evidence: `/home/devagent/ralphex-behavior-lab/evidence/EXP-02-20260905-144813`

### EXP-03 — retry recovery

- Result: PASS
- Run: `EXP-03-20260905-154309`
- Evidence: `/home/devagent/ralphex-behavior-lab/evidence/EXP-03-20260905-154309`

### EXP-04 — hard crash and resume

- Result: PASS
- Valid run: `EXP-04-20260906-013103`
- Evidence: `/home/devagent/ralphex-behavior-lab/evidence/EXP-04-20260906-013103`
- Earlier process-group harness attempts were INVALID / NOT SCORED.

### EXP-05 — human decision / unavailable authority

- Result: PASS
- Run: `EXP-05-20260906-014817`
- Evidence: `/home/devagent/ralphex-behavior-lab/evidence/EXP-05-20260906-014817`

### EXP-06 — Codex capacity interruption

- Result: PASS
- Run: `EXP-06-20260906-022520`
- Evidence: `/home/devagent/ralphex-behavior-lab/evidence/EXP-06-20260906-022520`

### EXP-07 — parallel worktrees

- Result: PASS
- Valid v2 run: `EXP-07-20260906-030309`
- Evidence: `/home/devagent/ralphex-behavior-lab/evidence/EXP-07-20260906-030309`
- v1 harness was not used as the final proof because its timing analyzer and `.ralphex/.gitignore` setup were flawed.

### EXP-08 — cross-plan integration conflict

- Result: PASS
- Native run: `EXP-08-20260906-031837`
- Evidence: `/home/devagent/ralphex-behavior-lab/evidence/EXP-08-20260906-031837`
- Exact-SHA integration verifier: `EXP-08-PART2-V2-20260906-033638`
- Verifier evidence: `/home/devagent/ralphex-behavior-lab/evidence/EXP-08-PART2-V2-20260906-033638`
- Final machine result: `EXPECTED_CROSS_PLAN_TEXTUAL_AND_SEMANTIC_CONFLICT_PROVEN`

### EXP-09B — Codex-authored candidate review comparison

- Result: PASS
- Run: `EXP-09B-20260906-060205`
- Evidence: `/home/devagent/ralphex-behavior-lab/evidence/EXP-09B-20260906-060205`
- Codex implementation SHA: `585698bae991753463bb4a75bd22271f5c197d64`
- Seeded specimen SHA: `69cbf6fe9ea8a16a7e7b8257681df61e38405b99`
- Same-model Codex review: 3/3 seeded defects fixed
- Cross-model Claude review: 3/3 seeded defects fixed
- Result: `EQUAL_SEEDED_DEFECT_FIX_COUNT`

Earlier EXP-09B attempts that stopped on missing Claude or a flawed harness detector were INVALID / NOT SCORED.

### EXP-09A — Claude-authored candidate review comparison

- Result: PASS
- Run: `EXP-09A-20260906-062257`
- Evidence: `/home/devagent/ralphex-behavior-lab/evidence/EXP-09A-20260906-062257`
- Claude implementation SHA: `ba9d15987d55b32324089e9b689750e30233f1fd`
- Seeded specimen SHA: `fd7556e0c5f2af482bc1ad3af8e60e9c3f16ec2a`
- Same-model Claude review: 3/3 seeded defects fixed
- Cross-model Codex review: 3/3 seeded defects fixed
- Result: `EQUAL_SEEDED_DEFECT_FIX_COUNT`

## Evidence retention rule

The future control plane should preserve equivalent run evidence under immutable run IDs rather than relying on plan-name-derived dashboard sessions or mutable progress-file history.

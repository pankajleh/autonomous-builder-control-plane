# G0-B Review Contract

Status: **BINDING**

Review authority:
- Repo C CAPSULE-004 SHA-256 `ce15cc729f43afc8927acc5389a1a675c69d0cbe4f30501955de8655a5116066`
- Correction-02 frozen authority `8d90a5ff506bd965bfc9e533d2be0b53c2adf73d`
- this G0-B execution pack and exact implementation base

Review 1 reads the complete base-to-head diff and reports only concrete Critical/Major frozen-authority, correctness, security, replay/idempotency, compatibility or path-ceiling defects.

If Review 1 is clean, emit `<<<RALPHEX:REVIEW_DONE>>>`.

If Review 1 causes accepted corrections, one Review 2 may verify only those fixes and remaining Critical/Major violations. There is no Review 3.

Out of scope: later Repo C tooling, product feature work, provider/Ralphex redesign, retry/recovery redesign, broad refactor/style cleanup, live infrastructure, autonomous merge/publication.

A required fix outside frozen scope is `<<<RALPHEX:TASK_FAILED>>>` and a ROADBLOCK, not permission to widen authority.

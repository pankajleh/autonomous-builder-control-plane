# ABCP-RC-P01 Review Contract

Status: **BINDING REVIEW AUTHORITY**
Date: 2026-09-19

## 1. Governing rule

Review verifies the bounded implementation against:
1. `ABCP-EP006-RC-INTEGRATION-ARCHITECTURE.md`;
2. `ABCP-RC-P01-RUN-ADMISSION.execution-pack.md`;
3. the exact implementation base SHA and allowed-path boundary.

Review does not design the product, reopen accepted architecture, discover later scope or optimize unrelated code.

## 2. Maximum review sequence

### Review 1 — comprehensive bounded review
Read the complete base-to-head diff and the complete governing authority.

If there are zero accepted findings, emit:
`<<<RALPHEX:REVIEW_DONE>>>`

If accepted findings exist, fix only those findings, add/regress tests, rerun affected required gates, commit executable corrections first and update evidence second. Review 1 then ends without `REVIEW_DONE`.

### Review 2 — final verification only
Review 2 is authorized only when Review 1 caused accepted corrections.

It checks:
- whether accepted Review-1 findings are actually fixed;
- whether those corrections introduced a new critical/major in-scope defect;
- whether a critical/major frozen-authority violation remains.

If clean, emit `<<<RALPHEX:REVIEW_DONE>>>`.

If a real remaining defect cannot be fixed inside frozen scope, emit `<<<RALPHEX:TASK_FAILED>>>` and identify the exact authority gap.

**There is no Review 3.**
## 3. ACCEPTED finding classes

A finding is actionable only when it demonstrates at least one of:

1. a concrete MUST/DO NOT/acceptance violation in the frozen architecture or P01 execution pack;
2. a reproducible correctness defect in an explicitly changed/in-scope path;
3. a reproducible security/privacy/replay/idempotency/durability/compatibility defect in an explicitly changed/in-scope path;
4. a false or stale evidence claim that binds required gates to the wrong executable SHA;
5. implementation mutation outside the allowed-path boundary.

The reviewer must state the violated frozen rule or the reproducible defect. Preference is not evidence.

## 4. Mandatory OUT_OF_SCOPE_REVIEW classes

Discard and do not fix in P01:
- later-pack features, including Repo C adapter/UI and human-decision continuation;
- general retry/recovery;
- provider or Ralphex redesign;
- alternative architecture or abstraction preference;
- broad refactor/simplification/style suggestions without correctness consequence;
- unrelated pre-existing defects;
- adjacent code cleanup discovered while reading;
- live infrastructure changes;
- any new assurance-capsule/A-B-C/V4 governance work.

A review comment cannot become actionable merely because it is labelled critical/major. It must satisfy Section 3 and the frozen authority chain.

## 5. Scope-crossing real defects

If Review 1 or Review 2 proves a real P01 defect whose required fix needs an excluded path/domain or a new architecture/product decision:
- do not broaden scope;
- do not implement the proposed wider fix;
- stop the bounded run;
- return `<<<RALPHEX:TASK_FAILED>>>`;
- identify the exact violated requirement and the smallest missing authority/addendum required.

Human/product authority decides whether a later micro-addendum/new pack exists.

## 6. Evidence discipline

Executable corrections are committed before evidence updates. Evidence must name the latest tested executable SHA.

An evidence-only successor must explicitly say it changes no executable behavior.

## 7. Review completion

The authorized review sequence is complete when:
- Review 1 is clean; or
- Review 1 accepted corrections, those corrections were implemented/tested, and Review 2 is clean.

After that, stop reviewing. Prepare the exact state for the human merge gate. No autonomous merge.

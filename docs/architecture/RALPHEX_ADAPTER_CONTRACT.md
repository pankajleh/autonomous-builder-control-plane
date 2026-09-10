# Ralphex Adapter Contract

## Purpose

The adapter is the structural process boundary between controller authority and a pinned Ralphex binary. For activated A/B/C autonomous development, only a B V3 capsule can admit Ralphex. Earlier prose allowing V2 to authorize that workflow is superseded. Historical V1/V2 evidence and separately governed deployment/recovery/maintenance behavior are unchanged.

## Pinned inputs and capability

A governed invocation binds the exact binary hash and source identity, repository and plan, mode, executor/model/effort, isolated config, candidate/base ref, rate-limit policy, per-invocation bounds, cumulative B counters, and Linux containment capability.

`RalphexCapabilityV1` must prove exact support for:

- `--max-iterations`, `--session-timeout`, `--idle-timeout`, `--skip-finalize`, and `--base-ref`;
- selected executor/model/effort flags and an isolated config directory;
- a controller-visible tasks-only, review-only, or stop-before-each-fix handoff boundary;
- the Linux cgroup-v2 containment handoff.

An asserted feature, repository-local `.ralphex` override, or success string is not capability evidence. Any missing proof returns `EXECUTION_BOUNDS_INVALID` before spawn.

## Structured invocation and bounds

Arguments are emitted as a structured string array; no shell concatenation is permitted. A B invocation includes all native per-process controls and always emits `--skip-finalize`. Codex task and review effort are exactly `xhigh`.

The initial hard ceilings are 10 iterations, 90 minutes per session, 45 minutes idle, three hours per invocation wall clock, `finalize=false`, and exactly one incomplete executable Task/Iteration section. Each capsule can tighten them.

B also carries finite positive maxima for Ralphex invocations, review reports, mutation leases, total fix batches, aggregate wall clock, changed files, and changed bytes. Counters and elapsed time are durable workflow state. Starting another process never resets them; reaching a ceiling is bounded non-success.

Native full review/fix is admitted only when Ralphex stops before every fix so the controller can validate the report and issue a one-use lease. Otherwise the controller uses separate bounded review and fix invocations. Tasks-only cannot silently enter review/fix.

## Linux containment

V3 execution requires a controller-created cgroup-v2 scope, or a separately proven equivalent primitive. Process-group isolation alone is insufficient. The contained runner proves the scope identity, verifies child membership, gracefully terminates on expiry, applies a bounded forced scope kill if needed, and proves `cgroup.procs` empty before returning. Creation, membership, teardown, or evidence failure is `EXECUTION_BOUNDS_INVALID`.

The supervisor wall-clock timeout bounds one invocation. Aggregate wall time includes every B invocation, controller handoff, and rate-limit wait.

## Results

The adapter records structured argv, pinned identities, scope/process identity, timestamps, exit/signal, stdout/stderr refs, effective bounds, and verified containment teardown. Ralphex completion maps at most to implementation completion pending controller validation. Model or Ralphex success text never creates checkpoint, acceptance, publication, or merge authority.

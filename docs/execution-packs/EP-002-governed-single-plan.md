# EP-002 — Governed Single-Plan Execution

**Status:** Ready after EP-001 review  
**Goal:** Launch one real Ralphex plan under control-plane authority and independently accept/reject its branch.

## Scope

### 1. Authority manifest

Introduce a typed manifest containing:

- `run_id`
- repository canonical path
- repository identity/remotes
- default branch
- start SHA
- plan path + SHA256
- Ralphex binary path + SHA256
- expected Ralphex source SHA metadata
- mode
- executor/model/effort policy
- worktree policy
- acceptance commands
- policy version

The manifest becomes immutable after authority validation.

### 2. Evidence store

Add an artifact writer that:

- creates a run-specific evidence directory;
- writes stdout/stderr and structured metadata files;
- computes SHA256 for each artifact;
- returns `ledger.EvidenceRef` values.

### 3. Process supervisor

Launch Ralphex directly with `exec.CommandContext`, not via shell string.

Record:

- PID;
- process-group identity on Linux;
- start/end timestamps;
- exit code or terminating signal;
- exact argv;
- stdout/stderr evidence refs.

### 4. Governed Ralphex runner

Map process lifecycle to ledger events and domain states:

```text
AUTHORITY_VALIDATED
→ EXECUTION_STARTING
→ IMPLEMENTING
→ IMPLEMENTATION_COMPLETED
```

A non-zero terminal outcome must not become `IMPLEMENTATION_COMPLETED`.

### 5. Deterministic branch acceptance

After Ralphex success, independently execute configured acceptance commands.

Map:

```text
IMPLEMENTATION_COMPLETED
→ BRANCH_ACCEPTANCE_PENDING
→ BRANCH_ACCEPTED
```

or blocker/failure state.

Acceptance must capture command argv, cwd, timestamps, exit status, stdout/stderr, and final Git SHA.

## Required tests

- authority manifest rejects missing plan or binary identity;
- manifest hashing stable for same content;
- evidence writer hashes bytes correctly;
- supervisor captures exit 0;
- supervisor captures non-zero exit;
- cancellation records signal/exit semantics;
- acceptance PASS advances to `BRANCH_ACCEPTED`;
- acceptance failure does not advance;
- integration/READY_FOR_MERGE remains unreachable in EP-002.

## Live acceptance

Use a disposable toy Git repository and a harmless fake Ralphex executable first. After deterministic process/evidence behavior passes, run one real pinned Ralphex plan on the Ubuntu behavior-lab host.

## Non-goals

- stale-worktree recovery;
- human-decision resume;
- multiple concurrent plans;
- integration controller;
- GitHub PR/merge;
- service API.

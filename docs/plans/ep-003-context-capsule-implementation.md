# EP-003 Addendum — Context Capsule Implementation

## Context Authority

Roadmap phase: Phase 2 — Recovery and blocker control.
Parent goal: make fresh-task execution durable and bounded without relying on chat history.
Base implementation: EP-003 head `308daebb91e49cff762c0615d652937517795dcc`.
Canonical policy: `docs/architecture/CONTEXT_AUTHORITY_AND_CAPSULE_POLICY.md`.
Architecture sources: roadmap, state machine, event/provenance model, Ralphex adapter contract, decision/acceptance policy.

Hard boundary: implement only the minimum context-capsule machinery needed for fresh tasks. Do not build semantic retrieval, embeddings, dashboard, scheduler, GitHub lifecycle, object storage, or retention workers.

## Goal

Provide a deterministic compact context capsule that fresh Ralphex/Codex/Claude tasks can read first, and bind that capsule to governed execution authority.

## Required behavior

- Capsule records roadmap phase, EP, task, base SHA, invariants, non-goals, predecessor outcomes, and explicit source documents.
- Every source document is identified by repository-relative path plus SHA256.
- Capsule generation is deterministic and bounded; no hidden model-generated retrieval.
- Verification fails closed on source drift, path escape/symlink, wrong base SHA, malformed capsule, or hash mismatch.
- Governed run authority can bind a capsule path/hash and verify it before Ralphex launch.
### Task 1: Implement deterministic context capsules

- [ ] Add `internal/context` with typed capsule/spec/source/outcome models and canonical JSON hashing.
- [ ] Add builder/validator that resolves only repository-contained regular files, hashes exact bytes, verifies repository/base SHA, rejects duplicates/path escape/symlinks, and enforces conservative size/count bounds.
- [ ] Keep capsule content compact: explicit invariants/non-goals/outcomes plus source references; do not copy entire source documents into the capsule.
- [ ] Add CLI commands to build and verify capsules using explicit input/output paths and structured arguments only.
- [ ] Extend governed authority with an optional context-capsule path + SHA256 binding; when present, validate it before execution. Preserve backward compatibility for pre-policy manifests.
- [ ] Add tests for deterministic generation, changed source, wrong base SHA, traversal/symlink, duplicate source, oversize capsule, authority hash binding, and CLI build/verify.
- [ ] Document the operating rule: starting EP-004, every executable plan must point fresh tasks to its verified capsule before task work begins.
- [ ] Run `gofmt -w cmd internal`, `go test ./...`, `go test -race ./...`, `go vet ./...`, `make smoke`, and `git diff --check`.
- [ ] Mark this task complete and commit only this bounded addendum.

## Acceptance

A fresh task can receive one small verified capsule plus its executable task, independently verify the capsule against the exact repository/base SHA, and follow durable project authority without loading full project history.

No automatic semantic context selection is part of this addendum; future selection may optimize source choice but cannot weaken deterministic source/hash authority.
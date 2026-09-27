# BP-01 sidecar resume overlap and worktree startup

Date: 2026-09-28

Exact base: `fd39b1a32e0f741e35d48013cf7a337aa7628e17`

## Problem

With the marker diagnostics from `fd39b1a` live, the third PX-07 proof run (`admission-2ed34354…`) named both defects that make a healthy run's checkpoints ineligible for Preview Runtime:

1. `provider-integrity step=ordering`. Reproduced directly against the pinned sidecar for an active and a completed session: `Last-Event-ID` one below the newest event resumes exactly, but a `Last-Event-ID` equal to the newest event is treated as unknown, and the whole session is replayed from a live-only ID 0 frame that never appears in history. The collector resumes from its newest committed event, so the first quiet batch after catching up re-read history as reordered and permanently failed provider collection. This is the integrity failure in all three proof runs.
2. `binding-unavailable step=worktree/branch-missing`. An activity subscriber that attaches within a second of admission resolves the binding before the provider has created the governed worktree branch, and a durable UNKNOWN marker is appended.

## Bounded correction

- Resume one event early: send `Last-Event-ID = last - 1`, or no header when only event 1 is committed, so the sidecar always takes its exact resume path.
- Events re-sent at or before the committed position must already be committed with an identical source digest and are not appended again. A never-committed frame (such as the live-only ID 0) or a changed payload there fails closed at `resume-overlap`. IDs must strictly increase within a batch (`ordering`).
- Quiet batches now reach the silent-gap check routinely, so it counts only progress growth observed before the bounded read. Bytes appended after the read ends are delivered by the next batch, not missing.
- A missing governed worktree branch is pending, with no marker, only while no provider proof exists and for 30 seconds after the run's first ledger fact. Every other binding failure, a missing worktree after that window, and a missing worktree after a provider proof exists remain immediate fail-closed markers.
- Run state, acceptance, admission, preview trust rules, marker contents and the activity schema are unchanged.

## Acceptance

```text
go test ./internal/activity ./internal/preview ./internal/serviceapi ./cmd/abcp
go test -race ./internal/activity ./internal/preview ./internal/serviceapi ./cmd/abcp
go vet ./internal/activity ./internal/preview ./internal/serviceapi ./cmd/abcp
go test ./...
git diff --check
```

New tests fail on the pre-correction behaviour: reverting the resume header, the silent-gap growth measure or the startup deferral each fails its test. `go test ./...` still reports the `internal/mergelifecycle` failures that exist on the unmodified base.

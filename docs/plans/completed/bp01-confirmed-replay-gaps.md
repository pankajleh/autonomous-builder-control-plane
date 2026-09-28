# BP-01: confirm provider replay gaps; refuse releasing running builds first

Date: 2026-09-28

Exact base: `7ac0cffef3887ba8d46bd1390b39c2033d048940`

Both defects were found by the live task-closure proof on Repo B `7ac0cff` with Repo C P0060 (run `admission-6f80645d…`).

## False replay-gap marker at run start

About three seconds after submission, the collector recorded `provider-replay-gap`, and the marker made every checkpoint of the run ineligible for preview. A full replay of the finished session afterwards was contiguous: IDs 1–67, with no ID 0 frame. The gap was therefore in the live stream only. The live Ralphex sidecar can deliver an event before an earlier one that it is still persisting.

The collector now confirms a gap before marking it:
- **First sighting.** It keeps every event before the gap, stops at the gap without advancing, and remembers the gap (run, provider generation, last event and received event).
- **Next batch.** It resumes from the last contiguous event, which makes the sidecar re-read the session. If the missing event is there, nothing is marked.
- **Second sighting.** Only the same gap seen again at the same position is marked, exactly as before.

Gaps are logged by event number only, with no provider text:

```text
abcp activity replay gap at=… run=… after=<n|none> got=<n> confirmed=<bool>
```

## Releasing a running build

`Sweeper.Release` (#53) inspected the retained flag and the worktree before checking whether the run had finished. A running build whose worktree did not exist yet would have answered `NO_WORKTREE` or `NOT_RETAINED`, and the product would have treated it as released. Live, the close attempt during the build hit a transient worktree state and returned `503` instead of `409`.

The finished check now comes first. An unfinished run is always refused with `run_not_finished`, and Repo C maps that to `409 TASK_HAS_ACTIVE_BUILD`.

## Acceptance

```text
go test ./internal/activity ./internal/eviction ./internal/serviceapi ./internal/preview ./cmd/abcp
go test -race ./internal/activity ./internal/eviction
go vet ./...
```

New and changed tests:
- New `TestLiveReplayGapResolvedByResumeRecordsNoMarker`: a gap resolved by the resumed read records no marker.
- `TestProviderReplayGapDedupeAndLastEventID`: a gap is marked only on its second sighting.
- `TestReplayRejectsChangedSourceOrdinalAssociation`: confirms its start gap with a second read before the conflicting replay.
- `TestReleaseRefusesUnfinishedRunsAndPolicyReasons`: a running build is refused even before its worktree exists, or when it retains none.

# BP-01: provider gap markers that were not gaps (Claude Code builds)

Date: 2026-09-28

Exact base: `7f76c6615d1d29179229cffde7e0e45a0681a7b0`

Found in Repo C's Claude Code engine trial (Repo C A1b, P0089). The owner asked for Claude Code as a second build engine, next to Codex.

## Observation

A product build run by ralphex with executor `claude` (run `admission-c430bee7…`) passed acceptance. Its share link and code worked. But every checkpoint of the run was refused for preview (`preview_ineligible`), because the activity stream carried three provider integrity markers:

- `provider-replay-gap` at the start: `after=none got=0`, seen twice and so confirmed.
- `provider-silent-gap` at 20:20:08 and 20:23:20.

The preview resolver treats any such marker as invalidating the run's checkpoints, which is intended. A Codex run of the same description on the same controller had no markers, and its preview opened.

## Causes

**Silent gap.** Both markers fall exactly where ralphex hands a Claude build's review to Codex. It appends a blank line and a `--- codex iteration N ---` header, and Codex then takes about 9 seconds to print its first line. Ralphex's progress parser behaves as follows (`pkg/web/tail.go`, `session_progress.go`, pinned source `2275e23`):
- it skips blank lines;
- it holds a section header back and publishes it together with the first timestamped line after it;
- a second header releases the first one.

So the file grew, correctly, with no event. The collector saw growth with no fresh events and marked a gap. Codex-only runs never write a header that sits unpublished for long.

**Replay gap at the start.** go-sse's `FiniteReplayer` numbers events from 0. Ralphex replays "everything" by asking for events after ID `0`, so a replay starts at 1, which is what the collector expects. A reader connected before ralphex has published anything receives event 0 live instead. A Claude build takes longer to print its first line, so both of the run's first bounded reads connected early and got 0.

Neither case is lost activity.

## Change

**Silent gap** (`progressProof`, `collectBatch`). The proof now computes, without persisting it, whether all growth since the previous proof lies in the file's trailing stretch that owes no event yet.
- `pendingFrom` walks back from the end of the file over blank lines and at most one section header, the newest. The stretch ends at the first other line.
- It fails toward marking in each of these cases: a partial last line, whitespace-only lines, a second header, or a tail longer than the 16 KiB window all count as owed.
- The flag's zero value keeps today's check.

A batch with no fresh events and growth wholly inside that stretch is not a silent gap. The next batch delivers the header with its first line.

**Replay gap** (`collectBatch`). With no prior source event, the expected first ID stays 1, except in a run's very first batch, before any progress proof exists. There ID 0 is the provider's genuine first event. A later restart from nothing that begins at 0 is still a gap: it stops on the first sighting and is marked on the second, as before.

Nothing else changes:
- replay-consistency checks;
- the resume-one-early rule;
- gap confirmation;
- the preview resolver's rule that any marker invalidates a run.

## Tests

- `TestSilentGapCountsOnlyGrowthBeforeTheRead` gains these cases:
  - no marker: a section header not yet published, blank lines only, and a CRLF header;
  - a marker: two headers, a line before a header, a partial last line, and a whitespace-only line.
- `TestPendingFromReadsOnlyABoundedTail`: the scanner's result for each tail shape, including one past the window.
- `TestOnlyARunsFirstBatchMayStartAtEventZero`: a first batch of `0, 1, 2` is recorded with no marker. After progress is proven, a restart beginning at 0 is a first-sighting gap, not accepted.
- With the two conditions disabled, the new "no marker" cases and the event-0 test fail.
- `go test ./...` passes.

## Deployment

Deployed with the next announced controller restart (Repo C A2.1). Afterwards, the Claude Code trial is repeated before Repo C offers `claude-code` as a build engine.

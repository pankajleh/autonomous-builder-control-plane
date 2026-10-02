# A review pass that fixes something should be reviewed again, not fail the run

Date: 2026-10-02 · Status: proposal (no code in this repository changes)

## What happened

Run `admission-d1cd2abf9011dca47056ed6d64ad27eb2fea1386896af8613cde2122f356b9bd` (a small test app built during the
parallel-builds check, Repo B #73) failed with:

```
error: runner: first review: first review pass emitted REVIEW_DONE after changing HEAD
```

The first review pass found something, committed a fix, and then signalled `REVIEW_DONE`. The pinned Ralphex
(`ralphex-v1.7.0-abcp`, source `pankajleh/ralphex-governance` `abcp/v1.7.0` @ `2275e23`) treats a HEAD change before
`REVIEW_DONE` as a broken contract and ends the run. For the person, an app that was built and reviewed ends as "the
build couldn't finish", and they have to build it again and pay for the build again.

The agent's behaviour is reasonable: it was asked to review, found a problem, fixed it and said it was done. What is
missing is a review of the fix.

## Proposal (a governance patch to Ralphex, then a re-pin here)

1. **A pass that changed HEAD and says `REVIEW_DONE` is not done.** The runner records the pass as having fixed
   something and runs another review pass on the new HEAD, as it does when a pass fixes something without a signal.
2. **Bounded.** At most two such extra passes. If a pass still changes HEAD at the limit, the run ends as it does today
   (failed, with today's message), so a review loop cannot run on or spend without end.
3. **Visible.** Each extra pass and its commit appear in the run's events, as review passes do now; ABCP already maps the
   review signals (`internal/activity/event.go`).
4. **Unchanged:** a pass that leaves HEAD alone and says `REVIEW_DONE` ends the review as today; acceptance, the
   validation commands and the merge rules do not change.

## Tests (in `ralphex-governance`)

- Pass 1 commits and says `REVIEW_DONE`; pass 2 changes nothing and says `REVIEW_DONE`: the run goes on to acceptance.
- Three passes in a row commit and say `REVIEW_DONE`: the run fails at the limit with today's message.
- A pass that changes nothing and says `REVIEW_DONE`: as today.

## Then, here

Build the patched release, re-pin it (`deploy/local-integration/README.md`, upgrade procedure), record the new pin in
`docs/CURRENT_STATE.md` and `docs/AUDIT_INDEX.md`, and restart ABCP in a window announced as before. Prove it with a
build whose first review pass commits a fix.

The governance repository is not reachable from the session that wrote this note, so the patch waits for it.

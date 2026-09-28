# BP-01: request-index shards keep their authority on their own directory

Date: 2026-09-28

Exact base: `fff4c12cded86c10ee4396310a9a65bacb7addb3`

Found while building real products end to end, which the user asked for on 2026-09-28 ("finish the products until they are served").

## Observation

The live controller stopped starting after a routine restart. Every `abcp serve` exited with `open service action journal safely`, and the builder was down for about 12 minutes.

- The service root inode carried seven extended attributes, 3 524 bytes of names and values. ext4 gives one inode about 4 KiB of extended attributes.
- Two of those attributes were request-index shard authorities (`a0`, `ea`), about 600 bytes each. A request-index has 256 shards, and until now each established shard kept its authority on the service root.
- One API cancel request had hashed to a third shard, `5e`. Its root record was created in the `initializing` state (130 bytes), but the next step needs about 650 bytes and failed with `ENOSPC`. The cancel returned 500.
- From then on, every journal open found the `initializing` record, tried to finish it and failed the same way. Nothing in `actions/` referred to the shard, and its directory was empty.

In short, the journal could hold two or three request shards on ext4. The next cancel, decision or retry in a new shard would stop the controller for good.

## Recovery on the host

Nothing was deleted.

- The `5e` root record's value was saved to `runtime/quarantine-20260928-actions/root-xattr-shard-5e-generation-v1.json` before the record was removed.
- The empty `actions/request-index/5e` directory was moved to the same folder. It had never held a request.
- A one-off diagnostic build that logged each integrity return located the failing write. After the recovery, `actioncontrol.Open` succeeded and the controller started on `2bd85f7`.

## Change

**Where a shard's authority lives.** An established shard keeps its generation authority as an extended attribute on its own directory inode (`user.abcp.actioncontrol.request-shard-generation-v1`). Each inode has its own extended-attribute space, so the number of shards no longer draws on the service root.

- The authority's content and canonical encoding are unchanged. It is still bound to the service root's device and inode, and it still names the shard directory, its identity anchor and its issuance history by device and inode. A copied or restored shard therefore still fails verification.
- Issuance checkpoints now advance on the shard directory.

**Registry.** The request-index directory carries a registry of established shards (`user.abcp.actioncontrol.request-shard-registry-v1`), a 256-bit set bound to the service root. A shard is established exactly when it is registered.

- A registered shard whose directory, identity or authority is missing fails closed.
- Shard entries that exist without a registration or a root record also fail closed.
- So losing a whole shard is still detected. That was the one property the root records gave that a per-shard record alone would not.

**The root during creation.** The service root holds a shard record only while that shard is being created. Creation runs under the root generation lock, so there is at most one such record.

At the end of creation, three steps each become durable before the next:

1. the authority is written to the shard directory;
2. the shard is registered;
3. the root record is removed.

A crash at any point leaves the root record in place. The next open then finishes the same steps, and each step accepts what is already durable.

**Journals written before this change.** A root record in the `established` state is moved the same way on the next open that sees it, under the root generation lock. The live journal's `a0` and `ea` move on the first start of this build.

**Diagnostics.**
- `abcp serve` and `abcp run` now print the underlying error when the action journal cannot be opened.
- An extended-attribute write refused for lack of space is reported as `extended attribute space exhausted`. It still counts as an integrity failure.

## Tests

`internal/actioncontrol/journal_shard_capacity_linux_test.go`, run on ext4:

- 32 requests in 32 different shards, then a reopen and a replay of every receipt. No shard record remains on the service root. On the previous code this failed at the fourth shard with the live error.
- Root-held (older) shard authorities move onto their shards, replay unchanged, and keep issuing.
- A move interrupted after the shard record was written is finished at the next open.
- The live incident's state (an `initializing` root record and an empty shard directory) is completed at open, and the shard then issues.
- Fail closed when:
  - a registered shard has lost its record;
  - a shard is present but not registered;
  - root and shard records disagree.

The ten existing tests that read a shard authority now read it from the shard directory. Where the path may have been replaced, they read the descriptor the trusting journal pinned. The full `internal/actioncontrol` package passes.

## Deploy

Deploy only when no `abcp run` is executing, queued ones included.

A run process started by the previous binary pinned the root-held shard records when it opened the journal. Once the first new open moves them, that process would fail closed at its next journal operation.

1. Wait until no `abcp run` process is alive for the service root.
2. Stop `abcp serve`, start this build, and confirm that the root no longer carries `journal-request-shard-*` records.

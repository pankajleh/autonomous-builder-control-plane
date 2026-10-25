# Runs that change exact words with no builder (Repo C BF-01d)

Status: **built** (this PR), not yet live. Repo C's design note `BF01D_QUICK_EDIT.md` §4.5 (the owner, 2026-10-25: "all as
recommended, go ahead with BF-01d except 4, build for everyone"). A person picks words on their app's preview and types
new ones; Repo C finds them exactly once in the version's files and asks ABCP for a run that changes only them, with no
builder and no model, and runs every check as for any build.

## The request

`POST /v1/runs` takes an optional `exact_edit`, beside the usual authorization, version, manifest digest and repository
base (the profile repository's HEAD, as for every run):

```json
"exact_edit": {
  "source_sha": "<the commit of the version being changed>",
  "replacements": [{"path": "public/index.html", "base_blob": "<the file's blob at source_sha>", "find": "Book now", "replace": "Reserve a table"}]
}
```

One to ten replacements, each in a different file; `find` 1 to 1,024 bytes and `replace` at most 1,024, both UTF-8 without
NUL and different; a relative path with no empty, `.` or `..` segment, no backslash or control character, never under
`.git` or `.ralphex` and never in a `migrations` or `migration` folder. An exact edit names no template and no executor
choice. Omitted, every request keeps its digest.

**Refused before anything is written** (`422 exact_edit_not_accepted`): the source commit is not in the repository or
no run's branch (`refs/heads/abcp/*`) holds it; a file is missing there, is not a regular file, or its blob is not
`base_blob`; `find` is not found, or is found more than once (overlapping occurrences count); a file is over 2 MiB.
Admission only reads Git for this (`run.CheckExactEdit`).

## The run

`manifest.exact_edit` binds the run to the edit; the authority refuses one with a template copy, governance, a read-only
review or no worktree. The run validates its authority as every run does, then, instead of starting the builder:

1. **The copy commit**: the source commit's tree on the run's start, as a change build's first commit copies the version
   before (Repo C design note `A2B_A_CHANGE_STAYS_ON_THE_SAME_APP.md`).
2. **The edit commit**: the replacements, made from Git objects alone (`hash-object`, a private index, `commit-tree`), so
   no checkout or index of the repository is touched. Both are signed `Autobuild <builder@autobuild.invalid>`.
3. **`refs/heads/abcp/<run>`** is created only if it does not exist (`update-ref` with an empty old value).
4. **The check in place of a review**: the branch points at the edit commit; each commit has the one expected parent; the
   copy holds exactly the source tree; and the edit commit changes exactly the declared files, each from its blob at the
   source to the declared content, mode unchanged, nothing else.
5. **The worktree**: the branch is checked out where the builder keeps one (`.ralphex/worktrees/abcp/<run>`), so the
   activity's checkpoint, the preview and the worktree's eviction find it as for any build; kept when the authority
   retains worktrees (every product profile does), removed otherwise.

The run's lease and slots are freed once the worktree exists. The ledger reads `EXECUTION_STARTING` (`exact_edit: true`),
`IMPLEMENTING` and `IMPLEMENTATION_COMPLETED` from the source `exact-edit`, with the evidence `exact-edit.json` (the
source, copy and head commits, the branch, the worktree and each file's old and new blob), then the branch acceptance
every run has: the acceptance commands on a checkout of the head, ending `BRANCH_ACCEPTED` or `FAILED` like any build.
A refused edit ends `FAILED` from `IMPLEMENTING` with the reason and writes no branch. An exact edit is never resumed.

The activity reads no builder detail for such a run (`Scope.ExactEdit`), so it gets no "detail unavailable" marker.

## Tests

`internal/run/exactedit_test.go`: a run accepted with no builder (two commits, only the declared file changed, the
default checkout unmoved, the check run on the edit); a failing check stops it; a changed file, words found twice and
words not found are each refused with no branch written; `CheckExactEdit`'s refusals (a commit no run made, one not in
the repository, the start, overlapping words, a folder, a missing file, a migration, a glob read literally) change no
ref; an undeclared change beside the edit is refused; the worktree is where the builder keeps one, clean, on the branch.
`internal/runadmission/exactedit_linux_test.go`: admission refuses each case with `exact_edit_not_accepted` and starts
nothing, binds the edit into the run's manifest, replays the same request and refuses the same request id with other
words. Request validation, the authority's checks and the 422 mapping have their own cases.

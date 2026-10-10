# A build that starts from a template copy: ABCP names the template and checks the copy

Status: **built**, 2026-10-10, not deployed. Repo C design note `docs/product/design-notes/DECORATION.md`, phase 2.

Exact base: `6354db7` (PR #79)

Owner, 2026-10-10: "https://github.com/pankajleh/autonomous-builder-control-plane is ABCP repo, option 2", then the
choice "Copy like change builds": the Product API exports the template's exact files to a read-only folder, as it
already does for change builds; the build's instructions tell the builder to copy them first; ABCP names the template
and checks, after the build, that its files arrived unchanged.

## Why

Repo C's decoration picks, for a person's description, the closest template of the library
(`AutonomousBuild/templates`, a version tag per template) and the changes it needs. A build that starts from that
template's files is cheaper and better than a fresh build. The copy itself happens the way a change build already
starts: Repo C exports the files read-only and the task markdown tells the builder to copy them before anything else.
What was missing is a check that does not depend on the builder: a build that skipped the copy, or changed a file while
copying, must not pass as a build from the template.

## What changes

- **The request.** `RunAdmissionRequestV1` gains an optional `template`
  (`template_id`, `version`, `commit_sha`, `tree_sha`). It is omitted when unset, so every existing request and
  receipt keeps its digest. The validator accepts a template id (`^[a-z][a-z0-9_]{0,63}$`), a dotted version and two
  lowercase Git object IDs. No path is accepted.
- **The profile.** `ProfileV1` gains an optional `template_mirror_path`: a bare mirror of the library that ABCP only
  reads. It is omitted when unset, so every existing profile keeps its binding digest. A profile without it admits no
  request that names a template. Loading a profile refuses a mirror that is not a canonical absolute bare repository.
  A manifest template never names a copy; the copy is derived per run.
- **Admission.** Before a new run is materialized, ABCP checks that the mirror holds the commit and that the tree of
  `dist/<template_id>` at that commit is `tree_sha`. Otherwise `422 template_not_accepted`, before any input is
  written and before any process starts. Replays use the frozen binding, as before.
- **The manifest.** `authority.Manifest` gains an optional `template_copy` (`mirror_path`, `template_id`, `version`,
  `commit_sha`, `tree_sha`), derived from the request and the profile. Authority validation checks the identity and
  canonicalizes the mirror directory. It is omitted for every run that starts fresh.
- **The check.** Before acceptance (`prepareAcceptanceTarget`), a run whose authority names a template is refused
  unless one of its first 16 commits after the start holds every file of the template's tree with the same blob.
  Later commits may change those files; that is the decoration's work. Modes are not compared. The run fails with
  `acceptance-controller` and a reason that names the template, how many files differ, and the first of them. A
  read-only review is not checked.

## Not changed

- Ralphex owns the worktree and every commit, as before: ABCP makes no commit and creates no branch.
- The plan, the context capsule, the receipt and the binding formats; the development admission.
- Runs without a template: no new field, no new check, the same digests.

## Tests

- `internal/serviceapi`: a request without a template marshals without the key; a template by id, version, commit
  and tree is accepted; a path id, a bad version, a short commit or a missing tree is refused.
- `internal/authority`: the canonical form of a fresh run is unchanged; the copy is validated (id, version, object IDs,
  existing absolute mirror) and the authority keeps its own copy.
- `internal/runadmission`: a profile without a mirror refuses a template with `ErrTemplateNotAccepted` and starts
  nothing; a profile with a mirror refuses a tree the mirror does not hold, binds a held template into the run's
  manifest, replays it, and treats the same request id without its template as a conflict; a fresh build's manifest
  names no template; a working repository or a relative path is not a mirror.
- `internal/run`: the copy is found when later commits change it; a file changed while copying is refused and named;
  a run that never copied or made no commit is refused; a copy after the first 16 commits does not count; a tree the
  mirror does not hold at that commit is refused; the mirror is only read.

## Rollout

1. Deploy ABCP with no build running (`deploy/local-integration/README.md`). Existing profiles have no mirror, so
   nothing changes for any build.
2. Set up the library's bare mirror on the host, and add `template_mirror_path` to the node-app and web-app profiles.
   Restart ABCP with no build running, and run the drift check.
3. Repo C sends `template` only when its decoration is on for the person and the version's decoration chose a template.

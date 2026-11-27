# A first version with no builder: a template's copy with exact writes (Repo C R3a)

Status: **built**, 2026-11-27, not deployed. Exact base: `6ae5f89` (#210). Repo C's design note
`TEMPLATE_LIBRARY_ROUND_2.md` §10.5 (the owner, 2026-11-27: "yes, start R3 with the block fixes"). It joins two live
features: Quick edit's runs with no builder (`exact-edit-runs.md`) and builds from a template copy (`template-copy.md`).

## Why

When a person's ask fits a library template and every change is a setting, a build is only "copy the template, write the
settings, run the template's checks". A builder did it until now, so such an app still cost a builder run. An exact edit
already makes a version with no builder in seconds, but only from a run's earlier version (`source_sha`), never a first
one. Now it can start from the template's copy.

## The request

`POST /v1/runs` with a `template` and an `exact_edit` that names no `source_sha` (left out), and no `executor`:

```json
"template": {"template_id": "booking_lite", "version": "1.0.0", "commit_sha": "<tag's commit>", "tree_sha": "<dist/booking_lite>"},
"exact_edit": {"replacements": [
  {"path": "presets/salon.json", "base_blob": "<blob in the template's tree>", "find": "", "replace": "", "pointer": "", "from": "<the whole file>", "to": "<the person's preset>"},
  {"path": "app.config.json", "base_blob": "<blob>", "find": "", "replace": "", "pointer": "/cal.slotMinutes", "from": "30", "to": "45"},
  {"path": "app.config.json", "base_blob": "<blob>", "find": "", "replace": "", "pointer": "/site.brand", "from": "{…}", "to": "{…}"}
]}
```

This is exactly what Repo C's `AbcpRequestFactory` sends for a settings-only build (`SettingsOnly`, R3b). Every
`base_blob` is the file's blob in the template's tree. The replacements are those of today's exact edit, with these
extensions, as Repo C's `validTemplateExactEdit` makes them (`authority.ValidateTemplateExactEdit`):

- **At most 40** replacements, in any number of files.
- **A file may take several JSON values** (`pointer`, `from`, `to`; `find` and `replace` empty), at pointers that do not
  nest: neither is the other or a prefix path of it (`/a` holds `/a/b`, not `/ab` or `/a~1b`).
- **The pointer `""` is the whole document** (RFC 6901), so it is its file's only replacement. It may be sent as
  `"pointer": ""` or left out: a JSON value with no pointer is the whole document, which no other exact edit takes.
- **Words** (`find`, `replace`) are as today, 1 KB each, and their file's only replacement.
- **Each value**, `from` and `to`, is one JSON value of at most 64 KB; every value and word together at most 256 KB.
- **`from` must equal the file's value** there, compared as parsed JSON (numbers by exact value), as today.
- The same paths as today: no `.git`, `.ralphex`, `migrations` or `migration` folder, no `..`, backslash or control
  character. A file is still at most 2 MiB, before and after.

The **form** (paths, blob IDs, a pointer's escapes, values that are JSON, words or a value but not both) is checked as
every request is (`400 invalid_request`). Everything that depends on the bounds or the template's files is admission's,
and is refused with one of the two codes Repo C sends the same submission the builder's way on (§10.6):

- **`422 exact_edit_not_accepted`**: a file the template's tree does not have or not a regular file there, a blob that
  is not the file's, a `from` that is not the value there, a pointer that names nothing or two values, nesting pointers,
  `""` beside another replacement in its file, words beside a value, more than 40, a value over 64 KB, more than 256 KB
  together, a file over 2 MiB, words not found exactly once, or a read-only review profile.
- **`422 template_not_accepted`**: a profile with no `template_mirror_path`, a mirror without the commit, a commit whose
  `dist/<template_id>` is not `tree_sha`, or a mirror that names objects otherwise than the product repository (a
  SHA-256 library and a SHA-1 repository).

**Checked before the receipt.** Repo C sends the builder's request under the same request ID after a refusal. So a
template's copy with exact writes is checked before its request is receipted, and a refused one leaves no receipt,
no input and no run; the builder's request is then admitted as a new request. It is checked again under the launch lock,
as every request is. Once admitted, a replay answers the same run, and the same request ID with other writes is
`409 request_id_conflict`.

An exact edit with a `source_sha` is unchanged and still never beside a template. Every request without the new shape
keeps its JSON, digest and receipt byte for byte (`TestRequestDigestsArePinned`).

## The run

`manifest.template_copy` and `manifest.exact_edit` (with no `source_sha`) bind the run to both. The authority accepts an
exact edit with no source only beside a template copy and within the bounds above, and still refuses one with a source
beside a template, with governance, a read-only review or no worktree. The run validates its authority as every run
does, then, with no builder and no model:

1. **It reads and checks everything first**: the mirror holds the template; each file, from its blob in the template's
   tree in the mirror, takes all of its replacements in memory.
2. **The template's objects** are copied from the mirror into the product repository: the mirror lists the tree's
   objects (`rev-list --objects --no-object-names`) and packs exactly them onto a pipe (`pack-objects --stdout`), and
   the repository unpacks them (`unpack-objects`, which skips objects it holds). The mirror is only read: no ref,
   alternate, configuration or file is written there. The repository gets those objects alone, not the library's
   history or its other templates, and no ref; the copy commit then keeps them. The repository is checked to hold every
   one (`cat-file --batch-check`).
3. **The copy commit**: the template's `dist/<template_id>` tree on the run's start, as an exact edit's copy commit is
   today ("Copy of template <id> <version>").
4. **The edit commit**: the writes, made from Git objects alone ("Settings written in <n> file(s)"). Several JSON values
   in one file are written in one pass: each value is written compact in its place and every other byte of the file is
   kept, as today's pointer edit does; a whole document's value is written compact, with the spacing and the line end
   around it kept.
5. **`refs/heads/abcp/<run>`** is created only if it does not exist.
6. **The check in place of a review**: the copy holds exactly the template's tree, and the edit commit changes exactly
   the declared files, each from its blob in the template to the declared content.
7. A site-kit website's pages are regenerated as for any exact edit; the worktree is where the builder keeps one, kept
   when the authority retains worktrees.

The ledger reads `EXECUTION_STARTING` (`exact_edit: true`, `template_id`, `template_version`, the number of
replacements; no `source_sha`), `IMPLEMENTING` and `IMPLEMENTATION_COMPLETED` from `exact-edit`, with no file name and no
value. `exact-edit.json` names the template (`template_id`, `version`, `commit_sha`, `tree_sha`) in place of the source,
and each file's blobs. Then the profile's acceptance (the template's own checks), whose template-copy check finds the
copy in the first commit, ends it `BRANCH_ACCEPTED` or `FAILED`. A refusal at the run ends it `FAILED` from `IMPLEMENTING`
with no branch and no object written.

A run's own manifest may now be up to 2 MiB (`MaxRunManifestBytes`): each value is a JSON string in it, where Go writes a
quote in two bytes and `<`, `>`, `&` in six. Admission, the activity, the preview and a resume read it with that bound;
the profile's manifest template stays at 256 KiB.

## Tests

- `internal/run/exactedit_template_test.go`: accepted with two commits (the copy is the mirror's tree on the start and
  holds the template for the template-copy check; the edit changes only the four declared files, the untouched parts of
  `app.config.json` byte for byte, the preset whole and compact with its line end), no builder, the library's commit and
  another template's files not copied, the worktree on the branch and clean, the evidence and the ledger; a failing
  check stops it; a wrong blob, a `from` that is not there, a whole document that is not the file, a pointer that names
  nothing, a missing file, words twice and a tree the mirror does not hold are each refused with no branch and no object;
  `CheckTemplateExactEdit` refuses nesting, the same pointer twice, `""` beside another value, words beside a value,
  more than 40, a value over 64 KB, over 256 KB together, two blobs for one file, a source commit and a mirror without
  the commit, writing no ref or object; a SHA-256 library is not held; the mirror is unchanged throughout.
- `internal/run/jsonpointer_test.go`: several values and the whole document; nesting, a value that differs, one that is
  not there and an escape refused.
- `internal/runadmission/templatewrites_linux_test.go`: each refusal maps to its 422 (or, with a source, to invalid) and
  leaves no receipt, input or run; the builder's request under a refused request ID is admitted; the manifest binds
  both; a replay answers the same run; the same request ID with other writes is a conflict; the mirror is unchanged; a
  profile without a mirror refuses with `template_not_accepted`; writes at their bounds fit the run's manifest.
- `internal/runadmission/digest_pin_linux_test.go`, `internal/run/exactedit_pin_test.go`: a fresh build's, a quick
  edit's and a template build's request JSON and digest, a quick edit's manifest edit and its evidence, pinned at main.
- `internal/authority`, `internal/serviceapi`: the authority's checks; Repo C's exact JSON over HTTP, the two 422s, the
  bounds reaching admission and the form refused as `400`.

## Rollout

1. Deploy ABCP with no build running (`deploy/local-integration/README.md`), after the owner's word. Nothing changes for
   any request without the new shape.
2. The profiles already name the mirror (`template-copy.md`, #83): the web-app and node-app ones admit the new shape at
   once; every other profile refuses it with `template_not_accepted`, which Repo C sends the builder's way.
3. Repo C turns `product-api.decoration.settings-only.for` to `admins` only once this is live (R3c), then proves it as
   QA's administrator (§10.7).

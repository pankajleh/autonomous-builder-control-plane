# A WordPress website's standard pages on Try it and on its live site

Repo C design note `STANDARD_PAGES.md` §14 (SP1b). Repo C writes an app's standard pages (privacy policy, terms, refunds
and the rest) for the person's business and keeps each approved version as a page set: one JSON file a page, which the
kit's `wp autobuild-site page set <slug> <file>` puts on a WordPress site. ABCP carries those files to the two places
they go and reads nothing in them: to ABCP they are opaque named files (`serviceapi/pagefiles.go`).

## The page files

`PageFileV1` is `{"name", "content_base64"}`. A set is at most 20 files, each at most 256 KiB, 1 MiB in all; names match
`^[a-z][a-z0-9-]{0,59}\.json$` and come in strictly increasing order; each file is canonical standard base64 (one
spelling: no line breaks, padded) of UTF-8 text that is one JSON object, and nothing else is checked. A set is named by
`PageFilesDigest`: the SHA-256 of one line per file, its name, a space and its bytes' SHA-256 in hex, in name order.
A request carrying files may be up to `MaxPageFilesRequestBytes` (their base64 and 64 KiB more, 1,463,640 bytes), the
one bound past the server's megabyte besides a release's and an account lane's question; it applies to a preview's
create and a site's pages only.

## Try it: `/pages` in a `wp-v1` preview

- A preview profile's service may say `"mount_pages": true`: at most one service, never in a hosted profile, omitted
  when false so other profiles keep their digests. Repo C's `wp-v1` sets it on its `app` service (the site's own server,
  `autobuild/wp-runtime`), whose `start.sh` sets every `/pages/*.json` after its import.
- `POST /v1/runs/{run}/previews` takes `"pages"`. A profile without a pages service refuses them (400
  `invalid_request`, `ErrPreviewPagesRefused`). The receipt binds them by digest (`commandBinding.PagesDigest`, its
  body digest taken without them) and the preview answers `pages_digest`; the journal never holds the files, so its
  32 KiB frames and 64 MiB are unchanged. The same request ID with other files conflicts.
- The worker writes them to `<service-root>/previews/pages/<preview>/` (0755, files 0644, under a private 0700 parent)
  after the source is checked out, and starts the group with `StartPages`: the folder is bound read-only at `/pages` in
  the pages service only, with the source's options (`readonly,bind-recursive=readonly,bind-propagation=rprivate`, or
  `readonly,bind-propagation=rprivate` under gVisor, which refuses recursive read-only binds). A kept-data profile takes
  its app's volume as `StartKept` does.
- Inspection (`endpointWith`) accepts exactly that bind: type bind, destination `/pages`, read-only, the preview's own
  folder, once, in the pages service, and only for a preview started with page files. Anything else at `/pages`, or
  that bind anywhere else, fails the start or the health read, as any other mount does.
- The startup probe of a profile with a pages service mounts a probe page file and reads it with `cat` through the
  service's own user, beside the source probe; the profile is approved only if both read back.
- The folder is removed with the source once the containers are gone; a restart removes every folder an earlier
  controller left (`PageFiles.Reconcile`). Every start is a new preview, so each start carries the files again.

## A live site: `POST /v1/targets/{key}/sites/{site}/pages`

`{"request_id", "files": [PageFileV1...]}` (1 to 20 files) for a WordPress website (`kind` wp) ABCP serves, not stopped,
on a confirmed, idle target; 202 with an `OperationV1` of kind `pages`. Refusals keep the deployments' codes: a Node app
or static site, a bad request ID or bad files 400 `invalid_request`; an unknown site 404 `site_unknown`; a stopped site
409 `site_stopped`; another operation running 409 `target_busy`. The same request ID answers the same operation, and
the same request ID with other files is refused (the record keeps their digest, never their bytes).

The operation's steps:

1. `check_app`: `docker exec autobuild-app-<site> php /usr/local/bin/wp --path=/opt/wordpress plugin get
   autobuild-site --field=version` must say 1.2.0 or later, the first kit with `page set`. An older server image fails
   the operation here, before any backup: "the site's kit is 1.1.0, older than 1.2.0: update the live site first".
2. `back_up`: the WordPress website's backup, as before an update (database dump and uploads, rows counted, copied
   off the server where the target's backups are), with reason `before_update`.
3. `send_pages`: `/scratch/autobuild-pages` made afresh in the site's container (its memory-backed `/scratch`, mode
   0700, the container's user's), and each file written there with `docker exec -i ... /bin/busybox tee`, its bytes on
   standard input; nothing of a file reaches a script or an argument.
4. `set_pages`: for each file, `docker exec autobuild-app-<site> php /usr/local/bin/wp --path=/opt/wordpress
   autobuild-site page set <name less .json> /scratch/autobuild-pages/<name> --format=json`. Its last line of output
   is read: `slug` must be the file's, `page` one of `created`, `updated`, `unchanged` or `kept`, `footer` one of
   `none`, `there`, `added` or `full`. A failed command, or an answer that is not that, makes the file `refused` with
   its last line of error. The step never fails on one page: its detail counts them ("3 set, 1 kept, 0 refused").
5. `clean_up`: the folder removed; one that cannot be goes with the container's memory when it restarts.

The answer's `pages` is each file's `SitePageV1`: `file`, `outcome` (`created`, `updated`, `unchanged`, `kept` or
`refused`), `why` (the kit's words for a kept page: edited in WordPress, in the bin, or an address one of the site's
own pages has), `footer`, and `detail` for a refused one. The files are in ABCP's memory only while the operation
runs, and dropped however it ends; an interrupted operation becomes `unknown`, as every other.

## Switching it on

- The preview: the `wp-v1` profile's `app` service gains `"mount_pages": true` and the runtime image built from Repo C
  with SP1's `start.sh` and plugin 1.2.0 (published by Repo C's `wp-site-kit.yml` from main); then a restart, whose
  probe proves the mount. Until then Repo C must not send pages to `wp-v1`, which would be refused.
- The live site: `WordPressServerImage` (`wpsites.go`) is pinned to the server image Repo C publishes from main with
  plugin 1.2.0, in a commit of its own once that digest exists; each site takes it at its next update. A site still on
  an older image answers `check_app` failed, with nothing changed.

## Tests

`TestPageFilesAreNamedBoundedJSONObjectsInOneCanonicalSpelling`, `TestAPreviewCreateMayCarryPageFilesWithinTheirOwnBound`
and `TestASitesPageFilesArePostedWithinTheirBoundAndAnswerAnOperation` (serviceapi); `TestPageFilesAreAPreviewsOwnReadableFolderRemovedWithIt`,
`TestAPagesServiceIsOneServiceOfAPreviewProfileAndKeepsOldDigests`, `TestPagesMountIsReadOnlyInThePagesServiceAloneUnderBothRuntimes`,
`TestInspectionAcceptsExactlyThePreviewsReadOnlyPagesMount`, `TestAPreviewWithPagesStartsWithThemAndPassesItsInspection`
(default runtime and gVisor), `TestTheProbeProvesThePagesMountThroughThePagesService` and
`TestAPreviewCreatedWithPageFilesStartsWithThemAndTheyGoWithIt` (preview, receipts across a restart);
`TestAWordPressWebsitesPagesGoOnAfterABackupAndEachSaysWhatItCameTo`, `TestPagesWaitForASiteWhoseKitIsTooOld` and
`TestPagesAreRefusedForAnythingButARunningWordPressWebsite` (remotetarget, on the fake Docker that plays the kit).
Nothing here ran on a real Docker host, gVisor or WordPress; the kit's own CI proves `page set` on WordPress.

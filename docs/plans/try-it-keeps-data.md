# Try it keeps each app's data

Repo C design note `TRY_IT_KEEPS_DATA.md` (the owner, 2026-10-26: "Yes, keep Try it data"). A web app with accounts
was tried beside a database that lived only as long as the preview, so every change, restart or expiry started it again
from the app's sample data, and what a person had entered there was gone.

## What ABCP does

- **`keep_data` on a preview profile.** Its one data service (`data_volume`) mounts a volume kept for the preview's app,
  `abcp-tryit-<namespace>-<digest>`, named from a digest of the preview's product task (known from the run's admission,
  never sent by the caller). Until now `data_volume` was for hosted profiles only; a kept-data profile is never hosted,
  has exactly one data volume and no backup or restore arguments. The field is omitted when false, so other profiles
  keep their digests.
- **One preview of an app holds its data.** A kept-data preview starts only through `StartKept`. Under a lock per app,
  it first ends the app's other live kept-data previews (`STOPPED`) and waits until each one's worker has stopped its
  containers; `StartKept` itself refuses while any container still mounts the volume (`docker ps --filter volume=`). Two
  Postgres servers never share one data directory.
- **`POST /v1/runs/{run}/previews/{preview}/forget-data`** (a stop's body): for that preview's app, ends every live
  preview of the app, removes the app's volume (refused while a container mounts it) and answers the named preview,
  ended. It replays by request ID; the journal accepts its receipt as it accepts a stop's.
- The volume is kept until forget-data, or until 90 days pass without a Try it of the app (#153, the owner,
  2026-10-26): `SweepKeptData`, run by `abcp serve` 15 minutes after start and then every 6 hours, removes the volume
  of each app whose newest kept-data preview started 90 days ago or more and that has none running, checked again
  under the app's lock; a volume in use or one whose app it cannot name stays. Repo C's archive forgets an app's data
  at once (P0783). Live since 2026-10-26 17:46:27 UTC; the first sweep, at 18:01:26, removed nothing (#154). A plain
  profile never takes the kept path, and the preview controller's cleanup (containers, their anonymous volumes and
  networks) never removes a named volume.

## The profile Repo C uses

`node-pg-v2`: `node-pg-v1` with the hosted Postgres image and start (`autobuild-postgres hosted`, data in `/data`),
`data_volume` on the database and `keep_data`. Repo C's live setting `product-api.preview.profiles.node-app` moves to it.

## Tests

`TestKeptDataPreviewsHoldTheAppsDataOneAtATimeAndForgetRemovesIt` (a second Try it ends the first and starts only
after it; forget-data ends every live preview, removes the data once, replays, and its receipt survives a restart),
`TestAPlainPreviewNeverTakesAppData`, `TestKeptDataProfilesHaveOneVolumeAndAreNotHosted`,
`TestKeptDataDockerVolumeIsTheAppsAndNeverShared` (the volume's name, and no start or removal while a container
mounts it), and the HTTP route in `TestPreviewRoutesRequireAuthorityAndPreserveLegacyCapabilities`;
`TestKeptDataUnusedForNinetyDaysIsRemovedButNeverWhileATryItRuns` and `TestKeptKeysAreThisNamespacesTryItVolumesOnly`
for the 90-day sweep (#153).

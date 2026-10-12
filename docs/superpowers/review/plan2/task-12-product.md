# Task 12 Product review (service teardown, worker side)

Commits: 28211dd, 66457af, 473a502, 5fb3c08. Reviewed from code (teardown.go, store/teardown.go, starter.go) and the plan; no UI exists yet for delete (Task 17), so no UI walk-through was possible. Note the committed code is stricter than the plan text (cancel step, scrub, strict payload).

Mechanics relied on: DELETE returns 202 after soft delete + outbox event. Worker: CancelServiceDeploys (poll up to 3 min per attempt, activity timeout 6 min, 6 attempts, backoff 10s..5m) then TeardownService (2 min timeout, same retry). Order inside TeardownService: fail open deployments, remove containers, mark stopped, remove route, optional volume purge, scrub env/connections/repoUrl (scrub is LAST).

## Findings

[PROD-1] severity: high
where: TeardownWorkflow failure path (teardown.go), DELETE /services/:id UX
problem: If teardown fails after retries (Docker down, nginx reload fails, cancel never settles), the service is already soft-deleted: it vanishes from canvas/lists, yet its container may keep running, its `<serviceId>.localhost` route may keep answering, and a managed DB keeps its data. The user got a 202 and has no signal, no retry button and no way to see the service again. Failed workflow = outbox event already "published", and WorkflowID teardown-<id> with REJECT_DUPLICATE means re-emitting the event does NOT retry. An operator needs a documented manual path.
evidence: worst case before the workflow gives up: cancel step 6 x 3 min + backoff (~5 min) = roughly 23 min of "deleted but alive"; teardown step another ~5 min of backoff.
fix: (a) Operator runbook in release note / cutover checklist: find failed `teardown-<id>` in Temporal UI (localhost:8233), fix cause, then reset/restart it (Temporal "Reset" or terminate + re-send outbox event with a new workflow id), or manually `docker rm -f` containers labelled with the service id, delete `svc-<id>.conf`, reload nginx. (b) Provide a worker log line/metric on teardown permanently failed (alert). (c) Preferably later: a "teardown failed" state visible to the user (soft-deleted services with failed teardown shown as "Deletion failed, retry"), or allow the starter to restart a failed teardown (ALLOW_DUPLICATE_FAILED_ONLY policy instead of REJECT_DUPLICATE). Minimum for Task 12: (a) and (b).

[PROD-2] severity: high
where: purgeData=false default for mongodb/redis (TeardownService + ScrubDeletedService)
problem: Keeping the volume while deleting the generated password (EnvVar rows scrubbed unconditionally) means kept data is unreadable by anyone and consumes disk forever with no UI to list or reclaim it. "Keep data" is a false promise: a user who re-creates a database gets a new service id, a new volume and a new password, never the old data. Keeping is only useful to an operator doing manual forensics, and even then the password is gone (Mongo/Redis auth baked in the volume cannot be recovered).
evidence: `if in.PurgeData {RemoveVolume}` then `ScrubDeletedService` deletes env rows; VolumeName is cp-vol-<serviceId>; no listing tool.
fix: For managed types the default must be a conscious choice: the Task 17 dialog should have NO preselected answer for mongodb/redis (two buttons: "Delete database and its data" / "Delete service, keep data volume on disk"), with the keep option explained as "data stays on the server disk, is not reachable from Infra Pilot, and the password is deleted; cannot be restored". Strongly consider making API default purgeData=true for managed types (a database the user deleted is gone), keep false only for stateless types where there is no volume anyway. Alternative: do not scrub the password when keeping the volume (but then the secret stays in DB; weaker security; not recommended without a restore feature). Also add an operator doc line: kept volumes are `docker volume ls` named `cp-vol-<serviceId>`; remove with `docker volume rm`.

[PROD-3] severity: medium
where: timeline between 202 and actual removal
problem: Canvas and lists drop the service immediately, but until teardown completes the service URL still serves (route file exists), a managed DB still accepts connections, and a deploy in progress keeps running up to 3 min while it is cancelled. For a database, a connected app keeps writing for that window. Not harmful for hobbyists if described, but the 202 body/UI must not say "deleted and stopped".
evidence: normal case: seconds (outbox poll 1s + container remove). Slow case: up to 3 min waiting for a build/deploy workflow to close; failure case: PROD-1.
fix: Task 17 toast: "Deleting... the service stops within a minute (a running build can take up to 3 minutes)". Do not show "Deleted" as confirmed-stopped. Release note says the same. Optionally make the API response say `{status: "deleting"}`.

[PROD-4] severity: medium
where: irreversible actions
problem: Soft delete is not an undo: env vars (incl. secrets), connections and repoUrl are scrubbed (a partial scrub at the end of teardown). There is no restore, and with purge=true there is no data recovery. The row remains only as a tombstone for deployments history. Users used to the Node backend may not expect this (Node had no delete at all, so this is net new, no regression).
fix: Confirm dialog must say: removes containers, route and env vars; connections to other services are removed (the canvas edges disappear; connected services' env vars that reference this service are NOT updated, so they may break); cannot be undone. For databases require typing the service name. Release note states no restore exists.

[PROD-5] severity: medium
where: connected services
problem: ServiceConnection rows are deleted, but other services' env values that embed this service's host/URL (e.g. a Redis URL pasted into an app) are untouched. The dependent app keeps pointing at a dead host after its next restart, with no hint.
fix: Dialog lists connected services ("3 services connect to this one") from the canvas data; release note mentions it. No backend change needed now.

[PROD-6] severity: medium
where: malformed purgeData (starter.go parseTeardownPayload)
problem: A purgeData that is not a boolean returns ErrOutboxPermanent: the event goes dead and the service stays soft-deleted but RUNNING and invisible; teardown never starts. Falling back to false would remove containers and keep data (the safe direction), instead of leaving a running invisible service. The API (Task 14) should make this unreachable by validating the body (400 on non-boolean), so the dead path is only for corrupted rows.
fix: Acceptable as is only if Task 14 rejects non-boolean purgeData with 400 before the delete commits (verify there; ties to PROD-1: a dead teardown event also needs an operator alert/runbook line). Do not fallback to true under any circumstances.

[PROD-7] severity: low
where: deployments/logs history of deleted services
problem: Deployment rows stay (marked stopped/failed) and Redis/ClickHouse/S3 logs remain, but the service row is a tombstone with Name kept. Whether GET /deployments, /logs/:id still list them for deleted services is decided in Tasks 14-15, not here. Open deployments are set to `failed` by teardown, which will read in the history as a failed build the user did not cause.
fix: Decide in the API tasks: hide deployments of deleted services from lists (recommended; a user who deleted a service does not expect it in history), and document that logs age out by retention. Consider a distinct status/message ("cancelled: service deleted") rather than plain `failed`; low priority.

[PROD-8] severity: low
where: name reuse
problem: No unique index on Service name exists in the migrations, so creating a service with the same name right after delete succeeds, and it gets a new id, new volume, new route (`<newId>.localhost`). No collision. The old URL is dead after teardown (nginx 404/502), not reused.
fix: none. Mention in release note that the URL changes because it is id-based.

[PROD-9] severity: low
where: registry images
problem: Images pushed to the local registry (localhost:5001) for the service are never deleted, so disk grows. Not user visible, but a hobbyist on a laptop will notice eventually.
fix: Release note / known limitation, follow-up task for registry GC. Same for `cp-builder` build cache.

[PROD-10] severity: low
where: purge=true + legacy data
problem: purge also deletes the Node-era host directory LEGACY_VOLUME_ROOT/<serviceId> (good: otherwise the old data comes back; there is the risk that a user kept that directory deliberately as their only backup). Mention in the dialog text for databases migrated from the Node era.
fix: One line in release note.

Not an issue: concurrent double delete (REJECT_DUPLICATE plus idempotent activities), delete of a service whose workflow is mid-build (cancelled, then containers swept by label), stray event for a live service (refused, non-retryable).

## Task 17 UI items (delete flow)
1. Delete action reachable from the service node/panel; confirm dialog, not window.confirm.
2. Text: "Stops and removes the container and its URL. Removes env vars and connections. This cannot be undone. Services that use this one are not updated."
3. For mongodb/redis: required explicit choice, no default selected: "Delete the data too" vs "Keep the data volume on the server (not accessible from Infra Pilot; password is deleted)". Type-the-name confirmation for the purge choice.
4. Show the number/names of connected services.
5. After 202: toast "Deleting. This can take up to a few minutes if a build is running"; remove the node optimistically but refetch the canvas; handle 404 (already deleted), 403/404 ownership, 5xx with a readable message.
6. Disable the button while the request is pending (avoid double submits).
7. No restore UI; no "deleted services" list. Consider a later "failed deletions" view (PROD-1).

## Release-note lines
- New: services can be deleted (DELETE /services/:id, UI button in a later release). Deletion is asynchronous: the service disappears at once, containers and the URL stop within seconds to a few minutes.
- Deleting is permanent: env vars, connections and repo URL are erased; there is no undo or restore.
- Managed databases: by default (if purgeData is false) the data volume stays on disk but its password is deleted, so the data cannot be read again; it is not shown anywhere and uses disk until removed with `docker volume rm cp-vol-<serviceId>`. Pass purgeData=true (or choose "delete data" in the UI) to remove it.
- Services that connected to the deleted one keep their own env vars and may fail to connect.
- Registry images for deleted services are not cleaned up yet.
- A new service with the same name gets a new id and URL.

## Cutover-checklist lines
- Verify a teardown end to end on staging: container, `svc-<id>.conf`, volume (purge true and false), env rows gone, workflow Completed in Temporal UI.
- Confirm Task 14 returns 400 for non-boolean purgeData and that deployments of deleted services are hidden.
- Operator runbook for failed `teardown-<id>` workflows (Temporal UI search `WorkflowId STARTS_WITH "teardown-"` and status Failed), and the manual cleanup commands; alert on failed teardowns.
- Decide the managed-DB default (recommend purge=true by default or explicit choice) before enabling the UI button.
- Document orphan volumes: `docker volume ls --filter name=cp-vol-` compared with live services.
- Note registry image growth.

RESULT: CHANGES REQUESTED (PROD-1 and PROD-2 need a decision/doc before the feature is user-facing; neither blocks the worker code itself).

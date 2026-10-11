# Plan 2 Task 8 - Product review (commits 058321e, 0eb270f, 962886b, ce69f7f)

Scope: user-facing impact of the Go service, deploy, env and delete endpoints against `cp-ui/src` and the Node API. No code edited.

## What the UI actually does (verified)

- `CreateServiceModal.tsx:250-290`: `POST /services` with `{projectId, name, serviceType, repoUrl?, branch?}` (branch defaults to "main"), then `POST /services/:id/deploy` with `{commitSha:"HEAD"}`. Reads only `id` from the create response. Errors shown as raw `Error("<status>: <body>")`.
- `ServiceDetailsPanel.tsx:56-130` (`EnvVarSection`): list, add-form (new key + value), per-row Delete, per-row eye toggle. There is NO in-place value editing. Re-adding a key with the same name overwrites it, with a value the user just typed.
- `ServiceDetailsPanel.tsx:478`: Deploy button also sends `commitSha:"HEAD"`.
- The UI never displays or resubmits `repoUrl` (grep: only the create modal's own input state). It has no service delete button and no call to `DELETE /services/:id`; it only deletes env vars and canvas connections.

## Findings

[PROD-1] severity: medium
where: `ServiceDetailsPanel.tsx:231` (eye toggle) against `GET /services/:id/env`
problem: The list is now masked (`********`) unless `?reveal=true`. The eye toggle shows `env.value`, so clicking it shows the literal text `********` where the secret used to appear. The UI claims to show the value and shows a fake one. Users will think their values were lost or overwritten.
data-loss check: NOT a data-loss bug. Nothing in the UI sends a listed value back. There is no edit-in-place, and the add form posts only what the user typed. Real values are never overwritten with `********`. The risk is only a user who copies `********` into the form by hand. The server could reject a value equal to the mask on POST as cheap protection (suggestion for Task 8 close-out, optional).
fix (Task 15): on the first eye click for a service, call `GET /services/:id/env?reveal=true` and cache the result in component state. Keep the default display as the dots. Alternatively, drop the per-row value and show only "set". Until Task 15 lands, the eye toggle is wrong. Do not cut over before then.

[PROD-2] severity: medium
where: any UI error path (`apiFetch` in CreateServiceModal, ServiceDetailsPanel, ProjectCanvasPage and others)
problem: Errors display as `400: {"error":"invalid_repo_url"}`, `404: {"error":"service_not_found"}`, `400: {"error":"invalid_env_var"}`, `400: {"error":"invalid_request"}`. Codes are not human-readable, and `invalid_request` and the free-text `repoUrl required for docker/nodejs/nextjs/react services` do not say which field is wrong. Node's zod errors were also raw, so this is not a regression, but Go's repo URL rule is much stricter, so users will hit it more: only http/https (and the URL must be printable ASCII) passes. `git@github.com:x/y.git`, `ssh://` and URLs with non-ASCII characters are now 400. These were accepted by Node's `z.string().url()` (ssh:// and file:// parsed as valid URLs), although the worker could not clone most of them.
fix (Task 15): the shared API client parses `{error}` and maps codes to messages, for example `invalid_repo_url` -> "Repository URL must be an https:// or http:// address", `invalid_env_var` -> "Names may use letters, digits, _ . - ; values cannot contain line breaks and are limited to 32 KiB", `service_not_found` -> "This service no longer exists" (and refresh the canvas), `invalid_request` -> "Check the name (2+ characters) and branch". The create modal should pre-validate the https:// prefix and give a hint that SSH-style URLs are not supported.

[PROD-3] severity: medium
where: `DELETE /services/:id` (no UI exists)
problem: Delete is new in the API and absent from the UI, so nothing regresses. The semantics are surprising for managed DBs: the default is `purgeData=false`, so the volume `cp-vol-<serviceId>` is kept forever. The service is gone from the canvas and listings at once (soft delete, 202), and a re-created service gets a new id, so the data is orphaned with no way to reach it from the UI and it silently uses disk. The opposite choice (default purge) would be worse (accidental data loss); only the exact string `true` purges, so `1` or `True` keeps data, which is the safe direction. For app services the flag is irrelevant. Between the 202 and teardown completion, the container and route keep running for a few seconds while the service has already disappeared. A fast re-create of the same name is fine (new id).
fix (Task 15/17): add a Delete service action (details panel footer or node menu) with a confirmation dialog. For `mongodb` and `redis` show a checkbox "Also permanently delete stored data" (unchecked by default) with a warning that it cannot be undone, and send `?purgeData=true` only when it is checked. For other types a plain confirm. After the 202: optimistically remove the node and its edges, close the panel, show a toast "Deleting... containers are being removed". Treat 404 on delete as success (already deleted). Orphaned kept volumes need a cleanup story (see release note and a follow-up: list or reap volumes of deleted services).

[PROD-4] severity: low
where: `defaultRef` (services.go:196) against the UI
problem: The default ref is now the service Branch (or HEAD) instead of the placeholder `main`. The change is correct and invisible to the current UI because both the Deploy button and the create modal always send `commitSha:"HEAD"`, and the builder treats `HEAD` and `ref == Branch` as the branch tip (`worker/build/builder.go:142`). Deployment rows from the UI therefore show `HEAD` as the commit label, as with Node. API-only callers that omit `commitSha` on a service with Branch `dev` previously built `main`, and now build `dev`; this is a fix, but call it out. Branch names that are not valid refs (for example `feature/x`) fall back to `HEAD`, which still checks out the service branch.
fix: none required. Optionally have the UI stop sending `HEAD` (omit the body) so the server default applies. Release note.

[PROD-5] severity: low
where: env keys (`envKeyPattern` `^[A-Za-z0-9_][A-Za-z0-9_.-]*$`, <=256 bytes)
problem: The relaxed rule is friendlier than the plan's shell-name rule (allows `.` and `-` and a leading digit), so keys users have in Node-era data are accepted. The plan text (`^[A-Za-z_][A-Za-z0-9_]{0,127}$`) is now out of date; the 2026-10-10 ruling supersedes it, and the plan's test row `BAD-KEY` -> 400 must be updated (code passes it now). Keys with spaces, `=`, `/` or an empty name still fail. The UI already trims nothing on the key and only blocks an empty key, so a pasted `FOO=bar` or `my key` gets a raw 400. Values: Node accepted line breaks and the worker would then write a broken env file; Go rejects `\r\n` and NUL and values over 32 KiB. A multiline PEM key pasted into the value box is now refused with `invalid_env_var`. This is a real capability loss for users with multi-line secrets (private keys, JSON blobs); the docker env-file format cannot carry them. Suggest telling users to base64-encode, in the UI hint and the release note.
fix (Task 15): hint text under the add form; trim the key; map the error (PROD-2).

[PROD-6] severity: low
where: repo URL credential masking
problem: Responses show `https://****@github.com/x/y` and tokens in the query are masked/dropped. The UI never shows the URL and never re-submits it, so there is no loss. But anyone who later adds an "edit repo URL" field or a "Repository" row to the panel must not pre-fill the masked URL and re-post it: that would store `****` as the credential. Also note the masking applies to every response containing a service: Task 10 canvas must use `publicService` too (the plan amendment says so; verify in the Task 10 review). Private repos with a token in the URL keep working because the stored value is raw.
fix: none now. Add a note in the Task 15 UI brief and make PATCH/update endpoints (if ever added) reject URLs containing `****`.

[PROD-7] severity: low
where: ownership 404s
problem: `/services/:id/*` for a service the caller does not own now answers `404 {"error":"service_not_found"}` (same as Node's code, new behaviour is that it fires for foreign services too). Foreign project on create answers `404 {"error":"Project not found"}` (different wording and style from `service_not_found`; the UI shows both raw). With a single owner per project there is no visible change for legitimate users, except after the forced re-login at cutover (a new user id if the JWT sub changes? no: ids are kept). A user whose token belongs to a different account than the one that created the services will see an empty canvas and 404 in the panel with no explanation.
fix: Task 15 message mapping (PROD-2).

[PROD-8] severity: low
where: `GET /services/:id/env` response shape
problem: Shape is unchanged (`[{id?, serviceId, key, value, ...}]`, key-sorted?). Check that the list ordering is still by key, as Node did (`orderBy key asc`); the new unit test expects `masked[0]` to be `API_KEY` and `LEGACY` second, which is alphabetical, consistent. `POST /env` now returns `value:"********"` where Node returned the stored row with the real value; the UI ignores the response body and reloads, so no impact. A Node-era plain value is returned as-is with `?reveal=true` and masked otherwise, fine. Empty value (`""`) is accepted (the UI sends `""` when the value box is blank), matching Node.
fix: none.

No critical or high findings. Data-loss concern from masking: ruled out (see PROD-1).

## Task 15 / Task 17 UI items

1. Shared API client: parse `{error}`, map the codes (`invalid_repo_url`, `invalid_env_var`, `invalid_request`, `service_not_found`, `Project not found`, the free-text `repoUrl required ...`, `internal_error`) to readable text; 404 on service routes refreshes the canvas.
2. Env editor: eye toggle fetches `?reveal=true` (lazily, cached); never display `********` as if it were the value; hint "names: letters, digits, `_ . -`; values: single line, up to 32 KiB (base64 for multi-line secrets)". Optionally show "set" with no dots for masked rows. Consider a "Saved. Redeploy to apply" message, since env changes only reach containers on the next deploy.
3. Create modal: pre-validate https:// (or http://) repo URL, say that SSH URLs are unsupported, explain the branch field. Optionally drop `commitSha:"HEAD"` so the server default applies.
4. Delete service: new action with confirmation; managed DB purge checkbox (default off, loud warning); 202 handling (optimistic removal and toast); 404 treated as success. Decide what the panel shows if open while deleted (close it).
5. Do not pre-fill or re-submit a masked repo URL anywhere.
6. Task 17 (end-to-end walk): create service with a bad URL and read the message; add key `FOO.BAR`, `BAD KEY`, multiline value; toggle the eye; delete an app service and a Redis service with and without purge; confirm the canvas node disappears at once, the container stops shortly after, and `cp-vol-<id>` is removed only with purge.

## Cutover-checklist lines

- UI release with the eye-toggle `?reveal=true` fix ships with (or before) the Go API; otherwise the toggle shows `********`.
- Env values written by the Node API stay plain until re-saved; the Go API reads plain and `enc:v1` rows. Confirm the worker decrypts env values at deploy and that `ENV_ENCRYPTION_KEY` is backed up (losing it makes every re-saved env value unrecoverable).
- Smoke: delete a throwaway Redis service with `purgeData=true` and one without; check `docker volume ls` and the nginx route removal.
- Teardown depends on Task 12 (`TeardownWorkflow`); until it ships, `teardown.requested` events pile up in the outbox, and deleted services keep running containers. Do not expose delete in the UI (Task 15) before Task 12 is deployed.
- List kept (unpurged) `cp-vol-*` volumes after the first deletions; decide on a reaper or an admin doc.

## Release-note lines

- Environment variable values are now encrypted at rest and shown masked in the API; the dashboard reveals them on request (`GET /services/:id/env?reveal=true`).
- Environment variable names may contain letters, digits, `_`, `.` and `-`. Values must be a single line (no line breaks) and at most 32 KiB; encode multi-line secrets (PEM keys) as base64.
- Repository URLs must be http(s) addresses; SSH-style (`git@host:org/repo`), `ssh://`, `file://` and non-ASCII URLs are rejected with `invalid_repo_url`. Credentials in a repo URL are masked (`https://****@host/...`) in every API response; they are still used for cloning.
- Services can now be deleted: `DELETE /services/:id` returns 202 and removes the service from the project at once; containers and routes are removed in the background. Add `?purgeData=true` to also delete a managed database's stored data; without it the data volume is kept.
- Deploys without an explicit `commitSha` now build the service's configured branch instead of always `main`.
- Every service, env and deploy route is limited to the project owner; other accounts get `404 service_not_found`.
- Re-login is required once at cutover (Plan 2 auth change; see Task 7 review).

RESULT: APPROVE (no critical/high; PROD-1 and PROD-3 are gating items for Task 15/17, and Task 12 must precede any UI delete)

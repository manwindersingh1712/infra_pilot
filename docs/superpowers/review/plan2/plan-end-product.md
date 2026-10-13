# Plan 2 plan-end: Product review (Task 18 Step 3, Product part)

Reviewer: Product. Date: 2026-10-13. HEAD at start: bd3b98a (branch go-migration). No code changed. Release note: `docs/superpowers/release-note-plan2.md` (consolidated, committed separately).

## Method

Headless Google Chrome driven over CDP (own scripts, real mouse and key events via `Input.dispatch*`), as in `task-17-round2-verifier.md`. Throwaway `postgres:16` (tmpfs, 127.0.0.1:25460) migrated with `go run ./cmd/migrate up` (`migrate status`: 00001-00004 applied); Go API built from HEAD on 8102 (APP_ENV=development, random JWT_SECRET and ENV_ENCRYPTION_KEY, `CORS_ORIGINS=http://localhost:5192`, metrics 19102); UI from `git archive HEAD cp-ui` on Vite 5192 (`VITE_API_BASE=http://localhost:8102`, dev build, so StrictMode doubles some requests). Dev Redis 6380 was used only for stream tickets and for one throwaway log stream key `logs:stream:<my deployment>` (deleted afterwards); no other key touched. Managed-database deployments and the AES-GCM password rows were inserted by SQL (no worker, so nothing deploys). NOT touched: the Go worker, the dev DB, the Node API 8080, encrypt-env, Temporal, any Docker prune. Screenshots (42) are in the scratchpad `.../scratchpad/shots/` (names quoted below).

## Walkthrough: pass / fail / needs worker

Checklist numbers are Step 5 of the plan (`go-plan2-api.md`) and step 11 of `runbook/api-cutover.md`.

| # | Item | Result | Evidence |
|---|---|---|---|
| 1 | Forced re-login: old Node-style token | PASS | Tokens with no `exp` (valid signature) and one signed with the Node dev secret, no refresh token: opening `/projects` lands on `/login`, notice "Your session expired, please sign in again.", `cp_token` cleared (shots 01-*) |
| 2a | Sign up | PASS | 72-byte password OK, lands on `/projects`, `cp_token` and `cp_refresh` stored (04) |
| 2b | Password limits | PASS | 7 chars: "Password must be at least 8 characters." (02); mismatch: "Passwords do not match"; 73 ASCII bytes and 37 x "é" (74 bytes): "Password is too long (the limit is 72 bytes; accented or non-Latin characters take more than one byte)." (03) |
| 2c | Log in / log out | PASS | Login ok; avatar menu, Sign out: back to `/`, both tokens removed, the old refresh token then answers 401 (09, 10) |
| 2d | Refresh (access token expired) | PASS | Forged `exp` one hour ago: `GET /projects` 401, `POST /auth/refresh` 200, retry 200, page renders, refresh token rotated (08). Replaying the old refresh token: 401 `invalid_refresh_token` |
| 3 | Ownership 404 (curl, user B on user A's ids) | PASS with one note | `/services/<id>/env`, `/logs/deployments/<id>`, `/projects/<id>/canvas`, `/services/<id>/connection`, stream-ticket: 404. `GET /deployments?serviceId=<A's id>` returns `200 []`, not 404 (a deliberate "no existence oracle" in `api/deployments.go`; the plan text says 404, so the checklist line should say "empty list") |
| 4 | Create project / service (UI) | PASS (create); deploy and redeploy NEEDS WORKER | Project 2-100 chars: 1 char disables Create (05), 2 enables, spaces only disables. nodejs service created; ssh URL gives "Enter a public https:// repository URL. SSH and file URLs are not supported." (18). Create also queues a deployment row (as Node did). Container retirement needs the worker |
| 4b | Project names: 1 / 100 / 101 chars, emoji | PASS with a note | 101 typed or pasted: the input stops at 100 (no message; the API itself answers 400 `invalid_request` for 101). 100-emoji name: the input accepts only 50 (see PROD-P3). The saved emoji project shows on the list (07) |
| 4c | Canvas drag persists | PASS | Three drags: 3x `POST .../position` 200; after leaving and reopening the project the relative positions are the same (22) |
| 5a | Env vars masked, Reveal | PASS | Added `SECRET_TOKEN`: dots only, value absent from text and from `outerHTML`; the eye shows it with one `?reveal=true` GET (two in the dev build); hide removes it again (23, 24). Bad name: the human sentence is shown (25). `psql`: every `EnvVar.value` starts `enc:v1:` |
| 5b | Container receives the plain value (`docker exec ... printenv`) | NEEDS WORKER | verify at cutover |
| 6a | Logs: SSE with ticket, no token in URL | PASS | `POST .../stream-ticket` 200, `GET .../stream?ticket=` 200, no `token`/`Bearer` in any request URL. Seeded lines show with SYS/OUT/ERR tags (26) |
| 6b | Truncation at 8 KiB | PASS | A 9,000 char line is shown as 8,192 chars + `...[truncated]` |
| 6c | 5,000-line cap | PASS | 5,200 lines published live: exactly 5,000 rows remain, newest kept (27) |
| 6d | Reconnect after API restart | PASS | API killed: badge Reconnecting (amber) + "Cannot reach the server. This panel keeps retrying." + Retry (28); API restarted: Live again within 12 s, a line published after the restart appears, all 5,000 rows kept (29) |
| 6e | Seeded lines only show with source stdout/stderr/system | INFO | My first seed used other `source` values and the panel stayed on "No logs yet" (the UI filters unknown sources). The worker writes stdout/stderr/system, so no issue in real use |
| 7 | Managed DB: Connection tab | PASS (UI); Redis ping from project network NEEDS WORKER | Redis and Mongo: Host `svc-<id>`, Port, (User), Password, URL, Local URL, CLI all masked by default, no plain value anywhere in the DOM, Reveal/Hide secrets work (30, 31). The explanatory banner says the database is not exposed to the internet. `redis-cli -u ... ping` from `cp-proj-<id>` and the `enc:v1:` password check on a real container: verify at cutover |
| 8a | Delete (UI) | PASS | App service: one-step dialog, `DELETE ?purgeData=false` 202, node gone at once, "Deleting… the service stops within a minute (a running build can take up to 3 minutes)." (43, 44). Managed DB: two choices; first purge click arms ("Click again to erase the data permanently"), second click sends one `DELETE ?purgeData=true` (40, 41, 42). Outbox: two `teardown.requested` rows with the right `purgeData` |
| 8b | Containers, nginx route and volume really removed, Temporal `teardown-<id>` Completed | NEEDS WORKER | verify at cutover |
| 9 | 6th wrong login gives 429 | PASS | Six clicks in 1.5 s: attempts 1-5 "Invalid email or password.", 6th and 7th "Too many requests. Wait a moment and try again." (11). Raw: 429 with `Retry-After: 60` readable from the UI origin. (Slow attempts, about 3 s apart, never hit it: the bucket refills 1 per 6 s) |
| 10 | Production-mode start refusal, no `/dev/login`, Origin check | NOT RUN BY ME | API behaviour, not user-facing; Verifier's list |
| 11 | Metrics | NOT RUN BY ME | same |
| extra | Legacy services still list, connections | NOT RUN | needs the real DB; Verifier |

Verify at cutover (needs the real worker): deploy a nodejs service and redeploy it (old container retired, status badge moves through queued/building/deploying/deployed, failure shows `failed`); `docker exec cp-<id> printenv KEY` shows the plain value; Redis `ping` from the project network with the real password; Mongo connection with the generated password; delete with and without data purge removes container, nginx route (and the volume when purged), workflow `teardown-<id>` Completed; the canvas "Online" badge for a queued or failed deployment (PROD-P2); logs of a real build.

## Findings (user-facing)

[PROD-P1] severity: medium
where: delete dialog for managed databases, `cp-ui/src/components/DeleteServiceDialog.tsx:233`
problem: the "Delete service, keep data volume on disk" option says "The password is deleted, so it cannot be restored." The server does the opposite on purpose: `ScrubDeletedService(keepGeneratedSecret=true)` keeps ONE encrypted generated-password row so the operator can read the kept volume (see `runbook/teardown-stuck.md` section 5 and the release note). A user is told something untrue, and the operator note contradicts the UI.
evidence: dialog text in shot 40; `internal/store/teardown.go` lines 45-77.
fix: one-line wording change, for example "The data stays on the server disk and cannot be reattached from Infra Pilot. Infra Pilot will not show the password again." (or change the server to delete the row, but that removes the operator's recovery path). Release note already avoids the claim for users and states the truth for the operator.

[PROD-P2] severity: low (existing Node behaviour, not a Plan 2 regression)
where: canvas node badge, `ServiceNode.tsx:68-140`
problem: "Online" (green) means "has at least one deployment", so a service whose only deployment is queued, building or failed is also "Online". Creating a service queues a deployment at once, so a brand-new service is green before anything runs. Without a worker (or after a failed build) it stays green.
evidence: after creating `cache`, `api`, `store` with no worker, all three show Online (19b) while their deployments were `queued` (psql).
fix: later UI polish: derive the badge from the newest deployment status. Not for this cutover; mention if a failed build confuses the user.

[PROD-P3] severity: low
where: create-project dialog, `ProjectsPage.tsx:578` (`maxLength={PROJECT_NAME_MAX}`)
problem: the API and the UI counter count characters (code points), but the HTML `maxLength` counts UTF-16 units, so a name of emoji stops at 50 emoji while the rule allows 100. Typing more than 100 plain characters is cut silently with no message.
evidence: typing 100 emoji produced a 50-emoji project name (06, 07); typing 101 `x` left 100.
fix: drop `maxLength` and rely on the existing validity check plus a message ("Names are 2 to 100 characters"); or leave as is (cosmetic). Not blocking.

[PROD-P4] severity: low
where: new services on the canvas
problem: every new service is created at position (0, 0), so a second and third service sit exactly on top of the first (the canvas shows one node and "3 services") until the user drags them apart. The Node API did the same (the `positionX` default is 0) and the old UI had the same blind spot, so this is not a regression, but the first thing a new user sees after "Create Database" is nothing new.
evidence: three nodes with identical bounding boxes (19b, `getBoundingClientRect` all `500,345`).
fix: later: offset each new node or call the existing `POST /projects/:id/canvas/auto-layout` (the Go API has it; the UI does not use it). Backlog.

[PROD-P5] severity: low
where: sign-in 429 message and rate limit
problem: "Wait a moment and try again" does not say how long. The API sends `Retry-After: 60` but the bucket refills one attempt every 6 s, so the header overstates the wait and the UI ignores it anyway. Shared behaviour for register and login.
evidence: shot 11; raw response `429 retry-after=60`.
fix: none needed; optionally show "Try again in a few seconds".

[PROD-P6] severity: info
where: checklist wording
problem: Step 5 item 3 expects 404 for `/deployments?serviceId=<other user's id>`; the Go API returns `200 []`. Update the checklist line to "empty list" so the Verifier does not log a false failure.

[PROD-P7] severity: info
where: dev stack Connection banner
problem: in development the banner says "...in development it is also available on localhost" and shows Local URL and CLI; in production those rows disappear and only the internal host remains, which only resolves inside the project network. The release note says so. No change.

Nothing silent or raw: every error I triggered showed a sentence (bad env name, ssh URL, 7/73-byte password, 429, API down, wrong credentials, expired session).

## Behaviour changes against the brief (item 3 of the role brief)

| Change | Visible and understandable? |
|---|---|
| Forced re-login at cutover | Yes: login page with "Your session expired, please sign in again." Release note first paragraph |
| Masked env values | Yes: dots and an eye icon, hint text "Changes apply to the next deployment" |
| Managed DB password and Connection tab | Yes: masked rows, Reveal, Copy; old databases say "Redeploy to generate a password" (the Plan 1 Mongo warning still applies and is repeated in the note) |
| No public DB ports | Yes: banner "not exposed to the internet" |
| Ownership 404 | Yes: "That item no longer exists. Reload the page." |
| SSE logs | Yes: badge, notices, Retry |
| Delete | Yes, apart from PROD-P1 |
| Password and name limits | Yes: messages name the limit |

## Release note

Written: `docs/superpowers/release-note-plan2.md` (user section plus a ten-point operator section). Every figure was checked against code or the walkthrough: 15 minute access token and 30 day refresh (`auth/jwt.go`, `auth/refresh.go`), 8-72 byte passwords and 100 character names (`api/auth.go`, `api/projects.go`), 5 quick login attempts then 429 (`api/ratelimit.go`: burst 5, one per 6 s), 5 streams per user and 20 per deployment, hourly renewal (`api/stream.go`), 8 KiB line cut (`api/logs.go`), 5,000 lines (`lib/logStream.ts`), 100 history lines, 5 minute hidden-tab close, ports bound to 127.0.0.1 and only in development (`config.go`, `runtime/docker.go`). Correction made to the earlier stub: the nginx `TRUSTED_PROXIES` and `X-Forwarded-For` items are not part of this cutover (the dev UI talks to the API directly; Staff plan-end finding), so they now point at Plan 3.

## Verdict (product view)

GO for the cutover, conditional on: the release note is shown to the user before they are signed out; PROD-P1 wording fixed or consciously accepted (the dialog states something untrue about the password; a one-line UI change, no data impact); the "verify at cutover" list above is run with the real worker (deploy, redeploy, container checks, teardown). Nothing I found blocks.

RESULT: APPROVE (with PROD-P1 as a follow-up fix before or right after the cutover)

## Cleanup

Chrome (own profile under the scratchpad, deleted), Go API (8102, 19102) and Vite (5192) stopped; container `prod18_pg` removed (no prune); `cp-ui/node_modules/.vite` and the extracted `git archive` copy removed; the seeded Redis stream key deleted; the throwaway secrets and logs deleted; ports 8102, 5192, 25460, 9392, 19102 free; other containers untouched. `df`: 12 GiB free before and after. Only files written in the repo: this file and the release note; `git status` otherwise shows the known untracked files.

# Plan 2 Task 17 - Verifier report (commits abb5017, effa11b, 5b0306d; HEAD = 5b0306d)

RESULT: PASS (every requested check passes; two low-severity observations, none blocking)

## Method
Independent of the Engineer's 49 checks (fresh scripts in the session scratchpad `.../scratchpad/v17v`). Headless Google Chrome over CDP (real mouse and key events, Network and Fetch domains for the request log and for holding, failing or faking single DELETE requests). UI: Vite on 5185 from `git archive HEAD cp-ui` (`VITE_API_BASE=http://localhost:8095`). Throwaway `postgres:16` (tmpfs, 127.0.0.1:25438) migrated with `go run ./cmd/migrate up`. Go API built from HEAD on 8095 (APP_ENV=development, random JWT_SECRET and ENV_ENCRYPTION_KEY, dev Redis 6380 only for the stream ticket, metrics 19095). Fresh user, one project, 8 app services and 12 managed databases seeded; deployments and AES-GCM encrypted password env rows inserted by SQL (the API refuses to set the reserved password keys). Not touched: the Go worker, the dev DB, the Node API 8080, encrypt-env, Temporal. df before 14 GiB free, after 13 GiB.

## Static and suite checks
| Check | Result |
|---|---|
| `cd cp-ui && npm run lint` | PASS: 0 errors, 5 warnings (same five react-hooks/exhaustive-deps warnings as before; none new) |
| `npm run build` | PASS: `built in 1.70s` (chunk-size warning only) |
| `npm run check:logic` | PASS: `ok: 57 checks passed` |
| `cd control-plane-go && go test -race -count=1 -p 2 ./internal/api/` | PASS: `ok infrapilot/internal/api 76.868s` |

## Browser and API checks
| # | Check | Result |
|---|---|---|
| 1 | Env eye reveals lazily | PASS |
| 2 | Redis Connection tab: host, reveal, encoding, Local URL | PASS |
| 3 | Mongo Connection tab (same, plus User row) | PASS |
| 4 | Deployment selection: stopped / failed newest, never deployed | PASS |
| 5 | Delete dialog, app service | PASS |
| 6 | Delete dialog, managed DB: two explicit buttons, nothing preselected | PASS |
| 7 | Cancel, backdrop, Escape send nothing | PASS |
| 8 | purgeData literal in request and in outbox payload | PASS |
| 9 | Double click sends one DELETE | PASS |
| 10 | Escape, backdrop and Cancel blocked while pending | PASS |
| 11 | Network error and 500 keep the dialog open, retry works | PASS |
| 12 | 404 counts as success | PASS |
| 13 | Node and edges vanish, no full-page loader | PASS |
| 14 | No further polls or log requests after delete | PASS |
| 15 | Deleted service hidden from /deployments, logs, stream ticket (404) | PASS |
| 16 | Bare DELETE of a managed DB returns 400 purge_choice_required | PASS |
| 17 | Password never in a request URL, page URL, console, localStorage | PASS |

### 1. Env eye PASS
`app1` has env `SECRET_TOKEN`. Env Vars tab open: 0 `GET ...?reveal=true`, text shows only `••••••••`, secret text absent. Click the eye (`title="Show value"`): exactly 1 GET `/services/<id>/env?reveal=true` (200), secret shown. Hide: secret gone from the DOM, no new request. (Re-show issued one more GET: EnvVarSection was not changed by Task 17, so this is existing behaviour.)

### 2-3. Connection tab values PASS
Special password `p@ss:w/rd#?%+&= x` (seeded encrypted in the throwaway DB):
- `redisA` (deployed, hostPort 20001): rows `host svc-<serviceId>`, `port 6379`, `password p@ss:w/rd#?%+&= x` (revealed, plain), `url redis://:p%40ss%3Aw%2Frd%23%3F%25%2B%26%3D%20x@svc-<id>:6379`, `local redis://:p%40ss%3Aw%2Frd%23%3F%25%2B%26%3D%20x@localhost:20001`.
- `mongoA` (hostPort 20002): rows host, `port 27017`, `user admin`, password, `url mongodb://admin:p%40ss...%20x@svc-<id>:27017/?authSource=admin`, `local ...@localhost:20002/?authSource=admin`.
- `redisB` (deployed, hostPort NULL): host, port, password `plainSimplePw123`, url; no `local` row (4 rows only).
- Password is fetched only by `GET /services/<id>/env?reveal=true`; it appears in no URL.

### 4. Deployment selection PASS
- `mongoStopNew` (older deployed hostPort 20003, newest deployed but `stoppedAt` set, hostPort 20004): Connection uses the older live one: local URL port 20003, not 20004.
- `redisFailNew` (older deployed 20005, newest `failed`): uses the older (local 20005).
- `redisStopOnly` (only deployment stopped): "Not running." + "Deploy the service to see connection details.", no rows, no alert.
- `redisFailOnly` (only a failed deployment): "Not running.", 0 reveal requests.
- `redisNever` (no deployment): "Not running.", 0 reveal requests.

### 5-8. Dialog PASS
- App service (`app6`): buttons `Delete service` and `Cancel` only; focus on Cancel. Cancel, backdrop click and Escape each close it; DELETE requests seen by the Fetch hook: none.
- Managed (`redisKeep`): buttons `Delete database and its data`, `Delete service, keep data volume on disk`, `Cancel`; 0 checked / pressed / selected inputs; focus on Cancel. Connected services text: "1 service is connected to this one and may break: app4." Cancel, backdrop and Escape: dialog closed, 0 DELETEs, 0 `teardown.requested` rows.
- Keep click: request `DELETE /services/<redisKeep>?purgeData=false` (202), outbox payload `{"purgeData": false, "serviceId": "<redisKeep>"}`.
- Purge click (`mongoPurge`): `DELETE /services/<id>?purgeData=true`, payload `{"purgeData": true, "serviceId": "<mongoPurge>"}`.
- App service (`app7`): `DELETE ...?purgeData=false`, payload `purgeData: false`.
- Final outbox: one `teardown.requested` row per deleted service (8 rows for 8 deletes, including the 404 case's separate API delete); no duplicate rows.

### 9-10. Double click and pending PASS
`app7` with the DELETE held by Fetch: CDP clickCount 1 then 2, plus two synchronous `.click()` calls: the browser issued exactly 1 DELETE. Pending dialog: confirm button reads "Deleting…" and is disabled, Cancel disabled. Escape while pending: dialog stays. Backdrop click: stays. Cancel click: stays, DELETE count still 1. After release: dialog closed, node gone, total DELETEs for `app7` = 1.

### 11. Error and 500 PASS
- `app8`, connection refused on the DELETE: dialog stays open with alert "Cannot reach the server. Check your connection and try again.", node still on the canvas, buttons enabled; retry sends a second DELETE, succeeds, dialog closes, node gone.
- `redisErr` (managed), faked 500 `{"error":"internal_error"}`: dialog stays, alert "Something went wrong on our side. Try again in a moment.", both choices enabled; retry with purge sends `?purgeData=true` again, succeeds.

### 12-13. 404 and canvas PASS
`app2` was deleted server-side by an API call first (202), then the stale UI deleted it: response 404, no error toast or dialog alert, dialog closed, node gone, status toast "Deleting... the service stops within a minute...". `app11`/`app12` pair (edge between them): 3 edges -> 2 edges and node gone within the first 30 ms sample after the click; states over 1.5 s sampled every 30 ms: `3/true/false` then `2/false/false` (edges / node exists / "Loading canvas" visible): the full-page loader never appeared. The other node of the pair stayed.

### 14. No more requests PASS
`app11` had a non-terminal (building) deployment and its Logs tab open. Before the delete the page made 8 requests mentioning the service or deployment (deployments GET, stream-ticket POST, stream GET). From the DELETE onward and for 10.5 s afterwards: 0 requests mentioning the service id or deployment id (no deployments poll, no log or ticket request). Same result for the first run with `app9`.

### 15. Hidden after delete PASS
With the owner's token: `GET /deployments?serviceId=<deleted>` -> 200 `[]`; `GET /deployments` does not contain the service or deployment; `GET /logs/deployments/<dep>` -> 404 `{"error":"deployment not found"}`; `POST /logs/deployments/<dep>/stream-ticket` -> 404 `{"error":"deployment not found"}`; `GET /services/<id>/env` -> 404 `service_not_found`; the project canvas no longer lists it.

### 16. Bare delete PASS
`DELETE /services/<mongoBare>` -> 400 `{"error":"purge_choice_required"}`; `?purgeData=` (empty) -> same 400; `?purgeData=maybe` -> 400 `invalid_purge_data`; bare DELETE of a redis service -> 400 `purge_choice_required`. The service still exists afterwards (`GET .../env` 200) and no outbox row was written.

### 17. Password leakage PASS
Scanned for the special password (plain, `encodeURIComponent`, `encodeURI`) and all other seeded passwords and the env secret: request URLs (every request the page made, all methods), page URL, console messages (console API, exceptions, Log domain) and localStorage / sessionStorage: 0 hits. `localStorage` keys: `cp_token`, `cp_refresh` only. API and Vite logs contain none of the passwords (grep count 0).

## Observations (not failures)
1. Stale first render when switching services: when the previous panel was on the Connection tab and another service is opened, the first render mounts `ConnectionSection` (key = new service id) with the previous service's deployments before the reset effect runs, so a `?reveal=true` GET fires for the new service although its Connection tab is not shown (observed: +2 GETs under StrictMode, 1 in production; a flicker was not observed). The password goes only to the owner's page and is not displayed, so no leak; it is an unnecessary reveal call and an audit-log entry ("env revealed"). Resetting `deployments` and `activeTab` in the same render (or keying the panel by service id) would avoid it.
2. StrictMode in the Vite dev build doubles the effect, so a Connection tab open shows 2 reveal GETs (dev only).
3. While the password is loading the URL row shows `redis://:@host:6379`; its Copy button is disabled until the password arrives (by design in the diff).

## NOT verified
- Reveal endpoint failing (the "Could not load the password" alert) and a reveal that returns no password key ("Not available"): covered by `check:logic` only, not exercised in the browser.
- Real worker teardown (container, route and volume removal; the outbox row was inspected, never consumed), a real managed container with the shown host name `svc-<id>` actually resolving, and `connectedNames` for a service with several links beyond the one- and zero-link cases seen.
- Keyboard focus trap inside the dialog (Tab order), screen readers, mobile layout, other browsers, a production Vite build in the browser.
- The background canvas refresh toast when the refresh fails ("Could not refresh the canvas").

## Cleanup
Chrome (own profile) and the Go API (8095, 19095) and Vite (5185) stopped; container `vfy17_pg` removed with `-v` (no prune); ports 8095, 5185, 9338, 25438, 19095 free; other containers (cp_*, t23-mongo-b-*) untouched; no Redis keys were written by me except the throwaway deployments' stream tickets (random keys, short TTL). Scripts and binaries live only in the session scratchpad; the extracted UI copy was removed. No repository code changed, nothing pushed. `git status` shows untracked `task-17-product.md`, `task-17-security.md`, `task-17-staff.md` from other reviewers; the only file I wrote is this one.

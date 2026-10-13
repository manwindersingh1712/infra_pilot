# Plan 2 Task 17 - Round 2 Verifier report (commits 805d133, 2f04cdc, 1c25f76; HEAD = 1c25f76)

RESULT: PASS (every requested check passes; two low-severity observations, none blocking)

## Method
Own CDP scripts (not the Engineer's, not round 1's), headless Google Chrome, real mouse and key events via `Input.dispatch*`, Network and Fetch domains for the request log and for holding, failing or faking single requests. Clipboard: `Browser.grantPermissions` (clipboardReadWrite, clipboardSanitizedWrite) and `navigator.clipboard.readText()` in the page. UI: Vite on 5188 from `git archive HEAD cp-ui` (`VITE_API_BASE=http://localhost:8098`, dev build, so StrictMode doubles effects). Throwaway `postgres:16` (tmpfs, 127.0.0.1:25448) migrated with `go run ./cmd/migrate up`; Go API built from HEAD on 8098 (APP_ENV=development, random JWT_SECRET and ENV_ENCRYPTION_KEY, dev Redis 6380 only for stream tickets and the seeded log lines, metrics 19098). One user, one project, 12 app services and 13 managed databases; deployments and AES-GCM encrypted password env rows inserted by SQL (the API refuses the reserved password keys). Special password used everywhere: `q'u o$HOME`id`"z;&|(x)` (single quote, space, `$`, backtick, double quote, `;`, `&`, `|`, parentheses). Not touched: the Go worker, the dev DB, the Node API 8080, encrypt-env, Temporal, Docker prune. df before 14 GiB free, after 14 GiB.

## Static and suite checks
| Check | Result |
|---|---|
| `cd cp-ui && npm run lint` | PASS: 0 errors, 5 warnings (the same five react-hooks/exhaustive-deps warnings as round 1) |
| `npm run build` | PASS: `built in 1.38s` (chunk-size warning only) |
| `npm run check:logic` | PASS: `ok: 65 checks passed` |

## Browser and API checks (pass/fail, evidence)
| # | Check | Result |
|---|---|---|
| 1 | Connection tab masks Password, URL, Local URL, CLI by default | PASS |
| 2 | Plain password nowhere in document text, attributes, outerHTML, input values while masked | PASS |
| 3 | Reveal secrets / Hide secrets | PASS |
| 4 | Copy puts the REAL value on the clipboard (while masked) | PASS |
| 5 | Copy failed on a rejected write and when `navigator.clipboard` is missing | PASS |
| 6 | CLI row quoting round-trips through `sh` exactly (redis and mongo) | PASS |
| 7 | Reveal failure shows error + Retry (blocked and 500) | PASS |
| 8 | No-password service | PASS |
| 9 | `Deploying…` vs `Not running.` | PASS |
| 10 | No reveal GET for a different service on switching | PASS |
| 11 | Delete dialog two-step purge (first click sends nothing, 5 s revert, Cancel disarms) | PASS |
| 12 | Second click sends exactly one DELETE `purgeData=true`, one outbox row true; Keep sends false | PASS |
| 13 | Focus trap (Tab, Shift+Tab), Escape restores focus | PASS |
| 14 | Pending state | PASS |
| 15 | Panel switched mid-delete, success and failure | PASS |
| 16 | Refresh failure shows both notices | PASS |
| 17 | Node and edges disappear with no full-page loader | PASS |
| 18 | Regression: env eye lazy reveal | PASS |
| 19 | Regression: log stream opens with seeded Redis lines | PASS |
| 20 | Regression: token refresh | PASS |

### 1-3. Masking and Reveal PASS
`redisA` (deployed, hostPort 20001), Connection tab, default state. Row texts (`data-field` rows): `password` = `••••••••••`; `url` = `redis://:••••••••••@svc-<id>:6379`; `local` = `redis://:••••••••••@localhost:20001`; `cli` = `redis-cli -u 'redis://:••••••••••@localhost:20001'`. `mongoA`: `user admin`, masked password/url/local and `mongosh 'mongodb://admin:••••••••••@localhost:20002/?authSource=admin'`. `redisB` (hostPort NULL): no `local` row; `cli` uses the internal host, masked.
Leak scan while masked (`document.documentElement.outerHTML` + `body.innerText` + every attribute value of every element + all input/textarea values) for the plain password, `encodeURIComponent`, `encodeURI`, `q%27u`, `HOME%60id`, `q'\''u`: 0 hits, also after pressing Copy on all four rows. Clicking Reveal secrets: all four rows show the plain values (password `q'u o$HOME`id`"z;&|(x)`, url `redis://:q'u%20o%24HOME%60id%60%22z%3B%26%7C(x)@svc-<id>:6379`, cli `redis-cli -u 'redis://:q'\''u%20o%24...'`), toggle reads `Hide secrets`. Hide: all rows masked again and the leak scan returns 0 hits (plain value gone from the DOM).

### 4-5. Copy PASS
With the rows masked, Copy on password, url, local, cli: clipboard (`readText`) equals the real value exactly for each, button reads `Copied!`. Page's `navigator.clipboard.writeText` replaced by a rejecting promise: Copy shows `Copy failed`, back to `Copy` after about 2 s. With `navigator.clipboard` undefined: `Copy failed`. (Informational: `Browser.setPermission denied` for clipboard-write has no effect in headless Chrome and the write still succeeded, so a real permission denial was not exercised, only the rejected promise.)

### 6. CLI quoting PASS
The clipboard text of the CLI row was written to a file in the scratchpad (no side effects, no real redis-cli or mongosh invoked). A `sh` script defined `show() { printf '%s' "$2"; }`, replaced the leading `redis-cli` with `show` and `eval`'d the line: the single argument printed equals the Local URL byte for byte, `decodeURIComponent` of its password part equals the original password (including `'`, space, `$`, backtick, `"`, `;`, `&`, `|`, parentheses). `sh -c 'f(){ echo $#; }; f -u <quoted url>'` printed `2` (exactly `-u` and one URL). The mongo CLI row (`mongosh '...'`) round-trips the same way.

### 7. Reveal failure PASS
`redisRevealFail`, `?reveal=true` blocked by Fetch (ConnectionRefused): alert `Could not load the password. Cannot reach the server. Check your connection and try again.` with a Retry button; rows: host, port, password = `Could not load`; no URL, Local URL or CLI row, no Reveal toggle. Retry while still blocked: exactly one more reveal request, alert stays. Unblocked then Retry: alert gone, masked URL/Local URL/CLI rows appear. Separate run with a faked 500 `{"error":"internal_error"}`: `Could not load the password. Something went wrong on our side. Try again in a moment.` + Retry.

### 8-9. No password, Deploying PASS
`redisNoPw` (live, no password env row): rows host, port, password only; password text `Redeploy to generate a password`; no Copy button on it; no URL, Local URL or CLI row; no Reveal toggle. `redisDeploying` (newest deployment `building`) and `redisQueuedOnly` (mongo, `queued`): `Deploying…` + `Connection details appear when the deployment finishes.`, 0 reveal GETs. `redisNone` (no deployment): `Not running.` + `Deploy the service to see connection details.`, 0 reveal GETs.

### 10. Switching services PASS
From a panel on the Connection tab of `redisA`, then switching (close, open another node) to `app2` (an app service; tabs Deployments, Env Vars, Logs, no Connection), `redisDeploying`, `redisNone`, `redisB`, `redisNoPw`: 0 reveal requests of any method for the new service each time. The tab resets to Deployments on opening. Round 1's observation 1 (unnecessary reveal GET for the new service) is fixed.

### 11-12. Delete dialog, purge and outbox PASS
- Managed dialog: buttons `Delete database and its data`, `Delete service, keep data volume on disk`, `Cancel`; initial focus on Cancel; nothing preselected.
- First purge click: label becomes `Click again to erase the data permanently`, dialog stays open, 0 DELETE requests. Still armed at about 2.8 s. Label reverted at 4.75 s after my timer start (about 5.05 s after the click), 0 DELETE. A click after the revert re-arms (0 DELETE). Escape on the armed dialog: closed, 0 DELETE, 0 outbox rows.
- Cancel disarms: arm, Cancel: dialog closed, 0 DELETE; reopened dialog shows the unarmed label `Delete database and its data`.
- `mongoPurge`: first click 0 DELETE, second click exactly one `DELETE /services/<id>?purgeData=true` (202); outbox: one `teardown.requested` row `{"purgeData": true, "serviceId": "<id>"}`.
- `redisKeep`: arm purge, then click Keep: exactly one `DELETE ...?purgeData=false` (202), one outbox row `{"purgeData": false, ...}`.
- Final outbox: 7 `teardown.requested` rows for 7 distinct deleted services, no duplicates (purgeData true only for mongoPurge).

### 13. Focus trap PASS
Initial focus on Cancel. Tab sequence: Cancel -> purge -> keep -> Cancel -> purge (wraps, never leaves the dialog). Shift+Tab: purge -> Cancel (wraps) -> keep -> purge -> Cancel. With focus blurred to body: Tab enters the first button (purge), Shift+Tab enters the last (Cancel). Escape closes the dialog and `document.activeElement` is the panel's `Delete service` button (the opener); the same after Cancel on an app-service dialog.

### 14. Pending PASS
`app6` DELETE held by Fetch: `role=status` text `Deleting “app6”… this takes a few seconds. Keep this window open.`, dialog `aria-busy=true`, confirm button reads `Deleting…` and is disabled, Cancel disabled, `document.activeElement` is the dialog. Tab and Shift+Tab keep focus on the dialog; Escape and a backdrop click do nothing; still exactly one DELETE in flight. After release: dialog and panel closed, node gone, `Deleting…` notice shown, `DELETE ?purgeData=false` 202.

### 15. Panel switched mid-delete PASS
The modal backdrop blocks real clicks on nodes, so the switch was done with a synthetic `node.click()` on the canvas node (real browser event, not through the backdrop).
- Success: `app7` DELETE held, panel switched to `app8`: dialog unmounted at once. After release: `app7` node gone, panel still open on `app8` (headers `p17r2|app8`, Deployments tab present), no stuck dialog, exactly one DELETE for app7.
- Failure: `app9` DELETE held, panel switched to `app10`, then the held request failed (ConnectionRefused): toast `Could not delete “app9”. Cannot reach the server. Check your connection and try again.`, `app9` node still on the canvas, panel for `app10` stays open, no dialog.

### 16-17. Refresh failure and canvas PASS
`app11` (edge to `app12`) deleted with every GET `/canvas` request failing. Sampled every 30 ms (edges / node exists / "Loading canvas" visible): `4/true/false` then `3/false/false`; the loader text never appeared, the node and its edge vanished in the first sample after the click. After 2.5 s both notices were visible together: `Deleting… the service stops within a minute (a running build can take up to 3 minutes).` and `Could not refresh the canvas. Cannot reach the server. Check your connection and try again.`, stacked at y=751 and y=824 (no overlap).

### 18-20. Regressions PASS
- Env eye (`app1`, `SECRET_TOKEN`): Env Vars tab open: 0 `GET ...?reveal=true`, secret absent from text. Click the eye (`title="Show value"`): exactly 1 GET (200), secret shown. Hide: secret gone from text.
- Log stream (`app2`, deployed): seeded two Redis stream lines (`XADD logs:stream:<dep>`) before opening the Logs tab: `POST /logs/deployments/<dep>/stream-ticket` 200 and `GET .../stream?ticket=` 200 (a first POST was aborted by the dev-build StrictMode remount); both seeded lines shown; a line then published on `logs:pubsub:<dep>` arrived in the open stream.
- Token refresh: access token forged with an `exp` one hour in the past (HS256, throwaway secret); API probe returned 401. Page load: `GET /projects/<id>` 401 -> `POST /auth/refresh` 200 -> `GET /projects/<id>` 200 -> canvas 200, new `cp_token` stored, nodes rendered. (A first attempt reusing an already rotated refresh token correctly got 401 from the API; it was my script's reuse, repeated with a fresh login.)

### Password leakage (extra)
Over the whole connection run: no request URL or body contained the password (plain, `encodeURIComponent`, `encodeURI`); the page console had no messages or exceptions.

## Observations (not failures)
1. Reopening the same service after closing the panel from its Connection tab fires an unnecessary `GET ?reveal=true` for that same service (2 GETs in the dev build because of StrictMode, 1 in production) although the tab has reset to Deployments (observed: 2 GETs after open on Connection, +2 after reopen). It is the first render with the old active tab and the old deployments before the reset effect runs; for a different service `deploymentsOf` now filters the stale rows, so round 1's observation 1 is fixed. No leak (the response is only held in the unmounted component), only a needless reveal call and audit-log entry.
2. The Vite dev build doubles effects (StrictMode): 2 reveal GETs and an aborted first stream-ticket POST are dev-only artefacts.

## NOT verified
- A real browser clipboard permission denial (`Browser.setPermission` has no effect in headless Chrome); only a rejected `writeText` promise and a missing `navigator.clipboard` were exercised.
- Panel switch mid-delete with a real user path: not possible with a mouse because the modal backdrop covers the canvas, so a synthetic `click()` on the node was used.
- Real worker teardown (the outbox rows were inspected, never consumed), a real managed container resolving `svc-<id>`, screen readers, mobile layout, other browsers, a production Vite build in the browser.

## Cleanup
Chrome (own profiles under the scratchpad, deleted), Go API (8098, 19098), Vite (5188) stopped; container `vfy17r2_pg` removed (no prune); `cp-ui/node_modules/.vite` and the extracted `git archive` copy removed; the seeded Redis stream keys deleted (stream tickets are random short-TTL keys); ports 8098, 5188, 9348, 25448, 19098 free; other containers (cp_*, t23-mongo-b-*) untouched. `cp-ui/dist` (git-ignored) was produced by `npm run build`. No repository code changed, nothing committed or pushed; the only file I wrote in the repo is this one (git status shows other reviewers' untracked files and the known untracked ones).

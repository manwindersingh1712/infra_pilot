# Plan 2 Task 16 - Verifier report, round 2 (commits 80104df, 95275b7, 709711d, 4af70b0; HEAD = 4af70b0)

RESULT: PASS (every requested check passes; observations below, none blocking)

## Method
Independent of round 1 and of the Engineer (fresh scripts in the session scratchpad `.../scratchpad/v16b`). Headless Google Chrome over CDP (real mouse events, Network and Fetch domains for the request log and for failing or holding single requests). UI: Vite on 5183 from `git archive HEAD cp-ui` (`VITE_API_BASE=http://localhost:8093`, `node_modules` symlinked to the repo's). Throwaway `postgres:16` (tmpfs, 127.0.0.1:25437) migrated with `go run ./cmd/migrate up` (00001-00004). Go API built from HEAD (`go build ./cmd/api`) on 8093, APP_ENV=development, random JWT_SECRET and ENV_ENCRYPTION_KEY, dev Redis 6380, metrics 19094. Fresh user, one project, 8 services each with a queued deployment (no worker). Log lines seeded with `redis-cli XADD logs:stream:<dep>` and `PUBLISH logs:pubsub:<dep>` into the throwaway deployments' own keys only, and deleted afterwards. Not touched: the Go worker, the dev DB, the Node API on 8080, encrypt-env. df before: 16 GiB free (40% used); after: 15 GiB (42%).

## Static and suite checks (tree = HEAD, tracked files clean)
| Check | Result |
|---|---|
| `cd cp-ui && npm run lint` | PASS: `0 errors, 5 warnings` (the same five react-hooks/exhaustive-deps warnings as round 1; none in logStream.ts, useLogs.ts or LogViewer.tsx) |
| `npm run build` | PASS: `built in 2.63s` (chunk-size warning only) |
| `npm run check:logic` | PASS: `ok: 49 checks passed` (round 1: 41) |
| `cd control-plane-go && go build ./... && go vet ./... && golangci-lint run` | PASS: `0 issues.` |
| `go test -race -count=1 -p 2 ./internal/api/ ./internal/config/` | PASS: `ok infrapilot/internal/api 74.970s`, `ok infrapilot/internal/config 1.366s` |

## Browser checks
| # | Check | Result |
|---|---|---|
| 1 | `end` flood (MaxDuration 200 ms): no 0 ms loop, gaps grow to the cap | PASS |
| 2 | 7,000 seeded lines -> exactly 5,000 DOM rows, responsive, Clear works | PASS |
| 3 | 429 on the ticket POST: Retry-After readable cross-origin, panel waits that long | PASS |
| 4 | Real background tab (`Target.createTarget background:true`): Paused, 0 ticket POSTs until activated | PASS |
| 5 | Cap hint: absent after non-consecutive failures, shown with 5 other streams open | PASS |
| 6 | Replace on return (stubbed visibility) | PASS |
| 7a | Restart by SIGTERM and by kill -9: merge without loss or duplicates, scroll kept, Clear kept | PASS |
| 7b | Service deletion: terminal message, no Retry button, no further ticket POSTs | PASS |
| 7c | `<script>` / `<img onerror>` / `<svg onload>` as text | PASS |
| 7d | Expired and corrupt token refresh, stream opens | PASS |
| 7e | Long line truncation | PASS |

### 1. `end` flood PASS
`vbapi-fast` built with `go build -overlay` (only `StreamConfig{Shutdown: streamCtx, MaxDuration: 200 * time.Millisecond}` changed in a copy of `cmd/api/main.go`; the repo file untouched, `git status` shows no tracked change). Panel open for 115 s: 10 ticket POSTs, 9 EventSource opens. Gaps between consecutive ticket POSTs (ms):
```
0, 745, 1990, 2438, 7561, 8502, 30052, 21559, 23575
```
The first `0` is the React StrictMode double mount in the Vite dev build (the first POST is cancelled by the effect cleanup; the first and second POST carry the same millisecond), not a loop: after it every gap is non-zero and the gaps follow the jittered ceilings 1, 2, 4, 8, 16, 30 s (each draw from the upper half) and reach the 30 s cap. Badge timeline over the 115 s: `Connecting…@3 | Live@3 | Reconnecting…@4 | Live@4 | Reconnecting…@4 | Live@6 | Reconnecting…@6 | Live@9 | ... | Live@76 | Reconnecting…@76 | Live@100 | Reconnecting…@100`: the badge shows Reconnecting between opens (not silent) because the streams are younger than 10 s, as `planAfterEnd` specifies. No alert text. Server returned 200 for every ticket.

### 2. 7,000 lines PASS
Panel Live on a deployment, then 7,000 `PUBLISH`es (`L00000`..`L06999`) in one pipeline. Last line visible 4.9 s after the publish. DOM of the log pane: `{"n":5000,"first":"04:59:28 OUT L02000","last":"04:59:33 OUT L06999","uniq":5000}`. Three more lines (`X1..X3`) keep the count at 5,000 (`first` moves to `L02003`, `last` is `X3`). Main thread responsive: Runtime.evaluate round trip 0 ms and a `requestAnimationFrame` callback within 0 to 1 ms after the burst. Clear: `1 row`, text "Logs cleared. New lines will appear here.", done in 29 ms; a later live line (`after-clear`) appears alone.

### 3. 429 on the ticket POST PASS
The ticket route's bucket (burst 20, 1 token/s per client address) was kept empty with a curl-style loop from node while the page opened. The page's own ticket POSTs got real 429s; alert text "Too many requests. This panel is retrying shortly.", badge "Connecting…". From inside the page (origin `http://localhost:5183`, API origin `http://localhost:8093`, so cross-origin) with the bucket empty: `fetch(...).then(r => r.headers.get('Retry-After'))` gave `{"s":429,"ra":"60","origin":"http://localhost:5183"}`: the header is readable (it is exposed by 80104df). Request log of the panel, relative to its first 429:
```
429@0.0  429@0.0 (StrictMode second mount)  429@2.1 (my in-page fetch)  200@60.0
```
After the hammer stopped, the panel sent no ticket POST for 60.0 s (the Retry-After value, also the clamp limit), then got 200 and the badge read Live. Without a readable header it would have retried after the 30 s default.

### 4. Background tab PASS
Foreground tab logged in; a second tab created with `Target.createTarget {background: true}` on the project page (attached over its own CDP socket). `document.visibilityState` there is `hidden`, `document.hidden` is `true`. The service and its Logs tab were opened with `element.click()`. After 6 s hidden: badge "Paused", the panel text "Paused while this tab is in the background.", ticket POSTs 0, EventSource opens 0, no Retry button; the only API requests were the project, canvas and deployments GETs. `/json/activate/<id>` then made the tab visible: Live after 89 ms, ticket POSTs 1, EventSource 1.

### 5. Cap hint PASS
Interception with `Fetch.failRequest` of EventSource `.../stream?ticket=` requests (the ticket POST succeeds each time), against the 200 ms API so that every pass opens a stream (resetting the count):
- Attempt pattern `ppffpffp` (p = pass, f = refuse), 70 s: attempts seen `ppffpffp`; alerts observed (polled every 100 ms): none. No hint after four refusals that were never three in a row.
- Control `fffp...`, 25 s: the hint appeared after the third consecutive refusal: "Your log streams may be at the limit (5 per account). Close other log panels, or wait a few minutes: this panel keeps retrying." and was gone after the next successful open.
- Real cap: 5 tabs Live on one deployment, a 6th tab: it stayed "Connecting…" (6 ticket POSTs, 5 EventSource attempts), the same hint at 3 s; tabs 1-5 stayed Live; after closing tab 1 the 6th became Live (23 s after it opened) and the alert disappeared.

### 6. Replace on return PASS (visibility stubbed)
Headless Chrome cannot hide a foreground tab, so `document.visibilityState` and `document.hidden` were overridden with getters and `visibilitychange` dispatched. This stands in for a hidden tab; the real hidden behaviour at page load is check 4.
- A: "hidden", 31 s, "visible" with the ticket POST held by `Fetch`. Hidden: badge Live, 0 new POSTs. Visible: 1 ticket POST pending, badge Live, 0 new EventSource opens yet; a line published during the hold appeared (old stream alive). After releasing the ticket: 1 new EventSource, the API log shows exactly 1 more finished stream handler (the old one closed), `after-replace` arrived once, rows `["OUT pre-1","OUT during-hold","OUT after-replace"]`, 0 duplicates, badge timeline `["Live"]` (no flicker).
- B: the same with the ticket POST failing: badge stays Live, no alert, 0 new EventSource, 0 old streams closed, a new line (`after-failed-replace`) still arrived through the old stream.

### 7a. Restarts PASS
60 history lines (`hist-00`..`59`) plus 5 live lines (65 rows), pane scrolled to `scrollTop 300`.
- SIGTERM, two lines (`gapA-1`, `gapA-2`) written to the Redis stream during the outage, API back after 3 s: badge `Live | Reconnecting… | Live`; rows 65 -> 68 (the two gap lines and the live line `live-after-A`), each `hist-NN` exactly once, each gap line once, all rows unique; scroll `{top:300, scrollHeight:1593}` before, `{top:300, scrollHeight:1666}` after.
- Click Clear (list: "Logs cleared..."), kill -9, `gapB-1` written during the outage: after reconnect the list is exactly `["OUT gapB-1","OUT live-after-B"]`: the cleared older lines did not come back.

### 7b. Service deletion PASS
Separate one-service project. `DELETE /services/:id` -> `202 {"deleted":true}`; the open stream stays Live. API kill -9 and restart: the first ticket POST `ERR_CONNECTION_REFUSED`, the second 404. After 2,168 ms: badge "Disconnected", alert "This deployment no longer exists.", Retry button absent. Ticket POSTs: 2 at the moment of the 404, still 2 sixty seconds later; EventSource opens after the terminal state: 0. Timeline `["Live","Reconnecting…","Disconnected"]`.

### 7c. Script injection PASS
History lines and live lines `<script>window.__xss=1</script>`, `<img src=x onerror="window.__xss=2">`, `</div><svg onload="window.__xss=3">` and a combined one. All render as literal text in the rows; `window.__xss` is `null`; `img, script, svg` elements inside the pane: 0; the pane's `innerHTML` has no `<img`.

### 7d. Token refresh PASS
After the stream was Live, `cp_token` was overwritten and the API kill -9'd and restarted to force a new ticket request.
- Corrupt (`garbage.not.ajwt`): `POST stream-ticket 401`, `POST /auth/refresh 200`, `POST stream-ticket 200`, `GET .../stream?ticket=* 200`; badge Live, no alert, `cp_token` replaced by a valid JWT, still on the project page.
- Expired (HS256 JWT signed with the throwaway secret, `exp` 10 min ago; the API answers 401 to it): on the first run the refresh already happened in the 500 ms before the restart (the 2 s deployment poll hit 401 first), so the ticket POST was already 200. Re-run recording from the moment of the overwrite: `GET /deployments?serviceId=... ERR_CONNECTION_REFUSED`, `POST stream-ticket 401`, `POST /auth/refresh 200`, `POST stream-ticket 200`, `GET .../stream?ticket=* 200`; Live, no alert, new valid `cp_token`, not signed out.

### 7e. Long lines PASS
20,001-byte (history) and 30,001-byte (live) ASCII lines and 6,000 x "€" (18,000 bytes): rendered ASCII rows are 13 (time and source label) + 8,192 + 14 (`...[truncated]`) = 8,219 characters; the euro row is 2,759 characters (cut on a rune boundary, no U+FFFD). Three rows carry the suffix. No row, pane or document horizontal overflow.

## Observations (not failures)
- StrictMode in the Vite dev build issues two ticket POSTs at mount (the first is aborted); a production build will not. It shows as a 0 ms first gap in check 1.
- Rate limit for the ticket route: 20 burst, 1 token per second per client address. The retry after a 429 uses the server's `Retry-After: 60` fully (clamped to 1-60 s).
- A Vite run with `node_modules` symlinked wrote its dependency cache under `cp-ui/node_modules/.vite` (ignored by git; no tracked or untracked file shows up in `git status`).
- In the project canvas all eight service nodes sit at the same position, so a mouse click always opens the topmost service (svc7). This is a property of my seeded data, not of the change; element `.click()` was used to select others where needed.
- `git status` at the end lists a `task-16-round2-security.md` file that is not mine (another reviewer's report); the only file I wrote is this one.

## NOT verified
- A real 1 h MaxDuration cycle (200 ms and 6 s builds stood in for it); the `end reason=shutdown` delay only through SIGTERM.
- Real transition of a visible tab to hidden (30 s stale reconnect and the 5 minute pause were exercised only through the stubbed `visibilityState`, check 6); the 5 minute close timer was not waited out.
- The browser `online` event; a dead session (refresh token also invalid) giving the sign-out outcome.
- ClickHouse-tier history, real worker-produced logs (no worker run), a production Vite build in the browser (dev server used), other browsers, the mobile screen, proxy behaviour (nginx buffering, heartbeats).
- Memory or frame-rate profiling of the 5,000-row list beyond the responsiveness probes above.

## Cleanup
Chrome (headless, own profile), Vite 5183 and the API (8093, metrics 19094) stopped; container `vfy16b_pg` removed with `-v` (no prune); ports 8093, 5183, 9337, 25437, 19094 free; the throwaway deployments' Redis keys (`logs:stream:*`, `logs:flushed:*`, `logs:pubsub:*`) deleted and a scan for the seeded id prefix finds none. Other containers (cp_*, t23-mongo-b-*) untouched. Scripts and binaries live only in the session scratchpad. No repository code changed, nothing pushed.

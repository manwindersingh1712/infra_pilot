# Plan 2 Task 16 - Verifier report (commits 059b1e5..f70715f, HEAD = f70715f)

RESULT: PASS (every requested check passes; observations listed below, none blocking)

## Method
Independent of the Engineer's run (fresh scripts in the session scratchpad `.../scratchpad/v16`). Headless Google Chrome over CDP (real mouse events, Network domain for the request log, a MutationObserver on the badge sampled with timestamps). UI: Vite on 5180 from `git archive HEAD cp-ui` (`VITE_API_BASE=http://localhost:8090`). Throwaway `postgres:16` (tmpfs, 127.0.0.1:25436) migrated with `go run ./cmd/migrate up` (00001-00004 applied). Go API built from the repo (`go build ./cmd/api`) on 8090, APP_ENV=development, random JWT_SECRET/ENV_ENCRYPTION_KEY, dev Redis 6380, metrics 19093. Fresh user, one project, 8 services each with a queued deployment (POST /services/:id/deploy; no worker). Log lines seeded with `redis-cli XADD logs:stream:<dep>` + `PUBLISH logs:pubsub:<dep>` into the throwaway deployments' own keys only, and deleted afterwards. The Go worker, dev DB, Node API 8080 and encrypt-env were not touched. df before: 18 GiB free (38% used); after: 17 GiB.

## Static checks (cp-ui, tree = HEAD, tracked files clean)
| Check | Result |
|---|---|
| `npm run lint` | PASS: 0 errors, 5 warnings (the same five react-hooks/exhaustive-deps as before, none in the new files) |
| `npm run build` | PASS: `built in 1.35s` (chunk-size warning only) |
| `npm run check:logic` | PASS: `ok: 41 checks passed` |
| `grep -rn socket.io cp-ui/src` | PASS: no matches (exit 1) |
| `go test -race -count=1 -p 2 ./internal/api/` (repo untouched) | PASS: `ok  infrapilot/internal/api  80.610s` |

## Browser checks
| # | Check | Result |
|---|---|---|
| 1 | Ticket POST 200, `/stream?ticket=` is an EventSource, no `token=` | PASS |
| 2 | Live lines arrive in order | PASS |
| 3a | API restart by SIGTERM: Live -> amber Reconnecting -> Live, merge, scroll kept | PASS |
| 3b | Restart by kill -9, Clear not undone, outage lines merged | PASS |
| 4 | ~2 minute outage: capped backoff, never gives up, Retry works | PASS |
| 5 | 6s MaxDuration build: no badge flash across `end` reconnects | PASS |
| 6 | 6 tabs: 5-stream hint, recovery after closing one | PASS |
| 7 | Over-8 KiB line truncated, no overflow | PASS |
| 8 | Service deletion: terminal message, no further ticket POSTs | PASS |
| 9 | Expired and corrupted cp_token refresh through the shared client | PASS |
| 10 | `<script>` / `<img onerror>` render as text | PASS |

### 1. Ticket and EventSource PASS
Open the project, click the service, Logs tab. CDP request list for the stream:
```
POST Fetch  /logs/deployments/<dep>/stream-ticket            200   (preceded by an OPTIONS 204 preflight)
GET  EventSource  /logs/deployments/<dep>/stream?ticket=<43 chars>  200
```
Resource type is `EventSource`; the URL carries only `ticket=`, no `token=`. The API log has 0 lines containing `ticket=`. Badge reads "Live"; with an empty Redis stream the meta line says `Source: clickhouse (0 total)`, with seeded lines `Source: redis`.

### 2. Live order PASS
60 history lines seeded (`hist-00..59`), page opened (60 rows), then `live-1..live-5` via XADD+PUBLISH 250 ms apart. Tail of the list: `hist-59, live-1, live-2, live-3, live-4, live-5`.

### 3a. SIGTERM restart PASS
Page with 65 rows, log pane scrolled up (scrollTop 300, not following the tail). API stopped by SIGTERM (stopped in 65 ms), two lines (`gapA-1`, `gapA-2`) added to the Redis stream during the outage, API restarted after 4 s.
- Badge timeline from the observer: `Live@-2213ms | Reconnecting…@51ms | Live@6275ms` (no Disconnected). Computed colour while down: `rgb(245, 158, 11)` (amber); the alert text read "Cannot reach the server. Check your connection and try again." and is gone after reconnect.
- Rows 65 -> 67: the two gap lines added after `live-5`, no duplicates (every `hist-NN` appears exactly once), live lines still flow afterwards (`live-after-A` shown).
- Scroll position: `{top:300, scrollHeight:1593}` before, `{top:300, scrollHeight:1642}` after (it stayed, the content grew by the two lines).
### 3b. kill -9 PASS
Click Clear (list empty), then `kill -9` the API, `gapB-1` seeded during the outage, restarted after 3.5 s. Badge `Reconnecting…@35ms | Live@5448ms`. After reconnect the list is exactly `['gapB-1']`: the 65+ older lines that the history event carries stayed cleared; a later live line (`live-after-B`) appended.

### 4. ~2 minute outage PASS
API stopped by SIGTERM for 135 s. Ticket POSTs sent during it: 9, all `ERR_CONNECTION_REFUSED`. Gaps between attempts (s): `1.9, 3.9, 4.7, 11.0, 27.0, 26.0, 19.7, 25.0`, which fit the jittered ceiling 1,2,4,8,16,30,30,30 (each delay drawn from the upper half) and never exceed 30 s. At 135 s: badge "Reconnecting…" amber `rgb(245, 158, 11)`, amber notice, Retry button present, no Disconnected at any point (timeline `Live | Reconnecting… | Live`). Clicking Retry while the API was still down made an immediate attempt (ticket POST within 700 ms, badge stays Reconnecting…). After starting the API and clicking Retry (before the next scheduled attempt) the badge was Live in 69 ms; notice and Retry button gone; the line written during the outage (`during-outage`) was in the list.

### 5. Short MaxDuration PASS
`apibin-short` built with `go build -overlay` (only `StreamConfig{Shutdown: streamCtx, MaxDuration: 6 * time.Second}` changed; the repo file untouched, `git status` shows no tracked changes). The badge was sampled every 50 ms for 40 s: 744 samples, 0 not "Live"; badge timeline `["Live"]` only; 6 ticket POSTs and 6 EventSource opens (one per `end`). A live line every 2 s (19 lines): all 19 present once each plus the history line, 20 rows, no duplicates, no notice.

### 6. Six tabs PASS
Six tabs, same user, same deployment. Tabs 1-5 Live. Tab 6 stayed "Connecting…" (never connected) and after about 8 s showed the notice "You have 5 log streams open; close one and this panel will reconnect." (6 ticket POSTs, 5 EventSource opens in tab 6 at t+20 s; tabs 1-5 unaffected). After closing tab 1 (`/json/close`), tab 6 became Live after 5.3 s with no notice; tabs 2-5 stayed Live.

### 7. Long line PASS
Seeded both as history and as live: a 20,001-byte ASCII line, a 30,001-byte line, and 6,000 x "€" (18,000 bytes). Rendered: ASCII lines are 8,192 bytes + `...[truncated]` (8,206 chars); the euro line is cut on a rune boundary (8,190 bytes of "€" + suffix = 8,204 bytes, no U+FFFD). No row or span `scrollWidth > clientWidth`, log pane and page have no horizontal overflow.

### 8. Service deletion PASS
Separate one-service project. `DELETE /services/:id` -> 202 `{"deleted":true}` (the open stream stays Live, unchanged). Then `kill -9` + restart of the API to force a reconnect: first ticket POST `ERR_CONNECTION_REFUSED`, the second 404; badge "Disconnected" (red), notice "This deployment no longer exists.", Retry button present. Ticket POSTs: 2 right after, still 2 sixty seconds later; 0 EventSource opens after the 404. Timeline `Live | Reconnecting… | Disconnected`.

### 9. Token refresh PASS
After the stream was Live, `cp_token` was overwritten (corrupt: `garbage.not.ajwt`; expired: a real HS256 JWT with `exp` 10 min in the past, signed with the throwaway secret; the API answers 401 to it), then the API was kill -9'd and restarted so the hook fetched a new ticket. Both variants, identical request sequence:
```
POST /logs/deployments/<dep>/stream-ticket 401
POST /auth/refresh 200
POST /logs/deployments/<dep>/stream-ticket 200
GET  /logs/deployments/<dep>/stream?ticket=* 200
```
Badge Live, no notice, `cp_token` replaced by a valid JWT, still on the project page (not signed out).

### 10. Script injection PASS
History and live lines `<script>window.__xss=1</script>`, `<img src=x onerror="window.__xss=2">`, `</div><svg onload="window.__xss=3">`, and a combined line. Rows show the literal text (`innerText` contains the tags); `window.__xss` stayed `undefined`; no `img`, `script` or `svg` element inside the log pane; `innerHTML` has no `<img`.

## Observations (not failures)
- Tab 6 in check 6 reads "Connecting…" rather than "Reconnecting…" while the cap holds it out (by design: `everConnected` false).
- After a service is deleted the already open stream is not closed by the server; the user learns of it only at the next reconnect (the first `end`, restart, or network blip). Matches the spec as I read it; not judged.
- The 5-stream hint appears only after 3 failed opens (about 8 s), as coded.
- Mid-run, `git status` once listed `cp-ui/src/lib/logStream.ts` as modified and a moment later (after `git diff` showed an empty diff) it was clean again, with no content change; I wrote nothing there (index stat refresh most likely).

## NOT verified
- A real 1 h MaxDuration cycle (a 6 s build stood in for it); `end` with `reason=shutdown` was exercised only through SIGTERM, which is where the 1-3 s reconnect delay applies.
- Hidden-tab behaviour (5 minute close, 30 s stale reconnect, `visibilitychange`) and the browser `online` event: headless tabs were not hidden.
- A dead session (refresh token also invalid) giving the terminal sign-out outcome; 429 `Retry-After` handling of the ticket route (only covered by `check:logic`).
- ClickHouse-tier history (lines older than the Redis TTL), real worker-produced logs (no worker run), other browsers, the mobile screen.
- Proxy behaviour (nginx buffering / heartbeats through a proxy).

## Cleanup
Chrome, Vite and both API processes stopped; container `vfy16_pg` removed with `-v` (no prune); ports 8090, 5180, 9336, 25436, 19093 free; seeded Redis keys (`logs:stream:*`, `logs:flushed:*`) for all throwaway deployments deleted. A `reaper_*` container from `go test` (testcontainers) may linger briefly. Scripts and binaries live only in the session scratchpad. No repository files changed; `git status` shows only the known untracked files, the known `.DS_Store` modification, and this report.

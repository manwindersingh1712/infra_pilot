# Plan 2 Task 11: Product review (commits 4132954, abad375, 5ff53dc, bfbc495)

Scope: user-facing impact of the stream-ticket endpoint, the log history endpoint and the SSE log stream with limits. Read: `cp-ui/src/hooks/useLogs.ts`, `cp-ui/src/components/LogViewer.tsx` (the only consumer, mounted in `ServiceDetailsPanel.tsx:954` with `key={latestDeploymentId}`), `control-plane/apps/api/src/socket-handlers/logs.ts` and `routes/logs.ts` (Node, read only), `internal/api/{stream,logs,ratelimit,server}.go`, and the Task 15/16 plan text. No UI was run: Task 11 is backend only and the UI still talks Socket.io until Task 16.

## What the UI does today (baseline)

- Socket.io only. One socket per `useLogs`; `subscribe:logs` returns `logs:history` (100 lines) then `logs:new` per line. Socket.io reconnects by itself with its JWT in the handshake, so reconnects are invisible and free.
- The UI never calls `GET /logs/deployments/:id`. Search, `sources`, `before/after`, "load more" do not exist in the UI. The history endpoint changes therefore cannot break any screen today.
- Build and runtime logs share one `deploymentId` and one stream (same `LogEntry` with `source` stdout/stderr/system). After a redeploy `key={latestDeploymentId}` remounts the viewer, so the old stream closes and a new one opens. Nothing special for build vs runtime.
- Header badge: green "Live" / red "Disconnected"; red error strip shows `error`; a "Source: redis|clickhouse (N total)" label shows `meta`.
- "Clear" empties the array only.
- The Node socket path had no ownership check; any logged-in user could subscribe to any deployment id. The Go path returns 404 for foreign ids. Intended improvement, no user impact.

## Findings

[PROD-1] severity: high
where: Task 16 hook (plan lines ~4448-4505), `useLogs.ts`
problem: the reconnect budget is 5 attempts with delays 2, 4, 8, 15, 15 s, about 44 s total, and the counter only resets on `onopen`. An API restart or deploy that takes longer than that (the Plan 2 cutover itself, or a laptop that slept) leaves the panel on "Connection lost. Reopen the logs to retry." forever. There is no Retry button and no listener for coming back online or the tab becoming visible. With Socket.io this recovered by itself.
evidence: stop the Go API for 60 s with the Logs panel open; after about 44 s the hook stops. Restarting the API does not bring the stream back.
fix: retry indefinitely with capped backoff (max 15-30 s) and jitter, and reset the counter on `window.online` and `visibilitychange`. Keep a visible "Retry" button. Show "Reconnecting..." (amber), not red "Disconnected", while retrying.

[PROD-2] severity: medium
where: Task 16 hook, `history` handler (`setLogs(data.logs)`)
problem: every (re)connect replaces the whole list with the last 100 lines. After the 1 h `max_duration` cut, an API restart or any blip, a user who has 5000 lines on screen (and is scrolled up reading) loses all but 100 and the scroll position jumps. It also undoes "Clear". With Socket.io the reconnect re-subscribed and replaced too, but reconnects were rarer.
evidence: watch a chatty service, scroll up, wait for a reconnect.
fix: on a reconnect, merge: append only history lines not already present (the existing dedupe on timestamp+message), keep earlier lines, and keep the clear state. Only replace on the first history of a mount.

[PROD-3] severity: medium
where: Task 16 hook, `onerror` and the `end` event
problem: the hook does not listen for `end`. When the server sends `end` (max_duration after 1 h, shutdown on API restart) and closes, the browser raises `onerror`, which the hook treats as a failure: badge flips to "Disconnected", `attempts` goes to 1 and the wait is 2 s, then a new ticket. It works, but the user sees a red flash every hour and a 2 s gap. Lines from the gap are covered by `history` only if fewer than 100 arrived.
Overnight watching: the 1 h cap is acceptable ONLY if the client reconnects silently. It is not acceptable if the panel stays disconnected (see PROD-1). The cap itself is fine for solo developers (bounded resources, tokens revalidated hourly, the deployment ownership is re-checked on every reconnect).
fix: `source.addEventListener("end", ev => { reason = JSON.parse(ev.data).reason; source.close(); reconnect immediately (no backoff, do not count an attempt) })`. For `shutdown` use a short jittered delay (1-3 s) because the server is going away. Show a muted "Reconnecting..." not an error. Keep `onerror` for real failures.

[PROD-4] severity: medium
where: Task 16 hook, error classification; `apiFetch` throws `"<status>: <body>"`
problem: the hook catches everything and shows "Connection error", then retries 5 times. Cases that need a different message and NO retry:
- ticket POST 404 `deployment not found`: the service/deployment was deleted (soft-deleted ids give the same 404). Retrying 5 times is pointless. Show "This deployment no longer exists."
- ticket POST 429 `too_many_requests` (Retry-After 60): show "Too many reconnects, retrying in a minute", wait for Retry-After, do not burn the 5 attempts.
- ticket POST 401 after refresh failed: apiFetch already redirects to /login.
- stream-side failures are invisible: `EventSource` never exposes the status code of a non-200 stream response. A 429 `too_many_streams`, 401 `invalid_ticket` (60 s TTL expired, or the ticket was spent by a first attempt) and 503 all arrive as `onerror` with `readyState` CLOSED. The hook cannot tell them apart.
fix: classify the ticket POST by status (parse the leading number of the Error message or have apiFetch throw an `ApiError` with `status`). For stream-side failures that happen without an `onopen`, show "Could not open the log stream" with the hint "Other log tabs may be using all 5 slots" after the second consecutive failure (see PROD-5).

[PROD-5] severity: medium
where: stream caps (5 per user, 20 per deployment; `streamSlots`), `429 too_many_streams`
problem: a user with several tabs, or who flips quickly through services, can hit 5. Today one panel = one stream (the viewer is only mounted for the selected service and `key` changes close the old one), so normal single-tab use stays under 5. The risk is ghost slots: a laptop that sleeps or a Wi-Fi drop leaves a half-open TCP connection. The server only notices when a write fails; 15 s pings go into the kernel buffer and a dead peer can take minutes to be detected. During that time the slot is still held while the client reconnects, so after a few network blips with 3-4 tabs open the user sees 429 on the new stream. EventSource hides that 429 (PROD-4) and `Retry-After: 5` is ignored by browsers, so the UI looks like a generic connection error that retries and gives up in 44 s.
fix (UI): on repeated stream failures without `onopen`, say "Too many log streams open (limit 5). Close other log tabs." Close the stream when the tab is hidden for a while (optional, frees slots), and reopen when visible. Do not retry tightly: each attempt costs 2 rate-limit tokens (ticket + stream).
fix (server, optional, for the backlog): lower TCP keepalive on the listener (for example 30 s) so dead peers free slots in about a minute.
Note for support: the cap is in memory per API instance, so it multiplies if the API is ever scaled out.

[PROD-6] severity: medium
where: shared per-IP bucket (`streamLimit`, burst 20, one token per second, keyed on the TCP peer, X-Forwarded-For not trusted)
problem: the ticket POST and the stream GET share the bucket, so each connect costs 2 tokens: about 10 connects in a burst and 30 per minute sustained per address. Direct to `:8080` in dev this is plenty. Behind nginx, a load balancer or a shared NAT/office the peer address is the proxy's, so ALL users share one bucket; a single tab in a reconnect loop, or a few users after an API restart (every open panel reconnects at once, with no jitter), can starve everyone's logs for up to a minute (`Retry-After: 60`). It does not touch login (separate buckets), which is good.
fix: add jitter to reconnects (PROD-1). Cutover item: decide whether the API will sit behind a proxy, and if so add a trusted-proxy setting before relying on this limiter, or document the shared bucket.

[PROD-7] severity: low
where: 100-line history, 8 KiB per message cap (`capEntry`, suffix `...[truncated]`)
problem: truncation is visible in-line (a line ends with `...[truncated]`), which is honest, but there is no way to see the rest. Build output (npm progress, minified bundle errors, JSON blobs, stack traces on one line) can exceed 8 KiB and the cut is silent apart from the suffix. The 100-line history is the same as Node; a long build with more than 100 lines before the user opens the panel shows only the tail, with no "load earlier" because the UI never uses the HTTP history endpoint.
fix: release-note line. Optional follow-up: "Load earlier" button using `GET /logs/deployments/:id?before=<first timestamp>&limit=500`, and a muted tooltip on truncated lines.

[PROD-8] severity: low
where: `GET /logs/deployments/:id` behaviour changes (no UI consumer today)
items, all acceptable and none visible in the current UI:
- `search` over 256 bytes is a 400, and `%`, `_`, `\` are literal (Node treated them as wildcards, so a search for `100%` used to match everything). A future search box must show the 400 text, or cap its input at 256.
- invalid `sources` (anything outside stdout, stderr, system, or more than 3 items) is a 400; Node accepted any string. A future filter must send exactly those names.
- `logs` is always an array (never null): safe for the UI. `meta.totalCount` is omitted when unknown: LogViewer already guards with `!== undefined`, but its type says `totalCount?: number` and the history event meta now omits it the same way. Good. `hasMore` is false when totalCount is nil, so a future "load more" cannot rely on `hasMore` for the Redis tier.
- the stream's `history` event meta has no `count` or `hasMore` (only source and optional totalCount), the same as the old `logs:history`. Fine.
- `before`/`after` must be RFC 3339, otherwise 400 with `invalid after: expected an ISO-8601 timestamp`.

[PROD-9] severity: low
where: error text shown to users
problem: `apiFetch` throws `"429: {\"error\":\"too_many_requests\"}"`. The Task 16 hook hides this behind "Connection error", which is better than the raw text but not actionable. Server error codes for this task are snake_case machine codes (`invalid_ticket`, `too_many_streams`, `unavailable`, `internal_error`) plus `deployment not found`. None is meant for display.
fix: a small map in the UI from code or status to a sentence (see Task 16 list).

[PROD-10] severity: low
where: duplicate suppression (`timestamp+message`)
problem: unchanged from Socket.io, but worth knowing: two identical lines in the same millisecond collapse into one. The Go side does not add ids, so `Last-Event-ID` resume does not exist and history replay is the only recovery. Acceptable.

No critical findings. Nothing in Task 11 breaks the current UI (it keeps using Socket.io until Task 16; the history endpoint is not used). Note the Node API must keep Socket.io until the UI is switched, so Tasks 14 (cutover wiring) and 16 must ship together; a Go-only API with the old UI has NO live logs at all (the Go API has no Socket.io).

## Reconnect and ticket semantics (answers)

- A ticket is single-use and bound to `<user>:<deployment>`, TTL 60 s. Every (re)connect must POST `/stream-ticket` first. The Task 16 hook does that and ALSO calls `source.close()` in `onerror`, which is what prevents the browser's built-in EventSource retry from looping with a dead ticket. That close must stay (add a code comment). With a non-200 response the browser would not retry anyway; with a network drop it would retry the same URL and get `401 invalid_ticket`, so closing is required.
- Heartbeat: `: ping` every 15 s is a comment, ignored by EventSource, invisible to users. The client cannot detect a silent half-open connection from it either (no event fires). Optional: a client watchdog that reconnects if nothing (including pings, which are not observable) arrives; with EventSource this is not possible, so rely on server-side `end`/TCP errors. Leave it.
- `end` reasons: `max_duration` means reconnect now; `shutdown` means the server is going down, reconnect after a short jittered delay.
- 1 h cap overnight: acceptable provided the UI reconnects invisibly (PROD-2, PROD-3). Without that fix, a user returns in the morning to a stale red panel, and with PROD-1 it stays dead.

## Exactly what Task 15 must do (client and cross-cutting)

1. `apiFetch` must throw a typed error carrying `status` and the parsed `error` string, not only `"<status>: <text>"`, so the hook can branch on 404/429/401 without string matching. Keep the old message format for existing callers.
2. Export `API_BASE`, `getToken`, `apiFetch` (the plan does). A ticket POST that gets 401 must refresh once and retry; the SSE stream URL is not a Bearer call so the refresh logic never runs on it, so the POST must come immediately before `new EventSource`.
3. Add a shared `humanizeApiError(status, code)` used by Tasks 16 and 17: 404 deployment not found, 429 too_many_requests ("Too many requests, wait a minute"), 429 too_many_streams, 401 invalid_ticket, 503 unavailable ("Server is restarting, retrying"), 500 internal_error ("Something went wrong on our side").
4. Respect `Retry-After` on 429 in `apiFetch` consumers (surface it on the typed error).

## Exactly what Task 16 (SSE hook) must do

1. Per connect: POST ticket, then open `EventSource` with the ticket in the URL; never the JWT. (As planned.)
2. In `onerror`: `source.close()` first, set state to "reconnecting", schedule a new ticket. (As planned; keep.)
3. Listen for `end`: close, reconnect immediately for `max_duration`, jittered 1-3 s for `shutdown`, do not count against the attempt budget, do not show an error.
4. Retry forever with capped exponential backoff and jitter; reset on `online` and `visibilitychange`; remove the 5-attempt dead end; add a manual "Retry" button as well.
5. Do not retry on ticket 404 (show "deployment no longer exists"); respect 429 `Retry-After`; show a "limit of 5 open log streams" hint after repeated opens that fail without `onopen`.
6. Merge history on reconnect instead of replacing; keep user "Clear"; preserve scroll position (LogViewer only auto-scrolls within 50 px of the bottom, which already holds).
7. Three badge states: "Live" (green), "Reconnecting..." (amber), "Disconnected" (red, only after a hard stop). Replace `error` strings with human text.
8. Guard `JSON.parse` of event data in try/catch (one bad event must not kill the handler).
9. Clean up on unmount and on `deploymentId` change: set `closed`, clear the timer, close the source (as planned). Also close the stream when the tab has been hidden for a few minutes to free the per-user slot (optional).
10. Remove `socket.io-client` and verify no `socket.io` in `src` (as planned). Update `LogViewer` header: `meta.totalCount` label is still correct.
11. Manual check list for the step "Try it": restart API (panel recovers silently, no red state longer than the restart), kill API for 2 min (recovers when it returns), open 6 tabs on the same service (6th shows the stream-limit hint), wait through a `max_duration` (set `Stream.MaxDuration` to 30 s in a local run) and watch for a silent reconnect with the list preserved, a log line over 8 KiB shows `...[truncated]`.

## Exactly what Task 17 must do (related to this review)

Task 17 (env reveal, DB connection, delete) does not touch logs, but:
1. Service deletion makes the open Logs panel's deployment 404. The panel must close with the service (the selected service is cleared) or show "deployment no longer exists" and stop retrying (Task 16 item 5).
2. Use the shared error humanizer for the reveal/delete failures rather than the raw `status: body`.
3. After deletion, make sure nothing keeps an EventSource open (unmount the viewer before the DELETE returns).

## Release-note lines

- Live logs now use a standard one-way event stream instead of Socket.io. Logs reconnect on their own (new one-time ticket each time), after at most one hour a stream is renewed transparently.
- Each user can have up to 5 live log streams open at once (20 per deployment). Close extra log tabs if the panel says "too many log streams".
- A single log line is cut at 8 KiB and ends with `...[truncated]`. The live panel shows the last 100 lines on open.
- Logs are private to the deployment's owner. Opening another user's deployment id, or a deleted service's, shows "not found".
- Log history API: `search` is limited to 256 characters and now matches `%`, `_` and `\` literally; `sources` accepts only `stdout`, `stderr`, `system`.
- Upgrade note: the log panel needs the new UI; the old UI (Socket.io) will not receive live logs from the Go API.

## Cutover checklist lines

- [ ] Ship UI Tasks 15 and 16 in the same cutover as the Go API; the Go API has no Socket.io endpoint.
- [ ] `CORS_ORIGINS` includes the UI origin (the stream is a cross-origin EventSource without credentials; ticket POST needs Authorization in `Access-Control-Allow-Headers`, already allowed).
- [ ] If a proxy (nginx) fronts the API: keep `X-Accel-Buffering: no` honoured, set `proxy_buffering off`, `proxy_read_timeout` above 15 s (at least 75 s), HTTP/1.1; and decide on the shared per-IP rate bucket (peer address is the proxy; XFF is not trusted).
- [ ] cmd/api passes `Stream.Shutdown` so shutdown sends `end` with reason `shutdown`; keep the http.Server `WriteTimeout` for other routes (the stream overrides it per write).
- [ ] Ticket TTL is 60 s and Redis is the ticket store: Redis down means 503 `unavailable` on streams; the UI shows "retrying".
- [ ] In-memory caps and limiter hold for one API instance only; do not scale out without revisiting.
- [ ] Smoke: ticket 404 for a foreign id, a reused ticket gives 401 `invalid_ticket`, 6th stream gives 429 `too_many_streams`, restart the API and the open panel recovers.
- [ ] Nobody logs the query string at the proxy (the ticket is in the URL); redact `ticket=` in nginx access logs.

RESULT: CHANGES REQUESTED (for Task 16 and 15; Task 11 backend itself is fine for users, no blocking backend issue)

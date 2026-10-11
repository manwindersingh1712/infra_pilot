# Plan 2 Task 11 - Staff review (4132954, abad375, 5ff53dc, bfbc495)

Scope: `control-plane-go/internal/api/{logs,stream,server}.go` and tests, against spec §5/§6, backlog (SEC-1 Task 11, Engineer notes), plan Task 11 (lines 3266-3672).

Checks run (repo checkout, no edits): `go build ./...`, `go vet`, `golangci-lint run ./internal/api/...` (0 issues), `go test -p 2 -race -count=1 ./internal/api/` (ok, 72 s), stream/ticket tests `-race -count=5` (ok, no flakes), shutdown and max-duration tests `-count=20` (ok). Mutations ran in a `git archive` copy under the scratchpad; the repo is untouched.

## Answers to the questions

- **1 h max duration:** acceptable. It bounds a stream that outlives its auth: the ticket is checked once, so logout, refresh-family revocation or service deletion do not end an open stream. 1 h is a long window for that, but a shorter one (15-30 min) only adds reconnect churn against the 20-burst limiter. Keep 1 h, and revisit if token revocation must be immediate (then push a close on revoke). It is configurable.
- **Caps (5 per user, 20 per deployment):** 5 per user is sensible. 20 per deployment is dead by default: deployments are owner-only (no sharing), so the per-user cap of 5 always trips first. It is harmless, but it protects nothing. What is missing is a global cap: each stream holds a goroutine and a dedicated go-redis PubSub connection, so N users x 5 grows with user count (see STAFF-1).
- **`end` event:** acceptable and useful. EventSource ignores unknown events unless a listener is registered, so it is backward-safe. The plan's Task 16 hook does not listen for `end`: it works anyway because the server close fires `onerror`, the hook closes the source and reconnects with a fresh ticket after 2 s. Task 16 should add the listener only if it wants to skip that backoff. The spec names only `history` and `log`, so add `end` (reasons `shutdown`, `max_duration`) to §5/Task 16 text. Backlog already records it.
- **Deviations:** `{id}` path value, uniform 401 (better than the plan's 404: no oracle between "wrong deployment" and "bad ticket"), dropping `Connection: keep-alive` (correct; HTTP/2 forbids it), `ParseLogQuery` hardening, `logs` always an array (Node returns `[]` from the aggregator too), harness changes: all justified. Route shapes match Node (`/logs/deployments/:id`, `/logs/health`).

## Findings

[STAFF-1] severity: low
where: control-plane-go/internal/api/stream.go:55-100 (`streamSlots`, defaults 5/20)
problem: Per-deployment cap (20) can never trip with default settings because only the owner can open a stream (5 < 20). There is no global cap on concurrent streams, and each open stream costs a goroutine plus one dedicated Redis PubSub connection (`logs.Redis.Subscribe`). Registration is only limited per IP, so the total grows with user count.
evidence: reasoning from `deploymentOwned` + `acquire`; `TestStreamPerDeploymentCap` has to raise `MaxPerUser` to 10 to reach the deployment cap.
fix: add a `MaxTotal` (e.g. 500) checked in `acquire`, or drop the per-deployment cap and say why. Backlog for Plan 3 (quotas) is fine.

[STAFF-2] severity: low
where: control-plane-go/internal/api/server.go (stream route wrapped in `stream(...)`), stream_test.go
problem: No test proves the stream route itself is rate limited or that the limiter runs before the ticket is redeemed (the comment claims an over-limit request does not consume the ticket). Backlog SEC-1 (Task 11) asked for the stream route to be rate limited.
evidence: mutant removing `stream(...)` from `GET /logs/deployments/{id}/stream` SURVIVES (only the stream-ticket route has `TestStreamTicketRouteIsRateLimited`).
fix: add a test with `Stream.Burst = 1`: issue 2 tickets, GET stream with the first (200 held open), GET with the second returns 429 and the second ticket is still in `fakeTickets.m`.

[STAFF-3] severity: low
where: control-plane-go/internal/api/stream.go:248-251 (`case e, ok := <-live`), stream stop paths
problem: When `live` closes (the real `Subscribe` closes it on ctx cancel and when the Redis PubSub dies), the handler returns with no `end` event. On shutdown or max-duration, `ctx.Done` and the channel close can both be ready if the handler is busy in `send`, and `select` picks randomly, so the `end` event can be missing. A Redis PubSub failure silently ends a 200 stream (the client only sees EOF and reconnects, which is acceptable but invisible in logs).
evidence: no test closes `fakeLive.ch`. Mutant "ignore `!ok`" and mutant "return on `!ok`" both survive. I made `fakeLive` behave like the real one (close on ctx cancel) in the copy: 20 runs of the shutdown and max-duration tests all passed, so the race is narrow (the handler is normally parked in `select` and wakes on `ctx.Done` first). It needs the handler mid-write.
fix: on `!ok`, fall into the same classification as `ctx.Done` (`ctx.Err() != nil` -> end event; otherwise log Warn with deploymentId and return). Add a test where the live channel is closed by the fake.

[STAFF-4] severity: low
where: control-plane-go/internal/api/stream_test.go:439 `TestStreamHeartbeat`
problem: the heartbeat test uses a 20 ms interval, far below the 10 s default write timeout, so it cannot show that the ping re-arms the write deadline.
evidence: mutant "heartbeat write without `arm()`" SURVIVES. In production that mutant would end every idle stream about 10 s after the last event, because the previous absolute deadline would have expired.
fix: set `Heartbeat = 50ms`, `WriteTimeout = 100ms`, read 3 pings. Small.

[STAFF-5] severity: low (UI, for Task 16 not this task)
where: plan Task 16 hook `history` handler -> `setLogs(data.logs)`
problem: `history` is the OLDEST 100 hot lines (`XRANGE - + COUNT 100`; same as Node). After the 1 h `end` reconnect, or any reconnect, the hook replaces its list with those 100 oldest lines and loses everything shown since, and with a chatty deployment (stream up to 10 000 lines) a gap appears between those 100 and the live lines. Node had the same gap on a first connect, but it never reconnected after 1 h.
evidence: `logs/redis.go` `Read` uses `XRangeN(... "-", "+", count)`; `Aggregator.Query` returns it unchanged.
fix: Task 16: merge history into existing lines with the existing timestamp+message dedupe instead of replacing, or ask the aggregator for the newest N (XREVRANGE) in a later task. Record in backlog.

[STAFF-6] severity: low (operability)
where: backlog Engineer note, Task 14/18
problem: ticket and stream share one limiter bucket (burst 20, 1 token/s per IP); every connect spends two tokens, and a 429 on the cap burns the ticket (redeem must precede the cap check because the cap key comes from the ticket). Behind nginx all users share one IP until `TRUSTED_PROXIES` lands, so about 10 connects per burst and 0.5 connect/s in total. A reconnect storm (API restart, all UIs reconnect, each after its 2 s backoff) will see 429s.
evidence: server.go routes; stream.go order (redeem, ownership, acquire).
fix: already in the Engineer note. Make Task 14 block on `TRUSTED_PROXIES` or raise the stream burst for the proxy case. The UI hook should treat 429 as retryable with backoff (it does, via the generic error path).

## Mutation results (single-change mutants, `go test -run 'Stream|Ticket|Log|WriteEvent|CapEntry'`)

Killed: ticket deployment id not compared; prefix match; ownership re-check removed; ticket not redeemed at all; wrong-deployment answered 404; not-owned answered 404; Redeem non-ErrInvalid error mapped to 401; caps never released; release only on error paths (disconnect leak); release for user only (deployment slot leak); user cap not checked; deployment cap not checked; cap off by one; Retry-After dropped; write deadline removed; max duration removed; shutdown ignored; shutdown pre-check removed; shutdown reason wrong; `end` event removed for max_duration; subscription never released (fakeLive count); history cap and limit both removed; per-line cap removed (history and live); raw newlines in SSE data; ticket logged (raw query) and ticket not redacted; stream Cache-Control dropped; X-Accel-Buffering dropped; ticket-route limiter removed; stream behind JWT middleware; ticket not owner-bound or issued without ownership check; GET logs ownership removed; ILIKE escape removed (each of `\`, `%`, `_`); sources allow-list removed; sources count removed; search length cap removed; limit clamp removed; getLogs line cap removed; `logs: null` for empty; both Cache-Control settings dropped on the ticket route.

Survived:
- stream route limiter removed (STAFF-2) - real gap.
- heartbeat write without `arm()` (STAFF-4) - real gap.
- live channel closed ignored / returns silently (STAFF-3) - real gap.
- user id not compared (`HasSuffix(value, ":"+id)`): equivalent. `uid` is parsed from the ticket value itself, so the "user binding" is the ownership re-check on that uid; changing the compare to suffix-only changes nothing observable because ids contain no ':'.
- empty uid allowed: equivalent, `deploymentOwned("", ...)` is false, so the answer is still 401.
- history `[:min(...)]` removed alone, or `Limit: historyEntries` changed alone: redundant defences (each covers the other); removing both is killed.
- Cache-Control no-store on the ticket response removed from either place alone: redundant with the other (middleware plus handler); removing both is killed.
- write deadline not reset on exit (`defer SetWriteDeadline(zero)`): harmless, the connection ends with the handler; no test needed.
- ticket "after cap": reorder is not possible (the cap key comes from the ticket); not a mutant. A 429 burns the ticket (documented, STAFF-6).

## Concurrency review

- `streamSlots` uses one mutex, never held while calling out, so no lock ordering issue and no deadlock. Release is deferred right after a successful acquire, so every return path, including the Subscribe/Query failure paths, releases (tests cover disconnect, start failure, shutdown, max duration).
- `release` iterates a map literal of two pointers: fine (order is irrelevant).
- No goroutine leaks seen: `context.AfterFunc` stop is deferred, ticker stopped, Subscribe is tied to a ctx that is cancelled on every exit. `-race -count=5` is clean. Per-write deadlines mean a stalled client is dropped after 10 s; a half-open peer is also bounded by Go's default 15 s TCP keepalive.
- `total()` and `forDeployment()` are used only by tests but live in production code (lint is fine); move to a `_test.go` file if you want.

## Security-adjacent notes (Security owns the verdict)

- Ticket is never in logs: the request log middleware logs `r.URL.Path` only, stream errors pass through `redactTicket`; the metrics label uses the route pattern. Nginx query-string logging is in the Task 18 note.
- Uniform 401 `invalid_ticket` for all ticket problems; Redis/Postgres failures are 503/500 (SEC-1 satisfied).
- The `search` escaping applies to ClickHouse only (parameterised `message ILIKE ?` with `%`+search+`%`); the Redis tier ignores search (already in the Engineer's limitations). There is no ClickHouse-backed test showing `\%` is literal; it is standard ClickHouse LIKE escaping.

## Compatibility

- cp-ui today (`src/hooks/useLogs.ts`) uses Socket.io: `subscribe:logs` / `logs:history` / `logs:new` against the Node API and a JWT in the handshake. None of that exists in Go. Tasks 15/16 replace it with `POST .../stream-ticket` + `EventSource` and events `history`, `log`, (`end`). `LogViewer.tsx` reads only `logs, meta, isConnected, error, clearLogs`, so its contract stays. After Task 16 remove `socket.io-client`.
- Node shapes: `GET /logs/deployments/:id` returns `{logs, meta:{source,count,totalCount?,hasMore}}` identical to Node (Node's `totalCount: undefined` is omitted too). The 404 text `deployment not found` matches. Node did no ownership check, so Go is stricter by design (§5).
- `history` payload equals Node's `logs:history` (`{logs, meta:{source,totalCount}}`).

RESULT: APPROVE (0 critical, 0 high, 0 medium, 6 low). The lows are test gaps (STAFF-2/3/4) and follow-ups for Tasks 14/16 (STAFF-1/5/6); STAFF-2 and STAFF-3 are cheap enough to fix in this task.

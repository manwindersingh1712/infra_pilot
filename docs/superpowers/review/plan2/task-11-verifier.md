# Plan 2 Task 11 - Verifier report

Commits: 4132954, abad375, 5ff53dc, bfbc495 (HEAD b110025, branch go-migration). Tree clean except the known untracked files.

RESULT: PASS (functional). 0 blocking failures; 3 observations below.

## Suite (control-plane-go/)
- `go build ./...` OK; `go vet ./...` OK
- `golangci-lint run --allow-parallel-runners` -> `0 issues.`
- `gosec -quiet -exclude=G204 ./...` -> no findings
- `gitleaks git --redact` (repo root) -> `377 commits scanned ... no leaks found`
- `go test -race -count=3 -p 2 ./internal/api/ ./internal/tickets/ ./internal/logs/` -> api ok 209.9s, tickets ok 14.0s, logs ok 39.8s
- `go test -race -count=1 -p 2 ./...` -> every package ok, exit 0 (outbox ok, no Ryuk flake)

## Real end-to-end (git archive HEAD copy outside the repo; real Redis 7, Postgres 16, ClickHouse 24.8 via testcontainers; real TCP httptest server, plain http client; tokens/tickets redacted)
Test file was written only in the copy (zz_e2e_test.go). Config used: Heartbeat 2s, WriteTimeout 1.5s, Burst 100000.
- POST stream-ticket -> 200, `{"ticket":"<REDACTED>"}` (43 chars), `Cache-Control: no-store`.
- GET stream -> 200 `text/event-stream`, `Cache-Control: no-cache, no-store`; first frame `history` (seed-0..2, meta source redis); live `log` frame arrives for a line published afterwards; `: ping` after 2s (the configured value).
- Reuse of same ticket -> 401 `{"error":"invalid_ticket"}`. Ticket for X used on Y -> 401. Ticket after its service was soft-deleted -> 401.
- Empty / "garbage" / 43-char wrong / 5000-char / "%00" / "../x" / missing param -> all 401 with one identical body.
- User B asking for a ticket for A's deployment, missing id, "%00", "nul%00x", `x'y`, 500-char id -> 404 `{"error":"deployment not found"}`, identical bodies. History GET for foreign / malformed ids also 404 identical. Unauthenticated ticket/health -> 401.
- Hostile line `line1\nevent: evil\ndata: injected\r\nid: 9\n U+2028 ... U+2029 \n\n` arrived as exactly one frame (2 lines: event, data), JSON round-trips byte-identical.
- Slots: 5 streams open, 6th -> 429 `too_many_streams` with `Retry-After: 5`; closing one frees a slot and a new stream gets 200; after all closed slots = 0; Redis PUBSUB NUMSUB on the channel = 0; goroutines before=13, with 5 streams=48, after=13.
- Stalled client (raw TCP, 1 KB read buffer, never reads, ~8 KB lines published): slot released 1.6 s after start (WriteTimeout 1.5 s); goroutines 13 -> 10 (no leak).
- MaxDuration 1.5s -> `end` frame `{"reason":"max_duration"}`; Shutdown cancel -> `end` `{"reason":"shutdown"}`, body closes cleanly (err=nil); new stream after shutdown -> 503 `{"error":"unavailable"}`.
- Redis container stopped before redeem -> 503 `{"error":"unavailable"}`.
- Captured structured server log (82 lines, 16.7 KB): 19 tickets/tokens issued, 0 found; no `ticket=` text. The only stream error line: `redeem ticket: dial tcp [::1]:56421: connect: connection refused`.

## History endpoint (real ClickHouse)
search `%` -> only "100% done"; `_` -> only "a_b value" (so `axb value` not matched); `'` literal ok; `\` -> no rows. sources=stderr -> 1 row; stdout,system -> 4; bogus -> 400 "invalid sources: use stdout, stderr or system"; 4 sources -> 400. limit=2 -> newest 2; 99999 -> capped at 1000; -5 / abc -> default 100. Bad after/before -> 400 "invalid after: expected an ISO-8601 timestamp"; after > before -> 200 empty; 300-char search -> 400. Owner hot-tier JSON `{"logs":[{deploymentId,source,message,timestamp}],"meta":{"source":"redis","count":2,"hasMore":false}}`. `/logs/health` -> `{"activeLogStreams":1,"status":"healthy"}`.

Compared with Node (control-plane/apps/api/src/routes/logs.ts, services/log-aggregator.ts): same entry fields, same meta keys (source, count, totalCount omitted when absent, hasMore), same tiering/merge. Differences, all improvements: Node has no ownership check (any deployment id readable by anyone, unauthenticated), splices `search` into SQL with only quote doubling and does not escape `%`/`_`, accepts any `sources`, and returns 500 for a bad date or NaN limit; Go answers 400/clamps.

## Observations (not blocking)
1. [OBS-1, inherited from Node] The hot tier reads XRANGE `- + COUNT n`, i.e. the OLDEST n lines. For a stream with more than 100 lines in the 10 min window, the `history` event holds lines 1..100 and live starts at the newest, so lines 101..N are never delivered on a (re)connect. Node's readLogs has the same behavior; not a regression, but worth a backlog item (read the newest n with XREVRANGE and reverse).
2. [OBS-2] My e2e probe with id `..` returned `{"error":"not_found"}` (404, mux path cleaning) while other bad ids return `{"error":"deployment not found"}`. Bodies differ only for literal dot-segments, which never reach the handler; no ownership information leaks. Cosmetic.
3. [OBS-3] `streamLogs` returns 500 `internal_error` if the DB ownership lookup fails after the ticket was spent (ticket is burned; client simply requests a new one). By design.

## Dev environment (read-only)
- `go run ./cmd/migrate status`: 00001..00003 applied, `00004 00004_refresh_family.sql pending` (not run).
- `~/cp-logs/go-worker.log`: last line `flushed logs processed=0 errors=0` at 04:31; 0 error lines.
- Node API `curl localhost:8080/healthz` -> `{"ok":true} 200`.
- Repo not edited; only this file added (uncommitted). Throwaway e2e copy lives in the session scratchpad; testcontainers were removed by the test cleanup.

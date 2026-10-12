# Plan 2 Task 14 — Staff review

Range: 6dca7f5 (TRUSTED_PROXIES), 760040a (worker key fingerprint), 98497fe (cmd/api). Branch go-migration.
Reviewer: Staff. Against Task 14 text + AMENDMENTS (plan2) and the Task 14 carry-forwards in backlog.md.

Mechanical checks (control-plane-go/): `go build ./...` and `go vet ./...` clean; `golangci-lint run` (whole module): 0 issues;
`go test -race -count=1 ./cmd/... ./internal/config/... ./internal/api/...`: all ok. No go.mod/go.sum change. Disk 25 GiB free before runs.

I also checked two Go net/http behaviours in a scratch program (outside the repo, scratchpad `scratch14`), because the repo has no test for them:
with `ReadTimeout` and `WriteTimeout` 2 s, a handler that calls `SetWriteDeadline(now+1s)` before each write kept an SSE-style stream alive
for 3.6 s (4 pings, no error), and `r.Context()` was not cancelled by `ReadTimeout` for a bodiless GET held 5 s. So the design (server
`WriteTimeout` 30 s + per-request deadline in the stream) works; it is only unproven in the repo.

## Carry-forward ledger

| Carry-forward | Status |
|---|---|
| SEC-3 server timeouts (`ReadHeaderTimeout`, `ReadTimeout`, `IdleTimeout`, `MaxHeaderBytes`; metrics `ReadHeaderTimeout`) | Done (`newHTTPServer`; metrics listener already had them). Test only asserts they are non-zero. |
| `WriteTimeout` must not break SSE | Done by design, **no test** (STAFF-3). |
| STAFF-5 `NewSigner` error | Done. |
| STAFF-5 `BaseContext` not the signal ctx; `Stream.Shutdown` = separate ctx cancelled at shutdown start | Done (`streamCtx`/`cancelStreams`, called before `srv.Shutdown`). |
| STAFF-5 wait for the metrics goroutine | Done, but see STAFF-1 (ordering vs amendment 7). |
| `/metrics` only on `API_METRICS_ADDR`, not the API mux | True (grep: no `/metrics` route in `internal/api`), but no test pins it (STAFF-6, low). |
| Ticket TTL 60 s | Done (`ticketTTL`). |
| SEC-2 / STAFF-6 `TRUSTED_PROXIES`, all four limiters | Done in code; wiring from `Deps` to the limiters is untested (STAFF-2). Default empty, so behind nginx every client still shares one bucket until the env is set; not documented anywhere operator-facing (STAFF-7). |
| Key fingerprint in api and worker | Done in both. Api is covered by `TestStartupLogHasNoSecrets`; worker line is untested (acceptable for one log line). |
| SEC-7 `arm()` needs `Unwrap` | Not touched; the middleware wrapper has it, and the scratch check shows the controller works through the real server. |
| PROD-5 half-open connections, shorter keepalive | Neither done nor recorded as deferred. Go's `ListenAndServe` already enables 15 s TCP keepalive, and the stream's per-heartbeat write deadline (10 s) bounds a stuck write, so the practical risk is small. Record it as consciously deferred in backlog.md (STAFF-5). |
| PROD-7 / PROD-3 (hide deleted services, `deleting` body) | Task 14 only wires it; handlers are Task 12 code and out of this diff. Not re-reviewed. |
| Task 18: nginx `X-Forwarded-For $remote_addr` + path test | Still owed to Task 18, correctly not in this commit. |

## Findings

[STAFF-1] severity: medium
where: control-plane-go/cmd/api/main.go:112-120 (and the comment at :163)
problem: The metrics server is run on `metricsCtx`, derived from the signal context `ctx`. On SIGTERM `ctx` is cancelled first, so `/metrics`
stops serving immediately and the whole 10 s drain happens unobserved. Amendment 7 says the order is: log, `cancelStreams()`, `srv.Shutdown`,
cancel the signal context, wait for metrics, close pools. The comment `stop() // then the deferred metrics stop and wait` describes an order
that does not hold, because `stopMetrics()` is effectively a no-op after the signal. The wait itself is correct (the defer runs before the
pool/client closes).
evidence: `metricsCtx, stopMetrics := context.WithCancel(ctx)`; `ctx` is the `signal.NotifyContext` result.
fix: `context.WithCancel(context.Background())`, so metrics outlive the drain and are stopped only by the deferred `stopMetrics()`. That also
makes the comment true. (Alternative: change the comment and amendment, but observing the drain is the point of the metrics.)

[STAFF-2] severity: medium
where: control-plane-go/internal/api/server.go:73-83, tests in internal/api/trustedproxy_test.go
problem: Nothing tests that `Deps.TrustedProxies` reaches the limiters. `TestWrapUsesTrustedProxyClientKey` builds `newIPLimiter` and sets
`l.trusted` itself, and no test file sets `Deps.TrustedProxies`. Deleting `l.trusted = d.TrustedProxies` from the `lim` helper, or wiring only
three of the four limiters, passes the whole suite. The amendment says "all four limiters"; `newServer` has three `ipLimiter`s (auth, refresh, stream) serving four route groups (register/login, refresh/logout, stream-ticket, stream), so the count is a naming slip, but all three are built through `lim` and none is exercised via `NewHandler` with the option set.
evidence: `grep -rln TrustedProxies internal --include=*_test.go` returns only `internal/config/config_test.go`.
fix: one table test through `NewHandler`/`newServer` with `TrustedProxies` set: for each limited route (login, refresh, stream ticket/stream),
two clients behind the trusted peer with distinct XFF get separate buckets, and an untrusted peer with forged XFF does not. 

[STAFF-3] severity: medium
where: control-plane-go/cmd/api/main_test.go (TestNewHTTPServerTimeouts), amendment 3
problem: Amendment 3 says "A test or the smoke run must show a stream outliving WriteTimeout." The commit has neither. The existing
`TestStreamIdleSurvivesWriteTimeoutShorterThanHeartbeat` uses the stream fixture (an `httptest` server with no `http.Server` timeouts), so it
proves the re-arm logic but not the interaction with a real `WriteTimeout`/`ReadTimeout`. The Engineer flagged the missing test as a deviation;
the justification (cost) is weak because it is a ~30-line test: `newHTTPServer` with shrunk timeouts is trivially parameterisable, or build the
`http.Server` in the test from the same constructor and override the fields. My scratch check shows it works today, so this is about
regression protection (a future `WriteTimeout` move, `http.TimeoutHandler`, or a middleware that drops `Unwrap` would silently cut streams at 30 s).
Also `srv.BaseContext != nil` check in the same test is vacuous: `newHTTPServer` cannot set it, so it can never fail.
fix: add a test that serves the handler through `newHTTPServer(...)` with `WriteTimeout`/`ReadTimeout` set to ~300 ms and asserts a stream
receives pings past 3x that. Drop the `BaseContext` assertion (or move it to a test of `run` wiring, which does not exist).
If the test is deferred, add it to backlog.md for Task 18 (smoke) rather than leaving the amendment silently unmet.

[STAFF-4] severity: low
where: control-plane-go/cmd/api/main.go:48-55, main_test.go TestCheckDevLogin
problem: `checkDevLogin(cfg.IsDevelopment(), cfg.AppEnv)` can never fail: `IsDevelopment()` is `AppEnv == "development"`, so the error branch is
unreachable from `run`. The function and its test guard against a hypothetical future wiring change and would pass whatever `run` does; the
test passes `(true, "production")`, a state `run` cannot produce. Amendment 4 asked for something different (refuse dev login on a non-loopback
listen in a production-like env). Since the listen address stays `:port` and dev login exists only when `APP_ENV=development`, the amendment
itself accepts that no real guard exists. Net effect: dead code plus a test that gives false assurance, and `/dev/login` (unauthenticated token
mint) is reachable on all interfaces whenever `APP_ENV=development` (or `NODE_ENV=development`, which the Node-era `.env` sets).
fix: either delete `checkDevLogin` and its test and keep the `devLogin` startup field (the log line is the useful part), or make the guard
real: in development default the listen address to `127.0.0.1:port` and add an explicit opt-in (e.g. `API_LISTEN_ADDR`) for `0.0.0.0`.
Pick one and record the choice in decisions.md.

[STAFF-5] severity: low
where: control-plane-go/cmd/api/main.go:127-132
problem: (a) `logger.Info("api listening")` is written right after `go srv.ListenAndServe()`, before the bind is known; a port-in-use failure
logs "listening" and then "api exited". Splitting `net.Listen` from `srv.Serve(ln)` fixes this and lets the log carry the real address.
(b) A metrics bind failure only logs an Error and the API keeps running with no metrics (the worker uses a supervisor and exits). Probably
acceptable for the API, but make it a conscious choice. (c) PROD-5 (keepalive) has no recorded decision, see ledger.
fix: (a) `ln, err := net.Listen("tcp", srv.Addr)` then `go srv.Serve(ln)`. (b)/(c) one line each in decisions.md or backlog.md.

[STAFF-6] severity: low
where: control-plane-go/internal/config/config_test.go TestTrustedProxies; internal/api (no test)
problem: `parseProxy` rejects an IPv4-mapped CIDR (`::ffff:10.0.0.0/104`) but the table has no case for it, so that branch is unpinned. The
"`/metrics` is not on the API mux" carry-forward (c) is true but not tested. A `0.0.0.0/0` entry (trust everyone) is accepted silently; with
that value the header is never honoured because every entry is "trusted" and the key falls back to the peer, so it is safe but surprising.
fix: add the mapped-CIDR case; add a one-assert test that `GET /metrics` on `NewHandler` is 404; optionally log a Warn for a /0 prefix.

[STAFF-7] severity: low
where: CLAUDE.md "Env vars the worker reads"/API list; control-plane-go/.env example (none)
problem: `TRUSTED_PROXIES` appears only in plans and reviews. An operator putting the API behind nginx gets one shared stream/login bucket
(burst 20, 1/s) with no hint why. CLAUDE.md lists API-only vars (`API_PORT`, `JWT_SECRET`, `CORS_ORIGINS`, `API_METRICS_ADDR`) and should list
this one with its default (empty) and the nginx example (`172.18.0.0/16` style Docker subnet or the nginx container IP).
fix: add it to the CLAUDE.md env list now; the nginx header and path test remain Task 18.

## Notes on things that are correct

- Shutdown order otherwise matches amendment 7: log, `cancelStreams()` (new streams get 503 before the ticket is spent, open streams end with
  "end"), `srv.Shutdown(10 s)`, `stop()`, deferred metrics stop+wait, then `tix.Close`, `ch.Close`, `rdb.Close`, `pool.Close` (LIFO, metrics
  defer is registered last so it runs first). No goroutine leak on the early-return paths: `errc` is buffered, the metrics goroutine is joined
  on every return after it starts, and `cancelStreams` is deferred.
- `requestClientKey` is sound: untrusted peer never honours XFF; right-most untrusted entry; multiple header lines joined; unparsable or empty
  entry, all-trusted, absent header all fall back to the peer (fail-safe: one shared bucket, never an attacker-chosen one); IPv4-mapped and
  zone handling; `/64` reduction kept via `clientKey`. The test table covers each branch, and `TestWrapUsesTrustedProxyClientKey` fails if
  `wrap` goes back to `clientKey(r.RemoteAddr)`.
- `TestStartupLogHasNoSecrets` would fail if the key, JWT, DB/Redis/ClickHouse passwords were logged in either text or JSON format, and
  requires the fingerprint and `devLogin` fields. It tests `logStartup`, which `run` calls, so the coverage is real.
- Added `Logger: logger` to `logs.Aggregator` (not in the plan snippet) is a good deviation: flush failures become visible.
- Engineer's "red phase not observed": the new `requestClientKey` and `TRUSTED_PROXIES` parsing tests clearly would fail without the code
  (new symbols / behaviour); the weak ones are exactly STAFF-2, STAFF-3 and STAFF-4 above, where the test cannot fail.
- Style and comment density match neighbouring code (doc comments say why, no restating).

RESULT: CHANGES REQUESTED (0 critical, 0 high, 3 medium, 4 low)

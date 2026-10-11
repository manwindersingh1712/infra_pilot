# Plan 2 Task 5 review (commit 58e43bf)

## Security

Scope: control-plane-go/internal/api (middleware.go, ratelimit.go, respond.go, server.go, health.go), internal/metrics/api.go, go.mod/go.sum (golang.org/x/time v0.11.0 -> v0.16.0, now a direct dependency).

### Tool output
- govulncheck ./... (hard-limited, full path): exit 0, "Your code is affected by 0 vulnerabilities" (1 vulnerability in a required module that the code does not call; unrelated to x/time).
- gosec -quiet ./internal/api/... ./internal/metrics/...: no findings (exit 0).
- gitleaks git --redact from repo root: 319 commits, "no leaks found".
- npm audit: not run (cp-ui/package.json unchanged).
- go test ./internal/api/: ok.

### gitleaks: root vs control-plane-go/
Cause confirmed. gitleaks reads `.gitleaksignore` from the scan source path (the current directory), not from the git root. The only ignore file is `/.gitleaksignore` (repo root, tracked). Running from control-plane-go/ finds no ignore file, so the four triaged fingerprints are reported (jwt auth_test.go a49bfda; generic-api-key README.md 0cd1c59; curl-auth-user and jwt in the plan doc 8235ae6). Passing the file explicitly (`--gitleaks-ignore-path ../.gitleaksignore`) from control-plane-go/ gives "no leaks found". The scan itself is repo-wide in both cases (319 commits), so the four hits are the same entries the ignore file already covers. CI is not affected: .github/workflows/ci.yml job `secrets` uses gitleaks-action@v2 on a full checkout (fetch-depth 0) and runs at the repo root, where the ignore file is found. Note: the role brief says to run from control-plane-go/; it should say repo root (or pass --gitleaks-ignore-path).

### What was verified clean
- CORS: exact slices.Contains match against the allow-list, ACAO is the request origin only on a match, no Allow-Credentials, `Vary: Origin` always added, config rejects `*`. Preflight (OPTIONS with Access-Control-Request-Method) answered 204 before auth and the mux; a disallowed origin gets 204 with no ACAO/ACAM headers, so the browser blocks. Plain OPTIONS falls to the mux (405). CORS is the outer layer, so 401/429/500 responses also carry the headers.
- Auth: failures return the fixed body `{"error":"unauthorized"}` regardless of cause; no token or verify error text is echoed.
- Request ID: client value kept only if `^[A-Za-z0-9-]{1,64}$`, else 8 random bytes from crypto/rand; no CRLF or log injection possible. Logged path is r.URL.Path only (no query), and slog quotes/escapes it.
- Panic recovery: returns fixed `internal_error`; panic value goes to the log only, not the response. Metrics labels use r.Pattern (or "unmatched"), so cardinality is bounded.
- /metrics: not registered on the API mux (routes has only /healthz and /readyz); promhttp handler is exposed only through metrics.API.Handler(), intended for the separate loopback API_METRICS_ADDR listener.
- Health: /healthz and /readyz return only `{"ok":bool}`, no error details.
- Error responses: generic strings; internalError logs the error but returns `internal_error`.
- Dependency: x/time v0.16.0, no known vulnerabilities (govulncheck).

### Findings

[SEC-1] severity: medium
where: control-plane-go/internal/api/ratelimit.go:58-66 (and server.go design)
problem: The rate-limit key is the TCP peer only, and X-Forwarded-For is deliberately ignored. The CLAUDE.md architecture puts nginx in front of services, and the API is likely deployed behind a reverse proxy/Docker bridge. Then every client shares one peer IP, so one bucket (burst 5, 10/min) covers all users of /auth/*: an attacker can lock everyone out of login with ~5 requests per minute, and legitimate users hit 429 under normal load. Ignoring XFF is the right default (spoofing), but there is no way to configure a trusted proxy.
evidence: `ip, _, err := net.SplitHostPort(r.RemoteAddr)` is the only source.
fix: Before the Plan 2 cutover, either confirm the API is exposed directly to clients (and document it), or add a `TRUSTED_PROXIES` CIDR list and take the right-most untrusted XFF entry only when the peer is in that list. Add a test for both cases.

[SEC-2] severity: low
where: control-plane-go/internal/api/ratelimit.go:35-52
problem: (a) IPv6 clients are keyed by the full address, so a single /64 (the normal per-subscriber allocation) gives 2^64 distinct buckets and bypasses the limiter completely. (b) The same property lets one attacker grow the map by sending requests from many addresses; entries are pruned only after 10 idle minutes, and the prune is an O(n) scan under the mutex on the request path, once per minute. No hard cap on map size.
evidence: `c, ok := l.clients[ip]` with `ip` the raw host string.
fix: Mask IPv6 to /64 (netip.Prefix) before keying; add a max-entries cap (e.g. 100k, reject or evict oldest when full).

[SEC-3] severity: low
where: control-plane-go/internal/api/server.go (NewHandler); no http.Server exists yet (cmd/ has only migrate and worker)
problem: Nothing in this commit sets server timeouts. The handler is safe to review, but the cmd/api wiring task must set `ReadHeaderTimeout` (e.g. 10s), `ReadTimeout`, `IdleTimeout`, `MaxHeaderBytes`, and a `WriteTimeout` that does not kill the SSE log stream (use http.ResponseController.SetWriteDeadline per request for SSE). Without them the API is open to slowloris. The metrics listener (promhttp) needs `ReadHeaderTimeout` too (gosec G112 will flag it).
fix: Track as a must-do in the task that adds cmd/api; re-check at the Plan 2 end gate.

[SEC-4] severity: low
where: control-plane-go/internal/api/respond.go:30-37 (decodeJSON, currently unused)
problem: Body limit uses io.LimitReader, which silently truncates at 1 MiB; it returns a decode error rather than a 413, does not tell the client why, and ignores trailing data after the first JSON value. Not exploitable, but the limit is the DoS control for every POST route in Tasks 6+. It also cannot be enforced by http.MaxBytesReader's connection-close behaviour.
fix: `http.MaxBytesReader(w, r.Body, 1<<20)`, map *http.MaxBytesError to 413, and check `dec.More()`/a second Decode returns io.EOF. Consider `DisallowUnknownFields` where the request shapes are fixed.

[SEC-5] severity: low
where: control-plane-go/internal/api/server.go:57, middleware.go
problem: No security headers on responses. For a JSON API the useful ones are `X-Content-Type-Options: nosniff` and `Cache-Control: no-store` (auth and env-reveal responses must not be cached by shared caches); `Content-Type` is set correctly by writeJSON, but mux-generated 404/405 are text/plain without nosniff.
fix: Add a small outer middleware setting those two headers (plus `Referrer-Policy: no-referrer` if desired).

[SEC-6] severity: low (informational)
where: control-plane-go/internal/api/middleware.go:32
problem: `strings.CutPrefix(header, "Bearer ")` ignores the result, so a bare token without the `Bearer ` scheme is also accepted. Harmless today (the token still must verify), but it is looser than the contract and could surprise later middleware (for example, a log scrubber keyed on the scheme). Also withRecover swallows http.ErrAbortHandler panics as 500s and may write a header after a partial response (double WriteHeader warning).
fix: Require the prefix (`ok` false -> 401) and re-panic on http.ErrAbortHandler.

RESULT: FINDINGS (0 critical, 0 high, 1 medium, 5 low)

## Verifier

Commit 58e43bf (Plan 2 Task 5). RESULT: PASS

Suite (control-plane-go/):
- `go build ./...` OK; `go vet ./...` OK
- `golangci-lint run --allow-parallel-runners` -> `0 issues.` (exit 0)
- `go test -race -count=1 ./...` -> all packages ok, exit 0 (internal/api 6.946s, internal/runtime 78.750s, internal/workflows 13.682s)
- `go test -race -count=3 ./internal/api/` -> `ok infrapilot/internal/api 3.668s`

Feature (no cmd/api yet): throwaway in-package test in a `git archive 58e43bf` copy under the scratchpad (outside the repo; repo untouched), real HTTP listener on a random port, real Postgres via testcontainers, fake Redis pinger, real JWT signer. Handler chain as in NewHandler, plus a test-only `GET /projects` behind s.private and `/metrics` mounted from metrics.API.Handler().
- GET /healthz -> 200 {"ok":true}, X-Request-ID generated, Vary: Origin
- GET /readyz (DB+Redis up) -> 200 {"ok":true}; Redis ping failing -> 503 {"ok":false}
- X-Request-ID "abc-123" echoed; malformed "bad id;<x>" replaced by a generated id
- Preflight Origin http://localhost:5173 (allowed) -> 204, ACAO=http://localhost:5173, Allow-Methods "GET, POST, PUT, DELETE, OPTIONS", Allow-Headers "Authorization, Content-Type, X-Request-ID"
- Preflight Origin http://evil.example -> 204, no Access-Control-* headers
- Protected route no token -> 401 {"error":"unauthorized"}; "Bearer garbage" -> 401; valid token -> 200 {"u":"user-1"}
- Unknown route -> 404 "404 page not found" (plain text)
- /metrics: cp_http_requests_total labelled by route pattern, e.g. route="GET /healthz",status="200" 3; route="GET /projects",status="401" 2; route="unmatched",status="404" 1 (no raw paths)

Observations (not failures):
- NewHandler does not mount /metrics; it is expected on the separate API_METRICS_ADDR listener (Task 14 wiring to confirm).
- Preflights are answered by withCORS before withRecover, so they are neither logged nor counted in metrics.
- 404/405 from the mux are plain text, not the JSON error shape.
- No protected routes are registered yet; s.private was exercised only via the test-only route.

Cleanup: throwaway copy deleted, no leftover containers.

## Staff

Checks run from `control-plane-go/`: `go build ./...` ok, `go vet ./...` ok, `golangci-lint run ./internal/api/... ./internal/metrics/...` 0 issues, `go test -count=1 ./internal/api/` ok (7 tests, Postgres via testcontainers). Mutations were run in a throwaway `git archive` copy under the scratchpad, never in the repo.

Matches the plan text and spec §5 (CORS allow-list, per-IP bucket burst 5 / 6 s refill, fixed 401 body) and §12 (in-memory limiter). Harness names match the plan (`newHarness`, `hs.token`, `hs.do`, `decode`, `expectStatus`, `expectBody`) and the `//nolint:unused` markers are on exactly the later-task helpers (`token`, `decode`, `internalError`, `decodeJSON`); `.golangci.yml` has no `nolintlint`, so stale markers will not be flagged, which means Tasks 6+ must remove them by hand. Middleware order is requestID -> CORS -> recover/log/metrics -> mux, which is right: CORS headers appear on 401/429/500, `r.Pattern` is read after the mux fills it (same `*Request`), and the 401 from `private` is logged and metered. Graceful shutdown is not part of this commit: it lands in Task 14 (`cmd/api`), see STAFF-5.

### Mutation results (test fails = good)
| Mutation | Result |
|---|---|
| CORS: allow any origin | caught |
| CORS: allow origin containing "localhost" (substring match) | SURVIVED |
| CORS: answer preflight headers to a disallowed origin | SURVIVED |
| CORS: drop `Vary: Origin` / drop `Expose-Headers` | SURVIVED (both) |
| Rate limit: always allow / one shared bucket | caught |
| Rate limit: honour X-Forwarded-For | SURVIVED |
| 401 body echoes `err.Error()` | caught (both auth tests) |
| Token from `?token=` query | caught |
| Metrics label = raw path (matched route) | caught |
| Metrics label for 404 = raw path instead of "unmatched" | SURVIVED |
| Request ID accepts unsafe value | caught |
| Log path includes the query | SURVIVED |

[STAFF-1] severity: medium
where: control-plane-go/internal/metrics/api.go:42, internal/api/middleware.go:112
problem: The `method` label is the client-supplied `r.Method`. Go's server accepts any token as a method, so a client can mint unbounded series (`unmatched` x arbitrary method x status). The route label is bounded, the method label is not, so the "route patterns, not raw paths" cardinality guarantee does not hold.
evidence: throwaway test sending methods M0, M1, M2 to `/nope/N` produced `cp_http_requests_total{method="M0",route="unmatched",status="404"} 1` and likewise M1, M2.
fix: normalise to the standard set (GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS) else "OTHER" in `Observe`. Since `r.Pattern` already carries the method for matched routes, the method label could also be dropped for matched routes.

[STAFF-2] severity: medium
where: control-plane-go/internal/api/middleware_test.go (TestCORSAllowsListedOriginOnly, TestHealthReadyAndMetrics, TestRateLimitPerIP)
problem: Tests do not pin several guarantees the task exists to deliver. Surviving mutants: (a) substring/prefix origin matching passes because the only negative origin shares nothing with the allow-list; (b) a preflight from a disallowed origin is never tested (only a simple GET), so leaking `Allow-Methods/Headers` or a non-204 goes unnoticed; (c) the "unmatched" metrics label (the cardinality guard for 404/405) is untested, since only a matched route is hit; (d) X-Forwarded-For being ignored is documented but untested; (e) `Vary: Origin` and `Expose-Headers` are unasserted.
fix: add negative origins `http://localhost:5173.evil.test` and `http://localhost:51730`, plus a disallowed-origin preflight asserting no ACAO/ACAM/ACAH; assert the unmatched label after hitting `/nope/123` and a wrong method on `/healthz`; send a request with `X-Forwarded-For` from a limited peer and assert still 429; assert `Retry-After` and the 429 body.

[STAFF-3] severity: low
where: control-plane-go/internal/api/middleware_test.go:~120 (TestRequireAuth, TestRequireAuthFixedBodyForEveryFailure)
problem: No test shows a valid token without the `Bearer ` scheme is rejected (SEC-6). The "lowercase type" case uses a foreign-key token, so it would be 401 regardless of prefix handling, and so proves nothing. Same for the rate limiter's refill: nothing shows a bucket recovers after 6 s (fake clock not possible because `time.Now` is hard-wired).
fix: add a case with a valid token and no prefix; make "lowercase type" use a valid token. Optionally inject a `now func() time.Time` into `ipLimiter` and test refill and prune.

[STAFF-4] severity: low
where: control-plane-go/internal/api/api_test.go (harness)
problem: Fit for Tasks 6-11 is good but two gaps will force churn: `hs.do` only takes a JSON-marshalable body and cannot send malformed/oversized bodies, custom headers (Origin, X-Request-ID, Authorization scheme) or a chosen `RemoteAddr`; and `AuthBurst: 1000` means the harness cannot test the real limiter through the handler (Task 6 needs a second Deps with a small burst). Also `fakeTickets` mints `string(rune('a'+n))`, which becomes non-letters after 26 tickets (harmless, noted).
fix: add `hs.doRaw(method, path, token string, body io.Reader, hdr map[string]string)` (and `hs.doFrom(ip, ...)`) now, or a `newHarnessWith(t, func(*Deps))` option; do it in Task 6 before the auth tests duplicate it.

[STAFF-5] severity: medium (carry-forward for Task 14 `cmd/api`, plan lines ~4070-4194)
where: docs/superpowers/plans/2026-10-07-go-plan2-api.md ~4157-4194
problem: Three issues in the planned wiring: (1) `BaseContext` returns the signal context, so on SIGTERM every in-flight request context is cancelled at the same instant `Shutdown` starts, aborting DB queries and defeating the 10 s drain; only SSE needs that cancel. (2) `auth.NewSigner(cfg.JWTSecret)` is shown with one return value, but since c3b50e8 it returns `(*Signer, error)`; the snippet will not compile. (3) The metrics server goroutine is not waited on, and SEC-3's server timeouts (`MaxHeaderBytes`, per-request SSE write deadline) are not in the snippet.
fix: use a separate `baseCtx` cancelled after `Shutdown` returns (or let the SSE handler also watch a shutdown channel), handle the `NewSigner` error, wait for the metrics goroutine, and record this in START-HERE so Task 14 does not copy the snippet verbatim.

[STAFF-6] severity: low
where: control-plane-go/internal/api/middleware.go:94-120
problem: `withRecover` swallows `http.ErrAbortHandler` (should re-panic) and writes a 500 after a partial response (double WriteHeader log noise). Preflight 204s and requests rejected by CORS never reach the log/metrics layer, so OPTIONS traffic is invisible. Overlaps SEC-6.
fix: re-panic on `http.ErrAbortHandler`; track `sw.wrote` and skip `writeError` if headers were sent. Moving withRecover outside withCORS would make preflights visible, at the cost of CORS headers on panic 500s; prefer keeping order and counting preflights separately, or accept.

### Step 4 (red) skipped
The only expected failure was `undefined: NewHandler` (a compile error), so skipping loses no behavioural red. The real gap is the one above: with no red phase and no mutation pass, five guarantees are untested (STAFF-2). The mutation table is the substitute evidence; fixing STAFF-2 closes the loop. Acceptable as a deviation if STAFF-2 is fixed.

### Cross-checks with the Security section
SEC-1 (single peer IP behind a proxy) and SEC-2 (IPv6 /64 bypass, map cap) stand; STAFF-2(d) adds the missing XFF test. SEC-4 (`io.LimitReader` truncates silently) should be fixed before Task 6 uses `decodeJSON`, since it is the first POST consumer.

RESULT: CHANGES REQUESTED (0 critical, 0 high, 3 medium, 3 low)

## Staff (round 2)

Range 58e43bf..6c22430 (cb0d6aa, 866915a, 2fb2825, 5f5fadc, 9a7f740, 830ef1b, 6c22430) in internal/api and internal/metrics. Reviewed at committed HEAD. `go build ./...`, `go vet ./...` ok; `go test -race -count=1 ./internal/api/ ./internal/metrics/` ok. `golangci-lint run` at HEAD FAILS: `internal/api/middleware_test.go:368:5: SA4000 identical expressions on the left and right side of the '||' operator` (`!l.allow("10.0.0.1") || !l.allow("10.0.0.1")`). An uncommitted working-tree edit (loop `for i := range 2`) already fixes it and gives `0 issues`; it needs committing.

### Previous findings
| ID | Status |
|---|---|
| STAFF-1 method label | Fixed: `normaliseMethod` in metrics.Observe (standard 7 else "OTHER"); metrics unit test and handler test both catch the raw label. |
| STAFF-2 test gaps | Fixed: near-miss origins x simple/preflight, ACAO/Expose/Vary, unmatched label, XFF/X-Real-IP/Forwarded, 429 body and Retry-After. All six listed mutations now caught. |
| STAFF-3 Bearer / refill | Fixed: bare token, `Token`, lowercase `bearer` with a VALID token, double space all 401; fake clock refill test exists. |
| STAFF-4 harness | Fixed: `newHarnessWith`, `doRaw`, `doFrom`, with a test that goes through the full handler. |
| STAFF-5 cmd/api plan snippet | Not addressed in these commits (docs only; carry-forward to Task 14, still open, not a code defect here). |
| STAFF-6 ErrAbortHandler | Partly fixed: re-panics after metrics/log, no body written; tested, both mutations caught. The "500 after partial response / double WriteHeader" half is NOT fixed (no `wrote` tracking in statusWriter), and preflights remain unlogged/uncounted. Low, accept or note. |
| SEC-2 | Fixed: /64 mask, `Unmap()` for v4-mapped, zone stripped, LRU with cap 100k and idle sweep from the back. Locking is correct: one mutex covers map and list, `size()` locks, `remove` is only called under the lock, no scan of the whole map. Unparseable host falls back to the raw string (bounded by TCP peer format, fine). |
| SEC-4 | Fixed: `MaxBytesReader`, 413 `request_too_large` JSON via `writeDecodeError`, trailing data rejected via `dec.Token()` (whitespace-only tail accepted, empty body accepted, truncated JSON is a 400). A MaxBytesError raised while probing the tail is also passed through (correct). |
| SEC-5 | Fixed: `withSecurityHeaders` is the outermost layer (covers preflight, 401, 404/405). |
| SEC-6 | Fixed: missing `Bearer ` prefix is 401 (case-sensitive, stricter than RFC 7235, acceptable and tested). |
| Verifier obs. JSON 404/405 | Fixed: `jsonErrors` uses `mux.Handler` to detect the unmatched case, keeps `Allow`, and matched routes still go through `mux.ServeHTTP` so `r.Pattern` is set for metrics (unmatched label still tested). Redirect-handler case (pattern "" with 301) falls through to `mux.ServeHTTP` unchanged. |

### Mutation results at HEAD (throwaway git archive copy outside the repo; "caught" = a test fails)
Previous-review mutations: substring origin match caught (also prefix match); preflight leaking Allow-* to a disallowed origin caught; unmatched label = raw path caught; XFF honoured caught; Vary: Origin dropped caught; Expose-Headers dropped caught; method label raw caught (metrics unit test and handler test).
New-code mutations caught: bare token accepted, no re-panic, swallow ErrAbortHandler, no IPv6 /64 mask, no Unmap, no cap, evict newest instead of oldest, no idle sweep, LimitReader instead of MaxBytesReader, trailing data accepted, 413 mapped to 400, nosniff dropped, no-store dropped, plain-text 404, Allow dropped, security headers moved inside CORS.
SURVIVED: dropping `l.lru.MoveToFront(e)` in `ipLimiter.allow`.

[STAFF-7] severity: medium
where: control-plane-go/internal/api/middleware_test.go (limiter tests), ratelimit.go:63
problem: The one thing that makes the structure an LRU is untested. Without `MoveToFront` on a hit, an active client keeps its insertion position: at the cap it is evicted ahead of idle clients (its bucket resets, so an attacker at the cap can reset limits by cycling), and the idle sweep from the back stops at the first recent-`seen` entry while older entries behind it never expire, so the O(expired) sweep invariant is violated. All limiter tests pass with this line deleted because the cap test touches each key once.
evidence: mutation `no-movefront` run in the archive copy: `ok infrapilot/internal/api`.
fix: add a case: cap 3, add a, b, c; hit a again (spend/limit it); add d; assert a is still tracked/limited and b is the one evicted (fresh bucket). Similar idle-sweep case: touch the oldest key at t+9m, advance to t+11m, assert it survives while an untouched peer is dropped.

[STAFF-8] severity: low
where: control-plane-go/internal/api/ratelimit.go:66-68
problem: At the cap an attacker with many source addresses (IPv4 botnet, many /64s) evicts legitimate clients' buckets, resetting their limits; this is the accepted trade-off of a bound, but it is undocumented and cap-eviction is not logged or counted. Not a regression versus the unbounded map. Also eviction drops an active (non-expired) entry silently.
fix: comment the trade-off in `ipLimiter`; optionally a counter metric for evictions (useful operability signal). SEC-1 (trusted proxy) still open by design, unchanged here.

[STAFF-9] severity: low
where: committed HEAD, middleware_test.go:368
problem: golangci-lint (SA4000) fails at the committed tip (see above); the uncommitted working-tree change fixes it. Not a behavioural issue, but the round must not close with a red lint. Commit it.

### Other notes
- `decodeJSON` and `writeDecodeError` keep `//nolint:unused`; Tasks 6+ must remove them by hand (no nolintlint).
- Oversize test covers the value-too-big path; the "valid value followed by >1 MiB whitespace" path (Token returns MaxBytesError) is correct by reading but untested. Low, optional.

RESULT: CHANGES REQUESTED (0 critical, 0 high, 1 medium, 2 low) -- STAFF-7 (LRU MoveToFront untested), STAFF-8, STAFF-9 (lint red at HEAD; working-tree fix exists, commit it). All round 1 findings are fixed except the carry-forward STAFF-5 (Task 14 docs) and the minor double-WriteHeader half of STAFF-6.

## Close-out (orchestrator)
Fix round 1 commits: cb0d6aa, 866915a, 2fb2825, 5f5fadc, 9a7f740, 830ef1b, 6c22430; then 689c49e (VER-1: SA4000 lint, found by the round-2 Verifier) and 7abf89d (STAFF-7: LRU test, mutation-proven: deleting MoveToFront fails both subtests). STAFF-9 (lint at HEAD) was fixed by 689c49e. After these the orchestrator ran `golangci-lint run`: 0 issues. The round-2 Verifier had already passed build, vet, gosec, gitleaks (repo root, 327 commits, no leaks), `-count=3` on api/metrics and the full `go test -race -count=1 ./...`; the two later commits touch only `middleware_test.go`. Result: task clean. SEC-1 (trusted proxy) is a Task 18 cutover blocker; Task 14 notes and lows are in backlog.md.

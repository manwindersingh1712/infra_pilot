# Plan 2 Task 11 security review (commits 4132954, abad375, 5ff53dc, bfbc495)

Scope: control-plane-go/internal/api (logs.go, stream.go, server.go, ratelimit.go, middleware.go), internal/logs, internal/tickets.

## Tool results
- govulncheck ./... : no vulnerabilities affecting called code (1 in a required module, not called). Exit 0.
- gosec -quiet (api, logs, tickets): no findings.
- gitleaks detect --log-opts="4132954^..bfbc495": no leaks (4 commits).
- go test -p 2 ./internal/api -run 'Stream|Ticket|Logs|Log': ok. internal/logs and internal/tickets: ok. (`-short` panics in services_test.go:630 because a DB test needs Docker; not related to this task.)

## What was verified (no finding)
- Ticket: 256-bit crypto/rand, strict base64 shape check before Redis, GETDEL (atomic single use, 32-way race test), TTL via SET EX. Bound value is `<userID>:<deploymentID>` and compared exactly against the URL id. Ownership is re-checked at redeem time with GetDeploymentForOwner (soft-deleted and foreign both false).
- Uniform 401 `invalid_ticket` for missing, malformed, unknown, expired, reused, wrong deployment, wrong user, unowned. Redis/DB faults map to 503/500 with no detail.
- Unauthenticated callers cannot touch the stream caps: acquire() runs only after a valid redeem and ownership check. Release is deferred immediately after acquire, so it runs on every exit incl. panic.
- Ticket leakage: nothing logs the query (access log and panic log use r.URL.Path only; metrics use r.Pattern); error text is run through redactTicket; response has Cache-Control no-store; ticket endpoint returns no-store.
- SSE framing: event name is a constant; data is json.Marshal output (escapes \n, \r, U+2028/2029, NUL, invalid UTF-8 -> U+FFFD), so no event/field injection. Lines capped at 8 KiB at a rune boundary; history capped at 100 and 1000 via limit. No header values come from tenant data.
- Write deadline is armed before every write/heartbeat via ResponseController; statusWriter has Unwrap, so it reaches the real conn. Subscription and goroutine are tied to a context with MaxDuration, client disconnect and Shutdown; stop() is deferred.
- History endpoint: ownership with uniform 404, limit clamped to 1000, search <=256 bytes, `\ % _` escaped, sources allow-listed, times parsed as RFC3339, all values bound as parameters in ClickHouse (limit/offset are integers via strconv; offset is never set from input). No string concatenation of user data.

## Findings

[SEC-1] severity: low
where: control-plane-go/internal/api/stream.go:157-172 (acquire after Redeem), 183-199
problem: the ticket is spent before the stream caps and before Subscribe/Query. A legitimate user who hits too_many_streams (429), or whose Subscribe/history read fails (500), loses a valid ticket and must mint a new one. It cannot be abused against another user (the attacker would need the victim's ticket), so impact is a minor reliability issue that also costs the client one extra rate-limit token per retry.
evidence: `Redeem` at line 154, `s.streams.acquire` at 169, `Subscribe` at 195.
fix: acceptable as is (the uid is only known after redeem). Optionally document that the client must request a new ticket after 429/5xx, which the UI plan already does on reconnect.

[SEC-2] severity: low (medium if the API is deployed behind a single reverse proxy/NAT address)
where: control-plane-go/internal/api/ratelimit.go (wrap, clientKey), server.go:136-140
problem: the stream-ticket and stream routes share one per-IP bucket (burst 20, 1 req/s), keyed on the TCP peer. The stream route is unauthenticated before the limiter, so garbage GETs consume tokens. Behind nginx/ingress (X-Forwarded-For deliberately untrusted) every client shares one bucket, and one attacker (even without an account) can lock all users out of log viewing. Direct exposure is fine.
fix: Task 14 must either document that the API is exposed directly, or add a trusted-proxy setting that takes the client address from X-Forwarded-For only when the peer is a configured proxy. Consider a separate, larger bucket for the ticket endpoint (authenticated) so unauthenticated stream spam cannot starve it.

[SEC-3] severity: low
where: control-plane-go/internal/api/stream.go:134-170 (timing)
problem: response time differs by failure class (malformed: no Redis call; unknown: Redis only; valid ticket but wrong deployment: Redis, no DB; wrong owner/deleted: Redis + DB). Status and body are identical. Only a holder of a genuine, already-redeemed ticket can observe the difference, and it reveals nothing they did not mint, so no practical oracle.
fix: none required. Do not add distinguishing codes later.

[SEC-4] severity: low
where: control-plane-go/internal/api/logs.go:164-171 (logsHealth), server.go:130
problem: /logs/health is JWT-only (not owner scoped) and returns the process-wide count of Redis log streams (`activeLogStreams`), a cross-tenant activity signal available to any registered user. Value is a bare integer and matches the Node behaviour, but it is not needed by tenants.
fix: restrict to an operator role/internal listener, or drop the count from the tenant-facing response and expose it only as a metric on the loopback metrics address.

[SEC-5] severity: low (info for the plan)
where: control-plane-go/internal/logs/aggregator.go:55-63
problem: when the Redis tier has data it is returned as is: `search`, `sources`, `after/before` and `offset` are ignored on that path (and Redis.Read returns the OLDEST N via XRANGE - + COUNT). Not a leak of other tenants' data (key is the owned deployment id), but a filter the caller expects is silently not applied, and the escaped search value is only used by ClickHouse. It matches Node semantics per the code comment, so treat as a correctness note for the Task 11 reviewer.
fix: apply Query filters on the hot path, or reject filter params when the hot tier answers.

[SEC-6] severity: info
where: control-plane-go/internal/api/stream.go, logs.go
problem: log content is secrets-bearing (containers print env values, tokens) and nothing redacts it in history, SSE, Redis or ClickHouse. Access is owner-scoped, so this is tenant-to-self exposure, but it also lands in browser history/devtools of the viewer. Not a regression vs the Node API.
fix: none now; note in the threat model that log redaction is out of scope (or add a build-time scrub of known env var values later).

[SEC-7] severity: info
where: no http.Server exists yet in cmd/api (Task 14)
problem: the per-write deadline design assumes the server keeps ReadHeaderTimeout, a bounded MaxHeaderBytes and a normal WriteTimeout (overridden per stream). None of these are configured in this diff. Without ReadHeaderTimeout, slow-loris on the unauthenticated stream route is possible. If ResponseController.SetWriteDeadline returns ErrNotSupported (a wrapper without Unwrap), arm() silently skips the deadline; today the middleware has Unwrap so it works.
fix: Task 14: set ReadHeaderTimeout (e.g. 10 s), MaxHeaderBytes, IdleTimeout, and pass StreamConfig.Shutdown; keep the stream test that proves a stalled reader is dropped.

RESULT: FINDINGS (0 critical, 0 high, 0 medium, 5 low, 2 info). No blocking issues; ticket flow, SSE framing, SQL/ClickHouse parameterisation and cap release are sound.

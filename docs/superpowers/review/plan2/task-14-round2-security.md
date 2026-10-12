# Plan 2 Task 14 round 2 security review (ef38f46..88c4f64)

Reviewer: Security. Scope: SEC-1 and SEC-4 confirmation, new helpers (listenAddr, startMetrics, shutdownAPI, serveAPI, api.NewHTTPServer, logEncryptionKey), docs.

## Tool output
- df: 24 GiB free (88% used). No cache clean, no Docker prune.
- gosec ./cmd/... ./internal/api/... ./internal/config/...: 19 files, 3636 lines, Issues 0, Nosec 0.
- gitleaks detect --log-opts="ef38f46..88c4f64" (repo root): 6 commits scanned, no leaks found.
- govulncheck ./... (180 s alarm): exit clean. 0 vulnerabilities affect the code; 1 in a required module that is not called (same as round 1).

## Round 1 findings

[SEC-1] CONFIRMED FIXED.
- listenAddr(devLogin=true) returns "127.0.0.1:"+port, a literal IPv4 loopback. No "localhost" resolution is involved (so no ::1/IPv4 ambiguity and no resolver or /etc/hosts dependence). IPv6 ::1 is not bound, which is the safe direction. net.Listen("tcp", srv.Addr) in serveAPI binds exactly that.
- The decision input is cfg.IsDevelopment(), the same value passed to Deps.DevLogin (main.go lines for api.Deps and addr), so the route registration and the bind cannot diverge. DevLogin false gives ":"+port (production, route not registered).
- NODE_ENV fallback: config.go:132 uses oneOf(... "development","production") over APP_ENV, then NODE_ENV, then "production". Only the exact strings pass; anything else (including "Development", "dev", "test") is a config error, not a silent dev mode. A stale NODE_ENV=development on a deployed host now yields a loopback-only API (so not remotely reachable, but the API would also be unreachable from nginx on another host/container, which fails visibly). The startup line and a Warn log "dev login is enabled ... binding loopback only" make it observable (devLogin field logged). Backlog item "NODE_ENV fallback warning" stays deferred per ruling; the new Warn mostly covers it.
- API_PORT is validated numeric 1..65535 by config.port, so it cannot inject a host ("0.0.0.0:80") into "127.0.0.1:"+port. strconv.Atoi accepts "+80", which would produce an invalid listen address and fail at Listen (fail closed, cosmetic).
- Residual (info, dev only, not a regression): loopback bind does not stop (a) a local reverse proxy or a port-forward on the same host from re-exposing the API (nginx in host network mode proxying to 127.0.0.1:8080 with APP_ENV=development would expose /dev/login; the docs say nginx fronting is a non-dev setup, and TRUSTED_PROXIES docs do not warn about this combination), (b) DNS rebinding from a browser on the dev machine, since the API does not validate the Host header, so a hostile page could rebind to 127.0.0.1:8080 and call /dev/login same-origin. Both need the developer to run dev mode while browsing hostile pages. Optional hardening: reject requests whose Host is not localhost/127.0.0.1 when DevLogin is on. Not required.
- Docs: CLAUDE.md and release note state "With APP_ENV=development the API binds 127.0.0.1 only; otherwise all interfaces." Accurate against the code. Note for operators: in a Docker container with APP_ENV=development, the published port will not reach the API (loopback inside the container); the docs do not mention it. Low value, add a half-sentence if desired.

[SEC-4] CONFIRMED FIXED, no leak or hang found.
- startMetrics uses context.Background(), so SIGTERM no longer stops /metrics. shutdownAPI order: cancelStreams, srv.Shutdown(10 s timeout), then mr.Stop(). Metrics stay up for the whole drain (TestShutdownKeepsMetricsDuringDrain covers it).
- If Shutdown times out: the error is returned after mr.Stop() still runs (not skipped, since Stop is called unconditionally after Shutdown), the process then exits and drops remaining connections. No hang.
- mr.Stop bound: cancel() then <-done. metrics.Serve returns as soon as its internal srv.Shutdown is called (Serve returns ErrServerClosed immediately; the 5 s shutdown context runs in a detached goroutine), so done closes promptly and a slow scraper cannot block Stop. If Serve failed to bind it returns at once and done is closed. If ctx is cancelled before the listener binds, Serve may still bind and then serve until its goroutine sees the cancelled context; it shuts down immediately. No path leaves done open.
- Stop is idempotent (cancel twice is fine; reading a closed channel is fine). It is called by shutdownAPI and again by the deferred mr.Stop(); the defer runs before the pool/Redis/ClickHouse closes (LIFO order, defer registered after them).
- failed is buffered 1 and written at most once, so the goroutine never blocks on an unread error. The Failed() branch in the select calls cancelStreams, srv.Close and returns an error; deferred Stop completes. A metrics failure arriving during shutdown is dropped silently into the buffer (fine).
- Metrics failure now ends the process (STAFF-5 closed); the error is wrapped "metrics: ...".

## New findings

[SEC-6] severity: info
where: cmd/api/main.go serveAPI / run select
problem: After Shutdown returns, Serve's result in errc is ignored (buffered channel of 1, no goroutine leak). If Shutdown timed out, handlers still running are abandoned at exit. Accepted per round 1.
fix: none.

[SEC-7] severity: info
where: internal/api/server.go NewHTTPServer
problem: Timeouts identical to the previous inline values (ReadHeader 10 s, Read 30 s, Write 30 s, Idle 2 min, MaxHeaderBytes 64 KiB); no ErrorLog/ConnState hooks. Extraction is behaviour-preserving; the stream outliving WriteTimeout is now tested (TestStreamOutlivesServerWriteTimeout) against this exact constructor, not a copy. Good.
fix: none.

[SEC-8] severity: info
where: cmd/worker/startup.go logEncryptionKey
problem: Logs only secrets.Fingerprint(key) (12 hex of a hash), and the test asserts neither raw nor base64 key appears. API startup line likewise logs the fingerprint and the redacted Config. No leak.
fix: none.

[SEC-9] severity: info
where: cmd/api/main.go serveAPI
problem: onListening runs only after a successful Listen, so "api listening" can no longer be logged for a failed bind (and the secrets-bearing, redacted config line is not emitted for a process that never served). logStartup uses cfg via LogValue (redacted).
fix: none.

## Checked and OK
- TRUSTED_PROXIES docs say to list only the nginx /32 and require `X-Forwarded-For $remote_addr`; consistent with SEC-3 and the ruling. Release note mirrors it.
- TrustedProxies reaches every limiter (trustedproxy_test.go, 71 lines) and IPv4-mapped CIDR rejection is pinned.
- /metrics absent on the API mux is tested; metrics address stays on its own loopback listener (isLoopbackAddr warns otherwise).
- No new dependencies in the range.

RESULT: PASS. SEC-1 and SEC-4 confirmed fixed. No medium or higher findings; only informational notes (optional Host-header check in dev mode, and a docs half-sentence about containers).

# Plan 2 Task 14 security review (6dca7f5, 760040a, 98497fe)

Reviewer: Security. Scope: TRUSTED_PROXIES client key, cmd/api wiring, key fingerprint log.

## Tool output
- govulncheck ./... (180 s alarm): exit 0. "Your code is affected by 0 vulnerabilities"; 1 vulnerability in a required module that the code does not call (not reachable).
- gosec -quiet ./...: 2 findings, both G204 (MEDIUM) in code not touched by this range: internal/runtime/logstream.go:61 (fixed argv, only containerID varies, passed as an argument not a shell string) and internal/runtime/exec.go:26 (the Runner abstraction; callers pass argument lists). Both are false positives by design. The full run also printed "could not import ... export data version 5" type errors for many packages (stale go-build cache vs go1.27.2 toolchain), so coverage of the full tree was degraded. A re-run limited to ./cmd/api/... ./internal/api/... ./internal/config/... printed no issues.
- gitleaks detect --log-opts="6dca7f5^..98497fe": 3 commits scanned, no leaks found.
- cp-ui unchanged, npm audit not needed.

## Findings

[SEC-1] severity: medium
where: control-plane-go/cmd/api/main.go:46-52, 59-65 (checkDevLogin, newHTTPServer Addr), internal/config/config.go:132,212,230
problem: The dev-login guard is tautological and the decision's second half is not implemented. run() calls checkDevLogin(cfg.IsDevelopment(), cfg.AppEnv); IsDevelopment() is AppEnv == "development", so the check can never fail. Decisions.md (Plan 2 Task 6 rulings, Follow-ups) asked Task 14 for "DevLogin only from config.Env == development and refuse non-loopback listen with it in production-like env". The server always listens on ":"+port (all interfaces). In development mode the JWT placeholder/length checks (config.go:230) are skipped, so a dev-mode API on all interfaces has an unauthenticated token-minting endpoint (/dev/login) and a guessable JWT secret ("dev-secret-change-me" from the documented .env). Also APP_ENV falls back to NODE_ENV (config.go:132): a Node-era .env with NODE_ENV=development carried into a deployed host silently enables /dev/login.
evidence: `if devLogin && appEnv != "development"` with devLogin == (appEnv == "development"); `Addr: ":" + port`.
fix: When DevLogin is on, bind to 127.0.0.1:port (or refuse a non-loopback Addr, as metrics.isLoopbackAddr already does), and log a warning that dev login is on. Optionally add an explicit DEV_LOGIN=true opt-in instead of deriving it from APP_ENV, and make the test cover a case that can actually fail.

[SEC-2] severity: low
where: control-plane-go/internal/api/ratelimit.go:122-135 (requestClientKey)
problem: An unparsable X-Forwarded-For entry (including "ip:port", e.g. "1.2.3.4:5678", or a bracketed IPv6 "[::1]") makes the function return the proxy peer key, so every such request shares the proxy's single bucket. Safe from bypass (fails closed to one shared bucket), but a client able to inject such an entry as the right-most value gets itself and everyone else sharing that bucket throttled. With nginx appending $remote_addr as the last entry this is not reachable from the internet; it is reachable only if the edge hop does not rewrite XFF or a second trusted proxy appends one.
evidence: `if err != nil { return peer }`.
fix: Acceptable as is for the ruling. Optionally strip a port (netip.ParseAddrPort) and brackets before parsing, and log at debug when the fallback is taken so a misconfigured proxy is visible.

[SEC-3] severity: low
where: control-plane/infra/nginx/conf.d/routes/*.conf:15 (existing Node-era route configs), decisions.md ruling "X-Forwarded-For $remote_addr (overwrite, not append)"
problem: The existing nginx config uses $proxy_add_x_forwarded_for (append). With the right-most-untrusted algorithm append is still safe (the real address is the last entry and the client-supplied prefix is ignored), and I verified the algorithm does not use the left-most entry. The decision wants overwrite for the edge hop; that is not yet done and is Task 18's job. Note that if the trusted prefix is a whole Docker subnet, any other container on that network can send arbitrary XFF and mint buckets (limiter bypass for the brute-force limits) because the peer is trusted.
fix: Task 18: set the API location block to `proxy_set_header X-Forwarded-For $remote_addr;`, and list only the nginx container's address (/32) or a dedicated subnet in TRUSTED_PROXIES, not a shared bridge CIDR. Add a startup warning when a TRUSTED_PROXIES prefix is wider than /24 (v4) or /64 (v6).

[SEC-4] severity: low
where: control-plane-go/cmd/api/main.go:128-146 (shutdown ordering)
problem: metricsCtx derives from the signal ctx, so the metrics listener stops the instant SIGTERM arrives, not after the drain; metrics for in-flight requests during the 10 s drain are unscrapable. Not exploitable. Also Shutdown does not track hijacked connections, but the stream is SSE (no Hijack found), and streamCtx is cancelled before Shutdown, so open streams end and the drain is not held up. Ordering of cancelStreams -> Shutdown -> stop -> deferred metrics stop -> pool/client closes is correct. If Shutdown times out the error is returned and the process exits; remaining connections are dropped by exit, acceptable.
fix: Optional: derive metricsCtx from context.Background() and cancel it after Shutdown returns.

## Checked and OK (no finding)
- XFF spoofing: a peer outside the list never has XFF honoured (ratelimit.go:117-119); with an empty list the peer is always the key. Multiple XFF headers are joined in order (Header.Values then split), right-most-first scan is correct across headers. Whitespace trimmed. Left-most forged entries are ignored.
- IPv6/mapped: peer and entries are Unmap()'d and zones stripped before prefix matching; parseProxy unmaps bare addresses and rejects IPv4-mapped prefixes (no silent mismatch). Bare IP becomes /32 or /128; CIDR is Masked(). Invalid entries fail config load (fail closed, not silently empty). Note: the code keys IPv6 by /64 (clientKey) also on the XFF path, whereas the ruling text says "full address"; the code is stricter against bucket minting and is consistent with the peer path, so I treat it as a harmless improvement; the spec text should be updated to say /64.
- Limiter memory: three limiters each capped at 100 000 keys with LRU eviction and 10 min idle expiry, O(1) per request, XFF keys are bounded by the same cap. Under a flood of distinct IPv4 sources an attacker could evict legitimate entries (resets their bucket) but needs 100k distinct sources; acceptable for single instance.
- Secrets in logs: the fingerprint is 12 hex chars (48 bits) of sha256 of a 256-bit random key, not usable for recovery; fingerprints appear in the api startup line and worker startup. Config.LogValue redacts DB/Redis/ClickHouse URLs and prints only set-flags for JWT secret, key and ClickHouse password. TRUSTED_PROXIES is not secret. Config.String uses the same redacted view.
- Timeouts: ReadHeaderTimeout 10 s, ReadTimeout 30 s, WriteTimeout 30 s, IdleTimeout 2 min, MaxHeaderBytes 64 KiB; SSE extends its own write deadline per write (stream.go:225-237) and resets it. Body limit via MaxBytesReader in respond.go:35. Slowloris on headers and body is bounded.
- No new dependencies in this range (godotenv already present, verified by govulncheck being clean).
- RequireAPI is called before listening; placeholder/short JWT secrets are rejected outside development.

RESULT: FINDINGS (medium 1, low 3)

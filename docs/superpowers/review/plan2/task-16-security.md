# Plan 2 Task 16: Security review (live logs over SSE)

Commits: 059b1e5, 1c0ae8c, ca4d781, f70715f (branch go-migration). Reviewer: Security. No code changed.
Scope: `cp-ui/src/lib/logStream.ts`, `cp-ui/src/hooks/useLogs.ts`, `cp-ui/src/components/LogViewer.tsx`, `cp-ui/src/lib/api.ts`, `cp-ui/src/lib/errors.ts`, plus the server files needed to judge the CORS finding.
Disk before starting: 18 GiB free (91 % used), fine.

## Tools

- `gitleaks detect --log-opts="059b1e5^..f70715f"`: 4 commits scanned, no leaks.
- `gitleaks detect` (whole repo, 469 commits): no leaks.
- `npm audit --omit=dev` in cp-ui (no fix run): 2 high, both `react-router` / `react-router-dom` (7.0.0-pre.0 to 7.18.1). Down from 5 before f70715f; `ws`, `engine.io-client`, `socket.io-parser` are gone. The remaining advisories are mostly SSR/RSC/framework-mode (turbo-stream RCE, `__manifest` DoS, RSC XSS, `deserializeErrors`). Two could matter for a plain SPA: open redirect via `<Link>`/`useNavigate` (GHSA-wrjc-x8rr-h8h6, GHSA-2j2x-hqr9-3h42) and route-matching DoS (GHSA-chx6-hx7r-mcp5). Pre-existing, not introduced here (SEC-9).
- govulncheck and gosec: not run. No Go files changed in these commits.

## Findings

[SEC-1] severity: medium
where: cp-ui/src/lib/logStream.ts:102-104 (appendLive), cp-ui/src/hooks/useLogs.ts:180, cp-ui/src/components/LogViewer.tsx:171
problem: The log list has no upper bound. The hook now retries forever and reconnects silently every hour, so a tab left open on a chatty container grows `logs` without limit. Each live line also does `prev.some(...)` (O(n)) and copies the array (`[...prev, e]`, O(n)), so n lines cost O(n^2) CPU. LogViewer renders every row with no windowing. A noisy or hostile workload in a user's own container can freeze or crash that user's tab. This is not cross-tenant.
evidence: `return prev.some((l) => l.timestamp === e.timestamp && l.message === e.message) ? prev : [...prev, e];` and `logs.map((log, index) => ...)` with no slice. `mergeHistory` (logStream.ts:111-132) builds a Set and sorts the whole list on every merge.
fix: Cap the list (for example the newest 5,000 lines, dropping the oldest) in `appendLive` and `mergeHistory`. Keep a bounded Set of keys instead of `some`. Optionally render only `logs.slice(-N)`. Add a check in `scripts/check-api-logic.ts` for the cap.

[SEC-2] severity: low
where: cp-ui/src/hooks/useLogs.ts:241-242 (mount), 222-238 (onVisibility)
problem: The hidden-tab rule only starts on a `visibilitychange` to hidden. A tab opened in the background (middle-click, "open in new tab") never fires that event. Its 5-minute close timer never starts, so its stream and silent hourly reconnects continue indefinitely. A few such tabs fill the per-user cap of 5 streams and can trigger the "close one" hint on the foreground tab.
fix: At the start of the effect, if `document.visibilityState === "hidden"`, run the same path as the hidden branch (set `hiddenAt` and start `hiddenTimer`).

[SEC-3] severity: low
where: cp-ui/src/hooks/useLogs.ts:183-200 (`end` handler), cp-ui/src/lib/logStream.ts:39-42
problem: The `end` loop guard is weak.
- A `max_duration` end that arrives more than 1 s after open reconnects with delay 0, is not counted as an attempt, and shows no badge. A server or proxy that ends every stream at about 1.1 s makes a tight loop of one ticket POST plus one stream GET per second, forever. The guard only covers streams that end within 1 s.
- `shutdown` (or an unknown reason) waits only 1-3 s, and `onopen` resets `attempts` to 0. A server that accepts and then ends every stream also loops every 1-3 s with no growth. `attempts += 1` at line 198 has no effect because `endDelay` ignores it.
- This needs a faulty or hostile server or proxy, so it is a DoS-amplification and robustness gap, not a user-reachable exploit.
fix: Treat `max_duration` as silent and immediate only if the stream lived long enough (for example 60 s or more, since the real cap is 1 h). Otherwise use `Math.max(endDelay(...), backoffDelay(streamLifetime < 10_000 ? attempts : 0, rand))`. Do not reset `attempts` in `onopen` until the stream has stayed open for some seconds.

[SEC-4] severity: low
where: cp-ui/src/hooks/useLogs.ts:204-216 (restart), 218-238
problem: `restart()` (Retry button, `online`, visible) sets `attempts = 0` and calls `connect()` at once, with no minimum interval. It discards a pending 429 `minDelayMs` and the backoff. Rapid tab switching, a flapping network or a click-spammed Retry sends one ticket POST each time while the API is rate-limiting. Each call is user-initiated and `connect()` aborts the previous in-flight ticket request, so there is no unbounded fan-out. The amplification is bounded by user speed, but there is no floor.
fix: Add a floor between restart-driven connects (for example 1-2 s, or never sooner than the last 429's Retry-After). `Retry` clicks inside the floor are coalesced.

[SEC-5] severity: low
where: control-plane-go/internal/api/middleware.go:72 (`Access-Control-Expose-Headers`), cp-ui/src/lib/api.ts:132, cp-ui/src/lib/logStream.ts:166-168
problem: This assesses the Engineer's CORS finding. The finding is CONFIRMED. The server only exposes `X-Request-ID`, so in cross-origin use (UI on :5173, API on :8080, or any deployment where the UI origin differs from the API origin) `res.headers.get("Retry-After")` returns null. `parseRetryAfter` returns undefined and `classifyTicketError` falls back to 5 s. Rate-limit 429s (`Retry-After: 60`, ratelimit.go:168) are therefore retried after about 5 s or the normal backoff, not 60 s. Same-origin deployments (UI behind the same proxy) are unaffected. The failure is graceful: backoff still grows to 30 s, and the 5 s floor bounds the load. For `/stream`, `Retry-After: 5` (stream.go:189) is unreachable regardless, because EventSource exposes no status or headers. The "too_many_streams" hint is therefore a heuristic (3 refused opens, useLogs.ts:153) and can mislabel other failures after tickets were granted.
Exposing the header is safe. `Retry-After` is not a secret, and the allow-list is still an exact-origin match. `Access-Control-Allow-Credentials` is not involved, so the CTO re-review condition in the code comment does not apply.
fix: Change line 72 to `h.Set("Access-Control-Expose-Headers", "X-Request-ID, Retry-After")` and add a CORS test asserting it. Client side, when a 429 arrives without a parseable header, default to 30-60 s instead of 5 s (`const secs = err.retryAfter ?? 30`).

[SEC-6] severity: low (hardening)
where: cp-ui/src/hooks/useLogs.ts:104,122
problem: The ticket response is not validated. `({ ticket } = await apiFetch<...>)` trusts the type, so a malformed 200 body (proxy error page as JSON, `{}`) gives `ticket === undefined` and opens `/stream?ticket=undefined`. That is a wasted request that then counts toward the stream-cap hint. Separately, `new EventSource(...)` at line 122 sits outside any try/catch, so a bad `API_BASE` throws an unhandled rejection from `connect()` and leaves the badge on "Connecting…" forever.
fix: Require `typeof ticket === "string" && ticket` (otherwise treat as a retryable failure). Wrap the constructor in try/catch and treat a throw as terminal or retryable.

[SEC-7] severity: info
where: cp-ui/src/lib/logStream.ts:96-104,111-120
problem: Deduping on `timestamp + message` also drops genuinely repeated identical lines that share a timestamp (two identical lines in the same millisecond or second). The result is log loss, not a security issue. This is the contract in amendment 4, so it is recorded only. The Clear logic (`clearedUpTo`) uses `<=` on parsed time, so lines with identical timestamps after a Clear can be hidden; that is also acceptable.

[SEC-8] severity: info
where: cp-ui/src/lib/errors.ts:94-106, cp-ui/src/lib/logStream.ts:161-172
problem: No action needed. The ticket, ticket-error and Retry-After paths are clean.
- `humanizeApiError` maps codes and statuses to fixed sentences. It echoes `err.code` only if it contains a space. `ticket` is never put in an error, and `ApiError.message` is just `status: code`. Server error text is the only echoed content, and it is rendered as a text node.
- No `console.*` in `src/hooks` or `src/lib`.
- The ticket appears only in the `EventSource` URL query (useLogs.ts:122), is `encodeURIComponent`-ed, is not stored in state, and is never re-sent. The server logs `r.URL.Path` only (middleware.go:110,118) and redacts tickets in stream errors (stream.go:144).
- Referer: the page URL carries no ticket, and the ticket is in the request URL, never in a Referer.
- Single use: `es.close()` runs in `onerror` and `end` before scheduling the next attempt (useLogs.ts:144,186), so the browser never retries a spent ticket. A fresh ticket is fetched each time.
- Retry-After parsing: `^\d{1,6}$` rejects HTTP dates, negatives, hex and floats. The classifier clamps to 1-60 s, so a hostile huge value cannot stall the client for long and 0 is not a hot loop. Header injection is not possible.
- Stale tickets and races: the `generation` counter (checked at line 106 and 120), the `AbortController` passed to `apiFetch`, the `closed` flag and the `source !== es` guards on every handler mean a late ticket never opens a stream after unmount, a deploymentId change or a pause.
- JSON.parse of events: every parse goes through `parseJSON` (try/catch). Each payload is shape-checked with `isLogEntry`, `source` is checked against an enum, and unknown fields are dropped. A `__proto__` key in the JSON becomes an own property of the parsed object and never reaches the log entries (not copied). The ticket response is parsed inside `apiFetch` within the `connect` try/catch.
- XSS: log text renders as a React text node (`{log.message}`, LogViewer.tsx:206). No `dangerouslySetInnerHTML` or `innerHTML` anywhere in `src`. ANSI/HTML in a line is shown as inert text, `whiteSpace: pre-wrap` is cosmetic only, and style colors come from a constant map keyed by the validated `source` enum. Meta fields are an enum and a number.
- Hidden-tab close (5 min), `online` and `visibilitychange` handling, listener and timer cleanup on unmount and `deploymentId` change: correct (apart from SEC-2).
- Backoff: floor 500 ms (ceiling/2 with ceiling 1 s), cap 30 s, jitter in the upper half. A 429 `minDelayMs` is applied via `Math.max` (useLogs.ts:87), except as noted in SEC-4.
- The `socket.io-client` removal (f70715f) deletes only lockfile entries and one dependency line. It reduces the attack surface.

[SEC-9] severity: medium (pre-existing, not introduced by this task; tracked as Task 15 SEC-6)
where: cp-ui/package.json (`react-router`, `react-router-dom`)
problem: 2 high `npm audit --omit=dev` findings remain (see Tools). `npm audit fix` is available but was deliberately not run. If the app passes user-controlled strings to `navigate()` or `<Link>` (for example a `?next=` redirect after login), the open-redirect advisories apply to this SPA.
fix: Schedule a separate dependency-bump task: update `react-router-dom` to the patched release, then `npm run lint && npm run build`. Check any `navigate(searchParam)` usage.

## Summary

Ticket handling, XSS and parsing are sound. There are no critical or high findings. The main residual risks are an unbounded client log list (SEC-1) and weak reconnect floors against a misbehaving server (SEC-3, SEC-4). The Engineer's CORS finding is real and low severity, and the one-line server fix is safe (SEC-5).

RESULT: FINDINGS (critical 0, high 0, medium 2 [SEC-1, SEC-9 pre-existing], low 5 [SEC-2, SEC-3, SEC-4, SEC-5, SEC-6], info 2 [SEC-7, SEC-8 clean-checks])

# Plan 2 Task 15 security review (cp-ui shared API client)

Commits: 17d7680, d0901ce, 0ebb2b6, 51f9913, 9fbe99c, 4ffffde (branch go-migration). Read-only review, no code changed. Disk checked first: 25 GiB free.

## Tool output
- `gitleaks detect --log-opts="17d7680^..4ffffde"`: 6 commits scanned, no leaks found. `gitleaks detect --no-git --source cp-ui/src`: no leaks found.
- `npm audit --omit=dev` (cp-ui): 5 vulnerabilities (1 moderate, 4 high): react-router/react-router-dom 6.0.0-7.18.1, socket.io-parser 4.0.0-4.2.6, ws 8.0.0-8.20.1, engine.io-client. package.json did not change in these commits, so all are pre-existing. See SEC-6.
- govulncheck/gosec not run: no Go files changed in this range.

## Verified OK
- Sign-out conditions: `refresh.ts` `attempt()` returns "rejected" only for 401; 429, 5xx, network errors, malformed 200 all map to "unavailable"; `api.ts:108-110` signs out only on "expired" and throws `refresh_unavailable` otherwise. Matches amendment 1. A 401 on the retried request after a successful refresh also signs out (`api.ts:112`), acceptable.
- Re-read of `cp_refresh` after a 401 and one retry with the new value (`refresh.ts` lines after `first`): matches the Task 6 ruling.
- Open redirect: `expireSession` redirects to the constant `/login` (`api.ts:80-88`); no user-controlled URL. `handleLogout` redirects to constant `/`.
- XSS: session notice is a constant string stored in sessionStorage (`NOTICE_KEY`) and rendered as a React text node (`App.tsx:221`, `{error}`), so even a tampered value is escaped. No `dangerouslySetInnerHTML`/`innerHTML` anywhere in cp-ui/src. Toast renders `message` as text.
- Error text: `ApiError.message` is `status: code` only (never the body, never a token). `humanizeApiError` uses `Object.hasOwn` (no prototype-key lookup) and returns fixed strings; the only pass-through is a server code containing a space (sentence), rendered as text.
- Tokens not in URLs or logs: shared client sends the access token only in the Authorization header; refresh/logout send the refresh token in a JSON body. The `console.error` calls in ProjectCanvasPage (155, 203, 223) and ServiceCanvas:114 log an `ApiError` (status+code), no secrets.
- reveal=true: server side requires ownership and logs only an audit line without keys/values (`services.go:220-223`), and every response has `Cache-Control: no-store` (`server.go:157`). Client keeps revealed values only in React state, reset on every `loadEnvVars` (`ServiceDetailsPanel.tsx:53-54`), never in storage or logs.
- CORS/credentials: the client uses bearer tokens and no `credentials: "include"` / cookies, so no CSRF surface and no cookie-based CORS dependency. The API must allow the `Authorization` and `Content-Type` headers for the UI origin (Plan 2 CORS_ORIGINS); not a client issue.

## Findings

[SEC-1] severity: medium
where: cp-ui/src/hooks/useLogs.ts:22-24, 35-40 (not touched by Task 15)
problem: Amendment 2 says every other caller goes through the shared client. The log stream hook still has its own `getToken()` reading `cp_token` and opens Socket.io once with `auth: { token }`. The access token is 15 minutes; after expiry the socket cannot reconnect (the stored token is stale and `connect_error` just sets "Connection error"), and it never triggers a refresh or the session-expired notice. Also the access token is sent in the socket handshake `auth` payload, which is not logged in URLs by default, but any reconnect re-sends the (stale) one captured in the closure. Not a leak, but the stream silently breaks and, if the Go API later moves to ticket-based streams (`invalid_ticket`, `too_many_streams` codes exist), this hook is the one remaining caller outside the client.
evidence: `function getToken() { return localStorage.getItem("cp_token") ?? ""; }` and `io(API, { ..., auth: { token } })`.
fix: Use `getToken` from lib/api, read it lazily via `auth: (cb) => cb({ token: getToken() })`, and on `connect_error` with an auth message call a shared refresh (export a `refreshNow()` from api.ts) before reconnecting. Or defer to the stream-ticket work and note it in the plan as an open item.

[SEC-2] severity: low
where: cp-ui/src/lib/api.ts:103-113, cp-ui/src/lib/refresh.ts (attempt)
problem: A refresh response lost in transit (network error after the server already rotated the token) leaves the old `cp_refresh` in storage. The next refresh sends it, falls inside the server grace window (`auth.go:180`, plain 401), nothing changed in localStorage, so the client returns "expired" and signs the user out. This is the one remaining benign case that signs out.
evidence: `if (reread && reread !== used) {...} return "expired";`
fix: Accept as known limitation (rare, only on a dropped response within the grace window) or, when the first attempt ended in a network error, treat a following 401 as "unavailable" once. Document in the Task 15 notes.

[SEC-3] severity: low
where: cp-ui/src/lib/api.ts:67-69
problem: `navigator.locks.request` can reject (SecurityError in some sandboxed or opaque-origin contexts). The rejection propagates out of `refreshOnce` and `apiFetch` as a raw non-ApiError, so `humanizeApiError` shows a generic message and the session is not touched (safe), but the user is stuck without the storage-lock fallback being tried.
fix: wrap in try/catch and fall back to `withStorageLock` on error.

[SEC-4] severity: low (informational for same-origin attackers)
where: cp-ui/src/lib/refresh.ts:84-125 (withStorageLock)
problem: Fallback lock key `cp_refresh_lock` accepts any `id|expiry` value. A same-origin writer (XSS, browser extension) could set a far-future expiry and delay refresh by up to `waitMs` (15 s) per call, after which `fn` runs anyway, so it is not an authentication bypass and cannot cause sign-out. Attacker with same-origin script access already reads `cp_token`/`cp_refresh` directly (see SEC-5), so this adds no capability. `Math.random` for the owner id is fine: it is not a secret.
fix: none required. Optionally clamp `expiry` to `now() + ttl` when reading.

[SEC-5] severity: low
where: cp-ui/src/lib/api.ts:9-22 (localStorage storage of access and refresh tokens)
problem: Both tokens live in localStorage, readable by any script on the origin. The refresh token raises the impact of any XSS from 15 minutes to the refresh lifetime (server rotates and revokes by family, which limits it). Same design as the existing `cp_token` and the amendments, so accepted, not a regression. No XSS sinks were found in this diff.
fix: Backlog: consider httpOnly SameSite cookie for the refresh token with CSRF protection when the API gets a same-site deployment; add a CSP header at the nginx/static host (no inline scripts needed except Vite build output).

[SEC-6] severity: medium (pre-existing dependency advisories, not introduced by Task 15)
where: cp-ui/package.json (react-router 7.x <=7.18.1, socket.io-parser <=4.2.6, ws <=8.20.1 via engine.io-client)
problem: `npm audit --omit=dev` reports 4 high, 1 moderate. Relevance for this SPA: the react-router advisories mostly concern SSR/RSC/`__manifest`/turbo-stream paths the app does not use, but two are client-side relevant: open redirect via backslash or `//` in `<Link>`/`useNavigate` (GHSA-wrjc-x8rr-h8h6, GHSA-2j2x-hqr9-3h42), which only matters if user-controlled paths reach navigate (none found in the diff: all targets are constants or `/projects/${id}` with server IDs). socket.io-parser memory exhaustion and `ws` advisories apply to a malicious server, and `ws` is Node-side only in a browser bundle. Not caused by these commits.
fix: Schedule a dependency bump (react-router, socket.io-client) as its own task with `npm run build` + lint; do not run `npm audit fix` blindly. Mention in the Plan 2 gate.

[SEC-7] severity: low
where: cp-ui/src/components/ServiceDetailsPanel.tsx:93-103, 234
problem: Revealing one variable fetches and holds all decrypted values of the service in component state until the panel reloads or unmounts; hiding a value does not clear it from memory. Values are not persisted, but React DevTools/heap snapshots would show them. The `revealed` map is not cleared when `serviceId` changes unless `loadEnvVars` reruns (it does via the effect, so OK) and is not cleared on unmount explicitly (GC handles it).
fix: Optional: drop the map when the last value is hidden, and/or expire it after ~60 s. Server-side per-key reveal would be tighter but is an API change; accept for now.

## Result
RESULT: FINDINGS (0 critical, 0 high, 2 medium [SEC-1 unmigrated socket caller, SEC-6 pre-existing dependency advisories], 5 low [SEC-2, SEC-3, SEC-4, SEC-5, SEC-7])

No blocking issue in the refresh single-flight, sign-out conditions, notice rendering, error mapping or reveal handling. SEC-1 should be fixed or explicitly deferred (recorded in the plan) before Task 6 and 15 cutover.

# Plan 2 plan-end Security review

Reviewer: Security. Range: `git diff 53f064a..HEAD` (branch `go-migration`, HEAD 8846c3d, 187 commits). Scope per brief: tools, cross-cutting model vs spec §5/§12, open cutover items, missing checklist items. Read-only: no code changed, nothing pushed, no `.env` content printed (only booleans and lengths below).

## Recommendation: CONDITIONAL GO (security view)

No critical or high finding in code. The Go API enforces auth and ownership on every route, secrets are encrypted at rest, tokens expire and rotate, and every tool is clean except `npm audit` (not exploitable in this SPA, see SEC-P2-3). The cutover is GO provided the gating conditions in section 4 are met. Two of them are hard stops that cannot be waived by this review: the Plan 1 rollback window (open until 2026-10-25 per `decisions.md`, user must close it in writing) and a verified off-machine backup of the DB plus `ENV_ENCRYPTION_KEY` before `encrypt-env` (the only irreversible step).

Finding counts: critical 0, high 0, medium 4 (one process hard stop, one policy item with no exploit path), low 8, info 6.

## 1. Tool results

| Tool | Result |
|---|---|
| `govulncheck ./...` (alarm 240, exit 0) | 0 affecting code. 1 vulnerability in a required module, not called: GO-2026-5932 in `golang.org/x/crypto@v0.58.0` (no fix published). Re-check at cutover day. |
| `gosec -quiet -exclude=G204 ./...` | exit 0, no findings. |
| `golangci-lint run` | `0 issues.` |
| `gitleaks detect --log-opts="53f064a..HEAD"` | 187 commits, no leaks. |
| `cd cp-ui && npm audit --omit=dev` | 2 high: `react-router` 6.0.0-7.18.1 (installed 7.14.0) and `react-router-dom`; fix via `npm audit fix` (7.18.x+). socket.io/ws/engine.io advisories from earlier rounds are gone (socket.io-client removed in Task 16). |

## 2. Cross-cutting walk

### 2.1 Route table (`control-plane-go/internal/api/server.go`)

Middleware chain for all routes (server.go:`buildHandler`): `withSecurityHeaders` (nosniff, no-store) -> `withRequestID` (regex `^[A-Za-z0-9-]{1,64}$`, else random) -> `withCORS` (exact allow-list) -> `withRecover` (path-only log, metrics by pattern, panic -> 500) -> `jsonErrors` -> mux. Body cap 1 MiB (`respond.go:35`), one JSON value, ReadHeaderTimeout 10 s, ReadTimeout 30 s, WriteTimeout 30 s, MaxHeaderBytes 64 KiB.

| Line | Route | Auth middleware | Ownership check | Tests (no token / foreign) |
|---|---|---|---|---|
| 103 | GET /healthz | public | n/a | TestHealthReadyAndMetrics |
| 104 | GET /readyz | public (returns `{ok}` only) | n/a | same |
| 108 | POST /auth/register | authLimit 10/min burst 5 | n/a | TestRegister*, TestAuthRoutesAreRateLimited |
| 109 | POST /auth/login | authLimit | n/a; dummy bcrypt for unknown email | TestLoginUnknownEmailPaysForBcrypt |
| 110 | POST /auth/refresh | refreshLimit 60/min burst 20 | token hash -> family | TestRefresh* (9 tests) |
| 111 | POST /auth/logout | refreshLimit | presenter's family | TestLogout* |
| 113 | POST /dev/login | registered only if `DevLogin` (APP_ENV=development) | n/a; loopback bind | TestDevLoginOnlyInDevelopment |
| 116 | POST /projects | `private` | owner = caller; deleted user -> 401 | projects_test:26-27,103 |
| 117 | GET /projects | `private` | `ownerUserId = caller` | projects_test |
| 118 | GET /projects/{id} | `private` | `GetProjectForOwner`, 404 | projects_test:28 |
| 120 | POST /services | `private` | `ownsProject`, 404 | services_test:95 |
| 121 | GET /services | `private` | `ListServicesForOwner` (owner join) | services_test:96 |
| 122 | DELETE /services/{id} | `private` | `ownedService` (live + owned) | services_test:518, TestDeleteServiceRejectsBadIDs |
| 123 | POST /services/{id}/deploy | `private` | `ownedService`; deployment+outbox in one tx | services_test:255, TestDeployDeletedServiceIs404 |
| 124-126 | GET/POST/DELETE env | `private` | `ownedService` first (POST validates body before ownership, no leak) | TestServiceEnvOwnership (services_test:342,376) |
| 128-130 | canvas GET/PUT/auto-layout | `private` | `ownsProject`; writes scoped `UpdateServicePositionInProject(id, projectId)` | TestCanvasRequiresOwnershipAndRejectsBadIDs; no-token case covered only for position/connection routes |
| 131 | POST /services/{id}/position | `private` | `GetServiceForOwner` | canvas_test:201,226 |
| 132 | POST /services/connections | `private` | `CreateConnectionForOwner`: both ends live, same project, owner = caller, in SQL | canvas_test |
| 133 | DELETE /services/connections/{id} | `private` | `GetConnectionForOwner`: both ends owned | canvas_test:497,528 |
| 135 | GET /deployments | `private` | `ownedByCaller` subquery, LIMIT 50, hides soft-deleted | deployments_test:12,52,62 |
| 137 | GET /logs/deployments/{id} | `private` | `ownedDeployment`; limit capped 1000, search 256 B, LIKE-escaped, line cap 8 KiB | logs_test:74,92 |
| 138 | GET /logs/health | `private` | caller's own stream count only | stream_limits_test:237 |
| 145 | POST .../stream-ticket | streamLimit then `private` | `ownedDeployment`; ticket value `uid:depId` | ticket_test:13,38-39 |
| 148 | GET .../stream | streamLimit then single-use ticket (not Bearer) | ticket == `uid:<URL id>`, ownership re-checked at redeem, per-user 5 / per-deployment 20 caps, 1 h max | stream_test (ticket reuse, wrong deployment, never logged, SSE injection) |

Result: every route except health, `/auth/*`, `/dev/login` (dev only) and the ticket stream goes through `s.private`; ownership is enforced in SQL joins (not in handlers) and answers 404 for foreign, deleted and malformed ids (`pathID`/`idShape`). `s.private` accepts only `Authorization: Bearer` (a `?token=` is rejected, tested). I found no IDOR path. Gap: there is no route-enumeration test that walks `mux` and asserts 401 for every non-public pattern (SEC-P2-9).

### 2.2 CORS
`middleware.go:65` exact-match allow-list, no `Allow-Credentials` (comment pins a re-review if added), `Vary: Origin`, config rejects `*` (config.go:181). Preflight answered 204 for any origin but without allow headers when not listed. Tests: near-miss origins, Retry-After exposure. Default when `CORS_ORIGINS` is unset is `http://localhost:5173` even in production (SEC-P2-11).

### 2.3 Rate limiter and TRUSTED_PROXIES
`ratelimit.go`: three per-IP buckets (auth 10/min, refresh 60/min, stream 60/min), LRU capped at 100k clients, IPv6 reduced to /64, XFF honoured only when the TCP peer is inside `TRUSTED_PROXIES`, right-most untrusted entry, fail closed to the peer on any parse problem. Correct. In memory, single instance (spec §12). Limiter runs before auth on the stream-ticket route, so unauthenticated requests spend the shared bucket (low, SEC-P2-12). There is no limit or quota on authenticated writes (deploy, project create, env writes): SEC-P2-5. Today no nginx fronts the API (`control-plane/infra/nginx` only has the default 404 server and per-service routes), so with `TRUSTED_PROXIES` empty and the API reached directly the key is the real peer. That is correct for the dev stack and wrong the moment a proxy is added.

### 2.4 JWT, refresh families, Node tokens
- Access: HS256 only (`WithValidMethods`), `exp` required (`WithExpirationRequired`), 15 min, `sub` required (`auth/jwt.go:41-52`). Node tokens (`jwt.sign({sub})`, no `exp`; Node `index.ts:79`, `authController.ts:28,57`) are rejected by the Go API: forced re-login at cutover (release note has it). That is the desired behaviour; do not "fix" it.
- Refresh: 256-bit `crypto/rand`, stored as SHA-256 hash only (test TestRefreshTokenStoredOnlyAsHash), rotated in one tx under a family advisory lock (`store/refresh.go:59`), replay after the 10 s grace revokes the family and logs a Warn without the token, unknown/expired/revoked/replayed all give the same 401 body. Logout revokes the family, idempotent. Correct.
- Session lifetime is sliding: each rotation issues a fresh 30-day token, so a stolen-then-kept-alive family never expires absolutely and nothing purges expired rows (SEC-P2-8, low).
- The live `.env` files use the placeholder `JWT_SECRET` (20 chars, equals `dev-secret-change-me`) with `APP_ENV=development`. Accepted by `RequireAPI` only because dev mode skips the placeholder/length check, and dev mode binds 127.0.0.1. Fine on the loopback dev stack, forgeable the instant it is exposed (section 4, item 8).

### 2.5 Secrets at rest
`secrets.Box`: AES-256-GCM, random 12-byte nonce from `crypto/rand`, `enc:v1:` prefix, key length checked, generic decrypt error (no key/plaintext). The API always encrypts on `POST env`; list masks to `********` unless `?reveal=true`, and a reveal writes an audit line with ids only. The masked placeholder cannot be saved back. Config `LogValue` redacts DB/Redis/ClickHouse URLs (including query passwords) and prints the key only as a 12-hex fingerprint (api `logStartup`, worker `logEncryptionKey`). No AAD binds ciphertext to its row (a DB writer could swap two env values between services): info. `Service.repoUrl` may carry a token in its userinfo and is stored in plain text (responses redact via `publicService`; release note says repo URLs must be public https, but `ValidRepoURL` does not forbid userinfo): SEC-P2-7. `Deployment.volumePath` (host path) is returned to the owner (Node parity): info.

### 2.6 Dev login
Route exists only when `DevLogin` is set; it is set only from `cfg.IsDevelopment()`; `listenAddr` then binds 127.0.0.1 only; `register` refuses `dev@local`; the stored dev password is not a bcrypt hash so `/auth/login` cannot succeed for it. Risk: `APP_ENV` falls back to `NODE_ENV`, and both existing `.env` files set `NODE_ENV="development"` (SEC-P2-2).

### 2.7 Request id and log redaction
Request id sanitised (CRLF test). Access log is path-only (never the query), so the stream ticket never reaches Go logs (TestStreamTicketNeverLogged; `redactTicket` on stream errors). The refresh token and passwords are never logged. `internalError` logs `err` (pgx errors do not carry bound args). Edge proxies are the remaining place a ticket can leak (section 4).

### 2.8 SSE tickets
256-bit random, `GETDEL` single use, 60 s TTL, format-checked before Redis, one uniform 401 `invalid_ticket` for every failure, bound to user and deployment, ownership re-checked at redeem, limiter runs before redeem so a 429 does not burn the ticket, write deadline per write, shutdown context ends streams. No issues.

### 2.9 Teardown and delete
`DELETE /services/{id}` soft-deletes and writes the `teardown.requested` outbox event in one tx; managed DBs require an explicit `purgeData` (`purge_choice_required`, `invalid_purge_data`); concurrent deletes write one event; owner check is the live-service join so a deleted service's deployments/logs/streams are hidden (PROD-7 closed). Worker side was reviewed in Tasks 12 round 1-3 (cancel in-flight deploys, scrub repoUrl, delete env rows, `ValidServiceID` for route files). Kept volumes (`cp-vol-*`) are operator-only; documented.

### 2.10 Migrations 00003 and 00004
00003: `Service.deletedAt`, `RefreshToken` table (FK cascade on user), index on Deployment. 00004: adds `familyId`/`rotatedAt`, backfills `familyId = id`, then `SET NOT NULL`. Both are additive, no row is dropped or rewritten besides the backfill; Down is symmetric. 00003 is already applied on the dev DB; 00004 is pending there per START-HERE (re-verify at cutover). Data-loss risk: none from the SQL. Operational risk: once `deletedAt` exists the frozen Node API (Prisma schema without it) lists and deploys soft-deleted services, and after `encrypt-env` Node would hand `enc:v1:` ciphertext to containers (SEC-P2-4: Node cannot serve this DB again; rollback is only restore-from-backup).

### 2.11 encrypt-env runbook and tool
Tool: per-row compare-and-swap (`ReplaceEnvVarIfEquals ... AND "value" = $3`), idempotent, wrong-key guard (refuses when some `enc:v1:` rows exist, none decrypt and plain rows exist), `--dry-run`, refuses an unknown target, prints counts and service ids only, never a key name or value, 8 MiB cap, interruption-safe. Runbook has stop-Node-first, backup with umask 077 and PG* vars, mandatory key-fingerprint comparison, a suspect-row SQL that deletes exactly one row or nothing, and Postgres-log warning. Data-loss exposure comes only from key loss or a mismatched key on a DB with no `enc:v1:` rows yet; both are covered by the mandatory fingerprint step. Doc drift: runbook says the Go API/worker "do not log a fingerprint", but both do (api `logStartup`, worker `logEncryptionKey`); it is the easier comparison path (SEC-P2-13, info). Missing from the runbook: an explicit "this is one-way for Node" statement and a rollback paragraph (SEC-P2-4).

### 2.12 UI token storage
Access and refresh tokens are in `localStorage` (`cp-ui/src/lib/api.ts:12-22,47`), with a Web Locks/storage lock for single-flight refresh and a 30 s client timeout. No `dangerouslySetInnerHTML`/`innerHTML`/`eval` in `src`; log lines render as text. Impact: any XSS yields a 30-day sliding refresh token (SEC-P2-6, medium by design; no CSP is set on the Vite/static host). The stream ticket goes in the query string only for the EventSource GET. All `navigate()`/`<Navigate>` targets are constants or server-issued ids, so the react-router redirect advisories have no user-controlled input here.

## 3. Findings

[SEC-P2-1] severity: medium (process hard stop, not a code defect)
where: docs/superpowers/review/decisions.md "Plan 1 worker cutover" (rollback window until 2026-10-25); START-HERE.md
problem: The API cutover is the point of no return for Node, and decisions.md forbids starting it inside the Plan 1 window unless the user closes it in writing. Today is 2026-10-13.
fix: Obtain the user's written approval to close the window early, or wait until 2026-10-25. Record it in decisions.md.

[SEC-P2-2] severity: medium
where: control-plane-go/internal/config/config.go:132; control-plane/.env and control-plane-go/.env (`NODE_ENV="development"`)
problem: `APP_ENV` falls back to `NODE_ENV`. Both live env files set `NODE_ENV=development` (the Go one also sets `APP_ENV=development`). Anyone who copies these files to a shared or internet-reachable host gets dev login (no credentials) plus the placeholder JWT secret (tokens forgeable by anyone who knows the public string). Loopback bind limits this to the local machine today. Also `control-plane/.env` is mode 644 (the Go one is 600).
evidence: JWT_SECRET length 20, equals the placeholder; `APP_ENV=development`.
fix: For any non-loopback deployment set `APP_ENV=production` explicitly, remove `NODE_ENV` from the Go env, set `JWT_SECRET` to `openssl rand -base64 32`, `chmod 600 control-plane/.env`. Optional: log which variable selected the environment.

[SEC-P2-3] severity: medium (policy; not exploitable here)
where: cp-ui/package.json:18 (`react-router-dom ^7.14.0`, installed 7.14.0)
problem: `npm audit --omit=dev`: 2 high. The listed advisories are framework/SSR/RSC (turbo-stream, `__manifest`, RSC, SSR hydration) or need user-controlled navigation targets; cp-ui is a plain `BrowserRouter` SPA with constant targets, so none is reachable. Backlog (T15 SEC-6, T16 SEC-9) says bump before cutover.
fix: Bump to a patched 7.x in its own commit, `npm run lint && npm run build`, re-run audit. Cheap; do it, but I do not consider an unfixed audit a cutover blocker on exploitability grounds.

[SEC-P2-4] severity: medium
where: docs/superpowers/runbook/encrypt-env.md; docs/superpowers/release-note-plan2.md
problem: After migration 00003/00004 and `encrypt-env`, the frozen Node API/worker cannot serve this DB (Prisma schema lacks `deletedAt`/`RefreshToken`, would deploy ciphertext as env values). There is no written rollback step and no statement that the point of no return is the `encrypt-env --yes` plus the Node stop. A mid-cutover rollback therefore needs restore-from-backup (losing writes made since) and nobody has rehearsed it.
fix: Add a "Rollback" section: stop Go API, restore the pre-cutover dump (and keep the Go worker/Node worker mutually exclusive, per Plan 1 STAFF-7), restart Node. Rehearse once on a throwaway DB. Say explicitly that encrypting is one-way for Node.

[SEC-P2-5] severity: low
where: control-plane-go/internal/api/server.go:116-135 (all authenticated writes)
problem: No per-user quota or rate limit on project/service creation, deploys (each deploy = DB rows + outbox event + a build of an arbitrary https repo), env writes or list endpoints. With open `/auth/register` (no email verification, 409 enumeration accepted as Node parity) any visitor can queue unbounded builds. Single-user dev stack: acceptable; public host: not.
fix: Already in backlog (T8 SEC-3, Plan 3). Do not expose registration publicly before it lands, or disable `/auth/register` at the proxy.

[SEC-P2-6] severity: low
where: cp-ui/src/lib/api.ts:12-22
problem: Refresh token (30 d, sliding) lives in `localStorage`; XSS means durable account takeover. No CSP.
fix: Plan 3/4: move the refresh token to an `HttpOnly; Secure; SameSite=Strict` cookie scoped to `/auth`, add a CSP on the UI host. Not a cutover item.

[SEC-P2-7] severity: low
where: control-plane-go/internal/gitsrc/gitsrc.go:27-47; store `Service.repoUrl`
problem: `ValidRepoURL` allows `https://user:token@host/...`; the token is stored in clear in `Service.repoUrl` (redacted in responses and scrubbed at teardown) and the host may be a private/loopback address (blind SSRF, already in backlog P1 T8/T22).
fix: Either reject userinfo (matches the "public https" release note) or encrypt the column; host allow-list in Plan 3.

[SEC-P2-8] severity: low
where: control-plane-go/internal/store/refresh.go:59-110
problem: Sliding 30-day refresh tokens with no absolute family lifetime and no purge of expired/revoked rows (backlog T2 SEC-3).
fix: Add `familyExpiresAt` or cap family age (e.g. 90 d) and a periodic delete of rows past `expiresAt + 7 d`.

[SEC-P2-9] severity: low
where: control-plane-go/internal/api/*_test.go
problem: Auth coverage is per route, not structural. Missing explicit no-token assertions for `GET/PUT /projects/{id}/canvas`, `POST .../auto-layout`, `POST /services/connections`, `POST/DELETE /services/{id}/env...`.
fix: One table test that walks the registered patterns and asserts 401 without a token for everything outside {healthz, readyz, auth/*, dev/login, stream}, so a future route registered with `HandleFunc` instead of `private` fails CI.

[SEC-P2-10] severity: low
where: control-plane-go/api (untracked, 28 MB binary; `.gitignore` only lists `bin/` and `.env`)
problem: A stray `go build` output sits in the working tree; one `git add -A` would commit it.
fix: Delete it or add `/api` to `control-plane-go/.gitignore`; keep staging by explicit path. Same for `ruvector.db`, `agentdb.rvf*`, `README_NEW.md` (already noted in CLAUDE.md).

[SEC-P2-11] severity: low
where: control-plane-go/internal/config/config.go:140
problem: `CORS_ORIGINS` defaults to `http://localhost:5173` even when `APP_ENV=production`; production would silently trust a localhost origin (no credentials mode, so impact is low) and fail to allow the real UI origin.
fix: Require `CORS_ORIGINS` explicitly in production (`RequireAPI`).

[SEC-P2-12] severity: low
where: control-plane-go/internal/api/server.go:145
problem: The stream bucket wraps `private`, so unauthenticated requests spend the same per-IP bucket as a logged-in viewer behind the same NAT or proxy (60/min). Availability only.
fix: Acceptable; revisit with TRUSTED_PROXIES testing.

[SEC-P2-13] severity: info
where: docs/superpowers/runbook/encrypt-env.md step 2
problem: Says the Go API and worker do not log a key fingerprint; both do (`envKeyFingerprint` at startup).
fix: Update the doc; use the startup line as the comparison source.

[SEC-P2-14] severity: info
where: control-plane-go/go.mod (`golang.org/x/crypto v0.58.0`)
problem: GO-2026-5932, no fixed version, code does not call the vulnerable symbol (bcrypt only). Re-run govulncheck on cutover day.

[SEC-P2-15] severity: info
where: control-plane-go/internal/secrets/secrets.go:44
problem: GCM without AAD: ciphertext is not bound to (serviceId, key). Needs DB write access to exploit. Consider `enc:v2` with AAD in Plan 3.

[SEC-P2-16] severity: info
where: control-plane-go/internal/api/server.go (security headers), nginx
problem: API sends nosniff and no-store only; HSTS/CSP/frame headers must come from the TLS-terminating proxy that does not exist yet in this repo.

[SEC-P2-17] severity: info
where: control-plane-go/internal/store/deployments.go:89-103
problem: `volumePath` (host path) and `containerId` are returned to the owner (Node parity).

[SEC-P2-18] severity: info
where: control-plane-go/internal/api/services.go:207-224
problem: `GET /services/{id}/env?reveal=true` returns plaintext to any valid Bearer without re-authentication (audit log line only). Matches the UI design; revisit with 2FA/step-up later.

## 4. Cutover blockers and backlog items: verdicts

| Item (source) | Blocks cutover? | Verdict |
|---|---|---|
| Plan 1 rollback window open to 2026-10-25 (decisions.md) | YES (process) | SEC-P2-1: user's written close, or wait. |
| Backups: DB dump, `ENV_ENCRYPTION_KEY` copy, off-machine (decisions.md 2026-10-11 follow-up: user must back up `~/cp-backups` and the key copy) | YES | Verify the files exist, mode 600, and a copy is off this Mac before `encrypt-env --yes`. |
| `encrypt-env` runbook (T13) | YES, run exactly as written | Key fingerprint check is mandatory; `suspect=0`, `too_large=0`, final dry run `would_encrypt=0`. Stop Node API first. Add rollback text (SEC-P2-4). |
| Migration 00004 pending on dev DB (START-HERE) | YES (functional) | Without it refresh/login 500. Apply with `go run ./cmd/migrate up` during the controlled step, after the backup. Re-check `status`. |
| TRUSTED_PROXIES + nginx XFF overwrite, `proxy_buffering off`, `proxy_read_timeout` >= 75 s, no query string in access log (T5 SEC-1, T11, T14 SEC-3) | Only if a proxy fronts the API | Implemented and tested in Go. Currently no API vhost exists and the dev API is reached directly (loopback), so with an empty list the rate limiter is correct. If nginx is introduced at cutover it BLOCKS until: `X-Forwarded-For $remote_addr`, only the nginx address in `TRUSTED_PROXIES`, access log format without `$request`/`$request_uri` (use `$uri`), buffering off, and a smoke test through the proxy (6th login 429 per client, not global). |
| `APP_ENV` / `NODE_ENV` fallback (T14 lows) | Only for non-loopback exposure | SEC-P2-2. |
| `npm audit` react-router (T15 SEC-6, T16 SEC-9) | No (not exploitable), do it anyway | SEC-P2-3. |
| `legacyDefaultRef` / blanket `TenantCaps` removal (decisions 2026-10-11) | No | Post-cutover cleanups; security-neutral. `legacyDefaultRef` removal must follow, not precede, confirming the Go API sends Branch/HEAD/hex (it does: `deployService` + `defaultRef`). |
| Node-token check (START-HERE) | No | Verified: Node tokens carry no `exp` and are rejected by `WithExpirationRequired`; users re-login. Keep this behaviour. |
| Teardown runbook and live teardown smoke test (T12) | YES (smoke test before sign-off) | Run purge true/false on a throwaway Redis service, kill Docker mid-teardown, alert rule on `cp_teardown_activity_failures_total`. |
| Legacy cross-tenant connection rows (T10) | No | Fixed in queries (ListProjectConnections both-ends); record the legacy count query result. |
| Node-era managed DB exposure (Plan 1 SEC-1: Mongo admin/admin123 on 0.0.0.0, containers on `cp_net`) | Must be closed or accepted in writing (Plan 1 gate condition, not shown as closed) | Check `docker ps` for `0.0.0.0:` bindings and `cp_net` members; Mongo services redeploy only after the user decides about orphaned volumes. |
| SSRF host allow-list, builder egress to 169.254.169.254, image digest pinning | No on a single-user dev stack; YES before multi-tenant/public | Plan 3. |
| Quotas / open registration (SEC-P2-5) | No on loopback dev; YES before public | Disable `/auth/register` at the edge until quotas land. |

## 5. Missing from the cutover checklist (add these)

1. Written user approval closing the Plan 1 rollback window (or date >= 2026-10-25), recorded in decisions.md.
2. Pre-flight evidence that backups exist and are restorable: `pg_restore --list` on the dump, key copy fingerprint equals the running key fingerprint (api/worker startup line), copy off the machine.
3. Rollback procedure and one rehearsal on a throwaway DB (SEC-P2-4); state that `encrypt-env` and migration 00003/00004 make the Node API unusable on this DB.
4. Rotate `JWT_SECRET` to >= 32 random bytes (invalidates every outstanding Node token too) and set `APP_ENV` explicitly; remove `NODE_ENV=development` for any non-loopback host; `chmod 600` env files (SEC-P2-2).
5. Mutually exclusive Node and Go API on the same DB and port: stop Node API first (also required by `encrypt-env`), confirm port 8080 owner, UI built with the new client served before or with the API.
6. If any proxy is added: the nginx checklist in section 4 plus a through-the-proxy test of rate limit per client and of "ticket not in access log" (`grep ticket= access.log` returns nothing).
7. CORS: set `CORS_ORIGINS` to the real UI origin; verify it with a preflight from that origin and from a foreign origin (SEC-P2-11).
8. Post-cutover smoke set from the backlog: foreign-id 404 on every resource, reused ticket 401, 6th stream 429, restart recovery of the log panel, delete with and without purge, reveal audit line present and containing no values.
9. Post-cutover grep of the Go API and worker logs for the strings `ticket=`, `refreshToken`, `password`, and an env value you saved as a canary: expect none.
10. Re-run `govulncheck`, `npm audit` after the react-router bump, and `gitleaks` on cutover day; confirm `control-plane-go/api` and the untracked `*.db`/`*.rvf` files are not staged.
11. Plan 1 gate leftover: `docker ps` shows no `0.0.0.0:` bindings and no containers on `cp_net`, or the exceptions are written down.
12. Delete or shred the DB dumps containing plain-text env values only after sign-off (runbook already says when); keep them mode 600 until then.

## 6. Decision

GO for the cutover from a security view, conditional on: items 1-5 of section 5 (hard stops), the `encrypt-env` runbook executed as written, migration 00004 applied after the backup, and the teardown smoke test. Not blocking but should be done in the same window: react-router bump, the proxy checklist if a proxy appears, and the Node-era exposure check. The code itself needs no change before cutover.

RESULT: FINDINGS (critical 0, high 0, medium 4 of which 1 is process and 1 is a policy item with no exploit path, low 8, info 6)

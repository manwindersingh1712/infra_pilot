## Security

Reviewed d531344 (Plan 2 Task 6): control-plane-go/internal/api/{auth,server,respond}.go, store/refresh.go, store/users.go, auth/*.

### Tools
- govulncheck (hard-limited, full path): 0 reachable vulnerabilities; 1 in a required module not called (known GO-2026-5932, x/crypto/openpgp, already accepted in backlog).
- gosec ./...: 2 issues, both G204 in internal/runtime (logstream.go:61, exec.go:26), outside this commit (pre-existing, argument-list exec by design). gosec on ./internal/api, ./internal/auth, ./internal/store: no issues. (Whole-repo run printed "export data version" import warnings from a stale build cache; per-package run was clean.)
- gitleaks git --redact (repo root): 331 commits, no leaks.

### Findings

[SEC-1] severity: medium
where: control-plane-go/internal/api/auth.go:166-176, store/refresh.go:27-54
problem: Refresh reuse detection is not atomic with token issue. Two concurrent refreshes of one token: A wins the UPDATE, B sees revoked and runs revoke-all, then A's issueTokens inserts a fresh token AFTER the revoke-all. The thief/victim whoever won keeps a live 30-day session, so reuse detection fails to kill the family exactly in the race it exists for (attacker and victim refreshing at the same time). Also Consume and issue are separate statements: if CreateRefreshToken fails after Consume, the user's session is lost.
evidence: ConsumeRefreshToken revokes then returns; s.issueTokens runs afterwards with no shared transaction or family id. No concurrent-refresh test exists.
fix: Do consume + insert of the replacement in one transaction, and give tokens a family/generation (or make revoke-all also mark a user "tokensValidAfter" timestamp checked on consume). At minimum add a two-goroutine test; accept the double-submit-logs-user-out behaviour explicitly in the spec.

[SEC-2] severity: medium
where: control-plane-go/internal/api/server.go:69-72, ratelimit.go
problem: One limiter (one bucket per IP) is shared by register, login and refresh. A burst of refreshes (UI with several tabs, or any client on a shared NAT) can lock the same IP out of login, and an attacker can burn a victim IP's bucket. This is the shared-bucket issue already recorded; confirmed present. Because the key is the TCP peer and X-Forwarded-For is ignored, behind nginx/any reverse proxy every user shares ONE bucket (global 5-burst, one token per 6s) so legitimate logins are throttled for everyone, and one script can deny login to all users. Burst default is 5 (comment says 10/min refill).
fix: Separate limiters per endpoint (login/register strict, refresh looser), and either a trusted-proxy setting (TRUSTED_PROXIES CIDRs, use the right-most untrusted XFF entry) before the API is put behind nginx, or document that the API is exposed directly. Consider a per-email login limiter against distributed credential stuffing (per-IP only is bypassable).

[SEC-3] severity: low
where: auth.go:93-103 (register)
problem: Explicit 409 email_already_exists enumerates registered emails (Node parity, accepted by spec parity), reachable at 5 requests/burst per IP. dev@local returns 409 before bcrypt, so it is also distinguishable by timing (negligible, since 409 already tells everyone).
fix: Accept as parity and record in backlog; real fix is email verification flow (roadmap).

[SEC-4] severity: low
where: auth.go:121-139, auth/password.go
problem: Login timing equalisation covers unknown email but not a stored hash that is malformed (e.g. dev@local has password 'dev-password-not-used', store/users.go:37): bcrypt returns immediately, so POST /auth/login reveals that dev@local exists (dev DB only). Login also accepts passwords >72 bytes and returns fast on x/crypto v0.58 (ErrPasswordTooLong), same for both branches, so no leak there.
fix: In CheckPassword failure path for a parse error, run a compare against dummyHash(); or give the dev user a real bcrypt of a random value.

[SEC-5] severity: low
where: auth.go:70-78, 101-110
problem: No length bounds on email or name beyond the 1 MiB body cap; a 1 MiB email/name is stored in User and echoed in every auth response. Email is case-sensitive: A@x.com and a@x.com are separate accounts (identical to Node, so not a regression, but allows lookalike registration and confusion). mail.ParseAddress accepts forms Node's zod .email() rejects (e.g. dev@local, no TLD, quoted local parts); a.Address == s guards display-name forms only.
fix: Cap email at 254 and name at ~200 chars; lowercase+trim email on register and login together with a one-off data migration (plan an lower(email) unique index), or log as a deliberate Node-parity decision. Reject quoted local parts.

[SEC-6] severity: low
where: server.go:73 (logout), auth.go:174-190
problem: POST /auth/logout is outside the rate limiter and unauthenticated (acceptable: it needs the token). An unauthenticated caller can issue unlimited DB UPDATEs, low-cost DoS only. Logging out with a previously rotated token (already revoked) is treated by Consume as reuse on the next /auth/refresh, i.e. a stale client logs the whole user out; by design.
fix: Put logout behind a looser limiter.

[SEC-7] severity: low (informational, for the wiring task)
where: auth.go / server.go:74
problem: DevLogin is only a Deps bool; no cmd sets it yet (grep finds no non-test caller), so the gating (APP_ENV=development only, unset means production) must be reviewed when cmd/api is wired. Handler gating itself is correct: route not registered unless set, test TestDevLoginOnlyInDevelopment covers it. Dev user is created with a non-bcrypt password so it can't be logged into via /auth/login.
fix: At wiring, derive DevLogin only from config.Env == "development", and refuse to start with DevLogin when the listen address is non-loopback in production-like env.

### Checked and fine
- Unknown-email login: dummy bcrypt (same cost 10, sync.OnceValue), same 401 invalid_credentials body for unknown vs wrong password; malformed stored hash fails closed (401).
- Password policy: 8..72 bytes (bytes, not chars) on register, 400 otherwise; no silent bcrypt truncation. No complexity rules (Node parity: min 8).
- Refresh tokens: 32 bytes crypto/rand, SHA-256 hashed at rest (tested), unique index, 30-day expiry in SQL (now()), UPDATE ... WHERE revokedAt IS NULL AND expiresAt > now() is a single atomic claim so two concurrent requests cannot both succeed; unknown/expired/revoked/replayed all return the same 401 body; replay log carries only requestId, never the token or user id.
- Logout: revokes exactly one token, idempotent, no existence oracle, other sessions untouched.
- Response bodies: only token/refreshToken/user{id,email,name}; no hash. Errors are fixed strings; internalError logs err server-side only and returns internal_error. Passwords/tokens never passed to slog.
- JSON decoding: MaxBytesReader 1 MiB, single value, trailing data rejected, 413 on overflow. Rate limiter runs before body parsing. Access token HS256 with pinned method, exp required, 15 min.
- SQL: all parameterised.

RESULT: FINDINGS (0 critical, 0 high, 2 medium, 5 low)

## Staff

Reviewer: Staff engineer. Commit d531344, `control-plane-go/internal/api` (auth.go, auth_test.go, respond.go, server.go, api_test.go) against spec §5 and plan lines 1808-2155.

Gate: `go build ./...`, `go vet ./...`, `golangci-lint run` are clean (0 issues). `go test -race ./internal/api/` passes (about 42 s, real Postgres via testcontainers).

### Deviations (the Engineer's list; I judged the five named in the brief and the ones visible in the diff)
- decodeJSON(w, r, dst) instead of decodeJSON(r, dst): justified. `http.MaxBytesReader` needs the writer, and the plan's version would turn an oversized body into a 400 with no 413. `writeDecodeError` maps `*http.MaxBytesError` to 413. Covered by TestAuthBodyErrors for all four POST routes.
- No expired-token sentinel: justified. The single UPDATE filters on `expiresAt > now()`, and the follow-up SELECT maps "unknown or not revoked" to ErrNotFound. Expired tokens therefore get the same 401 and do not trigger mass revocation. That is the right call: expiry is not evidence of theft.
- dev@local reserved with 409 on register: justified, with one note. It stops anyone from squatting the account that `/dev/login` upserts. It is a small behaviour change from Node (Node allowed it). Because `UpsertDevUser` stores the non-bcrypt string `dev-password-not-used`, nobody can log in to that row through `/auth/login`. The 409 reuses `email_already_exists`, so it leaks nothing new.
- nolint marker kept on harness.token (comment reworded to "first used by Task 7"): acceptable. Lint is clean. The marker must be removed in Task 7; it is not currently tracked anywhere except the comment.
- nolint markers removed from respond.go helpers now that they are used: correct.
- Tests split and added (hash-only storage, lifetime, expiry, reuse log, 72-byte limit, malformed stored hash, body errors, per-route and per-client rate limits): an improvement over the plan, no objection.
- Dummy bcrypt compare (SEC-3), 72-byte check (STAFF-2) and the token-free log line on reuse are additions required by earlier review findings. All are in place and tested.

### Mutation results
Throwaway copy via `git archive d531344` outside the repo; each mutant run with `go test ./internal/api/`.

| Mutation | Caught by |
|---|---|
| remove the dummy bcrypt compare | TestLoginUnknownEmailPaysForBcrypt |
| remove the 72-byte check | TestRegisterPasswordLengthLimit |
| skip mass revoke on reuse | TestRefreshReuseRevokesEveryTokenOfThatUser |
| accept expired refresh tokens (drop `expiresAt > now()`) | TestRefreshExpiredToken |
| register /dev/login in production (`if true`) | TestDevLoginOnlyInDevelopment (and the reuse test, via rate-limit side effects) |
| log the refresh token on reuse | TestRefreshReuseLogsWithoutTheToken |
| store the plain token instead of the hash | six tests fail (incl. TestRefreshTokenStoredOnlyAsHash path via rotation breakage) |
| logout does not revoke | TestLogoutRevokesRefreshToken |

All eight mutants were killed. No surviving mutant.

### Compatibility
- cp-ui (`src/App.tsx` lines 54-80) reads only `token` and `user` from login and register and stores `token` under `cp_token`. The Go responses are a strict superset (`refreshToken` added), with `user {id,email,name}` unchanged. The UI will not break. It ignores `refreshToken`, so until Task 15 sessions end after 15 minutes. The plan already says to ship Tasks 6 and 15 together.
- Node auth (`controllers/authController.ts`): same status codes and error strings (409 `email_already_exists`, 401 `invalid_credentials`). `/dev/login` keeps `{token, userId}` and adds `refreshToken`. Node-hashed bcrypt passwords verify (TestLoginWithPasswordHashedByNode). I checked that `bcrypt.CompareHashAndPassword` in the repo's x/crypto still accepts a 72-byte-plus input against a hash made from its first 72 bytes, so Node-era users with long passwords can still log in (Node truncated them too).

### Findings

[STAFF-1] severity: low
where: auth.go register (`len(body.Password) > maxPasswordB`)
problem: Node had no upper bound on password length, so signup with a password over 72 bytes now returns 400 `invalid_request`. The UI shows a generic error. This is the intended STAFF-2 trade-off, but the UI has no hint.
fix: Task 15 or the Product review: add a short client-side max-length message. No Go change.

[STAFF-2] severity: low
where: auth.go refresh and store/refresh.go ConsumeRefreshToken
problem: Two tabs refreshing with the same token at nearly the same time lead to one 200 and one "reuse", which revokes every session of that user. This is by design (spec: rotation plus reuse detection), but it makes the UI side matter.
fix: Task 15 should single-flight the refresh call (one in-flight promise shared across tabs/requests, e.g. via a lock). Note it in the Task 15 brief.

[STAFF-3] severity: low
where: auth.go validEmail
problem: `mail.ParseAddress` accepts dotless domains like `x@local`; zod `.email()` in Node rejected them. That is why dev@local needed a reservation. It also lets users register addresses like `admin@localhost`.
fix: optional: require a dot in the domain part. Not blocking.

[STAFF-4] severity: low
where: auth_test.go TestLoginUnknownEmailPaysForBcrypt
problem: A wall-clock threshold (15 ms against bcrypt cost 10, about 40-60 ms on this machine). It does kill the mutant, but could flake on an unusually fast CI runner or pass vacuously if the cost were lowered later.
fix: keep, or assert the dummy hash's cost equals `auth.HashPassword`'s cost instead of timing. Not blocking.

[STAFF-5] severity: low
where: api_test.go harness.token `//nolint:unused`
problem: marker survives until Task 7. Nothing tracks removal.
fix: add to the Task 7 checklist.

RESULT: APPROVE (0 critical, 0 high, 0 medium, 5 low)

## Verifier (commit d531344)

RESULT: PASS

Suite (control-plane-go/):
- `go build ./...` ok; `go vet ./...` ok
- `golangci-lint run --allow-parallel-runners` -> `0 issues.`
- `gosec -quiet -exclude=G204 ./...` -> no output, exit 0
- `gitleaks git --redact` (repo root) -> `331 commits scanned ... no leaks found`
- `go test -race -count=3 ./internal/api/ ./internal/auth/ ./internal/store/` -> ok api 136.7s, ok auth 27.4s, ok store 19.5s
- `go test -race -count=1 ./...` -> every package ok (api 72.4s, runtime 116.5s, ...); no failures, no reruns needed

Feature (git archive HEAD copy in /tmp/verify-task6, real TCP httptest server + testcontainers Postgres; tokens redacted; copy deleted):
- register -> 200 {"token","refreshToken","user":{id,email,name:null}}
- login -> 200 same shape
- refresh (valid) -> 200 {"refreshToken","token"}; new refresh token differs from old
- refresh with OLD token -> 401 {"error":"invalid_refresh_token"}
- refresh with the NEW token afterwards -> 401 {"error":"invalid_refresh_token"} (revoked)
- logout -> 200 {"ok":true}; refresh after logout -> 401 invalid_refresh_token
- wrong password and unknown email -> both 401 {"error":"invalid_credentials"}, identical status and body
- bad JSON -> 400 {"error":"invalid_request"}
- 2 MiB body -> 413 {"error":"request_too_large"} (limit is 1 MiB; a first 200 KB probe correctly gave 400)
- /dev/login with DevLogin=false -> 404 {"error":"not_found"}; with DevLogin=true -> 200
- default burst: logins 1-5 -> 401, login 6 and 7 -> 429 {"error":"too_many_requests"}

Failures: none.

## Product (restored by the orchestrator from the reviewer's report; the original section was overwritten by a concurrent append)
Result: CHANGES REQUESTED (no code issue in the commit; cutover and Task 15 risks). Read from code; the UI was not run.
- **PROD-1 (high):** cp-ui stores only `cp_token` and has no 401 handler or refresh. After 15 minutes users see raw `401: {"error":"unauthorized"}` banners. Tasks 6 and 15 must ship in the same cutover: hard gate.
- **PROD-2 (high):** two tabs share localStorage and refresh with the same token; the second request replays a consumed token and the server revokes ALL of the user's sessions (other devices too). Same for logout then a stale refresh, and for a retry after a lost response. Server: grace window and no revoke-all on logout-revoked tokens. UI: single-flight refresh, cross-tab lock, re-read localStorage before refreshing.
- **PROD-3 (medium):** register/login/refresh share one bucket (burst 5, refill 1 per 6 s) keyed on the TCP peer; behind nginx or a shared NAT everyone shares it. The UI must never clear tokens on a 429 from /auth/refresh. Decide the trusted-proxy policy before cutover; consider a separate looser refresh bucket.
- **PROD-4 (medium):** the 72-byte password rule returns the generic `400 invalid_request`; the UI only checks >= 8 characters and the limit is in bytes (emoji/CJK hit it early). Use a distinct error code and a byte-length check with a clear message in signup.
- **PROD-5 (low):** login 401 and register 409 map to today's messages; a malformed email on login is now 400, and 400/429/500 show raw `status: body`. Task 15 needs one central error mapper.
- **PROD-6 (low):** registering `dev@local` returns 409 (shown as "Email already registered"); acceptable.
- **PROD-7 (low):** the old UI's logout never calls /auth/logout; three inline `removeItem("cp_token")` sites (App.tsx:78, ProjectsPage:84, ProjectCanvasPage:111) should become one helper that also sends the refresh token. Socket/log hooks read `cp_token` at connect time and must re-read the refreshed token on reconnect.
- Task 15 UI items: `apiFetch` wrapper (attach token, single-flight refresh on 401 with a cross-tab lock, retry once, else clear both tokens and go to /login); store the refresh token as `cp_refresh` and send it on logout; do not clear tokens on 429/5xx from /auth/refresh; map 400/401/409/429/500 to readable messages; enforce 8-72 bytes on signup; re-read the access token when sockets/log hooks reconnect.
- Release-note lines: signed out once at the upgrade; sessions last 30 days with automatic renewal and logout ends the session on the server; passwords limited to 72 bytes (longer existing ones still work for login); sign-in attempts from one network limited to about 10 per minute.
- Cutover checklist: ship Go /auth/* and the Task 15 UI together; everyone logs in again once and old Node JWTs must bounce cleanly to /login via the new 401 handler; decide the client-IP/proxy-trust policy before exposing the API behind nginx.

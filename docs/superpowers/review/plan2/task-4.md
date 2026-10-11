
## Security

Reviewer: Security. Commit a49bfda (Plan 2 Task 4), `control-plane-go/internal/auth` plus go.mod/go.sum.

### Tool output
- govulncheck (180s limit): "Your code is affected by 0 vulnerabilities." One module-level advisory, GO-2026-5932 (`golang.org/x/crypto/openpgp` unmaintained, no fix). We import only `x/crypto/bcrypt` and the openpgp package is never called, so it is not reachable. Informational.
- gosec `./internal/auth/`: 0 issues. `gosec ./...` reports 2 G204 (internal/runtime/logstream.go:61, exec.go:26), both in code not touched by this commit (args passed as lists, not shell strings); also prints type-check noise ("export data version 5 > 4": gosec built with an older Go than go 1.27.2), so the whole-tree run is not fully reliable. Pre-existing.
- gitleaks `a49bfda^!`: 1 hit, rule `jwt`, `internal/auth/auth_test.go` (the `nodeToken` fixture). False positive: it is a deliberately committed test vector signed with the throwaway secret `test-secret`, carries no exp, and is asserted to be rejected. No real secret. Consider a `.gitleaksignore` entry or a `// gitleaks:allow` comment so later runs stay quiet.
- cp-ui/package.json unchanged, so no npm audit.

### Review of the checklist
- Signing-method pinning: `jwt.WithValidMethods(["HS256"])` runs before the keyfunc; alg=none (test covers it, needs UnsafeAllowNoneSignatureType only to forge) and RS/ES/PS and HS384/512 are rejected. The key is a `[]byte`, so an RSA-public-key/HMAC confusion attack is not possible. Good.
- exp: `WithExpirationRequired()`; test proves Node tokens (no exp) are rejected. nbf is validated by the library when present (we never issue it). iat is written but not validated (no `WithIssuedAt`), harmless. Leeway is 0; fine for a single-issuer service on one clock. TTL 15 min. `sub` required and non-empty.
- Constant-time compare: the HMAC signature check uses `hmac.Equal` inside jwt. Refresh tokens are looked up by SHA-256 hash in the DB, so no direct secret comparison exists in this code.
- Refresh token: 32 bytes from crypto/rand (256 bits), base64url, stored as SHA-256 hex. A fast hash is correct for high-entropy tokens. Rotation/reuse detection belongs to the later store/handler tasks.
- bcrypt: cost 10 (matches Node), `$2b$` hashes verify (test with a real Node hash). `CheckPassword` returns only a bool, so no error text leaks.
- Dependencies: golang-jwt/jwt/v5 v5.3.1 (current, maintained, no advisories found), x/crypto 0.58.0 (direct now), x/sync/sys/text patch bumps. No findings.

### Findings

[SEC-1] severity: low
where: control-plane-go/internal/auth/jwt.go:20
problem: `NewSigner` accepts any secret, including empty or short. The only length floor (32 bytes in production, placeholder blocklist) lives in config; a future caller or test constructing a Signer directly gets no protection.
evidence: `func NewSigner(secret string) *Signer { return &Signer{secret: []byte(secret), ...} }`
fix: Optional defence in depth: return an error (or panic at startup) when `len(secret) == 0`; keep the 32-byte policy in config. Confirm in Task 5/6 wiring that `cfg.JWTSecret` is the only source.

[SEC-2] severity: low
where: control-plane-go/internal/auth/jwt.go:41
problem: `Verify` returns the raw library error (e.g. "token signature is invalid", "token has invalid claims: token is expired"). Safe in itself, but if a handler echoes `err.Error()` to the client it discloses validation detail.
fix: Handlers must map every Verify failure to a fixed 401 body (log the detail server-side). Check this in the middleware task. Could also wrap in a sentinel `ErrInvalidToken`.

[SEC-3] severity: low (note for the login task)
where: control-plane-go/internal/auth/password.go:14
problem: (a) For unknown emails the login handler must still run a bcrypt compare against a dummy hash, otherwise response time reveals which emails exist. (b) bcrypt only uses the first 72 bytes; x/crypto v0.58 `GenerateFromPassword` errors on longer input, while Node silently truncated, so a Node-era user with a password over 72 bytes would fail to log in (fail closed, not a vulnerability). Registration should cap length at 72 bytes with a clear 400.
fix: Dummy-hash compare in login; enforce a max password length of 72 bytes at registration.

[SEC-4] severity: info
where: control-plane-go/internal/auth/jwt.go
problem: No `iss`/`aud` claims. Acceptable with one issuer and one secret; matters only if the secret is ever shared with another service.

RESULT: CLEAN for blocking issues (0 critical, 0 high, 0 medium; 3 low, 1 info, all advisory/forward-looking)

## Staff (commit a49bfda)

Checks run from `control-plane-go/`: `go build ./...` ok, `go vet ./...` ok, `golangci-lint run` 0 issues, `go test -race ./internal/auth/` ok, `go mod tidy -diff` empty, `govulncheck ./...` reports only vulnerabilities in required modules that the code does not call. The code matches the plan text and spec §5 (HS256, 15 min `exp`, `iat`, SHA-256 hashed 30-day opaque refresh token, Node-compatible bcrypt cost 10). No deviations.

Mutation checks (throwaway copy under the scratchpad, repo untouched):
- Drop `WithExpirationRequired`: FAIL (caught by the Node-token test).
- Bcrypt cost 10 -> 4: FAIL (caught).
- Drop `WithValidMethods`: tests still PASS. Not caught.
- Drop the empty-`sub` check: tests still PASS. Not caught.

[STAFF-1] severity: medium
where: control-plane-go/internal/auth/auth_test.go:60 (TestVerifyRejectsBadTokens)
problem: Algorithm pinning is untested. The only alg test uses `none`, which golang-jwt rejects on its own without `WithValidMethods` (the key is not `UnsafeAllowNoneSignatureType`). With the pin removed, an HS512 token signed with the correct secret is accepted.
evidence: Mutation deleting the `jwt.WithValidMethods(...)` line: `ok infrapilot/internal/auth`. A probe test with an HS512 token and the right secret returned `err=<nil>`.
fix: Add a case that signs with `jwt.SigningMethodHS512` (and optionally HS384) using the correct secret and a valid `exp`, and expect an error. Add a case with a valid signature and `exp` but no `sub` (or `sub: ""`) for the sub check.

[STAFF-2] severity: low
where: control-plane-go/internal/auth/password.go:10
problem: `bcrypt.GenerateFromPassword` returns `ErrPasswordTooLong` for passwords over 72 bytes. Node's `bcrypt` silently truncated, so the Node API accepted them. `CheckPassword` is fine (a Node hash of a long password still verifies, confirmed by probe), but the Task 6 register handler must either return 400 for >72 bytes or the UI must cap length; otherwise register returns 500. Also `HashPassword` returns `string(b)` alongside a possibly nil-derived empty string on error, which is harmless.
fix: In Task 6 validate length (`max 72 bytes`) before hashing and map the error to 400. Add a test there.

[STAFF-3] severity: low
where: control-plane-go/internal/auth/jwt.go:15
problem: API-shape notes for Tasks 5 and 6.
- `NewSigner("")` is accepted, and an empty HMAC key still signs and verifies. Config loading (Task 5/6) must reject an empty or short `JWT_SECRET`; the dev default `dev-secret-change-me` in CLAUDE.md should be refused when `APP_ENV` is production.
- `Sign("")` is accepted but `Verify` would reject it; harmless but asymmetric. Consider returning an error.
- `now` is unexported, so tests in other packages (middleware, refresh handler) cannot control the clock. Either add an option such as `NewSignerWithClock`, or those tests must use real time and a short-lived workaround. Decide before Task 5.
- `Verify` returns the raw jwt error. The middleware should map every error to 401 and must not echo the text to clients; the refresh handler may want to distinguish expiry (`errors.Is(err, jwt.ErrTokenExpired)`), which forces `internal/api` to import jwt. A small exported `ErrExpired` sentinel would avoid that.
- `Verify` has no leeway. Fine for one API host; note it if a second instance ever appears.

[STAFF-4] severity: low
where: control-plane-go/internal/auth/refresh.go
problem: Refresh token generation, hashing, and TTL are tested, but nothing ties `RefreshTTL` to the store (`expiresAt`); that lands in Task 6, so the 30-day value has no test yet. Not a defect in this commit.
fix: Cover rotation, reuse of a revoked token, and expiry in the Task 6 store/handler tests.

Dependency bumps (go.mod): adds `golang-jwt/jwt/v5 v5.3.1` and promotes `golang.org/x/crypto` to direct at v0.58.0 (was indirect v0.57.0); `x/sync` 0.23 -> 0.24, `x/sys` 0.48 -> 0.49, `x/text` 0.42 -> 0.43. `go get` pulled the x/ bumps as minimum-version side effects. All are patch-level moves of Go's own x/ modules, `go mod tidy -diff` is clean, build/vet/lint/tests pass, govulncheck shows nothing reachable. Acceptable. Note `x/net` stays at 0.60.0, so the set is consistent. No indirect dependency leaked into a different major.

Test quality summary: expiry-required, Node-token rejection, expired token, wrong secret, bcrypt Node hash and cost are well covered and fail under mutation. The Node token test is meaningful because signature is checked first, so the "exp" error proves the secret matched. The one real gap is alg pinning (STAFF-1). `ParseUnverified` and `GetExpirationTime` errors are ignored in the test (acceptable in a test, but lint-clean only because errcheck is off for tests). The tolerance `AccessTTL-time.Minute` is loose but not flaky.

RESULT: CHANGES REQUESTED (0 critical, 0 high, 1 medium, 3 low)

## Verifier (commit a49bfda)

RESULT: PASS (real Node token check not run, see below)

- `go build ./...` -> OK; `go vet ./...` -> OK
- `golangci-lint run --allow-parallel-runners` (run alone) -> `0 issues.`
- `go test -race -count=1 ./...` (full, log in scratchpad) -> 17 packages `ok`, no FAIL, no skips, `EXIT=0`; last lines: `ok infrapilot/internal/workflows 21.945s`, `? infrapilot/migrations [no test files]`. Docker/testcontainers-backed packages (worker/build, worker/outbox, workflows) pass after the x/crypto, x/sync, x/sys, x/text bump.
- `go test -race -count=3 ./internal/auth/` -> `ok infrapilot/internal/auth 13.862s`
- Real Node-issued token (~/cp-logs/.dev-token): NOT verified. The file exists, but my throwaway check (reading the token and the Node JWT_SECRET) was blocked by the permission classifier, so I did not retry. Rejection of exp-less tokens rests on the unit test and on `jwt.WithExpirationRequired()` in `Verify`. Needs the user's go-ahead to run.

## Fix round 1 (orchestrator check)
Commits a4c427e (STAFF-1 tests: HS384/HS512 and missing/empty `sub`, mutation-proven in a throwaway copy), c3b50e8 (SEC-1/STAFF-3: `NewSigner` returns an error on an empty secret; signature is now `(*Signer, error)`, a deviation the Task 5/6 wiring must handle), d952096 (`.gitleaksignore` entry for the a49bfda `nodeToken` fixture). Engineer ran build, vet, golangci-lint (0 issues), gosec (clean), `-count=3` on `internal/auth`, and the full `go test -race -count=1 ./...` (all ok). The orchestrator re-ran `gitleaks git --redact`: 317 commits scanned, no leaks. Result: task clean. Open lows are in backlog.md (Tasks 5 and 6). The live check against a real Node-issued token was not run (needs the user's go-ahead).

# Plan 2 Task 14, Round 2: Verifier

Commit under test: 88c4f64 (fixes ef38f46..88c4f64), branch go-migration.

RESULT: PASS

## Suite
- `go build ./...` -> OK
- `go vet ./...` -> OK
- `golangci-lint run` -> `0 issues.`
- `go test -race -p 2 ./...` -> `EXIT 0`, 19 packages `ok` (including cmd/api, cmd/migrate, cmd/worker, internal/workflows), none failed.

## Feature (real run of the cmd/api binary)
Setup: throwaway postgres:16-alpine on 127.0.0.1:55439 with migrations 00001-00004 applied (`migrate up`), dev Redis 6380 and ClickHouse 8123, API on 8083, metrics on 127.0.0.1:19093, JWT secret and ENV_ENCRYPTION_KEY generated at run time.

(a) APP_ENV=development
- `lsof`: `api ... TCP 127.0.0.1:8083 (LISTEN)` and `127.0.0.1:19093 (LISTEN)`. Nothing on a wildcard address.
- `curl http://192.168.0.104:8083/healthz` (LAN IP) -> `lan=000` (refused). Loopback -> 200 `{"ok":true}`.
- `POST /dev/login` on loopback -> 200, returns token, refreshToken and userId.
- Startup log has the WARN `dev login is enabled: POST /dev/login needs no credentials, binding loopback only` with addr 127.0.0.1:8083. The `api listening` line shows `"addr":"127.0.0.1:8083","devLogin":true`, `envKeyFingerprint` as a 12-char fingerprint, and a redacted config (`databaseURL ... vt:xxxxx@...`, `jwtSecretSet:true`, `envEncryptionKeySet:true`).
- Secret check: `grep -c -F` of the generated JWT secret and of the encryption key in the log -> 0 and 0. The same check on the production-run log -> 0.

(b) APP_ENV=production, 48-byte secret
- `api listening` addr `[::]:8083`, `devLogin:false`; `lsof`: `TCP *:8083 (LISTEN)`.
- LAN IP /healthz -> 200, loopback -> 200 (all interfaces).
- `POST /dev/login` -> 404 `{"error":"not_found"}` on both the LAN IP and loopback.
- SIGTERM -> exit=0.
- Extra: production with `JWT_SECRET=shortsecret` -> `api exited ... JWT_SECRET must be at least 32 bytes outside development, got 11`, exit=1.

(c) SSE and graceful drain (dev instance)
- Created a project, a redis service and a deployment, then took a stream ticket and opened `GET /logs/deployments/{id}/stream?ticket=...`. It returned `event: history`.
- Held a slow request: `POST /projects` with Content-Length 20 and 6 s of silence before the body was completed. Sent SIGTERM at 20:40:31.57 while both were open.
- The log shows `api shutting down` at 20:40:31.576.
- The SSE client received `event: end` with `data: {"reason":"shutdown"}`.
- `/metrics` returned 200 at t+1, t+2, t+3 and t+4 s after SIGTERM, while the slow request was draining. The listener was still bound, and new API connections were refused (healthz 000).
- The slow request completed with `HTTP/1.1 200 OK` (log: `POST /projects status 200 ms 6014`). The process was gone by t+5, with `/metrics` -> 000.
- The log has no error line. That dev process was started in a different shell invocation, so its exit code was not captured. The production SIGTERM run above and the two TRUSTED_PROXIES runs below each gave exit=0.

(d) Port already in use (listener holding :8083 with no SO_REUSEADDR)
- production: `api exited ... listen tcp :8083: bind: address already in use`, exit=1, `api listening` count 0.
- development (127.0.0.1:8083 held): same error on `127.0.0.1:8083`, exit=1, `api listening` count 0.

(e) TRUSTED_PROXIES=127.0.0.1/32, `POST /auth/login` with bad credentials (limiter burst 5)
- Same XFF 10.1.1.1, 8 requests: `401 401 401 401 401 429 429 429`.
- 8 distinct XFFs: `401 x8`, so separate buckets per XFF.
- Control with TRUSTED_PROXIES unset: the distinct-XFF requests all gave `429 x8`, because XFF is ignored and they share the peer-address bucket. This is the expected behaviour.

## Regression
- Full suite passes (above). No tracked files were changed by this run.

## Findings (non-blocking)
- [VER-1, informational] In development mode, a bind failure is logged after the `dev login is enabled ... binding loopback only` WARN, because the WARN is written before `net.Listen`. The `api listening` line is correctly absent. The WARN could be moved to after the bind (cosmetic).
- Test-method note: on macOS, a listener on 0.0.0.0:8083 with SO_REUSEADDR does not conflict with the API's dual-stack `[::]:8083` bind. My first (d) attempt used it and was invalid. The API was not at fault, and the attempt was redone as above.

## Cleanup
- Removed container `vt14r2-pg` and /tmp/verify-t14r2. Killed the stray api processes from the first (d) attempt. Nothing is listening on 8083 or 19093.
- Remaining containers are the pre-existing cp_* and t23-mongo-b ones. `df`: 24Gi free (31% used).
- Untouched: the real dev DB, Node API 8080, the Go worker and encrypt-env. No prune was run. No code changes and no push.

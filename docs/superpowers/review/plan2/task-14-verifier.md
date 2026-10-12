# Plan 2 Task 14 - Verifier report

RESULT: PASS (one note: the worker fingerprint line has no direct unit test, see check 9)

Commits: 6dca7f5, 760040a, 98497fe on go-migration. Tree: only the known untracked files plus the .DS_Store change. df: 25 GiB free.

| # | Check | Result | Evidence |
|---|-------|--------|----------|
| 1 | go build ./... | PASS | no output, exit 0 |
| 2 | go vet ./... | PASS | no output, exit 0 |
| 3 | golangci-lint run | PASS | `0 issues.` |
| 4 | go test -race -p 2 ./... | PASS | 19 packages `ok`, no FAIL/panic, log ended `EXIT 0` |
| 5 | Step 3 smoke on API_PORT=8082 (metrics 127.0.0.1:9092; throwaway postgres:16-alpine `verif-t14-pg` on 55600, migrations 00001-00004 applied via `go run ./cmd/migrate up`; dev Redis 6380, ClickHouse 8123; JWT_SECRET and ENV_ENCRYPTION_KEY generated at runtime) | PASS | healthz `{"ok":true}`; dev/login keys `["refreshToken","token","userId"]`; GET /projects with token -> `0`; /auth/refresh keys `["refreshToken","token"]`; /projects without token `{"error":"unauthorized"}`; `grep -c cp_http_requests_total` on the metrics port -> 7 |
| 6 | /metrics not on API port | PASS | `GET :8082/metrics` -> 404 `{"error":"not_found"}`; served only on 127.0.0.1:9092 |
| 7a | TRUSTED_PROXIES empty: XFF ignored | PASS | 8 POST /auth/login (bad creds, burst 5) with distinct X-Forwarded-For 203.0.113.1..8 -> `401 401 401 401 401 429 429 429` (one shared bucket) |
| 7b | TRUSTED_PROXIES=127.0.0.1/32 (startup log `trustedProxies=[127.0.0.1/32]`), peer 127.0.0.1 | PASS | 8 distinct XFF -> `401 x8` (separate buckets); same XFF x8 -> `401 x5 then 429 x3`; forged varying leftmost + fixed rightmost `10.0.0.$i, 198.51.100.9` x8 -> `401 x5, 429 x3` (forgery does not help); `10.1.1.$i, 198.51.100.20, 127.0.0.1` (right-most trusted skipped) -> `401 x5, 429 x3`; no XFF -> keyed by peer `401 x5, 429 x3` |
| 7c | Untrusted peer ignores XFF even when a list is set | PASS (incidental) | First trusted-run used `localhost`, which curl resolved to ::1 (not in 127.0.0.1/32): 8 distinct XFF -> `401 x5, 429 x3`, so XFF was ignored for the untrusted peer |
| 8 | SIGTERM with an open SSE stream | PASS | Stream opened 20:28:11 (history event, `: ping` heartbeats); still open at 20:28:49 (38 s, past the 30 s WriteTimeout). SIGTERM at 20:28:53.717; process gone 20:28:53.84 (~0.12 s). Client received `event: end` / `data: {"reason":"shutdown"}`, curl exit 0. Log: `api shutting down`, then the stream request logged status=200 ms=42159 |
| 9 | Worker fingerprint line | PASS with note | cmd/worker/main.go:77 `logger.Info("encryption key loaded", "envKeyFingerprint", secrets.Fingerprint(cfg.EnvEncryptionKey))`. The worker was NOT run. There is no test of that line itself (cmd/worker has no test touching it); coverage is `internal/secrets` TestFingerprint plus cmd/api TestStartupLogHasNoSecrets (which asserts `envKeyFingerprint` in the API log). Not a blocker; a worker-side assertion would be a nice-to-have |
| 10 | Startup log has no secrets | PASS | Startup line shows `envKeyFingerprint=d0fd2a529945`, `jwtSecretSet=true`, `envEncryptionKeySet=true`, DB URL as `postgresql://v:xxxxx@...?sslmode=xxxxx`. grep of both run logs for the exact JWT_SECRET and ENV_ENCRYPTION_KEY values -> 0 matches each; DB password not present. cmd/api TestStartupLogHasNoSecrets passes |

Notes (not failures):
- Observation: tokens do not survive a restart with a regenerated JWT_SECRET (my first stream-ticket try returned 401 after I restarted for the TRUSTED_PROXIES run). Expected, not a bug.
- The rate-limit probes used the register/login class (burst 5 / 6 s refill), via bad-credential logins.

Cleanup: API process gone, `verif-t14-pg` removed (0 verif containers left), secret scratch files deleted. No images or volumes pruned. Dev DB, Node API (8080), Go worker and encrypt-env untouched. No code modified, nothing pushed.

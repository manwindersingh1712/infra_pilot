# Plan 1, Task 11: Log entry and the Redis hot tier

## Implementation
- **Engineer stalls.** The first Engineer wrote `internal/logs/{entry.go,redis.go,redis_test.go}` and added go-redis v9.23.0 plus the testcontainers redis module v0.44.0 to go.mod. It was killed by the 10-minute watchdog during its CI run. A resumed run stalled again, and so did a fresh commit-only Engineer, which stalled before running any command even with `caffeinate` keeping the Mac awake.
- **The orchestrator committed the code** (user ruling, `decisions.md` 2026-10-09). The files were byte-identical to the plan (lines 3216-3453). Indirect go.mod bumps: x/sys 0.47 to 0.48, mdelapenya/tlscert, go.uber.org/atomic.
- **Failing run:** with `entry.go` and `redis.go` moved aside, the build failed with `no non-test Go files ... FAIL infrapilot/internal/logs [build failed]`. This is the external-test-package form of the plan's `undefined: logs.FormatTime`.
- **CI checks by the orchestrator:** gofmt and vet clean, golangci-lint 0 issues, `go test -race -count=1 ./...` all ok (logs 3.1 s), `-short` ok with TestRedis skipped, govulncheck 0 affecting vulnerabilities, gosec clean.
- **Commit** `3eb7941` feat(go): redis hot log tier with pub/sub.
- **Mac sleep diagnosis.** One race run took about 16 minutes of wall-clock time but reported `db 24.5 s`. Go's monotonic clock does not advance while macOS sleeps, so the Mac was sleeping during the run. The orchestrator now keeps `caffeinate -i` running during sessions. That did not stop the agent stalls, which happen inside the model stream.

## Reviews (commit 3eb7941)
- **Verifier: PASS.** The tests compared Go against what Node does, using a throwaway Redis:
  - Node-style `XADD` is readable by Go `Read`.
  - Go `Write` produces the same fields and order, PTTL ≈ 600000, and pub/sub JSON matching Node's `JSON.stringify(entry)`.
  - Unicode and newlines round-trip, and `stop()` closes the channel.
  - `ActiveStreams` lists every stream.
  - `FormatTime` matches `toISOString`.
- **Staff: APPROVE.** The code is plan-exact and Node-compatible (keys, fields, MAXLEN ~10000, TTL, JSON order). SCAN replaces Node's KEYS. Lows STAFF-1..5 (T11): STAFF-1, which duplicates SEC-2, was fixed below; the rest went to the backlog.
- **Security: FINDINGS (2 medium, 2 low).** govulncheck, gosec and gitleaks are clean, and the new dependencies are maintained.
  - **SEC-1 (medium):** `redis.ParseURL` returns a `*url.Error` that contains the raw URL with its password, and `NewRedis` wrapped it with `%w`.
  - **SEC-2 (medium):** the `Subscribe` goroutine ignored ctx cancellation while idle and never closed the pub/sub connection, so each dropped viewer leaked a connection (a DoS risk).
  - SEC-3 and SEC-4 went to the backlog.

## Fix round 1
- `c008b1c` fix(go): keep REDIS_URL password out of parse errors (SEC-1 T11).
  - Any `*url.Error` now becomes the fixed message `parse REDIS_URL: invalid URL`.
  - **Deviation:** the plan suggested keeping `ue.Err`, but a bad %-escape echoes part of the password, so it is dropped as well.
  - The test runs under `-short`.
- `877810a` fix(go): Subscribe closes on ctx cancel and releases the connection (SEC-2 T11, STAFF-1 T11).
  - The goroutine now selects on ctx, a `done` channel and the message channel, and always calls `stop()`. `stop` is wrapped in `sync.Once`.
  - **Deviation:** a `done` channel was added so that calling `stop` while the goroutine is blocked on a full `out` still closes it.
  - Each new test was shown failing before its fix.
- **Orchestrator full suite at 877810a:** `-race` all ok (db 24.7 s, logs 2.9 s), golangci-lint 0 issues, `-short` all ok.
- **Security re-check: CLEAN.** It probed the remaining go-redis parse errors and none echo the userinfo. Double stop, stop racing a cancel, and stop on a full `out` are all safe.
- **Verifier re-run: PASS.**
  - The password does not appear in the error.
  - 50 quiet subscriptions all closed after the ctx was cancelled (50/50). Redis `CLIENT LIST` went from 2 before to 52 during and back to 2 after.
  - Double stop is fine, live delivery still works, and Node-style XADD is still readable.

## Result
**Done 2026-10-09.** Verifier PASS, and nothing critical, high or medium is open. Next: Task 12.

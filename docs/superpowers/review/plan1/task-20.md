# Plan 1, Task 20: Resume log streams after a worker restart

## Implementation
- Engineer commit `832a496` feat(go): resume log streams for running containers on worker start (`internal/runtime/{resume.go,resume_test.go}`). Plan-verbatim; compatible with the Task 19 fix-round streamer (only uses public `Start`). Failed first (`undefined: Running`); CI dry run green. Not wired yet (Task 26 main: `ListRunningDeployments` → `Resume(ctx, deps, docker.IsRunning)`).

## Round 0 reviews (832a496)
- **Security: CLEAN (2 low).** Container IDs validated in both `IsRunning` and `Start`; deployment IDs are server-generated. SEC-1 T20: no cap on resumed streams (bounded by services in practice). SEC-2 T20: inspects run serially with no per-call timeout; a hung Docker daemon blocks startup; loop ignores cancellation.
- **Staff: CHANGES REQUESTED (3 low).** STAFF-1: count includes no-op Starts (Start should return bool). STAFF-2: `ctx.Err()` not checked in the loop (misleading Warns after SIGINT). STAFF-3 forward (Task 26): O(N) serial inspects before the outbox starts. Per-item recommendation on T19 backlog: fix now STAFF-4(a)+VER-4 (no false "ended (exit -1)" on shutdown; "Log stream stopped" on Stop), STAFF-5 (warn once per stream, ideally stop writing after first timeout, which also covers SEC-6), VER-3 doc comment on Start, STAFF-6/7/8 cleanups; defer STAFF-4(b) `--tail=100` duplicates to Plan 3 with a comment; STAFF-4(c) `Wait()` to Task 26.
- **Verifier: PASS.** Real Docker + dev Redis: running container streamed (13 entries), stopped and missing containers skipped (missing = quiet `(false, nil)`), Resume returned 1. Simulated restart: new Streamer resumed, `--tail=100` re-sent all 13 earlier lines (VER-1 T20 = STAFF-4 T19), plus a false "Log stream ended (exit code: -1)" (VER-2 T20).

## Orchestrator decision
All findings are low, so the gate is not blocked, but restarts write a user-visible false "ended" line. One cleanup fix round takes: STAFF-1, STAFF-2 + SEC-2 (ctx check, per-inspect timeout), STAFF-4(a)+VER-4/VER-2, STAFF-5 + SEC-6 (warn once, stop writing after the first sink timeout), VER-3 doc comment, STAFF-4(b) comment, STAFF-6/7/8. Deferred: STAFF-4(c) `Wait()` + STAFF-3 → Task 26; SEC-1 stream cap → Plan 3; `--since` dedupe → Plan 3.

## Fix round 1 (cleanup)
- `f95df5a` STAFF-1: Resume counts only newly started streams. `Start` keeps no return value (Task 24's `LogStarter` interface is `Start(id, ctr)`); logic moved into unexported `start(...) bool`.
- `86fac20` STAFF-2 + SEC-2: ctx check in the loop; per-inspect `inspectTimeout` (var, 5 s). Only effective if `isRunning` honours ctx (`Docker.IsRunning` goes through the runner with ctx; Task 24/26 must keep passing it).
- `83d9fe0` STAFF-4(a)/VER-4 (T19), VER-2 (T20): no system entry on root-ctx shutdown (Info log); "Log stream stopped" on `Stop`; natural exit/start errors unchanged. The T19 Stop test now expects "Log stream stopped".
- `53d3170` STAFF-5 + SEC-6 (T19): per-stream `lineWriter` warns once and drops later lines after a sink failure (pipe still drained); lines use the stream ctx; final system entry tried once with WithoutCancel + timeout (≤ 2 sink timeouts per stream).
- `c2496b7` VER-3 + STAFF-4(b): doc comments. `871ed00` STAFF-8. `35fb60e` STAFF-7 (release hook, mutation-checked). `75b97de` STAFF-6 (WaitDelay removed, comment).
- Engineer CI dry run green; Resume/Streamer tests 5/5 under -race.

## Round 1 re-check (f95df5a..75b97de)
- **Staff + Security (combined): CHANGES REQUESTED (1 medium, 3 low).** All original items resolved; no leaks, no races, gosec/gitleaks clean; public `Start(id, ctr)` signature kept. The final-entry-after-failure choice is accepted.
  - STAFF-4 T20 (medium, test flake): `TestStreamerStopsWritingAfterSinkFailure` failed 1 in 5 under -race (`line writes=2`): stdout and stderr pumps can both pass `failed.Load()` before either stores. Fix: hold a mutex across check+write in `lineWriter.write` (or loosen to `<= 2`).
  - STAFF-5 T20 (low): `end()` resets `failed`, so the final entry is a third attempt; correct the comment/record (bound = 1 concurrent line timeout + 1 final-entry timeout).
  - STAFF-6 T20 (low): `Stop()` racing a start failure writes "Log stream error: context canceled" instead of "stopped"; use "stopped" if the stream ctx is cancelled.
  - STAFF-7 T20 (low, cosmetic): unterminated exactly-16 KiB final line + lone `\r` at EOF is flagged as cut.
- **Verifier: PASS.** Suite + `-count=10` green. Real Docker + dev Redis: shutdown cancel leaves 0 system entries (no more false "exit code: -1"); resume streams again (7 duplicated lines = expected tail, Plan 3); `Stop` → "Log stream stopped", goroutines at baseline; `docker stop` → "ended (exit code: 0)"; failing sink → exactly 1 Warn, container unblocked, 0 leftover `docker logs`; 2 MiB line → one 16399-byte entry (16384 + 15-byte marker; "≤16 KiB" means the payload), later lines kept arriving. VER-3 T20 info only.

## Fix round 2 plan
STAFF-4 (mutex, real fix, not a looser assertion), STAFF-5 (comment), STAFF-6, STAFF-7 (trivial).

## Fix round 2
- `3eabd0f` STAFF-4: `lineWriter` mutex across failed-check + sink write + failure mark (flake reproduced 6/30 before, `-count=100` clean after; assertion not loosened; combined bound = one sinkTimeout for line writes). `914ffca` STAFF-5 comments. `d6c06c2` STAFF-6 ("stopped" when Stop cancels a failing start). `e9d6578` STAFF-7 (lone CR at EOF).
- **Orchestrator check (no reviewer re-dispatch, diff is ~50 lines):** read the mutex diff (`end()` runs after the pumps finish so `resetFailed` can't deadlock; lock is held only during the bounded write); `-race -count=30` of the three touched tests and `-count=5` of Resume|Streamer green; gofmt/vet clean; govulncheck exit 0.

## Result
**PASSED 2026-10-10** after two fix rounds. Open (backlog): STAFF-3 T20 + STAFF-4(c) T19 (Task 26: Resume placement, `Streamer.Wait()`), SEC-1 T20 (stream cap, Plan 3), VER-1 T20 (`--tail=100` duplicates, Plan 3), VER-3 T20 info.

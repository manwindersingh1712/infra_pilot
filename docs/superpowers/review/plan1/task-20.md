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

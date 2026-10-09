# Plan 1, Task 16: Command runner and the Docker runtime

## Implementation
- Engineer commit `67aba8e` (previous session): `internal/runtime/{exec.go,exec_test.go,docker.go,docker_test.go}`, plan-verbatim (gofmt alignment only).

## Round 0 reviews (67aba8e)
- **Security: FINDINGS (0/0/1/4).** SEC-1 medium: unbounded stdout/stderr buffers (tenant build output could OOM the worker). SEC-2..5 low: env-file validation, limits/network on helper containers, WaitDelay, positional-argument validation.
- **Staff: CHANGES REQUESTED (0/1/2/3).** STAFF-1 high: managed volume mounted at `/data`, but mongo:7 declares `/data/db` and `/data/configdb`, so Mongo data lived in an anonymous volume and never survived a redeploy (Node had the same bug). STAFF-2 medium: no WaitDelay, cancel error lost ctx.Err(). STAFF-3 medium: untested branches. STAFF-4..6 low.
- **Verifier: FAIL.** VER-1: `IsRunning` matched "No such object" but Docker 29.8.2 prints lowercase, so a missing container returned an error. Everything else passed against real Docker (limits, labels, aliases, 127.0.0.1-only ports, env values not in argv, network isolation, idempotency).

## Triage
- CTO ruling (`../decisions.md`, "2026-10-09: Managed volume mount path and the Node-era Mongo migration"): `RunSpec.VolumeTarget` + `Tmpfs`; Mongo uses `/data/db` with `/data/configdb` on tmpfs; drop `legacyMongoPassword`/`hasExistingData` (every managed DB gets a generated password); Redis-only legacy copy; Node-era Mongo data moves by `mongodump`/`mongorestore` in the Task 27 runbook. Spec §4/§5/§6/§10/§12 and Plan 1 Tasks 23/27 amended (`d285d31`, `bb0c3e9`). Security items folded into the same round.

## Fix round 1 (bb0c3e9..32ea9c6)
- `d0a9a8b` VER-1, `298ffbb` STAFF-2/SEC-4, `0184a16` SEC-1, `c62ed78` STAFF-1, `50e04c2` STAFF-5/SEC-2/STAFF-6, `e2074e1` SEC-3, `bef2e76` SEC-5, `5631d78` STAFF-3, `32ea9c6` STAFF-4 (real-Docker Mongo volume test).
- Deviations: `VolumeTarget` required whenever `Volume` is set (no silent `/data` default); `Remove` also matches not-found case-insensitively; validation extended to aliases, probe host and IsRunning id.
- Orchestrator CI dry run on 32ea9c6: all green, including `TestDockerIntegrationMongoVolumeLayout`.

## Fix round 1 re-checks
- **Security: CLEAN.** All five closed (50 MiB flood → 1 MiB, 6 MB total allocation). Tooling gap: gosec v2.29.0 can't read go1.27 export data even when rebuilt, so type-aware rules are blind → backlog (medium, tooling).
- **Staff: APPROVE.** All six closed; Task 23 amendment matches the RunSpec field names; generated names/images pass the new checks. Lows STAFF-7..9 (T16r1) → backlog (STAFF-7: Node-era env keys like `my-key` or PEM values now fail validation → make it non-retryable in Task 23).
- **Verifier: PASS.** Real Docker 29.8.2: VER-1 fixed; Mongo data persists across container replacement with only the named volume mounted and 0 new anonymous volumes; Redis on `/data` works; helper containers carry limits and the copy helper has no network; bad input rejected with 0 runner calls; 50 MiB stdout capped at 1 MiB in 33 ms; 200 ms cancel returns in 202 ms with DeadlineExceeded; env files cleaned up.

## Result
**Done 2026-10-10** after fix round 1. No open critical/high/medium. Next: Task 17.

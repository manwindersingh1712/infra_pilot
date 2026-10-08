# Plan 1, Task 4: Test helpers (Postgres container, fakes)

## Implementation
- Engineer commit `8cee8ac` test(go): postgres testcontainer helper and fakes (`internal/testutil/{postgres.go,fakes.go,postgres_test.go}`, go.mod, go.sum). Code plan-verbatim.
- Deps: testcontainers-go v0.44.0 (+ modules/postgres), images `postgres:16`, `testcontainers/ryuk:0.14.0`.
- Deviations: upgraded `golang.org/x/crypto` v0.55.0→v0.56.0 (GO-2026-6355/6354 reachable via testcontainers Terminate → ssh) and `moby/go-archive` v0.2.0→v0.3.0 (GO-2026-6253) so govulncheck exits 0; stub file to show the expected failing-test reason; tidy made testcontainers direct.
- Timings: full suite ~4-6 s with cached images; `-short` ~1 s with the integration test skipped.

## Reviews (commit 8cee8ac)
- **Security: FINDINGS (0/0/0/2).** Production binaries (`go list -deps ./cmd/...`) contain no testcontainers/moby/testutil; testutil never reads DATABASE_URL/.env (cannot reach the dev DB). Lows SEC-1 (T4) floating `postgres:16` tag, SEC-2 (T4) no CI guard against prod importing testutil → backlog. GO-2026-5932 (x/crypto/openpgp, no fix, not imported) accepted, re-check at each plan gate.
- **Staff: APPROVE.** Code = plan; API matches every later call site in Plans 1-2 (RunWithPostgres, NeedDB, FakeRunner vs Task 16 Runner). Lows STAFF-1..5 (T4) → backlog. Proposed a "db hardening" fix round after Task 5 or 6 → CTO.
- **Verifier: PASS.** CI job green; caller package shows per-test isolation by TRUNCATE (no t.Parallel); containers cleaned up; no-Docker run fails loudly without -short and skips with -short; nothing macOS-specific for CI. Observation: killing a test mid-start can leave a stopped (Created) container that Ryuk doesn't reap.

## Result
**Done 2026-10-08.** Verifier PASS, no open critical/high/medium. Next: Task 5. CTO asked to rule on the db-hardening round.

# Plan 1, Task 7: Projects, services, env vars and the `Seed` fixture

## Implementation
- Engineer commit `1dc7b04` feat(go): project, service and env var queries (`internal/store/{projects.go,services.go,envvars.go,deployments.go (stub for Task 8),projects_test.go}`, `store.go` nolint removed, `internal/testutil/fixtures.go`). Production code byte-identical to the plan.
- Added `TestScannedTimestampsAreUTCAndUserJSONHasNoPassword` (closes backlog STAFF-1 (T6)). Env var values plain text by design (encryption is Task 10 / callers).

## Reviews (commit 1dc7b04)
- **Security: CLEAN.** All SQL bound; owner-scoped lookups exist; unscoped helpers (`ListServices("")`, `GetService`, `UpdateServicePosition`) not used in production yet; worker path can't cross tenants. Plan 2 watch items → backlog.
- **Staff: APPROVE.** Node parity for ordering, env upsert/delete semantics, canvas keys, position updates; Task 8 replaces the stub cleanly; Seed fits all later call sites. Lows STAFF-1..3 (T7) → backlog.
- **Verifier: PASS.** CI job green, no nolint left. Caller test exercised owner scoping, all six service types, positions, canvas, env vars, JSON. Read-only dev DB comparison: projects, services (all/per project), 4 env lists and one canvas byte-identical to Node; the other canvas has the same objects in a different order (OBS-1: Node has no orderBy) → backlog note for Plan 2. Dev DB unchanged.

## Result
**Done 2026-10-08.** Verifier PASS, no open critical/high/medium. Next: Task 8.

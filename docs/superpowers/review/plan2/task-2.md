
## Security

Reviewer: Security. Commit 415ea93 (Plan 2 Task 2). Scope: control-plane-go store layer and migration 00003.

Tool output:
- govulncheck: exit 0. "Your code is affected by 0 vulnerabilities." One vulnerability sits in a required module that the code does not call (not reachable).
- gosec: 2 issues, both G204 (subprocess with variable) in internal/runtime/logstream.go:61 and internal/runtime/exec.go:26. Neither file is in this commit. They are the runner abstraction; args are passed as an argument list, never a shell string, so they are accepted false positives. gosec also printed type-import errors (export data version mismatch between the gosec build and the local Go toolchain), so its coverage of packages that import Temporal and embed is incomplete. This is a tooling problem, not a code problem.
- gitleaks (415ea93^!): no leaks found.
- cp-ui unchanged, so no npm audit.

Review notes:
- SQL: every query in the diff uses bound parameters ($n). The only string concatenation joins compile-time constants (aliveIn, alive, ownedByCaller, serviceCols). No injection.
- Owner scoping and soft delete: GetService, ListServices, ListServicesForOwner, GetServiceForOwner, UpdateServicePosition(InProject), ListCanvasServices, GetConnectionForOwner, ListProjectConnections, ListSourceEdges, GetDeploymentForOwner and ListDeploymentsForOwner all filter deletedAt and, for the *ForOwner ones, the project owner. Foreign IDs yield ErrNotFound (404). ListDeploymentsForOwner caps at 50.
- Refresh tokens: only a hash is stored (caller must pass SHA-256 of a crypto/rand token; the handler is not in this commit, so re-check it in the auth task). Rotation is an atomic UPDATE ... WHERE revokedAt IS NULL AND expiresAt > now() RETURNING, so two concurrent consumers cannot both win. Reuse of a consumed token revokes all of the user's live tokens. Expiry is checked in SQL against server time. Unique index on tokenHash; FK cascades on user delete.
- Migration 00003 is additive: nullable column with no default, new table, new index. No existing column, type or constraint changes. Prisma/Node selects named columns, so it ignores deletedAt and RefreshToken; the running Go worker is unaffected (its queries do not reference the new objects). Down migration is destructive but only reachable by an explicit goose down.

[SEC-1] severity: low
where: control-plane-go/internal/store/deployments.go:254 (GetJob)
problem: GetJob joins Service without "deletedAt" IS NULL. After SoftDeleteService, a queued deployment or a retried workflow can still build and start a container for a deleted service before TeardownWorkflow finishes. Likely deliberate (teardown needs the row), but then the deploy path needs its own guard.
evidence: `FROM "Deployment" d JOIN "Service" s ON s."id" = d."serviceId" WHERE d."id" = $1`; activities.go:126 and :170 call it.
fix: In the deploy activity, fail non-retryably if the service is soft-deleted (add a Deleted flag to Job rather than filtering, so teardown can still use it). Have SoftDeleteService cancel in-flight deployment workflows in the same handler.

[SEC-2] severity: low
where: control-plane-go/internal/store/connections.go:37 and :47
problem: Connection reads and ownership checks only look at one end. ListProjectConnections matches source OR target in the project, and GetConnectionForOwner checks only the source owner. If the create handler does not verify the caller owns both source and target, a user can link to a foreign service. They can then see edges that point at a foreign service ID, and the target owner's canvas lists an edge from someone else's service.
evidence: `WHERE "id" = $1 AND "sourceId" IN (... p."ownerUserId" = $2 ...)`; no target check.
fix: The create-connection handler (a later task) must check GetServiceForOwner for both ends, with a test. Optionally require both ends owned in GetConnectionForOwner.

[SEC-3] severity: low
where: control-plane-go/internal/store/refresh.go:35-52
problem: (a) Any consumed or revoked token that is replayed revokes all of the user's sessions, including a logged-out token replayed after expiry, and a legitimate client double-submit (two tabs refreshing at once). That is availability noise, not a vulnerability, but the handler should treat ErrTokenReused as 401 and log it (without the token). (b) The reuse path is two statements outside a transaction; harmless. (c) Expired rows are never purged.
fix: Add a periodic DELETE of RefreshToken rows past expiresAt plus a retention window. Consider treating a replay within a few seconds of rotation as a plain 401 instead of a mass revoke.

[SEC-4] severity: low (operational)
where: control-plane-go/migrations/00003_api.sql:20
problem: CREATE INDEX on "Deployment" is not CONCURRENTLY, so it blocks Deployment writes (the Node API and Go worker keep running) for the build time. goose wraps the migration in a transaction, and ADD COLUMN also takes a brief ACCESS EXCLUSIVE lock. The table is small, so the impact is negligible now.
fix: Acceptable as is. For a large table, use a separate `-- +goose NO TRANSACTION` migration with CREATE INDEX CONCURRENTLY.

Note (no action): Node-written services are visible to Go and vice versa. The Node API has no soft-delete awareness, so a service the Go API soft-deletes still shows in any remaining Node endpoint. Cutover is done, so this only matters if the Node API is run again.

RESULT: FINDINGS (0 critical, 0 high, 0 medium, 4 low)

## Staff

Commit 415ea93 (Plan 2 Task 2), reviewed against spec §7 (security/refresh tokens) and §4 (TeardownWorkflow). `go build ./...`, `go vet ./...`, `golangci-lint run` (0 issues) and `go test -short ./internal/store/` are clean. No code was edited.

Caller check: the new `deletedAt` filter changes `GetService`, `ListServices`, `UpdateServicePosition*`, `ListCanvasServices` and the connection queries. Grep over `internal/` and `cmd/` finds no non-test caller of `GetService`, `ListServices` or `GetServiceForOwner`, so the worker, workflows and outbox are unaffected. They reach services only through `GetJob` (joins `Service`, no filter) and `ClaimDeploy` (row lock on `Service`, no filter). Both are deliberately or accidentally unfiltered; see STAFF-1 and STAFF-2.

[STAFF-1] severity: medium
where: control-plane-go/internal/store/services.go:36-38 (and deployments.go:259, :95)
problem: Once `GetService` hides soft-deleted rows, nothing can read a deleted service. TeardownWorkflow (spec §4) runs after the API has soft-deleted the service and needs its type, name and project. `GetJob` joins `Service` unfiltered, so it works only through a deployment id; there is no `GetServiceIncludingDeleted`.
evidence: the only unfiltered paths are `GetJob` and `ClaimDeploy`'s `FOR NO KEY UPDATE`.
fix: In the teardown task, add an explicit store method (e.g. `GetServiceAnyState`) rather than loosening `GetService`. Record this in the Task for teardown.

[STAFF-2] severity: medium
where: control-plane-go/internal/store/deployments.go:25-33, :84-100, :256-260
problem: Deploys of a soft-deleted service are not blocked. `CreateDeployment` and `ClaimDeploy` have no `deletedAt` check, so a deploy queued or started just before the delete races the teardown. It can recreate a container and nginx route after teardown ran, and the service is invisible so the user cannot see or remove it.
evidence: the handler's `GetServiceForOwner` check and the insert are not in one transaction; the outbox row can sit queued for a while.
fix: Make `ClaimDeploy` return `ClaimLost` (or a new terminal result) when the service is deleted, and have `CreateDeployment` fail on a deleted service (`INSERT ... SELECT ... WHERE deletedAt IS NULL`). Add a test for each. Alternatively have TeardownWorkflow terminate or wait for in-flight `deploy-<id>` workflows; decide which in the teardown task.

[STAFF-3] severity: low
where: control-plane-go/internal/store/envvars.go:83-86
problem: `SetEnvVarValue` is an unconditional update by id. The encrypt-env backfill reads plain values and then writes the ciphertext, so a user edit in between is overwritten with the stale (encrypted) old value. The file already has `ReplaceEnvVarIfEquals` for exactly this.
fix: Use compare-and-swap (`WHERE id = $1 AND value = $3`) and return a bool, or reuse `ReplaceEnvVarIfEquals`. Note also that `NOT LIKE 'enc:v1:%'` treats a user's plain value that starts with `enc:v1:` as already encrypted; unlikely, accept or note.

[STAFF-4] severity: low
where: control-plane-go/internal/store/refresh.go:36-55
problem: Presenting a token that was revoked by logout (`RevokeRefreshToken`) takes the reuse branch and revokes every session of the user. A client that retries after logout, or two tabs racing the same refresh (the second sees an already-consumed token), logs the user out everywhere. Spec §7 says "rotate on every use and revoke on logout", and does not say logout replay is theft. Reuse revocation is also two statements, not atomic with the first check.
fix: Distinguish consumed-by-rotation from revoked-by-logout (e.g. a `replacedAt` column or only treat tokens with a successor as reuse), or document the behavior and add a short grace window for concurrent refresh. Minimum: a test for logout-then-replay stating the intended outcome. There is also no purge of expired or revoked rows; add to backlog.

[STAFF-5] severity: low
where: control-plane-go/internal/store/owner_test.go:30-48
problem: Errors from `CreateService`, `CreateConnection` and the list calls are discarded (`_`), and several branches are not proven: `UpdateServicePositionInProject` and `ListServices` with a deleted row; the inbound-edge/OR branch of `ListProjectConnections` with a deleted source (only a deleted target is seeded); `GetConnectionForOwner` and `ownedByCaller` with a deleted service (no test that a deleted service's deployments disappear from `GetDeploymentForOwner`/`ListDeploymentsForOwner`); an expired or revoked-by-logout token does not assert that other sessions survive.
fix: Check errors; add the missing assertions. Same low-severity pattern as Task 1.

[STAFF-6] severity: low
where: control-plane-go/migrations/00003_api.sql:2, ../control-plane/prisma/schema.prisma
problem: The frozen Node Prisma schema has neither `deletedAt` nor `RefreshToken`. Prisma ignores unknown columns, so Node keeps running, but any Node reader would list soft-deleted services. Acceptable only because cutover is done (tracker) and Node is a frozen reference; confirm the Node API is not run against this DB. No partial index on `Service(deletedAt)`; fine at current scale.
fix: None in code; note in the release note that Node must not serve this DB after migration 00003.

Compatibility: migration is additive (nullable column, new table, new index), Down is the exact inverse, outbox event types and JSON shapes are untouched. Spec §7 is met for hashed storage, rotation, and cascade on user delete; the `exp`/logout endpoints are for later tasks.

RESULT: APPROVE (0 critical, 0 high, 2 medium, 4 low) — the two mediums should be recorded as requirements for the teardown/deploy-handler tasks; neither breaks existing callers today.

## Verifier

Commit 415ea93, branch go-migration, run 2026-10-11.

RESULT: FAIL

- `go build ./...` -> BUILD_OK; `go vet ./...` -> VET_OK
- `golangci-lint run --allow-parallel-runners` -> `0 issues.` (exit 0)
- `go test -race ./...` -> exit 1. All packages ok except `infrapilot/internal/db`:
  - [VER-1] `TestMigrateRefusesUnbaselinedPrisma`: `migrate_integration_test.go:511: goose rows = [0 1 2 3], want [0 1 2]`
  - [VER-1] `TestConcurrentBaselineAndMigrate/1..5`: `migrate_integration_test.go:549: goose rows = [0 1 2 3], want [0 1 2]`
  - Cause: migration 00003_api.sql was added but the two hardcoded expectations (lines ~510 and ~548) were not updated to `[0 1 2 3]`.
- `go run ./cmd/migrate status` -> 00001, 00002, 00003 all applied (00003 at 2026-10-11T14:42:43Z). Clean.
- Go worker (pid 36178, up ~13h): log has no error/warn lines; "flushed logs processed=0 errors=0" continues at 20:12 after the migration.
- Node API (pid 36180, port 8080): GET /healthz, /projects, /deployments with dev token -> 200 each; node-api.log has no 4xx/5xx or error entries.

## Staff (round 2)

Fix round 1, commits 15f2293 (VER-1) and f6dab36 (STAFF-2/SEC-1). `go build ./...`, `go vet ./...`, `golangci-lint run` (0 issues), `go test -short ./internal/store/ ./internal/workflows/` and the new store tests (non-short, real DB) all pass. No code was edited.

Verified:
- ClaimDeploy: the `deletedAt` read and the `FOR NO KEY UPDATE` lock are one statement, so the flag is read under the lock. `SoftDeleteService` is an UPDATE, which takes a conflicting row lock, so a claim either sees the delete or blocks the delete until it commits. Race-free for the claim.
- ClaimLost in the workflow: ClaimDeploy runs before EnsureNetwork/credentials/volume/RunContainer, so no resource exists when it loses. `deploy()` returns `(st, nil)` with an empty stage, `DeployWorkflow` returns nil without cleanup (covered by `TestDeployLostDeployClaimEndsWithoutCleanup`). A deleted service whose workflow already built an image leaves the pushed image in the registry; that belongs to teardown.
- GetJob: LoadJob and BuildImage map ErrNotFound to a non-retryable `NotFound` error. A deleted service therefore fails the workflow at LoadJob (queued row) or, if deleted mid-build, goes through the default branch (MarkFailed, RemoveContainer). No half-created state.
- Tests: `TestCreateDeploymentRejectsSoftDeletedService` and `TestClaimDeployAndGetJobIgnoreSoftDeletedService` assert specific results and the unchanged row status. VER-1 now derives expected rows from the embedded migrations.

[STAFF-7] severity: low
where: control-plane-go/internal/store/deployments.go:29-32 (CreateDeployment)
problem: `INSERT ... SELECT ... FROM "Service" WHERE "deletedAt" IS NULL` does not lock the service row. The FK check takes only FOR KEY SHARE, which a non-key UPDATE of `deletedAt` does not conflict with. A delete that commits between the SELECT snapshot and the insert still leaves a queued deployment for a deleted service. ClaimDeploy and GetJob neutralise it, so it is harmless today, but the doc comment ("inserts nothing") is slightly too strong.
fix: Add `FOR SHARE OF s` to the SELECT (conflicts with the delete's row lock; READ COMMITTED re-checks the predicate), or soften the comment. Optional.

[STAFF-8] severity: low
where: control-plane-go/internal/store/deployments.go:94-102 (ClaimDeploy), deploy.go:187
problem: A row already `deploying` whose service was then deleted now returns ClaimLost instead of ClaimWon (the old "own retry wins" rule). Fine before RunContainer (nothing started), but no test covers a deploying row of a deleted service in `TestClaimDeployActivity`/workflow tests; the store test does cover it (`running` row). Also a deletion after ClaimDeploy is not noticed by later steps (RunContainer, SwitchRoute take the job from workflow input), so a delete that lands mid-deploy still starts a container and route.
fix: Teardown must cancel or terminate `deploy-<id>` workflows for the service before removing resources (already recorded for the teardown task). Add that as an explicit requirement there.

[STAFF-9] severity: low
where: open question, deletions of services with queued/building/deploying rows
problem: Rows of a deleted service stay `queued` (LoadJob not found, workflow fails) or `deploying` (ClaimLost after the claim won) forever. They are invisible to users (owner queries filter deleted services) and nothing in `internal/` or `cmd/` rescans queued rows, so nothing re-dispatches them. Acceptable for now.
fix: Accept. Have TeardownWorkflow (or SoftDeleteService's handler) mark the service's non-terminal deployments `failed` via a store method so counts and the Plan 3 reconciler do not see stale rows. No test currently covers LoadJob/BuildImage for a deleted service at the activity level; add one when teardown lands.

RESULT: APPROVE (0 critical, 0 high, 0 medium, 3 low). STAFF-2 and SEC-1 are fixed; the STAFF-2 medium is closed.

## Verifier (round 2)

RESULT: PASS (HEAD f6dab36, on 15f2293 and 415ea93)
- `go build ./...` exit 0; `go vet ./...` exit 0; `golangci-lint run --allow-parallel-runners` -> `0 issues.`
- `go test -race -count=1 ./...` exit 0; every package ok, last lines: `ok infrapilot/internal/workflows 23.324s`, `? infrapilot/migrations [no test files]`; internal/db ok 28.709s.
- `go test -race -count=1 -v -run 'TestMigrateRefusesUnbaselinedPrisma|TestConcurrentBaselineAndMigrate' ./internal/db`: `--- PASS: TestMigrateRefusesUnbaselinedPrisma (0.23s)`, `--- PASS: TestConcurrentBaselineAndMigrate (0.76s)`. VER-1 resolved.
- ~/cp-logs/go-worker.log: only periodic `flushed logs processed=0 errors=0` INFO lines (last 20:17:10); no errors or panics.
- git status: only known untracked files. No code edited.

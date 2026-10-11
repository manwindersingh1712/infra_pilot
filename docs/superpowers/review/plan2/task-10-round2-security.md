# Plan 2 Task 10, fix round 1: Security re-review

Range: 757c55e..5f51cbd (control-plane-go/). RESULT: CLEAN (no new findings; SEC-1 and SEC-2 closed).

## Tools
- govulncheck (hard-limited, full path): 0 vulnerabilities affecting the code (1 in a required module, not called). Exit 0.
- gosec -quiet ./...: 2 issues, both G204 (exec with variable args) in internal/worker (docker logs) and internal/runtime/exec.go; neither file is in this diff, argument-list execution, pre-existing and accepted.
- gitleaks (757c55e..HEAD): no leaks.
- go test -p 2 -count=1 ./internal/api ./internal/store: both ok. go vet clean.

## (1) Cross-tenant leak closed
- ListProjectConnections and ListSourceEdges: `"sourceId" IN aliveIn AND "targetId" IN aliveIn`, where aliveIn = live services with projectId = $1 (bound parameter). The alive/OR clauses are gone. Both ends must be live services of the project, so a foreign id, a foreign label, a soft-deleted end and a cross-project end all drop out. NULLs: IN over a non-null id subquery cannot match NULL, so a NULL end is excluded too.
- Other places that return connections or service ids: grep of all non-test code shows only getCanvas (ListCanvasServices + ListProjectConnections, both project-scoped), autoLayout (ids from ListCanvasServices, edges from ListSourceEdges, layout only places known ids; writes via UpdateServicePositionInProject scoped to the project), createConnection (returns only the row just inserted by CreateConnectionForOwner, same-project same-owner single statement; all failures are one 404 "Service not found"), deleteConnection (GetConnectionForOwner requires both ends owned and live; 404 otherwise; returns only success). No other caller exists. Error messages do not name ids.
- New tests (store: TestLegacyConnectionRowsNeverCrossProjectsOrTenants; api: TestCanvasNeverShowsLegacyCrossTenantConnections) cover both directions, other tenant, same user other project, soft-deleted end, both users' canvases, response-body substring checks, and auto-layout.

## (2) Changed assertions: none weakened
- TestConnectionListsRespectProjectAndDirection: counts moved from 3/3/2/1 to 1/1/0/0 and the edge check to exactly [F, db]. Each is stricter (fewer rows allowed, exact id checked, the previously unused `elsewhere` now asserted for P2). Matches the ruling.
- TestDeleteConnectionRequiresOwnershipOfBothEnds: the old check went through ListProjectConnections, which now hides the mixed row and would have been vacuous. The raw `SELECT count(*) ... WHERE id = ANY(own, mixed)` expects exactly 2, so it still proves that a rejected delete removed nothing, and is better than the list (it sees the cross-tenant row). Parameterised; no weakening.
- Removed TestCanvasMasksRepoURLCredentials is replaced by TestCanvasHasNoRepoURL (asserts no "repoUrl", token or host in the body). Masking remains covered by TestRepoURLCredentialsMasked on services endpoints (still present).

## (3) repoUrl gone from canvas
CanvasService has no RepoURL field, the SQL no longer selects "repoUrl", publicCanvasService is deleted and getCanvas no longer calls it. Remaining uses of repoUrl in the API are services.go create/list (still through publicService/RedactURL, untouched) and the worker path.

## (4) Bulk cap and atomicity
- `len(Services) > 500` -> 400 invalid_request, checked right after decode and before any validation loop, ownership check or write. Exactly 500 passes (test). Duplicates count toward the cap and are applied in order (last wins) inside the one transaction; harmless. Body size is already bounded by decodeJSON.
- UPDATE is `WHERE id=$1 AND projectId=$2 AND deletedAt IS NULL`, project from the ownership-checked path id (foreign ids silently ignored, as before).
- Atomicity test: the trigger/function are created only inside the test via the test pool, WHEN positionX = 777, with t.Cleanup dropping both. Not in migrations, not in non-test code. The package's tests run sequentially and no other test uses 777, so no cross-test interference. Test asserts 500 and that all three rows kept 0/0, so it does prove rollback.

## (5) layout.go
`git diff --stat 757c55e HEAD -- internal/api/layout.go` is empty. Only layout_test.go changed (added exact-position cases).

## Informational (no action needed)
- I1 (info): The trigger cleanup in the atomicity test is best-effort; if the test process is killed the trigger would remain in the throwaway test DB. It fires only on positionX = 777, so impact is nil.
- I2 (info, carried): CreateConnection (unchecked store method) is still used by tests to fabricate legacy rows; ensure no handler calls it (grep confirms none does).

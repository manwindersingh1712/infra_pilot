# Plan 2 Task 9 - Staff review (commit d1772bc)

Build, vet, golangci-lint: clean (0 issues). `go test -p 2 ./internal/api/ -run TestListDeployments`: 4/4 pass.

## Deviations judged
- idShape guard on ?serviceId= (malformed -> 200 `[]`): justified. A NUL or invalid UTF-8 would be a Postgres 500; the same guard is already used for projectId in services.go. Answers like a missing or foreign id (no existence oracle). Guard runs before the store, so the owner check is not skipped on any path. Accept.
- 3 extra tests: justified, each kills a distinct mutant (below).

## Shape / compat
- Go returns 13 keys: Node's 12 Prisma fields (id, serviceId, commitSha, image, status, containerId, hostPort, containerPort, runtimeUrl, volumePath, createdAt, updatedAt) plus the new `stoppedAt` (nullable, additive; migration 00002). cp-ui `Deployment` interface (ServiceDetailsPanel.tsx:7) reads only existing fields and ignores stoppedAt: compatible.
- Timestamps are ISO-8601 with ms and Z, same as Prisma's JSON. Empty result is `[]` (collect uses `[]T{}`), nulls for nullable columns, same as Node.
- Behavioral change vs Node (intended): Node listed every user's deployments unscoped; Go scopes to live services in owned projects. Empty/absent serviceId = all, as in Node. limit 50, createdAt DESC, same as Node.

## Mutation results (throwaway git-archive copy outside repo)
Killed: owner check dropped (other users leak, test "leaked" fails); soft-delete filter removed; empty list as null (3 tests); idShape guard removed (NUL -> 500, test fails); guard returns null; guard condition made inert; route unprotected (401 test + others); userID replaced by "".
SURVIVED:
- serviceId filter ignored (`($1='' OR TRUE)`)
- LIMIT 50 -> 100
- LIMIT 50 -> 1
- ORDER BY createdAt DESC -> ASC

## Findings

[STAFF-1] severity: medium
where: control-plane-go/internal/api/deployments_test.go:15 (and store/owner_test.go:61)
problem: the serviceId filter is not proven for a same-owner case. The caller has one service in the API test, so `?serviceId=` returns the same set as no filter; the foreign case is covered by owner scoping alone. Ignoring the filter survives. The UI calls this endpoint with ?serviceId= per service panel, so a regression would show every service's deployments in each panel.
evidence: mutation `($1 = '' OR "serviceId" = $1)` -> `($1 = '' OR TRUE)`: all 4 tests pass.
fix: seed a second service under the same user/project (or a second deployment for another owned service) and assert ?serviceId=A returns only A's deployments while no filter returns both.

[STAFF-2] severity: low
where: control-plane-go/internal/api/deployments_test.go
problem: no test for ordering (newest first) or the 50 cap; both are part of the contract the UI relies on (`deployments[0]` is taken as latest, though the UI re-sorts client side).
evidence: ASC order, LIMIT 1 and LIMIT 100 all survive.
fix: create 2+ deployments and assert order; for the cap, insert 51 (cheap via store) and assert len 50 and that the oldest is absent. Could live in a store test.

[STAFF-3] severity: low
where: control-plane-go/internal/store/deployments.go:290
problem: ORDER BY "createdAt" DESC has no tiebreaker; deployments created within the same timestamp tick come back in unspecified order. Same as Node, so not a regression.
fix: optional `, "id" DESC`.

RESULT: CHANGES REQUESTED (0 critical, 0 high, 1 medium, 2 low)

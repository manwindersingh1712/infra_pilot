# Plan 2 Task 10 (canvas): verifier report

Commits: 67cba5d, 7eb042e, 43b3fd7, 4779ed5, 757c55e (branch go-migration). No repo code edited.

RESULT: PASS with one finding (VER-1, legacy cross-user connection rows leak through the canvas read).

## Suite
- `go build ./...` OK; `go vet ./...` OK
- `golangci-lint run --allow-parallel-runners` -> `0 issues.`
- `gosec -quiet -exclude=G204 ./...` -> no output (no findings)
- `gitleaks git --redact` (repo root) -> `365 commits scanned ... no leaks found`
- `go test -race -count=3 -p 2 ./internal/api/ ./internal/store/` -> `ok infrapilot/internal/api 236.612s`, `ok infrapilot/internal/store 13.107s`
- `go test -race -count=1 -p 2 ./...` -> every package `ok` (cmd/migrate, cmd/worker, api 95s, auth, config, db, gitsrc, logging, logs, metrics, runtime, secrets, store, testutil, tickets, worker/build, worker/outbox, workflows); no re-run needed.

## Feature (real run)
`git archive HEAD` copy in /tmp/verify-task10 (outside repo), real Postgres via testcontainers, real TCP httptest server, users via store, tokens redacted. Throwaway test `internal/api/zz_e2e_test.go` in the copy only.

| Check | Result |
|---|---|
| GET canvas | 200, camelCase, positions 0/0, `"connections":[]` (not null), repoUrl `https://****@github.com/x/y.git` (raw was `user:secretpw@`), `repoUrl:null` for managed |
| chain a->b (label uses), b->c | 200, row incl. createdAt |
| duplicate | 409 `Connection already exists` |
| self | 400 `Cannot connect service to itself`; self on a foreign service -> 404 |
| cross-project target / cross-project source | both 404 `Service not found` (identical) |
| foreign-user target / source | both 404 `Service not found` (identical to cross-project) |
| soft-deleted target / source | both 404 |
| label with NUL | 400 |
| delete: foreign / own / repeat | 404 / 200 / 404 |
| single position ok; foreign | 200; 404 |
| position 1e999, "NaN", 1000001, missing y | all 400 `invalid_request` |
| bulk with one bad entry (1e999) and with a missing y | 400; canvas afterwards unchanged (a stays 0,0) |
| bulk ok including a foreign service id | 200; foreign service position unchanged (0,0) |
| bulk / layout / read on A's project as B, or foreign project as A | 404 `Project not found` |
| auto-layout chain | a x=0, b x=380, c x=760 (y=0); isolated d at 0,230 |
| ids `%00`, `%ff`, 65 chars (project, connection, service pos, body ids) | all 404 |
| no token / garbage token | 401 `unauthorized` |

## Legacy cross-project / cross-user connection (VER-1)
Inserted by SQL: `legacy1` A.a -> B.bsvc label `SECRET-LABEL-FROM-A`, `legacy2` B.bsvc -> A.d label `b-label`.
- A's canvas `connections` includes both legacy rows verbatim: ids, `sourceId`/`targetId` including B's service id `cmv4d1ag8000b1v3nugdh3br6`, and B's label. B's service name/type/repoUrl are NOT returned in `services`.
- B's canvas `connections` includes both rows too: it receives A's service ids (`...nyj3nhapa`, `...nbzamg31k`) and A's label `SECRET-LABEL-FROM-A`. None of A's service details are in B's `services`.
- Both canvases therefore carry edges whose other end is not in `services` (dangling edge ids; React Flow may warn or drop them).
- DELETE `legacy1` as A -> 404, as B -> 404 (neither can delete it, because both-ends ownership is required): a stuck edge visible on both canvases that nobody can remove via the API.
- Auto-layout is unaffected (ListSourceEdges requires both ends live in the project): A still gets 0/380/760, B gets its single node.
- Cause: `ListProjectConnections` uses OR (source or target in the project). Node does the same (`OR: [source.projectId, target.projectId]`), so this is parity, but it is a cross-tenant leak of foreign ids and labels. Fix suggestion: require both ends in the project (or in the owner's projects) in `ListProjectConnections`; if the OR is kept for same-owner other-project edges, filter dangling ones. Only reachable for pre-existing Node-era rows, since the Go create path forbids cross-project/cross-user edges.

## Performance (300 services, 600 edges, in-process Postgres container, local TCP)
- canvas read: 3.7 / 3.1 / 3.3 ms (105,901 bytes)
- auto-layout (writes 300 positions in one tx): 69.7 / 70.5 / 71.3 ms; longest chain gave maxX 113620 (well under the 1e6 coordinate bound).

## Node comparison (control-plane/apps/api/src/routes/canvas.ts)
- Canvas read services fields: Node id,name,serviceType,positionX,positionY,deploymentCount; Go adds masked `repoUrl` (additive) and filters soft-deleted services (Node does not). Connections: id,sourceId,targetId,label in both (same OR semantics).
- Create: Node checks owner per end but not same project and not soft-deleted; Go requires one project, live, owner, single-statement. Node orders 404 before self-400; Go does the same (ownership first, so no oracle). Response: full row in both.
- Delete: Node checks source ownership only; Go both ends live and owned (stricter).
- Bulk update: Node no validation and partial writes per element; Go validates all first (400, nothing saved), ignores foreign ids.
- Position: Node unvalidated; Go validates finite and |v| <= 1e6.
- Auto-layout: Node dagre (LR, nodesep 150, ranksep 200, node 180x80); Go longest-path ranks. X matches for a chain (0/380/760); Y within a rank stacks top-down at 230 instead of dagre's centered ordering, so layouts may differ visually for branches. Node layout also repositions soft-deleted services.

## Dev environment (read-only)
- `go run ./cmd/migrate status` -> `00004 00004_refresh_family.sql pending` (not applied, as expected)
- ~/cp-logs/go-worker.log: last lines `level=INFO msg="flushed logs" processed=0 errors=0`; 0 error-level lines
- `curl localhost:8080/healthz` -> `{"ok":true}`

## Cleanup
/tmp/verify-task10 removed after the report; testcontainers terminated by the test.

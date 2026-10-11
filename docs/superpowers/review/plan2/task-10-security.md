# Plan 2 Task 10 security review (commits 67cba5d..757c55e)

Tools: govulncheck: 0 reachable vulnerabilities (1 in a required module, not called). gosec -quiet -exclude=G204: no findings. gitleaks (67cba5d^..757c55e): no leaks. `go test -p 2 ./internal/api/... ./internal/store/...`: ok.

## Findings

[SEC-1] severity: medium
where: control-plane-go/internal/store/connections.go (ListProjectConnections, ListSourceEdges), internal/api/canvas.go getCanvas
problem: ListProjectConnections keeps Node's OR semantics: a row is returned when EITHER end is a live service of the project and both ends are live anywhere (`alive`). Node had no ownership checks, so a legacy row can link a service of project A (tenant 1) to a service of project B (tenant 2). GET /projects/A/canvas then returns `{id, sourceId, targetId, label}` with tenant 2's service id and the (tenant-chosen) label, confirming that foreign service exists and is live; tenant 2's canvas leaks tenant 1's id the same way. Names/repoUrl of the foreign service are NOT returned (services come from ListCanvasServices, project-scoped), so the leak is ids + label + existence only. Secondary effects: the caller sees an edge it can never delete (GetConnectionForOwner requires both ends owned, so DELETE answers 404), and the UI would draw an edge to a node that is not on the canvas. New rows cannot be created like this (CreateConnectionForOwner), so this is legacy-data only.
ListSourceEdges (auto-layout) has the same shape (source in project, target anywhere live) but layout() drops edges to unknown ids and the ids are not returned, so no leak there; tighten it anyway for consistency.
fix: require both ends in the project.
```sql
-- ListProjectConnections
SELECT <connCols> FROM "ServiceConnection"
WHERE "sourceId" IN `+aliveIn+` AND "targetId" IN `+aliveIn+`
ORDER BY "createdAt" ASC
-- ListSourceEdges
SELECT "sourceId","targetId" FROM "ServiceConnection"
WHERE "sourceId" IN `+aliveIn+` AND "targetId" IN `+aliveIn+`
ORDER BY "createdAt" ASC
```
(the `alive` constant then becomes unused; remove it.) Update the doc comment ("both ends in the project"). Cross-project edges of ONE owner also disappear from the canvas; that is correct since the canvas only shows one project's nodes. Test: in store, insert with the unchecked `CreateConnection` a row between user A's service and user B's service (and one between two projects of the same user); assert ListProjectConnections for A's project and for B's project return none, ListSourceEdges returns none, and keep the existing same-project and soft-deleted-end cases. In api, GET A's canvas after seeding that row: `connections == []` and the body does not contain B's service id or the label.

[SEC-2] severity: low
where: control-plane-go/internal/api/canvas.go:92-113 (updateCanvas), :153-170 (autoLayout)
problem: the bulk PUT has no cap on the number of entries. The only bound is the 1 MiB body, which allows roughly 25k minimal entries `{"id":"a","positionX":0,"positionY":0}`; each valid-shape id runs one UPDATE round trip inside ONE transaction, so one authenticated request holds a transaction (and row locks on the project's services) for thousands of sequential statements; duplicate ids are not de-duplicated and non-matching ids still cost a round trip. Not atomicity-breaking (all-or-nothing is correct) but cheap amplification per request. Same for autoLayout: one UPDATE per service in a project (project size is unbounded; no service quota exists).
fix: reject `len(body.Services) > 500` (or the project's service cap) with 400 `invalid_request`; ideally one statement: `UPDATE "Service" s SET "positionX"=v.x,"positionY"=v.y FROM unnest($2::text[],$3::float8[],$4::float8[]) AS v(id,x,y) WHERE s."id"=v.id AND s."projectId"=$1 AND s."deletedAt" IS NULL` (same project scoping, one round trip, also used by autoLayout). Test: 501 entries -> 400; duplicate ids ok.

[SEC-3] severity: low
where: control-plane-go/internal/api/layout.go:25-46
problem: layout is O(n * E) worst case (n passes over all edges). Cycles terminate (rank capped below n, plus the pass loop bound), no recursion, no goroutines, so no stack or hang risk. But E can reach n^2 (unique source/target pair only) and there is no per-project service or connection quota, so a user with 2k services and ~1M connections costs about 2e9 map lookups in one request. Requires creating that many rows first (each by its own authenticated request), so it is a consequence of missing quotas, not of the algorithm.
fix: acceptable now. Carry-forward: per-project caps on services and connections (or cap edges fed to layout, e.g. 10k) with the Plan 2 quota work; rate limit authenticated mutating routes.

[SEC-4] severity: low (informational)
where: control-plane-go/internal/store/connections.go:30-47 (CreateConnectionForOwner)
problem: TOCTOU analysis. The INSERT...SELECT runs at READ COMMITTED with no row locks. A soft delete (UPDATE deletedAt) committing after the statement's snapshot but before its commit can leave an edge to a just-deleted service; ListProjectConnections and ListSourceEdges filter deleted ends, and Task 12 teardown should delete the service's connections (verify). A hard delete mid-statement is mapped to ErrNotFound by the FK check. There is no project-move or ownership-transfer endpoint, so "project move" cannot race. Same class as the Task 2 STAFF-7 backlog item.
fix: optional `FOR SHARE OF a, b` on the SELECT; otherwise ensure Task 12 teardown removes connections of the deleted service.

## Checked and fine
- Identical 404 for every wrong end: malformed ids, missing, soft-deleted, foreign, different project all give 404 `Service not found` with the same body; self-connection checks ownership first and a foreign self id gives the same 404 (only an owned id gets the distinguishable 400, which reveals nothing). Timing differs only by one DB round trip (malformed ids skip it); ids are unguessable cuids, accepted.
- Both-ends ownership, same project, a<>b in one statement; unique violation -> 409 only after both ends passed ownership (the pair-exists signal is therefore not an oracle for foreign ids).
- GetConnectionForOwner requires both ends live and owned by the caller; delete handler calls it first, then deletes by id (benign race, idempotent). Matches the Task 1/2 backlog owed items.
- Label: valid UTF-8, <=100 runes, no control chars (NUL excluded); `$4::text` bound.
- Positions: NaN/Inf rejected (JSON cannot carry them; `1e999` fails Go's decoder with 400), |v|<=1e6, missing coordinates rejected via pointers, struct has only id/positionX/positionY (no mass assignment), writes scoped by `projectId` + `deletedAt IS NULL`, single and bulk; single PUT scopes to the project returned by the ownership read. Bulk is one transaction (all or nothing). Foreign/unknown ids are silently ignored (no existence oracle).
- Ids: path ids through pathID/idShape, body ids through idShape before reaching Postgres (bulk skips bad shapes, create gives 404); decodeJSON caps the body at 1 MiB, one value, no trailing data, 413 on overflow.
- Canvas response: services come only from ListCanvasServices (id, name, serviceType, repoUrl, positionX/Y, deploymentCount; no env, no internal fields) and every item goes through publicCanvasService -> publicService/RedactURL. Connection output is a fixed struct (no createdAt). Auto-layout response is ids + coordinates only.
- Dependencies: none added.

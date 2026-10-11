# Plan 2 Task 10 - Product review (canvas endpoints and auto-layout)

Commits: 67cba5d, 7eb042e, 43b3fd7, 4779ed5, 757c55e. Reviewed by reading code (UI not run).

## Contract comparison (Node vs Go vs cp-ui)

| Call | Node | Go | cp-ui (live: pages/ProjectCanvasPage.tsx) |
|---|---|---|---|
| GET /projects/:id/canvas | services {id,name,serviceType,positionX,positionY,deploymentCount}, connections {id,sourceId,targetId,label} | same plus `repoUrl` (masked via publicService); soft-deleted services hidden | called; extra field ignored |
| POST /services/:id/position | {success:true}; 404 "Service not found" | same; 400 `invalid_request` on missing/NaN/>1e6 | called on onNodeDragStop only (once per drag, no debounce needed) |
| POST /services/connections | 200 connection; 404/400/409 strings | same strings; 200 body keeps id/sourceId/targetId/label/createdAt | called; uses only `id`, `label` |
| DELETE /services/connections/:id | {success}; 404 "Connection not found" | same | called |
| PUT /projects/:id/canvas | bulk | same, now validated (400 invalid_request) | NOT called by any UI |
| POST /projects/:id/canvas/auto-layout | dagre | longest-path layout, same constants (380 rank step, 230 vertical step) | only from useCanvas.ts/ServiceCanvas.tsx/CanvasToolbar.tsx, which no file imports (dead code). No Auto Layout button in the live page |

Paths, verbs, status codes and error strings are unchanged. The UI calls no endpoint Go lacks.

## Findings

[PROD-1] severity: low
where: cp-ui/src/pages/ProjectCanvasPage.tsx onConnect (~line 188-221), onEdgesDelete (~226-236), onNodeDragStop (~155-175)
problem: Every failure is only console.error. For a failed connection (409 duplicate, 400 self, 404 any wrong end) the optimistic edge just vanishes; no message at all, so the raw `status: body` text never reaches the user. For a failed edge delete, the edge is already gone locally and returns on reload. A failed position save (new 400 on NaN/|v|>1e6) silently reverts on reload.
evidence: Node behaved identically; Go adds no new visible failure for normal use. The identical 404 for every wrong end is therefore not explained to the user, but the UI cannot produce a wrong-end or cross-project connection (only same-project nodes are drawn), so only 409 (duplicate drag) and 404 (service deleted in another tab) are reachable.
fix: Not Task 10. Optional Task 15: show a toast ("Already connected", "Service no longer exists") for onConnect failures; apiFetch (Task 15) will give a parsed `error` string rather than `409: {...}`.

[PROD-2] severity: low
where: GET /projects/{id}/canvas, legacy cross-project connection rows
problem: ListProjectConnections keeps Node's OR semantic (source OR target in project), so a pre-existing cross-project row is still returned for both projects. Its other end is not a node on this canvas; React Flow cannot render an edge to a missing node (dropped, dev console warning), so it is invisible but still in the `connections` state. It can no longer be deleted from the UI (no edge to select). DELETE works for the owner if both ends are theirs. New cross-project rows cannot be created (CreateConnectionForOwner joins on same projectId, 404).
evidence: store/connections_test.go TestConnectionListsRespectProjectAndDirection. Auto-layout only uses edges whose source is in the project, so an inbound legacy edge is ignored (fine).
fix: Cutover checklist: run `SELECT count(*) FROM "ServiceConnection" c JOIN "Service" a ON a.id=c."sourceId" JOIN "Service" b ON b.id=c."targetId" WHERE a."projectId"<>b."projectId" OR c."sourceId"=c."targetId"`; expect 0 or accept invisible rows. No data change needed.

[PROD-3] severity: low
where: auto-layout results
problem: Spacing equals Node (node 180x80, ranksep 200 -> x step 380; nodesep 150 -> y step 230), so a laid-out canvas has the same grid. Differences: Go has no crossing minimization, so ordering inside a rank is creation order (ORDER BY createdAt) instead of dagre's order, and positions are whole numbers starting at 0,0. Node also repositioned soft-deleted services; Go lays out live ones only. Users who click Auto Layout (if it is ever re-exposed) may see a different but equivalent arrangement.
fix: none required; mention in release note only if the button is added.

[PROD-4] severity: low (info)
where: Go auto-layout / PUT canvas are unreachable from the shipped UI
problem: Auto-layout is not new on the server, but the live page has no button; the hook that calls it is dead code (ServiceCanvas.tsx, CanvasToolbar.tsx, useCanvas.ts are not imported). Task 15 plans to edit useCanvas.ts and will switch dead code to the shared client; harmless. Nobody noticed a missing feature vs. Node, since Node's UI also lacked it.
fix: Optionally decide whether to wire the button (small product win); otherwise leave.

## Checked and fine
- Dragging: one POST per drag end, no debounce, no burst. Rate limiters in Go (ipLimiter) only wrap register/login/refresh/logout; canvas routes are not limited. 1e6 bound is far beyond any real canvas (React Flow coordinates are in the hundreds to thousands); drag-far-away would need a 1,000,000 px pan.
- repoUrl in canvas services: UI ignores it; it is run through publicService so credentials in URLs are masked. Not a regression.
- Edges Node allowed but Go forbids (cross-project, self): not creatable from the UI (React Flow's self-connection is possible in principle by dropping on own handle; Go returns 400 "Cannot connect service to itself" for owner; the UI removes the temp edge silently, same as Node).
- Label: UI never sends a label, so the new 100-rune/no-control-char rule is unreachable.
- Malformed JSON/body errors now `invalid_request` instead of Fastify's messages: UI does not display them.
- Stricter deleteConnection (both ends must be caller's live services): affects only legacy rows pointing at another user's service (none expected, Node already required ownership of source only).

## UI items (Task 15 / Task 17)
- Task 15 (shared apiFetch, refresh): useCanvas.ts and ProjectCanvasPage.tsx switch to lib/api.ts. Canvas calls need no other change. Consider surfacing connection-create errors (PROD-1) while editing onConnect there. Make sure the 401 retry does not replay a POST position twice in a harmful way (idempotent, fine).
- Task 17: ProjectCanvasPage gets `onDeleted={loadCanvasData}`; soft-deleted services vanish from canvas and from connections (Go filters by deletedAt), consistent with this task; verify edges to a deleted service disappear after reload (store test covers it).

## Release-note lines
(No docs/superpowers/release-note-plan2.md exists yet.)
- Canvas: no change for users. Moving nodes, connecting and disconnecting work as before. Connections can only join two services in the same project; existing cross-project links, if any, will no longer be drawn.
- Deleted services no longer appear in canvas data or connections.
- Repo URLs in canvas data have credentials masked.

## Cutover-checklist lines
- Check the legacy cross-project/self connection count (query in PROD-2) and note the number.
- Run the plan's existing ownership item against `/projects/<id>/canvas` (404 "Project not found") and as owner: drag a node, reload, position persists; connect two nodes; connect the same pair again (edge disappears, no error shown, expected); delete the edge; reload.
- Compare `/projects/$PROJECT/canvas` normalized Node vs Go output (already in Task 18 `check`): expect only the added `repoUrl` field difference; make the NORMALIZE step drop or tolerate it.

RESULT: APPROVE

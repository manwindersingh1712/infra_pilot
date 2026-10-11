# Plan 2 Task 10 (canvas endpoints and auto-layout), Staff review

Commits: 67cba5d, 7eb042e, 43b3fd7, 4779ed5, 757c55e (branch go-migration).
Checked against: plan lines 2853-3265, `review/decisions.md`, `review/backlog.md` (Plan 2 Task 1, Task 8 carry-forwards), Node `control-plane/apps/api/src/routes/canvas.ts`, `cp-ui/src/hooks/useCanvas.ts`, `ServiceCanvas.tsx`.

Gates: `go build ./...`, `go vet ./...`, `golangci-lint run` all clean (0 issues). `go test -p 2 ./internal/api/ ./internal/store/` passes (api 9.5 s, store 3.4 s) on a `git archive` copy of HEAD.

## Mutation results (throwaway copy outside the repo, 46 mutants)

Killed (test fails): drop owner check in CreateConnectionForOwner; drop same-project join; drop `a.id <> b.id` (store); drop the handler self check; drop source or target `deletedAt`; GetConnectionForOwner source-only (the old query) -> `TestGetConnectionForOwnerChecksBothEnds` and `TestDeleteConnectionRequiresOwnershipOfBothEnds` fail, so the "reasoned, not run" claim holds; GetConnectionForOwner target-only; GetConnectionForOwner without the `deletedAt` filter; delete handler without GetConnectionForOwner; bulk write unscoped (UpdateServicePosition); update-position ownership replaced by plain GetService; canvas ownership dropped; magnitude cap dropped; NaN/Inf-accepting `validCoord` (whole function); missing coordinate accepted; bulk non-atomic (validate inside the loop, commit on early return); repoUrl unmasked; `[]` as null for connections and for layout; bulk id-shape skip dropped; `services: null` accepted; label validation dropped; label control chars allowed; label cap 1000; create id-shape dropped; conflict -> 500; layout nodeWidth, rankSep, nodeSep and slot-increment changes; ListProjectConnections as AND, source-only, target-only, no alive filter; ListSourceEdges no alive target, target-side included; canvas order DESC; soft-deleted services shown.

Survived:
- S1 layout: shortest-path instead of longest-path rank. No test has a diamond or a skip edge (`a->b, b->c, a->c`), so the "longest path" claim in the comment is unproven. (STAFF-1)
- S2 layout: cycle rank cap `next < len(ids)` removed. `TestLayoutTerminatesOnCyclesAndIgnoresUnknownNodes` asserts only `len(got) == 2`, not the positions. (STAFF-1)
- S3 layout: self-loop `from == to` check removed, and S4 unknown-node check removed. Same test, same weak assertion; the self-loop mutant moves a node to rank 1 and nothing notices. (STAFF-1)
- S5 layout: `for range ids` replaced by `for {}`. Equivalent: the cap plus `changed` still terminate. Not a gap.
- S6 `validCoord` with only the IsNaN/IsInf terms removed. Equivalent through HTTP: JSON cannot carry NaN or Inf and `1e999` fails decode (test "overflow" covers that path). The terms are dead defence in depth; harmless. (STAFF-5, note)
- S7 single-position write and auto-layout write unscoped (UpdateServicePosition). Equivalent today: the single path writes the id/project it just read after the ownership check, and auto-layout ids come from the project listing. Only a concurrent move-service-between-projects race could differ, and no such API exists.
- Not mutated: removing `InTx` from updateCanvas. No test makes the second UPDATE fail, so transaction atomicity against a DB error is unproven (the validate-first atomicity is proven). (STAFF-4)

## Engineer deviations

1. Error strings follow repo convention (`invalid_request`, not the plan's `invalid_json`): accepted. `decisions.md` and the Task 6/8 handlers use `invalid_request` and `request_too_large` (413 via `writeDecodeError`); the backlog Task 15 item wants one code map.
2. `CanvasService` gains `repoUrl` (7eb042e): see STAFF-2. The mask is applied and tested, which satisfies the Task 8 backlog entry, but the field is not in Node's canvas response and the UI never reads it.
3. Bulk PUT validation and silent ignore of unknown ids: accepted. Ignoring ids outside the project matches Node's `updateMany`; rejecting NaN, Inf, `|v| > 1e6`, missing coordinates and `services: null` is the Task 4-8 input-validation convention. Up-front validation gives all-or-nothing (proven by mutant). Compat: Node would persist a 1e12 coordinate; the UI never sends one.
4. Single-service position via GetServiceForOwner + UpdateServicePositionInProject: accepted. It makes the write project-scoped and `deletedAt`-aware. Behaviour is identical to the plan for an owner. (The unscoped mutant survives, which is expected; see S7.)
5. Label validation (100 runes, UTF-8, no control chars): accepted. It follows the Task 6 SEC-8 NUL-byte rule (a NUL would 500). The cap is new relative to Node, which stored any label; a user with an existing >100 rune label is unaffected because only create validates.
6. Plan test files rewritten into focused tests: accepted. Every plan assertion is still present (same status codes, bodies, 4-key connection, 380/230 spacing), plus the negative cases the plan lacked. The plan's `TestLayoutTerminatesOnCycles...` was kept as written, which is why S2/S3 survive.
7. Both-ends GetConnectionForOwner test: now proven by running the old query shape as a mutant (killed by two tests).

## Open questions

Q1, tighten ListProjectConnections from OR to both-ends-in-project: recommend yes, but not in this task and not a blocker. Spec/Node compat is the only reason for OR (Node used OR). Since 4779ed5 the API can create only same-project connections, so a cross-project row can exist only from Node-era data (Node allowed any two services of the same owner). With OR such a row appears on both canvases with a dangling endpoint; `ServiceCanvas.tsx` maps every connection to a React Flow edge, and React Flow drops an edge whose node is absent with a console error. It also exposes the other project's service id. No cross-tenant leak (Node required one owner for both ends, and `GetConnectionForOwner` still requires it). Tightening hides those rows and makes them undeletable from the UI, which is acceptable because they are already unrenderable. Put it in the backlog as a low item with the cutover checklist line "count `ServiceConnection` rows whose ends are in different projects"; if any exist, delete or keep OR. The current test `TestConnectionListsRespectProjectAndDirection` kills the AND mutant, so a change would need that test updated deliberately.

Q2, connections with a soft-deleted end are undeletable via the API: acceptable, and the right owner is Task 12 teardown. The rows are invisible in the canvas and in layout, cannot conflict with a new connection (service ids are never reused), and cannot leak. They are only garbage. Task 12's teardown transaction should `DELETE FROM "ServiceConnection" WHERE sourceId = $1 OR targetId = $1` next to the EnvVar scrub already listed under Plan 2 Task 8 SEC-6/STAFF-5 (see STAFF-3). Do not loosen GetConnectionForOwner to allow deleted ends: that would let a user delete a connection through a service the rest of the API treats as non-existent.

## Findings

[STAFF-1] severity: low
where: control-plane-go/internal/api/layout_test.go:23-27
problem: the layout tests do not prove longest-path ranking, the cycle cap, self-loop skipping or unknown-node skipping. The code comment promises all four.
evidence: mutants "shortest path" (`rank[to] == 0 && ...`), "no rank cap", "self-loop not ignored" and "unknown nodes not ignored" all survive `go test`. `TestLayoutTerminatesOnCyclesAndIgnoresUnknownNodes` only checks `len(got) == 2`.
fix: add (a) a diamond `a->b, b->c, a->c` expecting c at 760; (b) `{a,b}` with `a->b, b->a` expecting the exact positions {a 0,0}{b 380,0}; (c) `a->a` alone expecting {a 0,0}; (d) `zzz->a` expecting a at 0. Replace the length-only assertion with DeepEqual.

[STAFF-2] severity: low
where: control-plane-go/internal/store/services.go:106 (`CanvasService.RepoURL`), internal/api/canvas.go:18-22
problem: the canvas response gains `repoUrl` that Node never returned and the UI never reads (`useCanvas.ts` `CanvasService` has no such field; only the create modal uses `repoUrl`). It is masked, but it widens a response whose only job is topology, and every future change to `publicService` now has one more call site to keep in step.
evidence: Node `getCanvasHandler` selects id, name, serviceType, positionX, positionY, `_count`. Go test `TestCanvasMasksRepoURLCredentials` exists and the mutant "repoUrl unmasked" is killed, so the leak risk is covered today.
fix: preferred: drop `RepoURL` from `CanvasService` and the masking helper (shape then matches Node exactly: the Engineer chose this to satisfy the Task 8 backlog line "every response containing a service must use publicService", and removing the field satisfies it more simply). If Task 17 needs the URL on the canvas, keep it and say so in the task. CTO call; harmless either way.

[STAFF-3] severity: low
where: backlog (Task 12 teardown)
problem: soft-deleted services leave their `ServiceConnection` rows (Q2) and, from Task 8, EnvVar rows.
fix: have the Task 12 teardown tx delete connections of the service, and add a one-line backlog entry now so Task 12 does not miss it.

[STAFF-4] severity: low
where: control-plane-go/internal/api/canvas.go:99-110 (updateCanvas), 1 MiB body limit in respond.go:28
problem: (a) no test makes a mid-batch UPDATE fail, so removing `InTx` would pass; (b) no cap on `len(services)`: a 1 MiB body holds roughly 15-20k entries, each one a round trip inside one transaction held open for seconds, by any authenticated user. Node had the same hole.
evidence: the "bulk no tx" mutant could not be built cheaply and no test exercises a DB failure; `idShape`-valid unknown ids are all executed.
fix: reject more than, say, 500 entries with 400 `invalid_request` (a real canvas has tens of nodes; add a boundary test), and add a trigger-based failing-UPDATE test like `failOutboxInserts` (services_test) to pin atomicity.

[STAFF-5] severity: low (note)
where: control-plane-go/internal/api/canvas.go:44-49 `validCoord`
problem: the NaN/Inf terms are unreachable through JSON (decoder rejects `1e999`, and literal NaN/Infinity are invalid JSON); the mutant that removes them survives. Keep them as defence in depth or drop them; do not count them as tested. The `maxCoord` term is tested.
fix: none required; a one-line comment would stop a future reader from "fixing" the survivor.

[STAFF-6] severity: low (note, JSON shape / UI)
where: cp-ui/src/hooks/useCanvas.ts:14-19
problem: Go returns `"label": null` for an unlabelled connection (Node's `select` also returned null), while the UI type says `label?: string`. Runtime is fine (`ServiceCanvas.tsx:57` passes it to React Flow, where null is falsy). Type is slightly wrong.
fix: Task 15/16 shared client types `label: string | null`. No Go change.

## Compatibility checks (Node canvas vs Go)

- GET `/projects/:id/canvas`: `services[]` keys id, name, serviceType, positionX, positionY, deploymentCount (Go adds repoUrl, STAFF-2); `connections[]` keys id, sourceId, targetId, label (exactly 4, tested). Ordering differs (Go createdAt ASC), already recorded as VER OBS-1. Soft-deleted services and connections touching them are hidden (new, by design).
- POST `/services/connections`: returns the full row (id, sourceId, targetId, label, createdAt) like Prisma; 404 `Service not found`, 400 `Cannot connect service to itself`, 409 `Connection already exists` strings match Node. Difference: Go answers 404 for a foreign self-connection (Node checked ownership first too, same), and now also 404 for a service in another project of the same owner (Node allowed it; intentional per backlog SEC-1).
- DELETE returns `{success:true}` / 404 `Connection not found`. Go requires both ends owned (Node checked only the source).
- POST `/services/:id/position`, PUT canvas, POST auto-layout: bodies and `{success:true}` match; auto-layout returns `{services:[{id,positionX,positionY}]}` as the UI's `runAutoLayout` expects. Layout positions are not identical to dagre (dagre centres ranks vertically and reorders to reduce crossings; Go stacks top-aligned in input order). X coordinates match for chains. The plan accepted this.
- Route patterns: `POST /services/connections` versus `POST /services/{id}/position` and `DELETE /services/connections/{id}` versus `DELETE /services/{id}` do not conflict; the tests run, so no mux panic.

## Cohesion

- `canvas.go` is 273 lines for six handlers plus coordinate and label validators; `layout.go` is 55. Acceptable. `validLabel` and `validProjectName` repeat the "no control characters" loop (and the Task 6 name validator); a shared `noControlChars(string) bool` in the api package would remove three copies. Low, optional.
- `ownedProjectID` + `ownsProject` + `pathID` re-run `idShape` twice per request (`ownedProjectID` discards its `pathID` result, then `ownsProject` re-checks). Harmless; a comment explains it.
- Handler ordering: body validation runs before ownership in `updateCanvas`, `updatePosition` and `createConnection`. This only reveals that the body was bad, never whether a resource exists, so there is no oracle. Fine.
- Contexts are passed through, errors go to `internalError`, no dropped errors in production code. Tests still discard some setup errors (`db, _ := hs.st.CreateService(...)`), same style as neighbouring tests (backlog STAFF-5 theme).
- `ListCanvasServices` and `ListSourceEdges` in autoLayout are two reads outside a transaction; a service created between them is simply unplaced/ignored. Fine.

RESULT: APPROVE (0 critical, 0 high, 0 medium, 6 low)

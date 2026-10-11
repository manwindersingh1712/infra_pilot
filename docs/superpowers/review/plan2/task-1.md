
## Security

Commit e9c3043 (Plan 2 Task 1, service connection queries). Reviewer: Security.

Tools (from control-plane-go/):
- govulncheck ./... : "No vulnerabilities found. Your code is affected by 0 vulnerabilities." (1 vuln in a required module, not called by our code; pre-existing, not from this commit).
- gosec -quiet ./... : 2 issues, both G204 in internal/runtime (exec.go:26, a logs-follow command). Neither file is in this commit. G204 is the documented engineer exclusion; no finding.
- gitleaks detect --log-opts="e9c3043^!" : 1 commit scanned, no leaks found.
- cp-ui unchanged, no npm audit.

Diff review:
- SQL: all queries use bound parameters ($1..$4); only the constant column list is concatenated. No injection.
- IDs from cuid.New(); label is a bound value. No secrets, no logging, no error text with user data (ErrConflict/ErrNotFound are sentinel errors).
- No new dependencies (pgx and cuid already used).
- Foreign IDs: GetConnectionForOwner returns ErrNotFound for a non-owner (tested), so the handler can return 404.

[SEC-1] severity: low
where: control-plane-go/internal/store/connections.go:36-45 (GetConnectionForOwner), :23 (CreateConnection), :50 (DeleteConnection)
problem: Tenant isolation depends on the Task 2 handlers. (a) GetConnectionForOwner checks ownership of the source service only, not the target. (b) CreateConnection checks nothing, so the handler must verify that both source and target services belong to the caller (ideally the same project). If it does not, a user can link their service to a foreign service ID. ListProjectConnections matches on source OR target in the project, so the foreign project would then see an edge whose other end is another tenant's service ID. (c) DeleteConnection takes only an id and has no owner check; it is safe only if the handler calls GetConnectionForOwner first.
evidence: `WHERE "id" = $1 AND "sourceId" IN (SELECT s."id" ... WHERE p."ownerUserId" = $2)`; `DELETE FROM "ServiceConnection" WHERE "id" = $1`
fix: In the POST /canvas/connections handler, load both services with the owner-scoped getter and return 404 if either is missing or they are in different projects, before CreateConnection. Always call GetConnectionForOwner before DeleteConnection. Optionally make the store safer by ownership-scoping the delete and the create (INSERT ... SELECT with an owner join). Carry this as a Task 2 review checkpoint; it is not exploitable at this commit because no route calls these functions yet.

RESULT: FINDINGS (low: 1)

## Staff

Commit e9c3043. `go build`, `go vet`, `golangci-lint` clean (0 issues). `go test ./internal/store -run Connections` passes; `-short` skips cleanly. Code matches the plan verbatim, and the schema matches Prisma (`@@unique([sourceId,targetId])`, cascade FKs).

[STAFF-1] severity: low
where: control-plane-go/internal/store/connections_test.go:95-115
problem: Test discards errors from the list calls (`list, _ =`) and leaves branches unproven. The `OR targetId` branch of ListProjectConnections, cross-project exclusion, and the inbound-edge exclusion in ListSourceEdges would all pass if broken. The duplicate case uses a nil label, so a label change on conflict is not checked.
evidence: Only one project and one outbound edge are seeded.
fix: Check errors. Add a second project's service as target (listed by ListProjectConnections, absent from the other project's ListSourceEdges). Seed a second project and assert it sees no rows.

[STAFF-2] severity: low
where: control-plane-go/internal/store/connections.go:147-155
problem: An FK violation (23503, unknown source or target) is returned as a raw pgx error, which would surface as a 500. Only the unique violation is mapped. Self-connections (source == target) are allowed.
evidence: Node API behaviour is the same, but the handler must check that both services exist and are owned (Task for handlers).
fix: No change needed here if the handler validates ownership of both ends. Note this for the handler task. Optionally map 23503 to ErrNotFound.

[STAFF-3] severity: low
where: control-plane-go/internal/store/connections.go:167-173
problem: GetConnectionForOwner checks ownership through the source service only. A connection whose target is in another project (possible if the handler does not validate it) is readable and deletable by the source owner. This is consistent with ListSourceEdges but not with ListProjectConnections (either end).
fix: Document the invariant that both ends must be in the same owner's project, enforced at create in the handler.

RESULT: APPROVE (0 critical, 0 high, 0 medium, 3 low)

## Verifier

Commit e9c3043 (Plan 2 Task 1, service connection queries). Tree clean apart from the known untracked files.

RESULT: PASS

- `cd control-plane-go && go build ./... && go vet ./...` -> `BUILD_OK`, `VET_OK`
- `golangci-lint run` -> `0 issues.` (first attempt collided with a parallel golangci-lint run; re-run with `--allow-parallel-runners` was clean)
- `go test -race ./...` -> exit 0, no FAIL lines. Last lines:
  `ok  infrapilot/internal/workflows 17.038s`, `? infrapilot/migrations [no test files]`
  (db 21.0s, runtime 79.2s, worker/outbox 14.8s ran uncached)
- `go test -race -count=1 ./internal/store/` re-run uncached (see output in the report).
- Feature/regression: store-only change; covered by the store tests above. No cp-ui changes, no live API run needed.

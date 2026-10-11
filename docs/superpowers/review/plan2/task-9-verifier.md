# Plan 2 Task 9 (d1772bc, GET /deployments) - Verifier

RESULT: PASS

## Suite (control-plane-go/, commit d1772bc)
- go build ./... -> exit 0; go vet ./... -> exit 0
- golangci-lint run --allow-parallel-runners -> "0 issues."
- gosec -quiet -exclude=G204 ./... -> exit 0, no findings
- gitleaks git --redact (repo root) -> "358 commits scanned ... no leaks found"
- go test -race -count=1 -p 2 ./internal/api/ ./internal/store/ -> ok api 86.9s, ok store 11.2s
- go test -race -count=1 -p 2 ./... -> all packages ok (api 77.2s, runtime 79.7s, store 4.5s ...), exit 0

## Real end-to-end (git archive d1772bc copy in /tmp, real Postgres via testcontainers, real TCP httptest server, users via store; copy deleted)
- User A: 2 services (s1 with 60 deployments, s2 with 3).
- GET /deployments (A): 200, count 50 (cap), newest-first (first commitSha d002, last c013), no foreign rows.
- ?serviceId=s1: 50, newest is the last created (c059). ?serviceId=s2: 3, newest-first order verified by id.
- B: /deployments -> `[]`; ?serviceId=<A's s1> -> `[]`; NUL (a%00b) -> `[]`; %ff -> `[]`; 65 chars -> `[]` (all 200, body is `[]`, not null).
- No token -> 401 {"error":"unauthorized"}; garbage token -> 401.
- After soft-deleting s2: A's list shows 50 and none from s2; ?serviceId=s2 -> 200 `[]`.

Actual JSON of one deployment:
{"id":"cmv4c7xgn001yzv3nqy9g0v7u","serviceId":"cmv4c7xbm0007zv3nivv5lm3o","commitSha":"d002","image":null,"status":"queued","containerId":null,"hostPort":null,"containerPort":null,"runtimeUrl":null,"volumePath":null,"createdAt":"2026-10-11T21:30:52.583Z","updatedAt":"2026-10-11T21:30:52.583Z","stoppedAt":null}

## Node comparison (control-plane/apps/api/src/controllers/deploymentController.ts, routes/deployments.ts)
Node: prisma.deployment.findMany({ where: serviceId ? {serviceId} : undefined, orderBy createdAt desc, take 50 }).
- Same: route, optional serviceId filter, createdAt desc, limit 50, camelCase keys, ISO-ms "Z" dates, nulls for unset image/containerId/hostPort/containerPort/runtimeUrl/volumePath, JWT required (401 {"error":"unauthorized"}).
- Fields: Node/Prisma model has 12 (id, serviceId, commitSha, image, status, containerId, hostPort, containerPort, runtimeUrl, volumePath, createdAt, updatedAt). Go returns the same 12 plus `stoppedAt` (null when unset): additive; column comes from goose migration 00002, absent in the Prisma schema. Clients ignoring unknown keys are unaffected.
- Intended divergences (hardening): Go is owner-scoped (Node returned every user's deployments, and any serviceId), hides soft-deleted services' deployments, and answers malformed serviceId with `[]` (Node: Prisma returns [] for a plain unknown string too).

## Dev environment (read-only)
- go run ./cmd/migrate status: 00001-00003 applied; "00004 00004_refresh_family.sql pending" (migrate up NOT run).
- ~/cp-logs/go-worker.log: last write 02:56, 0 lines at level=ERROR, 0 WARN; only periodic "flushed logs processed=0 errors=0".
- Node API: curl localhost:8080/healthz -> {"ok":true} [200].

## Notes
- At the end of the run `git status` showed uncommitted modifications to internal/api/deployments_test.go and internal/store/deployments.go (not by the Verifier; probably review fixes in progress). All checks above ran against the committed d1772bc tree (suite ran from the working tree before those edits appeared: git status was clean at start; e2e used git archive).
- Failures: none.

# Task 9 security review (commit d1772bc, GET /deployments)

Reviewer: Security. Scope: internal/api/deployments.go, deployments_test.go, server.go, store.ListDeploymentsForOwner.

## Tool results
- go test -p 2 ./internal/api/ ./internal/store/: ok
- govulncheck ./...: 0 vulnerabilities affecting code (1 in a required module, not called; not introduced by this commit)
- gosec -quiet ./...: 2 issues, both pre-existing and outside the diff (internal/runtime/exec.go:26 G204 on the arg-list runner, intended; one more elsewhere). None in the touched files.
- gitleaks (d1772bc^!): no leaks

## Review
- Auth: route is wrapped in s.private (Bearer JWT verified); no token gives 401.
- Ownership: SQL filter `"serviceId" IN (SELECT s.id FROM Service s JOIN Project p ... WHERE p.ownerUserId = $2 AND s.deletedAt IS NULL)`. Soft-deleted services are excluded; foreign serviceId combines with the owner filter, so it returns `[]`, identical to a missing or malformed id (no existence oracle). Test covers own/foreign/all.
- SQL: all values bound ($1 serviceId, $2 owner); the only concatenation is the constant column list and constant ownedByCaller.
- Validation: serviceId must match idShape ^[A-Za-z0-9_-]{1,64}$ else `[]` (200); avoids NUL/invalid UTF-8 reaching Postgres. Empty string means no filter. Repeated params: Query().Get takes the first; harmless.
- DoS: LIMIT 50 hard-coded, no user-controlled paging. The subquery is indexed by owner; fine at this scale.
- Errors: internalError path, no raw error text to the client.

## Response fields (13), compared with Node
Node returned prisma.deployment.findMany unfiltered: all columns, no auth scoping at all (any caller saw everyone's deployments). Go returns the same 13 columns, so this is a strict improvement and no regression.

| field | UI uses (cp-ui/src) | sensitivity |
|---|---|---|
| id, serviceId, status, createdAt | yes | none |
| commitSha | yes (slice 8) | low, user's own repo |
| runtimeUrl | yes | none, intended public URL |
| hostPort | yes (ServiceDetailsPanel:299, managed DB connection port) | low, loopback-bound, owner-only |
| containerPort | declared in interface | low |
| image | declared in interface | low: reveals registry host and tag (localhost:5001/...) |
| containerId | no | low: docker id, owner-only |
| volumePath | no | low: host filesystem path (/tmp/cp-volumes/...) leaks host layout |
| updatedAt, stoppedAt | no (stoppedAt is new for the Product review) | none |

No secrets, env values, error text, log excerpts or credentials are in the row. Data is only visible to the owning user.

## Findings
[SEC-1] severity: low (optional hardening, not blocking)
where: control-plane-go/internal/store/models.go:82 (Deployment JSON), used by internal/api/deployments.go
problem: containerId and volumePath are returned to the browser but the UI never reads them; volumePath exposes a host filesystem path, containerId a docker id. image exposes the internal registry host (read in interface only). Parity with Node, owner-only, so impact is minimal information disclosure.
fix: Optional. Either leave for parity (the Plan 2 contract fixes 13 fields and the test asserts len==13), or introduce a list-view DTO omitting containerId and volumePath (and image if confirmed unused) in a later task, together with updating the test count. Do not trim in this commit without Product agreement since the test and contract pin 13 fields.

RESULT: FINDINGS (low 1, optional). No critical/high/medium issues; ownership, injection, auth and DoS checks are clean.

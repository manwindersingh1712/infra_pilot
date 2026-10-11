# Task 9 Product review (commit d1772bc, GET /deployments)

Reviewed by reading code only: cp-ui/src, control-plane (Node, read only), control-plane-go store and API. The UI was not run.

## What cp-ui actually uses

Only one caller: `ServiceDetailsPanel.tsx:467` does `GET /deployments?serviceId=<id>`. The unscoped list is never requested by the UI. The UI `Deployment` interface (ServiceDetailsPanel.tsx:7, DeploymentStageTracker.tsx:1) reads: id, serviceId, commitSha, image, runtimeUrl, status, hostPort, containerPort, createdAt. All nine are present in the Go response with the same camelCase names. `createdAt` is ISO with milliseconds and Z (Timestamp marshaller), same as Prisma/Node. The UI ignores volumePath, containerId, updatedAt and stoppedAt.

Status strings: the UI branches on queued, building, deploying, deployed and failed. The Go enum is the same five (migration 00001). There is no `stopped` status; "stopped" is `stoppedAt != null` on a row whose status stays `deployed`. The UI never tests for `stopped`, so nothing breaks.

Polling: every 2 s while any listed item is queued/building/deploying. The Go list answers the same, and it is owner-scoped, so the poll needs a valid access token. Task 15 must refresh it; otherwise the 15 minute expiry makes the poll fail (it only sets `error`).

Ordering: `created_at DESC LIMIT 50`, identical to Node. The UI re-sorts client side anyway, and treats `deployments[0]` as the latest, which is right.

Empty: `[]` gives "No deployments yet."; same as Node.

Malformed `serviceId`: 200 `[]` (Node also returned `[]`, since Prisma matched nothing). Same behaviour.

Node differences: Node had no auth or ownership check on this route, so any caller saw all users' deployments. Go returns only the caller's. A repeated `?serviceId=a&serviceId=b` is a Node zod error; Go uses the first value. Neither matters to the UI.

## Findings

[PROD-1] severity: low
where: ServiceDetailsPanel.tsx deployment history (lines ~813-930) and DeploymentStageTracker.tsx
problem: Superseded deployments are stopped (`stoppedAt` set) but keep `status: "deployed"`. The UI ignores `stoppedAt`, so every old deployment shows as a green "deployed" with its runtime link, even though the container is gone. Same as the Node UI today (Node never marked stopped), so not a regression.
evidence: store/deployments.go:222 sets stoppedAt only; the status is not changed. The UI has no `stoppedAt` field.
fix: Optional, in Task 15/17 or later. Add `stoppedAt: string | null` to the UI type and show "Stopped" / hide the runtime link for those rows. Do not change the API.

[PROD-2] severity: low
where: ServiceDetailsPanel.tsx:948, Connection tab
problem: `deployments.find(d => d.status === "deployed")` takes the newest "deployed" row. If the newest deployment failed and the older one has since been stopped, the connection panel shows a stopped container's details. It is only reachable if an old deployment gets stopped without a successful replacement. Same as Node. Task 17 is rebuilding this panel, so it is the place to fix it.
fix: In Task 17, filter on `status === "deployed" && !stoppedAt`.

[PROD-3] severity: low (note)
where: more than 50 deployments
problem: Nothing paginates. The UI request is per service, so the limit is the 50 newest deployments of that one service (Node had the same limit but across all users). A service with over 50 deployments loses older history from the panel (no data loss). The latest and any pending deployment are always in the 50. The unscoped `/deployments` would truncate across all services, but the UI never calls it.
fix: Accept. Optionally put "last 50 deployments" in the release note. Pagination is a post-cutover idea.

[PROD-4] severity: low (note)
where: deployments of a deleted service
problem: Rows are hidden (the `deletedAt IS NULL` filter) and the service is gone from the canvas, so the UI has nothing to show them under. `GET /deployments?serviceId=<deleted>` gives `[]`, not 404, so a stale open panel for a just-deleted service quietly shows "No deployments yet." (the polling stops as the list is empty). Logs for a deleted service's deployments also stop being reachable through Task 8 ownership, which is acceptable.
fix: None needed. Task 17 should close the panel after a delete succeeds.

[PROD-5] severity: low (note)
where: ownership
problem: Foreign or unknown serviceIds answer 200 `[]`, so a user cannot tell "not yours" from "no deployments". That is the intended no-leak behaviour and the UI cannot hit it in normal use. A 401 (expired token) shows the raw error text in the panel until Task 15 lands.

No critical, high or medium findings. The UI contract holds: no field the UI uses is omitted or renamed, and the status strings match.

## Task 15 / 17 UI items
- Task 15: the shared client with refresh must cover the 2 s deployments poll; check a refresh during polling does not stop it, and that `error` shows a readable "session expired, log in again" rather than the raw 401 body.
- Task 15 (optional): add `stoppedAt: string | null` to the shared Deployment type.
- Task 17: in the Connection panel choose the deployment with `status === "deployed" && !stoppedAt` (PROD-2); close the details panel and refresh the canvas after a service delete (PROD-4).
- Later UI nicety: render "Stopped" for superseded deployments (PROD-1).

## Release-note / cutover-checklist lines
- "The deployment list now only shows your own services' deployments (previously any signed-in user could list everyone's). It shows the 50 most recent deployments per service."
- "Deployments of deleted services are no longer listed."
- "Each deployment now has a `stoppedAt` timestamp (null while running); superseded deployments keep status `deployed`."
- Cutover checklist: ship Task 15 with Task 6 (tokens expire every 15 min and the panel polls every 2 s).

RESULT: APPROVE

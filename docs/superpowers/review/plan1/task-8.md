# Plan 1, Task 8: Deployments, jobs and container lifecycle

## Implementation
- Engineer commit `e546f34` feat(go): deployment queries, workflow jobs and container lifecycle (`internal/store/{models.go,deployments.go,deployments_test.go}`). Plan-verbatim; gofmt realigned the Deployment struct tags.

## Reviews (commit e546f34)
- **Security: CLEAN** for this commit (all SQL bound; ID-scoped transitions fine for the worker; Plan 2 must use the owner-scoped variants). Forward findings in **later plan code**: FWD-A1 high (git argument injection / file:// transports in the Task 22 builder + loose Plan 2 validation), FWD-A2 medium (repo URL tokens in logs and Temporal history via LoadJob), FWD-A3 low (legacy volumePath trusted) → sent to the CTO.
- **Staff: APPROVE.** ClaimDeployment is the same compare-and-set as Node's `updateMany`; updatedAt set on every write; live/running/UsedHostPorts semantics fit Tasks 17, 20, 23; `Job` is a Deployment⋈Service read model. Lows STAFF-1..3 (T8) → backlog. STAFF-4 (workflow cleanup after MarkDeployed can mark a deployed row failed and remove the live container) → sent to the CTO with the forward findings.
- **Verifier: PASS.** Lifecycle walk: 10 concurrent claims → exactly 1 winner; MarkBuilt returns to queued (as Node); MarkStopped idempotent; ListLive/ListRunning/UsedHostPorts exclude stopped rows; updatedAt advances on every write. Read-only: Go `ListDeployments` vs Node `GET /deployments` (30 rows, and 13 for one service): identical values, types, order and key order; Go adds `stoppedAt`. Dev DB unchanged. One host stall (15 min at 0% CPU) on the first suite run; retry fine.

## Result
**Done 2026-10-08.** Verifier PASS, no open critical/high/medium in this commit. Forward findings pending a CTO ruling on later plan text (Tasks 21-24, Plan 2).

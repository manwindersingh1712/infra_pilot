# Go + Temporal Migration — Design (v2, after CTO review)

**Date:** 2026-10-07
**Status:** Design approved in principle; detailed plans to follow.
**Replaces:** `docs/superpowers/plans/2026-10-07-go-backend-migration.md` (RabbitMQ version, marked SUPERSEDED). Tasks from that file are reused wherever this design does not change them.

## 1. Goal

Rewrite the backend in `control-plane/` (Node/TypeScript, about 2,800 lines) in Go, with Temporal running the build/deploy pipeline. Use the rewrite to fix the security and correctness problems that block `SAAS_ROADMAP.md` (a Railway/Fly-style PaaS: one VM first, then several VMs, then Kubernetes).

The migration is complete when:
- only the Go API and Go worker run;
- RabbitMQ and MinIO are gone;
- the Node code stays in `control-plane/` as a frozen, unmaintained reference (also tagged `node-final`), and the Go backend lives in `control-plane-go/`;
- roadmap Phase 2 ("Security & Multi-Tenancy") is done.

**Non-goals:** GitHub OAuth and webhooks (Phase 3), billing (Phase 5), multiple VMs and Kubernetes (Phase 6). The design must not block them, but it does not build them.

## 2. Decisions

| # | Topic | Decision |
|---|---|---|
| D1 | Job pipeline | Temporal replaces RabbitMQ. One `DeployWorkflow` and one `TeardownWorkflow` per service operation. |
| D2 | Starting workflows | Keep the `OutboxEvent` table. The API writes the state change and an outbox row in one transaction; the worker's outbox starter calls `ExecuteWorkflow`. The workflow ID is `deploy-<deploymentId>` / `teardown-<serviceId>` (a soft-deleted service cannot be deleted again) with the reject-duplicate policy, and "already started" counts as success. |
| D3 | Order | **Worker first** (Plan 1), API second (Plan 2), hardening and cleanup third (Plan 3). The riskiest new piece, Temporal, gets proven first. |
| D4 | Node code | **Never deleted.** Runnable during the rollback window (§9). After that it is frozen: tagged `node-final`, marked with `control-plane/FROZEN.md`, excluded from CI, and not maintained. The Go backend stays in `control-plane-go/` (no rename). |
| D5 | Live logs | SSE, authenticated with a single-use stream ticket (never the JWT in the URL). |
| D6 | Temporal hosting | Dev: `temporal server start-dev` in docker-compose. Single-VM production: Temporal server on the existing Postgres (`temporal` and `temporal_visibility` databases) plus the Temporal UI behind auth. Revisit Temporal Cloud at roadmap Phase 6. |
| D7 | Runtime abstraction | Workflows depend only on the `Runtime` (containers, networks, volumes, probes) and `Router` (ingress) interfaces. Docker is today's implementation; Kubernetes can replace it in Phase 6. |
| D8 | Log tiers | Redis (hot) and ClickHouse (warm, 30-day TTL). **The S3/MinIO archive is cut**: nothing ever read it. |
| D9 | Estimate | About 5–6 weeks of engineering, plus a 2-week rollback window that overlaps Plan 2. Feature freeze on the backend during the migration. |

## 3. Architecture

```
UI ──HTTPS──> Go API ──tx──> Postgres (state + OutboxEvent)          Redis (hot logs, pub/sub,
   ◄──SSE (ticket)──┘                                                  stream tickets, flush cursors)
                                                                           ▲
Go worker ─┬─ outbox starter ──ExecuteWorkflow──> Temporal ──tasks──┐      │
           ├─ Temporal worker (queue "deployments") ◄──────────────┘      │
           │     activities: Runtime (docker) · Router (nginx) · Builder (buildx) · Store
           ├─ log streamer (docker logs -f → Redis) ───────────────────────┘
           └─ flush loop (Redis → ClickHouse, cursor per deployment)

Docker host: nginx ── attached to every project network ── cp-proj-<projectId>
                                                             ├─ app containers (no published ports)
                                                             └─ managed DBs (password, named volume)
```

Package layout (Go module `infrapilot`):
- `cmd/`: `api`, `worker`, `migrate`.
- `internal/`:
  - `config`, `db`, `store` (all SQL);
  - `auth` (JWT, refresh tokens, bcrypt);
  - `secrets` (AES-GCM for env vars);
  - `logs` (Redis, ClickHouse, aggregator);
  - `runtime` (Docker implementation of `Runtime`, nginx implementation of `Router`, buildx `Builder`, streamer);
  - `workflows` (workflows and activities);
  - `worker/outbox`;
  - `api`;
  - `metrics`;
  - `testutil`.

## 4. Workflows

### DeployWorkflow(deploymentID)

Every activity has its own timeout and a retry policy: 10 s initial interval, ×2 backoff, 6 attempts unless noted. A failure that cannot succeed on retry (missing repo, unknown service type) is a non-retryable `ApplicationError`.

1. **LoadJob.** Return early if the status is not `queued`. The result never carries the repo URL (it may embed a token); `BuildImage` reads it from the store.
2. **Built services only, when the deployment has no image yet** (a Node-built row skips straight to step 3):
   1. `SetStatus(building)`.
   2. `BuildImage`: buildx, StartToClose 30 min, heartbeat 1 min.
   3. `MarkBuilt(image)`.
3. **ClaimDeploy:** a service-scoped claim `queued → deploying`. In one transaction it locks the service row and refuses (retryable `ServiceBusy`, retried every 5 s for up to 30 min) while another deployment of the same service is `deploying` (a `deploying` row older than 1 h counts as abandoned). A row already `deploying` is this run's own and wins (crash resume). Builds may overlap; deploys of one service run one at a time.
4. **EnsureNetwork:** `cp-proj-<projectId>`, with nginx attached (idempotent).
5. **Managed DBs only:**
   1. **EnsureCredentials:** generate a 24-byte random password on first deploy and store it as an encrypted env var (`REDIS_PASSWORD` or `MONGO_INITDB_ROOT_PASSWORD`).
   2. **StopPrevious:** stop and remove the old container *before* starting the new one, because two database processes must never share a data directory.
6. **RunContainer**, named `cp-<deploymentId>`:
   - network alias `svc-<serviceId>`;
   - resource limits;
   - named volume `cp-vol-<serviceId>` mounted at the image's data directory (`/data` for Redis, `/data/db` for Mongo, with `/data/configdb` on tmpfs) for managed DBs;
   - Redis runs with `--requirepass`;
   - no published ports, except managed DB ports bound to `127.0.0.1` when `EXPOSE_MANAGED_PORTS=true` (default on in dev only; refused when `APP_ENV=production`).
7. **WaitHealthy:** a TCP probe of `svc-<serviceId>:<port>` from a throwaway `busybox` container on the project network, polled every 2 s for up to 60 s.
8. **SwitchRoute:** write the nginx server block and reload nginx (app services only). The activity runs with `WaitForCancellation`; the workflow marks the route as attempted before the call, because an error does not prove nginx was untouched.
9. **MarkDeployed:** the commit point; runs in a disconnected context with extended retries (up to 30 min).
10. **RetirePrevious** (app services only): stop and remove containers of this service's older deployments, and set their `stoppedAt`.
11. **StartLogStream.**
12. **On final failure:** cleanup runs in a disconnected context. `MarkFailed` never overwrites `deployed`. The new container is removed only if the route was never attempted, or the attempt failed and `RevertRoute` (repoint to the previous live deployment, or remove the route) succeeded; if the revert fails, or after a successful `SwitchRoute`, the container is kept. When the route was attempted, the order is `RevertRoute`, then `RemoveContainer` (only if the revert succeeded), then `MarkFailed` last, because `MarkFailed` releases the service-scoped `ClaimDeploy` lock and a successor must not switch the route before the revert ran; `MarkFailed` is called even if the revert failed. In the other paths the order is irrelevant. After `MarkDeployed`, the deployment stays `deployed`: `RetirePrevious` and `StartLogStream` also run in a disconnected context so a cancel cannot skip them, and a failure of either fails the workflow run (visible in the Temporal UI) but runs no cleanup. Failed runs are visible in the Temporal UI, which replaces the DLQ.

### TeardownWorkflow(serviceID, purgeData)

1. Stop and remove every container of the service.
2. Remove the nginx route.
3. Remove the named volume, only if `purgeData`.
4. Mark the deployments `stoppedAt`.

The API has already soft-deleted the service (`deletedAt`), so it disappears from listings immediately.

### Outbox starter

Keeps the existing claim, backoff and `dead` logic, and calls a `WorkflowStarter` interface. Event mapping:
- `build.requested` and `deploy.requested` start `DeployWorkflow`. Both keep their names, because the Node API still writes them during Plan 1.
- `teardown.requested` starts `TeardownWorkflow`.

## 5. Security changes (roadmap Phase 2, folded into the migration)

| Problem today | Change |
|---|---|
| Managed Redis has no password, Mongo uses `admin/admin123`, and both ports are published on the host | Generated passwords; no published ports in production. Every managed DB gets a generated password. Node mounted Mongo's volume at `/data`, so its data lived only in the running container's anonymous volume. Node-era Mongo data is moved by the cutover runbook (`mongodump`/`mongorestore`), not by a credential backfill. |
| All containers share `cp_net` | One network per project. nginx joins each project network. |
| No resource limits | Defaults `CONTAINER_MEMORY=512m`, `CONTAINER_CPUS=0.5`, `CONTAINER_PIDS=256`, overridable in config. |
| Tenant containers run as root with Docker's default capabilities (generated Dockerfiles have no `USER`) | Every container starts with `--cap-drop ALL` and `--security-opt no-new-privileges`. Images add back only what their entrypoint needs, from the allow-list {CHOWN, SETUID, SETGID, DAC_OVERRIDE, DAC_READ_SEARCH, NET_BIND_SERVICE} (anything else is rejected): Mongo `CHOWN`, `SETUID`, `SETGID`, `DAC_READ_SEARCH` (a redeploy walks a 0700 data dir as root); Redis `DAC_OVERRIDE` (runs as root, RDB persistence as in Node); `react` services (stock `nginx:alpine`) `CHOWN`, `SETUID`, `SETGID`. All verified against the real images. Read-only root filesystems are not enabled, because tenant apps write to their own filesystem. |
| Builds run on the host Docker daemon | `docker buildx build --builder cp-builder --push`. `cp-builder` is a `docker-container` BuildKit builder with CPU and memory limits, so Dockerfile steps run inside BuildKit, not on the host daemon. Rootless BuildKit or a separate build VM is the next step (Phase 6). |
| JWTs never expire | 15-minute access tokens with an `exp` claim, plus 30-day opaque refresh tokens stored as SHA-256 hashes in a new `RefreshToken` table. Refresh tokens rotate on every use and are revoked on logout. Endpoints: `POST /auth/refresh`, `POST /auth/logout`. Tokens without `exp` (issued by Node) are rejected after the Plan 2 cutover, so users log in again once. |
| The JWT would sit in the SSE URL | `POST /logs/deployments/:id/stream-ticket` returns a single-use ticket: Redis key with a 60 s TTL, consumed with `GETDEL`. The stream URL is `?ticket=`. |
| `POST /dev/login` is open | Registered only when `APP_ENV=development` (falls back to `NODE_ENV`; unset means production, any other value is a startup error). |
| No brute-force protection | Per-IP token bucket on `/auth/*`: 10 requests/min, burst 5 (in memory; fine for a single API instance). |
| CORS reflects any origin | Allow-list from `CORS_ORIGINS` (default `http://localhost:5173`). |
| Env var values stored and returned in plain text | AES-256-GCM with `ENV_ENCRYPTION_KEY` (32 bytes, base64), stored as `enc:v1:<base64>`. Readers accept both forms during the backfill, and `go run ./cmd/migrate encrypt-env` encrypts existing rows. Lists return masked values; `?reveal=true` returns plain text to the owner. |
| No ownership checks on most routes | Every route checks that the caller owns the project/service/deployment. Foreign or missing IDs return 404 with today's error strings. |
| Build reads and writes files inside an untrusted cloned repo (symlinks can point anywhere on the worker host) | All access to the clone goes through `os.OpenRoot`; a generated Dockerfile is created with `O_EXCL` and never written through an existing entry. |
| Build runs `git` with a user-supplied repo URL, branch and commit (option injection such as `--upload-pack`; `file://`, `ext::` and ssh reach the worker's own files, keys and credential helpers); repo URLs may embed access tokens | The API and the worker both validate with `internal/gitsrc`: the repo URL is `https://` with a host, no whitespace and no leading `-`; the branch and commit are ref-safe names with no leading `-` and no `..`. git runs with `-c protocol.allow=never -c protocol.https.allow=always -c credential.helper=`, and `--` / `--end-of-options` comes before positionals (clone: `--`; fetch: `--end-of-options`; revision switch: `git switch --detach --end-of-options <ref>`, which fails the build if the ref does not resolve; for a hex commit `rev-parse HEAD` must match it). The repo URL is printable ASCII only. Each git step has its own timeout. The worker needs git >= 2.30. Repo URL userinfo never appears in logs, Temporal inputs or results, or error text; API responses mask it. Proper private-repo credentials come with GitHub integration (roadmap Phase 3). |

## 6. Correctness changes

1. **Redeploys no longer leak containers:** `RetirePrevious` and `stoppedAt`.
2. **Managed DB redeploys no longer corrupt data:** `StopPrevious` before start.
3. **Managed data survives reboots:** named volumes instead of `/tmp/cp-volumes`. A one-time move copies existing Node-era data into `cp-vol-<serviceId>` on first deploy: from the deployment's `volumePath` if it lies inside `LEGACY_VOLUME_ROOT`, otherwise from `LEGACY_VOLUME_ROOT/<serviceId>`, if the directory exists. This applies to Redis only. Node-era Mongo data was never in that directory (see §5) and is moved by the cutover runbook.
4. **Traffic switches only after the app is up:** `WaitHealthy` before `SwitchRoute`.
5. **Services can be deleted:** `DELETE /services/:id?purgeData=true|false` plus `TeardownWorkflow`.
6. **Log streams survive worker restarts:** on startup, re-attach to every running deployment that has a `containerId` and no `stoppedAt`.
7. **No duplicate ClickHouse rows (best effort):** a per-deployment flush cursor in `logs:flushed:<deploymentId>`, read with `XRANGE (<lastId> +`. Every flush pass refreshes the cursor's TTL (15 min) while the stream exists, so a ClickHouse outage never rewinds it. A crash between the ClickHouse insert and the cursor update can duplicate at most one batch; this is accepted and readers tolerate it. Exactly one flusher runs (the single Go worker's flush ticker). (CTO 2026-10-09)
8. **No port-allocation race:** app containers publish no ports; the dev-only managed DB ports keep the existing allocator.

## 7. Data model (goose)

- `00001_baseline.sql`: today's schema, checked against Prisma with `pg_dump` (unchanged from the old plan).
- `00002_deployment_stopped_at.sql` (Plan 1): `Deployment.stoppedAt TIMESTAMP(3) NULL`. Additive only, so the Node API, still running in Plan 1, ignores it.
- `00003_api.sql` (Plan 2, after Node is retired from the API):
  - `Service.deletedAt TIMESTAMP(3) NULL`;
  - table `RefreshToken` (`id`, `userId` FK, `tokenHash` unique, `expiresAt`, `revokedAt`, `createdAt`, index on `userId`);
  - index `Deployment(serviceId, stoppedAt)`.

Enum values are not changed, so Prisma could still read every row while rollback is possible.

## 8. Observability and delivery

- **Logs:** `slog`, JSON when `LOG_FORMAT=json` (production), text in dev. Every API log line carries the `X-Request-ID` (generated if missing and echoed in the response).
- **Metrics:** Prometheus on `METRICS_ADDR`, separate from the public port; default `127.0.0.1:9090` for the API and `127.0.0.1:9091` for the worker.
  - API: requests by route pattern, status and duration.
  - Worker: workflow outcomes, build duration, deploy duration, outbox rows by status (including `dead`), and outbox lag (`cp_outbox_oldest_pending_age_seconds`, the age of the oldest `pending`/`publishing` row). Temporal SDK metrics (a `client.Options.MetricsHandler` on the worker's registry) are added in Plan 3 with the production compose. (CTO 2026-10-09)
- **CI (first task of Plan 1):** `.github/workflows/ci.yml` runs:
  - Go: `go vet`, `golangci-lint`, `go test -race ./...` (testcontainers run on GitHub's Ubuntu runners), `govulncheck ./...`, `gosec ./...` (only G204 excluded; a CI step forbids `sh`/`bash` invocation in non-test code; any other suppression is a per-line `#nosec` with a reason);
  - secrets: `gitleaks` on every push;
  - `control-plane/` (frozen Node) is excluded;
  - UI: `npm ci`, `npm run lint`, `npm run build`.
- **Packaging (Plan 3):**
  - `cmd/api/Dockerfile`: distroless.
  - `cmd/worker/Dockerfile`: alpine with `docker-cli`, `docker-cli-buildx` and `git`. It mounts `/var/run/docker.sock`; that this mount amounts to root on the host is documented.
  - `docker-compose.prod.yml`: api, worker, Temporal on Postgres, and the Temporal UI with no public port.

## 9. Rollout and rollback

1. **Plan 1 cutover (worker):**
   1. Wait until `cp.build.q` and `cp.deploy.q` are empty.
   2. Stop the Node worker, which also stops its outbox publisher.
   3. Start the Go worker. The Node API keeps running.
2. **Rollback window (2 weeks from the Plan 1 cutover):**
   - Rollback means: stop the Go worker, start the Node worker. RabbitMQ stays up.
   - Containers started by Go stay on their project networks, and nginx is still attached, so routes keep working.
   - Plan 2 is built during this window.
3. **Plan 2 cutover (API) is the point of no return for Node.**
   1. `pg_dump` backup.
   2. Run `migrate up` (00003) and `migrate encrypt-env`.
   3. Switch the UI build to the new auth and stream flow.
   4. From here, rollback means the previous Go release, not Node.
4. **Plan 3:**
   1. `git tag node-final`.
   2. Move `docker-compose.yml` and `infra/` from `control-plane/` into `control-plane-go/` (with `mv`, so untracked nginx route files move too). Compose `name: control-plane` keeps the existing named volumes.
   3. Remove the `rabbitmq` and `minio` services and their env vars.
   4. Add `control-plane/FROZEN.md`: "Reference copy of the original Node backend, frozen at tag `node-final`. Not maintained. Needs RabbitMQ and the old docker-compose (`git show node-final:control-plane/docker-compose.yml`) to run." **No Node files are deleted.**

## 10. The three plans

| Plan | File | Contents | Effort |
|---|---|---|---|
| 1 — Foundation + Temporal worker | `docs/superpowers/plans/2026-10-07-go-plan1-worker.md` | CI; config/db/goose (00001, 00002); store subset for the worker; logs (Redis, ClickHouse, cursor); runtime (Docker `Runtime` with networks, volumes, limits and probes; nginx `Router`; buildx `Builder`; streamer with resume); `secrets` package (encrypts new values such as generated DB passwords; reads both plain and `enc:v1`; while the Node API is still running it shows these few values as ciphertext, which is acceptable); workflows and activities; outbox starter; worker binary; Temporal in dev compose; managed DB generated credentials; cutover | 2–2.5 weeks |
| 2 — API | `docs/superpowers/plans/2026-10-07-go-plan2-api.md` | Rest of the store; goose 00003; auth (exp, refresh, logout, Node-token handling); every route with ownership; teardown endpoint; encrypted env and `encrypt-env`; stream tickets and SSE; rate limit, CORS allow-list, dev-login gating; request IDs and metrics; UI: one shared `src/lib/api.ts` client (replacing the six copied `apiFetch` helpers) with refresh-on-401, `useLogs` with tickets, managed DB connection panel showing `svc-<id>` and the password, delete-service action; `compare-api.sh`; cutover | 2–2.5 weeks |
| 3 — Hardening and cleanup | `docs/superpowers/plans/2026-10-07-go-plan3-cutover.md` | Dockerfiles and prod compose (Temporal on Postgres); remove RabbitMQ and MinIO; tag `node-final`; move compose and `infra/` into `control-plane-go/`; add `FROZEN.md` (Node code kept); update `.claude/CLAUDE.md` and `README.md`; mark roadmap Phase 2 done in `SAAS_ROADMAP.md` | 3–4 days |

Old-plan tasks reused unchanged:
- 2 (config): extended with the new variables;
- 3 (db and baseline);
- 4 (testutil): without the AMQP fakes;
- 5–9 (store);
- 13–14 (Redis, ClickHouse);
- 16 (aggregator): without the cold tier;
- 17–19 (exec, docker, ports, nginx), extended;
- 20 (streamer);
- 21–22 (Dockerfile generation; the builder moves to buildx);
- 26–33 (API), extended.

Dropped: 10–12 (RabbitMQ), 15 (S3), 23–25 (AMQP consumers and the old worker main), 36 (deleting Node and renaming the folder).

## 11. Testing

- **Unit:** pure logic (Dockerfile generation, layout, backoff, query building, masking, AES-GCM round trip, token rotation).
- **Integration (testcontainers):** store queries, the outbox claim race, Redis cursor and tickets, ClickHouse round trip.
- **Workflows:** `go.temporal.io/sdk/testsuite` with mocked activities, covering:
  - a built service: happy path;
  - managed DB: stop-before-start ordering;
  - health check fails: the previous deployment keeps serving and the new container is removed;
  - non-retryable errors;
  - idempotent re-run;
  - teardown with and without `purgeData`.
- **Activities:** `FakeRunner` asserts the exact `docker` commands (network create/connect, `--memory`/`--cpus`/`--pids-limit`, volume names, `--requirepass`, buildx flags).
- **End-to-end:** one integration test with `testsuite.StartDevServer`: outbox row → workflow → activities against `FakeRunner`. Each plan's cutover also has a manual checklist against real Docker.
- Everything runs with `-race` in CI.

## 12. Risks

| Risk | Mitigation |
|---|---|
| The team is new to Temporal | Worker-first order surfaces problems in week 1; workflow tests stay deterministic through the test suite. |
| The Docker socket in the worker is root-equivalent | Documented. BuildKit isolation reduces exposure from builds. The real fix (separate build and run hosts) belongs to roadmap Phase 6. |
| Existing managed DB volumes in `/tmp` and Mongo's hardcoded password | Redis data copied once on first Go deploy; Mongo dumped and restored by the cutover runbook; the old container's anonymous volume is kept as a fallback. |
| Forced re-login when Node tokens are rejected | Expected once, at the Plan 2 cutover; announce it. |
| Single API instance assumptions (in-memory rate limit, in-process log streams) | Acceptable for a single VM; listed for Phase 6 (Redis-backed limiter, streaming per host task queue). |

## 13. Execution: an engineering team of subagents

The plans are executed by an orchestrated team. One engineer writes code; independent reviewers check every task in parallel; the CTO arbitrates. Reviewers never edit code, so there is only ever one writer and no conflicting edits.

### Roles

| Role | Runs | Does | Writes code? |
|---|---|---|---|
| **Orchestrator** (main session) | Always | Dispatches agents, triages findings, decides when a task is done, keeps the records. | No |
| **Engineer** | Every task, and every fix round | Implements the task exactly as planned (test first), commits, and fixes accepted findings in follow-up commits. Starts fresh for each task and each fix round. | **Yes, the only role that does** |
| **Security reviewer** | Every task | Reviews the diff for vulnerabilities and runs `govulncheck ./...`, `gosec ./...`, `gitleaks detect --log-opts=<sha>^!` and, if UI dependencies changed, `npm audit --omit=dev`. Checks the §5 rules: no published ports, limits set, ownership checked, secrets encrypted and never logged, no injection (SQL, shell arguments, nginx config), auth on every route. | No |
| **Staff engineer** | Every task | Reviews correctness, concurrency and races, error handling, test quality (do the tests prove the behavior?), consistency with existing packages, simplicity, and match with the plan and spec. | No |
| **Verifier** | Every task | Runs the full suite, uses the feature for real, and checks earlier features for regressions (details below). | No (throwaway checks only, outside the repo) |
| **Product reviewer** | Tasks that change API responses, UI or user-visible behavior | Checks behavior from a user's point of view: response shapes the UI depends on, error messages, UI flows (runs the UI when it changed), behavior changes users will notice (forced re-login, masked env vars, delete flow), and whether the change needs a note to users. | No |
| **CTO** | End of each plan; whenever reviewers disagree; whenever a task or finding would change the architecture or depart from this spec | Decides: fix now, defer to the backlog, or change the spec. Records each decision. Gives the plan-level go/no-go before a cutover. | No |

### Flow for each task

1. **Engineer** implements the task and commits.
2. **Parallel review of that commit:** Security, Staff and Verifier, plus Product if the task changes anything a user can see. All start fresh and get only the task text, the commit SHA, this spec and their role brief.
3. **Every finding uses one format:** severity (critical / high / medium / low), `file:line`, the problem, the evidence (command output or a code quote), and a suggested fix.
4. **The orchestrator triages:**
   - A Verifier FAIL, or any critical or high finding: must be fixed.
   - Medium: fixed, unless the CTO defers it.
   - Low: appended to `docs/superpowers/review/backlog.md`.
   - Reviewers contradicting each other, or a finding that would change the spec: goes to the **CTO**. The decision is appended to `docs/superpowers/review/decisions.md` (date, context, decision, why).
5. **Fix round:** a fresh Engineer gets the accepted findings and fixes them in follow-up commits. Then the reviewers who raised blocking findings re-check, and the **Verifier always re-runs**.
6. **Limit:** after 3 fix rounds without a clean result, the orchestrator stops and asks the user.
7. **The task is done when** the Verifier passes and there are no open critical, high or medium findings. Only then does the next task start.

### Plan-end gate (before each cutover)

1. **Security** scans the whole plan's diff (not per task).
2. **Staff** reviews the whole plan's diff for cross-task consistency.
3. **Product** walks through the cutover checklist in the running UI.
4. **CTO** gives go/no-go.
5. **The user approves the cutover itself.** Cutovers stop and start the running system, so they are never automatic.

### What the Verifier does

1. **Full suite:**
   - in `control-plane-go`: `go build ./... && go vet ./... && golangci-lint run && go test -race ./...`;
   - `cd cp-ui && npm run lint && npm run build` when `cp-ui/` changed.
2. **Real use of the feature:**
   - Store/API tasks: start the API against the dev database and `curl` the new endpoints, including the error and ownership cases.
   - Runtime/workflow tasks: run the worker against local Docker and Temporal dev, trigger the path (for example, deploy a sample repo), and inspect the containers, networks, nginx route and Temporal history.
   - Library-only tasks: run a throwaway check outside the repo.
3. **Regressions:** re-run the cutover checklist items that already apply.
4. **Report** PASS or FAIL with the exact commands and quoted output.

### Records and briefs

- **Per-task record:** `docs/superpowers/review/<plan>/task-<n>.md`, holding every reviewer report, the triage, the fix commits and the final Verifier result.
- **Role briefs:** `docs/superpowers/agents/{engineer,security,staff,product,cto,verifier}.md`. Each holds that role's instructions and checklist, and is passed verbatim in every dispatch so reviews stay consistent. They are written together with the plans.
- **Agent types:** Engineer is `general-purpose`, Staff is `superpowers:code-reviewer`, and the others are `general-purpose` with their role brief. The flow builds on the `superpowers:subagent-driven-development` skill.
- **Expected cost:** about 3–4 agent runs per task, plus fix rounds and the plan-end gates.

## 14. Prerequisites and credentials

No paid third-party service or API key is needed to execute the three plans. Everything runs locally or in the existing GitHub repo.

| Need | Status (checked 2026-10-07) | Action |
|---|---|---|
| Go 1.24+ | Not installed | `brew install go` (Plan 1, Task 1) |
| git >= 2.30 (`switch --detach --end-of-options`, `fetch --end-of-options`) | Apple git 2.39.5 here; worker image alpine/Debian ships >= 2.39 | Task 26 logs `git --version` at startup and refuses to start below 2.30 |
| golangci-lint, gosec, gitleaks | Not installed | `brew install golangci-lint gosec gitleaks` |
| govulncheck | Not installed | `go install golang.org/x/vuln/cmd/govulncheck@latest` |
| Docker Desktop running | Installed, daemon not running | Start Docker Desktop before executing (testcontainers, builds, deploys) |
| GitHub repo + Actions | `origin` = `github.com/manwindersingh1712/infra_pilot`, `gh` logged in | None; CI uses the built-in `GITHUB_TOKEN` |
| Temporal | Self-hosted dev server in docker-compose | None (no Temporal Cloud account) |
| Postgres, Redis, ClickHouse, registry, nginx | Existing docker-compose | None |
| `JWT_SECRET`, `ENV_ENCRYPTION_KEY` | Generated locally (`openssl rand -base64 32`) | None |

Optional:
- **Docker Hub account and access token.** Anonymous image pulls are rate-limited (about 100 per 6 hours per IP). Many test runs with testcontainers, or CI, can hit that limit. With a token, add `DOCKERHUB_USERNAME` / `DOCKERHUB_TOKEN` as GitHub Actions secrets and run `docker login` locally.

Not needed for this migration, but needed later for roadmap Phase 1 (production):
- a VM (e.g. Hetzner) with SSH access;
- a domain and DNS (wildcard `*.yourdomain` for service subdomains);
- an email provider if password reset is added.

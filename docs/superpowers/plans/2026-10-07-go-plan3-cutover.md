# Plan 3 — Hardening, Retiring RabbitMQ/MinIO and Freezing Node

> **For agentic workers:** REQUIRED: execute this plan with the team orchestration in spec §13 (`docs/superpowers/specs/2026-10-07-go-temporal-design.md`). The Engineer follows `docs/superpowers/agents/engineer.md`. After every task, Security, Staff and Verifier review in parallel, and Product reviews Task 4 (docs). Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Go backend deployable on one VM. Then remove the infrastructure only Node needed, freeze the Node code without deleting it, and update the docs.

**Architecture:**
- Two images: `api` (distroless, non-root) and `worker` (alpine with the docker CLI, buildx and git, talking to the host daemon through the Docker socket).
- `docker-compose.prod.yml` adds Temporal on the existing Postgres, the Temporal UI (localhost only), the API and the worker.
- `docker-compose.yml` and `infra/` move into `control-plane-go/`, with `name: control-plane`, so the existing named volumes are reused.

**Tech Stack:** Docker, Docker Compose v2.24+ (for `!reset`), `temporalio/auto-setup`, `temporalio/ui`, `gcr.io/distroless/static-debian12`.

**Prerequisites:**
- Plan 2's cutover is done: the Go API and worker serve everything, and the Node API is stopped.
- The 2-week rollback window from Plan 1 has closed (check `docs/superpowers/review/decisions.md`).

**Never delete or modify Node source files** under `control-plane/apps`, `control-plane/packages` or `control-plane/prisma` (user decision, spec D4).

---

### Task 1: Container images for the API and the worker

**Files:**
- Create: `control-plane-go/cmd/api/Dockerfile`, `control-plane-go/cmd/worker/Dockerfile`, `control-plane-go/.dockerignore`

- [ ] **Step 1: Write the files**

`control-plane-go/.dockerignore`:
```
.env
.env.*
bin/
infra/
**/*_test.go
```

`control-plane-go/cmd/api/Dockerfile`:
```dockerfile
# syntax=docker/dockerfile:1
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/api /out/migrate /usr/local/bin/
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
```

`control-plane-go/cmd/worker/Dockerfile`:
```dockerfile
# syntax=docker/dockerfile:1
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker

# The worker drives the host's Docker through /var/run/docker.sock (mounted in
# compose). That access is root-equivalent on the host: see spec §12.
FROM alpine:3.20
RUN apk add --no-cache docker-cli docker-cli-buildx git ca-certificates
COPY --from=build /out/worker /usr/local/bin/worker
ENTRYPOINT ["/usr/local/bin/worker"]
```

- [ ] **Step 2: Build both images**

```bash
cd control-plane-go
docker build -f cmd/api/Dockerfile -t infrapilot/api:dev .
docker build -f cmd/worker/Dockerfile -t infrapilot/worker:dev .
docker run --rm infrapilot/api:dev 2>&1 | head -1
docker run --rm --entrypoint sh infrapilot/worker:dev -c 'docker --version && docker buildx version && git --version'
docker image inspect infrapilot/api:dev -f '{{.Config.User}}'
```
Expected:
- both builds succeed;
- the API exits with `config: missing required env: DATABASE_URL`;
- the worker image prints three version lines;
- the user is `nonroot:nonroot`.

- [ ] **Step 3: Commit**

```bash
git add .dockerignore cmd/api/Dockerfile cmd/worker/Dockerfile
git commit -m "build(go): container images for api and worker"
```

- [ ] **Step 4: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

### Task 2: Move the infrastructure, remove RabbitMQ and MinIO, freeze Node

**Files:**
- Move: `control-plane/docker-compose.yml`, `control-plane/infra/` → `control-plane-go/`
- Modify: `control-plane-go/docker-compose.yml`, `control-plane-go/.env` (untracked), `control-plane-go/.gitignore`
- Create: `control-plane/FROZEN.md`

- [ ] **Step 1: Tag the last commit where the Node backend is runnable**

```bash
cd /Users/manwindersingh/Desktop/infra-pilot
git status --short   # only the known untracked files may appear
git tag -a node-final -m "Last commit with the runnable Node backend (RabbitMQ + MinIO compose)"
```
Ask the user before pushing the tag (`git push origin node-final`).

- [ ] **Step 2: Move the compose file and nginx config**

```bash
cd control-plane && docker compose down && cd ..
mv control-plane/docker-compose.yml control-plane/infra control-plane-go/
ls control-plane
```
Use `mv`, not `git mv`, so the untracked nginx route files under `infra/nginx/conf.d/routes/` move too and existing routes keep working. Named volumes are kept by `down`.

Expected `ls` output: only Node files (`apps`, `packages`, `prisma`, `node_modules`, `package.json`, `package-lock.json`, `tsconfig.json`, `nodemon.json`, `prisma.config.ts`, `.env`, `.gitignore`). **Nothing in `control-plane/` is deleted.**

- [ ] **Step 3: Edit `control-plane-go/docker-compose.yml`**

1. Add as the first line: `name: control-plane`. Compose names volumes after the project, so this keeps `control-plane_cp_pgdata` and the other volumes.
2. Delete the whole `rabbitmq:` service.
3. Delete the whole `minio:` service, and `minio_data:` under `volumes:`.

Then:
```bash
cd control-plane-go
docker compose config --services
docker compose up -d && docker compose ps --format '{{.Name}} {{.State}}'
docker volume ls --format '{{.Name}}' | grep control-plane_
```
Expected:
- the services are `postgres registry nginx redis clickhouse temporal`, with no `rabbitmq` or `minio`;
- all of them are running;
- the volume list still shows `control-plane_cp_pgdata`, so the existing data is back.

- [ ] **Step 4: Clean up the env and gitignore**

In `control-plane-go/.env`, delete the lines starting with `NGINX_ROUTES_DIR=`, `AMQP_URL=`, `S3_ENDPOINT=`, `AWS_ACCESS_KEY_ID=`, `AWS_SECRET_ACCESS_KEY=`, `LOGS_BUCKET=` and `PRISMA_CLIENT_ENGINE_TYPE=`. The default routes dir (`infra/nginx/conf.d/routes`) is now correct.

Append to `control-plane-go/.gitignore`:
```
infra/nginx/conf.d/routes/*.conf
```

- [ ] **Step 5: Mark the Node backend as frozen**

`control-plane/FROZEN.md`:
```markdown
# Frozen: original Node.js backend

This folder is a reference copy of the original Node/TypeScript control plane.
It is **not maintained and not run**. The live backend is `../control-plane-go/`.

- Frozen at git tag `node-final`.
- It needs RabbitMQ and MinIO, which the current docker-compose no longer has. To run it for
  comparison, check out `node-final` (`git show node-final:control-plane/docker-compose.yml`).
- Do not run `npm run prisma:migrate` against the live database: goose
  (`control-plane-go/migrations`) owns the schema now.
```

- [ ] **Step 6: Verify end to end**

From `control-plane-go`, start `go run ./cmd/api` and `go run ./cmd/worker`, and the UI. Then:
1. Log in, deploy a service and a Redis instance, and check logs and env vars.
2. Check that existing apps still answer at `http://<serviceId>.localhost/`.
3. Run `git status --short control-plane`. The only changes are the moved files (shown as deleted there, added under `control-plane-go`) and the new `FROZEN.md`.

- [ ] **Step 7: Commit**

```bash
cd /Users/manwindersingh/Desktop/infra-pilot
git add -A control-plane/docker-compose.yml control-plane/infra control-plane/FROZEN.md control-plane-go/docker-compose.yml control-plane-go/infra control-plane-go/.gitignore
git commit -m "chore: move infrastructure to control-plane-go, drop RabbitMQ/MinIO, freeze Node backend"
```

- [ ] **Step 8: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

### Task 3: Production compose (Temporal on Postgres)

**Files:**
- Create: `control-plane-go/docker-compose.prod.yml`, `control-plane-go/.env.production.example`

- [ ] **Step 1: Write the files**

`control-plane-go/docker-compose.prod.yml` (used together with the base file):
```yaml
# docker compose -f docker-compose.yml -f docker-compose.prod.yml --env-file .env.production up -d --build
services:
  temporal:
    image: temporalio/auto-setup:latest
    user: "1000:1000"
    command: ["autosetup"]
    environment:
      DB: postgres12
      DB_PORT: "5432"
      POSTGRES_SEEDS: postgres
      POSTGRES_USER: ${POSTGRES_USER}
      POSTGRES_PWD: ${POSTGRES_PASSWORD}
    depends_on: [postgres]
    ports: !reset []
    volumes: !reset []

  temporal-ui:
    image: temporalio/ui:latest
    environment:
      TEMPORAL_ADDRESS: temporal:7233
    ports:
      - "127.0.0.1:8233:8080" # reach it through an SSH tunnel; never expose publicly
    depends_on: [temporal]

  api:
    build: { context: ., dockerfile: cmd/api/Dockerfile }
    env_file: .env.production
    environment:
      APP_ENV: production
      LOG_FORMAT: json
      API_METRICS_ADDR: 0.0.0.0:9090 # only reachable inside the compose network
      DATABASE_URL: postgresql://${POSTGRES_USER}:${POSTGRES_PASSWORD}@postgres:5432/control_plane
      REDIS_URL: redis://redis:6379
      CLICKHOUSE_URL: http://clickhouse:8123
    ports:
      - "8080:8080"
    depends_on: [postgres, redis, clickhouse]
    restart: unless-stopped

  worker:
    build: { context: ., dockerfile: cmd/worker/Dockerfile }
    env_file: .env.production
    environment:
      APP_ENV: production
      LOG_FORMAT: json
      WORKER_METRICS_ADDR: 0.0.0.0:9091
      DATABASE_URL: postgresql://${POSTGRES_USER}:${POSTGRES_PASSWORD}@postgres:5432/control_plane
      REDIS_URL: redis://redis:6379
      CLICKHOUSE_URL: http://clickhouse:8123
      TEMPORAL_HOST_PORT: temporal:7233
      NGINX_ROUTES_DIR: /routes
      EXPOSE_MANAGED_PORTS: "false"
    volumes:
      # Root-equivalent access to the host's Docker (spec §12, roadmap Phase 6 fixes this).
      - /var/run/docker.sock:/var/run/docker.sock
      - ./infra/nginx/conf.d/routes:/routes
    depends_on: [postgres, redis, clickhouse, temporal]
    restart: unless-stopped
```

`control-plane-go/.env.production.example`:
```
# Copy to .env.production (never commit it) and replace every value.
POSTGRES_USER=cp
POSTGRES_PASSWORD=change-me
JWT_SECRET=generate-with-openssl-rand-base64-32
ENV_ENCRYPTION_KEY=generate-with-openssl-rand-base64-32
CORS_ORIGINS=https://app.example.com
SERVICE_BASE_DOMAIN=example.com
REGISTRY_HOST=localhost:5001
```

Add `!.env.production.example` after the `.env` line in `control-plane-go/.gitignore`, so the example file is committed while real env files stay ignored.

Also add to `docs/superpowers/review/backlog.md`:
- "pin `temporalio/*` image tags to digests" (low);
- "Temporal SDK metrics via tally/prometheus" (low, spec §8);
- "Postgres credentials in the base compose are dev defaults; production must override them" (medium, roadmap Phase 1).

- [ ] **Step 2: Validate and smoke-test**

```bash
cd control-plane-go
cp .env.production.example .env.production
sed -i '' "s|^JWT_SECRET=.*|JWT_SECRET=$(openssl rand -base64 32)|; s|^ENV_ENCRYPTION_KEY=.*|ENV_ENCRYPTION_KEY=$(grep '^ENV_ENCRYPTION_KEY=' .env | cut -d= -f2-)|; s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=cp|; s|^CORS_ORIGINS=.*|CORS_ORIGINS=http://localhost:5173|; s|^SERVICE_BASE_DOMAIN=.*|SERVICE_BASE_DOMAIN=localhost|" .env.production
docker compose -f docker-compose.yml -f docker-compose.prod.yml --env-file .env.production config >/dev/null && echo CONFIG_OK
docker compose -f docker-compose.yml -f docker-compose.prod.yml --env-file .env.production up -d --build
sleep 20
curl -s localhost:8080/readyz
docker compose -f docker-compose.yml -f docker-compose.prod.yml logs worker | grep -m1 'worker up'
```
Expected: `CONFIG_OK`, `{"ok":true}`, and a JSON log line containing `"msg":"worker up"`.

Deploy a service through the UI against this stack and check it reaches `deployed`. Then bring the stack down, `rm .env.production`, and return to the dev setup with `docker compose up -d`.

- [ ] **Step 3: Commit**

```bash
git add docker-compose.prod.yml .env.production.example .gitignore ../docs/superpowers/review/backlog.md
git commit -m "build: production compose with Temporal on Postgres"
```

- [ ] **Step 4: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Security, Staff and Verifier.

---

### Task 4: Update the docs

**Files:**
- Modify: `.claude/CLAUDE.md`, `README.md`, `SAAS_ROADMAP.md`

- [ ] **Step 1: Rewrite `.claude/CLAUDE.md` for the Go backend**

Keep the file's header line and the UI, Nginx Routing and Infrastructure Ports sections. Replace the rest so it says:
- **Project Overview:**
  - Go backend in `control-plane-go/` (module `infrapilot`): `cmd/api`, `cmd/worker`, `cmd/migrate`.
  - Temporal for deployments; Postgres with goose migrations; Redis for hot logs, pub/sub and stream tickets; ClickHouse for logs; Docker; nginx.
  - `control-plane/` is the frozen Node reference (`FROZEN.md`; never edit or run it).
- **Commands** (all from `control-plane-go/`):
  - `docker compose up -d`, `go run ./cmd/migrate up`, `go run ./cmd/api`, `go run ./cmd/worker`;
  - `go test ./...` (needs Docker), `go test -short ./...`, `go test ./internal/store/ -run TestName`;
  - `golangci-lint run`;
  - UI: `cd cp-ui && npm run dev | lint | build`;
  - prod: `docker compose -f docker-compose.yml -f docker-compose.prod.yml --env-file .env.production up -d --build`.
- **Architecture:**
  - deploy flow: API → `Deployment` + `OutboxEvent` in one transaction → the worker's outbox starter → `DeployWorkflow` (ID `deploy-<deploymentId>`) → activities;
  - delete flow: `TeardownWorkflow`;
  - per-project networks `cp-proj-<projectId>`, alias `svc-<serviceId>`, container `cp-<deploymentId>`, managed volume `cp-vol-<serviceId>`;
  - limits on every container; no public ports;
  - health check before the route switch; old containers retired.
- **Conventions:** only `store` writes SQL (quoted Prisma names; `updatedAt` set manually); only `runtime` shells out; workflow code is deterministic; env values are encrypted (`enc:v1:`) and masked in the API.
- **Auth:** 15-minute JWT plus rotating refresh tokens; `/dev/login` in development only; SSE uses one-time tickets.
- **API endpoints:** the current list, including `/auth/refresh`, `/auth/logout`, `DELETE /services/:id`, `/logs/deployments/:id`, `/logs/deployments/:id/stream-ticket`, `/logs/deployments/:id/stream?ticket=`, and the canvas routes.
- **Execution process:** plans in `docs/superpowers/plans/`, the team orchestration in spec §13, and role briefs in `docs/superpowers/agents/`.

- [ ] **Step 2: Update `README.md`**

- Control Plane API is "Go (net/http) API".
- Database is "PostgreSQL (pgx, goose migrations)".
- Replace "Message Queue (RabbitMQ)" with "Workflow engine (Temporal)".
- Remove the MinIO line.
- Prerequisites: Go 1.24+, Node.js 20+ (UI only), Docker.
- Setup, migrate and start commands match `CLAUDE.md`.
- The ports list drops RabbitMQ and MinIO and adds Temporal (`7233`, UI `8233`).
- The "Idempotent workers" bullet says "Temporal workflow IDs + conditional status updates".

- [ ] **Step 3: Update `SAAS_ROADMAP.md`**

1. Tick every Phase 2 item, except "Prefix container names with user ID". Per-project networks and labels replace that item; note this next to it.
2. Tick Phase 4.3 "Randomize default passwords".
3. Set the Phase 2 status row to "Done (Go migration)".

- [ ] **Step 4: Check for stale references**

Run: `grep -nE "RabbitMQ|rabbitmq|MinIO|Socket\.io|Fastify|Prisma ORM|npm run (dev:api|dev:worker|prisma)" README.md .claude/CLAUDE.md`
Expected: matches only in sentences describing the frozen Node backend.

- [ ] **Step 5: Commit**

```bash
git add .claude/CLAUDE.md README.md SAAS_ROADMAP.md
git commit -m "docs: describe the Go + Temporal backend and the frozen Node reference"
```

- [ ] **Step 6: Review gate.** Stop. The orchestrator runs the spec §13 reviews: Staff, Verifier and **Product**.

---

### Task 5: Final gate

- [ ] **Step 1: Migration-wide reviews**

Run these on `master..go-migration`:
- **Security:** full scan and review, including `govulncheck`, `gosec`, `gitleaks` and `npm audit`.
- **Staff:** full review.
- **Product:** a full walkthrough of every user flow.
- **CTO:** final GO, plus a check against the spec §1 done criteria. Every criterion is met, or is an explicit backlog item with a roadmap phase.

- [ ] **Step 2: Hand off to the user**

Report:
- the decisions log;
- the open backlog;
- that CI is green on `go-migration`;
- the recommended next step: open a PR to `master` (with the user's approval, using the PR attribution line).

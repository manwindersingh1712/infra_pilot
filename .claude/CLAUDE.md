# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

> **Go + Temporal migration in progress:** start with `docs/superpowers/START-HERE.md`. It links the design spec, the three plans, the agent role briefs, the review logs and the progress tracker. Never delete code in `control-plane/` (Node is kept as a frozen reference).

## Project Overview

Infra Pilot is a minimal control-plane + UI for managing projects, services, and deployments. It uses:
- **Postgres** for state persistence
- **RabbitMQ** for async job queues
- **Docker** for building images and running containers
- **Nginx** for subdomain-based service routing
- **Redis** for Socket.io adapter, pub/sub, and hot log storage
- **MinIO/S3** for log archival
- **ClickHouse** for structured log aggregation

## Repository Structure

```
control-plane/          # Node.js backend (npm workspaces, no actual workspace config)
├── apps/
│   ├── api/            # Fastify API (port 8080)
│   └── worker/         # Background job processor
├── packages/
│   └── shared/         # Prisma, AMQP, event topology, MQ constants
├── prisma/
│   └── schema.prisma   # Database schema
├── infra/nginx/        # Nginx config for reverse proxy
└── docker-compose.yml  # Infrastructure services

cp-ui/                  # React 19 + Vite frontend (port 5173)
```

## Development Commands

### Initial Setup

```bash
# Start infrastructure dependencies
cd control-plane && docker compose up -d

# Install dependencies
cd control-plane && npm install
cd ../cp-ui && npm install

# Run database migrations
cd control-plane-go && go run ./cmd/migrate up   # goose owns the schema now; never `npm run prisma:migrate` (see Prisma Commands)
```

### Running the Application

```bash
# Terminal 1: API server
cd control-plane && npm run dev:api

# Terminal 2: Worker
cd control-plane && npm run dev:worker

# Terminal 3: UI
cd cp-ui && npm run dev
```

### UI Build / Lint / Typecheck

```bash
cd cp-ui
npm run lint      # eslint
npm run build     # tsc -b && vite build (this is also the typecheck)
```

There is no test suite in either package, and `control-plane` has no lint or typecheck script. To typecheck the backend, run `cd control-plane && npx tsc --noEmit`.

### Prisma Commands

```bash
cd control-plane
npm run prisma:migrate    # Run migrations in dev mode
npm run prisma:studio     # Open Prisma Studio GUI
```

> **Migration in progress:** goose (`control-plane-go/migrations`, `go run ./cmd/migrate`) now owns the schema. Do not run `npm run prisma:migrate`, `prisma migrate dev`, `prisma db push` or `prisma db pull` against the dev DB: Prisma's drift detection sees `goose_db_version` and `migrate dev` offers to reset (wipe) the database.

## TypeScript Configuration

The control-plane uses `NodeNext` module resolution with `@/*` path aliases mapped to the repo root:

```typescript
import { prisma } from "@/packages/shared/src/db.js";
import { MQ } from "@/packages/shared/src/mq.js";
```

**Important:** File extensions (`.js`) are required in imports even for `.ts` files due to `NodeNext` resolution.

## Architecture

### Data Flow (Deployment)

1. API receives `POST /services/:id/deploy` → creates `Deployment` row + `OutboxEvent` in the same transaction
2. Outbox publisher (runs in **worker**, not API) polls `OutboxEvent` table → claims via `FOR UPDATE SKIP LOCKED` → publishes to RabbitMQ exchange → marks as `published`
3. Build consumer receives message → clones repo → auto-generates Dockerfile (if needed) → builds Docker image → pushes to registry → creates deploy outbox event
4. Deploy consumer receives message → runs container → configures nginx route → starts log streaming

### Outbox Pattern

The outbox pattern ensures at-least-once message delivery:

- All state changes and events are written in the same database transaction
- The `OutboxEvent` table stores pending events with `status: pending`
- The outbox publisher polls every second, claims events via `FOR UPDATE SKIP LOCKED`, publishes to RabbitMQ, then marks as `published`
- Failed publishes retry with exponential backoff; after 10 attempts events become `dead`

### Service Types

- **docker** - Custom Dockerfile builds (repoUrl required)
- **nodejs** - Node.js apps with auto-generated Dockerfile (detects package.json scripts, node version, package manager)
- **nextjs** - Next.js apps with auto-generated Dockerfile
- **react** - React SPA apps with auto-generated Dockerfile (detects Vite vs CRA)
- **mongodb** - Managed MongoDB container (skips build entirely, uses volume persistence)
- **redis** - Managed Redis container (skips build entirely, uses volume persistence)

Managed services (`mongodb`, `redis`) bypass the build phase entirely. When deployed, the API creates a `DEPLOY_REQUESTED` outbox event directly instead of `BUILD_REQUESTED`.

### Idempotency and Retry Logic

Workers use atomic database updates to ensure exactly-once processing:

```typescript
// Only one worker wins this race
const claimed = await prisma.deployment.updateMany({
  where: { id: dep.id, status: "queued" },
  data: { status: "building" }
});
if (claimed.count !== 1) return; // Another worker got it
```

Failed messages are republished with incremented `x-retry-count` header; after 5 retries they go to DLQ (`cp.dlq.q`). On failure, the deployment status is reset to `queued` so the next retry can claim it.

### Deployment State Machine

```
queued → building → deploying → deployed
                          └→ failed
```

- `queued`: Initial state, waiting for build/deploy
- `building`: Build consumer claimed it, cloning/building/pushing
- `deploying`: Deploy consumer claimed it, starting container
- `deployed`: Container is running, nginx route configured
- `failed`: Build failed after max retries

### Log Aggregation Pipeline (3-Tier Storage)

1. **Hot (Redis)**: Build/deploy logs stream from Docker containers via `docker logs -f`. The worker publishes to Redis Streams (10-minute TTL) and Redis Pub/Sub for real-time broadcast.
2. **Warm (ClickHouse)**: A background job (`flush-logs-job.ts`, runs every 5 minutes) flushes Redis logs to ClickHouse for fast historical queries.
3. **Cold (S3/MinIO)**: Large log batches (>1000 entries) are also uploaded to S3 for archival.

Real-time logs reach the UI via two paths:
- **Socket.io** (via Redis adapter for horizontal scaling) → immediate broadcast to connected clients
- **HTTP API** (`GET /logs/:deploymentId`) → queries the appropriate tier automatically based on time range

### Nginx Routing

Services are accessible via `http://<serviceId>.localhost/` (requires `/etc/hosts` entry or local DNS). The worker dynamically creates nginx route configs at `/infra/nginx/conf.d/routes/` and reloads nginx.

The nginx config uses Docker's embedded DNS resolver (`127.0.0.11`) to resolve container names at request time, not just at reload time. This is critical because containers may be recreated with new IPs while the nginx config remains the same.

### Cross-App Imports

The worker occasionally imports from the API app (e.g., `log-aggregator.ts`). This is intentional — both apps share the same `tsconfig.json` base, and the `@/*` alias makes this possible:

```typescript
// worker importing API services
import { flushLogsToColdStorage } from "@/apps/api/src/services/log-aggregator.js";
```

### Auto-Generated Dockerfiles

For `nodejs`, `nextjs`, and `react` service types, if the cloned repo does not contain a `Dockerfile`, one is auto-generated based on `package.json` contents:

- Detects Node version from `engines.node`
- Detects start script (`npm start`, `npm run start:prod`, `npm run serve`, falls back to `node index.js`)
- Replaces `nodemon` with `node` in production
- For React apps, detects Vite (looks for `vite.config.js`) vs CRA to determine build output directory (`dist` vs `build`)
- React apps use a multi-stage build with `nginx:alpine` serving static files

## Go worker (control-plane-go)

Plan 1 of the Go + Temporal migration: the Go worker replaces the Node worker (the Node API still writes the outbox rows). Runbook: `docs/superpowers/plans/2026-10-07-go-plan1-worker.md`, Task 27; release note: `docs/superpowers/release-note-plan1.md`.

```bash
cd control-plane-go
go run ./cmd/migrate status|up|baseline   # goose; baseline only for a Node-era DB (needs _prisma_migrations)
go run ./cmd/worker                       # reads control-plane-go/.env (godotenv); Temporal UI http://localhost:8233
scripts/smoke-worker.sh                   # throwaway compose project smoke test
scripts/e2e-temporal.sh                   # real-Temporal e2e (kill-worker, cancel, queued pair, reject-duplicate)
```

Env vars the worker reads (`internal/config/config.go`; defaults in brackets):
- Required: `DATABASE_URL`, `ENV_ENCRYPTION_KEY` (base64, 32 bytes: `openssl rand -base64 32`; back it up, it decrypts generated managed-DB passwords).
- App/log: `APP_ENV` or `NODE_ENV` [production], `LOG_FORMAT` [text], `WORKER_METRICS_ADDR` [127.0.0.1:9091]. (API-only, Plan 2: `API_PORT`, `JWT_SECRET`, `CORS_ORIGINS`, `API_METRICS_ADDR`.)
- API behind nginx, `TRUSTED_PROXIES` [empty]: comma-separated IPs or CIDRs of the reverse proxies whose `X-Forwarded-For` the API honours (right-most untrusted entry is the client). Empty means every client is keyed by its TCP peer, so behind nginx all clients share one login/refresh/stream rate-limit bucket. List ONLY the nginx container's address (a `/32`, e.g. `172.18.0.5`), not the Docker subnet: any other container on a listed subnet could forge the header. Nginx must send `proxy_set_header X-Forwarded-For $remote_addr;` (Plan 2 Task 18). With `APP_ENV=development` the API binds `127.0.0.1` only (dev login is on); otherwise it binds all interfaces.
- Temporal: `TEMPORAL_HOST_PORT` [localhost:7233], `TEMPORAL_NAMESPACE` [default].
- Stores: `REDIS_URL` [redis://localhost:6379; the dev stack uses redis://localhost:6380], `CLICKHOUSE_URL` [http://localhost:8123], `CLICKHOUSE_USER` [default], `CLICKHOUSE_PASSWORD`.
- Build/registry: `REGISTRY_HOST` [localhost:5001], `BUILD_WORKDIR` [/tmp/cp-builds], `BUILDER_NAME` [cp-builder], `BUILDER_MEMORY` [2g], `BUILDER_CPUS` [2], `PROBE_IMAGE` [busybox:1.36].
- Containers: `CONTAINER_MEMORY` [512m], `CONTAINER_CPUS` [0.5], `CONTAINER_PIDS` [256], `DEFAULT_CONTAINER_PORT` [3080], `HEALTH_TIMEOUT_SECONDS` [60, max 600], `EXPOSE_MANAGED_PORTS` [true only when APP_ENV=development], `DATA_PLANE_PORT_RANGE_START/END` [20000/20100], `LEGACY_VOLUME_ROOT` [/tmp/cp-volumes].
- Nginx: `NGINX_ROUTES_DIR` [infra/nginx/conf.d/routes, relative: set an absolute path, e.g. `<repo>/control-plane/infra/nginx/conf.d/routes`], `NGINX_CONTAINER` [cp_nginx], `NGINX_PORT` [80], `SERVICE_BASE_DOMAIN` [localhost].

Never commit `ruvector.db`, `agentdb.rvf*`, `README_NEW.md` or any `.env`; stage by explicit path.

## Other Docs

`docs/` has deeper write-ups (`logging-service.md`, `INFRA-PILOT-NODEJS.md`, diagrams). `IMPLEMENTATION_PLAN.md` and the untracked `SAAS_ROADMAP.md` describe planned work, not current behavior.

## Environment Variables

Create `.env` files (not committed):

**control-plane/.env:**
```
API_PORT=8080
DATABASE_URL="postgresql://cp:cp@localhost:5432/control_plane?schema=public"
AMQP_URL="amqp://cp:cp@localhost:5672"
JWT_SECRET="dev-secret-change-me"
NODE_ENV="development"
REGISTRY_HOST=localhost:5001
BUILD_WORKDIR=/tmp/cp-builds
DEFAULT_CONTAINER_PORT=3080
NGINX_PORT=80
REDIS_URL=redis://localhost:6380
```

**cp-ui/.env:**
```
VITE_API_BASE=http://localhost:8080
```

## API Endpoints

### Auth
- `POST /dev/login` - Dev auth (returns JWT, auto-creates user)
- `POST /auth/register`, `POST /auth/login` - User auth with bcrypt passwords

### Projects
- `POST /projects` - Create project
- `GET /projects` - List projects

### Services
- `POST /services` - Create service (repoUrl required for docker/nodejs/nextjs/react)
- `GET /services?projectId=` - List services
- `POST /services/:id/deploy` - Trigger deployment (optional `commitSha`, defaults to `main` or `latest` for managed)
- `GET /services/:id/env` - List env vars
- `POST /services/:id/env` - Set env var
- `DELETE /services/:id/env/:key` - Delete env var

### Deployments
- `GET /deployments` - List deployments

### Canvas
- `GET /canvas/:projectId` - Get services and connections
- `POST /canvas/:projectId/position/:serviceId` - Update node position `{x, y}`
- `POST /canvas/connections` - Create connection `{sourceId, targetId}`
- `DELETE /canvas/connections/:id` - Delete connection

### Logs
- `GET /logs/:deploymentId` - Get deployment logs (auto-tiers: Redis for <10min, ClickHouse for older)
- `GET /logs/:deploymentId/stream` - Stream logs (Socket.io, room `logs:${deploymentId}`)

### Health
- `GET /healthz` - Always returns `{ok: true}`
- `GET /readyz` - Checks DB and AMQP connectivity, returns 503 if unhealthy

## UI Architecture

- **React 19** with **Vite** (no CRA)
- **React Router v7** for routing
- **@xyflow/react** (React Flow) for the interactive canvas showing service topology
- **Socket.io client** for real-time log streaming
- **Inline styles only** — no Tailwind, no CSS-in-JS library. Components are styled via `style={{...}}` objects; `src/index.css` and `src/App.css` exist only for global/base rules.
- Animations use the `motion` library.
- Mobile users are shown a "Desktop Required" screen

Routes:
- `/` → Landing page (or redirects to `/projects` if authenticated)
- `/login`, `/signup` → Auth pages
- `/projects` → Project list (protected)
- `/projects/:projectId` → Canvas view with service topology (protected)

Auth is JWT-based, stored in `localStorage` as `cp_token`.

## Infrastructure Ports

- `5432` - PostgreSQL
- `5672` - RabbitMQ (AMQP)
- `15672` - RabbitMQ Management UI
- `5001` - Docker Registry
- `6380` - Redis (Docker `cp_redis`, loopback only; 6379 is left to a host Redis)
- `9000` - MinIO S3 API
- `9001` - MinIO Console
- `8123` - ClickHouse HTTP
- `9009` - ClickHouse Native (mapped to avoid conflict)
- `80` - Nginx reverse proxy
- `8080` - API server
- `5173` - UI dev server

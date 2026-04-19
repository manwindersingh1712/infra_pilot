# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Infra Pilot is a minimal control-plane + UI for managing projects, services, and deployments. It uses:
- **Postgres** for state persistence
- **RabbitMQ** for async job queues
- **Docker** for building images and running containers
- **Nginx** for subdomain-based service routing
- **Redis** for Socket.io adapter and pub/sub
- **MinIO/S3** for log archival
- **ClickHouse** for structured log aggregation

## Repository Structure

```
control-plane/          # Node.js backend (npm workspaces)
├── apps/
│   ├── api/            # Fastify API (port 8080)
│   └── worker/         # Background job processor
├── packages/
│   └── shared/         # Prisma, AMQP, event topology
├── prisma/
│   └── schema.prisma   # Database schema
├── infra/nginx/        # Nginx config for reverse proxy
└── docker-compose.yml  # Infrastructure services

cp-ui/                  # React + Vite frontend (port 5173)
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
cd control-plane && npm run prisma:migrate
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

### Prisma Commands

```bash
cd control-plane
npm run prisma:migrate    # Run migrations in dev mode
npm run prisma:studio     # Open Prisma Studio GUI
```

## Architecture

### Data Flow (Deployment)

1. API receives `POST /services/:id/deploy` → creates `Deployment` row + `OutboxEvent`
2. Outbox publisher polls `OutboxEvent` table → publishes to RabbitMQ exchange
3. Build consumer receives message → clones repo → builds Docker image → pushes to registry → creates deploy outbox event
4. Deploy consumer receives message → runs container → configures nginx route

### Outbox Pattern

The outbox pattern ensures at-least-once message delivery:

- All state changes and events are written in the same database transaction
- The `OutboxEvent` table stores pending events with `status: pending`
- The outbox publisher (runs in worker) polls every second, claims events via `FOR UPDATE SKIP LOCKED`, publishes to RabbitMQ, then marks as `published`
- Failed publishes retry with exponential backoff; after 10 attempts events become `dead`

### Service Types

- **docker** - Custom Dockerfile builds
- **nodejs** - Node.js apps with auto-generated Dockerfile
- **nextjs** - Next.js apps with static export
- **react** - React SPA apps
- **mongodb** - Managed MongoDB container (skips build, uses volume persistence)
- **redis** - Managed Redis container (skips build, uses volume persistence)

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

Failed messages are republished with incremented `x-retry-count` header; after 5 retries they go to DLQ (`cp.dlq.q`).

### Log Aggregation Pipeline

1. Build/deploy logs stream from Docker containers to the worker
2. Logs are aggregated in memory and flushed to ClickHouse and S3 periodically
3. Real-time logs are broadcast via Socket.io to connected UI clients
4. Log history is served from ClickHouse (fast queries) or S3 (archival)

### Nginx Routing

Services are accessible via `http://<serviceId>.localhost/` (requires `/etc/hosts` entry or local DNS). The worker dynamically creates nginx route configs at `/infra/nginx/conf.d/routes/` and reloads nginx.

### Path Aliases

The control-plane uses TypeScript path mapping (`@/*`) for imports:

```typescript
import { prisma } from "@/packages/shared/src/db.js";
import { MQ } from "@/packages/shared/src/mq.js";
```

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
```

**cp-ui/.env:**
```
VITE_API_BASE=http://localhost:8080
```

## API Endpoints

### Auth
- `POST /dev/login` - Dev auth (returns JWT)
- `POST /auth/register`, `POST /auth/login` - User auth

### Projects
- `POST /projects` - Create project
- `GET /projects` - List projects

### Services
- `POST /services` - Create service
- `GET /services?projectId=` - List services
- `POST /services/:id/deploy` - Trigger deployment
- `GET /services/:id/env` - List env vars
- `POST /services/:id/env` - Set env var
- `DELETE /services/:id/env/:key` - Delete env var

### Deployments
- `GET /deployments` - List deployments

### Canvas
- `GET /canvas/:projectId` - Get services and connections
- `POST /canvas/:projectId/position/:serviceId` - Update node position
- `POST /canvas/connections` - Create connection
- `DELETE /canvas/connections/:id` - Delete connection

### Logs
- `GET /logs/:deploymentId` - Get deployment logs
- `GET /logs/:deploymentId/stream` - Stream logs (Socket.io)

## Infrastructure Ports

- `5432` - PostgreSQL
- `5672` - RabbitMQ (AMQP)
- `15672` - RabbitMQ Management UI
- `5001` - Docker Registry
- `6379` - Redis
- `9000` - MinIO S3 API
- `9001` - MinIO Console
- `8123` - ClickHouse HTTP
- `9009` - ClickHouse Native (mapped to avoid conflict)
- `80` - Nginx reverse proxy
- `8080` - API server
- `5173` - UI dev server

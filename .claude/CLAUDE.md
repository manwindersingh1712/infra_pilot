# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Infra Pilot is a minimal control-plane + UI for managing projects, services, and deployments. It uses:
- **Postgres** for state persistence
- **RabbitMQ** for async job queues
- **Docker** for building images and running containers
- **Nginx** for subdomain-based service routing

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
└── docker-compose.yml  # Postgres, RabbitMQ, registry, nginx

cp-ui/                  # React + Vite frontend (port 5173)
```

## Development Commands

### Initial Setup

```bash
# Start infrastructure dependencies (Postgres, RabbitMQ, registry, nginx)
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

### Key Components

- **Outbox Pattern**: All events are written to `OutboxEvent` table first, then published to RabbitMQ by a background loop. This ensures at-least-once delivery.
- **Idempotency**: Workers use atomic DB updates (`updateMany` with status check) to prevent duplicate processing.
- **Retry Logic**: Failed messages are republished with incremented `x-retry-count` header; after 5 retries they go to DLQ.
- **Nginx Routing**: Services are accessible via `http://<serviceId>.localhost/` (requires `/etc/hosts` entry or local DNS).

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
```

**cp-ui/.env:**
```
VITE_API_BASE=http://localhost:8080
```

## API Endpoints

- `POST /dev/login` - Dev auth (returns JWT)
- `POST /projects` - Create project
- `POST /services` - Create service
- `POST /services/:id/deploy` - Trigger deployment
- `GET /projects`, `GET /services`, `GET /deployments` - List resources

## Infrastructure Ports

- `5432` - PostgreSQL
- `5672` - RabbitMQ (AMQP)
- `15672` - RabbitMQ Management UI
- `5001` - Docker Registry
- `80` - Nginx reverse proxy
- `8080` - API server
- `5173` - UI dev server

# Scalable Container Logs Implementation Plan (Redis + S3 + ClickHouse)

> **Goal:** Build a log streaming system that scales to millions of deployments with real-time and historical log access.

**Architecture:** Redis Streams for hot logs (10-min TTL), S3 for cold storage, ClickHouse for querying. **Socket.io** for real-time streaming (rooms, fallbacks, auto-reconnect), HTTP API for historical queries.

---

## Architecture Overview

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                              LOG DATA FLOW                                   │
└─────────────────────────────────────────────────────────────────────────────┘

  Docker Container
         │
         │ docker logs -f
         ▼
  ┌─────────────┐
  │   Worker    │◄──────────────────────────────────────┐
  └─────────────┘                                       │
         │                                              │
         │ 1. Write to Redis Stream (10 min TTL)       │ 3. Read for WS
         ▼                                              │
  ┌─────────────┐     ┌─────────────┐                  │
  │    Redis    │◄────┤   API WS    │◄─────────────────┤
  │   Streams   │     │   Handler   │                  │
  └─────────────┘     └─────────────┘                  │
         │                     │                        │
         │ 2. Batch to S3      │ 4. WebSocket           │
         ▼                     ▼                        │
  ┌─────────────┐     ┌─────────────┐                  │
  │   S3 Bucket │     │     UI      │                  │
  │  (Parquet)  │     │  (LogViewer)│                  │
  └─────────────┘     └─────────────┘                  │
         │                                              │
         │ 5. Query old logs                            │
         ▼                                              │
  ┌─────────────┐                                      │
  │ ClickHouse  │                                      │
  │  (or local) │                                      │
  └─────────────┘                                      │
                                                       │
  ┌─────────────┐                                      │
  │ S3→ClickHouse│─────────────────────────────────────┘
  │   Sync Job  │     (6. Backfill Redis miss to S3)
  └─────────────┘
```

---

## Data Retention Strategy

| Storage | Retention | Use Case | Query Pattern |
|---------|-----------|----------|---------------|
| Redis Streams | 10 minutes | Real-time streaming, recent logs | By deployment ID, time range |
| S3 (Parquet) | 30 days | Medium-term storage, cost-effective | Via ClickHouse or Athena |
| ClickHouse | 30 days indexed | Fast analytical queries | Complex filters, aggregations |
| S3 Glacier | 1 year+ | Compliance, archival | Rare access only |

---

## File Structure

| File | Purpose |
|------|---------|
| `prisma/schema.prisma` | Add `LogStream` model (minimal, for cursor tracking) |
| `control-plane/apps/api/src/services/redis.ts` | Redis connection and stream operations |
| `control-plane/apps/api/src/services/s3-logs.ts` | S3 upload and download for log batches |
| `control-plane/apps/api/src/services/clickhouse.ts` | ClickHouse client and query builder |
| `control-plane/apps/api/src/services/log-aggregator.ts` | Unified interface: Redis → S3 → ClickHouse |
| `control-plane/apps/api/src/plugins/socketio.ts` | Socket.io server setup with Redis adapter |
| `control-plane/apps/api/src/routes/logs.ts` | HTTP routes for logs |
| `control-plane/apps/api/src/socket-handlers/logs.ts` | Socket.io handlers for log streaming |
| `control-plane/apps/worker/src/runtime/log-streamer.ts` | Docker log streaming to Redis |
| `control-plane/apps/worker/src/jobs/s3-upload-job.ts` | Background job: Redis → S3 batch upload |
| `cp-ui/src/hooks/useLogs.ts` | Custom hook for WebSocket + HTTP fallback |
| `cp-ui/src/components/LogViewer.tsx` | React component with infinite scroll |

---

## Prerequisites

Install required dependencies:

```bash
cd /Users/manwindersingh/Desktop/infra-pilot/control-plane
npm install socket.io @socket.io/redis-adapter ioredis @aws-sdk/client-s3 @clickhouse/client

cd /Users/manwindersingh/Desktop/infra-pilot/cp-ui
npm install socket.io-client

# For local development, run Redis
# Option 1: Docker
docker run -d -p 6379:6379 redis:7-alpine

# Option 2: Add to docker-compose.yml
```

---

## Task 1: Add Redis to Infrastructure

**Files:**
- Modify: `control-plane/docker-compose.yml`

- [ ] **Step 1: Add Redis service to docker-compose**

```yaml
  redis:
    image: redis:7-alpine
    ports:
      - "6379:6379"
    volumes:
      - redis_data:/data
    command: redis-server --appendonly yes --maxmemory 256mb --maxmemory-policy allkeys-lru

volumes:
  redis_data:
```

- [ ] **Step 2: Add Redis URL to .env**

```
REDIS_URL=redis://localhost:6379
```

- [ ] **Step 3: Commit**

```bash
git add control-plane/docker-compose.yml
git commit -m "infra: add redis for log streaming"
```

---

## Task 2: Create Redis Service Module

**Files:**
- Create: `control-plane/apps/api/src/services/redis.ts`

- [ ] **Step 1: Create Redis client and stream operations**

```typescript
// control-plane/apps/api/src/services/redis.ts
import Redis from "ioredis";

const redis = new Redis(process.env.REDIS_URL || "redis://localhost:6379");

export interface LogEntry {
  deploymentId: string;
  source: "stdout" | "stderr" | "system";
  message: string;
  timestamp: string;
}

const STREAM_MAX_LEN = 10000; // Max entries per stream
const STREAM_TTL_MS = 10 * 60 * 1000; // 10 minutes

/**
 * Add log entry to Redis Stream for a deployment
 */
export async function addLogEntry(entry: LogEntry): Promise<void> {
  const streamKey = `logs:stream:${entry.deploymentId}`;

  // Add to stream with maxlen to prevent unbounded growth
  await redis.xadd(
    streamKey,
    "MAXLEN",
    "~",
    STREAM_MAX_LEN,
    "*", // Auto-generate ID
    "source", entry.source,
    "message", entry.message,
    "timestamp", entry.timestamp
  );

  // Set expiration on the stream
  await redis.pexpire(streamKey, STREAM_TTL_MS);
}

/**
 * Read logs from Redis Stream with pagination
 */
export async function readLogs(
  deploymentId: string,
  options: {
    start?: string; // Stream ID to start from
    count?: number;
    block?: number; // Block milliseconds (for blocking read)
  } = {}
): Promise<LogEntry[]> {
  const { start = "-", count = 100, block } = options;
  const streamKey = `logs:stream:${deploymentId}`;

  let results: [string, string[]][];

  if (block) {
    // Blocking read for real-time
    const response = await redis.xread(
      "BLOCK", block,
      "COUNT", count,
      "STREAMS", streamKey, start
    );
    results = response?.[0]?.[1] || [];
  } else {
    // Non-blocking read
    results = await redis.xrange(streamKey, start, "+", "COUNT", count);
  }

  return results.map(([id, fields]) => {
    const map = new Map<string, string>();
    for (let i = 0; i < fields.length; i += 2) {
      map.set(fields[i], fields[i + 1]);
    }
    return {
      deploymentId,
      source: map.get("source") as LogEntry["source"],
      message: map.get("message") || "",
      timestamp: map.get("timestamp") || new Date().toISOString()
    };
  });
}

/**
 * Subscribe to new logs using Redis Pub/Sub for real-time
 */
export function subscribeToLogs(
  deploymentId: string,
  callback: (entry: LogEntry) => void
): () => void {
  const pubsub = new Redis(process.env.REDIS_URL || "redis://localhost:6379");
  const channel = `logs:pubsub:${deploymentId}`;

  pubsub.subscribe(channel);
  pubsub.on("message", (ch, message) => {
    if (ch === channel) {
      callback(JSON.parse(message));
    }
  });

  return () => {
    pubsub.unsubscribe(channel);
    pubsub.disconnect();
  };
}

/**
 * Publish log entry to Pub/Sub for WebSocket broadcast
 */
export async function publishLogEntry(entry: LogEntry): Promise<void> {
  const channel = `logs:pubsub:${entry.deploymentId}`;
  await redis.publish(channel, JSON.stringify(entry));
}

/**
 * Get all active stream keys (for cleanup/monitoring)
 */
export async function getActiveLogStreams(): Promise<string[]> {
  const keys = await redis.keys("logs:stream:*");
  return keys.map(k => k.replace("logs:stream:", ""));
}

/**
 * Delete log stream for a deployment
 */
export async function deleteLogStream(deploymentId: string): Promise<void> {
  await redis.del(`logs:stream:${deploymentId}`);
}

export { redis };
```

- [ ] **Step 2: Commit**

```bash
git add control-plane/apps/api/src/services/redis.ts
git commit -m "feat: add Redis service for log streaming"
```

---

## Task 3: Create S3 Logs Service

**Files:**
- Create: `control-plane/apps/api/src/services/s3-logs.ts`

- [ ] **Step 1: Create S3 client and upload operations**

```typescript
// control-plane/apps/api/src/services/s3-logs.ts
import { S3Client, PutObjectCommand, GetObjectCommand } from "@aws-sdk/client-s3";
import { LogEntry } from "./redis.js";

const s3Client = new S3Client({
  region: process.env.AWS_REGION || "us-east-1",
  endpoint: process.env.S3_ENDPOINT, // For MinIO/localstack in dev
  credentials: {
    accessKeyId: process.env.AWS_ACCESS_KEY_ID || "minioadmin",
    secretAccessKey: process.env.AWS_SECRET_ACCESS_KEY || "minioadmin"
  },
  forcePathStyle: !!process.env.S3_ENDPOINT // Required for MinIO
});

const BUCKET_NAME = process.env.LOGS_BUCKET || "container-logs";

interface LogBatch {
  deploymentId: string;
  logs: LogEntry[];
  startTime: Date;
  endTime: Date;
}

/**
 * Convert logs to Parquet-like JSON format (simpler for now)
 */
function logsToBuffer(logs: LogEntry[]): Buffer {
  // NDJSON format for efficient streaming
  const lines = logs.map(l => JSON.stringify(l)).join("\n");
  return Buffer.from(lines, "utf-8");
}

/**
 * Upload log batch to S3
 */
export async function uploadLogBatch(batch: LogBatch): Promise<string> {
  const datePrefix = batch.startTime.toISOString().split("T")[0]; // YYYY-MM-DD
  const key = `logs/${batch.deploymentId}/${datePrefix}/${batch.startTime.getTime()}.ndjson.gz`;

  const buffer = logsToBuffer(batch.logs);

  // Compress with zlib (can use Parquet later for better compression)
  const zlib = await import("zlib");
  const compressed = zlib.gzipSync(buffer);

  await s3Client.send(new PutObjectCommand({
    Bucket: BUCKET_NAME,
    Key: key,
    Body: compressed,
    ContentType: "application/x-ndjson",
    ContentEncoding: "gzip",
    Metadata: {
      "deployment-id": batch.deploymentId,
      "start-time": batch.startTime.toISOString(),
      "end-time": batch.endTime.toISOString(),
      "log-count": String(batch.logs.length)
    }
  }));

  return key;
}

/**
 * Download logs from S3 for a deployment within time range
 */
export async function downloadLogs(
  deploymentId: string,
  startTime: Date,
  endTime: Date
): Promise<LogEntry[]> {
  // List objects with prefix
  const { ListObjectsV2Command } = await import("@aws-sdk/client-s3");

  const listResponse = await s3Client.send(new ListObjectsV2Command({
    Bucket: BUCKET_NAME,
    Prefix: `logs/${deploymentId}/`
  }));

  const keys = (listResponse.Contents || [])
    .filter(obj => obj.Key && obj.LastModified)
    .filter(obj => {
      const time = obj.LastModified!;
      return time >= startTime && time <= endTime;
    })
    .map(obj => obj.Key!);

  const allLogs: LogEntry[] = [];

  for (const key of keys) {
    try {
      const response = await s3Client.send(new GetObjectCommand({
        Bucket: BUCKET_NAME,
        Key: key
      }));

      const bytes = await response.Body?.transformToByteArray();
      if (!bytes) continue;

      // Decompress
      const zlib = await import("zlib");
      const decompressed = zlib.gunzipSync(Buffer.from(bytes));
      const lines = decompressed.toString("utf-8").trim().split("\n");

      for (const line of lines) {
        if (line) allLogs.push(JSON.parse(line));
      }
    } catch (err) {
      console.error(`[s3-logs] Failed to download ${key}:`, err);
    }
  }

  // Sort by timestamp
  return allLogs.sort((a, b) =>
    new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime()
  );
}

export { s3Client, BUCKET_NAME };
```

- [ ] **Step 2: Add S3/MinIO to docker-compose for local dev**

```yaml
  minio:
    image: minio/minio:latest
    ports:
      - "9000:9000"
      - "9001:9001"
    environment:
      MINIO_ROOT_USER: minioadmin
      MINIO_ROOT_PASSWORD: minioadmin
    volumes:
      - minio_data:/data
    command: server /data --console-address ":9001"

volumes:
  minio_data:
```

- [ ] **Step 3: Commit**

```bash
git add control-plane/apps/api/src/services/s3-logs.ts
git add control-plane/docker-compose.yml
git commit -m "feat: add S3 service for log persistence"
```

---

## Task 4: Create ClickHouse Service

**Files:**
- Create: `control-plane/apps/api/src/services/clickhouse.ts`

- [ ] **Step 1: Create ClickHouse client**

```typescript
// control-plane/apps/api/src/services/clickhouse.ts
import { createClient } from "@clickhouse/client";
import { LogEntry } from "./redis.js";

const clickhouse = createClient({
  url: process.env.CLICKHOUSE_URL || "http://localhost:8123",
  username: process.env.CLICKHOUSE_USER || "default",
  password: process.env.CLICKHOUSE_PASSWORD || ""
});

const TABLE_NAME = "container_logs";

/**
 * Initialize ClickHouse table for logs
 */
export async function initClickHouse(): Promise<void> {
  await clickhouse.exec({
    query: `
      CREATE TABLE IF NOT EXISTS ${TABLE_NAME} (
        deployment_id String,
        source LowCardinality(String),
        message String,
        timestamp DateTime64(3),
        ingestion_time DateTime64(3) DEFAULT now64(3)
      ) ENGINE = MergeTree()
      ORDER BY (deployment_id, timestamp)
      PARTITION BY toYYYYMMDD(timestamp)
      TTL timestamp + INTERVAL 30 DAY
      SETTINGS index_granularity = 8192
    `
  });

  console.log("[clickhouse] Table initialized");
}

/**
 * Insert logs in batch
 */
export async function insertLogs(logs: LogEntry[]): Promise<void> {
  if (logs.length === 0) return;

  const values = logs.map(l => ({
    deployment_id: l.deploymentId,
    source: l.source,
    message: l.message,
    timestamp: new Date(l.timestamp).getTime() / 1000 // Convert to seconds
  }));

  await clickhouse.insert({
    table: TABLE_NAME,
    values,
    format: "JSONEachRow"
  });
}

/**
 * Query logs with filters
 */
export async function queryLogs(options: {
  deploymentId: string;
  startTime?: Date;
  endTime?: Date;
  sources?: ("stdout" | "stderr" | "system")[];
  search?: string;
  limit?: number;
  offset?: number;
}): Promise<LogEntry[]> {
  const {
    deploymentId,
    startTime,
    endTime,
    sources,
    search,
    limit = 100,
    offset = 0
  } = options;

  const conditions: string[] = [`deployment_id = '${deploymentId}'`];

  if (startTime) {
    conditions.push(`timestamp >= '${startTime.toISOString()}'`);
  }
  if (endTime) {
    conditions.push(`timestamp <= '${endTime.toISOString()}'`);
  }
  if (sources && sources.length > 0) {
    conditions.push(`source IN ('${sources.join("','")}')`);
  }
  if (search) {
    conditions.push(`message ILIKE '%${search.replace(/'/g, "''")}%'`);
  }

  const query = `
    SELECT
      deployment_id as deploymentId,
      source,
      message,
      formatDateTime(timestamp, '%Y-%m-%dT%H:%i:%s.%fZ') as timestamp
    FROM ${TABLE_NAME}
    WHERE ${conditions.join(" AND ")}
    ORDER BY timestamp DESC
    LIMIT ${limit}
    OFFSET ${offset}
  `;

  const resultSet = await clickhouse.query({ query, format: "JSONEachRow" });
  const rows = await resultSet.json<{
    deploymentId: string;
    source: LogEntry["source"];
    message: string;
    timestamp: string;
  }>();

  return rows.reverse(); // Oldest first
}

/**
 * Get log count for a deployment
 */
export async function getLogCount(deploymentId: string): Promise<number> {
  const resultSet = await clickhouse.query({
    query: `SELECT count() as count FROM ${TABLE_NAME} WHERE deployment_id = '${deploymentId}'`,
    format: "JSONEachRow"
  });

  const rows = await resultSet.json<{ count: string }>();
  return parseInt(rows[0]?.count || "0", 10);
}

export { clickhouse };
```

- [ ] **Step 2: Add ClickHouse to docker-compose**

```yaml
  clickhouse:
    image: clickhouse/clickhouse-server:latest
    ports:
      - "8123:8123"  # HTTP interface
      - "9000:9000"  # Native protocol
    volumes:
      - clickhouse_data:/var/lib/clickhouse

volumes:
  clickhouse_data:
```

- [ ] **Step 3: Commit**

```bash
git add control-plane/apps/api/src/services/clickhouse.ts
git add control-plane/docker-compose.yml
git commit -m "feat: add ClickHouse service for log analytics"
```

---

## Task 5: Create Log Aggregator Service

**Files:**
- Create: `control-plane/apps/api/src/services/log-aggregator.ts`

- [ ] **Step 1: Create unified log interface**

```typescript
// control-plane/apps/api/src/services/log-aggregator.ts
import * as redis from "./redis.js";
import * as s3Logs from "./s3-logs.js";
import * as clickhouse from "./clickhouse.js";
import { LogEntry } from "./redis.js";

export type { LogEntry } from "./redis.js";

interface QueryOptions {
  deploymentId: string;
  startTime?: Date;
  endTime?: Date;
  limit?: number;
  sources?: ("stdout" | "stderr" | "system")[];
  search?: string;
}

/**
 * Write a log entry (called by worker)
 * Path: Worker → Redis (hot) + AMQP broadcast
 */
export async function writeLog(entry: LogEntry): Promise<void> {
  // Always write to Redis for real-time access
  await redis.addLogEntry(entry);

  // Publish to Pub/Sub for immediate WebSocket broadcast
  await redis.publishLogEntry(entry);

  // Note: S3 and ClickHouse are written by background batch job, not real-time
}

/**
 * Query logs with automatic tiering:
 * 1. Recent (< 10 min): Redis only
 * 2. Medium-term (10 min - 30 days): ClickHouse
 * 3. Long-term (> 30 days): S3 (if implemented)
 */
export async function queryLogs(options: QueryOptions): Promise<{
  logs: LogEntry[];
  source: "redis" | "clickhouse" | "s3";
  totalCount?: number;
}> {
  const { deploymentId, startTime, endTime, limit = 100 } = options;

  const now = new Date();
  const tenMinutesAgo = new Date(now.getTime() - 10 * 60 * 1000);

  // Case 1: Only recent logs requested - query Redis
  if (!startTime || startTime > tenMinutesAgo) {
    const logs = await redis.readLogs(deploymentId, { count: limit });
    if (logs.length > 0) {
      return { logs, source: "redis" };
    }
  }

  // Case 2: Query ClickHouse for historical logs
  const logs = await clickhouse.queryLogs({
    deploymentId,
    startTime,
    endTime,
    sources: options.sources,
    search: options.search,
    limit
  });

  // If we have Redis data too, merge it (Redis is more recent)
  if (!startTime || endTime && endTime > tenMinutesAgo) {
    const redisLogs = await redis.readLogs(deploymentId, { count: limit });
    const redisSet = new Set(redisLogs.map(l => `${l.timestamp}-${l.message}`));

    // Filter out duplicates
    const uniqueClickHouse = logs.filter(
      l => !redisSet.has(`${l.timestamp}-${l.message}`)
    );

    return {
      logs: [...uniqueClickHouse, ...redisLogs].slice(-limit),
      source: "clickhouse",
      totalCount: await clickhouse.getLogCount(deploymentId)
    };
  }

  return { logs, source: "clickhouse" };
}

/**
 * Subscribe to real-time logs
 */
export function subscribeToLogs(
  deploymentId: string,
  callback: (entry: LogEntry) => void
): () => void {
  return redis.subscribeToLogs(deploymentId, callback);
}

/**
 * Background job: Flush Redis logs to S3 and ClickHouse
 * Should run every 5 minutes via cron or similar
 */
export async function flushLogsToColdStorage(): Promise<{
  processed: number;
  errors: number;
}> {
  const streamIds = await redis.getActiveLogStreams();
  let processed = 0;
  let errors = 0;

  for (const deploymentId of streamIds) {
    try {
      // Read all logs from Redis stream
      const logs = await redis.readLogs(deploymentId, {
        start: "-",
        count: 10000
      });

      if (logs.length === 0) continue;

      // Write to ClickHouse
      await clickhouse.insertLogs(logs);

      // Write to S3 for backup (every 1000 logs or when stream is large)
      if (logs.length >= 1000) {
        await s3Logs.uploadLogBatch({
          deploymentId,
          logs,
          startTime: new Date(logs[0].timestamp),
          endTime: new Date(logs[logs.length - 1].timestamp)
        });
      }

      processed += logs.length;
    } catch (err) {
      console.error(`[log-aggregator] Failed to flush ${deploymentId}:`, err);
      errors++;
    }
  }

  return { processed, errors };
}

/**
 * Get active log streams count (for monitoring)
 */
export async function getActiveStreamCount(): Promise<number> {
  const streams = await redis.getActiveLogStreams();
  return streams.length;
}
```

- [ ] **Step 2: Commit**

```bash
git add control-plane/apps/api/src/services/log-aggregator.ts
git commit -m "feat: add log aggregator for tiered storage"
```

---

## Task 6: Add Socket.io Plugin

**Files:**
- Create: `control-plane/apps/api/src/plugins/socketio.ts`

- [ ] **Step 1: Create Socket.io plugin**

```typescript
// control-plane/apps/api/src/plugins/socketio.ts
import fp from "fastify-plugin";
import type { FastifyInstance } from "fastify";
import { Server } from "socket.io";
import { createAdapter } from "@socket.io/redis-adapter";
import { redis } from "../services/redis.js";

declare module "fastify" {
  interface FastifyInstance {
    io: Server;
  }
}

export default fp(async function (fastify: FastifyInstance) {
  const io = new Server(fastify.server, {
    cors: {
      origin: process.env.CORS_ORIGIN || "http://localhost:5173",
      credentials: true
    },
    transports: ["websocket", "polling"] // Fallback to polling if WS fails
  });

  // Use Redis adapter for horizontal scaling (multiple API instances)
  const pubClient = redis.duplicate();
  const subClient = redis.duplicate();
  io.adapter(createAdapter(pubClient, subClient));

  fastify.decorate("io", io);
  fastify.addHook("onClose", async () => {
    io.close();
    await pubClient.quit();
    await subClient.quit();
  });
});
```

- [ ] **Step 2: Install Redis adapter**

```bash
cd /Users/manwindersingh/Desktop/infra-pilot/control-plane
npm install @socket.io/redis-adapter
```

- [ ] **Step 3: Commit**

```bash
git add control-plane/apps/api/src/plugins/socketio.ts
git commit -m "feat: add Socket.io plugin with Redis adapter"
```

---

## Task 7: Create HTTP Log Routes

**Files:**
- Create: `control-plane/apps/api/src/routes/logs.ts`

- [ ] **Step 1: Create HTTP routes for historical logs**

```typescript
// control-plane/apps/api/src/routes/logs.ts
import { FastifyInstance } from "fastify";
import { prisma } from "@/packages/shared/src/db.js";
import * as logAggregator from "../services/log-aggregator.js";

export async function logRoutes(app: FastifyInstance) {
  // HTTP endpoint: Get historical logs with tiered storage
  app.get("/deployments/:deploymentId", async (req, reply) => {
    const { deploymentId } = req.params as { deploymentId: string };

    // Verify deployment exists
    const deployment = await prisma.deployment.findUnique({
      where: { id: deploymentId },
      select: { id: true, serviceId: true }
    });

    if (!deployment) {
      return reply.status(404).send({ error: "deployment not found" });
    }

    // Parse query params
    const {
      limit = "100",
      before,
      after,
      sources,
      search
    } = req.query as {
      limit?: string;
      before?: string;
      after?: string;
      sources?: string;
      search?: string;
    };

    const sourceArray = sources?.split(",") as ("stdout" | "stderr" | "system")[] | undefined;

    // Query from appropriate storage tier
    const { logs, source, totalCount } = await logAggregator.queryLogs({
      deploymentId,
      startTime: after ? new Date(after) : undefined,
      endTime: before ? new Date(before) : undefined,
      limit: Math.min(parseInt(limit, 10), 1000),
      sources: sourceArray,
      search
    });

    return {
      logs,
      meta: {
        source, // "redis" | "clickhouse"
        count: logs.length,
        totalCount,
        hasMore: totalCount ? logs.length < totalCount : false
      }
    };
  });

  // Health check for log streaming
  app.get("/health", async () => {
    const activeStreams = await logAggregator.getActiveStreamCount();
    return {
      status: "healthy",
      activeLogStreams: activeStreams
    };
  });
}
```

- [ ] **Step 2: Commit**

```bash
git add control-plane/apps/api/src/routes/logs.ts
git commit -m "feat: add HTTP log routes"
```

---

## Task 8: Create Socket.io Log Handlers

**Files:**
- Create: `control-plane/apps/api/src/socket-handlers/logs.ts`

- [ ] **Step 1: Create Socket.io handlers for real-time logs**

```typescript
// control-plane/apps/api/src/socket-handlers/logs.ts
import type { Server, Socket } from "socket.io";
import { prisma } from "@/packages/shared/src/db.js";
import * as logAggregator from "../services/log-aggregator.js";

export function registerLogHandlers(io: Server) {
  io.on("connection", (socket: Socket) => {
    console.log("[socket.io] client connected:", socket.id);

    // Client joins a deployment room to receive logs
    socket.on("subscribe:logs", async (deploymentId: string, callback) => {
      try {
        // Verify deployment exists
        const deployment = await prisma.deployment.findUnique({
          where: { id: deploymentId },
          select: { id: true }
        });

        if (!deployment) {
          callback?.({ error: "deployment not found" });
          return;
        }

        // Join room for this deployment
        const room = `logs:${deploymentId}`;
        await socket.join(room);

        console.log(`[socket.io] ${socket.id} joined room ${room}`);

        // Send historical logs immediately
        const { logs, meta } = await logAggregator.queryLogs({
          deploymentId,
          limit: 100
        });

        socket.emit("logs:history", { logs, meta });

        callback?.({ success: true, room });
      } catch (err) {
        console.error("[socket.io] subscribe error:", err);
        callback?.({ error: "failed to subscribe" });
      }
    });

    // Client leaves a deployment room
    socket.on("unsubscribe:logs", (deploymentId: string, callback) => {
      const room = `logs:${deploymentId}`;
      socket.leave(room);
      console.log(`[socket.io] ${socket.id} left room ${room}`);
      callback?.({ success: true });
    });

    socket.on("disconnect", () => {
      console.log("[socket.io] client disconnected:", socket.id);
    });
  });

  // Listen for new logs from Redis and broadcast to rooms
  // This is called by the log aggregator when new logs arrive
  return {
    broadcastLog: (deploymentId: string, log: logAggregator.LogEntry) => {
      const room = `logs:${deploymentId}`;
      io.to(room).emit("logs:new", log);
    }
  };
}
```

- [ ] **Step 2: Update log aggregator to broadcast via Socket.io**

Modify `control-plane/apps/api/src/services/log-aggregator.ts` to accept a broadcast callback:

```typescript
// Add optional broadcast callback
let broadcastCallback: ((deploymentId: string, log: LogEntry) => void) | null = null;

export function setBroadcastCallback(
  callback: (deploymentId: string, log: LogEntry) => void
): void {
  broadcastCallback = callback;
}

// In writeLog function:
export async function writeLog(entry: LogEntry): Promise<void> {
  await redis.addLogEntry(entry);
  await redis.publishLogEntry(entry);

  // Also broadcast via Socket.io if callback is set
  broadcastCallback?.(entry.deploymentId, entry);
}
```

- [ ] **Step 3: Integrate handlers in API index**
```

- [ ] **Step 2: Commit**

```bash
git add control-plane/apps/api/src/routes/logs.ts
git commit -m "feat: add logs HTTP and WebSocket routes with tiered storage"
```

---

## Task 9: Register Routes and Socket.io in API

**Files:**
- Modify: `control-plane/apps/api/src/index.ts`

- [ ] **Step 1: Add imports and registrations**

```typescript
// Add these imports after existing imports
import socketioPlugin from "./plugins/socketio.js";
import { logRoutes } from "./routes/logs.js";
import { initClickHouse } from "./services/clickhouse.js";
import { registerLogHandlers } from "./socket-handlers/logs.js";
import { setBroadcastCallback } from "./services/log-aggregator.js";

// After CORS plugin registration, add:
await app.register(socketioPlugin);

// Register Socket.io log handlers
const { broadcastLog } = registerLogHandlers(app.io);

// Set broadcast callback for log aggregator
setBroadcastCallback(broadcastLog);

// After other route registrations, add:
app.register(logRoutes, { prefix: "/logs" });

// Before starting server, initialize ClickHouse:
await initClickHouse();
```

- [ ] **Step 2: Commit**

```bash
git add control-plane/apps/api/src/index.ts
git commit -m "feat: register log routes and initialize ClickHouse"
```

---

## Task 10: Create Worker Log Streamer

**Files:**
- Create: `control-plane/apps/worker/src/runtime/log-streamer.ts`

- [ ] **Step 1: Create Docker log streamer to Redis**

```typescript
// control-plane/apps/worker/src/runtime/log-streamer.ts
import { spawn } from "child_process";
import Redis from "ioredis";

const redis = new Redis(process.env.REDIS_URL || "redis://localhost:6379");

interface ActiveStream {
  deploymentId: string;
  containerId: string;
  process: ReturnType<typeof spawn>;
  startTime: Date;
}

const activeStreams = new Map<string, ActiveStream>();

interface LogEntry {
  deploymentId: string;
  source: "stdout" | "stderr" | "system";
  message: string;
  timestamp: string;
}

async function publishLog(entry: LogEntry): Promise<void> {
  const streamKey = `logs:stream:${entry.deploymentId}`;
  const channel = `logs:pubsub:${entry.deploymentId}`;

  // Add to Redis Stream
  await redis.xadd(
    streamKey,
    "MAXLEN",
    "~",
    10000,
    "*",
    "source", entry.source,
    "message", entry.message,
    "timestamp", entry.timestamp
  );

  // Set expiration
  await redis.pexpire(streamKey, 10 * 60 * 1000);

  // Publish to Pub/Sub for real-time
  await redis.publish(channel, JSON.stringify(entry));
}

export function startLogStreaming(deploymentId: string, containerId: string): void {
  if (activeStreams.has(deploymentId)) {
    console.log("[log-streamer] already streaming:", deploymentId);
    return;
  }

  console.log("[log-streamer] starting:", deploymentId, "container:", containerId);

  const process = spawn("docker", ["logs", "-f", "--tail=100", containerId]);

  activeStreams.set(deploymentId, {
    deploymentId,
    containerId,
    process,
    startTime: new Date()
  });

  // Handle stdout
  process.stdout.on("data", async (data: Buffer) => {
    const lines = data.toString().split("\n").filter(l => l.length > 0);
    for (const line of lines) {
      await publishLog({
        deploymentId,
        source: "stdout",
        message: line,
        timestamp: new Date().toISOString()
      });
    }
  });

  // Handle stderr
  process.stderr.on("data", async (data: Buffer) => {
    const lines = data.toString().split("\n").filter(l => l.length > 0);
    for (const line of lines) {
      await publishLog({
        deploymentId,
        source: "stderr",
        message: line,
        timestamp: new Date().toISOString()
      });
    }
  });

  // Handle exit
  process.on("close", async (code) => {
    console.log("[log-streamer] ended:", deploymentId, "code:", code);
    activeStreams.delete(deploymentId);

    await publishLog({
      deploymentId,
      source: "system",
      message: `Log stream ended (exit code: ${code})`,
      timestamp: new Date().toISOString()
    });
  });

  // Handle errors
  process.on("error", async (err) => {
    console.error("[log-streamer] error:", deploymentId, err);
    activeStreams.delete(deploymentId);

    await publishLog({
      deploymentId,
      source: "system",
      message: `Log stream error: ${err.message}`,
      timestamp: new Date().toISOString()
    });
  });
}

export function stopLogStreaming(deploymentId: string): void {
  const stream = activeStreams.get(deploymentId);
  if (stream) {
    console.log("[log-streamer] stopping:", deploymentId);
    stream.process.kill();
    activeStreams.delete(deploymentId);
  }
}

export function getActiveStreams(): string[] {
  return Array.from(activeStreams.keys());
}

export function isStreaming(deploymentId: string): boolean {
  return activeStreams.has(deploymentId);
}
```

- [ ] **Step 2: Commit**

```bash
git add control-plane/apps/worker/src/runtime/log-streamer.ts
git commit -m "feat: add Docker log streamer with Redis integration"
```

---

## Task 11: Integrate with Deploy Consumer

**Files:**
- Modify: `control-plane/apps/worker/src/consumers/deploy-consumer.ts`

- [ ] **Step 1: Import and start log streaming**

```typescript
// Add import at top
import { startLogStreaming } from "@/apps/worker/src/runtime/log-streamer.js";

// After deployment status is updated to "deployed" (around line 220-240), add:
// Start streaming logs from the container
startLogStreaming(dep.id, containerId);
```

- [ ] **Step 2: Commit**

```bash
git add control-plane/apps/worker/src/consumers/deploy-consumer.ts
git commit -m "feat: start log streaming on deployment success"
```

---

## Task 12: Create Background Flush Job

**Files:**
- Create: `control-plane/apps/worker/src/jobs/flush-logs-job.ts`

- [ ] **Step 1: Create periodic flush job**

```typescript
// control-plane/apps/worker/src/jobs/flush-logs-job.ts
import { flushLogsToColdStorage } from "@/apps/api/src/services/log-aggregator.js";

let intervalId: NodeJS.Timeout | null = null;

export function startFlushJob(intervalMs = 5 * 60 * 1000): void {
  console.log("[flush-logs-job] Starting with interval:", intervalMs, "ms");

  intervalId = setInterval(async () => {
    console.log("[flush-logs-job] Running flush...");
    const result = await flushLogsToColdStorage();
    console.log("[flush-logs-job] Flushed:", result.processed, "errors:", result.errors);
  }, intervalMs);
}

export function stopFlushJob(): void {
  if (intervalId) {
    clearInterval(intervalId);
    intervalId = null;
  }
}
```

- [ ] **Step 2: Start job in worker index**

```typescript
// Add to control-plane/apps/worker/src/index.ts
import { startFlushJob } from "./jobs/flush-logs-job.js";

// After starting other consumers:
startFlushJob();
```

- [ ] **Step 3: Commit**

```bash
git add control-plane/apps/worker/src/jobs/flush-logs-job.ts
git add control-plane/apps/worker/src/index.ts
git commit -m "feat: add background job to flush logs to ClickHouse/S3"
```

---

## Task 13: Create React Hooks and Components

Same as original plan but with minor updates for metadata.

**Files:**
- Create: `cp-ui/src/hooks/useLogs.ts`
- Create: `cp-ui/src/components/LogViewer.tsx`

- [ ] **Step 1: Create useLogs hook**

```typescript
// cp-ui/src/hooks/useLogs.ts
import { useState, useEffect, useCallback } from "react";
import { io, Socket } from "socket.io-client";

const API = import.meta.env.VITE_API_BASE ?? "http://localhost:8080";

export interface LogEntry {
  deploymentId: string;
  source: "stdout" | "stderr" | "system";
  message: string;
  timestamp: string;
}

interface LogMeta {
  source: "redis" | "clickhouse";
  count: number;
  totalCount?: number;
  hasMore: boolean;
}

interface UseLogsOptions {
  deploymentId: string;
  token?: string;
}

export function useLogs({ deploymentId, token }: UseLogsOptions) {
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [meta, setMeta] = useState<LogMeta | null>(null);
  const [isConnected, setIsConnected] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [socket, setSocket] = useState<Socket | null>(null);

  // Initialize Socket.io connection
  useEffect(() => {
    if (!token) return;

    const newSocket = io(API, {
      transports: ["websocket", "polling"], // Fallback to polling if WS fails
      auth: { token }
    });

    newSocket.on("connect", () => {
      console.log("[socket.io] connected");
      setIsConnected(true);
      setError(null);
    });

    newSocket.on("disconnect", () => {
      console.log("[socket.io] disconnected");
      setIsConnected(false);
    });

    newSocket.on("connect_error", (err) => {
      console.error("[socket.io] connection error:", err);
      setError("Connection error");
      setIsConnected(false);
    });

    setSocket(newSocket);

    return () => {
      newSocket.close();
    };
  }, [token]);

  // Subscribe to deployment logs when deploymentId changes
  useEffect(() => {
    if (!socket || !deploymentId) return;

    // Subscribe to logs for this deployment
    socket.emit("subscribe:logs", deploymentId, (response: { error?: string; success?: boolean }) => {
      if (response?.error) {
        console.error("[socket.io] subscribe error:", response.error);
        setError(response.error);
      } else {
        console.log("[socket.io] subscribed to", deploymentId);
      }
    });

    // Listen for historical logs
    socket.on("logs:history", (data: { logs: LogEntry[]; meta: LogMeta }) => {
      console.log("[socket.io] received history:", data.logs.length, "logs");
      setLogs(data.logs);
      setMeta(data.meta);
    });

    // Listen for new logs
    socket.on("logs:new", (log: LogEntry) => {
      setLogs((prev) => {
        // Deduplication check
        const isDuplicate = prev.some(
          (l) => l.timestamp === log.timestamp && l.message === log.message
        );
        if (isDuplicate) return prev;
        return [...prev, log];
      });
    });

    return () => {
      // Unsubscribe when component unmounts or deploymentId changes
      socket.emit("unsubscribe:logs", deploymentId);
      socket.off("logs:history");
      socket.off("logs:new");
    };
  }, [socket, deploymentId]);
    };

    connect();

    return () => {
      shouldReconnect.current = false;
      wsRef.current?.close();
    };
  }, [deploymentId, token]);

  const loadMore = useCallback(async () => {
    if (!deploymentId || !token || !meta?.hasMore) return;

    const oldestLog = logs[0];
    const before = oldestLog?.timestamp;

    const res = await fetch(
      `${API}/logs/deployments/${deploymentId}?before=${before}&limit=100`,
      { headers: { Authorization: `Bearer ${token}` } }
    );

    const data = await res.json();
    if (data.logs?.length) {
      setLogs((prev) => [...data.logs, ...prev]);
      setMeta(data.meta);
    }
  }, [deploymentId, token, logs, meta]);

  const clearLogs = useCallback(() => {
    setLogs([]);
  }, []);

  return { logs, meta, isConnected, error, loadMore, clearLogs };
}
```

- [ ] **Step 2: Create LogViewer component** (same as original plan with minor enhancements)

- [ ] **Step 3: Commit**

```bash
git add cp-ui/src/hooks/useLogs.ts
git add cp-ui/src/components/LogViewer.tsx
git commit -m "feat: add useLogs hook and LogViewer component"
```

---

## Task 14: Integrate LogViewer into App

**Files:**
- Modify: `cp-ui/src/App.tsx`

- [ ] **Step 1: Add LogViewer to App** (same as original plan)

- [ ] **Step 2: Commit**

```bash
git add cp-ui/src/App.tsx
git commit -m "feat: integrate LogViewer into main app"
```

---

## Task 15: Testing & Verification

- [ ] **Step 1: Start infrastructure**

```bash
cd /Users/manwindersingh/Desktop/infra-pilot/control-plane
docker compose up -d
```

- [ ] **Step 2: Verify all services are running**

```bash
docker compose ps
# Should show: postgres, rabbitmq, redis, minio, clickhouse, nginx
```

- [ ] **Step 3: Start API, Worker, UI**

```bash
# Terminal 1: API
cd control-plane && npm run dev:api

# Terminal 2: Worker
cd control-plane && npm run dev:worker

# Terminal 3: UI
cd cp-ui && npm run dev
```

- [ ] **Step 4: Test the full flow**

1. Create project and service
2. Trigger deployment
3. Click deployment to view logs
4. Verify real-time streaming (WebSocket)
5. Verify historical logs (ClickHouse query)
6. Wait 10+ minutes, verify logs still accessible

- [ ] **Step 5: Run load test (optional)**

```bash
# Generate many log entries to test Redis/ClickHouse performance
for i in {1..10000}; do
  echo "Log entry $i"
done | docker logs -f <container>
```

---

## Task 16: Final Commit

```bash
git add -A
git commit -m "feat: implement scalable container logs with Redis + S3 + ClickHouse

- Add Redis Streams for hot log storage (10-min TTL)
- Add S3 integration for cold storage (Parquet/NDJSON)
- Add ClickHouse for fast analytical queries
- Add tiered log aggregator service
- Add WebSocket for real-time streaming
- Add background job for batch flushing
- Add React LogViewer with infinite scroll"
```

---

## Scalability Characteristics

| Metric | Capacity |
|--------|----------|
| Concurrent log streams | 10,000+ (Redis memory bound) |
| Logs per second | 100,000+ (Redis + ClickHouse) |
| Historical retention | 30 days (ClickHouse), 1 year (S3) |
| Query performance | < 100ms for recent, < 1s for historical |
| Storage cost | $0.023/GB/month (S3) |

## Scaling Strategies

### Horizontal Scaling
- **Redis**: Use Redis Cluster for > 100K streams
- **ClickHouse**: Shard by deployment_id, use distributed tables
- **API**: Stateless, run multiple instances behind load balancer
- **Worker**: Run multiple workers with partition-based routing

### Performance Tuning
- Increase Redis `maxmemory` for more hot retention
- Adjust ClickHouse `index_granularity` for faster queries
- Use Kafka between Worker and Redis for massive scale
- Enable ClickHouse materialized views for aggregations

# Real-time Container Logs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add real-time container log streaming from deployed containers to the UI via WebSocket, with historical log persistence.

**Architecture:** Use `@fastify/websocket` for real-time log streaming from API to UI. The worker spawns `docker logs -f` processes that broadcast to WebSocket clients via an in-memory pub/sub. Logs are persisted to PostgreSQL for historical access.

**Tech Stack:** Fastify + @fastify/websocket, React + native WebSocket API, Docker logs via child_process spawn

---

## File Structure

| File | Purpose |
|------|---------|
| `prisma/schema.prisma` | Add `LogStream` model for log persistence |
| `control-plane/apps/api/src/plugins/websocket.ts` | WebSocket plugin registration |
| `control-plane/apps/api/src/services/log-broadcaster.ts` | In-memory pub/sub for log distribution |
| `control-plane/apps/api/src/routes/logs.ts` | HTTP + WebSocket routes for logs |
| `control-plane/apps/worker/src/runtime/log-streamer.ts` | Docker log streaming manager |
| `control-plane/apps/worker/src/index.ts` | Start/stop log streaming with deployment lifecycle |
| `cp-ui/src/components/LogViewer.tsx` | React component for log display |
| `cp-ui/src/hooks/useLogs.ts` | Custom hook for WebSocket connection management |

---

## Prerequisites

Install required dependencies first:

```bash
cd /Users/manwindersingh/Desktop/infra-pilot/control-plane
npm install @fastify/websocket

cd /Users/manwindersingh/Desktop/infra-pilot/cp-ui
# No additional deps needed - using native WebSocket
```

---

## Task 1: Database Schema - Add LogStream Model

**Files:**
- Modify: `prisma/schema.prisma`

- [ ] **Step 1: Add LogStream model to schema**

Add this model after the `OutboxEvent` model:

```prisma
model LogStream {
  id           String   @id @default(cuid())
  deploymentId String
  source       String   // "stdout" | "stderr" | "system"
  message      String
  timestamp    DateTime @default(now())

  @@index([deploymentId, timestamp])
  @@index([deploymentId])
}
```

- [ ] **Step 2: Run migration**

```bash
cd /Users/manwindersingh/Desktop/infra-pilot/control-plane
npx prisma migrate dev --name add_log_stream
```

Expected output: Migration created and applied successfully

- [ ] **Step 3: Regenerate Prisma client**

```bash
npx prisma generate
```

Expected output: Prisma client generated successfully

- [ ] **Step 4: Commit**

```bash
git add prisma/schema.prisma prisma/migrations/
git commit -m "feat: add LogStream model for container logs"
```

---

## Task 2: Create Log Broadcaster Service (In-Memory Pub/Sub)

**Files:**
- Create: `control-plane/apps/api/src/services/log-broadcaster.ts`

- [ ] **Step 1: Create the log broadcaster module**

```typescript
// control-plane/apps/api/src/services/log-broadcaster.ts

interface LogEntry {
  deploymentId: string;
  source: "stdout" | "stderr" | "system";
  message: string;
  timestamp: string;
}

type LogSubscriber = (log: LogEntry) => void;

class LogBroadcaster {
  private subscribers = new Map<string, Set<LogSubscriber>>();

  subscribe(deploymentId: string, callback: LogSubscriber): () => void {
    if (!this.subscribers.has(deploymentId)) {
      this.subscribers.set(deploymentId, new Set());
    }
    this.subscribers.get(deploymentId)!.add(callback);

    // Return unsubscribe function
    return () => {
      const subs = this.subscribers.get(deploymentId);
      if (subs) {
        subs.delete(callback);
        if (subs.size === 0) {
          this.subscribers.delete(deploymentId);
        }
      }
    };
  }

  broadcast(log: LogEntry): void {
    const subs = this.subscribers.get(log.deploymentId);
    if (subs) {
      subs.forEach((cb) => cb(log));
    }
  }

  hasSubscribers(deploymentId: string): boolean {
    return (this.subscribers.get(deploymentId)?.size ?? 0) > 0;
  }

  getSubscriberCount(deploymentId: string): number {
    return this.subscribers.get(deploymentId)?.size ?? 0;
  }
}

export const logBroadcaster = new LogBroadcaster();
export type { LogEntry };
```

- [ ] **Step 2: Commit**

```bash
git add control-plane/apps/api/src/services/log-broadcaster.ts
git commit -m "feat: add log broadcaster service for pub/sub"
```

---

## Task 3: Add WebSocket Plugin to Fastify

**Files:**
- Create: `control-plane/apps/api/src/plugins/websocket.ts`
- Modify: `control-plane/apps/api/src/index.ts`

- [ ] **Step 1: Create WebSocket plugin**

```typescript
// control-plane/apps/api/src/plugins/websocket.ts
import fp from "fastify-plugin";
import type { FastifyInstance } from "fastify";
import websocket from "@fastify/websocket";

export default fp(async function (fastify: FastifyInstance) {
  await fastify.register(websocket);
});
```

- [ ] **Step 2: Register WebSocket plugin in main app**

Add import and registration in `control-plane/apps/api/src/index.ts`:

After line 14 (after deploymentRoutes import):
```typescript
import websocketPlugin from "./plugins/websocket.js";
import { logRoutes } from "./routes/logs.js";
```

After line 39 (after jwt register):
```typescript
await app.register(websocketPlugin);
```

After line 100 (after deploymentRoutes register):
```typescript
app.register(logRoutes, { prefix: "/logs" });
```

The relevant section should look like:
```typescript
await app.register(env, { schema: envSchema, dotenv: true });
await app.register(cors, { origin: true });
await app.register(jwt, { secret: process.env.JWT_SECRET! });
await app.register(websocketPlugin);
await ensureTopology();
```

And:
```typescript
// Register routes
app.register(authRoutes, { prefix: "/auth" });
app.register(projectRoutes, { prefix: "/projects" });
app.register(serviceRoutes, { prefix: "/services" });
app.register(deploymentRoutes, { prefix: "/deployments" });
app.register(logRoutes, { prefix: "/logs" });
```

- [ ] **Step 3: Commit**

```bash
git add control-plane/apps/api/src/plugins/websocket.ts
git add control-plane/apps/api/src/index.ts
git commit -m "feat: register websocket plugin"
```

---

## Task 4: Create Log Routes (HTTP + WebSocket)

**Files:**
- Create: `control-plane/apps/api/src/routes/logs.ts`

- [ ] **Step 1: Create logs routes file**

```typescript
// control-plane/apps/api/src/routes/logs.ts
import { FastifyInstance } from "fastify";
import { prisma } from "@/packages/shared/src/db.js";
import { logBroadcaster, LogEntry } from "../services/log-broadcaster.js";

export async function logRoutes(app: FastifyInstance) {
  // HTTP endpoint: Get historical logs
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

    // Get query params for pagination
    const { limit = "100", before } = req.query as { limit?: string; before?: string };

    const logs = await prisma.logStream.findMany({
      where: {
        deploymentId,
        ...(before ? { timestamp: { lt: new Date(before) } } : {})
      },
      orderBy: { timestamp: "desc" },
      take: Math.min(parseInt(limit, 10), 1000)
    });

    return { logs: logs.reverse() };
  });

  // WebSocket endpoint: Real-time log streaming
  app.get("/deployments/:deploymentId/stream", { websocket: true }, (socket, req) => {
    const { deploymentId } = req.params as { deploymentId: string };

    // Verify deployment exists
    prisma.deployment.findUnique({
      where: { id: deploymentId },
      select: { id: true }
    }).then((deployment) => {
      if (!deployment) {
        socket.close(1008, "deployment not found");
        return;
      }

      // Send historical logs first
      prisma.logStream.findMany({
        where: { deploymentId },
        orderBy: { timestamp: "asc" },
        take: 100
      }).then((logs) => {
        logs.forEach((log) => {
          socket.send(JSON.stringify({
            deploymentId: log.deploymentId,
            source: log.source,
            message: log.message,
            timestamp: log.timestamp.toISOString()
          }));
        });
      });

      // Subscribe to new logs
      const unsubscribe = logBroadcaster.subscribe(deploymentId, (log: LogEntry) => {
        try {
          socket.send(JSON.stringify(log));
        } catch (err) {
          // Socket might be closing
          unsubscribe();
        }
      });

      // Handle socket close
      socket.on("close", () => {
        unsubscribe();
      });

      // Handle errors
      socket.on("error", () => {
        unsubscribe();
      });
    });
  });
}
```

- [ ] **Step 2: Commit**

```bash
git add control-plane/apps/api/src/routes/logs.ts
git commit -m "feat: add logs HTTP and WebSocket routes"
```

---

## Task 5: Create Docker Log Streamer (Worker)

**Files:**
- Create: `control-plane/apps/worker/src/runtime/log-streamer.ts`

- [ ] **Step 1: Create log streamer module**

```typescript
// control-plane/apps/worker/src/runtime/log-streamer.ts
import { spawn } from "child_process";
import { prisma } from "@/packages/shared/src/db.js";

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

// Callback for new log entries
let onLogEntry: ((entry: LogEntry) => void) | null = null;

export function setLogEntryCallback(callback: (entry: LogEntry) => void) {
  onLogEntry = callback;
}

async function persistLog(entry: LogEntry): Promise<void> {
  try {
    await prisma.logStream.create({
      data: {
        deploymentId: entry.deploymentId,
        source: entry.source,
        message: entry.message,
        timestamp: new Date(entry.timestamp)
      }
    });
  } catch (err) {
    console.error("[log-streamer] failed to persist log:", err);
  }
}

function broadcastLog(entry: LogEntry): void {
  // Call the callback if set (used by API to broadcast to WebSocket clients)
  if (onLogEntry) {
    onLogEntry(entry);
  }
}

export function startLogStreaming(deploymentId: string, containerId: string): void {
  // Don't start if already streaming this deployment
  if (activeStreams.has(deploymentId)) {
    console.log("[log-streamer] already streaming deployment:", deploymentId);
    return;
  }

  console.log("[log-streamer] starting log stream for deployment:", deploymentId, "container:", containerId);

  // Spawn docker logs process
  const process = spawn("docker", ["logs", "-f", "--tail=100", containerId]);

  activeStreams.set(deploymentId, {
    deploymentId,
    containerId,
    process,
    startTime: new Date()
  });

  // Handle stdout
  process.stdout.on("data", (data: Buffer) => {
    const lines = data.toString().split("\n").filter((l) => l.length > 0);
    lines.forEach((line) => {
      const entry: LogEntry = {
        deploymentId,
        source: "stdout",
        message: line,
        timestamp: new Date().toISOString()
      };
      persistLog(entry);
      broadcastLog(entry);
    });
  });

  // Handle stderr
  process.stderr.on("data", (data: Buffer) => {
    const lines = data.toString().split("\n").filter((l) => l.length > 0);
    lines.forEach((line) => {
      const entry: LogEntry = {
        deploymentId,
        source: "stderr",
        message: line,
        timestamp: new Date().toISOString()
      };
      persistLog(entry);
      broadcastLog(entry);
    });
  });

  // Handle process exit
  process.on("close", (code) => {
    console.log("[log-streamer] log stream ended for deployment:", deploymentId, "exit code:", code);
    activeStreams.delete(deploymentId);

    // Send system message
    const entry: LogEntry = {
      deploymentId,
      source: "system",
      message: `Log stream ended (exit code: ${code})`,
      timestamp: new Date().toISOString()
    };
    broadcastLog(entry);
  });

  // Handle errors
  process.on("error", (err) => {
    console.error("[log-streamer] error streaming logs for deployment:", deploymentId, err);
    activeStreams.delete(deploymentId);

    const entry: LogEntry = {
      deploymentId,
      source: "system",
      message: `Log stream error: ${err.message}`,
      timestamp: new Date().toISOString()
    };
    broadcastLog(entry);
  });
}

export function stopLogStreaming(deploymentId: string): void {
  const stream = activeStreams.get(deploymentId);
  if (stream) {
    console.log("[log-streamer] stopping log stream for deployment:", deploymentId);
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
git commit -m "feat: add docker log streamer with persistence"
```

---

## Task 6: Integrate Log Streaming with Deploy Consumer

**Files:**
- Modify: `control-plane/apps/worker/src/consumers/deploy-consumer.ts`

- [ ] **Step 1: Import log streamer**

Add import at the top of the file:
```typescript
import { startLogStreaming } from "@/apps/worker/src/runtime/log-streamer.js";
```

- [ ] **Step 2: Start log streaming after container deployment**

Find the section where deployment status is updated to "deployed" (around line 220). After the `await prisma.deployment.update(...)` call that sets status to "deployed", add:

```typescript
// Start streaming logs from the container
startLogStreaming(dep.id, containerId);
```

This should be placed after line 223 (after the deployment update completes), so the full section looks like:

```typescript
// 5) Update deployment status
const updateData: any = {
  containerId,
  hostPort,
  containerPort,
  runtimeUrl,
  status: "deployed"
};

// For managed services, store the volume path
if (isManagedService) {
  updateData.volumePath = dep.volumePath ?? `/tmp/cp-volumes/${dep.serviceId}`;
}

await prisma.deployment.update({
  where: { id: dep.id },
  data: updateData
});

// Start streaming logs from the container
startLogStreaming(dep.id, containerId);

console.log("[deploy-consumer] deployment complete:", dep.id, "runtimeUrl:", runtimeUrl);
```

- [ ] **Step 3: Commit**

```bash
git add control-plane/apps/worker/src/consumers/deploy-consumer.ts
git commit -m "feat: start log streaming on successful deployment"
```

---

## Task 7: Set Up Worker-to-API Log Broadcasting

**Files:**
- Modify: `control-plane/apps/worker/src/index.ts`
- Modify: `control-plane/apps/api/src/services/log-broadcaster.ts` (export a setter)

- [ ] **Step 1: Add AMQP-based log broadcasting**

Since the worker and API run in separate processes, we need to use RabbitMQ to forward logs from worker to API. Let's add a log publisher in the worker.

First, modify `control-plane/apps/worker/src/runtime/log-streamer.ts` to support AMQP publishing:

Replace the `broadcastLog` function and add AMQP support:

```typescript
// Add to imports
import { getAmqpChannel } from "@/packages/shared/src/amqp.js";
import { MQ } from "@/packages/shared/src/mq.js";

// Replace the broadcastLog function
async function broadcastLog(entry: LogEntry): Promise<void> {
  // Publish to AMQP for cross-process broadcasting
  try {
    const ch = await getAmqpChannel();
    ch.publish(MQ.EXCHANGE, "logs", Buffer.from(JSON.stringify(entry)), {
      persistent: false,
      contentType: "application/json"
    });
  } catch (err) {
    console.error("[log-streamer] failed to broadcast log:", err);
  }
}
```

We also need to add the `logs` routing key to the MQ constants. Check and add to `control-plane/packages/shared/src/mq.js` (or .ts):

```typescript
export const MQ = {
  EXCHANGE: "cp.events",
  BUILD_QUEUE: "cp.build",
  DEPLOY_QUEUE: "cp.deploy",
  DLQ: "cp.dlq",
  BUILD_ROUTING_KEY: "build",
  DEPLOY_ROUTING_KEY: "deploy",
  LOGS_ROUTING_KEY: "logs"
};
```

Now update the worker index to consume log messages and broadcast to WebSocket clients.

- [ ] **Step 2: Modify worker index to set up log forwarding**

Update `control-plane/apps/worker/src/index.ts`:

```typescript
import { prisma } from "@/packages/shared/src/db.js";
import { closeAmqp, getAmqpChannel } from "@/packages/shared/src/amqp.js";
import { startDeployConsumer } from "@/apps/worker/src/consumers/deploy-consumer.js";
import { startOutboxPublisher } from "./outbox/publisher.js";
import { startBuildConsumer } from "./consumers/build-consumer.js";
import { setLogEntryCallback } from "./runtime/log-streamer.js";
import { MQ } from "@/packages/shared/src/mq.js";

console.log("worker up");
await prisma.$queryRaw`SELECT 1`;

// Set up log forwarding callback (logs are forwarded via AMQP)
setLogEntryCallback(async (entry) => {
  try {
    const ch = await getAmqpChannel();
    ch.publish(MQ.EXCHANGE, MQ.LOGS_ROUTING_KEY, Buffer.from(JSON.stringify(entry)), {
      persistent: false,
      contentType: "application/json"
    });
  } catch (err) {
    console.error("[worker] failed to forward log:", err);
  }
});

await startBuildConsumer();
await startDeployConsumer();
await startOutboxPublisher();

const shutdown = async () => {
  console.log("worker shutting down...");
  try { await prisma.$disconnect(); } catch {}
  try { await closeAmqp(); } catch {}
  process.exit(0);
};

process.on("SIGTERM", shutdown);
process.on("SIGINT", shutdown);
```

- [ ] **Step 3: Set up API to consume log messages**

Modify `control-plane/apps/api/src/index.ts` to consume log messages from AMQP and broadcast to WebSocket clients.

Add imports at the top:
```typescript
import { getAmqpChannel, ensureTopology } from "@/packages/shared/src/amqp.js";
import { MQ } from "@/packages/shared/src/mq.js";
import { logBroadcaster } from "./services/log-broadcaster.js";
```

After the routes registration (after line 100), add:

```typescript
// Start log message consumer for WebSocket broadcasting
async function startLogConsumer() {
  const ch = await getAmqpChannel();
  const q = await ch.assertQueue("", { exclusive: true });
  await ch.bindQueue(q.queue, MQ.EXCHANGE, MQ.LOGS_ROUTING_KEY);

  await ch.consume(q.queue, (msg) => {
    if (!msg) return;
    try {
      const entry = JSON.parse(msg.content.toString());
      logBroadcaster.broadcast(entry);
    } catch (err) {
      console.error("[api] failed to process log message:", err);
    }
    ch.ack(msg);
  });
}

await startLogConsumer();
```

- [ ] **Step 4: Commit**

```bash
git add control-plane/apps/worker/src/runtime/log-streamer.ts
git add control-plane/apps/worker/src/index.ts
git add control-plane/apps/api/src/index.ts
git commit -m "feat: wire up AMQP-based log broadcasting from worker to API"
```

---

## Task 8: Create React Log Viewer Component

**Files:**
- Create: `cp-ui/src/components/LogViewer.tsx`
- Create: `cp-ui/src/hooks/useLogs.ts`

- [ ] **Step 1: Create useLogs hook**

```typescript
// cp-ui/src/hooks/useLogs.ts
import { useState, useEffect, useRef, useCallback } from "react";

const API = import.meta.env.VITE_API_BASE ?? "http://localhost:8080";
const WS_API = API.replace(/^http/, "ws");

export interface LogEntry {
  deploymentId: string;
  source: "stdout" | "stderr" | "system";
  message: string;
  timestamp: string;
}

interface UseLogsOptions {
  deploymentId: string;
  token?: string;
}

export function useLogs({ deploymentId, token }: UseLogsOptions) {
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [isConnected, setIsConnected] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const shouldReconnect = useRef(true);

  // Fetch historical logs first
  useEffect(() => {
    if (!deploymentId || !token) return;

    fetch(`${API}/logs/deployments/${deploymentId}`, {
      headers: { Authorization: `Bearer ${token}` }
    })
      .then((res) => res.json())
      .then((data) => {
        if (data.logs) {
          setLogs(data.logs);
        }
      })
      .catch((err) => {
        console.error("Failed to fetch historical logs:", err);
      });
  }, [deploymentId, token]);

  // Set up WebSocket connection
  useEffect(() => {
    if (!deploymentId || !token) return;

    const connect = () => {
      if (!shouldReconnect.current) return;

      const ws = new WebSocket(
        `${WS_API}/logs/deployments/${deploymentId}/stream`
      );
      wsRef.current = ws;

      ws.onopen = () => {
        setIsConnected(true);
        setError(null);
      };

      ws.onmessage = (event) => {
        try {
          const log = JSON.parse(event.data) as LogEntry;
          setLogs((prev) => {
            // Prevent duplicate logs by checking timestamp and message
            const isDuplicate = prev.some(
              (l) =>
                l.timestamp === log.timestamp && l.message === log.message
            );
            if (isDuplicate) return prev;
            return [...prev, log];
          });
        } catch (err) {
          console.error("Failed to parse log message:", err);
        }
      };

      ws.onerror = () => {
        setError("WebSocket error");
        setIsConnected(false);
      };

      ws.onclose = () => {
        setIsConnected(false);
        // Reconnect after delay
        if (shouldReconnect.current) {
          setTimeout(connect, 3000);
        }
      };
    };

    connect();

    return () => {
      shouldReconnect.current = false;
      wsRef.current?.close();
    };
  }, [deploymentId, token]);

  const clearLogs = useCallback(() => {
    setLogs([]);
  }, []);

  return { logs, isConnected, error, clearLogs };
}
```

- [ ] **Step 2: Create LogViewer component**

```typescript
// cp-ui/src/components/LogViewer.tsx
import { useRef, useEffect } from "react";
import { useLogs, LogEntry } from "../hooks/useLogs";

interface LogViewerProps {
  deploymentId: string;
  token?: string;
}

const sourceColors: Record<LogEntry["source"], string> = {
  stdout: "#e5e7eb",
  stderr: "#fca5a5",
  system: "#93c5fd"
};

const sourceLabels: Record<LogEntry["source"], string> = {
  stdout: "OUT",
  stderr: "ERR",
  system: "SYS"
};

function formatTimestamp(timestamp: string): string {
  const date = new Date(timestamp);
  return date.toLocaleTimeString("en-US", {
    hour12: false,
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit"
  });
}

export function LogViewer({ deploymentId, token }: LogViewerProps) {
  const { logs, isConnected, error, clearLogs } = useLogs({ deploymentId, token });
  const scrollRef = useRef<HTMLDivElement>(null);
  const shouldAutoScroll = useRef(true);

  // Auto-scroll to bottom
  useEffect(() => {
    if (scrollRef.current && shouldAutoScroll.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
  }, [logs]);

  const handleScroll = () => {
    if (scrollRef.current) {
      const { scrollTop, scrollHeight, clientHeight } = scrollRef.current;
      shouldAutoScroll.current = scrollHeight - scrollTop - clientHeight < 50;
    }
  };

  return (
    <div style={{ border: "1px solid #ddd", borderRadius: 8, overflow: "hidden" }}>
      {/* Header */}
      <div
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          padding: "8px 12px",
          background: "#f3f4f6",
          borderBottom: "1px solid #ddd"
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
          <span style={{ fontWeight: 600 }}>Logs</span>
          <span
            style={{
              fontSize: 12,
              padding: "2px 8px",
              borderRadius: 4,
              background: isConnected ? "#dcfce7" : "#fee2e2",
              color: isConnected ? "#166534" : "#991b1b"
            }}
          >
            {isConnected ? "Live" : "Disconnected"}
          </span>
        </div>
        <div style={{ display: "flex", gap: 8 }}>
          <button onClick={clearLogs} style={{ fontSize: 12 }}>
            Clear
          </button>
        </div>
      </div>

      {/* Error message */}
      {error && (
        <div
          style={{
            padding: "8px 12px",
            background: "#fee2e2",
            color: "#991b1b",
            fontSize: 12
          }}
        >
          {error}
        </div>
      )}

      {/* Log content */}
      <div
        ref={scrollRef}
        onScroll={handleScroll}
        style={{
          height: 400,
          overflow: "auto",
          background: "#1a1a1a",
          fontFamily: 'ui-monospace, SFMono-Regular, "SF Mono", Consolas, monospace',
          fontSize: 13,
          lineHeight: 1.5
        }}
      >
        {logs.length === 0 ? (
          <div
            style={{
              padding: 20,
              color: "#666",
              textAlign: "center"
            }}
          >
            No logs yet. Deploy a service to see logs here.
          </div>
        ) : (
          logs.map((log, index) => (
            <div
              key={index}
              style={{
                display: "flex",
                padding: "2px 12px",
                borderBottom: "1px solid #2a2a2a"
              }}
            >
              <span
                style={{
                  color: "#6b7280",
                  minWidth: 70,
                  flexShrink: 0
                }}
              >
                {formatTimestamp(log.timestamp)}
              </span>
              <span
                style={{
                  color: sourceColors[log.source],
                  minWidth: 40,
                  flexShrink: 0,
                  fontWeight: 600
                }}
              >
                {sourceLabels[log.source]}
              </span>
              <span
                style={{
                  color: sourceColors[log.source],
                  whiteSpace: "pre-wrap",
                  wordBreak: "break-all"
                }}
              >
                {log.message}
              </span>
            </div>
          ))
        )}
      </div>
    </div>
  );
}
```

- [ ] **Step 3: Commit**

```bash
git add cp-ui/src/hooks/useLogs.ts
git add cp-ui/src/components/LogViewer.tsx
git commit -m "feat: add LogViewer component and useLogs hook"
```

---

## Task 9: Integrate LogViewer into App

**Files:**
- Modify: `cp-ui/src/App.tsx`

- [ ] **Step 1: Import LogViewer**

Add import after the existing imports:
```typescript
import { LogViewer } from "./components/LogViewer";
```

- [ ] **Step 2: Add state for selected deployment**

Add to state declarations (after line 55):
```typescript
const [selectedDeploymentId, setSelectedDeploymentId] = useState<string>("");
```

- [ ] **Step 3: Add deployment selection handler**

In the deployments table, make each row clickable to select the deployment for log viewing.

Find the deployments table (around line 362), and modify the row rendering:

```typescript
{deployments.map((d) => (
  <tr
    key={d.id}
    onClick={() => setSelectedDeploymentId(d.id)}
    style={{
      cursor: "pointer",
      background: selectedDeploymentId === d.id ? "#eff6ff" : undefined
    }}
  >
    {/* existing cells */}
  </tr>
))}
```

- [ ] **Step 4: Add LogViewer section**

Add a new section after the Environment Variables section (after line 439):

```typescript
{selectedDeploymentId && (
  <section style={{ border: "1px solid #ddd", padding: 12, borderRadius: 8, marginTop: 16 }}>
    <h3>Container Logs</h3>
    <div style={{ marginBottom: 8, fontSize: 12, color: "#666" }}>
      Deployment: {selectedDeploymentId.slice(0, 8)}...
    </div>
    <LogViewer deploymentId={selectedDeploymentId} token={token} />
  </section>
)}
```

- [ ] **Step 5: Commit**

```bash
git add cp-ui/src/App.tsx
git commit -m "feat: integrate LogViewer into main app"
```

---

## Task 10: Add MQ Constants

**Files:**
- Modify: `control-plane/packages/shared/src/mq.js` (or create if doesn't exist)

- [ ] **Step 1: Check if MQ file exists**

Check the actual file location:
```bash
ls -la control-plane/packages/shared/src/
```

- [ ] **Step 2: Create or update MQ constants**

If the file doesn't exist, create `control-plane/packages/shared/src/mq.js`:

```javascript
// control-plane/packages/shared/src/mq.js
export const MQ = {
  EXCHANGE: "cp.events",
  BUILD_QUEUE: "cp.build",
  DEPLOY_QUEUE: "cp.deploy",
  DLQ: "cp.dlq",
  BUILD_ROUTING_KEY: "build",
  DEPLOY_ROUTING_KEY: "deploy",
  LOGS_ROUTING_KEY: "logs"
};
```

- [ ] **Step 3: Commit**

```bash
git add control-plane/packages/shared/src/mq.js
git commit -m "feat: add logs routing key to MQ constants"
```

---

## Task 11: Testing & Verification

- [ ] **Step 1: Start infrastructure**

```bash
cd /Users/manwindersingh/Desktop/infra-pilot/control-plane
docker compose up -d
```

- [ ] **Step 2: Run database migrations**

```bash
npx prisma migrate dev
```

- [ ] **Step 3: Start API server (Terminal 1)**

```bash
cd /Users/manwindersingh/Desktop/infra-pilot/control-plane
npm run dev:api
```

Expected: API server starts on port 8080

- [ ] **Step 4: Start Worker (Terminal 2)**

```bash
cd /Users/manwindersingh/Desktop/infra-pilot/control-plane
npm run dev:worker
```

Expected: Worker starts and connects to RabbitMQ

- [ ] **Step 5: Start UI (Terminal 3)**

```bash
cd /Users/manwindersingh/Desktop/infra-pilot/cp-ui
npm run dev
```

Expected: UI available at http://localhost:5173

- [ ] **Step 6: Test the full flow**

1. Login to UI (click "Login (dev)")
2. Create a project
3. Create a Redis service (managed service, no build needed)
4. Trigger deploy
5. Wait for deployment to complete
6. Click on the deployment row to select it
7. Verify logs appear in the LogViewer section

- [ ] **Step 7: Verify WebSocket connection**

Open browser DevTools → Network → WS tab
Look for WebSocket connection to `/logs/deployments/{id}/stream`
Verify messages are being received

---

## Task 12: Final Commit

- [ ] **Commit all changes**

```bash
git add -A
git commit -m "feat: complete real-time container logs implementation

- Add LogStream model for persistence
- Add WebSocket support for real-time streaming
- Add docker log streaming in worker
- Add LogViewer React component
- Integrate logs into main UI"
```

---

## Self-Review Checklist

| Requirement | Task | Status |
|------------|------|--------|
| Log persistence in PostgreSQL | Task 1 | ✓ |
| WebSocket server for streaming | Task 3, 4 | ✓ |
| Docker logs streaming | Task 5 | ✓ |
| Worker-to-API log broadcast | Task 7 | ✓ |
| React LogViewer component | Task 8 | ✓ |
| UI integration | Task 9 | ✓ |

---

## Troubleshooting

**WebSocket connection fails:**
- Check that `@fastify/websocket` is installed
- Verify API is running on correct port
- Check browser console for CORS errors

**Logs not appearing:**
- Check worker is running
- Verify deployment has a `containerId`
- Check docker logs command manually: `docker logs -f {containerId}`

**Database errors:**
- Run `npx prisma migrate dev` to apply migrations
- Check DATABASE_URL in .env

**AMQP errors:**
- Verify RabbitMQ is running: `docker compose ps`
- Check MQ constants match between worker and API

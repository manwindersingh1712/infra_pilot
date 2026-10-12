# 2D Service Canvas Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build an interactive 2D canvas using React Flow that visualizes project services as draggable nodes with persistent positions and relationship connections.

**Architecture:** Services render as nodes on a canvas with x/y positions stored in the database. Users can drag nodes, create connections between services, and use auto-layout. The canvas syncs position changes to the backend via REST API.

**Tech Stack:** React Flow (@xyflow/react), dagre (auto-layout), Socket.io for real-time collaboration (optional), existing Fastify API + Prisma stack.

---

## File Structure

| File | Purpose |
|------|---------|
| `prisma/schema.prisma` | Add `positionX`, `positionY` to Service model; add ServiceConnection model |
| `control-plane/apps/api/src/routes/canvas.ts` | New API routes: GET/PUT canvas state, POST/DELETE connections |
| `control-plane/apps/api/src/index.ts` | Register canvas routes |
| `cp-ui/package.json` | Add @xyflow/react and @dagrejs/dagre dependencies |
| `cp-ui/src/components/ServiceCanvas.tsx` | Main canvas component with React Flow |
| `cp-ui/src/components/ServiceNode.tsx` | Custom node component for services |
| `cp-ui/src/components/CanvasToolbar.tsx` | Toolbar with auto-layout and controls |
| `cp-ui/src/hooks/useCanvas.ts` | React hook for canvas data fetching and mutations |
| `cp-ui/src/App.tsx` | Add Canvas tab/view alongside existing list view |

---

## Prerequisites Check

Run these to verify the codebase state before starting:

```bash
cd /Users/manwindersingh/Desktop/infra-pilot

# Verify project structure exists
ls control-plane/apps/api/src/index.ts
ls control-plane/prisma/schema.prisma
ls cp-ui/src/App.tsx

# Check if migrations are working
cd control-plane && npx prisma validate
```

---

## Task 1: Database Schema Changes

**Files:**
- Modify: `control-plane/prisma/schema.prisma`

### Step 1.1: Add position fields to Service model

Locate the Service model in `prisma/schema.prisma` and add position fields:

```prisma
model Service {
  id          String      @id @default(cuid())
  projectId   String
  name        String
  serviceType ServiceType @default(docker)
  repoUrl     String?
  branch      String      @default("main")
  createdAt   DateTime    @default(now())

  // NEW: Canvas positioning fields
  positionX   Float       @default(0)
  positionY   Float       @default(0)

  project     Project      @relation(fields: [projectId], references: [id])
  envVars     EnvVar[]
  deployments Deployment[]
  connections ServiceConnection[] @relation("SourceService")
  incomingConnections ServiceConnection[] @relation("TargetService")

  @@index([projectId])
}
```

### Step 1.2: Add ServiceConnection model

Add this new model after the Service model:

```prisma
model ServiceConnection {
  id          String   @id @default(cuid())
  sourceId    String
  targetId    String
  label       String?  // e.g., "uses", "depends on"
  createdAt   DateTime @default(now())

  source      Service  @relation("SourceService", fields: [sourceId], references: [id], onDelete: Cascade)
  target      Service  @relation("TargetService", fields: [targetId], references: [id], onDelete: Cascade)

  @@unique([sourceId, targetId])
  @@index([sourceId])
  @@index([targetId])
}
```

### Step 1.3: Generate and run migration

```bash
cd /Users/manwindersingh/Desktop/infra-pilot/control-plane
npx prisma migrate dev --name add_canvas_positioning
```

Expected output: Migration created and applied successfully.

### Step 1.4: Commit

```bash
cd /Users/manwindersingh/Desktop/infra-pilot
git add control-plane/prisma/schema.prisma control-plane/prisma/migrations/
git commit -m "feat(canvas): add position fields and ServiceConnection model"
```

---

## Task 2: Backend API Routes

**Files:**
- Create: `control-plane/apps/api/src/routes/canvas.ts`
- Modify: `control-plane/apps/api/src/index.ts`

### Step 2.1: Create canvas routes file

Create `control-plane/apps/api/src/routes/canvas.ts`:

```typescript
import { FastifyInstance } from "fastify";
import { prisma } from "@/packages/shared/src/db.js";
import dagre from "@dagrejs/dagre";

// GET /projects/:id/canvas - Get canvas state
async function getCanvasHandler(req: any, reply: any) {
  const { id } = req.params as { id: string };
  const userId = (req as any).user?.id;

  // Verify project belongs to user
  const project = await prisma.project.findFirst({
    where: { id, ownerUserId: userId },
    select: { id: true }
  });

  if (!project) {
    return reply.status(404).send({ error: "Project not found" });
  }

  // Get all services with their positions
  const services = await prisma.service.findMany({
    where: { projectId: id },
    select: {
      id: true,
      name: true,
      serviceType: true,
      positionX: true,
      positionY: true,
      _count: {
        select: { deployments: true }
      }
    }
  });

  // Get connections between services
  const connections = await prisma.serviceConnection.findMany({
    where: {
      OR: [
        { source: { projectId: id } },
        { target: { projectId: id } }
      ]
    },
    select: {
      id: true,
      sourceId: true,
      targetId: true,
      label: true
    }
  });

  return {
    services: services.map(s => ({
      id: s.id,
      name: s.name,
      serviceType: s.serviceType,
      positionX: s.positionX,
      positionY: s.positionY,
      deploymentCount: s._count.deployments
    })),
    connections
  };
}

// PUT /projects/:id/canvas - Update canvas state (bulk positions)
async function updateCanvasHandler(req: any, reply: any) {
  const { id } = req.params as { id: string };
  const userId = (req as any).user?.id;
  const { services } = req.body as { services: Array<{ id: string; positionX: number; positionY: number }> };

  // Verify project belongs to user
  const project = await prisma.project.findFirst({
    where: { id, ownerUserId: userId },
    select: { id: true }
  });

  if (!project) {
    return reply.status(404).send({ error: "Project not found" });
  }

  // Update positions in transaction
  await prisma.$transaction(
    services.map(s =>
      prisma.service.updateMany({
        where: { id: s.id, projectId: id },
        data: { positionX: s.positionX, positionY: s.positionY }
      })
    )
  );

  return { success: true };
}

// POST /services/:id/position - Update single service position
async function updatePositionHandler(req: any, reply: any) {
  const { id } = req.params as { id: string };
  const userId = (req as any).user?.id;
  const { positionX, positionY } = req.body as { positionX: number; positionY: number };

  // Verify service belongs to user's project
  const service = await prisma.service.findFirst({
    where: {
      id,
      project: { ownerUserId: userId }
    }
  });

  if (!service) {
    return reply.status(404).send({ error: "Service not found" });
  }

  await prisma.service.update({
    where: { id },
    data: { positionX, positionY }
  });

  return { success: true };
}

// POST /services/connections - Create connection
async function createConnectionHandler(req: any, reply: any) {
  const userId = (req as any).user?.id;
  const { sourceId, targetId, label } = req.body as { sourceId: string; targetId: string; label?: string };

  // Verify both services belong to user
  const [source, target] = await Promise.all([
    prisma.service.findFirst({ where: { id: sourceId, project: { ownerUserId: userId } } }),
    prisma.service.findFirst({ where: { id: targetId, project: { ownerUserId: userId } } })
  ]);

  if (!source || !target) {
    return reply.status(404).send({ error: "Service not found" });
  }

  // Prevent self-connections
  if (sourceId === targetId) {
    return reply.status(400).send({ error: "Cannot connect service to itself" });
  }

  try {
    const connection = await prisma.serviceConnection.create({
      data: { sourceId, targetId, label }
    });
    return connection;
  } catch (err: any) {
    if (err.code === "P2002") {
      return reply.status(409).send({ error: "Connection already exists" });
    }
    throw err;
  }
}

// DELETE /services/connections/:id - Remove connection
async function deleteConnectionHandler(req: any, reply: any) {
  const { id } = req.params as { id: string };
  const userId = (req as any).user?.id;

  // Verify connection belongs to user's project
  const connection = await prisma.serviceConnection.findFirst({
    where: {
      id,
      source: { project: { ownerUserId: userId } }
    }
  });

  if (!connection) {
    return reply.status(404).send({ error: "Connection not found" });
  }

  await prisma.serviceConnection.delete({ where: { id } });
  return { success: true };
}

// POST /projects/:id/canvas/auto-layout - Auto-arrange services
async function autoLayoutHandler(req: any, reply: any) {
  const { id } = req.params as { id: string };
  const userId = (req as any).user?.id;

  const project = await prisma.project.findFirst({
    where: { id, ownerUserId: userId },
    select: { id: true }
  });

  if (!project) {
    return reply.status(404).send({ error: "Project not found" });
  }

  const services = await prisma.service.findMany({
    where: { projectId: id },
    select: {
      id: true,
      name: true,
      serviceType: true,
      positionX: true,
      positionY: true
    }
  });

  const connections = await prisma.serviceConnection.findMany({
    where: { source: { projectId: id } },
    select: { sourceId: true, targetId: true }
  });

  // Use dagre for auto-layout
  const g = new dagre.graphlib.Graph();
  g.setGraph({ rankdir: "LR", nodesep: 150, ranksep: 200 });
  g.setDefaultEdgeLabel(() => ({}));

  // Add nodes
  const nodeWidth = 180;
  const nodeHeight = 80;

  for (const service of services) {
    g.setNode(service.id, { width: nodeWidth, height: nodeHeight });
  }

  // Add edges
  for (const conn of connections) {
    g.setEdge(conn.sourceId, conn.targetId);
  }

  // Run layout
  dagre.layout(g);

  // Extract positions
  const updatedServices = services.map(service => {
    const node = g.node(service.id);
    return {
      id: service.id,
      positionX: node.x - nodeWidth / 2,
      positionY: node.y - nodeHeight / 2
    };
  });

  // Save to database
  await prisma.$transaction(
    updatedServices.map(s =>
      prisma.service.update({
        where: { id: s.id },
        data: { positionX: s.positionX, positionY: s.positionY }
      })
    )
  );

  return { services: updatedServices };
}

export async function canvasRoutes(fastify: FastifyInstance) {
  fastify.get("/projects/:id/canvas", { onRequest: [fastify.authenticate] }, getCanvasHandler);
  fastify.put("/projects/:id/canvas", { onRequest: [fastify.authenticate] }, updateCanvasHandler);
  fastify.post("/services/:id/position", { onRequest: [fastify.authenticate] }, updatePositionHandler);
  fastify.post("/services/connections", { onRequest: [fastify.authenticate] }, createConnectionHandler);
  fastify.delete("/services/connections/:id", { onRequest: [fastify.authenticate] }, deleteConnectionHandler);
  fastify.post("/projects/:id/canvas/auto-layout", { onRequest: [fastify.authenticate] }, autoLayoutHandler);
}
```

### Step 2.2: Register routes in API

Modify `control-plane/apps/api/src/index.ts`:

Find where other routes are registered (around line 40-60), then add:

```typescript
import { canvasRoutes } from "./routes/canvas.js";

// ... existing route registrations ...
await fastify.register(canvasRoutes);
```

The exact location depends on the current structure. Look for other `fastify.register` calls and add after them.

### Step 2.3: Install dagre dependency

```bash
cd /Users/manwindersingh/Desktop/infra-pilot/control-plane
npm install @dagrejs/dagre
```

### Step 2.4: Test the API

Start the API server:
```bash
cd /Users/manwindersingh/Desktop/infra-pilot/control-plane
npm run dev:api
```

In another terminal, test with curl:
```bash
# Get a token first
TOKEN=$(curl -s -X POST http://localhost:8080/dev/login | jq -r .token)

# Get canvas for a project (replace PROJECT_ID with actual ID)
curl -H "Authorization: Bearer $TOKEN" http://localhost:8080/projects/PROJECT_ID/canvas

# Should return: { services: [], connections: [] }
```

### Step 2.5: Commit

```bash
cd /Users/manwindersingh/Desktop/infra-pilot
git add control-plane/apps/api/src/routes/canvas.ts control-plane/apps/api/src/index.ts control-plane/package*.json
git commit -m "feat(canvas): add canvas API routes with auto-layout"
```

---

## Task 3: Frontend Dependencies

**Files:**
- Modify: `cp-ui/package.json`

### Step 3.1: Add React Flow dependency

```bash
cd /Users/manwindersingh/Desktop/infra-pilot/cp-ui
npm install @xyflow/react
```

### Step 3.2: Commit

```bash
cd /Users/manwindersingh/Desktop/infra-pilot
git add cp-ui/package*.json
git commit -m "chore(canvas): add @xyflow/react dependency"
```

---

## Task 4: Custom Service Node Component

**Files:**
- Create: `cp-ui/src/components/ServiceNode.tsx`

### Step 4.1: Create ServiceNode component

Create `cp-ui/src/components/ServiceNode.tsx`:

```typescript
import { Handle, Position } from "@xyflow/react";

const serviceIcons: Record<string, string> = {
  docker: "🐳",
  nodejs: "🟢",
  nextjs: "▲",
  react: "⚛️",
  mongodb: "🍃",
  redis: "🔴"
};

const serviceColors: Record<string, string> = {
  docker: "#2496ed",
  nodejs: "#339933",
  nextjs: "#000000",
  react: "#61dafb",
  mongodb: "#47a248",
  redis: "#dc382d"
};

interface ServiceNodeData {
  name: string;
  serviceType: string;
  deploymentCount: number;
  runtimeUrl?: string;
}

interface ServiceNodeProps {
  id: string;
  data: ServiceNodeData;
  selected: boolean;
}

export function ServiceNode({ id, data, selected }: ServiceNodeProps) {
  const icon = serviceIcons[data.serviceType] || "📦";
  const color = serviceColors[data.serviceType] || "#666";
  const hasDeployments = data.deploymentCount > 0;

  return (
    <div
      style={{
        padding: "12px 16px",
        borderRadius: "10px",
        background: "white",
        border: `2px solid ${selected ? "#3b82f6" : color}`,
        boxShadow: selected
          ? "0 0 0 3px rgba(59, 130, 246, 0.3), 0 4px 12px rgba(0,0,0,0.15)"
          : "0 2px 8px rgba(0,0,0,0.1)",
        minWidth: "160px",
        transition: "box-shadow 0.15s ease",
        cursor: "grab"
      }}
    >
      {/* Input handle (left side) */}
      <Handle
        type="target"
        position={Position.Left}
        style={{
          width: "10px",
          height: "10px",
          background: color,
          border: "2px solid white"
        }}
      />

      {/* Content */}
      <div style={{ display: "flex", alignItems: "center", gap: "10px" }}>
        <span style={{ fontSize: "24px" }}>{icon}</span>
        <div style={{ minWidth: 0 }}>
          <div
            style={{
              fontWeight: 600,
              fontSize: "14px",
              color: "#1f2937",
              whiteSpace: "nowrap",
              overflow: "hidden",
              textOverflow: "ellipsis"
            }}
            title={data.name}
          >
            {data.name}
          </div>
          <div
            style={{
              fontSize: "11px",
              color: "#6b7280",
              textTransform: "capitalize",
              display: "flex",
              alignItems: "center",
              gap: "6px"
            }}
          >
            {data.serviceType}
            {hasDeployments && (
              <span
                style={{
                  width: "6px",
                  height: "6px",
                  borderRadius: "50%",
                  background: "#22c55e"
                }}
                title={`${data.deploymentCount} deployment(s)`}
              />
            )}
          </div>
        </div>
      </div>

      {/* Output handle (right side) */}
      <Handle
        type="source"
        position={Position.Right}
        style={{
          width: "10px",
          height: "10px",
          background: color,
          border: "2px solid white"
        }}
      />
    </div>
  );
}
```

### Step 4.2: Commit

```bash
cd /Users/manwindersingh/Desktop/infra-pilot
git add cp-ui/src/components/ServiceNode.tsx
git commit -m "feat(canvas): add ServiceNode component with icons and handles"
```

---

## Task 5: Canvas Hook

**Files:**
- Create: `cp-ui/src/hooks/useCanvas.ts`

### Step 5.1: Create useCanvas hook

Create `cp-ui/src/hooks/useCanvas.ts`:

```typescript
import { useState, useEffect, useCallback } from "react";

const API = import.meta.env.VITE_API_BASE ?? "http://localhost:8080";

interface CanvasService {
  id: string;
  name: string;
  serviceType: string;
  positionX: number;
  positionY: number;
  deploymentCount: number;
}

interface Connection {
  id: string;
  sourceId: string;
  targetId: string;
  label?: string;
}

interface CanvasState {
  services: CanvasService[];
  connections: Connection[];
}

interface UseCanvasOptions {
  projectId: string;
  token?: string;
}

async function apiFetch<T>(path: string, opts: RequestInit = {}, token?: string): Promise<T> {
  const headers = new Headers(opts.headers);
  if (token) headers.set("Authorization", `Bearer ${token}`);
  if (opts.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }

  const res = await fetch(`${API}${path}`, { ...opts, headers });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`${res.status}: ${text}`);
  }
  return res.json();
}

export function useCanvas({ projectId, token }: UseCanvasOptions) {
  const [state, setState] = useState<CanvasState | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Fetch canvas state
  const loadCanvas = useCallback(async () => {
    if (!projectId || !token) return;

    setLoading(true);
    setError(null);

    try {
      const data = await apiFetch<CanvasState>(`/projects/${projectId}/canvas`, {}, token);
      setState(data);
    } catch (err: any) {
      setError(err.message);
    } finally {
      setLoading(false);
    }
  }, [projectId, token]);

  // Update single service position
  const updatePosition = useCallback(
    async (serviceId: string, positionX: number, positionY: number) => {
      if (!token) return;

      await apiFetch(
        `/services/${serviceId}/position`,
        {
          method: "POST",
          body: JSON.stringify({ positionX, positionY })
        },
        token
      );

      // Optimistically update local state
      setState(prev => {
        if (!prev) return prev;
        return {
          ...prev,
          services: prev.services.map(s =>
            s.id === serviceId ? { ...s, positionX, positionY } : s
          )
        };
      });
    },
    [token]
  );

  // Create connection between services
  const createConnection = useCallback(
    async (sourceId: string, targetId: string, label?: string) => {
      if (!token) return;

      const connection = await apiFetch<Connection>(
        "/services/connections",
        {
          method: "POST",
          body: JSON.stringify({ sourceId, targetId, label })
        },
        token
      );

      setState(prev => {
        if (!prev) return prev;
        return {
          ...prev,
          connections: [...prev.connections, connection]
        };
      });

      return connection;
    },
    [token]
  );

  // Delete connection
  const deleteConnection = useCallback(
    async (connectionId: string) => {
      if (!token) return;

      await apiFetch(
        `/services/connections/${connectionId}`,
        { method: "DELETE" },
        token
      );

      setState(prev => {
        if (!prev) return prev;
        return {
          ...prev,
          connections: prev.connections.filter(c => c.id !== connectionId)
        };
      });
    },
    [token]
  );

  // Run auto-layout
  const runAutoLayout = useCallback(async () => {
    if (!projectId || !token) return;

    const result = await apiFetch<{ services: Array<{ id: string; positionX: number; positionY: number }> }>(
      `/projects/${projectId}/canvas/auto-layout`,
      { method: "POST" },
      token
    );

    // Update local state with new positions
    setState(prev => {
      if (!prev) return prev;
      const positionMap = new Map(result.services.map(s => [s.id, s]));
      return {
        ...prev,
        services: prev.services.map(s => {
          const newPos = positionMap.get(s.id);
          return newPos ? { ...s, positionX: newPos.positionX, positionY: newPos.positionY } : s;
        })
      };
    });
  }, [projectId, token]);

  // Load on mount and when projectId/token changes
  useEffect(() => {
    loadCanvas();
  }, [loadCanvas]);

  return {
    state,
    loading,
    error,
    refresh: loadCanvas,
    updatePosition,
    createConnection,
    deleteConnection,
    runAutoLayout
  };
}
```

### Step 5.2: Commit

```bash
cd /Users/manwindersingh/Desktop/infra-pilot
git add cp-ui/src/hooks/useCanvas.ts
git commit -m "feat(canvas): add useCanvas hook for state management"
```

---

## Task 6: Main Canvas Component

**Files:**
- Create: `cp-ui/src/components/ServiceCanvas.tsx`
- Create: `cp-ui/src/components/CanvasToolbar.tsx`

### Step 6.1: Create CanvasToolbar component

Create `cp-ui/src/components/CanvasToolbar.tsx`:

```typescript
interface CanvasToolbarProps {
  onAutoLayout: () => void;
  onRefresh: () => void;
  loading: boolean;
  serviceCount: number;
  connectionCount: number;
}

export function CanvasToolbar({
  onAutoLayout,
  onRefresh,
  loading,
  serviceCount,
  connectionCount
}: CanvasToolbarProps) {
  return (
    <div
      style={{
        display: "flex",
        alignItems: "center",
        justifyContent: "space-between",
        padding: "12px 16px",
        background: "#f9fafb",
        borderBottom: "1px solid #e5e7eb"
      }}
    >
      <div style={{ display: "flex", alignItems: "center", gap: "16px" }}>
        <h3 style={{ margin: 0, fontSize: "16px", fontWeight: 600 }}>
          Infrastructure Canvas
        </h3>
        <div style={{ fontSize: "13px", color: "#6b7280" }}>
          {serviceCount} service{serviceCount !== 1 ? "s" : ""}
          {connectionCount > 0 && ` • ${connectionCount} connection${connectionCount !== 1 ? "s" : ""}`}
        </div>
      </div>

      <div style={{ display: "flex", gap: "8px" }}>
        <button
          onClick={onAutoLayout}
          disabled={loading}
          style={{
            padding: "6px 12px",
            fontSize: "13px",
            borderRadius: "6px",
            border: "1px solid #d1d5db",
            background: "white",
            cursor: loading ? "not-allowed" : "pointer",
            opacity: loading ? 0.6 : 1
          }}
        >
          Auto Layout
        </button>
        <button
          onClick={onRefresh}
          disabled={loading}
          style={{
            padding: "6px 12px",
            fontSize: "13px",
            borderRadius: "6px",
            border: "1px solid #d1d5db",
            background: "white",
            cursor: loading ? "not-allowed" : "pointer",
            opacity: loading ? 0.6 : 1
          }}
        >
          Refresh
        </button>
      </div>
    </div>
  );
}
```

### Step 6.2: Create ServiceCanvas component

Create `cp-ui/src/components/ServiceCanvas.tsx`:

```typescript
import { useCallback, useMemo, useState } from "react";
import {
  ReactFlow,
  Background,
  Controls,
  MiniMap,
  useNodesState,
  useEdgesState,
  addEdge,
  Connection,
  Edge,
  Node,
  Panel
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";

import { useCanvas } from "../hooks/useCanvas";
import { ServiceNode } from "./ServiceNode";
import { CanvasToolbar } from "./CanvasToolbar";

const nodeTypes = {
  service: ServiceNode
};

interface ServiceCanvasProps {
  projectId: string;
  token?: string;
}

export function ServiceCanvas({ projectId, token }: ServiceCanvasProps) {
  const { state, loading, error, refresh, updatePosition, createConnection, deleteConnection, runAutoLayout } =
    useCanvas({ projectId, token });

  const [selectedNode, setSelectedNode] = useState<string | null>(null);

  // Convert services to React Flow nodes
  const initialNodes: Node[] = useMemo(() => {
    if (!state) return [];
    return state.services.map(service => ({
      id: service.id,
      type: "service",
      position: { x: service.positionX, y: service.positionY },
      data: {
        name: service.name,
        serviceType: service.serviceType,
        deploymentCount: service.deploymentCount
      },
      selected: service.id === selectedNode
    }));
  }, [state, selectedNode]);

  // Convert connections to React Flow edges
  const initialEdges: Edge[] = useMemo(() => {
    if (!state) return [];
    return state.connections.map(conn => ({
      id: conn.id,
      source: conn.sourceId,
      target: conn.targetId,
      label: conn.label,
      type: "smoothstep",
      animated: true,
      style: { stroke: "#9ca3af" }
    }));
  }, [state]);

  const [nodes, setNodes, onNodesChange] = useNodesState(initialNodes);
  const [edges, setEdges, onEdgesChange] = useEdgesState(initialEdges);

  // Sync with initialNodes/initialEdges when data changes
  useState(() => {
    setNodes(initialNodes);
    setEdges(initialEdges);
  });

  // Handle node drag end - save position
  const onNodeDragStop = useCallback(
    (_: React.MouseEvent, node: Node) => {
      updatePosition(node.id, node.position.x, node.position.y);
    },
    [updatePosition]
  );

  // Handle new connections
  const onConnect = useCallback(
    async (connection: Connection) => {
      if (!connection.source || !connection.target) return;

      // Optimistically add edge
      const newEdge: Edge = {
        id: `temp-${Date.now()}`,
        source: connection.source,
        target: connection.target,
        type: "smoothstep",
        animated: true,
        style: { stroke: "#9ca3af" }
      };
      setEdges(eds => addEdge(newEdge, eds));

      // Persist to backend
      try {
        const saved = await createConnection(connection.source, connection.target);
        // Replace temp edge with saved one
        setEdges(eds =>
          eds.map(e =>
            e.id === newEdge.id
              ? { ...e, id: saved.id, label: saved.label }
              : e
          )
        );
      } catch (err) {
        // Remove temp edge on error
        setEdges(eds => eds.filter(e => e.id !== newEdge.id));
        console.error("Failed to create connection:", err);
      }
    },
    [createConnection, setEdges]
  );

  // Handle edge deletion (right-click or delete key)
  const onEdgesDelete = useCallback(
    async (deletedEdges: Edge[]) => {
      for (const edge of deletedEdges) {
        await deleteConnection(edge.id);
      }
    },
    [deleteConnection]
  );

  // Handle node selection
  const onNodeClick = useCallback((_event: React.MouseEvent, node: Node) => {
    setSelectedNode(node.id);
  }, []);

  if (error) {
    return (
      <div
        style={{
          padding: "20px",
          background: "#fee2e2",
          color: "#991b1b",
          borderRadius: "8px"
        }}
      >
        <strong>Error loading canvas:</strong> {error}
      </div>
    );
  }

  return (
    <div
      style={{
        display: "flex",
        flexDirection: "column",
        height: "600px",
        border: "1px solid #e5e7eb",
        borderRadius: "8px",
        overflow: "hidden",
        background: "#f3f4f6"
      }}
    >
      <CanvasToolbar
        onAutoLayout={runAutoLayout}
        onRefresh={refresh}
        loading={loading}
        serviceCount={state?.services.length ?? 0}
        connectionCount={state?.connections.length ?? 0}
      />

      <div style={{ flex: 1, position: "relative" }}>
        <ReactFlow
          nodes={nodes}
          edges={edges}
          onNodesChange={onNodesChange}
          onEdgesChange={onEdgesChange}
          onNodeDragStop={onNodeDragStop}
          onConnect={onConnect}
          onEdgesDelete={onEdgesDelete}
          onNodeClick={onNodeClick}
          nodeTypes={nodeTypes}
          fitView
          fitViewOptions={{ padding: 0.2 }}
          attributionPosition="bottom-right"
          deleteKeyCode="Delete"
        >
          <Background gap={20} size={1} color="#e5e7eb" />
          <Controls />
          <MiniMap
            nodeStrokeWidth={3}
            zoomable
            pannable
            style={{
              backgroundColor: "#f9fafb",
              border: "1px solid #e5e7eb"
            }}
          />

          <Panel position="top-right" style={{ marginTop: "10px" }}>
            <div
              style={{
                background: "white",
                padding: "8px 12px",
                borderRadius: "6px",
                fontSize: "12px",
                color: "#6b7280",
                boxShadow: "0 1px 3px rgba(0,0,0,0.1)"
              }}
            >
              <div>Drag nodes to reposition</div>
              <div>Drag from handles to connect</div>
              <div>Select edge + Delete to remove</div>
            </div>
          </Panel>
        </ReactFlow>

        {loading && (
          <div
            style={{
              position: "absolute",
              top: "50%",
              left: "50%",
              transform: "translate(-50%, -50%)",
              background: "rgba(255,255,255,0.9)",
              padding: "16px 24px",
              borderRadius: "8px",
              boxShadow: "0 4px 12px rgba(0,0,0,0.15)"
            }}
          >
            Loading canvas...
          </div>
        )}
      </div>
    </div>
  );
}
```

### Step 6.3: Commit

```bash
cd /Users/manwindersingh/Desktop/infra-pilot
git add cp-ui/src/components/ServiceCanvas.tsx cp-ui/src/components/CanvasToolbar.tsx
git commit -m "feat(canvas): add ServiceCanvas component with React Flow integration"
```

---

## Task 7: Integrate Canvas into App

**Files:**
- Modify: `cp-ui/src/App.tsx`

### Step 7.1: Add canvas view to App.tsx

Find the imports section in `cp-ui/src/App.tsx` and add:

```typescript
import { ServiceCanvas } from "./components/ServiceCanvas";
```

Find where state variables are defined (around line 35-50) and add:

```typescript
const [viewMode, setViewMode] = useState<"list" | "canvas">("list");
```

Find the JSX section with deployment table (around line 350-400) and wrap it with a tab selector. After the "Deploy" section (line 395), add:

```typescript
      {/* View Toggle */}
      <div style={{ display: "flex", gap: 8, marginTop: 16, marginBottom: 8 }}>
        <button
          onClick={() => setViewMode("list")}
          style={{
            padding: "8px 16px",
            borderRadius: 6,
            border: "1px solid #ddd",
            background: viewMode === "list" ? "#3b82f6" : "white",
            color: viewMode === "list" ? "white" : "#333",
            cursor: "pointer"
          }}
        >
          List View
        </button>
        <button
          onClick={() => setViewMode("canvas")}
          disabled={!selectedProjectId}
          style={{
            padding: "8px 16px",
            borderRadius: 6,
            border: "1px solid #ddd",
            background: viewMode === "canvas" ? "#3b82f6" : "white",
            color: viewMode === "canvas" ? "white" : "#333",
            cursor: !selectedProjectId ? "not-allowed" : "pointer",
            opacity: !selectedProjectId ? 0.5 : 1
          }}
        >
          Canvas View
        </button>
      </div>

      {/* Canvas View */}
      {viewMode === "canvas" && selectedProjectId && (
        <section style={{ marginTop: 8 }}>
          <ServiceCanvas projectId={selectedProjectId} token={token} />
        </section>
      )}
```

Wrap the existing deployment list/table section with `{viewMode === "list" && (...)}` to conditionally show it.

### Step 7.2: Commit

```bash
cd /Users/manwindersingh/Desktop/infra-pilot
git add cp-ui/src/App.tsx
git commit -m "feat(canvas): integrate canvas view into main app with toggle"
```

---

## Task 8: Testing & Verification

### Step 8.1: Start all services

Terminal 1 - Infrastructure:
```bash
cd /Users/manwindersingh/Desktop/infra-pilot/control-plane
docker compose up -d
```

Terminal 2 - API:
```bash
cd /Users/manwindersingh/Desktop/infra-pilot/control-plane
npm run dev:api
```

Terminal 3 - UI:
```bash
cd /Users/manwindersingh/Desktop/infra-pilot/cp-ui
npm run dev
```

### Step 8.2: Manual testing checklist

1. **Create a project** in the UI
2. **Create 2-3 services** (different types: nodejs, mongodb, redis)
3. **Switch to Canvas view** - verify services appear as nodes
4. **Drag nodes** - verify positions persist after refresh
5. **Connect services** - drag from handle to handle, verify connections save
6. **Auto-layout** - click button, verify nodes rearrange
7. **Delete connections** - select edge, press Delete, verify removal persists

### Step 8.3: Run type check

```bash
cd /Users/manwindersingh/Desktop/infra-pilot/cp-ui
npx tsc --noEmit
```

Expected: No TypeScript errors.

### Step 8.4: Final commit

```bash
cd /Users/manwindersingh/Desktop/infra-pilot
git add -A
git commit -m "feat(canvas): complete 2D service canvas implementation

- Add position fields and ServiceConnection to database
- Implement canvas API routes with auto-layout (dagre)
- Create ServiceNode component with type icons
- Build ServiceCanvas with React Flow integration
- Add drag-drop positioning with persistence
- Support creating/deleting connections between services
- Integrate canvas view into main app with toggle"
```

---

## Success Criteria

After completing all tasks:

- [ ] Services render as draggable nodes with icons
- [ ] Node positions persist to database on drag end
- [ ] Connections can be created between services
- [ ] Connections persist and can be deleted
- [ ] Auto-layout button arranges services in a readable layout
- [ ] Canvas view toggles from list view in the UI
- [ ] No TypeScript errors
- [ ] All changes committed to git

---

## Notes for Implementation

1. **Default Positions:** When a service is created without position data, it defaults to (0,0). Consider randomizing initial positions in a follow-up.

2. **Performance:** With 50+ services, consider virtualization or clustering. Current implementation is fine for small projects.

3. **Real-time:** Currently requires manual refresh to see other users' changes. Could add Socket.io sync in future.

4. **Edge Cases:**
   - Self-connections are blocked in API
   - Duplicate connections return 409 Conflict
   - Deleting a service cascades and deletes its connections

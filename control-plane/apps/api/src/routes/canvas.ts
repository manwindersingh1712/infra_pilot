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
  fastify.get("/projects/:id/canvas", getCanvasHandler);
  fastify.put("/projects/:id/canvas", updateCanvasHandler);
  fastify.post("/services/:id/position", updatePositionHandler);
  fastify.post("/services/connections", createConnectionHandler);
  fastify.delete("/services/connections/:id", deleteConnectionHandler);
  fastify.post("/projects/:id/canvas/auto-layout", autoLayoutHandler);
}

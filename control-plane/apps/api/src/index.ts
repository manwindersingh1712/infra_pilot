import Fastify from "fastify";
import cors from "@fastify/cors";
import jwt from "@fastify/jwt";
import env from "@fastify/env";
import { z } from "zod";

import { prisma } from "@/packages/shared/src/db.js";
import { getAmqpChannel, closeAmqp } from "@/packages/shared/src/amqp.js";

import { ensureTopology } from "@/packages/shared/src/topology.js";
import { EVENT_TYPES } from "@/packages/shared/src/events.js";

const envSchema = {
  type: "object",
  required: ["API_PORT", "DATABASE_URL", "AMQP_URL", "JWT_SECRET"],
  properties: {
    API_PORT: { type: "string" },
    DATABASE_URL: { type: "string" },
    AMQP_URL: { type: "string" },
    JWT_SECRET: { type: "string" },
    NODE_ENV: { type: "string" }
  }
} as const;

const app = Fastify({
  logger: {
    transport:
      process.env.NODE_ENV === "development"
        ? { target: "pino-pretty", options: { translateTime: "SYS:standard" } }
        : undefined
  }
});

await app.register(env, { schema: envSchema, dotenv: true });
await app.register(cors, { origin: true });
await app.register(jwt, { secret: process.env.JWT_SECRET! });
await ensureTopology();


app.get("/healthz", async () => ({ ok: true }));

app.get("/readyz", async (_req, reply) => {
  try {
    await prisma.$queryRaw`SELECT 1`;
    await getAmqpChannel(); // connect + open channel
    return { ok: true };
  } catch {
    return reply.status(503).send({ ok: false });
  }
});

// TEMP dev auth (replace later with GitHub OAuth)
app.post("/dev/login", async () => {
  const user = await prisma.user.upsert({
    where: { email: "dev@local" },
    update: {},
    create: { email: "dev@local" }
  });

  const token = app.jwt.sign({ sub: user.id });
  return { token, userId: user.id };
});

// Auth hook
app.addHook("preHandler", async (req, reply) => {
  const open =
    req.routeOptions.url === "/healthz" ||
    req.routeOptions.url === "/readyz" ||
    req.routeOptions.url === "/dev/login";

  if (open) return;

  try {
    await req.jwtVerify();
  } catch {
    return reply.status(401).send({ error: "unauthorized" });
  }
});

// Create Project
app.post("/projects", async (req) => {
  const body = z.object({ name: z.string().min(2) }).parse(req.body);
  const userId = (req.user as any).sub as string;

  return prisma.project.create({
    data: { name: body.name, ownerUserId: userId }
  });
});

// Create Service
app.post("/services", async (req) => {
  const body = z
    .object({
      projectId: z.string(),
      name: z.string().min(2),
      serviceType: z.enum(["docker", "nodejs", "react", "mongodb", "redis"]).optional(),
      repoUrl: z.string().url().optional(),
      branch: z.string().min(1).optional()
    })
    .refine((data) => {
      // repoUrl required for docker, nodejs, nextjs, react types
      const type = data.serviceType ?? "docker";
      if (type === "docker" || type === "nodejs" || type === "nextjs" || type === "react") {
        return !!data.repoUrl;
      }
      return true;
    }, { message: "repoUrl required for docker/nodejs/nextjs/react services" })
    .parse(req.body);

  const serviceType = body.serviceType ?? "docker";

  return prisma.service.create({
    data: {
      projectId: body.projectId,
      name: body.name,
      serviceType,
      repoUrl: body.repoUrl,
      branch: body.branch ?? "main"
    }
  });
});

// Deploy Service
app.post("/services/:id/deploy", async (req, reply) => {
  const params = z.object({ id: z.string() }).parse(req.params);
  const body = z.object({ commitSha: z.string().min(4).optional() }).parse(req.body);

  const service = await prisma.service.findUnique({
    where: { id: params.id },
    select: { id: true, serviceType: true }
  });

  if (!service) return reply.status(404).send({ error: "service_not_found" });

  const isManagedService = service.serviceType === "mongodb" || service.serviceType === "redis";
  const commitSha = body.commitSha ?? (isManagedService ? "latest" : "main");

  const result = await prisma.$transaction(async (tx) => {
    const deployment = await tx.deployment.create({
      data: {
        serviceId: service.id,
        commitSha,
        status: "queued"
      }
    });

    // For managed services (mongodb/redis), skip build and go straight to deploy
    const eventType = isManagedService ? EVENT_TYPES.DEPLOY_REQUESTED : EVENT_TYPES.BUILD_REQUESTED;

    await tx.outboxEvent.create({
      data: {
        type: eventType,
        payload: { deploymentId: deployment.id }
      }
    });

    return deployment;
  });

  return { deploymentId: result.id, status: result.status };
});

// Get Projects
app.get("/projects", async (req) => {
  const userId = (req.user as any).sub as string;
  return prisma.project.findMany({
    where: { ownerUserId: userId },
    orderBy: { createdAt: "desc" }
  });
});

// Get Services
app.get("/services", async (req) => {
  const q = z.object({ projectId: z.string().optional() }).parse(req.query);

  return prisma.service.findMany({
    where: q.projectId ? { projectId: q.projectId } : undefined,
    orderBy: { createdAt: "desc" }
  });
});

// Get Deployments
app.get("/deployments", async (req) => {
  const q = z.object({ serviceId: z.string().optional() }).parse(req.query);

  return prisma.deployment.findMany({
    where: q.serviceId ? { serviceId: q.serviceId } : undefined,
    orderBy: { createdAt: "desc" },
    take: 50
  });
});

// Get Service Env Vars
app.get("/services/:id/env", async (req, reply) => {
  const params = z.object({ id: z.string() }).parse(req.params);

  const service = await prisma.service.findUnique({
    where: { id: params.id },
    select: { id: true }
  });

  if (!service) return reply.status(404).send({ error: "service_not_found" });

  return prisma.envVar.findMany({
    where: { serviceId: params.id },
    orderBy: { key: "asc" }
  });
});

// Set Service Env Var (create or update)
app.post("/services/:id/env", async (req, reply) => {
  const params = z.object({ id: z.string() }).parse(req.params);
  const body = z.object({ key: z.string().min(1), value: z.string() }).parse(req.body);

  const service = await prisma.service.findUnique({
    where: { id: params.id },
    select: { id: true }
  });

  if (!service) return reply.status(404).send({ error: "service_not_found" });

  return prisma.envVar.upsert({
    where: { serviceId_key: { serviceId: params.id, key: body.key } },
    update: { value: body.value },
    create: { serviceId: params.id, key: body.key, value: body.value }
  });
});

// Delete Service Env Var
app.delete("/services/:id/env/:key", async (req, reply) => {
  const params = z.object({ id: z.string(), key: z.string() }).parse(req.params);

  const service = await prisma.service.findUnique({
    where: { id: params.id },
    select: { id: true }
  });

  if (!service) return reply.status(404).send({ error: "service_not_found" });

  await prisma.envVar.deleteMany({
    where: { serviceId: params.id, key: params.key }
  });

  return { deleted: true };
});

const port = Number(process.env.API_PORT ?? 8080);
await app.listen({ port, host: "0.0.0.0" });

// Graceful shutdown (important for scale)
const shutdown = async () => {
  app.log.info("Shutting down...");
  try { await app.close(); } catch {}
  try { await prisma.$disconnect(); } catch {}
  try { await closeAmqp(); } catch {}
  process.exit(0);
};

process.on("SIGTERM", shutdown);
process.on("SIGINT", shutdown);

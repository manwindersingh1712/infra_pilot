import Fastify from "fastify";
import cors from "@fastify/cors";
import jwt from "@fastify/jwt";
import env from "@fastify/env";
import { z } from "zod";

import { prisma } from "../../../packages/shared/src/db.js";
import { getAmqpChannel, closeAmqp } from "../../../packages/shared/src/amqp.js";

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
      repoUrl: z.string().url(),
      branch: z.string().min(1).optional()
    })
    .parse(req.body);

  return prisma.service.create({
    data: {
      projectId: body.projectId,
      name: body.name,
      repoUrl: body.repoUrl,
      branch: body.branch ?? "main"
    }
  });
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

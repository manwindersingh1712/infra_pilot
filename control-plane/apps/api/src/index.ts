import Fastify from "fastify";
import cors from "@fastify/cors";
import jwt from "@fastify/jwt";
import env from "@fastify/env";
import { z } from "zod";

import { prisma } from "@/packages/shared/src/db.js";
import { getAmqpChannel, closeAmqp } from "@/packages/shared/src/amqp.js";

import { ensureTopology } from "@/packages/shared/src/topology.js";
import { authRoutes } from "./routes/auth.js";
import { projectRoutes } from "./routes/projects.js";
import { serviceRoutes } from "./routes/services.js";
import { deploymentRoutes } from "./routes/deployments.js";
import { logRoutes } from "./routes/logs.js";
import { canvasRoutes } from "./routes/canvas.js";
import socketioPlugin from "./plugins/socketio.js";
import { registerLogHandlers } from "./socket-handlers/logs.js";
import { setBroadcastCallback } from "./services/log-aggregator.js";
import { initClickHouse } from "./services/clickhouse.js";

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
await app.register(socketioPlugin);

// Register Socket.io log handlers and set broadcast callback
const { broadcastLog } = registerLogHandlers(app.io);
setBroadcastCallback(broadcastLog);

await ensureTopology();
await initClickHouse();


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
    create: {
      email: "dev@local",
      password: "dev-password-not-used"
    }
  });

  const token = app.jwt.sign({ sub: user.id });
  return { token, userId: user.id };
});

// Auth hook
app.addHook("preHandler", async (req, reply) => {
  // Use routeOptions.url which gives us the route pattern (e.g., "/healthz", "/:id/deploy")
  // Combined with the prefix registered, this uniquely identifies the route
  const routePattern = (req as any).routeOptions?.url ?? "";
  const routerPath = (req as any).routerPath ?? "";

  // Check both - routerPath includes the prefix when available
  const url = routerPath || routePattern;

  const open =
    url === "/healthz" ||
    url === "/readyz" ||
    url === "/dev/login" ||
    url === "/auth/register" ||
    url === "/auth/login";

  if (open) return;

  try {
    await req.jwtVerify();
  } catch {
    return reply.status(401).send({ error: "unauthorized" });
  }
});

// Register routes
app.register(authRoutes, { prefix: "/auth" });
app.register(projectRoutes, { prefix: "/projects" });
app.register(serviceRoutes, { prefix: "/services" });
app.register(deploymentRoutes, { prefix: "/deployments" });
app.register(logRoutes, { prefix: "/logs" });
app.register(canvasRoutes);

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

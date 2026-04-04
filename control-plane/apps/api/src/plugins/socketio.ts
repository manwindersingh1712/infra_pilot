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
    transports: ["websocket", "polling"]
  });

  // Use Redis adapter for horizontal scaling
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

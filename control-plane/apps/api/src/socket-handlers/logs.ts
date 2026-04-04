import type { Server, Socket } from "socket.io";
import { prisma } from "@/packages/shared/src/db.js";
import * as logAggregator from "../services/log-aggregator.js";
import * as redis from "../services/redis.js";

export function registerLogHandlers(io: Server) {
  io.on("connection", (socket: Socket) => {
    console.log("[socket.io] client connected:", socket.id);

    // Track Redis subscriptions per deployment for cleanup
    const redisSubscriptions = new Map<string, () => void>();

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
        const { logs, source, totalCount } = await logAggregator.queryLogs({
          deploymentId,
          limit: 100
        });

        socket.emit("logs:history", { logs, meta: { source, totalCount } });

        // Subscribe to Redis Pub/Sub for real-time logs from worker
        const unsubscribe = redis.subscribeToLogs(deploymentId, (entry) => {
          socket.to(room).emit("logs:new", entry);
          // Also emit to the subscribing socket itself
          socket.emit("logs:new", entry);
        });

        redisSubscriptions.set(deploymentId, unsubscribe);
        console.log(`[socket.io] ${socket.id} subscribed to Redis Pub/Sub for ${deploymentId}`);

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

      // Unsubscribe from Redis Pub/Sub
      const unsubscribe = redisSubscriptions.get(deploymentId);
      if (unsubscribe) {
        unsubscribe();
        redisSubscriptions.delete(deploymentId);
        console.log(`[socket.io] ${socket.id} unsubscribed from Redis Pub/Sub for ${deploymentId}`);
      }

      console.log(`[socket.io] ${socket.id} left room ${room}`);
      callback?.({ success: true });
    });

    socket.on("disconnect", () => {
      console.log("[socket.io] client disconnected:", socket.id);
      // Clean up all Redis subscriptions
      for (const [deploymentId, unsubscribe] of redisSubscriptions) {
        unsubscribe();
        console.log(`[socket.io] cleaned up Redis subscription for ${deploymentId}`);
      }
      redisSubscriptions.clear();
    });
  });

  // Return broadcast function for external use
  return {
    broadcastLog: (deploymentId: string, log: logAggregator.LogEntry) => {
      const room = `logs:${deploymentId}`;
      io.to(room).emit("logs:new", log);
    }
  };
}

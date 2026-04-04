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
        const { logs, source, totalCount } = await logAggregator.queryLogs({
          deploymentId,
          limit: 100
        });

        socket.emit("logs:history", { logs, meta: { source, totalCount } });

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

  // Return broadcast function for external use
  return {
    broadcastLog: (deploymentId: string, log: logAggregator.LogEntry) => {
      const room = `logs:${deploymentId}`;
      io.to(room).emit("logs:new", log);
    }
  };
}

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

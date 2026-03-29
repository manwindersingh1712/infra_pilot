import { FastifyRequest, FastifyReply } from "fastify";
import { prisma } from "@/packages/shared/src/db.js";

export async function getDeployments(req: FastifyRequest, reply: FastifyReply) {
  const { serviceId } = req.query as { serviceId?: string };

  return prisma.deployment.findMany({
    where: serviceId ? { serviceId } : undefined,
    orderBy: { createdAt: "desc" },
    take: 50
  });
}

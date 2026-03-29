import { FastifyRequest, FastifyReply } from "fastify";
import { ServiceType } from "@prisma/client";
import { prisma } from "@/packages/shared/src/db.js";
import { EVENT_TYPES } from "@/packages/shared/src/events.js";

export async function createService(req: FastifyRequest, reply: FastifyReply) {
  const { projectId, name, serviceType, repoUrl, branch } = req.body as {
    projectId: string;
    name: string;
    serviceType?: string;
    repoUrl?: string;
    branch?: string;
  };

  const type = (serviceType ?? "docker") as ServiceType;

  return prisma.service.create({
    data: {
      projectId,
      name,
      serviceType: type,
      repoUrl,
      branch: branch ?? "main"
    }
  });
}

export async function deployService(req: FastifyRequest, reply: FastifyReply) {
  const { id } = req.params as { id: string };
  const { commitSha: providedCommitSha } = req.body as { commitSha?: string };

  const service = await prisma.service.findUnique({
    where: { id },
    select: { id: true, serviceType: true }
  });

  if (!service) return reply.status(404).send({ error: "service_not_found" });

  const isManagedService = service.serviceType === "mongodb" || service.serviceType === "redis";
  const commitSha = providedCommitSha ?? (isManagedService ? "latest" : "main");

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
}

export async function getServices(req: FastifyRequest, reply: FastifyReply) {
  const { projectId } = req.query as { projectId?: string };

  return prisma.service.findMany({
    where: projectId ? { projectId } : undefined,
    orderBy: { createdAt: "desc" }
  });
}

export async function getServiceEnv(req: FastifyRequest, reply: FastifyReply) {
  const { id } = req.params as { id: string };

  const service = await prisma.service.findUnique({
    where: { id },
    select: { id: true }
  });

  if (!service) return reply.status(404).send({ error: "service_not_found" });

  return prisma.envVar.findMany({
    where: { serviceId: id },
    orderBy: { key: "asc" }
  });
}

export async function setServiceEnv(req: FastifyRequest, reply: FastifyReply) {
  const { id } = req.params as { id: string };
  const { key, value } = req.body as { key: string; value: string };

  const service = await prisma.service.findUnique({
    where: { id },
    select: { id: true }
  });

  if (!service) return reply.status(404).send({ error: "service_not_found" });

  return prisma.envVar.upsert({
    where: { serviceId_key: { serviceId: id, key } },
    update: { value },
    create: { serviceId: id, key, value }
  });
}

export async function deleteServiceEnv(req: FastifyRequest, reply: FastifyReply) {
  const { id, key } = req.params as { id: string; key: string };

  const service = await prisma.service.findUnique({
    where: { id },
    select: { id: true }
  });

  if (!service) return reply.status(404).send({ error: "service_not_found" });

  await prisma.envVar.deleteMany({
    where: { serviceId: id, key }
  });

  return { deleted: true };
}

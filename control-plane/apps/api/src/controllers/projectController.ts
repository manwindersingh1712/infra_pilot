import { FastifyRequest, FastifyReply } from "fastify";
import { prisma } from "@/packages/shared/src/db.js";

export async function createProject(req: FastifyRequest, reply: FastifyReply) {
  const { name } = req.body as { name: string };
  const userId = (req.user as any).sub as string;

  return prisma.project.create({
    data: { name, ownerUserId: userId }
  });
}

export async function getProjects(req: FastifyRequest, reply: FastifyReply) {
  const userId = (req.user as any).sub as string;

  return prisma.project.findMany({
    where: { ownerUserId: userId },
    orderBy: { createdAt: "desc" }
  });
}

export async function getProjectById(req: FastifyRequest, reply: FastifyReply) {
  const userId = (req.user as any).sub as string;
  const { id } = req.params as { id: string };

  const project = await prisma.project.findFirst({
    where: { id, ownerUserId: userId }
  });

  if (!project) {
    return reply.status(404).send({ error: "Project not found" });
  }

  return project;
}

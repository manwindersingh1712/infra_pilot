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

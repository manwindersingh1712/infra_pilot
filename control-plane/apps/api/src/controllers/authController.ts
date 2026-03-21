import { FastifyRequest, FastifyReply } from "fastify";
import bcrypt from "bcrypt";
import { prisma } from "@/packages/shared/src/db.js";

const SALT_ROUNDS = 10;

export async function register(req: FastifyRequest, reply: FastifyReply) {
  const { email, password, name } = req.body as { email: string; password: string; name?: string };

  const existing = await prisma.user.findUnique({
    where: { email }
  });

  if (existing) {
    return reply.status(409).send({ error: "email_already_exists" });
  }

  const passwordHash = await bcrypt.hash(password, SALT_ROUNDS);

  const user = await prisma.user.create({
    data: {
      email,
      password: passwordHash,
      name
    }
  });

  const token = req.server.jwt.sign({ sub: user.id });

  return {
    token,
    user: {
      id: user.id,
      email: user.email,
      name: user.name
    }
  };
}

export async function login(req: FastifyRequest, reply: FastifyReply) {
  const { email, password } = req.body as { email: string; password: string };

  const user = await prisma.user.findUnique({
    where: { email }
  });

  if (!user || !user.password) {
    return reply.status(401).send({ error: "invalid_credentials" });
  }

  const valid = await bcrypt.compare(password, user.password);

  if (!valid) {
    return reply.status(401).send({ error: "invalid_credentials" });
  }

  const token = req.server.jwt.sign({ sub: user.id });

  return {
    token,
    user: {
      id: user.id,
      email: user.email,
      name: user.name
    }
  };
}

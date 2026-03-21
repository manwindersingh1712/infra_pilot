import { FastifyInstance } from "fastify";
import { z } from "zod";
import * as authController from "../controllers/authController.js";

export async function authRoutes(app: FastifyInstance) {
  app.post("/register", async (req, reply) => {
    const body = z
      .object({
        email: z.string().email(),
        password: z.string().min(8),
        name: z.string().optional()
      })
      .parse(req.body);

    return authController.register(req, reply);
  });

  app.post("/login", async (req, reply) => {
    const body = z
      .object({
        email: z.string().email(),
        password: z.string()
      })
      .parse(req.body);

    return authController.login(req, reply);
  });
}

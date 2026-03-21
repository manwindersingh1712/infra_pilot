import { FastifyInstance } from "fastify";
import { z } from "zod";
import * as deploymentController from "../controllers/deploymentController.js";

export async function deploymentRoutes(app: FastifyInstance) {
  app.get("/", async (req, reply) => {
    const q = z.object({ serviceId: z.string().optional() }).parse(req.query);

    (req as any).query = q;
    return deploymentController.getDeployments(req, reply);
  });
}

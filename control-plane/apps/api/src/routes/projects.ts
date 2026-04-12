import { FastifyInstance } from "fastify";
import { z } from "zod";
import * as projectController from "../controllers/projectController.js";

export async function projectRoutes(app: FastifyInstance) {
  app.post("/", async (req, reply) => {
    const body = z.object({ name: z.string().min(2) }).parse(req.body);
    (req as any).body = body;
    return projectController.createProject(req, reply);
  });

  app.get("/", async (req, reply) => {
    return projectController.getProjects(req, reply);
  });

  app.get("/:id", async (req, reply) => {
    return projectController.getProjectById(req, reply);
  });
}

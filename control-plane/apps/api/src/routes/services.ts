import { FastifyInstance } from "fastify";
import { z } from "zod";
import * as serviceController from "../controllers/serviceController.js";

export async function serviceRoutes(app: FastifyInstance) {
  app.post("/", async (req, reply) => {
    const body = z
      .object({
        projectId: z.string(),
        name: z.string().min(2),
        serviceType: z.enum(["docker", "nodejs", "react", "mongodb", "redis"]).optional(),
        repoUrl: z.string().url().optional(),
        branch: z.string().min(1).optional()
      })
      .refine((data) => {
        // repoUrl required for docker, nodejs, nextjs, react types
        const type = data.serviceType ?? "docker";
        if (type === "docker" || type === "nodejs" || type === "react") {
          return !!data.repoUrl;
        }
        return true;
      }, { message: "repoUrl required for docker/nodejs/nextjs/react services" })
      .parse(req.body);

    (req as any).body = body;
    return serviceController.createService(req, reply);
  });

  app.post("/:id/deploy", async (req, reply) => {
    const params = z.object({ id: z.string() }).parse(req.params);
    const body = z.object({ commitSha: z.string().min(4).optional() }).parse(req.body);

    (req as any).params = params;
    (req as any).body = body;
    return serviceController.deployService(req, reply);
  });

  app.get("/", async (req, reply) => {
    const q = z.object({ projectId: z.string().optional() }).parse(req.query);

    (req as any).query = q;
    return serviceController.getServices(req, reply);
  });

  app.get("/:id/env", async (req, reply) => {
    const params = z.object({ id: z.string() }).parse(req.params);

    (req as any).params = params;
    return serviceController.getServiceEnv(req, reply);
  });

  app.post("/:id/env", async (req, reply) => {
    const params = z.object({ id: z.string() }).parse(req.params);
    const body = z.object({ key: z.string().min(1), value: z.string() }).parse(req.body);

    (req as any).params = params;
    (req as any).body = body;
    return serviceController.setServiceEnv(req, reply);
  });

  app.delete("/:id/env/:key", async (req, reply) => {
    const params = z.object({ id: z.string(), key: z.string() }).parse(req.params);

    (req as any).params = params;
    return serviceController.deleteServiceEnv(req, reply);
  });
}

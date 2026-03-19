import { getAmqpChannel } from "@/packages/shared/src/amqp.js";
import { prisma } from "@/packages/shared/src/db.js";
import { MQ } from "@/packages/shared/src/mq.js";
import { ensureTopology } from "@/packages/shared/src/topology.js";
import { allocateHostPort } from "@/apps/worker/src/runtime/ports.js";
import {
  detectContainerPort,
  dockerPull,
  dockerRun,
  dockerRunManaged
} from "@/apps/worker/src/runtime/docker-runner.js";
import fs from "node:fs/promises";
import {
  upsertServiceRoute,
  SERVICE_BASE_DOMAIN
} from "@/apps/worker/src/runtime/nginx.js";

const MAX_RETRIES = 5;

const MANAGED_SERVICE_IMAGES: Record<string, { image: string; defaultPort: number; env: Record<string, string> }> = {
  mongodb: {
    image: "mongo:7",
    defaultPort: 27017,
    env: {
      MONGO_INITDB_ROOT_USERNAME: "admin",
      MONGO_INITDB_ROOT_PASSWORD: "admin123"
    }
  },
  redis: {
    image: "redis:7-alpine",
    defaultPort: 6379,
    env: {}
  }
};

type DeployMsg = { deploymentId: string };

export async function startDeployConsumer() {
  await ensureTopology();
  const ch = await getAmqpChannel();
  console.log("[deploy-consumer] started, listening on queue:", MQ.DEPLOY_QUEUE);

  await ch.consume(MQ.DEPLOY_QUEUE, async (msg) => {
    if (!msg) {
      console.log("[deploy-consumer] received null message");
      return;
    }

    const retryCount = Number(msg.properties.headers?.["x-retry-count"] ?? 0);
    console.log("[deploy-consumer] received message, retry count:", retryCount);

    try {
      const body = JSON.parse(msg.content.toString()) as DeployMsg;
      console.log("[deploy-consumer] deploymentId:", body.deploymentId);

      // 1) Fetch deployment with service info
      const dep = await prisma.deployment.findUnique({
        where: { id: body.deploymentId },
        select: {
          id: true,
          status: true,
          image: true,
          serviceId: true,
          volumePath: true,
          service: { select: { serviceType: true } }
        }
      });

      // If not found, ack (nothing to do)
      if (!dep) {
        console.log("[deploy-consumer] deployment not found:", body.deploymentId);
        ch.ack(msg);
        return;
      }
      console.log("[deploy-consumer] found deployment, status:", dep.status, "serviceType:", dep.service.serviceType);

      const isManagedService = dep.service.serviceType === "mongodb" || dep.service.serviceType === "redis";

      // For managed services, image comes from config, not build
      if (!dep.image && !isManagedService) {
        throw new Error("image_not_built_yet");
      }

      // 2) Idempotency: if already progressed, ack and exit
      // queued -> deploying -> deployed/failed
      if (dep.status !== "queued") {
        ch.ack(msg);
        return;
      }

      // 3) Atomic transition: only queued -> deploying
      // This prevents two workers from processing the same deployment.
      const updated = await prisma.deployment.updateMany({
        where: { id: dep.id, status: "queued" },
        data: { status: "deploying" }
      });

      // Another worker won the race
      if (updated.count !== 1) {
        ch.ack(msg);
        return;
      }

      // 4) Fetch env vars for the service
      const envVars = await prisma.envVar.findMany({
        where: { serviceId: dep.serviceId }
      });
      const envMap: Record<string, string> = {};
      for (const ev of envVars) {
        envMap[ev.key] = ev.value;
      }

      // 5) Run container via data-plane runner
      const hostPort = await allocateHostPort();
      const containerName = `cp-${dep.id}`;

      let image: string;
      let containerPort: number;
      let containerId: string;

      if (isManagedService) {
        // Managed services: use official image with volume
        console.log("[deploy-consumer] deploying managed service:", dep.service.serviceType);
        const config = MANAGED_SERVICE_IMAGES[dep.service.serviceType];
        image = config.image;
        containerPort = config.defaultPort;
        console.log("[deploy-consumer] using image:", image, "port:", containerPort);

        // Create volume directory for persistence
        const volumePath = dep.volumePath ?? `/tmp/cp-volumes/${dep.serviceId}`;
        await fs.mkdir(volumePath, { recursive: true });

        // Merge config env with user-defined env vars
        const mergedEnv = { ...config.env, ...envMap };

        const result = await dockerRunManaged({
          image,
          name: containerName,
          hostPort,
          containerPort,
          volumePath,
          env: mergedEnv
        });
        containerId = result.containerId;
      } else {
        // Regular services: use built image
        image = dep.image!;
        await dockerPull(image);

        const detectedPort = await detectContainerPort(image).catch(() => null);
        const fallbackPort = Number(process.env.DEFAULT_CONTAINER_PORT ?? 3080);
        containerPort = detectedPort ?? fallbackPort;

        // Merge default env with user-defined env vars
        const mergedEnv = {
          NODE_ENV: "production",
          ...envMap
        };

        const result = await dockerRun({
          image,
          name: containerName,
          hostPort,
          containerPort,
          env: mergedEnv
        });
        containerId = result.containerId;
      }

      // 4b) Register the service subdomain in nginx
      const nginxPort = process.env.NGINX_PORT ?? "80";

      await upsertServiceRoute({
        serviceId: dep.serviceId,
        containerName,
        containerPort
      });

      const portSuffix = nginxPort === "80" ? "" : `:${nginxPort}`;
      const runtimeUrl = `http://${dep.serviceId}.${SERVICE_BASE_DOMAIN}${portSuffix}`;

      console.log("[deploy-consumer] container started:", containerId, "hostPort:", hostPort);

      // 5) Update deployment status
      const updateData: any = {
        containerId,
        hostPort,
        containerPort,
        runtimeUrl,
        status: "deployed"
      };

      // For managed services, store the volume path
      if (isManagedService) {
        updateData.volumePath = dep.volumePath ?? `/tmp/cp-volumes/${dep.serviceId}`;
      }

      await prisma.deployment.update({
        where: { id: dep.id },
        data: updateData
      });

      console.log("[deploy-consumer] deployment complete:", dep.id, "runtimeUrl:", runtimeUrl);

      // 6) Ack message
      ch.ack(msg);
    } catch (err) {
      console.error("[deploy-consumer] error:", err);
      if (retryCount >= MAX_RETRIES) {
        // Send to DLQ
        ch.sendToQueue(MQ.DLQ, msg.content, {
          persistent: true,
          contentType: msg.properties.contentType,
          messageId: msg.properties.messageId,
          headers: {
            ...msg.properties.headers,
            "x-retry-count": retryCount
          }
        });
        ch.ack(msg);
        return;
      }

      // Requeue by publishing again (simple v0)
      ch.publish(MQ.EXCHANGE, MQ.DEPLOY_ROUTING_KEY, msg.content, {
        persistent: true,
        contentType: msg.properties.contentType,
        messageId: msg.properties.messageId,
        headers: {
          ...msg.properties.headers,
          "x-retry-count": retryCount + 1
        }
      });

      ch.ack(msg);
    }
  });
}

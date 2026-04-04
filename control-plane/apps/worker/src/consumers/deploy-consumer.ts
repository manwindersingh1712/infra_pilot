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
import { startLogStreaming } from "@/apps/worker/src/runtime/log-streamer.js";

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

    let body: DeployMsg | null = null;
    try {
      body = JSON.parse(msg.content.toString()) as DeployMsg;
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
      console.log("[deploy-consumer] isManagedService:", isManagedService, "serviceType:", dep.service.serviceType);

      // For managed services, image comes from config, not build
      if (!dep.image && !isManagedService) {
        console.log("[deploy-consumer] error: image_not_built_yet for deployment:", dep.id);
        throw new Error("image_not_built_yet");
      }

      // 2) Idempotency: if already progressed, ack and exit
      // queued -> deploying -> deployed/failed
      if (dep.status !== "queued") {
        console.log("[deploy-consumer] deployment not queued, current status:", dep.status, "- skipping");
        ch.ack(msg);
        return;
      }

      // 3) Atomic transition: only queued -> deploying
      // This prevents two workers from processing the same deployment.
      console.log("[deploy-consumer] attempting to claim deployment:", dep.id);
      const updated = await prisma.deployment.updateMany({
        where: { id: dep.id, status: "queued" },
        data: { status: "deploying" }
      });

      // Another worker won the race
      if (updated.count !== 1) {
        console.log("[deploy-consumer] another worker claimed deployment:", dep.id);
        ch.ack(msg);
        return;
      }
      console.log("[deploy-consumer] successfully claimed deployment:", dep.id);

      // 4) Fetch env vars for the service
      console.log("[deploy-consumer] fetching env vars for service:", dep.serviceId);
      const envVars = await prisma.envVar.findMany({
        where: { serviceId: dep.serviceId }
      });
      const envMap: Record<string, string> = {};
      for (const ev of envVars) {
        envMap[ev.key] = ev.value;
      }
      console.log("[deploy-consumer] loaded", envVars.length, "env vars:", Object.keys(envMap).join(", ") || "none");

      // 5) Run container via data-plane runner
      console.log("[deploy-consumer] allocating host port...");
      const hostPort = await allocateHostPort();
      const containerName = `cp-${dep.id}`;
      console.log("[deploy-consumer] allocated hostPort:", hostPort, "containerName:", containerName);

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
        console.log("[deploy-consumer] creating volume path:", volumePath);
        await fs.mkdir(volumePath, { recursive: true });

        // Merge config env with user-defined env vars
        const mergedEnv = { ...config.env, ...envMap };
        console.log("[deploy-consumer] running dockerRunManaged with env:", Object.keys(mergedEnv).join(", "));

        const result = await dockerRunManaged({
          image,
          name: containerName,
          hostPort,
          containerPort,
          volumePath,
          env: mergedEnv
        });
        containerId = result.containerId;
        console.log("[deploy-consumer] dockerRunManaged completed, containerId:", containerId);
      } else {
        // Regular services: use built image
        image = dep.image!;
        console.log("[deploy-consumer] pulling image:", image);
        await dockerPull(image);
        console.log("[deploy-consumer] image pulled successfully");

        console.log("[deploy-consumer] detecting container port from image...");
        const detectedPort = await detectContainerPort(image).catch(() => null);
        const fallbackPort = Number(process.env.DEFAULT_CONTAINER_PORT ?? 3080);
        containerPort = detectedPort ?? fallbackPort;
        console.log("[deploy-consumer] using containerPort:", containerPort, "(detected:", detectedPort, ")");

        // Merge default env with user-defined env vars
        const mergedEnv = {
          NODE_ENV: "production",
          ...envMap
        };
        console.log("[deploy-consumer] running dockerRun with env:", Object.keys(mergedEnv).join(", "));

        const result = await dockerRun({
          image,
          name: containerName,
          hostPort,
          containerPort,
          env: mergedEnv
        });
        containerId = result.containerId;
        console.log("[deploy-consumer] dockerRun completed, containerId:", containerId);
      }

      // 4b) Register the service subdomain in nginx
      const nginxPort = process.env.NGINX_PORT ?? "80";
      console.log("[deploy-consumer] configuring nginx route for service:", dep.serviceId, "container:", containerName, "port:", containerPort);

      await upsertServiceRoute({
        serviceId: dep.serviceId,
        containerName,
        containerPort
      });
      console.log("[deploy-consumer] nginx route configured successfully");

      const portSuffix = nginxPort === "80" ? "" : `:${nginxPort}`;
      const runtimeUrl = `http://${dep.serviceId}.${SERVICE_BASE_DOMAIN}${portSuffix}`;

      console.log("[deploy-consumer] container started:", containerId, "hostPort:", hostPort, "runtimeUrl:", runtimeUrl);

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

      // Start streaming logs from the container
      startLogStreaming(dep.id, containerId);

      console.log("[deploy-consumer] deployment complete:", dep.id, "runtimeUrl:", runtimeUrl);

      // 6) Ack message
      ch.ack(msg);
    } catch (err: any) {
      console.error("[deploy-consumer] ERROR:", err?.message || err);
      console.error("[deploy-consumer] stack:", err?.stack);
      console.error("[deploy-consumer] retryCount:", retryCount, "/", MAX_RETRIES);

      // Reset status to queued so next retry can claim it
      if (body?.deploymentId) {
        try {
          await prisma.deployment.updateMany({
            where: { id: body.deploymentId, status: "deploying" },
            data: { status: "queued" }
          });
          console.log("[deploy-consumer] reset status to queued for retry");
        } catch (updateErr) {
          console.error("[deploy-consumer] failed to reset status:", updateErr);
        }
      }

      if (retryCount >= MAX_RETRIES) {
        console.log("[deploy-consumer] max retries reached, sending to DLQ");
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
        console.log("[deploy-consumer] message sent to DLQ");
        return;
      }

      // Requeue by publishing again (simple v0)
      console.log("[deploy-consumer] requeueing message with retry count:", retryCount + 1);
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
      console.log("[deploy-consumer] message requeued");
    }
  });
}

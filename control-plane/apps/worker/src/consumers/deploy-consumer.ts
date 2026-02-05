import { getAmqpChannel } from "@/packages/shared/src/amqp.js";
import { prisma } from "@/packages/shared/src/db.js";
import { MQ } from "@/packages/shared/src/mq.js";
import { ensureTopology } from "@/packages/shared/src/topology.js";
import { allocateHostPort } from "@/apps/worker/src/runtime/ports.js";
import {
  detectContainerPort,
  dockerPull,
  dockerRun
} from "@/apps/worker/src/runtime/docker-runner.js";
import { writeNginxRoute, reloadNginx } from "@/apps/worker/src/runtime/nginx.js";

const MAX_RETRIES = 5;

type DeployMsg = { deploymentId: string };

export async function startDeployConsumer() {
  await ensureTopology();
  const ch = await getAmqpChannel();

  await ch.consume(MQ.DEPLOY_QUEUE, async (msg) => {
    if (!msg) return;

    const retryCount = Number(msg.properties.headers?.["x-retry-count"] ?? 0);

    try {
      const body = JSON.parse(msg.content.toString()) as DeployMsg;

      // 1) Fetch deployment
      const dep = await prisma.deployment.findUnique({
        where: { id: body.deploymentId },
        select: { id: true, status: true, image: true, serviceId: true }
      });

      // If not found, ack (nothing to do)
      if (!dep) {
        ch.ack(msg);
        return;
      }

      if (!dep.image) throw new Error("image_not_built_yet");

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

      // 4) Run container via data-plane runner
      const host = process.env.DATA_PLANE_HOST ?? "localhost";
      const hostPort = await allocateHostPort();
      const containerName = `cp-${dep.id}`;

      await dockerPull(dep.image);

      const detectedPort = await detectContainerPort(dep.image).catch(() => null);
      const fallbackPort = Number(process.env.DEFAULT_CONTAINER_PORT ?? 3080);
      const containerPort = detectedPort ?? fallbackPort;

      const { containerId } = await dockerRun({
        image: dep.image,
        name: containerName,
        hostPort,
        containerPort,
        env: {
          // later: inject service env vars
          NODE_ENV: "production"
        }
      });
      
      const runtimeUrl = `http://${host}/s/${dep.serviceId}`;

      await writeNginxRoute({
        serviceId: dep.serviceId,
        hostPort
      });

      await reloadNginx();

      // 5) Update deployment status
      await prisma.deployment.update({
        where: { id: dep.id },
        data: {
          containerId,
          hostPort,
          containerPort,
          runtimeUrl,
          status: "deployed"
        }
      });

      // 6) Ack message
      ch.ack(msg);
    } catch (err) {
      // Retry strategy: re-publish with incremented header
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

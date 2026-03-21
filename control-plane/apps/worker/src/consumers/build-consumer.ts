import { getAmqpChannel } from "@/packages/shared/src/amqp.js";
import { prisma } from "@/packages/shared/src/db.js";
import { MQ } from "@/packages/shared/src/mq.js";
import { ensureTopology } from "@/packages/shared/src/topology.js";
import { EVENT_TYPES } from "@/packages/shared/src/events.js";
import path from "node:path";
import { realBuildAndPush } from "../build/build-exec.js";

const MAX_RETRIES = 5;

type BuildMsg = { deploymentId: string };

export async function startBuildConsumer() {
  await ensureTopology();
  const ch = await getAmqpChannel();

  console.log("[build-consumer] started, listening on queue:", MQ.BUILD_QUEUE);

  await ch.consume(MQ.BUILD_QUEUE, async (msg) => {
    if (!msg) {
      console.log("[build-consumer] received null message");
      return;
    }

    const retryCount = Number(msg.properties.headers?.["x-retry-count"] ?? 0);
    console.log("[build-consumer] received message, retry count:", retryCount);
    let depId = "";

    try {
      const body = JSON.parse(msg.content.toString()) as BuildMsg;
      console.log("[build-consumer] deploymentId:", body.deploymentId);
      depId = body.deploymentId;

      const dep = await prisma.deployment.findUnique({
        where: { id: depId },
        select: {
          id: true,
          status: true,
          image: true,
          commitSha: true,
          serviceId: true,
          service: { select: { repoUrl: true, branch: true, name: true, serviceType: true } }
        }
      });

      // If deployment doesn't exist, ack
      if (!dep) {
        console.log("[build-consumer] deployment not found:", depId);
        ch.ack(msg);
        return;
      }
      console.log("[build-consumer] found deployment, status:", dep.status, "serviceType:", dep.service.serviceType);

      // Skip build for managed services - they go straight to deploy
      if (dep.service.serviceType === "mongodb" || dep.service.serviceType === "redis") {
        console.log("[build-consumer] skipping build for managed service:", dep.service.serviceType);
        ch.ack(msg);
        return;
      }

      // repoUrl is required for docker/nodejs builds
      if (!dep.service.repoUrl) {
        throw new Error("repoUrl_required_for_build");
      }

      // Idempotency: if image already exists OR status not queued, nothing to do
      if (dep.image || dep.status !== "queued") {
        ch.ack(msg);
        return;
      }

      // Atomic claim: queued -> building (only one worker wins)
      const claimed = await prisma.deployment.updateMany({
        where: { id: dep.id, status: "queued", image: null },
        data: { status: "building" }
      });

      if (claimed.count !== 1) {
        ch.ack(msg);
        return;
      }

      // Build and push the image to the registry
      const registry = process.env.REGISTRY_HOST ?? "localhost:5000";
      const baseDir = process.env.BUILD_WORKDIR ?? "/tmp/cp-builds";

      const imageRef = `${registry}/${dep.serviceId}:${dep.commitSha}`;

      const workDir = path.join(baseDir, dep.id);

      console.log("[build-consumer] starting build for:", dep.serviceId, "image:", imageRef);
      await realBuildAndPush({
        repoUrl: dep.service.repoUrl,
        branch: dep.service.branch,
        commitSha: dep.commitSha,
        imageTag: imageRef,
        workDir,
        serviceType: dep.service.serviceType as "docker" | "nodejs" | "nextjs" | "react"
      });
      console.log("[build-consumer] build successful, enqueuing deploy");

      // On success: set image + enqueue deploy via Outbox in ONE txn
      await prisma.$transaction(async (tx) => {
        await tx.deployment.update({
          where: { id: dep.id },
          data: {
            image: imageRef,
            status: "queued" // ready for deploy
          }
        });

        await tx.outboxEvent.create({
          data: {
            type: EVENT_TYPES.DEPLOY_REQUESTED,
            payload: { deploymentId: dep.id }
          }
        });
      });

      console.log("[build-consumer] deploy event created");
      ch.ack(msg);
    } catch (err) {
      console.error("[build-consumer] error:", err);

      if (retryCount >= MAX_RETRIES) {
        ch.sendToQueue(MQ.DLQ, msg.content, {
          persistent: true,
          contentType: msg.properties.contentType,
          messageId: msg.properties.messageId,
          headers: { ...msg.properties.headers, "x-retry-count": retryCount }
        });
        ch.ack(msg);
        await prisma.deployment.update({
          where: { id: depId },
          data: { status: "failed" }
        });
        return;
      }

      ch.publish(MQ.EXCHANGE, MQ.BUILD_ROUTING_KEY, msg.content, {
        persistent: true,
        contentType: msg.properties.contentType,
        messageId: msg.properties.messageId,
        headers: { ...msg.properties.headers, "x-retry-count": retryCount + 1 }
      });

      if (depId) {
        await prisma.deployment.update({
          where: { id: depId },
          data: { status: "queued" }
        });
        console.log("[build-consumer] reset status to queued for retry");
      }
      
      ch.ack(msg);
    }
  });
}

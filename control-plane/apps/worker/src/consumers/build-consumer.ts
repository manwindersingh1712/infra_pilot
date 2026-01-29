import { getAmqpChannel } from "@/packages/shared/src/amqp.js";
import { prisma } from "@/packages/shared/src/db.js";
import { MQ } from "@/packages/shared/src/mq.js";
import { ensureTopology } from "@/packages/shared/src/topology.js";
import { EVENT_TYPES } from "@/packages/shared/src/events.js";

const MAX_RETRIES = 5;

type BuildMsg = { deploymentId: string };

export async function startBuildConsumer() {
  await ensureTopology();
  const ch = await getAmqpChannel();

  await ch.consume(MQ.BUILD_QUEUE, async (msg) => {
    if (!msg) return;

    const retryCount = Number(msg.properties.headers?.["x-retry-count"] ?? 0);

    try {
      const body = JSON.parse(msg.content.toString()) as BuildMsg;

      const dep = await prisma.deployment.findUnique({
        where: { id: body.deploymentId },
        select: {
          id: true,
          status: true,
          image: true,
          commitSha: true,
          serviceId: true,
          service: { select: { repoUrl: true, branch: true, name: true } }
        }
      });

      // If deployment doesn't exist, ack
      if (!dep) {
        ch.ack(msg);
        return;
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

      // ---- BUILD (v0 simulated) ----
      // Later: git clone + docker build + push to registry
      // For now: generate deterministic image ref
      const imageRef = `local-registry/${dep.serviceId}:${dep.commitSha}`;

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

      ch.ack(msg);
    } catch (err) {
      // retry republish
      if (retryCount >= MAX_RETRIES) {
        ch.sendToQueue(MQ.DLQ, msg.content, {
          persistent: true,
          contentType: msg.properties.contentType,
          messageId: msg.properties.messageId,
          headers: { ...msg.properties.headers, "x-retry-count": retryCount }
        });
        ch.ack(msg);
        return;
      }

      ch.publish(MQ.EXCHANGE, MQ.BUILD_ROUTING_KEY, msg.content, {
        persistent: true,
        contentType: msg.properties.contentType,
        messageId: msg.properties.messageId,
        headers: { ...msg.properties.headers, "x-retry-count": retryCount + 1 }
      });

      ch.ack(msg);
    }
  });
}

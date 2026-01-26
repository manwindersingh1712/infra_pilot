import { getAmqpChannel } from "@/packages/shared/src/amqp.js";
import { prisma } from "@/packages/shared/src/db.js";
import { MQ } from "@/packages/shared/src/mq.js";
import { ensureTopology } from "@/packages/shared/src/topology.js";

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
        select: { id: true, status: true }
      });

      // If not found, ack (nothing to do)
      if (!dep) {
        ch.ack(msg);
        return;
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

      // 4) TODO: call data-plane runner to run container
      // For now, simulate deploy work
      // await callRunner(...)

      // 5) Mark deployed
      await prisma.deployment.update({
        where: { id: dep.id },
        data: { status: "deployed" }
      });

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

import { prisma } from "@/packages/shared/src/db.js";
import { getAmqpChannel } from "@/packages/shared/src/amqp.js";
import { MQ } from "@/packages/shared/src/mq.js";
import { ensureTopology } from "@/packages/shared/src/topology.js";
import { EVENT_TYPES } from "@/packages/shared/src/events.js";

const BATCH_SIZE = 25;
const MAX_ATTEMPTS = 10;

// exponential-ish backoff (seconds)
function backoffSeconds(attempt: number) {
  const base = Math.min(60, 2 ** Math.min(attempt, 6)); // 2,4,8..64 capped at 60
  return base + Math.floor(Math.random() * 5);
}

type OutboxEvent = {
  id: string;
  type: string;
  payload: any;
  attempts: number;
}

export async function startOutboxPublisher() {
  await ensureTopology();
  const ch = await getAmqpChannel();
  console.log("[outbox-publisher] started");

  // loop
  setInterval(async () => {
    try {
      await prisma.$transaction(async (tx) => {
        // Claim events using SKIP LOCKED so multiple publishers can run
        const events = await tx.$queryRaw<Array<OutboxEvent>>`
          SELECT id, type, payload, attempts
          FROM "OutboxEvent"
          WHERE status = 'pending'
            AND "nextAttemptAt" <= now()
          ORDER BY "createdAt" ASC
          FOR UPDATE SKIP LOCKED
          LIMIT ${BATCH_SIZE}
        `;

        if (events.length === 0) return;
        console.log("[outbox-publisher] processing", events.length, "events");

        // Mark as publishing (still inside same transaction)
        const ids = events.map((e) => e.id);
        await tx.outboxEvent.updateMany({
          where: { id: { in: ids } },
          data: { status: "publishing" }
        });

        // Publish outside DB changes but still inside same interval tick
        for (const e of events) {
          try {
            const body = Buffer.from(JSON.stringify(e.payload));

            // Map Outbox type -> routing key
            const routingKey =
              e.type === EVENT_TYPES.DEPLOY_REQUESTED ? MQ.DEPLOY_ROUTING_KEY :
              e.type === EVENT_TYPES.BUILD_REQUESTED ? MQ.BUILD_ROUTING_KEY :
              e.type; // fallback

            ch.publish(MQ.EXCHANGE, routingKey, body, {
              persistent: true,
              contentType: "application/json",
              messageId: e.id
            });
            console.log("[outbox-publisher] published event:", e.type, "->", routingKey, "payload:", e.payload);

            await tx.outboxEvent.update({
              where: { id: e.id },
              data: {
                status: "published",
                publishedAt: new Date(),
                lastError: null
              }
            });
          } catch (err: any) {
            const attempts = e.attempts + 1;

            if (attempts >= MAX_ATTEMPTS) {
              await tx.outboxEvent.update({
                where: { id: e.id },
                data: {
                  status: "dead",
                  attempts,
                  lastError: String(err?.message ?? err)
                }
              });
            } else {
              const delay = backoffSeconds(attempts);
              await tx.outboxEvent.update({
                where: { id: e.id },
                data: {
                  status: "pending",
                  attempts,
                  lastError: String(err?.message ?? err),
                  nextAttemptAt: new Date(Date.now() + delay * 1000)
                }
              });
            }
          }
        }
      });
    } catch {
      // swallow to keep loop alive; logs can be added later
    }
  }, 1000);
}

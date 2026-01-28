import { prisma } from "@/packages/shared/src/db.js";
import { closeAmqp } from "@/packages/shared/src/amqp.js";
import { startDeployConsumer } from "@/apps/worker/src/consumers/deploy-consumer.js";
import { startOutboxPublisher } from "./outbox/publisher.js";

console.log("worker up");
await prisma.$queryRaw`SELECT 1`;

await startDeployConsumer();
await startOutboxPublisher();

const shutdown = async () => {
  console.log("worker shutting down...");
  try { await prisma.$disconnect(); } catch {}
  try { await closeAmqp(); } catch {}
  process.exit(0);
};

process.on("SIGTERM", shutdown);
process.on("SIGINT", shutdown);

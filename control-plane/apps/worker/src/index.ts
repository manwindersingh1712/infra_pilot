import { prisma } from "@/packages/shared/src/db.js";
import { closeAmqp } from "@/packages/shared/src/amqp.js";
import { startDeployConsumer } from "@/apps/worker/src/consumers/deploy-consumer.js";
import { startOutboxPublisher } from "./outbox/publisher.js";
import { startBuildConsumer } from "./consumers/build-consumer.js";
import { startFlushJob } from "./jobs/flush-logs-job.js";

console.log("worker up");
await prisma.$queryRaw`SELECT 1`;

await startBuildConsumer();
await startDeployConsumer();
await startOutboxPublisher();

// Start background job to flush logs to ClickHouse/S3
startFlushJob();

const shutdown = async () => {
  console.log("worker shutting down...");
  try { await prisma.$disconnect(); } catch {}
  try { await closeAmqp(); } catch {}
  process.exit(0);
};

process.on("SIGTERM", shutdown);
process.on("SIGINT", shutdown);

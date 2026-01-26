import "dotenv/config";
import { prisma } from "@/packages/shared/src/db.js";
import { getAmqpChannel, closeAmqp } from "@/packages/shared/src/amqp.js";

console.log("worker up");

await prisma.$queryRaw`SELECT 1`;
await getAmqpChannel();

const shutdown = async () => {
  console.log("worker shutting down...");
  try { await prisma.$disconnect(); } catch {}
  try { await closeAmqp(); } catch {}
  process.exit(0);
};

process.on("SIGTERM", shutdown);
process.on("SIGINT", shutdown);

// Keep alive
setInterval(() => {}, 1 << 30);

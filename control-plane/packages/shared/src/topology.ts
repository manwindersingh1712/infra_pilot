import { getAmqpChannel } from "./amqp.js";
import { MQ } from "./mq.js";

export async function ensureTopology() {
  const ch = await getAmqpChannel();

  // Exchange (topic so we can route build/deploy/other job types later)
  await ch.assertExchange(MQ.EXCHANGE, "topic", { durable: true });

  // Main queues
  await ch.assertQueue(MQ.DEPLOY_QUEUE, {
    durable: true,
    // dead-letter all rejected/expired messages to DLQ
    deadLetterExchange: "",
    deadLetterRoutingKey: MQ.DLQ
  });

  await ch.assertQueue(MQ.BUILD_QUEUE, {
    durable: true,
    deadLetterExchange: "",
    deadLetterRoutingKey: MQ.DLQ
  });

  // DLQ
  await ch.assertQueue(MQ.DLQ, { durable: true });

  // Bindings
  await ch.bindQueue(MQ.DEPLOY_QUEUE, MQ.EXCHANGE, MQ.DEPLOY_ROUTING_KEY);
  await ch.bindQueue(MQ.BUILD_QUEUE, MQ.EXCHANGE, MQ.BUILD_ROUTING_KEY);

  return true;
}

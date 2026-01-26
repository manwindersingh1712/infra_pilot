import { getAmqpChannel } from "../../../../packages/shared/src/amqp.js";
import { MQ } from "../../../../packages/shared/src/mq.js";

export async function publishDeployRequested(payload: { deploymentId: string }) {
  const ch = await getAmqpChannel();

  const body = Buffer.from(JSON.stringify(payload));

  ch.publish(MQ.EXCHANGE, MQ.DEPLOY_ROUTING_KEY, body, {
    persistent: true,
    contentType: "application/json",
    messageId: payload.deploymentId // for traceability/dedup auditing
  });
}

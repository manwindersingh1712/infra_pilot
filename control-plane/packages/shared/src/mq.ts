export const MQ = {
  EXCHANGE: "cp.jobs",
  BUILD_ROUTING_KEY: "build.requested",
  DEPLOY_ROUTING_KEY: "deploy.requested",
  BUILD_QUEUE: "cp.build.q",
  DEPLOY_QUEUE: "cp.deploy.q",
  DLQ: "cp.dlq.q"
} as const;

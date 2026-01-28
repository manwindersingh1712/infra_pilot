export const EVENT_TYPES = {
    DEPLOY_REQUESTED: "deploy.requested",
    BUILD_REQUESTED: "build.requested"
} as const;

export type EventType = (typeof EVENT_TYPES)[keyof typeof EVENT_TYPES];

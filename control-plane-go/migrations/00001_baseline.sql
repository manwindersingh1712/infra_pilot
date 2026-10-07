-- +goose Up
CREATE TYPE "DeploymentStatus" AS ENUM ('queued', 'building', 'deploying', 'deployed', 'failed');
CREATE TYPE "OutboxStatus" AS ENUM ('pending', 'publishing', 'published', 'dead');
CREATE TYPE "ServiceType" AS ENUM ('docker', 'nodejs', 'mongodb', 'redis', 'nextjs', 'react');

CREATE TABLE "User" (
    "id" TEXT NOT NULL,
    "email" TEXT NOT NULL,
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "name" TEXT,
    "password" TEXT NOT NULL,
    "updatedAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "User_pkey" PRIMARY KEY ("id")
);

CREATE TABLE "Project" (
    "id" TEXT NOT NULL,
    "name" TEXT NOT NULL,
    "ownerUserId" TEXT NOT NULL,
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "Project_pkey" PRIMARY KEY ("id")
);

CREATE TABLE "Service" (
    "id" TEXT NOT NULL,
    "projectId" TEXT NOT NULL,
    "name" TEXT NOT NULL,
    "repoUrl" TEXT,
    "branch" TEXT NOT NULL DEFAULT 'main',
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "serviceType" "ServiceType" NOT NULL DEFAULT 'docker',
    "positionX" DOUBLE PRECISION NOT NULL DEFAULT 0,
    "positionY" DOUBLE PRECISION NOT NULL DEFAULT 0,
    CONSTRAINT "Service_pkey" PRIMARY KEY ("id")
);

CREATE TABLE "EnvVar" (
    "id" TEXT NOT NULL,
    "serviceId" TEXT NOT NULL,
    "key" TEXT NOT NULL,
    "value" TEXT NOT NULL,
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "EnvVar_pkey" PRIMARY KEY ("id")
);

CREATE TABLE "Deployment" (
    "id" TEXT NOT NULL,
    "serviceId" TEXT NOT NULL,
    "commitSha" TEXT NOT NULL,
    "image" TEXT,
    "status" "DeploymentStatus" NOT NULL DEFAULT 'queued',
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "containerId" TEXT,
    "containerPort" INTEGER,
    "hostPort" INTEGER,
    "runtimeUrl" TEXT,
    "updatedAt" TIMESTAMP(3) NOT NULL,
    "volumePath" TEXT,
    CONSTRAINT "Deployment_pkey" PRIMARY KEY ("id")
);

CREATE TABLE "OutboxEvent" (
    "id" TEXT NOT NULL,
    "type" TEXT NOT NULL,
    "payload" JSONB NOT NULL,
    "status" "OutboxStatus" NOT NULL DEFAULT 'pending',
    "attempts" INTEGER NOT NULL DEFAULT 0,
    "nextAttemptAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "lastError" TEXT,
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "publishedAt" TIMESTAMP(3),
    CONSTRAINT "OutboxEvent_pkey" PRIMARY KEY ("id")
);

CREATE TABLE "ServiceConnection" (
    "id" TEXT NOT NULL,
    "sourceId" TEXT NOT NULL,
    "targetId" TEXT NOT NULL,
    "label" TEXT,
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "ServiceConnection_pkey" PRIMARY KEY ("id")
);

CREATE UNIQUE INDEX "User_email_key" ON "User"("email");
CREATE INDEX "Service_projectId_idx" ON "Service"("projectId");
CREATE UNIQUE INDEX "EnvVar_serviceId_key_key" ON "EnvVar"("serviceId", "key");
CREATE INDEX "Deployment_serviceId_createdAt_idx" ON "Deployment"("serviceId", "createdAt");
CREATE INDEX "OutboxEvent_status_nextAttemptAt_idx" ON "OutboxEvent"("status", "nextAttemptAt");
CREATE INDEX "OutboxEvent_createdAt_idx" ON "OutboxEvent"("createdAt");
CREATE INDEX "ServiceConnection_sourceId_idx" ON "ServiceConnection"("sourceId");
CREATE INDEX "ServiceConnection_targetId_idx" ON "ServiceConnection"("targetId");
CREATE UNIQUE INDEX "ServiceConnection_sourceId_targetId_key" ON "ServiceConnection"("sourceId", "targetId");

ALTER TABLE "Project" ADD CONSTRAINT "Project_ownerUserId_fkey" FOREIGN KEY ("ownerUserId") REFERENCES "User"("id") ON DELETE RESTRICT ON UPDATE CASCADE;
ALTER TABLE "Service" ADD CONSTRAINT "Service_projectId_fkey" FOREIGN KEY ("projectId") REFERENCES "Project"("id") ON DELETE RESTRICT ON UPDATE CASCADE;
ALTER TABLE "EnvVar" ADD CONSTRAINT "EnvVar_serviceId_fkey" FOREIGN KEY ("serviceId") REFERENCES "Service"("id") ON DELETE RESTRICT ON UPDATE CASCADE;
ALTER TABLE "Deployment" ADD CONSTRAINT "Deployment_serviceId_fkey" FOREIGN KEY ("serviceId") REFERENCES "Service"("id") ON DELETE RESTRICT ON UPDATE CASCADE;
ALTER TABLE "ServiceConnection" ADD CONSTRAINT "ServiceConnection_sourceId_fkey" FOREIGN KEY ("sourceId") REFERENCES "Service"("id") ON DELETE CASCADE ON UPDATE CASCADE;
ALTER TABLE "ServiceConnection" ADD CONSTRAINT "ServiceConnection_targetId_fkey" FOREIGN KEY ("targetId") REFERENCES "Service"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- +goose Down
DROP TABLE "ServiceConnection", "OutboxEvent", "Deployment", "EnvVar", "Service", "Project", "User";
DROP TYPE "ServiceType", "OutboxStatus", "DeploymentStatus";

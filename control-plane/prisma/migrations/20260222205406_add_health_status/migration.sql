-- CreateEnum
CREATE TYPE "HealthStatus" AS ENUM ('starting', 'healthy', 'unhealthy', 'crashed');

-- AlterTable
ALTER TABLE "Deployment" ADD COLUMN "healthStatus" "HealthStatus" DEFAULT 'starting';

-- CreateIndex
CREATE INDEX "Deployment_status_healthStatus_idx" ON "Deployment"("status", "healthStatus");

-- CreateEnum
CREATE TYPE "ServiceType" AS ENUM ('docker', 'nodejs', 'mongodb', 'redis');

-- AlterTable
ALTER TABLE "Deployment" ADD COLUMN     "volumePath" TEXT;

-- AlterTable
ALTER TABLE "Service" ADD COLUMN     "serviceType" "ServiceType" NOT NULL DEFAULT 'docker',
ALTER COLUMN "repoUrl" DROP NOT NULL;

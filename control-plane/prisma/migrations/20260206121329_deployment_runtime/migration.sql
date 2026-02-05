/*
  Warnings:

  - Added the required column `updatedAt` to the `Deployment` table without a default value. This is not possible if the table is not empty.

*/
-- AlterTable
ALTER TABLE "Deployment" ADD COLUMN     "containerId" TEXT,
ADD COLUMN     "containerPort" INTEGER,
ADD COLUMN     "hostPort" INTEGER,
ADD COLUMN     "runtimeUrl" TEXT,
ADD COLUMN     "updatedAt" TIMESTAMP(3) NOT NULL;

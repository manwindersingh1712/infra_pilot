-- AlterTable
ALTER TABLE "Service" ADD COLUMN     "positionX" DOUBLE PRECISION NOT NULL DEFAULT 0,
ADD COLUMN     "positionY" DOUBLE PRECISION NOT NULL DEFAULT 0;

-- CreateTable
CREATE TABLE "ServiceConnection" (
    "id" TEXT NOT NULL,
    "sourceId" TEXT NOT NULL,
    "targetId" TEXT NOT NULL,
    "label" TEXT,
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "ServiceConnection_pkey" PRIMARY KEY ("id")
);

-- CreateIndex
CREATE INDEX "ServiceConnection_sourceId_idx" ON "ServiceConnection"("sourceId");

-- CreateIndex
CREATE INDEX "ServiceConnection_targetId_idx" ON "ServiceConnection"("targetId");

-- CreateIndex
CREATE UNIQUE INDEX "ServiceConnection_sourceId_targetId_key" ON "ServiceConnection"("sourceId", "targetId");

-- AddForeignKey
ALTER TABLE "ServiceConnection" ADD CONSTRAINT "ServiceConnection_sourceId_fkey" FOREIGN KEY ("sourceId") REFERENCES "Service"("id") ON DELETE CASCADE ON UPDATE CASCADE;

-- AddForeignKey
ALTER TABLE "ServiceConnection" ADD CONSTRAINT "ServiceConnection_targetId_fkey" FOREIGN KEY ("targetId") REFERENCES "Service"("id") ON DELETE CASCADE ON UPDATE CASCADE;

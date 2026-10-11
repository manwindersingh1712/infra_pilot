-- +goose Up
ALTER TABLE "Service" ADD COLUMN "deletedAt" TIMESTAMP(3);

CREATE TABLE "RefreshToken" (
    "id" TEXT NOT NULL,
    "userId" TEXT NOT NULL,
    "tokenHash" TEXT NOT NULL,
    "expiresAt" TIMESTAMP(3) NOT NULL,
    "revokedAt" TIMESTAMP(3),
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT "RefreshToken_pkey" PRIMARY KEY ("id")
);
CREATE UNIQUE INDEX "RefreshToken_tokenHash_key" ON "RefreshToken"("tokenHash");
CREATE INDEX "RefreshToken_userId_idx" ON "RefreshToken"("userId");
ALTER TABLE "RefreshToken" ADD CONSTRAINT "RefreshToken_userId_fkey"
    FOREIGN KEY ("userId") REFERENCES "User"("id") ON DELETE CASCADE ON UPDATE CASCADE;

CREATE INDEX "Deployment_serviceId_stoppedAt_idx" ON "Deployment"("serviceId", "stoppedAt");

-- +goose Down
DROP INDEX "Deployment_serviceId_stoppedAt_idx";
DROP TABLE "RefreshToken";
ALTER TABLE "Service" DROP COLUMN "deletedAt";

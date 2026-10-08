-- +goose Up
ALTER TABLE "Deployment" ADD COLUMN "stoppedAt" TIMESTAMP(3);

-- +goose Down
ALTER TABLE "Deployment" DROP COLUMN "stoppedAt";

// control-plane/apps/api/src/services/s3-logs.ts
import { S3Client, PutObjectCommand, GetObjectCommand, ListObjectsV2Command } from "@aws-sdk/client-s3";
import { LogEntry } from "./redis.js";
import { gzipSync, gunzipSync } from "zlib";

const s3Client = new S3Client({
  region: process.env.AWS_REGION || "us-east-1",
  endpoint: process.env.S3_ENDPOINT, // For MinIO/localstack in dev
  credentials: {
    accessKeyId: process.env.AWS_ACCESS_KEY_ID || "minioadmin",
    secretAccessKey: process.env.AWS_SECRET_ACCESS_KEY || "minioadmin"
  },
  forcePathStyle: !!process.env.S3_ENDPOINT // Required for MinIO
});

const BUCKET_NAME = process.env.LOGS_BUCKET || "container-logs";

interface LogBatch {
  deploymentId: string;
  logs: LogEntry[];
  startTime: Date;
  endTime: Date;
}

/**
 * Convert logs to NDJSON format (newline-delimited JSON)
 */
function logsToBuffer(logs: LogEntry[]): Buffer {
  const lines = logs.map(l => JSON.stringify(l)).join("\n");
  return Buffer.from(lines, "utf-8");
}

/**
 * Upload log batch to S3
 */
export async function uploadLogBatch(batch: LogBatch): Promise<string> {
  const datePrefix = batch.startTime.toISOString().split("T")[0]; // YYYY-MM-DD
  const key = `logs/${batch.deploymentId}/${datePrefix}/${batch.startTime.getTime()}.ndjson.gz`;

  const buffer = logsToBuffer(batch.logs);
  const compressed = gzipSync(buffer);

  await s3Client.send(new PutObjectCommand({
    Bucket: BUCKET_NAME,
    Key: key,
    Body: compressed,
    ContentType: "application/x-ndjson",
    ContentEncoding: "gzip",
    Metadata: {
      "deployment-id": batch.deploymentId,
      "start-time": batch.startTime.toISOString(),
      "end-time": batch.endTime.toISOString(),
      "log-count": String(batch.logs.length)
    }
  }));

  return key;
}

/**
 * Download logs from S3 for a deployment within time range
 */
export async function downloadLogs(
  deploymentId: string,
  startTime: Date,
  endTime: Date
): Promise<LogEntry[]> {
  const listResponse = await s3Client.send(new ListObjectsV2Command({
    Bucket: BUCKET_NAME,
    Prefix: `logs/${deploymentId}/`
  }));

  const keys = (listResponse.Contents || [])
    .filter(obj => obj.Key && obj.LastModified)
    .filter(obj => {
      const time = obj.LastModified!;
      return time >= startTime && time <= endTime;
    })
    .map(obj => obj.Key!);

  const allLogs: LogEntry[] = [];

  for (const key of keys) {
    try {
      const response = await s3Client.send(new GetObjectCommand({
        Bucket: BUCKET_NAME,
        Key: key
      }));

      const bytes = await response.Body?.transformToByteArray();
      if (!bytes) continue;

      const decompressed = gunzipSync(Buffer.from(bytes));
      const lines = decompressed.toString("utf-8").trim().split("\n");

      for (const line of lines) {
        if (line) allLogs.push(JSON.parse(line));
      }
    } catch (err) {
      console.error(`[s3-logs] Failed to download ${key}:`, err);
    }
  }

  // Sort by timestamp
  return allLogs.sort((a, b) =>
    new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime()
  );
}

export { s3Client, BUCKET_NAME };

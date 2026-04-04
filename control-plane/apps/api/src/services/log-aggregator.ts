import * as redis from "./redis.js";
import * as s3Logs from "./s3-logs.js";
import * as clickhouse from "./clickhouse.js";
import { LogEntry } from "./redis.js";

export type { LogEntry } from "./redis.js";

interface QueryOptions {
  deploymentId: string;
  startTime?: Date;
  endTime?: Date;
  limit?: number;
  sources?: ("stdout" | "stderr" | "system")[];
  search?: string;
}

// Optional broadcast callback for Socket.io
let broadcastCallback: ((deploymentId: string, log: LogEntry) => void) | null = null;

export function setBroadcastCallback(
  callback: (deploymentId: string, log: LogEntry) => void
): void {
  broadcastCallback = callback;
}

/**
 * Write a log entry (called by worker)
 * Path: Worker -> Redis (hot) + broadcast
 */
export async function writeLog(entry: LogEntry): Promise<void> {
  // Always write to Redis for real-time access
  await redis.addLogEntry(entry);

  // Publish to Pub/Sub for immediate broadcast
  await redis.publishLogEntry(entry);

  // Also broadcast via callback if set (Socket.io)
  broadcastCallback?.(entry.deploymentId, entry);

  // Note: S3 and ClickHouse are written by background batch job, not real-time
}

/**
 * Query logs with automatic tiering:
 * 1. Recent (< 10 min): Redis only
 * 2. Medium-term (10 min - 30 days): ClickHouse
 * 3. Long-term (> 30 days): S3 (if implemented)
 */
export async function queryLogs(options: QueryOptions): Promise<{
  logs: LogEntry[];
  source: "redis" | "clickhouse" | "s3";
  totalCount?: number;
}> {
  const { deploymentId, startTime, endTime, limit = 100 } = options;

  const now = new Date();
  const tenMinutesAgo = new Date(now.getTime() - 10 * 60 * 1000);

  // Case 1: Only recent logs requested - query Redis
  if (!startTime || startTime > tenMinutesAgo) {
    const logs = await redis.readLogs(deploymentId, { count: limit });
    if (logs.length > 0) {
      return { logs, source: "redis" };
    }
  }

  // Case 2: Query ClickHouse for historical logs
  const logs = await clickhouse.queryLogs({
    deploymentId,
    startTime,
    endTime,
    sources: options.sources,
    search: options.search,
    limit
  });

  // If we have Redis data too, merge it (Redis is more recent)
  if (!startTime || (endTime && endTime > tenMinutesAgo)) {
    const redisLogs = await redis.readLogs(deploymentId, { count: limit });
    const redisSet = new Set(redisLogs.map(l => `${l.timestamp}-${l.message}`));

    // Filter out duplicates
    const uniqueClickHouse = logs.filter(
      l => !redisSet.has(`${l.timestamp}-${l.message}`)
    );

    return {
      logs: [...uniqueClickHouse, ...redisLogs].slice(-limit),
      source: "clickhouse",
      totalCount: await clickhouse.getLogCount(deploymentId)
    };
  }

  return { logs, source: "clickhouse" };
}

/**
 * Subscribe to real-time logs
 */
export function subscribeToLogs(
  deploymentId: string,
  callback: (entry: LogEntry) => void
): () => void {
  return redis.subscribeToLogs(deploymentId, callback);
}

/**
 * Background job: Flush Redis logs to S3 and ClickHouse
 * Should run every 5 minutes via cron or similar
 */
export async function flushLogsToColdStorage(): Promise<{
  processed: number;
  errors: number;
}> {
  const streamIds = await redis.getActiveLogStreams();
  let processed = 0;
  let errors = 0;

  for (const deploymentId of streamIds) {
    try {
      // Read all logs from Redis stream
      const logs = await redis.readLogs(deploymentId, {
        start: "-",
        count: 10000
      });

      if (logs.length === 0) continue;

      // Write to ClickHouse
      await clickhouse.insertLogs(logs);

      // Write to S3 for backup (every 1000 logs or when stream is large)
      if (logs.length >= 1000) {
        await s3Logs.uploadLogBatch({
          deploymentId,
          logs,
          startTime: new Date(logs[0].timestamp),
          endTime: new Date(logs[logs.length - 1].timestamp)
        });
      }

      processed += logs.length;
    } catch (err) {
      console.error(`[log-aggregator] Failed to flush ${deploymentId}:`, err);
      errors++;
    }
  }

  return { processed, errors };
}

/**
 * Get active log streams count (for monitoring)
 */
export async function getActiveStreamCount(): Promise<number> {
  const streams = await redis.getActiveLogStreams();
  return streams.length;
}

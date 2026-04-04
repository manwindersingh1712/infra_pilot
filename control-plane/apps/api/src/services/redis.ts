import { Redis } from "ioredis";

const redis = new Redis(process.env.REDIS_URL || "redis://localhost:6379");

export interface LogEntry {
  deploymentId: string;
  source: "stdout" | "stderr" | "system";
  message: string;
  timestamp: string;
}

const STREAM_MAX_LEN = 10000; // Max entries per stream
const STREAM_TTL_MS = 10 * 60 * 1000; // 10 minutes

/**
 * Add log entry to Redis Stream for a deployment
 */
export async function addLogEntry(entry: LogEntry): Promise<void> {
  const streamKey = `logs:stream:${entry.deploymentId}`;

  // Add to stream with maxlen to prevent unbounded growth
  await redis.xadd(
    streamKey,
    "MAXLEN",
    "~",
    STREAM_MAX_LEN,
    "*", // Auto-generate ID
    "source", entry.source,
    "message", entry.message,
    "timestamp", entry.timestamp
  );

  // Set expiration on the stream
  await redis.pexpire(streamKey, STREAM_TTL_MS);
}

/**
 * Read logs from Redis Stream with pagination
 */
export async function readLogs(
  deploymentId: string,
  options: {
    start?: string; // Stream ID to start from
    count?: number;
    block?: number; // Block milliseconds (for blocking read)
  } = {}
): Promise<LogEntry[]> {
  const { start = "-", count = 100, block } = options;
  const streamKey = `logs:stream:${deploymentId}`;

  let results: [string, string[]][];

  if (block) {
    // Blocking read for real-time - using any to work around ioredis types
    const response = await (redis as any).xread(
      "BLOCK", block,
      "COUNT", count,
      "STREAMS", streamKey, start
    );
    results = (response?.[0]?.[1] || []) as [string, string[]][];
  } else {
    // Non-blocking read
    results = await redis.xrange(streamKey, start, "+", "COUNT", count);
  }

  return results.map(([_id, fields]) => {
    const map = new Map<string, string>();
    for (let i = 0; i < fields.length; i += 2) {
      map.set(fields[i], fields[i + 1]);
    }
    return {
      deploymentId,
      source: map.get("source") as LogEntry["source"],
      message: map.get("message") || "",
      timestamp: map.get("timestamp") || new Date().toISOString()
    };
  });
}

/**
 * Subscribe to new logs using Redis Pub/Sub for real-time
 */
export function subscribeToLogs(
  deploymentId: string,
  callback: (entry: LogEntry) => void
): () => void {
  const pubsub = new Redis(process.env.REDIS_URL || "redis://localhost:6379");
  const channel = `logs:pubsub:${deploymentId}`;

  pubsub.subscribe(channel);
  pubsub.on("message", (ch: string, message: string) => {
    if (ch === channel) {
      callback(JSON.parse(message));
    }
  });

  return () => {
    pubsub.unsubscribe(channel);
    pubsub.disconnect();
  };
}

/**
 * Publish log entry to Pub/Sub for WebSocket broadcast
 */
export async function publishLogEntry(entry: LogEntry): Promise<void> {
  const channel = `logs:pubsub:${entry.deploymentId}`;
  await redis.publish(channel, JSON.stringify(entry));
}

/**
 * Get all active stream keys (for cleanup/monitoring)
 */
export async function getActiveLogStreams(): Promise<string[]> {
  const keys = await redis.keys("logs:stream:*");
  return keys.map((k: string) => k.replace("logs:stream:", ""));
}

/**
 * Delete log stream for a deployment
 */
export async function deleteLogStream(deploymentId: string): Promise<void> {
  await redis.del(`logs:stream:${deploymentId}`);
}

export { redis };

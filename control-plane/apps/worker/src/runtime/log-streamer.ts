// control-plane/apps/worker/src/runtime/log-streamer.ts
import { spawn } from "child_process";
import { Redis } from "ioredis";

const redis = new Redis(process.env.REDIS_URL || "redis://localhost:6379");

interface ActiveStream {
  deploymentId: string;
  containerId: string;
  process: ReturnType<typeof spawn>;
  startTime: Date;
}

const activeStreams = new Map<string, ActiveStream>();

interface LogEntry {
  deploymentId: string;
  source: "stdout" | "stderr" | "system";
  message: string;
  timestamp: string;
}

async function publishLog(entry: LogEntry): Promise<void> {
  const streamKey = `logs:stream:${entry.deploymentId}`;
  const channel = `logs:pubsub:${entry.deploymentId}`;

  // Add to Redis Stream
  await redis.xadd(
    streamKey,
    "MAXLEN",
    "~",
    10000,
    "*",
    "source", entry.source,
    "message", entry.message,
    "timestamp", entry.timestamp
  );

  // Set expiration
  await redis.pexpire(streamKey, 10 * 60 * 1000);

  // Publish to Pub/Sub for real-time
  await redis.publish(channel, JSON.stringify(entry));
}

export function startLogStreaming(deploymentId: string, containerId: string): void {
  if (activeStreams.has(deploymentId)) {
    console.log("[log-streamer] already streaming:", deploymentId);
    return;
  }

  console.log("[log-streamer] starting:", deploymentId, "container:", containerId);

  const process = spawn("docker", ["logs", "-f", "--tail=100", containerId]);

  activeStreams.set(deploymentId, {
    deploymentId,
    containerId,
    process,
    startTime: new Date()
  });

  // Handle stdout
  process.stdout.on("data", async (data: Buffer) => {
    const lines = data.toString().split("\n").filter(l => l.length > 0);
    for (const line of lines) {
      await publishLog({
        deploymentId,
        source: "stdout",
        message: line,
        timestamp: new Date().toISOString()
      });
    }
  });

  // Handle stderr
  process.stderr.on("data", async (data: Buffer) => {
    const lines = data.toString().split("\n").filter(l => l.length > 0);
    for (const line of lines) {
      await publishLog({
        deploymentId,
        source: "stderr",
        message: line,
        timestamp: new Date().toISOString()
      });
    }
  });

  // Handle exit
  process.on("close", async (code) => {
    console.log("[log-streamer] ended:", deploymentId, "code:", code);
    activeStreams.delete(deploymentId);

    await publishLog({
      deploymentId,
      source: "system",
      message: `Log stream ended (exit code: ${code})`,
      timestamp: new Date().toISOString()
    });
  });

  // Handle errors
  process.on("error", async (err) => {
    console.error("[log-streamer] error:", deploymentId, err);
    activeStreams.delete(deploymentId);

    await publishLog({
      deploymentId,
      source: "system",
      message: `Log stream error: ${err.message}`,
      timestamp: new Date().toISOString()
    });
  });
}

export function stopLogStreaming(deploymentId: string): void {
  const stream = activeStreams.get(deploymentId);
  if (stream) {
    console.log("[log-streamer] stopping:", deploymentId);
    stream.process.kill();
    activeStreams.delete(deploymentId);
  }
}

export function getActiveStreams(): string[] {
  return Array.from(activeStreams.keys());
}

export function isStreaming(deploymentId: string): boolean {
  return activeStreams.has(deploymentId);
}

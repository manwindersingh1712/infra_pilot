// control-plane/apps/worker/src/jobs/flush-logs-job.ts
import { flushLogsToColdStorage } from "@/apps/api/src/services/log-aggregator.js";

let intervalId: NodeJS.Timeout | null = null;

export function startFlushJob(intervalMs = 5 * 60 * 1000): void {
  console.log("[flush-logs-job] Starting with interval:", intervalMs, "ms");

  intervalId = setInterval(async () => {
    console.log("[flush-logs-job] Running flush...");
    const result = await flushLogsToColdStorage();
    console.log("[flush-logs-job] Flushed:", result.processed, "errors:", result.errors);
  }, intervalMs);
}

export function stopFlushJob(): void {
  if (intervalId) {
    clearInterval(intervalId);
    intervalId = null;
  }
}

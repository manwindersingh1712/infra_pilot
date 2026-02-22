import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { DOCKER_NETWORK } from "./docker-runner.js";

const execFileAsync = promisify(execFile);

/**
 * Health check configuration
 */
const HEALTH_CHECK_TIMEOUT_MS = Number(process.env.HEALTH_CHECK_TIMEOUT_MS ?? 5000);
const HEALTH_CHECK_INTERVAL_MS = Number(process.env.HEALTH_CHECK_INTERVAL_MS ?? 10000);
const READINESS_PATH = process.env.READINESS_PATH ?? "/health";

export { HEALTH_CHECK_INTERVAL_MS };

export type HealthCheckResult = {
  status: "healthy" | "unhealthy" | "crashed";
  error?: string;
};

/**
 * Check if a Docker container is running.
 * Returns true if running, false if stopped/exited/crashed.
 */
async function isContainerRunning(containerName: string): Promise<boolean> {
  try {
    const { stdout } = await execFileAsync("docker", [
      "inspect",
      "--format",
      "{{.State.Running}}",
      containerName
    ]);

    return stdout.trim() === "true";
  } catch (err) {
    // Container doesn't exist or can't be inspected
    return false;
  }
}

/**
 * Perform HTTP readiness probe for web services.
 * Checks if the application responds with a 2xx status code.
 * 
 * Uses hostPort when available (worker runs on host), otherwise tries container DNS.
 */
async function checkHttpReadiness(
  containerName: string,
  containerPort: number,
  hostPort?: number
): Promise<{ healthy: boolean; error?: string }> {
  try {
    // If hostPort is available, use localhost (worker runs on host machine)
    // Otherwise, try Docker network DNS (if worker were in a container)
    const url = hostPort 
      ? `http://localhost:${hostPort}${READINESS_PATH}`
      : `http://${containerName}:${containerPort}${READINESS_PATH}`;

    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), HEALTH_CHECK_TIMEOUT_MS);

    try {
      const response = await fetch(url, {
        method: "GET",
        signal: controller.signal,
        headers: {
          "User-Agent": "infra-pilot-health-check/1.0"
        }
      });

      clearTimeout(timeoutId);

      // Consider 2xx and 3xx as healthy (some apps redirect /health)
      if (response.status >= 200 && response.status < 400) {
        return { healthy: true };
      } else {
        return {
          healthy: false,
          error: `HTTP ${response.status}`
        };
      }
    } catch (fetchErr: any) {
      clearTimeout(timeoutId);
      if (fetchErr.name === "AbortError") {
        return {
          healthy: false,
          error: "timeout"
        };
      }
      throw fetchErr;
    }
  } catch (err: any) {
    return {
      healthy: false,
      error: err.message || "connection_failed"
    };
  }
}

/**
 * Perform health check on a deployment.
 * For web services: checks container status + HTTP readiness probe.
 * 
 * @param containerName - Docker container name (e.g., "cp-<deploymentId>")
 * @param containerPort - Port the application listens on inside the container
 * @param hostPort - Port mapped to host (optional, used for health checks from host)
 * @returns Health check result
 */
export async function checkDeploymentHealth(
  containerName: string,
  containerPort: number,
  hostPort?: number
): Promise<HealthCheckResult> {
  // First, check if container is running
  const isRunning = await isContainerRunning(containerName);

  if (!isRunning) {
    return {
      status: "crashed",
      error: "container_not_running"
    };
  }

  // For web services, perform HTTP readiness probe
  // (For now, assume all services are web services)
  const httpCheck = await checkHttpReadiness(containerName, containerPort, hostPort);

  if (httpCheck.healthy) {
    return {
      status: "healthy"
    };
  } else {
    return {
      status: "unhealthy",
      error: httpCheck.error
    };
  }
}

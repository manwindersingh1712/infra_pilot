import { prisma } from "@/packages/shared/src/db.js";
import { checkDeploymentHealth, HEALTH_CHECK_INTERVAL_MS } from "@/apps/worker/src/runtime/health-check.js";
import { upsertServiceRoute, removeServiceRoute } from "@/apps/worker/src/runtime/nginx.js";

/**
 * Health check monitor that periodically checks all deployed containers
 * and updates their health status and routing accordingly.
 * 
 * Core principles:
 * - Health is about the application inside the container, not just Docker status
 * - Ingress must be gated by health: starting/unhealthy/crashed → DO NOT route
 * - Only healthy deployments receive traffic via nginx
 */
export async function startHealthCheckMonitor() {
  console.log("Health check monitor started");

  const runHealthChecks = async () => {
    try {
      // Find all deployments that are deployed (container is running)
      const deployments = await prisma.deployment.findMany({
        where: {
          status: "deployed",
          containerId: { not: null },
          containerPort: { not: null }
        },
        select: {
          id: true,
          serviceId: true,
          containerId: true,
          containerPort: true,
          hostPort: true,
          healthStatus: true
        }
      });

      for (const dep of deployments) {
        if (!dep.containerId || !dep.containerPort) continue;

        const containerName = `cp-${dep.id}`;

        try {
          // Perform health check
          const healthResult = await checkDeploymentHealth(
            containerName,
            dep.containerPort,
            dep.hostPort ?? undefined
          );

          const newHealthStatus = healthResult.status;
          const oldHealthStatus = dep.healthStatus;

          // Determine if we should route traffic
          const shouldRoute = newHealthStatus === "healthy";
          const currentlyRouted = oldHealthStatus === "healthy";

          // Log health status changes
          if (oldHealthStatus !== newHealthStatus) {
            console.log(
              `[Health] Deployment ${dep.id} health changed: ${oldHealthStatus ?? "null"} → ${newHealthStatus}${healthResult.error ? ` (${healthResult.error})` : ""}`
            );
          }

          // Update health status in database
          await prisma.deployment.update({
            where: { id: dep.id },
            data: {   }
          });

          // Update routing based on health
          if (shouldRoute && !currentlyRouted) {
            // Health check passed - route traffic
            console.log(`[Health] Deployment ${dep.id} is healthy, routing traffic`);
            await upsertServiceRoute({
              serviceId: dep.serviceId,
              containerName,
              containerPort: dep.containerPort
            });
          } else if (!shouldRoute && currentlyRouted) {
            // Health check failed or container crashed - remove route
            console.log(
              `[Health] Deployment ${dep.id} is ${newHealthStatus}, removing route`
            );
            await removeServiceRoute(dep.serviceId);
          }
        } catch (err: any) {
          console.error(`[Health] Error checking deployment ${dep.id}:`, err.message);
          
          // Mark as unhealthy if health check itself fails
          if (dep.healthStatus !== "unhealthy") {
            await prisma.deployment.update({
              where: { id: dep.id },
              data: { healthStatus: "unhealthy" }
            });

            // Remove route if currently routed
            if (dep.healthStatus === "healthy") {
              await removeServiceRoute(dep.serviceId);
            }
          }
        }
      }
    } catch (err: any) {
      console.error("[Health] Error in health check monitor:", err.message);
    }
  };

  // Run immediately, then on interval
  await runHealthChecks();

  // Set up periodic health checks
  setInterval(runHealthChecks, HEALTH_CHECK_INTERVAL_MS);
}

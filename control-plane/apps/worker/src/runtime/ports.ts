import { prisma } from "@/packages/shared/src/db.js";
import { execFile } from "node:child_process";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

async function getDockerUsedPorts(): Promise<Set<number>> {
  try {
    const { stdout } = await execFileAsync("docker", [
      "ps",
      "--format",
      "{{.Ports}}"
    ]);

    const ports = new Set<number>();
    // Parse port mappings like "0.0.0.0:20000->27017/tcp"
    const portRegex = /:(\d+)->/g;
    let match;

    for (const line of stdout.split("\n")) {
      while ((match = portRegex.exec(line)) !== null) {
        ports.add(parseInt(match[1], 10));
      }
    }

    return ports;
  } catch {
    return new Set();
  }
}

export async function allocateHostPort(): Promise<number> {
  const start = Number(process.env.DATA_PLANE_PORT_RANGE_START ?? 20000);
  const end = Number(process.env.DATA_PLANE_PORT_RANGE_END ?? 20100);

  // get used ports from database
  const dbUsed = await prisma.deployment.findMany({
    where: { hostPort: { not: null } },
    select: { hostPort: true }
  });

  const usedSet = new Set(dbUsed.map((x) => x.hostPort!).filter(Boolean));

  // also check Docker's actual port usage
  const dockerPorts = await getDockerUsedPorts();
  dockerPorts.forEach((p) => usedSet.add(p));

  for (let p = start; p <= end; p++) {
    if (!usedSet.has(p)) return p;
  }
  throw new Error("no_free_ports_in_range");
}

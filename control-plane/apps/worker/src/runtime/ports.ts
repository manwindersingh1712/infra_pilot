import { prisma } from "@/packages/shared/src/db.js";

export async function allocateHostPort(): Promise<number> {
  const start = Number(process.env.DATA_PLANE_PORT_RANGE_START ?? 20000);
  const end = Number(process.env.DATA_PLANE_PORT_RANGE_END ?? 20100);

  // get used ports
  const used = await prisma.deployment.findMany({
    where: { hostPort: { not: null } },
    select: { hostPort: true }
  });

  const usedSet = new Set(used.map((x) => x.hostPort!).filter(Boolean));

  for (let p = start; p <= end; p++) {
    if (!usedSet.has(p)) return p;
  }
  throw new Error("no_free_ports_in_range");
}

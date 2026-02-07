import { execFile } from "node:child_process";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

async function run(cmd: string, args: string[], opts?: { cwd?: string }) {
  const { stdout, stderr } = await execFileAsync(cmd, args, {
    cwd: opts?.cwd,
    env: process.env,
    maxBuffer: 10 * 1024 * 1024
  });
  return { stdout, stderr };
}

export async function dockerPull(image: string) {
  await run("docker", ["pull", image]);
}

export async function detectContainerPort(image: string): Promise<number> {
  const { stdout } = await run("docker", [
    "image",
    "inspect",
    image,
    "--format",
    "{{json .Config.ExposedPorts}}"
  ]);

  const raw = stdout.trim();
  if (!raw || raw === "null") {
    throw new Error("no_exposed_ports_found");
  }

  const exposed = JSON.parse(raw) as Record<string, unknown>;
  const ports = Object.keys(exposed)
    .map((key) => Number(key.split("/")[0]))
    .filter((port) => Number.isFinite(port));

  if (ports.length === 0) {
    throw new Error("no_exposed_ports_found");
  }

  return ports.sort((a, b) => a - b)[0];
}

export async function dockerRun(params: {
  image: string;
  name: string;           // container name
  hostPort: number;
  containerPort: number;  // usually 3000/8080 etc
  env?: Record<string, string>;
}) {
  const envArgs: string[] = [];
  for (const [k, v] of Object.entries(params.env ?? {})) {
    envArgs.push("-e", `${k}=${v}`);
  }

  // If container already exists from previous attempt, remove it (idempotent-ish)
  await run("docker", ["rm", "-f", params.name]).catch(() => {});

  const { stdout } = await run("docker", [
    "run",
    "-d",
    "--name",
    params.name,
    "-p",
    `${params.hostPort}:${params.containerPort}`,
    ...envArgs,
    params.image
  ]);

  return { containerId: stdout.trim() };
}

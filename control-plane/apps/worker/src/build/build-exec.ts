import { execFile } from "node:child_process";
import { promisify } from "node:util";
import fs from "node:fs/promises";
import path from "node:path";
import { generateNodejsDockerfile, parsePackageJson } from "./dockerfile-gen.js";

// Helps to execute the binary without spawing a shell
const execFileAsync = promisify(execFile);

async function run(cmd: string, args: string[], opts?: { cwd?: string }) {
    // cmd: executable name (git, docker)
    // args: array of arguments (safe, structured)
    // cwd: where the command runs
    // env: inherits current environment
    // maxBuffer: allows big Docker build logs
  const { stdout, stderr } = await execFileAsync(cmd, args, {
    cwd: opts?.cwd,
    env: process.env,
    // We add maxBuffer to limit how much stdout/stderr (combined) the child process 
    // can return; the default is 1MB, which can be too small for verbose commands like 
    // Docker builds and cause the child process to fail with a buffer overflow. 
    // 10MB is a conservative bump to reliably capture larger logs without risking excessive memory use.
    
    // 10 * 1024 * 1024 bytes = 10MB; this is well below Node.js's maxBuffer hard limit, which historically was ~1GB+,
    // so this value is safe for the typical Node.js constraints on 64-bit systems.
    // See: https://github.com/nodejs/node/issues/26821 and child_process documentation.
    maxBuffer: 10 * 1024 * 1024
  });
  return { stdout, stderr };
}

export async function realBuildAndPush(params: {
  repoUrl: string;
  branch: string;
  commitSha: string;
  imageTag: string;     // e.g. localhost:5000/<serviceId>:<sha>
  workDir: string;      // e.g. /tmp/cp-builds/<deploymentId>
  serviceType?: "docker" | "nodejs";  // defaults to "docker"
}) {
  const serviceType = params.serviceType ?? "docker";
  console.log(`[build-exec] Starting build for ${serviceType}: ${params.imageTag}`);
  const repoDir = path.join(params.workDir, "repo");
  await fs.rm(params.workDir, { recursive: true, force: true });
  await fs.mkdir(params.workDir, { recursive: true });
  console.log(`[build-exec] Prepared workDir: ${params.workDir}`);

  // 1) Clone (shallow)
  console.log(`[build-exec] Cloning ${params.repoUrl}#${params.branch}`);
  await run("git", ["clone", "--depth", "1", "--branch", params.branch, params.repoUrl, repoDir]);
  console.log(`[build-exec] Clone complete`);

  // (Optional) If you want exact commit SHA checkout:
  // - For short SHAs or non-tip commits, you need fetch.
  // We'll do a safe fetch + checkout:
  await run("git", ["fetch", "--depth", "50", "origin", params.commitSha], { cwd: repoDir }).catch(() => {});
  await run("git", ["checkout", params.commitSha], { cwd: repoDir }).catch(() => {});

  // 2) For Node.js services, auto-generate Dockerfile if one doesn't exist
  if (serviceType === "nodejs") {
    const dockerfilePath = path.join(repoDir, "Dockerfile");
    let hasDockerfile = false;
    try {
      await fs.access(dockerfilePath);
      hasDockerfile = true;
    } catch {
      hasDockerfile = false;
    }

    if (!hasDockerfile) {
      console.log(`[build-exec] Generating Dockerfile for Node.js`);
      const packageJson = await parsePackageJson(repoDir);
      const dockerfileContent = await generateNodejsDockerfile(packageJson, repoDir);
      await fs.writeFile(dockerfilePath, dockerfileContent, "utf-8");
      console.log(`[build-exec] Dockerfile generated`);
    }
  }

  // 3) Docker build
  console.log(`[build-exec] Building Docker image: ${params.imageTag}`);
  await run("docker", ["build", "-t", params.imageTag, "."], { cwd: repoDir });
  console.log(`[build-exec] Docker build successful`);

  // 4) Push
  console.log(`[build-exec] Pushing image: ${params.imageTag}`);
  await run("docker", ["push", params.imageTag]);
  console.log(`[build-exec] Push successful`);

  // 5) Cleanup workspace (optional)
  await fs.rm(params.workDir, { recursive: true, force: true });

  return { image: params.imageTag };
}

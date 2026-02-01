import { execFile } from "node:child_process";
import { promisify } from "node:util";
import fs from "node:fs/promises";
import path from "node:path";

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
}) {
  const repoDir = path.join(params.workDir, "repo");
  await fs.rm(params.workDir, { recursive: true, force: true });
  await fs.mkdir(params.workDir, { recursive: true });

  // 1) Clone (shallow)
  await run("git", ["clone", "--depth", "1", "--branch", params.branch, params.repoUrl, repoDir]);

  // (Optional) If you want exact commit SHA checkout:
  // - For short SHAs or non-tip commits, you need fetch.
  // We'll do a safe fetch + checkout:
  await run("git", ["fetch", "--depth", "50", "origin", params.commitSha], { cwd: repoDir }).catch(() => {});
  await run("git", ["checkout", params.commitSha], { cwd: repoDir }).catch(() => {});

  // 2) Docker build
  await run("docker", ["build", "-t", params.imageTag, "."], { cwd: repoDir });

  // 3) Push
  await run("docker", ["push", params.imageTag]);

  // 4) Cleanup workspace (optional)
  await fs.rm(params.workDir, { recursive: true, force: true });

  return { image: params.imageTag };
}

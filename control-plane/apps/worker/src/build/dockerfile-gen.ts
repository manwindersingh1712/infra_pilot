/**
 * Generate Dockerfile content based on package.json contents
 * Detects package manager (npm/yarn/pnpm), start script, and node version
 */
export async function generateNodejsDockerfile(
  packageJson: { scripts?: Record<string, string>; engines?: { node?: string } },
  _workDir: string
): Promise<string> {
  const nodeVersion = packageJson.engines?.node ?? "20-alpine";
  const cleanVersion = nodeVersion.replace(/[^0-9a-zA-Z.-]/g, "");
  const baseImage = `node:${cleanVersion.includes("alpine") ? cleanVersion : cleanVersion + "-alpine"}`;

  // For now, default to npm. Could be extended to detect lock files
  const packageManager = "npm";

  // Detect start command
  const scripts = packageJson.scripts ?? {};
  const startCommand = scripts.start
    ? "npm start"
    : scripts["start:prod"]
      ? "npm run start:prod"
      : scripts.serve
        ? "npm run serve"
        : "node index.js";

  // Build command detection (for build step)
  const hasBuildScript = !!scripts.build;

  const dockerfileLines: string[] = [
    `FROM ${baseImage}`,
    "",
    "WORKDIR /app",
    "",
    "# Install dependencies first (better caching)",
    `COPY package*.json ./`,
    "",
  ];

  // Install command
  if (packageManager === "npm") {
    dockerfileLines.push("RUN npm ci --only=production");
  }

  dockerfileLines.push("");
  dockerfileLines.push("# Copy source code");
  dockerfileLines.push("COPY . .");

  if (hasBuildScript) {
    dockerfileLines.push("");
    dockerfileLines.push("# Build application");
    dockerfileLines.push("RUN npm run build");
  }

  dockerfileLines.push("");
  dockerfileLines.push("# Expose port");
  dockerfileLines.push("EXPOSE 3000");

  dockerfileLines.push("");
  dockerfileLines.push("# Start application");
  dockerfileLines.push(`CMD ["sh", "-c", "${startCommand}"]`);

  return dockerfileLines.join("\n");
}

/**
 * Parse package.json from a directory
 */
export async function parsePackageJson(workDir: string): Promise<{
  scripts?: Record<string, string>;
  engines?: { node?: string };
}> {
  const fs = await import("node:fs/promises");
  const path = await import("node:path");

  try {
    const content = await fs.readFile(path.join(workDir, "package.json"), "utf-8");
    return JSON.parse(content);
  } catch {
    return {};
  }
}

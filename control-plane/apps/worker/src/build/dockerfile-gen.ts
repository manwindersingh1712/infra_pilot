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
  let startCommand = scripts.start
    ? "npm start"
    : scripts["start:prod"]
      ? "npm run start:prod"
      : scripts.serve
        ? "npm run serve"
        : "node index.js";

  // Replace nodemon with node (nodemon is a devDependency, not installed in production)
  if (scripts.start?.includes("nodemon")) {
    startCommand = scripts.start.replace("nodemon", "node");
  }

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
 * Generate Dockerfile for Next.js applications
 * Next.js needs build step and runs as Node.js server
 */
export async function generateNextjsDockerfile(
  packageJson: { scripts?: Record<string, string>; engines?: { node?: string } },
  _workDir: string
): Promise<string> {
  const nodeVersion = packageJson.engines?.node ?? "20-alpine";
  const cleanVersion = nodeVersion.replace(/[^0-9a-zA-Z.-]/g, "");
  const baseImage = `node:${cleanVersion.includes("alpine") ? cleanVersion : cleanVersion + "-alpine"}`;

  const dockerfileLines: string[] = [
    `FROM ${baseImage}`,
    "",
    "WORKDIR /app",
    "",
    "# Install dependencies",
    "COPY package*.json ./",
    "RUN npm ci",
    "",
    "# Copy source code",
    "COPY . .",
    "",
    "# Build Next.js app",
    "RUN npm run build",
    "",
    "# Expose port (Next.js default is 3000)",
    "EXPOSE 3000",
    "",
    "# Start Next.js in production mode",
    'CMD ["npm", "start"]',
    ""
  ];

  return dockerfileLines.join("\n");
}

/**
 * Generate Dockerfile for React SPAs (CRA, Vite, etc.)
 * Builds static files and serves with nginx
 */
export async function generateReactDockerfile(
  packageJson: { scripts?: Record<string, string>; engines?: { node?: string } },
  workDir: string
): Promise<string> {
  const fs = await import("node:fs/promises");
  const path = await import("node:path");

  const nodeVersion = packageJson.engines?.node ?? "20-alpine";
  const cleanVersion = nodeVersion.replace(/[^0-9a-zA-Z.-]/g, "");
  const baseImage = `node:${cleanVersion.includes("alpine") ? cleanVersion : cleanVersion + "-alpine"}`;

  // Detect build output directory (dist for Vite, build for CRA)
  let buildOutputDir = "build";
  try {
    // Check if vite.config exists
    await fs.access(path.join(workDir, "vite.config.js"));
    buildOutputDir = "dist";
  } catch {
    // Default to CRA's build directory
    buildOutputDir = "build";
  }

  const dockerfileLines: string[] = [
    "# Build stage",
    `FROM ${baseImage} AS builder`,
    "",
    "WORKDIR /app",
    "",
    "# Install dependencies",
    "COPY package*.json ./",
    "RUN npm ci",
    "",
    "# Copy source and build",
    "COPY . .",
    "RUN npm run build",
    "",
    "# Production stage with nginx",
    "FROM nginx:alpine",
    "",
    `# Copy built files from builder stage`,
    `COPY --from=builder /app/${buildOutputDir} /usr/share/nginx/html`,
    "",
    "# Create nginx config for SPA routing",
    "RUN echo 'server { listen 80; location / { root /usr/share/nginx/html; try_files $uri $uri/ /index.html; } }' > /etc/nginx/conf.d/default.conf",
    "",
    "EXPOSE 80",
    "",
    'CMD ["nginx", "-g", "daemon off;"]',
    ""
  ];

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

import { writeFile } from "node:fs/promises";
import path from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

/**
 * Directory on the HOST where per-service nginx config files are stored.
 * Each file defines a full server block with subdomain-based routing.
 */
const NGINX_ROUTES_DIR =
  process.env.NGINX_ROUTES_DIR ??
  path.resolve(import.meta.dirname, "../../../../infra/nginx/conf.d/routes");

const NGINX_CONTAINER = process.env.NGINX_CONTAINER ?? "cp_nginx";

/**
 * Base domain for service subdomains.
 *   local  → "localhost"  →  <serviceId>.localhost
 *   prod   → "example.com" →  <serviceId>.example.com
 */
export const SERVICE_BASE_DOMAIN =
  process.env.SERVICE_BASE_DOMAIN ?? "localhost";

/**
 * Write (or overwrite) an nginx server block for a service so it is
 * reachable at  http://<serviceId>.<baseDomain>/
 *
 * The app runs at "/" on its own subdomain — zero rewriting needed.
 * Uses a variable so nginx resolves the container name via Docker DNS
 * at request time (not just at reload time).
 */
export async function upsertServiceRoute(params: {
  serviceId: string;
  containerName: string;
  containerPort: number;
}) {
  const { serviceId, containerName, containerPort } = params;

  const serverName = `${serviceId}.${SERVICE_BASE_DOMAIN}`;
  const varName = `upstream_${serviceId.replace(/[^a-zA-Z0-9]/g, "_")}`;

  const conf = [
    `# auto-generated – service ${serviceId}`,
    `server {`,
    `    listen 80;`,
    `    server_name ${serverName};`,
    ``,
    `    resolver 127.0.0.11 valid=10s;`,
    ``,
    `    location / {`,
    `        set $${varName} http://${containerName}:${containerPort};`,
    `        proxy_pass $${varName};`,
    ``,
    `        proxy_http_version 1.1;`,
    `        proxy_set_header Host $host;`,
    `        proxy_set_header X-Real-IP $remote_addr;`,
    `        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;`,
    `        proxy_set_header X-Forwarded-Proto $scheme;`,
    `        proxy_set_header Upgrade $http_upgrade;`,
    `        proxy_set_header Connection "upgrade";`,
    `    }`,
    `}`,
    ``
  ].join("\n");

  const confPath = path.join(NGINX_ROUTES_DIR, `svc-${serviceId}.conf`);
  await writeFile(confPath, conf, "utf-8");

  await reloadNginx();
}

/**
 * Ask the running nginx container to gracefully reload its configuration.
 */
async function reloadNginx() {
  await execFileAsync("docker", [
    "exec",
    NGINX_CONTAINER,
    "nginx",
    "-s",
    "reload"
  ]);
}

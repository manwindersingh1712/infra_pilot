import fs from "node:fs";
import path from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";

const execAsync = promisify(execFile);

const NGINX_CONF_DIR = path.resolve(process.cwd(), "infra/nginx/conf.d");
const NGINX_RELOAD_TIMEOUT_MS = 10_000;

export async function writeNginxRoute(params: {
  serviceId: string;
  hostPort: number;
}) {
  fs.mkdirSync(NGINX_CONF_DIR, { recursive: true });

  const conf = `
server {
  listen 80;

  location /s/${params.serviceId}/ {
    proxy_pass http://host.docker.internal:${params.hostPort}/;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $remote_addr;
  }
}
`.trim();

  const filePath = path.join(
    NGINX_CONF_DIR,
    `service-${params.serviceId}.conf`
  );

  console.log("Writing nginx route to:", filePath);

  fs.writeFileSync(filePath, conf);
}

export async function reloadNginx() {
  await execAsync(
    "docker",
    ["exec", "cp_nginx", "nginx", "-s", "reload"],
    { timeout: NGINX_RELOAD_TIMEOUT_MS }
  );
}

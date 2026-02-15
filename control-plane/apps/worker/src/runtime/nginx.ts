import { writeFile } from "node:fs/promises";
import path from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

/**
 * Directory on the HOST where per-service nginx route snippets are stored.
 * These are included by default.conf inside the main server block.
 */
const NGINX_ROUTES_DIR =
  process.env.NGINX_ROUTES_DIR ??
  path.resolve(import.meta.dirname, "../../../../infra/nginx/conf.d/routes");

const NGINX_CONTAINER = process.env.NGINX_CONTAINER ?? "cp_nginx";

/**
 * Write (or overwrite) an nginx location snippet for a service so it is
 * reachable at  /s/<serviceId>/
 *
 * The snippet proxies to the container by name on the shared Docker network.
 * Uses a variable so nginx resolves the name via Docker DNS at request time
 * (not just at reload time).
 */
export async function upsertServiceRoute(params: {
  serviceId: string;
  containerName: string;
  containerPort: number;
}) {
  const { serviceId, containerName, containerPort } = params;

  // Use an underscore-safe variable name (cuid IDs are alphanumeric)
  const varName = `upstream_${serviceId.replace(/[^a-zA-Z0-9]/g, "_")}`;

  const prefix = `/s/${serviceId}/`;

  // Compact client-side script that patches History API so SPA routers
  // (React Router, Vue Router, etc.) prepend the service base path on navigation.
  const patchScript = [
    `<script>(function(){`,
    `var B="${prefix.slice(0, -1)}";`,
    `var p=history.pushState,r=history.replaceState;`,
    `function f(u){return typeof u==="string"&&u[0]==="/"&&u.indexOf(B)!==0?B+u:u}`,
    `history.pushState=function(s,t,u){return p.call(this,s,t,f(u))};`,
    `history.replaceState=function(s,t,u){return r.call(this,s,t,f(u))};`,
    `})()</script>`
  ].join("");

  const snippet = [
    `# auto-generated – service ${serviceId}`,
    `location ${prefix} {`,
    `    set $${varName} http://${containerName}:${containerPort};`,
    ``,
    `    # Strip the /s/<id>/ prefix before forwarding to the container`,
    `    rewrite ^${prefix}(.*)$ /$1 break;`,
    `    proxy_pass $${varName};`,
    ``,
    `    # Disable upstream compression so sub_filter can inspect response bodies`,
    `    proxy_set_header Accept-Encoding "";`,
    `    sub_filter_once off;`,
    `    sub_filter_types text/css application/javascript;`,
    ``,
    `    # 1) Inject a History-API patch so SPA client-side routing keeps the prefix`,
    `    sub_filter '</head>' '${patchScript}\\n</head>';`,
    ``,
    `    # 2) Rewrite absolute asset paths in HTML so they route back through the proxy`,
    `    sub_filter 'href="/' 'href="${prefix}';`,
    `    sub_filter 'src="/' 'src="${prefix}';`,
    ``,
    `    # 3) Rewrite SPA route definitions in JS bundles (e.g. path:"/" → path:"/s/<id>/")`,
    `    sub_filter 'path:"/' 'path:"${prefix}';`,
    ``,
    `    # 4) Rewrite CSS url() and JS import paths`,
    `    sub_filter 'from "/' 'from "${prefix}';`,
    `    sub_filter 'url(/' 'url(${prefix}';`,
    ``,
    `    proxy_http_version 1.1;`,
    `    proxy_set_header Host $host;`,
    `    proxy_set_header X-Real-IP $remote_addr;`,
    `    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;`,
    `    proxy_set_header X-Forwarded-Proto $scheme;`,
    `    proxy_set_header Upgrade $http_upgrade;`,
    `    proxy_set_header Connection "upgrade";`,
    `}`,
    ``
  ].join("\n");

  const confPath = path.join(NGINX_ROUTES_DIR, `svc-${serviceId}.conf`);
  await writeFile(confPath, snippet, "utf-8");

  // Reload nginx so the new route takes effect
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

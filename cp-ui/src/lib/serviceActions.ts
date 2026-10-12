// Pure helpers for the service details panel: which deployment the Connection
// tab describes, the connection rows, and the delete dialog's state machine.
// No DOM, no import.meta, so node can run them (scripts/check-api-logic.ts).

import { ApiError, humanizeApiError } from "./errors.ts";

export interface DeploymentLike {
  status: string;
  stoppedAt: string | null;
  hostPort: number | null;
  createdAt: string;
}

// pickConnectionDeployment returns the newest deployment that is deployed and
// not stopped. A stopped container's host port and password are not live.
export function pickConnectionDeployment<T extends DeploymentLike>(deployments: T[]): T | null {
  let best: T | null = null;
  for (const d of deployments) {
    if (d.status !== "deployed" || d.stoppedAt) continue;
    if (best === null || Date.parse(d.createdAt) > Date.parse(best.createdAt)) best = d;
  }
  return best;
}

export interface ConnectionRow {
  label: string;
  value: string;
  field: string;
}

const MONGO_PORT = 27017;
const REDIS_PORT = 6379;

export function connectionHost(serviceId: string): string {
  return `svc-${serviceId}`;
}

// buildConnectionUrl: the password is percent-encoded so characters such as
// "@", ":" or "/" cannot change the URL's meaning.
export function buildConnectionUrl(serviceType: string, host: string, port: number, password: string): string {
  const pw = encodeURIComponent(password);
  return serviceType === "mongodb"
    ? `mongodb://admin:${pw}@${host}:${port}/?authSource=admin`
    : `redis://:${pw}@${host}:${port}`;
}

// buildConnectionRows lists what to show for a deployed managed database.
// passwordPending is true until the reveal request finished; password is ""
// when the service has none stored. The Local URL row exists only when the
// deployment published a host port.
export function buildConnectionRows(
  serviceType: string,
  serviceId: string,
  hostPort: number | null,
  password: string | null
): ConnectionRow[] {
  const isMongo = serviceType === "mongodb";
  const port = isMongo ? MONGO_PORT : REDIS_PORT;
  const host = connectionHost(serviceId);
  const pw = password ?? "";
  const rows: ConnectionRow[] = [
    { label: "Host", value: host, field: "host" },
    { label: "Port", value: String(port), field: "port" },
  ];
  if (isMongo) rows.push({ label: "User", value: "admin", field: "user" });
  rows.push({
    label: "Password",
    value: password === null ? "Loading…" : pw === "" ? "Not available" : pw,
    field: "password",
  });
  rows.push({ label: "URL", value: buildConnectionUrl(serviceType, host, port, pw), field: "url" });
  if (hostPort) {
    rows.push({ label: "Local URL", value: buildConnectionUrl(serviceType, "localhost", hostPort, pw), field: "local" });
  }
  return rows;
}

export function passwordEnvKey(serviceType: string): string {
  return serviceType === "mongodb" ? "MONGO_INITDB_ROOT_PASSWORD" : "REDIS_PASSWORD";
}

// ---- delete

export function isManagedDb(serviceType: string): boolean {
  return serviceType === "redis" || serviceType === "mongodb";
}

export type DeleteChoice = "purge" | "keep";

export type DeleteState =
  | { phase: "closed" }
  | { phase: "confirm"; error: string | null }
  | { phase: "pending"; purgeData: boolean };

export const DELETE_CLOSED: DeleteState = { phase: "closed" };

export function openDelete(): DeleteState {
  return { phase: "confirm", error: null };
}

// beginDelete moves confirm -> pending and says whether to send the request.
// Anywhere else (a second click while pending, a click on a closed dialog) it
// changes nothing and send is false, so one click path can never send twice.
// A managed database needs an explicit choice; other types ignore it.
export function beginDelete(
  state: DeleteState,
  serviceType: string,
  choice: DeleteChoice | null
): { state: DeleteState; send: boolean; purgeData: boolean } {
  if (state.phase !== "confirm") return { state, send: false, purgeData: false };
  let purgeData = false;
  if (isManagedDb(serviceType)) {
    if (choice === null) return { state, send: false, purgeData: false };
    purgeData = choice === "purge";
  }
  return { state: { phase: "pending", purgeData }, send: true, purgeData };
}

// cancelDelete closes the dialog unless a request is in flight.
export function cancelDelete(state: DeleteState): DeleteState {
  return state.phase === "pending" ? state : DELETE_CLOSED;
}

export function failDelete(state: DeleteState, message: string): DeleteState {
  return state.phase === "pending" ? { phase: "confirm", error: message } : state;
}

const DELETE_MESSAGES: Record<string, string> = {
  purge_choice_required: "Choose whether to delete the data or keep it, then try again.",
};

export type DeleteOutcome = { kind: "done" } | { kind: "error"; message: string };

// classifyDeleteError: a 404 means the service is already gone, which is what
// the user wanted. Everything else is shown inside the dialog.
export function classifyDeleteError(err: unknown): DeleteOutcome {
  if (err instanceof ApiError && err.status === 404) return { kind: "done" };
  return { kind: "error", message: humanizeApiError(err, DELETE_MESSAGES) };
}

// deletePath sends purgeData as a literal boolean query value.
export function deletePath(serviceId: string, purgeData: boolean): string {
  return `/services/${encodeURIComponent(serviceId)}?purgeData=${purgeData ? "true" : "false"}`;
}

export const DELETING_NOTICE =
  "Deleting… the service stops within a minute (a running build can take up to 3 minutes).";

// connectedServiceNames lists the other services joined to this one by a
// canvas connection, each once, in canvas order.
export function connectedServiceNames(
  serviceId: string,
  services: { id: string; name: string }[],
  connections: { sourceId: string; targetId: string }[]
): string[] {
  const linked = new Set<string>();
  for (const c of connections) {
    if (c.sourceId === serviceId) linked.add(c.targetId);
    else if (c.targetId === serviceId) linked.add(c.sourceId);
  }
  linked.delete(serviceId);
  return services.filter((s) => linked.has(s.id)).map((s) => s.name);
}

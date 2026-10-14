// Pure helpers for the service details panel: which deployment the Connection
// tab describes, the connection rows, and the delete dialog's state machine.
// No DOM, no import.meta, so node can run them (scripts/check-api-logic.ts).

import { ApiError, humanizeApiError } from "./errors.ts";

export interface DeploymentLike {
  serviceId?: string;
  status: string;
  stoppedAt: string | null;
  hostPort: number | null;
  createdAt: string;
}

// deploymentsOf keeps only the rows that belong to serviceId. The panel state
// can briefly hold the previous service's rows right after a switch; nothing
// (and above all no password request) may be driven by them.
export function deploymentsOf<T extends DeploymentLike>(serviceId: string, deployments: T[]): T[] {
  return deployments.filter((d) => d.serviceId === serviceId);
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

const IN_PROGRESS = new Set(["queued", "building", "deploying"]);

export type StatusTone = "success" | "failed" | "pending" | "neutral";

// deploymentBadge words and colours a deployment's status badge. A row that is
// "deployed" with stoppedAt set was superseded (its container was stopped), so
// it reads "Stopped" in neutral grey instead of a green "deployed".
export function deploymentBadge(status: string, stoppedAt?: string | null): { label: string; tone: StatusTone } {
  if (status === "deployed") return stoppedAt ? { label: "Stopped", tone: "neutral" } : { label: "deployed", tone: "success" };
  if (status === "failed") return { label: status, tone: "failed" };
  if (IN_PROGRESS.has(status)) return { label: status, tone: "pending" };
  return { label: status, tone: "neutral" };
}

export type ConnectionPanelState<T> =
  | { kind: "live"; deployment: T }
  | { kind: "deploying" }
  | { kind: "none" };


// connectionPanelState: a live deployment wins; otherwise "deploying" while a
// deployment is queued, building or deploying; "none" (Not running) only when
// nothing is on its way.
export function connectionPanelState<T extends DeploymentLike>(deployments: T[]): ConnectionPanelState<T> {
  const live = pickConnectionDeployment(deployments);
  if (live) return { kind: "live", deployment: live };
  if (deployments.some((d) => IN_PROGRESS.has(d.status) && !d.stoppedAt)) return { kind: "deploying" };
  return { kind: "none" };
}

export type PasswordState =
  | { kind: "loading" }
  | { kind: "ready"; password: string } // "" = the service has no stored password
  | { kind: "failed"; message: string };

export interface ConnectionRow {
  label: string;
  // What is drawn: masked while the secret is hidden.
  value: string;
  // What Copy puts on the clipboard (the real value), or null when there is nothing to copy.
  copyValue: string | null;
  field: string;
  // Rows that contain the password, so the tab can mask them together.
  secret: boolean;
}

const MONGO_PORT = 27017;
const REDIS_PORT = 6379;
export const MASK = "••••••••••";
export const NO_PASSWORD_TEXT = "Redeploy to generate a password";

// safeEncodeURIComponent: encodeURIComponent throws URIError on a lone
// surrogate; replace those with U+FFFD first so a odd value cannot blank the tab.
export function safeEncodeURIComponent(v: string): string {
  return encodeURIComponent(v.replace(/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/g, "\uFFFD"));
}

export function connectionHost(serviceId: string): string {
  return `svc-${serviceId}`;
}

// buildConnectionUrl: the password is percent-encoded so characters such as
// "@", ":" or "/" cannot change the URL's meaning. With mask the password is
// drawn as dots instead.
export function buildConnectionUrl(serviceType: string, host: string, port: number, password: string, mask = false): string {
  const pw = mask ? MASK : safeEncodeURIComponent(password);
  return serviceType === "mongodb"
    ? `mongodb://admin:${pw}@${host}:${port}/?authSource=admin`
    : `redis://default:${pw}@${host}:${port}`;
}

// shellQuote wraps a value in single quotes for a POSIX shell. encodeURIComponent
// leaves ' alone, so a password containing one must still not end the quote.
export function shellQuote(v: string): string {
  return `'${v.replace(/'/g, `'\\''`)}'`;
}

// buildConnectionRows lists what to show for a live managed database. Until
// the password is ready, or when the service has none, no URL row exists, so
// there is never a copyable URL that cannot authenticate. Secret rows are
// masked unless revealed; Copy always carries the real value.
export function buildConnectionRows(
  serviceType: string,
  serviceId: string,
  hostPort: number | null,
  password: PasswordState,
  revealed: boolean
): ConnectionRow[] {
  const isMongo = serviceType === "mongodb";
  const port = isMongo ? MONGO_PORT : REDIS_PORT;
  const host = connectionHost(serviceId);
  const plain = (label: string, value: string, field: string): ConnectionRow => ({ label, value, copyValue: value, field, secret: false });
  const rows: ConnectionRow[] = [plain("Host", host, "host"), plain("Port", String(port), "port")];
  if (isMongo) rows.push(plain("User", "admin", "user"));
  if (password.kind === "loading") {
    rows.push({ label: "Password", value: "Loading…", copyValue: null, field: "password", secret: false });
    return rows;
  }
  if (password.kind === "failed") {
    rows.push({ label: "Password", value: "Could not load", copyValue: null, field: "password", secret: false });
    return rows;
  }
  const pw = password.password;
  if (pw === "") {
    rows.push({ label: "Password", value: NO_PASSWORD_TEXT, copyValue: null, field: "password", secret: false });
    return rows;
  }
  const secret = (label: string, real: string, masked: string, field: string): ConnectionRow => ({
    label,
    value: revealed ? real : masked,
    copyValue: real,
    field,
    secret: true,
  });
  rows.push(secret("Password", pw, MASK, "password"));
  const url = buildConnectionUrl(serviceType, host, port, pw);
  rows.push(secret("URL", url, buildConnectionUrl(serviceType, host, port, pw, true), "url"));
  let cliUrl = url;
  let cliMasked = buildConnectionUrl(serviceType, host, port, pw, true);
  if (hostPort) {
    const local = buildConnectionUrl(serviceType, "localhost", hostPort, pw);
    const localMasked = buildConnectionUrl(serviceType, "localhost", hostPort, pw, true);
    rows.push(secret("Local URL", local, localMasked, "local"));
    cliUrl = local;
    cliMasked = localMasked;
  }
  const tool = isMongo ? "mongosh" : "redis-cli -u";
  rows.push(secret("CLI", `${tool} ${shellQuote(cliUrl)}`, `${tool} ${shellQuote(cliMasked)}`, "cli"));
  return rows;
}

export function passwordEnvKey(serviceType: string): string {
  return serviceType === "mongodb" ? "MONGO_INITDB_ROOT_PASSWORD" : "REDIS_PASSWORD";
}

// revealKeysPath is the env request that reveals only the named keys; every
// other value comes back masked. The Connection tab asks for its password key
// alone, so no other secret of the service is decrypted for it.
export function revealKeysPath(serviceId: string, keys: string[]): string {
  return `/services/${encodeURIComponent(serviceId)}/env?reveal=true&keys=${keys.map(encodeURIComponent).join(",")}`;
}

export type CopyResult = { field: string; ok: boolean } | null;

// copyButtonLabel: "Copied!" only after the clipboard write succeeded.
export function copyButtonLabel(result: CopyResult, field: string): string {
  if (!result || result.field !== field) return "Copy";
  return result.ok ? "Copied!" : "Copy failed";
}

// ---- delete

export function isManagedDb(serviceType: string): boolean {
  return serviceType === "redis" || serviceType === "mongodb";
}

export type DeleteChoice = "purge" | "keep";

export type DeleteState =
  | { phase: "closed" }
  | { phase: "confirm"; error: string | null; armed: boolean; armedAt: number }
  | { phase: "pending"; purgeData: boolean };

export const DELETE_CLOSED: DeleteState = { phase: "closed" };

export function openDelete(): DeleteState {
  return { phase: "confirm", error: null, armed: false, armedAt: 0 };
}

// beginDelete moves confirm -> pending and says whether to send the request.
// Anywhere else (a second click while pending, a click on a closed dialog) it
// changes nothing and send is false, so one click path can never send twice.
// A managed database needs an explicit choice; other types ignore it.
// ARM_GUARD_MS: after the purge button is armed, activation is ignored for this
// long, so a double-click or a held Enter cannot arm and erase in one go.
export const ARM_GUARD_MS = 700;

export interface ActivationInput {
  now: number; // ms, any monotonic clock (performance.now())
  repeat?: boolean; // keyboard auto-repeat
  detail?: number; // click count of a mouse click (2 = the second click of a double-click)
}

// isDeliberateConfirm: the second purge action counts only if it is not a
// key repeat, not part of a multi-click, and comes after the guard time.
export function isDeliberateConfirm(armedAt: number, input: ActivationInput): boolean {
  if (input.repeat) return false;
  if ((input.detail ?? 0) > 1) return false;
  return input.now - armedAt >= ARM_GUARD_MS;
}

export function beginDelete(
  state: DeleteState,
  serviceType: string,
  choice: DeleteChoice | null,
  input: ActivationInput = { now: 0 }
): { state: DeleteState; send: boolean; purgeData: boolean } {
  if (state.phase !== "confirm") return { state, send: false, purgeData: false };
  let purgeData = false;
  if (isManagedDb(serviceType)) {
    if (choice === null) return { state, send: false, purgeData: false };
    // Erasing data takes two clicks: the first only arms the button.
    if (choice === "purge" && !state.armed) {
      return { state: { ...state, armed: true, armedAt: input.now }, send: false, purgeData: false };
    }
    if (choice === "purge" && !isDeliberateConfirm(state.armedAt, input)) return { state, send: false, purgeData: false };
    purgeData = choice === "purge";
  }
  return { state: { phase: "pending", purgeData }, send: true, purgeData };
}

// cancelDelete closes the dialog unless a request is in flight.
export function cancelDelete(state: DeleteState): DeleteState {
  return state.phase === "pending" ? state : DELETE_CLOSED;
}

export function failDelete(state: DeleteState, message: string): DeleteState {
  return state.phase === "pending" ? { phase: "confirm", error: message, armed: false, armedAt: 0 } : state;
}

// disarmPurge reverts an armed purge button (after 5 s, or on any other choice).
export function disarmPurge(state: DeleteState): DeleteState {
  return state.phase === "confirm" && state.armed ? { ...state, armed: false } : state;
}

export const PURGE_ARM_MS = 5000;
export const PURGE_LABEL = "Delete database and its data";
export const PURGE_ARMED_LABEL = "Click again to erase the data permanently";

export function purgeButtonLabel(state: DeleteState): string {
  return state.phase === "confirm" && state.armed ? PURGE_ARMED_LABEL : PURGE_LABEL;
}

// nextFocusIndex implements a Tab focus trap over `count` focusable elements:
// it wraps at both ends, and from outside the list (-1) it enters at the
// first element (Tab) or the last (Shift+Tab).
export function nextFocusIndex(current: number, count: number, shift: boolean): number {
  if (count <= 0) return -1;
  if (current < 0 || current >= count) return shift ? count - 1 : 0;
  if (shift) return current === 0 ? count - 1 : current - 1;
  return current === count - 1 ? 0 : current + 1;
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
  return `/services/${safeEncodeURIComponent(serviceId)}?purgeData=${purgeData ? "true" : "false"}`;
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

// shouldClosePanel: after a confirmed delete the panel closes only if it still
// shows the deleted service; another service's panel must stay open.
export function shouldClosePanel(shownServiceId: string | undefined, deletedServiceId: string): boolean {
  return shownServiceId === deletedServiceId;
}

// deleteFailedNotice is the toast text for a delete that failed after its
// dialog was already gone.
export function deleteFailedNotice(name: string, message: string): string {
  return `Could not delete “${name}”. ${/[.!?]$/.test(message) ? message : message + "."}`;
}

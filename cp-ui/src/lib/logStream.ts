// Pure helpers for the live-log hook: no DOM, no React, no import.meta, so
// node can run them directly (see scripts/check-api-logic.ts).
import { ApiError, humanizeApiError } from "./errors.ts";

export interface LogEntry {
  deploymentId: string;
  source: "stdout" | "stderr" | "system";
  message: string;
  timestamp: string;
}

export interface LogMeta {
  source: "redis" | "clickhouse";
  totalCount?: number;
}

export const MAX_BACKOFF_MS = 30_000;
const BASE_BACKOFF_MS = 1_000;

// backoffDelay is capped exponential backoff with jitter: the ceiling doubles
// per failed attempt (1 s, 2 s, 4 s ... 30 s) and the delay is drawn from the
// upper half of it, so a fleet of tabs does not reconnect in lockstep. `attempt`
// is 0 for the first retry; `rand` is a number in [0, 1).
export function backoffDelay(attempt: number, rand: number): number {
  const n = Math.max(0, Math.floor(attempt));
  const ceiling = Math.min(MAX_BACKOFF_MS, BASE_BACKOFF_MS * 2 ** n);
  const r = Math.min(Math.max(rand, 0), 1);
  return Math.round(ceiling / 2 + (ceiling / 2) * r);
}

// Delay before reconnecting after an `end` event. max_duration is the
// server's routine 1 h cap and is reconnected at once; anything else
// (shutdown, an unknown reason) waits 1-3 s.
export const SHUTDOWN_DELAY_MIN_MS = 1_000;
export const SHUTDOWN_DELAY_SPAN_MS = 2_000;

export type EndReason = "max_duration" | "shutdown";

export function endDelay(reason: EndReason, rand: number): number {
  if (reason === "max_duration") return 0;
  return Math.round(SHUTDOWN_DELAY_MIN_MS + SHUTDOWN_DELAY_SPAN_MS * Math.min(Math.max(rand, 0), 1));
}

// ---- parsing of event payloads (server data is never trusted to be well formed)

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

const SOURCES = new Set(["stdout", "stderr", "system"]);

export function isLogEntry(v: unknown): v is LogEntry {
  return (
    isRecord(v) &&
    typeof v.message === "string" &&
    typeof v.timestamp === "string" &&
    typeof v.source === "string" &&
    SOURCES.has(v.source)
  );
}

function parseJSON(data: unknown): unknown {
  if (typeof data !== "string") return undefined;
  try {
    return JSON.parse(data);
  } catch {
    return undefined;
  }
}

export function parseLogEvent(data: unknown): LogEntry | null {
  const v = parseJSON(data);
  return isLogEntry(v) ? v : null;
}

export function parseHistoryEvent(data: unknown): { logs: LogEntry[]; meta: LogMeta | null } | null {
  const v = parseJSON(data);
  if (!isRecord(v) || !Array.isArray(v.logs)) return null;
  let meta: LogMeta | null = null;
  if (isRecord(v.meta) && (v.meta.source === "redis" || v.meta.source === "clickhouse")) {
    meta = { source: v.meta.source };
    if (typeof v.meta.totalCount === "number") meta.totalCount = v.meta.totalCount;
  }
  return { logs: v.logs.filter(isLogEntry), meta };
}

// parseEndEvent never returns null for a well-formed event; an unknown or
// missing reason is treated like "shutdown" (reconnect after a short delay).
export function parseEndEvent(data: unknown): EndReason {
  const v = parseJSON(data);
  return isRecord(v) && v.reason === "max_duration" ? "max_duration" : "shutdown";
}

// ---- the line buffer: capped, ordered, O(1) dedupe

// The most lines the panel keeps. The stream never gives up and renews itself
// every hour, so without a cap a chatty service grows the list forever.
export const MAX_LINES = 5_000;

export function entryKey(e: LogEntry): string {
  return `${e.timestamp}\u0000${e.message}`;
}

// LogBuffer is mutable and lives in a ref: `entries` is replaced by a new array
// on every change (so React sees a new value) and `keys` holds the entryKey of
// exactly the entries in it, kept in step with the cap.
export interface LogBuffer {
  max: number;
  entries: LogEntry[];
  keys: Set<string>;
}

export function newBuffer(max = MAX_LINES): LogBuffer {
  return { max, entries: [], keys: new Set() };
}

export function bufferReset(b: LogBuffer, entries: LogEntry[] = []): void {
  b.keys = new Set();
  const kept: LogEntry[] = [];
  for (const e of entries) {
    const k = entryKey(e);
    if (b.keys.has(k)) continue;
    b.keys.add(k);
    kept.push(e);
  }
  b.entries = kept;
  trim(b);
}

function trim(b: LogBuffer): void {
  const extra = b.entries.length - b.max;
  if (extra <= 0) return;
  for (const e of b.entries.slice(0, extra)) b.keys.delete(entryKey(e));
  b.entries = b.entries.slice(extra);
}

// bufferAppend adds one live line unless the same timestamp+message is already
// held (a line can arrive both in a history event and live). The oldest line
// is dropped past the cap. Returns whether the buffer changed.
export function bufferAppend(b: LogBuffer, e: LogEntry): boolean {
  const k = entryKey(e);
  if (b.keys.has(k)) return false;
  b.keys.add(k);
  b.entries = [...b.entries, e];
  trim(b);
  return true;
}

// bufferMerge folds the lines of a later `history` event into the buffer:
// duplicates are dropped, lines the user cleared (timestamp <= clearedUpToMs)
// stay gone, the result is in time order (stable, so equal timestamps keep
// their arrival order) and capped to the newest `max`. Returns whether the
// buffer changed.
export function bufferMerge(b: LogBuffer, incoming: LogEntry[], clearedUpToMs = -Infinity): boolean {
  const fresh: LogEntry[] = [];
  const seen = new Set<string>();
  for (const e of incoming) {
    const k = entryKey(e);
    if (b.keys.has(k) || seen.has(k)) continue;
    const ms = Date.parse(e.timestamp);
    if (!Number.isNaN(ms) && ms <= clearedUpToMs) continue;
    seen.add(k);
    fresh.push(e);
  }
  if (fresh.length === 0) return false;
  const merged = [...b.entries, ...fresh];
  for (const k of seen) b.keys.add(k);
  // Only sort when every timestamp parses: a NaN comparison would make the
  // order undefined.
  const times = merged.map((e) => Date.parse(e.timestamp));
  b.entries = times.some(Number.isNaN)
    ? merged
    : merged
        .map((e, i) => ({ e, t: times[i] }))
        .sort((x, y) => x.t - y.t) // Array.prototype.sort is stable
        .map((x) => x.e);
  trim(b);
  return true;
}

// latestMs is the newest parseable timestamp in the list (or `floor`).
export function latestMs(entries: LogEntry[], floor = -Infinity): number {
  let m = floor;
  for (const e of entries) {
    const t = Date.parse(e.timestamp);
    if (!Number.isNaN(t) && t > m) m = t;
  }
  return m;
}

// ---- what to do after the server ends a stream

// A stream must have lived this long, and the last silent reconnect must be at
// least this old, for a max_duration end to reconnect at once and silently.
export const STABLE_MS = 10_000;

// A hostile or broken server that ends every stream just after STABLE_MS would
// keep the silent path alive forever. At most MAX_SILENT_IN_WINDOW silent
// reconnects are allowed per SILENT_WINDOW_MS; after that the normal backoff
// applies.
export const SILENT_WINDOW_MS = 5 * 60_000;
export const MAX_SILENT_IN_WINDOW = 3;

// recentSilent keeps the silent-reconnect times that are still inside the window.
export function recentSilent(times: number[], now: number): number[] {
  return times.filter((t) => now - t < SILENT_WINDOW_MS && t <= now);
}

export interface EndPlan {
  delayMs: number;
  // Reconnect without touching the badge.
  silent: boolean;
  // The wait counts as a failed attempt (backoff grows).
  countAttempt: boolean;
}

// planAfterEnd decides the reconnect after an `end` event.
//   max_duration, stream lived >= 10 s and no silent reconnect in the last 10 s:
//       immediately, silently, not counted (the routine 1 h cap)
//   max_duration after 3 silent reconnects in the last 5 minutes: backoff
//   max_duration otherwise (a server or proxy ending streams early): backoff
//   shutdown (or unknown): 1-3 s, never less than the backoff, counted
export function planAfterEnd(input: {
  reason: EndReason;
  msSinceOpen: number;
  msSinceLastSilent: number;
  attempts: number;
  rand: number;
  // Silent reconnects already made in the last SILENT_WINDOW_MS.
  silentInWindow?: number;
}): EndPlan {
  const { reason, msSinceOpen, msSinceLastSilent, attempts, rand, silentInWindow = 0 } = input;
  if (
    reason === "max_duration" &&
    msSinceOpen >= STABLE_MS &&
    msSinceLastSilent >= STABLE_MS &&
    silentInWindow < MAX_SILENT_IN_WINDOW
  ) {
    return { delayMs: 0, silent: true, countAttempt: false };
  }
  const backoff = backoffDelay(attempts, rand);
  if (reason === "max_duration") return { delayMs: backoff, silent: false, countAttempt: true };
  return { delayMs: Math.max(endDelay("shutdown", rand), backoff), silent: false, countAttempt: true };
}

// attemptsAfterClose forgets earlier failures only when the stream that just
// closed had been open long enough to count as healthy; a server that accepts
// and immediately ends streams keeps growing the backoff.
export function attemptsAfterClose(attempts: number, msOpen: number): number {
  return msOpen >= STABLE_MS ? 0 : attempts;
}

// ---- classification of a failed ticket request

export type TicketOutcome =
  // Stop retrying; the badge reads Disconnected. canRetry: whether the Retry
  // button can help (not when the deployment is gone).
  | { kind: "terminal"; message: string; canRetry: boolean }
  // Keep going (badge: Reconnecting). The next attempt waits at least minDelayMs
  // on top of the normal backoff.
  | { kind: "retry"; message: string; minDelayMs: number };

export const DEPLOYMENT_GONE_MESSAGE = "This deployment no longer exists.";

// The panel retries by itself, so its messages say so instead of "try again".
const PANEL_MESSAGES: Record<string, string> = {
  too_many_requests: "Too many requests. This panel is retrying shortly.",
  network_error: "Cannot reach the server. This panel keeps retrying.",
  unavailable: "The service is temporarily unavailable. This panel keeps retrying.",
  internal_error: "Something went wrong on our side. This panel keeps retrying.",
  refresh_unavailable: "Could not refresh your session right now. This panel keeps retrying.",
  invalid_ticket: "The log stream link expired. This panel is getting a new one.",
};

const GENERIC_PANEL_MESSAGE = "The log stream could not be reached. This panel keeps retrying.";

// panelMessage words a ticket failure for this panel (no "try again").
function panelMessage(err: unknown): string {
  if (!(err instanceof ApiError)) return err instanceof TypeError ? PANEL_MESSAGES.network_error : GENERIC_PANEL_MESSAGE;
  if (Object.hasOwn(PANEL_MESSAGES, err.code)) return PANEL_MESSAGES[err.code];
  if (err.code === "too_many_streams") return humanizeApiError(err);
  if (err.status === 0) return PANEL_MESSAGES.network_error;
  if (err.status === 429) return PANEL_MESSAGES.too_many_requests;
  if (err.status === 503) return PANEL_MESSAGES.unavailable;
  if (err.status >= 500) return PANEL_MESSAGES.internal_error;
  return GENERIC_PANEL_MESSAGE;
}

export const DEFAULT_RATE_LIMIT_WAIT_S = 30;

// classifyTicketError decides what a failed POST .../stream-ticket means.
//   404            the deployment (or its service) is gone: terminal, no Retry
//   session_expired the shared client already signed the user out: terminal
//   429            honour Retry-After (clamped to 1-60 s; 30 s when it is missing)
//   401            invalid_ticket or anything else: fetch a new ticket
//   0, 5xx, other  server or network trouble: keep reconnecting
export function classifyTicketError(err: unknown): TicketOutcome {
  const message = panelMessage(err);
  if (err instanceof ApiError) {
    if (err.status === 404) return { kind: "terminal", message: DEPLOYMENT_GONE_MESSAGE, canRetry: false };
    if (err.code === "session_expired") return { kind: "terminal", message: humanizeApiError(err), canRetry: true };
    if (err.status === 429) {
      const secs = err.retryAfter ?? DEFAULT_RATE_LIMIT_WAIT_S;
      return { kind: "retry", message, minDelayMs: Math.min(60, Math.max(1, secs)) * 1000 };
    }
  }
  return { kind: "retry", message, minDelayMs: 0 };
}

export const BAD_TICKET_MESSAGE = "The server sent an unexpected answer. This panel keeps retrying.";
export const OPEN_FAILED_MESSAGE = "Could not open the log stream. This panel keeps retrying.";

// parseTicketResponse accepts only {"ticket": "<non-empty string>"}.
export function parseTicketResponse(v: unknown): string | null {
  if (isRecord(v) && typeof v.ticket === "string" && v.ticket !== "") return v.ticket;
  return null;
}

// ---- the stream-cap hint

// After this many CONSECUTIVE opens that were refused after a ticket was
// granted, one of the likeliest causes is the per-user cap of open streams
// (EventSource reports no status), so the hook shows the hint.
export const FAILED_OPENS_BEFORE_HINT = 3;

export type OpenEvent = "refused" | "ticket_failed" | "opened";

// nextFailedOpens counts consecutive refused opens: any ticket failure or
// successful open breaks the run.
export function nextFailedOpens(count: number, ev: OpenEvent): number {
  return ev === "refused" ? count + 1 : 0;
}

export function showCapHint(count: number): boolean {
  return count >= FAILED_OPENS_BEFORE_HINT;
}

export const TOO_MANY_STREAMS_MESSAGE = humanizeApiError(new ApiError(429, "too_many_streams"));

// ---- tab visibility and restarts

export type VisibleAction = "none" | "restart" | "replace";

// A tab away for longer than this may hold a dead connection (sleep, network
// change): reopen the stream, but only replace the old one once a new ticket
// is in hand.
export const HIDDEN_STALE_MS = 30_000;

// initialMode: a tab opened in the background does not open a stream until it
// becomes visible.
export function initialMode(visibilityState: string): "connect" | "paused" {
  return visibilityState === "hidden" ? "paused" : "connect";
}

// visibleAction decides what to do when the tab becomes visible.
//   terminal   nothing (Retry is the way out)
//   paused or no open stream   restart (open one)
//   open stream, away > 30 s   replace (new ticket first, old stream kept
//                              until the new one is ready)
export function visibleAction(input: { terminal: boolean; paused: boolean; connected: boolean; msAway: number }): VisibleAction {
  if (input.terminal) return "none";
  if (input.paused || !input.connected) return "restart";
  return input.msAway > HIDDEN_STALE_MS ? "replace" : "none";
}

// Restarts the user or the browser trigger (Retry, online, visible) are spaced
// at least this far apart.
export const MIN_RESTART_GAP_MS = 1_000;

// restartDelay is how long to wait before a restart requested at `now`.
export function restartDelay(now: number, lastRestartAt: number): number {
  return Math.max(0, lastRestartAt + MIN_RESTART_GAP_MS - now);
}

// ---- empty-state text

export function emptyStateText(status: "live" | "reconnecting" | "disconnected" | "paused", cleared: boolean): string {
  if (status === "paused") return "Paused while this tab is in the background.";
  if (status === "reconnecting") return "Waiting for the log stream…";
  if (status === "disconnected") return "No logs to show.";
  return cleared ? "Logs cleared. New lines will appear here." : "No logs yet. Deploy a service to see logs here.";
}

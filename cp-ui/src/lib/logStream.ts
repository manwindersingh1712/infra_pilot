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

// ---- history merge

export function entryKey(e: LogEntry): string {
  return `${e.timestamp}\u0000${e.message}`;
}

// appendLive adds one live line unless the same timestamp+message is already
// in the list (a line can arrive both in a history event and live).
export function appendLive(prev: LogEntry[], e: LogEntry): LogEntry[] {
  return prev.some((l) => l.timestamp === e.timestamp && l.message === e.message) ? prev : [...prev, e];
}

// mergeHistory folds the lines of a later `history` event into what the user
// already has: duplicates (same timestamp and message) are dropped, lines the
// user cleared (timestamp <= clearedUpToMs) stay gone, and the result is in
// time order (stable, so equal timestamps keep their arrival order). When
// nothing new is added the same array is returned, so React does not re-render.
export function mergeHistory(prev: LogEntry[], incoming: LogEntry[], clearedUpToMs = -Infinity): LogEntry[] {
  const seen = new Set(prev.map(entryKey));
  const fresh: LogEntry[] = [];
  for (const e of incoming) {
    const k = entryKey(e);
    if (seen.has(k)) continue;
    const ms = Date.parse(e.timestamp);
    if (!Number.isNaN(ms) && ms <= clearedUpToMs) continue;
    seen.add(k);
    fresh.push(e);
  }
  if (fresh.length === 0) return prev;
  const merged = [...prev, ...fresh];
  // Only sort when every timestamp parses: a NaN comparison would make the
  // order undefined.
  const times = merged.map((e) => Date.parse(e.timestamp));
  if (times.some(Number.isNaN)) return merged;
  return merged
    .map((e, i) => ({ e, t: times[i] }))
    .sort((a, b) => a.t - b.t) // Array.prototype.sort is stable
    .map((x) => x.e);
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

// ---- classification of a failed ticket request

export type TicketOutcome =
  // Stop retrying; the badge reads Disconnected.
  | { kind: "terminal"; message: string }
  // Keep going (badge: Reconnecting). The next attempt waits at least minDelayMs
  // on top of the normal backoff.
  | { kind: "retry"; message: string; minDelayMs: number };

export const DEPLOYMENT_GONE_MESSAGE = "This deployment no longer exists.";

// classifyTicketError decides what a failed POST .../stream-ticket means.
//   404            the deployment (or its service) is gone: terminal
//   session_expired the shared client already signed the user out: terminal
//   429            honour Retry-After (clamped to 1-60 s)
//   401            invalid_ticket or anything else: fetch a new ticket
//   0, 5xx, other  server or network trouble: keep reconnecting
export function classifyTicketError(err: unknown): TicketOutcome {
  const message = humanizeApiError(err);
  if (err instanceof ApiError) {
    if (err.status === 404) return { kind: "terminal", message: DEPLOYMENT_GONE_MESSAGE };
    if (err.code === "session_expired") return { kind: "terminal", message };
    if (err.status === 429) {
      const secs = err.retryAfter ?? 5;
      return { kind: "retry", message, minDelayMs: Math.min(60, Math.max(1, secs)) * 1000 };
    }
  }
  return { kind: "retry", message, minDelayMs: 0 };
}

// After this many opens in a row that fail before the stream ever opened
// (with a ticket granted each time), the likeliest cause is the per-user cap
// of open streams: EventSource reports no status, so the hook shows the hint.
export const FAILED_OPENS_BEFORE_HINT = 3;

export const TOO_MANY_STREAMS_MESSAGE = humanizeApiError(new ApiError(429, "too_many_streams"));

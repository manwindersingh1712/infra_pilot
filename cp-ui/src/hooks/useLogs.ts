import { useState, useEffect, useRef, useCallback } from "react";
import { API_BASE, apiFetch, getToken } from "../lib/api";
import {
  BAD_TICKET_MESSAGE, OPEN_FAILED_MESSAGE, TOO_MANY_STREAMS_MESSAGE, attemptsAfterClose, backoffDelay,
  bufferAppend, bufferMerge, bufferReset, classifyTicketError, initialMode, latestMs, newBuffer, nextFailedOpens,
  parseEndEvent, parseHistoryEvent, parseLogEvent, parseTicketResponse, planAfterEnd, recentSilent, restartDelay, showCapHint,
  visibleAction, type LogEntry, type LogMeta,
} from "../lib/logStream";

export type { LogEntry } from "../lib/logStream";

// live: the stream is open. reconnecting: trying again (it never gives up).
// paused: the tab is in the background and holds no stream. disconnected:
// terminal, the deployment no longer exists or the session ended.
export type LogStatus = "live" | "reconnecting" | "paused" | "disconnected";

interface UseLogsOptions {
  deploymentId: string;
}

// A tab hidden this long closes its stream (it reopens when the tab is shown).
const HIDDEN_CLOSE_MS = 5 * 60_000;

// Streams a deployment's logs over Server-Sent Events. Every connection uses a
// fresh one-time ticket obtained through the shared API client (EventSource
// cannot send the Authorization header). The server sends one "history" event,
// then a "log" event per line, and "end" before it closes the stream itself.
// The decisions (backoff, what to do after `end`, visibility, the cap hint)
// are pure functions in lib/logStream.ts; this hook only wires them to the
// browser.
export function useLogs({ deploymentId }: UseLogsOptions) {
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [meta, setMeta] = useState<LogMeta | null>(null);
  const [status, setStatus] = useState<LogStatus>("reconnecting");
  const [everConnected, setEverConnected] = useState(false);
  const [canRetry, setCanRetry] = useState(true);
  const [cleared, setCleared] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const buffer = useRef(newBuffer());
  // Lines at or before this time were cleared by the user and stay gone when a
  // later history event brings them back.
  const clearedUpTo = useRef(-Infinity);
  const newest = useRef(-Infinity);
  const reconnectNow = useRef<() => void>(() => {});

  useEffect(() => {
    if (!deploymentId || !getToken()) return;

    let closed = false; // unmounted or deploymentId changed
    let terminal = false; // stop retrying until the user presses Retry
    let paused = false; // tab in the background: no stream on purpose
    let connected = false; // a stream is open right now
    let firstHistory = true;
    let attempts = 0;
    let failedOpens = 0; // consecutive opens refused after a granted ticket
    let generation = 0; // bumped per connection attempt; stale async work checks it
    let source: EventSource | null = null;
    let openedAt = 0;
    let lastSilentAt = -Infinity;
    let silentTimes: number[] = []; // silent reconnects inside the last 5 minutes
    let lastRestartAt = -Infinity;
    let retryTimer: ReturnType<typeof setTimeout> | undefined;
    let hiddenTimer: ReturnType<typeof setTimeout> | undefined;
    let restartTimer: ReturnType<typeof setTimeout> | undefined;
    let hiddenAt = 0;
    let ticketAbort: AbortController | null = null;

    // A new deployment starts from nothing.
    clearedUpTo.current = -Infinity;
    newest.current = -Infinity;
    bufferReset(buffer.current);
    // eslint-disable-next-line react-hooks/set-state-in-effect -- intentional reset of the previous deployment's state when deploymentId changes
    setLogs([]);
    setMeta(null);
    setError(null);
    setCleared(false);
    setCanRetry(true);
    setEverConnected(false);

    const path = `/logs/deployments/${encodeURIComponent(deploymentId)}`;

    function stopStream() {
      generation += 1;
      ticketAbort?.abort();
      ticketAbort = null;
      source?.close();
      source = null;
      connected = false;
    }

    function schedule(delayMs: number) {
      if (closed || terminal || paused) return;
      clearTimeout(retryTimer);
      retryTimer = setTimeout(() => void connect(false), delayMs);
    }

    // Counts as one failed attempt and waits with capped, jittered backoff.
    function scheduleBackoff(minDelayMs = 0) {
      const delay = Math.max(backoffDelay(attempts, Math.random()), minDelayMs);
      attempts += 1;
      schedule(delay);
    }

    // The open stream (if any) closed on its own: forget old failures only if it was healthy.
    function noteClosed() {
      attempts = attemptsAfterClose(attempts, connected ? Date.now() - openedAt : 0);
      connected = false;
    }

    // replace: keep the current stream (if any) until a new ticket is in hand
    // and the new stream is created; a failure then leaves the old one alone.
    async function connect(replace: boolean) {
      if (closed || terminal || paused) return;
      clearTimeout(retryTimer);
      if (replace && source) {
        generation += 1;
        ticketAbort?.abort();
      } else {
        stopStream();
      }
      const mine = generation;
      const abort = new AbortController();
      ticketAbort = abort;

      let ticket: string | null;
      try {
        // Right before EventSource, through the shared client: an expired
        // access token refreshes here, and a dead session signs the user out.
        ticket = parseTicketResponse(
          await apiFetch<unknown>(`${path}/stream-ticket`, { method: "POST", signal: abort.signal })
        );
      } catch (err) {
        if (closed || mine !== generation) return; // superseded or unmounted: say nothing
        if (replace && source) return; // the old stream still works
        failedOpens = nextFailedOpens(failedOpens, "ticket_failed");
        connected = false;
        const outcome = classifyTicketError(err);
        setError(outcome.message);
        if (outcome.kind === "terminal") {
          terminal = true;
          setCanRetry(outcome.canRetry);
          setStatus("disconnected");
          return;
        }
        setStatus("reconnecting");
        scheduleBackoff(outcome.minDelayMs);
        return;
      }
      // A late ticket never opens a stream after unmount, a newer attempt, or a pause.
      if (closed || mine !== generation) return;
      if (ticket === null) {
        failedOpens = nextFailedOpens(failedOpens, "ticket_failed");
        if (replace && source) return;
        setError(BAD_TICKET_MESSAGE);
        setStatus("reconnecting");
        scheduleBackoff();
        return;
      }

      let es: EventSource;
      try {
        es = new EventSource(`${API_BASE}${path}/stream?ticket=${encodeURIComponent(ticket)}`);
      } catch {
        failedOpens = nextFailedOpens(failedOpens, "ticket_failed");
        if (replace && source) return;
        setError(OPEN_FAILED_MESSAGE);
        setStatus("reconnecting");
        scheduleBackoff();
        return;
      }
      const old = source;
      source = es;
      old?.close();
      // The old stream's health says nothing about the new one: until it opens,
      // a refusal must count as a failure (noteClosed sees connected=false).
      connected = false;
      openedAt = 0;
      let opened = false;

      es.onopen = () => {
        if (source !== es) return;
        opened = true;
        openedAt = Date.now();
        connected = true;
        failedOpens = nextFailedOpens(failedOpens, "opened");
        // `attempts` is not reset here: it resets only when a stream has stayed
        // open long enough (attemptsAfterClose), so a server that accepts and
        // immediately ends streams keeps growing the backoff.
        setStatus("live");
        setEverConnected(true);
        setError(null);
      };

      es.onerror = () => {
        if (source !== es) return;
        // Close first. The ticket is single use: left to itself the browser
        // would retry the same URL with the spent ticket, get 401, and loop
        // without ever fetching a new one. We reconnect ourselves instead.
        es.close();
        source = null;
        noteClosed();
        if (closed) return;
        setStatus("reconnecting");
        if (!opened) {
          // EventSource hides the HTTP status. Several consecutive refused
          // opens while tickets are granted may mean the per-user stream cap.
          failedOpens = nextFailedOpens(failedOpens, "refused");
          if (showCapHint(failedOpens)) setError(TOO_MANY_STREAMS_MESSAGE);
        }
        scheduleBackoff();
      };

      es.addEventListener("history", (ev) => {
        if (source !== es) return;
        const data = parseHistoryEvent((ev as MessageEvent<string>).data);
        if (!data) return;
        newest.current = latestMs(data.logs, newest.current);
        if (firstHistory) {
          // Only the first one replaces; later ones (after a reconnect) merge, so
          // a long session is not cut to 100 lines, Clear is not undone and
          // scroll position is not lost.
          firstHistory = false;
          bufferReset(buffer.current, data.logs);
          setLogs(buffer.current.entries);
        } else if (bufferMerge(buffer.current, data.logs, clearedUpTo.current)) {
          setLogs(buffer.current.entries);
        }
        if (data.meta) setMeta(data.meta);
      });

      es.addEventListener("log", (ev) => {
        if (source !== es) return;
        const entry = parseLogEvent((ev as MessageEvent<string>).data);
        if (!entry) return;
        newest.current = latestMs([entry], newest.current);
        if (bufferAppend(buffer.current, entry)) setLogs(buffer.current.entries);
      });

      es.addEventListener("end", (ev) => {
        if (source !== es) return;
        const reason = parseEndEvent((ev as MessageEvent<string>).data);
        const now = Date.now();
        silentTimes = recentSilent(silentTimes, now);
        const plan = planAfterEnd({
          silentInWindow: silentTimes.length,
          reason,
          msSinceOpen: now - openedAt,
          msSinceLastSilent: now - lastSilentAt,
          attempts: attemptsAfterClose(attempts, now - openedAt),
          rand: Math.random(),
        });
        attempts = attemptsAfterClose(attempts, now - openedAt);
        es.close();
        source = null;
        connected = false;
        if (plan.silent) {
          // The routine 1 h cap: reopen at once and silently (the badge keeps
          // saying Live and no attempt is counted).
          lastSilentAt = now;
          silentTimes.push(now);
        } else {
          setStatus("reconnecting");
        }
        if (plan.countAttempt) attempts += 1;
        schedule(plan.delayMs);
      });
    }

    // Reconnect, forgetting earlier failures (online, tab visible, Retry).
    // Spaced at least MIN_RESTART_GAP_MS apart: calls inside the gap are
    // coalesced into one restart when it ends.
    function restart(replace = false) {
      if (closed) return;
      if (restartTimer !== undefined) return;
      const wait = restartDelay(Date.now(), lastRestartAt);
      if (wait > 0) {
        restartTimer = setTimeout(() => {
          restartTimer = undefined;
          restart(replace);
        }, wait);
        return;
      }
      lastRestartAt = Date.now();
      terminal = false;
      paused = false;
      attempts = 0;
      failedOpens = 0;
      if (!connected && !replace) setStatus("reconnecting");
      void connect(replace);
    }
    reconnectNow.current = () => {
      setError(null);
      setCanRetry(true);
      restart();
    };

    function pause() {
      paused = true;
      clearTimeout(retryTimer);
      stopStream();
      setStatus("paused");
    }

    function onOnline() {
      if (!paused && !terminal && !connected) restart();
    }

    function onVisibility() {
      if (document.visibilityState === "hidden") {
        hiddenAt = Date.now();
        clearTimeout(hiddenTimer);
        hiddenTimer = setTimeout(pause, HIDDEN_CLOSE_MS);
        return;
      }
      clearTimeout(hiddenTimer);
      const msAway = hiddenAt > 0 ? Date.now() - hiddenAt : 0;
      hiddenAt = 0;
      const action = visibleAction({ terminal, paused, connected, msAway });
      if (action !== "none") restart(action === "replace");
    }

    window.addEventListener("online", onOnline);
    document.addEventListener("visibilitychange", onVisibility);
    if (initialMode(document.visibilityState) === "paused") {
      // Opened in the background: no ticket, no stream until the tab is shown.
      paused = true;
      hiddenAt = Date.now();
      setStatus("paused");
    } else {
      setStatus("reconnecting");
      void connect(false);
    }

    return () => {
      closed = true;
      clearTimeout(retryTimer);
      clearTimeout(hiddenTimer);
      clearTimeout(restartTimer);
      window.removeEventListener("online", onOnline);
      document.removeEventListener("visibilitychange", onVisibility);
      stopStream();
      reconnectNow.current = () => {};
    };
  }, [deploymentId]);

  const clearLogs = useCallback(() => {
    clearedUpTo.current = newest.current;
    bufferReset(buffer.current);
    setLogs(buffer.current.entries);
    setCleared(true);
  }, []);

  const retry = useCallback(() => reconnectNow.current(), []);

  return { logs, meta, status, everConnected, canRetry, cleared, isConnected: status === "live", error, clearLogs, retry };
}

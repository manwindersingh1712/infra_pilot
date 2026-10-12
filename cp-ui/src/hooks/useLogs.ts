import { useState, useEffect, useRef, useCallback } from "react";
import { API_BASE, apiFetch, getToken } from "../lib/api";
import {
  FAILED_OPENS_BEFORE_HINT, TOO_MANY_STREAMS_MESSAGE, appendLive, backoffDelay, classifyTicketError, endDelay,
  latestMs, mergeHistory, parseEndEvent, parseHistoryEvent, parseLogEvent,
  type LogEntry, type LogMeta,
} from "../lib/logStream";

export type { LogEntry } from "../lib/logStream";

// live: the stream is open. reconnecting: trying again (it never gives up).
// disconnected: terminal, the deployment no longer exists or the session ended.
export type LogStatus = "live" | "reconnecting" | "disconnected";

interface UseLogsOptions {
  deploymentId: string;
}

// A tab hidden this long closes its stream (it reopens when the tab is shown).
const HIDDEN_CLOSE_MS = 5 * 60_000;
// A tab hidden this long may hold a dead connection (sleep, network change):
// reconnect when it comes back instead of trusting the old socket.
const HIDDEN_STALE_MS = 30_000;

// Streams a deployment's logs over Server-Sent Events. Every connection uses a
// fresh one-time ticket obtained through the shared API client (EventSource
// cannot send the Authorization header). The server sends one "history" event,
// then a "log" event per line, and "end" before it closes the stream itself.
export function useLogs({ deploymentId }: UseLogsOptions) {
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [meta, setMeta] = useState<LogMeta | null>(null);
  const [status, setStatus] = useState<LogStatus>("reconnecting");
  const [everConnected, setEverConnected] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Lines at or before this time were cleared by the user and stay gone when a
  // later history event brings them back.
  const clearedUpTo = useRef(-Infinity);
  const newest = useRef(-Infinity);
  const reconnectNow = useRef<() => void>(() => {});

  useEffect(() => {
    if (!deploymentId || !getToken()) return;

    let closed = false; // unmounted or deploymentId changed
    let terminal = false; // stop retrying until the user presses Retry
    let paused = false; // tab hidden for a long time: stream closed on purpose
    let connected = false; // a stream is open right now
    let firstHistory = true;
    let attempts = 0;
    let failedOpens = 0;
    let generation = 0; // bumped per connection attempt; stale async work checks it
    let source: EventSource | null = null;
    let retryTimer: ReturnType<typeof setTimeout> | undefined;
    let hiddenTimer: ReturnType<typeof setTimeout> | undefined;
    let hiddenAt = 0;
    let ticketAbort: AbortController | null = null;

    // A new deployment starts from nothing.
    clearedUpTo.current = -Infinity;
    newest.current = -Infinity;
    // eslint-disable-next-line react-hooks/set-state-in-effect -- intentional reset of the previous deployment's state when deploymentId changes
    setLogs([]);
    setMeta(null);
    setError(null);
    setStatus("reconnecting");
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
      retryTimer = setTimeout(() => void connect(), delayMs);
    }

    // Counts as one failed attempt and waits with capped, jittered backoff.
    function scheduleBackoff(minDelayMs = 0) {
      const delay = Math.max(backoffDelay(attempts, Math.random()), minDelayMs);
      attempts += 1;
      schedule(delay);
    }

    async function connect() {
      if (closed || terminal || paused) return;
      clearTimeout(retryTimer);
      stopStream();
      const mine = generation;
      const abort = new AbortController();
      ticketAbort = abort;

      let ticket: string;
      try {
        // Right before EventSource, through the shared client: an expired
        // access token refreshes here, and a dead session signs the user out.
        ({ ticket } = await apiFetch<{ ticket: string }>(`${path}/stream-ticket`, { method: "POST", signal: abort.signal }));
      } catch (err) {
        if (closed || mine !== generation) return; // superseded or unmounted: say nothing
        connected = false;
        const outcome = classifyTicketError(err);
        setError(outcome.message);
        if (outcome.kind === "terminal") {
          terminal = true;
          setStatus("disconnected");
          return;
        }
        setStatus("reconnecting");
        scheduleBackoff(outcome.minDelayMs);
        return;
      }
      // A late ticket never opens a stream after unmount, a newer attempt, or a pause.
      if (closed || mine !== generation) return;

      const es = new EventSource(`${API_BASE}${path}/stream?ticket=${encodeURIComponent(ticket)}`);
      source = es;
      let opened = false;
      let openedAt = 0;

      es.onopen = () => {
        if (source !== es) return;
        opened = true;
        openedAt = Date.now();
        connected = true;
        attempts = 0;
        failedOpens = 0;
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
        connected = false;
        if (closed) return;
        setStatus("reconnecting");
        if (!opened) {
          failedOpens += 1;
          // EventSource hides the HTTP status. Several refused opens in a row
          // while tickets are granted usually mean the per-user stream cap.
          if (failedOpens >= FAILED_OPENS_BEFORE_HINT) setError(TOO_MANY_STREAMS_MESSAGE);
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
          setLogs(data.logs);
        } else {
          setLogs((prev) => mergeHistory(prev, data.logs, clearedUpTo.current));
        }
        if (data.meta) setMeta(data.meta);
      });

      es.addEventListener("log", (ev) => {
        if (source !== es) return;
        const entry = parseLogEvent((ev as MessageEvent<string>).data);
        if (!entry) return;
        newest.current = latestMs([entry], newest.current);
        setLogs((prev) => appendLive(prev, entry));
      });

      es.addEventListener("end", (ev) => {
        if (source !== es) return;
        const reason = parseEndEvent((ev as MessageEvent<string>).data);
        es.close();
        source = null;
        connected = false;
        // An `end` right after opening is not the 1 h cap; back off instead of
        // hammering the server in a tight loop.
        if (reason === "max_duration" && Date.now() - openedAt > 1000) {
          // The routine 1 h cap: reopen at once and silently (the badge keeps
          // saying Live and no attempt is counted).
          schedule(endDelay(reason, 0));
          return;
        }
        setStatus("reconnecting");
        attempts += 1;
        schedule(endDelay(reason, Math.random()));
      });
    }

    // Reconnect now, forgetting earlier failures (online, tab visible, Retry).
    function restart() {
      if (closed) return;
      terminal = false;
      paused = false;
      attempts = 0;
      failedOpens = 0;
      if (!connected) setStatus("reconnecting");
      void connect();
    }
    reconnectNow.current = () => {
      setError(null);
      restart();
    };

    function onOnline() {
      if (!paused && !terminal && !connected) restart();
    }

    function onVisibility() {
      if (document.visibilityState === "hidden") {
        hiddenAt = Date.now();
        clearTimeout(hiddenTimer);
        hiddenTimer = setTimeout(() => {
          paused = true;
          clearTimeout(retryTimer);
          stopStream();
        }, HIDDEN_CLOSE_MS);
        return;
      }
      clearTimeout(hiddenTimer);
      if (terminal) return;
      const wasAway = hiddenAt > 0 ? Date.now() - hiddenAt : 0;
      hiddenAt = 0;
      if (paused || !connected || wasAway > HIDDEN_STALE_MS) restart();
    }

    window.addEventListener("online", onOnline);
    document.addEventListener("visibilitychange", onVisibility);
    void connect();

    return () => {
      closed = true;
      clearTimeout(retryTimer);
      clearTimeout(hiddenTimer);
      window.removeEventListener("online", onOnline);
      document.removeEventListener("visibilitychange", onVisibility);
      stopStream();
      reconnectNow.current = () => {};
    };
  }, [deploymentId]);

  const clearLogs = useCallback(() => {
    clearedUpTo.current = newest.current;
    setLogs([]);
  }, []);

  const retry = useCallback(() => reconnectNow.current(), []);

  return { logs, meta, status, everConnected, isConnected: status === "live", error, clearLogs, retry };
}

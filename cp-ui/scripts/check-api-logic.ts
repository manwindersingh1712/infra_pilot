// Run: npm run check:logic  (node scripts/check-api-logic.ts)   (Node 22.18+; no test framework needed)
import assert from "node:assert/strict";
import { refreshSession, withStorageLock, type RefreshCallResult, type RefreshDeps } from "../src/lib/refresh.ts";
import { isValidProjectName, projectNameTooLong } from "../src/lib/projectName.ts";
import { repoUrlProblem, REPO_URL_SSH_MESSAGE, REPO_URL_FILE_MESSAGE, REPO_URL_HINT, REPO_URL_HTTP_MESSAGE } from "../src/lib/repoUrl.ts";
import { newServiceOverlaps } from "../src/lib/canvasLayout.ts";
import { ApiError, humanizeApiError, rateLimitMessage, parseErrorCode, parseRetryAfter, SESSION_EXPIRED_MESSAGE } from "../src/lib/errors.ts";
import {
  DEFAULT_RATE_LIMIT_WAIT_S, MAX_SILENT_IN_WINDOW, SILENT_WINDOW_MS, recentSilent, FAILED_OPENS_BEFORE_HINT, HIDDEN_STALE_MS, MAX_LINES, MIN_RESTART_GAP_MS, STABLE_MS,
  TOO_MANY_STREAMS_MESSAGE, attemptsAfterClose, backoffDelay, bufferAppend, bufferMerge, bufferReset,
  classifyTicketError, emptyStateText, endDelay, entryKey, initialMode, latestMs, newBuffer, nextFailedOpens,
  parseEndEvent, parseHistoryEvent, parseLogEvent, parseTicketResponse, planAfterEnd, restartDelay, showCapHint,
  visibleAction, type LogEntry,
} from "../src/lib/logStream.ts";

import {
  DELETE_CLOSED, DELETING_NOTICE, beginDelete, buildConnectionRows, deploymentBadge, buildConnectionUrl, cancelDelete,
  classifyDeleteError, connectedServiceNames, deletePath, failDelete, openDelete, passwordEnvKey,
  pickConnectionDeployment, MASK, NO_PASSWORD_TEXT, ARM_GUARD_MS, isDeliberateConfirm, safeEncodeURIComponent, connectionPanelState, copyButtonLabel, deploymentsOf, shellQuote,
  type ConnectionRow, type PasswordState, PURGE_ARMED_LABEL, PURGE_ARM_MS, PURGE_LABEL, disarmPurge, nextFocusIndex, shouldClosePanel, deleteFailedNotice, purgeButtonLabel,
} from "../src/lib/serviceActions.ts";

let passed = 0;
function check(name: string, fn: () => void | Promise<void>): Promise<void> {
  return Promise.resolve(fn()).then(
    () => {
      passed++;
    },
    (e) => {
      console.error(`FAIL ${name}`);
      throw e;
    }
  );
}

// ---- errors.ts
await check("parseErrorCode reads {error}", () => {
  assert.equal(parseErrorCode('{"error":"invalid_request"}\n'), "invalid_request");
  assert.equal(parseErrorCode('{"error":"Project not found"}'), "Project not found");
  assert.equal(parseErrorCode(""), "");
  assert.equal(parseErrorCode("<html>502</html>"), "");
  assert.equal(parseErrorCode('{"error":42}'), "");
  assert.equal(parseErrorCode("null"), "");
});

await check("humanizeApiError maps every documented code", () => {
  const codes = [
    "invalid_request", "password_too_short", "password_too_long", "invalid_repo_url", "service_not_found",
    "invalid_env_var", "request_too_large", "too_many_requests", "too_many_streams", "invalid_ticket",
    "purge_choice_required", "invalid_purge_data", "timeout",
  ];
  // A dropped code would fall back to the status text, so compare against
  // the fallback of an unmapped code with the same status (418 has no entry).
  const fallback = humanizeApiError(new ApiError(418, "zzz_unmapped"));
  for (const c of codes) {
    const m = humanizeApiError(new ApiError(418, c));
    assert.ok(m.length > 10 && !m.includes(c), `${c} -> ${m}`);
    assert.notEqual(m, fallback, `${c} has no message of its own`);
  }
});

await check("humanizeApiError status fallbacks", () => {
  assert.equal(humanizeApiError(new ApiError(401, "")), SESSION_EXPIRED_MESSAGE);
  assert.match(humanizeApiError(new ApiError(404, "")), /no longer exists/i);
  assert.match(humanizeApiError(new ApiError(429, "")), /too many/i);
  assert.match(humanizeApiError(new ApiError(503, "")), /unavailable/i);
  assert.match(humanizeApiError(new ApiError(500, "")), /went wrong/i);
  assert.match(humanizeApiError(new ApiError(502, "")), /went wrong/i);
  assert.match(humanizeApiError(new ApiError(0, "network_error")), /reach the server/i);
  assert.match(humanizeApiError(new TypeError("Failed to fetch")), /reach the server/i);
  assert.match(humanizeApiError("weird"), /went wrong/i);
});

await check("humanizeApiError tolerates sentences and never leaks raw JSON", () => {
  assert.equal(humanizeApiError(new ApiError(404, "Project not found")), "That project no longer exists.");
  assert.equal(humanizeApiError(new ApiError(409, "Connection already exists")), "Connection already exists");
  assert.equal(humanizeApiError(new ApiError(401, "invalid_credentials")), "Invalid email or password.");
  assert.ok(!humanizeApiError(new ApiError(418, "teapot_code")).includes("teapot_code"));
  assert.ok(!humanizeApiError(new ApiError(400, "__proto__")).includes("function"));
});

await check("humanizeApiError overrides win", () => {
  assert.equal(humanizeApiError(new ApiError(400, "invalid_request"), { invalid_request: "X" }), "X");
});

await check("project name validity uses the trimmed length", () => {
  assert.equal(isValidProjectName(""), false);
  assert.equal(isValidProjectName("a"), false);
  assert.equal(isValidProjectName("  a  "), false);
  assert.equal(isValidProjectName("   "), false);
  assert.equal(isValidProjectName("ab"), true);
  assert.equal(isValidProjectName("  ab  "), true);
  assert.equal(isValidProjectName("x".repeat(100)), true);
  assert.equal(isValidProjectName("x".repeat(101)), false);
  assert.equal(isValidProjectName("\u{1F600}".repeat(100)), true, "code points, not UTF-16 units");
});

// ---- refresh.ts
function harness(opts: { access?: string; refresh?: string | null; replies: Array<RefreshCallResult | "throw" | ((h: { refresh: string | null }) => RefreshCallResult)>; onCall?: (rt: string, h: { refresh: string | null }) => void }) {
  const h = { access: opts.access ?? "old", refresh: opts.refresh === undefined ? "r1" : opts.refresh, calls: [] as string[] };
  const deps: RefreshDeps = {
    readRefresh: () => h.refresh,
    readAccess: () => h.access,
    call: async (rt) => {
      h.calls.push(rt);
      opts.onCall?.(rt, h);
      const r = opts.replies.shift();
      if (r === undefined || r === "throw") throw new TypeError("Failed to fetch");
      return typeof r === "function" ? r(h) : r;
    },
    store: (t, r) => {
      h.access = t;
      h.refresh = r;
    },
  };
  return { h, deps };
}

await check("refresh 200 stores the new pair", async () => {
  const { h, deps } = harness({ replies: [{ status: 200, token: "a2", refreshToken: "r2" }] });
  assert.equal(await refreshSession(deps, "old"), "refreshed");
  assert.deepEqual([h.access, h.refresh, h.calls], ["a2", "r2", ["r1"]]);
});

await check("no refresh token -> expired, no call", async () => {
  const { h, deps } = harness({ refresh: null, replies: [] });
  assert.equal(await refreshSession(deps, "old"), "expired");
  assert.equal(h.calls.length, 0);
});

await check("401 with an unchanged token -> expired (one call)", async () => {
  const { h, deps } = harness({ replies: [{ status: 401 }] });
  assert.equal(await refreshSession(deps, "old"), "expired");
  assert.equal(h.calls.length, 1);
});

await check("401 but another tab rotated the token -> retry once with the new one", async () => {
  const { h, deps } = harness({
    replies: [
      (hh) => {
        hh.refresh = "r-from-other-tab";
        return { status: 401 };
      },
      { status: 200, token: "a3", refreshToken: "r3" },
    ],
  });
  assert.equal(await refreshSession(deps, "old"), "refreshed");
  assert.deepEqual(h.calls, ["r1", "r-from-other-tab"]);
});

await check("401, changed token, second 401 -> expired, only two calls", async () => {
  const { h, deps } = harness({
    replies: [
      (hh) => {
        hh.refresh = "r-other";
        return { status: 401 };
      },
      { status: 401 },
    ],
  });
  assert.equal(await refreshSession(deps, "old"), "expired");
  assert.equal(h.calls.length, 2);
});

await check("401 then the token was removed by another tab's logout -> expired", async () => {
  const { deps } = harness({ replies: [(hh) => { hh.refresh = null; return { status: 401 }; }] });
  assert.equal(await refreshSession(deps, "old"), "expired");
});

for (const status of [429, 500, 502, 503, 504, 400, 403]) {
  await check(`status ${status} never signs out`, async () => {
    const { h, deps } = harness({ replies: [{ status }] });
    assert.equal(await refreshSession(deps, "old"), "unavailable");
    assert.equal(h.refresh, "r1");
    assert.equal(h.access, "old");
  });
}

await check("network error never signs out", async () => {
  const { deps } = harness({ replies: ["throw"] });
  assert.equal(await refreshSession(deps, "old"), "unavailable");
});

await check("200 without tokens is unavailable, nothing stored", async () => {
  const { h, deps } = harness({ replies: [{ status: 200 }] });
  assert.equal(await refreshSession(deps, "old"), "unavailable");
  assert.equal(h.access, "old");
});

await check("access token already rotated by another tab -> no server call", async () => {
  const { h, deps } = harness({ access: "fresh", replies: [] });
  assert.equal(await refreshSession(deps, "old"), "refreshed");
  assert.equal(h.calls.length, 0);
});

await check("empty stale access with a stored access token counts as refreshed elsewhere", async () => {
  const { h, deps } = harness({ access: "fresh", replies: [] });
  assert.equal(await refreshSession(deps, ""), "refreshed");
  assert.equal(h.calls.length, 0);
});

await check("corrupted access token (x) refreshes", async () => {
  const { h, deps } = harness({ access: "x", replies: [{ status: 200, token: "jwt", refreshToken: "r2" }] });
  assert.equal(await refreshSession(deps, "x"), "refreshed");
  assert.equal(h.access, "jwt");
});

// ---- storage lock
function memStorage() {
  const m = new Map<string, string>();
  return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), removeItem: (k: string) => void m.delete(k), m };
}

await check("storage lock serialises concurrent holders", async () => {
  const st = memStorage();
  let active = 0;
  let maxActive = 0;
  const work = async () => {
    active++;
    maxActive = Math.max(maxActive, active);
    await new Promise((r) => setTimeout(r, 40));
    active--;
  };
  await Promise.all([1, 2, 3].map((i) => withStorageLock(st, work, { id: `t${i}`, settleMs: 5, pollMs: 5 })));
  assert.equal(maxActive, 1);
  assert.equal(st.m.size, 0, "lock released");
});

await check("storage lock: a dead holder expires", async () => {
  const st = memStorage();
  st.setItem("cp_refresh_lock", `dead|${Date.now() - 1}`);
  let ownerInside = "";
  // waitMs is long: falling through after the wait would not hold the lock.
  await withStorageLock(st, async () => void (ownerInside = st.getItem("cp_refresh_lock") ?? ""), {
    id: "me",
    waitMs: 5_000,
    settleMs: 5,
    pollMs: 5,
  });
  assert.match(ownerInside, /^me\|/, "the dead holder's claim was replaced by ours before fn ran");
  assert.equal(st.m.size, 0, "lock released");
});

await check("storage lock: a claim overwritten during the settle window is not held", async () => {
  const st = memStorage();
  let raced = false;
  const racy = {
    getItem: st.getItem,
    removeItem: st.removeItem,
    // The other tab's write lands right after ours, the way two tabs that
    // check the key in the same instant end up writing back to back.
    setItem: (k: string, v: string) => {
      st.setItem(k, v);
      if (!raced && v.startsWith("me|")) {
        raced = true;
        st.setItem(k, `other|${Date.now() + 80}`);
      }
    },
  };
  const t0 = Date.now();
  let startedAfter = -1;
  await withStorageLock(racy, async () => void (startedAfter = Date.now() - t0), { id: "me", waitMs: 5_000, settleMs: 5, pollMs: 5 });
  assert.ok(startedAfter >= 70, `fn ran ${startedAfter} ms in, while the other tab's claim was live`);
});

await check("storage lock: does not remove a key another tab took over during fn", async () => {
  const st = memStorage();
  await withStorageLock(
    st,
    async () => {
      st.setItem("cp_refresh_lock", `other|${Date.now() + 60_000}`);
    },
    { id: "me", settleMs: 5, pollMs: 5 }
  );
  assert.match(st.getItem("cp_refresh_lock") ?? "", /^other\|/);
});

await check("storage lock: an absurd expiry from storage is treated as expired", async () => {
  const st = memStorage();
  st.setItem("cp_refresh_lock", `evil|${Date.now() + 10 * 365 * 86_400_000}`);
  const t0 = Date.now();
  let ran = false;
  // ttl 60 ms: the far-future claim must be treated as expiring within 60 ms.
  await withStorageLock(st, async () => void (ran = true), { id: "me", ttlMs: 60, waitMs: 5_000, settleMs: 5, pollMs: 5 });
  assert.ok(ran);
  assert.ok(Date.now() - t0 < 1_000, "did not wait out the forged expiry");
  assert.equal(st.m.size, 0, "acquired (and released) the lock rather than falling through");
});

await check("storage lock: runs anyway after waitMs and leaves the other holder's claim", async () => {
  const st = memStorage();
  st.setItem("cp_refresh_lock", `other|${Date.now() + 60_000}`);
  let ran = false;
  await withStorageLock(st, async () => void (ran = true), { id: "me", ttlMs: 120_000, waitMs: 30, settleMs: 5, pollMs: 5 });
  assert.ok(ran);
  assert.match(st.getItem("cp_refresh_lock") ?? "", /^other\|/);
});

// ---- logStream.ts
const entry = (t: string, m: string, source: LogEntry["source"] = "stdout"): LogEntry => ({ deploymentId: "d", source, message: m, timestamp: t });
const T = (s: number) => new Date(Date.UTC(2026, 0, 1, 0, 0, s)).toISOString();

await check("parseRetryAfter accepts whole seconds only", () => {
  assert.equal(parseRetryAfter("5"), 5);
  assert.equal(parseRetryAfter(" 30 "), 30);
  assert.equal(parseRetryAfter(null), undefined);
  assert.equal(parseRetryAfter("Wed, 21 Oct 2026 07:28:00 GMT"), undefined);
  assert.equal(parseRetryAfter("-1"), undefined);
  assert.equal(parseRetryAfter("1.5"), undefined);
  assert.equal(parseRetryAfter(""), undefined);
  assert.equal(new ApiError(429, "too_many_streams", 7).retryAfter, 7);
  assert.equal(new ApiError(500, "x").retryAfter, undefined);
});

await check("backoffDelay grows, stays within [ceiling/2, ceiling], is capped at 30 s and jittered", () => {
  assert.equal(backoffDelay(0, 0), 500);
  assert.equal(backoffDelay(0, 1), 1000);
  assert.equal(backoffDelay(1, 1), 2000);
  assert.equal(backoffDelay(2, 1), 4000);
  assert.equal(backoffDelay(4, 1), 16000);
  assert.equal(backoffDelay(5, 1), 30000);
  assert.equal(backoffDelay(50, 1), 30000);
  assert.equal(backoffDelay(1000, 0), 15000);
  assert.equal(backoffDelay(3, 0), 4000);
  assert.ok(backoffDelay(3, 0.25) < backoffDelay(3, 0.75), "jitter spreads the delay");
  for (let a = 0; a < 40; a++) for (const r of [0, 0.3, 0.999]) {
    const d = backoffDelay(a, r);
    assert.ok(d > 0 && d <= 30000, `attempt ${a}: ${d}`);
  }
  assert.equal(backoffDelay(-3, 1), 1000, "negative attempt behaves like 0");
  assert.equal(backoffDelay(2, 7), 4000, "rand above 1 is clamped");
  assert.equal(backoffDelay(2, -7), 2000, "rand below 0 is clamped");
});

await check("endDelay: max_duration is immediate, shutdown waits 1-3 s", () => {
  assert.equal(endDelay("max_duration", 0.9), 0);
  assert.equal(endDelay("shutdown", 0), 1000);
  assert.equal(endDelay("shutdown", 1), 3000);
  assert.equal(endDelay("shutdown", 0.5), 2000);
});

await check("parseEndEvent: only max_duration is routine; junk counts as shutdown", () => {
  assert.equal(parseEndEvent('{"reason":"max_duration"}'), "max_duration");
  assert.equal(parseEndEvent('{"reason":"shutdown"}'), "shutdown");
  assert.equal(parseEndEvent('{"reason":"other"}'), "shutdown");
  assert.equal(parseEndEvent("not json"), "shutdown");
  assert.equal(parseEndEvent(undefined), "shutdown");
  assert.equal(parseEndEvent("null"), "shutdown");
});

await check("parseLogEvent / parseHistoryEvent ignore malformed payloads", () => {
  assert.deepEqual(parseLogEvent(JSON.stringify(entry(T(1), "hi"))), entry(T(1), "hi"));
  assert.equal(parseLogEvent("{oops"), null);
  assert.equal(parseLogEvent("null"), null);
  assert.equal(parseLogEvent("[]"), null);
  assert.equal(parseLogEvent(JSON.stringify({ message: 1, timestamp: "x", source: "stdout" })), null);
  assert.equal(parseLogEvent(JSON.stringify({ message: "m", timestamp: "x", source: "weird" })), null);
  assert.equal(parseLogEvent(42), null);
  const h = parseHistoryEvent(JSON.stringify({ logs: [entry(T(1), "a"), { bad: true }, entry(T(2), "b")], meta: { source: "redis", totalCount: 9 } }));
  assert.deepEqual(h?.logs.map((l) => l.message), ["a", "b"]);
  assert.deepEqual(h?.meta, { source: "redis", totalCount: 9 });
  assert.equal(parseHistoryEvent("{}"), null);
  assert.equal(parseHistoryEvent("{oops"), null);
  assert.equal(parseHistoryEvent('{"logs":"x"}'), null);
  assert.equal(parseHistoryEvent('{"logs":[],"meta":{"source":"nope"}}')?.meta, null);
  assert.deepEqual(parseHistoryEvent('{"logs":[],"meta":{"source":"clickhouse"}}')?.meta, { source: "clickhouse" });
});

const msgs = (b: { entries: LogEntry[] }) => b.entries.map((l) => l.message);
const bufWith = (es: LogEntry[], max = MAX_LINES) => {
  const b = newBuffer(max);
  bufferReset(b, es);
  return b;
};
const keysMatch = (b: { entries: LogEntry[]; keys: Set<string> }) =>
  b.keys.size === b.entries.length && b.entries.every((e) => b.keys.has(entryKey(e)));

await check("bufferMerge: dedupes on timestamp+message, keeps time order, keeps the user's lines", () => {
  const b = bufWith([entry(T(1), "a"), entry(T(2), "b"), entry(T(5), "e")]);
  assert.equal(bufferMerge(b, [entry(T(2), "b"), entry(T(3), "c"), entry(T(6), "f")]), true);
  assert.deepEqual(msgs(b), ["a", "b", "c", "e", "f"]);
  const b2 = bufWith([entry(T(1), "x")]);
  bufferMerge(b2, [entry(T(1), "y"), entry(T(2), "x")]);
  assert.deepEqual(msgs(b2), ["x", "y", "x"]);
  const b3 = newBuffer();
  bufferMerge(b3, [entry(T(1), "d"), entry(T(1), "d")]);
  assert.equal(b3.entries.length, 1);
  const b4 = bufWith([entry(T(1), "p")]);
  bufferMerge(b4, [entry(T(1), "q"), entry(T(1), "r")]);
  assert.deepEqual(msgs(b4), ["p", "q", "r"]);
  const big = Array.from({ length: 500 }, (_, i) => entry(T(i), `m${i}`));
  const b5 = bufWith(big);
  assert.equal(bufferMerge(b5, big.slice(-100)), false);
  assert.equal(b5.entries.length, 500);
  assert.ok(keysMatch(b));
});

await check("bufferMerge: no change keeps the same array; Clear is not undone", () => {
  const b = bufWith([entry(T(1), "a")]);
  const before = b.entries;
  assert.equal(bufferMerge(b, [entry(T(1), "a")]), false);
  assert.equal(bufferMerge(b, []), false);
  assert.equal(b.entries, before);
  const c = newBuffer();
  bufferMerge(c, [entry(T(1), "a"), entry(T(2), "b"), entry(T(3), "c")], Date.parse(T(2)));
  assert.deepEqual(msgs(c), ["c"], "lines at or before the clear point stay gone");
  const d = newBuffer();
  bufferMerge(d, [entry(T(1), "a")], Date.parse(T(1)) - 1);
  assert.equal(d.entries.length, 1);
});

await check("bufferMerge: unparseable timestamps are kept in arrival order", () => {
  const b = bufWith([entry("bad", "a")]);
  bufferMerge(b, [entry(T(1), "b"), entry("worse", "c")], Date.parse(T(0)));
  assert.deepEqual(msgs(b), ["a", "b", "c"]);
  const m = bufWith([entry(T(5), "a"), entry("bad", "m"), entry(T(6), "z")]);
  bufferMerge(m, [entry(T(1), "b")]);
  assert.deepEqual(msgs(m), ["a", "m", "z", "b"], "no sorting when a timestamp is unparseable");
});

await check("bufferAppend: dedupes in O(1), replaces the array on change only", () => {
  const b = bufWith([entry(T(1), "a")]);
  const before = b.entries;
  assert.equal(bufferAppend(b, entry(T(1), "a")), false);
  assert.equal(b.entries, before, "a duplicate leaves the array alone");
  assert.equal(bufferAppend(b, entry(T(1), "b")), true);
  assert.notEqual(b.entries, before, "a change gives React a new array");
  assert.equal(before.length, 1, "the old array is not mutated");
  assert.equal(bufferAppend(b, entry(T(2), "a")), true);
  assert.deepEqual(msgs(b), ["a", "b", "a"]);
  assert.ok(keysMatch(b));
});

await check("buffer cap: oldest lines drop, order kept, key set stays in step", () => {
  const b = newBuffer(3);
  for (let i = 1; i <= 5; i++) bufferAppend(b, entry(T(i), `l${i}`));
  assert.deepEqual(msgs(b), ["l3", "l4", "l5"]);
  assert.ok(keysMatch(b));
  // a dropped line is new again (it is not remembered)...
  assert.equal(bufferAppend(b, entry(T(1), "l1")), true);
  assert.deepEqual(msgs(b), ["l4", "l5", "l1"]);
  assert.ok(keysMatch(b));
  // ...and the cap holds for merges, keeping the NEWEST lines
  const m = bufWith([entry(T(10), "j"), entry(T(11), "k")], 3);
  bufferMerge(m, [entry(T(1), "old"), entry(T(12), "l"), entry(T(13), "m")]);
  assert.deepEqual(msgs(m), ["k", "l", "m"]);
  assert.ok(keysMatch(m));
  // bufferReset caps and dedupes too
  const r = newBuffer(2);
  bufferReset(r, [entry(T(1), "a"), entry(T(1), "a"), entry(T(2), "b"), entry(T(3), "c")]);
  assert.deepEqual(msgs(r), ["b", "c"]);
  assert.ok(keysMatch(r));
  const dup = newBuffer(10);
  bufferReset(dup, [entry(T(1), "a"), entry(T(1), "a"), entry(T(2), "b")]);
  assert.deepEqual(msgs(dup), ["a", "b"], "a history event with a repeated line keeps one");
  assert.ok(keysMatch(dup));
  assert.equal(newBuffer().max, 5000);
  assert.equal(MAX_LINES, 5000);
  // a long run never exceeds the cap
  const big = newBuffer(50);
  for (let i = 0; i < 400; i++) bufferAppend(big, entry(T(i), `n${i}`));
  assert.equal(big.entries.length, 50);
  assert.equal(big.keys.size, 50);
});

await check("latestMs", () => {
  assert.equal(latestMs([]), -Infinity);
  assert.equal(latestMs([entry(T(3), "x"), entry(T(9), "y"), entry("bad", "z")]), Date.parse(T(9)));
  assert.equal(latestMs([entry(T(3), "x")], Date.parse(T(50))), Date.parse(T(50)));
});

await check("planAfterEnd: max_duration is silent only for a stream that lived long enough", () => {
  const base = { reason: "max_duration" as const, msSinceOpen: 3_600_000, msSinceLastSilent: Infinity, attempts: 0, rand: 0.5 };
  assert.deepEqual(planAfterEnd(base), { delayMs: 0, silent: true, countAttempt: false });
  assert.deepEqual(planAfterEnd({ ...base, msSinceOpen: STABLE_MS }), { delayMs: 0, silent: true, countAttempt: false });
  const quick = planAfterEnd({ ...base, msSinceOpen: STABLE_MS - 1 });
  assert.equal(quick.silent, false);
  assert.equal(quick.countAttempt, true);
  assert.ok(quick.delayMs >= 500, "a quick end never reconnects at 0 ms");
  // a silent reconnect less than 10 s ago also backs off
  const again = planAfterEnd({ ...base, msSinceLastSilent: STABLE_MS - 1 });
  assert.equal(again.silent, false);
  assert.ok(again.delayMs >= 500);
  assert.equal(planAfterEnd({ ...base, msSinceLastSilent: STABLE_MS }).silent, true);
  // the backoff grows with the attempt count, to the 30 s cap
  assert.equal(planAfterEnd({ ...base, msSinceOpen: 0, attempts: 3, rand: 1 }).delayMs, backoffDelay(3, 1));
  assert.equal(planAfterEnd({ ...base, msSinceOpen: 0, attempts: 40, rand: 1 }).delayMs, 30_000);
});

await check("planAfterEnd: silent reconnects are capped at 3 per 5 minutes", () => {
  const base = { reason: "max_duration" as const, msSinceOpen: 10_100, msSinceLastSilent: 10_100, attempts: 0, rand: 0.5 };
  assert.equal(MAX_SILENT_IN_WINDOW, 3);
  for (const n of [0, 1, 2]) assert.equal(planAfterEnd({ ...base, silentInWindow: n }).silent, true, `n=${n}`);
  for (const n of [3, 4, 100]) {
    const p = planAfterEnd({ ...base, silentInWindow: n });
    assert.equal(p.silent, false, `n=${n}`);
    assert.equal(p.countAttempt, true);
    assert.ok(p.delayMs >= 500);
  }
  // omitted means none yet
  assert.equal(planAfterEnd(base).silent, true);
  // a stream that lived an hour is also limited once the budget is spent
  assert.equal(planAfterEnd({ ...base, msSinceOpen: 3_600_000, msSinceLastSilent: Infinity, silentInWindow: 3 }).silent, false);
});

await check("recentSilent keeps only reconnects inside the 5 minute window", () => {
  const now = 1_000_000_000;
  assert.deepEqual(recentSilent([], now), []);
  assert.deepEqual(recentSilent([now - SILENT_WINDOW_MS - 1, now - SILENT_WINDOW_MS, now - SILENT_WINDOW_MS + 1, now], now), [now - SILENT_WINDOW_MS + 1, now]);
});

await check("planAfterEnd: shutdown waits 1-3 s, never less than the backoff, and counts", () => {
  const sd = { reason: "shutdown" as const, msSinceOpen: 3_600_000, msSinceLastSilent: Infinity, attempts: 0 };
  for (const rand of [0, 0.5, 1]) {
    const p = planAfterEnd({ ...sd, rand });
    assert.equal(p.silent, false);
    assert.equal(p.countAttempt, true);
    assert.ok(p.delayMs >= 1_000 && p.delayMs <= 3_000, String(p.delayMs));
  }
  assert.equal(planAfterEnd({ ...sd, rand: 0 }).delayMs, 1_000);
  assert.equal(planAfterEnd({ ...sd, rand: 1 }).delayMs, 3_000);
  assert.equal(planAfterEnd({ ...sd, attempts: 5, rand: 0 }).delayMs, 15_000, "backoff wins when larger");
  assert.equal(planAfterEnd({ ...sd, msSinceOpen: 0, rand: 0 }).delayMs, 1_000, "even a quick shutdown end is not silent");
});

await check("attemptsAfterClose forgets failures only after a healthy stream", () => {
  assert.equal(attemptsAfterClose(7, STABLE_MS), 0);
  assert.equal(attemptsAfterClose(7, 3_600_000), 0);
  assert.equal(attemptsAfterClose(7, STABLE_MS - 1), 7);
  assert.equal(attemptsAfterClose(7, 0), 7);
  assert.equal(attemptsAfterClose(0, 5), 0);
});

await check("cap hint counts only consecutive refused opens", () => {
  let n = 0;
  for (const ev of ["refused", "refused"] as const) n = nextFailedOpens(n, ev);
  assert.equal(n, 2);
  assert.equal(showCapHint(n), false);
  assert.equal(showCapHint(nextFailedOpens(n, "refused")), true, "the third refusal in a row shows it");
  assert.equal(nextFailedOpens(2, "ticket_failed"), 0, "a ticket failure breaks the run");
  assert.equal(nextFailedOpens(2, "opened"), 0, "an open breaks the run");
  assert.equal(nextFailedOpens(0, "refused"), 1);
  // refused, ticket failure, refused, refused: never reaches the hint
  n = 0;
  for (const ev of ["refused", "ticket_failed", "refused", "refused"] as const) n = nextFailedOpens(n, ev);
  assert.equal(showCapHint(n), false);
  assert.equal(FAILED_OPENS_BEFORE_HINT, 3);
  assert.equal(showCapHint(FAILED_OPENS_BEFORE_HINT - 1), false);
  assert.equal(showCapHint(FAILED_OPENS_BEFORE_HINT), true);
  assert.match(TOO_MANY_STREAMS_MESSAGE, /may be at the limit \(5 per account\)/);
  assert.match(TOO_MANY_STREAMS_MESSAGE, /keeps retrying/);
});

await check("hook transitions: initial mode, visibility, restart spacing", () => {
  assert.equal(initialMode("hidden"), "paused");
  assert.equal(initialMode("visible"), "connect");
  const v = { terminal: false, paused: false, connected: true, msAway: 0 };
  assert.equal(visibleAction(v), "none");
  assert.equal(visibleAction({ ...v, msAway: HIDDEN_STALE_MS }), "none");
  assert.equal(visibleAction({ ...v, msAway: HIDDEN_STALE_MS + 1 }), "replace");
  assert.equal(visibleAction({ ...v, connected: false }), "restart");
  assert.equal(visibleAction({ ...v, paused: true }), "restart");
  assert.equal(visibleAction({ ...v, paused: true, msAway: 5 }), "restart");
  assert.equal(visibleAction({ ...v, terminal: true, paused: true, connected: false, msAway: 10 ** 9 }), "none");
  assert.equal(restartDelay(10_000, -Infinity), 0);
  assert.equal(restartDelay(10_000, 9_500), 500);
  assert.equal(restartDelay(10_000, 9_000), 0);
  assert.equal(restartDelay(10_000, 10_000), MIN_RESTART_GAP_MS);
  assert.equal(restartDelay(10_000, 1_000), 0);
});

await check("parseTicketResponse and emptyStateText", () => {
  assert.equal(parseTicketResponse({ ticket: "abc" }), "abc");
  assert.equal(parseTicketResponse({ ticket: "" }), null);
  assert.equal(parseTicketResponse({}), null);
  assert.equal(parseTicketResponse(undefined), null);
  assert.equal(parseTicketResponse(null), null);
  assert.equal(parseTicketResponse({ ticket: 5 }), null);
  assert.equal(parseTicketResponse([]), null);
  assert.equal(parseTicketResponse("abc"), null);
  assert.match(emptyStateText("paused", false), /Paused/);
  assert.match(emptyStateText("reconnecting", false), /Waiting for the log stream/);
  assert.match(emptyStateText("disconnected", false), /No logs to show/);
  assert.match(emptyStateText("live", true), /Logs cleared/);
  assert.match(emptyStateText("live", false), /No logs yet/);
  assert.match(emptyStateText("reconnecting", true), /Waiting/, "status wins over cleared while not live");
});

await check("classifyTicketError: 404 is terminal without Retry, expired session is terminal, the rest keep reconnecting", () => {
  const gone = classifyTicketError(new ApiError(404, "deployment not found"));
  assert.equal(gone.kind, "terminal");
  assert.equal(gone.kind === "terminal" && gone.canRetry, false);
  assert.match(gone.message, /no longer exists/);
  const exp = classifyTicketError(new ApiError(401, "session_expired"));
  assert.equal(exp.kind, "terminal");
  assert.equal(exp.kind === "terminal" && exp.canRetry, true);
  for (const e of [new ApiError(0, "network_error"), new ApiError(500, "internal_error"), new ApiError(503, "refresh_unavailable"), new ApiError(401, "invalid_ticket"), new TypeError("x")]) {
    const o = classifyTicketError(e);
    assert.equal(o.kind, "retry", String(e));
    assert.equal(o.kind === "retry" && o.minDelayMs, 0);
    assert.ok(o.message.length > 0);
    assert.doesNotMatch(o.message, /try again/i, "the panel retries by itself: " + o.message);
  }
  assert.match(classifyTicketError(new ApiError(0, "network_error")).message, /keeps retrying/);
  for (const e of [new ApiError(400, "invalid_request"), new ApiError(503, ""), new ApiError(502, ""), new ApiError(0, ""), new Error("x")]) {
    const o = classifyTicketError(e);
    assert.equal(o.kind, "retry");
    assert.match(o.message, /retry/i, "every retry message says the panel retries: " + o.message);
    assert.doesNotMatch(o.message, /try again/i);
  }
});

await check("classifyTicketError: 429 honours Retry-After, clamped to 1-60 s, 30 s when missing", () => {
  const ms = (e: ApiError) => {
    const o = classifyTicketError(e);
    assert.equal(o.kind, "retry");
    assert.doesNotMatch(o.message, /try again/i);
    return o.kind === "retry" ? o.minDelayMs : -1;
  };
  assert.equal(ms(new ApiError(429, "too_many_requests", 12)), 12_000);
  assert.equal(ms(new ApiError(429, "too_many_requests", 0)), 1_000);
  assert.equal(ms(new ApiError(429, "too_many_requests", 100_000)), 60_000);
  assert.equal(ms(new ApiError(429, "too_many_requests")), 30_000);
  assert.equal(DEFAULT_RATE_LIMIT_WAIT_S, 30);
  assert.match(classifyTicketError(new ApiError(429, "too_many_streams", 5)).message, /limit/);
});

await check("projectNameTooLong counts code points", () => {
  assert.equal(projectNameTooLong("\u{1F600}".repeat(100)), false);
  assert.equal(projectNameTooLong("\u{1F600}".repeat(101)), true);
  assert.equal(projectNameTooLong("x".repeat(100)), false);
  assert.equal(projectNameTooLong("x".repeat(101)), true);
  assert.equal(projectNameTooLong(" ".repeat(5) + "x".repeat(100) + " "), false, "trimmed");
  assert.equal(isValidProjectName("\u{1F600}".repeat(101)), false);
});

await check("429 message names the wait when Retry-After is known", () => {
  const h = (e: ApiError) => humanizeApiError(e);
  assert.equal(h(new ApiError(429, "too_many_requests", 60)), "Too many requests. Wait about 60 seconds and try again.");
  assert.equal(h(new ApiError(429, "too_many_requests", 1)), "Too many requests. Wait about 1 second and try again.");
  assert.equal(h(new ApiError(429, "too_many_requests", 2.2)), "Too many requests. Wait about 3 seconds and try again.");
  assert.equal(h(new ApiError(429, "too_many_requests")), "Too many requests. Wait a moment and try again.");
  assert.equal(h(new ApiError(429, "too_many_requests", 0)), "Too many requests. Wait a moment and try again.");
  assert.equal(rateLimitMessage(Number.NaN), "Too many requests. Wait a moment and try again.");
  assert.match(h(new ApiError(429, "too_many_streams", 9)), /limit/);
});

await check("newServiceOverlaps: only a new service on top of another", () => {
  const s = (id: string, x: number, y: number) => ({ id, positionX: x, positionY: y });
  assert.equal(newServiceOverlaps([s("a", 0, 0), s("b", 0, 0)], "b"), true);
  assert.equal(newServiceOverlaps([s("a", 300, 40), s("b", 0, 0)], "b"), false, "dragged service elsewhere");
  assert.equal(newServiceOverlaps([s("a", 0, 200), s("b", 0, 0)], "b"), false, "same column only");
  assert.equal(newServiceOverlaps([s("a", 200, 0), s("b", 0, 0)], "b"), false, "same row only");
  assert.equal(newServiceOverlaps([s("a", 0, 0)], "a"), false, "alone");
  assert.equal(newServiceOverlaps([s("a", 0, 0), s("b", 0, 0), s("c", 5, 5)], "c"), false, "stacked pair does not involve c");
  assert.equal(newServiceOverlaps([s("a", 0, 0)], "missing"), false);
});

// ---- serviceActions.ts
await check("pickConnectionDeployment: newest deployed and not stopped", () => {
  const d = (status: string, createdAt: string, stoppedAt: string | null = null) => ({ status, stoppedAt, hostPort: null, createdAt });
  assert.equal(pickConnectionDeployment([]), null);
  assert.equal(pickConnectionDeployment([d("failed", "2026-01-03T00:00:00Z")]), null);
  const stopped = d("deployed", "2026-01-02T00:00:00Z", "2026-01-02T01:00:00Z");
  assert.equal(pickConnectionDeployment([d("failed", "2026-01-03T00:00:00Z"), stopped]), null);
  const live = d("deployed", "2026-01-01T00:00:00Z");
  assert.equal(pickConnectionDeployment([d("failed", "2026-01-03T00:00:00Z"), stopped, live]), live);
  const newer = d("deployed", "2026-01-04T00:00:00Z");
  assert.equal(pickConnectionDeployment([live, newer, stopped]), newer, "order of the input does not matter");
  assert.equal(pickConnectionDeployment([d("building", "2026-01-05T00:00:00Z")]), null);
});

await check("buildConnectionUrl percent-encodes the password", () => {
  assert.equal(buildConnectionUrl("redis", "svc-a", 6379, "p@ss:w/rd#?"), "redis://:p%40ss%3Aw%2Frd%23%3F@svc-a:6379");
  assert.equal(buildConnectionUrl("mongodb", "svc-a", 27017, "a b"), "mongodb://admin:a%20b@svc-a:27017/?authSource=admin");
  assert.equal(buildConnectionUrl("redis", "svc-a", 6379, ""), "redis://:@svc-a:6379");
});

await check("buildConnectionRows: masked by default, Copy carries the real value", () => {
  const rows = (t: string, port: number | null, pw: PasswordState, rev: boolean) => buildConnectionRows(t, "abc", port, pw, rev);
  const by = (rs: ConnectionRow[], f: string) => rs.find((r) => r.field === f);
  const ready = { kind: "ready", password: "s3cr@t" } as const;
  const hidden = rows("redis", null, ready, false);
  assert.deepEqual(hidden.map((r) => r.field), ["host", "port", "password", "url", "cli"]);
  assert.equal(by(hidden, "host")?.value, "svc-abc");
  assert.equal(by(hidden, "port")?.value, "6379");
  for (const f of ["password", "url", "cli"]) {
    const r = by(hidden, f)!;
    assert.equal(r.secret, true, f);
    assert.ok(r.value.includes(MASK), f + " shows dots: " + r.value);
    assert.ok(!r.value.includes("s3cr") && !r.value.includes("%40"), f + " leaks nothing: " + r.value);
  }
  assert.equal(by(hidden, "url")?.value, `redis://:${MASK}@svc-abc:6379`);
  assert.equal(by(hidden, "password")?.copyValue, "s3cr@t");
  assert.equal(by(hidden, "url")?.copyValue, "redis://:s3cr%40t@svc-abc:6379");
  assert.equal(by(hidden, "host")?.secret, false);
  const shown = rows("redis", null, ready, true);
  assert.equal(by(shown, "password")?.value, "s3cr@t");
  assert.equal(by(shown, "url")?.value, "redis://:s3cr%40t@svc-abc:6379");
  assert.equal(by(shown, "cli")?.value, "redis-cli -u 'redis://:s3cr%40t@svc-abc:6379'");
  assert.equal(by(hidden, "cli")?.copyValue, "redis-cli -u 'redis://:s3cr%40t@svc-abc:6379'");
  assert.equal(by(hidden, "cli")?.value, `redis-cli -u 'redis://:${MASK}@svc-abc:6379'`);
  const mongo = rows("mongodb", 20001, { kind: "ready", password: "pw" }, true);
  assert.deepEqual(mongo.map((r) => r.field), ["host", "port", "user", "password", "url", "local", "cli"]);
  assert.equal(by(mongo, "port")?.value, "27017");
  assert.equal(by(mongo, "user")?.value, "admin");
  assert.equal(by(mongo, "local")?.value, "mongodb://admin:pw@localhost:20001/?authSource=admin");
  assert.equal(by(mongo, "cli")?.value, "mongosh 'mongodb://admin:pw@localhost:20001/?authSource=admin'", "CLI uses the Local URL when there is one");
  assert.equal(rows("redis", 0, { kind: "ready", password: "x" }, true).some((r) => r.field === "local"), false);
  assert.equal(passwordEnvKey("redis"), "REDIS_PASSWORD");
  assert.equal(passwordEnvKey("mongodb"), "MONGO_INITDB_ROOT_PASSWORD");
});

await check("buildConnectionRows: loading, failed and no stored password never give a URL row", () => {
  for (const pw of [{ kind: "loading" }, { kind: "failed", message: "x" }, { kind: "ready", password: "" }] as PasswordState[]) {
    const rs = buildConnectionRows("mongodb", "abc", 20001, pw, true);
    assert.deepEqual(rs.map((r) => r.field), ["host", "port", "user", "password"], pw.kind);
    assert.equal(rs.find((r) => r.field === "password")?.copyValue, null, pw.kind);
  }
  assert.equal(buildConnectionRows("redis", "a", null, { kind: "loading" }, false).at(-1)?.value, "Loading…");
  assert.equal(buildConnectionRows("redis", "a", null, { kind: "failed", message: "x" }, false).at(-1)?.value, "Could not load");
  assert.equal(buildConnectionRows("redis", "a", null, { kind: "ready", password: "" }, false).at(-1)?.value, NO_PASSWORD_TEXT);
});

await check("shellQuote survives a single quote in the password", () => {
  assert.equal(shellQuote("a b"), "'a b'");
  assert.equal(shellQuote("it's"), "'it'\\''s'");
  const rows = buildConnectionRows("redis", "a", null, { kind: "ready", password: "x'y" }, true);
  assert.equal(rows.find((r) => r.field === "cli")?.value, "redis-cli -u 'redis://:x'\\''y@svc-a:6379'");
});

await check("connectionPanelState: live wins, deploying while something is on its way, else none", () => {
  const d = (status: string, stoppedAt: string | null = null) => ({ status, stoppedAt, hostPort: null, createdAt: "2026-01-01T00:00:00Z" });
  assert.equal(connectionPanelState([]).kind, "none");
  assert.equal(connectionPanelState([d("failed")]).kind, "none");
  assert.equal(connectionPanelState([d("deployed", "2026-01-02T00:00:00Z")]).kind, "none");
  for (const s of ["queued", "building", "deploying"]) assert.equal(connectionPanelState([d(s)]).kind, "deploying", s);
  assert.equal(connectionPanelState([d("building"), d("deployed")]).kind, "live", "an older live container is still live");
  assert.equal(connectionPanelState([d("failed"), d("queued", "2026-01-02T00:00:00Z")]).kind, "none");
});

await check("deploymentsOf keeps only the service's own rows", () => {
  const rows = [{ serviceId: "a", status: "deployed", stoppedAt: null, hostPort: null, createdAt: "x" }, { serviceId: "b", status: "deployed", stoppedAt: null, hostPort: null, createdAt: "x" }];
  assert.equal(deploymentsOf("a", rows).length, 1);
  assert.equal(deploymentsOf("c", rows).length, 0);
  assert.equal(connectionPanelState(deploymentsOf("c", rows)).kind, "none", "the previous service's live row must not drive the new one");
});

await check("copyButtonLabel: Copied! only after a successful write", () => {
  assert.equal(copyButtonLabel(null, "url"), "Copy");
  assert.equal(copyButtonLabel({ field: "url", ok: true }, "url"), "Copied!");
  assert.equal(copyButtonLabel({ field: "url", ok: false }, "url"), "Copy failed");
  assert.equal(copyButtonLabel({ field: "url", ok: true }, "host"), "Copy");
});

await check("delete dialog: managed databases need an explicit choice", () => {
  const open = openDelete();
  for (const t of ["redis", "mongodb"]) {
    const none = beginDelete(open, t, null);
    assert.equal(none.send, false);
    assert.equal(none.state, open);
    const arm = beginDelete(open, t, "purge");
    assert.deepEqual([arm.send, arm.purgeData], [false, false], "the first purge click only arms");
    assert.deepEqual(arm.state, { phase: "confirm", error: null, armed: true, armedAt: 0 });
    const purge = beginDelete(arm.state, t, "purge", { now: ARM_GUARD_MS });
    assert.deepEqual([purge.send, purge.purgeData], [true, true]);
    assert.deepEqual(purge.state, { phase: "pending", purgeData: true });
    const keep = beginDelete(open, t, "keep");
    assert.deepEqual([keep.send, keep.purgeData], [true, false]);
  }
  const app = beginDelete(open, "nodejs", null);
  assert.deepEqual([app.send, app.purgeData], [true, false]);
  assert.equal(beginDelete(open, "nodejs", "purge").purgeData, false, "a stateless service never purges");
});

await check("delete dialog: no second request while pending, cancel is blocked while pending", () => {
  const pending = beginDelete(openDelete(), "redis", "keep").state;
  const again = beginDelete(pending, "redis", "purge");
  assert.equal(again.send, false);
  assert.equal(again.state, pending);
  assert.equal(beginDelete(DELETE_CLOSED, "nodejs", null).send, false);
  assert.equal(cancelDelete(pending), pending);
  assert.deepEqual(cancelDelete(openDelete()), DELETE_CLOSED);
  assert.deepEqual(failDelete(pending, "boom"), { phase: "confirm", error: "boom", armed: false, armedAt: 0 });
  assert.equal(failDelete(DELETE_CLOSED, "boom"), DELETE_CLOSED);
  const retry = beginDelete(failDelete(pending, "boom"), "redis", "keep");
  assert.equal(retry.send, true, "after an error the user can try again");
});

await check("two-step purge: arm, disarm, keep needs no arming, an error disarms", () => {
  const armed = beginDelete(openDelete(), "redis", "purge").state;
  assert.equal(purgeButtonLabel(openDelete()), PURGE_LABEL);
  assert.equal(purgeButtonLabel(armed), PURGE_ARMED_LABEL);
  assert.match(PURGE_ARMED_LABEL, /again.*permanently/);
  assert.deepEqual(disarmPurge(armed), openDelete());
  const unarmed = openDelete();
  assert.equal(disarmPurge(unarmed), unarmed);
  const keep = beginDelete(armed, "redis", "keep");
  assert.deepEqual([keep.send, keep.purgeData], [true, false], "keep while armed deletes without erasing");
  assert.equal(disarmPurge(DELETE_CLOSED), DELETE_CLOSED);
  const pending = beginDelete(armed, "redis", "purge", { now: ARM_GUARD_MS + 1 }).state;
  assert.equal(disarmPurge(pending), pending);
  const failed = failDelete(pending, "boom");
  assert.equal(beginDelete(failed, "redis", "purge", { now: 99999 }).send, false, "after an error the purge must be armed again");
  assert.equal(PURGE_ARM_MS, 5000);
  // stateless services have no purge step
  assert.equal(beginDelete(openDelete(), "nodejs", null).send, true);
});

await check("isDeliberateConfirm / beginDelete: the armed purge ignores fast, repeated and multi-click activation", () => {
  assert.equal(ARM_GUARD_MS, 700);
  assert.equal(isDeliberateConfirm(1000, { now: 1000 + ARM_GUARD_MS }), true);
  assert.equal(isDeliberateConfirm(1000, { now: 1000 + ARM_GUARD_MS - 1 }), false);
  assert.equal(isDeliberateConfirm(1000, { now: 1010 }), false);
  assert.equal(isDeliberateConfirm(1000, { now: 5000, repeat: true }), false);
  assert.equal(isDeliberateConfirm(1000, { now: 5000, detail: 2 }), false);
  assert.equal(isDeliberateConfirm(1000, { now: 5000, detail: 3 }), false);
  assert.equal(isDeliberateConfirm(1000, { now: 5000, detail: 1 }), true);
  assert.equal(isDeliberateConfirm(1000, { now: 5000, detail: 0, repeat: false }), true);
  const armed = beginDelete(openDelete(), "redis", "purge", { now: 1000 });
  assert.equal(armed.send, false);
  assert.equal(beginDelete(armed.state, "redis", "purge", { now: 1100 }).send, false, "a double-click is too fast");
  assert.equal(beginDelete(armed.state, "redis", "purge", { now: 9000, repeat: true }).send, false, "key repeat");
  assert.equal(beginDelete(armed.state, "redis", "purge", { now: 9000, detail: 2 }).send, false, "second click of a double-click");
  assert.equal(beginDelete(armed.state, "redis", "purge", { now: 1100 }).state, armed.state, "an ignored click keeps the armed state");
  const go = beginDelete(armed.state, "redis", "purge", { now: 1000 + ARM_GUARD_MS });
  assert.deepEqual([go.send, go.purgeData], [true, true]);
  assert.equal(beginDelete(armed.state, "redis", "keep", { now: 1001 }).send, true, "keep is not guarded");
});

await check("safeEncodeURIComponent replaces lone surrogates instead of throwing", () => {
  assert.throws(() => encodeURIComponent("a\uD800b"), URIError);
  assert.equal(safeEncodeURIComponent("a\uD800b"), "a%EF%BF%BDb");
  assert.equal(safeEncodeURIComponent("a\uDC00"), "a%EF%BF%BD");
  assert.equal(safeEncodeURIComponent("\uD83D\uDE00"), "%F0%9F%98%80", "a valid pair is kept");
  assert.equal(safeEncodeURIComponent("p@ss/1"), "p%40ss%2F1");
  assert.equal(safeEncodeURIComponent("\uD83D"), "%EF%BF%BD");
  const rows = buildConnectionRows("redis", "a", null, { kind: "ready", password: "x\uD800y" }, true);
  assert.equal(rows.find((r) => r.field === "url")?.copyValue, "redis://:x%EF%BF%BDy@svc-a:6379");
  assert.equal(deletePath("a\uD800", false), "/services/a%EF%BF%BD?purgeData=false");
});

await check("timeout error: its own message, not the network one", () => {
  const m = humanizeApiError(new ApiError(0, "timeout"));
  assert.equal(m, "The server is taking too long to answer. Try again.");
  assert.notEqual(m, humanizeApiError(new ApiError(0, "network_error")));
  const o = classifyDeleteError(new ApiError(0, "timeout"));
  assert.deepEqual(o, { kind: "error", message: m });
});

await check("nextFocusIndex: wraps at both ends, enters from outside", () => {
  assert.equal(nextFocusIndex(0, 3, false), 1);
  assert.equal(nextFocusIndex(2, 3, false), 0);
  assert.equal(nextFocusIndex(0, 3, true), 2);
  assert.equal(nextFocusIndex(2, 3, true), 1);
  assert.equal(nextFocusIndex(-1, 3, false), 0);
  assert.equal(nextFocusIndex(-1, 3, true), 2);
  assert.equal(nextFocusIndex(5, 3, false), 0);
  assert.equal(nextFocusIndex(0, 1, false), 0);
  assert.equal(nextFocusIndex(0, 1, true), 0);
  assert.equal(nextFocusIndex(0, 0, false), -1);
});

await check("classifyDeleteError: 404 is success, the rest is readable", () => {
  assert.deepEqual(classifyDeleteError(new ApiError(404, "service_not_found")), { kind: "done" });
  assert.deepEqual(classifyDeleteError(new ApiError(404, "")), { kind: "done" });
  const msg = (e: unknown) => {
    const o = classifyDeleteError(e);
    assert.equal(o.kind, "error");
    return o.kind === "error" ? o.message : "";
  };
  assert.match(msg(new ApiError(400, "purge_choice_required")), /delete the data or keep it/);
  assert.match(msg(new ApiError(400, "invalid_purge_data")), /not valid/);
  assert.equal(msg(new ApiError(401, "session_expired")), SESSION_EXPIRED_MESSAGE);
  assert.match(msg(new ApiError(500, "internal_error")), /went wrong/);
  assert.match(msg(new ApiError(0, "network_error")), /reach the server/);
  assert.match(msg(new TypeError("x")), /reach the server/);
});

await check("deletePath sends a literal boolean and encodes the id", () => {
  assert.equal(deletePath("abc", true), "/services/abc?purgeData=true");
  assert.equal(deletePath("abc", false), "/services/abc?purgeData=false");
  assert.equal(deletePath("a/b?c", false), "/services/a%2Fb%3Fc?purgeData=false");
  assert.match(DELETING_NOTICE, /^Deleting/);
  assert.doesNotMatch(DELETING_NOTICE, /Deleted/);
});

await check("shouldClosePanel and deleteFailedNotice", () => {
  assert.equal(shouldClosePanel("a", "a"), true);
  assert.equal(shouldClosePanel("b", "a"), false, "another service's panel stays open");
  assert.equal(shouldClosePanel(undefined, "a"), false);
  assert.equal(deleteFailedNotice("db", "Cannot reach the server."), "Could not delete “db”. Cannot reach the server.");
  assert.equal(deleteFailedNotice("db", "Something odd"), "Could not delete “db”. Something odd.");
});

await check("connectedServiceNames: both directions, once each, never itself", () => {
  const services = [{ id: "a", name: "A" }, { id: "b", name: "B" }, { id: "c", name: "C" }, { id: "d", name: "D" }];
  const conns = [
    { sourceId: "a", targetId: "b" }, { sourceId: "c", targetId: "a" }, { sourceId: "a", targetId: "b" },
    { sourceId: "c", targetId: "d" }, { sourceId: "a", targetId: "a" },
  ];
  assert.deepEqual(connectedServiceNames("a", services, conns), ["B", "C"]);
  assert.deepEqual(connectedServiceNames("d", services, conns), ["C"]);
  assert.deepEqual(connectedServiceNames("b", services, []), []);
});

await check("deploymentBadge: a stopped deployed row reads Stopped, not a green deployed", () => {
  assert.deepEqual(deploymentBadge("deployed", null), { label: "deployed", tone: "success" });
  assert.deepEqual(deploymentBadge("deployed", undefined), { label: "deployed", tone: "success" });
  assert.deepEqual(deploymentBadge("deployed", "2026-10-13T10:00:00Z"), { label: "Stopped", tone: "neutral" });
  assert.deepEqual(deploymentBadge("failed", "2026-10-13T10:00:00Z"), { label: "failed", tone: "failed" });
  for (const st of ["queued", "building", "deploying"]) assert.deepEqual(deploymentBadge(st, null), { label: st, tone: "pending" });
  assert.deepEqual(deploymentBadge("weird", null), { label: "weird", tone: "neutral" });
});

await check("repoUrlProblem rejects ssh/git@/file/http and accepts https", () => {
  for (const ok of [
    "https://github.com/user/repo", "https://github.com/user/repo.git", "https://gitlab.com/group/sub/repo",
    "https://git.example.org:8443/x/y.git", "  https://bitbucket.org/a/b  ", "HTTPS://GitHub.com/a/b", "",
  ]) assert.equal(repoUrlProblem(ok), null, ok);
  for (const ssh of ["git@github.com:user/repo.git", "ssh://git@github.com/user/repo.git", "git://github.com/user/repo", "deploy@host.example.com:team/repo"]) {
    assert.equal(repoUrlProblem(ssh), REPO_URL_SSH_MESSAGE, ssh);
  }
  for (const f of ["file:///srv/repo", "/srv/repo", "./repo", "../repo", "~/repo", "C:\\repo", "FILE://x"]) {
    assert.equal(repoUrlProblem(f), REPO_URL_FILE_MESSAGE, f);
  }
  for (const bad of ["http://github.com/a/b", "github.com/a/b", "https://", "https://git hub.com/a", "ftp://x.com/a", "not a url"]) {
    assert.notEqual(repoUrlProblem(bad), null, bad);
  }
  assert.equal(repoUrlProblem("http://github.com/a/b"), REPO_URL_HTTP_MESSAGE);
  assert.equal(REPO_URL_HINT, "Public https:// URLs only");
});

console.log(`ok: ${passed} checks passed`);

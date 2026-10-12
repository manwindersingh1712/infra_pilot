// Run: node scripts/check-api-logic.ts   (Node 22.18+; no test framework needed)
import assert from "node:assert/strict";
import { refreshSession, withStorageLock, type RefreshCallResult, type RefreshDeps } from "../src/lib/refresh.ts";
import { isValidProjectName } from "../src/lib/projectName.ts";
import { ApiError, humanizeApiError, parseErrorCode, SESSION_EXPIRED_MESSAGE } from "../src/lib/errors.ts";

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
    "purge_choice_required", "invalid_purge_data",
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
  assert.match(humanizeApiError(new ApiError(404, "")), /not found/i);
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

console.log(`ok: ${passed} checks passed`);

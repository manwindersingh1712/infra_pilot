// Pure refresh logic (no DOM globals, no import.meta) so it can be run by
// node in scripts/check-api-logic.ts. api.ts wires it to localStorage, fetch
// and Web Locks.

export interface RefreshCallResult {
  status: number;
  token?: string;
  refreshToken?: string;
}

export interface RefreshDeps {
  readRefresh(): string | null;
  readAccess(): string;
  // POST /auth/refresh. Rejects on a network error.
  call(refreshToken: string): Promise<RefreshCallResult>;
  store(token: string, refreshToken: string): void;
}

// refreshed:   a usable access token is stored, retry the request.
// expired:     the server said the refresh token is dead; sign out.
// unavailable: 429, 5xx, a network error or an odd reply. Keep the session
//              and tell the user to try again. NEVER sign out on these.
export type RefreshOutcome = "refreshed" | "expired" | "unavailable";

async function attempt(deps: RefreshDeps, refreshToken: string): Promise<RefreshOutcome | "rejected"> {
  let res: RefreshCallResult;
  try {
    res = await deps.call(refreshToken);
  } catch {
    return "unavailable";
  }
  if (res.status === 200) {
    if (!res.token || !res.refreshToken) return "unavailable";
    deps.store(res.token, res.refreshToken);
    return "refreshed";
  }
  if (res.status === 401) return "rejected";
  return "unavailable";
}

// refreshSession runs while the caller holds the cross-tab lock.
// staleAccess is the access token the failed request was sent with.
export async function refreshSession(deps: RefreshDeps, staleAccess: string): Promise<RefreshOutcome> {
  // Another tab (or an earlier caller) already refreshed while we waited.
  const nowAccess = deps.readAccess();
  if (nowAccess !== "" && nowAccess !== staleAccess) return "refreshed";

  const used = deps.readRefresh();
  if (!used) return "expired";

  const first = await attempt(deps, used);
  if (first !== "rejected") return first;

  // 401: a tab that was not holding our lock may have rotated the token
  // between our read and the call. Re-read; retry once if it changed.
  const reread = deps.readRefresh();
  if (reread && reread !== used) {
    const second = await attempt(deps, reread);
    return second === "rejected" ? "expired" : second;
  }
  return "expired";
}

// ---- storage-based lock, used when Web Locks are unavailable

export interface LockStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

export interface StorageLockOptions {
  key?: string;
  ttlMs?: number; // a holder older than this is presumed dead
  waitMs?: number; // give up waiting after this and run anyway
  settleMs?: number; // delay between writing the claim and re-reading it
  pollMs?: number;
  now?: () => number;
  id?: string;
}

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

// withStorageLock is a best-effort mutex across tabs: write a claim, wait a
// moment, and hold the lock only if the claim survived. It cannot be perfect
// (localStorage has no compare-and-set), which is why refreshSession also
// re-reads the refresh token on a 401. If the wait runs out, fn runs anyway.
export async function withStorageLock<T>(
  storage: LockStorage,
  fn: () => Promise<T>,
  opts: StorageLockOptions = {}
): Promise<T> {
  const key = opts.key ?? "cp_refresh_lock";
  const ttl = opts.ttlMs ?? 10_000;
  const wait = opts.waitMs ?? 15_000;
  const settle = opts.settleMs ?? 25;
  const poll = opts.pollMs ?? 50;
  const now = opts.now ?? Date.now;
  const id = opts.id ?? Math.random().toString(36).slice(2);

  const claim = () => `${id}|${now() + ttl}`;
  const owner = (v: string | null) => (v ? v.split("|")[0] : "");
  const expiry = (v: string | null) => Number(v?.split("|")[1] ?? 0);

  const deadline = now() + wait;
  let held = false;
  while (now() < deadline) {
    const cur = storage.getItem(key);
    if (cur === null || expiry(cur) <= now()) {
      storage.setItem(key, claim());
      await sleep(settle);
      if (owner(storage.getItem(key)) === id) {
        held = true;
        break;
      }
    }
    await sleep(poll);
  }
  try {
    return await fn();
  } finally {
    if (held && owner(storage.getItem(key)) === id) storage.removeItem(key);
  }
}

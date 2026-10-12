import { ApiError, parseErrorCode, SESSION_EXPIRED_MESSAGE } from "./errors";
import { refreshSession, withStorageLock, type RefreshDeps, type RefreshOutcome } from "./refresh";

export { ApiError, humanizeApiError, SESSION_EXPIRED_MESSAGE } from "./errors";

export const API_BASE: string = import.meta.env.VITE_API_BASE ?? "http://localhost:8080";

export const ACCESS_KEY = "cp_token";
export const REFRESH_KEY = "cp_refresh";

export function getToken(): string {
  return localStorage.getItem(ACCESS_KEY) ?? "";
}

export function setSession(token: string, refreshToken: string): void {
  localStorage.setItem(ACCESS_KEY, token);
  localStorage.setItem(REFRESH_KEY, refreshToken);
}

export function clearSession(): void {
  localStorage.removeItem(ACCESS_KEY);
  localStorage.removeItem(REFRESH_KEY);
}

const NOTICE_KEY = "cp_notice";

// readSessionNotice returns the message left by a forced sign-out, for the
// login page to show. It has no side effects (safe in a state initializer);
// the page calls clearSessionNotice() from an effect so it shows only once.
export function readSessionNotice(): string | null {
  try {
    return sessionStorage.getItem(NOTICE_KEY);
  } catch {
    return null;
  }
}

export function clearSessionNotice(): void {
  try {
    sessionStorage.removeItem(NOTICE_KEY);
  } catch {
    // storage blocked: nothing to clear
  }
}

const deps: RefreshDeps = {
  readRefresh: () => localStorage.getItem(REFRESH_KEY),
  readAccess: getToken,
  call: async (refreshToken) => {
    const res = await fetch(`${API_BASE}/auth/refresh`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ refreshToken }),
    });
    if (!res.ok) return { status: res.status };
    try {
      const data = (await res.json()) as { token?: string; refreshToken?: string };
      return { status: res.status, token: data.token, refreshToken: data.refreshToken };
    } catch {
      return { status: res.status };
    }
  },
  store: setSession,
};

// One refresh at a time per page; across tabs, Web Locks (or a storage lock
// where they are missing) serialise the refresh. Whoever gets the lock second
// sees the new access token and does not call the server.
let inflight: Promise<RefreshOutcome> | null = null;

function refreshOnce(staleAccess: string): Promise<RefreshOutcome> {
  inflight ??= (async () => {
    try {
      const run = () => refreshSession(deps, staleAccess);
      if (typeof navigator !== "undefined" && navigator.locks) {
        // request() can reject (SecurityError in sandboxed or opaque-origin
        // contexts). `started` keeps a failure inside run() from running it
        // a second time under the storage lock.
        let started = false;
        try {
          return await navigator.locks.request("cp-refresh", () => {
            started = true;
            return run();
          });
        } catch (e) {
          if (started) throw e;
        }
      }
      return await withStorageLock(localStorage, run);
    } finally {
      inflight = null;
    }
  })();
  return inflight;
}

// Forced sign-out: tell the login page why, then leave.
function expireSession(): never {
  clearSession();
  try {
    sessionStorage.setItem(NOTICE_KEY, SESSION_EXPIRED_MESSAGE);
  } catch {
    // private mode: the redirect alone is enough
  }
  if (window.location.pathname !== "/login") window.location.assign("/login");
  throw new ApiError(401, "session_expired");
}

async function send(path: string, opts: RequestInit): Promise<Response> {
  const headers = new Headers(opts.headers);
  const token = getToken();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  if (opts.body != null && !headers.has("Content-Type")) headers.set("Content-Type", "application/json");
  try {
    return await fetch(`${API_BASE}${path}`, { ...opts, headers });
  } catch {
    throw new ApiError(0, "network_error");
  }
}

export async function apiFetch<T>(path: string, opts: RequestInit = {}): Promise<T> {
  const sentWith = getToken();
  let res = await send(path, opts);
  if (res.status === 401 && !path.startsWith("/auth/") && (getToken() || localStorage.getItem(REFRESH_KEY))) {
    const outcome = await refreshOnce(sentWith);
    if (outcome === "expired") expireSession();
    // A 429, 5xx or network error while refreshing keeps the session.
    if (outcome === "unavailable") throw new ApiError(503, "refresh_unavailable");
    res = await send(path, opts);
    if (res.status === 401) expireSession();
  }
  if (!res.ok) throw new ApiError(res.status, parseErrorCode(await res.text()));
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

// logout revokes the refresh token on the server and clears local tokens.
export async function logout(): Promise<void> {
  const refreshToken = localStorage.getItem(REFRESH_KEY);
  clearSession();
  if (!refreshToken) return;
  try {
    await fetch(`${API_BASE}/auth/logout`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ refreshToken }),
    });
  } catch {
    // Offline: the token still expires on its own.
  }
}

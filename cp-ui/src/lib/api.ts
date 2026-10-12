import { ApiError, parseErrorCode } from "./errors";

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
  const res = await send(path, opts);
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

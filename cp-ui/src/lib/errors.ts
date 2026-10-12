// Pure helpers: no DOM, no localStorage, no import.meta, so they can be run
// directly by node (see scripts/check-api-logic.ts).

// ApiError carries the HTTP status and the machine code the API sends as
// {"error": "<code>"}. status 0 means the request never got a response.
export class ApiError extends Error {
  status: number;
  code: string;

  constructor(status: number, code: string) {
    super(code ? `${status}: ${code}` : String(status));
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

// parseErrorCode extracts the "error" string from a response body. Anything
// that is not {"error": "<string>"} yields "".
export function parseErrorCode(body: string): string {
  try {
    const v: unknown = JSON.parse(body);
    if (v && typeof v === "object" && "error" in v && typeof v.error === "string") {
      return v.error;
    }
  } catch {
    // not JSON (proxy error page, empty body)
  }
  return "";
}

export const SESSION_EXPIRED_MESSAGE = "Your session expired, please sign in again.";

// Messages by server code. The Go API mostly sends snake_case codes but still
// sends a few sentences ("Project not found"); both are keyed here.
const CODE_MESSAGES: Record<string, string> = {
  invalid_request: "That request was not valid. Check what you entered and try again.",
  password_too_short: "Password must be at least 8 characters.",
  password_too_long: "Password must be at most 72 bytes (72 characters if they are all plain letters and digits).",
  invalid_repo_url: "Enter a public https:// repository URL. SSH and file URLs are not supported.",
  service_not_found: "That service no longer exists.",
  invalid_env_var:
    "Invalid environment variable. Names use letters, digits, _ . - ; values are a single line of at most 32 KiB (use base64 for multi-line values such as PEM keys).",
  reserved_env_var: "That variable name is reserved by the platform. Choose another name.",
  request_too_large: "That request was too large.",
  too_many_requests: "Too many requests. Wait a moment and try again.",
  too_many_streams: "Too many log streams are open. Close another logs panel and try again.",
  invalid_ticket: "The log stream link expired. Reopen the logs.",
  purge_choice_required: "Choose whether to keep or delete the service's data before deleting it.",
  invalid_purge_data: "That data-deletion choice was not valid. Reload the page and try again.",
  invalid_credentials: "Invalid email or password.",
  email_already_exists: "That email is already registered.",
  invalid_refresh_token: SESSION_EXPIRED_MESSAGE,
  unauthorized: SESSION_EXPIRED_MESSAGE,
  session_expired: SESSION_EXPIRED_MESSAGE,
  refresh_unavailable: "Could not refresh your session right now. Try again in a moment.",
  network_error: "Cannot reach the server. Check your connection and try again.",
  unavailable: "The service is temporarily unavailable. Try again in a moment.",
  internal_error: "Something went wrong on our side. Try again in a moment.",
  not_found: "Not found.",
  "Project not found": "That project no longer exists.",
  "Service not found": "That service no longer exists.",
  "Connection not found": "That connection no longer exists.",
  "deployment not found": "That deployment no longer exists.",
};

const STATUS_MESSAGES: Record<number, string> = {
  0: CODE_MESSAGES.network_error,
  400: CODE_MESSAGES.invalid_request,
  401: SESSION_EXPIRED_MESSAGE,
  404: "Not found.",
  413: CODE_MESSAGES.request_too_large,
  429: CODE_MESSAGES.too_many_requests,
  500: CODE_MESSAGES.internal_error,
  503: CODE_MESSAGES.unavailable,
};

// humanizeApiError turns any thrown value into text a user can act on.
// `overrides` lets a call site give a code a more specific message (for
// example invalid_request in the create-project dialog).
export function humanizeApiError(err: unknown, overrides: Record<string, string> = {}): string {
  if (err instanceof ApiError) {
    if (err.code && Object.hasOwn(overrides, err.code)) return overrides[err.code];
    if (err.code && Object.hasOwn(CODE_MESSAGES, err.code)) return CODE_MESSAGES[err.code];
    if (Object.hasOwn(STATUS_MESSAGES, err.status)) return STATUS_MESSAGES[err.status];
    // A sentence the server wrote for people ("Connection already exists").
    if (err.code.includes(" ")) return err.code;
    if (err.status >= 500) return CODE_MESSAGES.internal_error;
    return "Something went wrong. Try again.";
  }
  if (err instanceof TypeError) return CODE_MESSAGES.network_error;
  return "Something went wrong. Try again.";
}

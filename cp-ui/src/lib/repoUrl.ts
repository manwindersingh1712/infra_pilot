// Client-side check of the repo URL field. The server is the authority (https
// only, with a host); this only turns the common mistakes into a human message
// before a round trip. No DOM, no import.meta: node runs it in check:logic.

export const REPO_URL_HINT = "Public https:// URLs only";

export const REPO_URL_SSH_MESSAGE =
  "SSH and git@ addresses are not supported. Use the public https:// address of the repository, e.g. https://github.com/user/repo.";
export const REPO_URL_FILE_MESSAGE = "Local paths and file:// URLs are not supported. Use a public https:// address.";
export const REPO_URL_HTTP_MESSAGE = "Use an https:// address; plain http:// is not supported.";
export const REPO_URL_GENERIC_MESSAGE =
  "Enter a public https:// address, e.g. https://github.com/user/repo.";

// repoUrlProblem returns a message for a URL that cannot work, or null when
// the value is empty (the required field handles that) or looks like a valid
// https URL. It never rejects a well-formed https URL: a .git suffix, a
// non-GitHub host, a port or a path are all fine.
export function repoUrlProblem(raw: string): string | null {
  const s = raw.trim();
  if (s === "") return null;
  const lower = s.toLowerCase();
  if (lower.startsWith("ssh://") || lower.startsWith("git://") || lower.startsWith("git+ssh://") || /^[\w.-]+@[\w.-]+:/.test(s)) {
    return REPO_URL_SSH_MESSAGE;
  }
  if (lower.startsWith("file:") || s.startsWith("/") || s.startsWith("./") || s.startsWith("../") || s.startsWith("~") || /^[a-z]:[\\/]/i.test(s)) {
    return REPO_URL_FILE_MESSAGE;
  }
  if (lower.startsWith("http://")) return REPO_URL_HTTP_MESSAGE;
  if (/\s/.test(s)) return REPO_URL_GENERIC_MESSAGE;
  let u: URL;
  try {
    u = new URL(s);
  } catch {
    return REPO_URL_GENERIC_MESSAGE;
  }
  if (u.protocol !== "https:" || u.hostname === "") return REPO_URL_GENERIC_MESSAGE;
  return null;
}

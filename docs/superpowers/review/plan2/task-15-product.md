# Plan 2 Task 15: Product review (commits 17d7680..4ffffde)

Reviewer: Product. Method: code reading of `cp-ui/src` (lib/errors.ts, api.ts, refresh.ts, projectName.ts, App.tsx, ProjectsPage, ProjectCanvasPage, useCanvas, CreateServiceModal, ServiceDetailsPanel, Toast) cross-checked against every error code the Go API emits (`internal/api/*.go`), plus `node --experimental-strip-types cp-ui/scripts/check-api-logic.ts` (27 checks pass). No browser tool was available in this session, so nothing was run in a UI and no throwaway stack was started (nothing to clean up). Anything below that depends on rendering is marked "not run".

Note: the working tree has an uncommitted edit to `cp-ui/src/lib/refresh.ts` (`while (true)` with `deadline` unused, so `tsc -b` fails with TS6133). It is not in the reviewed commits; whoever owns it must not commit it as is.

## Binding requirement check

| # | Requirement (backlog / Product sections / plan amendments) | Status |
|---|---|---|
| 1 | Single-flight refresh across tabs, re-read `cp_refresh` on 401 and retry once, never sign out on 429/5xx/network | Met (`refresh.ts`, `api.ts` `refreshOnce`; unavailable maps to 503 `refresh_unavailable`, session kept) |
| 2 | 2 s deployment poll and all callers use the shared client; "session expired" before redirect | Met (all six files moved; `expireSession` stores a notice that the login page shows once) |
| 3 | One `humanizeApiError`, all listed codes | Met (every listed code present; also `reserved_env_var`, `invalid_credentials`, `email_already_exists`; sentences like "Project not found" tolerated). Wording notes below |
| 4 | Create-project: maxLength 100, "2-100 characters", Create disabled until trimmed length >= 2, send trimmed, errors inside the modal | Met (code-point counting matches the Go rune count) |
| 4b | Signup shows `password_too_short`/`_too_long` (8-72 byte hint) | Met (placeholder "8 to 72 characters", client-side byte check) |
| 5 | Canvas on shared client; toast on connect, edge delete, position-save failure; `label` nullable | Met. Bonus: failed edge delete is restored, 404 on edge delete treated as success |
| 6 | Env editor: lazy `?reveal=true`, hints, trimmed key, "redeploy to apply" | Met |
| 7 | Pure functions + script, no test framework added | Met |
| - | Optional "N services" line | Consciously removed (the count was always 0) |
| - | Optional drop `commitSha: "HEAD"` | Not done (still sent), harmless |
| - | Create modal: https:// only, SSH/file unsupported | Partly met: placeholder is https, the server error is mapped, but no pre-validation or hint text (PROD-3) |
| - | Delete-service confirm dialog, purgeData checkbox | Deferred to Tasks 12/17 as agreed (no delete UI exists yet) |
| - | Stopped-deployment status, Connection tab filter, `stoppedAt` type | Deferred to Task 17 |

## Findings

[PROD-1] severity: medium
where: CreateServiceModal.tsx `handleSubmit` (about lines 248-270)
problem: The modal creates the service and then deploys it in one try block. If the deploy call fails (503, 429, 401 then retry), the service already exists but the modal stays open with an error, and the canvas is not refreshed. Pressing Create again makes a second service with the same name. With the new messages the user also reads e.g. "Too many requests" with no hint that the service was created.
evidence: read from code; a 429 from the deploy rate limiter or a refresh outage between the two calls triggers it. Not run.
fix: split the two calls. After a successful create, call `onCreated()` and close; if the deploy fails, show a toast "Service created, but starting the deployment failed: <message>. Open the service and press Deploy." This is pre-existing logic, but Task 15 made the failure visible and the limiters (Task 9/10) make it likelier, so fix before cutover or record it in the backlog with a date.

[PROD-2] severity: low
where: errors.ts `password_too_long`
problem: "Password must be at most 72 bytes (72 characters if they are all plain letters and digits)." is clumsy for a solo developer. The signup placeholder says "8 to 72 characters", which is wrong for non-ASCII passwords, but the message corrects it only after submit.
fix: "Password is too long (the limit is 72 bytes; accented or non-Latin characters take more than one byte)." and change the placeholder to "8 to 72 characters (bytes)", or just "At least 8 characters".

[PROD-3] severity: low
where: CreateServiceModal.tsx repo URL input (line ~702)
problem: Requirement said the modal pre-validates the https:// prefix and hints that SSH URLs are unsupported. Only the placeholder shows https. A user pasting `git@github.com:me/app.git` finds out after submit (the mapped message "Enter a public https:// repository URL. SSH and file URLs are not supported." is good).
fix: helper text under the field and disable Create unless the URL starts with `https://` (or `http://`, if the API still accepts it: the message says https only, the old Product fix suggested "https:// or http://"; make message and validation agree with `invalid_repo_url` in Go).

[PROD-4] severity: low
where: errors.ts fallbacks
problem: Some phrases are vague or inconsistent. (a) Any 404 not in the map reads "Not found." (b) 400 `invalid_request` everywhere except the create-project dialog reads "That request was not valid. Check what you entered and try again." (signup with a name over 100 characters or an email over 254 bytes lands here; the inputs have no maxLength). (c) Go sentences pass through verbatim, including "repoUrl required for docker/nodejs/nextjs/react services" (a field name, not plain words) and "Cannot connect service to itself". (d) 405 `method_not_allowed` gives "Something went wrong. Try again."
fix: map the `repoUrl required...` sentence to "Enter the repository URL for this service type."; add `maxLength` on the signup name/email fields; make 404 "That item no longer exists. Reload the page." Cheap, can wait.

[PROD-5] severity: low
where: ProjectCanvasPage.tsx toasts
problem: Toast text is `"Could not connect the services. " + humanizeApiError(err)`. For server sentences without a final period this reads "Could not connect the services. Connection already exists". For a failed position save the node stays where the user dropped it until the next reload, then jumps back. The toast does not say that.
fix: append a period when the humanized text has none; for position save say "Could not save the new position; it will reset when you reload."

[PROD-6] severity: low
where: App.tsx LoginPage (`takeSessionNotice`)
problem: The "Your session expired, please sign in again." notice reuses the red error box, so a normal expiry looks like a failure. Content and one-time behavior are right.
fix: neutral/amber info style for notices (optional).

[PROD-7] severity: low (carry-over, not Task 15)
where: ProjectsPage.tsx user dropdown
problem: Hard-coded "user@example.com", "Free Plan" and a Settings button that does nothing. With real accounts this shows the wrong email. Login/register responses already include `user`, but the UI discards it.
fix: store `user.email` at login and show it, or hide the line; backlog for the SaaS work.

## Flow-by-flow notes (code reading, not run)

- Login: wrong password gives "Invalid email or password." (the `/auth/` path is excluded from the refresh retry, so a bad login cannot trigger a sign-out loop). Rate limit gives "Too many requests. Wait a moment and try again."
- Signup: 8-72 byte check client side with matching server codes; `email_already_exists` has a message.
- Logout: tokens cleared first, then best-effort `/auth/logout`; works offline. Lands on the landing page.
- Cutover: a browser with only an old `cp_token` (no `cp_refresh`) gets a 401, `refreshSession` returns "expired", the user lands on /login with the session-expired notice. Understandable; add to the release note.
- Session expiry mid-use (2 s poll, canvas calls): single refresh, then one retry; a second 401 signs out with the notice. 429/5xx/offline during refresh show "Could not refresh your session right now. Try again in a moment." and keep the session.
- Create project: Create disabled until the trimmed length is 2-100, trimmed value sent, error shown in the dialog with the typed name kept, dialog reopens with the error cleared.
- Env editor: first eye click fetches revealed values once and caches them (reset on every reload of the list); a failed reveal shows the error and keeps the dots; hints sit above the form; key trimmed and URL-encoded on delete.
- "N services" line is gone from project cards; nothing else referenced it.

## Release-note lines users should see
- You will be signed out once when this version is deployed; sign in again. Sessions now renew automatically, so you stay signed in across the 15-minute access-token limit.
- Passwords must be 8-72 bytes; project names 2-100 characters.
- Environment variable values are hidden until you click the eye icon; changes apply on the next deploy.
- Repository URLs must be public https:// URLs; SSH and file URLs are not supported.

RESULT: APPROVE (no critical or high findings; PROD-1 should be fixed or backlogged with a decision before cutover)

# Plan 2 release note: the API is now written in Go

Cutover steps: `docs/superpowers/runbook/api-cutover.md`. Product walkthrough on a throwaway stack: `docs/superpowers/review/plan2/plan-end-product.md`.

## RELEASE NOTE (for users)

The server behind the Infra Pilot web app has been rewritten in Go. Projects, services, deployments and the canvas work as before. A few things look different, and most of the changes are about keeping your secrets safe.

**You will be signed out once.** When this version goes live, sign in again with your email and password. After that you stay signed in: the app renews your session by itself every few minutes (a session lasts 15 minutes at a time, and each renewal extends it by another 30 days of inactivity). You are asked to sign in again only if you do not use the app for 30 days, if you press Sign out, or if your session cannot be renewed; the login page then says "Your session expired, please sign in again." Sign out ends the session in this browser only.

**Passwords and names**
- A password must be 8 to 72 bytes. Plain letters and digits are one byte each; accented or non-Latin characters take two or more, so a very long passphrase in another alphabet can be too long. The app tells you which limit you hit.
- A project name must be 2 to 100 characters (an emoji counts as one character). A name or email that is too long is refused with a message instead of being cut. The Name field at sign-up is optional (up to 100 characters); an email can be up to 254 characters.
- Signing in or up is limited to a short burst of attempts: after 5 quick wrong tries you see "Too many requests. Wait a moment and try again." Waiting a few seconds frees one more attempt.
- Repository URLs must be public `https://` URLs. SSH and `file:` URLs are not supported.

**Environment variables are hidden**
- Values show as dots. Click the eye icon on one row to see that value; hiding it again removes it from the page. Values are stored encrypted on the server.
- Names use letters, digits, `_`, `.` and `-`. A value is a single line of up to 32 KiB (use base64 for multi-line values such as PEM keys). Some names are reserved by the platform and are refused.
- Changes apply on the next deployment: redeploy to apply them.

**Managed databases (MongoDB, Redis): a Connection tab with real details**
- Open the database on the canvas and choose Connection. You see Host (`svc-<serviceId>`), Port, the generated Password, a connection URL and a command-line (CLI) line. Other services in the same project reach the database at that host. A Mongo database also shows its user (`admin`).
- The password, the URL and the CLI line are masked. "Reveal secrets" shows them, "Hide secrets" hides them again. Copy always copies the real value, even while it is masked.
- Databases are no longer reachable from outside the server. Their ports are not published to the internet. Only when the server runs in development mode does the tab also show a Local URL and CLI line on `localhost`.
- A database created before this version has no generated password yet, and the tab says "Redeploy to generate a password". Read the Plan 1 note first: redeploying an old MongoDB service starts it empty.
- While a deployment is still running the tab says "Deploying…", and for a service that was never deployed "Not running."

**Deleting a service**
- Service panel, then Delete service. The service disappears from the canvas at once; its containers and URL are removed within a minute (a build that is still running can take up to 3 minutes to stop). The server keeps retrying until the teardown succeeds.
- Deleting is permanent. A MongoDB or Redis service asks what to do with its data: "Delete database and its data" needs a second click ("Click again to erase the data permanently"; it goes back to the first state after 5 seconds), or "Delete service, keep data volume on disk". A kept volume stays on the server and cannot be re-attached from the app; creating the service again gives a new, empty database.
- Services that were connected to a deleted one are not changed, so update their settings yourself.

**Live logs**
- The Logs tab now streams over Server-Sent Events instead of Socket.io. The badge shows Live (green), Reconnecting (amber), Paused (grey, the tab is in the background) or Disconnected (red). The panel never gives up on its own while the deployment exists; Retry reconnects at once, and it recovers by itself after an API restart or a network drop.
- The stream renews itself every hour and after sleep without a visible break. Lines already on screen are kept and Clear stays cleared. After a long gap only the latest 100 lines are fetched again, so some lines in between can be missing. The panel keeps the newest 5,000 lines.
- A single log line is cut at 8 KiB and ends with `...[truncated]`.
- A tab hidden for more than 5 minutes closes its stream and reopens it when you return.
- Each user can have 5 live log streams open at once (20 per deployment). If the panel cannot connect it says your streams may be at the limit and keeps retrying; streams that were left open by a closed tab free their slot within a few minutes.
- If the deployment or its service was deleted, the panel says "This deployment no longer exists." and stops.

**Smaller things**
- You only ever see your own projects and services; anything that belongs to someone else behaves as if it does not exist.
- Errors in the app are now short sentences ("Cannot reach the server. Check your connection and try again.") instead of raw codes.
- The new web app is required: the old one cannot talk to the new server (see the operator section).

## OPERATOR NOTE (Manwinder)

1. **Update the UI together with the API.** The Go API has no Socket.io and issues refresh tokens, so an old cp-ui build cannot show logs or stay signed in. At cutover restart `npm run dev` (or deploy the new build) and hard-reload every open tab. Set `CORS_ORIGINS` to the exact UI origin (default `http://localhost:5173`; `*` is refused).
2. **Everybody is signed out.** Old Node tokens carry no expiry and are rejected on their first request; the browser clears them and shows the login page. Users keep their accounts and passwords. A user who registered on the Node API with a password longer than 72 bytes (Node truncated it) will get "Invalid email or password" and needs a password reset by you; an unusual legacy email can get a 400 instead of a 401. Count them with the legacy queries in the cutover runbook, step 4.
3. **Secrets.** Run `migrate encrypt-env` once before or at the cutover: stop the Node API, back up the database and `ENV_ENCRYPTION_KEY`, `--dry-run` (expect `suspect=0`), `--yes` until `conflicts=0`, then a final dry run with `would_encrypt=0`. The key must be the same one the worker uses; the API and the worker both log `envKeyFingerprint` at start, and the two must match. Without the key, stored env values and database passwords cannot be read. Full steps: `docs/superpowers/runbook/encrypt-env.md`. After encryption the Node API cannot read env values (rollback is the step 3 backup, see the cutover runbook).
4. **Settings.** Set `APP_ENV` explicitly (`development` binds `127.0.0.1` only, because `POST /dev/login` is on and needs no credentials; any other value binds all interfaces and removes `/dev/login`). With any non-loopback exposure use `APP_ENV=production`, a fresh `JWT_SECRET` (`openssl rand -base64 32`) and `CORS_ORIGINS` (required in production: `config.Load`, so the API and the worker, refuses to start without it; the worker's `.env` needs it too); `POST /dev/login` also answers 403 to a Host other than `localhost`, `127.0.0.1` or `[::1]`; outside development the API refuses to start with a placeholder or a `JWT_SECRET` shorter than 32 bytes.
5. **Behind nginx (not needed at this cutover).** On the dev stack the UI talks to the API directly, so leave `TRUSTED_PROXIES` empty. When a proxy is put in front (Plan 3), set `TRUSTED_PROXIES` to the nginx container's address only (one IP or CIDR; comma-separate several; it changes if nginx is recreated, so pin it) and have the route send `proxy_set_header X-Forwarded-For $remote_addr;`; otherwise every client shares one login, refresh and log-stream rate-limit bucket.
6. **Database ports.** `EXPOSE_MANAGED_PORTS` is true only in development, and then the port is bound to `127.0.0.1`; in production it must stay off. Containers that the Node worker started earlier (for example Mongo on `0.0.0.0`) keep their old bindings until they are redeployed: check `docker ps` for `0.0.0.0:` bindings and accept or fix them.
7. **Delete and kept data.** Delete is asynchronous; if one gets stuck use `docs/superpowers/runbook/teardown-stuck.md`. A managed database deleted with "keep data" leaves its volume `cp-vol-<serviceId>` on disk and ONE encrypted generated-password row so you can recover the data; nothing in the UI can re-attach it. Purge a kept volume by hand (runbook section 5). The delete dialog tells users the data stays on disk and only the server administrator can recover or erase it.
8. **Logs.** SSE streams use single-use tickets in the URL (never the access token), so access logs must not record query strings; grep the API log for `ticket=` after the cutover (it must be empty). When nginx is added (Plan 3) the stream route needs `proxy_buffering off`, a long `proxy_read_timeout`, and a log format without query strings.
9. **Sessions in the database.** Refresh tokens are stored hashed with a 30-day expiry per use. Replaying a refresh token that was already replaced (outside a 10 second window for two tabs) signs that whole session out.
10. **Not yet verified without a worker:** deploy and redeploy, container checks (`docker exec ... printenv`), the Redis `ping` from the project network, the teardown of real containers and the nginx route. These are cutover checks (checklist step 11).
11. **Env reveal contract.** `GET /services/:id/env?reveal=true&keys=A,B` decrypts only the listed keys (max 50, unknown keys ignored); all other keys stay listed with `********`; `reveal=true` without `keys` still reveals all; the audit log line carries ids only.

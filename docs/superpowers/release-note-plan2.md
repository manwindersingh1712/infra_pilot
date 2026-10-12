# Plan 2 release note

Stub. The full note is written at the Plan 2 cutover (see "Plan 2 Task 12 carry-forwards" in `docs/superpowers/review/backlog.md`).

## Teardown (deleting a service)

Deleting a service is asynchronous and permanent: the service disappears at once and its containers and URL are removed within a minute (a running build can take up to 3 minutes to stop). Teardown retries until it succeeds. If one gets stuck, see `docs/superpowers/runbook/teardown-stuck.md`.

### Managed databases (MongoDB, Redis) deleted with `purgeData=false`

The data volume `cp-vol-<serviceId>` is kept on the server, together with ONE encrypted generated-password row, so an operator can recover the data. It cannot be re-attached from the UI. Creating the service again gives a new service id and an empty volume. To remove a kept volume, see section 5 of the runbook.

## Env var encryption

Env var values written by the Node API are plain text. Before or at the cutover run `migrate encrypt-env` once to encrypt them: stop the Node API, back up the database and `ENV_ENCRYPTION_KEY`, `--dry-run` (expect `suspect=0`), `--yes` until `conflicts=0`, then a final dry run with `would_encrypt=0`. The command refuses to write when a value looks like ciphertext but does not decrypt (a wrong key, or a plain value starting with `enc:v1:`). Full steps, exit codes and how to resolve suspect rows: `docs/superpowers/runbook/encrypt-env.md`.

## API behind nginx: TRUSTED_PROXIES

Set `TRUSTED_PROXIES` to the address of the nginx container, for example `TRUSTED_PROXIES=172.18.0.5` (one IP or CIDR; comma-separate several). Without it the API keys its rate limits by the TCP peer, so behind nginx every client shares one login, refresh and log-stream bucket. List only the nginx address, not the Docker network's subnet: any other container on a listed subnet could send a forged `X-Forwarded-For` and get a fresh bucket. The nginx route for the API must set `proxy_set_header X-Forwarded-For $remote_addr;` (Plan 2 Task 18). Default: empty.

With `APP_ENV=development` the API binds `127.0.0.1` only, because `POST /dev/login` is enabled and needs no credentials. Any other value binds all interfaces.

## What users will notice in the UI

- You are signed out once when this version is deployed; sign in again. Sessions then renew automatically, so you stay signed in past the 15-minute access-token limit.
- Passwords must be 8 to 72 bytes (accented or non-Latin characters take more than one byte). Project names must be 2 to 100 characters.
- Environment variable values are hidden until you click the eye icon, and hidden again (and forgotten by the page) when you hide the last one. Changes apply on the next deployment: redeploy to apply them.
- Repository URLs must be public https:// URLs; SSH and file URLs are not supported.
- Live logs now use Server-Sent Events instead of Socket.io; the panel shows Live (green), Reconnecting (amber), Paused (grey, tab in the background) or Disconnected (red) and never gives up on its own while the deployment exists. Retry reconnects at once.
- The stream renews itself every hour and after sleep or network changes without a visible interruption. Lines kept on screen are not lost and Clear stays cleared; after a long gap only the latest 100 lines are re-fetched, so some lines in between may be missing. The panel keeps at most the newest 5,000 lines.
- A tab hidden for more than 5 minutes closes its log stream and reopens it when you come back; a tab opened in the background opens no stream until you show it.
- Up to 5 live log streams per user (20 per deployment). If the panel cannot connect it says your streams may be at the limit and keeps retrying; abandoned connections free their slot within a few minutes.
- If the deployment or its service was deleted the panel says "This deployment no longer exists." and stops.
- A single log line is cut at 8 KiB and ends with `...[truncated]`.
- The new UI is required at cutover; the Go API has no Socket.io.

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

# Plan 1 release note: Go worker replaces the Node worker

Cutover steps are in `docs/superpowers/plans/2026-10-07-go-plan1-worker.md`, Task 27. Decision: `docs/superpowers/review/decisions.md`, 2026-10-10.

## RELEASE NOTE (for users)

The part of Infra Pilot that builds and runs your services has been rewritten in Go and now runs on Temporal. The UI and the API are unchanged. Deploys work the same way from the UI; what changes is what happens underneath.

**What is better**
- Redeploying a service now removes the previous container once the new one is serving. Old containers no longer pile up.
- Traffic switches to the new version only after it passes a health check.
- A worker restart no longer loses a deploy in progress or the log stream of running apps.
- Managed Redis and MongoDB get a generated password and a named Docker volume (`cp-vol-<serviceId>`) instead of a folder in `/tmp`.
- Apps run with limits and no published host ports: 512 MB memory, 0.5 CPU, 256 processes, all Linux capabilities dropped. They are reachable only through `http://<serviceId>.localhost/`.

**Things you must know**
1. **MongoDB: dump before you redeploy.** A Mongo service redeployed by the new worker starts empty and the old `admin123` password stops working. Dump it first and verify the dump (operator note, step 6). Do not redeploy any Mongo service until that is done.
2. **Redis connection panel shows no password.** After a Redis service is redeployed, the connection panel shows `redis://localhost:<port>` without a password, and the Env tab shows the password as `enc:v1:...` ciphertext. Read the real password with `docker exec cp-<deploymentId> printenv REDIS_PASSWORD`. Existing Redis services are unaffected until their next deploy. This is fixed in Plan 2 (Task 17).
3. **Failure reasons are not shown in the UI.** A failed deployment shows `failed` and nothing else. The reason is in the Temporal UI (http://localhost:8233, workflow `deploy-<deploymentId>`) and in the worker log. Showing it in the deployment logs is planned for Plan 2.
4. **Build sources are stricter.**
   - The repo URL must be `https://`. Other schemes fail with `invalid_build_source`.
   - The ref must be `HEAD`, a branch name or a hex commit (4 or more characters). Tags and other refs fail.
   - Services with an invalid environment variable name (not `[A-Za-z_][A-Za-z0-9_]*`) or a value with line breaks (for example a PEM key) fail with `InvalidSpec` (one attempt, no retries). Affected services found before the cutover: **<list service ids here from Step 3.3>**.
5. **Runtime limits can break some apps.** Apps that `chown`, `su`/`gosu`, or write outside `/tmp` and their own app directory may fail. Heavy Next.js builds in the 2 GB builder may run out of memory. Static React sites keep the CHOWN/SETUID/SETGID capabilities nginx needs. Build and container logs are cut at 16 KiB per line group.
6. **Waiting behaviour.** A second deploy of a service that is already deploying waits in `queued` for up to 30 minutes, then fails. A stuck `deploying` row blocks the service for at most 1 hour. The health-check timeout is capped at 600 seconds. Redeploying Redis removes the old container before starting the new one, so expect a short outage.
7. **First deploy after the cutover.** Containers started by the Node worker keep running; they are replaced when you redeploy each service. Nginx routes persist, container names are the same, log streaming resumes, and Redis keeps its `dump.rdb`.
8. **Tenant isolation is not final.** App and builder containers can still reach ports that the dev compose publishes on the host (Postgres, Redis, RabbitMQ, MinIO, ClickHouse). Production compose (Plan 3) closes this.
9. **No rollback button.** Rolling back is an operator action (below).

## OPERATOR NOTE

1. Do not start until the user approved Task 27 Step 2 and the gate conditions in the decision entry are met (STAFF-7 fix, `REGISTRY_HOST` default `localhost:5001`, Verifier re-run).
2. Make `control-plane-go/.env` with: `DATABASE_URL`, `ENV_ENCRYPTION_KEY` (`openssl rand -base64 32`; back it up outside the repo, it is the only way to read generated managed-DB passwords), `REGISTRY_HOST=localhost:5001`, `NGINX_ROUTES_DIR` (absolute path to `control-plane/infra/nginx/conf.d/routes`), `TEMPORAL_HOST_PORT`, `REDIS_URL`, `CLICKHOUSE_URL/USER/PASSWORD`, `WORKER_METRICS_ADDR`. Never print or commit it.
3. Back up the database: `pg_dump -Fc` to `~/cp-backups/pre-go-cutover-<date>.dump` (container `cp_postgres`).
4. Run the Step 1 SQL (non-https repo URLs, odd commit refs, invalid env keys). Put the affected service ids in item 4 above.
5. `docker compose up -d`, then `migrate status`, then `migrate up` (run `migrate baseline` first only if it says so; it needs the `_prisma_migrations` table with all Prisma migrations finished). If `up` fails: restore the dump, do not start Go.
6. Dump every Mongo service (`umask 077`, `chmod 600`, verify with `gzip -t`, delete after verification). Hard gate: no Mongo redeploy before this.
7. Drain check: both RabbitMQ queues `0`, no `building`/`deploying` rows. Stop the Node worker. Check again for stranded `building`/`deploying` rows and reset each to `queued` plus a new `build.requested` outbox row.
8. Start the Go worker in its own terminal (`go run ./cmd/worker`). Success: "worker up" log line, the worker visible in http://localhost:8233, and `curl 127.0.0.1:9091/metrics` answers. If it will not start, restart the Node worker.
9. Run checklist items 1-10 (item 7 takes minutes, item 9 needs 10+ minutes of waiting). Check the Temporal UI before deciding to roll back on a single failed deploy.
10. Redeploy every managed DB, then run `docker ps` for any `0.0.0.0:` binding and `docker network inspect cp_net` for containers still on it. Remove Node-era Redis/Mongo leftovers (`docker rm -f`, no `-v`).
11. Rollback (within 14 days of the cutover date): no running `deploy-*` workflows; stop the Go worker; `UPDATE "OutboxEvent" SET status='pending' WHERE status='publishing'`; reset `building`/`deploying` rows (including `deploying` older than 1 hour) to `queued`; re-queue terminated ones with a new outbox row; start the Node worker. Go-created managed DBs use `cp-vol-<serviceId>` with a generated password: Node would redeploy them with `admin123` and no auth, so do not redeploy managed DBs under Node; restore from the dump.
12. Record the cutover date and "rollback window open until <date + 14 days>" in `decisions.md`.

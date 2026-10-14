# Plan 2 API cutover verification (runbook step 11 + Task 18 Step 5 items 1-11)

Date: 2026-10-15. Target: the REAL dev stack (Go API 127.0.0.1:8080, Go worker, UI :5173, Node API frozen). Verifier role. Written incrementally.
Safety: only `vfy-cutover-*` users/projects/services created and deleted. No restart of API/worker/cp_* containers, no encrypt-env, no migrate down, no restore. Secrets never printed. Scratch scripts in the session scratchpad.
Env at start: df 24 GiB free; infra containers cp_redis, cp_nginx, cp_clickhouse, cp_temporal, cp_postgres, cp_rabbitmq, cp_registry, cp_minio up; 14 pre-existing service rows in the DB; all pre-existing app containers are Exited (stopped before the cutover, untouched).

## Results log

### Item 1: forced re-login - PASS
- `~/cp-logs/.dev-token` (Node era, claims `sub`,`iat`, no `exp`, 151 chars): `GET /projects` -> 401 `{"error":"unauthorized"}`.
- A token I minted with the real `JWT_SECRET` (read inside a script, never printed), claims `sub`+`iat` and NO `exp`: 401. Same secret and sub WITH `exp`: 200. So the rejection is the missing `exp`, not the signature.
- No token: 401.
- UI (headless Chrome over CDP, UI :5173 -> API :8080): `cp_token` = the Node-era dev token, navigate to `/projects`: two 401s on `/projects`, no refresh attempted (no refresh token), URL ends on `/login`, `cp_token` cleared, notice shown: "Your session expired, please sign in again."

### Item 2: auth flows - PASS (curl and UI)
curl (users `vfy-cutover-a/b@example.test`):
- register -> 200 user object; duplicate email -> 409 `email_already_exists`; login -> `{token, refreshToken, user}`; access token ttl 900 s (`exp-iat`).
- corrupt access token (`<jwt>x`) -> 401. `POST /auth/refresh` with the refresh token -> 200 new pair, new access works (200). Replaying the used refresh token inside the grace window -> 401 `invalid_refresh_token`.
- logout (`POST /auth/logout` with the refresh token) -> 200 `{"ok":true}`; refresh afterwards -> 401. Access token stays valid until its 15 min `exp` (stateless; by design).
UI (user `vfy-cutover-ui3@example.test`): Sign up form -> 200 `/auth/register`, lands on `/projects`, both `cp_token` and `cp_refresh` stored. Appended `x` to `cp_token`, reload `/projects`: `401 /projects` -> `200 /auth/refresh` -> `200 /projects`, stays on `/projects`, the token hash changed. User menu "Sign out": `200 /auth/logout`, both tokens cleared, route `/`. Sign in form: `200 /auth/login` -> `/projects`.
Observation (not a failure): the auth limiter bucket (burst 5, one token per 6 s, per client IP) counts register AND login, successful or not. After about 7 register/login calls within a minute the next login returned 429 `too_many_requests` until the bucket refilled. Item 9 below is therefore consistent, but a normal user behind a shared IP can hit it.
Slip: my first refresh command echoed one throwaway refresh token and one access token of the test user `vfy-cutover-a` (replay response printed). Test user only, the family is revoked and the user will be orphaned; no real secret was involved.

### Item 3: ownership - PASS
User B against user A's project/service/deployment (all ids are A's):
| Request as B | Result |
|---|---|
| GET /services/<A svc>/env (and ?reveal=true) | 404 `service_not_found` |
| GET /deployments?serviceId=<A svc> | 200 `[]` (by design); same call as A returns the deployment |
| GET /logs/deployments/<A dep> | 404 `deployment not found` |
| POST /logs/deployments/<A dep>/stream-ticket | 404 |
| GET /projects/<A proj>/canvas | 404 `Project not found` |
| GET /projects/<A proj> | 404 |
| GET /services?projectId=<A proj> | 200 `[]` |
| POST /services/<A svc>/deploy, DELETE /services/<A svc>, POST /services/<A svc>/env | 404 each (no side effect) |

### Item 4: create, deploy, redeploy - PASS (after three repo attempts that exposed generator limits; see Findings VFY-1..3)
Service `vfy-cutover-fc` (nodejs, public repo `fishcharlie/HelloWorldExpressServer`, branch master) in project `vfy-cutover-proj-a`.
- Deploy 1: queued -> deployed in ~9 s. Workflow `deploy-<dep1>` Completed. Container `cp-<dep1>` on network `cp-proj-<projectId>`, mem 512 MiB, pids 256. nginx route file `svc-<serviceId>.conf` generated (upstream `cp-<dep1>:3000`); `curl -H "Host: <serviceId>.localhost" http://127.0.0.1/` -> `Hello World` 200.
- Redeploy (UI "Deploy Latest" click): new deployment deployed, `docker ps -a | grep ^cp-cmv` shows ONLY the new container `cp-<dep2>`; the old `cp-<dep1>` is gone; route upstream rewritten to `cp-<dep2>:3000`; app answers 200. Both workflows Completed. The old deployment row keeps status `deployed` but now has `stoppedAt` set (2026-10-14T20:32:01Z); the new one has `stoppedAt: null`.
Failed attempts (deployments `failed`, workflows Failed, services later deleted by me):
- heroku/node-js-getting-started: `engines.node` = `22.x || 24.x || 26.x` -> generated `FROM node:22.x-alpine`, tag does not exist, build fails (5 attempts).
- digitalocean/sample-nodejs: no `package-lock.json` -> generated `RUN npm ci --only=production` fails.
- dragosrobertn/hello-world-express: builds, container starts, but listens on `process.env.PORT || 4000` while the image `EXPOSE`s 3000 and no `PORT` is set -> `WaitHealthy` "cp-<dep>:3000 not reachable after 1m0s", deployment `failed`.

### Item 5: env vars - PASS
Set `VFY_KEY` through the API: response value `********`; list masked; `?reveal=true` returns the plain value (checked by comparing, not printed).
- `psql`: `select "key", left(value,7), length(value) from "EnvVar" where "serviceId"=...` -> `VFY_KEY | enc:v1: | 71`.
- `docker exec cp-<dep> printenv VFY_KEY` prints the plain value (matches what I set) in both the first and the redeployed container.

### Item 6: live logs over SSE with a ticket - PASS (restart-reconnect case SKIPPED by instruction: the API must not be restarted)
- UI (headless Chrome, project canvas, service panel Logs tab / "View logs"): `POST /logs/deployments/<dep>/stream-ticket` (200, two calls because the dev build runs StrictMode double effects), then ONE `GET /logs/deployments/<dep>/stream?ticket=<T>`; the page URL never contains a token; the stream request carries no `Authorization` header (EventSource); badge "Live"; first SSE event `history`. The fc test app writes nothing to stdout, so that panel correctly shows "No logs yet".
- curl with REAL worker log lines: managed Redis deployment, ticket then `GET .../stream?ticket=...` -> `200 text/event-stream`, `Cache-Control: no-cache, no-store`, `X-Accel-Buffering: no`, `event: history` with 7 `stdout` lines from the container (`Redis is starting`, `Redis version=7.4.11`, ... `Ready to accept connections tcp`), `meta.source: "redis"`.
- Ticket is single-use: reusing it -> 401; no ticket -> 401; Bearer header without ticket -> 401; user B asking a ticket for A's deployment -> 404.
- Not covered: build logs. The Go worker streams only container `docker logs -f` output into Redis (see `internal/runtime/logstream.go`); a failed build's output lives only in the Temporal activity error, so the UI log panel for a failed build is empty. Managed DB panels have no Logs tab and no "View logs" button in the UI (Deployments, Connection only).

### Item 7: managed Redis connection from the project network - FAIL for the literal checklist command (service itself works)
Two test Redis services deployed (`vfy-cutover-redis-r1`, `-r2`): `redis:7-alpine`, `--requirepass` set, volume `cp-vol-<serviceId>` mounted at `/data`, published only on `127.0.0.1:200xx`, network `cp-proj-<projectId>`, env row `REDIS_PASSWORD` (masked `********` in the list; reveal returns 32 alphanumeric characters).
- UI Connection tab: rows `host=svc-<id>`, `port=6379`, `password=••••••••••`, `url=redis://:••••••••••@svc-<id>:6379`, `local=redis://:••••••••••@localhost:20007`, `cli=redis-cli -u '...'`. Plain password absent from the DOM (outerHTML) while masked and after "Hide"; present after "Reveal secrets". One `GET /services/<id>/env?reveal=true&keys=REDIS_PASSWORD` per reveal.
- Checklist command: `docker run --rm --network cp-proj-<projectId> redis:7-alpine redis-cli -u 'redis://:<pw>@svc-<id>:6379' ping` -> `AUTH failed: WRONGPASS invalid username-password pair or user is disabled.` (also with the password percent-encoded, which is a no-op for an alphanumeric password).
- Same network, no password: `NOAUTH Authentication required.` (auth is enforced).
- `redis-cli -u 'redis://default:<pw>@svc-<id>:6379' ping` -> `PONG`; `REDISCLI_AUTH=<pw> redis-cli -h svc-<id> ping` -> `PONG`. `redis-cli --version` in the image: 7.4.11.
- Conclusion: the password and network are right; the URL shape with an EMPTY username (`redis://:pw@host`) that the UI shows (`cp-ui/src/lib/serviceActions.ts:101`) and the plan prescribes does not authenticate with redis-cli 7.4.11. See VFY-4.

### Canvas: drag persists, connections - PASS
- UI: dragged the `vfy-cutover-fc` node with real mouse events (10 moves); one `POST /services/<id>/position {"positionX":161.05,"positionY":120.79}`; after a page reload the node is at `translate(161.053px, 120.789px)` and the API canvas returns the same coordinates.
- `POST /services/connections` (fc -> r1) 200; the connection appears in `GET /projects/<id>/canvas` (`connections` length 1); user B creating a connection on A's services -> 404.
- Slip: this script also logged a refresh token of the test user in its output (test user only, already consumed).

### Legacy data through the Go API - PASS
Dev login (`POST /dev/login`, loopback, development) 200. For the other four legacy users I minted a read-only access token with the server secret (never printed) and issued only GETs. Counts, API vs database (live services, `deletedAt IS NULL`):
| user | projects | services | canvas 200 | connections shown | deployments |
|---|---|---|---|---|---|
| abc@test.com | 2/2 | 3/3 | 2/2 | 2 | 11/11 |
| dev@local | 2/2 | 5/5 | 2/2 | 3 | 5/5 |
| manwindersingh@test.com | 2/2 | 2/2 | 2/2 | 0 | 3/3 |
| manwinder@test.com | 2/2 | 4/4 | 2/2 | 2 | 18/18 |
| singh@admin.com | 1/1 | 0/0 | 1/1 | 0 | 0/0 |
14 legacy services total (3+5+2+4), 7 legacy connections shown + my 1 = the 8 `ServiceConnection` rows; none hidden (no soft-deleted endpoint, no cross-owner row).
- The 3 legacy EnvVar rows (`verify-redis` REDIS_PASSWORD, `portfolio` key `manwinder`, `manwinder` service key `aads`) are all `enc:v1:` in the DB, list masked (`********`) and `?reveal=true` returns a non-empty plain (non `enc:v1:`) value each (lengths 32, 9, 4; values not printed).
- Old deployments list: dev project `test` services show `failed/main` (the old `main` ref), and `deployed/HEAD` x3, `stoppedAt` null; `GET /deployments` (no filter) returns the dev user's 5 rows.
- UI as dev user (token injected into localStorage): `/projects` lists `verify-p1t1a` and `test`; canvas of `test` renders 4 nodes (redis, mongo, port, cutover-broken-repo) and 3 edges.
- Observation: those legacy nodes all show "Online" although their containers are Exited (stack restart, see decisions); the status comes from the last deployment row, not from Docker.

### Project names - PASS
`POST /projects`: 2 chars `ab` 200; 100 ASCII chars 200; 101 ASCII 400 `invalid_request`; 100 emoji (`🚀` x100, counted as runes) 200; 101 emoji 400; `🚀 rocket 日本` 200 (stored and listed intact); empty and whitespace-only 400. A 1-character name is 400 by design (`maxProjectNameRunes`/min 2, mirrored in `cp-ui/src/lib/projectName.ts`: 2-100).

### Item 8: delete - PASS
Before: containers `cp-<dep>` x3, volumes `cp-vol-<redis serviceId>` x2, route `svc-<fc serviceId>.conf`.
- `DELETE /services/<fc>` -> 202 `{"deleted":true}`; `DELETE /services/<r1>?purgeData=false` -> 202; `DELETE /services/<r2>?purgeData=true` -> 202.
- 15 s later: no `cp-cmv*` container left; route file gone (host dir and inside `cp_nginx`); volume of r2 (purge) gone; volume of r1 (no purge) KEPT (`cp.managed=true`), as designed; I removed that kept volume myself afterwards because it was my test data. Workflows `teardown-<fc>`, `teardown-<r1>`, `teardown-<r2>` Completed (also the three earlier teardowns of my failed-build services). `GET /services?projectId=` -> `[]`, canvas `{svcs:0, conns:0}`; the connection row to a deleted service is no longer shown.
- Also removed my three test images `localhost:5001/<svc>:<tag>`. The project network `cp-proj-<projectId>` stays (it still has `cp_nginx` attached), by design.

### Item 9: rate limit - PASS
7 fast wrong-password logins from one client: `401 401 401 401 401 429 429`, the 429 carries `Retry-After: 6`.

### Item 10: production-mode start and dev login - PASS
Second binary built from HEAD `077f78d` (`go build -o <scratch>/api2 ./cmd/api`), run from `control-plane-go/` against the same DB on :8081 with metrics on :9092, killed right after each run (the real API on :8080 stayed up, `/readyz` 200):
| Case | Result |
|---|---|
| `APP_ENV=production JWT_SECRET=short` | exit 1: `config: JWT_SECRET must be at least 32 bytes outside development, got 5` |
| production, `JWT_SECRET=dev-secret-change-me` | exit 1: `config: JWT_SECRET is a placeholder; generate one with: openssl rand -base64 32` |
| production, random 32-byte secret, `CORS_ORIGINS` empty | exit 1: `config: CORS_ORIGINS is required when APP_ENV=production ...` |
| production, `openssl rand -base64 32` secret, `CORS_ORIGINS=https://app.example.test` | starts (`api listening addr=[::]:8081 devLogin=false`), `POST /dev/login` -> 404, `/healthz` 200; `Origin: https://evil.test` -> no `Access-Control-Allow-Origin`; `Origin: https://app.example.test` -> `Access-Control-Allow-Origin: https://app.example.test`; process killed, no listener left on :8081 |
The throwaway binary and its log were deleted.

### Item 11: metrics - PASS
`curl 127.0.0.1:9090/metrics | grep cp_http_requests_total` -> 43 series, labelled by route PATTERN and status, e.g. `{method="GET",route="GET /services/{id}/env",status="404"}`, `{route="POST /logs/deployments/{id}/stream-ticket",status="200"}`, `{route="POST /auth/login",status="429"}`; no raw ids in any label. `GET :8080/metrics` -> 404 (metrics only on the private listener).

### Secret / ticket hygiene in logs - PASS
Scanned `~/cp-logs/go-api.log` (358 lines), `~/cp-logs/go-worker.log` (86), `docker logs cp_nginx` (55) and `~/cp-logs/cp-ui.log`:
- `ticket=`: 0 hits in all four. The API access log records method, path (no query string), status, ms, requestId only, e.g. `method=POST path=/logs/deployments/<id>/stream-ticket status=200`; the stream GET lines carry no ticket.
- `eyJ` (JWT prefix) 0; my test users' password 0; the Redis passwords 0; `vfy-plain-value` (the env values) 0; the real `JWT_SECRET` value 0; the real `ENV_ENCRYPTION_KEY` value 0; `"refreshToken"` 0. The startup config line prints `databaseURL="postgresql://cp:xxxxx@localhost:5432/...?schema=xxxxx"` (redacted).

### Node-era containers published on 0.0.0.0 - REPORT (no change made)
- No Node-era Mongo container exists in a running state; the only Mongo container is `t23-mongo-b-...` (`mongo:7`, Exited, no port bindings). The 14 legacy services have no running containers at all.
- Pre-existing Exited container `cp-cmpcpwei90001i83nvizvft8f` (`redis:7-alpine`, 4 months old) still has the Node-era binding `6379/tcp -> HostIp "" HostPort 20029` (all interfaces) and `cp-cmpcptpv10002093no6xud46i` (`80/tcp -> 20028`): if started with `docker start` they would listen on 0.0.0.0. The Go worker publishes managed ports on 127.0.0.1 only (both of my Redis tests were on `127.0.0.1:200xx`).
- Running infra published on all interfaces: postgres 5432 (cp:cp dev credentials), rabbitmq 5672/15672, clickhouse 8123/9009, registry 5001, minio 9000-9001, nginx 80. Three stopped throwaway postgres containers (`eloquent_babbage` etc., ports 56971-56974) also hold 0.0.0.0 bindings. Dev-stack settings, noted for Plan 3.

## Findings
- VFY-1 (Medium, carried from Plan 1, not Plan 2): the Dockerfile generator turns `engines.node` into an image tag verbatim. `heroku/node-js-getting-started` (`"22.x || 24.x || 26.x"`) produces `FROM node:22.x-alpine`, which does not exist, so the build fails five times. `build/dockerfile.go` (`baseImage`) should keep only `^[0-9]+` or fall back to the default.
- VFY-2 (Low, Plan 1): generated Dockerfiles always run `npm ci`, so a repo without `package-lock.json` (digitalocean/sample-nodejs) cannot build. The failure text is only visible in the Temporal activity error and the worker log; the deployment just shows `failed`.
- VFY-3 (Low/Medium, Plan 1): the container port is the image's `EXPOSE` (3000 for generated images) but nothing sets `PORT`; an app that listens on `process.env.PORT || 4000` (dragosrobertn/hello-world-express) never becomes reachable: `WaitHealthy ... cp-<dep>:3000 not reachable after 1m0s`.
- VFY-4 (Medium, Plan 2 UI and checklist): the Connection tab URL/Local URL/CLI use `redis://:<pw>@host:port` (`cp-ui/src/lib/serviceActions.ts:101`), and Task 18 item 7 prescribes the same shape. With `redis:7-alpine` (redis-cli 7.4.11) that form returns `WRONGPASS invalid username-password pair or user is disabled`, while `redis://default:<pw>@...` and `REDISCLI_AUTH` give `PONG`. The password itself and the network are correct, so users who copy the CLI row from the UI get an auth failure. Suggested fix: emit `redis://default:<pw>@host:port` (valid for ioredis and node-redis too), update the checklist and tests. The Task 17 verifier checked only quoting round-trips, never a real `redis-cli`.
- VFY-5 (Observation): the managed Redis container is started with `sh -c "exec redis-server --dir /data --requirepass <password>"`, so the password is readable by anyone who can run `docker inspect` or `ps` on the host (I redacted it in my own output).
- VFY-6 (Observation): the register/login limiter (burst 5, one token per 6 s, per IP) counts successful calls too, and the UI redirects to `/projects` after login; fine at the 6th wrong login, but 6 normal sign-ins in a minute from one office IP are blocked.
- VFY-7 (Observation): `POST /services` has no update/edit endpoint and there is no `DELETE /projects`, so test projects cannot be removed through the API. Residue left in the dev DB by this run (all owned by `vfy-cutover-*` users): users `vfy-cutover-a`, `-b`, `-ui`, `-ui2`, `-ui3` (`@example.test`); 5 projects (`vfy-cutover-proj-a`, `ab`, `yyyy...` (100 chars), `🚀 rocket 日本`, 100 x `🚀`; the UI users own none); 6 soft-deleted `vfy-cutover-*` Service rows with their Deployment/EnvVar/connection rows. No pre-existing row, container, volume or env var was modified.
- VFY-8 (Observation): legacy nodes render "Online" although their containers are Exited (status comes from the last deployment row).

## Verdict per item
| # | Item | Verdict |
|---|---|---|
| 1 | Forced re-login (Node token without `exp` rejected 401, `.dev-token` rejected, UI sent to /login) | PASS |
| 2 | Auth: sign up, login, refresh, logout (curl and UI) | PASS |
| 3 | Ownership 404s (env, logs, canvas, ticket, deploy, delete; deployments?serviceId 200 `[]` by design) | PASS |
| 4 | Create, deploy, redeploy, old container retired | PASS (public repo fishcharlie/HelloWorldExpressServer; three other repos exposed VFY-1..3) |
| 5 | Env masked, reveal, `enc:v1:` in psql, plain in container | PASS |
| 6 | SSE logs with ticket, no token in URL | PASS (API-restart reconnect SKIPPED by instruction) |
| 7 | Managed Redis connection from the project network | FAIL for the literal `redis://:<pw>@` command (VFY-4); PASS with `default` user / REDISCLI_AUTH |
| 8 | Delete with and without purge, route, volume, workflow Completed | PASS |
| 9 | 6th wrong login 429 | PASS |
| 10 | Production start refusal, no /dev/login, CORS | PASS |
| 11 | Metrics by route pattern | PASS |
| - | Canvas drag persists; connections; legacy 14 services/envs/deployments | PASS |
| - | Project names 1/101/emoji | PASS (1 char rejected by design, min 2) |
| - | `ticket=` and secrets in logs | PASS (0 hits) |
| - | Node-era Mongo containers on 0.0.0.0 | REPORTED (none running; see above) |

## Overall verdict
PASS with one defect: 10 of the 11 checklist items pass on the real dev stack with no data loss and no pre-existing object touched. Item 7 fails as literally written (VFY-4, the copyable URL shape is wrong for redis-cli 7.4; the service itself, its password, network isolation and volume handling are correct). I do not consider it a rollback trigger (no data impact, rollback table section 4 not needed); it should be fixed forward in the UI/checklist. VFY-1..3 are worker/generator limits that predate this cutover. The API-restart reconnect case was not run by instruction.
Cleanup confirmed: every `vfy-cutover-*` service deleted via the API, containers/route/volumes/images gone, headless Chrome closed, scratch tokens and passwords deleted, df 22 GiB free at the end. Go API (pid 91106), Go worker, UI and all `cp_*` containers were never restarted.

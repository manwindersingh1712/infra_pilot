# Plan 2 follow-up fix batch - Verifier report (`7c29d61..HEAD`, HEAD = f68b8ab, 19 commits)

RESULT: PASS (every requested check passes; two low-severity observations, none blocking)

## Method
Own scripts, nothing from earlier reports. Throwaway `postgres:16` (tmpfs, 127.0.0.1:25460), `go run ./cmd/migrate up` (migrations 00001-00004 applied), Go API built from HEAD into the scratchpad, dev Redis 6380 (stream tickets and one seeded log stream) and dev ClickHouse 8123 (reachable, not queried), random JWT_SECRET (48 chars) and ENV_ENCRYPTION_KEY. UI: Vite on 5195 from `git archive HEAD cp-ui` (`npm ci`, dev build, so StrictMode doubles effects), API on 8105, headless Google Chrome over CDP with real `Input.dispatch*` events. Seeded by the API (register, projects, services, env) plus SQL for deployment rows and plain-text `REDIS_PASSWORD`/`OTHER_SECRET` env rows. Not touched: the Go worker, the dev DB (5432), Node API 8080, encrypt-env, Temporal, Docker prune. df before 14 GiB free, after 13 GiB (the `npm ci` copy and Postgres container were removed; the figure rounds).

## 1. Suites
| Check | Result |
|---|---|
| `cd cp-ui && npm run lint` | PASS: 0 errors, 5 warnings (react-hooks/exhaustive-deps, same five as before) |
| `npm run build` | PASS: `built in 1.60s` (chunk-size warning only) |
| `npm run check:logic` | PASS: `ok: 76 checks passed` |
| `cd control-plane-go && go build ./... && go vet ./... && golangci-lint run` | PASS: no output from build/vet; `0 issues.` |
| `go test -race -p 2 ./...` | PASS: every package `ok` (most `(cached)`, which is why the next row exists) |
| `go test -count=1 -race -p 2 ./internal/api/ ./internal/config/ ./cmd/...` (uncached) | PASS: `internal/api 92.018s`, `internal/config 1.701s`, `cmd/api 17.965s`, `cmd/migrate 11.236s`, `cmd/worker 1.894s` |

## 2. Real cmd/api run (curl)
| # | Check | Result |
|---|---|---|
| 1 | Retry-After is the real remaining wait | PASS |
| 2 | `/dev/login` Host check | PASS |
| 3 | Production refuses without CORS_ORIGINS; starts with it; `/dev/login` 404 | PASS |
| 4 | `NODE_ENV=development` without APP_ENV logs the legacy-source WARN | PASS |
| 5 | `reveal=true&keys=K1` reveals only K1 | PASS |
| 6 | `keys` over 50 -> 400; `reveal=true` alone reveals all | PASS |
| 7 | Names stored trimmed (spaces and NBSP) | PASS |
| 8 | TRUSTED_PROXIES buckets by the XFF client, with port | PASS |
| 9 | Route walk without a token | PASS |

1. Six wrong logins (bucket burst 5, refill 1 per 6 s): requests 1-5 `401`; request 6 `429` with `Retry-After: 5` (not 60). Repeats after `sleep 2`: `Retry-After: 3`; after another `sleep 2`: `Retry-After: 1`. It tracks the real wait and decreases.
2. `POST /dev/login -H 'Host: evil.test'` -> `{"error":"forbidden_host"}` 403. `Host: localhost:8105` 200, `127.0.0.1:8105` 200, `[::1]:8105` 200. `evil.test:8105` 403 and `localhost.evil.test` 403 (no suffix confusion).
3. `APP_ENV=production` with a 48-char JWT_SECRET and no CORS_ORIGINS: exit 1, `level=ERROR msg="api exited" err="config: CORS_ORIGINS is required when APP_ENV=production: set it to the exact origin(s) of the UI, e.g. https://app.example.com"`. Also refused for `NODE_ENV=production` and for no env at all (default is production). With `CORS_ORIGINS=https://app.example.com`: starts, `/healthz` 200, `POST /dev/login` -> `{"error":"not_found"}` 404, no "dev login" log line, `Access-Control-Allow-Origin: https://app.example.com` and `Access-Control-Expose-Headers: X-Request-ID, Retry-After`.
4. `NODE_ENV=development` only: `level=WARN msg="APP_ENV is not set; the environment mode comes from the NODE_ENV fallback. Set APP_ENV explicitly" source=NODE_ENV appEnv=development`. With APP_ENV also set: no such WARN (only the dev-login WARN). With `NODE_ENV=production`: the same WARN, then the CORS refusal.
5. A redis service with K1, K2, K3: plain GET all `********`. `reveal=true&keys=K1` -> K1 `val-K1`, K2 and K3 `********`. `keys=K1,K3` -> K1 and K3 revealed, K2 masked. `reveal=true` alone -> all three revealed. `keys=K1` without `reveal` -> all masked, 200.
6. 51 keys -> `{"error":"invalid_request"}` 400; exactly 50 keys -> 200.
7. `{"name":"  <NBSP>Proj One<NBSP>  "}` -> stored `"Proj One"`; service `" <NBSP> svc one <NBSP> "` -> stored `"svc one"`. A whitespace-only and an NBSP-only project name -> 400 `invalid_request`. (Created through the UI later with `"  db-one  "`: stored `db-one`.)
8. `TRUSTED_PROXIES=127.0.0.1/32`, peer 127.0.0.1: five `X-Forwarded-For: 203.0.113.9:4444` logins `401`, sixth `429`; the same IP without a port `429` (same bucket); `203.0.113.10` `401` (own bucket); `203.0.113.9:5555` `429` (port ignored). `[2001:db8::1]:99` x6 -> sixth `429`, then bare `2001:db8::1` `429`. Config log shows `trustedProxies=[127.0.0.1/32]`. Control: a server without TRUSTED_PROXIES ignores XFF (seven different XFF addresses: 5x401 then 429).
9. Every route in `routes()` without a token: public as designed (`/healthz` 200, `/readyz` 200, `/auth/register|login|refresh` 400 for the empty body, `/auth/logout` 200, `/dev/login` 200 in dev). All 20 private routes (projects, services, env, canvas, connections, deployments, logs, stream-ticket, stream) -> 401. `GET /nope` -> 404 `not_found`.

## 3. Browser (headless Chrome, Go API 8105, Vite 5195)
| # | Check | Result |
|---|---|---|
| 1 | Header Delete service on every tab | PASS |
| 2 | Stopped badge | PASS |
| 3 | Repo URL hint and rejection rules | PASS |
| 4 | Connection tab uses `keys=`, shows only the password after Reveal | PASS |
| 5 | Three created services do not stack | PASS |
| 6 | 100-emoji project name | PASS |
| 7 | 429 message shows the wait | PASS |
| 8 | Whole UI loads with the removed ServiceCanvas cluster, no console errors | PASS (see observation 1) |
| 9 | Login, refresh, logout | PASS |
| 10 | Log stream still opens with a seeded Redis line | PASS |

1. `Delete service` is at y=29 in the panel header on every tab: appA/appC `Deployments`, `Env Vars`, `Logs`; redisB `Deployments`, `Connection`. All visible.
2. appA history row of a superseded deployment (deployed + stoppedAt): `STOPPED` (the live row is `DEPLOYED`). appC, whose newest deployment is deployed + stoppedAt: top row `STOPPED def`, its older failed row `REMOVED abc` (unchanged behaviour). redisB history row `STOPPED`. Screenshots checked.
3. Node.js form: initial hint `Public https:// URLs only`. `git@github.com:user/repo.git`, `ssh://...`, `git://...` -> `SSH and git@ addresses are not supported. Use the public https:// address of the repository, e.g. https://github.com/user/repo.`; `file:///tmp/x` and `/tmp/x` -> `Local paths and file:// URLs are not supported. Use a public https:// address.`; `http://github.com/u/r` -> `Use an https:// address; plain http:// is not supported.`; `not a url` and `github.com/u/r` -> `Enter a public https:// address, ...`. All shown with `role=alert`, `aria-invalid=true`. `https://github.com/u/r`, `.../r.git` and `https://gitlab.example.com:8443/g/r` -> no problem. Submitting with `git@...` sent no `POST /services`.
4. redisB, Connection tab: one `GET /services/<id>/env?reveal=true&keys=REDIS_PASSWORD` (two in dev because of StrictMode). Masked state: host/port visible, password, URL, Local URL, CLI masked; neither `pw-secret-xyz` nor the other secret `other-secret-abc` appears in the outerHTML or text. After `Reveal secrets`: password `pw-secret-xyz`, URL, Local URL and CLI show it; `other-secret-abc` is still absent from the page (and from the API response: `OTHER_SECRET` came back `********`). No further env request on Reveal.
5. Empty project, three databases created through the modal (redis, mongodb, redis): each did `POST /services`, `POST /services/<id>/deploy`, and from the second on `POST /projects/<id>/canvas/auto-layout`. Stored positions (0,0), (0,230), (0,460); rendered boxes y=345, 805, 1265 (viewport units), no overlap.
6. 101 emoji: `Names are 2 to 100 characters. This one is too long.`, Create disabled. 100 emoji: `2-100 characters`, Create works, the API stores 100 characters (200 UTF-16 units) and the project list renders it.
7. Nine fast wrong logins on `/login`: first 6 `Invalid email or password.`, then `Too many requests. Wait about 5 seconds and try again.`, `... about 3 seconds ...`, `... about 2 seconds ...`. The wait comes from `Retry-After` across origins (exposed by CORS).
8. `/`, `/login`, `/signup`, `/projects`, the canvas, each node panel, the create modal and a deleted-project URL: no page exceptions. Console output: only the browser line for the deliberate 404 on a non-existent project, plus the React dev warnings in observation 1.
9. Login -> `/projects`, `cp_token` and `cp_refresh` set. With `cp_token` set to `bad.token.value`: `GET /projects` 401 -> `POST /auth/refresh` 200 -> `GET /projects` 200, both tokens rotated, projects render. Sign out: `POST /auth/logout` 200, storage emptied, lands on `/`; the old refresh token then gives 401 from `/auth/refresh`.
10. appA Logs tab: `POST /logs/deployments/dA-live/stream-ticket` 200, `GET .../stream?ticket=` 200, badge `Live`, both seeded lines (stdout and stderr) shown from the history event, and a line published to `logs:pubsub:dA-live` arrived live. No console errors.

## Observations (not failures)
1. Opening the create-service modal in the dev build prints two React warnings, `Invalid DOM property fill-rule. Did you mean fillRule` and the same for `clip-rule`. They come from an SVG icon in `CreateServiceModal.tsx` (also in `ServiceNode.tsx` and `ServiceDetailsPanel.tsx`), added in an old commit (256fc46 "svg updates"), not by this batch. Dev-only noise; cosmetic.
2. API-created services all start at (0,0) and stack until `auto-layout` is called (the UI calls it itself on create). A service created by another client without calling auto-layout stacks; this is the documented design ("New services start at (0, 0)"), noted only because my first seeded canvas overlapped until I called it.

My own mistake, not a product bug: my first seeded log lines used `source=build|runtime`, which the UI's `isLogEntry` filters out (valid: stdout, stderr, system). After reseeding with `stdout`/`stderr` they showed.

## NOT verified
- A production Vite build in the browser (dev server only); a real worker deploy, Temporal, nginx or containers; ClickHouse history for old deployments (the API only had a reachable ClickHouse); Retry-After through a real reverse proxy; mobile layout, other browsers; the Delete service flow itself (only its presence in the header was checked; the dialog was verified in the Task 17 round 2 report); a real `cp_` DB with Node-era rows.

## Cleanup
Chrome (own profiles, deleted), Go API instances (8105, 8106, 8107, 8108, metrics 19105 to 19108), Vite 5195 stopped; container `vfyfb_pg` removed (no prune); the extracted `git archive` copy with its `node_modules` and `.vite` removed; the seeded Redis key `logs:stream:dA-live` deleted (stream tickets are short-TTL random keys). Ports 8105, 8106, 5195, 9411, 25460 free; other containers (cp_*, t23-mongo-b-*) untouched. `cp-ui/dist` (git-ignored) was produced by `npm run build`. No repository code changed, nothing committed or pushed; the only file I wrote in the repo is this one (git status shows other reviewers' untracked files and the known untracked ones).

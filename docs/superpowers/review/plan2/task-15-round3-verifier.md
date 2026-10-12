# Plan 2 Task 15, round 3 - Verifier report (fix commits b316b49, d3b4e5a, 3a937c2; HEAD = 8853c07)

RESULT: PASS (all five browser checks pass; PROD-1 is now fixed in the real browser flow)

## Method
Independent of the Engineer's run. Headless Google Chrome driven over CDP (real mouse, keyboard and `Input.insertText` events; `Fetch` domain interception for delay/fail/hold). UI served by Vite on 5178 from `git archive HEAD cp-ui` (StrictMode on, `VITE_API_BASE=http://localhost:8088`). Throwaway `postgres:16` (tmpfs, 127.0.0.1:25435) migrated with `go run ./cmd/migrate up`; Go API built from the repo to the scratchpad and run with APP_ENV=development on 8088 (random JWT_SECRET and ENV_ENCRYPTION_KEY, dev Redis 6380, ClickHouse 8123, metrics 19092). A fresh user and project per scenario. df before: 23 GiB free. Evidence: DOM text and element state sampled over CDP, CDP Network events (non-GET list) and the API request log.

## Static checks (cp-ui, working tree = HEAD, clean)
| Check | Result |
|---|---|
| `npm run lint` | PASS: 0 errors, 5 warnings (react-hooks/exhaustive-deps, same five as round 2) |
| `npm run build` | PASS: `built in 1.43s` (chunk-size warning only) |
| `npm run check:logic` | PASS: `ok: 30 checks passed` |

## Browser checks
| # | Check | Result |
|---|---|---|
| 1 | PROD-1: failed deploy keeps the dialog, retry deploys the same id | PASS |
| 1b | Back arrow after a failed deploy | PASS |
| 2 | SEC-9a: close while create/deploy is in flight (Escape, backdrop, close during deploy, reopen before resolve) | PASS |
| 3 | Stale env reveal | PASS (see the reachability note) |
| 4 | Background canvas refresh failure | PASS |
| 5a | Initial load shows the loader; empty-canvas dialog auto-opens once | PASS |
| 5b | Corrupt cp_token refresh | PASS |
| 5c | Logout | PASS |

### 1. PROD-1 PASS
Setup: new project (empty canvas, dialog auto-opens), GitHub Repository > Docker, name `retry-svc`, repo `https://github.com/heroku/node-js-getting-started`, Create Service. CDP Fetch interception on `*/deploy` set to `failRequest ConnectionRefused`.
- State sampled every 50 ms for 2.5 s after the click gave exactly one distinct state: `{"loading":false,"btn":["Deploy"],"inputs":[true,true,true]}` (no loader, no unmount, submit button reads "Deploy", name, repo and branch inputs all disabled).
- Message shown: "The service was created, but starting its deployment failed: Cannot reach the server. Check your connection and try again. The fields are locked because the service exists; Deploy only retries the deployment. Or close this dialog and deploy it from the service panel." The canvas behind it already lists `retry-svc` and "1 service".
- Retry while still blocked (click Deploy): second failed deploy, button still "Deploy". Then interception set to pass-through, click Deploy.
- CDP non-GET log for the whole flow:
  ```
  POST /services 200
  POST /services/cmv5jd12s0009lf3ns1t63m4a/deploy FAILED:net::ERR_CONNECTION_REFUSED
  POST /services/cmv5jd12s0009lf3ns1t63m4a/deploy FAILED:net::ERR_CONNECTION_REFUSED
  POST /services/cmv5jd12s0009lf3ns1t63m4a/deploy 200
  ```
  One POST /services, three deploys, all for the same id. After the 200 the dialog closed ("Service Name" text gone).
- API log (`method=POST path=/services status=200`) has exactly one line per run; `GET /projects/<id>/canvas` returns `['retry-svc']`; `GET /deployments` shows one `queued` deployment for that service id. One service in the DB.
- 1b Back arrow: new project, same flow with deploy failing, then click the back arrow `←` in the dialog header: the dialog closed, `back-svc` appears once on the canvas ("1 service"), non-GET log is `POST /services 200` + one failed deploy, DB has one service `['back-svc']`.

### 2. SEC-9a PASS
Interception: POST `*/services` delayed 3 s (via `Fetch.continueRequest` after a timer), `*/deploy` failed. Each variant on a fresh empty project, filled in and submitted, button reads "Creating..." while in flight, then:
- **Escape** at 0.7 s: dialog closed. After the 3 s resolved: no dialog, canvas shows the service and "1 service". Non-GET log: `POST /services 200` only (the stale submit returns before the deploy, so no deploy is sent; the service exists and needs a manual deploy, as the code comment says). Reopen with the Add button: first step shows (category list and the "What would you like to create?" input), after GitHub Repository > Docker the form is `Create Service` (disabled, empty name), inputs `my-service val="" dis=false`, repo `val="" dis=false`, branch `main`, no "The service was created" text. DB: one service.
- **Backdrop click** at (40,40): identical outcome and DB state.
- **Close during deploy** (deploy request held, then Escape, then the held request failed with ConnectionRefused): dialog closed, no error text appears afterwards, canvas shows the service, reopened form clean and enabled, button `Create Service`, one service in the DB. Non-GET: `POST /services 200 ; POST .../deploy FAILED`.
- **Reopen before the in-flight create resolves** (the nastiest race): Escape at 0.5 s, reopen with Add, GitHub Repository > Docker, type `second-draft`, wait for the 3 s create to resolve. The second dialog afterwards: `my-service val="second-draft" dis=false`, button `Create Service`, no created/deploy-failed text, still open. The first create did not leak a `createdId` or `error` into it. Canvas shows `first-svc`.

### 3. Stale env reveal PASS
Two services (A, B) on one project, both with env `SECRET` (A=`AAA-value-111`, B=`BBB-value-222`). Open A > Env Vars, `?reveal=true` interception set to hold, click the eye (request held; the CDP list shows it pending), close the panel with the x button, open B > Env Vars, release the held A request (200). Result: B's value is not visible, A's value is not in `innerText` or `outerHTML`, B's eye title is "Show value". Then click B's eye: B shows `BBB-value-222`, `AAA-value-111` absent. Reveal requests: A once (held, released), B once.
- Reachability note: the panel renders a full-screen backdrop and returns null when closed, so in the real UI the only way to change service is to close the panel first, which unmounts `EnvVarSection`. That path was verified. The `key={service.id}` remount and the `currentService` ref guard (the case of a service prop changing while the panel stays open) cannot be triggered from the UI, so only the unmount path was observed in the browser; the guard itself was read in the diff, not exercised.

### 4. Background canvas refresh failure PASS
Empty project, dialog auto-opens, then GET `*/canvas` interception set to fail, create a service (deploy passes). States sampled every 40 ms from the click: only `{loader:false, errScreen:false}` throughout (no loader, no error screen). A toast appears: "Could not refresh the canvas. Cannot reach the server. Check your connection and try again." with a dismiss `x`. The page stays (project header `p-s4`, Add button), the dialog completed the deploy (`POST /services 200 ; POST .../deploy 200`) and closed, the toast is gone after 6 s. Reload with interception off shows `bgfail-svc` and does not reopen the dialog (one service). Regression within this check: with the canvas GET failing on a fresh load the full error screen still shows ("Error: Cannot reach the server. Check your connection and try again." + Retry); Retry after unblocking loads the canvas.

### 5. Regression PASS
- 5a: with the initial `/canvas` GET held, `Loading canvas...` is shown and no dialog; after release the loader is gone and the dialog auto-opens. After Escape it stays closed for 2.5 s (it opens once; the page's two canvas GETs are the dev StrictMode double effect).
- 5b: corrupt `cp_token`, load /projects: stays on /projects, exactly one `POST /auth/refresh` (200), cp_token is a JWT again, the refresh token rotated.
- 5c: avatar menu > Sign out: lands on `/`, cp_token and cp_refresh null, one `POST /auth/logout` 200, the old refresh token then gets 401 from `/auth/refresh`.

## NOT verified
- A genuine server-side 5xx deploy failure: the deploy failure was injected as a dropped request (`ERR_CONNECTION_REFUSED`). The message therefore shows the "Cannot reach the server" wording only.
- The same-component stale guard of `EnvVarSection` (see the reachability note in check 3).
- The JS heap contents (that stale state objects are actually gone); only DOM and outerHTML were inspected.
- After a mid-flight close, the service is created but never deployed (no deploy request is sent). This is how the code behaves; I did not judge whether it is the intended product behavior.
- Other browsers, the mobile screen, deployments/logs panels (no worker run), Go tests (UI-only change).

## Cleanup
Chrome, Vite and the API stopped; container `vfy15c_pg` removed with `-v` (no prune); ports 8088, 5178, 9335, 25435, 19092 free. Scripts and the built API binary live only in the session scratchpad (`.../scratchpad/v4`). No repository files changed; `git status` shows only the known untracked files plus this report. Node API 8080, Go worker, real dev DB and encrypt-env were not touched. The only dev services touched were Redis 6380 and ClickHouse 8123 (read connections from the throwaway API).

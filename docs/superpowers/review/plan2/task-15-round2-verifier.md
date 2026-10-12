# Plan 2 Task 15, round 2 - Verifier report (fix commits eb3c353..652f001)

RESULT: FAIL (1 failure: PROD-1 is not fixed in the real browser flow; everything else passes)

## Method
Same as round 1: headless Google Chrome 154 driven over CDP (real mouse and keyboard events), UI served from `git archive HEAD cp-ui` (HEAD = 652f001, Vite on 5177, `VITE_API_BASE=http://localhost:8087`, StrictMode on as in dev), throwaway `postgres:16` (tmpfs, port 25434) migrated with `go run ./cmd/migrate up`, Go API built from the repo and run with APP_ENV=development on 8087 (random JWT_SECRET and ENV_ENCRYPTION_KEY, dev Redis 6380, ClickHouse 8123, metrics 19091). Evidence comes from the DOM, localStorage, CDP Network events and the API request log. df before: 24 GiB free.

## Static checks (cp-ui, working tree = HEAD)
| Check | Result |
|---|---|
| `npm run lint` | PASS: 0 errors, 5 warnings (react-hooks/exhaustive-deps, unchanged style; one is `handleClose` in CreateServiceModal) |
| `npm run build` | PASS: `built in 1.50s` (chunk-size warning only) |
| `npm run check:logic` | PASS: `ok: 30 checks passed` |

## Browser checks
| # | Check | Result |
|---|---|---|
| 1 | PROD-1: failed deploy in Create Service dialog | **FAIL** (details below) |
| 2 | SEC-7: hide drops revealed values, next eye click refetches | PASS |
| 3 | Session-expired notice: amber, single, no flash | PASS |
| 4a | password_too_long wording (73 bytes) | PASS |
| 4b | 404 wording | PASS |
| 4c | signup maxLength | PASS |
| 5a | Corrupt cp_token, reload: one refresh | PASS |
| 5b | Two tabs, 3 rounds: one refresh per round | PASS |
| 5c | Log out via UI | PASS |

### 1. PROD-1 FAIL
Setup: new project (empty canvas, so the Create dialog auto-opens), GitHub Repository > Docker, name `retry-svc`, repo `https://github.com/heroku/node-js-getting-started`, click "Create Service". CDP Fetch interception made the deploy request fail, in two separate runs: (a) `failRequest ConnectionRefused` (the request never leaves Chrome), (b) the request URL rewritten to `/services/00000000-0000-0000-0000-000000000000/deploy`, which the real API answers `404` (API log: `POST /services/00000000-.../deploy status=404`).

Observed, identical in both runs:
- `POST /services` succeeds and the dialog's `onCreated()` runs, which is `loadCanvasData` in ProjectCanvasPage.
- The dialog then shows none of the intended state. Sampling the DOM every 40 ms for 3 s after the click gave exactly one state: `{"m":false (no "The service was created" text),"btn":[] (no submit button),"form":false,"sel":true (the first dialog step, "GitHub Repository / Database", is showing)}`. The page text is the canvas with `retry-svc`, "Offline", "1 service" behind a fresh "select type" dialog. There is no "service was created, deploy failed" message and no "Deploy" button.
- Doing what a user would do next (re-select Docker, type the same name and URL, click Create Service) produced a second service. CDP request log, non-GET only:
  ```
  POST /projects
  POST /services
  POST /services/cmv5iwscs000adh3n541fe5wt/deploy      (failed, intercepted)
  POST /services
  POST /services/cmv5iwvpa000bdh3nba75sycv/deploy      (200)
  ```
  and `GET /projects/<id>/canvas` then lists `retry-svc,retry-svc`. That is the original duplicate-service bug.
- The "Deploy" button label, the retry-only-deploy path and "Back closes" could not be exercised at all, because the component that holds `createdId` and `error` is gone by then.

Likely cause (read from the code, matches the observation): `onCreated` is `loadCanvasData`, which calls `setLoading(true)`. `ProjectCanvasPage.tsx` lines 236-250 `return` a bare "Loading canvas..." div while `loading` is true, so `<CreateServiceModal>` (rendered at line ~620 in the main return) is unmounted. When loading ends it is mounted again with fresh state (`step=select-type`, `createdId=null`, `error=null`) while the parent's `isCreateModalOpen` is still true. The `createdId` and error state added in e0e4000 therefore never survive the refresh that the same commit triggers. A fix has to keep the modal mounted across the refresh (for example do not set `loading` for a background reload, or hold `createdId`/`error` in the parent), and then re-run this check.

### 2. SEC-7 PASS
Service panel > Env tab, two vars added through the UI (`SECRET_A`, `SECRET_B`). Env requests seen by CDP:
- masked state: no plaintext in the DOM, both titles "Show value".
- eye 1 click: one `GET .../env?reveal=true`, `alpha-secret-111` visible.
- eye 2 click (other var): no new request, both visible. Hide 1: no request. Hide 2 (last one): no request, no plaintext in the DOM or `outerHTML`, both titles "Show value".
- eye click again: a second `GET .../env?reveal=true` (API log shows two `env revealed` lines), value visible again.
Not observable from outside: the JS heap itself (`revealed` set to null); only the behavior that follows from it (refetch) was proven.

### 3. Session-expired notice PASS
Logged in through the UI, revoked the refresh token server-side (`POST /auth/logout` returned 200), set cp_token to `corrupt.token.value`, navigated to /projects. API calls: `GET /projects` x2, `POST /auth/refresh` (rejected). Lands on /login. Sampled the DOM every 25 ms: states were `/projects` (no notice), `/login` (no notice yet, before React rendered the route), then `/login` with exactly one `[role=status]` and the text count of "Your session expired" = 1; never 2, never in a red box (0 red elements with that text). Computed style of the notice: `color rgb(245, 158, 11)`, `background rgba(245, 158, 11, 0.1)`, `border rgba(245, 158, 11, 0.25)`. sessionStorage `cp_notice` is cleared after the first render, cp_token/cp_refresh are null, and reloading /login does not show the notice again. A plain visit to /projects with no session redirects to /login with no notice. (StrictMode is on in the Vite dev server, so the effect ran twice with no visible effect.) A wrong password afterwards showed the normal error `Invalid email or password.`

### 4. Wording PASS
- 4a: 73 `a` characters in the signup password: message "Password is too long (the limit is 72 bytes; accented or non-Latin characters take more than one byte)."; no request sent, cp_token/cp_refresh stay null. 37 x `é` (37 characters, 74 bytes) gives the same message. The server itself answers a 73-byte password with `{"error":"password_too_long"}` (curl), the code the new text is keyed on. The browser path only exercised the client-side byte check, not the server response going through `humanizeApiError`.
- 4b: unknown project id: "Error: That project no longer exists." (sentence code "Project not found"). A bare `404 {"error":"not_found"}` (canvas request rewritten by CDP to the unrouted path `/nope`, real server reply): "Error: That item no longer exists. Reload the page."
- 4c: signup inputs: name `maxLength=100`, email `maxLength=254`, password placeholder "At least 8 characters". Typing 120 chars into name leaves 100; typing 255 into email leaves 254.

### 5. Regression PASS
- 5a: corrupt cp_token + reload: stays on /projects, API log shows exactly one `/auth/refresh 200`, refresh token rotated, token is a valid JWT.
- 5b: two tabs navigated in the same Promise.all, 3 rounds: each round one `/auth/refresh 200` in the API log (not two), both tabs on /projects with content rendered, nobody signed out.
- 5c: UI avatar menu > Sign out: lands on `/`, cp_token and cp_refresh null, one `/auth/logout` call, old refresh token then gets 401 from `/auth/refresh`.
- Login with wrong password, then correct password: error shown, then lands on /projects.

## NOT verified
- PROD-1's intended behavior (message text, "Deploy" button, deploy-only retry, Back closing the dialog, a single POST /services): unreachable because of the failure above. Only the component code was read.
- Deploy failure via a genuine server-side 5xx: the failure was injected (dropped request, or URL rewritten to an unknown service, which the real API rejected with 404).
- Server-side `password_too_long` through the UI mapping (the client check fires first); `invalid_request` wording; the `repoUrl required...` sentence mapping.
- Web Locks rejection fallback (SEC-3) and storage-lock expiry clamp (SEC-4): Chrome has Web Locks, and these are covered only by the 30 `check:logic` assertions.
- Canvas toast period/wording changes (PROD-5, STAFF-5) and the position-save toast; the release-note doc commit.
- Other browsers, the mobile screen, deployments/logs panels (no worker running), `go test`/golangci-lint (UI-only change).

## Cleanup
Chrome, Vite, and the API process stopped; container `vfy15b_pg` removed with `-v` (no prune); ports 8087, 5177, 9334, 25434 free. Scratch scripts live only in the session scratchpad. No repository files changed by this run; `git status` shows only the known untracked files plus `task-15-round2-security.md` (another agent's file) and this report. Node API 8080, Go worker, real dev DB and encrypt-env were not touched.

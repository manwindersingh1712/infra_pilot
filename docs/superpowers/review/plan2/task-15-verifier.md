# Plan 2 Task 15 - Verifier report (commits 17d7680..4ffffde)

RESULT: PASS (no failures found; two minor observations below)

## Method
- No built-in-browser or chrome-browser tools exist in this session (ToolSearch found none). I drove the real UI instead with the locally installed Google Chrome 154 (headless, CDP over a node 24 WebSocket script, real mouse/keyboard events via Input.dispatch*/insertText).
- UI was served from `git archive 4ffffde cp-ui` (a clean copy of the commit, node_modules symlinked), because the working tree briefly had an uncommitted edit to `cp-ui/src/lib/refresh.ts` (not mine; it had disappeared by the end of the run). So the tested code is exactly 4ffffde.
- Stack: throwaway `postgres:16` (port 25433, tmpfs), `go run ./cmd/migrate up` (00001-00004 applied), Go API binary built from the repo on 127.0.0.1:8085 (APP_ENV=development, CORS_ORIGINS=http://localhost:5175, random JWT_SECRET/ENV_ENCRYPTION_KEY, Redis 6380, ClickHouse 8123 read-only, metrics 19090), Vite on 5175 with VITE_API_BASE=http://localhost:8085. Evidence of server-side effects comes from the API request log.
- Not touched: Node API 8080, Go worker, real dev DB, encrypt-env. df before: 25 GiB free.

## Static checks
| Check | Result |
|---|---|
| `npm run lint` (cp-ui) | PASS: 0 errors, 5 warnings (react-hooks/exhaustive-deps, pre-existing style) |
| `npm run build` (tsc -b + vite build) | PASS: built in 1.84s (chunk-size warning only) |
| `node scripts/check-api-logic.ts` | PASS: `ok: 27 checks passed` |

## Browser checks
| # | Check | Result | Evidence |
|---|---|---|---|
| 1 | Signup with short password shows the message, makes no tokens | PASS | "Password must be at least 8 characters."; cp_token/cp_refresh both null after the click |
| 2 | Signup with valid password | PASS | lands on /projects; cp_token `eyJhbGci...`, cp_refresh present; log `POST /auth/register 200` |
| 3 | Corrupt cp_token, reload | PASS | page loads /projects; token replaced by a valid JWT, refresh token rotated; log: two `GET /projects 401`, ONE `POST /auth/refresh 200`, then two `GET /projects 200` (single-flight inside a page) |
| 4 | Two tabs refreshing concurrently, 3 rounds | PASS | token corrupted, both tabs navigated in the same Promise.all; each round: both tabs on /projects, token valid, refresh rotated once; log shows exactly 3 `/auth/refresh` calls for 3 rounds (one per round, not two); nobody signed out |
| 5 | API killed during refresh | PASS | corrupt token + API down + reload: stays on /projects, cp_refresh kept, no cp_notice, banner "Cannot reach the server. Check your connection and try again."; after API restart, reload recovers with a new valid token |
| 6 | Refresh returns 503 / 500 / 429 | PASS | (in-page fetch stub for /auth/refresh only) each: stays on /projects, cp_refresh unchanged, no notice, 1 refresh call, message "Could not refresh your session right now. Try again in a moment."; recovery afterwards OK |
| 7 | Refresh token revoked server-side, then a call | PASS | revoked via `POST /auth/logout` (200), corrupt access token, reload: redirected to /login, both keys cleared, notice "Your session expired, please sign in again." shown; UI login then works (cp_token + cp_refresh set) |
| 8 | Log out via UI (avatar menu, Sign out) | PASS | `POST /auth/logout 200` in log; cp_token and cp_refresh null; lands on /; the old refresh token then gets 401 from /auth/refresh |
| 9 | Create-project dialog: empty, 1 char, "   a   " | PASS | Create button disabled for all three |
| 10 | 101 characters typed | PASS | input value length is 100 (maxLength cap), button enabled |
| 11 | Trimmed name sent | PASS | typed "  Alpha Project  ", captured body `{"name":"Alpha Project"}`; project listed |
| 12 | Server error shown in the modal | PASS | real server rejection (name "ab<TAB>cd": control char passes UI check, fails API; log `POST /projects 400`); `role=alert` text "Project name must be 2-100 characters." inside the open modal, typed value kept, alert cleared when typing resumes |
| 13 | Env eye toggle fetches `?reveal=true` lazily | PASS | opening Env tab: only `GET /services/<id>/env` (masked, no value in DOM, dots shown). First eye click: one `GET ...env?reveal=true` (API log `env revealed`), value shown. Hide then show again: no second reveal request. Adding a var reloads the list and resets reveal; the new var reveals correctly after a fresh reveal fetch |
| 14 | Env hints | PASS | panel shows "Names: letters, digits, _ . -.", "Values: a single line, up to 32 KiB. Use base64 ...", "Changes apply to the next deployment: redeploy to apply them."; bad key "bad key!" shows the humanized invalid_env_var message |
| 15 | Canvas toast: failed connect | PASS | API down, real drag handle-to-handle: toast "Could not connect the services. Cannot reach the server. Check your connection and try again." (positive control with API up created the edge) |
| 16 | Canvas toast: failed edge delete | PASS | API down, click edge + Delete: toast "Could not delete the connection. Cannot reach the server..." and the edge is restored (1 edge still present). With API up the same action removes it (0 edges, no toast) |
| 17 | Canvas toast: failed position save | PASS | API down, real node drag: "Could not save the new position. Cannot reach the server..." |

## Observations (not failures)
- Opening the Env tab fires `GET /services/<id>/env` twice. This is React StrictMode double-mounting the effect in dev (`main.tsx` wraps in StrictMode); the production build will do one.
- The service create body used for the test: `{projectId, name, serviceType:"docker", repoUrl:"https://github.com/heroku/node-js-getting-started"}` (4 services ended up created because an earlier script run was retried; harmless in the throwaway DB).

## NOT verified
- Real 5xx/429 from the server on /auth/refresh: simulated with an in-page fetch stub (a docker-paused Postgres only delayed the request ~5.5s and then returned 200). The API-killed case was real.
- Natural access-token expiry (15 min TTL); expiry was simulated by corrupting cp_token, which takes the same 401 path.
- Web Locks fallback (`withStorageLock`, used when `navigator.locks` is absent): Chrome has Web Locks, so only the Web Locks path ran in the browser; the fallback is covered only by the 27 node checks.
- Other browsers (Firefox/Safari), the mobile "Desktop Required" screen, deployments/logs panels (no worker was running), Create Service modal in the UI (services were created via the API).
- Full Go suite (`go test -race`, golangci-lint): out of scope for this UI-only task; only `go build` of cmd/api and `cmd/migrate up` ran.

## Cleanup
Chrome, Vite and the API stopped; container `vfy15_pg` removed with `-v` (no prune); ports 8085/5175/9333 free; scratch files live only in the session scratchpad. No repository files changed (`git status` shows only the known untracked files).

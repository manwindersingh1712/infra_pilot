# Plan 2 plan-end Staff review (spec §13)

Range: `53f064a..HEAD` (HEAD `9b82cff`, 188 commits, 115 non-doc files). Reviewer: Staff. Date: 2026-10-13.

## Mechanical results (run by Staff, `control-plane-go/` and `cp-ui/`)

| Check | Result |
|---|---|
| `go build ./...`, `go vet ./...` | clean |
| `golangci-lint run` | `0 issues.` |
| `go test -race -p 2 ./...` | all packages `ok` (most served from the Go test cache, inputs unchanged) |
| Uncached re-run `go test -race -count=1 -p 2` of outbox, store, db, workflows, cmd/migrate | all `ok` (outbox 6.3 s, store 5.0 s, db 11.5 s, workflows 8.2 s, migrate 6.5 s). No Ryuk or outbox timeout seen this time. |
| `gosec -quiet -exclude=G204 ./...` | clean |
| `govulncheck ./...` | 0 affecting; 1 in a required module, not called |
| cp-ui `npm run lint` | 0 errors, 5 `react-hooks/exhaustive-deps` warnings (CreateServiceModal 171, ServiceDetailsPanel 64/541/558, ProjectCanvasPage 70) |
| cp-ui `npm run build` | OK; one 621 kB chunk (gzip 192 kB) over the 500 kB warning |
| cp-ui `npm run check:logic` | `ok: 68 checks passed` |
| cp-ui `npm audit --omit=dev` | 2 high (react-router / react-router-dom), fix available |

## 1. Plan versus delivered (18 tasks)

| Task | Delivered | Note |
|---|---|---|
| 1 service connections | yes | |
| 2 migration 00003, soft delete, owner scoping, refresh tokens | yes | plus 00004 (token families) added later by CTO ruling; not in plan text |
| 3 stream tickets | yes | |
| 4 exp tokens, refresh, passwords | yes | |
| 5 API skeleton | yes | |
| 6 auth endpoints | yes | |
| 7-10 projects, services, deployments, canvas | yes | |
| 11 logs, tickets, SSE | yes | |
| 12 teardown (worker) | yes | workflow registered in `cmd/worker` |
| 13 `migrate encrypt-env` | yes | far larger than planned (464 lines + runbook) |
| 14 API binary | yes | `TRUSTED_PROXIES` added by ruling |
| 15-17 UI | yes | shared client, SSE, connection panel, delete dialog |
| 18 | Step 1 done (`9b82cff`); Steps 2-6 open | see below |

Silent or undocumented changes: refresh-token families and rotation grace (00004); per-class rate-limit buckets; `TRUSTED_PROXIES`; teardown uses allow-duplicate-failed-only (spec D2 updated); dev login binds 127.0.0.1 only. All are in the spec/decisions. Nothing promised and missing except the Task 18 items below.

## 2. Findings

### [STAFF-PE-1] severity: high (cutover blocker, process)
where: spec §9.2, `decisions.md` 2026-10-11, `START-HERE.md`
problem: The Plan 1 rollback window is open until 2026-10-25. Spec: the API cutover must not start inside it unless the user closes it in writing. `encrypt-env` also makes Node-worker rollback impossible (the Node worker would hand `enc:v1:...` strings to containers) and 00003 gives the frozen Node Prisma schema no `deletedAt`, so a Node API would list soft-deleted services as live ghosts.
fix: Either wait to 2026-10-25 or get the user's written closure of the window. The cutover record must say the Plan 1 rollback is gone from that moment.

### [STAFF-PE-2] severity: high
where: plan Task 18 Step 4 ("the Go worker from Plan 1 keeps running"); `cmd/worker/main.go`
problem: The worker running now was started before Task 12. It does not register `TeardownWorkflow` and its `Starter` has no `teardown.requested` case. `DELETE /services/:id` would soft-delete the row and queue an event that the old worker rejects ("unknown event type"), retries and finally marks `dead`. The service's containers and route stay up. Also the new worker needs the new `Temporal` activity dependency.
fix: Rebuild and restart the worker after `migrate up` and before the API accepts traffic. Add a smoke check: delete a throwaway service, expect container and route gone and workflow `teardown-<id>` Completed (backlog STAFF-R2-1: the real binary has no teardown test).

### [STAFF-PE-3] severity: high
where: plan Task 18 Step 4 `go run ./cmd/migrate encrypt-env`
problem: A bare call writes nothing (exits 2). The plan text contradicts `runbook/encrypt-env.md` (key-fingerprint check, `--dry-run`, `--yes`, repeat until `conflicts=0`, final dry run). The runbook also says the API and worker "do not log a fingerprint"; both now do (`envKeyFingerprint` in `cmd/api` `logStartup` and `cmd/worker` `logEncryptionKey`), which is the easiest key-match check.
fix: Replace Step 4's line with a pointer to the runbook sequence; fix the runbook sentence.

### [STAFF-PE-4] severity: medium
where: rollback story (plan Step 6, spec §9.3, release note)
problem: Three different statements. Real rollback tiers are below in section 4. Key facts: Node API cannot show decrypted env values after `encrypt-env` (lists ciphertext) but Go worker accepts plain and encrypted, so a Node API written plain value still deploys. Node worker cannot. A Go API rollback to the previous Go release does not exist for the API (Plan 2 is the first Go API), so "previous Go release" means the Plan 1 worker plus Node API, which is the case above.
fix: Adopt the table in section 4 in `decisions.md`.

### [STAFF-PE-5] severity: medium
where: cutover Step 4 ordering, macOS port 8080
problem: Dev login binds `127.0.0.1:8080`; Node listens on wildcard 8080. macOS allows a specific-address bind next to a wildcard bind, so the Go API could start while Node still runs and take loopback traffic silently.
fix: Stop Node, then `lsof -nP -iTCP:8080 -sTCP:LISTEN` must be empty before starting Go, and show exactly one (Go) afterwards.

### [STAFF-PE-6] severity: medium
where: nginx / `TRUSTED_PROXIES`; `release-note-plan2.md:19`; `plans/...plan3-cutover.md`
problem: On the dev stack nothing proxies the API (UI uses `VITE_API_BASE=http://localhost:8080`; nginx only serves `svc-*.conf` routes and a default 404). So `TRUSTED_PROXIES` stays EMPTY for this cutover and the nginx items (XFF `$remote_addr`, `proxy_buffering off`, `proxy_read_timeout >= 75s`, no `ticket=` in access logs) do not apply yet. But no task owns them: the release note says "Plan 2 Task 18" adds the nginx route (it does not) and Plan 3 has no XFF, buffering or log-format step (grep found none). Also a single container IP such as `172.18.0.5` changes when nginx is recreated; the API then silently falls back to one shared bucket (login lockouts, spec risk PROD-6).
fix: Add the nginx API route (and a fixed `ipv4_address` for nginx, or a startup/runtime warning when `X-Forwarded-For` arrives from an untrusted peer) to Plan 3; fix the release-note sentence.

### [STAFF-PE-7] severity: medium
where: `cp-ui/package.json`
problem: `npm audit --omit=dev`: 2 high on react-router(-dom), flagged "before cutover" in SEC-6 and SEC-9 and still open. Mostly SSR/RSC advisories (not used), but the `<Link>`/`navigate` open-redirect applies to an SPA.
fix: Bump react-router-dom as its own commit (review the diff, no blind `audit fix`), then lint, build, check:logic.

### [STAFF-PE-8] severity: medium
where: `docs/superpowers/release-note-plan2.md`, `.claude/CLAUDE.md`
problem: The release note is still a stub. Missing: Node must not serve this DB after 00003 (STAFF-6); DB ports no longer public (Product item in Task 18 Step 3); a deploy without `commitSha` builds the configured branch not `main`; env key charset and 32 KiB cap; foreign ids return 404; `DELETE` returns 202; the 429 behaviour. CLAUDE.md API section is stale (`/logs/:deploymentId`, `/canvas/*`, `/readyz` "DB and AMQP" is now DB and Redis); API env defaults (`API_PORT` 8080, `CORS_ORIGINS` default `http://localhost:5173`, singular `CORS_ORIGIN` fallback, `API_METRICS_ADDR` 127.0.0.1:9090) and the JWT_SECRET rule (placeholder refused and at least 32 bytes outside development) are not documented. Note for users: opening the UI as `127.0.0.1:5173` fails CORS.
fix: Product completes the note; update CLAUDE.md at cutover (or Plan 3).

### [STAFF-PE-9] severity: medium
where: `.github/workflows/ci.yml`
problem: CI is stale for Plan 2: the `ui` job runs lint and build but not `npm run check:logic` (68 checks; it needs Node 22.18 or newer, CI pins Node 20) and no `npm audit`. START-HERE still lists CI RED (`TestConcurrentMigrates`); `53f064a` fixed it but CI has not run on any Plan 2 commit (origin/go-migration is at `8846c3d`, and 188 commits are unpushed or unchecked). Passing locally, not proven on a runner.
fix: Bump Node to 22+ in the ui job, add `check:logic` and `npm audit --omit=dev --audit-level=high`; push only with user approval and watch the first run.

### [STAFF-PE-10] severity: medium
where: `cmd/api/main.go` `run`, `control-plane-go/api`
problem: (a) `run()` wiring (RequireAPI, secrets box, tickets, Stream.Shutdown, TrustedProxies, CORS) has no test; `cmd/api` tests cover only the helpers. Reviewed by eye: wiring is correct. (b) An untracked 28 MB binary `control-plane-go/api` sits in the tree and is not git-ignored; one `git add -A` commits it. (c) The API requires ClickHouse `Init` and Redis at startup (as Node did) so a ClickHouse outage blocks login.
fix: (a) one smoke test or a script check in the checklist; (b) delete it and add `/control-plane-go/api` to `.gitignore`; (c) accept, note in runbook.

### [STAFF-PE-11] severity: low
where: UI dead code
problem: `components/ServiceCanvas.tsx` is imported nowhere; `hooks/useCanvas.ts` is used only by it. Both were ported to the new client, so they cost maintenance and tests of nothing. The 5 lint warnings are `exhaustive-deps`, not new defects (CI does not fail on them). Bundle 621 kB is above the warning limit. No copies of the old `apiFetch` helper or `socket.io-client` remain (only `src/lib/api.ts` calls `fetch`).
fix: Delete the two files (Plan 3 cleanup) and code-split later.

### [STAFF-PE-12] severity: low
where: legacy data shapes
problem: (a) Node `bcrypt` truncated passwords at 72 bytes; the Go login with a longer password fails (401) for a Node user who registered with one, and login runs `validEmail` so a legacy odd email gets 400 instead of 401. (b) A plain legacy env value that starts with `enc:v1:` is a "suspect" row: Go reveal returns 500 for the whole list (one bad row) and the worker fails that deploy; `encrypt-env` detects it, the runbook handles it. (c) The compare script drops `null` fields and treats null equal to missing; the UI must tolerate the Go shapes (nullable `repoUrl`, `name`). (d) `compare-api.sh` covers only the dev user's first project and service. (e) Migration 00004 (`familyId = id`, then `NOT NULL`) is safe with existing rows; 00003 index creation is on small tables. Both down-migrations exist.
fix: Note (a) and (b) in the release note; run the compare against a project with a repo URL, env var and a deployment as planned.

### [STAFF-PE-13] severity: low
where: post-cutover cleanup
problem: Deferred items that need an owner and a date: remove `legacyDefaultRef` in `builder.go` (only once no pending deployment has commit `main`), remove the blanket `TenantCaps` for non-managed services, `APP_ENV` falling back to `NODE_ENV` warning, UI `user@example.com` placeholder, deploy idempotency, deployment list `take 50`. Stale processes are unsupervised (started with `nohup`).
fix: Put them in the Plan 3 task list with owners.

Consistency checks that passed: metrics (API :9090, worker :9091, route-pattern labels); every API log line carries `requestId`, the ticket never appears in logged paths; only `store` writes SQL; teardown payload parsing is strict; error codes match Node strings (`service_not_found`, `Project not found`, `unauthorized`); Node route set is a subset of the Go route set; nothing left of Socket.io on either side.

## 3. Consolidated cutover checklist (ONE ordered list)

Owners: USER = Manwinder (approval, secrets), ORCH = orchestrating session, ENG = engineer subagent, VER = Verifier, PROD = Product, SEC = Security, CTO. Every step that stops or starts the running system needs the user's approval first.

| # | Step | Owner | Verify | Rollback if it fails |
|---|---|---|---|---|
| 0 | Gate. Plan 1 window closed (after 2026-10-25) or closed in writing by USER; STAFF-PE-1..3, 5, 7, 8 resolved; Security plan-end scan; CTO GO in `decisions.md` | USER, CTO, SEC | written entries | do not start |
| 1 | Pre-flight: `git status` clean except known untracked; delete `control-plane-go/api` binary; `df -h` shows >= 10 GiB; `compare-api.sh` run (Node :8080, Go :8081 with `APP_ENV=development`) and every line `same`; Task 18 Step 2 result recorded | ORCH, VER | script exit 0 | fix handler with a test, rerun |
| 2 | Bump react-router-dom, then lint, build, check:logic; push the branch and see CI green (needs USER ok to push) | ENG, USER | CI green | revert bump |
| 3 | Backups: `pg_dump -Fc` (runbook "Backup commands", owner-only perms, `pg_restore --list`), copy of `ENV_ENCRYPTION_KEY`; record `--print-key-fingerprint` | USER, ORCH | dump listed; fingerprint noted | none needed |
| 4 | Legacy data queries: suspect `enc:v1:` values, cross-project connection rows count, users with >72-byte passwords or odd emails, `Deployment.commitSha = 'main'` pending rows, DB timezone UTC | VER | numbers recorded | resolve per runbook before going on |
| 5 | Stop Node API (no new plain-text writes). `lsof -nP -iTCP:8080 -sTCP:LISTEN` empty | USER | no listener | restart Node: `cd control-plane && nohup npm run dev:api` |
| 6 | `go run ./cmd/migrate up` (00003, 00004) | ORCH | `migrate status` shows 4 | restore the step 3 dump; start Node |
| 7 | Encrypt env: runbook sequence (fingerprint, `--dry-run` suspect=0 too_large=0, `--yes` until `conflicts=0`, final dry run `would_encrypt=0`) | ORCH, USER | final dry run | restore step 3 dump (the ONLY way back to plain text); POINT OF NO RETURN for the Node worker |
| 8 | Rebuild and restart the Go worker (kill the pid in `~/cp-logs/go-worker.pid`, `nohup go run ./cmd/worker`) AFTER step 6 | ORCH | log shows `worker up`, key fingerprint equals step 3 | start previous worker binary (git stash not needed: checkout `53f064a` build) |
| 9 | Start Go API: `APP_ENV=development` (dev stack), `JWT_SECRET` from `.env`, `TRUSTED_PROXIES` EMPTY, `nohup go run ./cmd/api > ~/cp-logs/go-api.log`, pid file | ORCH | `api listening addr=127.0.0.1:8080`, `lsof` one listener, `/readyz` 200, key fingerprint equals worker's | stop it; if before step 7 start Node again; after step 7 see section 4 |
| 10 | UI: the new build is required (no Socket.io in Go). Dev: restart `npm run dev` and hard reload all tabs | ORCH | login page appears for old tabs | revert UI only with a Node API (not possible after step 7) |
| 11 | Plan checklist items 1-11 (forced re-login, auth/refresh, ownership 404, deploy and redeploy, env masked/reveal and `enc:v1:` in `psql`, SSE logs with restart reconnect, Redis connection from project network, delete with and without purge incl. nginx route removed and workflow Completed, 6th wrong login 429, production-mode start refusal and no `/dev/login`, metrics) plus: canvas drag persists, 1/101-char/emoji project names, legacy services still list, connections | VER, PROD | each result recorded in the task file | any failing item with data impact: section 4 |
| 12 | Product release note finished (STAFF-PE-8) and pasted into `decisions.md`; CLAUDE.md updated; START-HERE tracker; cutover record with date, "Node API frozen" and the rollback table | PROD, ORCH | commit | n/a |
| 13 | Later: remove `legacyDefaultRef` and blanket `TenantCaps` (after no `main` deployments are pending), nginx API route in Plan 3 with fixed nginx IP, `TRUSTED_PROXIES`, `proxy_buffering off`, log format without query strings | ENG (Plan 3), CTO | Plan 3 gate | n/a |

## 4. Rollback table

| Situation | Action |
|---|---|
| Fails before step 6 | Nothing changed except Node stopped: start Node. |
| Fails at step 6 (migration) | Goose down is defined for 00003/00004; simpler: restore the step 3 dump, start Node. |
| Go API misbehaves after step 6 but before step 7 | Stop Go API, start Node. Node ignores the new columns. Soft-deleted services (from Go deletes) would show as ghosts: restore the dump if any delete was done. |
| Go API misbehaves after step 7 (the hard case) | Node cannot read `enc:v1:` (the UI would show ciphertext, and a Node worker would deploy ciphertext). Options in order: (1) fix forward in the Go API, keep Go worker; (2) run a Go API of the previous commit; (3) restore the step 3 dump (loses all data written since: users, deployments, env edits) and start Node API plus Node worker. The Go worker alone keeps working with mixed plain and encrypted values, so a Node API in front of the Go worker is acceptable as a stopgap only for read and deploy flows, not for the env tab. |
| Teardown stuck | `runbook/teardown-stuck.md`. |
| Wrong `ENV_ENCRYPTION_KEY` suspected | Stop; do not write; compare fingerprints; restore dump if encrypted with a wrong key. |

## 5. Gaps flagged (summary)

Plan Step 4 omits the worker restart and the real encrypt-env procedure; no owner for nginx API route, XFF, buffering and log redaction; Plan 1 rollback window conflict; release note stub; react-router audit; CI does not run check:logic and has not run on Plan 2; untracked 28 MB binary; no supervision for Go API and worker processes.

## 6. Recommendation

RESULT: CHANGES REQUESTED (high 3, medium 7, low 3)

**Recommendation: NO-GO today; GO once the conditions below are met.** The code itself is in good shape (build, vet, lint, gosec, govulncheck, UI check all clean, tests pass uncached). The blockers are process and plan text, not code:
1. Plan 1 rollback window closed on or after 2026-10-25, or closed in writing by the user (PE-1).
2. Plan Task 18 Step 4 corrected: worker restart after migration, real encrypt-env sequence (PE-2, PE-3); Node-dead and single-listener check (PE-5).
3. react-router bump done, CI green on a pushed branch (user approval to push) (PE-7, PE-9).
4. Release note completed and the rollback table in section 4 recorded in `decisions.md` (PE-4, PE-8).
With these, and the user's approval of the cutover itself, GO.

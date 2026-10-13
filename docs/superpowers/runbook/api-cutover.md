# Plan 2 API cutover runbook

Source: `docs/superpowers/review/plan2/plan-end-staff.md` sections 3 and 4 (consolidated by the Staff plan-end review, 2026-10-13), amended by the orchestrator with the Security plan-end items (`plan-end-security.md`). **This replaces Task 18 Steps 4 and 5 of the Plan 2 text where they differ** (Step 4's bare `migrate encrypt-env` writes nothing; the Go worker must be rebuilt and restarted AFTER `migrate up`; the Node API must be confirmed dead with `lsof` before the Go API binds :8080). Nothing here starts without the user's written approval for the step.

Extra items from Security that belong in the steps below:
- Step 0: the user closes the Plan 1 rollback window in writing (or waits until 2026-10-25).
- Step 3: the backup and a copy of `ENV_ENCRYPTION_KEY` are stored OFF this machine's data volume (user's choice of location) and the dump restore is rehearsed once into a throwaway Postgres (`pg_restore` into a scratch container) before Step 5.
- Step 9: set `APP_ENV=development` explicitly, not through the `NODE_ENV` fallback. For any non-loopback exposure: `APP_ENV=production`, a fresh `JWT_SECRET` (`openssl rand -base64 32`), `CORS_ORIGINS` set, and rotate the dev placeholder secret.
- Step 11 additions: grep the API/nginx logs for `ticket=` (must be empty); CORS preflight from the UI origin including a 429 `Retry-After` readable; teardown smoke test with the REAL worker (containers, route, volume, Temporal `teardown-<id>` Completed); Node-era Mongo containers bound to `0.0.0.0` checked and accepted or fixed; the Task 17 worker-side checks (`redis-cli -u '<Local URL>' ping`, enc:v1 password in the Connection tab).
- Rollback rehearsal: restore step-3 dump into a scratch Postgres and start the Node API against it once before the real cutover.

## Ordered checklist

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

## Rollback table

| Situation | Action |
|---|---|
| Fails before step 6 | Nothing changed except Node stopped: start Node. |
| Fails at step 6 (migration) | Goose down is defined for 00003/00004; simpler: restore the step 3 dump, start Node. |
| Go API misbehaves after step 6 but before step 7 | Stop Go API, start Node. Node ignores the new columns. Soft-deleted services (from Go deletes) would show as ghosts: restore the dump if any delete was done. |
| Go API misbehaves after step 7 (the hard case) | Node cannot read `enc:v1:` (the UI would show ciphertext, and a Node worker would deploy ciphertext). Options in order: (1) fix forward in the Go API, keep Go worker; (2) run a Go API of the previous commit; (3) restore the step 3 dump (loses all data written since: users, deployments, env edits) and start Node API plus Node worker. The Go worker alone keeps working with mixed plain and encrypted values, so a Node API in front of the Go worker is acceptable as a stopgap only for read and deploy flows, not for the env tab. |
| Teardown stuck | `runbook/teardown-stuck.md`. |
| Wrong `ENV_ENCRYPTION_KEY` suspected | Stop; do not write; compare fingerprints; restore dump if encrypted with a wrong key. |


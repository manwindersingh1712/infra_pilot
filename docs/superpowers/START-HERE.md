# Go + Temporal Migration: Start Here

This is the single entry point for migrating the backend from Node.js to Go with Temporal. Give this file to any new Claude session ("read `docs/superpowers/START-HERE.md` and continue"). It points to everything else and records where the work stands.

---

## 1. What we're doing

The Node/TypeScript backend in `control-plane/` is being rewritten in Go in `control-plane-go/`. Temporal replaces RabbitMQ. The rewrite also fixes the security and correctness problems listed in the design spec.

**The Node code is never deleted.** It becomes a frozen reference, tagged `node-final`.

## 2. Where everything is

| What | File |
|---|---|
| **Design spec** (read first: decisions, architecture, security, rollout) | `docs/superpowers/specs/2026-10-07-go-temporal-design.md` |
| **Plan 1**: foundation + Temporal worker (27 tasks) | `docs/superpowers/plans/2026-10-07-go-plan1-worker.md` |
| **Plan 2**: Go API, auth hardening, UI changes (18 tasks) | `docs/superpowers/plans/2026-10-07-go-plan2-api.md` |
| **Plan 3**: images, prod compose, retire RabbitMQ/MinIO, freeze Node, docs (5 tasks) | `docs/superpowers/plans/2026-10-07-go-plan3-cutover.md` |
| Role brief: **Engineer** (the only role that writes code) | `docs/superpowers/agents/engineer.md` |
| Role brief: **Security reviewer** | `docs/superpowers/agents/security.md` |
| Role brief: **Staff engineer** | `docs/superpowers/agents/staff.md` |
| Role brief: **Product reviewer** | `docs/superpowers/agents/product.md` |
| Role brief: **CTO** (arbitration, go/no-go) | `docs/superpowers/agents/cto.md` |
| Role brief: **Verifier** (tests and real end-to-end checks) | `docs/superpowers/agents/verifier.md` |
| CTO decisions log | `docs/superpowers/review/decisions.md` |
| Deferred / low-severity findings | `docs/superpowers/review/backlog.md` |
| Per-task review records | `docs/superpowers/review/<plan>/task-<n>.md` (created during execution) |
| **Session logs** (read the latest before resuming) | `docs/superpowers/review/session-log-2026-10-07-to-09.md` |
| Product roadmap the design must not block | `SAAS_ROADMAP.md` |
| Frozen Node backend (never edit or run after Plan 3) | `control-plane/` (see `control-plane/FROZEN.md` once Plan 3 adds it) |

## 3. How the work is executed (team orchestration)

The full rules are in spec §13. In short:

1. **For each task:**
   1. A fresh **Engineer** subagent implements the task exactly as written: test first, then code, then commit.
   2. In parallel, these review the commit, each using its brief from `docs/superpowers/agents/`:
      - **Security**: `govulncheck`, `gosec`, `gitleaks`, and a diff review;
      - **Staff Engineer**;
      - **Verifier**: full test suite plus a real run of the feature;
      - **Product**: only when the task changes something users see.
   3. Fix rounds:
      - Critical, high and medium findings go back to a fresh Engineer to fix, then get re-checked.
      - Low findings go to `backlog.md`.
      - Disagreements, or anything that would change the spec, go to the **CTO**, who decides and records it in `decisions.md`.
      - After 3 fix rounds without a clean result, stop and ask the user.
   4. The next task starts only when the Verifier passes and nothing critical, high or medium is open.
2. **At the end of each plan:**
   1. Security and Staff review the whole plan's diff.
   2. Product walks through the cutover checklist.
   3. The CTO gives go/no-go.
   4. **The user approves every cutover.** Cutovers stop and start the running system.
3. **Execution skill:** use `superpowers:subagent-driven-development` with the reviewer steps above.

## 4. Before starting

The checks below were last done on 2026-10-07 (spec §14 has the full table).

- [ ] **Docker Desktop is running** (`docker info` works).
- [ ] **Tools** (Plan 1, Task 1 installs them, with your approval): `brew install go golangci-lint gosec gitleaks`, then `go install golang.org/x/vuln/cmd/govulncheck@latest`.
- [ ] **GitHub:** `gh auth status` shows you logged in (already true).
- [ ] **Optional:** a Docker Hub token stored as GitHub secrets `DOCKERHUB_USERNAME` / `DOCKERHUB_TOKEN`, to avoid image-pull rate limits.
- [ ] **No paid third-party services or API keys are needed.**

## 5. Progress tracker

> **Last session (2026-10-07 to 2026-10-09):** see `review/session-log-2026-10-07-to-09.md` (now also covers the 2026-10-09/10 session at the bottom), which covers what was built, the CTO decisions, operational changes, process problems and how to resume. Plan 1 resumed 2026-10-09 at Task 10.

Update this section as the work moves. The orchestrating session should tick boxes and fill in dates at each plan gate.

| Plan | Status | Started | Gate passed | Cutover done | Notes |
|---|---|---|---|---|---|
| Plan 1 — worker | Done; cutover completed 2026-10-11 | 2026-10-07 | 2026-10-10 | 2026-10-11 (rollback window until 2026-10-25) | | Rollback window: 2 weeks after its cutover |
| Plan 2 — API + UI | In progress: Tasks 1-15 done and reviewed (2026-10-11/12); Task 16 (SSE logs hook) next | 2026-10-11 | | | Point of no return for Node; take a DB backup first |
| Plan 3 — hardening | Not started | | | | Requires Plan 1's rollback window to be closed |

- **Branch:** `go-migration` (created 2026-10-07 off `go`; first commit holds these docs).
- **STATE AT HANDOFF (2026-10-11): PLAN 1 DONE AND CUT OVER.** All 27 Plan 1 tasks passed review; the Go worker runs the dev stack; checklist items 1-5, 7, 8, 10 passed, item 9 passed for the Redis deployment, item 6 N/A (record: `review/decisions.md` "2026-10-11 Plan 1 worker cutover", `review/plan1/task-27.md`). **Rollback window open until 2026-10-25**; the Plan 2 API cutover must not start before then unless the user closes it in writing, and always needs the user's approval. Commits made during the cutover: `f4a2b89` legacy default ref `main` treated as the branch tip (Node API defaults commitSha to main; all existing services use `master`), `c84dc1c` `TenantCaps` (CHOWN/SETUID/SETGID) for all non-managed services, `541e8c3` test race fix, `e690232` Docker `cp_redis` published on 127.0.0.1:6380 (a Homebrew Redis owns 6379; both `.env` files use `redis://localhost:6380`). Remove `legacyDefaultRef` and the blanket `TenantCaps` after the Plan 2 API cutover (backlog).
- **OPEN: GitHub CI is RED.** Run 38102630592 on `e690232` (pushed 2026-10-11, the first CI run since 2026-10-07): `secrets` and `ui` jobs passed; the `go` job failed at `go test -race ./...` on ONE test, `TestConcurrentMigrates` in `control-plane-go/internal/db` (timed out after 300 s; package 331 s). It passes locally many times, so the suspected cause is a slow/contended runner for the Docker/testcontainers DB test (unconfirmed). FIRST ACTION in the next session: read that test, run it locally with `-race -count=5`, find the root cause (migration advisory-lock wait, testcontainers start time, subtest timeout), fix it, run the CI dry run, then ask before pushing (the user approved pushing the branch only; no PR unless asked).
- **PLAN 2 STATE UPDATE 2 (2026-10-12): Task 15 (UI shared API client, refresh, humanized errors, create-service retry) PASSED three review rounds (`review/plan2/task-15-closeout.md`). Task 16 is next and has BINDING requirements in backlog.md (useLogs.ts through the shared client, remove socket.io-client, Product PROD-1/2/3 for the SSE hook, spec/plan text for the `end` event must be amended before the Engineer starts). UI verification method that works: headless Chrome over CDP against a throwaway Postgres + Go API + Vite (see `review/plan2/task-15-round3-verifier.md`); no browser tool exists in subagents. Subagents can hit the account session limit (resume with SendMessage after it resets). Commits after `388ba1e` are still NOT pushed. **PLAN 2 STATE UPDATE (earlier): Task 14 (cmd/api, TRUSTED_PROXIES, key fingerprint, loopback dev-login bind) PASSED two review rounds (`review/plan2/task-14-closeout.md`); Task 15 (UI) is next; commits after `388ba1e` are still NOT pushed (ask first). Older state follows. **PLAN 2 STATE (2026-10-12): Tasks 1-13 PASSED review (13 = `migrate encrypt-env`, runbook `docs/superpowers/runbook/encrypt-env.md`, never run against the real DB yet) (canvas/auto-layout; legacy cross-tenant connection leak found and fixed; log endpoints, stream tickets and SSE; restartable TeardownWorkflow with runbook `docs/superpowers/runbook/teardown-stuck.md`), Task 14 (cmd/api: wire everything; owes TRUSTED_PROXIES, server timeouts, Stream.Shutdown context, DevLogin from APP_ENV, key fingerprint log line, see backlog) is next, then 15-17 (UI), 18 (compare + cutover). (Task 12 was owed: delete EnvVar rows + scrub repoUrl, cancel in-flight `deploy-<id>` workflows and fail open deployments, remove the service's ServiceConnection rows, GetServiceAnyState, starter mapping for `teardown.requested`, purgeData; read backlog.md first), 13 (encrypt-env), 14 (cmd/api), 15-17 (UI), 18 (compare + cutover).** Branch pushed up to `388ba1e` (CI green); later commits are NOT pushed: ask before pushing. Per-task close-outs: `review/plan2/task-<n>-closeout.md`. Run Go tests with `-p 2` locally (unrestricted parallelism overloads Docker Desktop and causes testcontainers timeouts; CI is fine). Older text follows (Tasks 1-6): Records: `review/plan2/task-<n>.md` (Task 6 round 2 is in `task-6-round2-{security,staff,verifier}.md`), CTO rulings in `review/decisions.md` ("2026-10-11 Plan 2 Task 6 rulings": refresh-token families, 10 s grace window, two rate-limit buckets, password codes, TRUSTED_PROXIES in scope for Task 14/18), carry-forwards in `review/backlog.md` (read the Plan 2 sections before each task). Commits since the CI fix (`53f064a`, pushed, CI green) are NOT pushed: ask the user before pushing. **Migrations `00003` is applied to the dev DB; `00004_refresh_family.sql` is NOT (dev DB shows it pending; apply it in a controlled step before the API runs against dev).** CI-relevant rules learned: run gitleaks from the REPO ROOT (`gitleaks git --redact`; it reads `.gitleaksignore` from the cwd); no hard-coded JWTs in tests (build them at runtime); golangci-lint must be 0 issues (CI runs it); `pgxpool` defaults to max(4, NumCPU) connections, so CI's 4-core runner behaves differently from this Mac. Review process: reviewers write to SEPARATE files (parallel appends to one file overwrote the Product section once); Engineers stall on the 10-minute watchdog during long fix rounds: tell them to run slow things in the background and commit item by item, resume with SendMessage, and verify with fresh reviewers. **Cutover blockers recorded for Task 18:** SEC-1 trusted proxies / shared rate-limit bucket behind nginx; Tasks 6 and 15 must ship together (old UI has no refresh); `legacyDefaultRef` and blanket `TenantCaps` removal; the live Node-token check against `auth.Verify` was never run (needs the user's go-ahead). Plan 1 rollback window is still open until 2026-10-25.
- **DISK / DOCKER INCIDENT (2026-10-12):** the Mac's data volume hit 99% (2.2 GB free) during Plan 2 test runs; the Go build cache (`~/Library/Caches/go-build`) had grown to 14 GB (every `-race` test build of many packages) and Docker Desktop's VM stopped and hung on restart (stale `com.docker.backend`). The user approved `go clean -cache` (now ~16-40 GB free) and a force-kill of the single hung backend; `docker compose up -d` brought the `cp_*` stack back with all data intact (5 users, 9 projects, 14 services, 37 deployments; migrations unchanged, `00004` still pending); the Node API (`npm run dev:api`, log `~/cp-logs/node-api.log`) had to be restarted by hand (it crashes on an AMQP heartbeat error when RabbitMQ drops); the Go worker recovered by itself. Rules: check `df -h /System/Volumes/Data` before long test sessions; ask before `go clean -cache` (regenerable, safe) but NEVER prune Docker images/volumes without asking; if Docker hangs: quit the app, `kill -TERM` then (with the user's OK) `kill -9` only the stuck `com.docker.backend` pid, `open -a Docker`, `cd control-plane && docker compose up -d`, then restart the Node API.
- **Live processes (started detached, not under any supervisor):** Go worker (log `~/cp-logs/go-worker.log`, pid file `~/cp-logs/go-worker.pid`) and Node API (`~/cp-logs/node-api.log`). After a reboot: start Docker Desktop, `cd control-plane && docker compose up -d`, then `cd control-plane-go && nohup go run ./cmd/worker > ~/cp-logs/go-worker.log 2>&1 &` and `cd control-plane && nohup npm run dev:api > ~/cp-logs/node-api.log 2>&1 &`. Dev JWT for curl: `~/cp-logs/.dev-token`. Backups (dev data, stay on this Mac by the user's choice): `~/cp-backups/pre-go-cutover-2026-10-10.dump` and `env-encryption-key-2026-10-10.txt`.
- **User decisions (2026-10-11):** the 2 Mongo services are test deployments, leave them alone (no Node-era Mongo container exists; do not redeploy); backups stay local; Redis moved to Docker on 6380; push the branch only; **start Plan 2 next** (after the CI fix). Plan 2: `docs/superpowers/plans/2026-10-07-go-plan2-api.md` (18 tasks; Tasks 15-17 are the UI changes: one shared API client with token refresh, SSE logs with tickets, DB connection panel + delete service). Use the same team orchestration (spec §13), one fresh Engineer per task, Security/Staff/Verifier reviews, CTO rulings in `review/decisions.md`. Commit attribution: `Co-Authored-By: Claude Sonnet 5.5`. Environment: subagents stall on the 10-minute watchdog when waiting on slow tests (run DB/Docker tests in the background with a log and poll; commit item by item; resume with SendMessage); macOS has no `timeout`; never prune Docker/Go caches or images without asking; stage by explicit path (never `git add -A`; `ruvector.db`, `agentdb.rvf*`, `README_NEW.md` are untracked and must stay so).
- **Environment (2026-10-10):** disk is tight (~4.6 GB free): never prune caches/images without asking; local images mongo:7, redis:7-alpine, nginx:alpine, busybox are present. macOS has no `timeout` command. Subagents stall on the 10-minute watchdog when they wait on slow tests: tell them to run DB/Docker tests in the background with a log file and poll, commit item by item, and send progress messages (resume stalled agents with SendMessage; nothing is lost between commits).
- **govulncheck can hang** on its network fetch (one run hung for an hour; the retry took 3 s). Always run it with a hard limit, e.g. `perl -e 'alarm 180; exec @ARGV or exit 127' "$(go env GOPATH)/bin/govulncheck" ./...`, and retry once. **Use the full path and `or exit 127`:** govulncheck is not on the agent shell PATH, and the old wrapper turned "command not found" into exit 0, so the "govulncheck exit 0" results recorded for Tasks 9 and 10 before 2026-10-09 are not trustworthy (see `review/plan1/task-10.md`).
- **Agent stalls:** if Engineers stall on plan-verbatim code, the orchestrator may commit it after running the CI checks (user ruling, `decisions.md` 2026-10-09). Keep the Mac awake during sessions (`caffeinate -i`); macOS sleep pauses long runs.
- **Environment note:** subagents on this machine are often killed by the 10-minute stream watchdog during long commands. If a reviewer stalls twice, the orchestrator may run that reviewer's mechanical checks itself (background commands, throwaway files outside the repo) and record it in the task file.
- **Inserted tasks:** 1a (cp-ui lint/build baseline), 5a (db and test-helper hardening). See `review/decisions.md`.
- **Dev DB:** baselined by goose on 2026-10-07 (pre-baseline backup in that session's scratchpad). Never run `npm run prisma:migrate` again.
- **Pushed:** `go-migration` first pushed 2026-10-07 (CI green). Every later push needs user approval.

## 6. Prompts to paste into a new Claude session

Use **one session per plan**. The plans are long, and a fresh session keeps the context clean.

**Start or continue a plan** (change the plan file name as needed):
```
Read docs/superpowers/START-HERE.md, then execute
docs/superpowers/plans/2026-10-07-go-plan1-worker.md task by task.
Follow the team orchestration in spec §13: one Engineer subagent implements
each task using docs/superpowers/agents/engineer.md; after each task dispatch
Security, Staff and Verifier (plus Product when the task changes anything users
see) in parallel using their briefs in docs/superpowers/agents/; fix findings
until the task is clean; record each task in docs/superpowers/review/.
Never modify or delete code in control-plane/ except where a task says so.
Stop and ask me before any cutover step. Update the progress tracker in
START-HERE.md as you go.
```

**Resume after an interruption:**
```
Read docs/superpowers/START-HERE.md. Check the progress tracker, git log on the
go-migration branch and docs/superpowers/review/ to find the last task that
passed its review gate, then continue the same plan from the next task with the
same orchestration.
```

**After the migration is finished** (new features or follow-up work):
```
Read docs/superpowers/START-HERE.md and the design spec for context. The
migration is complete; review docs/superpowers/review/backlog.md and
SAAS_ROADMAP.md, then propose the next piece of work. Use the same team
orchestration (spec §13) and role briefs for any implementation.
```

## 7. Rules every session must follow

0. **Save context before it runs out.** When the session's context usage gets near 90%, stop starting new work and update this file first: the progress tracker (§5: current task, its exact state, uncommitted files, agents still running), any new rules or environment facts, and a short entry in the latest session log (`review/session-log-*.md`) with what was done, decisions made, open findings and the exact next step. Include everything a new session needs to continue without this conversation. Commit these docs. The user then starts a new session with the "Resume after an interruption" prompt.

1. **Never delete Node code** in `control-plane/`. It becomes a frozen reference (spec D4).
2. **Ask the user before:**
   - any cutover step;
   - pushing a tag or branch;
   - opening a PR;
   - anything destructive to data, such as dropping volumes or restoring dumps.
3. **Never commit secrets:** `.env`, `.env.production`, keys or tokens.
4. **Schema:** goose (`control-plane-go/migrations`) owns it. Never run `npm run prisma:migrate` again.
5. **Plan order:** the plan text and the spec are the source of truth. A deviation needs a reason in the task record, and spec changes go through the CTO (`decisions.md`).
6. **Plan code is untested.** The code in the plans has not been compiled yet, so small fixes during execution are expected. The Engineer records each one, and the Verifier proves the result.

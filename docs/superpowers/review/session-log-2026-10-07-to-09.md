# Session log: Plan 1 execution, 2026-10-07 to 2026-10-09

This is a record of the first execution session of the Go + Temporal migration. It covers what was built, what the reviews found, what the CTO decided, what changed in the plans and the environment, and where the work stopped. The per-task details are in `review/plan1/task-<n>.md`. The rulings are in `review/decisions.md`, and deferred items are in `review/backlog.md`.

## 1. Outcome

- **Plan 1 progress:** Tasks 1, 1a, 2, 3, 4, 5, 5a, 6, 7, 8 and 9 passed their review gates. 1a and 5a are tasks the CTO inserted.
- **Paused at Task 10** (encrypted secrets), at the user's request, because background subagents kept stalling (see §6). The Task 10 code is written but **uncommitted**:
  - The files are `control-plane-go/internal/secrets/{secrets.go,secrets_test.go}`, currently untracked.
  - The orchestrator's CI dry run on that code passed on 2026-10-09.
  - Still missing: a recorded failing-test run, the commit, and the Security/Staff/Verifier reviews.
- **Branch:** `go-migration`, created off `go`. The user approved a first push on 2026-10-07 at `77eeb6a`. GitHub Actions run https://github.com/manwindersingh1712/infra_pilot/actions/runs/37620641256 passed: go, ui and secrets jobs all green. Every commit after that is **local and unpushed**, and the user is asked before each push.
- **Dev database `control_plane`:**
  - Baselined by goose (version 1) and migrated to version 2 (`Deployment.stoppedAt`).
  - Row counts are unchanged throughout: User/Project/Service/Deployment/OutboxEvent/EnvVar/ServiceConnection = 5/9/13/30/49/2/7.
  - Two backups sit in that session's scratchpad: `control_plane-pre-baseline.dump` and `control_plane-pre-00002.dump`.

## 2. What each task delivered

| Task | Delivered | Key commits | Review rounds |
|---|---|---|---|
| 1 | Toolchain, Go module `infrapilot` (go 1.27.1), CI (go/ui/secrets jobs), golangci config | `efacab3`; fixes `7818fb8` `d79dede` `c52426d` `5562fbd` `2fa5b46` | 1 fix round |
| 1a (inserted) | cp-ui lint and type errors fixed so the ui CI job passes; no behaviour change | `8d4bb0c` | clean |
| 2 | `internal/config`: unset APP_ENV fails closed to production; range checks; JWT strength outside dev; `Config.LogValue`/`String` redaction | `e1801fe`; fix `0ab3bd8` | 1 fix round |
| 3 | `internal/db` pgx connection; goose baseline `00001` matching the Prisma schema; `cmd/migrate up\|status\|baseline` | `42a4770`; fixes `66d719b` `a038c25` `e41ea2f` | 1 fix round |
| 4 | `internal/testutil`: Postgres testcontainer helper, `NeedDB`, `FakeRunner` | `8cee8ac` | clean |
| 5 | Migration `00002`: nullable `Deployment.stoppedAt` | `af72fc7` | clean |
| 5a (inserted) | db and test-helper hardening: connect timeout, no password in errors, goose Provider with one lock, `up` refuses an un-baselined Prisma DB, read-only `status`, Prisma drift guard, CI guard against test code in binaries | `efa19fa` … `c6c8945`; fixes `3b1e7e0` `15f8916` `dfae298` `f3471ef` | 1 fix round |
| 6 | `internal/store` core, JSON models, user queries | `a355fe1` | clean |
| 7 | Projects, services, env vars, canvas, `testutil.Seed` | `1dc7b04` | clean |
| 8 | Deployments, job read model, container lifecycle (claim, mark, stop, live/running, ports) | `e546f34` | clean |
| 9 | Outbox claim and status queries (`FOR UPDATE SKIP LOCKED`, same semantics as Node) | `6af23ce` | clean |
| 10 | AES-256-GCM `internal/secrets` | **uncommitted** | not reviewed |

Parity checks against the running Node API, done read-only on the dev DB:
- User JSON, projects, services, env vars and deployments are byte-identical. All 30 deployments match, with Go adding `stoppedAt`.
- One canvas list returns its services in a different order, because Node's query has no `orderBy`. This is harmless.

## 3. CTO decisions in this session (full text in `decisions.md`)

1. **Task 1:**
   - Insert Task 1a (cp-ui baseline).
   - gosec excludes only G204, and a CI step forbids `sh`/`bash`. Any other suppression is a per-line `#nosec` with a reason.
   - Files inside a clone are opened only via `os.OpenRoot`.
   - First-push gate: Tasks 1, 1a and 2 green.
2. **Task 2:**
   - Unset APP_ENV/NODE_ENV means production.
   - Validate every range.
   - Redact config in logs now.
3. **Task 4 → Task 5a:** a db and test-helper hardening round inserted after Task 5.
4. **Task 5a fix round:**
   - We hold the migration lock ourselves around every goose operation.
   - `migrate up` refuses an un-baselined Prisma schema ("run `migrate baseline` first").
   - `migrate status` is read-only.
   - The drift guard compares type modifiers.
5. **Task 8 forward findings, amending later plan text before it runs:**
   - **FWD-A1 (high):** git option injection or unsafe transports in the builder. Fix: a new `internal/gitsrc` package for API and worker validation (https only, ref-safe names), `-c protocol.*` flags, and `--`/`--end-of-options`. Lands in Tasks 22/23/27 and Plan 2 Task 8.
   - **FWD-A2 (medium):** repo URL tokens kept out of logs, Temporal history and errors; masked in API responses; `LoadJob` no longer returns the URL.
   - **FWD-A3 (low):** legacy `volumePath` is used only inside `LEGACY_VOLUME_ROOT`.
   - **STAFF-4 (T8):** `DeployWorkflow` cleanup never undoes a live deploy, and the build is skipped when an image already exists (Task 24). `MarkFailed` never overwrites `deployed`.
6. **Spec updates:**
   - §4 steps 1, 2, 6 and 12.
   - §5: new rows for the cloned repo, git sources and credentials, plus the `/dev/login` row.
   - §6.3: the legacy volume.
   - §7: the `00002` filename.
   - §8: the gosec rule.

## 4. Operational changes the user must know

- **Never run `npm run prisma:migrate`** (or `prisma migrate dev`, `db push` or `db pull`) against the dev DB. goose owns the schema, and Prisma's `migrate dev` would offer to reset it. CLAUDE.md now says to use `go run ./cmd/migrate up`.
- **`migrate status` output format** is now one line per migration, e.g. `00001 00001_baseline.sql applied 2026-10-07T12:28:02Z`.
- **Plan 1 cutover (Task 27):**
  - Node-era DBs other than the dev DB must run `migrate baseline` first.
  - Services whose `repoUrl` is not `https://` will fail to build with `invalid_build_source`. The cutover checklist counts them, and the release note must mention them.
- **`APP_ENV=production` refuses the placeholder `JWT_SECRET`.** Both `.env` files still use `dev-secret-change-me`, which is fine in development. Plan 2's cutover check was updated for this.
- **`ENV_ENCRYPTION_KEY`** in `control-plane-go/.env` (mode 600, gitignored) was printed once in a reviewer's own transcript. Nothing is encrypted with it yet, so rotating it now is free. Back up whichever key you keep.
- **Leftover test data in the dev DB:** project `verify-p1t1a` (`cmuy2a1mw00003u3n546iezzm`) and an undeployed redis service row (`cmuy2a7v900013u3nbqos1e6s`). The Node API has no delete endpoint for them.
- **Tools installed:** go 1.27.1, golangci-lint 2.14.0, gosec 2.29.0, gitleaks 8.30.1, govulncheck v1.8.0. The PATH line was added to `~/.zshrc`. Docker Desktop is required.

## 5. Notable review findings and how they were handled

- **Accepted vulnerability:** GO-2026-5932 (`x/crypto/openpgp`, no fix, not imported). Re-check it at every plan gate.
- **Dependency upgrades:** `x/crypto` and `moby/go-archive` were upgraded in Task 4 to clear vulnerabilities that the code actually calls.
- **Low findings:** about 60 went to `backlog.md`. Each line names its target task or plan. Notable groups:
  - Plan 2 API watch items: owner-scoped queries, masking EnvVar values, encrypting before `UpsertEnvVar`, rejecting registration of `dev@local`.
  - Task 23/25 follow-ups: a per-start Temporal timeout, the `MarkBuilt`/`MarkDeployed` `ErrNotFound`, and test gaps.
  - Plan 3 hardening: SHA-pinned actions and dependabot, image digests, an SSRF filter for repo hosts, a clean git HOME, reconciling orphan containers, and an sslmode policy.

## 6. Process problems and workarounds

- **Subagent stalls.** Background agents were often killed by the 10-minute stream watchdog ("no progress for 600s").
  - For Task 9, five Verifier agents in a row stalled, so the orchestrator ran the verification itself. It used background commands and throwaway files outside the repo, and recorded this in `task-9.md`.
  - For Task 10, the Engineer stalled twice, including on a short commit step.
  - The user chose to pause rather than change the process.
- **govulncheck can hang** on its network fetch. One run hung for an hour; the retry took 3 s. The briefs and START-HERE now say to run it as `perl -e 'alarm 180; exec @ARGV' govulncheck ./...` and retry once.
- **Host pauses.** Long commands sometimes sat at 0% CPU for 10+ minutes even though the work behind them takes seconds, which looks like the host pausing them. Run long commands in the background.
- **Usage limits.** Several agents stopped with HTTP 429 usage-limit errors and were resumed.
- **Transient safety-check errors.** Some tool calls failed with "auto mode classifier gave no verdict". Retrying once worked.
- **Rule added to the briefs:** never print `.env` contents.

## 7. How to resume

1. Start a new session in the repo and say: "Read docs/superpowers/START-HERE.md. Check the progress tracker, git log on the go-migration branch and docs/superpowers/review/ to find the last task that passed its review gate, then continue the same plan from the next task with the same orchestration."
2. **Task 10 first:**
   - A fresh Engineer re-reads `internal/secrets/*` against plan Task 10 (lines 3001-3200) and spec §5.
   - It shows a failing-test run if feasible, then commits.
   - Security, Staff and Verifier review it, and the result goes in `review/plan1/task-10.md`.
3. Then continue with Task 11 (Redis hot tier) onward.
4. **Before any push:** ask the user, run full-history gitleaks, and run the local CI dry run.

---

# Session 2026-10-09 to 2026-10-10 (Tasks 10-17)

- **Another session** (after the pause) completed Tasks 10-15 and committed Task 16. Records: `review/plan1/task-10.md` … `task-15.md`. Go toolchain moved to go1.27.2. The old govulncheck wrapper hid a missing binary, so earlier "exit 0" results for Tasks 9-10 were not trustworthy; the corrected command is in START-HERE §5.
- **Task 16 (Docker runtime): done after one fix round.** Big finding: managed Mongo data never persisted (mongo:7 writes to `/data/db`, the volume was at `/data`; Node had the same bug). CTO ruling: `RunSpec.VolumeTarget`/`Tmpfs`, Mongo at `/data/db` with `/data/configdb` on tmpfs, no `admin123` (always generated passwords), Redis-only legacy copy, Node-era Mongo moved by `mongodump`/`mongorestore` in Task 27 step 6. Spec + Task 23/27 amended. Also: capped runner output (1 MiB stdout / 64 KiB stderr), WaitDelay, env/name/image validation, limits on helper containers, real-Docker Mongo test.
- **Task 17 (ports): fix round 1 in progress** (see `task-17.md`).
- **Open tooling issue (medium):** gosec v2.29.0 can't read go1.27 export data, so its type-aware rules are blind locally and probably in CI. Needs a newer gosec (backlog). Use `$(go env GOPATH)/bin/gosec`, not Homebrew's.
- **New rule (user):** at ~90% context, save state into START-HERE + this log and commit (START-HERE §7 rule 0).
- **User questions answered:** 34 tasks left (Plan 1: 11 incl. 17; Plan 2: 18; Plan 3: 5). Kubernetes is roadmap Phase 6, not in the migration plans; the `Runtime`/`Router` interfaces (spec D7) keep it possible.
- **Next step:** check whether the Task 17 fix commit landed (`git log`); if `ports.go`/`ports_test.go` are still uncommitted, run the CI dry run and have an Engineer commit (or the orchestrator, per the 2026-10-09 user ruling), then Staff + Verifier re-check, close `task-17.md`, update the tracker, start Task 18 (nginx routes).

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

Update this section as the work moves. The orchestrating session should tick boxes and fill in dates at each plan gate.

| Plan | Status | Started | Gate passed | Cutover done | Notes |
|---|---|---|---|---|---|
| Plan 1 — worker | In progress | 2026-10-07 | | | Rollback window: 2 weeks after its cutover |
| Plan 2 — API + UI | Not started | | | | Point of no return for Node; take a DB backup first |
| Plan 3 — hardening | Not started | | | | Requires Plan 1's rollback window to be closed |

- **Branch:** `go-migration` (created 2026-10-07 off `go`; first commit holds these docs).
- **Current task:** Plan 1, Task 6 (Store core, models and users). Tasks 1, 1a, 2, 3 (2026-10-07), 4, 5 and 5a (2026-10-08) passed their gates.
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

# Plan 1, Task 5: Additive migration — `Deployment.stoppedAt`

## Implementation
- Engineer commit `af72fc7` feat(go): add Deployment.stoppedAt (additive, Node-compatible). Plan-verbatim, no deviations.
- Dev DB: backup `<session scratchpad>/control_plane-pre-00002.dump` (26,134 bytes); `migrate up` → version 2; row counts 5/9/13/30/49/2/7 unchanged; Node API `/readyz`, `/dev/login`, `GET /projects`, `GET /deployments` (30 rows) fine; `prisma migrate status` up to date.

## Reviews (commit af72fc7)
- **Security: CLEAN.** Nullable column without default: catalog-only change on PG16 (brief ACCESS EXCLUSIVE lock, no rewrite); Down unreachable (`cmd/migrate` has no `down`).
- **Staff: APPROVE.** Prisma conventions met; no Node raw SQL touches Deployment; later plan uses need only the column (index comes in Plan 2 00003). Lows STAFF-1 (T5) (no table_schema filter), STAFF-2 (T5) (test placement) → backlog; STAFF-3 (T5) spec filename `00002_additive.sql` → orchestrator fixed spec §7.
- **Verifier: PASS.** CI job green; fresh DB → v2 with `timestamp(3)` nullable; production simulation (pre-00002 backup restored, already at v1) → `up` to v2 with counts unchanged; dev DB at v2; Node API created a real Deployment via Prisma (stoppedAt NULL), then the test Deployment and its OutboxEvent were deleted by id (dev counts back to 5/9/13/30/49/2/7; the outbox row never reached RabbitMQ).

## Result
**Done 2026-10-08.** Verifier PASS, no open critical/high/medium.

## CTO ruling during this task
- Staff's db-hardening proposal → new **Task 5a** (after Task 5, before Task 6). Plan 1 text inserted; Task 27 Step 3 gains a baseline-first note for non-dev Node-era DBs; 14 backlog lines marked "moved to Task 5a". Task 26 had no connect-timeout wording to remove.

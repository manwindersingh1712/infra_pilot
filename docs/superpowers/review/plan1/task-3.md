# Plan 1, Task 3: Database connection, schema migrations and the `migrate` command

## Implementation
- Engineer commit `42a4770` feat(go): pgx connection and goose baseline migration matching Prisma schema. Code plan-verbatim.
- Dependencies: pgx/v5 v5.11.0, goose/v3 v3.28.0, godotenv v1.5.1 (+ indirect). govulncheck clean.
- Safety backup before baselining the dev DB: `<session scratchpad>/control_plane-pre-baseline.dump` (24,527 bytes, pg_dump -Fc).
- Dev DB `control_plane` baselined: goose_db_version rows (0, 1). Row counts User/Project/Service/Deployment/OutboxEvent/EnvVar/ServiceConnection = 5/9/13/30/49/2/7 before and after.
- Deviation (gate method): the plan's Step 10 diff vs the dev DB showed only `COMMENT ON SCHEMA public IS '';` and `REVOKE USAGE ON SCHEMA public FROM PUBLIC;` (dev `public` schema recreated by a Prisma reset). Equivalence proven by applying the 10 Prisma migration.sql files to a fresh DB → byte-identical to goose.

## Round 0 reviews (commit 42a4770)
- **Security: FINDINGS (0/0/2/2).** SEC-1 medium: `normalizeURL` wraps `url.Parse` errors, leaking the password. SEC-2 medium: keyword/value DSNs mangled into a "host" containing the password (in errors and DNS lookups). SEC-3 low: no connect/ping timeout. SEC-4 low: no sslmode policy for production.
- **Staff: CHANGES REQUESTED (0/0/2/4).** STAFF-1 medium: `baseline` accepts a partially-migrated Prisma DB. STAFF-2 medium (plan text): prisma:migrate warning only in Task 4; `migrate dev` would offer a reset after baselining. STAFF-3 low: plan wording (version rows, Step 10 gate). STAFF-4 low: goose globals / no lock. STAFF-5 low: normalizeURL edge cases. STAFF-6 low: migrate.go untested.
- **Verifier: FAIL.** VER-1 = SEC-1. Everything else passed: fresh goose DB = fresh Prisma DB, dev data identical to the backup (full data diff), Node API works, `prisma migrate status` up to date.

## Triage
- Fix now: SEC-1/VER-1, SEC-2, STAFF-1 (Engineer). STAFF-2, STAFF-3: orchestrator plan/CLAUDE.md text. Lows SEC-3, SEC-4, STAFF-4, STAFF-5 (rest), STAFF-6 → backlog (mostly Task 4).

## Fix round 1
- `66d719b` fix(go): do not echo DATABASE_URL in parse errors (SEC-1, VER-1)
- `a038c25` fix(go): pass non-URL DSNs to pgx unchanged (SEC-2)
- `e41ea2f` fix(go): refuse baseline on incomplete Prisma history (STAFF-1): 10 frozen migration names (test compares with control-plane/prisma/migrations), read-only pre-check, one transaction under `pg_advisory_xact_lock`, version re-read.
- Deviations: "unfinished" = `finished_at IS NULL AND rolled_back_at IS NULL`; pre-check so a refused baseline leaves no goose table.
- Docs `27bdedd`: Task 3 Steps 10/11 notes, prisma:migrate warning in Task 3 and CLAUDE.md, backlog.

## Fix round 1 re-checks
- **Security: FINDINGS (0/0/0/1).** SEC-1, SEC-2 closed (binary probes with many malformed/valid URLs, live Postgres). New low SEC-5 (T3r1): malformed keyword/value DSNs can survive pgx's own redaction via `Connect`'s wrapped ParseConfig error → backlog (Task 4).
- **Staff: APPROVE.** Transaction code correct; deviations justified; live checks on a scratch DB. New lows STAFF-7 (unknown Prisma rows), STAFF-8 (drift test should Fatalf), STAFF-9 (concurrent first baseline races in goose) → backlog (Task 4).
- **Verifier: PASS.** Full CI job green; VER-1 repro fixed; keyword DSN reaches pgx unchanged; baseline complete/incomplete/empty paths correct with no goose table left on refusal; dev DB unchanged (counts and goose rows); Node API and `prisma migrate status` fine; regression green. Observation: CLAUDE.md Initial Setup still said `npm run prisma:migrate` → orchestrator replaced it with `go run ./cmd/migrate up`.

## Result
**Done 2026-10-07.** Verifier PASS, no open critical/high/medium. Next: Task 4 (test helpers), executed as written. Backlog items SEC-5 (T3r1) and STAFF-4..9 for internal/db are candidates once testcontainers exist; scheduling them is a CTO call (at latest the Plan 1 gate).

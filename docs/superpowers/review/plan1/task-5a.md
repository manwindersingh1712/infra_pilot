# Plan 1, Task 5a: db and test-helper hardening (inserted by CTO, 2026-10-08)

## Implementation (429b7b1..c6c8945)
- `efa19fa` fix(go): fixed DATABASE_URL parse error and connect timeout (SEC-5, SEC-3)
- `6f97c05` fix(go): quote schema, reject duplicate schema, drop Prisma-only URL params (STAFF-5)
- `b750c09` refactor(go): goose Provider with one session lock for up and baseline (STAFF-4, STAFF-9, STAFF-7, STAFF-8)
- `e727a95` test(go): db integration tests and Prisma drift guard on fresh databases (STAFF-6, STAFF-5)
- `695b8dd` fix(go): test-helper hint, NeedDB truncates every table, FakeRunner copies args (STAFF-1..4)
- `c6c8945` ci(go): forbid test helpers in production binaries (SEC-2)
- Deviations: group 1 test inputs adjusted where pgx accepts them; locker built per operation (go-retry attempt counter never resets); `WithDisableGlobalRegistry`; pre-check dropped; `export_test.go`; extra in-progress row for "unfinished"; new `migrate status` format (`00001 00001_baseline.sql applied <RFC3339 UTC>`).
- The Engineer's first run stalled (no progress 600 s, host likely paused); resumed with context, finished all six groups.
- Dev DB unchanged (status 00001/00002 applied, counts 5/9/13/30/49/2/7).

## Round 0 reviews
- **Security: CLEAN.** SEC-5, SEC-3, SEC-2 closed (binary probes: no secret in any parse/connect error; unroutable host fails at 10 s; guard catches testutil imports). `Password=X` runtime param: value appears in neither client error nor server log.
- **Staff: CHANGES REQUESTED (0/0/1/4).** STAFF-1 medium: goose `HasPending` creates the version table outside the session lock, so baseline and up started together can fail; an up before any baseline fails on an un-baselined Prisma schema. Lows STAFF-2 (drift guard ignores type modifiers), STAFF-3 (`status` writes), STAFF-4 (CI guard silent if `go list` fails), STAFF-5 (`Password=` runtime param).
- **Verifier: FAIL.** VER-1 = STAFF-1: all-four-at-once fails every run (4/5 end at goose rows (0,1)); staged passes; deterministic repro by holding the advisory lock in psql. Everything else passed. Note: the Verifier printed the dev `.env` to its own transcript (no persistence); reviewers are now told never to print `.env`.

## Triage
- CTO ruling (`../decisions.md`, "Task 5a fix round 1"): we hold the lock ourselves around every Provider op (Provider without `WithSessionLocker`, `WithLockTimeout(1, 300)`); `up` refuses an un-baselined Prisma schema (`ErrNotBaselined`); `status` read-only and lock-free when no version table; drift guard compares type modifiers; CI guard fails if `go list` fails. MarkBaseline retry rejected. Verifier check changed accordingly. STAFF-5 → backlog.
- Orchestrator: plan Task 5a Verifier text replaced and fix-round note added; Task 27 Step 3 comment extended; backlog updated.

## Fix round 1 (717b106..f3471ef)
- `3b1e7e0` fix(go): take the migration lock ourselves and refuse an un-baselined Prisma schema (VER-1, STAFF-1)
- `15f8916` fix(go): migrate status takes no lock and writes nothing (STAFF-3)
- `dfae298` test(go): drift guard compares column type modifiers (STAFF-2)
- `f3471ef` ci(go): test-helper guard fails when go list fails (STAFF-4)
- New tests failed on c6c8945 (lock-hold test: "goose_db_version was created while another session held the migration lock"), then passed.

## Fix round 1 re-checks
- **Security (quick look): CLEAN.** CI guard fails closed; no new error path echoes the URL; SQL static/bound.
- **Staff: APPROVE.** Verified against goose v3.28.0 / pgx v5.11.0 source: NewProvider/Close never touch the DB; lock held on a dedicated conn for the whole op and released on every path; ErrNotBaselined checked inside the lock before any goose call; Status read-only both ways. `-count=3` concurrency runs: no flakiness. Lows STAFF-6 (T5ar1) (2-conn floor assumes SQL-only migrations), STAFF-7 (T5ar1) (failed unlock returns a locked conn to the pool) → backlog.
- **Verifier: PASS.** (i) all four at once ×5: baselines always 0; ups 0 or the exact "run `migrate baseline` first" refusal (runs 2-4), never a Postgres error; further up 0; rows (0,1,2); seeded User kept. (ii) staged all 0. (iii) lock held in psql: no goose_db_version for 10 s; up finished after unlock. (iv) un-baselined up refused, no table. (v) status on empty DB writes nothing. Dev DB unchanged; malformed DSN no secret; unroutable host 10 s; Node `/dev/login` OK; regression green.

## Result
**Done 2026-10-08** after fix round 1. Verifier PASS, no open critical/high/medium. Next: Task 6.

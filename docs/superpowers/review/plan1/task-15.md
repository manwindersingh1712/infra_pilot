# Plan 1, Task 15: Logging, worker metrics and outbox counts

## Implementation
- **Commit** `ba6b86b` feat(go): structured logging, worker metrics and outbox counts.
  - Plan-verbatim, except that test setup errors are now checked with t.Fatal, and `go mod tidy` was run.
  - New direct dependency: prometheus/client_golang v1.25.0 (indirect: client_model, common, procfs, perks, goautoneg, protobuf v1.36.12).
- **Failing run:** `undefined: New`, `undefined: NewWorker`, `CountOutboxByStatus undefined`.
- **Orchestrator full suite at ba6b86b:** build/vet ok, `-race` all ok, lint 0 issues, `-short` ok.

## Reviews (commit ba6b86b)
- **Verifier: PASS.**
  - JSON logging escapes `\n` and `\r\n` within a single line.
  - `/metrics` exposes every `cp_*` series. A failed deployment is not added to the deploy histogram, and all 4 outbox statuses are always emitted.
  - POST returns 405 and other paths return 404.
  - A port already in use returns an error at once. Cancelling ctx makes Serve return nil and closes the port.
- **Security: 2 low**, both in the backlog:
  - Serve sets only ReadHeaderTimeout, and the bind address isn't enforced to be private;
  - the outcome label is a free string.

  CRLF injection was tested and is escaped. Labels are bounded with no tenant data. gosec, gitleaks and govulncheck are clean.
- **Staff: APPROVE with 1 medium, a spec gap and not an Engineer defect.**
  - STAFF-1: spec §8 asks for outbox lag and Temporal SDK metrics, which no task builds. It went to the CTO.
  - STAFF-2 and STAFF-3 plus test gaps went to the backlog.
  - STAFF-4 (Task 26 wiring) and STAFF-5 (Task 23 counters) were forwarded to the CTO.

## CTO ruling (`decisions.md` 2026-10-09 "Worker metrics scope")
- Add the outbox lag gauge now, in this task's fix round.
- Temporal SDK metrics are deferred to Plan 3, with spec §8 edited.
- STAFF-4 goes into a Task 26 amendment (log query errors, set the lag gauge).
- STAFF-5 goes into a Task 23 amendment (`MarkFailed` returns `(bool, error)` and counts only when a row changed).
- Commit `c56365d`.

## Fix round 1
- `cc6fdb1` feat(go): outbox lag gauge (STAFF-1 T15, CTO 2026-10-09): `Store.OldestUnpublishedOutboxAge` (amendment SQL) plus `cp_outbox_oldest_pending_age_seconds` / `SetOutboxLag`, with tests. Failing run: `OldestUnpublishedOutboxAge undefined`.
- **Orchestrator full suite at cc6fdb1:** build/vet ok, `-race` all ok, lint 0 issues, `-short` ok.
- **Staff re-check: RESOLVED.**
  - The code matches the amendment verbatim.
  - The Engineer's time-zone concern is already handled by `db.go:63` (`RuntimeParams["timezone"]="UTC"`), so it is info only.
  - Optional nits went to the backlog.
- **Verifier re-run: PASS** on a migrated throwaway Postgres:
  - a pending row backdated 300 s gives `cp_outbox_oldest_pending_age_seconds 300.00298`;
  - once all rows are published it reads 0;
  - with the DB time zone set to Asia/Kolkata, a new row reads about 0.0016 s, because the pool forces UTC.

## Result
**Done 2026-10-09.** Verifier PASS, and nothing critical, high or medium is open. Next: Task 16.

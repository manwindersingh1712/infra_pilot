# Plan 1, Task 9: Outbox queries

## Implementation
- Engineer commit `6af23ce` feat(go): outbox claim and status queries (`internal/store/{outbox.go,outbox_test.go,events.go}`). Plan-verbatim. OutboxEvent.status is enum "OutboxStatus" (pending/publishing/published/dead).

## Reviews (commit 6af23ce)
- **Security: CLEAN.** All SQL bound; Node payload is `{deploymentId}` only (no repo URLs, tokens or env values), and the planned starter passes only the ID into Temporal, consistent with FWD-A2; claim batch bounded (25); dead rows excluded from claims.
- **Staff: APPROVE.** Same predicate, order, statuses, event-type strings and absolute-attempts semantics as Node's publisher, so either worker can continue the other's rows (spec §9); Task 25 keeps claim+settle in one tx, copies Node's backoff exactly and treats unknown types as retry-then-dead. Lows STAFF-1..3 (T9) → backlog (STAFF-3 is a Task 25 follow-up: per-start timeout).
- **Verifier: PASS (run by the orchestrator).** Five Verifier agents in a row were killed by the 10-minute stream watchdog (two resumed, three fresh; one with background commands). Diagnosis: the db tests take ~23 s and the test binary builds/starts in 1-2 s; the stalls are in the agents' waiting, not the code. The orchestrator ran the checks directly (background commands, throwaway files only under /tmp, no repo edits):
  - CI go job: gofmt clean, build/vet, golangci-lint 0 issues, `go test -race -count=1 ./...` all ok (db 24.2 s), govulncheck exit 0 (accepted GO-2026-5932 only), gosec clean, `-short` all ok; both guard steps exit 0.
  - Caller test (copy of the module, TestMain via RunWithPostgres): 60 due events, 4 concurrent claimers in InTx holding 200 ms → 25/25/10/0, claimed=60, duplicates=0, missing=0; future-dated, dead and published rows never claimed; MarkOutboxRetry → pending, attempts=3, lastError set, nextAttemptAt +30 s.
  - Dev DB (read-only): OutboxEvent 49 rows, all `published`, types `build.requested`/`deploy.requested` = events.go constants, payload `{"deploymentId": …}`; counts 5/9/13/30/49/2/7 unchanged; migrate status 00001+00002 applied.
  - Regression: full-history gitleaks clean; cp-ui lint 0 errors, build OK. Compose stopped again (an earlier stalled Verifier had started it); no testcontainers left.

## Result
**Done 2026-10-09.** Verifier PASS, no open critical/high/medium. Next: Task 10.

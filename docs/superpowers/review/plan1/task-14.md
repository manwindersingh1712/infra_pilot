# Plan 1, Task 14: Tiered query and cursor-based flush (`Aggregator`)

## Before implementation
- **CTO ruling** "Task 14 flush durability" (`decisions.md` 2026-10-09). It closes the Task 12 review items:
  - refresh the cursor TTL on every pass;
  - drain each stream in a loop (`BatchSize`, `maxFlushBatches = 20`);
  - exactly one flusher;
  - default an empty timestamp at write time.

  Spec §6.7 is reworded to "best effort". The amendment is pasted under the Task 14 heading in the plan. Commit `ce26d64`.

## Implementation
- `d89f0fe` feat(go): tiered log queries and duplicate-free flush. Plan code plus amendment items a–c.
- `2bfbd3c` fix(go): default empty log timestamp at write time (CTO T14 amendment d, STAFF-1 T12). Also adds the redis tests for the cursor TTL and an expired stream (STAFF-3 T12).
- **Deviations:**
  - an extra `TestFlushStopsAfterMaxBatches`;
  - a `drain` helper;
  - cancellation between streams returns early without counting an error;
  - gofmt reformatting of the plan's fakes;
  - `_, _ = fmt.Sscan`.
- **Failing runs:**
  - `undefined: Aggregator`;
  - `timestamp not stable: "…47.459Z" then "…47.465Z"` before amendment d.
- **Orchestrator full suite at 2bfbd3c:** build/vet ok, `-race` all ok, lint 0 issues, `-short` ok.

## Reviews (range 838d9bc..2bfbd3c)
- **Verifier: PASS** with real Redis 7 and ClickHouse 26.9:
  - 253 entries flushed, 3 of them with an empty timestamp. A second flush copied 0; after 5 more writes it copied 5. The table then held 258 rows, all unique.
  - Cursor TTL was cut to 5 s, and an idle flush restored it to about 900000.
  - With ClickHouse stopped: errs 1, the cursor unchanged, and its TTL refreshed.
  - Query returns Source "redis" for recent logs, and "clickhouse" with TotalCount 258 for a range or after the stream is deleted.
- **Security: 2 low**, both in the backlog:
  - no cap of 1000 on Limit, and no clamp on Offset;
  - ILIKE wildcards are reachable.

  Flush bounds, tenant scoping and error contents are clean. gosec, gitleaks and govulncheck are clean.
- **Staff: CHANGES REQUESTED.**
  - Every amendment item is implemented and tested exactly. Query tiering matches Node's `queryLogs`.
  - **STAFF-1 (medium):** every flush error was dropped without a log line. Node logged the deploymentId and the error.
  - Lows: STAFF-2 (untested error branches) went to the backlog; STAFF-3 and STAFF-4 were handled with comments.

## Fix round 1
- `f6a51be` fix(go): log each failed log-flush step with deploymentId (STAFF-1 T14).
  - New optional `Logger` field. Each failure logs a Warn "log flush failed" with deploymentId, step and err.
  - Tests:
    - Insert #2 fails: processed 2, errs 1, cursor "2", plus the log fields.
    - An ActiveStreams error is covered.
  - Failing run: `log missing "msg":"log flush failed"`.
- **Plan change:** the Task 26 wiring now passes `Logger: logger` (`8a49a34`).
- **Orchestrator full suite at f6a51be:** build/vet ok, `-race` all ok, lint 0 issues, `-short` ok.
- **Staff re-check: APPROVE.**
- **Verifier re-run: PASS.**
  - 120 entries flushed with no warnings.
  - ClickHouse down: one Warn with `"deploymentId":"v1","step":"insert"`. The driver masks the password as `***`, and no log text appears.
  - Redis down: one Warn with `"step":"activeStreams"` and no deploymentId.

## Result
**Done 2026-10-09.** Verifier PASS, and nothing critical, high or medium is open. Next: Task 15.

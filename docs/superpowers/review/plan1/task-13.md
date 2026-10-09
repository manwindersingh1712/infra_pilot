# Plan 1, Task 13: ClickHouse warm tier

## Implementation
- **Commit** `1667196` feat(go): clickhouse warm log tier with bound parameters.
- **Dependencies:** clickhouse-go/v2 v2.49.0 and the testcontainers clickhouse module v0.44.0 as direct dependencies, plus the standard clickhouse-go indirect set. moby api and client got minor bumps.
- **The orchestrator checked the DDL against Node's** (`control-plane/apps/api/src/services/clickhouse.ts`): the plan's DDL is the same as Node's.
- **Deviations:**
  - **TTL:** `toDateTime(timestamp) + INTERVAL 30 DAY`, because ClickHouse 24.8 rejects a TTL on DateTime64 (`BAD_TTL_EXPRESSION`). It gives the same 30 days. An existing Node table is untouched (`IF NOT EXISTS`).
  - **Test fixtures:** timestamps are relative to now. The fixed 2026-01-01 rows were already past the TTL and expired mid-test.
  - `go mod tidy`.
- **Failing run:** `undefined: buildQuery` / `Query` / `NewClickHouse`, then `[build failed]`.
- **Orchestrator full suite at 1667196:** build/vet ok, `-race` all ok (logs 11.9 s), lint 0 issues, `-short` ok, gosec clean.

## Reviews (commit 1667196)
- **Verifier: PASS** on clickhouse-server:latest (26.9):
  - Node's exact DDL followed by Go `Init` succeeds, and the Node-inserted row reads back in Entry shape.
  - Quotes round-trip.
  - Sources, Search, Start/End, Limit/Offset and Count all behave correctly.
  - A fresh Go-created table has the same columns as Node's.

  Two observations:
  - Searching for a literal `%` or `_` matches every row.
  - The password leaks in URL parse errors.
- **Staff: CHANGES REQUESTED, 1 medium.**
  - **STAFF-1 (medium):** Start/End were bound as positional time.Time, which clickhouse-go formats in whole seconds. Bounds lost up to 999 ms; Node had millisecond precision.
  - The deviations are justified.
  - Lows STAFF-2..5 went to the backlog.
- **Security: 1 medium, 3 low, 1 info.**
  - **SEC-1 (medium):** the `CLICKHOUSE_URL` parse error quotes the raw URL, password included (the same bug as Task 11 SEC-1).
  - SEC-2..5 went to the backlog: https silently downgraded to http, no Limit/Offset clamps, ILIKE wildcards in Search, client timeouts.
  - govulncheck 0 affecting vulnerabilities; gosec and gitleaks clean.

## Fix round 1
- `ff6fd1d` fix(go): keep CLICKHOUSE_URL credentials out of parse errors (SEC-1 T13).
  - Any url.Parse error now returns the fixed `parse CLICKHOUSE_URL: invalid URL`.
  - The test (runs under `-short`) failed before the fix, showing the leaked URL.
- `89529fc` fix(go): millisecond-precision time bounds in ClickHouse queries (STAFF-1 T13).
  - Bounds are now `timestamp >= toDateTime64(?, 3, 'UTC')` with a `2006-01-02 15:04:05.000` UTC string argument.
  - The unit test's expected bound form was updated, and it now uses a 7 ms start.
  - The new integration assertions with sub-second bounds failed before the fix (`End bound = []`).
- **Orchestrator full suite at 89529fc:** build/vet ok, `-race` all ok, lint 0 issues, `-short` ok.
- **Security re-check: CLEAN.** The bounds are still bound parameters.
- **Staff re-check: APPROVE.** Bounds are inclusive and the instant is UTC-correct. It works on 24.8 and newer servers. The test change is legitimate. It matches Node's millisecond precision.
- **Verifier re-run: PASS** on :latest.
  - 5 rows 1 ms apart: Start +101 ms returns 4 rows; End +102 ms returns 3; both bounds return 3.
  - A +05:30 zone gives the same result.
  - The URL error has no password.

## Result
**Done 2026-10-09.** Verifier PASS, and nothing critical, high or medium is open. Next: Task 14, with the CTO amendment from `decisions.md` 2026-10-09.

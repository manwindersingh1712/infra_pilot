# Plan 1, Task 12: Stream IDs and the flush cursor

## Implementation
- Engineer commits:
  - `14b8a26` feat(go): redis stream IDs and per-deployment flush cursor. Plan-verbatim; `errors` was already imported, so it was not re-added.
  - `1bd53f7` fix(go): missing stream fields read as empty, timestamp defaults to now (STAFF-3 T11). Shared `toEntry`/`field` helpers; this matches Node's `message || ""` and `timestamp || toISOString()`.
  - `dd18fcf` test(go): stream TTL, second live entry and ParseTime coverage (STAFF-4 T11).
- Deviations: the two Task 11 backlog items that targeted this task were folded in with the orchestrator's approval.
- Failing runs:
  - `-run Cursor` failed to build: `r.FlushCursor undefined`, `r.ReadAfter undefined`, `r.SetFlushCursor undefined`.
  - The sparse-entry test failed with `Source:<nil> ... Timestamp:<nil>` before 1bd53f7.
- Orchestrator full suite at dd18fcf: `-race` all ok (db 24.0 s, logs 2.8 s), golangci-lint 0 issues, `-short` all ok, govulncheck exit 0 (only the uncalled module vulnerability, the accepted GO-2026-5932).

## Reviews (range c1e6325..dd18fcf)
- **Staff: APPROVE.** The API fits Task 14's `HotStore` exactly. The deviations are justified, and the tests are specific with no sleeps. Lows STAFF-1..4 (T12) and two notes for the Task 14 review went to the backlog:
  - an empty timestamp is re-stamped on every read;
  - the cursor-TTL comment is wrong;
  - test gaps (cursor TTL, ReadAfter on an expired stream);
  - SetFlushCursor has no doc comment;
  - Task 14 notes: MAXLEN trimming against a 10000-entry batch, and a single flusher.
- **Security: 1 low.**
  - gosec and gitleaks clean; govulncheck 0 affecting vulnerabilities.
  - XRANGE arguments are RESP bulk strings, so the cursor cannot inject commands.
  - Cursor keys are written only by the worker.
  - `logs:flushed:` is not matched by the ActiveStreams SCAN.
  - SEC-1 (T12), low, went to the backlog: duplicate inserts after an outage longer than 15 min, or a crash between insert and cursor write; make the ClickHouse insert idempotent.
- **Verifier: PASS.** Flush-loop simulation on a throwaway Redis:
  - 250 entries in batches of 100 came back as 250 in order, no duplicates.
  - 5 more entries: exactly those 5 came back. An immediate re-run returned nothing.
  - PTTL: cursor about 900000, stream about 600000.
  - ActiveStreams returns `["f1"]` and not the cursor key.
  - A Node-style full entry reads correctly. A message-only entry reads with Source "" and a current timestamp.

## Result
**Done 2026-10-09.** Verifier PASS, and nothing critical, high or medium is open. Next: Task 13.

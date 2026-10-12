# Task 13 Staff review: `migrate encrypt-env` (commits 1d1651b, cd24aad)

Checks run from control-plane-go/: `go build ./...`, `go vet ./...`, `golangci-lint run ./cmd/... ./internal/store/...` all clean (0 issues).
`go test -p 2 -race -count=5 ./cmd/migrate/` ok (10.7 s); store and secrets packages ok. Tests use a throwaway testcontainers Postgres, never the dev DB. Disk: 34 GB free.

## Deviations: verdict
All justified. CAS via existing ReplaceEnvVarIfEquals (closes backlog STAFF-3, and no long transaction/lock is better than SKIP LOCKED for a one-shot tool); reading all rows is required to detect suspect values; key-before-connect and early dispatch in main.go are correct (a bad key never opens a connection; tested with a connect stub that must not be called); EnsureCredentials is the real worker read path for a managed password (it Decrypts and errors on failure), so it is the right worker-side test. Removed store methods have no remaining callers (git grep clean).

## Mutation results (throwaway git archive copy outside repo)
27 mutations. Killed: CAS removed; IsEncrypted skip removed; dry-run writes; value printed in conflict / error / suspect message; key name printed in conflict message; keyset OFFSET skip; keyset `>=` repeat; cursor = first of page; ORDER BY id DESC; ORDER BY createdAt; suspect encrypted anyway; suspect counted as already; decrypt check dropped; errors swallowed (exit 0); conflicts counted as errors; update error not counted; short key accepted (secrets test); soft-deleted rows skipped; --dry-run flag ignored; positional arg accepted; Scanned not counted; batch default removed.
SURVIVED (3):
- "ctx cancel ignored" (see STAFF-3).
- "key printed on success" (print ENV_ENCRYPTION_KEY from runEncryptEnv before encrypting): see STAFF-2.
- "encrypt-fail not counted": the Encrypt-error branch is unreachable with a valid box (only rand failure); equivalent mutant, not a gap.
Batch-boundary: with batch 100 over 1000 rows (exact multiple, so one full page then an empty one) and the default 200, off-by-one mutants die. Batch sizes 1 and 2 are not directly tested, but every mutant I tried at the boundary was killed by the 100/1000 case.
Timing: no sleeps; hooks are synchronous (afterRead). -race -count=5 stable.

## Open questions
1. Exit 0 on conflicts: acceptable (conflict = a user just wrote a fresh value; the run is safe to repeat, readers accept plaintext during backfill). But the cutover runbook must say "re-run until conflicts=0 suspect=0". See STAFF-1.
2. Suspect handling: right to skip and never touch (cannot distinguish lookalike plaintext from foreign ciphertext; no data destroyed). Exit 0 is the weak part. See STAFF-1.
3. Wrong-key detection: only indirect (suspect count) and only after writes. See STAFF-1.

## Findings

[STAFF-1] severity: medium
where: control-plane-go/cmd/migrate/encrypt.go (encryptEnv, wrong-key / suspect path)
problem: a wrong ENV_ENCRYPTION_KEY (different from the one the API/worker will use) is not detected before writing. Plain rows are encrypted with the wrong key, and the only signal is a warning line at the end plus exit 0. Rows the worker already encrypted with the right key show up as suspect, and plaintext rows are then permanently under the wrong key (data unreadable after cutover until re-keyed). A scripted cutover that checks only the exit code proceeds.
evidence: summary prints `suspect=N` and returns nil; no pre-scan; the plan's only check is exit status.
fix: (a) before the first write, when the first pass sees suspect > 0 and AlreadyEncrypted == 0 (every prefixed value fails), abort with an error unless a new `--allow-suspect` flag is given; or simply (b) exit non-zero when Suspect > 0 (flag to override) and document in the Task 18/27 runbook: "run `encrypt-env --dry-run` first, require suspect=0; run for real; require conflicts=0 suspect=0 errors=0 (re-run on conflicts)". Add a test for the chosen behavior (currently TestEncryptEnvPrefixedPlaintextIsSkippedAndCounted pins exit 0).

[STAFF-2] severity: low
where: control-plane-go/cmd/migrate/encrypt_test.go (TestRunEncryptEnv*)
problem: the "output never contains the key" test calls encryptEnv, not runEncryptEnv, so a print of the key or DATABASE_URL in runEncryptEnv's success path (mutation "key printed on success") survives. The flags test only checks the output lacks "plain-a".
fix: in TestRunEncryptEnvFlagsAndDryRun assert out contains neither the base64 key nor the DATABASE_URL on every run.

[STAFF-3] severity: low
where: control-plane-go/cmd/migrate/encrypt.go:~118 and cmd/migrate/main.go:~31
problem: the ctx-cancellation branch (`if ctx.Err() != nil { return res, ctx.Err() }`) is untested (mutant survived), and main passes context.Background() with no signal handling, so the branch is dead in the real binary; Ctrl-C kills the process mid-run (safe, because each CAS is atomic, but no summary is printed). Also pgx errors on a cancelled ctx at ListEnvVarsAfter are wrapped fine.
fix: use signal.NotifyContext in main for encrypt-env and add a test with a pre-cancelled context expecting context.Canceled and no partial-summary claims; or delete the branch.

[STAFF-4] severity: low
where: control-plane-go/cmd/migrate/encrypt.go:21-24 (encryptOptions.afterRead)
problem: an unexported test hook lives in the production options struct. It is documented and unexported, so acceptable, but note it.
fix: none required.

[STAFF-5] severity: low
where: control-plane-go/cmd/migrate/main.go:30-33, 38
problem: `migrate --dry-run encrypt-env` (flag before the command) falls through to the generic usage error, which is fine, but the generic usage string `up|status|baseline|encrypt-env [--dry-run]` suggests --dry-run applies to all. Minor wording. Also config.Load validates every env var for this command (e.g. a malformed REDIS_URL-type setting fails the migration); this follows the decisions.md note that encrypt-env is a config.Load caller, so acceptable.
fix: usage `up|status|baseline|encrypt-env [--dry-run]` -> `migrate up|status|baseline, or migrate encrypt-env [--dry-run]`.

[STAFF-6] severity: low
where: docs (Task 18 / Task 27 runbook)
problem: nothing in the repo tells the operator that encrypt-env must run after the Node API is stopped (Node writes plaintext; a row written after the run stays plaintext, harmless but un-migrated) and what to do with conflicts/suspect lines.
fix: add the sequence from STAFF-1 to the cutover runbook.

## Clean aspects
Naming, doc comments and style match the package. Counts-only output verified by mutation. Edge values (empty, 1 MiB, unicode, newline, quotes) round-trip. Soft-deleted rows are included and tested. Only store writes SQL. Resources closed (defer closePool). Unknown flags and positional args rejected.

RESULT: CHANGES REQUESTED (0 critical, 0 high, 1 medium, 5 low)

# Plan 2 Task 13 round 2 security review (b098970, 95ab550, 4e7a9cf, b83c2ed, 359ece3 vs cd24aad)

Tools: govulncheck 0 affected (1 unreachable module vuln, same as round 1); gosec on cmd/migrate, internal/store, internal/secrets: no findings; gitleaks cd24aad..HEAD: no leaks; `go test -p 2 -count=1` cmd/migrate, internal/store, internal/secrets: pass. Disk 32 GB free. Tests use a throwaway testcontainers Postgres only; no migrate command was run against the dev DB, no cp_* container touched. I added and removed a temporary probe test in cmd/migrate (deleted; the tree is as committed).

## Status of round 1

- SEC-1 closed. Pass 1 (read-only, `ListEnvVarsAfter`) runs before any write path in every mode, dry-run included (the `Yes`/`DryRun` branch and all `ReplaceEnvVarIfEquals` calls come after it). The wrong-key guard (`AlreadyEncrypted==0 && Suspect>0 && Plain>0`) returns before the `AllowSuspect` check, so the flag cannot bypass it. Edge cases: zero rows ok; suspect only, no plain: refused without the flag, with the flag nothing to write; ok>0 and suspect>0: needs `--allow-suspect`; a plain row edited to `enc:v1:` garbage between the passes is classed suspect in pass 2 and not written, and every write is the CAS `value = <read value>`, so nothing can be clobbered. The key is fixed for the process, so there is no key TOCTOU. Remaining blind spot (inherent, not a defect): with no `enc:v1:` rows at all, a wrong key cannot be detected (the first run); the runbook says to back up the key but not to confirm it is the one the Go API runs with before that first run (see SEC-R2-4).
- SEC-2 mostly closed, see SEC-R2-1 (target can be misreported). `--yes` gate: exit 2 only via `exitCodeError`, 1 for every other error, 0 ok; `TestRealMainEncryptEnvExit2ThenZero` covers the real `realMain` path. Pass 1 refusals come before the `--yes` check, so a run without `--yes` on a suspect DB exits 1 not 2 (documented, fine). `.env` logging prints only the path.
- SEC-3 closed as documentation (see SEC-R2-3 for the SQL hand-wave). I verified the claim: `internal/api/services.go:258` encrypts every submitted value unconditionally, so re-saving a value that starts with `enc:v1:` stores a proper ciphertext.
- SEC-4 closed. The cap is in the SQL itself: `CASE WHEN octet_length("value") > $3 THEN '' ELSE "value" END, octet_length("value") > $3`, bound parameter, bytes not characters (multi-byte test exists). The big value is never selected (`octet_length` on text works from the toast header; the CASE branch is not evaluated). Batch 50 x 8 MiB is up to ~400 MB transient worst case; acceptable for a one-shot tool.
- SEC-5 closed (runbook step 1 and step 6).
- Signals: `signal.NotifyContext` for SIGINT/SIGTERM; the loop checks `ctx.Err()` per row and each write is a single-statement CAS, so a cancel leaves no half-written row (a cancelled in-flight UPDATE either committed or did not; the next run reclassifies). Exit code 1, "run again to resume" printed; `TestEncryptEnvCancelStopsCleanlyAndResumes` covers resume. Note: after the first signal the context stays cancelled and a second Ctrl-C is also swallowed by NotifyContext until `stop()`; if a query hangs the operator needs SIGKILL (SEC-R2-5, informational).
- No stale callers: the only callers of `ListEnvVarsAfter` are encrypt.go and the store tests; `ListUnencryptedEnvVars`/`SetEnvVarValue` have no references. Test changes: the removed assertions are a changed message string and call-signature updates; no coverage was weakened and about 20 tests were added. One gap: no test hooks between pass 1 and pass 2 (`afterRead` only runs in pass 2), so "plain row becomes `enc:v1:` garbage between passes" is correct by reading but not proven by a test.

## Findings

[SEC-R2-1] severity: medium
where: control-plane-go/cmd/migrate/encrypt.go:62-80 (targetOf), runEncryptEnv
problem: The `target:` line is derived with `net/url` from the URL path/host only, but pgx lets query parameters override them. The printed target can differ from the database actually written, which defeats the purpose of SEC-2 (and of the runbook step "read the target line"). Verified with pgconn.ParseConfig:
evidence:
```
postgres://u:p@localhost/db?host=/tmp/sock           target=localhost:5432/db   pgx=/tmp/sock:5432/db
postgres://u:p@localhost:5432/db?dbname=other&port=6000   target=localhost:5432/db   pgx=localhost:6000/other
postgres://u:p@a:1,b:2/db   -> unparseable (safe); pgx connects to a:1 with fallback b:2
host=h password=SECRET dbname=d   -> "(unparseable ...)" (safe) but pgx connects to h:5432/d
```
Also the env defaults PGHOST/PGPORT/PGDATABASE apply when absent. Also an unparseable target does not block `--yes`: a real run proceeds while showing no target.
Credential check (the part I was asked to prove): I found no input where a user, password or key fragment is printed after a successful connect. Unescaped `@`, `/`, `?`, `#` in passwords make Go's parser either fail (port/db regex) or select the same host pgx selects; the unparseable text is never echoed; hostRe/dbRe reject `@ : , % space`. Theoretical residue: a URL like `postgres://u:12/pw#x@h/db` makes Go print `u:12/pw` (a password prefix); pgx would use the same host `u`, so it only matters if a host named `u` is reachable, which is not realistic. Defense in depth is the same fix as below.
fix: Print what the pool really uses: build the config via `db.poolConfig`-equivalent (expose a `db.Target(url)` using `cfg.ConnConfig.Host/Port/Database`, still validated with hostRe/dbRe, for multi-host print the first plus a count). If it cannot be determined, make `--yes` refuse (exit 1) instead of continuing blind. Add test rows for `?host=`, `?dbname=`, `?port=`.

[SEC-R2-2] severity: low
where: encrypt.go:260 (printSummary too_large branch), docs/superpowers/runbook/encrypt-env.md steps 3 and 6, exit codes table
problem: `too_large>0` exits 0 and the final check (step 6) only requires `would_encrypt=0` and `suspect=0`. A plaintext secret over 8 MiB stays in the clear, and the cutover sign-off can succeed with `too_large>0` (step 3 says to look, step 6 does not). The Go readers accept plain values, so nothing breaks and the secret is simply unencrypted at rest. The value is also never counted in `Plain`, so it is invisible to the wrong-key guard (harmless).
fix: Add `too_large=0` to the step 6 acceptance line, and either exit non-zero (e.g. 3) when `too_large>0` in a non-dry run or at least print it on the last line. Tell the operator how to resolve (delete, or re-save through the API, which will reject it over 32 KiB, so the real fix is to delete/recreate the variable).

[SEC-R2-3] severity: low
where: docs/superpowers/runbook/encrypt-env.md "Resolving suspect rows", first bullet
problem: "or, after taking a backup, rewrite it in SQL" gives no statement. An operator will improvise an `UPDATE "EnvVar"` that either writes plain text again or runs unguarded without a key/service/id predicate. The teardown runbook standard (guarded, quoted, one row, check row count) is not met.
fix: Remove the SQL option, or give a single-row example using the row id and a transaction with the expected count (`... WHERE "id" = '<id>' AND "serviceId" = '<id>'` then check `UPDATE 1`), with the value passed via psql variable, never pasted in history. Preferred: only the API re-save route.

[SEC-R2-4] severity: low (informational)
where: runbook step 2/3
problem: Nothing tells the operator to confirm that the `ENV_ENCRYPTION_KEY` in the shell/.env is the same one the Go API and worker run with before the first real run, which is the one case the tool cannot detect (no `enc:v1:` rows yet). Plain rows then get locked under a wrong key.
fix: Add a line: take the key from the same secret store as the API/worker; run `encrypt-env --dry-run` only after the Go API has written at least one encrypted value if possible (then the guard works), or have the operator create one test env var through the Go API first.

[SEC-R2-5] severity: low (informational)
where: main.go:30-32
problem: `signal.NotifyContext` is not stopped after the first signal, so a second SIGINT does nothing; a stuck connect or query needs SIGKILL.
fix: Optional: after the first cancel call `stop()` in a goroutine (`<-ctx.Done(); stop()`) so a second Ctrl-C kills the process.

## Not proven by tests
- Plain row turned into `enc:v1:` garbage between pass 1 and pass 2 (no hook between the passes).
- `?host=` / `?dbname=` / `?port=` target mismatch (SEC-R2-1).
- Interrupt in the middle of an in-flight UPDATE (the cancel test cancels between rows).

RESULT: FINDINGS (0 high, 1 medium, 4 low)

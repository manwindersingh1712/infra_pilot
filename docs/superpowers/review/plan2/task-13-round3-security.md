# Plan 2 Task 13 round 3 security review (1b97f16, ce35536, 495f7ba, 83902c7, fac0463, d314f8d vs 359ece3)

Tools: govulncheck 0 affected (1 unreachable module vuln, same as before); gosec on cmd/migrate, internal/db, internal/store: no findings; gitleaks 359ece3..HEAD: no leaks; `go test -p 2 -count=1` cmd/migrate, internal/db, internal/store: pass. Disk 26 GB free. Only throwaway testcontainers Postgres used; no migrate command run against the dev DB, no cp_* container touched. A temporary probe test in internal/db was added and removed (tree as committed).

## Status of round 2

- SEC-R2-1 closed. `db.Target` calls `poolConfig`, the same function `db.Connect` uses, and prints cfg.ConnConfig.Host/Port/Database plus every Fallback (deduped). Probe, with PGHOST/PGPORT/PGDATABASE set in the env (target == pgx config in every row):
  `?host=/tmp/sock` -> `/tmp/sock:7777/db`; `?dbname=other&port=6000` -> `localhost:6000/other`; `a:1,b:2` -> `a:1,b:2/db`; key=value DSN -> `h:7777/d`; `host=h,i port=1,2` -> `h:1,i:2/d`; IPv6 -> `[::1]:5433/db`; empty URL / `postgres:///` -> env defaults; `hostaddr=` is ignored by pgx and by Target alike. Unsafe or odd shapes (`sslkey/passfile/service` with a bad service, `host=a%20b`, `dbname=` empty, `u:12/pw#x@h` host-in-password) give ok=false -> `(unknown target)`, never a fragment. No user/password/sslkey/passfile text can reach output (only regex-checked host, port number, dbname). Fallback hosts are listed. TLS-fallback duplicates (sslmode prefer) collapse. Real run with unknown target: refused with exit 2 before any connection (runEncryptEnv, and again inside encryptEnv). Dry run with unknown target proceeds but is read-only (pass 1 + count; no write path), so a wrong database cannot be modified and only counts are printed. Residual (informational): Target and Connect each call ParseConfig, so a PGSERVICEFILE/passfile edited between the two calls could differ; not realistic.
- SEC-R2-2 closed. `too_large>0` appends an error at the very end (after every other row), exit 1, service ids in the message; runbook step 6, exit-code table and Output reference agree.
- SEC-R2-3 mostly closed, see SEC-R3-3 / SEC-R3-4 for defects in the new example.
- SEC-R2-4 closed (sha256 fingerprint of the key; `printf` is a shell builtin so the key is not in argv; only 12 hex chars of a hash of a 256-bit key are printed). Caveat SEC-R3-5.
- SEC-R2-5 closed as documentation (SIGKILL note). Verified behaviour unchanged (NotifyContext not stopped), the note is accurate.
- R-1 (connect/ping redaction) closed for connect and ping: fixed text + target + class (SQLSTATE only for PgError, `network error`, `timeout`, `interrupted`). Update failures print service id + `SQLSTATE xxxxx` or `database error`, never err text. See SEC-R3-1 for the read path, which is not redacted.
- R-4 (byte budget) closed. ListEnvVarsAfter: `cum` is a running sum over the id-ordered page, so `cum <= budget` selects a prefix; `OR rn = 1` adds the first row. Oversize rows weigh 0, are selected with value '' (CASE on `s.n > $3`, so the big value is never read), and a first row larger than the budget is returned alone (the next row has cum >= w1 > budget and is excluded), so progress is guaranteed and nothing is skipped: the next page starts after the last returned id, which is the end of the prefix. Weight-0 rows after an oversize first row are deferred to the next page, not lost. Ordering uses the same collation for `>` and `ORDER BY`. Empty page = end; no infinite loop. Peak memory per page <= 16 MiB + one value <= 8 MiB.
- R-3 (in-flight verification) closed with a caveat (SEC-R3-2): a fresh context (WithoutCancel + 5 s), counts at most once and returns immediately; no write is issued after the cancel (ctx checked per row, a cancelled ctx makes pgx refuse to send).

## Findings

[SEC-R3-1] severity: low
where: control-plane-go/cmd/migrate/encrypt.go (both `fmt.Errorf("read env vars: %w", err)`)
problem: Read errors are not redacted like connect and update errors. If the pooled connection drops mid-run and pgx re-dials and fails, the error is a pgconn connect error that names the DB user, database and host:port, and it is printed to stderr via realMain. No password and no value, so low. Probe output: ``failed to connect to `user=secretuser database=dbn`: 127.0.0.1:1 (127.0.0.1): dial error: dial tcp 127.0.0.1:1: connect: connection refused``.
fix: wrap with the same redaction as connectFailure (class / SQLSTATE only): `fmt.Errorf("read env vars failed (%s)", sqlState(err))`, keeping `%w` off the printed text or mapping net errors to "network error".

[SEC-R3-2] severity: low (informational)
where: encrypt.go inflightCommitted
problem: "exact" count is best effort. After cancel, pgx closes the connection; the server may still be executing the UPDATE. A verification SELECT on a fresh connection under READ COMMITTED sees the old value until the UPDATE commits, so `encrypted=` can understate by one and no "unverified" note is printed. Harmless (the next run reports it as already_encrypted), but "exactly" in the commit message overstates. Also EnvMap loads every value of the service (up to 8 MiB each), so a service with many large values makes the verification heavy; a targeted `SELECT "value" = $3` would be bounded.
fix: optional: verify with `SELECT 1 FROM "EnvVar" WHERE "serviceId"=$1 AND "key"=$2 AND "value"=$3` (no value read), and word the message "at least".

[SEC-R3-3] severity: low
where: docs/superpowers/runbook/encrypt-env.md "Resolving suspect rows" backup command
problem: (a) `pg_dump ... "$DATABASE_URL"`: the Go/Prisma-era URL carries `?schema=public`, which libpq rejects as an invalid URI query parameter, so the documented backup fails as written (the same applies to the step 2 `pg_dump`). (b) The dump holds plaintext env values and is written to `~` with the default umask (typically world-readable on a shared host). (c) A URL with the password on the command line is visible in `ps`.
fix: say to strip `schema=` (or use PG* env vars / a service name), `umask 077` or `chmod 600` the file and delete it after sign-off, and prefer `PGPASSFILE`/`PGPASSWORD` over a URL argument.

[SEC-R3-4] severity: low
where: runbook SQL block `suspect-row`
problem: The `[[ "$SID" =~ ... ]]` shell check validates a shell variable that is never passed to psql (the SQL block sets `\set sid` by hand), so it protects nothing. The "must say DELETE 1, else ROLLBACK" step only works when typed interactively; with `psql -f` the COMMIT runs regardless. The statement itself is sound (psql `:'var'` quoting, key + serviceId + `LIKE 'enc:v1:%'` predicate, single row, backup first); deleting one suspect Node-era row is sensible because its value is already unusable by the Go readers (a plain value that starts with `enc:v1:` fails to decrypt).
fix: pass the id with `psql -v sid="$SID"`, state "interactive only", or use a DO block that raises unless ROW_COUNT = 1.

[SEC-R3-5] severity: low (informational)
where: runbook step 2 fingerprint
problem: The command fingerprints `$ENV_ENCRYPTION_KEY` of the shell, but the tool also loads `./.env`. If the key lives only in `.env` the shell variable is empty and the printed digest is the empty-string hash (e3b0c44298fc), which looks like a normal mismatch but could be misread. Exit code 2 is also returned for an unknown target, but the exit-code table lists only "real run without --yes" for 2.
fix: tell the operator to expect/recognize the empty hash and to export the key from the same source as the tool; add "unknown target (real run)" to the code 2 row.

## Not proven by tests / noted
- Target vs real connection for the multi-host + `target_session_attrs` and `?host=` cases is covered by my probe only (the repo tests cover URL shapes; I did not find a test for the PG* env default).
- Mid-run connection loss text (SEC-R3-1) and the post-cancel race (SEC-R3-2) have no test.

RESULT: FINDINGS (0 high, 0 medium, 5 low/informational)

# Plan 2 Task 13 security review (commits 1d1651b, cd24aad): `migrate encrypt-env`

Tools: govulncheck 0 reachable (1 unreachable module vuln); gosec on cmd/migrate, internal/store, internal/secrets: no findings; gitleaks on 1d1651b^..cd24aad: no leaks; `go test -p 2` for cmd/migrate, store, secrets: pass. Disk 34 GB free. No migrate command was run against any real DB.

## Findings

[SEC-1] severity: high
where: control-plane-go/cmd/migrate/encrypt.go:78-90 (and runEncryptEnv)
problem: A wrong ENV_ENCRYPTION_KEY is not detected before writing. Every existing Go-written `enc:v1:` row is classed "suspect" (a warning, exit 0), while every plaintext row is encrypted with the wrong key. Those values become permanently unreadable once the right key is back, unless the operator keeps the wrong key. Exit code is 0, so scripts do not notice.
evidence: suspect path only increments counters and `continue`s; Errors is the only thing that makes the run fail.
fix: Pre-flight before the first write: sample (or just walk) all `enc:v1:` rows and try Decrypt. Refuse to write (non-zero exit) if any sampled row fails while some succeed, or if there are encrypted rows and zero decrypt. Better: two passes, pass 1 read-only classifying everything; only start pass 2 when suspect == 0 or the operator passes an explicit `--allow-suspect` flag. Also make suspect > 0 a non-zero exit (or distinct code, e.g. 2) in the real (non-dry) run, so cutover scripts stop. Dry-run should also exit non-zero on suspects.

[SEC-2] severity: medium
where: encrypt.go (runEncryptEnv, no target echo)
problem: No indication of which database is being modified. Running with the wrong DATABASE_URL (e.g. a leftover .env: main.go calls godotenv.Load() from the cwd) writes immediately with no confirmation.
fix: Print host:port/dbname (parse URL, never user/password/query) and the scanned/plaintext counts first. For non-dry runs, consider requiring `--yes` or a confirmation flag, or at least have the operator run `--dry-run` first (document it). Print the key fingerprint only if it is a truncated hash of a hash (e.g. first 4 bytes of SHA-256 of SHA-256(key)); optional, low value, skip if in doubt.

[SEC-3] severity: low
where: encrypt.go:96-101
problem: Plaintext Node-era values that start with `enc:v1:` stay in the database as plaintext. The Go API/worker will treat them as ciphertext and fail to decrypt at deploy time, so the service breaks, and the value stays in the clear. Counting and listing service ids is right, but there is no way to resolve them from the tool, and the message conflates this case with "wrong key" (see SEC-1; after SEC-1 the wrong-key case is excluded when the other rows decrypt, so the message can be narrowed).
fix: Document in the cutover runbook: list ids, then have owners re-save the value via the API (the API would encrypt it, check that the API write path does not skip values starting with the prefix). Optional `--force-plain-prefixed` out of scope.

[SEC-4] severity: low
where: encrypt.go:62-70, store.ListEnvVarsAfter
problem: Memory is bounded only by batch rows x value size (200 x unbounded; the test uses 1 MiB). Does not cap size; 200 x 1 MiB = ~200 MB plus ciphertext copies (~1.33x) transient. Acceptable for env vars but unbounded in principle. Not a correctness issue.
fix: Optional: lower batch (e.g. 50) or note the API's max value size. Check the API bounds the value length (not examined here).

[SEC-5] severity: low (informational)
where: encrypt.go:82 `st.ReplaceEnvVarIfEquals`
problem: Rows inserted during the run with an id sorting before the cursor are missed (cuids are roughly time-ordered so this is rare; ids from the Node API are also cuid). A later run catches them, and the API encrypts new writes anyway. Also, if run while the Node API is still live and writing plaintext, new rows are plaintext after the run. This is a runbook ordering point: run after cutover to the Go API (or re-run after), then run `--dry-run` and expect would_encrypt=0.
fix: Add a line to the runbook: finish with a dry-run that reports 0.

## Verified (no issue)

1. Secret hygiene: key comes from env via config.Load/RequireWorker/secrets.New; config errors name only the variable and decoded length, never the value. Not in args (flags: only --dry-run; extra args rejected; flag parse error echoes only the bad flag name). Output is counts + service ids only; error rows print service id and SQLSTATE only (driver error text deliberately dropped, good). `read env vars: %w` wraps a query whose parameters are an id cursor and limit: no values. Connect errors come from db.Connect, whose parse errors do not include the URL (comment at db.go:50; pool has no query tracer/logger). Residual: if Postgres runs with log_min_error_statement/log_statement=all the server log would hold the bound parameters in DETAIL for a failed UPDATE; that is server-side config, mention in runbook. Panics: none in the path. The service id list is not secret.
2. Crypto: same `secrets.Box.Encrypt` the API/worker use: AES-256-GCM, 12-byte random nonce from crypto/rand per call, no AAD (nil, same as Decrypt), `enc:v1:` + base64(nonce||ct). Empty and 1 MiB values round trip (tests). No double encryption: prefix check precedes Encrypt, and the CAS compares the old plaintext. Suspect path leaks nothing (ids only) and cannot be abused: the row is not modified and no decrypted output or error text from Decrypt is printed (its errors are static strings anyway).
3. Data safety: CAS is `WHERE serviceId AND key AND value = $3` with bound params; text equality is exact (NULL cannot match, but column is NOT NULL in the baseline; empty and very long values compared exactly). Concurrent API write: the API's value differs (or is ciphertext) so the update affects 0 rows and is counted as a conflict; same-value ABA is harmless (result is still the correct encryption of the current value). Delete+recreate with same key and value is also harmless. Partial failure: errors counted, loop continues, exit 1 with a repeat-safe message; context cancellation returns early. Each CAS is its own statement (no long tx, no table lock); a kill midway leaves a valid mix and rerun is idempotent. Dry run: the only write is after the `opt.DryRun` branch `continue`s, so no write is reachable (also tested). Keyset paging `id > $1 ORDER BY id LIMIT` skips nothing and repeats nothing on a stable set; soft-deleted services are included (FK RESTRICT; harmless).
4. ListUnencryptedEnvVars/SetEnvVarValue: no references remain in Go code. They are mentioned only in the plan document (historical plan text, lines 295-602 and 4011-4020), no code impact.
5. Wrong database / exit codes: see SEC-1/SEC-2. Exit 1 occurs on config error, connect error, read error and row errors; 0 otherwise, including suspect and conflicts. A conflicts>0 run exits 0 (it prints a note); consider non-zero for the cutover so a script reruns.

## Tests: not proven
- Wrong key with mixed plaintext + ciphertext rows: the test only asserts rows with another key are skipped, it does not (and cannot today) show that plaintext is protected from encryption under the wrong key (SEC-1).
- No test that a SQL/driver error never echoes values other than the trigger case (which uses a plpgsql RAISE with fixed text); a server error whose DETAIL contains the value is only covered by sqlState dropping the text (by construction).
- No test of the real `main`/`run` path (godotenv, os.Args, exit code); runEncryptEnv is tested with an injected getenv/connect.
- No test of rows inserted behind the cursor, or concurrent insertion during paging; no test for NULL (column NOT NULL), invalid UTF-8 (not storable in Postgres text).
- Memory use on 200 x large values is not measured (1 MiB single-row only).
- Interruption (SIGINT/ctx cancel midway) then rerun is not tested; only idempotent re-runs are.
- The CAS race is simulated deterministically through the afterRead hook, not with truly concurrent writers.

RESULT: FINDINGS (1 high, 1 medium, 3 low)

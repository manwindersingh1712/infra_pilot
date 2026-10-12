# Runbook: encrypt the Node-era plain-text env vars (`migrate encrypt-env`)

The Node API stored env var values as plain text. The Go API and worker store and read them as `enc:v1:` ciphertext (AES-256-GCM, key `ENV_ENCRYPTION_KEY`). `encrypt-env` rewrites every remaining plain-text value in place. It is safe to repeat and can be stopped at any point (Ctrl-C, SIGTERM): every row is its own compare-and-swap update, so a value saved through the API while the command runs is never overwritten (it is counted as a conflict and picked up by the next run).

Run it from `control-plane-go/`:

```bash
go run ./cmd/migrate encrypt-env [--dry-run] [--yes] [--allow-suspect]
```

It reads `DATABASE_URL` and `ENV_ENCRYPTION_KEY` from the environment, and from `./.env` if one exists in the working directory (the command prints the path of the file it loaded, never its content). A stray `.env` can silently select the database: always read the `target:` line before you confirm anything.

Never print the key or the database URL (no `echo`, no `set -x`). `DATABASE_URL` must have a percent-encoded password (`@` is `%40`, `/` is `%2F`, `:` is `%3A`, `#` is `%23`).

## Cutover sequence

1. **Stop the Node API first**, so nothing writes plain text any more. If you cannot stop it, at least confirm that only the Go API is serving writes. A plain value written after the run stays plain (the readers accept it) but is not migrated, and the final dry run in step 6 will show it.
2. **Back up** the database (`pg_dump`, stored off the host) and **back up `ENV_ENCRYPTION_KEY`** (password manager). Without the key every encrypted value is unreadable for good. Do not paste either into a terminal log.
3. `encrypt-env --dry-run`. Check ALL of these before going on:
   - `target:` is the database you mean (host:port/dbname).
   - `suspect=0`. If not, see "Resolving suspect rows" below and do not go on.
   - `too_large=0` (see the table below), and `would_encrypt` is a plausible count (about the number of env vars you expect to be plain).
   - The command exits 0.
4. `encrypt-env --yes`. Without `--yes` a real run writes nothing, prints what it would do and exits 2.
5. **Repeat step 4 until the last line no longer says `conflicts>0: run again until conflicts=0`** and the summary shows `conflicts=0 errors=0`. A run with conflicts exits 0 on purpose; a script must look at the summary, not only the exit code. If you see `errors>0` the exit code is 1: fix the cause (the line names the service id and the SQLSTATE) and run again.
6. A final `encrypt-env --dry-run` must report `would_encrypt=0` and `suspect=0`. Anything else means a plain value was written (or a row is still unresolved): go back to step 3.

## Resolving suspect rows

A "suspect" value starts with `enc:v1:` but does not decrypt with the current key. There are two causes.

- **A Node-era plain-text value that happens to start with `enc:v1:`.** The tool cannot tell it from ciphertext, leaves it untouched and lists the service ids. Until it is fixed the Go API and worker fail to decrypt it (a deploy of that service breaks). Fix: ask the owner to re-save the value through the API (the Go API always encrypts what it receives, including values starting with `enc:v1:`), or, after taking a backup, rewrite it in SQL. Then re-run the dry run.
- **A wrong `ENV_ENCRYPTION_KEY`.** Signs: `suspect` is large, it includes rows the Go API wrote earlier, and `already_encrypted=0`. The command refuses this case on its own: if some `enc:v1:` rows exist, none of them decrypt and plain rows exist, it exits 1 without writing, and `--allow-suspect` does NOT override it. Fix the key (the one the Go API and worker run with), never the data. Writing with the wrong key would lock every plain value under it for good.

`--allow-suspect` exists for the first cause only: after you have looked at every listed service and accept that those rows stay as they are, it encrypts the plain rows and leaves the suspect rows untouched (the warning is still printed). Use it only when `already_encrypted` is greater than 0.

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Done (including `conflicts>0`, which needs another run). A dry run that completed with `too_large=0`. |
| 1 | Refused or failed: suspect rows without `--allow-suspect`, the wrong-key guard, a bad key or config, a database or read error, `errors>0`, `too_large>0` (reported at the very end, after every other row was processed; a dry run too), or interrupted (SIGINT/SIGTERM). No write happens in the two refusals. |
| 2 | A real run without `--yes`. Nothing was changed. |

## Output reference

`pre-check: scanned= plain= already_encrypted= suspect= too_large=` is the read-only pass that always runs first. The final line `scanned= encrypted= already_encrypted= suspect= too_large= conflicts= errors=` is the result (`would_encrypt=` instead of `encrypted=` in a dry run). Output holds counts and service ids only, never a key name or a value.

`too_large` rows are values over 8 MiB: they are not read, not changed and listed by service id. The API accepts 32 KiB, so these are Node-era oddities; look at them by hand. Because they stay in plain text, the command exits 1 when `too_large>0` (after finishing every other row); the last error line names the service ids. Delete or re-save those variables through the API, then run again.

## Postgres server logs

The command sends the old and new values as bound parameters of an `UPDATE`. If the server logs statements with their parameters (`log_statement = all`, or `log_min_error_statement` at `error` or lower after a failed update), plain-text values and ciphertext can end up in the server log. Turn that logging off for the run, or accept it and protect and rotate those logs.

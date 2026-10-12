# Runbook: encrypt the Node-era plain-text env vars (`migrate encrypt-env`)

The Node API stored env var values as plain text. The Go API and worker store and read them as `enc:v1:` ciphertext (AES-256-GCM, key `ENV_ENCRYPTION_KEY`). `encrypt-env` rewrites every remaining plain-text value in place. It is safe to repeat and can be stopped at any point (Ctrl-C, SIGTERM): every row is its own compare-and-swap update, so a value saved through the API while the command runs is never overwritten (it is counted as a conflict and picked up by the next run).

Run it from `control-plane-go/`:

```bash
go run ./cmd/migrate encrypt-env [--dry-run] [--yes] [--allow-suspect]
```

It reads `DATABASE_URL` and `ENV_ENCRYPTION_KEY` from the environment, and from `./.env` if one exists in the working directory (the command prints the path of the file it loaded, never its content). A stray `.env` can silently select the database: always read the `target:` line before you confirm anything. The line shows what the driver will really connect to (`host:port[,fallback host:port]/dbname`, after URL query overrides such as `?host=`, `?port=`, `?dbname=`, and `PG*` environment defaults), never the user or password. A `key=value` DSN (`host=... dbname=...`) is supported only if the `target:` line prints for it. If it says `target: (unknown target)`, a real run is refused (exit 2, with or without `--yes`; `--dry-run` still works): fix `DATABASE_URL`. A failed connection prints a fixed message with the target and the error class (for example `SQLSTATE 28P01` for a wrong password), never the driver text.

Never print the key or the database URL (no `echo`, no `set -x`). `DATABASE_URL` must have a percent-encoded password (`@` is `%40`, `/` is `%2F`, `:` is `%3A`, `#` is `%23`).

## Cutover sequence

1. **Stop the Node API first**, so nothing writes plain text any more. If you cannot stop it, at least confirm that only the Go API is serving writes. A plain value written after the run stays plain (the readers accept it) but is not migrated, and the final dry run in step 6 will show it.
2. **Back up** the database (see "Backup commands" below; copy it off the host) and **back up `ENV_ENCRYPTION_KEY`** (password manager). Without the key every encrypted value is unreadable for good. Do not paste either into a terminal log.
   **First-run key check (mandatory).** On a database with no `enc:v1:` rows yet, a wrong `ENV_ENCRYPTION_KEY` cannot be detected by the tool: it would lock every plain value under the wrong key for good. Before the first real run, confirm the key in your shell is the SAME key the Go API and worker run with. Compare sha256 fingerprints, never the key itself (no `echo`, no `set -x`, nothing pasted into a log):

   ```bash
   printf %s "$ENV_ENCRYPTION_KEY" | shasum -a 256 | cut -c1-12      # in the shell you run encrypt-env from
   ```

   Run the same line in the environment of the Go API or worker (same secret store, same `.env` source) and compare the 12 characters. Where you cannot, the alternative is to save one test env var through the Go API first (the database then holds an `enc:v1:` row written with the right key), run `encrypt-env --dry-run` and check that it shows `already_encrypted` greater than 0 and `suspect=0`: a wrong key would fail that row and the wrong-key guard would refuse the run.
3. `encrypt-env --dry-run`. Check ALL of these before going on:
   - `target:` is the database you mean (host:port/dbname).
   - `suspect=0`. If not, see "Resolving suspect rows" below and do not go on.
   - `too_large=0` (see the table below), and `would_encrypt` is a plausible count (about the number of env vars you expect to be plain).
   - The command exits 0.
4. `encrypt-env --yes`. Without `--yes` a real run writes nothing, prints what it would do and exits 2 (but exits 1 while unresolved suspect rows make it refuse first). A second Ctrl-C does not kill a hung query (the first one only cancels the run; a stuck connect or query keeps the process alive): use `kill -9` (SIGKILL). That is safe: every row is its own compare-and-swap, so a killed run leaves each row either plain or fully encrypted; run it again.
5. **Repeat step 4 until the last line no longer says `conflicts>0: run again until conflicts=0`** and the summary shows `conflicts=0 errors=0`. A run with conflicts exits 0 on purpose; a script must look at the summary, not only the exit code. If you see `errors>0` the exit code is 1: fix the cause (the line names the service id and the SQLSTATE) and run again.
6. A final `encrypt-env --dry-run` must exit 0 and report `would_encrypt=0`, `too_large=0` and `conflicts=0`, and either `suspect=0`, or, when you used `--allow-suspect` on purpose, the final dry run must be run WITH `--allow-suspect` too (without it a dry run with suspect rows exits 1) and must show the same suspect service ids you accepted in step 3, nothing new. Anything else means a plain value was written or a row is still unresolved: go back to step 3. (`too_large>0` also makes the command exit 1, see the exit codes.)

## Backup commands

`pg_dump` (libpq) does not accept the `?schema=public` suffix of a Prisma-style `DATABASE_URL` (`invalid URI query parameter: "schema"`), and a URL on the command line puts the password in `ps`. So take the connection from `PG*` variables and a password file, never from the URL. The dump holds the env values (plain text for the Node-era rows), so it must be owner-only:

```bash
umask 077                                        # every file below is created owner-only (dir 700, files 600)
mkdir -p "$HOME/infrapilot-backup"
export PGHOST=db.example.internal PGPORT=5432 PGUSER=cp PGDATABASE=control_plane   # the values from the target: line
export PGPASSFILE="$HOME/.pgpass-infrapilot"     # one line: host:port:database:user:password
                                                 # (write ':' and '\' in the password as '\:' and '\\'; the file must be mode 600)
pg_dump --format=custom --file="$HOME/infrapilot-backup/full-$(date +%Y%m%d-%H%M%S).dump"
pg_dump --format=custom --data-only --table='"EnvVar"' --file="$HOME/infrapilot-backup/envvar-$(date +%Y%m%d-%H%M%S).dump"
pg_restore --list "$HOME"/infrapilot-backup/envvar-*.dump | tail -3     # sanity check: lists the EnvVar table data
```

Create the password file once with `umask 077` in effect (`printf '%s\n' 'host:5432:control_plane:cp:the-password' > "$HOME/.pgpass-infrapilot"`; type the line in an editor if the password must not enter shell history). If you cannot use a password file, read the password without echo into the environment of that shell only: `read -rs PGPASSWORD; export PGPASSWORD`. Do not put it in the URL or in an argument.

Keep the dumps in `$HOME/infrapilot-backup` (mode 700) or move them to encrypted storage off the host. Delete them (`shred -u` or `rm`) after sign-off, that is once the step 6 dry run passed and the Go stack has run a few days on the migrated data, and delete the password file when you no longer need it. Until then treat them like the database itself.

## Resolving suspect rows

A "suspect" value starts with `enc:v1:` but does not decrypt with the current key. There are two causes.

- **A Node-era plain-text value that happens to start with `enc:v1:`.** The tool cannot tell it from ciphertext, leaves it untouched and lists the service ids. Until it is fixed the Go API and worker fail to decrypt it (a deploy of that service breaks). Fix: ask the owner to re-save the value through the API (the Go API always encrypts what it receives, including values starting with `enc:v1:`). That is the only supported way to put a new value in. If the owner cannot, remove just that one variable so it can be created again through the API (never write a value from SQL). Back up first, check the service id, look at the row, and delete exactly one row in a transaction:

  Take the backup first (see "Backup commands"; the `EnvVar`-only dump is enough here), then pick the service id and key name and check the row.

  ```sql suspect-row
  \set sid 'cm...'
  \set k 'THE_KEY_NAME'
  SELECT "id", "serviceId", "key", length("value") AS len FROM "EnvVar" WHERE "serviceId" = :'sid' AND "key" = :'k';   -- exactly one row, the one you mean
  BEGIN;
  DELETE FROM "EnvVar" WHERE "serviceId" = :'sid' AND "key" = :'k' AND "value" LIKE 'enc:v1:%';
  -- must say DELETE 1. Anything else: ROLLBACK;
  COMMIT;
  ```

  Then have the variable created again through the API and re-run the dry run.
- **A wrong `ENV_ENCRYPTION_KEY`.** Signs: `suspect` is large, it includes rows the Go API wrote earlier, and `already_encrypted=0`. The command refuses this case on its own: if some `enc:v1:` rows exist, none of them decrypt and plain rows exist, it exits 1 without writing, and `--allow-suspect` does NOT override it. Fix the key (the one the Go API and worker run with), never the data. Writing with the wrong key would lock every plain value under it for good.

`--allow-suspect` exists for the first cause only: after you have looked at every listed service and accept that those rows stay as they are, it encrypts the plain rows and leaves the suspect rows untouched (the warning is still printed). Use it only when `already_encrypted` is greater than 0.

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Done (including `conflicts>0`, which needs another run). A dry run that completed with `too_large=0`. |
| 1 | Refused or failed: suspect rows without `--allow-suspect`, the wrong-key guard, a bad key or config, a database or read error, `errors>0`, `too_large>0` (reported at the very end, after every other row was processed; a dry run too), or interrupted (SIGINT/SIGTERM). No write happens in the two refusals. |
| 2 | A real run without `--yes`. Nothing was changed. |

## Output reference

`pre-check: scanned= plain= already_encrypted= already_encrypted_large= suspect= too_large=` is the read-only pass that always runs first. The final line `scanned= encrypted= already_encrypted= already_encrypted_large= suspect= too_large= conflicts= errors=` is the result (`would_encrypt=` instead of `encrypted=` in a dry run). Output holds counts and service ids only, never a key name or a value.

The size cap (8 MiB) applies to the PLAINTEXT. A plain value up to 8 MiB is encrypted, even though its `enc:v1:` ciphertext is about a third bigger (a 7 MiB value becomes about 9.4 MiB) and so is over the cap on later runs. On a later run such an oversize value that starts with `enc:v1:` is counted as `already_encrypted_large`: it is never loaded and NOT decrypt-verified (assumed to be ciphertext; a wrong key cannot be detected on it, and it counts neither as `already_encrypted` nor as `suspect` for the wrong-key guard). It does not make the run fail. `already_encrypted_large` is printed in the pre-check, the summary and a note line.

`too_large` rows are plain (not `enc:v1:`-prefixed) values over 8 MiB: they are not read, not changed and listed by service id. The API accepts 32 KiB, so these are Node-era oddities; look at them by hand. Because they stay in plain text, the command exits 1 when `too_large>0` (after finishing every other row); the last error line names the service ids. Delete or re-save those variables through the API, then run again.

## Memory

Each read batch holds at most 50 rows and about 16 MiB of values (a single value is capped at 8 MiB), so peak memory is a few tens of MiB for typical data and about 250 MiB in the worst case of 8 MiB values; it does not grow with the table. Measured with 50 values of 8 MiB: about 114 MB peak RSS for a dry run, about 253 MB for a real run.

## Postgres server logs

The command sends the old and new values as bound parameters of an `UPDATE`. If the server logs statements with their parameters (`log_statement = all`, or `log_min_error_statement` at `error` or lower after a failed update), plain-text values and ciphertext can end up in the server log. Turn that logging off for the run, or accept it and protect and rotate those logs.

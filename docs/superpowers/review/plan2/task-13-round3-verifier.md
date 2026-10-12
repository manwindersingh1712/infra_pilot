# Plan 2 Task 13, round 3 verification (final fix round)

Commits: 1b97f16, ce35536, 495f7ba, 83902c7, fac0463, d314f8d on 359ece3. HEAD d314f8d, branch go-migration.
Free disk before: 26 GB; after: 25 GB.

RESULT: PASS (one low-severity finding, VER-1, a runbook/behavior gap for values larger than about 6 MiB).

## Suite (control-plane-go/)
- `go build ./...` rc=0
- `go vet ./...` rc=0
- `golangci-lint run --allow-parallel-runners ./...` -> `0 issues.`
- `gosec -quiet -exclude=G204 ./...` rc=0, no findings
- `gitleaks git --redact` (repo root) -> `419 commits scanned ... no leaks found`
- `go test -race -count=3 -p 2 ./cmd/... ./internal/store/ ./internal/secrets/ ./internal/db/` -> all ok (cmd/migrate 9.1s, cmd/worker, store, secrets, db)
- `go test -race -count=1 -p 2 ./...` -> all ok (api 76s, runtime 70s, worker/outbox ok first time); rc=0

## Scenarios (built binary, throwaway postgres:16 `vfy13c-pg`, random port 25493, removed afterwards; keys via env var only)
Exit codes match the runbook table in every case below.

(A) correct key
- dry-run: `target: 127.0.0.1:25493/vfydb`, `would_encrypt=5`, exit 0.
- no `--yes`: `would encrypt 5 plain value(s)... Nothing was changed`, exit 2.
- `--yes`: `encrypted=5`, exit 0. Rerun: `encrypted=0 already_encrypted=5`, exit 0. Final dry-run: `would_encrypt=0`, exit 0.
- With 2 suspect rows, 2 plain rows, 1 value of 9 MiB: dry-run, no-`--yes` and `--yes` all exit 1 with the suspect refusal, row checksum unchanged (`a56334d5...|10|7` before and after).
- `--allow-suspect --yes`: `encrypted=2 ... suspect=2 too_large=1`, exit 1. Last line: `migrate: too_large=1 value(s) over the size limit were skipped and never encrypted: ... (services [cmbigvalue000000000000001])` (ids only). Rerun same, exit 1.
- Step 6 wording works as written: with the 9 MiB row present the final dry-run (with `--allow-suspect`) exits 1 with `too_large=1` (the runbook says it must report too_large=0, so a sign-off correctly fails). After deleting the big row: `--allow-suspect --dry-run` -> `would_encrypt=0 suspect=2 too_large=0`, exit 0, same suspect ids. The same dry-run WITHOUT `--allow-suspect` exits 1 (as documented). `--allow-suspect` without `--yes` exits 2.

(B) wrong key (6 Go-encrypted rows + 1 plain row)
- `--allow-suspect --yes`: `error: no enc:v1: value decrypts and plain rows exist...--allow-suspect does not override this.` exit 1. Dry-run and plain `--yes` the same. Checksum unchanged (`0f9f775d...|7|6`).

(C) SIGTERM, 30,000-row table (50-row batches)
- During read (0.25 s, before the pre-check line): `interrupted: stopped before finishing (encrypted=0 so far); nothing further was written; run again to resume`, exit 1, 0 rows encrypted.
- During write, 14 interrupted runs at random delays: printed `encrypted=N so far` equalled the DB delta of `enc:v1:` rows every time (e.g. 2954/2954, 4435/4435, 3540/3540, 3411/3411, 2108/2108, 2174/2174, 4189/4189, 4159/4159; earlier series 2498, 5558), never a "could not be verified" note, exit 1.
- Big-value table (50 x 8 MiB): SIGTERM at 4 s/7 s/9 s printed 2, 14, 20; DB totals 2, 16, 36: exact.
- Rerun converges: after the interrupted runs, `--yes` -> `encrypted=3208` (or 3030), conflicts=0 errors=0, exit 0; final dry-run `would_encrypt=0 already_encrypted=30000`, exit 0.
- Not separately forced: an interrupt specifically in pass 2's read (the code path is the same message). The read-during-pass-1 case was observed.

(D) TARGET line vs pgconn.ParseConfig (scratch program) and, where reachable, a real connection
| case | printed | truth | match |
|---|---|---|---|
| plain URL | `127.0.0.1:25493/vfydb` | same | yes (connected) |
| `?host=/tmp/sock` | `/tmp/sock:25493/vfydb` (shown in connect-failure message) | `/tmp/sock:25493/vfydb` | yes |
| `?dbname=other&port=5999` | `127.0.0.1:5999/other` | same | yes |
| URL port 1 with `?port=25493` | `127.0.0.1:25493/vfydb` | same | yes (connected) |
| `?dbname=other` | `127.0.0.1:25493/other` | same | yes (connected to db `other`) |
| key=value DSN | `127.0.0.1:25493/vfydb` | same | yes (connected) |
| `postgres://u:p@h1:1,h2:2/db` | `h1:1,h2:2/db` | primary h1:1, fallbacks h1:1 h2:2 h2:2 (duplicates) | yes (deduped) |
| multi-host with live fallback | `127.0.0.1:1,127.0.0.1:25493/vfydb` | same | yes (connected via fallback) |
| IPv6 `[::1]:25493` | `[::1]:25493/vfydb` | `::1:25493/vfydb` | yes (bracketed) |
| IPv6 `[2001:db8::1]:6543` | `[2001:db8::1]:6543/vfydb` | same | yes |
| PGHOST/PGPORT/PGDATABASE env, URL without them | `127.0.0.1:25493/vfydb` | same | yes (connected) |
| same env, URL with db in path | `.../vfydb` (URL wins over PGDATABASE=other) | `.../vfydb` | yes |
| empty dbname | `(unknown target)` | pgconn db is empty | refused as unknown |
| password `p@s/s?w#r%40d` percent-encoded (`p%40s%2Fs%3Fw%23r%2540d`) | `127.0.0.1:25493/vfydb` | same | yes (connected) |

Unknown target: empty dbname and `?host=bad%20host` with the DB STOPPED: `--yes` exit 2, no-`--yes` exit 2, output is `target: (unknown target)` plus `refusing a real run: the database target cannot be determined (nothing was changed)`, with no connection error, so no connection was attempted. `--dry-run` with an unknown target proceeds to connect (exit 1 only because the DB was stopped). A known target with the DB stopped exits 1 with the network-error message.

(E) connect failures: all print only `cannot connect to the database at <target> (<class>); check the target, user, password and database name in DATABASE_URL`.
- wrong password and wrong user: `(SQLSTATE 28P01)`; unreachable host: `(network error)`; nonexistent db: `(SQLSTATE 3D000)`; `sslmode=require` against non-TLS server: `(connection error)`; bad port / bad percent-escape / bad DSN syntax: `parse DATABASE_URL: invalid URL` / `invalid connection string`. All exit 1. Zero hits for user, password, marker or driver text (`failed to connect`, `dial`, `FATAL`).
- Marker in the path after the db (`/vfydb/MARKERPW_path`): `(unknown target)`, marker not shown. A marker used AS the database name (`/MARKERPW_db`) is shown, by design (the dbname is part of the target; it is not a credential).
- Not a connect failure: a read error on the wrong database prints `read env vars: ERROR: relation "EnvVar" does not exist (SQLSTATE 42P01)`, i.e. driver text with a table name only. No secret, noted for completeness.

(F) memory, 50 values of 8,384,012 bytes: `/usr/bin/time -l`
- dry-run: 89,686,016 bytes max RSS (about 90 MB), 6.3 s.
- `--yes`: 236,322,816 bytes max RSS (about 236 MB), 16.7 s.
- The runbook says about 114 MB and 253 MB: the measured values are below those, so the documented bounds hold.

(G) leak grep over combined stdout+stderr of every run (36 KB; about 47 exits recorded). Counts: PLAINSECRET 0, BIGSEED 0, BIGPLAINTEXT 0, PLAINSUSPECT 0, DB password 0, DB user 0, both encoded/decoded special-char passwords 0, key A 0, key B 0 (with and without trailing `=`). The only `MARKERPW` hit is the deliberate dbname case in (E).

Other exit codes: missing key, non-base64 key, unknown flag: exit 1 with a fixed message.

## Findings
- [VER-1] (low) Plain values larger than about 6 MiB (6,291,456 bytes) become ciphertext larger than the 8 MiB cap (base64 x 4/3). After a successful real run (50 values of 8.38 MB, all encrypted: `scanned=50 encrypted=50`, exit 0), every later run re-reads them as `too_large=50 already_encrypted=0` and exits 1, so the runbook's step 6 final dry run (`too_large=0`, exit 0) can never pass for such data, and the `already_encrypted` check/wrong-key guard ignore those rows. Quote: `pre-check: scanned=50 plain=0 already_encrypted=0 suspect=0 too_large=50`, `[exit=1]`. Data is safe (nothing is double-encrypted, DB lengths 11,178,727). Only Node-era values between 6 and 8 MiB are affected (the API limit is 32 KiB), so this can be documented ("values over about 6 MiB are encrypted but then reported as too_large") rather than fixed.
- No other failures.

Cleanup: `vfy13c-pg` removed (`docker rm -f`; 0 left). No cp_* containers, volumes, ~/cp-backups or ~/cp-logs touched. The real database was never connected to. Scratch files stay in the session scratchpad. `git status` shows no tracked changes other than the pre-existing `public/assets/.DS_Store`.

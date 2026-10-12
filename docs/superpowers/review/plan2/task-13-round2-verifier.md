# Plan 2 Task 13, round 2 verification (Verifier)

Commits verified: b098970, 95ab550, 4e7a9cf, b83c2ed, 359ece3 (on cd24aad), branch go-migration, control-plane-go/.

RESULT: PASS (all required scenarios pass; 4 non-blocking findings below)

## Suite (from control-plane-go/)
- `go build ./...` rc=0; `go vet ./...` rc=0
- `golangci-lint run --allow-parallel-runners` -> `0 issues.`
- `gosec -quiet -exclude=G204 ./...` rc=0, no findings
- `gitleaks git --redact` (repo root) -> `413 commits scanned ... no leaks found`
- `go test -race -count=3 -p 2 ./cmd/... ./internal/store/ ./internal/secrets/` -> ok cmd/migrate 7.4s, cmd/worker, internal/store, internal/secrets; rc=0
- `go test -race -count=1 -p 2 ./...` -> every package ok (api 77.7s, runtime 73.7s, outbox 7.1s, workflows 8.7s ...), rc=0, no flake.
- Disk: 32 GiB free at start, 29 GiB at end.

## End-to-end (built binary, throwaway postgres:16 `vfy13b-pg` (+ `vfy13b-pg6` for IPv6), both removed)
Binary was run from a scratch cwd (no .env). Seed: plain ASCII, empty, unicode, multi-line, 1 MiB, 9 MiB, valid key-A ciphertext x2, `enc:v1:` garbage, key-B ciphertext, plain value starting `enc:v1:`, 2 soft-deleted service rows, 1500 bulk rows (1513 rows).

(A) correct key (KA)
- `--dry-run` with suspect rows: `target: 127.0.0.1:26831/vfydb` / `pre-check: scanned=1513 plain=1507 already_encrypted=2 suspect=3 too_large=1`, exit 1 (suspect refusal; no credentials in target line). Table checksum unchanged.
- real run without `--yes`, with suspect: exit 1 (suspect check comes before the confirm check). With `--allow-suspect` and no `--yes`: exit 2, "Nothing was changed", checksum unchanged. On a clean DB (scenario C) no `--yes` -> exit 2.
- `--yes` with suspect rows: exit 1 "refusing to write ... (nothing was changed)", checksum unchanged.
- `--allow-suspect --yes`: exit 0, `scanned=1513 encrypted=1507 already_encrypted=2 suspect=3 too_large=1 conflicts=0 errors=0`. Suspect rows + 9 MiB row: md5 identical before/after. Decrypting results with an independent decryptor: plain, empty, unicode (exact), multi-line, 1 MiB (len 1048587), bulk, soft-deleted all round-trip.
- rerun: `encrypted=0`, exit 0. Final `--allow-suspect --dry-run`: `would_encrypt=0`. Final dry-run WITHOUT `--allow-suspect`: exit 1 (see finding R-2).

(B) wrong key (KB) vs KA-encrypted + plain rows: `--dry-run`, `--yes`, `--allow-suspect --yes`, `--allow-suspect` all exit 1 "almost certainly the wrong ENV_ENCRYPTION_KEY (nothing was changed)"; md5 `5ba05c41...` unchanged. Then the right key on the same DB: exit 0, `encrypted=4 already_encrypted=3`.

(C) plain-only DB, fresh key KC: no-`--yes` exit 2 unchanged; dry-run exit 0; `--yes` encrypted=3 exit 0; final dry-run would_encrypt=0; empty-value row ok.

(D) SIGTERM, 120000-row table: 6 runs, killed 2-7 s in. All: exit 1, `interrupted: stopped before finishing (encrypted=N so far); nothing further was written; run again to resume` + `migrate: interrupted: context canceled`. After each, all `enc:v1:` rows matched the exact ciphertext shape and a 300-row sample decrypted to the original; every other row was byte-identical plain. Rerun: exit 0, `scanned=120000 encrypted=108603 already_encrypted=11397`; final dry run would_encrypt=0. One earlier kill landed in the read: output was `migrate: read env vars: context canceled` (exit 1, still safe, but not the "interrupted ... run again" text, see R-3).

(E) concurrency (120000 rows, an updater session rewriting rows just ahead of the cursor, 61950 rows rewritten): run 1 `encrypted=115487 conflicts=4513 errors=0`, prints `conflicts>0: run again until conflicts=0`, exit 0. Run 2: `conflicts=0`. Independent check of all 120000 rows: ok=120000 bad=0 (every updated row decrypts to the updater's value, every other to its original). No lost updates.

(F) leaks: 30 run logs (stdout+stderr) searched for 21 patterns (all seeded plaintexts, the 3 keys, DB password, DB user `vfyusr`): 0 hits.
Target-line / error output for odd DSNs (role `spec`, password `p@ss/w?rd#1%40x:y`, URL-encoded):
- `postgresql://spec:<enc>@127.0.0.1:P/vfydb?sslmode=disable` -> `target: 127.0.0.1:26831/vfydb` (no user, no password fragment)
- IPv6 `@[::1]:26832/vfydb` -> `target: [::1]:26832/vfydb`
- key=value DSN -> `target: (unparseable DATABASE_URL, not shown)` (run still proceeds and works)
- unencoded password with `@ / ? #` -> `migrate: parse DATABASE_URL: invalid connection string`; `p%zz` -> `invalid URL` (no fragment)
- unix-socket style `postgresql://spec:...@/vfydb?host=/var/run/postgresql` -> connect error (no socket on host), no target line; a success path via a socket was not testable on this host (code: empty host -> "(unparseable ...)").
- Password never appears in any output except F7 below.

(G) memory (`/usr/bin/time -l`, peak RSS): empty 11.0 MiB; one 9 MiB row (too_large, dry and real) 11.2 MiB; one 1 MiB row (real) 25.3 MiB; 50 x 1 MiB (one batch) 136 MiB; 50 x ~8 MiB (just under cap, one batch) 751 MiB.

Exit codes 0/1/2 match the runbook table in every case above; bad key / no key / bad flag / extra arg exit 1; `.env` in cwd prints `loaded env file: <path> (values not shown)` and explicit env vars win over it.

## Findings (none blocking)
- R-1 (low, leak): pgx connect errors print the DB user, and for a mis-built URL a password fragment. `failed to connect to \`user=spec database=vfydb\`` (wrong password, unix socket, missing db). Worst case: a password placed in the URL path gives `database "vfydb?password=p@ss" does not exist` and prints `p@ss` (F7). The password itself was not printed with correct or wrong-password URLs. The Task 13 intent "never print the DB URL" holds for the target line but not for connect errors; consider wrapping the ping error to `SQLSTATE`/host only, or note it in the runbook.
- R-2 (low, runbook): step 6 says the final dry run must show `suspect=0` and exit cleanly. On the `--allow-suspect` path suspect stays >0 and a final dry run without `--allow-suspect` exits 1. Say: on the allow-suspect path add `--allow-suspect` to the final dry run and expect `would_encrypt=0` with the same suspect count. Also step 5/6 should say a real run without `--yes` exits 1 (not 2) while suspect rows are unresolved.
- R-3 (low): SIGTERM that lands in a DB read gives `read env vars: context canceled` (exit 1) instead of the "interrupted ... run again" text; and the `encrypted=N so far` figure can be 1 lower than the rows actually written (an in-flight UPDATE that committed as the context was cancelled). Both harmless: data valid, rerun resumes.
- R-4 (info): worst-case memory for one 50-row batch of just-under-8 MiB values is about 750 MiB RSS (bounded, not unbounded). Runbook could mention it for hosts with small memory, or the batch could shrink by byte budget. A `key=value` DSN prints `(unparseable DATABASE_URL, not shown)`, so the operator cannot confirm the target by eye; the runbook says "always read the target line" without covering that case. The flag-parse usage text omits `--yes`/`--allow-suspect`. Warnings (suspect, too_large) print twice (pre-check, then summary).

## Runbook accuracy
Steps 1-6, the suspect/wrong-key section, the exit-code table, `too_large` (not read, exit 0, listed by service id) and the conflicts rerun loop all matched observed behavior, except R-2 above. The runbook says to run `go run ./cmd/migrate` from control-plane-go/, where a real `.env` exists; the printed `loaded env file:` and `target:` lines make that visible, but this verification deliberately used a built binary from a clean directory.

## Safety / cleanup
No command touched control_plane / cp_postgres or any cp_* container, ~/cp-backups or ~/cp-logs. Containers `vfy13b-pg` and `vfy13b-pg6` removed; /tmp/verify-13b and the throwaway key files deleted. Repo code not edited; this file is uncommitted.

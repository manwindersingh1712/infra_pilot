# Plan 2 Task 13 verifier report (commits 1d1651b, cd24aad): RESULT PASS

Free disk at start 34 GB, at end 34 GB (/System/Volumes/Data).

## Suite (control-plane-go/)
- go build ./... -> ok; go vet ./... -> ok
- golangci-lint run --allow-parallel-runners ./... -> "0 issues."
- gosec -quiet -exclude=G204 ./... -> no output, exit 0
- gitleaks git --redact (repo root) -> "408 commits scanned ... no leaks found"
- go test -race -count=3 -p 2 ./cmd/... ./internal/store/ ./internal/secrets/ -> ok migrate 7.2s, worker 2.4s, store 7.0s, secrets 1.4s
- go test -race -count=1 -p 2 ./... -> 17 ok; only FAIL internal/worker/outbox: testcontainers "wait for reaper ... context deadline exceeded" (infra flake). Re-run alone: `ok infrapilot/internal/worker/outbox 8.565s`.
- go test -race -run Env ./internal/api/ -> 4 PASS (TestServiceEnvIsEncryptedAndMasked etc.)

## Real end-to-end (built binary, throwaway postgres:16 container vfy13-pg, removed afterwards)
Seed: 1512 EnvVar rows: ascii, empty, unicode, multi-line (CRLF/tab), 1 MiB (1048575 chars), SQL-injection-looking, valid enc:v1 (same key), enc:v1 garbage (not base64), enc:v1 of another key, plain value that starts with enc:v1:, 2 soft-deleted services, 1500 bulk rows. Key via env var only.
- `migrate up` exit 0, 4 migrations applied.
- `encrypt-env --dry-run` exit 0: `dry run (nothing written): scanned=1512 would_encrypt=1508 already_encrypted=1 suspect=3 conflicts=0 errors=0` + warning listing services [svc3 svc4 svc5]. md5 checksum of whole EnvVar table identical before/after.
- `encrypt-env` exit 0: `scanned=1512 encrypted=1508 already_encrypted=1 suspect=3 conflicts=0 errors=0`. Independent decrypt (copy of secrets.go in a scratch module) of all rows: 1508 match the seeded plaintext sha256 (incl. empty, unicode, multi-line, 1 MiB, soft-deleted); the valid pre-encrypted row unchanged and decrypts to its plaintext; the 3 suspect rows byte-identical to seed (hash match). Empty value became a 47-char `enc:v1:` value (7 + 40 base64 of 12-byte nonce + 16-byte tag).
- Second run: `encrypted=0 already_encrypted=1509 suspect=3`, table checksum unchanged (no-op). Dry run afterwards: would_encrypt=0.
- Key failures, DB container STOPPED (proves no connection attempted), all exit 1:
  - no key / empty key: `migrate: config: ENV_ENCRYPTION_KEY is required (generate with: openssl rand -base64 32)`
  - 16-byte key: `migrate: config: ENV_ENCRYPTION_KEY must decode to 32 bytes, got 16`
  - `not*base64!!`: `migrate: config: ENV_ENCRYPTION_KEY must be base64`
  - `--bogus` / extra arg: usage errors, exit 1.
- Valid key, DB unreachable (stopped; and port 1), exit 1: `migrate: ping postgres: failed to connect to \`user=postgres database=vfy\`: 127.0.0.1:1 (127.0.0.1): dial error: dial tcp 127.0.0.1:1: connect: connection refused`. No DATABASE_URL: `config: missing required env: DATABASE_URL`. No password in any message.
- Leak grep over combined stdout+stderr of every run (plaintext markers PLAINSECRET/UPDSECRET/hunter2, the key regex, the DB password): 0 matches.

## Concurrency
+20000 plain rows; a second psql session streamed 40000 autocommit UPDATEs (random rows, 17246 distinct rows, last write tracked in a table) while encrypt-env ran.
- Run 1 (during updates) exit 0: `scanned=21512 encrypted=19824 already_encrypted=1509 suspect=3 conflicts=176 errors=0` + note "176 value(s) changed while the command ran and were left alone; run it again".
- Run 2: encrypted=11273 (rows updated after run 1 passed them); run 3: encrypted=0.
- Verification by decrypting: all 17246 updated rows decrypt to exactly the last value the updater wrote (0 bad); all 2754 never-updated rows decrypt to original (0 bad). No lost update.

## Format vs API
No API server binary exists yet (no cmd/api). internal/api/services.go calls the same secrets.Box.Encrypt used by encrypt-env, and writes through UpsertEnvVar; TestServiceEnvIsEncryptedAndMasked passes. Values written by the migration decrypt with an independent copy of Box (prefix `enc:v1:`, base64(nonce12 || ct || tag16)), and a Box-made ciphertext (seeded valid row) is recognised as already_encrypted: both directions confirmed.

## Observations (not failures)
- Suspect rows (looks like enc:v1 but not decryptable) are left untouched and only warned about; exit 0 by design.
- The API rejects values containing CR/LF, but the migration encrypts multi-line Node-era values fine; they decrypt correctly.

Failures: none.

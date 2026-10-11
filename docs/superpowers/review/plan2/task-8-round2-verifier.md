# Plan 2 Task 8 round 2 verifier

RESULT: PASS (HEAD 77f8daf on ce69f7f; tree clean apart from the known untracked files)

## Static checks (control-plane-go/, run alone, before tests)
- `go build ./...` -> BUILD_OK; `go vet ./...` -> VET_OK
- `golangci-lint run --allow-parallel-runners` -> `0 issues.` (exit 0)
- `gosec -quiet -exclude=G204 ./...` -> no output, exit 0
- `gitleaks git --redact` (repo root) -> `356 commits scanned ... no leaks found`

## Tests (-p 2, background)
- `go test -race -count=3 -p 2 ./internal/api/ ./internal/store/ ./internal/runtime/` ->
  ok internal/api 228.804s / ok internal/store 17.292s / ok internal/runtime 218.780s
- `go test -race -count=1 -p 2 ./...` -> every package ok, final `EXIT=0`
  (cmd/migrate, cmd/worker, api 81.6s, auth, config, db, gitsrc, logging, logs, metrics, runtime 76.2s, secrets, store, testutil, tickets, worker/build, worker/outbox, workflows).

## Real end-to-end (git archive HEAD copy outside the repo, testcontainers Postgres 16, httptest.NewServer on real TCP, users via store, real signed tokens never printed)
```
E2E redis POST REDIS_PASSWORD -> 400 {"error":"reserved_env_var"} [OK]
E2E redis DELETE REDIS_PASSWORD -> 400 {"error":"reserved_env_var"} [OK]
E2E redis POST OTHER -> 200 / DELETE OTHER -> 200 {"deleted":true} [OK]
E2E redis stored generated secret unchanged: true
E2E mongodb POST MONGO_INITDB_ROOT_PASSWORD -> 400 {"error":"reserved_env_var"} [OK]
E2E mongodb DELETE MONGO_INITDB_ROOT_PASSWORD -> 400 {"error":"reserved_env_var"} [OK]
E2E mongodb POST OTHER -> 200 / DELETE OTHER -> 200 [OK]; stored generated secret unchanged: true
E2E nodejs POST/DELETE REDIS_PASSWORD -> 200 / 200 ; MONGO_INITDB_ROOT_PASSWORD -> 200 / 200 [OK]
E2E reveal -> 200 (body has plaintext: true)
SERVER LOG after reveal:
level=INFO msg="env revealed" serviceId=... userId=... requestId=efae2b63b4b5327e
level=INFO msg=request method=GET path=/services/.../env status=200 ms=0 requestId=efae2b63b4b5327e
contains key name: false; contains value: false
E2E POST masked placeholder -> 400 {"error":"invalid_env_var"} [OK]
E2E after placeholder, reveal -> 200, still original: true
```
Note: my throwaway test reported "audit log wrong" only because it expected exactly one log line in total; the second line is the normal request-access-log middleware line. There is exactly one `env revealed` line, ids only; key name and value are absent from the whole log. Not a product defect.

## Worker regression: key rule old (ce69f7f validateEnv) vs new (HEAD ValidEnvKey and validateEnv)
Throwaway test run against a `git archive ce69f7f` copy (real old validateEnv) and the HEAD copy (ValidEnvKey and validateEnv), 41 keys: FOO, foo_bar, A, _X, 9LIVES, a-b, a.b, "a b", " a", "a ", A=B, =A, A\x00B, \x00, empty, -a, .a, newline/CR/tab, #, unicode (é, 日本, ÀB, zero-width space), 255/256/257 ASCII bytes, 128 and 129 x "é" (256/258 bytes), REDIS_PASSWORD, MONGO_INITDB_ROOT_PASSWORD.
Result both runs: `KEYCMP total=41 diffs=0` (old regexp/len rule == old validateEnv == ValidEnvKey == new validateEnv for every key). No behaviour difference. Worker/runtime/workflows tests pass in the full run.

## Dev environment (read-only)
- `go run ./cmd/migrate status`: 00001-00003 applied; `00004 00004_refresh_family.sql pending` (not run).
- ~/cp-logs/go-worker.log: last write 02:26, only "flushed logs processed=0 errors=0" lines, 0 error-level lines.
- `curl localhost:8080/healthz` -> `{"ok":true} 200` (Node API).

## Cleanup
Throwaway tests lived only in the scratchpad archive copies; no repo code edited; testcontainers removed by ryuk.

Failures: none.

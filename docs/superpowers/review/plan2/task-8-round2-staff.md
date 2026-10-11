# Plan 2 Task 8, fix round 1 - Staff re-review

Range: 6d83d2f, f91a9b8, 1621a0d, f6c9e78, a1798a0, 77f8daf (vs ce69f7f), `control-plane-go/`. No repo code edited.
Checks (from a `git archive` copy outside the repo): `go build ./...`, `go vet` on api/runtime/store/workflows clean; `golangci-lint run` on those packages: 0 issues.
Tests: new tests `-count=5` pass (4.4 s); full `./internal/api/` under `-race -count=3 -p 2` passes (228 s); no flake seen.

## Earlier findings

- STAFF-1 (reserved keys): FIXED, correct. `store.ReservedEnvKey(type, key)` is checked after the ownership lookup on both set and delete, returns 400 `reserved_env_var`. Same names stay ordinary on nodejs services (test covers set and delete). The delete check uses `r.PathValue("key")` (decoded), before the lenient legacy path, so it cannot be bypassed by encoding. Other keys on a managed service stay writable. Side effect to know: a user can no longer pre-set their own Redis/Mongo password before the first deploy (the worker generates it when absent). That is the intended trade, but say so in the release note.
- STAFF-2 (one validator): FIXED, minimal. `runtime.ValidEnvKey` is used by `validateEnv` and by the API; the api copy of regex and cap is gone. The `maxEnvValue` cap stays api-only, which is fine (worker has no value cap).
- STAFF-3 (dead utf8 check): FIXED. Clause removed, comment corrected; `TestEnvValueInvalidUTF8IsReplacedByDecoder` pins the real behavior (stored value `a<U+FFFD>b`). The `utf8` import stays for the live path-key check.
- STAFF-4 (transaction mutants): FIXED, all killed (see below).
- Constants: `store.RedisPasswordEnv` / `MongoPasswordEnv` and `workflows.Managed` referencing them: correct and no behavior change; the `#nosec G101` is on the right line and lint is clean. The test file still uses the literal strings, which is good (a rename of the constant cannot silently pass).
- SEC-2 (reveal audit line) and PROD-1 (masked placeholder): implemented; the log carries serviceId, userId, requestId, no key or value; only the exact `********` is refused.

## Mutation results (throwaway copy, `go test -p 2 -count=1` over api, runtime, store, workflows)

All KILLED:
- reserved check removed on delete only (TestManagedPasswordKeysAreReserved); removed on set only; ignores mongodb; ignores redis; applies to every service type
- masked-value check removed (TestMaskedPlaceholderIsNotAcceptedAsValue)
- reveal log: includes a key; removed; logged always (also on masked reads); userId dropped (TestRevealIsLoggedWithoutSecrets)
- key cap +1, -1, removed (TestEnvKeyCharset in runtime, TestServiceEnvIsEncryptedAndMasked in api); value cap +1
- delete transaction dropped (TestDeleteServiceIsAtomicWithItsOutboxEvent); deploy transaction dropped (TestDeployServiceIsAtomicWithItsOutboxEvent)
- deploy `ErrNotFound` race branch -> 500 (TestDeployRacingADeleteIs404); delete `ErrNotFound` branch -> 500 (TestDeleteRacingADeleteIs404 and TestConcurrentDeletesQueueOneTeardown)

SURVIVORS: none. (One mutant, "api swaps in its own weaker validator", failed to compile on an unused import; not counted. The cap mutants cover the same ground.)

## Test mechanics

- Trigger approach (`failOutboxInserts`): robust enough. The api package uses ONE shared Postgres (one container, tables truncated per test by `NeedDB`), not a database per test, so "isolated" holds only because the package forbids `t.Parallel` (it has none) and runs serially. The trigger and function are dropped in `t.Cleanup`, so later tests see a clean table; TRUNCATE does not fire INSERT triggers. Weak spot: the cleanup ignores errors, so a failed DROP would make every later outbox insert in the package fail with confusing 500s. Low.
- Tracer race test: deterministic. The `sync.Once` fires on the first `SELECT` completed by the traced pool, which is the ownership lookup (the JWT middleware does not touch the DB, and `hs.st`/fixtures use the untraced pool). The mutation "deploy ErrNotFound -> 500" is killed, which proves the race window is really hit, so the test is not passing vacuously via an earlier 404. Fragility: it keys on the command tag containing "SELECT"; if a future middleware adds a DB read before the handler, the test would pass vacuously on the ownership 404 without failing (the body check `service_not_found` is identical). A cheap guard would be asserting the fn ran with a flag, or asserting the tracer saw the owner query. Low.
- `-race -count=3` and `-count=5` produced no failures or races.

## New findings

[STAFF-7] severity: low
where: internal/api/services.go:10 (import `infrapilot/internal/runtime`)
problem: The API now links the package that shells out to docker, for a 1-line predicate. My STAFF-2 proposal was a leaf package (`store` next to `ValidServiceType`); the fix picked `runtime` instead. No cycle (`go list -deps`: runtime imports only `logs`, which api already imported) and no new third-party weight, so this is cosmetic, but it blurs "only runtime shells out, api stays pure" and nothing (no depguard rule in .golangci.yml) stops api from calling a runtime exec function later.
evidence: `go list -deps ./internal/api` now includes infrapilot/internal/runtime; before the round it did not.
fix: Optional. Move `ValidEnvKey` plus its regex and cap into `store` (it already hosts `ValidServiceType`, `ReservedEnvKey`) and have `runtime.validateEnv` call it; or accept and add a depguard rule limiting api to the one symbol. Defer to the plan-end gate if wanted.

[STAFF-8] severity: low
where: docs/superpowers/review/backlog.md:109
problem: The open P1 T23 line ("env API should refuse to delete or overwrite REDIS_PASSWORD / MONGO_INITDB_ROOT_PASSWORD") is now implemented but still reads as open.
fix: Mark it done (6d83d2f) when the CTO next edits the backlog. Not edited by me.

[STAFF-9] severity: low
where: internal/api/services_test.go `failOutboxInserts` cleanup
problem: Cleanup errors from DROP TRIGGER / DROP FUNCTION are discarded; see Test mechanics.
fix: `t.Errorf` on a cleanup failure so a leaked trigger fails loudly at its source.

RESULT: APPROVE (critical 0, high 0, medium 0, low 3). Every earlier finding is really fixed and no mutant survives.

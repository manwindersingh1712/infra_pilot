# Plan 2 Task 6, fix round 1: Staff review

Range: d531344..55c66b1 (bab3c79, 5c680c3, 55c66b1), `control-plane-go/`. Reviewed against "2026-10-11 Plan 2 Task 6 rulings" in `decisions.md` and spec §5.

Verification run: `go build ./...`, `go vet ./...`, `golangci-lint run` (0 issues), `go test ./internal/... -count=1` (all packages ok, real Postgres). Mutations were run in a throwaway copy under the session scratchpad; nothing in the repo was edited.

## Conformance to the ruling

- State table in `RotateRefreshToken` matches the ruling line by line: unknown -> ErrNotFound; lock, then a fresh `FOR UPDATE` re-read; live -> rotate + child insert in the same family; rotated within grace -> not found, no revoke; rotated beyond grace -> family revoke committed, `ErrTokenReused`; expired/unrevoked, logout-revoked (`rotatedAt` NULL) and earlier family revoke -> ErrNotFound with no revoke. Any failure rolls back (old token stays usable).
- `RevokeRefreshFamily` looks up the family of any presented token (rotated or revoked), takes the same advisory lock, revokes live family members; unknown token returns nil. Logout answers `{"ok":true}`.
- The revoke-must-persist trick (return nil from the tx func, translate to `ErrTokenReused` afterwards) is correct and commented.
- Grace is a constant passed as integer seconds; boundary is `<=` as ruled. `now()` is the transaction start, so a loser that queued behind the winner sees a negative age and is correctly treated as inside grace.
- Handler: new token generated first, then rotate, then sign; log lines match the ruling (Info for grace with requestId only, Warn for reuse); same 401 body for all cases.
- Limiters: `limiterStrict` (6 s, burst 5) for register/login, refresh limiter (1 s, burst 20) for refresh and logout; `newIPLimiter(burst, every)`; `RefreshBurst` 0 means 20. Comment fix done.
- Validation: email cap 254 bytes before `ParseAddress`, on register and login; register order email, short, long, name; login has no password check; name uses `utf8.RuneCountInString` and null/absent stays valid.
- Spec §5 edits match the ruling text. Backlog lines added. Node-era rows: backfill gives `familyId = id`, `rotatedAt` NULL, so old revoked rows behave as logout-style and never trigger a revoke.
- Timezone: pool sets `timezone=UTC` (`internal/db/db.go:63`), so `now() - "rotatedAt"` on a `TIMESTAMP(3)` column is consistent.

## `store.ErrReplayInGrace` (deviation): sound

It wraps `ErrNotFound` with `%w`, so `errors.Is(err, ErrNotFound)` still holds for any caller that does not care, and the handler checks the more specific sentinel first. Returning it from inside `BeginFunc` rolls back an empty tx, which is harmless. It is the smallest way to get the ruled Info log without a second return value. The ordering dependency in the handler `switch` (grace case before NotFound) is load-bearing; a one-line comment would protect it (see STAFF-3).

## Mutation results

Killed (test names are the first failing ones):

| Mutation | Result |
|---|---|
| logout on strict bucket | killed (BucketsAreIndependent) |
| refresh on strict bucket | killed (BucketsAreIndependent, DefaultBursts) |
| logout with no limiter | killed (AuthRoutesAreRateLimited, BucketsAreIndependent) |
| login on refresh bucket | killed |
| default strict 6; default refresh 19 / 21 | killed (DefaultBursts) |
| refresh burst ignored (uses strict) | killed |
| `refreshEvery` 2 s; `strictEvery` 5 s (constants) | killed (RefillInterval, RefillsAfterSixSeconds) |
| password short 7 / 9, long 73 / 71 | killed (PasswordLengthLimits) |
| email cap 255 / 253 | killed (EmailLengthCap) |
| name by bytes (`len`), cap 101 / 99 | killed (NameLengthCap) |
| email checked after password | killed |
| password check added to login | killed (LoginHasNoPasswordLengthCheck) |
| grace = 0 | killed in store (Basics, Concurrent, ReuseAfterGrace), api (ReplayInsideGrace, ConcurrentRequests, ReuseLogs) and the auth constant test |
| advisory lock removed (`lockFamily` no-op) | killed (ReplayRacesChildRotation) |
| revoke whole user instead of family (rotate path and logout path) | killed (ReuseAfterGrace, RevokeRefreshFamily) |
| reuse revoke not committed | killed (ReuseAfterGrace, ReplayRacesChildRotation) |
| replay inside grace revokes | killed |
| expired token treated as reuse | killed |
| child inserted in wrong family | killed |
| `rotatedAt` not set on rotation | killed |
| logout unknown token returns error | killed |

Survived:

- Swapping the refill intervals at the wiring site in `NewHandler` (`newIPLimiter(strict, refreshEvery)` or `newIPLimiter(refresh, strictEvery)`): see STAFF-1.
- Dropping `FOR UPDATE` from the re-read: survives because the advisory lock already serialises; the row lock is redundant belt and braces. Not a defect.
- "Drop the transaction" (ruling mutation h): I could not construct a faithful mutation of `pgx.BeginFunc` by regex. Test (b) `TestRotateRefreshTokenIsAtomic` reads correctly (a duplicate `newHash` forces the child INSERT to fail, then asserts the old token is live and rotates), but I did not see it fail under a mutation.

## Harness defaults

`RefreshBurst: 1000` and `AuthBurst: 1000` in `newHarnessWith` do not hide production behaviour: the production defaults (0 -> 5 / 20) are asserted through the full `NewHandler` by `TestRateLimitDefaultBursts`, bucket separation and route assignment by `TestRateLimitBucketsAreIndependent` and `TestAuthRoutesAreRateLimited` (all with explicit small bursts). The only gap is the refill interval at the wiring site (STAFF-1). `cmd/api` does not exist yet (Task 14), so nothing else sets `Deps` today.

## Findings

[STAFF-1] severity: low
where: control-plane-go/internal/api/server.go (NewHandler), control-plane-go/internal/api/middleware_test.go (TestRateLimitRefillInterval)
problem: Nothing proves that the strict limiter refills every 6 s and the refresh limiter every 1 s once built by `NewHandler`. The refill tests construct `newIPLimiter` directly with the constants, so wiring them the wrong way round is not caught.
evidence: Mutations `newIPLimiter(strict, refreshEvery)` and `newIPLimiter(refresh, strictEvery)` both left `go test ./internal/api` green. Effect in production would be login at 60/min or refresh at 10/min, the exact numbers the ruling fixed.
fix: Cheap test: build the handler, grab `s.authLimit.every` and `s.refreshLimit.every` (NewHandler returns an `http.Handler`, so either expose via an `export_test.go` helper or construct `Server` via a small `newServer(Deps) *Server` that `NewHandler` calls), and assert 6 s / 1 s. Alternatively inject a clock and check refill through HTTP.

[STAFF-2] severity: low
where: control-plane-go/internal/db/migrate_integration_test.go (TestMigration00004RefreshFamily)
problem: The test checks backfill, NOT NULL and the index after up, then checks the columns are gone after down, but it does not assert that the index is gone after down, and it does not insert a row after the down/up cycle.
evidence: A down that left the index behind would only show up as the second up failing on a duplicate `CREATE INDEX`, which does catch it indirectly, so this is a nit. Idempotent re-up is exercised. Migration itself is fine: goose Up/Down annotations present, one statement per concern, down drops index then both columns, backfill before `SET NOT NULL`, matches the ruling text exactly.
fix: Optional: assert `pg_indexes` count is 0 after down.

[STAFF-3] severity: low
where: control-plane-go/internal/api/auth.go (refresh handler switch)
problem: The case order is load-bearing and not marked: `ErrReplayInGrace` must come before `ErrNotFound` because it wraps it. Reordering silently loses the Info log. Also the Warn line for reuse carries only `requestId`, so an operator cannot tell which session was cut.
evidence: `TestRefreshReuseLogsWithoutTheToken` checks the grace log is emitted, so reordering would be caught for the grace log; the absence of a user or family id in the Warn is by ruling ("existing Warn line"), so this is an operability nit only.
fix: One-line comment on the case order. Optionally return the family id or user id from the store for the Warn line in a later round (never the token).

## Summary

No critical, high or medium findings. The implementation matches the ruling's state table, the deviation is sound, the migration follows the ruling and goose conventions, and 30 of 32 mutations in scope were killed (the survivors are the wiring-interval swap, a real but low gap, and the redundant row lock). The faithful "drop the transaction" mutation could not be built mechanically.

RESULT: APPROVE (0 critical, 0 high, 0 medium, 3 low)

## Orchestrator follow-up
STAFF-1 fixed by the orchestrator: `newServer(d)` extracted from `NewHandler`, and `TestServerLimiterRefillIntervals` asserts 6s/1s intervals and 5/20 default bursts. Mutation (strict limiter built with `refreshEvery`) fails the test. golangci-lint: 0 issues. STAFF-2 and STAFF-3 went to backlog.md.

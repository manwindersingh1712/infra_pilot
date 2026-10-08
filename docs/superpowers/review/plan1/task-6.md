# Plan 1, Task 6: Store core, models and users

## Implementation
- Engineer commit `a355fe1` feat(go): store core, JSON models and user queries (`internal/store/{store.go,models.go,users.go,models_test.go,main_test.go,users_test.go}`, go.mod/go.sum + `github.com/lucsky/cuid` v1.2.1).
- First run stopped on a usage-limit error before any change; resumed and restarted cleanly from e105cfb.
- Deviations: `//nolint:unused` on `collect` (first caller in Task 7; plan Task 7 now says to remove it); `go mod tidy` made cuid direct.

## Reviews (commit a355fe1)
- **Security: FINDINGS (0/0/0/1).** All SQL bound; `User.Password` is `json:"-"`; `UpsertDevUser` matches only `dev@local` and never overwrites a password. Low SEC-1 (T6): cuid v1 IDs are guessable (math/rand, same as Prisma) → backlog. Plan 2 notes (mask EnvVar values in lists, JSON no-password test, reject registering `dev@local`, malformed-hash = failed login) → backlog.
- **Staff: APPROVE.** Plan-verbatim; Timestamp JSON = `toISOString()` format; nullable → `null`; empty lists → `[]`; JSON field names match Prisma/Node for every model; Plan 2 call sites match. Lows STAFF-1 (T6) (no UTC/no-password JSON test), STAFF-2 (T6) (Timestamp MarshalText format) → backlog. updatedAt on User writes not needed (column has a default; matches Prisma `update: {}`).
- **Verifier: PASS.** CI job green; caller package: create/lookup/duplicate→ErrConflict/dev upsert idempotent/InTx rollback; User JSON has no password. Read-only against the dev DB: Go JSON for `dev@local` and its projects is byte-identical to Node `GET /projects` (field names, order, millisecond `Z` timestamps). Dev DB unchanged. Note: no `GetUserByID` yet (not in the plan; later tasks add it when needed).

## Result
**Done 2026-10-08.** Verifier PASS, no open critical/high/medium. Next: Task 7.

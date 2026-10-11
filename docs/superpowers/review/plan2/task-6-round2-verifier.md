# Plan 2 Task 6, fix round 1: Verifier (commits bab3c79, 5c680c3, 55c66b1 on d531344)

RESULT: PASS

## Suite (control-plane-go/, tree clean apart from the known untracked files)
- `go build ./...` ok; `go vet ./...` ok
- `golangci-lint run --allow-parallel-runners` (run alone) -> `0 issues.`
- `gosec -quiet -exclude=G204 ./...` -> no output, exit 0
- `gitleaks git --redact` (repo root) -> `335 commits scanned.` / `no leaks found`
- `go test -race -count=3 ./internal/api/ ./internal/auth/ ./internal/store/ ./internal/db/` -> ok api 218.8s, ok auth 19.1s, ok store 13.3s, ok db 30.2s, exit 0
- `go test -race -count=1 ./...` -> every package ok (api 92.2s, runtime 95.1s, db 28.8s, ...), exit 0, no reruns needed

## Feature (git archive HEAD copy outside the repo, testcontainers Postgres 16, real TCP httptest server, tokens redacted; copy deleted)
Flow (all as expected):
- register 200; login 200 (family A); second login 200 (family B)
- two concurrent `/auth/refresh` with the same token -> `200 and 401` (exactly one each); the winner's new token refreshed 200
- replay of the old token inside 10 s -> `401 {"error":"invalid_refresh_token"}`; revoked/total rows 2/5 before and after (nothing revoked); the newest token still refreshed 200
- after backdating rotatedAt by 1 minute through the pool, replay of the old token -> 401; the family's newest token then -> 401 (family revoked); stolen family has 0 live rows
- family B token (second login) afterwards -> 200 (untouched); logout -> `200 {"ok":true}`; refresh after logout -> 401
- note: register also creates a refresh family, so 2 live rows remained after theft detection (register family + family B), as expected

Validation limits on /auth/register:
- password 7 bytes -> 400 `password_too_short`
- password 73 bytes -> 400 `password_too_long`; 19 emoji (76 bytes) -> 400 `password_too_long`; 72 bytes -> 200
- email 254 bytes -> 200; 255 bytes -> 400 `invalid_request`
- name 100 chars (multi-byte) -> 200; 101 chars -> 400 `invalid_request`

Burst limits (default Deps): register + 4 logins -> 200 x4, 5th and 6th login -> 429 (bucket of 5 shared with register); `/auth/refresh` right after -> 401 (answered, not 429), so the buckets are separate.
(With the register counted, the "6th login" is the 5th request in the bucket; the 6th login request returned 429 as required.)

Migration 00004 on a database holding RefreshToken rows (goose provider, 3 rows seeded at version 3):
- DOWN: columns createdAt,expiresAt,id,revokedAt,tokenHash,userId
- UP: rows=3, familyId!=id: 0, nulls: 0, columns gain familyId,rotatedAt
- DOWN again: rows=3; UP again: rows=3, familyId!=id: 0

## Dev database and running services (read-only)
- `go run ./cmd/migrate status` -> `00004 00004_refresh_family.sql pending` (00001-00003 applied); `migrate up` NOT run
- ~/cp-logs/go-worker.log: still `flushed logs processed=0 errors=0` every 5 min, worker pid up 17h+, no errors
- Node API `localhost:8080/healthz` -> `{"ok":true}`; node-api pid still running

## Failures
None. Caveat: the e2e harness is my own throwaway test (first run had a wrong expectation of 1 live row; corrected to 2 because registration creates a family; no product bug).

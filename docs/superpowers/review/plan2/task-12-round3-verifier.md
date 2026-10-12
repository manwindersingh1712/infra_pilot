# Plan 2 Task 12, round 3: Verifier findings

RESULT: PASS (commits 6359a87, 5aa8a7f, 91275b2, 2703492, 9fee26e on top of d7fef14; HEAD fdb0576, branch go-migration)

Disk: 38 GiB free at start (>= 10 GB), 34 GiB free at the end.

## Suite (control-plane-go/)
- `go build ./...` OK; `go vet ./...` OK.
- `golangci-lint run --allow-parallel-runners` -> `0 issues.`
- `gosec -quiet -exclude=G204 ./...` -> no findings, exit 0.
- `gitleaks git --redact` (repo root) -> `405 commits scanned ... no leaks found`.
- `go test -race -count=3 -p 2 ./internal/runtime/ ./internal/workflows/ ./internal/store/ ./internal/api/ ./internal/worker/... ./cmd/...` -> all `ok` (runtime 210.9s, workflows 13.5s, store 8.3s, api 216.5s, worker/build 3.8s, worker/outbox 8.7s, cmd/migrate 1.4s, cmd/worker 2.1s).
- `go test -race -count=1 -p 2 ./...` -> 18 packages `ok`, no FAIL lines (no Ryuk flake this time).

## (1) SEC-7 CancelServiceDeploys refuses a live service
- Go test `TestCancelServiceDeploysRefusesLiveService` passes (part of the runs above): live service with an in-flight deploy -> non-retryable `NotDeleted`, `ft.cancelled` empty; unknown well-formed id -> no-op success.
- Real Temporal: `E2E_PROJECT=vfy12c E2E_TEMPORAL_PORT=38417 E2E_GO_TEST_ARGS="-run TestE2ETeardown" scripts/e2e-temporal.sh` -> `TestE2ETeardownRealDocker PASS (55s)`, RestartsAfterFailureOnly PASS, RunningStartIsSwallowed PASS, `TestE2ETeardownRunbookRestart PASS`, `e2e: PASS`.
- The shipped e2e creates the "live" service WITHOUT an in-flight deploy (`mk(store.TypeNodejs, false)`), so it did not by itself prove "cancels nothing". I made a scratch copy (outside the repo, deleted afterwards), changed live to `mk(..., true)` (running deploy workflow) and asserted the deploy workflow is still `Running` after the forged teardown:
  - with the fix: Temporal log `ActivityType CancelServiceDeploys ... Error service is not deleted (type: NotDeleted, retryable: false)`; `VFY live deploy workflow still Running after forged teardown`; PASS.
  - mutation (guard removed from `cancelServiceDeploys` in the copy): `VFY live in-flight deploy workflow not Running after forged teardown (running=0)`; FAIL. So the behavior is real and the check is sensitive.
- Suggestion (non-blocking): make the shipped e2e live service in-flight and assert its deploy workflow stays Running.

## (2) SEC-8 runbook SQL
Extracted every ```sql block of docs/superpowers/runbook/teardown-stuck.md (inspect-service, dead-outbox, restart-teardown, scrub-rows, orphan-volumes, purge-password) and ran them by hand with `psql -v sid=...` against a throwaway Postgres 16 (`vfy12c-pg`, goose-migrated; removed afterwards), with a live redis service and a soft-deleted redis service (each with env rows incl. REDIS_PASSWORD, connections, repoUrl, a teardown outbox row).
- LIVE: inspect-service shows `deletedAt` empty; restart-teardown `INSERT 0 0`; scrub-rows `DELETE 0 / DELETE 0 / UPDATE 0`; purge-password `DELETE 0`; dead-outbox 0 rows. Before/after snapshots of EnvVar, ServiceConnection, Service.repoUrl and OutboxEvent were identical.
- SOFT-DELETED: inspect shows a timestamp; restart-teardown `INSERT 0 1` (new pending row copied); scrub-rows `DELETE 1 / DELETE 1 / UPDATE 1` (FOO and the connection removed, REDIS_PASSWORD kept, repoUrl NULL); orphan-volumes lists it; purge-password `DELETE 1`; orphan-volumes then empty. The live service and the peer were untouched.
- Note: orphan-volumes and dead-outbox are read-only global lists, they are not scoped to :sid (they listed the deleted service while sid was the live one). That is by design.
- Mutation: in a scratch copy of the runbook I changed `"deletedAt"` to `"deletedAtt"` in the purge-password block; `go test -run TestRunbook ./internal/worker/outbox` -> `FAIL TestRunbookEverySQLBlockRunsAndSparesLiveServices: block "purge-password" failed on real Postgres: ERROR: column "deletedAtt" does not exist (SQLSTATE 42703)`. With the real runbook copied back: `ok`.

## (3) SEC-9 runbook shell snippets
The validation one-liner `[[ "$SID" =~ ^[a-z0-9]{20,32}$ ]]` run in bash and zsh (echo only; nothing destructive run):
- `cm1234567890abcdefghij` ACCEPT
- empty -> REJECT; `cm12` -> REJECT; `CM1234567890ABCDEFGHIJ` -> REJECT
- `abcdefghij$(rm -rf x)abcdefghij` -> REJECT; `abcdefghijklmnopqrstu; echo pwned` -> REJECT
- valid id plus trailing newline -> REJECT (both shells)
The snippets set SID once, validate it, use `"$SID"` quoted everywhere, list before removing, guard `rm -f $IDS` with `[ -n "$IDS" ]`, and `docker volume rm` has no `-f`. The step 4 and step 5 volume snippets repeat the check. No unquoted or unvalidated use found. (A pre-existing /tmp/x file dated 02:57 is not mine; nothing was executed.)

## (4) SEC-10 UpsertRoute vs RemoveRoute
`checkRoute` (used by UpsertRoute) and `RemoveRoute` both call `ValidServiceID` (`^[a-z0-9]{20,32}$`, internal/runtime/ids.go); the local `serviceIDRE` was deleted. `TestUpsertRouteAndRemoveRouteAcceptTheSameIDs` covers 20 and 32 chars (valid), 19 and 33, short, dashes, uppercase, empty, `../etc/passwd`, dot, trailing newline; passes under -race -count=3.

## (5) Docs / release note
- runbook: step 0 mandatory, guards on every destructive statement, SID validation, section 5 now says the "kept volume cannot be purged later by an event" holds only inside the Temporal namespace retention window, then purge by hand.
- release-note-plan2.md has the "Managed databases deleted with purgeData=false" section: volume `cp-vol-<serviceId>` kept with ONE encrypted password row, not re-attachable from the UI, re-creating gives a new id and empty volume, and points to runbook section 5. It does not repeat the retention-window caveat (that lives in the runbook only); acceptable, but the caveat could be added if the release note must be self-contained.

## Docker snapshot (before vs after, read only)
Only differences: my `vfy12c_temporal` and `vfy12c-pg` containers were created and removed by name (none left); `go test` testcontainers left one `reaper_fa14c6...` (Ryuk, Up, self-removes) and one anonymous volume `3fb2ace0...` (testcontainers). No cp_* container, real volume, network, ~/cp-backups, ~/cp-logs or the dev DB was touched; no `migrate up` against the dev DB (only against `vfy12c-pg`). Scratch copies and /tmp/vfy12c-* files removed. No repo code edited.

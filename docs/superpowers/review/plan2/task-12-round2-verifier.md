# Plan 2 Task 12, fix round 1: Verifier report (round 2)

RESULT: PASS

HEAD d7fef14 (dcd8564, f4ae53c, 6a42b4e, 5f1e04c, f073cc5, 3a8d171, e4568fe, d7fef14 on top of 5fb3c08). Tree clean except the known untracked files.

## Static
- `go build ./...` OK; `go build -o <scratch>/worker-bin ./cmd/worker` OK; `go vet ./...` OK
- `golangci-lint run --allow-parallel-runners` -> `0 issues.`
- `gosec -quiet -exclude=G204 ./...` -> no output, rc=0
- `gitleaks git --redact` (repo root) -> `398 commits scanned. no leaks found`

## Tests
- `go test -race -count=3 -p 2 ./internal/runtime/ ./internal/workflows/ ./internal/store/ ./internal/api/ ./cmd/...`
  ok runtime 245.8s, workflows 23.5s, store 14.6s, api 259.3s, cmd/migrate 1.5s, cmd/worker 2.1s
- `go test -race -count=1 -p 2 ./...` -> 18 packages ok, 0 FAIL (outbox 6.3s, workflows 8.9s, runtime 77.6s, api 83.2s; no flake this time)
- cmd/worker/startup.go read: `checkLegacyVolumeRoot` (Lstat; symlink -> error; missing or "" -> ok) is called from main.go:59 before the worker starts; `newActivities` refuses nil Temporal; teardown workflow and activities are registered.

## Repo e2e (throwaway Temporal vfy12b_temporal on 127.0.0.1:37433)
`E2E_PROJECT=vfy12b E2E_TEMPORAL_PORT=37433 E2E_GO_TEST_ARGS='-run TestE2ETeardown' scripts/e2e-temporal.sh` rc=0 (no SKIP lines)
```
--- PASS: TestE2ETeardownRealDocker (55.88s)
--- PASS: TestE2ETeardownRestartsAfterFailureOnly (0.25s)
--- PASS: TestE2ETeardownRunningStartIsSwallowed (0.14s)
--- PASS: TestE2ETeardownRunbookRestart (0.47s)
e2e: PASS   /  e2e: tearing down vfy12b_temporal
```

## Own end-to-end (test in a `git archive HEAD` copy outside the repo, internal/vfy12b; real Postgres testcontainer, real HTTP via httptest.NewServer(api.NewHandler), real Temporal on a throwaway container vfy12bown_temporal:37434, real outbox Publisher + Starter + real Activities/Store; Docker and Router are recording fakes, so no real containers were touched)
`TEMPORAL_E2E_ADDR=127.0.0.1:37434 go test -race -count=1 -v ./internal/vfy12b/` -> both tests PASS, `ok infrapilot/internal/vfy12b 7.210s`

(1) API:
- redis and mongodb DELETE with no param, `?purgeData=`, `?x=1` -> 400 `{"error":"purge_choice_required"}`; service still visible; outbox rows = 0
- `?purgeData=TRUE|True|1|yes|0|true&purgeData=true|false&purgeData=` on redis, mongodb, nodejs -> 400 `{"error":"invalid_purge_data"}`; nothing deleted; no outbox row
- foreign user, every variant above (plus `purgeData=1&purgeData=2`) -> 404 body byte-identical to an unknown id: `{"error":"service_not_found"}`; state unchanged
- redis `purgeData=false` -> 202; mongodb `purgeData=true` -> 202; nodejs no param -> 202. Outbox: `{"purgeData": false, "serviceId": "<redis>"}`, `{"purgeData": true, ...<mongo>}`, `{"purgeData": false, ...<node>}`. A repeat DELETE -> 404, no second row.

(2) Forced failure + runbook SQL: TeardownService wrapper fails once with a non-retryable error for R (redis, purge=false) and M (mongodb, purge=true) -> both workflows Failed; R2 (purge=true) Completed. Runbook SQL taken verbatim from the `sql restart-teardown` block, `:'sid'` replaced as psql would: `INSERT 0 1` for R; the new row is pending with `"purgeData": false`; M's outbox rows unchanged; after a publisher tick R is a new run, Completed; M stayed Failed. Re-running the runbook on the Completed R: row consumed, run id unchanged, rows unchanged. Runbook for M afterwards: new run Completed, purgeData=true preserved (volume `cp-vol-<M>` removed, 0 rows).

(3) Keep semantics: R (purgeData=false): exactly 1 EnvVar row left (`REDIS_PASSWORD`), ciphertext byte-identical to before and `enc:` prefixed; connections 0; no RemoveVolume call. `GET /services/R/env` and `?reveal=true` -> 404 `service_not_found`; POST env -> 404. Before the delete, reveal returned 200. R2 and M (purgeData=true): 0 rows, RemoveVolume `cp-vol-<id>` issued.

(4) Forged payloads -> outbox row `dead`, no workflow: `{"serviceId":"../etc",...}`, empty id, no serviceId, numeric id, unknown key `extra`, JSON array, JSON string, upper-case cuid, 40-char id. purgeData `"true"`, `1`, `"TRUE"`, `null`, `"yes"` on a deleted redis -> workflow Completed, volume NOT removed, 1 row (password) kept. Forged `{"serviceId":<LIVE>,"purgeData":true}` for a live redis (L) and live nodejs (L2) -> workflow Failed (NotDeleted); no docker/route call mentioned either id, services still visible, L's env rows and connection intact, `GET /services/L/env?reveal=true` still 200.

## Docker snapshot
`docker volume ls -q` before/after: identical (diff empty). `docker ps -a` before/after: identical except one extra `reaper_...` (testcontainers Ryuk of another agent's concurrent session, left alone); vfy12b_temporal and vfy12bown_temporal removed by exact name; no `vfy12b`/`e2etd` leftovers. No cp_*/cp-vol-* real resources touched; dev DB and ~/cp-backups, ~/cp-logs not touched.

## Observations (non-blocking, no failures)
- O1: `POST /services/:id/env` with an invalid/empty body returns 400 before the ownership check (my first run of the test hit it). Pre-existing behaviour of setServiceEnv (decode first, owner check second); it lets a caller probe nothing about existence with a valid body (404), only with a bad body (400 for any id). Not part of this round.
- O2: `CancelServiceDeploys` runs before the `NotDeleted` check in TeardownService, so a forged teardown event for a LIVE service with in-flight deployments would still request cancellation of its deploy workflows (nothing is removed or scrubbed). Needs write access to OutboxEvent; I did not exercise it (my live services had no open deployments).
- O3: `OutboxEvent.nextAttemptAt` is TIMESTAMP(3) rounded up, so a row is not claimable for up to 1 ms after insert; irrelevant for a 1 s poller, but a test that ticks immediately after an insert can see 0 claimed (it hit my scratch test once; fixed in the scratch test with a 30 ms wait).

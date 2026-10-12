# Plan 2 Task 12 - Verifier report (commits 28211dd, 66457af, 473a502, 5fb3c08)

RESULT: PASS

## Suite (cd control-plane-go, branch go-migration, HEAD 5fb3c08)
- `go build ./...` -> ok; `go build -o /dev/null ./cmd/worker` -> ok
- `go vet ./...` -> clean
- `golangci-lint run --allow-parallel-runners` (alone) -> `0 issues.`
- `gosec -quiet -exclude=G204 ./...` -> no output, exit 0
- `gitleaks git --redact` (repo root) -> `389 commits scanned ... no leaks found`
- `go test -race -count=3 -p 2 ./internal/runtime/ ./internal/workflows/ ./internal/store/` ->
  ok runtime 213.989s / ok workflows 16.580s / ok store 11.863s
- `go test -race -count=1 -p 2 ./...` -> all ok except internal/worker/outbox: the known Ryuk flake
  (`wait for reaper ... context deadline exceeded`); re-run alone
  `go test -race -count=1 ./internal/worker/outbox/` -> `ok 5.669s`. internal/workflows ok 7.659s, cmd/worker ok.

## Real end-to-end (git archive HEAD copy in /private/tmp, since deleted)
Throwaway: Temporal dev server container `vfy12-temporal` (127.0.0.1:37299), `vfy12-nginx`, testcontainers Postgres,
busybox containers `cp-<deploymentId>` / `vfy12-orphan-<svcId>` labelled cp.serviceId=<id>, volumes `cp-vol-<svcId>`,
temp routes dir and temp legacy dir. Test file (not in repo): scratchpad vfy12_test.go.txt.
Command: `TEMPORAL_E2E_ADDR=127.0.0.1:37299 go test -race -count=1 -run TestVfy12Teardown -v ./internal/workflows/` -> `--- PASS` / `ok`.
Real Postgres store + real Docker runner + real nginx runner (reload against vfy12-nginx) + real Temporal; events entered via the real
`workflows.Starter.Start(teardown.requested)`. The in-flight DeployWorkflow is the real workflow with the e2e fake activities (blocked in RunContainer).

Observed (quoted from the log):
- A (purgeData=false): `A containers after: []`; route file gone; `A volume KEPT (purgeData=false): ok`; `A legacy dir KEPT: ok`
- B (purgeData=true): containers gone, route gone; `B volume REMOVED (purgeData=true): ok`; `B legacy dir REMOVED: ok`
- Rows: `envvars=0 connections=0 repoUrl=<nil> deletedAt!=nil:true openDeployments=0 failedDeployments=2` (A: queued+building -> failed)
- Ordering: `ORDERING ok: deploy workflow cleanup (failed:<dep>) < cancel activity end < teardown activity start`
  trace: `claim-deploy run ACT cancel-start failed:<dep> remove:cp-<dep> ACT cancel-end ACT teardown-start ...`
- Second run (new workflow id, purge false and true): success; starter duplicate teardown event -> nil (REJECT_DUPLICATE); the purge re-run then removed A's kept volume.
- LIVE service: `LIVE service refused, non-retryable: type=NotDeleted msg=service is not deleted (type: NotDeleted, retryable: false)`
- Forged Starter payloads all rejected with ErrOutboxPermanent: `../etc`, purgeData "true"/null (not a boolean), unknown key, duplicate key,
  `[]`, trailing data, `{}`, uppercase id. Forged direct workflow input ("../x", "", "S1", "a b", "x;rm") -> activity error `invalid service id (type: InvalidInput, retryable: false)`.
- Other LIVE service L (own container, orphan-labelled container, volume, route, legacy dir, env var, connection, repoUrl): all untouched
  (`volume=true route=exists legacy=exists env=1 conn=1 repoUrl set`).

## cmd/worker
`go build ./cmd/worker` ok; main.go registers `TeardownWorkflow` and passes `Temporal: tc`; `newActivities` now refuses a nil Temporal (unit test in cmd/worker passes).
There is no separate "registration check" in cmd/worker; registration was exercised for real by the e2e (worker.RegisterWorkflow(TeardownWorkflow) + RegisterActivity(*Activities)
including CancelServiceDeploys/TeardownService against a live Temporal). The worker was NOT started against the dev stack.

## Docker safety
Snapshots of `docker ps -a --format '{{.Names}} {{.Status}}'` and `docker volume ls -q` before/after. All vfy12-* resources were removed by exact name
(`docker rm -f vfy12-temporal vfy12-nginx`; test-created containers/volumes removed by the test). Residual diff vs. before: a testcontainers `reaper_66fddf5f...`,
a postgres:16 container `hopeful_hertz` and one anonymous volume, all carrying a different testcontainers sessionId (66fddf5f...) than any of my runs, started
after my tests ended = another agent's concurrent session, not mine, left alone. Dev stack (cp_*, minio, clickhouse, rabbitmq, registry, nginx), ~/cp-backups, ~/cp-logs
and the dev DB were not touched.

## Failures
None. (Observation only: the scratchpad is shared with other agents running mutation tests in a copy; my logs were not affected.)

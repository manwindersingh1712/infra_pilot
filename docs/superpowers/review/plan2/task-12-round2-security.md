# Plan 2 Task 12 fix round 1: security re-review (5fb3c08..d7fef14)

Tools: govulncheck (hard-limited, full path): no vulnerabilities affecting the code (1 in a required module, not called). gitleaks `5fb3c08..HEAD`: no leaks. gosec: 2 G204 (logstream.go:61, exec.go:26), both pre-existing, outside the diff, argv lists with no shell. gosec again printed "export data version 5 > 4" import errors for several packages (toolchain mismatch), so coverage of the changed files is partial; a manual read replaced it. Tests: `go test -p 2 ./internal/store ./internal/runtime ./internal/workflows ./internal/api ./internal/metrics ./internal/worker/... ./cmd/...` all pass.

## Earlier findings: status
- SEC-1 (restart impossible): fixed. Starter uses ALLOW_DUPLICATE_FAILED_ONLY, `teardownRetry` has MaximumAttempts 0; `ignoreAlreadyStarted` still swallows Running/Completed.
- SEC-2 (password row): fixed by keeping only the generated password row (see Q3).
- SEC-4 (RemoveVolume): fixed. `cp-vol-` prefix and `ValidServiceID` of the remainder are enforced inside the function before docker runs; RemoveServiceContainers and RemoveRoute use it too.
- SEC-5 (symlink root): fixed at startup (`Lstat`, symlink is fatal, missing dir is fine, empty disables). Residual TOCTOU is negligible: it needs a local attacker able to swap the directory after the worker starts, and the delete stays confined to one child named with a validated id (os.Root). Accepted.
- SEC-3 (residue: images, logs, deployment rows) and SEC-6 (info) remain as backlog/known residue.

## Answers to the focus questions

1. Unlimited retry + ALLOW_DUPLICATE_FAILED_ONLY. No infinite hot loop: activity retries are server-side with 10 s doubling to a 5 min cap, so a forever-failing step costs one attempt per 5 min per service and adds no workflow history. A poison event is bounded by shape: a non-cuid id is rejected as permanent in the starter (dead row, Error log with serviceId); an unknown but well-formed id makes TeardownService return success (no-op); a live service gives `NotDeleted` (non-retryable, the workflow fails once). A failed run is restarted only by a new outbox row, and the publisher publishes each row once, so there is no restart loop. Two concurrent runs cannot overlap: a Running workflow makes a second Start return AlreadyStarted (swallowed). The only overlap is a Terminated run whose in-flight activity attempt finishes while a restart begins; every step is idempotent and Scrub is one row-locked transaction, so this is harmless. TeardownService checks `DeletedAt` on every attempt (`GetServiceAnyState`), and Scrub re-checks under `FOR UPDATE`. Gap: see SEC-7 (the cancel step has no such guard).

2. Runbook. See SEC-8 and SEC-9. serviceId is passed with psql `:'sid'`, which quotes and escapes it, so SQL injection is not possible; an unset variable produces a syntax error, not a wrong statement. The restart INSERT copies `payload` as stored, so purgeData is preserved byte for byte (it is exercised by `runbook_test.go` against real Postgres and the starter). It does not check that the service is soft-deleted. `docker rm -f $(docker ps -aq --filter label=cp.serviceId=<id>)` is an exact key=value filter, no wildcard, so a partial id matches nothing, but a mistyped full id, or `<id>` left empty or the `=` dropped (`label=cp.serviceId` matches every service container) removes the wrong containers. An empty command substitution makes `docker rm -f` fail harmlessly.

3. Managed-DB semantics. Keeping the encrypted password row (`enc:v1:`, same key) is acceptable. All env routes (GET, reveal, POST, DELETE) go through `ownedService`, which uses `GetServiceForOwner` with `deletedAt IS NULL`; a deleted service answers 404 with the same body as a foreign or missing id, and `TestServiceEnv...` asserts GET env for a deleted service gives 404. No other store query reads EnvVar except by serviceId from live paths and the key-rotation scan, and connections (which could inject a peer's env) are scrubbed. `parsePurgeData` rejects a repeated parameter and anything but `true` or `false`; the 400 `invalid_purge_data` and `purge_choice_required` are returned after `ownedService`, so a foreign or deleted id still gets 404 first and no oracle exists. `purge_choice_required` fires only for mongodb and redis, before the transaction, so nothing is deleted. No finding.

4. Id validators. `UpsertRoute` still uses `serviceIDRE = ^[a-z0-9][a-z0-9-]*$`. It contains no `/`, `.` or NUL, so a route file cannot escape `NGINX_ROUTES_DIR`. But an id that this regex accepts and `ValidServiceID` rejects (hyphen, shorter than 20, longer than 32) would be written by UpsertRoute and could never be removed by `RemoveRoute` (it now refuses it), leaving a permanent route. Production callers pass DB ids (cuids), and `RemoveServiceContainers`/teardown refuse such ids too, so this is defense in depth only. See SEC-10.

5. Startup check, metrics, logs, e2e. Metric labels are a fixed pair (`cancel|teardown`; any other value ignored), no ids or values. Log lines carry `serviceId` (clipped to 40), step, attempt and `err`; errors from the activities contain no env values. The purgeData warning logs the value clipped to 40 bytes as a structured slog attribute, so text and JSON handlers quote/escape CR, LF and quotes (no log injection). The rejected-event path logs `fmt.Sprint(serviceId)` clipped to 40 as an attribute, same protection. The e2e tests use a Postgres testcontainer, a randomly named nginx (`e2etd-nginx-<rid>`), service ids from `testutil.Seed` (random), label `cp.e2e=teardown`, `t.TempDir()` for routes and legacy root, and clean up exact names; the real-Temporal tests use random task queues. They never reference `cp_*` names and cannot touch the dev stack, because every destructive call is keyed by the random ids.

6. Starter `Log` and `TaskQueue` fields: `TaskQueue` is test-only, defaults to the constant, and is not set from external input; `Log` nil falls back to slog.Default. `registerWorkflowsAndActivities` is a plain extraction behind a small interface. No finding.

## Findings

[SEC-7] severity: low
where: internal/workflows/teardown.go cancelServiceDeploys (about line 117)
problem: `CancelServiceDeploys` runs first and has no NotDeleted check. Only `TeardownService` refuses a live service. A misrouted, forged or runbook-restarted teardown event for a LIVE service therefore cancels that service's in-flight deploys (and with unlimited retry and a fresh restart, can do it again) before the workflow fails with NotDeleted. Requires DB write access or an operator mistake, so low.
evidence: cancelServiceDeploys calls checkServiceID then ListOpenDeploymentIDs and CancelWorkflow with no `GetServiceAnyState`/`DeletedAt` check.
fix: At the top of the cancel activity, load `GetServiceAnyState`; not found: return nil; `DeletedAt == nil`: non-retryable `NotDeleted`. Add a test (live service with an open deploy is not cancelled).

[SEC-8] severity: medium (operator safety; documentation fix)
where: docs/superpowers/runbook/teardown-stuck.md sections 3, 4, 5
problem: The destructive SQL has no "service is soft-deleted" guard. Section 4 `DELETE FROM "EnvVar" WHERE "serviceId" = :'sid'` and the `DELETE FROM "ServiceConnection"` run against whatever id was set; a mistyped `\set sid` that names a LIVE service wipes its env vars (including the encrypted generated DB password, which loses a managed database for good) and its connections. Section 5's `DELETE FROM "EnvVar"` has the same problem; it also deletes every env row rather than only the kept password key. The section 3 INSERT can queue a teardown event for a live service (see SEC-7). Only the `UPDATE ... "repoUrl"` carries `"deletedAt" IS NOT NULL`. The text says "The service must already be soft-deleted" but offers no check before the statements.
fix: Add the guard to every statement, for example `DELETE FROM "EnvVar" WHERE "serviceId" = :'sid' AND EXISTS (SELECT 1 FROM "Service" WHERE "id" = :'sid' AND "deletedAt" IS NOT NULL);` (same for ServiceConnection), and `AND EXISTS (...)` in the restart INSERT's WHERE (join `Service`). In section 5 restrict to the password key (`AND "key" IN ('REDIS_PASSWORD','MONGO_INITDB_ROOT_PASSWORD')`). Put a mandatory first step: `SELECT "id","name","serviceType","deletedAt" FROM "Service" WHERE "id" = :'sid';` must show a non-null `deletedAt` and the expected name.

[SEC-9] severity: low
where: docs/superpowers/runbook/teardown-stuck.md section 4 and 5 shell commands
problem: `docker rm -f $(docker ps -aq --filter label=cp.serviceId=<id>)`, `rm .../svc-<id>.conf` and `docker volume rm cp-vol-<id>` use a hand-typed `<id>` with no verification. A wrong id removes another service's containers/route; `label=cp.serviceId` (value dropped) matches every managed container. `docker volume rm` without `-f` refuses an in-use volume, which helps.
fix: Use a shell variable set once and verified: `ID=cm...; test ${#ID} -ge 20 || exit 1`, then `docker ps -a --filter "label=cp.serviceId=$ID"` (read first, no removal), then `docker rm -f $(docker ps -aq --filter "label=cp.serviceId=$ID")`. Tell the operator to run the section 8 SQL check first and to never drop the `=<id>` part. Section 5: list candidates with the SQL `SELECT id FROM "Service" WHERE "deletedAt" IS NOT NULL` and remove only volumes in that set.

[SEC-10] severity: low (defense in depth)
where: internal/runtime/nginx.go:27 and checkRoute (line 33)
problem: `serviceIDRE` (`^[a-z0-9][a-z0-9-]*$`) is looser than `ValidServiceID`. It cannot escape the routes directory, but it can create a `svc-<id>.conf` that `RemoveRoute`/teardown will never match, an orphan route that stays exposed after the service is deleted.
fix: Make `checkRoute` use `ValidServiceID` and delete `serviceIDRE`, so create and remove accept the same ids. Check tests using short or hyphenated ids (production ids are cuids).

[SEC-11] severity: info
where: docs/superpowers/release-note-plan2.md, Teardown paragraph
note: Says delete is "permanent" and teardown retries until it succeeds. It does not mention that a managed DB deleted with `purgeData=false` keeps its volume and an encrypted password row on the server disk. Add a sentence matching the ruling 3 wording when the full note is written (the backlog item already exists).

RESULT: FINDINGS (0 critical, 0 high, 1 medium, 3 low, 1 info)

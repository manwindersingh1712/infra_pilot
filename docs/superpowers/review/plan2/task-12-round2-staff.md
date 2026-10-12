# Plan 2 Task 12 fix round 1: Staff re-review

Range: 5fb3c08..d7fef14 (dcd8564, f4ae53c, 6a42b4e, 5f1e04c, f073cc5, 3a8d171, e4568fe, d7fef14). Reviewer: Staff.

Gates: `go build ./... && go vet ./...` clean; `golangci-lint run`: 0 issues.
`go test -p 2 -race -count=5 ./internal/workflows ./internal/runtime ./internal/store ./internal/api ./internal/metrics ./cmd/worker ./internal/worker/outbox`: all ok, no flake.
`scripts/e2e-temporal.sh` (throwaway Temporal container): PASS, including TestE2ETeardownRealDocker, TestE2ETeardownRestartsAfterFailureOnly, TestE2ETeardownRunningStartIsSwallowed, TestE2ETeardownRunbookRestart.
Real-Temporal and real-Docker tests are env-gated (`TEMPORAL_E2E_ADDR`, `-short`, image presence) and skip cleanly.

## Rulings checked

1. Retry forever: `teardownRetry` = MaximumAttempts 0, 10 s, x2, max 5 m, non-retryable `InvalidInput` and `NotDeleted`; both option sets use it; no ScheduleToClose; the starter sets no WorkflowExecutionTimeout or RunTimeout (grep clean), so nothing caps the run. Per-attempt timeouts (6 m / 1 m heartbeat / 2 m) unchanged. Retry/option edits do not change commands, so replay stays deterministic (no non-determinism: only options and an extra non-workflow wrapper). ALLOW_DUPLICATE_FAILED_ONLY allows a restart after Failed, Terminated, Canceled and TimedOut and refuses Completed and Running, which is the SDK/server semantic; the real-Temporal test covers Failed, Completed and Running only. Metric `cp_teardown_activity_failures_total{step}` and Warn/Error (from attempt 3) logs with serviceId, step, attempt: present and tested. Rejected-permanent event logs the serviceId: present. Runbook: all items (i)-(vi) present and the numbers match the ruling.
2. `purgeData`: only the literal `true` purges; other values warn and mean false; strictness elsewhere kept. API: `invalid_purge_data` before the transaction, repeated params rejected, ownership first (foreign caller sees 404). Matches.
3. Managed DB: `purge_choice_required` for mongodb and redis only, after ownership; `ScrubDeletedService(ctx, id, keepGeneratedSecret)` keeps only the type's own password key (a redis service does not keep the Mongo key); workflow passes `!PurgeData`. Matches.
4. `runtime.ValidServiceID` in one place; starter, activities, RemoveServiceContainers, RemoveRoute, RemoveVolume use it. RemoveVolume requires `cp-vol-` plus a valid id inside the function.
5. `checkLegacyVolumeRoot` Lstat at start: symlink fatal, missing fine, empty disabled; `run` calls it before touching anything.
6. STAFF-4 a/b/c: heartbeat listener test, cancel-fails-no-teardown test, `registerWorkflowsAndActivities` with a recording registry. Present.
7. Spec D2 and the §4 paragraph match decisions.md wording word for word.

## Findings

[STAFF-R2-1] severity: low
where: cmd/worker/main.go:171 (`registerWorkflowsAndActivities(w, acts)`), scripts/e2e-temporal.sh, Engineer deviation 1
problem: The ruling asked for a teardown case that uses the real worker binary. The e2e script instead runs Go tests that start their own worker, and the registry test covers only the helper. Deleting the call in main.go leaves every test green (mutant below survives). The same gap applies to `DeployWorkflow`, so it is not new, but STAFF-4c's mutant ("RegisterWorkflow(TeardownWorkflow) removed from main.go") is now only killed when removed inside the helper, not at the call site.
evidence: mutant `registerWorkflowsAndActivities(w, acts)` -> `_ = acts`: `ok infrapilot/cmd/worker`. The helper-internal mutant (Teardown registration removed) is killed by TestRegisterWorkflowsAndActivities.
fix: acceptable to defer to Task 27 (smoke-worker.sh runs the real binary against a real Temporal): add one line there that deletes a seeded service and waits for `teardown-<id>` Completed. Record it in the backlog. Not a blocker.

[STAFF-R2-2] severity: low
where: internal/worker/outbox/runbook_test.go
problem: The test runs only the `restart-teardown` block. It asserts what matters (one row, new id, pending, attempts 0, purgeData kept, other services and event types untouched, `INSERT 0 0` for an unknown service, then a real publisher starts a new run on real Temporal and the activity sees `PurgeData=true` twice). It does not run the other SQL in the runbook: the `dead` listing query (step 1) and the step 4/5 DELETE/UPDATE statements. A typo in a column name there would ship. It also does not check the fenced block is a single statement with the `psql` variable form, only that `:'sid'` appears.
fix: optional: extend the helper to extract every ```sql block and run them against the test DB (the step-1 SELECT and the step 4/5 statements with `:'sid'` replaced), asserting no error.

[STAFF-R2-3] severity: low
where: internal/workflows/teardown.go:76 (comment `// failed passes err through ...` above `teardownFailed`)
problem: the doc comment names `failed`, the function is `teardownFailed`. Also `recordTeardownFailure` logs with `context.Background()` and counts context-cancelled attempts (worker shutdown, StartToClose expiry) as failures, so a rolling worker restart can bump the counter; the 30 m `for` clause in the alert absorbs it.
fix: rename the comment. No code change needed for the metric.

[STAFF-R2-4] severity: low (informational)
where: internal/runtime/nginx.go:27 (`serviceIDRE = ^[a-z0-9][a-z0-9-]*$`, used by UpsertRoute), Engineer deviation 2
problem: UpsertRoute still accepts shorter or hyphenated ids, while RemoveRoute needs 20-32 lowercase alphanumerics. Ruling 4 lists RemoveServiceContainers, RemoveRoute, RemoveVolume, the starter and the activities, not UpsertRoute, so the Engineer is within the ruling. The consequence: a route written for an id that RemoveRoute would refuse could never be removed by teardown. Production ids are cuids, so this cannot happen today; deploy-side tests use short ids.
fix: none now. Backlog: tighten UpsertRoute when the deploy-side test ids are moved to cuids (Plan 3).

[STAFF-R2-5] severity: low (informational)
where: internal/workflows/teardown_test.go TestCancelServiceDeploysHeartbeatsWhileWaiting (`beats < 1`), Engineer deviation 3
problem: `>= 1` is what the ruling asked for and kills the removed-heartbeat mutant. It would not catch heartbeating only once before the loop; the poll interval is 1 ms in the test, so the loop must run several times. Acceptable.
fix: none.

[STAFF-R2-6] severity: info
where: workflow id retention
problem: A Completed `teardown-<id>` stays unique only while the namespace keeps the closed execution (default retention 72 h in dev, set by the namespace). After that a late outbox event could start a second teardown. It is harmless (every step is idempotent and `ScrubDeletedService` is a no-op), except that a second event with `purgeData=true` after a kept volume would purge it. The runbook already says a kept volume "cannot be purged later by an event", which is true only inside the retention window. Wording nit in runbook section 5 and decisions.md; no code change.

[STAFF-R2-7] severity: info
where: tests moved from redis to nodejs (Engineer deviation 4); `deploymentIDPattern` kept separate (deviation 5)
verdict: both justified. Redis-typed delete tests would now get 400 `purge_choice_required`; TestDeleteServiceDefaultsToKeepingData now covers four non-managed types, empty and unrelated query forms, and the managed cases have their own test. `deploymentIDPattern` is a different id (deployment cuid, shorter minimum in the deploy path) and sharing `ValidServiceID` would widen the contract; keep separate.

## Mutation results (throwaway `git archive` copy outside the repo, `go test -p 2`)

Killed: heartbeat removed; workflow ignores cancel error; reuse policy REJECT_DUPLICATE and ALLOW_DUPLICATE; retry MaximumAttempts 6; MaximumInterval 0; scrub always deletes; scrub always keeps; keepGeneratedSecret inverted (workflow and store); kept password for every type; store keep flag ignored; store keeps the wrong key (Redis name for Mongo); RemoveVolume with no prefix check; RemoveVolume prefix only (no id validation); metric increment removed; Error level never used; API managed check skipped; API managed check for only mongodb or only redis; API accepts any purgeData value; API accepts a repeated parameter; starter treats odd purgeData as true; starter does not warn; symlink check removed; symlink check call removed from main.go; `TeardownWorkflow` registration removed in the helper.

Invalid mutants (build failures, re-done compile-safe): RemoveVolume prefix-only, API managed check skipped, symlink check removed, register call removed.

SURVIVED:
1. `registerWorkflowsAndActivities(w, acts)` removed from cmd/worker/main.go (STAFF-R2-1).
2. `workflows.Starter{Client: tc, Log: logger}` -> `{Client: tc}` in main.go: falls back to slog.Default(), equivalent in effect except the handler; untested, harmless.
3. Not mutated, by design: "validate purgeData after the delete commits" is killed by the API tests' assertion that the service stays live and no outbox row exists (the "bad value accepted" mutant dies on the same assertions).

## Cohesion

teardown.go 249 lines, activities.go 598, starter.go 199: acceptable; teardown.go holds the retry policy, metrics/log helpers, both activities and the workflow, which is one concern. Naming consistent (`teardownRetry`, `teardownOptions`, `cancelOptions`). Metric name follows the `cp_..._total` convention and the step label is bounded by the constants (`metrics.StepCancel/StepTeardown`; other values ignored). Doc comments present except the nit in STAFF-R2-3.

RESULT: APPROVE (0 critical, 0 high, 0 medium, 4 low, 3 info). The two earlier mediums (STAFF-1, STAFF-2) are fixed as ruled; all earlier surviving mutants except the call site in main.go are now killed.

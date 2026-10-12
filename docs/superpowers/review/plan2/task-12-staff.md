# Plan 2 Task 12 (teardown, worker side): Staff review

Range: 28211dd, 66457af, 473a502, 5fb3c08 (control-plane-go/). Reviewer: Staff.
`go build ./... && go vet ./...` clean; `golangci-lint run`: 0 issues.
`go test -p 2 ./internal/workflows ./internal/runtime ./internal/store ./cmd/worker`: all pass.

Spec/backlog coverage: GetServiceAnyState (T2 STAFF-1), cancel of deploy-<id> first (STAFF-8), FailOpenDeployments (STAFF-9), EnvVar + repoUrl scrub (T8 SEC-6/STAFF-5), ServiceConnection delete (T10 STAFF-3), starter mapping. All present. The deviations are justified: RemoveRoute already exists; splitting the activity is right, because the cancel step needs its own heartbeat and timeout; the label sweep closes the gap where a cancelled deploy started a container no row points at; the nil-dependency check covers Temporal.

TeardownWorkflow is deterministic: two sequential ExecuteActivity calls, no time, rand, maps or goroutines. `retry` is a shared read-only pointer.

## Findings

[STAFF-1] severity: medium
where: internal/workflows/teardown.go:48-54 (activity options), starter.go (REJECT_DUPLICATE), spec §4 / D2
problem: A FAILED TeardownWorkflow can never be retried. Both activities use `retry` (6 attempts, 10 s to 5 min backoff). When they are exhausted the run fails, the outbox row is already `published`, and `teardown-<serviceId>` is reserved by REJECT_DUPLICATE. The API cannot delete again (already soft-deleted, spec D2). Result: the service is hidden from the user but its containers keep running, its route keeps serving, and the volume stays. Only a manual `tctl workflow reset` or a hand-written outbox row recovers it. The failure window is short: a Docker daemon restart, an nginx reload failure or a Postgres blip longer than about 5 minutes (about 23 minutes for CancelServiceDeploys) is enough. Plan 3's reconciler is the only planned recovery and is not in scope here.
evidence: `cancelOptions()` and `shortOptions()` both set `RetryPolicy: retry` (MaximumAttempts 6). Starter uses WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE and `ignoreAlreadyStarted` swallows the error. Nothing re-emits the event.
fix: Teardown is idempotent and has no commit point, so retry it effectively forever. Give TeardownService (and ideally CancelServiceDeploys) a RetryPolicy with MaximumAttempts 0, InitialInterval 10 s and MaximumInterval 5 min, with no ScheduleToClose limit. Failed attempts then show in the Temporal UI and a metric. Keep the non-retryable error types (InvalidInput, NotDeleted). Alternatively use ALLOW_DUPLICATE_FAILED_ONLY plus an operator runbook line for re-sending the event. At minimum, record in backlog and in the cutover runbook that a failed teardown needs a manual reset.

[STAFF-2] severity: medium
where: internal/workflows/starter.go:102-134 (parseTeardownPayload), Engineer open question 1
problem: A malformed `purgeData` (string, number, null, object) is rejected permanently (ErrOutboxPermanent), so the workflow never starts and the already soft-deleted service is left running, serving traffic and hidden from the user. The two possible errors are not equal. Falling back to `false` keeps the data (recoverable; the volume can be purged later) and still stops the service. Rejecting keeps both the data and the live container.
evidence: the test table `purge string/number/null/object` expects ErrOutboxPermanent. The API is the only writer and writes a typed bool, so this can only happen on a bug or tampering; in that case a ghost service is the worse outcome.
fix: Answer to open question 1: keep strictness for `serviceId`, unknown keys, duplicate keys and trailing data (these mean the payload is not ours; a wrong serviceId would hit the wrong service). For `purgeData` only, treat anything other than the JSON literal `true` as false, and log a warning with the serviceId. Purge stays opt-in on the exact literal `true`, which the current tests already assert. Update the table: move the purge cases to a "starts, PurgeData=false" test. If the Engineer prefers the current behaviour, the dead event must at least be an alert (backlog line).

[STAFF-3] severity: low
where: internal/workflows/teardown.go:34-47 (cancelOptions) and :70-72 (defaults), Engineer open question 2
problem/answer: The 3-minute wait with 6 attempts is acceptable as a per-attempt bound but is not the right overall bound (see STAFF-1). Timeouts are consistent: StartToClose 6 min > CancelWaitTimeout 3 min, heartbeat every poll (1 s) vs HeartbeatTimeout 1 min, and the deadline is shared across all workflows in one attempt (good). A cancelled deploy can legitimately take longer than 3 min (RunContainer has WaitForCancellation and a 10 min StartToClose; BuildImage 30 min with heartbeat cancel), so the first attempts may time out and CancelWorkflow is re-sent (idempotent). Total worst case is about 6 x 3 min + 310 s backoff, about 23 min. With STAFF-1 fixed (unlimited retries) this is fine as is. The activity returns a plain error on timeout, so it is retryable. Good.
fix: no change beyond STAFF-1. Optionally raise the default wait to 10 min to match RunContainer's worst case and so avoid pointless retries.

[STAFF-4] severity: low
where: internal/workflows/teardown_test.go (TestCancelServiceDeploys*, TestTeardownWorkflowRunsActivities)
problem: Test teeth gaps found by mutation (see below): (a) the activity heartbeat is not asserted (removing `activity.RecordHeartbeat` survives; with HeartbeatTimeout 1 min, a regression would fail a long wait after 1 min); (b) the workflow test has only the happy path, so a workflow that ignores a CancelServiceDeploys error and tears down anyway survives; (c) nothing exercises TeardownWorkflow against a real Temporal server, and `w.RegisterWorkflow(workflows.TeardownWorkflow)` in cmd/worker/main.go can be deleted with every test green (the plan 1 e2e script covers only deploy).
fix: (a) use `testsuite.TestActivityEnvironment.SetOnActivityHeartbeatListener` in the wait test and assert at least one heartbeat. (b) add a workflow test where the cancel activity returns an error and TeardownService is never called (`env.OnActivity(...TeardownService).Times(0)`). (c) add a teardown case to scripts/e2e-temporal.sh (delete a service with a running deploy: the deploy ends cancelled, the container and route are gone) or a startup test that lists registered workflows. Task 27 may cover (c); record it.

[STAFF-5] severity: low
where: internal/workflows/teardown.go:84-111 (TeardownService order)
problem: Deployed deployments are not failed (correct) but a DeployWorkflow in its post-commit tail (RetirePrevious / StartLogStream, status already `deployed`) is not in ListOpenDeploymentIDs, so it is neither cancelled nor awaited. StartLogStream could start a log follower on a container that teardown has just removed. The window is seconds and the follower ends when the container goes, so this is cosmetic.
fix: none required; backlog note. Same for Node-era containers that lack the `cp.serviceId` label: the sweep does not see them, but they are covered by the per-deployment `Remove(ContainerName(d.ID))` as long as the Deployment row has no stoppedAt.

[STAFF-6] severity: low
where: internal/workflows/teardown.go:177-197 (removeLegacyData)
problem: Removes only `<LegacyVolumeRoot>/<serviceId>`. A Deployment.volumePath stored elsewhere under the root (the deploy side accepts any stored path inside the root, activities.go legacyDir) is not removed on purge. Node always stored `<root>/<serviceId>`, so this is theoretical.
fix: none now; optionally also remove the stored volumePaths of the service's deployments if they resolve inside the root.

## Positive checks (no finding)
- Cancel before removal is correct: the deploy cleanup (RevertRoute, RemoveContainer, MarkFailed) finishes before RemoveRoute, so a revert cannot re-add a route afterward. A workflow that does not exist yet is skipped; when it starts late, LoadJob (`deletedAt IS NULL` in GetJob) stops it.
- NotFound handling: both CancelWorkflow and Describe map `serviceerror.NotFound` to "closed". Non-RUNNING statuses end the wait.
- Non-retryable types: InvalidInput and NotDeleted are non-retryable; a missing service is a no-op success; the id is validated in the starter and in both activities, and again in Docker (checkName, serviceIDRE, container-id regexp). `docker rm -f <id>` cannot get a flag-like argument.
- `os.Root.RemoveAll` does not follow a symlink at the final component, so the symlink test is meaningful.
- Context passes through everywhere; the poll loop selects on ctx.Done().
- Operability: the "service does not exist" warning carries the serviceId. Failures surface as Temporal activity failures. Add serviceId to the log when a dead teardown event is rejected (the permanent-error text already names the clipped key but not the event id; low).

## Mutation results (throwaway `git archive` copy outside the repo, `go test -p 2`)
Killed (test fails): purge always on; purge on non-exact payloads (string/1/"true"/null); skip cancel step; cancel after removal (order); label sweep removed; label filter widened; `ps` without `-a`; sweep id check and output check removed; missing-container error not ignored; RemoveVolume without checkName; RemoveVolume not ignoring "no such volume"; live service teardown allowed; invalid serviceId accepted (activity and starter); unknown service returns error; FailOpenDeployments removed; MarkStopped removed; scrub removed; RemoveRoute removed; cancel NotFound fatal; wait skipped; wait timeout ignored (hangs); only the first workflow cancelled; foreign service's workflows cancelled; payload with unknown key / duplicate key / trailing data / non-boolean purgeData accepted; ErrOutboxPermanent dropped; ALLOW_DUPLICATE reuse policy; non-unique workflow id; "already started" treated as error; PurgeData dropped between workflow and activity; store: FailOpenDeployments not filtering status or service, GetServiceAnyState live-only, scrub without deleted-check, scrub not deleting env rows / source-end connections / target-end connections / not nulling repoUrl, ListOpenDeploymentIDs including deployed or ignoring service; nil Temporal accepted by newActivities.

SURVIVED:
1. Heartbeat removed from CancelServiceDeploys (STAFF-4a).
2. Workflow ignores a CancelServiceDeploys error and still tears down (STAFF-4b).
3. `RegisterWorkflow(TeardownWorkflow)` deleted from cmd/worker/main.go (STAFF-4c).
4. Equivalent mutants, not findings: "volume name built from the raw id" (VolumeName is `cp-vol-`+id, same value; the validation is covered by the bad-id killers) and "legacy removal with plain os.RemoveAll" (RemoveAll also unlinks a final symlink; the os.Root version is still the better defence against a swapped parent, which no test covers).

RESULT: CHANGES REQUESTED (0 critical, 0 high, 2 medium, 4 low)

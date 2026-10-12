# Plan 2 Task 12 fix round 2: security re-review (d7fef14..fdb0576)

Tools (from `control-plane-go/`, gitleaks on the range): govulncheck (hard-limited, full path): no vulnerabilities affecting the code (1 in a required module, not called). gosec: 2 G204 (logstream.go:61, exec.go:26), both pre-existing, outside the diff, argv lists; the usual "export data version 5 > 4" import errors appear, so the changed files were read by hand. gitleaks `d7fef14..HEAD`: no leaks. Tests: `go test -p 2 ./internal/runtime ./internal/workflows ./internal/worker/... ./internal/store` all pass (38 GB free before the run).

## Status of earlier findings

- SEC-7 closed. `cancelServiceDeploys` now runs `checkServiceID`, then `deletedService` (the same helper `teardownService` uses), and only then `ListOpenDeploymentIDs`/cancel. Not found returns nil (no-op), a live service returns non-retryable `NotDeleted`, other errors are retryable. `TestCancelServiceDeploysRefusesLiveService` proves a live service with a running deploy workflow cancels nothing and an unknown well-formed id succeeds. Residual TOCTOU between check and cancel is irrelevant (soft delete is one-way).
- SEC-8 closed. Every mutating runbook statement was read:
  - `restart-teardown` INSERT: `EXISTS (... "deletedAt" IS NOT NULL)` on `:'sid'`.
  - `scrub-rows`: EnvVar delete is joined to `Service` with `deletedAt IS NOT NULL` and keeps the two password keys only for `mongodb`/`redis` types (type join present, a user key named `REDIS_PASSWORD` on a node service is still removed); ServiceConnection delete and the `repoUrl` UPDATE carry the same guard. Step 4's by-hand text now keeps the managed password rows, consistent with the workflow and with section 5.
  - `purge-password`: limited to the two reserved keys, soft-deleted, `serviceType IN ('mongodb','redis')`. It cannot touch another service's rows.
  - Step 0 is mandatory and first. Read-only blocks (`inspect-service`, `dead-outbox`, `orphan-volumes`) change nothing.
  - Quoting: `:'sid'` is psql's literal quoting (escapes quotes); an unset variable is a syntax error. The test substitutes `'` + sid + `'` by plain string replace, which does not test hostile ids, but the ids in the tests are cuids and the quoting itself is psql's, so this hides nothing real. `::text` in the INSERT is not touched by the substitution.
  - `TestRunbookEverySQLBlockRunsAndSparesLiveServices` runs every `sql` block verbatim on real Postgres against a LIVE nodejs, redis and mongodb service and asserts 0 rows affected on all mutating statements and unchanged counts; `TestRunbookScrubAndPasswordStatementsOnDeletedServices` checks the deleted case and that a bystander is untouched. Statements are split on `;` and comments stripped; the runbook SQL has no `;` or `--` in literals, so nothing is silently skipped.
- SEC-9 closed. Verified in bash 3.2/5 and zsh: `^[a-z0-9]{20,32}$` accepts the cuid and rejects trailing newline, embedded newline, uppercase, empty and `a=b` (`$` does not match before a trailing newline in `[[ =~ ]]`); uppercase is rejected under en_US.UTF-8 too. The docker filter is `label=cp.serviceId=$SID` (exact match, value cannot be dropped as the text says), listing comes before removal, empty `IDS` skips `rm`, volume removal is by exact name without `-f`, and the volume list comes from the SQL `orphan-volumes` block.
- SEC-10 closed. `serviceIDRE` is deleted; `checkRoute` uses `ValidServiceID`, the same function `RemoveRoute` uses. `TestUpsertRouteAndRemoveRouteAcceptTheSameIDs` checks a fixed table of valid, short, long, dashed, uppercase, traversal, dot and newline ids (not a randomized property test, but the two functions now share one validator, so the table is sufficient). The only production callers pass DB cuids (activities.go:466, 483).
- SEC-11 closed. The release note now states that the volume and ONE encrypted password row are kept, that it cannot be re-attached from the UI, and points to runbook section 5. The retention-window claim in section 5 is accurate: the completed `teardown-<id>` workflow holds the id only inside the namespace retention window. The `teardownFailed` comment is accurate (a cancelled context also counts; the 30 min alert window absorbs it).

## New findings

[SEC-12] severity: info
where: docs/superpowers/runbook/teardown-stuck.md step 4 (`docker rm -f $IDS`) and the `exit 1` in the SID check
problem: The operator's shell is zsh. Verified: in zsh an unquoted `$IDS` is not word-split, so with two or more matching containers `docker rm -f $IDS` receives one argument containing a newline and fails (nothing is removed). It fails safe (no wrong container is removed), but the step does not work for a service with several containers. Separately `exit 1` closes an interactive terminal when the check fails.
fix: Use `docker ps -aq --filter "label=cp.serviceId=$SID" | xargs docker rm -f` (with GNU xargs `-r`, or keep the `[ -n "$IDS" ]` guard), and use `return 1` or a note that the check is meant for a script/subshell.

[SEC-13] severity: info
where: runbook step 5 item 2, `docker volume ls --filter "name=cp-vol-$SID"`
note: The `name` filter is a substring match. Because ids are 20 to 32 chars an id could in theory be a prefix of a longer one, so "exactly one line expected" is the right check and is stated; the removal itself is by exact name. No change needed.

RESULT: CLEAN for SEC-7..SEC-11 (all closed, nothing new above info). FINDINGS (0 critical, 0 high, 0 medium, 0 low, 2 info)

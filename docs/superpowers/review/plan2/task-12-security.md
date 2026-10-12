# Plan 2 Task 12 security review (commits 28211dd, 66457af, 473a502, 5fb3c08)

Tools: govulncheck: no vulnerabilities affecting the code (1 in a required module, not called). gitleaks (range 28211dd~1..5fb3c08): no leaks.
gosec: 2 G204 (logstream.go:61, exec.go:26), both pre-existing and outside the diff; args are built as lists, no shell. Note: gosec could not load export data for several packages ("export data version 5 > 4", toolchain/cache mismatch), so its coverage of teardown.go was partial; a manual read replaced it.
Tests: `go test -p 2 ./internal/store ./internal/runtime ./internal/workflows ./cmd/...` all pass.
Experiment: os.Root.RemoveAll on a symlink named like the id removes the link only (target untouched); `../x` rejected ("path escapes from parent").

## What was verified clean
- Id validation: `^[a-z0-9]{20,32}$` in starter parse, CancelServiceDeploys, TeardownService; runtime layers re-check (`serviceIDRE` in nginx.go, RemoveServiceContainers, `checkName` in RemoveVolume). Empty id rejected everywhere. No id can start with `-`.
- Label sweep: `--filter label=cp.serviceId=<id>` is an exact key=value match (no wildcard); output ids re-validated as hex before `docker rm -f`; no shell (argv list). The label is set only by activities.go:386 from DB ids, not by user input.
- Volume name = `cp-vol-`+id; route path = `svc-<id>.conf` joined to RoutesDir with a validated id (no separators possible); legacy dir via os.Root (symlink removed not followed, `..` refused).
- Payload parser: duplicate/unknown keys, null/non-bool purgeData, trailing data, non-object all rejected; unicode escapes are decoded before the regex check; ErrOutboxPermanent -> MarkOutboxDead immediately (publisher.go:99), so a malformed event cannot wedge the publisher. Error text uses clip/%q (no CRLF injection).
- Authorization: the worker refuses a live service (NotDeleted, non-retryable), unknown id is a no-op, Scrub re-checks deletedAt under FOR UPDATE. ClaimDeploy refuses deleted services under a row lock, so a deploy cannot start after delete. No restore/undelete path exists in the store (only SoftDeleteService), so no delete/undelete race today.
- purgeData: API requires literal "true"; parser only accepts JSON true; default false.
- Cancel-first: activity waits 3 min (poll) inside a 6 min StartToClose with heartbeat; deploy activities use WaitForCancellation, so a closed deploy workflow has no activity still running; sweep afterwards also catches kept containers.

## Findings

[SEC-1] severity: medium
where: internal/workflows/starter.go:69-81 (REJECT_DUPLICATE + ignoreAlreadyStarted), internal/api/services.go:297 (second delete -> 404)
problem: A teardown workflow that ends Failed (retry policy is 6 attempts, ~5 min of backoff; CancelServiceDeploys can burn 3 min x 6) can never be restarted. The service row is already soft-deleted so the API returns 404 on re-delete; a re-inserted outbox event is "published" silently because REJECT_DUPLICATE on a closed workflow returns AlreadyStarted, which is swallowed. Result: containers and the nginx route of a deleted service stay running and reachable (route is removed after containers, so a failure before that leaves the route live) until an operator uses `temporal workflow reset`, with no documented procedure.
fix: Use ALLOW_DUPLICATE_FAILED_ONLY for teardown (success stays unique, failure can be re-started by a new outbox event), and/or add an operator command/runbook entry; consider making the teardown activities' retry policy unbounded or much longer (teardown is idempotent).

[SEC-2] severity: low (data handling, document it)
where: internal/store/teardown.go:46-65, workflows/teardown.go:151
problem: With purgeData=false, ScrubDeletedService deletes the EnvVar rows, including the generated managed-DB password ciphertext, while the data volume is kept. The data is retained but unreadable, and there is no restore or purge-later path (a later purge needs a new delete event, which cannot be issued: SEC-1). Orphan volumes accumulate with no owner record.
fix: Either keep the generated-password row until purge, or state in the API/UI that "keep data" is unrecoverable; add a reconciler/purge-orphan-volumes task (Plan 3).

[SEC-3] severity: low
where: teardown.go TeardownService / activities (residue)
problem: Teardown leaves: Deployment rows (commit sha, image refs), registry images/tags under localhost:5001/..., Redis/ClickHouse/S3 logs (may contain secrets printed by the app) and build dirs. Only env rows, connections and repoUrl are scrubbed.
fix: Record as known residue; add log/registry retention or purge on purgeData=true in Plan 3. Ensure the UI wording of "delete" does not promise erasure.

[SEC-4] severity: low (defense in depth)
where: internal/runtime/docker.go:RemoveVolume (~line 440)
problem: RemoveVolume accepts any name matching nameRE (e.g. `cp_pgdata`, any host volume) and passes `-f`; safety rests on callers only passing VolumeName(validatedId). RemoveServiceContainers/RemoveRoute accept the looser runtime serviceIDRE (any length, hyphens) rather than the 20-32 cuid rule.
fix: Require the `cp-vol-` prefix (and the cuid shape of the remainder) inside RemoveVolume, and share one id validator between workflows and runtime.

[SEC-5] severity: low
where: internal/workflows/teardown.go:177 removeLegacyData; default LEGACY_VOLUME_ROOT=/tmp/cp-volumes
problem: os.OpenRoot follows a symlink at the root itself. In a world-writable /tmp, a local user could point /tmp/cp-volumes elsewhere; the delete is still limited to a single child named with a 20-32 char id, so impact is minimal. Also a purge failure after RemoveVolume is retried fine (idempotent).
fix: Optionally Lstat the root and refuse a symlink / non-owned directory; move the default out of /tmp.

[SEC-6] severity: info
where: teardown.go TeardownService
note: Race window: if the service is un-deleted (no such feature today) between the NotDeleted check and Scrub, containers would already be gone; Scrub refuses. Keep this in mind if a restore feature is added: it must cancel/skip pending teardown events. A forged outbox row requires DB write access and can only affect already soft-deleted services (and a forged purgeData=true on a soft-deleted keep-data service would destroy its volume), which is inherent to trusting the outbox.

RESULT: FINDINGS (0 critical, 0 high, 1 medium, 4 low, 1 info)

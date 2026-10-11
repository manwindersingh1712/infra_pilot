# Plan 2 Task 8 - Staff review

Range: 058321e, 0eb270f, 962886b, ce69f7f (go-migration). Files: `control-plane-go/internal/api/services.go` (316 lines), `services_test.go` (470), `server.go` routes, `store/events.go`.
Reviewer: Staff Engineer. No repo code edited.

Checks run from `control-plane-go/`: `go build ./...`, `go vet ./...` clean; `golangci-lint run ./internal/api/... ./internal/store/...` 0 issues; `go test -p 2 -count=1 ./internal/api/` ok (12 s).

## Findings

[STAFF-1] severity: medium
where: internal/api/services.go (setServiceEnv, deleteServiceEnv); review/backlog.md:109
problem: The open backlog line "P1 T23: the env API should refuse to delete or overwrite `REDIS_PASSWORD` / `MONGO_INITDB_ROOT_PASSWORD` for managed services (Plan 2 API)" is not implemented and no later Plan 2 task owns it. Task 17 is UI only. Task 8 is the only task that writes the env API.
evidence: `grep -n "REDIS_PASSWORD\|MONGO_INITDB" plans/2026-10-07-go-plan2-api.md` only hits Task 16/17 reading code (line 4647). decisions.md:265 records it as a Plan 2 follow-up. Today an owner can DELETE the password key of a running managed DB; the next deploy generates a new password the existing volume ignores, so auth breaks (data stays, access is lost).
fix: Either add the guard now (service is managed AND key in {REDIS_PASSWORD, MONGO_INITDB_ROOT_PASSWORD} -> 400 `invalid_env_var` on set and delete; ~8 lines plus a table test) or write a decisions.md line that moves it to a named task. Do not leave it silently open at the plan-end gate.

[STAFF-2] severity: low
where: internal/api/services.go:15-26 vs internal/runtime/docker.go:186-188
problem: The env key rule is now two copies: `envKeyPattern`/`maxEnvKeyLen` in api and `envKeyRE`/`maxEnvKeyLen` in runtime. The 2026-10-10 decision exists precisely so the API never stores a key the worker refuses; two literals can drift. api cannot import runtime (only runtime shells out, and api must not depend on it).
evidence: identical regex text in both files, each with its own test table.
fix: Put one `ValidEnvKey(k string) bool` in a leaf package both already import (`store` next to `ValidServiceType`, or `gitsrc`-style `envspec`) and have both call it. Optional for Plan 2; log in backlog if deferred.

[STAFF-3] severity: low
where: internal/api/services.go:218-220 (`!utf8.ValidString(*body.Value)`) and the comment above it
problem: Dead check with a wrong comment. `encoding/json` replaces invalid UTF-8 (and lone surrogate escapes) with U+FFFD while decoding, so a decoded body string is always valid UTF-8. The comment says invalid UTF-8 "would make Postgres fail (500)"; it cannot arrive this way. (The same check on the path `key` in deleteServiceEnv is live: `%ff` reaches PathValue raw.)
evidence: mutation "drop utf8.ValidString(*body.Value)" survives all 100+ api tests; no test can kill it because the input is unreachable.
fix: Remove the clause and fix the comment, or keep it as cheap defense and say so. No behavior change either way.

[STAFF-4] severity: low
where: internal/api/services_test.go (deploy and delete transaction tests)
problem: Two atomicity claims are only asserted on the happy path. Mutations that run the soft delete and the outbox insert (or the deployment and its event) as separate statements without a transaction survive. The commit messages and the plan CTO note claim "commit together".
evidence: survivors "delete outside tx", "deploy outside tx", "deploy: ErrNotFound -> 500 (race path)". The delete race path IS killed (TestConcurrentDeletesQueueOneTeardown); the deploy-vs-delete race is untested (needs a hook between ownedService and InTx).
fix: Cheap option: a store-level fake/wrapper that fails `CreateOutboxEvent` once and asserts the service is still visible / no deployment row. Alternatively accept and note it; InTx itself is covered in store tests.

[STAFF-5] severity: low (follow-up, not Task 8)
where: ce69f7f / TeardownWorkflow (Task 12)
problem: Soft delete leaves `EnvVar` rows (encrypted secrets) for the deleted service forever, and nothing in Task 8 or the comment says teardown removes them. Owner-scoped reads hide them, but they stay in backups and `encrypt-env`/`ListUnencryptedEnvVars` keep scanning them.
fix: Add "delete EnvVar rows for the service" to Task 12's TeardownWorkflow checklist, or backlog it.

[STAFF-6] severity: low (informational, interim UI)
where: cp-ui/src/components/ServiceDetailsPanel.tsx:231
problem: Until Task 17 lands, the eye toggle shows the literal `********` (UI prints `env.value`). Expected and planned; confirm Task 17 is not skipped before cutover.

## Engineer's 8 deviations

1. Env key pattern relaxed to `^[A-Za-z0-9_][A-Za-z0-9_.-]*$` plus 256-byte cap, instead of the plan's `^[A-Za-z_][A-Za-z0-9_]{0,127}$`. JUSTIFIED. decisions.md 2026-10-10 "Relax env-var KEY validation" explicitly ends with "Plan 2: API-side key validation backlog line", and Node accepted any key; the strict plan rule would reject keys that deploy today (dash, dot, digit-first) and the worker accepts. Tests cover accept (`BAD-KEY`, `my.key`, `1ST`, `_X`, 256 chars) and reject (space, `=`, `#`, `-`, `.` lead, non-ASCII, NUL, 257). See STAFF-2 on duplication.
2. Extra value checks (<=32 KiB, no CR/LF/NUL, UTF-8). ACCEPTED. The worker's `validateEnv` already refuses CR/LF and bad UTF-8 (so the API must too, same reasoning as 1); NUL and the size cap are API hardening (Postgres NUL error, row bloat) and are tested. The UTF-8 clause is dead for body values (STAFF-3). Node had no cap; a 32 KiB cap is far above any real value and a user-visible 400 `invalid_env_var`.
3. Env DELETE not pattern-checked (only <=1024 bytes, valid UTF-8, no NUL, non-empty). JUSTIFIED. Node-era rows may hold keys the write path now refuses (spaces etc.); deleting them must work. Unstorable keys answer `{"deleted":true}` like Node's `deleteMany`. Mutation "no guard" is killed (NUL/`%ff` would 500).
4. Default deploy ref = service Branch, else HEAD (not `main`). JUSTIFIED and consistent with decisions.md 2026-10-11 "Legacy default ref main": the follow-up says "Plan 2 API must send Branch, HEAD or hex" and the builder's `legacyDefaultRef` can then be removed. Verified against `build.checkoutRef`: `ref == s.Branch || ref == "HEAD"` is a no-op checkout, so the tip is built. Branch names that are not valid refs (`feature/x`, `v1`) fall back to HEAD, which is also the clone tip. Image tag ends `:<branch>` so tag/ref integrity holds. Note: the `legacyDefaultRef` backlog item stays open until Node deploys stop (UI sends `HEAD` explicitly, so the UI is unaffected).
5. 400 before 404 on deploy (body validated before ownership). ACCEPTED. Matches Node (zod parse first), leaks nothing (validation does not depend on the service), and avoids a DB read for junk. Test `TestDeployRejectsUnsafeRefsAndUnknownServices` asserts no rows are written on 400.
6. Bad `?projectId=` returns `[]`. ACCEPTED. Node returned `[]` for any unknown id; a shape-invalid id cannot be stored, so `[]` is the same answer as an empty/foreign project and avoids a 500 on NUL bytes. Consistent with spec §5 (no existence oracle). Tested (`%00`).
7. `ownedService` takes `r` (and reads `{id}` itself). ACCEPTED, better than the plan: one place does the `pathID` shape check, so malformed/missing/deleted/foreign all answer the identical 404. Mutation dropping the shape check is killed.
8. No red step. Not verifiable after the fact; the mutation run below shows the tests have teeth, which is what the red step is for. Process note only.

## Mutation results (throwaway `git archive` copy under the session scratchpad, outside the repo; `go test -p 2 ./internal/api/`)

KILLED (test that fails first):
- drop owner check: deploy (TestDeployService, TestDeployRejectsUnsafe...), env list / env set / env delete (TestServiceEnvOwnership), delete (TestDeleteServiceQueuesTeardown)
- list returns all users' services (TestCreateAndListServices); create without project ownership (same)
- env list: reveal always on / ignored / ciphertext returned / plaintext when not revealed (TestServiceEnvIsEncryptedAndMasked); env set response unmasked; env set stores plaintext
- RedactURL skipped in create, skipped in list, `publicService` no-op (TestRepoURLCredentialsMasked)
- accept `--upload-pack=x` branch; accept bad repoUrl; accept bad commitSha; name validation dropped; needsRepo dropped
- delete without outbox event; purgeData always true; delete returns 200 instead of 202; concurrent-delete race path -> 500
- deploy without outbox event; managed deploy emits build event; default ref always HEAD / always `main`
- env: value CR/LF/NUL check, value cap, key cap, strict key regex, unguarded delete key; shape checks for `{id}` and `?projectId=`
- store: `GetServiceForOwner` ignoring deletedAt; both deletedAt filters dropped (TestDeployDeletedServiceIs404)
- deploy of a soft-deleted service succeeds: needs BOTH layers broken to be reachable. Each layer alone: GetServiceForOwner alone is killed by the api tests; `CreateDeployment` ignoring deletedAt survives in the api package (the owner lookup still blocks it) but is killed by `store.TestCreateDeploymentRejectsSoftDeletedService`. Good defense in depth.

SURVIVED:
- delete not in a transaction (STAFF-4)
- deploy not in a transaction (STAFF-4)
- deploy `ErrNotFound` race branch turned into a 500 (STAFF-4; unreachable without a hook)
- `utf8.ValidString(*body.Value)` removed (STAFF-3; equivalent mutant, unreachable input)
- `CreateDeployment` deletedAt filter alone (killed in the store package, so fine)

(Two mutants first failed to compile because of my edit; re-run with fixed edits, both killed.)

## Compatibility (cp-ui and Node API)

- Service JSON `{id, projectId, name, serviceType, repoUrl, branch, createdAt, positionX, positionY}` matches Prisma's output for the fields cp-ui reads (`CreateServiceModal` reads only `id`; list consumers use the same names). `repoUrl` is `null` for managed types, as Prisma.
- EnvVar JSON `{id, serviceId, key, value, createdAt}` matches Prisma; `value` is now `********` (spec §5); the cp-ui `EnvVar` interface only needs `id,key,value`.
- Deploy returns `{deploymentId, status}` (same as Node); the UI ignores it. UI sends `commitSha:"HEAD"` for both auto-deploy and redeploy: valid ref, builds the branch tip; managed services thus get `HEAD` not `latest` (same as under Node).
- DELETE `/services/{id}` is new (202 `{deleted:true}`); no cp-ui caller yet (Task 17). `DELETE /services/{id}/env/{key}`: UI does not URL-encode the key; allowed keys contain only `[A-Za-z0-9_.-]` so this works.
- Error strings keep today's names: `service_not_found`, `Project not found`, and the Node zod message `repoUrl required for docker/nodejs/nextjs/react services`. New codes `invalid_request`, `invalid_repo_url`, `invalid_env_var`, `request_too_large` are consistent with Tasks 5-7 naming (snake_case). The one inconsistency (`Project not found` and the long repoUrl sentence are not snake_case) is deliberate Node compatibility per spec §5 "today's error strings".
- Superset note: Go accepts `nextjs` on create; Node's zod enum omitted it (UI already offers it, so Go fixes a latent bug).

## Cohesion

- services.go (316 lines, 7 handlers + `publicService`, `ownedService`, `ownsProject`, `defaultRef`) is within reason; one file per resource matches projects.go. If Task 10 canvas and Task 16/17 add more, split env handlers into `env.go` at that point.
- Validation is not duplicated within api, except the envKey rule across packages (STAFF-2). `validProjectName` is reused for service names (good); `ownsProject` is shared with the future canvas as the plan intends.
- `maxLegacyKey = 1024` for delete vs `maxEnvKeyLen = 256` for set is documented in the constant comment; fine.
- Comments explain the why (legacy rows, race outcomes) at the right density. No request bodies or repo URLs are logged (`internalError` logs method, path, requestId, err only).

RESULT: APPROVE with 1 medium (STAFF-1, needs a decision or a guard), 4 low. Counts: critical 0, high 0, medium 1, low 4 (+1 informational).

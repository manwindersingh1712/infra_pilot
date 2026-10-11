# Plan 2 Task 8 security review (058321e, 0eb270f, 962886b, ce69f7f)

Scope: control-plane-go/internal/api/{services.go,server.go,services_test.go}, internal/store/events.go; spec §5, §7; CTO amendments.

## Tools
- `gitleaks git --redact` (repo root): 349 commits scanned, no leaks.
- `govulncheck ./...` (hard-limited, full path): exit 0, 0 reachable vulnerabilities (1 module-level, not called: the known openpgp item).
- `gosec -quiet ./internal/api/... ./internal/store/...`: no output (clean).
- `go test -p 2 ./internal/api/... ./internal/store/... ./internal/gitsrc/...`: all ok.
- Probe (throwaway test, removed): `ValidRepoURL`/`RedactURL` on backslash-authority, `a@b@evil`, `:tok@h`, `$(id)`, backtick, NUL, IPv6 and localhost URLs. Backslash forms are rejected as unparseable (no Go/git parser differential); `https://a@b@evil.com/x` and `https://:tok@h/x` are redacted to `https://****@...`.

## Verified OK
- Ownership: every route (create, list, deploy, env get/set/delete, delete) goes through `ownedService` (`GetServiceForOwner`, filters `deletedAt IS NULL` and project owner) or `ownsProject`. Malformed, missing, deleted and foreign ids give the same 404 body. Create with foreign or missing projectId gives identical `Project not found`. List with a foreign or malformed projectId gives `[]`; list without projectId is owner-scoped in SQL. `pathID` is used for `{id}`; the `{key}` delete path is NUL/UTF-8/length guarded.
- 400-vs-404 ordering: body validation runs before the ownership lookup on create and deploy, but validation depends only on the request, so it reveals nothing about foreign resources.
- Mass assignment: the bodies decode into fixed structs (projectId, name, serviceType, repoUrl, branch; commitSha; key, value). Position, deletedAt, ownership and id fields are ignored. `serviceType` is checked against `store.ValidServiceType`.
- Env encryption: the value is always `Secrets.Encrypt`ed before `UpsertEnvVar` (never skipped on an `enc:v1:` prefix); responses mask on set and on list; reveal decrypts only after the ownership check; `Cache-Control: no-store` is global. No handler logs values or request bodies; `internalError` logs method, path, requestId and err (store errors do not carry values).
- Env validation: key regex identical to the worker's `envKeyRE`, max 256; value max 32 KiB, valid UTF-8, no CR, LF or NUL; body capped at 1 MiB (`MaxBytesReader`, 413).
- Repo URL/branch/ref: `ValidRepoURL` (https only, printable ASCII, no leading `-` host, 2048 cap), `ValidBranch`, `ValidRef`; `--upload-pack`, `-abc`, `a..bc` rejected. `publicService` is applied on create and list, the only handlers that return a `Service`.
- Deploy: ref default is the branch, or HEAD when the branch is not a valid ref; managed services get `latest`. Deployment and outbox event are in one tx with payload `{deploymentId}`.
- Delete: soft delete and `teardown.requested` outbox row in one tx; payload is `{serviceId, purgeData}` only (no secrets); double delete gives 404 with exactly one event (`RowsAffected` check). Delete-versus-deploy race: `CreateDeployment` is `INSERT ... SELECT ... deletedAt IS NULL` without a row lock, so a deployment can slip in after a concurrent soft delete, but `ClaimDeploy` re-checks `deletedAt` under `FOR NO KEY UPDATE` and refuses to start. Acceptable; Task 12 teardown must still fail such stragglers.
- Auth/rate limits: all service routes are behind `private`; auth routes have their own limiters.

## Findings

[SEC-1] severity: medium
where: internal/api/services.go:setServiceEnv / deleteServiceEnv (lines ~230-290)
problem: the backlog forward requirement (P1 T23, 2026-10-09) is not implemented. For managed services (redis, mongodb) the API lets the owner overwrite or delete the generated password variable (`REDIS_PASSWORD`, `MONGO_INITDB_ROOT_PASSWORD`). The next deploy then generates a new password that the existing data volume ignores, so authentication breaks and the data is unreachable until manual repair. Self-inflicted by the owner (no cross-tenant impact), but it is a data-availability foot-gun with a clear prior ruling.
evidence: neither handler checks `store.IsManaged(svc.ServiceType)`; no test covers it.
fix: for managed services, refuse writes and deletes of the reserved password keys (409 or 400 `reserved_env_var`), with a test; or document an explicit CTO ruling to defer.

[SEC-2] severity: low
where: services.go getServiceEnv
problem: `?reveal=true` returns every decrypted secret to any valid access token with no audit trail and no step-up. Ownership is correct. Spec §5 only demands the flag, but a bearer-token theft or XSS yields all secrets in one call and leaves no trace.
fix: `s.Log.Info("env revealed", "serviceId", svc.ID, "userId", userID(r), "requestId", ...)` (ids only, never keys or values). Consider counting reveals in metrics. Also note one undecryptable row turns the whole reveal into a 500 (no partial result); acceptable, but log the key name only.

[SEC-3] severity: low
where: services.go (all private routes), server.go
problem: no per-user quota or rate limit on authenticated writes: unlimited services per project, unlimited env vars per service (each up to 32 KiB, so ~32 vars per MiB body but unbounded count), unlimited deploys (each creates a Deployment row, an outbox event, a Temporal workflow and a build). List endpoints return every row (no pagination). Only /auth/* is limited, which is what the spec requires today.
fix: Plan 3 hardening: cap env vars per service (e.g. 200), services per project, queued deployments per service (reject a deploy with 429/409 while one is queued or building), and a per-user limiter on POST /services/{id}/deploy.

[SEC-4] severity: low (carry-forward, still open)
where: internal/gitsrc `refRe` used by deployService
problem: backlog T22 SEC-7: `ValidRef` accepts 4-6 character hex-looking refs (`^[A-Za-z0-9][A-Za-z0-9._-]{3,99}$`), which the worker does not rev-parse-verify, so a tenant's own tag/branch named `abcd` could be checked out and pushed under that tag. Tenant-scoped only. STAFF-6 (tags and other branches fail on the single-branch clone) is also unaddressed in the API or docs.
fix: require >= 7 hex chars when the ref is all hex (or verify the prefix in the worker for 4+); document that commitSha must be a hex sha, the branch or HEAD.

[SEC-5] severity: low
where: internal/store/models.go `Service.RepoURL json:"repoUrl"`
problem: redaction is opt-in per handler (`publicService`). Today only create and list return a Service, so nothing leaks, but the Task 10 canvas handler and any later `GetProject`-with-services response will leak a token if the author forgets it. Test coverage names create, list and canvas, but canvas does not exist yet.
fix: make the leak structurally impossible: `Service.MarshalJSON` that redacts, with a separate internal struct for the worker, or a test that walks every route response for a seeded token. At minimum ensure Task 10 adds the canvas row of `TestRepoURLCredentialsMasked`.

[SEC-6] severity: low
where: services.go deleteService / store.SoftDeleteService
problem: soft delete keeps `EnvVar` ciphertext rows and the raw `repoUrl` (which may hold a token in userinfo or query) indefinitely. The ciphertext is protected by the key, but the repo token is plaintext at rest, and deleted services still count against retention expectations.
fix: have the teardown workflow (Task 12) delete the service's EnvVar rows and null or scrub `repoUrl` after the containers are gone.

## Informational
- Repo URLs may point at loopback, private or link-local hosts (git SSRF); already recorded as P1 T8 SEC note (Plan 3). Not new.
- Env keys such as `NODE_OPTIONS`, `PATH` or `LD_PRELOAD` are accepted; they only affect the tenant's own container, which runs with dropped caps and limits. No action.
- Deploy enqueues a deployment even when the previous one is still building (the workflow serialises per service); see SEC-3 for the queue cap.

RESULT: FINDINGS (0 critical, 0 high, 1 medium, 5 low)

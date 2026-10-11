# Plan 2 Task 8 verifier report (commits 058321e, 0eb270f, 962886b, ce69f7f)

RESULT: PASS

## Suite (control-plane-go/, HEAD ce69f7f, tree clean apart from known untracked files)
- `go build ./...` -> BUILD_OK
- `go vet ./...` -> VET_OK
- `golangci-lint run --allow-parallel-runners` -> `0 issues.`
- `gosec -quiet -exclude=G204 ./...` -> no output (no findings)
- `gitleaks git --redact` (repo root) -> `349 commits scanned ... no leaks found`
- `go test -race -count=1 -p 2 ./internal/api/ ./internal/store/` -> `ok internal/api 77.306s`, `ok internal/store 6.151s`
- `go test -race -count=1 -p 2 ./...` -> every package ok, no FAIL lines (last: `ok internal/workflows 9.266s`, `? migrations [no test files]`)

## Feature: real E2E (git archive HEAD copy in /tmp, real Postgres 16 via testcontainers, real TCP httptest server, users created through the store)
96 checks PASS, 0 FAIL (`FAILS: 0`). Highlights:
- nodejs create with `https://user:<token>@github.com/org/repo.git` -> response `"repoUrl":"https://****@github.com/org/repo.git"`; token absent from create and list responses.
- redis create without repo -> 200. List returns 3 services.
- Deploy nodejs: Deployment row status=queued, outbox type `build.requested`, payload `{"deploymentId": ...}`. Deploy redis: commitSha=latest, outbox `deploy.requested`. Explicit commitSha `abc1234` stored.
- Env: set returns `********`; list masked; `?reveal=true` returns `plain-secret-value`; DB column `enc:v1:87V3z4t+...` (ciphertext); delete removes the row.
- Delete service: 202, `deletedAt` set, exactly one `teardown.requested` event (`{"purgeData": false, "serviceId": ...}`), second delete 404, deploy afterwards 404, list excludes it, still one event. `?purgeData=true` gives `"purgeData": true`.
- User B vs A's three service ids (deploy, env get, env get reveal, env set, env delete, service delete): all `404 {"error":"service_not_found"}`, byte-identical to nonexistent id, `%00`, `%ff` and 65-char ids. Same for A with the invalid ids. B creating in A's project -> 404; B list of A's project -> `[]`; nothing deleted.
- 400s: `file:///etc/passwd`, `ext::sh -c id`, branch `--upload-pack=x`, nodejs without repo, bad type, empty name, malformed JSON, commitSha `-abc` and `--upload-pack=x`, env key with space, env value with newline, env missing value.
- 413 on oversize (2 MiB) bodies for create, env set, deploy. 401 without token on all 7 routes and with a garbage token.

## Dev environment (read-only)
- `go run ./cmd/migrate status`: 00001-00003 applied, `00004 00004_refresh_family.sql pending` (not applied, as expected).
- Node API: `/healthz` 200 `{"ok":true}`, `/readyz` `{"ok":true}`.
- `~/cp-logs/go-worker.log`: only periodic `flushed logs processed=0 errors=0` lines, last 02:01; zero ERROR lines.

## Observations (not failures)
- Event types are lowercase `build.requested` / `deploy.requested` / `teardown.requested` (store/events.go), not the Node-style upper case.
- Default deploy ref is the branch only when it satisfies `gitsrc.ValidRef` (4-100 chars); branches such as `dev`, `v1`, `feature/x` default to `HEAD`. The builder clones with `--branch <Branch>`, so HEAD is that branch's tip; behaviour is correct and covered by a unit test.
- Service names under 2 characters are rejected (400), same rule as project names.

## Failures
None. (Three initial E2E mismatches were errors in my check script: uppercase event names, a 3-char branch expectation, and a 1-char name; the corrected run is 96/96.)

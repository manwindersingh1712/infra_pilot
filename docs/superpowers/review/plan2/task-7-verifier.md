# Plan 2 Task 7 verifier (commit f14c798, project endpoints)

RESULT: PASS

Suite (from control-plane-go/, tree clean apart from known untracked files):
- go build ./... -> ok; go vet ./... -> ok
- golangci-lint run --allow-parallel-runners -> `0 issues.`
- gosec -quiet -exclude=G204 ./... -> no output (clean)
- gitleaks git --redact (repo root) -> `339 commits scanned ... no leaks found`
- go test -race -count=1 -p 2 -v ./internal/api/ -> `ok  infrapilot/internal/api  119.875s` (52 tests, 0 FAIL)
- go test -race -count=1 -p 2 ./... -> 18 packages `ok`, no FAIL, no startup timeouts; ends with `ok infrapilot/internal/workflows 7.499s`

Feature (git archive HEAD copy outside repo, real Postgres via testcontainers, real TCP httptest server, tokens redacted):
- register A, B -> 200 {"token":"<redacted>","refreshToken":"<redacted>","user":{...}}
- A create -> 200 {"id","name":"Shop","ownerUserId","createdAt":"2026-10-11T20:17:36.076Z"} (camelCase, ISO ms Z)
- A list -> 200, 1 item; B list -> 200 `[]` (not null)
- B GET A's id -> 404 {"error":"Project not found"}; B GET missing id -> 404 identical body
- A GET own -> 200
- no token list/create -> 401 {"error":"unauthorized"}
- name "x" / 101 chars / NUL / tab / number / empty body / malformed JSON -> 400 {"error":"invalid_request"}
- 100 multi-byte runes -> 200 (rune count, not bytes)
- 2 MiB body -> 413 {"error":"request_too_large"}

Timing (internal/api, -race -p 2): 119.9 s wall this run (brief expected ~80 s single; machine was also busy with first testcontainers start). 52 tests. Slowest:
TestRefreshConcurrentRequests 34.3 s, TestRegisterAndLogin 10.8 s, TestLoginUnknownEmailPaysForBcrypt 8.4 s, TestRegisterNameLengthCap 7.8 s, TestRegisterPasswordLengthLimits 7.4 s, TestRefreshReuseRevokesTheFamilyOnly 4.9 s, TestLoginHasNoPasswordLengthCheck 4.6 s, TestEmailLengthCap 3.9 s.
TestProjects takes 0.09 s. The suite time is dominated by bcrypt under -race in auth tests (and one concurrency test at 34 s), not by Task 7; project tests add negligible time. Growth risk is any new test that registers users via HTTP (bcrypt); use hs.st.CreateUser + hs.token as TestProjects does.

Regression: no plan checklist items for earlier tasks beyond the full suite; all pass.
Failures: none.

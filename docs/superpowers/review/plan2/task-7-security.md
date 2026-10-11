# Plan 2 Task 7 security review (commit f14c798, project endpoints)

Tools (repo root / control-plane-go):
- gitleaks git --redact: 339 commits, no leaks.
- govulncheck: 0 affecting vulnerabilities (1 in a required module, not called).
- gosec: 2 issues, both pre-existing in internal/runtime/exec.go (G204 on the command runner, argument-list exec by design); none in internal/api or the commit.
- go test -p 2 ./internal/api/: ok.

Verified (a throwaway test in a detached worktree, since removed):
- All 3 routes wrapped in s.private; 401 without bearer; TestProjects covers it.
- Ownership: GetProjectForOwner uses `WHERE id=$1 AND ownerUserId=$2`; foreign and missing ids share one code path, identical 404 body `{"error":"Project not found"}` and identical single-query timing. List is owner-scoped.
- Mass assignment: body struct only has Name; client `ownerUserId`/`id` ignored (tested live: owner = token user).
- Response: Project model has exactly id, name, ownerUserId, createdAt; no internal fields.
- SQL: all bound parameters, constant column list.
- Name validation: 2-100 runes, valid UTF-8, no control chars (NUL rejected). 1 MiB body cap, trailing data rejected.
- Backlog carry-forwards for Task 7: STAFF-5 (nolint:unused on harness.token) is removed in this commit. Other Task 7-addressed notes (ListServices, env masking, etc.) concern service routes (Task 8+), not applicable here.

## Findings

[SEC-1] severity: low
where: control-plane-go/internal/api/projects.go:getProject (r.PathValue("id") passed unchecked to the store)
problem: A path id containing a NUL or invalid UTF-8 (`GET /projects/%00`, `/projects/%ff`) makes Postgres reject the text parameter, so the handler returns 500 `internal_error` (and logs an error) instead of the same 404 as any other unknown id. Not an IDOR or information leak (a foreign id still gives 404), but it breaks the "identical 404 for non-matching ids" contract, creates log noise/alert triggers an unauthenticated-ish caller (any registered user) can generate at will, and the same pattern will repeat in every `{id}` route in Tasks 8-12.
evidence: live run: `ID %00 500 {"error":"internal_error"}`, `ID %ff 500 {"error":"internal_error"}`, `ID nope 404 {"error":"Project not found"}`.
fix: add a shared helper (e.g. `validID(id)`: len 1..64, `^[A-Za-z0-9_-]+$`) used by every path-id handler, returning the same 404 on mismatch; add a test row for `%00`/`%ff`. Do this once now, so Tasks 8+ inherit it.

[SEC-2] severity: low
where: control-plane-go/internal/api/projects.go:createProject
problem: A validly signed token whose user no longer exists (deleted user, or a token minted for a nonexistent id) gives an FK violation, surfaced as 500 `internal_error` rather than 401. Tokens are only signed for real users today, so impact is limited to a deleted-user window until token expiry.
evidence: live run: `GHOST 500 {"error":"internal_error"}` for POST /projects with a token for user "ghostuser".
fix: map the FK violation (pgconn code 23503) to 401 `unauthorized`, or have `private` confirm the user exists (also helps the account-deletion story). Backlog item acceptable.

[SEC-3] severity: info
where: createProject / validProjectName
problem: Names may contain format/bidi characters (zero-width space, U+202E RLO) and HTML like `<script>` since only `unicode.IsControl` (Cc) is rejected. Output is JSON only (`application/json`, nosniff headers), so no injection here; the UI renders via React (escaped). Risk is limited to spoofed display names in the UI/logs.
evidence: live run: name `a​‮b<script>` accepted with 200.
fix: optional: also reject `unicode.Is(unicode.Cf, r)` (and Zl/Zp) in names, and trim/normalise; keep `<`/`>` allowed.

RESULT: FINDINGS (0 critical, 0 high, 0 medium, 2 low, 1 info)

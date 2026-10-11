# Plan 2 Task 7 (f14c798, project endpoints): Staff review

Gates (control-plane-go/): `go build ./...`, `go vet ./...`, `golangci-lint run` (0 issues). `go test -p 2 ./internal/api -run TestProjects` passes.

## Spec and shape
- Routes, status codes, `{"error":"Project not found"}`, 404 for foreign and missing ids all match the plan. All three routes go through `s.private`.
- Ownership is enforced in SQL (`WHERE id=$1 AND ownerUserId=$2`, list filtered by owner); owner comes from the token, not the body.
- JSON shape matches Node (`prisma.project.create/findMany/findFirst`, no `select`): `{id,name,ownerUserId,createdAt}`, camelCase, ISO ms `Z` timestamps via `store.Timestamp`, `[]` for empty (`collect` initialises `[]T{}`), newest first like Node.
- cp-ui (ProjectsPage, ProjectCanvasPage) reads `id`, `name`, `createdAt`, `_count?.services` (optional, defaults to 0; Node never returned it either, so no regression) and `GET /projects/:id` `.name`. Compatible.
- Test written with the code (no red step): acceptable here because the mutation run below shows it has teeth; the one gap is listed as STAFF-1.

## Mutation results (throwaway git-archive copy outside the repo)
| Mutation | Caught? |
|---|---|
| Owner check dropped in GetProjectForOwner (foreign id gives 200) | caught (projects_test.go:71) |
| List returns all users' projects | caught (:61, other user sees non-empty list) |
| 1-rune name allowed | caught ("short" row) |
| NUL byte allowed (control check disabled) | caught ("control" row, 500) |
| ownerUserId accepted from body | **SURVIVES** (STAFF-1) |
| 403 instead of 404 for foreign id | caught |
| No auth on POST / on list / on get (each separately) | all three caught (401 asserts for each route) |

## Name validation (2-100 runes, UTF-8, no control chars)
Judgement: justified. The NUL case is real (Postgres rejects 0x00 in text, so the plan's code gives a 500 not a 400) and the test pins it. The 100-rune cap and the other control chars are extra scope vs the plan and vs Node (which accepted any length >= 2), but cheap, bounded and sensible; the 1 MiB body cap was the only previous limit. Note the comment is slightly off: `utf8.ValidString` is effectively dead because `encoding/json` replaces invalid bytes with U+FFFD before the handler sees them (harmless).

## Findings

[STAFF-1] severity: low
where: control-plane-go/internal/api/projects_test.go
problem: Test does not pin that `ownerUserId` in the request body is ignored, and does not pin the name boundaries.
evidence: Mutation "take ownerUserId from body" passes the suite. Also nothing asserts that exactly 100 runes and exactly 2 runes are accepted, or that length is counted in runes (e.g. 100 x "é" accepted, 101 rejected); a switch to `len()` bytes would pass (only ASCII is used).
fix: Add a row POST {"name":"Shop","ownerUserId":<other.ID>} and assert the response ownerUserId == caller; add accept rows for 2 runes, 100 ASCII, and 100 multi-byte runes.

[STAFF-2] severity: low
where: control-plane-go/internal/api/projects.go:15-25
problem: Validation is only partly consistent: `unicode.IsControl` (Cc) rejects tab/newline/C1 but lets through whitespace-only names ("  "), U+2028/2029, zero-width and bidi-override format characters (Cf). The doc comment says "no control characters", which is accurate, but the stated goal (sane display names) is not fully met.
evidence: Reading the code; same behaviour as Node for whitespace-only (UI only checks `.trim()` non-empty client-side).
fix: Optional. Either leave as is (document that only Cc is rejected) or also reject names whose `strings.TrimSpace` is shorter than 2 runes and `unicode.IsGraphic` false runes. Not blocking.

[STAFF-3] severity: low
where: behavior note (Product/CTO awareness)
problem: Existing Node-era projects with names >100 runes or containing tab/newline remain readable but the user could not recreate that name; there is no rename endpoint, so no impact today. The UI shows the raw `400: {"error":"invalid_request"}` text for too-long names (no maxLength on the input) - same pattern as the existing 2-char error.
fix: Optional: add `maxLength={100}` to the cp-ui input at cutover; nothing for the backend.

No critical/high/medium findings. Error paths all go through `internalError` with request id; contexts are passed through; no SQL outside `store`.

RESULT: APPROVE (0 critical, 0 high, 0 medium, 3 low)

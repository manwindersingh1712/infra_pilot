# Task 7 (project endpoints, f14c798) - Product review

Scope: control-plane-go/internal/api/projects.go vs control-plane/apps/api/src/controllers (project controller, read only) vs cp-ui/src/pages/ProjectsPage.tsx and ProjectCanvasPage.tsx. Static review; UI not run (no UI change in this commit).

## UI contract: holds
- Shape: Project = {id, name, ownerUserId, createdAt}; same fields Prisma returned. UI reads only id, name, createdAt, and `_count?.services ?? 0`. Node list had no `_count` either (plain findMany), so the "0 services" card text is the same before and after (pre-existing UI quirk, not a regression).
- Status: create returns 200 (Node 200), list 200, get 200, other owner / missing 404 `{"error":"Project not found"}` (same string as Node). Order createdAt DESC as Node. ProjectCanvasPage only reads `.name` from GET /projects/:id.
- Empty list is `[]` (store test pins it); UI `projects.length === 0` empty state works. A `null` would crash `.map`/`.length`, so this matters and is covered.
- Existing projects: validation is only in createProject; list/get do no validation, so legacy names (1 char, >100, control chars) still load. Verified in code. Not covered by a test (see PROD-3).

## Findings

[PROD-1] severity: medium
where: POST /projects 400 -> ProjectsPage.createProject (cp-ui/src/pages/ProjectsPage.tsx:65-80, 361-375)
problem: Every name failure returns the same `{"error":"invalid_request"}`, and the UI prints it raw as `400: {"error":"invalid_request"}` in a banner on the page behind the modal. The UI only blocks empty/whitespace names, so a one-letter name ("A"), a 101+ char name, or a pasted tab/newline hits this. Node accepted all of these. The user gets no hint about the rule (2-100 chars). The banner also sits behind the open modal overlay (modal stays open on failure), so the user may see nothing change.
evidence: type "A", press Create: modal stays open, banner "400: {"error":"invalid_request"}".
fix: Task 15 (UI): add `minLength`/`maxLength={100}` on the input, a hint "2-100 characters", disable Create until trimmed length >= 2, and show the error inside the modal; map `invalid_request` to "Project name must be 2-100 characters with no control characters". Optionally the API could return a specific code (`invalid_project_name`) so the UI need not guess; if so update the plan's error table and tests. Not blocking for Task 7 since the generic code matches the rest of the API.

[PROD-2] severity: low
where: validProjectName
problem: The 100-rune cap and min 2 are a behavior change versus Node (no limits). Acceptable for solo developers (names are labels), and 100 is generous. Surrounding whitespace is not trimmed (Node did not either), so " a" passes as 2 runes and displays as "a"; the UI sends the untrimmed value. Same-name duplicates still allowed (as Node).
fix: UI should send `newProjectName.trim()` (Task 15) and the length hint should count the trimmed value. Server-side trimming is optional; do not add it without a plan note since it changes stored data.

[PROD-3] severity: low
where: projects_test.go
problem: No test pins "legacy names still load" (list/get of a project whose stored name is 1 char, 101 chars or contains a control character, inserted directly via SQL). The behavior is correct today but a future "validate on read" change would silently break existing users.
fix: add a store/API test inserting such a row and asserting GET /projects and GET /projects/{id} return 200 with the name unchanged.

[PROD-4] severity: low (informational)
where: error strings generally
problem: Project-not-found keeps the human string "Project not found" while every other Go error is a snake_case code (`invalid_request`). The UI must keep handling both styles; Task 15 should centralise an error-code -> message map rather than show raw JSON.

## Task 15 UI items (from this task)
1. ProjectsPage create dialog: input maxLength=100, helper text "2-100 characters", disable Create when trimmed length < 2, send trimmed name.
2. Show the create error inside the modal, in plain words, instead of `400: {"json"}` on the page behind it; map invalid_request (400), request_too_large (413), 401 (re-login) and 5xx ("Something went wrong, try again").
3. Optional: `_count.services` is never sent by Node or Go, so project cards always say "0 services". Either drop the text or have the list endpoint return a service count (separate decision, not a Plan 2 regression).

## Release-note lines
- Project names must now be 2-100 characters and cannot contain control characters (tabs, line breaks). Existing projects keep their names, whatever they are.
- Projects you do not own return "Project not found" (unchanged wording).

## Cutover checklist
Add a row: create a project with 1 character, 101 characters and a name with an emoji; confirm the first two show a readable message and the third works; confirm all pre-existing projects still list and open.

RESULT: APPROVE (no blocking findings; PROD-1 to be done in Task 15, PROD-3 test is a cheap add)

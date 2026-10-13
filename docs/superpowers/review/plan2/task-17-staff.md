# Plan 2 Task 17: Staff review

Commits: abb5017 (helpers + checks), effa11b (Connection tab), 5b0306d (delete dialog, Toast info tone, canvas wiring). Reviewed against Task 17 + AMENDMENTS 1-9 in `plans/2026-10-07-go-plan2-api.md`, `task-12-product.md` ("Task 17 UI items", PROD-2/3/4/5) and `task-9-product.md`.

## Commands run (df first: 14 GiB free, 93% used)

- `npm run lint`: 0 errors, 5 `react-hooks/exhaustive-deps` warnings (CreateServiceModal:171, ServiceDetailsPanel:61/485/502, ProjectCanvasPage:69). All are the existing "effect calls an inline function" pattern; none is in the new dialog.
- `npm run build` (tsc -b + vite): pass.
- `npm run check:logic`: `ok: 57 checks passed`.
- Working tree after all mutations: `git diff -- cp-ui/src cp-ui/scripts` is empty (only the pre-existing `.DS_Store` change).

## Mutation check of serviceActions.ts (all reverted)

Each mutation was applied with sed to `cp-ui/src/lib/serviceActions.ts`, `check-api-logic.ts` was run, then `git checkout` restored the file. Every one made the script throw an AssertionError:

| # | Mutation | Result |
|---|---|---|
| 1 | drop `|| d.stoppedAt` in pickConnectionDeployment | FAIL (check at line 609) |
| 2 | remove `encodeURIComponent(password)` | FAIL |
| 3 | `cancelDelete` closes even while pending | FAIL |
| 4 | remove the `phase !== "confirm"` guard in beginDelete | FAIL (line 663) |
| 5 | remove the "managed DB needs a choice" guard | FAIL |
| 6 | 404 no longer classified as done | FAIL |
| 7 | stateless service honours `purge` | FAIL |
| 8 | connectedServiceNames ignores one direction | FAIL |
| 9 | Local URL row always present | FAIL |
| 10 | pick the oldest instead of newest deployment | FAIL |
| 11 | failDelete ignores phase | FAIL |
| 12 | deletePath always sends `false` | FAIL |

Final run after reverting: 57 checks pass. The checks are specific, not smoke tests. Limitation: `check()` stops the script at the first failure, so a mutation only shows the first broken check (acceptable).

## Amendments

1. Step 1 not redone: OK. No change to EnvVarSection in these commits.
2. Connection tab: OK. `pickConnectionDeployment` (deployed and not stopped, newest by createdAt, order independent), `stoppedAt` is in the UI type and the Go API returns it (`deploymentCols` includes `"stoppedAt"`). Host `svc-<id>`, password via `?reveal=true` with `cancelled` flag, cleared on cleanup and unmount, `humanizeApiError`, percent-encoded URL, Local URL only if `hostPort` truthy, copy buttons disabled while the password is unknown. Password is not logged and not in the page URL. The copy timer is cleared on unmount.
3. Dialog: OK, with the gap in STAFF-1. In-app dialog, inline styles, names the service, no default for mongodb/redis, the two button labels match the amendment verbatim, explanation of the keep option present, single "Delete service" for other types, `purgeData` sent as literal `true`/`false`, `purge_choice_required` mapped, `invalid_purge_data` via `humanizeApiError`, 404 is success, 401 handled by the shared client.
4. After 202: OK. Panel closes (so the LogViewer unmounts and its stream stops), node and edges removed optimistically, `loadCanvasData(true)` background refresh (no loader, Task 15 lesson honoured), info toast `DELETING_NOTICE` ("Deleting… the service stops within a minute (a running build can take up to 3 minutes).") with a check that it never says "Deleted". Error stays in the dialog (`role="alert"`), buttons disabled while pending.
5. Stale panel/poll/stream: OK. `onClose` makes `isOpen` false, the 2 s interval effect returns early and clears; `shownServiceId` drops late responses.
6. PROD-7 backend: Engineer's claim verified, no Go change needed (details below).
7. Pure logic in `cp-ui/src/lib/serviceActions.ts`, checks in `scripts/check-api-logic.ts`, mutation-checked above, no new dependency: OK.
8. Browser proof (CDP, seeded rows, double-click, blocked DELETE): not part of these three commits and I did not verify it. `review/plan2/task-17-product.md` and `task-17-security.md` exist; the gate owner should confirm the proof is recorded there.
9. Step 4 manual checks listed for Task 18: no doc change in these commits. Make sure Task 18's checklist gets `docker ps` and the Temporal UI `teardown-<id>` checks.

## Engineer claims

- "Delete only on the Deployments tab": true (button sits inside the `activeTab === "deployments"` branch, line ~841). The dialog is rendered outside the tab branch, so switching tabs cannot unmount it. Acceptable under amendment 3 ("reachable from the panel"). See STAFF-5 for the ergonomic point.
- "No Go change, PROD-7 already handled": CONFIRMED.
  - `store.ownedByCaller` (`internal/store/deployments.go:276`) requires `s."deletedAt" IS NULL`; both `ListDeploymentsForOwner` (GET /deployments) and `GetDeploymentForOwner` use it.
  - Logs and the stream ticket go through `api.deploymentOwned` -> `GetDeploymentForOwner` (`internal/api/logs.go:101`, used by `getLogs` and `streamTicket`), which answers the same 404 for soft-deleted.
  - `GetJob`, `CreateDeployment`, services, canvas and connections queries all filter `deletedAt IS NULL`.
  - Tests exist: `TestListDeploymentsHidesSoftDeletedServices`, `ticket_test.go` ("soft-deleted" row), `logs_test.go` (soft-deleted row), `TestCreateDeploymentRejectsSoftDeletedService`, `TestClaimDeployAndGetJobIgnoreSoftDeletedService`.
  - Residual: `stream.go` does not re-check the service when a ticket is redeemed, so a ticket issued less than 60 s before the delete could still open a stream. Single use, bound to the user, no data beyond what they could already read. Info only.
- "Missing type-the-name confirmation": true, see STAFF-1.

## Findings

[STAFF-1] severity: medium
where: cp-ui/src/components/DeleteServiceDialog.tsx (managed branch)
problem: `task-12-product.md` Task 17 UI item 3 and PROD-4 say "Type-the-name confirmation for the purge choice" / "For databases require typing the service name". It is not implemented. AMENDMENT 3 (the orchestrator's summary) dropped it without a recorded ruling. "Delete database and its data" is irreversible and sits one click away, directly above the keep button, at the same size.
evidence: the dialog has no input; `beginDelete` has no name parameter; purge is one click.
fix: either add a confirm input that must equal `service.name` before "Delete database and its data" is enabled (pure helper `canPurge(name, typed)` plus a check, keep the keep-button free of it), or record a CTO/orchestrator ruling in `review/decisions.md` that two explicit labelled buttons are enough and move the item to the backlog. Do not leave it silently dropped.

[STAFF-2] severity: low
where: DeleteServiceDialog.tsx `submit`, ServiceDetailsPanel.tsx lines ~473-480 and ~1046-1056
problem: if the dialog unmounts while the DELETE is in flight (the reset effect `setDeleteOpen(false)` on service change, or the panel/page going away), the request still completes and `onDeleted()` runs the panel's stale closure: it calls the page's `onClose`, which closes whatever panel is open now, and a failure is dropped silently (`mounted.current` is false and no one is told). The page's `onDeleted` uses `selectedService?.id` from the click-time render, so the optimistic removal is correct, but `onClose` is not. In practice the full-screen overlay (z-index 150) blocks selecting another node, so this needs a programmatic selection change; it is a latent bug, not a reachable one today.
evidence: `submit` calls `onDeleted()` regardless of `mounted.current`; `onDeleted` in the panel calls `onClose()` unconditionally.
fix: in the panel's `onDeleted`, call `onClose()` only if `shownServiceId.current === service.id`; when the dialog is unmounted and the delete fails, report through a callback (`onFailed(message)` -> page toast). Add a short comment that the unmounted success path is intentional (a delete the user confirmed should still update the canvas).

[STAFF-3] severity: low
where: DeleteServiceDialog.tsx (focus handling)
problem: focus moves to Cancel on mount (good), but there is no focus trap (Tab reaches the panel and canvas behind the overlay), focus is not restored to the "Delete service" button on cancel, and Cancel is `disabled` while pending, which drops focus to `<body>` (keyboard users lose their place; Escape still works because the listener is on `document`). No `aria-busy` on pending. Escape and overlay click correctly do nothing while pending (they read `stateRef`, which is updated synchronously, so the double-submit/cancel race is closed).
fix: keep focus on the dialog container (`tabIndex={-1}`) when pending, trap Tab inside the dialog, and restore focus to the opener on close. Acceptable to defer to the a11y backlog.

[STAFF-4] severity: low
where: ProjectCanvasPage.tsx lines ~648-662 and the toast render
problem: (a) `loadCanvasData(true)` has no sequence guard: a refresh started earlier (for example after creating a service) that returns after the delete could re-add the deleted node until the delete's own refresh lands. Unlikely since soft delete is committed before the 202 and the later request wins in order, but concurrent responses can reorder. (b) `{toast ? <Toast error/> : notice && <Toast info/>}`: if the background refresh fails, the error toast replaces the "Deleting…" notice and the notice is never shown again, so the user sees "Could not refresh the canvas" with no mention that the delete was accepted.
fix: (a) ignore a refresh response if a newer refresh started (counter ref); (b) show both, or append the delete notice to the refresh-failure message.

[STAFF-5] severity: low
where: ServiceDetailsPanel.tsx ~841
problem: the Delete button exists only on the Deployments tab. A user looking at the Connection tab of a database (the screen where they decide to remove it) must switch tabs. Matches the Engineer's report and amendment 3, so not a defect; noting it for the Task 18 walk-through. Also the button sits directly under "Deploy Latest" with only a 20 px margin and a low-contrast red tint; fine, since the dialog is the guard.

[STAFF-6] severity: low
where: ServiceDetailsPanel.tsx ConnectionSection
problem: when the reveal request fails, `password` stays `null`, so the Password row shows "Loading…" forever next to the error line ("Could not load the password. …") and the URL rows show a URL built with an empty password (copy is disabled, good). Misleading but harmless.
fix: map error to a "Not available" state, or add a Retry; low priority.

[STAFF-7] severity: info
where: tests
note: the dialog's guards (pending, cancel, double click, 404, error stays open) are covered only through the pure state machine; the React wiring (`stateRef`, `mounted`, key, effects) has no automated test and the project has no test framework (amendment 7 forbids adding one). That makes amendment 8's browser proof the only evidence for the wiring, so it must be recorded at the Task 17 gate. Inline-styles-only rule: respected (no className, no CSS file touched). Toast `tone` is backward compatible (default error), `role="status"` for info, `role="alert"` for errors; z-index 200 is above the dialog (150) and panel (101).

## Panel details checked

- Stale responses: `loadDeployments` captures `requestedFor` and checks `shownServiceId.current` after the await and in the catch; `shownServiceId` is set from `isOpen ? service?.id : undefined` each render, so closing or switching drops late replies. The `finally` still clears `loading` (a dropped reply for A can clear B's spinner; cosmetic).
- Effect cleanups: poll interval cleared on change; ConnectionSection reveal effect sets `cancelled` and clears `password`/`error`; copy timer cleared on unmount; Escape listener removed on unmount.
- Keys: `key={service.id}` on the dialog, EnvVarSection and ConnectionSection forces a remount when the service changes, so no password or dialog state survives a switch. `LogViewer key={latestDeploymentId}` unchanged.
- Hooks order: `deleteOpen` state is declared before the `if (!isOpen || !service) return null` early return.

RESULT: CHANGES REQUESTED (0 critical, 0 high, 1 medium, 5 low, 1 info)

# Task 17 Product review (delete dialog, Connection tab, canvas after delete)

Commits: abb5017, effa11b, 5b0306d. Reviewed from code only (DeleteServiceDialog.tsx, serviceActions.ts, ServiceDetailsPanel.tsx, ProjectCanvasPage.tsx, Toast.tsx, errors.ts); no headless run was needed or done (no throwaway stack started, nothing to clean up).

## Checklist from earlier reviews

Task 12 "Task 17 UI items":
1. Reachable from panel, in-app dialog (not window.confirm): MET (Deployments tab only, see ruling b).
2. Dialog text: MET, verbatim ("Stops and removes the container and its URL. Removes env vars and connections. This cannot be undone. Services that use this one are not updated.").
3. mongodb/redis explicit choice, no default: MET (two buttons, Cancel is the focused element, no preselection). Type-the-name for purge: NOT MET, consciously skipped, see ruling a. Wording matches PROD-2 ("Delete database and its data" / "Delete service, keep data volume on disk", password deleted, cannot be restored). Legacy-data line (PROD-10) included for purge.
4. Connected services shown: MET (count plus names, amber, "may break").
5. After 202: node and edges removed at once, background refetch, info toast, 404 treated as success, other errors stay in the dialog via humanizeApiError (purge_choice_required, invalid_purge_data, session, network all have readable strings): MET. Toast says "Deleting… the service stops within a minute (a running build can take up to 3 minutes)." and never "Deleted": MET (PROD-3). Note: the toast text is the PROD-3 wording, slightly different from item 5's, which is fine.
6. Buttons disabled while pending, plus a synchronous ref guard against double submit: MET. Esc/backdrop/Cancel are ignored while pending (cannot dismiss mid-request): good.
7. No restore UI, no deleted list: MET.
PROD-1 (failed teardown invisible): still open by design, an operator runbook item, not a UI item. Confirm it is in the cutover checklist.
PROD-2 (managed default): MET in the UI (no default). PROD-4 (irreversible text) MET except type-the-name. PROD-5 MET. PROD-6 backend (Task 14). PROD-10 MET.

Task 9 items: PROD-2 (select deployed and not stopped): MET (pickConnectionDeployment, with unit checks). PROD-4 (close panel and refresh after delete): MET (onDeleted closes panel, drops the node, refetches). PROD-1 ("Stopped" rows in the history) still deferred, low. Late responses for a deleted/changed service are dropped (shownServiceId ref), so a stale panel cannot show "No deployments yet."

## Findings

[PROD-1] severity: medium
where: Connection tab, ServiceDetailsPanel.tsx ConnectionSection / serviceActions.ts buildConnectionRows
problem: The generated password is shown in clear text as soon as the tab opens, with no mask and no Reveal toggle, while the Env Vars tab masks values (Task 9). Anyone looking over a shoulder or sharing a screen sees the DB secret; it also sits in the DOM. It is fetched via ?reveal=true and cleared on unmount, which is good, but the screen itself is open.
fix: Show dots by default with a Reveal/Hide toggle; keep Copy working on the real value. Acceptable to ship as is for a hobbyist tool if release-noted; recommended before wider use. The URL row also embeds the password in clear (same toggle should mask it).

[PROD-2] severity: low
where: Connection tab "Not running." state
problem: The state appears for every case with no live deployment: never deployed, still building/deploying, failed, or stopped. The secondary line only says "Deploy the service to see connection details", which is wrong while a deploy is in progress.
fix: Optional: "Not running yet. Deploy the service, or wait for the current deployment to finish." (one string, no logic).

[PROD-3] severity: low
where: Connection tab rows
problem: The old CLI row (redis-cli -h ... -p ...) is gone. Minor regression of a copy-paste convenience; the Host `svc-<id>` is not a name a human recognises and is only resolvable from inside the Docker network, which the explanation sentence covers ("Services in this project reach this database at the host below"). The Local URL appears only when a host port is published (development), and the text says so; clear enough.
fix: Optional: add a CLI row for redis (uses the Local URL when present). Not blocking.

[PROD-4] severity: low
where: DeleteServiceDialog.tsx pending state for managed DBs
problem: While pending, the clicked button does not change; a plain "Deleting…" appears below the Cancel button, easy to miss. Both choice buttons dim, so it is understandable but quiet.
fix: Optional: show "Deleting…" in the dialog body above the buttons.

[PROD-5] severity: low
where: Delete reachability, see ruling b.

[PROD-6] severity: low (note)
where: a stale error toast
problem: If an error toast is up, the "Deleting…" notice is suppressed (the error toast takes the slot). The node still disappears, so the user sees no confirmation until the error toast times out (6 s). Rare. No action.

Canvas after delete (code reading): node and its edges vanish at once; the panel closes (ServiceDetailsPanel returns null when closed, so LogViewer unmounts and its stream/polling stops); the background refetch uses the soft-delete filter so the node does not come back; a 404 on delete also counts as success. Nothing found that leaves a ghost node or a running log viewer.

## Rulings (ready for the user)

(a) Type-the-service-name for the purge choice: RECOMMEND ACCEPT THE SKIP (do not block release). The two buttons are explicit, labelled with consequence text, red-styled, nothing is preselected and focus starts on Cancel, so it already needs a deliberate click on a button whose label says "and its data". Typing a name adds friction for hobbyists and the "keep" option is not much safer in practice (password deleted). If you want one cheap extra safeguard, make the purge button a two-step ("Delete database and its data" turns into "Click again to erase the data permanently") rather than a text field. Log it as a post-cutover nicety.

(b) Delete only on the Deployments tab: RECOMMEND ACCEPT FOR RELEASE, add to the follow-up list. Managed databases open on Deployments (their only other tab is Connection), and app services land there first; after pressing Deploy Latest the panel jumps to Logs, so one tab click returns to Delete. Not hidden, not dangerous. If it is cheap later, put a small "Delete" in the panel header so it is reachable from Logs/Env/Connection; the dialog is self-contained and can be reused unchanged.

## Release-note lines
- Services can now be deleted from the service panel (Deployments tab). Deletion is permanent: containers, URL, env vars and connections are removed; there is no undo or restore.
- The service disappears at once; it stops within about a minute (up to 3 minutes if a build is running).
- Databases (MongoDB, Redis): you choose between deleting the data or keeping the data volume on the server disk. Kept data is not reachable from Infra Pilot and its password is deleted; remove it with `docker volume rm cp-vol-<serviceId>`.
- Services connected to the deleted one are listed in the dialog and are not updated; they may fail to connect.
- The Connection tab shows the real host, generated password, and URLs (internal URL always; a localhost URL only in development). Passwords are shown in clear text (until masking is added).
- Registry images of deleted services are not cleaned up yet; a re-created service with the same name gets a new id and URL.
- Cutover checklist: operator runbook for failed `teardown-<id>` workflows (Task 12 PROD-1) must exist before the Delete button is enabled for users.

RESULT: APPROVE (no critical or high findings; PROD-1 medium recommended as a quick follow-up)

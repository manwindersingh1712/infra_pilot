# Task 17 security review (commits abb5017, effa11b, 5b0306d)

Scope: cp-ui connection tab, delete dialog, `lib/serviceActions.ts`; Go side read from `control-plane-go/internal/api/services.go`. No Go code changed in these commits, so govulncheck and gosec were not re-run. Disk checked first (14 GiB free).

## Tool output
- gitleaks (`--log-opts="abb5017^..5b0306d"`): 3 commits scanned, no leaks found.
- `cd cp-ui && npm audit --omit=dev`: 2 high, both from `react-router` / `react-router-dom` (7.0.0-pre.0 - 7.18.1). Advisories include GHSA-49rj-9fvp-4h2h (turbo-stream RCE, framework/SSR), GHSA-84g9-w2xq-vcv6 (CSRF on document requests), GHSA-wrjc-x8rr-h8h6 and GHSA-2j2x-hqr9-3h42 (open redirects), several RSC/SSR ones. `package.json` and the lockfile are unchanged by these commits, so nothing here is new. This SPA uses the library router only (no SSR, RSC or framework mode), so the RCE, RSC and CSRF advisories do not apply; the two open-redirect advisories could apply only if a user-controlled path reaches `<Link>`/`navigate`. `npm audit fix` is available.

## Findings

[SEC-1] severity: low
where: cp-ui/src/components/ServiceDetailsPanel.tsx:~437 (row render, `valueStyle` span) and cp-ui/src/lib/serviceActions.ts:68-77
problem: The generated DB password, and the URL and Local URL rows that embed it, are shown in plain text as soon as the tab opens. There is no masked default and no reveal click. This is the only place a revealed secret is auto-displayed, whereas the env section reveals per click. Risk is shoulder-surfing and screen sharing only.
evidence: `buildConnectionRows` puts `pw` straight into `value`; the span renders `{row.value}`.
fix: Show dots for the password and URL rows with an eye toggle (state local to the tab, reset on unmount). Copy can still copy the real value. Not blocking.

[SEC-2] severity: low
where: ServiceDetailsPanel.tsx copyToClipboard (diff line ~"void navigator.clipboard?.writeText(text).catch(() => undefined)")
problem: (a) "Copied!" is shown even when the write failed or `navigator.clipboard` is undefined (plain http on a non-localhost origin), so the user may paste an old clipboard value. (b) The clipboard is not cleared after the password is copied. Common behaviour, informational.
fix: Set `copiedField` only after the promise resolves; show "Copy failed" otherwise.

[SEC-3] severity: low
where: ServiceDetailsPanel.tsx ConnectionSection reveal effect; control-plane-go/internal/api/services.go:197-223
problem: The tab fetches `GET /services/:id/env?reveal=true`, which decrypts and returns every env var of the service, and keeps only one key. For managed DBs the set is small (the generated password), so the exposure is limited, but the response is broader than needed. The Go side does log the reveal as an audit line with ids only (no keys or values), which is good.
fix: Optional. A dedicated `.../connection` endpoint or `?reveal=true&key=` would reduce exposure. Backlog, not a blocker.

[SEC-4] severity: low
where: cp-ui/src/lib/serviceActions.ts:57-73 (`buildConnectionRows`); ServiceDetailsPanel.tsx copy button `disabled` expression
problem: When the service has no stored password (`""`), the URL and Local URL rows are built as `redis://:@svc-<id>:6379` / `mongodb://admin:@...` and their Copy buttons stay enabled, so the user can copy a URL that cannot authenticate. Only the Password row is disabled. Functional, not exploitable.
fix: Disable URL rows when `password === ""` too, or omit the password part.

[SEC-5] severity: low (edge race)
where: cp-ui/src/components/ServiceDetailsPanel.tsx (`onDeleted` handler on `<DeleteServiceDialog key={service.id}>`, calls `onClose()`); cp-ui/src/components/DeleteServiceDialog.tsx:58-76
problem: If the service prop changed while a DELETE was in flight, the dialog unmounts (key change and `setDeleteOpen(false)` on service change) but `submit` still runs `onDeleted()` from its closure, which calls the panel's current `onClose()`. That would close the panel of the newly selected service. The modal backdrop (zIndex 150) makes selecting another node while pending hard to do, and the page's own removal uses the closure's service id, so no wrong service is removed. Deletion of the intended service is unaffected.
fix: In `submit`, skip `onDeleted()` side effects that act on the current panel when `!mounted.current`, or make the page `onDeleted` take the id and close the panel only if it still shows that id.

[SEC-6] severity: info
where: DeleteServiceDialog.tsx:68-71, serviceActions.ts `classifyDeleteError`
problem: Any 404 is treated as success, including a 404 from a wrong route or proxy. The node is removed optimistically and `loadCanvasData(true)` then restores it if the service still exists, so the effect is a brief wrong "Deleting…" notice. The Go API also returns 404 for foreign ids, so this reveals nothing extra.

## Checked and found fine
- Reveal fetch: only issued while the Connection tab is mounted and a live deployment exists. The effect cleanup sets `cancelled`, so a late response for a previous service or deployment is dropped, and it clears `password`/`error`. The tab is `key={service.id}`, and `ServiceDetailsPanel` returns null when closed, so unmount clears the state. No `console.*`, no `title`/`aria-label`/data attributes with the value, never in a URL (the request path only has `?reveal=true`, the password is only in the response body).
- Error text: `humanizeApiError` maps codes to fixed strings. Only server sentences containing a space pass through, and React escapes them. The raw `ApiError.message` is not rendered. The reveal error shows `Could not load the password. <fixed text>`.
- URL building: the password goes through `encodeURIComponent`, so `@ : / ? #` cannot change the URL structure. The host is `svc-<id>` and the port a constant; the id comes from the API (cuid, shape-checked server side by `pathID`/`idShape`). `deletePath` and the other service paths encode or use server ids. This is a displayed string, not a navigation target, so there is no injection sink.
- Delete request: `purgeData` is built as the literal `"true"`/`"false"`; `beginDelete` returns `send:false` for a managed DB with `choice === null`, so no code path sends `purgeData=true` without clicking "Delete database and its data". Non-managed types always send `false`. There is no default selection and Enter does not map to a destructive button (focus starts on Cancel; Escape and the backdrop only cancel when not pending).
- Double submit: `stateRef` is updated synchronously before the await, buttons are disabled while pending, and Cancel/Escape/backdrop are ignored while pending.
- CSRF: the API uses a bearer token from `localStorage`, not cookies, so cross-site forms cannot authenticate a DELETE. (The usual XSS-reads-localStorage caveat is pre-existing and unchanged.) 401 goes through the shared refresh path.
- Rendering: service names and connected names are React text nodes (no `dangerouslySetInnerHTML`).
- Go side, `services.go:313-340`: route is behind `ownedService` (`GetServiceForOwner`, 404 `service_not_found` for foreign, malformed and soft-deleted ids). `parsePurgeData` accepts only none/empty, `true`, `false`; a repeated or other value gives 400 `invalid_purge_data` before any write. A managed DB with `purgeData` unset gives 400 `purge_choice_required`. Soft delete and the outbox event commit in one transaction; a concurrent second delete gets 404 and writes no second event. The audit log for reveals carries only service, user and request ids.
- Optimistic removal: the node and its edges are filtered immediately, then a background refresh runs; the server hides deleted services, so a stale in-flight load can at worst flash the node briefly (info only).

RESULT: FINDINGS (0 critical, 0 high, 0 medium, 5 low, 1 info). Plus the pre-existing `react-router` npm audit highs (not introduced by Task 17; recommend `npm audit fix` in a separate dependency commit).

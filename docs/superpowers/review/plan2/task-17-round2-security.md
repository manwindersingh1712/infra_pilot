# Task 17 round 2 security review (commits 805d133, 2f04cdc, 1c25f76)

Scope: cp-ui `ServiceDetailsPanel.tsx`, `DeleteServiceDialog.tsx`, `Toast.tsx`, `ProjectCanvasPage.tsx`, `lib/serviceActions.ts`, against round-1 findings SEC-1..SEC-6 and the 2026-10-13 rulings. Disk checked first (14 GiB free). No code changed.

## Tool output
- gitleaks (`--log-opts="5b0306d..1c25f76"`): 4 commits scanned, no leaks found.
- `cd cp-ui && npm audit --omit=dev`: unchanged from round 1, 2 high (react-router / react-router-dom, 7.0.0-pre.0 - 7.14.1 range). package.json and lockfile untouched by these commits. SPA uses the library router only. Recommend `npm audit fix` in a separate dependency commit.
- Shell quoting fuzz (run under node 24 + `sh -c`): see SEC-R2-1 "checked".

## Round 1 findings: status
- SEC-1 (password shown in plain text): FIXED. Password, URL, Local URL and CLI rows are masked (`MASK`) until "Reveal secrets"; the real value lives only in the `copyValue` field (a JS closure value, never rendered). Verified: no `title`, `aria-label`, `data-*`, or `console.*` in ServiceDetailsPanel.tsx / DeleteServiceDialog.tsx / serviceActions.ts carry the value (grep clean; the only `title=` is the env section's "Show value" label). The masked CLI/URL strings are built from `MASK`, so the real password does not appear in them (test asserted: no leak across all hostile inputs). `revealed` resets on unmount and when the deployment/service changes (effect cleanup).
- SEC-2 (false "Copied!"): FIXED. `copyToClipboard` sets `Copied!` only on a resolved write; a missing `navigator.clipboard` or a rejection gives "Copy failed" (serviceActions.ts `copyButtonLabel`, ServiceDetailsPanel.tsx ~356-369). Clipboard is still not cleared after copy: informational, unchanged.
- SEC-3: deferred by ruling (keys= filter). Still open, backlog.
- SEC-4 (copyable URL that cannot authenticate): FIXED. `buildConnectionRows` returns no URL/Local URL/CLI row while loading, on failure, or when the stored password is "".
- SEC-5 (stale onDeleted closes wrong panel): FIXED. `onDeleted` passes the captured id; the page filters by that id, and the panel closes only if `shouldClosePanel(shownServiceId.current, id)`. `shownServiceId` is a ref updated each render, so it is current when the async DELETE returns. `onFailed` after unmount raises a toast with the captured service name. The dialog is `key={service.id}` and `deleteOpen` is reset on service change (panel :526).
- SEC-6: unchanged, info.

## New findings

[SEC-R2-1] severity: low
where: cp-ui/src/lib/serviceActions.ts:131-135 (`beginDelete`, armed branch), cp-ui/src/components/DeleteServiceDialog.tsx:199-206 (purge button)
problem: The two-step purge arms on the first click and sends on the next click with no minimum delay. A double-click (the most common accidental input), or a held Enter on the focused button (keydown auto-repeat fires click repeatedly), arms and sends `purgeData=true` within milliseconds, before the user can read "Click again to erase the data permanently". `stateRef` is updated synchronously, so the second click sees `armed: true`. The ruling's intent (a deliberate second action) is defeated for exactly the accidental case. Data loss is irreversible, hence worth fixing, but the user must have already clicked the purge button on purpose, so low.
evidence: `beginDelete(openDelete(),"redis","purge")` then immediately `beginDelete(armedState,"redis","purge")` returns `send:true, purgeData:true`; nothing time-gates it.
fix: Record `armedAt` and ignore clicks for ~500-700 ms after arming (or ignore `e.detail > 1` and `e.repeat`), keeping the 5 s revert. Add a check-api-logic case.

[SEC-R2-2] severity: low
where: cp-ui/src/lib/api.ts:115 (`fetch` without a timeout/AbortSignal), DeleteServiceDialog.tsx:90-97 and :60-72
problem: While a delete is pending, all buttons are disabled, Escape/backdrop are ignored and Tab is trapped on the dialog container. If the request never settles (dropped connection with no TCP reset, stalled proxy), the user cannot dismiss the modal and has to reload. Not a permanent trap (reload recovers; the server-side delete is idempotent: a repeat gets 404, which is treated as done), but a UX/availability issue that also sits in the destructive path.
fix: Add a client timeout (e.g. 30 s via AbortSignal.timeout) for this call so the dialog falls back to the error state with its Cancel button, or allow Escape after N seconds.

[SEC-R2-3] severity: info
where: cp-ui/src/lib/serviceActions.ts:70 (`encodeURIComponent(password)`)
problem: `encodeURIComponent` throws `URIError` on a lone surrogate. A password string containing one (not producible by the worker's generator, but possible if a user later sets it through the env API) would throw inside render of the Connection tab and blank the panel unless an error boundary exists. No security impact, no injection.
fix: Optional: catch and fall back to a "password has unsupported characters" row, or normalise with `toWellFormed()`.

[SEC-R2-4] severity: info
where: serviceActions.ts:112-121 (CLI row)
problem: The CLI row puts the password on a command line (`redis-cli -u 'redis://:pw@...'`). When pasted, it lands in shell history and is visible in `ps` to local users. Intended by PROD-3 and masked by default, and the Local URL is dev-only. Worth a one-line hint (for example use `REDISCLI_AUTH` / mongosh prompt) in the runbook, no code change required.

## Checked and found fine
- Shell quoting (`shellQuote`, serviceActions.ts:77-79). Fuzzed under node: passwords `a'b`, `'; touch X; '`, `$(touch X)`, backticks, embedded newline plus `; touch X`, unicode and emoji, backslash/quote mixes, `${IFS}x`, `-rf`, `!!`; each against redis and mongodb, and against hostile service ids (`x'; touch X #`, `$(touch X)`) as host. Each generated CLI string was executed through `sh -c` with the tool replaced by `printf '%s'`; the single argv received equalled the Local URL exactly in every case, the canary file was never created, and the masked CLI string never contained the encoded password. Reasoning agrees: inside single quotes only `'` is special and it is rewritten to `'\''`; `encodeURIComponent` already percent-encodes `$`, backtick, `"`, `\`, newline, space and non-ASCII, leaving only `A-Za-z0-9 - _ . ! ~ * ' ( )`. The tool prefix is a constant; the host/port are inside the quoted URL. (zsh history expansion of `!` is not active inside single quotes in a non-interactive paste; in an interactive bash `!!` inside single quotes is also literal.)
- DOM/console leak when masked: values rendered via `{row.value}` text only; `copyValue` is used only as the argument of the click handler. No `console.*` in the three files. The reveal error shows `Could not load the password. <humanized message>`, never the response body. The password is not placed in any URL (request path only has `?reveal=true`).
- Reveal retry loops: a failed reveal does not retry automatically. The effect re-runs only when `service.id`, `passwordKey`, `deploymentId` or `attempt` changes, and `attempt` only changes on a Retry click. The 2 s deployments poll runs only while something is queued/building/deploying and does not change `deploymentId` unless a new deployment becomes live, so there is no request storm. Multiple rapid Retry clicks each cancel the previous one through the `cancelled` flag; no amplification beyond user clicks. `deploymentsOf` ensures no reveal GET is driven by the previous service's rows on a switch.
- Purge keyboard paths: `purgeData=true` can only come from `beginDelete(armed state, managed type, "purge")`. Enter on Cancel (initial focus) only cancels; Escape/backdrop never submit; "keep" always sends false even when armed; non-managed types always send false; `deletePath` builds a literal `true`/`false`. Armed timer: effect keyed on `armed`, cleared on unmount and on any state change, and the timeout reads `stateRef` so it cannot revert a pending state. After an error `failDelete` returns `armed:false`, so the user must arm again. (Only the double-click/key-repeat gap above.)
- Focus trap: Tab listener is document-level, removed on unmount; Escape closes unless pending; Tab with nothing enabled focuses the dialog container, so focus never escapes to the page behind or gets lost. Focus restore uses `document.contains`. Not permanent apart from the hung-request case (SEC-R2-2). The toast dismiss button is unreachable by keyboard while the dialog is open, acceptable.
- Stale guards: `shouldClosePanel` compares the ref to the captured id; `loadSeq` in the page drops out-of-order canvas responses so a late load cannot resurrect a deleted node; `onFailed` uses the captured service name. The page `onDeleted` uses functional state updates, safe after the dialog is gone.
- Toast stacking: error and notice toasts are both rendered (stacked container, notice first). Text is a React text node (escaped). The error text is `deleteFailedNotice(name, humanizeApiError(...))`: server messages pass through only when they contain a space, and the service name is the owner's own data, so no cross-user injection. Long unbroken names could overflow the 360 px toast (cosmetic, `wordBreak` not set on Toast).

RESULT: FINDINGS (0 critical, 0 high, 0 medium, 2 low, 2 info). All round-1 low findings except deferred SEC-3 are fixed; no approval-blocking issue. The pre-existing react-router npm audit highs are unchanged.

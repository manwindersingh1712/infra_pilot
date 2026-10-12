# Plan 2 Task 16: Product review (commits 059b1e5, 1c0ae8c, ca4d781, f70715f)

Scope: the live-log panel as a user sees it. Method: read `cp-ui/src/hooks/useLogs.ts`, `components/LogViewer.tsx`, `lib/logStream.ts`, `lib/errors.ts` and the one mount (`ServiceDetailsPanel.tsx:964`, `key={latestDeploymentId}`). I did not run the UI: every state is derivable from the code, and the Engineer's screenshots cover the visuals. Not exercised by me: a real API restart, sleep/wake, 6 tabs, an over-8 KiB line (the Verifier owns those).

## Binding requirements (Task 11 PROD-1..10 and the backlog) against the code

| Req | Status | Evidence |
|---|---|---|
| PROD-1 retry forever, capped jittered backoff, reset on online/visibility, Retry button, amber Reconnecting, three states | MET | `backoffDelay` 1 s doubling to 30 s with jitter, no attempt limit (useLogs 86-90); `onOnline` / `onVisibility` call `restart()` which zeroes `attempts` (203-238); Retry button shown whenever `status !== "live"` (LogViewer 120); badges live green / reconnecting amber / disconnected red |
| PROD-2 merge history after the first, keep Clear, keep scroll | MET | only the first history replaces; later ones go through `mergeHistory` with `clearedUpTo` (163-171); LogViewer auto-scrolls only within 50 px of the bottom, so reading position survives |
| PROD-3 `end`: `max_duration` silent and immediate, `shutdown` 1-3 s, uncounted, no error | MET | `end` handler (183-200): badge stays Live, `schedule(0)`, `attempts` untouched. A `max_duration` end within 1 s of opening is treated as a failure and backs off (sensible loop guard) |
| PROD-4 ticket through `apiFetch` right before `EventSource`; typed error; 404 terminal; Retry-After on 429; "5 streams" hint after repeated failed opens; `source.close()` in `onerror` with a comment | MET | 104, `classifyTicketError`, 144 with the comment, `FAILED_OPENS_BEFORE_HINT = 3`. Wording issues below (PROD-T16-2) |
| PROD-5 half-open slots | MET (server side, decided 2026-10-12); UI also closes the stream after 5 min hidden and reconnects on return after 30 s away |
| PROD-6 jitter on reconnect | MET for the client; the shared per-IP bucket / `TRUSTED_PROXIES` decision stays a cutover item |
| PROD-7 truncation | CONSCIOUSLY DEFERRED (see PROD-T16-5) |
| PROD-8 history API changes | N/A, no UI consumer |
| PROD-9 humanised errors | MET: `humanizeApiError` has `too_many_streams`, `invalid_ticket`, `unavailable`, `internal_error`, 429, 503, 404 |
| PROD-10 same-ms duplicate collapse | accepted, unchanged |
| JSON.parse guarded, cleanup on unmount and deploymentId change, socket.io removed | MET (`parseJSON` try/catch, effect cleanup, `generation` guard against late tickets, no `socket` left in `src` apart from one stale comment) |
| Task 17 carry-over: viewer unmounts on service delete | Not in this task; the 404 terminal state is the safety net |

## Findings

[PROD-T16-1] severity: low
where: `LogViewer.tsx:57` badge label "Connecting…" (the Engineer's question)
problem: `everConnected` is false until the first `onopen`, so a panel that cannot open because the user is at the 5-stream cap is labelled "Connecting…" (amber) for as long as the cap lasts, with the "5 log streams open" notice under it. Both together are understandable, and the label is correct for the first seconds and for an unreachable server (the notice then says "Cannot reach the server"). After the cap hint appears it is slightly off: the app is waiting for a slot, not connecting.
fix: keep "Connecting…" as is. Optional polish: when `error` is set and `!everConnected`, show "Reconnecting…" instead, so the badge and the notice never disagree. Not blocking.

[PROD-T16-2] severity: medium
where: `errors.ts:59` (`too_many_streams`), `useLogs.ts:153`
problem: the hint says "You have 5 log streams open; close one and this panel will reconnect." It is a guess made after 3 refused opens (EventSource hides the status). Two cases mislead: (a) a user with ONE tab after a sleep or Wi-Fi drop, whose old half-open connections still hold slots for a few minutes (the accepted PROD-5 decision): "close one" sends them looking for tabs that do not exist; (b) any other refused open with a granted ticket (proxy buffering, CORS, a 5xx on the stream) gets the same wrong text. The 429 path on the ticket POST is fine ("Too many requests. Wait a moment and try again.") but its "try again" is also odd because the panel retries by itself.
evidence: code read; the hint appears about 4-7 s after the first refusal.
fix: reword to cover the ghost-slot case and admit uncertainty, for example "Can't open the log stream. You may have 5 streams open: close other log tabs. If you only have this one, it frees up within a few minutes and this panel keeps retrying." Drop "try again" from the auto-retrying 429 and network messages in this panel (or say "retrying"). Wording only; no logic change.

[PROD-T16-3] severity: low
where: `useLogs.ts:110-113`, `LogViewer.tsx:120-123,160-169`
problem: (a) The terminal "This deployment no longer exists." state shows a Retry button that can only fail again, and the empty body still says "No logs yet. Deploy a service to see logs here.", which is wrong for Disconnected/Reconnecting and after Clear. (b) The badge and notice are red/amber consistently and have `role=status` / `role=alert`: good.
fix: hide Retry when the terminal reason is a 404 (keep it for session/other terminals); make the empty state depend on status ("Waiting for the log stream…", "Logs cleared. New lines will appear here."). Polish; the empty-state text predates this task.

[PROD-T16-4] severity: low
where: gap on reconnect, `useLogs.ts:163-171`
problem: after a long outage or the hourly renewal, the history event brings only the newest 100 lines, so a chatty service can lose lines in between with no marker. The merge keeps everything already on screen (the PROD-2 goal), but the user cannot tell a gap exists. Also the list is unbounded in memory and `appendLive` scans it per line (same as the Socket.io version).
fix: deferred, no change now. Backlog: a muted "some lines may be missing" divider when the incoming history's oldest line is newer than the newest line held; cap the buffer (for example 5000 lines) in a later UI task.

[PROD-T16-5] severity: low (conscious deferral of PROD-7)
where: truncated line display
problem: a line over 8 KiB ends with `...[truncated]` inside the normal message text (pre-wrap, break-all), same colour as the rest. Honest but easy to miss and no way to see the rest.
fix: release note only for now; optional later: muted styling or a tooltip on lines ending in `...[truncated]`, and a "load earlier" via the history endpoint.

[PROD-T16-6] severity: low
where: `useLogs.ts:42` early return when there is no token
problem: with no token the effect returns before setting anything, leaving the panel on "Connecting…" with no notice. In practice the shared client signs the user out first, so it is unreachable from a normal flow.
fix: none required; mention only.

[PROD-T16-7] severity: info
where: stale comment `useLogs.ts:22` ("trusting the old socket") is fine; no `socket.io` imports remain; `package.json`/lock cleaned in f70715f. CLAUDE.md still lists "Socket.io client" and "Socket.io (via Redis adapter)" under UI Architecture and Logs; update at the docs sweep.

## Walk-through of the states (code reading)

- Open panel: "Connecting…" amber, then green "Live" with the last 100 lines. No notice.
- API restarts: stream error closes the source, badge amber "Reconnecting…", no notice (nothing useful to say), retries 1 s, 2 s, 4 s ... 30 s with jitter, green again on open, lines merged, scroll and Clear preserved. Ticket failures during the outage show the amber notice "Cannot reach the server…" or "temporarily unavailable…".
- Hourly renewal: badge stays Live, no notice, immediate reopen, history merged. Verified silent by code; the brief gap between close and the new open is covered by history up to 100 lines.
- Hidden tab: stream closes after 5 min (badge is not updated while hidden), reopens when shown; away over 30 s while connected also reconnects on return without flashing amber.
- At the 5-stream cap: amber, "Connecting…" or "Reconnecting…", hint after 3 refused opens, keeps retrying, Retry button forces an immediate attempt, recovers by itself when a slot frees.
- Deployment or service deleted: ticket 404, red "Disconnected" plus the red notice "This deployment no longer exists.", no further retries.
- Session dead: shared client signs out; panel goes terminal red.
- Clear: empties the list; later history events do not bring older lines back; new live lines appear.

## Release-note lines

- Live logs now use Server-Sent Events instead of Socket.io; the panel shows Live (green), Reconnecting (amber) or Disconnected (red) and never gives up on its own while the deployment exists. Retry reconnects at once.
- The stream renews itself every hour and after sleep or network changes without a visible interruption. Lines kept on screen are not lost and Clear stays cleared; after a long gap only the latest 100 lines are re-fetched, so some lines in between may be missing.
- A tab hidden for more than 5 minutes closes its log stream and reopens it when you come back.
- Up to 5 live log streams per user (20 per deployment). If the panel cannot connect it tells you to close other log tabs; abandoned connections free their slot within a few minutes.
- If the deployment or its service was deleted the panel says "This deployment no longer exists." and stops.
- A single log line is cut at 8 KiB and ends with `...[truncated]`.
- The new UI is required at cutover; the Go API has no Socket.io.

## Verdict

Every binding Product requirement from Task 11 is implemented; PROD-7 truncation and the missing-lines marker are consciously deferred and harmless. One wording fix (PROD-T16-2) is worth doing before cutover; everything else is optional polish. "Connecting…" is acceptable as is.

RESULT: APPROVE (with PROD-T16-2 wording recommended before cutover, not blocking)

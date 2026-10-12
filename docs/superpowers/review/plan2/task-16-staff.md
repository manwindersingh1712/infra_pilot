# Plan 2 Task 16, Staff review

Commits: 059b1e5 (helpers + checks + ApiError.retryAfter), 1c0ae8c (useLogs), ca4d781 (LogViewer), f70715f (remove socket.io-client).
Reviewer: Staff. Date: 2026-10-13. Disk: 18 GiB free (91% used) before running anything.

## Commands run

- `cd cp-ui && npm run lint`: 0 errors, 5 warnings (all `react-hooks/exhaustive-deps` in CreateServiceModal, ServiceDetailsPanel, ProjectCanvasPage; none in Task 16 files, all pre-existing).
- `npm run build` (tsc -b + vite build): passes.
- `npm run check:logic`: `ok: 41 checks passed`.
- `grep socket.io src package.json`: no hits. `npm audit --omit=dev`: 2 high (react-router, react-router-dom), matches the commit message (5 -> 2). `npm audit fix` not run.
- Working tree clean for cp-ui source after the mutation run (`git status --short src scripts` empty).

## Amendment items 1-10

| # | Result | Notes |
|---|---|---|
| 1 never give up | Met | `MAX_RECONNECTS` gone; `backoffDelay` capped at 30 s with jitter; `online` and `visibilitychange` reset attempts and reconnect; Retry button calls `restart()`. See STAFF-4/5 for edge rough spots. |
| 2 three badge states | Met | live green / reconnecting amber / disconnected red (terminal only: ticket 404, `session_expired`). Hook returns `status`, `error`, `retry`, `isConnected`. Inline styles only, `role="status"` / `role="alert"` added. |
| 3 ticket via apiFetch | Met with a gap | `apiFetch` right before `new EventSource`, 404 terminal, 429 uses `retryAfter`, `humanizeApiError` for text, `es.close()` in `onerror` before scheduling, with the explaining comment. Gap: STAFF-2 (Retry-After never readable cross-origin) and STAFF-3 (hint heuristic). |
| 4 history merges | Met | first history replaces, later ones `mergeHistory` (dedupe, time order, Clear cutoff). Scroll: see below. |
| 5 `end` event | Partly met | `max_duration` after a normal-length stream is silent and uncounted; `shutdown` counts and waits 1-3 s. The Engineer's loop guard for a quick `max_duration` does not work: STAFF-1. |
| 6 JSON.parse / cleanup | Met | all parsing goes through try/catch helpers; cleanup covered below. |
| 7 remove socket.io, pure helpers with checks | Met | helpers exported from `lib/logStream.ts`, 13 new checks, mutation-checked below. |
| 8 manual/browser checks | Not evidenced | no Task 16 Engineer record exists in `review/plan2/` (only product and security files), so I cannot confirm ticket 200 / `eventsource` / no `token=`, API restart, 2-minute outage, 6 tabs, short `MaxDuration`, over-8 KiB line, deleted service. Not run by me. The Verifier must run them; STAFF-1 and STAFF-3 predict what the short-`MaxDuration` and 6-tab runs will show. |
| 9 npm audit | Met | recorded in the commit message, no `audit fix`. A task record line is still missing. |
| 10 spec/plan text | Met | spec D5 already lists `history`, `log`, `end` with both reasons. |

## Hook correctness (useLogs.ts)

Verified by reading, no bugs found in these:
- Effect cleanup: sets `closed`, clears both timers, removes both window/document listeners, `stopStream()` (bumps `generation`, aborts the ticket request, closes the EventSource), resets `reconnectNow`. All async continuations check `closed || mine !== generation` or `source !== es`, so there are no state updates after unmount and a late ticket never opens a stream.
- StrictMode double mount: locals are per effect run, so the second run starts clean; the first run's ticket POST is aborted (apiFetch wraps the AbortError as `ApiError(0)`, but the generation guard swallows it). One wasted ticket server-side in dev only (60 s TTL), harmless.
- `deploymentId` change: effect re-runs, state and refs are reset (`clearedUpTo`, `newest`, `firstHistory` is local so true again). Edge: if `deploymentId` becomes empty or the token is missing the early return keeps the previous deployment's lines and the initial `reconnecting` status. Low, not reachable today.
- Aborted ticket requests: covered above. The refresh path inside `apiFetch` is not abortable but is guarded the same way.
- Timers: `retryTimer` is always cleared before being set (`schedule`, `connect`), `hiddenTimer` cleared on visible and in cleanup.
- Listener removal: the EventSource listeners die with `es.close()`; they are additionally guarded by `source !== es`.
- `onerror` closes first, so the browser's built-in retry with the spent ticket cannot loop.

## Pure helpers and mutation check

Mutated one line at a time in `src/lib/logStream.ts`, ran `node scripts/check-api-logic.ts`, then `git checkout` the file. All 13 mutants were killed, none were no-ops:
cap 30 s -> 60 s; jitter removed; Clear cutoff `<=` -> `<`; dedupe key without message; sort removed; 404 no longer terminal; `endDelay(max_duration)` 0 -> 1; `parseEndEvent` max_duration -> shutdown; Retry-After upper clamp removed; unchanged-merge returns a copy instead of `prev`; history entries not filtered with `isLogEntry`; `appendLive` dedupe removed; `latestMs` inverted. Revert confirmed clean.

The checks are specific (exact values, boundaries, clamping). What they cannot cover is the hook state machine, which has no tests; STAFF-1 got through for exactly that reason.

## Engineer's deviations

- `everConnected` -> "Connecting…": justified, small, avoids a scary amber "Reconnecting…" on first load. Accept.
- Time-ordered merge: justified (a gap fill must land after older lines and before newer ones), stable sort, skipped when any timestamp is unparseable (documented and checked). Accept. Cost: see STAFF-6.
- Clear cutoff (`clearedUpTo = newest`): sound for the stated purpose. Residual: lines with an unparseable timestamp are never filtered and come back; a line in the same millisecond as the newest seen line but not yet received is dropped on a later history. Both negligible.
- `end` loop guard: intent right, implementation wrong: STAFF-1.
- Hidden-tab rules (5 min close, 30 s staleness reconnect): reasonable and match amendment 6 (optional). Rough edges: STAFF-4, STAFF-5.
- Cap-hint heuristic: STAFF-3.

## LogViewer

- Auto-scroll stays at the bottom only while within 50 px, so a merge while scrolled up does not jump. When the user is at the bottom, merged lines scroll into view. Clear while scrolled up: the shrink clamps scrollTop, fires a scroll event and re-arms auto-scroll. Fine.
- Rows are `key={index}`; a merge that inserts above the viewport re-labels rows and the browser's scroll anchoring cannot hold the position. Only matters when older lines appear on a gap fill, which is rare (gap lines are usually the newest). Low.
- The empty state still says "No logs yet. Deploy a service to see logs here." while Connecting or Disconnected. Cosmetic.
- Retry shows whenever `status !== "live"`; the notice colour follows the badge (amber vs red). Good.
- Inline-style-only rule: respected. No `className`, no CSS import added in the three Task 16 UI files; shared style objects are plain `style` props.

## Findings

[STAFF-1] severity: medium
where: cp-ui/src/hooks/useLogs.ts (`es.addEventListener("end", ...)`), cp-ui/src/lib/logStream.ts `endDelay`
problem: the "end right after opening" loop guard does nothing for `max_duration`. When the end arrives within 1 s of opening, the code falls to the shutdown branch, sets Reconnecting and `attempts += 1`, then calls `schedule(endDelay(reason, Math.random()))`. `endDelay("max_duration", _)` returns 0, and `attempts` is not used for this delay. So a server that answers `end: max_duration` immediately (a misconfigured or very short `MaxDuration`, a proxy replaying it) produces a tight ticket + stream loop at 0 ms, with the badge flashing Reconnecting/Live, limited only by the per-IP bucket (then 429 for 60 s, PROD-6).
evidence: `check-api-logic.ts` asserts `endDelay("max_duration", 0.9) === 0`; the guard's else-path passes the same `reason`. The "short MaxDuration" manual check (item 8) will hit this if the value is under 1 s.
fix: in the guarded branch use `scheduleBackoff()` (capped jittered backoff, which already increments `attempts`) instead of `attempts += 1; schedule(endDelay(...))`; keep `endDelay` for real `shutdown`. Better, extract a pure `planAfterEnd(reason, msSinceOpen, attempts, rand) -> {delayMs, silent, countAttempt}` into `logStream.ts` with checks for: quick max_duration backs off, slow max_duration is 0/silent/uncounted, shutdown 1-3 s counted.

[STAFF-2] severity: medium
where: control-plane-go/internal/api/middleware.go:72 (`Access-Control-Expose-Headers: X-Request-ID`), consumed at cp-ui/src/lib/api.ts (`parseRetryAfter(res.headers.get("Retry-After"))`)
problem: the UI at :5173 is cross-origin to the API at :8080, and only `X-Request-ID` is exposed, so `res.headers.get("Retry-After")` is always null there. `ApiError.retryAfter` is always undefined, `classifyTicketError` falls back to 5 s, and amendment 3's "honour Retry-After" only works same-origin (behind nginx). The rate-limit 429 asks for 60 s; the UI retries after about 5 s (plus backoff), repeatedly, while the bucket is empty.
evidence: `Retry-After` is set in `ratelimit.go:168` ("60") and `stream.go:189` ("5"); CORS code exposes only `X-Request-ID`. The unit checks use hand-built `ApiError`s, so they cannot see this.
fix: add `Retry-After` to the exposed headers (`"X-Request-ID, Retry-After"`) with a CORS test. This is not a credentials change, so the CTO re-review note on that function does not apply, but tell Security. If the Go change is out of Task 16's scope, record it as a Task 18 cutover item and keep the 5 s fallback.

[STAFF-3] severity: medium
where: cp-ui/src/hooks/useLogs.ts (`failedOpens`, `FAILED_OPENS_BEFORE_HINT`), cp-ui/src/lib/logStream.ts `TOO_MANY_STREAMS_MESSAGE`
problem: "3 refused opens with a ticket granted" is taken to mean the 5-stream cap, but it fits any refusal after the POST succeeded: a proxy 502 on `/stream`, an API restarting between the two calls, a stream-route rate limit. A user with one panel then reads "You have 5 log streams open; close one". The counter is also not consecutive: it is reset only on a successful open, so ticket failures between refused opens do not clear it (the comment says "in a row"). After the hint appears every further refusal keeps it, until an open succeeds.
evidence: `onerror` increments `failedOpens` when `!opened`; the ticket-failure path in `connect()` never touches it.
fix: reset `failedOpens = 0` when a ticket request fails; reword the hint so it is true in both cases ("The log stream could not be opened. If you have 5 log streams open, close one."); longer term, return `too_many_streams` from the ticket POST (check the cap there) so the client gets a real status instead of guessing.

[STAFF-4] severity: low
where: cp-ui/src/hooks/useLogs.ts (`onVisibility`, hiddenTimer callback)
problem: when the hidden timer pauses the stream it never updates `status`, so the badge says "Live" with no connection. On return `restart()` sees `connected === false`, flips to "Reconnecting…" for a moment, then Live.
fix: keep it (nobody sees a hidden tab) but on pause either leave `status` and skip the flash on resume, or set status on resume only if the reconnect fails. Optional.

[STAFF-5] severity: low
where: cp-ui/src/hooks/useLogs.ts `onVisibility` (`wasAway > HIDDEN_STALE_MS` -> `restart()`)
problem: coming back to a healthy tab after 30 s tears the live stream down (`stopStream()` runs before the new ticket is fetched) and reopens it. If the ticket fails, the user lost a working stream; if the server has not yet freed the slot the new open can hit the 5-stream cap, which feeds STAFF-3.
fix: fetch the new ticket first, or only restart when `connected` is false; rely on the 15 s heartbeat / `onerror` to detect a dead socket. Or raise `HIDDEN_STALE_MS` and document why.

[STAFF-6] severity: low
where: cp-ui/src/lib/logStream.ts `appendLive` / `mergeHistory`, cp-ui/src/components/LogViewer.tsx
problem: the log array is unbounded and every live line does an O(n) `some` scan, every merge rebuilds a Set and sorts, and all rows are rendered. Since the stream now never gives up and lives for days, a chatty service reaches tens of thousands of lines and a quadratic cost. Same-millisecond identical lines (e.g. repeated progress output) are also deduped away, as the backend aggregator does.
fix: cap the retained lines (for example 5,000 newest) in the hook, and keep a `Set` of keys for the live dedupe. Track as follow-up if the cap is a product decision.

[STAFF-7] severity: low
where: cp-ui/scripts/check-api-logic.ts
problem: nothing exercises the hook's decisions (terminal vs retry, pause, silent reconnect, guard). The unit checks only prove helpers in isolation, which is how STAFF-1 and the hint logic escaped.
fix: move the decision logic into pure functions (see STAFF-1) and check them; no new framework needed.

## Verdict

Amendments 1-7, 9, 10 are met; the helper checks are real (13/13 mutants killed); the hook's lifecycle (cleanup, StrictMode, deploymentId change, aborted tickets, timers, listeners, unmount) is correct. The end-event loop guard is broken (STAFF-1), Retry-After cannot be read cross-origin (STAFF-2), and the stream-cap hint can mislead (STAFF-3). Item 8 manual checks have no record and should be run by the Verifier.

RESULT: CHANGES REQUESTED (0 critical, 0 high, 3 medium, 4 low)

# Plan 2 Task 16: Security review, round 2 (live logs over SSE)

Commits reviewed: 80104df (CORS), 95275b7 (CLAUDE.md), 709711d (release note), 4af70b0 (UI fix round). Reviewer: Security. No code changed. Disk before starting: 16 GiB free (92 % used).

## Tools

- `golangci-lint run` (control-plane-go): 0 issues.
- `gosec -quiet -exclude=G204 ./internal/api/...`: no findings.
- `govulncheck ./...`: no vulnerabilities in code that is called (1 in a required module, not reached).
- `gitleaks detect` (whole repo, 474 commits): no leaks.
- `go test ./internal/api -run CORS -p 2`: pass. `npm run check:logic` (cp-ui): 49 checks pass.

## Round-1 findings: verdicts

### SEC-1 (log cap + Set): CLOSED
`logStream.ts` `LogBuffer` keeps `entries` and `keys` in step. Reasoning by path:
- Invariant: every key in `keys` belongs to exactly one entry. Entries are unique by key because `bufferAppend` and `bufferMerge` check `keys` (and `seen` inside one merge) first, and `bufferReset` dedupes while building.
- `bufferAppend`: adds key, appends, `trim`. `trim` deletes the key of each dropped (oldest) entry, so the Set cannot outgrow `max`.
- `bufferMerge`: new keys go into `keys` before the sort, then `trim` removes the oldest after sorting, with keys deleted. A dropped line that comes back in a later history is re-added, sorted to the front and trimmed again, so size stays bounded.
- `bufferReset` (first history, deployment change, Clear) builds a new Set; nothing is left over. Clear sets `clearedUpTo` and empties both.
- A hostile server sending a huge `history` makes a large transient `fresh` array, then it is cut to 5,000. Memory is bounded after each event, not during it.
- O(1) dedupe replaces the old `some()`. Each append still copies up to 5,000 entries and calls `setLogs`, so a very chatty container re-renders on every line (CPU, not unbounded memory). Not worth blocking.
- Residual (info, SEC-R2-1): the cap is on line count, not bytes. A workload printing megabyte lines can hold 5,000 of them. Own-container, own-tab only.

### SEC-2 (hidden at mount): CLOSED
`initialMode(document.visibilityState)` returns "paused" for a hidden tab; the effect sets `paused = true`, shows "Paused" and opens no ticket or stream. `visibleAction` returns "restart" for a paused tab, so it opens on first view. Same path on a `deploymentId` change while hidden. A paused tab that gets another `hidden` event schedules a redundant `pause()` later (idempotent).

### SEC-3 (end loop): CLOSED, with one low residual (SEC-R2-2)
State machine, every path that schedules a connect:
1. `max_duration` end, stream open >= 10 s and last silent reconnect >= 10 s ago: delay 0, silent, not counted. A hostile server that ends each stream at 10.1 s therefore causes one ticket POST + one stream GET every ~10 s per tab, with no badge. Bounded, 10x slower than before (was 1/s), but unbounded in count and invisible. See SEC-R2-2.
2. `max_duration` end, stream shorter than 10 s: `backoffDelay(attempts)` and `countAttempt`, so the wait grows 0.5-1 s, 1-2 s ... up to 15-30 s. `attempts` is not reset on `onopen`.
3. `shutdown` or unknown reason: `max(1-3 s jitter, backoff)`, counted. A server that accepts then ends every stream grows to the 30 s cap.
4. `onerror` (before or after open): `noteClosed` resets `attempts` only if the stream was open >= 10 s (`attemptsAfterClose`), then `scheduleBackoff`. Accept-then-drop in under 10 s keeps growing the backoff.
5. Ticket failure / bad ticket / EventSource constructor throw: all go through `scheduleBackoff` and grow `attempts`; 429 adds `minDelayMs`.
6. Only `restart()` zeroes `attempts`, and it is user/browser driven (Retry, `online`, visible) with a 1 s floor (SEC-4).
No path gives a zero-delay loop. The `msSinceLastSilent` check is effectively redundant (a new stream needs >= 10 s plus a ticket round trip to qualify) but harmless.

### SEC-4 (restart floor): CLOSED
`restart()` coalesces calls inside `MIN_RESTART_GAP_MS` (1 s) with a single `restartTimer`; the timer is cleared on cleanup. Rate is at most one ticket POST per second per tab, user-driven. Note it still ignores a pending 429 `minDelayMs` (by design: `attempts = 0`). A pending restart also clears `terminal`/`paused`; worst case is one extra attempt before a terminal state returns. Accepted.

### SEC-5 (CORS Retry-After): CLOSED
- `middleware.go:72`: `Access-Control-Expose-Headers: X-Request-ID, Retry-After` is set only inside `if allowed` (exact `slices.Contains` match against configured origins; config rejects `*`).
- `Vary: Origin` is added on every response, allowed or not, before the preflight branch.
- No `Access-Control-Allow-Credentials` anywhere; Allow-Methods/Headers/Max-Age unchanged and still only for allowed origins. The code comment about credentials re-review is still in place.
- `withCORS` wraps the whole mux (`server.go:95`), so the rate limiter's 429 (`Retry-After: 60`) carries the CORS headers; the UI can now read it.
- Test `TestCORSExposesRetryAfterOnlyToListedOrigins` covers listed / unlisted / no Origin. It does not assert `Vary: Origin` or the absence of `Allow-Credentials` (other existing tests cover headers on listed origins). Optional hardening (SEC-R2-3, info).
- Client: missing header on a 429 now waits 30 s (`DEFAULT_RATE_LIMIT_WAIT_S`), clamped 1-60 s. `parseRetryAfter` is digits-only (unchanged), so a hostile value cannot exceed 60 s or go below 1 s.

### SEC-6 (ticket type-check, EventSource try/catch): CLOSED
`parseTicketResponse` accepts only a non-empty string `ticket`; anything else is `BAD_TICKET_MESSAGE` and a counted backoff. `new EventSource` is in try/catch, falls to `OPEN_FAILED_MESSAGE` and backoff. Both paths bump `failedOpens` as a ticket failure (resets the cap-hint run), as ruled.

## New issues

[SEC-R2-1] info: cap is by line count, not size (see SEC-1). No action.

[SEC-R2-2] severity: low
where: `logStream.ts` `planAfterEnd`, `STABLE_MS = 10_000`
problem: A hostile or broken server/proxy that sends `end: max_duration` after 10-11 s every time causes a silent reconnect loop at about 1 per 10 s per open tab, forever, with the badge staying Live. It needs a faulty server, so it is a robustness gap, not a user-reachable exploit. Per-IP ticket rate limiting on the server also applies.
fix (optional): raise the silent threshold to 60 s (the real cap is 1 h), or count silent reconnects in a window and fall back to backoff after 3 within 5 minutes.

[SEC-R2-3] severity: info (test gap)
where: `middleware_test.go`
problem: The new test does not assert `Vary: Origin` on unlisted origins or that `Access-Control-Allow-Credentials` is absent. Behaviour is correct today; a one-line assertion would pin it.

[SEC-R2-4] severity: low
where: `useLogs.ts` replace path (`connect(true)`), `onerror` -> `noteClosed`
problem: Two small replace-on-return effects.
- Race check: no double stream and no leaked client-side ticket. `generation` is bumped by every newer `connect`, `stopStream` and `pause`; the ticket await is followed by `mine !== generation` before the synchronous `es` creation, so a stale ticket never opens a stream. Two overlapping replaces: the second aborts the first. An old-stream `end`/`onerror` during a pending replace leads to `connect(false)`, which aborts the replace. The old stream's late events are ignored by `source !== es`. An aborted ticket POST may leave a minted but unused ticket server-side; it is single use and expires, so only wasted.
- Stale state: between `source = es` and the new `onopen`, `connected` and `openedAt` still describe the old stream. If the new stream is refused before opening (for example the per-user cap, since the old stream is only closed client-side after the new one is created, so the server briefly sees two), `noteClosed` computes the old stream's lifetime and resets `attempts` to 0 once. Then `connected` is false and backoff grows normally. Effect: one extra fast retry (0.5-1 s), not a loop.
- The replace briefly holds 2 streams for the same tab, which can hit the 5-stream cap for a user with 5 panels open and make the cap hint appear after 3 refused opens. Cosmetic.
fix (optional): set `connected = false` when swapping to the new `es`, and record `openedAt` only in `onopen` (already so).

[Checked, no issue]
- Paused state: no ticket, no stream, no timers other than `hiddenTimer`; `schedule`/`connect` guard on `paused`; `online` ignored while paused; cleanup removes both listeners and all three timers. A stale error text can remain while Paused (UI only).
- 30 s default wait: only applies when a 429 has no usable header; clamped, cannot be driven above 60 s or to zero by the server. A broken server can only slow the user's own panel.
- No new `console.*`, `innerHTML`, or storage of the ticket. `LogViewer.tsx` and `errors.ts` changes are display text and keys (keys now use `entryKey`, not index).
- 95275b7 and 709711d are documentation only; no secrets.

## Summary

All seven round-1 items (SEC-1 to SEC-6; SEC-7 and SEC-9 deferred per ruling) are fixed as specified. No critical, high or medium findings remain from this task. Two low optional items (SEC-R2-2, SEC-R2-4) and two info notes.

RESULT: APPROVE (critical 0, high 0, medium 0, low 2 [SEC-R2-2, SEC-R2-4], info 2 [SEC-R2-1, SEC-R2-3])

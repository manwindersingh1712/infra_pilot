# Plan 2 Task 15 security review, round 2

Range: `git log eb3c353..HEAD` (12 commits, a6ae590 .. 652f001), branch go-migration. Read-only; only this file written. Disk checked first: 24 GiB free.

## Tool output
- `gitleaks detect --log-opts="eb3c353..HEAD"` (repo root): 12 commits scanned, no leaks found. `gitleaks detect --no-git --source cp-ui/src`: no leaks found.
- No Go files in the range; govulncheck/gosec not applicable. package.json only gained the `check:logic` script (no dependency change), so SEC-6 is unchanged and stays deferred per the ruling.

## Round-1 findings

### SEC-3 (navigator.locks.request rejection): CONFIRMED FIXED
`api.ts` `refreshOnce`: `request` is wrapped in try/catch; `started` is set inside the callback, so a rejection before the callback runs falls through to `withStorageLock`, while a failure inside `run()` is rethrown and `run` is never executed twice. Correct. Residual (info): a tab that fell back to the storage lock and a tab using Web Locks do not exclude each other; the 401 re-read of `cp_refresh` (Task 6 ruling) still covers that.

### SEC-4 (forged/NaN/far-future expiry): CONFIRMED FIXED
`refresh.ts` `expiry()` returns 0 unless the value is finite and `<= now()+ttl`. Cases:
- `Infinity`, `NaN`, `abc`, missing part: not finite or `Number("")=0`, so 0, treated as expired and claimed over.
- Far future (`9e15`), or anything above now+ttl: 0, expired. A forged value can therefore hold the lock at most ttl (10 s), same as a genuine claim.
- Negative: `n <= now()+ttl` passes, but a negative or past expiry is `<= now()`, so expired. Fine.
- No new lockout or DoS path: the lock is advisory and the loop is bounded by `deadline = now()+waitMs`. If the lock is never acquired, `fn()` runs anyway after waitMs (default 15 s), and `finally` only removes the key if `held`. A same-origin writer that keeps rewriting the key during the settle window (so `owner(...) !== id`) can still stall each refresh for up to waitMs plus one settle+poll (about 15.1 s), never longer, and never causes sign-out (the outcome logic is untouched). This is the same bound as round 1 and adds no capability beyond reading `cp_token`/`cp_refresh` directly.
- Answer to "can a same-origin writer still stall refresh beyond waitMs": not on the storage-lock path. On the Web Locks path a same-origin script can hold `cp-refresh` indefinitely with its own `navigator.locks.request` that never resolves, and `request` has no wait bound, so every refresh in every tab would hang. That is the same-origin attacker class (can already steal both tokens), so it is informational (SEC-8 below), not a regression.
- Clock note: the deadline uses `Date.now`, so a local clock jump could lengthen the wait; not attacker-reachable from the page.
- Tests: a6a14b4 adds mutant-killing tests (per commit message); not re-run here (read-only constraint on the cp-ui build was not needed for the review).

### SEC-7 (revealed values dropped): CONFIRMED FIXED, with one narrow residual
`ServiceDetailsPanel.tsx`: when the last visible key is hidden, `setRevealed(null)`; `loadEnvVars` also nulls it on every reload and serviceId change. The only consumer of `revealed` is the render expression `revealed?.[env.key]`; no copy exists in other state, refs, storage, console logs or props (grep of `revealed`/`visibleValues`: lines 42-45, 94-107, 230-258 only). Unmount discards state.
Residuals (low, no action required for Task 15):
- While at least one value is still shown, all decrypted values stay in memory (including hidden ones). This is what the ruling asked for ("once the last one is hidden").
- Stale-response race: `EnvVarSection` is rendered as `<EnvVarSection serviceId={service.id} />` without `key`. If the user clicks the eye and switches service while `?reveal=true` is in flight, the late response for service A calls `setRevealed` after `loadEnvVars` for service B reset state, and the key is then added to `visibleValues`, so B's row with the same key (for example `PORT`) would display A's value. Both services are the same user's; it is a correctness/information-mixing glitch, not cross-user. Cheap fix: `key={service.id}` on `EnvVarSection`, or ignore the response if `serviceId` changed.
- `revealed` is built with `Object.fromEntries`; a displayed key such as `constructor` that is absent from the object would read the prototype property (a function, renders nothing). Cosmetic.

## New issues in the round-2 changes

[SEC-8] severity: info
where: `api.ts` `refreshOnce` (Web Locks path). A same-origin script can starve refresh by holding `cp-refresh` forever (no timeout on `locks.request`). Same-origin attacker already has token read access. No fix required; optionally pass `{ signal: AbortSignal.timeout(20000) }` to `request` and fall back to the storage lock on abort, which would also make a hung holder tab (stuck fetch with no timeout at `api.ts:50`) self-heal. Low value, backlog.

[SEC-9] severity: low
where: `CreateServiceModal.tsx` (PROD-1 retry flow).
What is good: after POST succeeds, `createdId` is stored and `onCreated()` is called; a retry skips the POST, so the normal Deploy-retry path cannot create a second service. The submit button is disabled while `loading`, so double click and Enter during flight do not duplicate. Error text is `humanizeApiError(err)` (fixed strings, or a server sentence) interpolated into a template string and rendered as a React text node (line ~619), so no injection; no id or token appears in the text.
Problems:
1. Stale id after closing mid-flight. Escape (document listener) and the backdrop/close controls call `handleClose()` -> `resetForm()` (clears `createdId`) while the POST is still in flight. When the POST resolves it runs `setCreatedId(id)` and `onCreated()` anyway; if the deploy then fails, `createdId` stays set in the still-mounted component with the dialog closed. Reopening shows the button as "Deploy" and a submit (with whatever new name the user typed) deploys the OLD service instead of creating a new one, and the user sees only a "created, but deploy failed" style flow for the wrong service. On a successful deploy the trailing `resetForm()` clears it, so only the failure branch leaves the stale id. Fix: an `inFlight`/generation ref checked after each await (drop `setCreatedId` and the error if the dialog was closed or reset), or disable close while `loading`.
2. Form fields remain editable after create. After the service exists the user can change name, type, repo URL or branch; the retry silently ignores those edits and deploys the already-created service. Not a security issue but misleading; disable the inputs when `createdId !== null` or show the created name.
3. Lost-response duplicate deployment: if the first deploy request reached the server and only the response was lost (network error), Retry sends a second deploy. Whether that duplicates depends on the Go deploy handler's idempotency (workflow id is deduplicated server-side per the spec's reject-duplicate policy for deploy workflows, so the expected worst case is a harmless second queued deployment or a clean error). Informational; confirm in Task 16/17 testing that a double POST `/services/:id/deploy` does not produce two builds.
4. `handleBack` with a created service closes the dialog (intended and safe).

### Session-notice change: OK
`readSessionNotice` is side-effect free; `clearSessionNotice` runs in an effect (idempotent under StrictMode); storage access is try/catch guarded. The notice is rendered as a React text node in a `role="status"` div (no HTML sink). It comes from a constant written by `expireSession`; a same-origin tamperer could show arbitrary text on the login page, but cannot do anything beyond what script access already allows. The notice is hidden when `error` is set and cleared on submit. No new open redirect or token exposure.

### New wording and maxLength: OK
- `NOT_FOUND_MESSAGE`, `invalid_request`, `password_too_long` and the added `"repoUrl required for docker/nodejs/nextjs/react services"` key are fixed strings looked up with `Object.hasOwn`; no server text is echoed except the existing space-containing sentence pass-through, rendered as text. The `sentence()` helper in `ProjectCanvasPage` appends a period to server sentences, text only.
- `maxLength` 100 (name) and 254 (email) on signup are client convenience only; the server must still enforce them (the `invalid_request` message now states 100/254, so the Go validators should match; worth a one-line check by the Engineer in Task 14 notes). Password field has no `maxLength` by design (72 bytes, not characters, so a character cap would be wrong); the server returns `password_too_long`. The placeholder now says "At least 8 characters", consistent with `password_too_short`.
- `check:logic` script and `scripts` added to `tsconfig.node.json` `include`: build-time only, no runtime impact. `node scripts/check-api-logic.ts` relies on Node type stripping; it is a dev script, not shipped.

## Result
RESULT: PASS WITH NOTES (0 critical, 0 high, 0 medium; SEC-3, SEC-4, SEC-7 confirmed fixed; new: SEC-9 low, SEC-8 info, plus the low SEC-7 stale-response race)

Not blocking. Recommended small follow-ups for the Engineer (optional, any time before cutover): guard `CreateServiceModal` against close-during-flight leaving a stale `createdId` (SEC-9.1), add `key={service.id}` to `EnvVarSection`, disable the form fields once `createdId` is set. Carried forward unchanged from round 1: SEC-1 (binding for Task 16), SEC-6 (dependency bump task), SEC-2 and SEC-5 accepted.

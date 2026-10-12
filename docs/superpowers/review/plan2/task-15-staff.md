# Plan 2 Task 15: Staff review

Reviewer: Staff Engineer. Commits: 17d7680, d0901ce, 0ebb2b6, 51f9913, 9fbe99c, 4ffffde (branch go-migration, HEAD 4ffffde).
Disk before work: 25 GiB free (88% used). No code changed; all mutations reverted (`git status` shows only the pre-existing `?? ruvector.db`).

## Commands run (cp-ui)
- `npm run lint`: 0 errors, 5 warnings, all `react-hooks/exhaustive-deps` in CreateServiceModal.tsx:165, ServiceDetailsPanel.tsx:49/440/457, ProjectCanvasPage.tsx:62. These are the existing "effect calls a function declared below" patterns; none are in the new lib files or the script.
- `npm run build` (tsc -b + vite build): passes (only the existing 641 kB chunk-size notice).
- `node scripts/check-api-logic.ts` (Node v24.10): `ok: 27 checks passed`.
- `tsc --noEmit --strict` on the script alone: clean (see STAFF-4: the build does not cover it).
- `grep` for leftover copies: only `src/hooks/useLogs.ts` still has `const API`, `function getToken` and a `cp_token` read (Task 16 rewrites it). Nothing else outside `src/lib/` touches `cp_token`/`cp_refresh`/`VITE_API_BASE`; no `err.message`/`instanceof Error` remain in components.

## Amendment checklist
1. Single-flight across tabs (Web Locks plus storage-lock fallback), 401 re-read and one retry, never sign out on 429/5xx/network: MET. `refreshOnce` (per page) wraps `navigator.locks.request("cp-refresh", ...)` or `withStorageLock`; `refreshSession` short-circuits when the access token already changed, re-reads `cp_refresh` after a 401 and retries once, and maps everything except 401 to `unavailable`. Verified by mutation (below).
2. Poll and all callers use the shared client; "Your session expired, please sign in again" before `/login`: MET. `expireSession()` stores the notice in sessionStorage, `LoginPage` shows it via `takeSessionNotice()`. The 2 s deployment poll in ServiceDetailsPanel goes through `apiFetch`. Not exercised in a browser by me.
3. `humanizeApiError` and `ApiError{status,code}`: MET. Every code in the amendment is mapped; also adds `reserved_env_var`, `invalid_credentials`, `email_already_exists`, `invalid_refresh_token`, and the sentences "Project not found" etc. The server codes I grepped in `control-plane-go/internal/api` are all covered. `Object.hasOwn` guards `__proto__`.
4. Create-project dialog: MET (maxLength=100, "2-100 characters", disabled until trimmed code-point length >= 2, trimmed name sent, error shown in the modal and cleared on typing). Signup counts bytes (8-72) and shows the two messages; placeholder "8 to 72 characters".
5. useCanvas/ProjectCanvasPage on the shared client; toasts for connect, edge delete, position save; `label?: string | null`: MET.
6. Env editor: lazy `?reveal=true` on first eye click, hints, trimmed key, "redeploy to apply": MET. `revealed` is dropped on every reload so a changed value is never shown stale. Bonus: key is `encodeURIComponent`-ed on delete.
7. Pure logic plus a script, no test framework added: MET (see STAFF-3/4 for quality).
8. Step 4 manual check against a Go API: not verifiable by this review; it is the Verifier's.

No leftover copies except `useLogs.ts` (expected).

## Findings

[STAFF-1] severity: medium
where: cp-ui/scripts/check-api-logic.ts (storage lock section), cp-ui/src/lib/refresh.ts:106-126
problem: the three `withStorageLock` checks do not prove the mutual-exclusion logic. Three mutations survive with `ok: 27 checks passed`:
- M6, dead-holder expiry ignored (`expiry(cur) <= now()` replaced by nothing): the test "a dead holder expires" still passes because the lock falls through to the 15 s `waitMs` and "runs anyway". It only shows as a 15.3 s run time instead of 0.28 s.
- M7, claim verification removed (`owner(getItem) === id` replaced by `true`): "serialises concurrent holders" still passes, because the three callers start in the same tick and the later two see the first claim and wait. The actual race the settle step guards against (two tabs write a claim in the same window, the loser must back off) is never exercised.
- M8, release even when another tab owns the key: not covered.
evidence: mutation runs above, RC=0 for M6/M7/M8.
fix: (a) in the dead-holder test pass a small `waitMs` (e.g. 200) and assert the lock was acquired, not fell through, for example by checking the storage key holds `me|` while `fn` runs; (b) add a race test with a storage whose `getItem` returns a different owner after the first `setItem` (simulate a second tab overwriting the claim during `settleMs`) and assert only one `fn` runs at a time; (c) a test that a lock whose key changed owner during `fn` is not removed.

[STAFF-2] severity: low
where: cp-ui/scripts/check-api-logic.ts, "humanizeApiError maps every documented code"
problem: the loop only asserts `m.length > 10 && !m.includes(code)`. Dropping a code from the map (M13: `purge_choice_required` renamed) still passes because the fallback "That request was not valid" (status 400) satisfies it. Same weakness for any code tested with status 400.
fix: assert the message differs from the status fallback (`!== humanizeApiError(new ApiError(400, "zzz"))`) or assert on a key phrase per code. The other mutations I tried on errors.ts and projectName.ts (drop 429, drop `TypeError`, drop sentence tolerance, `hasOwn`->`in`, ignore overrides, no trim, UTF-16 count, no max, min 1) are all caught.

[STAFF-3] severity: low
where: cp-ui/src/lib/api.ts (apiFetch, refreshOnce, expireSession)
problem: the script covers only the pure modules. The glue that decides behaviour is untested: 401 then refresh then retry, "second 401 after a successful refresh signs out", `unavailable` -> `ApiError(503, "refresh_unavailable")`, no refresh on `/auth/*`, `inflight` reset, Web Locks branch selection. It cannot be run by node because of `import.meta.env`, `localStorage`, `window`.
fix: acceptable for now (amendment 7 allows a Verifier browser pass). Ask the Verifier to cover: expired access with 3 parallel requests gives one `/auth/refresh` call; two tabs; refresh returning 429/503 keeps both keys and shows the "could not refresh" text; revoked refresh token redirects with the notice.

[STAFF-4] severity: low
where: cp-ui/package.json, cp-ui/tsconfig.app.json
problem: `scripts/` is not in any tsconfig `include` (app: `src`; node: `vite.config.ts`), so `npm run build` never typechecks `check-api-logic.ts`, and there is no npm script to run it (the header comment says `node scripts/check-api-logic.ts`, which needs Node >= 22.18 for type stripping). Today it typechecks (I checked with `tsc --noEmit --strict`).
fix: add `"check:logic": "node scripts/check-api-logic.ts"` to package.json and either add `scripts` to tsconfig.node.json `include` or leave it and note it. Not a blocker.

[STAFF-5] severity: low
where: cp-ui/src/pages/ProjectCanvasPage.tsx:141-159 (`onNodeDragStop`)
problem: the position-save question. On failure the node stays where the user dropped it (React Flow keeps its local position; `setServices` is skipped, so `initialNodes` does not change and nothing resets it). The user sees a toast, but the canvas looks saved while the server has the old position, and the node snaps back on the next reload.
judgement: no revert is acceptable and better than reverting. A drag is a low-value, idempotent, last-write-wins action; snapping the node back under the user's cursor is more jarring than a stale position, a later successful drag overwrites it, and the toast is the PROD-1 requirement. Task text asks only for a toast.
fix (wording only): say what is true: "Could not save the new position, it will reset when you reload." Optionally keep the previous position in a ref and offer nothing more.

[STAFF-6] severity: low
where: cp-ui/src/App.tsx (`LoginPage`, `useState(() => takeSessionNotice())`)
problem: a state initializer with a side effect (it removes the sessionStorage key). StrictMode is on (src/main.tsx) and double-invokes initializers in dev. React keeps the first result, so it should work, but this is relying on a React implementation detail, and I have not seen it in a browser.
fix: read in a `useEffect`/`useState(null)` then set, or keep as is and have the Verifier confirm the notice appears in `npm run dev`.

[STAFF-7] severity: low (informational)
where: cp-ui/src/lib/refresh.ts `attempt`
problem: `deps.store` (localStorage.setItem) can throw (quota, blocked storage) and is outside the try, so the lock callback rejects with a raw error; callers then show the generic "Something went wrong". Also any non-401 failure (400, 403) maps to `unavailable`, so a corrupted `cp_refresh` that makes the server answer 400 would show "try again" forever instead of signing out. The Go handler returns 401 `invalid_refresh_token` for bad/unknown tokens and 400 only for an empty/undecodable body, so this does not occur in practice.
fix: none required.

## Specific questions
- Refresh logic correctness: sound. Order is right (stale-access check first, then read refresh token, call, on 401 re-read once, at most two server calls, tested). Per-page `inflight` is cleared in `finally`; late requests carrying the old token hit the stale-access shortcut instead of calling the server. Web Locks callback waits in queue (default mode), so a dead tab releases automatically. Storage-lock fallback is best-effort and says so, with `refreshSession`'s re-read as the safety net. The "lost response then retry later needs re-login" case is the accepted CTO behaviour. Never signs out on 429/5xx/network: verified by tests and by mutation M1.
- Mutation results for the 27 checks (each run, then `git checkout --` of the file):
  - Caught (script exits 1 on a specific check): non-401 treated as rejected (M1), no re-read retry (M2), no stale-access shortcut (M3), second 401 not "expired" (M4), unbounded retry (M4b), accept 200 without tokens (M5), wait forever (M9), no-token not "expired" (M10), overrides ignored (M11), `hasOwn`->`in` (M12), drop 429 mapping (M14), drop sentence tolerance (M15), drop `TypeError` (M16), no trim (M17), UTF-16 length (M18), no max (M19), min 1 (M20).
  - Survived: M6, M7, M8 (STAFF-1), M13 (STAFF-2).
  So 17 of 21 mutants are killed; the weak area is the storage lock.
- eslint scope: fine. `eslint .` already globs `**/*.{ts,tsx}`, so `scripts/` was in scope with no config change, and it lints clean (`typescript-eslint` recommended turns off `no-undef`, so the browser-only `globals` setting does not complain about Node imports). The cost is that browser globals are assumed; if the script later uses `process`/`Buffer` it would still pass lint but is outside any tsconfig (STAFF-4).
- Inline-styles-only rule: respected. New `Toast.tsx` and the dialog/hint/error blocks use `style={{...}}`; no CSS files or classes touched, `motion` `animate` used for the toast as the repo already does. The hint block uses bare `<code>` with no style, which is fine.
- Engineer's deviations (judged from the diff; no written deviation list was in my inputs):
  - Split into `lib/errors.ts`, `refresh.ts`, `projectName.ts`, `api.ts` so the pure code runs under node: justified (amendment 7).
  - Extra codes (`reserved_env_var`, `invalid_credentials`, `email_already_exists`, `invalid_refresh_token`) and `ApiError(0,"network_error")`: justified, they exist in the Go API.
  - Removed the "N services" line and `_count` from the Project type: allowed by the backlog ("optional: drop").
  - Hand-rolled `Toast.tsx`: justified, no toast exists; inline styles, auto-dismiss.
  - Edge delete: 404 treated as success and the edge is put back on other failures: good, matches the backlog's 404-as-success direction.
  - Session-expired notice via sessionStorage: meets amendment 2 without a new route.
  - Not done and not required: `stoppedAt` on the UI Deployment type (backlog "optionally").
  - Docs: no doc changes in this range.

## Summary
Every amendment item is implemented and the build, lint and script are green. The refresh logic is correct. The test script is a good fit for the no-framework constraint but the storage-lock tests are too weak to catch regressions in the lock (3 surviving mutants), and the integration glue in `api.ts` rests on the Verifier's browser pass.

RESULT: APPROVE (0 critical, 0 high, 1 medium, 5 low; STAFF-1 should be fixed in a follow-up commit, not blocking the gate)
